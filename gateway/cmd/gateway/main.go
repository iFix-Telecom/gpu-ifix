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
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
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

	// Build the three reverse proxies (Plan 02-04). Interceptors are passed
	// at construction time per Codex review [HIGH/MEDIUM] 02-05 decoupling.
	// Chat gets the audit interceptor (SSE streaming); embeddings + audio
	// never stream so their non-SSE bodies are captured by audit.Middleware
	// directly.
	chatRP, err := proxy.NewChatProxy(cfg.UpstreamLLMURL, log, auditInterceptor)
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
	go func() {
		// Hot-reload pipeline: NOTIFY → loader.Refresh → breaker.Rebuild
		// so newly-added upstreams get fresh CLOSED breakers and
		// removed upstreams have their breakers dropped.
		if err := upstreams.ListenAndReload(ctx, cfg.PGDSN, loader, func() {
			breakerSet.Rebuild(loader.Names())
		}, log); err != nil {
			log.Warn("upstreams listener exited", "err", err)
		}
	}()

	startedAt := time.Now()
	r := buildRouter(log, startedAt, verifier, proxies{
		chat:            chatRP,
		embed:           embedRP,
		audio:           audioRP,
		auditWriter:     auditWriter,
		resolver:        resolver,
		upstreamsHealth: upstreams.NewHealthHandler(loader, breakerSet, log),
		idemStore:       idemStore,
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

	// Authenticated /v1/* group.
	r.Group(func(pg chi.Router) {
		if verifier != nil {
			pg.Use(auth.Middleware(verifier, log))
		}
		if px.auditWriter != nil {
			pg.Use(audit.Middleware(px.auditWriter, log))
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
