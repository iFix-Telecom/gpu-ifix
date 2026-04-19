# Phase 3: Resilience & Fallback Chain - Pattern Map

**Mapped:** 2026-04-19
**Files analyzed:** 27 new/modified
**Analogs found:** 26 / 27 (one has no in-repo analog — breaker Pub/Sub subscriber)

Conventions observed project-wide (Phase 2 baseline) — every Phase 3 file inherits these unless noted otherwise:

- `gofmt -w .` / `go vet ./...` / `golangci-lint run` clean.
- Package comment on first file of each package, starting with `// Package <name>` (see `gateway/internal/proxy/director.go:1-12`, `gateway/internal/audit/writer.go:1-6`, `gateway/internal/config/config.go:1-5`).
- `log/slog` with `module=UPPER_SNAKE_CASE` attribute attached via `log.With("module", "NAME")` (see `gateway/internal/audit/writer.go:83`, `gateway/internal/models/resolver.go:48`, `gateway/internal/proxy/errors.go:18`, `gateway/internal/idempotency/middleware.go:43`).
- Sentinel errors as package-level `var`s, one per file named `errors.go` (see `gateway/internal/proxy/errors.go:13`, `gateway/internal/auth/errors.go:10-27`, `gateway/internal/idempotency/errors.go:10-20`, `gateway/internal/config/config.go:56`).
- OpenAI error envelope via `httpx.WriteOpenAIError(w, status, errType, code, msg)` from `gateway/internal/httpx/envelope.go:16-22` — never build an error response by hand.
- `gateway/internal/httpx/requestid.RequestIDFrom(ctx)` for request-id correlation in logs (used by `gateway/internal/proxy/errors.go:22`, `gateway/internal/upstreams/health.go:57`, `gateway/internal/audit/middleware.go:48`).
- sqlc `v1.30.0`; queries land in `gateway/db/queries/*.sql`; code generates into `gateway/internal/db/gen/` with `sql_package: pgx/v5`, `emit_json_tags: true`, `emit_interface: true`, overrides `uuid→google/uuid.UUID`, `timestamptz→time.Time` (see `gateway/sqlc.yaml`).
- goose migrations numbered sequentially `NNNN_<description>.sql` in `gateway/db/migrations/`, always wrapped in `-- +goose Up` / `-- +goose StatementBegin` / `-- +goose StatementEnd` / `-- +goose Down` and `SET search_path = ai_gateway, public;` at the top (see `gateway/db/migrations/0003_create_audit_log_partitioned.sql`).
- Integration tests use `//go:build integration` build tag, package `integration`, shared testcontainers setup in `gateway/internal/integration_test/setup_test.go` (`freshSchema(t, ctx)`, `discardLogger()`, `seedTenant`, `seedTenantAndKey` helpers).
- Tests co-located as `*_test.go`; compile-time interface checks use `var _ Interface = (*Impl)(nil)` (see `gateway/internal/audit/interceptor.go:62`).
- Prometheus collectors registered via `promauto.NewCounterVec`/`promauto.NewCounter` in `gateway/internal/obs/metrics.go`; Sentry init in `gateway/internal/obs/sentry.go` — Phase 3 extends both files.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `gateway/internal/breaker/breaker.go` | infra-wrapper | event-driven | `gateway/internal/models/resolver.go` | role-match (lifecycle constructor + RWMutex state) |
| `gateway/internal/breaker/mirror.go` | service | pub-sub | `gateway/internal/auth/cache.go` | role-match (Redis helpers + SHA-256 key namespace) |
| `gateway/internal/breaker/subscribe.go` | service | pub-sub | *no close analog in repo* | N/A — new capability (go-redis v9 Pub/Sub) |
| `gateway/internal/breaker/errors.go` | sentinel-errors | N/A | `gateway/internal/proxy/errors.go` + `gateway/internal/auth/errors.go` | exact |
| `gateway/internal/breaker/breaker_test.go` | test | N/A | `gateway/internal/auth/cache_test.go` + `gateway/internal/audit/writer_test.go` | exact |
| `gateway/internal/upstreams/loader.go` | service | CRUD | `gateway/internal/models/resolver.go` | exact (atomic pointer swap + RWMutex) |
| `gateway/internal/upstreams/listen.go` | service | event-driven | *no in-repo analog; Phase 3 introduces pgxlisten* | N/A — partial analog: goroutine pattern from `gateway/internal/audit/writer.go:128` |
| `gateway/internal/upstreams/probe.go` | service | batch | `gateway/internal/upstreams/health.go` (HTTP probe) + `gateway/internal/auth/touch_buffer.go` (batched UPDATE) | role-match |
| `gateway/internal/upstreams/health.go` (refactor) | handler | request-response | current `gateway/internal/upstreams/health.go` | exact (refactor in place) |
| `gateway/internal/upstreams/types.go` | types | N/A | `gateway/internal/idempotency/store.go` (Entry + SlotKind) | role-match |
| `gateway/internal/proxy/dispatcher.go` | service | request-response | `gateway/internal/models/rewrite.go` Handler + `gateway/internal/idempotency/middleware.go` | role-match (pre-dispatch guard chain) |
| `gateway/internal/proxy/openrouter_director.go` | director | transform | `gateway/internal/proxy/director.go` + `gateway/internal/models/rewrite.go` | exact |
| `gateway/internal/proxy/openai_embed_director.go` | director | transform | `gateway/internal/proxy/director.go` + `gateway/internal/models/rewrite.go` | exact |
| `gateway/internal/proxy/openai_whisper_director.go` | director | transform | `gateway/internal/proxy/director.go` | role-match |
| `gateway/internal/proxy/sensitive.go` | utility | batch | `gateway/internal/idempotency/store.go` `WaitForComplete` | exact (poll-loop w/ backoff + context) |
| `gateway/internal/proxy/streaming.go` | utility | streaming | `gateway/internal/proxy/chat.go` + `gateway/internal/proxy/errors.go` | role-match |
| `gateway/internal/proxy/toolcall.go` | interceptor | streaming | `gateway/internal/audit/interceptor.go` + `gateway/internal/audit/tee.go` | exact (ProxyResponseInterceptor contract) |
| `gateway/internal/proxy/tokencount.go` | utility | transform | `gateway/internal/idempotency/hash.go` + `gateway/internal/auth/cache.go` | role-match (Redis cache w/ TTL + sha256 key) |
| `gateway/internal/audit/` (extend) | types | N/A | `gateway/internal/audit/middleware.go` `upstreamForRoute` | exact (add `blocked_sensitive` const) |
| `gateway/internal/redisx/` (helpers added) | utility | pub-sub + CRUD | `gateway/internal/redisx/client.go` | exact (same package; append helpers) |
| `gateway/internal/config/config.go` (extend) | config | N/A | current `gateway/internal/config/config.go` | exact |
| `gateway/db/migrations/0007_create_upstreams.sql` | migration | N/A | `gateway/db/migrations/0005_create_model_aliases.sql` | exact |
| `gateway/db/migrations/0008_seed_upstreams.sql` | migration | N/A | `gateway/db/migrations/0001_create_tenants.sql` (INSERT … ON CONFLICT DO NOTHING seed pattern) | exact |
| `gateway/db/migrations/0009_upstreams_notify_trigger.sql` | migration | N/A | `gateway/db/migrations/0003_create_audit_log_partitioned.sql` (PL/pgSQL DO block inside StatementBegin) | role-match |
| `gateway/db/migrations/0010_audit_log_blocked_sensitive_enum.sql` | migration | N/A | `gateway/db/migrations/0002_create_api_keys.sql` (ENUM DDL) | role-match — note `audit_log.upstream` is TEXT (not ENUM); this migration may be a no-op comment |
| `gateway/db/queries/upstreams.sql` | sqlc-query | CRUD | `gateway/db/queries/admin.sql` + `gateway/db/queries/model_aliases.sql` + `gateway/db/queries/auth.sql` (TouchKeyLastUsed UPDATE) | exact |
| `gateway/cmd/gatewayctl/upstreams.go` | cli-subcommand | CRUD | `gateway/cmd/gatewayctl/tenant.go` + `gateway/cmd/gatewayctl/key.go` | exact |
| `gateway/internal/integration_test/breaker_state_machine_test.go` | test | N/A | `gateway/internal/integration_test/upstream_e2e_test.go` + `gateway/internal/integration_test/audit_write_test.go` | exact |
| `gateway/internal/integration_test/fallback_routing_test.go` | test | N/A | same | exact |
| `gateway/internal/integration_test/sensitive_block_test.go` | test | N/A | same | exact |
| `gateway/internal/integration_test/hot_reload_test.go` | test | N/A | same (plus new pgxlisten harness) | role-match |

## Pattern Assignments

### `gateway/internal/breaker/breaker.go` (per-upstream gobreaker v2 wrapper)

**Analog:** `gateway/internal/models/resolver.go` (constructor + long-lived state + RWMutex + logger w/ module attr)

**Package comment + imports** — copy pattern from `gateway/internal/models/resolver.go:1-16`:
```go
// Package breaker wraps sony/gobreaker/v2 circuit breakers per upstream,
// publishes state transitions to the Redis mirror hash, and subscribes to
// peer replicas' transitions for cross-replica convergence (CONTEXT.md
// D-D1). Authoritative state is the in-process *gobreaker.CircuitBreaker;
// Redis is a mirror, never the source of truth.
package breaker

import (
    "context"
    "log/slog"
    "sync"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/sony/gobreaker/v2"
)
```

**Constructor + logger With("module", ...)** — copy from `gateway/internal/models/resolver.go:44-51`:
```go
// NewSet constructs the per-upstream breaker map. Call once at boot with
// the list of enabled upstream names; hot-reload via Rebuild on LISTEN.
func NewSet(rdb *redis.Client, log *slog.Logger, names []string) *Set {
    s := &Set{
        rdb: rdb,
        log: log.With("module", "BREAKER"),
        cbs: make(map[string]*gobreaker.CircuitBreaker[*http.Response], len(names)),
    }
    for _, n := range names {
        s.cbs[n] = newBreaker(n, rdb, s.log)
    }
    return s
}
```

**gobreaker Settings (see RESEARCH.md §Pattern 1) + OnStateChange + IsSuccessful:**
```go
// Settings per CONTEXT.md D-A3: ConsecutiveFailures>=3 → OPEN, 30s cooldown,
// 1 success in HALF_OPEN → CLOSED. IsSuccessful filters per D-A4: 4xx and
// ctx.Canceled are NOT failures; 5xx, timeouts, and probe failures ARE.
func newBreaker(name string, rdb *redis.Client, log *slog.Logger) *gobreaker.CircuitBreaker[*http.Response] {
    return gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
        Name:        name,
        MaxRequests: 1,
        Interval:    0,
        Timeout:     30 * time.Second,
        ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= 3 },
        OnStateChange: func(name string, from, to gobreaker.State) {
            log.Info("breaker state change",
                "upstream", name,
                "from", from.String(),
                "to", to.String(),
                "at", time.Now().Format(time.RFC3339))
            // Redis publish is best-effort — do NOT block the state machine.
            go publishBreakerState(rdb, name, to)
        },
        IsSuccessful: isSuccessful,
    })
}
```

**Concurrent state — use same RWMutex pattern as resolver** (`gateway/internal/models/resolver.go:37-42, 55-69`):
```go
type Set struct {
    mu  sync.RWMutex
    cbs map[string]*gobreaker.CircuitBreaker[*http.Response]
    // ...
}
func (s *Set) Get(name string) (*gobreaker.CircuitBreaker[*http.Response], bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    cb, ok := s.cbs[name]
    return cb, ok
}
```

### `gateway/internal/breaker/mirror.go` (Redis HSet + Pub/Sub publisher)

**Analog:** `gateway/internal/auth/cache.go` (Redis key namespace + JSON payload + 60s TTL pattern)

**Key namespace (copy style from `gateway/internal/auth/cache.go:42-45, 50-53`):**
```go
// Key namespace conforms to the gateway-wide `gw:*` prefix (CONTEXT.md
// Integration Points). gw:breaker:{name} = Hash; gw:breaker:events = Pub/Sub.
func stateKey(name string) string { return "gw:breaker:" + name }

const eventsChannel = "gw:breaker:events"
```

**HSet + Publish (new helpers; add to redisx package OR wrap here):**
```go
// publishBreakerState mirrors the transition to Redis via HSET + PUBLISH.
// Failures are logged + counted in gateway_breaker_mirror_failures_total
// (D-D1 — breakers continue operating in-process if Redis is unreachable).
func publishBreakerState(rdb *redis.Client, name string, to gobreaker.State) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    now := time.Now().Unix()
    if err := rdb.HSet(ctx, stateKey(name), map[string]any{
        "state":             to.String(),
        "since_unix":        now,
        "trip_count":        0, // increment elsewhere
        "last_failure_code": "",
    }).Err(); err != nil {
        obs.BreakerMirrorFailuresTotal.Inc()
        return
    }
    payload, _ := json.Marshal(map[string]any{
        "upstream": name, "state": to.String(), "since": now,
    })
    _ = rdb.Publish(ctx, eventsChannel, payload).Err()
}
```

### `gateway/internal/breaker/subscribe.go` (Pub/Sub subscriber for cross-replica convergence)

**No close analog** in Phase 2 — go-redis v9 Pub/Sub is new for Phase 3. Use pattern from `gateway/internal/audit/writer.go:128-169` (long-lived goroutine with ctx cancel + `for select`):

```go
// Subscribe listens on gw:breaker:events and synthetically fires Fail/
// Succeed on local breakers so replicas converge. Exits on ctx cancel.
func (s *Set) Subscribe(ctx context.Context) {
    log := s.log.With("subsystem", "subscribe")
    for {
        if ctx.Err() != nil { return }
        ps := s.rdb.Subscribe(ctx, eventsChannel)
        ch := ps.Channel()
        for {
            select {
            case <-ctx.Done():
                _ = ps.Close()
                return
            case msg, ok := <-ch:
                if !ok { goto reconnect }
                s.applyRemoteEvent(msg.Payload)
            }
        }
    reconnect:
        log.Warn("pubsub channel closed; reconnecting")
        time.Sleep(1 * time.Second)
    }
}
```

### `gateway/internal/breaker/errors.go` (sentinel errors)

**Analog:** `gateway/internal/proxy/errors.go:1-13` + `gateway/internal/auth/errors.go:10-27` + `gateway/internal/idempotency/errors.go:10-20`

**Exact pattern (copy from `gateway/internal/idempotency/errors.go:10-20`):**
```go
package breaker

import "errors"

var (
    // ErrBreakerOpen — primary upstream's breaker is OPEN. Dispatcher should
    // route to tier-1 (D-A1) or enter sensitive retry loop (D-B1).
    ErrBreakerOpen = errors.New("breaker: circuit open")
    // ErrUpstreamUnavailable — all tiers exhausted (tier-0 + tier-1 OPEN).
    // Surfaces as 503 with OpenAI envelope code "upstream_unavailable".
    ErrUpstreamUnavailable = errors.New("breaker: all upstreams unavailable")
)
```

### `gateway/internal/upstreams/loader.go` (DB load + atomic swap)

**Analog:** `gateway/internal/models/resolver.go:53-69` (exact match — atomic map swap behind RWMutex after sqlc SELECT).

**Core pattern (copy from `gateway/internal/models/resolver.go:53-69`):**
```go
// Refresh loads all enabled upstreams into a fresh map and atomically
// swaps it in. Called from NewLoader at boot and from listen.go on NOTIFY.
func (l *Loader) Refresh(ctx context.Context) error {
    rows, err := l.q.ListEnabledUpstreams(ctx)
    if err != nil {
        return err
    }
    fresh := make(map[string]UpstreamConfig, len(rows))
    byRoleTier := make(map[roleTier]string, len(rows))
    for _, row := range rows {
        cfg := upstreamConfigFromRow(row)
        fresh[row.Name] = cfg
        byRoleTier[roleTier{Role: row.Role, Tier: int(row.Tier)}] = row.Name
    }
    l.mu.Lock()
    l.byName = fresh
    l.byRoleTier = byRoleTier
    l.mu.Unlock()
    l.log.Info("upstreams refreshed", "count", len(fresh))
    return nil
}
```

**Resolve helper** — copy from `gateway/internal/models/resolver.go:91-102`:
```go
// Resolve returns the UpstreamConfig for (role, tier). Missing rows return
// (UpstreamConfig{}, false) so the dispatcher can 503 with a precise error.
func (l *Loader) Resolve(role string, tier int) (UpstreamConfig, bool) {
    l.mu.RLock()
    defer l.mu.RUnlock()
    name, ok := l.byRoleTier[roleTier{Role: role, Tier: tier}]
    if !ok { return UpstreamConfig{}, false }
    cfg, ok := l.byName[name]
    return cfg, ok
}
```

### `gateway/internal/upstreams/listen.go` (Postgres LISTEN/NOTIFY)

**Analog:** long-lived goroutine pattern from `gateway/internal/audit/writer.go:128-169` (Run method with `for select { case <-ctx.Done(): ...}`) + `gateway/internal/models/resolver.go:74-89` (Start spawn pattern).

**pgxlisten is new to the codebase** — use library directly per RESEARCH.md §Pattern 4. Dedicated conn OUT of pgxpool:
```go
// ListenAndReload opens a dedicated pgx.Conn (NOT from pgxpool — pgx recommends
// a dedicated conn for LISTEN because notifications block the Rx side), calls
// LISTEN upstreams_changed, and triggers loader.Refresh on each NOTIFY. Exits
// on ctx cancel. Reconnects with 1s backoff if the conn drops.
func ListenAndReload(ctx context.Context, dsn string, loader *Loader, log *slog.Logger) error {
    log = log.With("module", "LISTEN")
    // listener setup uses github.com/jackc/pgxlisten — see RESEARCH.md §Pattern 4
}
```

**Context-cancel + drain idiom** (copy from `gateway/internal/audit/writer.go:143-169`):
```go
for {
    select {
    case <-ctx.Done():
        log.Info("listen loop exiting")
        return ctx.Err()
    // ...
    }
}
```

### `gateway/internal/upstreams/probe.go` (errgroup parallel probe)

**Analog:** `gateway/internal/upstreams/health.go:32-86` (HTTP client + timeout pattern) + `gateway/internal/auth/touch_buffer.go` (batched UPDATE pattern).

**Ticker + errgroup pattern** — use `errgroup.Group{}` (zero-value, NOT `errgroup.WithContext`) per RESEARCH.md §Pattern 5:
```go
// ProbeLoop runs a 10s ticker. Each tick dispatches synthetic E2E probes
// to all upstreams in parallel via a zero-value errgroup.Group (no cascade
// cancel — sibling probes continue on individual failures). Shared 5s
// deadline via context.WithTimeout. Results: cb.Succeed()/Fail() + enqueue
// to UPDATE batch channel.
func (p *Probe) ProbeLoop(ctx context.Context) {
    log := p.log.With("module", "PROBE")
    tick := time.NewTicker(10 * time.Second)
    defer tick.Stop()
    for {
        select {
        case <-ctx.Done():
            log.Info("probe loop exiting")
            return
        case <-tick.C:
            p.probeOnce(ctx)
        }
    }
}
```

**HTTP client + timeout** — copy shape from `gateway/internal/upstreams/health.go:32-33`:
```go
client := &http.Client{Timeout: probeBudget + 500*time.Millisecond}
// ...
ctx, cancel := context.WithTimeout(r.Context(), probeBudget)
defer cancel()
```

**Batched UPDATE channel** — follow `gateway/internal/audit/writer.go:22-27, 102-120` (buffered chan + Enqueue non-blocking):
```go
const (
    probeUpdateBufferSize = 100
    probeUpdateFlushIval  = 1 * time.Second
)
// BatchUpdates flushes last_probe_* rows via sqlc UpdateUpstreamProbe in a
// 1s tick. Non-blocking enqueue on probe hot path.
```

### `gateway/internal/upstreams/health.go` (refactor to multi-upstream payload)

**Analog:** **current** `gateway/internal/upstreams/health.go` (in-place refactor).

**Keep the 5s cache pattern (`gateway/internal/upstreams/health.go:19-50`)**. Replace bridge-proxy logic with in-memory reads from `*breaker.Set` + `*upstreams.Loader`:

```go
// Current cache idiom preserved:
mu.Lock()
if time.Since(cache.storedAt) < cacheTTL && cache.body != nil {
    b, s := cache.body, cache.status
    mu.Unlock()
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(s)
    _, _ = w.Write(b)
    return
}
mu.Unlock()
```

Reduce cacheTTL per "Claude's Discretion" to **2s** (CONTEXT.md). Build JSON from `loader.All()` + `breakerSet.Snapshot()`; derive `status: ok|degraded|failed` per CONTEXT.md D-D1 plumbing.

### `gateway/internal/upstreams/types.go` (UpstreamConfig + CircuitConfig structs)

**Analog:** `gateway/internal/idempotency/store.go:54-98` (Entry + SlotKind + Slot pattern — struct + iota enum + JSON tags).

**Struct + iota + JSON tags** — copy style from `gateway/internal/idempotency/store.go:82-98`:
```go
// UpstreamConfig is one row of ai_gateway.upstreams, resolved to live
// runtime values (URL + auth bearer pulled from env vars).
type UpstreamConfig struct {
    ID            uuid.UUID `json:"id"`
    Name          string    `json:"name"`
    Role          string    `json:"role"`   // "llm" | "stt" | "embed"
    Tier          int       `json:"tier"`   // 0 = primary, 1 = fallback
    URL           string    `json:"url"`
    AuthBearer    string    `json:"-"`      // resolved os.Getenv(auth_bearer_env); NEVER log or serialize
    AuthBearerEnv string    `json:"auth_bearer_env,omitempty"` // name only
    Enabled       bool      `json:"enabled"`
    CircuitConfig CircuitConfig `json:"circuit_config"`
}
```

### `gateway/internal/proxy/dispatcher.go` (multi-upstream tier-aware dispatcher)

**Analog:** `gateway/internal/idempotency/middleware.go:40-100` (middleware chain — auth ctx read + body read + pre-dispatch decision + handoff) + `gateway/internal/models/rewrite.go:60-77` (body rewrite + reinject + Content-Length fix).

**Pre-dispatch guard** — copy structure from `gateway/internal/idempotency/middleware.go:42-85`:
```go
// Dispatcher selects the tier-0 upstream if its breaker is CLOSED; otherwise
// tier-1 (for normal tenants) OR sensitive retry loop (for data_class=sensitive)
// per D-A1/D-B1. Pre-flight token count runs here per RES-07.
func Dispatcher(loader *upstreams.Loader, bs *breaker.Set, role string, proxies map[string]http.Handler, log *slog.Logger) http.Handler {
    log = log.With("module", "DISPATCHER", "role", role)
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ac, _ := auth.FromContext(r.Context())
        // ... token-count guard (tokencount.go) ...
        // ... tier-0 lookup + breaker state read ...
        // ... fallback to tier-1 OR sensitive retry OR 503 ...
    })
}
```

**Reading + restoring body** — copy from `gateway/internal/models/rewrite.go:67-76`:
```go
body, err := io.ReadAll(r.Body)
// rewrite...
r.Body = io.NopCloser(bytes.NewReader(rewritten))
r.ContentLength = int64(len(rewritten))
r.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
```

**Error responses** — always via `httpx.WriteOpenAIError` (see `gateway/internal/httpx/envelope.go:16-22`).

### `gateway/internal/proxy/openrouter_director.go` (body rewrite + auth bearer)

**Analog:** `gateway/internal/proxy/director.go:35-58` (Director shape + header stripping) + `gateway/internal/models/rewrite.go:21-53` (JSON body rewrite preserving field order).

**Extended Director** (new file — wraps `BuildDirector` with body rewrite):
```go
// BuildOpenRouterDirector returns a Director for the openrouter-chat upstream.
// Extends proxy.BuildDirector by (1) injecting Authorization: Bearer header
// from the resolved auth_bearer_env, (2) rewriting the request body to add
// {"provider":{"order":[...],"allow_fallbacks":false}} per D-C2.
func BuildOpenRouterDirector(upstream *url.URL, authBearer string, providerOrder []string, allowFallbacks bool) func(*http.Request) {
    base := BuildDirector(upstream)
    return func(r *http.Request) {
        base(r) // strips client auth, sets X-Request-ID, rewrites URL
        if authBearer != "" {
            r.Header.Set("Authorization", "Bearer "+authBearer)
        }
        // body rewrite — follow gateway/internal/models/rewrite.go:25-52 shape
        if r.Body != nil {
            body, _ := io.ReadAll(r.Body)
            rewritten, _ := injectProvider(body, providerOrder, allowFallbacks)
            r.Body = io.NopCloser(bytes.NewReader(rewritten))
            r.ContentLength = int64(len(rewritten))
            r.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
        }
    }
}
```

**JSON body rewrite** — copy from `gateway/internal/models/rewrite.go:21-53` (unmarshal into `map[string]json.RawMessage` to preserve unknown fields byte-for-byte).

### `gateway/internal/proxy/openai_embed_director.go` + `openai_whisper_director.go`

**Analog:** same as `openrouter_director.go`. Shape is identical; body rewrite differs per upstream:
- **embed Director:** swap `model` via `models.RewriteJSONModel` (reuse existing helper at `gateway/internal/models/rewrite.go:21-53`); inject `{"dimensions":1024}` via the same `map[string]json.RawMessage` pattern.
- **whisper Director:** no body rewrite (multipart stays untouched, per `gateway/internal/proxy/audio.go:12-19` comment); only header manipulation (auth bearer + `X-Request-ID`).

### `gateway/internal/proxy/sensitive.go` (3-attempt retry loop with breaker re-check)

**Analog:** `gateway/internal/idempotency/store.go:168-199` `WaitForComplete` (poll-loop with deadline + ctx cancel + per-iter sleep).

**Poll loop with backoff** — copy structure from `gateway/internal/idempotency/store.go:168-199`:
```go
// SensitiveRetry is the 3× exp-backoff loop for data_class=sensitive requests
// when the primary upstream's breaker is OPEN. Attempts: +200ms, +800ms, +3s.
// Re-reads breaker state from Redis mirror between attempts. Total budget
// ~4s (CONTEXT.md D-B1). Returns (closed, nil) when breaker closed during
// the loop; (false, ErrSensitiveRetryExhausted) after 3 attempts.
func SensitiveRetry(ctx context.Context, bs *breaker.Set, upstreamName string) (bool, error) {
    delays := []time.Duration{200 * time.Millisecond, 800 * time.Millisecond, 3 * time.Second}
    for _, d := range delays {
        select {
        case <-ctx.Done(): return false, ctx.Err()
        case <-time.After(d):
        }
        if cb, ok := bs.Get(upstreamName); ok {
            if cb.State() == gobreaker.StateClosed {
                return true, nil
            }
        }
    }
    return false, ErrSensitiveRetryExhausted
}
```

### `gateway/internal/proxy/streaming.go` (pre-flight + fail-fast)

**Analog:** `gateway/internal/proxy/chat.go:30-45` (Transport + FlushInterval: -1 + ErrorHandler) + `gateway/internal/proxy/errors.go:17-30` (ErrorHandler envelope).

**Pre-flight check** — copy ErrorHandler shape from `gateway/internal/proxy/errors.go:17-30`:
```go
// preFlightStreamGuard rejects streaming requests when the primary breaker
// is OPEN (D-B4 for sensitive; D-A1 for normal). Called BEFORE the proxy
// writes response headers — after that, Go stdlib's ErrorHandler is not
// invoked (see RESEARCH.md §D-A1 streaming semantics).
```

### `gateway/internal/proxy/toolcall.go` (ModifyResponse interceptor)

**Analog:** `gateway/internal/audit/interceptor.go:22-62` (ProxyResponseInterceptor implementation) + `gateway/internal/audit/tee.go:1-89` (TeeBody wrapping resp.Body + onClose).

**Exact implementation pattern** — extend `proxy.ProxyResponseInterceptor` (`gateway/internal/proxy/interceptor.go:17-19`):
```go
// ToolCallInterceptor implements proxy.ProxyResponseInterceptor. Tees SSE
// response chunks, scans for choices[0].delta.tool_calls; on upstream disconnect
// with a detected tool call, emits a terminal SSE error event per RES-06.
type ToolCallInterceptor struct {
    log  logWarner
    // ...
}

func (t *ToolCallInterceptor) Intercept(resp *http.Response) error {
    if !proxy.IsSSEResponse(resp) { return nil }
    resp.Body = newToolCallTee(resp.Body, t.onDetect, t.onDisconnect)
    return nil
}

var _ proxy.ProxyResponseInterceptor = (*ToolCallInterceptor)(nil)
```

**TeeBody extension** — copy structure from `gateway/internal/audit/tee.go:19-89` (synchronous tee — no goroutines; mu-protected; onClose fires exactly once).

### `gateway/internal/proxy/tokencount.go` (llama.cpp /tokenize + Redis cache)

**Analog:** `gateway/internal/idempotency/hash.go` (sha256 body hashing) + `gateway/internal/auth/cache.go:42-85` (Redis cache key namespace + SHA-256 key + TTL + JSON entry).

**Redis key + TTL** — copy pattern from `gateway/internal/auth/cache.go:42-45`:
```go
// tokenCacheKey namespaces under gw:tokenize:. Key is sha256(body)+model.
const tokenCacheTTL = 60 * time.Second
func tokenCacheKey(bodyHash string, model string) string {
    return "gw:tokenize:" + model + ":" + bodyHash
}
```

**Cache get/set** — copy pattern from `gateway/internal/auth/cache.go:55-75`:
```go
func (t *Counter) get(ctx context.Context, bodyHash, model string) (int, bool, error) {
    if t.rdb == nil { return 0, false, nil }
    raw, err := t.rdb.Get(ctx, tokenCacheKey(bodyHash, model)).Bytes()
    if errors.Is(err, redis.Nil) { return 0, false, nil }
    if err != nil { return 0, false, err }
    n, _ := strconv.Atoi(string(raw))
    return n, true, nil
}
```

**400 envelope on cap exceeded** — use `httpx.WriteOpenAIError` (`gateway/internal/httpx/envelope.go:16-22`) with `"invalid_request_error"` + `"context_length_exceeded"`.

### `gateway/internal/audit/` (extend with `blocked_sensitive` upstream value)

**Analog:** `gateway/internal/audit/middleware.go:147-158` `upstreamForRoute`.

**Add constant** — no table ENUM change needed (`audit_log.upstream` is TEXT in `gateway/db/migrations/0003_create_audit_log_partitioned.sql:13`):
```go
// UpstreamBlockedSensitive is the reserved value written to audit_log.upstream
// when a data_class=sensitive request is blocked from external fallback
// (CONTEXT.md D-B3).
const UpstreamBlockedSensitive = "blocked_sensitive"
```

**No `audit_log_content` write** — follow `gateway/internal/audit/writer.go:216-233` (only DataClass="normal" rows hit content table).

### `gateway/internal/redisx/` (helpers: Pub/Sub + HSet/HGetAll)

**Analog:** `gateway/internal/redisx/client.go:1-36` (package layout + NewClient pattern). Phase 3 adds helpers in same package.

**New helper file (`gateway/internal/redisx/breaker.go`):**
```go
package redisx

// PublishBreakerEvent publishes a JSON payload to gw:breaker:events.
func PublishBreakerEvent(ctx context.Context, rdb *redis.Client, payload []byte) error {
    return rdb.Publish(ctx, "gw:breaker:events", payload).Err()
}

// SubscribeBreakerEvents returns a *redis.PubSub subscribed to gw:breaker:events.
// Caller is responsible for Close().
func SubscribeBreakerEvents(ctx context.Context, rdb *redis.Client) *redis.PubSub {
    return rdb.Subscribe(ctx, "gw:breaker:events")
}

// WriteBreakerState HSETs the {state,since,trip_count,last_failure_code} hash.
func WriteBreakerState(ctx context.Context, rdb *redis.Client, name string, state string, since int64) error {
    return rdb.HSet(ctx, "gw:breaker:"+name, map[string]any{
        "state": state, "since_unix": since,
    }).Err()
}
```

### `gateway/internal/config/config.go` (extend with new env vars)

**Analog:** self — `gateway/internal/config/config.go:16-118`.

**Extend the Config struct** — follow existing field grouping + comments style (lines 40-53):
```go
// Add after existing Upstream block:
UpstreamOpenRouterChatURL            string   // UPSTREAM_LLM_OPENROUTER_URL
UpstreamOpenRouterChatAuthBearer     string   // UPSTREAM_LLM_OPENROUTER_AUTH_BEARER
UpstreamOpenRouterProviderOrder      []string // UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER (CSV)
UpstreamOpenRouterAllowFallbacks     bool     // UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS (default false)
UpstreamOpenAIWhisperURL             string   // UPSTREAM_STT_OPENAI_URL
UpstreamOpenAIWhisperAuthBearer      string   // UPSTREAM_STT_OPENAI_AUTH_BEARER
UpstreamOpenAIEmbedURL               string   // UPSTREAM_EMBED_OPENAI_URL
UpstreamOpenAIEmbedAuthBearer        string   // UPSTREAM_EMBED_OPENAI_AUTH_BEARER
ProbeIntervalSeconds                 int      // PROBE_INTERVAL_SECONDS (default 10)
ProbeBudgetSeconds                   int      // PROBE_BUDGET_SECONDS (default 5)
BreakerConsecutiveFailures           int      // BREAKER_CONSECUTIVE_FAILURES (default 3)
BreakerCooldownSeconds               int      // BREAKER_COOLDOWN_SECONDS (default 30)
```

**Load block** — extend the existing `cfg := Config{...}` literal at `gateway/internal/config/config.go:61-87` + add to `requiredOrder` if required, or leave absent-tolerant with defaults via `envOr`/`atoiOr`. External upstream URLs/bearers are OPTIONAL at boot — only required when the row is enabled in `upstreams` table (Loader reports missing-env warn log per CONTEXT.md Plumbing).

**CSV parser:**
```go
func csvOr(s string, def []string) []string {
    if s == "" { return def }
    parts := strings.Split(s, ",")
    for i, p := range parts { parts[i] = strings.TrimSpace(p) }
    return parts
}
```

### `gateway/db/migrations/0007_create_upstreams.sql` (table + indexes)

**Analog:** `gateway/db/migrations/0005_create_model_aliases.sql` (small schema table w/ CHECK + seed).

**Exact skeleton (copy header from `gateway/db/migrations/0005_create_model_aliases.sql:1-3`):**
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE TABLE IF NOT EXISTS ai_gateway.upstreams (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    role            TEXT NOT NULL,
    tier            INT NOT NULL,
    url_env         TEXT NOT NULL,
    auth_bearer_env TEXT,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    weight          INT,
    circuit_config  JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_probe_at   TIMESTAMPTZ,
    last_probe_ms   INT,
    last_probe_status TEXT,
    last_probe_error  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (role IN ('llm','stt','embed')),
    CHECK (last_probe_status IS NULL OR last_probe_status IN ('ok','failed','timeout')),
    UNIQUE (role, tier)
);

CREATE INDEX IF NOT EXISTS idx_upstreams_enabled_role_tier
    ON ai_gateway.upstreams (enabled, role, tier);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.upstreams;
-- +goose StatementEnd
```

### `gateway/db/migrations/0008_seed_upstreams.sql` (6 initial rows)

**Analog:** `gateway/db/migrations/0001_create_tenants.sql:17-19` + `gateway/db/migrations/0005_create_model_aliases.sql:13-19` (INSERT … ON CONFLICT DO NOTHING).

**Exact pattern:**
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;
INSERT INTO ai_gateway.upstreams (name, role, tier, url_env, auth_bearer_env) VALUES
    ('local-llm',       'llm',   0, 'UPSTREAM_LLM_URL',           NULL),
    ('openrouter-chat', 'llm',   1, 'UPSTREAM_LLM_OPENROUTER_URL','UPSTREAM_LLM_OPENROUTER_AUTH_BEARER'),
    ('local-stt',       'stt',   0, 'UPSTREAM_STT_URL',           NULL),
    ('openai-whisper',  'stt',   1, 'UPSTREAM_STT_OPENAI_URL',    'UPSTREAM_STT_OPENAI_AUTH_BEARER'),
    ('local-embed',     'embed', 0, 'UPSTREAM_EMBED_URL',         NULL),
    ('openai-embed',    'embed', 1, 'UPSTREAM_EMBED_OPENAI_URL',  'UPSTREAM_EMBED_OPENAI_AUTH_BEARER')
ON CONFLICT (name) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ai_gateway.upstreams WHERE name IN ('local-llm','openrouter-chat','local-stt','openai-whisper','local-embed','openai-embed');
-- +goose StatementEnd
```

### `gateway/db/migrations/0009_upstreams_notify_trigger.sql`

**Analog:** `gateway/db/migrations/0003_create_audit_log_partitioned.sql:40-57` (DO $$ block within `-- +goose StatementBegin`). The `StatementBegin/End` wrapper makes goose forward the entire PL/pgSQL block without splitting on `$$`.

**Exact wrapper pattern:**
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE OR REPLACE FUNCTION ai_gateway.notify_upstreams_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('upstreams_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END; $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS upstreams_change_notify ON ai_gateway.upstreams;
CREATE TRIGGER upstreams_change_notify
AFTER INSERT OR UPDATE OR DELETE ON ai_gateway.upstreams
FOR EACH ROW EXECUTE FUNCTION ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS upstreams_change_notify ON ai_gateway.upstreams;
DROP FUNCTION IF EXISTS ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd
```

### `gateway/db/migrations/0010_audit_log_blocked_sensitive_enum.sql`

**Note:** `audit_log.upstream` column is already `TEXT NOT NULL` (see `gateway/db/migrations/0003_create_audit_log_partitioned.sql:13`), not an ENUM. **This migration may reduce to a comment/no-op** — planner should confirm and either drop the migration or use it to add a CHECK constraint documenting the reserved values. Reference migration for ENUM DDL if needed: `gateway/db/migrations/0002_create_api_keys.sql:5-11` (`DO $$ BEGIN CREATE TYPE ... EXCEPTION WHEN duplicate_object THEN NULL; END $$;`).

### `gateway/db/queries/upstreams.sql` (sqlc queries)

**Analog:** `gateway/db/queries/admin.sql:1-23` (CRUD queries w/ `-- name: XxxName :one|:many|:exec` comments + inline rationale).

**Exact annotation style + explanatory comment pattern:**
```sql
-- name: ListEnabledUpstreams :many
-- Hot-path load at boot and on LISTEN/NOTIFY. Returns all enabled rows
-- ordered by (role, tier) so callers can deterministically build the
-- tier-0/tier-1 map (CONTEXT.md D-D2).
SELECT id, name, role, tier, url_env, auth_bearer_env, enabled, weight,
       circuit_config, last_probe_at, last_probe_ms, last_probe_status,
       last_probe_error, created_at, updated_at
FROM ai_gateway.upstreams
WHERE enabled = TRUE
ORDER BY role, tier;

-- name: GetUpstreamByName :one
SELECT id, name, role, tier, url_env, auth_bearer_env, enabled, weight,
       circuit_config, last_probe_at, last_probe_ms, last_probe_status,
       last_probe_error, created_at, updated_at
FROM ai_gateway.upstreams
WHERE name = $1;

-- name: UpdateUpstreamProbe :exec
-- Written by the probe goroutine every probe cycle (CONTEXT.md D-A2).
-- Batched via buffered channel + 1s flush; see gateway/internal/upstreams/probe.go.
UPDATE ai_gateway.upstreams
SET last_probe_at = $2, last_probe_ms = $3, last_probe_status = $4,
    last_probe_error = $5, updated_at = NOW()
WHERE name = $1;

-- name: UpdateUpstreamAdmin :exec
-- Called by gatewayctl upstreams update. Triggers NOTIFY via
-- ai_gateway.notify_upstreams_changed() (migration 0009) which the
-- listen goroutine consumes to reload config.
UPDATE ai_gateway.upstreams
SET tier = COALESCE($2, tier),
    enabled = COALESCE($3, enabled),
    circuit_config = COALESCE($4, circuit_config),
    updated_at = NOW()
WHERE name = $1;

-- name: SetUpstreamEnabled :exec
UPDATE ai_gateway.upstreams SET enabled = $2, updated_at = NOW() WHERE name = $1;

-- name: ListAllUpstreams :many
-- Admin-surface (gatewayctl upstreams list). NOT on hot path.
SELECT id, name, role, tier, url_env, auth_bearer_env, enabled, weight,
       circuit_config, last_probe_at, last_probe_ms, last_probe_status,
       last_probe_error, created_at, updated_at
FROM ai_gateway.upstreams
ORDER BY role, tier;
```

### `gateway/cmd/gatewayctl/upstreams.go` (CLI subcommand)

**Analog:** `gateway/cmd/gatewayctl/tenant.go:16-53` (simple create) + `gateway/cmd/gatewayctl/key.go:17-150` (multi-subcommand dispatch + flag.NewFlagSet + loadAndPool + error handling).

**Dispatcher** — copy from `gateway/cmd/gatewayctl/key.go:17-31`:
```go
func runUpstreams(ctx context.Context, args []string, log *slog.Logger) int {
    if len(args) == 0 {
        fmt.Fprintln(flag.CommandLine.Output(), "Usage: gatewayctl upstreams list|update|enable|disable [flags]")
        return 2
    }
    switch args[0] {
    case "list":    return runUpstreamsList(ctx, args[1:], log)
    case "update":  return runUpstreamsUpdate(ctx, args[1:], log)
    case "enable":  return runUpstreamsEnable(ctx, args[1:], log)
    case "disable": return runUpstreamsDisable(ctx, args[1:], log)
    default:
        fmt.Fprintf(flag.CommandLine.Output(), "unknown subcommand: %s\n", args[0])
        return 2
    }
}
```

**Flag parsing + loadAndPool** — exact copy from `gateway/cmd/gatewayctl/tenant.go:20-50`:
```go
fs := flag.NewFlagSet("upstreams update", flag.ExitOnError)
name := fs.String("name", "", "upstream name (required)")
tier := fs.Int("tier", -1, "tier (0 primary, 1 fallback)")
enabled := fs.Bool("enabled", true, "enabled")
if err := fs.Parse(args); err != nil { return 2 }
// ...
_, pool, err := loadAndPool(ctx, log)
if err != nil { fmt.Fprintf(fs.Output(), "error: %v\n", err); return 1 }
defer pool.Close()
q := gen.New(pool)
```

**Register in main.go switch** — add case to `gateway/cmd/gatewayctl/main.go:47-64`:
```go
case "upstreams":
    os.Exit(runUpstreams(ctx, args, log))
```

**Update usage() text** at `gateway/cmd/gatewayctl/main.go:20-34`.

### Integration tests (`gateway/internal/integration_test/*_test.go`)

**Analog:** `gateway/internal/integration_test/upstream_e2e_test.go` + `gateway/internal/integration_test/audit_write_test.go` + `gateway/internal/integration_test/setup_test.go` helpers.

**Mandatory boilerplate (copy from `gateway/internal/integration_test/upstream_e2e_test.go:1-14`):**
```go
//go:build integration

package integration

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
    // ...
)
```

**Fresh schema + mock upstream** — copy from `gateway/internal/integration_test/upstream_e2e_test.go:18-34`:
```go
func TestIntegration_XX_BreakerStateMachine(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    pool, rdb := freshSchema(t, ctx)
    _ = pool; _ = rdb

    var hits int64
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        atomic.AddInt64(&hits, 1)
        // simulate 500 for first 3 requests, then 200
        if atomic.LoadInt64(&hits) <= 3 {
            http.Error(w, "upstream 500", 500)
            return
        }
        w.WriteHeader(200)
    }))
    defer upstream.Close()
    // ... drive breaker through CLOSED → OPEN → HALF_OPEN → CLOSED ...
}
```

**Async flush wait idiom** — copy from `gateway/internal/integration_test/audit_write_test.go:68-75`:
```go
deadline := time.Now().Add(5 * time.Second)
for time.Now().Before(deadline) {
    _ = pool.QueryRow(ctx, "SELECT last_probe_status FROM ai_gateway.upstreams WHERE name=$1", "local-llm").Scan(&status)
    if status == "ok" { break }
    time.Sleep(100 * time.Millisecond)
}
```

**Tenant + api_key seeding for sensitive tests** — reuse `seedTenantAndKey(t, ctx, pool, "sensitive-tenant", auth.DataClassSensitive)` from `gateway/internal/integration_test/setup_test.go:214-233`.

## Shared Patterns

### Authentication context read

**Source:** `gateway/internal/auth/context.go` + usage in `gateway/internal/idempotency/middleware.go:60-67`.
**Apply to:** `gateway/internal/proxy/dispatcher.go`, `gateway/internal/proxy/sensitive.go`.

```go
ac, ok := auth.FromContext(r.Context())
if !ok || ac.TenantID == "" {
    httpx.WriteOpenAIError(w, http.StatusUnauthorized,
        "authentication_error", "no_api_key",
        "Authenticated tenant required.")
    return
}
// ac.DataClass is auth.DataClass: "normal" | "sensitive"
// ac.TenantID / ac.APIKeyID are strings
```

### OpenAI error envelope

**Source:** `gateway/internal/httpx/envelope.go:16-22`.
**Apply to:** All Phase 3 error paths (dispatcher 503, sensitive-retry-exhausted 503, context-length 400, upstream-unavailable 503, tool-call-partial 502).

```go
// 503 sensitive block (D-B2)
w.Header().Set("Retry-After", "30")
httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
    "service_unavailable", "upstream_unavailable_for_sensitive_tenant",
    "Primary inference upstream is unavailable; sensitive-data tenants cannot be routed to external providers.")

// 503 all upstreams (D-C4)
httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
    "service_unavailable", "upstream_unavailable",
    "All inference upstreams are unavailable.")

// 400 context length (RES-07)
httpx.WriteOpenAIError(w, http.StatusBadRequest,
    "invalid_request_error", "context_length_exceeded",
    "Request exceeds 16384 token cap.")

// 502 tool-call partial (RES-06)
httpx.WriteOpenAIError(w, http.StatusBadGateway,
    "api_error", "tool_call_partial_stream",
    "Primary upstream disconnected after tool call emission; retry with a fresh idempotency key.")
```

### slog module + RFC3339 timestamp

**Source:** `gateway/internal/proxy/errors.go:18` + `gateway/internal/audit/writer.go:83` + `gateway/internal/models/resolver.go:48`.
**Apply to:** All new packages (`BREAKER`, `PROBE`, `UPSTREAMS`, `LISTEN`, `TOKENIZE`, `DISPATCHER`, `FALLBACK`).

```go
log = log.With("module", "BREAKER")
log.Info("state change",
    "upstream", name,
    "from", from.String(),
    "to", to.String(),
    "at", time.Now().Format(time.RFC3339),
    "request_id", httpx.RequestIDFrom(r.Context()),
)
```

### Async buffered write with 1s flush

**Source:** `gateway/internal/audit/writer.go:22-27, 102-169` (buffer channel + Enqueue + Run drain).
**Apply to:** `gateway/internal/upstreams/probe.go` (last_probe_* UPDATE batching).

Rule: hot path **MUST NEVER BLOCK** on DB write. Use buffered channel + non-blocking `select` with default:
```go
select {
case w.ch <- event:
default:
    w.dropped.Add(1)
    obs.ProbeUpdateDroppedTotal.Inc()
}
```

### Redis key namespace

**Source:** `gateway/internal/auth/cache.go:42-53` (`gw:apikey:`) + `gateway/internal/idempotency/store.go:74` (`gw:idem:`).
**Apply to:** Phase 3 keys under `gw:breaker:*` (Hash + Pub/Sub) and `gw:tokenize:*` (cache). Never create keys outside the `gw:` prefix.

### ProxyResponseInterceptor extension point

**Source:** `gateway/internal/proxy/interceptor.go:17-39`.
**Apply to:** `gateway/internal/proxy/toolcall.go` (`ToolCallInterceptor`). Plug in via `proxy.NewChatProxy(..., auditInterceptor, toolCallInterceptor)` composition.

Rules: interceptors MUST NOT call `resp.Body.Close` themselves (the tee wrapper delegates); compile-time check `var _ proxy.ProxyResponseInterceptor = (*ToolCallInterceptor)(nil)`.

### Sentry breadcrumbs on breaker trip

**Source:** `gateway/internal/obs/sentry.go:19-41` (BeforeSend hook + redaction pattern).
**Apply to:** `gateway/internal/breaker/breaker.go` `OnStateChange`:
```go
sentry.AddBreadcrumb(&sentry.Breadcrumb{
    Category: "breaker",
    Message:  "state change",
    Level:    sentry.LevelWarning,
    Data: map[string]any{
        "upstream": name, "from": from.String(), "to": to.String(),
    },
})
```

### Prometheus collectors

**Source:** `gateway/internal/obs/metrics.go:1-53` (promauto.New* in init).
**Apply to:** Extend `gateway/internal/obs/metrics.go` with Phase 3 collectors:
```go
var BreakerState = promauto.NewGaugeVec(
    prometheus.GaugeOpts{Name: "gateway_breaker_state",
        Help: "0=closed, 1=half-open, 2=open."},
    []string{"upstream"},
)
var BreakerTripsTotal = promauto.NewCounterVec(
    prometheus.CounterOpts{Name: "gateway_breaker_trips_total",
        Help: "CLOSED→OPEN transitions by upstream."},
    []string{"upstream"},
)
// ... ProbeDurationMs, ProbeFailureTotal, SensitiveRetryTotal,
//     ToolCallPartialTotal, UpstreamsReloadTotal, BreakerMirrorFailuresTotal,
//     UpstreamThrottledTotal
```

Keep label cardinality bounded — `upstream` label has 6 values; `reason`/`status`/`outcome` labels enumerate small sets only (CONTEXT.md Plumbing note in obs/metrics.go:1-4).

### Sentinel errors file layout

**Source:** `gateway/internal/proxy/errors.go:13` + `gateway/internal/auth/errors.go:10-27` + `gateway/internal/idempotency/errors.go:10-20`.
**Apply to:** `gateway/internal/breaker/errors.go` (ErrBreakerOpen, ErrUpstreamUnavailable) + `gateway/internal/upstreams/errors.go` (ErrProbeTimeout, ErrUpstreamNotFound) + `gateway/internal/proxy/errors.go` extension (ErrSensitiveRetryExhausted, ErrToolCallPartialStream, ErrContextLengthExceeded).

Each errors.go file sits in the package it decorates; sentinel names begin with `Err` and include a `// ErrXxx — <1-line explanation including HTTP status code and error-code string>` godoc.

### Integration test build tag + helpers

**Source:** `gateway/internal/integration_test/setup_test.go:1-248` (TestMain, setupContainers, freshSchema, seedTenant, seedTenantAndKey, discardLogger).
**Apply to:** All 4 new integration tests. NEVER add `//go:build integration` files outside `gateway/internal/integration_test/`; reuse the shared harness.

For Phase 3 tests that need a mock upstream HTTP server, use `httptest.NewServer(http.HandlerFunc(...))` per `gateway/internal/integration_test/upstream_e2e_test.go:24-34`. Track hits with `atomic.AddInt64` and assert via `atomic.LoadInt64`.

### Config loading in CLI tools

**Source:** `gateway/cmd/gatewayctl/main.go:68-78` `loadAndPool`.
**Apply to:** `gateway/cmd/gatewayctl/upstreams.go` — reuse `loadAndPool(ctx, log)` unchanged.

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `gateway/internal/breaker/subscribe.go` | service | pub-sub | No existing Redis Pub/Sub consumer in repo. Closest structural analog is `gateway/internal/audit/writer.go:128-169` long-running goroutine; planner must also import go-redis Pub/Sub API per RESEARCH.md §Pattern 1 + §Standard Stack. |
| `gateway/internal/upstreams/listen.go` | service | event-driven | No existing `pgx` LISTEN consumer. Use `jackc/pgxlisten` (new dep) per RESEARCH.md §Pattern 4. Goroutine shape copied from `gateway/internal/audit/writer.go:128-169`; connection dedicated conn (NOT from pgxpool) per CONTEXT.md D-D4. |

## Metadata

**Analog search scope:**
- `gateway/internal/` (all packages — proxy, audit, auth, httpx, idempotency, models, redisx, obs, config, db, upstreams)
- `gateway/cmd/gateway/` + `gateway/cmd/gatewayctl/`
- `gateway/db/migrations/` (0001–0006) + `gateway/db/queries/` (admin.sql, audit.sql, auth.sql, model_aliases.sql)
- `gateway/internal/integration_test/`
- `pkg/openai/types.go`

**Files scanned:** ~55 Go files, 6 migrations, 4 sqlc query files, 1 sqlc.yaml.

**Pattern extraction date:** 2026-04-19
