// Binary gateway serves OpenAI-compatible endpoints fronted by
// multi-tenant authentication, per-request audit logging, idempotency
// keys, and a reverse proxy to the Phase 1 pod. Plan 02-03 wires the
// pgxpool + Redis client + auth middleware. Audit, idempotency, and
// proxy land in 02-04..02-06.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	// Phase 4 — embed all IANA timezone data into the binary so distroless
	// (or any minimal image without /usr/share/zoneinfo) can resolve
	// "America/Sao_Paulo" at boot. See RESEARCH §Pitfall 4. Cost ~400 KB.
	_ "time/tzdata"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/admin"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/dcgm"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/quota"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/schedule"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/shed"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// proxies bundles the three reverse proxies mounted under /v1/*. Any nil
// field falls back to the scaffold 501 handler so existing scaffold tests
// (nil verifier + nil proxies) keep passing. Production main always
// supplies all three.
//
// Plan 02-05 fields:
//   - auditWriter: when non-nil, audit.Middleware is mounted on the
//     authed /v1/* group. nil → middleware skipped (test variant).
//   - resolver: when non-nil, chat + embeddings are wrapped in
//     models.Handler for alias rewriting. nil → proxy runs directly.
//   - upstreamsHealth: when non-nil, overrides the /v1/health/upstreams
//     scaffold 501 with the real aggregator handler.
type proxies struct {
	chat            http.Handler
	embed           http.Handler
	audio           http.Handler
	auditWriter     *audit.Writer
	resolver        *models.Resolver
	upstreamsHealth http.Handler
	idemStore       *idempotency.Store

	// Phase 4 — middleware chain collaborators. All fields may be nil
	// in the scaffold test variant; production main wires all of them.
	tenantsLoader     *tenants.Loader
	quotaChecker      *quota.QuotaChecker
	adminUsageHandler http.Handler
	adminVerifier     *admin.Verifier
	rdb               redis.UniversalClient
	rateLimitFailOpen bool
	quotaFailOpen     bool

	// Phase 5 — shed middleware collaborators. nil disables the shed
	// middleware mount; production main always supplies all four
	// pointers after the Phase 5 wiring block runs.
	upstreamsLoader *upstreams.Loader
	shedSet         *shed.Set
	shedInflight    *shed.InflightRegistry
	shedLatency     map[string]*shed.LatencyRing
}

func main() {
	selfCheck := flag.Bool("self-check", false, "exit 0 immediately (docker healthcheck)")
	flag.Parse()
	if *selfCheck {
		fmt.Println("ok")
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gateway: config error: %v\n", err)
		os.Exit(2)
	}
	if err := obs.Init(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "gateway: sentry init: %v\n", err)
	}
	defer obs.Flush(2 * time.Second)

	log := newLogger(cfg)

	// Phase 4 — resolve the tenant schedule timezone ONCE at boot. The
	// tenants.Loader caches a per-tenant *time.Location from each row's
	// schedule_timezone column; this default is used as fallback for
	// any tenant that fails LoadLocation (invalid IANA name).
	location := mustLoadLocation("America/Sao_Paulo", log)

	log.Info("starting gateway",
		"port", cfg.Port,
		"env", cfg.Env,
		"upstream_llm", cfg.UpstreamLLMURL,
		"upstream_stt", cfg.UpstreamSTTURL,
		"upstream_embed", cfg.UpstreamEmbedURL,
		"upstream_health_bridge", cfg.UpstreamHealthBridgeURL,
		"version", obs.BuildVersion,
	)

	// Root context cancelled on SIGTERM/SIGINT.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("signal received, shutting down", "signal", sig.String())
		cancel()
	}()

	// Postgres pool — fail-fast. Required for auth + audit.
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		log.Error("db pool init failed", "err", err)
		os.Exit(2)
	}
	defer pool.Close()

	// Optional boot-time migration (CONTEXT.md D-D1 — applied at boot OR via
	// gatewayctl). Default off; ops decide via env.
	if os.Getenv("AI_GATEWAY_MIGRATE_ON_BOOT") == "true" {
		if err := db.Up(ctx, pool); err != nil {
			log.Error("migrate up at boot failed", "err", err)
			os.Exit(2)
		}
		// Reset pool so future AfterConnect calls register the now-created
		// ai_gateway.{api_key_status,data_class} ENUM types for sqlc scans.
		// Without this, pre-migration connections have no registered ENUM
		// codecs and sqlc's `interface{}` scan targets error at query time.
		pool.Reset()
		log.Info("migrations applied on boot")
	}

	// Partition automation (Plan 02-02 Task 3 + Codex review [LOW] 02-02).
	// Runs after migrations so the parent partitioned tables exist.
	if err := db.EnsurePartitions(ctx, pool, time.Now(), db.DefaultPartitionLookahead); err != nil {
		log.Error("ensure partitions failed", "err", err)
		os.Exit(2)
	}

	// Redis — fail-fast. Required for auth cache + idempotency (Plan 02-06).
	rdb, err := redisx.NewClient(ctx, cfg)
	if err != nil {
		log.Error("redis init failed", "err", err)
		os.Exit(2)
	}
	defer rdb.Close()

	// TouchBuffer: debounced last_used_at updates (Codex review [MEDIUM] 02-03).
	// flushFn uses a SEPARATE short-lived context so shutdown drains via
	// Run ctx cancel propagation but each UPDATE has its own 3s deadline.
	touchFlush := func(fctx context.Context, id uuid.UUID) error {
		return gen.New(pool).TouchKeyLastUsed(fctx, id)
	}
	touchBuf := auth.NewTouchBuffer(touchFlush, auth.DefaultTouchFlushInterval, log,
		obs.ApikeyTouchBufferedTotal.Inc,
		obs.ApikeyTouchFlushTotal.Inc,
	)
	tbCtx, tbCancel := context.WithCancel(ctx)
	go touchBuf.Run(tbCtx)
	defer tbCancel() // triggers final flush on shutdown

	verifier := auth.NewVerifier(pool, rdb, log, touchBuf)

	// Audit writer — async buffered flusher (Plan 02-05). Run exits on ctx
	// cancel after draining the channel. Non-blocking Enqueue on the hot
	// path; drops are observable via gateway_audit_dropped_total.
	auditWriter := audit.NewWriter(pool, log)
	go auditWriter.Run(ctx)

	// AuditInterceptor: plugs into ProxyResponseInterceptor chain for SSE
	// capture. onDisconnect fires when TeeBody.Close() runs (normal close,
	// client abort, upstream 5xx mid-stream, buffer cap — see Failure-mode
	// table in 02-05-PLAN.md). Codex review [HIGH/MEDIUM] 02-05.
	auditInterceptor := audit.NewAuditInterceptor(auditWriter, func(requestID string) {
		log.Debug("audit interceptor on-close", "request_id", requestID)
	}, log)

	// Model alias resolver (Plan 02-05). Initial refresh is fail-fast so
	// operators catch schema/seed problems at boot, not at request time.
	resolver := models.NewResolver(pool, log)
	if err := resolver.Refresh(ctx); err != nil {
		log.Error("model resolver initial refresh failed", "err", err)
		os.Exit(2)
	}
	resolver.Start(ctx)

	// Phase 4 — Accountant (Plan 04-05 billing). The accountant holds
	// per-request usage counters; the interceptor built below reads
	// SSE/JSON usage from the upstream response and writes them into the
	// accountant slot keyed by request_id.
	//
	// BL-01 fix: the UsageInterceptor is built AFTER billingFlusher +
	// pricesLoader + fxLoader + tenantsLoader are constructed (below), so
	// it can enqueue billing.Event records on Close with full cost
	// attribution. Consequently the chat/embed/audio reverse proxies are
	// ALSO constructed later (they need the interceptor at
	// NewChatProxy-time for ModifyResponse).
	accountant := billing.NewAccountant()
	// ME-03 fix: background reaper evicts accountant slots whose Close
	// path never ran (client abort pre-header, cold reset, panic in
	// the interceptor). Default TTL 5 min — longer than any plausible
	// Whisper transcription; shorter than the memory pressure horizon.
	go accountant.RunReaper(ctx, time.Minute, billing.DefaultReapTTL, log)
	toolCallInterceptor := proxy.NewToolCallInterceptor()

	// Idempotency store (Plan 02-06). Shares the same Redis client as auth
	// cache; keys live under `gw:idem:<tenant>:<key>` with SET NX EX
	// first-writer-wins semantics + 24h TTL post-completion.
	idemStore := idempotency.NewStore(rdb)

	// Phase 3 wiring (Plans 03-03 / 03-04 / 03-05) — replaces the Phase 2
	// pod-:9100 health-bridge proxy with an in-process aggregator over
	// the multi-upstream loader + per-upstream gobreaker set + synthetic
	// E2E probe goroutine.
	//
	// Construction order matters:
	//   1. Loader fail-fasts at boot if ai_gateway.upstreams is unreadable.
	//   2. breaker.Set is built from loader.Names() so each enabled
	//      upstream gets its own *gobreaker.CircuitBreaker.
	//   3. Subscribe (Pub/Sub) goroutine runs so cross-replica OPEN
	//      events from other gateways short-circuit local Execute.
	//   4. Probe loop drives breaker state via synthetic E2E even with
	//      zero client traffic (D-A2 / SC-1 ≤10s failover budget).
	//   5. ListenAndReload watches LISTEN upstreams_changed and rebuilds
	//      the breaker set on hot-reload (D-D4).
	loader, err := upstreams.NewLoader(ctx, pool, log)
	if err != nil {
		log.Error("upstreams loader init failed", "err", err)
		os.Exit(2)
	}
	breakerSet := breaker.NewSet(rdb, log,
		breaker.Options{
			ConsecutiveFailures: uint32(cfg.BreakerConsecutiveFailures),
			Cooldown:            time.Duration(cfg.BreakerCooldownSeconds) * time.Second,
		},
		loader.Names(),
	)
	go breakerSet.Subscribe(ctx)
	probe := upstreams.NewProbe(loader, breakerSet, gen.New(pool),
		upstreams.ProbeConfig{
			Interval: time.Duration(cfg.ProbeIntervalSeconds) * time.Second,
			Budget:   time.Duration(cfg.ProbeBudgetSeconds) * time.Second,
		},
		log,
	)
	go probe.Run(ctx)

	// Phase 4 — tenants loader + NOTIFY tenants_changed listener. Loads
	// the ai_gateway.tenants snapshot into atomic.Pointer (lock-free Get
	// on the hot path) and keeps it fresh via LISTEN/NOTIFY. Fail-fast
	// on initial refresh: a gateway without a tenants snapshot cannot
	// enforce quota/schedule/rate-limits safely (D-A2 / D-C1).
	//
	// Phase 5 (re-ordering): tenantsLoader now constructed BEFORE the
	// shed wiring so the shed.RunTicker TenantLabel closure can call
	// tenantsLoader.Get without a forward reference.
	tenantsLoader, err := tenants.NewLoader(ctx, pool, location, log)
	if err != nil {
		log.Error("tenants loader init failed", "err", err)
		os.Exit(2)
	}
	go func() {
		if err := tenants.ListenAndReload(ctx, cfg.PGDSN, tenantsLoader, log); err != nil {
			log.Warn("tenants listener exited", "err", err)
		}
	}()

	// Phase 4 — boot-time LGPD invariant check (D-C1 path 3). A DB CHECK
	// already prevents mode='peak' AND data_class='sensitive' rows from
	// being inserted, but a DBA-side ALTER TABLE could accidentally drop
	// the constraint. Count the offending rows and os.Exit(1) if any
	// leaked through — sensitive data MUST NEVER reach external upstreams.
	if err := tenantsLoader.CheckSensitivePeakInvariant(ctx); err != nil {
		log.Error("sensitive+peak invariant breach detected (CHECK constraint bypassed)",
			"err", err)
		os.Exit(1)
	}

	// ====== Phase 5 — Load Shedding wiring ======
	//
	// Construction order matters:
	//   1. LatencyRing per upstream + InflightRegistry (in-memory, no I/O).
	//   2. dcgm.Scraper — optional; nil receiver is safe (ReadMiB returns
	//      (0, true) which the FSM Evaluate treats as VRAM-unknown,
	//      reducing the 2-of-3 gate to 1-of-2 over inflight + p95.
	//   3. shed.Set with publishTransition wrapper into the Redis mirror.
	//   4. HydrateFromRedis BEFORE Subscribe so the replica reads the
	//      cluster-wide view before its first Pub/Sub event arrives.
	//   5. 3 goroutines (scraper.Run conditional, set.Subscribe, set.ReconcileLoop)
	//      + 1 ticker goroutine (RunTicker) that drives the FSM 1Hz.
	//   6. shed.Middleware mounts AFTER schedule and BEFORE per-role
	//      dispatchers — wired via the proxies struct fields below.
	log.Info("shed: initializing subsystems")
	upstreamNames := loader.Names()
	shedLatency := make(map[string]*shed.LatencyRing, len(upstreamNames))
	for _, n := range upstreamNames {
		shedLatency[n] = shed.NewLatencyRing(cfg.ShedLatencyRingSize)
	}
	shedInflight := shed.NewInflightRegistry(upstreamNames)

	// dcgmScraper declared BEFORE shed.NewSet so the OnChange closure
	// captures a stable pointer. The pointer remains nil when
	// DCGM_EXPORTER_URL is empty; ReadMiB() on a nil receiver still
	// returns (0, true) so the FSM treats VRAM as unknown and the gate
	// reduces to inflight + p95 (CONTEXT.md D-A3 fail-open contract).
	var dcgmScraper *dcgm.Scraper
	if cfg.DCGMExporterURL != "" {
		dcgmScraper = dcgm.New(
			cfg.DCGMExporterURL,
			time.Duration(cfg.ShedDcgmScrapeIntervalMs)*time.Millisecond,
			time.Duration(cfg.ShedDcgmTimeoutMs)*time.Millisecond,
			log,
		)
		go dcgmScraper.Run(ctx)
	} else {
		log.Warn("shed: DCGM_EXPORTER_URL empty — VRAM signal disabled; FSM operates on inflight+P95 only")
	}

	// publishTransition mirrors each FSM transition into Redis (HSET
	// gw:shed:{upstream} + PUBLISH gw:shed:events) for cross-replica
	// visibility. Best-effort fire-and-forget — failures bump
	// GatewayShedMirrorFailures but never block the FSM (D-C3).
	pubTransition := shed.MakePublishTransition(rdb)
	shedSet := shed.NewSet(rdb, log, shed.Options{
		DefaultArmSeconds:     30,
		DefaultRecoverSeconds: 60,
		OnChange: func(upstream string, _, to shed.State, reason string) {
			// Capture per-upstream signals at transition time for the
			// event payload. Defensive nil-check on dcgmScraper covers
			// both "DCGM disabled at boot" (scraper stays nil for the
			// process lifetime) and "scraper not yet fired" early boot.
			globalInflight := shedInflight.GlobalInflight(upstream)
			p95 := uint32(0)
			if ring, ok := shedLatency[upstream]; ok {
				p95 = ring.P95()
			}
			vramMiB := int64(0)
			if dcgmScraper != nil {
				vramMiB, _ = dcgmScraper.ReadMiB()
			}
			sig := &redisx.ShedEventSignals{
				Inflight: globalInflight,
				P95Ms:    p95,
				VramMiB:  vramMiB,
			}
			// Fire-and-forget publish — dispatches to the bounded
			// worker pool inside MakePublishTransition (WR-03). The
			// call itself is non-blocking; saturation bumps
			// GatewayShedMirrorDropped rather than spawning a new
			// goroutine for every transition.
			pubTransition(upstream, to, reason, sig)
		},
	})
	shedSet.Rebuild(upstreamNames)

	// Hydrate FSM remote-state overlay from Redis BEFORE Subscribe
	// starts (RESEARCH Pitfall 3 mitigation #1). Lossy Pub/Sub may have
	// missed the prior transitions; HGETALL gives the replica an
	// initial cluster-wide view.
	if rdb != nil {
		shedSet.HydrateFromRedis(ctx, rdb, log)
	}

	// 3 goroutines: cross-replica Pub/Sub subscriber, periodic
	// reconciliation (HGETALL convergence), and the 1Hz FSM ticker.
	go shedSet.Subscribe(ctx, rdb)
	go shedSet.ReconcileLoop(ctx, rdb, shed.DefaultReconcileInterval, log)

	// thresholdSrc returns per-upstream Thresholds from the loader
	// snapshot's CircuitConfig JSONB row. Hot-reload safe — the loader
	// uses atomic.Pointer; this closure reads the same snapshot the
	// dispatcher uses.
	thresholdSrc := func(upstream string) shed.Thresholds {
		u, ok := loader.Get(upstream)
		if !ok {
			return shed.Thresholds{}
		}
		return shed.Thresholds{
			InflightMax: int64(u.CircuitConfig.ShedInflightMax),
			P95Ms:       uint32(u.CircuitConfig.ShedP95Ms),
			VramMiB:     u.CircuitConfig.ShedVramUsedMiB,
		}
	}

	go shed.RunTicker(ctx, shed.TickerDeps{
		Set:          shedSet,
		Inflight:     shedInflight,
		Latency:      shedLatency,
		VramReader:   dcgmScraper, // nil-safe: ReadMiB on nil returns (0, true)
		ThresholdSrc: thresholdSrc,
		Rdb:          rdb,
		Interval:     time.Duration(cfg.ShedTickIntervalMs) * time.Millisecond,
		TenantLabel: func(id uuid.UUID) string {
			// Inline closure avoids SlugByID on the Loader API — Get
			// returns (TenantConfig, error). On miss we emit the empty
			// slug so the Prometheus fanout drops the sample
			// (high-cardinality safe).
			tc, err := tenantsLoader.Get(id)
			if err != nil {
				return ""
			}
			return tc.Slug
		},
	}, log)

	// Upstreams hot-reload listener — rebuilds BOTH breakerSet and
	// shedSet when NOTIFY upstreams_changed fires (Phase 3 D-D4 + Phase 5
	// D-C5). Also tops up shedLatency with rings for newly-added
	// upstreams; rings for removed upstreams stay (small constant memory
	// cost) — pruning would risk racing the dispatcher mid-write.
	go func() {
		if err := upstreams.ListenAndReload(ctx, cfg.PGDSN, loader, func() {
			breakerSet.Rebuild(loader.Names())
			shedSet.Rebuild(loader.Names())
			for _, n := range loader.Names() {
				if _, ok := shedLatency[n]; !ok {
					shedLatency[n] = shed.NewLatencyRing(cfg.ShedLatencyRingSize)
				}
				// WR-05: register newly-added upstreams in the
				// inflight registry so Inc/Dec from the middleware
				// start tracking immediately. Without this, a fresh
				// `gatewayctl upstreams create` would mean shed
				// middleware silently no-ops (and bumps
				// gateway_shed_inflight_unknown_upstream_total) until
				// the next gateway restart.
				shedInflight.AddUpstream(n)
			}
		}, log); err != nil {
			log.Warn("upstreams listener exited", "err", err)
		}
	}()

	// Phase 4 — prices + fx loaders with multiplexed NOTIFY listener on a
	// single pgx connection (Research Pattern 4). Fail-fast on initial
	// load: an empty prices snapshot would silently bill every request
	// at 0 BRL.
	pricesLoader, err := billing.NewPricesLoader(ctx, pool, log)
	if err != nil {
		log.Error("prices loader init failed", "err", err)
		os.Exit(2)
	}
	fxLoader, err := billing.NewFXLoader(ctx, pool, log)
	if err != nil {
		log.Error("fx loader init failed", "err", err)
		os.Exit(2)
	}
	go func() {
		if err := billing.ListenAndReload(ctx, cfg.PGDSN, pricesLoader, fxLoader, log); err != nil {
			log.Warn("prices/fx listener exited", "err", err)
		}
	}()

	// Phase 4 — billing flusher. One goroutine drains the channel buffer
	// in 500-row batches or every 1s, whichever hits first. Non-blocking
	// Enqueue from the hot path; drops are observable via
	// gateway_billing_flush_dropped_total.
	billingFlusher := billing.NewFlusher(pool, log)
	go billingFlusher.Run(ctx)

	// Phase 4 (BL-01 fix) — UsageInterceptor now receives the full billing
	// pipeline wiring so SSE Close can compute cost + Enqueue a
	// billing.Event. Without this wiring (as shipped by Plan 04-06)
	// ai_gateway.billing_events never received rows in production.
	usageInterceptor := proxy.NewUsageInterceptor(
		accountant,
		billingFlusher,
		pricesLoader,
		fxLoader,
		tenantsLoader,
		cfg.USDBRLDefault,
		log,
	)

	// Build the LOCAL reverse proxies (Plan 02-04). Interceptors are passed
	// at construction time per Codex review [HIGH/MEDIUM] 02-05 decoupling.
	// Chat gets the audit interceptor (SSE streaming), the tool-call
	// interceptor (Phase 3 RES-06 / SC-4), AND the UsageInterceptor
	// (Phase 4 billing capture). Embeddings + audio never stream so their
	// non-SSE bodies are captured by audit.Middleware directly.
	chatRP, err := proxy.NewChatProxy(cfg.UpstreamLLMURL, log, auditInterceptor, toolCallInterceptor, usageInterceptor)
	if err != nil {
		log.Error("build chat proxy", "err", err)
		os.Exit(2)
	}
	embedRP, err := proxy.NewEmbeddingsProxy(cfg.UpstreamEmbedURL, log)
	if err != nil {
		log.Error("build embeddings proxy", "err", err)
		os.Exit(2)
	}
	audioRP, err := proxy.NewAudioProxy(cfg.UpstreamSTTURL, log)
	if err != nil {
		log.Error("build audio proxy", "err", err)
		os.Exit(2)
	}

	// Phase 4 — admin verifier + quota checker. The admin verifier uses
	// the SAME Redis client as the auth verifier (gw:admin:<hash> vs
	// gw:apikey:<hash> — disjoint namespaces). The quota checker wraps
	// the sqlc queries (usage_counters today/month) with fail-closed
	// semantics per D-A2.
	quotaChecker := quota.NewQuotaChecker(gen.New(pool), log)
	adminVerifier := admin.NewVerifier(gen.New(pool), rdb, log)

	// Phase 4 — admin bootstrap key (D-D3). If no active admin keys
	// exist AND the bootstrap env var is set, hash + insert it. If the
	// env var is empty, generate a random key, log it with a loud WARN
	// (ROTATE THIS KEY IMMEDIATELY), and insert it anyway — the
	// operator then rotates via `gatewayctl admin-key create`.
	if err := bootstrapAdminKey(ctx, pool, cfg.AdminKeyBootstrap, log); err != nil {
		log.Warn("admin key bootstrap skipped", "err", err)
	}

	// Phase 3 — Wave 4 (Plan 03-06): build the EXTERNAL fallback reverse
	// proxies (OpenRouter chat, OpenAI embed, OpenAI Whisper) using the
	// directors that inject Authorization Bearer + body rewrites. Each
	// fallback is OPTIONAL — if the env URL is empty, the upstream is
	// dropped from the dispatcher map and the dispatcher returns 503
	// when tier-0 is OPEN. This matches the upstreams.Loader's "missing
	// url_env → drop the row" semantics from Plan 03-04.
	//
	// Each external proxy uses the same audit interceptor as its local
	// counterpart (chat gets BOTH audit + tool-call). The directors
	// already strip client auth via base BuildDirector then inject the
	// upstream-bound bearer.
	// Wrap each chat-capable proxy in ToolCallTerminalGuard so SC-4 holds
	// end-to-end: when an upstream emits a tool_call delta then disconnects
	// mid-stream, the gateway emits a terminal SSE error event with code
	// "tool_call_partial_stream" + bumps gateway_tool_call_partial_total,
	// and never failovers to tier-1 (the dispatcher already enforces the
	// no-failover policy by calling proxy.ServeHTTP exactly once).
	llmRoleProxies := map[string]http.Handler{
		"local-llm": proxy.ToolCallTerminalGuard(chatRP, toolCallInterceptor, "local-llm", "/v1/chat/completions"),
	}
	if u, ok := loader.Get("openrouter-chat"); ok && u.URL != "" {
		// Phase 4: OpenRouter chat also gets the usageInterceptor so
		// streaming usage chunks (Pitfall 5 — include_usage=true injected
		// by the director) are captured for cost attribution.
		orChatProxy, perr := buildOpenRouterChatProxy(u, cfg, log,
			auditInterceptor, toolCallInterceptor, usageInterceptor)
		if perr != nil {
			log.Warn("build openrouter-chat proxy", "err", perr)
		} else {
			llmRoleProxies["openrouter-chat"] = proxy.ToolCallTerminalGuard(
				orChatProxy, toolCallInterceptor, "openrouter-chat", "/v1/chat/completions",
			)
		}
	}
	embedRoleProxies := map[string]http.Handler{"local-embed": embedRP}
	if u, ok := loader.Get("openai-embed"); ok && u.URL != "" {
		oaEmbedProxy, perr := buildOpenAIEmbedProxy(u, log)
		if perr != nil {
			log.Warn("build openai-embed proxy", "err", perr)
		} else {
			embedRoleProxies["openai-embed"] = oaEmbedProxy
		}
	}
	sttRoleProxies := map[string]http.Handler{"local-stt": audioRP}
	if u, ok := loader.Get("openai-whisper"); ok && u.URL != "" {
		oaWhisperProxy, perr := buildOpenAIWhisperProxy(u, log)
		if perr != nil {
			log.Warn("build openai-whisper proxy", "err", perr)
		} else {
			sttRoleProxies["openai-whisper"] = oaWhisperProxy
		}
	}

	// Token counter — uses the LOCAL llm URL (tier-0) for /tokenize. This
	// is the authoritative tokenizer because llama-server's BPE matches
	// the model that will actually serve the request. Fail-open if the
	// /tokenize endpoint is unreachable (caller proceeds; breaker
	// catches actual outage).
	tokenCounter := proxy.NewTokenCounter(rdb, cfg.UpstreamLLMURL, log)

	// Dispatchers — one per role. Each applies breaker-driven fallback
	// per the dispatcher decision tree (CONTEXT.md D-A2 + D-B1..B4 + D-C4).
	chatDispatcher := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:         "llm",
		Loader:       loader,
		Breaker:      breakerSet,
		TokenCounter: tokenCounter,
		ContextCap:   proxy.ChatContextCap,
		Proxies:      llmRoleProxies,
		Log:          log,
	})
	embedDispatcher := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:         "embed",
		Loader:       loader,
		Breaker:      breakerSet,
		TokenCounter: tokenCounter,
		ContextCap:   proxy.EmbedContextCap,
		Proxies:      embedRoleProxies,
		Log:          log,
	})
	audioDispatcher := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "stt",
		Loader:  loader,
		Breaker: breakerSet,
		// STT skips token-cap enforcement (multipart, no JSON to parse).
		TokenCounter: nil,
		ContextCap:   0,
		Proxies:      sttRoleProxies,
		Log:          log,
	})

	// Per-route WriteTimeout (Phase 4 folded TODO — integer-second env
	// vars preferred over the legacy time.Duration fields). wrapWithTimeout
	// handles the <=0 case as "unlimited" (used for SSE chat per D-A1
	// Plumbing). The http.Server.WriteTimeout stays 0 so the per-route
	// TimeoutHandler controls non-streaming routes without breaking SSE.
	chatHandler := wrapWithTimeout(chatDispatcher, cfg.WriteTimeoutChatS)
	embedHandler := wrapWithTimeout(embedDispatcher, cfg.WriteTimeoutEmbedS)
	audioHandler := wrapWithTimeout(audioDispatcher, cfg.WriteTimeoutAudioS)

	// Phase 4 — admin usage handler. Mounted under /admin by buildRouter;
	// the admin.Middleware (X-Admin-Key bcrypt) gates all /admin/* routes
	// per D-D3.
	adminUsageHandler := admin.NewUsageHandler(gen.New(pool), log)

	startedAt := time.Now()
	r := buildRouter(log, startedAt, verifier, proxies{
		chat:              chatHandler,
		embed:             embedHandler,
		audio:             audioHandler,
		auditWriter:       auditWriter,
		resolver:          resolver,
		upstreamsHealth:   upstreams.NewHealthHandler(loader, breakerSet, log),
		idemStore:         idemStore,
		tenantsLoader:     tenantsLoader,
		quotaChecker:      quotaChecker,
		adminUsageHandler: adminUsageHandler,
		adminVerifier:     adminVerifier,
		rdb:               rdb,
		rateLimitFailOpen: cfg.RateLimitFailOpen,
		quotaFailOpen:     cfg.QuotaFailOpen,
		// Phase 5 — shed middleware wiring (D-B4).
		upstreamsLoader: loader,
		shedSet:         shedSet,
		shedInflight:    shedInflight,
		shedLatency:     shedLatency,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           http.MaxBytesHandler(r, cfg.MaxBodyBytes),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}

	serverErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		log.Error("http server failed", "err", err)
		cancel()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown error", "err", err)
	}
	wg.Wait()
	log.Info("gateway exited cleanly")
}

// buildRouter assembles the chi router + middleware stack and mounts the
// /health, /metrics, and /v1/* routes. /v1/* is wrapped in an
// auth-protected chi.Group; /health and /metrics stay unauthenticated.
// Extracted so main_test.go can exercise the exact same wiring.
//
// verifier may be nil for the test variant — in that case the auth group
// is replaced with a passthrough so existing scaffold tests keep working
// without booting Redis/Postgres. Production main always supplies a verifier.
//
// proxies fields may also be nil (test variant); nil fields fall back to
// scaffold 501 so the main_test.go scaffold assertions keep passing.
// Plan 02-04 wired real proxies in production main. Plan 02-05 adds:
//   - audit.Middleware on the authed /v1/* group (when px.auditWriter
//     non-nil)
//   - models.Handler wrapping chat + embeddings for alias rewrite (when
//     px.resolver non-nil)
//   - Real /v1/health/upstreams handler (when px.upstreamsHealth non-nil)
func buildRouter(log *slog.Logger, startedAt time.Time, verifier *auth.Verifier, px proxies) *chi.Mux {
	r := chi.NewRouter()
	r.Use(httpx.RequestID)
	r.Use(httpx.Logger(log))
	r.Use(httpx.Recoverer(log))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"version":  obs.BuildVersion,
			"uptime_s": int64(time.Since(startedAt).Seconds()),
		})
	})
	r.Handle("/metrics", obs.Handler())

	// Authenticated /v1/* group. Chain order (D-D1):
	//   obs.RequestsMiddleware (outermost — counts every response status) →
	//   auth → audit → rate-limit → quota → schedule → (per-handler)
	//   idempotency → tokencount (inside dispatcher) → proxy
	//
	// HI-04 fix (Phase 4 review): obs.RequestsMiddleware was originally
	// mounted LAST. chi.Use stacks OUTERMOST-first, so a "last" mount is
	// the INNERMOST wrapper — it never observes 4xx/5xx written by
	// rate-limit, quota, schedule, or auth because those run OUTSIDE it
	// and write to the raw ResponseWriter. Mounting FIRST makes the
	// statusRecorder wrap every other middleware, so gateway_requests_total
	// counts every exit path (429 rate-limit, 429 quota, 503 schedule,
	// 401 auth, etc.).
	r.Group(func(pg chi.Router) {
		// Phase 4 folded TODO — request instrumentation (RequestsTotal).
		// Mounted FIRST so the statusRecorder wraps every subsequent
		// middleware's response writer and captures the final status
		// regardless of which middleware wrote it.
		pg.Use(obs.RequestsMiddleware(log))

		if verifier != nil {
			pg.Use(auth.Middleware(verifier, log))
		}
		if px.auditWriter != nil {
			pg.Use(audit.Middleware(px.auditWriter, log))
		}
		// Phase 4 — rate-limit + quota + schedule (D-D1 chain order).
		// All three are mounted at the group level so every /v1/* route
		// inherits them. idempotency stays per-handler (chat only) per
		// D-C4; tokencount stays inside the dispatcher.
		if px.rdb != nil && px.tenantsLoader != nil {
			pg.Use(quota.RateLimitMiddleware(px.rdb, px.tenantsLoader, px.rateLimitFailOpen, log))
		}
		if px.quotaChecker != nil && px.tenantsLoader != nil {
			pg.Use(quota.QuotaMiddleware(px.quotaChecker, px.tenantsLoader, px.quotaFailOpen, log))
		}
		if px.tenantsLoader != nil {
			pg.Use(schedule.Middleware(px.tenantsLoader, log))
		}

		// Phase 5 — shed middleware (D-B4). Mounted AFTER schedule so a
		// peak-off-hours override stamped by schedule is observed (and
		// becomes shed_decision=skipped_peak_offhours); mounted BEFORE
		// the per-role dispatcher so the override+decision both flow
		// into dispatch / audit. Skipped if any of the four shed
		// collaborators is nil (scaffold test variant).
		if px.shedSet != nil && px.shedInflight != nil &&
			px.upstreamsLoader != nil && px.tenantsLoader != nil {
			pg.Use(shed.Middleware(shed.MiddlewareDeps{
				Loader:   px.upstreamsLoader,
				Tenants:  px.tenantsLoader,
				Set:      px.shedSet,
				Inflight: px.shedInflight,
				Latency:  px.shedLatency,
			}, log))
		}

		// Wrap chat + embed with model rewrite when a resolver is present.
		chatHandler := px.chat
		embedHandler := px.embed
		if px.resolver != nil {
			if chatHandler != nil {
				chatHandler = models.Handler(px.resolver, "llm", chatHandler)
			}
			if embedHandler != nil {
				embedHandler = models.Handler(px.resolver, "embed", embedHandler)
			}
		}
		// Idempotency middleware (Plan 02-06) wraps ONLY chat (D-C4 — not
		// embeddings, not audio). Runs AFTER auth + audit (needs tenant_id
		// scope, type-asserts the audit writer for the replay flag) and
		// BEFORE the proxy (intercepts request/response).
		if px.idemStore != nil && chatHandler != nil {
			chatHandler = idempotency.Middleware(px.idemStore, log)(chatHandler)
		}
		mount := func(method, route string, h http.Handler) {
			if h == nil {
				pg.MethodFunc(method, route, scaffoldNotImplemented)
				return
			}
			pg.Method(method, route, h)
		}
		mount(http.MethodPost, "/v1/chat/completions", chatHandler)
		mount(http.MethodPost, "/v1/embeddings", embedHandler)
		mount(http.MethodPost, "/v1/audio/transcriptions", px.audio)
		if px.upstreamsHealth != nil {
			pg.Method(http.MethodGet, "/v1/health/upstreams", px.upstreamsHealth)
		} else {
			pg.MethodFunc(http.MethodGet, "/v1/health/upstreams", scaffoldNotImplemented)
		}
		// GET scaffold on the 3 POST routes (keeps existing 501
		// semantics for non-POST methods on the proxied paths).
		pg.MethodFunc(http.MethodGet, "/v1/chat/completions", scaffoldNotImplemented)
		pg.MethodFunc(http.MethodGet, "/v1/embeddings", scaffoldNotImplemented)
		pg.MethodFunc(http.MethodGet, "/v1/audio/transcriptions", scaffoldNotImplemented)
		pg.MethodFunc(http.MethodPost, "/v1/health/upstreams", scaffoldNotImplemented)
	})

	// Phase 4 — admin sub-router (/admin/*) gated by X-Admin-Key bcrypt
	// middleware (D-D3). Mounted only when adminVerifier is present;
	// scaffold tests leave it nil and /admin/* falls through to r.NotFound
	// which emits a 404 envelope.
	if px.adminVerifier != nil {
		adminRouter := chi.NewRouter()
		adminRouter.Use(admin.Middleware(px.adminVerifier, log))
		if px.adminUsageHandler != nil {
			adminRouter.Method(http.MethodGet, "/usage", px.adminUsageHandler)
		}
		r.Mount("/admin", adminRouter)
	}

	// Any other path also returns an OpenAI envelope (not chi's default 404).
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteOpenAIError(w, http.StatusNotFound,
			"invalid_request_error", "not_found",
			"The requested path was not found.")
	})

	return r
}

func scaffoldNotImplemented(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteOpenAIError(w, http.StatusNotImplemented,
		"api_error", "not_implemented",
		"This route will be wired by subsequent Phase 2 plans.")
}

// Compile-time assertion that pgxpool/redis are imported (used inside main).
// Keeps imports honest if main is restructured.
var (
	_ = (*pgxpool.Pool)(nil)
	_ = (*redis.Client)(nil)
)

// buildOpenRouterChatProxy constructs a *httputil.ReverseProxy for the
// openrouter-chat fallback upstream. Uses BuildOpenRouterDirector to
// inject Authorization Bearer + body rewrite with provider.order +
// allow_fallbacks per CONTEXT.md D-C2 (Novita pin per D-C1 amendment).
//
// Wired with the same audit + tool-call interceptors as the local chat
// proxy so SSE streams from OpenRouter get the same observability +
// tool-call protection.
func buildOpenRouterChatProxy(u upstreams.UpstreamConfig, cfg config.Config, log *slog.Logger,
	auditInterceptor *audit.AuditInterceptor,
	toolCallInterceptor *proxy.ToolCallInterceptor,
	usageInterceptor *proxy.UsageInterceptor) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil {
		return nil, fmt.Errorf("parse openrouter url %q: %w", u.URL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid openrouter url %q (missing scheme or host)", u.URL)
	}
	rp := &httputil.ReverseProxy{
		Director: proxy.BuildOpenRouterDirector(parsed, u.AuthBearer,
			cfg.UpstreamOpenRouterProviderOrder, cfg.UpstreamOpenRouterAllowFallbacks),
		FlushInterval: -1, // SSE streaming
		Transport: &http.Transport{
			MaxIdleConns:          50,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		ErrorHandler:   proxy.ErrorHandler("openrouter-chat", log),
		ModifyResponse: proxy.ComposeInterceptors(auditInterceptor, toolCallInterceptor, usageInterceptor),
	}
	return rp, nil
}

// buildOpenAIEmbedProxy constructs the openai-embed fallback proxy. The
// director rewrites model="text-embedding-3-small" + dimensions=1024 for
// BGE-M3 parity. No streaming, no tool-call interceptor (embeddings are
// always non-streaming JSON).
func buildOpenAIEmbedProxy(u upstreams.UpstreamConfig, log *slog.Logger) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil {
		return nil, fmt.Errorf("parse openai-embed url %q: %w", u.URL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid openai-embed url %q", u.URL)
	}
	rp := &httputil.ReverseProxy{
		Director: proxy.BuildOpenAIEmbedDirector(parsed, u.AuthBearer),
		Transport: &http.Transport{
			MaxIdleConns:          50,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
		ErrorHandler: proxy.ErrorHandler("openai-embed", log),
	}
	return rp, nil
}

// buildOpenAIWhisperProxy constructs the openai-whisper fallback proxy.
// The director leaves the multipart body untouched (boundary preserved);
// only Authorization is added.
func buildOpenAIWhisperProxy(u upstreams.UpstreamConfig, log *slog.Logger) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil {
		return nil, fmt.Errorf("parse openai-whisper url %q: %w", u.URL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid openai-whisper url %q", u.URL)
	}
	rp := &httputil.ReverseProxy{
		Director: proxy.BuildOpenAIWhisperDirector(parsed, u.AuthBearer),
		Transport: &http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		ErrorHandler: proxy.ErrorHandler("openai-whisper", log),
	}
	return rp, nil
}

// bootstrapAdminKey seeds the ai_gateway.admin_keys table on first boot
// (D-D3). If at least one active admin key already exists, this is a
// no-op. Otherwise:
//
//   - If bootstrap != "", that value is hashed and inserted with
//     label="bootstrap". The operator knows the key; no WARN is logged.
//   - If bootstrap == "", a random 16-byte hex key is generated and
//     inserted with label="bootstrap-random". The key is logged via WARN
//     so the operator can capture it from the logs.
//
// Caller consumes any error with a Warn (boot is not blocked) — a failed
// bootstrap simply means admin endpoints stay locked until an operator
// runs `gatewayctl admin-key create`.
func bootstrapAdminKey(ctx context.Context, pool *pgxpool.Pool, bootstrap string, log *slog.Logger) error {
	q := gen.New(pool)
	n, err := q.CountActiveAdminKeys(ctx)
	if err != nil {
		return fmt.Errorf("count active admin keys: %w", err)
	}
	if n > 0 {
		return nil
	}
	label := "bootstrap"
	if bootstrap == "" {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("generate bootstrap key: %w", err)
		}
		bootstrap = "ifix_admin_" + hex.EncodeToString(buf)
		label = "bootstrap-random"
		// ME-06 fix: previously the key was logged via slog with
		// attribute name "key" — which is NOT in the Redactor
		// allow-list, so the plaintext key would flow unredacted to
		// Sentry/Portainer/any structured-log sink. Emit to stderr as
		// plain text (plus a slog marker without the key itself) so
		// operators capture the key once from console but the
		// structured log pipeline never sees it.
		fmt.Fprintf(os.Stderr, "\n*** ROTATE THIS KEY IMMEDIATELY ***\nbootstrap admin key: %s\n*** one-time display; subsequent boots will not reprint ***\n\n", bootstrap)
		log.Warn("bootstrap admin key generated; see stderr for the one-time display. ROTATE via `gatewayctl admin-key create` + revoke-bootstrap.")
	}
	sum := sha256.Sum256([]byte(bootstrap))
	bcryptHash, err := bcrypt.GenerateFromPassword([]byte(bootstrap), 10)
	if err != nil {
		return fmt.Errorf("bcrypt bootstrap key: %w", err)
	}
	// Preview suffix (last 4 chars) — displayed in admin UI + audit.
	suffix := bootstrap
	if len(bootstrap) >= 4 {
		suffix = bootstrap[len(bootstrap)-4:]
	}
	if _, err := q.InsertAdminKey(ctx, gen.InsertAdminKeyParams{
		KeyLookupHash: sum[:],
		KeyHash:       string(bcryptHash),
		KeyPrefix:     "ifix_admin_****" + suffix,
		Label:         label,
	}); err != nil {
		return fmt.Errorf("insert bootstrap admin key: %w", err)
	}
	log.Info("admin key bootstrapped",
		"label", label,
		"key_prefix", "ifix_admin_****"+suffix)
	return nil
}

// wrapWithTimeout wraps h with http.TimeoutHandler for `seconds` > 0,
// returning a handler that writes a JSON timeout envelope if the inner
// handler does not complete within the budget. seconds <= 0 means
// "unlimited" (used for SSE chat). Phase 4 folded TODO: per-route
// WriteTimeout (chat=0, embed=30, audio=120). Takes integer seconds
// directly from the cfg.WriteTimeoutChatS/EmbedS/AudioS fields.
func wrapWithTimeout(next http.Handler, seconds int) http.Handler {
	if seconds <= 0 {
		return next
	}
	return http.TimeoutHandler(next, time.Duration(seconds)*time.Second,
		`{"error":{"type":"timeout","message":"upstream timeout","code":"upstream_timeout"}}`)
}

// mustLoadLocation returns the *time.Location for name or fails the process
// fast at boot. Called by Phase 4 wiring (schedule middleware in 04-06) so a
// missing tzdata package or typo in the tenant's schedule_timezone column
// is caught at startup rather than on the first peak-mode request. The
// stdlib blank-import `_ "time/tzdata"` above guarantees all IANA zones
// resolve in any container image (distroless/static included).
func mustLoadLocation(name string, log *slog.Logger) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Error("failed to load time.Location; tzdata missing?", "zone", name, "err", err)
		os.Exit(1)
	}
	return loc
}

// newLogger builds the slog.Logger wrapped in the Redactor so sensitive
// attribute values are globally redacted (D-B7).
func newLogger(cfg config.Config) *slog.Logger {
	lvl := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var inner slog.Handler
	if cfg.Env == "development" {
		inner = slog.NewTextHandler(os.Stdout, opts)
	} else {
		inner = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(httpx.NewRedactor(inner)).With("module", "GATEWAY")
}
