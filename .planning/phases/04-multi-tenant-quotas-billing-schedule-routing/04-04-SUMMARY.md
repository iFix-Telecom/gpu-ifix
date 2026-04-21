---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 04
subsystem: foundation
tags:
  - phase-04
  - quota
  - tenants
  - schedule
  - rate-limit
  - lua
  - prometheus
  - wave-2

# Dependency graph
requires:
  - phase: 04-multi-tenant-quotas-billing-schedule-routing/04-01
    provides: "Sentinel errors (quota.ErrRateLimit*/ErrQuotaExceeded*/ErrQuotaCheckUnavailable, tenants.ErrTenantNotFound/ErrSensitivePeakInvariant, schedule.ErrOffHoursUpstreamUnavailable) + time/tzdata blank import + floatOr config helper"
  - phase: 04-multi-tenant-quotas-billing-schedule-routing/04-02
    provides: "sqlc bindings — GetUsageCountersToday/Month rows, ListTenantsForLoader rows, CountSensitivePeakInvariant, GetTenantConfig, UpdateTenantMode/UpdateTenantQuota"
provides:
  - "gateway/internal/quota: atomic RPS+RPM Lua token bucket + BucketConfig/RouteClass enum + QuotaChecker helpers + ErrorCode wire-format mapper"
  - "gateway/internal/tenants: atomic.Pointer[snapshot] Loader + pgxlisten tenants_changed handler + CheckSensitivePeakInvariant boot check + pgTimeToClock helper"
  - "gateway/internal/schedule: pure InWindow(now,start,end) with wrap-around + DecideUpstreamTier policy (24/7 → Tier0; peak in-window → Tier0; peak off-hours → Tier1; nil-Location → Tier0 fail-open)"
  - "gateway/internal/obs/metrics.go: 11 new Phase 4 prom collectors (GatewayRateLimitRejected, GatewayRateLimitCheckFailures, GatewayQuotaRejected, GatewayQuotaCheckFailures, GatewayScheduleRouting, GatewayTenantsReload, GatewayBillingFlush, GatewayBillingFlushFailures, GatewayBillingFlushDropped, GatewayPricesReload, GatewayAdminRequests)"
affects:
  - "04-05 (billing flusher + admin middleware — consumes GatewayBillingFlush*/GatewayPricesReload/GatewayAdminRequests; schedule mode consumes tenants.Loader)"
  - "04-06 (middleware chain wires quota.CheckBuckets + QuotaChecker + schedule.DecideUpstreamTier + tenants.Loader.Get + obs.GatewayRateLimitRejected/GatewayQuotaRejected/GatewayScheduleRouting)"
  - "04-07 (gatewayctl consumes tenants.Loader.All() for list output; admin-key subcommands unblocked by Task 2 loader contract)"
  - "04-08 (integration tests — SC-5 1000-goroutine harness against CheckBuckets; NOTIFY hot-reload roundtrip against Loader)"

# Tech tracking
tech-stack:
  added:
    - "github.com/alicebob/miniredis/v2 (already in go.mod; first Lua EVAL unit-test consumer in this plan)"
    - "//go:embed pattern for Lua script (first use in repo)"
  patterns:
    - "Lua script embedded at compile time via //go:embed + redis.NewScript for auto EVALSHA→EVAL fallback"
    - "atomic.Pointer[snapshot] lock-free loader (mirrors upstreams.Loader; now has a second concrete instance in tenants.Loader)"
    - "loaderQueries / countersQueries unexported interfaces for sqlc injection — test fakes live in same package"
    - "pgTimeToClock: convert pgtype.Time (microseconds since midnight) to a time.Time carrying Hour/Minute for schedule comparison"
    - "coerceDataClass: normalize sqlc's interface{}-typed ENUM columns to string without import cycle"
    - "Pure time-of-day InWindow with wrap-around: compare minutes-since-midnight and invert inclusion when startMin > endMin"
    - "Fail-open vs fail-closed split per D-A2: rate-limit check failures log + fail-open; quota check failures return ErrQuotaCheckUnavailable (middleware maps to 503)"

key-files:
  created:
    - "gateway/internal/quota/scripts/token_bucket.lua — atomic RPS+RPM bucket (setex-based, bounded TTL [60, 7200]s, returns 6-tuple including failedWindow discriminator)"
    - "gateway/internal/quota/lua.go — //go:embed wrapper; CheckBuckets + BucketKeyPrefix"
    - "gateway/internal/quota/bucket.go — RouteClass enum (chat/embed/stt) + BucketConfig with RPSRefillPerMs/RPMRefillPerMs derived rates"
    - "gateway/internal/quota/counters.go — QuotaChecker + QuotaLimits + ErrorCode mapper; countersQueries injection interface"
    - "gateway/internal/quota/lua_test.go — miniredis-backed burst/refill/rps-vs-rpm/prefix-shape tests"
    - "gateway/internal/quota/counters_test.go — fake countersQueries covering below/at-limit, audio_minutes+embeds, no-rows treated as under-limit, fail-closed on generic error, zero-limit disables dim, ErrorCode sentinel coverage"
    - "gateway/internal/tenants/config.go — TenantConfig value-typed struct"
    - "gateway/internal/tenants/loader.go — atomic.Pointer[snapshot] Loader + pgTimeToClock + coerceDataClass + CheckSensitivePeakInvariant"
    - "gateway/internal/tenants/listen.go — pgxlisten Listener for tenants_changed (5s reconnect, handler returns nil to stay alive)"
    - "gateway/internal/tenants/loader_test.go — fake loaderQueries covering nil snapshot, Refresh populates byID/bySlug, invalid timezone falls back, list error propagates, invariant raises sentinel, helper unit tests"
    - "gateway/internal/schedule/window.go — InWindow pure func with wrap-around"
    - "gateway/internal/schedule/policy.go — DecideUpstreamTier (24/7 always Tier0; peak uses Location + window; nil Location fails open)"
    - "gateway/internal/schedule/window_test.go — table-driven normal-range + wrap-around cases incl. start-inclusive/end-exclusive edges"
    - "gateway/internal/schedule/policy_test.go — 24/7 always Tier0, peak in/off-hours in America/Sao_Paulo, nil Location fails open, wrap-around window coverage"
  modified:
    - "gateway/internal/obs/metrics.go — appended 11 Phase 4 collectors (6 quota/tenants/schedule + 5 billing/admin consolidated)"

key-decisions:
  - "pgtype.Time is microseconds-since-midnight (not time.Time); adapted loader with pgTimeToClock helper rather than the plan's non-existent .Time/.Valid accessor"
  - "DataClass column is sqlc interface{} (driven by ai_gateway.data_class ENUM); introduced coerceDataClass to normalize without importing gen ENUM types"
  - "QuotaChecker instrumentation deferred to middleware (Plan 04-06) — counters.go only logs failures; keeps package free of obs import (avoids cyclic import risk and matches Plan 04-06's stated responsibility for metric wiring)"
  - "Zero-value QuotaLimits dimensions are treated as 'disabled' not 'blocked' — protects against partially-configured tenants from being wrongly locked out"
  - "CheckBuckets signature takes redis.UniversalClient instead of *redis.Client — accommodates Cluster mode in Phase 6 without API churn; go-redis test client satisfies the interface"

patterns-established:
  - "Embedded Lua script: //go:embed scripts/name.lua + redis.NewScript + Run().Slice() pattern extensible to future atomic ops"
  - "Hand-rolled fake + unexported queries interface for package-internal sqlc injection — already established by upstreams.Loader; now reinforced by quota + tenants"
  - "Phase 4 prom collector naming (gateway_<subsystem>_<event>_<suffix>_total) — extends the Phase 2/3 convention (gateway_upstreams_reload_total etc.) with new dimensions (tenant, window, dimension/period)"

requirements-completed:
  - TEN-03
  - TEN-04
  - TEN-05

# Metrics
duration: 8min
completed: 2026-04-21
---

# Phase 04 Plan 04: Quota Lua + Tenants Loader + Schedule Policy + Phase 4 Prom Collectors Summary

**Atomic Stripe-canonical RPS+RPM Lua bucket (miniredis-verified) + tenants.Loader mirroring the upstreams.Loader atomic.Pointer snapshot with LISTEN tenants_changed hot-reload + pure time-of-day InWindow/DecideUpstreamTier helpers + 11 Phase 4 Prometheus collectors consolidated to avoid Plan 04-05 file conflict.**

## Performance

- **Duration:** ~8 min (executor)
- **Started:** 2026-04-21T09:30:56Z
- **Completed:** 2026-04-21T09:38:56Z
- **Tasks:** 2 / 2 (both `type=auto` with `tdd=true`; no checkpoints)
- **Files created:** 14 (6 quota, 4 tenants, 4 schedule)
- **Files modified:** 1 (obs/metrics.go — +11 collectors appended)
- **Commits:** 2 atomic (feat) + this SUMMARY commit

## Accomplishments

- Plan 04-06 (middleware chain) can now `import "gateway/internal/quota"` and call `quota.CheckBuckets(...)` + `NewQuotaChecker(...).CheckQuotaToday/Month(...)` against the exact sentinels Plan 04-01 declared. The script is verified atomic under the miniredis single-threaded Lua engine — the 1000-goroutine SC-5 proof fits in Plan 04-08 without changing the package contract.
- Plan 04-06 can also `import "gateway/internal/tenants"` and call `loader.Get(tenantID)` / `loader.All()` hot-path lock-free. Boot-time LGPD check (`loader.CheckSensitivePeakInvariant`) is surfaced as a method for main.go to os.Exit(1) on.
- Plan 04-06 can `import "gateway/internal/schedule"` and call `schedule.DecideUpstreamTier(cfg, now)` with full wrap-around support — the dispatcher override hook has its decision primitive ready.
- All 11 Phase 4 prom collectors are live and registered by `promauto` — the middleware in 04-06 and the billing flusher in 04-05 can `.WithLabelValues(...).Inc()` without any further metrics plumbing.

## Task Commits

Each task was committed atomically (all `--no-verify` per parallel-executor protocol):

1. **Task 1: quota package (lua + counters)** — `9efcb5e` (feat) — 6 files, 677 insertions
2. **Task 2: tenants loader + schedule policy + obs collectors** — `e19cd26` (feat) — 9 files, 817 insertions (8 new + 1 modified)

Total diff: 15 files, 1494 insertions, 0 deletions.

## Files Created/Modified

### quota/
- `gateway/internal/quota/scripts/token_bucket.lua` — Stripe-canonical atomic RPS+RPM bucket
- `gateway/internal/quota/lua.go` — `//go:embed` wrapper + `CheckBuckets` + `BucketKeyPrefix`
- `gateway/internal/quota/bucket.go` — `RouteClass` enum + `BucketConfig` with derived refill rates
- `gateway/internal/quota/counters.go` — `QuotaChecker` + `QuotaLimits` + `ErrorCode` + unexported `countersQueries` injection interface
- `gateway/internal/quota/lua_test.go` — 4 miniredis tests (burst, refill, rps-vs-rpm discrimination, prefix shape)
- `gateway/internal/quota/counters_test.go` — 10 unit tests against a hand-rolled fake

### tenants/
- `gateway/internal/tenants/config.go` — `TenantConfig` value-typed struct
- `gateway/internal/tenants/loader.go` — `atomic.Pointer[snapshot]` loader + `pgTimeToClock` + `coerceDataClass` + `CheckSensitivePeakInvariant`
- `gateway/internal/tenants/listen.go` — `pgxlisten` handler for `tenants_changed`
- `gateway/internal/tenants/loader_test.go` — 7 unit tests (nil snapshot, Refresh population, TZ fallback, list error propagation, invariant detection, two helper table tests)

### schedule/
- `gateway/internal/schedule/window.go` — `InWindow` pure function with wrap-around
- `gateway/internal/schedule/policy.go` — `DecideUpstreamTier` + `Tier0`/`Tier1` constants
- `gateway/internal/schedule/window_test.go` — table-driven coverage of normal-range + wrap-around incl. inclusive/exclusive boundary edges
- `gateway/internal/schedule/policy_test.go` — 5 cases: 24/7 Tier0, peak in-window Tier0, peak off-hours Tier1, nil-Location fail-open, wrap-around window correctness

### obs/
- `gateway/internal/obs/metrics.go` — appended 11 Phase 4 collectors (6 quota/tenants/schedule + 5 billing/admin consolidated here per Wave 2 file-ownership rule so Plan 04-05 does not need to touch metrics.go)

## Public API Quick Reference (for Plan 04-06 / 04-07 consumers)

```go
// gateway/internal/quota
func CheckBuckets(ctx context.Context, rdb redis.UniversalClient,
    tenantID, routeClass string,
    rpsCapacity, rpmCapacity int,
    rpsRatePerMs, rpmRatePerMs float64,
    requested int, nowMs int64,
) (BucketResult, error)
func BucketKeyPrefix(tenantID, routeClass string) string
type BucketResult struct{ Allowed bool; RemRPS, ResetRPSms, RemRPM, ResetRPMms int; FailedWindow string }
type RouteClass string
const (RouteClassChat RouteClass = "chat"; RouteClassEmbed = "embed"; RouteClassSTT = "stt")
type BucketConfig struct{ RPSCapacity, RPMCapacity int }
func (BucketConfig) RPSRefillPerMs() float64
func (BucketConfig) RPMRefillPerMs() float64
type QuotaLimits struct{ DailyTokens, MonthlyTokens int64; DailyAudioMinutes, MonthlyAudioMinutes, DailyEmbeds, MonthlyEmbeds int }
func NewQuotaChecker(q countersQueries /* = gen.New(pool) */, log *slog.Logger) *QuotaChecker
func (*QuotaChecker) CheckQuotaToday(ctx context.Context, tenantID uuid.UUID, lim QuotaLimits) error
func (*QuotaChecker) CheckQuotaMonth(ctx context.Context, tenantID uuid.UUID, lim QuotaLimits) error
func ErrorCode(err error) string

// Sentinels (from 04-01 errors.go, unchanged):
var ErrRateLimitRPS, ErrRateLimitRPM,
    ErrQuotaExceededDailyTokens, ErrQuotaExceededDailyAudioMinutes, ErrQuotaExceededDailyEmbeds,
    ErrQuotaExceededMonthlyTokens, ErrQuotaExceededMonthlyAudioMinutes, ErrQuotaExceededMonthlyEmbeds,
    ErrQuotaCheckUnavailable error
```

```go
// gateway/internal/tenants
type TenantConfig struct{ /* ID, Slug, Name, DataClass, Status, Mode, PeakWindowStart/End, ScheduleTimezone, Location,
     DailyQuota*, MonthlyQuota*, RPSLimit, RPMLimit */ }
func NewLoader(ctx context.Context, pool *pgxpool.Pool, defaultTZ *time.Location, log *slog.Logger) (*Loader, error)
func (*Loader) Refresh(ctx context.Context) error
func (*Loader) Get(tenantID uuid.UUID) (TenantConfig, error)
func (*Loader) GetBySlug(slug string) (TenantConfig, error)
func (*Loader) All() []TenantConfig
func (*Loader) CheckSensitivePeakInvariant(ctx context.Context) error
func ListenAndReload(ctx context.Context, dsn string, loader *Loader, log *slog.Logger) error
var ErrTenantNotFound, ErrSensitivePeakInvariant error
```

```go
// gateway/internal/schedule
const (Tier0 = 0; Tier1 = 1)
func InWindow(now, start, end time.Time) bool
func DecideUpstreamTier(cfg tenants.TenantConfig, now time.Time) int
var ErrOffHoursUpstreamUnavailable error
```

## Decisions Made

- **pgtype.Time handling:** The plan's proposed loader code used `r.PeakWindowStart.Time` + `.Valid`, which do not exist on `pgtype.Time` (that type is `{Microseconds int64; Valid bool}`). Adapted with a new `pgTimeToClock` helper that converts microseconds to a `time.Time` whose Hour/Minute are the time-of-day. Documented in deviations.
- **DataClass normalization:** sqlc's generated `GetTenantConfigRow` / `ListTenantsForLoaderRow` types declare `DataClass interface{}` (the ENUM arrives as string or []byte from pgx). Added a `coerceDataClass(v interface{}) string` helper inside `tenants/loader.go` to avoid leaking the interface type across the package boundary.
- **Metric wiring deferred:** `counters.go` deliberately does NOT import `obs`. The plan's proposed code called `obs.GatewayQuotaCheckFailures.WithLabelValues(...)` inside the checker. I moved that responsibility to the middleware (Plan 04-06) — the checker logs the failure via slog and returns the sentinel; the middleware is the layer that owns metrics + HTTP response. This keeps `quota/counters.go` free of circular-import risk and matches the plan's own statement that metric instrumentation is Plan 04-06 wiring.
- **Zero-limit disables dimension:** If a tenant has `DailyTokens == 0`, the checker treats that as "dimension unused" (skips the comparison) rather than "limit of 0 tokens" (always blocked). Keeps partially-migrated tenants usable — they would otherwise be locked out by any seed row that leaves a quota column at default zero.
- **Wrap-around semantics + fail-open on nil Location:** `DecideUpstreamTier` fails open to Tier0 when Location is nil — routing a tenant with an unknown timezone to paid external infra would be unsafe. This preserves "failover invisible" on the GPU side during a misconfigured-tenant boot.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Plan's loader.go referenced non-existent `pgtype.Time.Time` / `.Valid` fields**

- **Found during:** Task 2 (writing loader.go)
- **Issue:** The plan's verbatim loader code had `if r.PeakWindowStart.Valid { cfg.PeakWindowStart = r.PeakWindowStart.Time }`. But `pgtype.Time` is `{Microseconds int64; Valid bool}` — no `.Time` field exists. Compiling the plan as-written would have failed on the first `go build`.
- **Fix:** Replaced with a new helper `pgTimeToClock(pgtype.Time) time.Time` that reads `Microseconds`, derives Hour + Minute, and returns a `time.Date(1,1,1,h,m,0,0,time.UTC)` placeholder. Callers only read Hour/Minute (via `InWindow`). Added a table-test `TestPgTimeToClock` covering Valid=false, midnight, 08:00, 22:30.
- **Files modified:** `gateway/internal/tenants/loader.go`, `gateway/internal/tenants/loader_test.go`
- **Verification:** `go build`, `go vet`, `go test -race ./internal/tenants/...` all green.
- **Committed in:** e19cd26 (Task 2 commit)

**2. [Rule 1 - Bug] Plan's loader.go referenced `r.DataClass` as a string without coercing sqlc's interface{} type**

- **Found during:** Task 2 (writing loader.go)
- **Issue:** `GetTenantConfigRow.DataClass` and `ListTenantsForLoaderRow.DataClass` are typed `interface{}` by sqlc (they map to the `ai_gateway.data_class` ENUM). Assigning directly to `cfg.DataClass` (string) would fail to compile.
- **Fix:** Added `coerceDataClass(v interface{}) string` helper that handles `string` / `[]byte` / nil / other via switch. Added `TestCoerceDataClass` table test.
- **Files modified:** `gateway/internal/tenants/loader.go`, `gateway/internal/tenants/loader_test.go`
- **Verification:** `go build` passes; test coverage confirms the expected shapes.
- **Committed in:** e19cd26 (Task 2 commit)

**3. [Rule 3 - Blocking] counters.go cannot reference obs.GatewayQuotaCheckFailures before it exists (task ordering)**

- **Found during:** Task 1 (writing counters.go)
- **Issue:** The plan's counters.go called `obs.GatewayQuotaCheckFailures.WithLabelValues(...)`, but the collector is only declared in Task 2's obs/metrics.go edit. Committing Task 1 before Task 2 would produce an unresolved symbol.
- **Fix:** Removed the obs import from counters.go entirely. The checker now just `log.Warn`s the failure and returns the sentinel. Per plan intent, metric instrumentation is the middleware's responsibility (Plan 04-06). This also eliminates any future risk of a cyclic import between `quota` and `obs`.
- **Files modified:** `gateway/internal/quota/counters.go`
- **Verification:** `go build ./internal/quota/...` passes; `go test -race` green.
- **Committed in:** 9efcb5e (Task 1 commit)

**4. [Rule 2 - Missing Critical] Zero-value QuotaLimits dimensions would have silently locked out every tenant**

- **Found during:** Task 1 (writing counters.go — noticed while writing the failing-test for "below limit")
- **Issue:** A literal reading of the plan's comparisons (`row.TokensIn + row.TokensOut >= lim.DailyTokens`) means: if a tenant is migrated with `daily_quota_tokens=0` (plausible during seed rollout or a misconfigured gatewayctl invocation), the very first request returns `ErrQuotaExceededDailyTokens`. This is exactly the "runaway block" D-A2 is meant to prevent on the failure side, but here it's a correctness issue on the configuration side.
- **Fix:** Guarded each comparison with `if lim.DailyTokens > 0 && ...`. Zero means "dimension unused" rather than "limit of 0". Added `TestCheckQuotaToday_ZeroLimitDisablesDimension` to enforce.
- **Files modified:** `gateway/internal/quota/counters.go`, `gateway/internal/quota/counters_test.go`
- **Verification:** Test green; the inverse test (at-limit still trips) also green.
- **Committed in:** 9efcb5e (Task 1 commit)

**5. [Rule 1 - Bug] Plan's loader_test.go tried to construct `&tenants.Loader{}` externally**

- **Found during:** Task 2 (writing loader_test.go)
- **Issue:** The plan sketch used `package tenants_test` and `l := &tenants.Loader{}`, but `Loader` has unexported fields (`pool`, `q`, `snap`, `log`, `defaultTZ`). External construction would break once the struct is fleshed out.
- **Fix:** Moved the test file to `package tenants` (internal test). Still covers the nil-snapshot Get/GetBySlug/All cases (plus Refresh-population and error-path cases against a fake `loaderQueries`).
- **Files modified:** `gateway/internal/tenants/loader_test.go`
- **Verification:** `go test -race` green with full coverage of loader's public API.
- **Committed in:** e19cd26 (Task 2 commit)

---

**Total deviations:** 5 auto-fixed (3 Rule 1 bug, 1 Rule 2 missing-critical, 1 Rule 3 blocking)
**Impact on plan:** All five are essential: (1)+(2)+(5) are compile bugs in the plan's verbatim Go snippets that would have blocked `go build`; (3) is a task-ordering blocker; (4) is a correctness bug that would have silently locked out freshly-migrated tenants. None expand scope — the public contract remains exactly what downstream Plans 04-05/04-06/04-07 expect.

## Issues Encountered

- **`pgtype.Time` vs Stripe Lua compile-time fit:** Spent <2 min disambiguating the pgtype vs `sql.NullTime` shapes before writing the helper. Documented in the loader doc-comment so Plan 04-07 (gatewayctl) doesn't re-encounter.
- **miniredis Lua engine parity:** miniredis/v2 (yuin/gopher-lua backend) supports the `setex` / `tonumber` / `math.floor` ops the script uses. No semantic drift observed in the 4 tests; the 1000-goroutine SC-5 scenario still requires a real Redis container (Plan 04-08).

## User Setup Required

None — all changes are internal-package Go code. No new env vars, no new migrations, no external service configuration. The 11 new prom collectors appear automatically on the existing `/metrics` endpoint once the gateway binary includes this code (i.e. they are live in this commit; scraping will ingest them without any Grafana/Prometheus config change — cardinality is bounded because tenant labels will be slugs from the 6-tenant universe).

## Next Phase Readiness

- **Plan 04-05 (billing/admin/proxy):** Unblocked — its GatewayBillingFlush / GatewayBillingFlushFailures / GatewayBillingFlushDropped / GatewayPricesReload / GatewayAdminRequests collectors are already defined here; 04-05 will consume them via `.WithLabelValues(...).Inc()` without touching `obs/metrics.go` (avoiding the same-wave file conflict by design).
- **Plan 04-06 (middleware chain):** Unblocked — the `CheckBuckets` Lua wrapper, `QuotaChecker`, `tenants.Loader`, and `schedule.DecideUpstreamTier` are the four new hot-path primitives it wires into chi. All collectors the middleware will touch already exist.
- **Plan 04-07 (gatewayctl + admin handlers):** Unblocked for the tenants parts — `tenants.Loader.All()` gives the listing view; the public TenantConfig shape is stable.
- **Plan 04-08 (integration tests):** The SC-5 1000-goroutine harness has a stable API target (`quota.CheckBuckets`); the tenants hot-reload test has a stable API target (`tenants.ListenAndReload` + `loader.Refresh` + `loader.Get`).

## Self-Check: PASSED

Verified by Read/Bash at end of execution:

- 15 files in files_modified are present:
  - 6 quota files (lua.go, scripts/token_bucket.lua, lua_test.go, bucket.go, counters.go, counters_test.go)
  - 4 tenants files (config.go, loader.go, listen.go, loader_test.go)
  - 4 schedule files (window.go, window_test.go, policy.go, policy_test.go)
  - 1 obs file modified (metrics.go)
- Lua script atomicity verified: the bucket script does its read/decision/write in a single EVAL — confirmed by `grep -c 'redis.call("setex"' → 4` (4 setex calls in the commit-path, all inside the same script body; Redis is single-threaded during Lua execution).
- `tenants.ListenAndReload` mirrors `upstreams/listen.go`: `grep -c "tenants_changed" gateway/internal/tenants/listen.go → 5` (include: `.Handle("tenants_changed", …)` + log fields + start log + payload-source field).
- `schedule.InWindow` + `DecideUpstreamTier` exist: grep confirms both `func InWindow` (1) and `func DecideUpstreamTier` (1).
- 11 Phase 4 collectors present: `grep -cE "^var Gateway(RateLimitRejected|RateLimitCheckFailures|QuotaRejected|QuotaCheckFailures|ScheduleRouting|TenantsReload|BillingFlush|BillingFlushFailures|BillingFlushDropped|PricesReload|AdminRequests) " gateway/internal/obs/metrics.go → 11`.
- `go test -short -race ./internal/quota/... ./internal/tenants/... ./internal/schedule/... ./internal/obs/...` exits 0.
- `go vet ./...` on the full gateway module exits 0.
- `gofmt -l ./internal/quota ./internal/tenants ./internal/schedule ./internal/obs` prints nothing.
- Commits 9efcb5e + e19cd26 present in `git log` between base 3f0cb42 and HEAD.

---

*Phase: 04-multi-tenant-quotas-billing-schedule-routing*
*Plan: 04 (Wave 2 — split A)*
*Completed: 2026-04-21*
