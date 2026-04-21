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

	// Build the LOCAL reverse proxies (Plan 02-04). Interceptors are passed
	// at construction time per Codex review [HIGH/MEDIUM] 02-05 decoupling.
	// Chat gets the audit interceptor (SSE streaming) AND the tool-call
	// interceptor (Phase 3 RES-06 / SC-4 — emit terminal SSE error event
	// on disconnect after tool_calls); embeddings + audio never stream so
	// their non-SSE bodies are captured by audit.Middleware directly.
	toolCallInterceptor := proxy.NewToolCallInterceptor()
	chatRP, err := proxy.NewChatProxy(cfg.UpstreamLLMURL, log, auditInterceptor, toolCallInterceptor)
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
		orChatProxy, perr := buildOpenRouterChatProxy(u, cfg, log, auditInterceptor, toolCallInterceptor)
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

	// Per-route WriteTimeout binding (Folded Todo from Phase 2; D-A1
	// Plumbing). Chat=0 (SSE — unlimited), embed=30s, audio=120s. Wraps
	// each dispatcher in http.TimeoutHandler so slow-client-DoS defense
	// applies to non-streaming routes without breaking SSE.
	var chatHandler http.Handler = chatDispatcher
	if cfg.WriteTimeoutChat > 0 {
		chatHandler = http.TimeoutHandler(chatDispatcher, cfg.WriteTimeoutChat, "request timeout")
	}
	var embedHandler http.Handler = embedDispatcher
	if cfg.WriteTimeoutEmbed > 0 {
		embedHandler = http.TimeoutHandler(embedDispatcher, cfg.WriteTimeoutEmbed, "request timeout")
	}
	var audioHandler http.Handler = audioDispatcher
	if cfg.WriteTimeoutAudio > 0 {
		audioHandler = http.TimeoutHandler(audioDispatcher, cfg.WriteTimeoutAudio, "request timeout")
	}

	startedAt := time.Now()
	r := buildRouter(log, startedAt, verifier, proxies{
		chat:            chatHandler,
		embed:           embedHandler,
		audio:           audioHandler,
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

// buildOpenRouterChatProxy constructs a *httputil.ReverseProxy for the
// openrouter-chat fallback upstream. Uses BuildOpenRouterDirector to
// inject Authorization Bearer + body rewrite with provider.order +
// allow_fallbacks per CONTEXT.md D-C2 (Novita pin per D-C1 amendment).
//
// Wired with the same audit + tool-call interceptors as the local chat
// proxy so SSE streams from OpenRouter get the same observability +
// tool-call protection.
func buildOpenRouterChatProxy(u upstreams.UpstreamConfig, cfg config.Config, log *slog.Logger,
	auditInterceptor *audit.AuditInterceptor, toolCallInterceptor *proxy.ToolCallInterceptor) (*httputil.ReverseProxy, error) {
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
		ModifyResponse: proxy.ComposeInterceptors(auditInterceptor, toolCallInterceptor),
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
