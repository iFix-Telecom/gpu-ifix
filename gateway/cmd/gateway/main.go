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
	"io"
	"log/slog"
	"net"
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
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/alert"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/dcgm"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
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
	tts             http.Handler         // Phase 06.7 — POST /v1/audio/speech (tts dispatcher: tier-0 pod Chatterbox -> tier-1 Piper adapter)
	voices          *proxy.VoiceHandlers // Phase 06.7 — /v1/audio/voices CRUD (nil disables the routes in the scaffold variant)
	voicesMaxBytes  int64                // Phase 06.7 — VOICE_MAX_UPLOAD_BYTES cap applied to the upload route
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

	// Phase 7 — admin observability handlers (OBS-01/OBS-07). Mounted
	// under the SAME admin-key-gated /admin sub-router as adminUsageHandler.
	// nil in the scaffold test variant; production main wires both.
	adminMetricsHandler http.Handler
	adminAuditHandler   http.Handler

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

	// ====== Phase 7 — Alerting goroutine wiring (OBS-04/05/06) ======
	//
	// Spawned EARLY — textually BEFORE go breakerSet.Subscribe(ctx) and
	// go emergReconciler.Run(ctx) — because Redis Pub/Sub is at-most-once
	// (07-RESEARCH Pitfall 4): a breaker/shed/emerg transition that fires
	// during the boot gap is silently lost if the alerter is not yet
	// subscribed. ReconcileBoot additionally replays an active emergency
	// incident found in the Redis state mirror so a mid-incident restart
	// still pages.
	//
	// Each of the three channels is OPTIONAL: if its required config
	// fields are unset, the client is skipped with a single WARN (the
	// SentryDSN precedent — an unset alert var NEVER fails boot). With
	// zero channels the Alerter still runs: it classifies, dedups, and
	// logs every event; the external fan-out is just an empty set.
	alertChannels := buildAlertChannels(cfg, log)
	alerter := alert.NewAlerter(rdb, alertChannels, log)
	alerter.ReconcileBoot(ctx)
	go alerter.Run(ctx)
	log.Info("Phase 7 alerter started", "channels", len(alertChannels))

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
	// Phase 06.9 R4: local-tier proxies pass-through — body.model forwarded
	// as-is; Plan 01 seeded identity rows (qwen,llm,qwen,local-llm) provide
	// alias→target mapping for any future resolver consumption. NewChatProxy /
	// NewEmbeddingsProxy / NewAudioProxy do NOT call the resolver and do NOT
	// touch body.model — the alias the client sent is what the local pod
	// receives. Validated by Plan 05b byte-identical regression tests.
	chatRP, err := proxy.NewChatProxy(cfg.UpstreamLLMURL, log, auditInterceptor, toolCallInterceptor, usageInterceptor)
	if err != nil {
		log.Error("build chat proxy", "err", err)
		os.Exit(2)
	}
	// Phase 06.9 R4: local-tier proxies pass-through — see comment above.
	embedRP, err := proxy.NewEmbeddingsProxy(cfg.UpstreamEmbedURL, log)
	if err != nil {
		log.Error("build embeddings proxy", "err", err)
		os.Exit(2)
	}
	// Phase 11.1: local-stt tier-0 was removed (D-A4). audioRP is now built
	// only when UPSTREAM_STT_URL is still set (transitional compat for stale
	// .env files); otherwise the local-stt entry in sttRoleProxies is omitted
	// and STT routes exclusively through the openai-whisper tier-1 fallback.
	var audioRP http.Handler
	if cfg.UpstreamSTTURL != "" {
		audioRP, err = proxy.NewAudioProxy(cfg.UpstreamSTTURL, log, resolver)
		if err != nil {
			log.Error("build audio proxy", "err", err)
			os.Exit(2)
		}
	}
	// Phase 06.7 — tier-0 TTS proxy (POST /v1/audio/speech). UpstreamTTSURL is
	// a placeholder the primary-pod reconciler overrides at runtime (D-11), so
	// it may be empty at boot. The constructor needs a syntactically valid URL;
	// fall back to a dead-localhost placeholder when unset so boot never
	// crashes — the breaker handles the unreachable tier-0 until the reconciler
	// writes the live override.
	ttsTier0URL := cfg.UpstreamTTSURL
	if ttsTier0URL == "" {
		ttsTier0URL = "http://127.0.0.1:1"
	}
	ttsRP, err := proxy.NewTTSProxy(ttsTier0URL, log)
	if err != nil {
		log.Error("build tts proxy", "err", err)
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
		// Dynamic primary/emergency pod override (loader.Resolve → "emergency_pod_llm").
		// SSE flush + chat interceptors, wrapped in the same tool-call guard as local-llm.
		"emergency_pod_llm": proxy.ToolCallTerminalGuard(
			proxy.NewDynamicOverrideProxy("llm",
				func() (string, bool) { return loader.Tier0OverrideURL("llm") },
				-1, &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 10, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 30 * time.Second},
				log, auditInterceptor, toolCallInterceptor, usageInterceptor),
			toolCallInterceptor, "emergency_pod_llm", "/v1/chat/completions"),
	}
	if u, ok := loader.Get("openrouter-chat"); ok && u.URL != "" {
		// Phase 4: OpenRouter chat also gets the usageInterceptor so
		// streaming usage chunks (Pitfall 5 — include_usage=true injected
		// by the director) are captured for cost attribution.
		orChatProxy, perr := buildOpenRouterChatProxy(u, cfg, log,
			auditInterceptor, toolCallInterceptor, usageInterceptor, resolver)
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
		oaEmbedProxy, perr := buildOpenAIEmbedProxy(u, log, resolver)
		if perr != nil {
			log.Warn("build openai-embed proxy", "err", perr)
		} else {
			embedRoleProxies["openai-embed"] = oaEmbedProxy
		}
	}
	sttRoleProxies := map[string]http.Handler{
		// Dynamic primary/emergency pod override (loader.Resolve → "emergency_pod_stt").
		// Buffered (transcription is a single JSON body); no interceptors (parity with audioRP).
		// quick 260617-jod (SEED-018): the override pod runs the SAME Speaches as
		// local-stt, so this STT-aware constructor rewrites the multipart "model"
		// field via the resolver against "local-stt" ((whisper,local-stt) →
		// Systran/faster-whisper-large-v3) — bringing the primary pod up no longer
		// regresses STT to a 404 "Model 'whisper' is not installed".
		"emergency_pod_stt": proxy.NewDynamicOverrideSTTProxy(
			func() (string, bool) { return loader.Tier0OverrideURL("stt") },
			0, &http.Transport{MaxIdleConns: 20, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 60 * time.Second},
			resolver, log),
	}
	// Phase 11.1: local-stt is registered ONLY if UPSTREAM_STT_URL still set
	// (transitional compat). New deployments leave it unset and STT routes via
	// the openai-whisper tier-1 fallback below.
	if audioRP != nil {
		sttRoleProxies["local-stt"] = audioRP
	}
	if u, ok := loader.Get("openai-whisper"); ok && u.URL != "" {
		oaWhisperProxy, perr := buildOpenAIWhisperProxy(u, log, resolver)
		if perr != nil {
			log.Warn("build openai-whisper proxy", "err", perr)
		} else {
			// Phase 06.9 WARNING-3: wrap the Whisper proxy in WhisperAbortGuard
			// so duplicate-"model" multipart requests are rejected with HTTP 400
			// BEFORE the proxy runs (no escape hatch / no degraded fallback).
			// Mirrors the ToolCallTerminalGuard wrapping pattern at line 619.
			sttRoleProxies["openai-whisper"] = proxy.WhisperAbortGuard(
				oaWhisperProxy, resolver, "openai-whisper", log,
			)
		}
	}
	// Phase 11.2 D-B4 — gemini-stt tier-1 primary fallback. Wired ONLY when
	// the upstream row exists in the loader (migration 0029) AND the URL +
	// API key env vars are populated. Without the key the proxy is omitted
	// and the dispatcher cascade simply skips gemini-stt (D-B5′ behavior).
	if u, ok := loader.Get("gemini-stt"); ok && cfg.UpstreamSTTFallback1URL != "" && cfg.UpstreamSTTFallback1AuthBearer != "" {
		geminiProxy, perr := buildGeminiSTTProxy(cfg.UpstreamSTTFallback1URL, cfg.UpstreamSTTFallback1AuthBearer, resolver, log)
		if perr != nil {
			log.Warn("build gemini-stt proxy", "err", perr)
		} else {
			sttRoleProxies["gemini-stt"] = geminiProxy
		}
		_ = u
	}
	// Phase 11.2 D-B8 — groq-whisper tier-1 fallback (REUSES openai-whisper
	// director — Groq endpoint is OpenAI-compatible). Loader row carries
	// the schema URL; the director takes URL + bearer from env config so
	// operators can hot-swap without a migration. WhisperAbortGuard wraps
	// it for consistent duplicate-model handling.
	if u, ok := loader.Get("groq-whisper"); ok && cfg.UpstreamSTTFallback2URL != "" && cfg.UpstreamSTTFallback2AuthBearer != "" {
		groqUpstream := upstreams.UpstreamConfig{
			Name:       "groq-whisper",
			URL:        cfg.UpstreamSTTFallback2URL,
			AuthBearer: cfg.UpstreamSTTFallback2AuthBearer,
		}
		groqRP, perr := buildGroqWhisperProxy(groqUpstream, log, resolver)
		if perr != nil {
			log.Warn("build groq-whisper proxy", "err", perr)
		} else {
			sttRoleProxies["groq-whisper"] = proxy.WhisperAbortGuard(
				groqRP, resolver, "groq-whisper", log,
			)
		}
		_ = u
	}
	// Phase 06.7 — tts role proxies. tier-0 (local-tts) = the JSON->binary
	// reverse proxy whose upstream the reconciler overrides; tier-1
	// (voice-api-piper) = the GATE-3 Option A adapter against UpstreamTTSPiperURL.
	// The tier-1 adapter is OPTIONAL: if UPSTREAM_TTS_PIPER_URL is unset the
	// fallback is dropped from the map and the dispatcher returns 503 when
	// tier-0 is OPEN (mirrors the openrouter/openai fallback semantics).
	ttsRoleProxies := map[string]http.Handler{"local-tts": ttsRP}
	// D-11/D-13 — when the primary reconciler overrides tts with the live pod
	// URL, loader.Resolve("tts",0) returns name "emergency_pod_tts". Register a
	// dynamic proxy under that name whose Director reads the override URL per
	// request (the pod URL changes per lifecycle). Without this the dispatcher
	// 503s ("Upstream proxy not registered") on every pod-routed TTS request.
	ttsRoleProxies["emergency_pod_tts"] = proxy.NewDynamicTTSProxy(
		func() (string, bool) { return loader.Tier0OverrideURL("tts") }, log)
	if cfg.UpstreamTTSPiperURL != "" {
		piperAdapter, perr := proxy.NewPiperTTSAdapter(cfg.UpstreamTTSPiperURL, log)
		if perr != nil {
			log.Warn("build piper-tts adapter", "err", perr)
		} else {
			ttsRoleProxies["voice-api-piper"] = piperAdapter
		}
	}

	// Token counter — uses the LOCAL llm URL (tier-0) for /tokenize. This
	// is the authoritative tokenizer because llama-server's BPE matches
	// the model that will actually serve the request. Fail-open if the
	// /tokenize endpoint is unreachable (caller proceeds; breaker
	// catches actual outage).
	tokenCounter := proxy.NewTokenCounter(rdb, cfg.UpstreamLLMURL, log)

	// ====== Phase 6 — Auto-provisioning Emergency Pod (Vast.ai) wiring ======
	//
	// Construction order matters:
	//   1. If VAST_AI_API_KEY is empty → log Warn + skip the entire block.
	//      Reconciler stays nil; chat dispatcher's EmergTraffic field stays
	//      nil; emerg.IsActive() is never called. Phase 6 cleanly disabled.
	//   2. Build the vast.Client + boot-time Ping (D-A5). Failure is
	//      NON-FATAL — we log Warn and continue so a stale/wrong key surfaces
	//      in the logs without crashing the gateway. Operator updates env
	//      via Portainer + redeploy to fix.
	//   3. Build FSM with an onChange callback that mirrors transitions to
	//      Redis (gw:emerg:state Hash + gw:emerg:events Pub/Sub) so other
	//      replicas + gatewayctl can observe live FSM state.
	//   4. Build Reconciler with full Deps (DB, Redis, FSM, Vast, Loader, Cfg).
	//      Spawn `go reconciler.Run(ctx)` — the Run loop spawns Subscribe +
	//      SubscribeEmergCommands goroutines internally (W11 ordering).
	//   5. Pass reconciler as EmergTraffic to the chat dispatcher (D-E3) so
	//      RegisterTraffic fires on each request that resolves to the
	//      emergency pod (drives the idle-grace destroy timer).
	//
	// Phase 7 (07-06): emergFSM is hoisted to function scope so the
	// /admin/metrics handler can read FSM.State() for the dashboard even
	// when Phase 6 is disabled (nil FSM → handler reports "unknown").
	var emergReconciler *emerg.Reconciler
	var emergFSM *emerg.FSM
	if cfg.VastAIAPIKey == "" {
		log.Warn("Phase 6 emergency reconciler DISABLED: VAST_AI_API_KEY not set")
	} else {
		vastClient := vast.NewClient(cfg.VastAIAPIKey)
		// Boot validation per D-A5. Non-fatal: a stale key should NOT prevent
		// the gateway from starting (the rest of the proxy works fine without
		// emergency provisioning). Operator sees the warning and rotates the
		// key via Portainer.
		pingCtx, pingCancel := context.WithTimeout(ctx, 30*time.Second)
		if perr := vastClient.Ping(pingCtx); perr != nil {
			log.Warn("vast.Ping failed at boot; emergency reconciler still starts but Vast ops will fail in runtime",
				"err", perr)
		} else {
			log.Info("vast.Ping ok")
		}
		pingCancel()

		// FSM onChange mirrors transitions to Redis so other replicas +
		// gatewayctl can observe via gw:emerg:state Hash + gw:emerg:events
		// Pub/Sub. Best-effort writes — failures are logged but never block
		// the in-process FSM (mirror philosophy from breaker/shed packages).
		emergFSM = emerg.NewFSM(log, func(from, to emerg.State, reason string) {
			ev := redisx.EmergEvent{
				Type:      "transition",
				State:     to.String(),
				Reason:    reason,
				SinceUnix: time.Now().Unix(),
				ReplicaID: hostnameOrUnknown(),
			}
			if perr := redisx.PublishEmergEvent(context.Background(), rdb, ev); perr != nil {
				log.Warn("emerg FSM onChange: PublishEmergEvent failed",
					"from", from.String(), "to", to.String(), "err", perr)
			}
			if werr := redisx.WriteEmergState(context.Background(), rdb,
				to.String(), "", "", "", time.Now().Unix()); werr != nil {
				log.Warn("emerg FSM onChange: WriteEmergState failed",
					"to", to.String(), "err", werr)
			}
		})
		// Phase 7 (OBS-07) — thread the shared async audit writer into the
		// FSM so every transition leaves an append-only audit_log row with
		// event_kind = "fsm_transition". auditWriter was constructed far
		// earlier in boot; this is purely a setter, no reordering needed.
		emergFSM.SetAuditWriter(auditWriter)
		emergReconciler = emerg.NewReconciler(emerg.Deps{
			DB:     pool,
			Redis:  rdb,
			FSM:    emergFSM,
			Vast:   vastClient,
			Loader: loader,
			Cfg:    cfg,
			Log:    log,
		})
		go emergReconciler.Run(ctx)
		log.Info("Phase 6 emergency reconciler started",
			"replica_id", emergReconciler.ReplicaID(),
			"trigger_seconds", cfg.ProvisionTriggerFailedOverSeconds,
			"healthy_seconds", cfg.ProvisionHealthyDurationSeconds,
			"idle_grace_seconds", cfg.ProvisionIdleGraceSeconds,
			"coldstart_budget_seconds", cfg.ProvisionColdStartBudgetSeconds,
			"monthly_budget_brl", cfg.MonthlyEmergencyBudgetBRL,
		)
	}

	// ====== Phase 6.6 — Primary Pod (scheduled-driven Vast.ai) wiring =========
	//
	// Construction order matters (depends on Plans 06.6-06a + 06.6-06b):
	//   1. If VAST_AI_API_KEY is empty → log Warn + skip the entire block.
	//      The primary reconciler shares Vast.ai credentials with emerg;
	//      a missing key disables BOTH features. The primary FSM stays
	//      unconstructed; gw:primary:events has no consumer.
	//   2. ParseScheduleEnv resolves IANA timezone + UpHour/DownHour/Days
	//      from cfg.PrimaryPodSchedule* env vars. Fail-fast on invalid
	//      timezone (Pitfall #4) — operator misconfig must surface at boot.
	//   3. Build primary.FSM. The optional stateChangeWriter is left nil
	//      (audit emission deferred to a future plan — audit.Writer's
	//      WriteStateChange signature `(kind string, ev Event)` does not
	//      satisfy primary.stateChangeWriter `(kind string, ev any) error`).
	//      onChange callback mirrors transitions to Redis (gw:primary:state
	//      Hash + gw:primary:events Pub/Sub) for cross-replica + gatewayctl
	//      visibility, mirroring the emerg FSM onChange shape.
	//   4. Build primary.Reconciler with full Deps (11 fields). Concrete
	//      types satisfy the 3 adapter interfaces via duck typing:
	//      *upstreams.Loader satisfies LoaderAdapter (3-role override map
	//      extended in Plan 06.6-06b), *dcgm.Scraper satisfies
	//      DCGMScraperAdapter (SetURL added via sync.RWMutex in
	//      Plan 06.6-06b), *shed.InflightRegistry satisfies InflightAdapter
	//      (Count added in Plan 06.6-06b). dcgmScraper may be nil — nil
	//      *dcgm.Scraper's SetURL is a defensive no-op (Plan 06.6-06b summary).
	//   5. Spawn `go primaryReconciler.Start(ctx)` UNCONDITIONALLY per
	//      reviews #2 (2026-05-17) — event subscriber always launches;
	//      cfg.PrimaryPodScheduleDisabled gates only schedule ticks inside
	//      runScheduleLoop, not Start() itself. gatewayctl primary force-up
	//      works even under DISABLED=true soak-gate posture (WAVE0-GATES
	//      Decision 5).
	//   6. If the emerg reconciler was wired (Phase 6), spawn its primary
	//      event subscriber so the Pitfall #11 force-destroy handoff runs
	//      (Plan 06.6-08 Task 1). Without this the emerg + primary pair can
	//      double-provision a tier-0 LLM pod during peak-window onset.
	//
	// Wave 0 orthogonality: this wiring is agnostic to in-pod orchestration
	// (Plan 06.6-04 supervisord LOCKED). The gateway sees 4 HTTP endpoints
	// on Vast-exposed host ports regardless of orchestration mechanism.
	var primaryReconciler *primary.Reconciler
	if cfg.VastAIAPIKey == "" {
		log.Warn("Phase 6.6 primary reconciler DISABLED: VAST_AI_API_KEY not set (shared with emerg)")
	} else {
		primaryRule, perr := primary.ParseScheduleEnv(cfg)
		if perr != nil {
			// Pitfall #4 fail-fast — invalid IANA timezone or other schedule
			// misconfig must surface at boot, not silently default. Operator
			// fixes env var + redeploys.
			log.Error("primary schedule parse failed", "err", perr)
			os.Exit(1)
		}

		// onChange mirrors FSM transitions to Redis for cross-replica +
		// gatewayctl observability. Best-effort writes — failures logged
		// but do NOT block the in-process FSM (mirror philosophy from
		// breaker/shed/emerg packages).
		primaryFSM := primary.NewFSM(nil, func(from, to primary.State, at time.Time, reason string) {
			ev := redisx.PrimaryEvent{
				Type:      "transition",
				State:     to.String(),
				Reason:    reason,
				SinceUnix: at.Unix(),
				ReplicaID: hostnameOrUnknown(),
			}
			if pubErr := redisx.PublishPrimaryEvent(context.Background(), rdb, ev); pubErr != nil {
				log.Warn("primary FSM onChange: PublishPrimaryEvent failed",
					"from", from.String(), "to", to.String(), "err", pubErr)
			}
			if werr := redisx.WritePrimaryState(context.Background(), rdb,
				to.String(), "", "", "", at.Unix()); werr != nil {
				log.Warn("primary FSM onChange: WritePrimaryState failed",
					"to", to.String(), "err", werr)
			}
		})

		// Build the real vast.Client for primary. We construct a SEPARATE
		// instance from emerg's vastClient so the two reconcilers do not
		// share connection-level state (parity with emerg's own Ping at
		// boot — covered there). Skipping a duplicate Ping here keeps boot
		// time bounded.
		primaryVastClient := vast.NewClient(cfg.VastAIAPIKey)

		// Default per-endpoint HealthCheck implementation. Each call issues
		// a single GET with a small timeout; 2xx → healthy. Matches the
		// pattern emerg's internal checkHealth uses, exposed as a function
		// here because the primary package's Deps.HealthCheck is a closure
		// (so tests can inject without ceremony).
		primaryHealthCheck := func(hctx context.Context, url string) bool {
			if url == "" {
				return false
			}
			probeCtx, cancel := context.WithTimeout(hctx, 5*time.Second)
			defer cancel()
			req, rerr := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
			if rerr != nil {
				return false
			}
			client := &http.Client{Timeout: 5 * time.Second}
			resp, herr := client.Do(req)
			if herr != nil {
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode >= 200 && resp.StatusCode < 300
		}

		// CR-01 (6.6.Y): connection-LEVEL reachability probe for Option B.
		// Distinguishes the observed spike failure (running + published Vast
		// ports yet TCP-unreachable: dial TIMEOUT) from a legitimately-warming
		// cold start (host up, service not ready: connect SUCCESS or REFUSED).
		// Returns true if the TCP dial connects OR is refused (host responding
		// — keep polling under the cold-start budget); false ONLY on a dial
		// timeout / no-route (host never NAT-published — fail fast once the
		// port-bind budget is exhausted). Keys on the URL's host:port, NOT on
		// the unreliable Vast ports map (spike DIRECTIVE).
		primaryReachable := func(rctx context.Context, rawURL string) bool {
			if rawURL == "" {
				return false
			}
			u, perr := url.Parse(rawURL)
			if perr != nil || u.Host == "" {
				return false
			}
			dialer := net.Dialer{Timeout: 5 * time.Second}
			conn, derr := dialer.DialContext(rctx, "tcp", u.Host)
			if derr == nil {
				_ = conn.Close()
				return true // host accepted the connection — reachable
			}
			// A connection REFUSED means the host IS reachable (NAT-published)
			// but nothing is listening on the port yet — services still
			// booting. That is a legitimate cold start, NOT the spike failure.
			// Only a dial TIMEOUT / no-route (host never published its port)
			// counts as unreachable.
			if errors.Is(derr, syscall.ECONNREFUSED) {
				return true
			}
			return false
		}

		// SEED-019 part 3: DeviceReport fetches the pod-reported whisper
		// device from the pod's :9100/whisper_device report and returns it
		// ONLY when it is exactly "cuda" or "cpu" (the whitelist / default-deny
		// gate). Any other value, a non-200, a parse error, or an unreachable
		// pod → "" → the reconciler skips the stt tier-0 override → STT routes
		// to the tier-1 gemini-stt cascade (fail-safe). The body is bounded
		// (io.LimitReader, parity with the upstreams/alert probes) to cap a
		// hostile/compromised pod's response (threat T-14-02).
		//
		// WR-01: the pod's :9100 responder binds asynchronously (nohup) right
		// before exec supervisord, so a single GET at pod-Ready can lose the
		// startup race and read "" → a GPU pod that CAN serve STT silently
		// routes to the costlier tier-1 cascade for its whole lifecycle. To
		// close that race we retry the fetch up to 3 times with a short backoff;
		// a retry only fires on a transient miss (empty/non-200/parse error),
		// NOT on a legitimate "cpu" value (which is a valid terminal answer).
		deviceReportOnce := func(dctx context.Context, u string) string {
			probeCtx, cancel := context.WithTimeout(dctx, 5*time.Second)
			defer cancel()
			req, rerr := http.NewRequestWithContext(probeCtx, http.MethodGet, u, nil)
			if rerr != nil {
				return ""
			}
			client := &http.Client{Timeout: 5 * time.Second}
			resp, herr := client.Do(req)
			if herr != nil {
				return ""
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return ""
			}
			var payload struct {
				WhisperDevice string `json:"whisper_device"`
			}
			// Bound the read (8 KiB is ample for the tiny device JSON) before
			// decoding so a hostile pod cannot stream an unbounded body.
			if derr := json.NewDecoder(io.LimitReader(resp.Body, 8*1024)).Decode(&payload); derr != nil {
				return ""
			}
			switch payload.WhisperDevice {
			case "cuda", "cpu":
				return payload.WhisperDevice
			default:
				return "" // whitelist / default-deny → fail-safe to gemini-stt
			}
		}
		primaryDeviceReport := func(dctx context.Context, u string) string {
			if u == "" {
				return ""
			}
			const deviceReportAttempts = 3
			const deviceReportBackoff = 500 * time.Millisecond
			for attempt := 0; attempt < deviceReportAttempts; attempt++ {
				if dev := deviceReportOnce(dctx, u); dev != "" {
					return dev
				}
				if attempt < deviceReportAttempts-1 {
					select {
					case <-dctx.Done():
						return ""
					case <-time.After(deviceReportBackoff):
					}
				}
			}
			return ""
		}

		primaryReconciler = primary.NewReconcilerFull(primary.Deps{
			Cfg:          cfg,
			Log:          log.With("subsys", "primary"),
			Vast:         primaryVastClient,
			HealthCheck:  primaryHealthCheck,
			Reachable:    primaryReachable,
			DeviceReport: primaryDeviceReport,
			Loader:       loader,       // *upstreams.Loader satisfies LoaderAdapter (3-role per Plan 06.6-06b)
			DCGMScraper:  dcgmScraper,  // *dcgm.Scraper satisfies DCGMScraperAdapter (SetURL per Plan 06.6-06b); nil-safe
			Inflight:     shedInflight, // *shed.InflightRegistry satisfies InflightAdapter (Count per Plan 06.6-06b)
			FSM:          primaryFSM,
			Rule:         primaryRule,
			DB:           pool,
			Redis:        rdb,
			ReplicaID:    hostnameOrUnknown(),
		})
		// Reviews #2 (2026-05-17): Start runs UNCONDITIONALLY — event
		// subscriber always launches; cfg.PrimaryPodScheduleDisabled gates
		// only schedule ticks inside runScheduleLoop, not Start() itself.
		// gatewayctl primary force-up works even under DISABLED=true
		// soak-gate posture.
		go primaryReconciler.Start(ctx)

		// Pitfall #11 — emerg subscriber to primary_ready events (Plan
		// 06.6-08 Task 1). Only spawned when emerg reconciler was wired
		// (i.e. both share VAST_AI_API_KEY). The leader-only filter inside
		// SubscribePrimaryEvents ensures non-leader replicas observe but
		// do NOT mutate emerg state.
		if emergReconciler != nil {
			go emergReconciler.SubscribePrimaryEvents(ctx)
		}

		nextUp, nextKind := primaryRule.NextTransition(time.Now())
		log.Info("Phase 6.6 primary reconciler started",
			"replica_id", primaryReconciler.ReplicaID(),
			"schedule_disabled", cfg.PrimaryPodScheduleDisabled,
			"next_transition", nextUp.Format(time.RFC3339),
			"next_kind", nextKind,
			"tz", cfg.PrimaryPodScheduleTimezone,
			"up_hour", cfg.PrimaryPodScheduleUpHour,
			"down_hour", cfg.PrimaryPodScheduleDownHour,
			"provision_lead_seconds", cfg.PrimaryPodScheduleProvisionLeadSeconds,
			"grace_ramp_down_seconds", cfg.PrimaryPodScheduleGraceRampDownSeconds,
			"coldstart_budget_seconds", cfg.PrimaryProvisionColdStartBudgetSeconds,
			"failure_cooldown_seconds", cfg.PrimaryProvisionFailureCooldownSeconds,
			// Phase 6.6.Y resolved primary-shape dump (fix-option-agnostic, locked
			// from 06.6.X-RESEARCH-ENV-PRECEDENCE). Surfaces BOTH shapes so the
			// operator can confirm resolved env precedence at boot.
			// shape0 = PRIMARY (1×3090 @ 0.30); shape1 = FALLBACK (2×3090 @ 0.60).
			"shape0_num_gpus", cfg.PrimaryVastNumGPUsPrimary,
			"shape0_gpu", cfg.PrimaryVastGPUNamePrimary,
			"shape0_cap", cfg.PrimaryVastPriceCapPrimary,
			"shape1_num_gpus", cfg.PrimaryVastNumGPUsFallback,
			"shape1_gpu", cfg.PrimaryVastGPUNameFallback,
			"shape1_cap", cfg.PrimaryVastPriceCapFallback,
			"allowlist", cfg.PrimaryVastMachineAllowlist,
			"port_bind_budget_seconds", cfg.PrimaryPublicPortBindBudgetSeconds,
			"reject_private_ip", cfg.PrimaryVastRejectPrivateIP,
			"monthly_budget_brl", cfg.MonthlyPrimaryBudgetBRL,
			"template_image", cfg.PrimaryTemplateImage, // Wave 0 SHA pin visibility for operator forensics
		)
	}
	// primaryReconciler is currently unused beyond Start — keep the
	// reference live so future plans (gatewayctl primary subcommand,
	// dispatcher integration) can wire it without re-introducing the
	// var declaration. Plan 06.6-09 (gatewayctl) will read this pointer.
	_ = primaryReconciler

	// emergTraffic is non-nil only when the Phase 6 reconciler is wired.
	// Plan 06-08 D-E3: the chat dispatcher calls RegisterTraffic on each
	// request that resolves to the emergency pod URL so the reconciler's
	// idle-grace destroy timer (D-D1, default 300s) sees recent activity.
	// Other roles (embed, audio) skip this — Phase 6 v1 only overrides
	// tier-0 LLM (D-E3 deferred ideas note).
	var emergTraffic proxy.EmergTrafficRegistrar
	if emergReconciler != nil {
		emergTraffic = emergReconciler
	}

	// Dispatchers — one per role. Each applies breaker-driven fallback
	// per the dispatcher decision tree (CONTEXT.md D-A2 + D-B1..B4 + D-C4).
	chatDispatcher := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:         "llm",
		Loader:       loader,
		Breaker:      breakerSet,
		TokenCounter: tokenCounter,
		ContextCap:   proxy.ChatContextCap,
		Proxies:      llmRoleProxies,
		EmergTraffic: emergTraffic,
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
	// Phase 06.7 — tts dispatcher. Same tier-0->tier-1 breaker fallback wrap as
	// chat/audio (NOT the raw single-upstream proxy): tier-0 = pod Chatterbox
	// (dynamic override), tier-1 = the GATE-3 Option A Piper adapter. TTS skips
	// token-cap enforcement (the body is synth text, not chat tokens).
	ttsDispatcher := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:         "tts",
		Loader:       loader,
		Breaker:      breakerSet,
		TokenCounter: nil,
		ContextCap:   0,
		Proxies:      ttsRoleProxies,
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
	// Phase 06.7 — TTS speech shares the audio write-timeout budget (long synth).
	ttsHandler := wrapWithTimeout(ttsDispatcher, cfg.WriteTimeoutAudioS)

	// Phase 06.7 — voice-clone CRUD handlers. The MinIO S3 client is built from
	// config.Minio* creds; if those are unset (scaffold/dev without MinIO) the
	// voices routes are disabled (nil handler -> buildRouter skips the mounts)
	// rather than crashing boot.
	var voiceHandlers *proxy.VoiceHandlers
	if cfg.MinioEndpoint != "" && cfg.MinioAccessKey != "" && cfg.MinioSecretKey != "" {
		objStore, oerr := proxy.NewMinioObjectStore(
			cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket)
		if oerr != nil {
			log.Warn("voices disabled: minio client build failed", "err", oerr)
		} else {
			voiceHandlers = &proxy.VoiceHandlers{
				Store:          gen.New(pool),
				Objects:        objStore,
				S3VoicePrefix:  cfg.S3VoicePrefix,
				MaxUploadBytes: cfg.VoiceMaxUploadBytes,
				Log:            log,
			}
		}
	} else {
		log.Warn("voices disabled: MinIO creds not configured (MINIO_ENDPOINT/ACCESS_KEY/SECRET_KEY)")
	}

	// Phase 4 — admin usage handler. Mounted under /admin by buildRouter;
	// the admin.Middleware (X-Admin-Key bcrypt) gates all /admin/* routes
	// per D-D3.
	adminUsageHandler := admin.NewUsageHandler(gen.New(pool), log)

	// Phase 7 — admin observability handlers (OBS-01/OBS-07). Mounted
	// under the SAME admin-key-gated /admin sub-router as adminUsageHandler.
	// MetricsHandler reads emergFSM.State() for the dashboard's FSM panel
	// (nil emergFSM when Phase 6 is disabled → handler reports "unknown",
	// it never panics). AuditHandler serves the paginated audit_log
	// state-change feed.
	adminMetricsHandler := admin.NewMetricsHandler(gen.New(pool), emergFSM, log)
	adminAuditHandler := admin.NewAuditHandler(gen.New(pool), log)

	startedAt := time.Now()
	r := buildRouter(log, startedAt, verifier, proxies{
		chat:                chatHandler,
		embed:               embedHandler,
		audio:               audioHandler,
		tts:                 ttsHandler,
		voices:              voiceHandlers,
		voicesMaxBytes:      cfg.VoiceMaxUploadBytes,
		auditWriter:         auditWriter,
		resolver:            resolver,
		upstreamsHealth:     upstreams.NewHealthHandler(loader, breakerSet, log),
		idemStore:           idemStore,
		tenantsLoader:       tenantsLoader,
		quotaChecker:        quotaChecker,
		adminUsageHandler:   adminUsageHandler,
		adminMetricsHandler: adminMetricsHandler,
		adminAuditHandler:   adminAuditHandler,
		adminVerifier:       adminVerifier,
		rdb:                 rdb,
		rateLimitFailOpen:   cfg.RateLimitFailOpen,
		quotaFailOpen:       cfg.QuotaFailOpen,
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

		// Phase 06.9: removed — per-upstream model rewrite now lives inside
		// each tier-1 Director (proxy/{openrouter,openai_whisper,openai_embed}
		// _director.go). The pre-06.9 models.Handler wraps ran at request edge
		// BEFORE dispatcher resolution, which collapsed all per-upstream
		// targets onto a single rewrite — incompatible with the per-upstream
		// resolver introduced in Plan 02. Directors call resolver.Resolve
		// AFTER dispatch with the compile-time-known upstream name.
		chatHandler := px.chat
		embedHandler := px.embed
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
		// Phase 06.7 — TTS speech (tier-0 pod Chatterbox -> tier-1 Piper adapter
		// dispatcher) + voice-clone CRUD. All mount on the SAME authed /v1/*
		// group so auth.MustFromContext is populated in the voice handlers
		// (D-10 tenant isolation). The upload route is wrapped with
		// http.MaxBytesHandler (VOICE_MAX_UPLOAD_BYTES) for the T-06.7-15 DoS cap.
		mount(http.MethodPost, "/v1/audio/speech", px.tts)
		if px.voices != nil {
			maxBytes := px.voicesMaxBytes
			if maxBytes <= 0 {
				maxBytes = 10485760
			}
			pg.Method(http.MethodPost, "/v1/audio/voices",
				http.MaxBytesHandler(http.HandlerFunc(px.voices.Create), maxBytes))
			pg.Method(http.MethodGet, "/v1/audio/voices", http.HandlerFunc(px.voices.List))
			pg.Method(http.MethodDelete, "/v1/audio/voices/{id}", http.HandlerFunc(px.voices.Delete))
		} else {
			pg.MethodFunc(http.MethodPost, "/v1/audio/voices", scaffoldNotImplemented)
			pg.MethodFunc(http.MethodGet, "/v1/audio/voices", scaffoldNotImplemented)
			pg.MethodFunc(http.MethodDelete, "/v1/audio/voices/{id}", scaffoldNotImplemented)
		}
		pg.MethodFunc(http.MethodGet, "/v1/audio/speech", scaffoldNotImplemented)
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
		// Phase 7 — observability dashboard data sources. Both gated by
		// the same admin.Middleware (X-Admin-Key bcrypt) as /usage. The
		// unauthenticated Prometheus /metrics at r.Handle above is a
		// DISTINCT endpoint and is deliberately left untouched.
		if px.adminMetricsHandler != nil {
			adminRouter.Method(http.MethodGet, "/metrics", px.adminMetricsHandler)
		}
		if px.adminAuditHandler != nil {
			adminRouter.Method(http.MethodGet, "/audit", px.adminAuditHandler)
		}
		// Phase 11 Plan 04 D-18.2 — operator-only synthetic panic emitter
		// used by `gatewayctl debug emit-error` to prove the
		// httpx.Recoverer + sentry.CurrentHub().Recover + sentry.Flush
		// path end-to-end in PROD. The route lives INSIDE the admin
		// sub-router so admin.Middleware (X-Admin-Key) is enforced before
		// the handler. httpx.Recoverer is applied globally at r.Use above
		// (line ~1152), so the effective wrap order is
		// Recoverer(adminMiddleware(DebugPanicHandler)) — Recoverer
		// outermost. An automated integration test in
		// gateway/internal/admin/debug_panic_test.go enforces both
		// invariants (unauth -> 401 AND auth -> 500 sanitized).
		adminRouter.Method(http.MethodPost, "/debug/panic",
			admin.DebugPanicHandler(log))
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
	usageInterceptor *proxy.UsageInterceptor,
	resolver *models.Resolver) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil {
		return nil, fmt.Errorf("parse openrouter url %q: %w", u.URL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid openrouter url %q (missing scheme or host)", u.URL)
	}
	rp := &httputil.ReverseProxy{
		// Phase 06.9 — pass resolver + "openrouter-chat" name + log so the
		// director rewrites body.model via per-upstream lookup (env-override-wins
		// per D-06 inherited transparently from Resolver.Resolve).
		Director: proxy.BuildOpenRouterDirector(parsed, u.AuthBearer,
			cfg.UpstreamOpenRouterProviderOrder, cfg.UpstreamOpenRouterAllowFallbacks,
			resolver, "openrouter-chat", log),
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
func buildOpenAIEmbedProxy(u upstreams.UpstreamConfig, log *slog.Logger, resolver *models.Resolver) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil {
		return nil, fmt.Errorf("parse openai-embed url %q: %w", u.URL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid openai-embed url %q", u.URL)
	}
	rp := &httputil.ReverseProxy{
		// Phase 06.9 — pass resolver + "openai-embed" name + log so the
		// director rewrites body.model via per-upstream lookup (was hard-coded
		// "text-embedding-3-small" pre-06.9). dimensions=1024 stays hard-coded
		// (BGE-M3 parity invariant).
		Director: proxy.BuildOpenAIEmbedDirector(parsed, u.AuthBearer, resolver, "openai-embed", log),
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

// buildGeminiSTTProxy constructs the gemini-stt tier-1 STT fallback proxy
// (Phase 11.2 D-B4). The director adapter translates OpenAI-shaped
// multipart requests into Gemini's `generateContent` JSON shape; the
// ModifyResponse flattens Gemini's envelope back into OpenAI's
// `{"text":"..."}` so downstream consumers see the same response.
//
// Both URL + API key come from env (UPSTREAM_STT_FALLBACK_1_URL,
// UPSTREAM_STT_FALLBACK_1_AUTH_BEARER) rather than the loader row to
// keep secrets out of the DB.
func buildGeminiSTTProxy(rawURL, apiKey string, resolver *models.Resolver, log *slog.Logger) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse gemini-stt url %q: %w", rawURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid gemini-stt url %q", rawURL)
	}
	director, modifyResponse := proxy.BuildGeminiSTTDirector(parsed, apiKey, resolver, "gemini-stt", log)
	rp := &httputil.ReverseProxy{
		Director:       director,
		ModifyResponse: modifyResponse,
		Transport: &http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		ErrorHandler: proxy.ErrorHandler("gemini-stt", log),
	}
	return rp, nil
}

// buildGroqWhisperProxy constructs the groq-whisper tier-1 STT fallback
// proxy (Phase 11.2 D-B8). Groq's `/openai/v1/audio/transcriptions` is
// OpenAI-compatible so this REUSES BuildOpenAIWhisperDirector verbatim —
// only URL + bearer differ. The director resolves the model via
// canonicalAliasForUpstream["groq-whisper"]="whisper" + upstreamEnvVarMap
// (UPSTREAM_STT_FALLBACK_2_MODEL).
func buildGroqWhisperProxy(u upstreams.UpstreamConfig, log *slog.Logger, resolver *models.Resolver) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil {
		return nil, fmt.Errorf("parse groq-whisper url %q: %w", u.URL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid groq-whisper url %q", u.URL)
	}
	rp := &httputil.ReverseProxy{
		Director: proxy.BuildOpenAIWhisperDirector(parsed, u.AuthBearer, resolver, "groq-whisper", log),
		Transport: &http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		ErrorHandler: proxy.ErrorHandler("groq-whisper", log),
	}
	return rp, nil
}

// buildOpenAIWhisperProxy constructs the openai-whisper fallback proxy.
// The director leaves the multipart body untouched (boundary preserved);
// only Authorization is added.
func buildOpenAIWhisperProxy(u upstreams.UpstreamConfig, log *slog.Logger, resolver *models.Resolver) (*httputil.ReverseProxy, error) {
	parsed, err := url.Parse(u.URL)
	if err != nil {
		return nil, fmt.Errorf("parse openai-whisper url %q: %w", u.URL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid openai-whisper url %q", u.URL)
	}
	rp := &httputil.ReverseProxy{
		// Phase 06.9 — pass resolver + "openai-whisper" name + log so the
		// director rewrites the multipart "model" form-field via per-upstream
		// lookup while preserving the audio file part byte-identical (R6).
		// The WhisperAbortGuard wrapper around this proxy handler (registered
		// below) catches duplicate-"model" multipart requests and returns
		// HTTP 400 to the client BEFORE the proxy runs (WARNING-3).
		Director: proxy.BuildOpenAIWhisperDirector(parsed, u.AuthBearer, resolver, "openai-whisper", log),
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
	// WR-07: refuse to bootstrap when the operator passed a key shorter
	// than 4 chars. The pre-fix fallback `suffix := bootstrap` would
	// concatenate the FULL plaintext key into key_prefix (stored in
	// ai_gateway.admin_keys AND emitted via log.Info), leaking the
	// entire key to the structured log sink. While a 1-3 char key is
	// already a deployment error, refuse-and-explain is the right defense.
	if len(bootstrap) < 4 {
		return fmt.Errorf("bootstrap admin key too short (got %d chars; need >= 16; the suffix-display path would otherwise leak the full plaintext into key_prefix)", len(bootstrap))
	}
	suffix := bootstrap[len(bootstrap)-4:]
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

// buildAlertChannels constructs the enabled alert delivery channels from
// config (OBS-04/05/06). Each channel is OPTIONAL: when its required
// config fields are unset, the channel is SKIPPED with a single WARN
// naming the missing env var — it NEVER fails boot (the SentryDSN
// precedent; see config.go Phase 7 fields). The returned slice may be
// empty; alert.NewAlerter handles that — the alerter still classifies,
// dedups, and logs every event, the external fan-out is just empty.
//
// The WARN logs the channel name + the missing env var NAME only —
// never a token value (threat T-07-23).
func buildAlertChannels(cfg config.Config, log *slog.Logger) []alert.Channel {
	var channels []alert.Channel

	// Chatwoot — critical-tier WhatsApp. Needs the API URL, the agent
	// token, and the on-call account/inbox/contact triple to address a
	// conversation.
	switch {
	case cfg.ChatwootAPIToken == "":
		log.Warn("chatwoot alert channel disabled — CHATWOOT_API_TOKEN unset")
	case cfg.ChatwootAPIURL == "":
		log.Warn("chatwoot alert channel disabled — CHATWOOT_API_URL unset")
	case cfg.ChatwootOncallAccountID == "":
		log.Warn("chatwoot alert channel disabled — CHATWOOT_ONCALL_ACCOUNT_ID unset")
	default:
		channels = append(channels, alert.NewChatwootClient(alert.ChatwootConfig{
			APIURL:    cfg.ChatwootAPIURL,
			APIToken:  cfg.ChatwootAPIToken,
			AccountID: cfg.ChatwootOncallAccountID,
			InboxID:   cfg.ChatwootOncallInboxID,
			ContactID: cfg.ChatwootOncallContactID,
		}))
		log.Info("chatwoot alert channel enabled")
	}

	// ClickUp — a task per critical/warning alert. Needs the static
	// personal token and the target list ID.
	switch {
	case cfg.ClickUpAPIToken == "":
		log.Warn("clickup alert channel disabled — CLICKUP_API_TOKEN unset")
	case cfg.ClickUpAlertListID == "":
		log.Warn("clickup alert channel disabled — CLICKUP_ALERT_LIST_ID unset")
	default:
		channels = append(channels, alert.NewClickUpClient(alert.ClickUpConfig{
			APIToken: cfg.ClickUpAPIToken,
			ListID:   cfg.ClickUpAlertListID,
		}))
		log.Info("clickup alert channel enabled")
	}

	// Brevo — critical+warning email via SMTP relay. Needs the relay
	// host, the SMTP credentials, and a from/to pair.
	switch {
	case cfg.BrevoSMTPHost == "":
		log.Warn("brevo alert channel disabled — BREVO_SMTP_HOST unset")
	case cfg.BrevoSMTPUser == "":
		log.Warn("brevo alert channel disabled — BREVO_SMTP_USER unset")
	case cfg.BrevoSMTPPass == "":
		log.Warn("brevo alert channel disabled — BREVO_SMTP_PASS unset")
	case cfg.AlertEmailFrom == "":
		log.Warn("brevo alert channel disabled — ALERT_EMAIL_FROM unset")
	case len(cfg.AlertEmailTo) == 0:
		log.Warn("brevo alert channel disabled — ALERT_EMAIL_TO unset")
	default:
		channels = append(channels, alert.NewBrevoClient(alert.BrevoConfig{
			Host: cfg.BrevoSMTPHost,
			Port: cfg.BrevoSMTPPort,
			User: cfg.BrevoSMTPUser,
			Pass: cfg.BrevoSMTPPass,
			From: cfg.AlertEmailFrom,
			To:   cfg.AlertEmailTo,
		}))
		log.Info("brevo alert channel enabled")
	}

	return channels
}

// hostnameOrUnknown returns os.Hostname() or "unknown" when the call
// fails. Used by the Phase 6 FSM onChange callback to stamp ReplicaID
// on EmergEvents — matches the convention emerg.Reconciler uses
// internally (NewReconciler) so Pub/Sub events from this main.go-level
// publisher and from the reconciler's own publishes share a consistent
// replica identifier.
func hostnameOrUnknown() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
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
