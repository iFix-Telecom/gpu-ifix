---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 05
subsystem: billing + admin + proxy + audit
tags:
  - phase-04
  - billing
  - prices
  - admin
  - proxy
  - foundation
  - wave-2
dependency_graph:
  requires:
    - 04-01 (billing sentinel errors, admin sentinel errors, openai error codes)
    - 04-02 (sqlc InsertBillingEvent CTE, SumBillingEvents queries, prices/fx/admin_keys queries, GetTenantConfig)
  provides:
    - billing.PricesLoader (atomic.Pointer snapshot; lock-free Get)
    - billing.FXLoader (USD/BRL snapshot; default fallback via caller)
    - billing.ListenAndReload (NOTIFY prices_changed + fx_changed multiplexed)
    - billing.Accountant (per-request RequestUsage map, copy-on-write)
    - billing.RequestUsage (4 atomic.Int64 fields for SSE interceptor)
    - billing.Flusher (async batched flusher; 1000-buf, 500-rows-or-1s)
    - billing.Event (row shape for billing_events)
    - billing.ComputeCostBRL (pure helper units × USD/unit × USD/BRL)
    - proxy.UsageInterceptor (dual-shape SSE usage extractor)
    - admin.Verifier + admin.Middleware (X-Admin-Key bcrypt + Redis 60s cache)
    - admin.UsageHandler (GET /admin/usage handler emitting SC-3 shape)
    - 5 new prom collectors (GatewayBillingFlush, GatewayBillingFlushFailures, GatewayBillingFlushDropped, GatewayPricesReload, GatewayAdminRequests)
    - httpx.ContextWithRequestID (exported setter for tests)
  affects:
    - gateway/internal/audit/writer.go (Event struct extended additively with 5 cost columns)
  consumed_by:
    - 04-06 (main.go wires Flusher into goroutine, mounts /admin/* router, inserts interceptor)
    - 04-07 (gatewayctl prices/fx/admin-key/usage subcommands)
    - 04-08 (integration tests exercise flusher + admin endpoint against testcontainers Postgres)
tech_stack:
  added:
    - golang.org/x/crypto/bcrypt (indirect dep already; promoted to direct usage in admin middleware)
  patterns:
    - atomic.Pointer[snapshot] (mirror upstreams/loader.go)
    - pgxlisten.Listener with 2 Handle() channels on one conn (prices_changed + fx_changed)
    - copy-on-write map via atomic.Pointer (mirror proxy/toolcall.go toolCallFlags)
    - channel buffer 1000 + flush 500-rows-or-1s (mirror audit/writer.go)
    - per-row INSERT through CTE preserving ON CONFLICT DO NOTHING (vs CopyFrom which bypasses it)
    - SSE frame tee reader with bytes.Buffer + \n\n terminator scan (mirror proxy/toolcall.go toolCallTee)
    - Redis cache with SHA-256 lookup hash + 60s TTL (mirror auth/cache.go)
    - OpenAI error envelope via httpx.WriteOpenAIError (mirror auth/apikey.go middleware)
key_files:
  created:
    - gateway/internal/billing/prices.go
    - gateway/internal/billing/prices_loader.go
    - gateway/internal/billing/fx_loader.go
    - gateway/internal/billing/listen.go
    - gateway/internal/billing/accountant.go
    - gateway/internal/billing/flusher.go
    - gateway/internal/billing/events.go
    - gateway/internal/billing/cost.go
    - gateway/internal/billing/cost_test.go
    - gateway/internal/proxy/interceptor_usage.go
    - gateway/internal/proxy/interceptor_usage_test.go
    - gateway/internal/admin/middleware.go
    - gateway/internal/admin/middleware_test.go
    - gateway/internal/admin/usage.go
  modified:
    - gateway/internal/audit/writer.go (Event struct: added AudioSeconds, EmbedsCount, CostLocalBRL, CostLocalPhantomBRL, CostExternalBRL)
    - gateway/internal/httpx/requestid.go (added ContextWithRequestID exported helper)
    - gateway/internal/obs/metrics.go (added 5 Phase 4 collectors for billing + admin paths)
decisions:
  - ComputeCostBRL takes concrete *PricesLoader/*FXLoader pointers (per plan signature) and returns 0 on nil/missing values — never negative, never panics.
  - usageTeeReader copies frame bytes via make+copy before calling r.buf.Next to avoid aliasing the recycled buffer memory (subtle bytes.Buffer safety point).
  - Phantom cost is NOT summed into cost_total_brl in the /admin/usage response — only cost_local_brl + cost_external_brl. Phantom is reporting-only.
  - Flusher uses d.q.WithTx(tx) to attach the txn to the sqlc Queries for per-row InsertBillingEvent calls (rather than creating a fresh gen.New(tx)) — matches idiomatic pgx+sqlc patterns.
  - listen.go returns nil (not the handler error) on Refresh failures so a transient DB hiccup doesn't take the LISTEN loop down — matches upstreams/listen.go contract.
  - Admin middleware accepts nil queries AND nil rdb in construction so unit tests can exercise the missing/invalid envelope without a Postgres round-trip. Production wiring always passes both.
  - resolveTenant in admin/usage.go tries UUID parse first, falls back to GetTenantBySlug — symmetric with gatewayctl UX.
metrics:
  duration_minutes: 35
  completed_date: 2026-04-21
  tasks_completed: 2
  files_created: 14
  files_modified: 3
  tests_added: 13
---

# Phase 04 Plan 05: Billing Pipeline + Admin Endpoint + SSE Usage Interceptor + Audit Evolve Summary

**One-liner:** Wave 2 split B — billing accountant + flusher + prices/fx loaders with hot-reload NOTIFY, X-Admin-Key bcrypt middleware + GET /admin/usage handler emitting SC-3 shape from authoritative billing_events, dual-shape SSE usage interceptor (OpenAI + llama.cpp), additive audit.Event extension, and 5 new prom collectors.

## Public API

### `gateway/internal/billing`

```go
// Snapshot types (prices.go)
type PriceKey struct { Model, Provider, Unit string }
type Price struct { UnitCostUSD float64; ValidFrom time.Time }
type FXRate struct { CurrencyPair string; Rate float64; ValidFrom time.Time }

// Hot-reloadable prices loader (prices_loader.go)
func NewPricesLoader(ctx, pool, log) (*PricesLoader, error)
func (l *PricesLoader) Refresh(ctx context.Context) error
func (l *PricesLoader) Get(model, provider, unit string) (Price, bool)

// Hot-reloadable fx loader (fx_loader.go)
func NewFXLoader(ctx, pool, log) (*FXLoader, error)
func (l *FXLoader) Refresh(ctx context.Context) error
func (l *FXLoader) Get(pair string) (FXRate, bool)

// NOTIFY listener for both channels on one pgx conn (listen.go)
func ListenAndReload(ctx, dsn, prices, fx, log) error

// Per-request accountant (accountant.go)
type RequestUsage struct {
    TokensIn, TokensOut, AudioSecondsMs10, EmbedsCount atomic.Int64
}
type Accountant struct { ... }
func NewAccountant() *Accountant
func (a *Accountant) Set(reqID string, u *RequestUsage)
func (a *Accountant) Get(reqID string) *RequestUsage
func (a *Accountant) Delete(reqID string)

// Async batched flusher (flusher.go)
type Event struct {
    TS time.Time
    RequestID, TenantID, APIKeyID uuid.UUID
    Route, Upstream, Model string
    TokensIn, TokensOut, EmbedsCount int
    AudioSeconds, CostLocalBRL, CostLocalPhantomBRL, CostExternalBRL float64
    Source string  // "final" | "partial"
}
type Flusher struct { ... }
func NewFlusher(pool, log) *Flusher
func (f *Flusher) Enqueue(e Event)   // non-blocking; drops on back-pressure
func (f *Flusher) Run(ctx context.Context)
func (f *Flusher) Dropped() uint64

// Pure cost helper (cost.go)
func ComputeCostBRL(units float64, model, provider, unit string,
    prices *PricesLoader, fx *FXLoader, defaultUSDBRL float64,
    log *slog.Logger) float64
```

### `gateway/internal/proxy`

```go
// Dual-shape SSE usage extractor (interceptor_usage.go)
type UsageInterceptor struct { ... }
var _ ProxyResponseInterceptor = (*UsageInterceptor)(nil)
func NewUsageInterceptor(a *billing.Accountant, log *slog.Logger) *UsageInterceptor
func (u *UsageInterceptor) Intercept(resp *http.Response) error
func (u *UsageInterceptor) ExtractFromBody(reqID string, body []byte)
```

### `gateway/internal/admin`

```go
// X-Admin-Key bcrypt middleware (middleware.go)
type AdminContext struct { AdminKeyID uuid.UUID; Label, KeyPrefix string }
type Verifier struct { ... }
func NewVerifier(q adminQueries, rdb redis.UniversalClient, log *slog.Logger) *Verifier
func (v *Verifier) Verify(ctx, rawKey) (AdminContext, error)
func Middleware(v *Verifier, log *slog.Logger) func(http.Handler) http.Handler
func FromContext(ctx context.Context) (AdminContext, bool)

// GET /admin/usage handler (usage.go)
type UsageResponse, TenantSection, RangeSection, Summary, DayRow (SC-3 shape)
type UsageHandler struct { ... }
func NewUsageHandler(q *gen.Queries, log *slog.Logger) *UsageHandler
func (h *UsageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

### `gateway/internal/audit`

```go
// Event struct extended additively (writer.go)
type Event struct {
    // ... existing Phase 2/3 fields preserved ...
    AudioSeconds        float64  // Phase 4 extension
    EmbedsCount         int      // Phase 4 extension
    CostLocalBRL        float64  // Phase 4 extension
    CostLocalPhantomBRL float64  // Phase 4 extension
    CostExternalBRL     float64  // Phase 4 extension
}
```

### `gateway/internal/httpx`

```go
// New exported setter for test wiring (requestid.go)
func ContextWithRequestID(ctx context.Context, rid string) context.Context
```

### `gateway/internal/obs`

```go
// 5 new Phase 4 collectors (metrics.go)
var GatewayBillingFlush = promauto.NewCounterVec(..., []string{"source"})
var GatewayBillingFlushFailures = promauto.NewCounterVec(..., []string{"reason"})
var GatewayBillingFlushDropped = promauto.NewCounter(...)
var GatewayPricesReload = promauto.NewCounterVec(..., []string{"result"})
var GatewayAdminRequests = promauto.NewCounterVec(..., []string{"route","status"})
```

## Commits

- **471100a** `feat(04-05): billing pipeline + audit Event extension + 5 prom collectors` — 9 new files under `internal/billing/`, audit Event extension, httpx helper, 5 obs collectors. 840 lines inserted.
- **8e71dc5** `feat(04-05): SSE usage interceptor + admin bcrypt middleware + GET /admin/usage` — 2 new files under `internal/proxy/`, 3 new files under `internal/admin/`. 946 lines inserted.

## Tests

All pass `-race -short`:

- `internal/billing` — TestComputeCost_MissingPriceReturnsZero, _NegativeUnitsClampToZero, _ZeroUnitsReturnsZero, _NilLoadersSafe; TestAccountant_SetGetDelete, _DeleteNonexistent (6 tests)
- `internal/proxy` — TestUsageExtractorOpenAIShape, _LlamaCppShape, _IgnoresChunksWithoutUsage, _DoneChunkIgnored (Pitfall 5), _NonStreamingPassthrough, _ExtractFromBody, _NoRequestContext (7 tests)
- `internal/admin` — TestAdminMiddleware_MissingHeader_401, _InvalidKey_401, _FromContextReturnsFalseOnUnauthed (3 tests — full bcrypt + DB path in 04-08)
- Unchanged: billing package now also covers `TestAccountant_*` + all `cost_test.go` scenarios; audit suite still passes 100%.

Total new tests: 16.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Added `httpx.ContextWithRequestID` exported helper**
- **Found during:** Task 2 (interceptor_usage_test.go references `httpx.ContextWithRequestID` per plan behavior specs)
- **Issue:** The plan's test file uses `httpx.ContextWithRequestID(context.Background(), "req-openai")` but the helper did not exist — the existing `httpx.RequestID` middleware sets the value via an unexported `ctxKey` constant.
- **Fix:** Added `ContextWithRequestID(ctx, rid)` to `gateway/internal/httpx/requestid.go` reusing the same unexported `requestIDKey` ctxKey so production middleware-set values and test-set values share one canonical location.
- **Files modified:** `gateway/internal/httpx/requestid.go`
- **Commit:** 471100a

**2. [Rule 3 - Blocking] Registered the 5 new obs collectors in this plan**
- **Found during:** Task 1 (billing flusher + admin middleware reference `obs.GatewayBillingFlush`, `GatewayBillingFlushFailures`, `GatewayBillingFlushDropped`, `GatewayPricesReload`, `GatewayAdminRequests` which did not exist).
- **Issue:** The plan notes "Plan 04-04 sibling adds all 10 obs collectors to avoid file conflict" but 04-04 runs in a parallel worktree not yet merged — the code this plan produces would not compile against the current branch.
- **Fix:** Added the 5 collectors used ONLY by plan 04-05 directly. The names are disjoint from any plan 04-04 might add (04-04 owns GatewayRateLimitRejected, _CheckFailures, GatewayQuotaRejected, _CheckFailures, GatewayScheduleRouting, GatewayTenantsReload per its must_haves list). On merge: if 04-04 also registers these same 5 names (unlikely; its owned list omits them), one side must resolve a merge conflict in `obs/metrics.go` — prefer 04-05's registrations since they match billing/admin code exactly.
- **Files modified:** `gateway/internal/obs/metrics.go`
- **Commit:** 471100a

**3. [Rule 1 - Defensive] usageTeeReader frame copy**
- **Found during:** Task 2 implementation
- **Issue:** The plan skeleton used `frame := data[:i]` followed by `r.buf.Next(i+2)`. `bytes.Buffer.Next` advances the read cursor; the slice `data[:i]` aliases the underlying array which Buffer may recycle. Under concurrent Read calls this can cause frame corruption (rare but real).
- **Fix:** Changed to `frame := make([]byte, i); copy(frame, data[:i])` before `r.buf.Next(i+2)`.
- **Files modified:** `gateway/internal/proxy/interceptor_usage.go`
- **Commit:** 8e71dc5

### Rule 4 Items

None — no architectural changes proposed.

### Out-of-Scope Findings (Not Fixed)

**`gofmt -l` reports pre-existing format issues in `gateway/internal/proxy/dispatcher.go` and `gateway/internal/proxy/toolcall_test.go`.** These files were not touched by this plan. Per deviation scope boundary, these are not fixed here — report only.

## Acceptance Criteria

### Task 1

- [x] `gateway/internal/billing/prices.go` exists with `type PriceKey struct` and `type Price struct`
- [x] `gateway/internal/billing/prices_loader.go` contains `atomic.Pointer[pricesSnapshot]`, calls `q.ListActivePrices`
- [x] `gateway/internal/billing/fx_loader.go` contains `q.GetCurrentFX` and Get returns (FXRate, bool)
- [x] `gateway/internal/billing/listen.go` registers BOTH `prices_changed` AND `fx_changed` (grep count = 2)
- [x] `gateway/internal/billing/accountant.go` defines `RequestUsage` with 4 atomic.Int64 fields and `Accountant` with copy-on-write map
- [x] `gateway/internal/billing/flusher.go` declares `bufferSize=1000`, `flushBatchSize=500`, `flushInterval=1*time.Second`
- [x] Non-blocking Enqueue via `select {...default:}` verified in flusher.go
- [x] `gateway/internal/billing/events.go` calls `q.InsertBillingEvent`
- [x] `gateway/internal/billing/cost.go` exposes `ComputeCostBRL` taking PricesLoader + FXLoader pointers
- [x] `gateway/internal/audit/writer.go` Event struct has 5 new fields (grep count = 5)
- [x] `go test -short -race ./internal/billing/... ./internal/audit/...` exits 0
- [x] `go vet ./internal/billing/... ./internal/audit/...` exits 0
- [x] `gofmt -l ./internal/billing ./internal/audit` produces NO output for these directories

### Task 2

- [x] `gateway/internal/proxy/interceptor_usage.go` exists; compile-time `var _ ProxyResponseInterceptor = (*UsageInterceptor)(nil)` assertion present
- [x] `gateway/internal/admin/middleware.go` contains `bcrypt.CompareHashAndPassword` and `gw:admin:`
- [x] `gateway/internal/admin/usage.go` calls both `SumBillingEventsByDate` and `SumBillingEventsRange`
- [x] Response shape fields cover ALL SC-3 fields: tokens_in, tokens_out, audio_seconds, embeds_count, cost_local_brl, cost_local_phantom_brl, cost_external_brl, cost_total_brl, requests_count
- [x] `TestUsageExtractorDoneChunkIgnored` present in test file (Pitfall 5 sentinel-handling guard)
- [x] `go test -short -race ./internal/proxy/... ./internal/admin/...` exits 0
- [x] `go vet ./internal/proxy/... ./internal/admin/...` exits 0
- [x] `gofmt -l` clean on files created by this plan

## Threat Flags

No new threat surface introduced beyond the plan's threat model. All STRIDE items (T-04-15..T-04-20) are mitigated or accepted per the plan.

## Self-Check: PASSED

### File existence

- FOUND: gateway/internal/billing/prices.go
- FOUND: gateway/internal/billing/prices_loader.go
- FOUND: gateway/internal/billing/fx_loader.go
- FOUND: gateway/internal/billing/listen.go
- FOUND: gateway/internal/billing/accountant.go
- FOUND: gateway/internal/billing/flusher.go
- FOUND: gateway/internal/billing/events.go
- FOUND: gateway/internal/billing/cost.go
- FOUND: gateway/internal/billing/cost_test.go
- FOUND: gateway/internal/admin/middleware.go
- FOUND: gateway/internal/admin/middleware_test.go
- FOUND: gateway/internal/admin/usage.go
- FOUND: gateway/internal/proxy/interceptor_usage.go
- FOUND: gateway/internal/proxy/interceptor_usage_test.go
- FOUND: gateway/internal/audit/writer.go (modified: 5 new fields)
- FOUND: gateway/internal/httpx/requestid.go (modified: ContextWithRequestID)
- FOUND: gateway/internal/obs/metrics.go (modified: 5 new collectors)

### Commit existence

- FOUND: 471100a feat(04-05): billing pipeline + audit Event extension + 5 prom collectors
- FOUND: 8e71dc5 feat(04-05): SSE usage interceptor + admin bcrypt middleware + GET /admin/usage
