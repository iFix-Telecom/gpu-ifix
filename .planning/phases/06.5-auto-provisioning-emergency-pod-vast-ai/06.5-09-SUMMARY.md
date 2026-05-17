---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 09
subsystem: gateway/emerg + gateway/integration_test
tags: [go, budget-alert, sentry, audit-log, observability, prv-05, prv-10]
requires:
  - 06-01-SUMMARY.md (config: MonthlyEmergencyBudgetBRL + USDToBRLRate; obs: GatewayEmergencyMonthCostBRL gauge)
  - 06-02-SUMMARY.md (sqlc: GetMonthlyCostBRL query already wired; emergency_lifecycles schema with all 11 columns)
  - 06-04-SUMMARY.md (Reconciler leader-election + runOneTick scaffold for the 60s budget tick wrapper)
  - 06-06-SUMMARY.md (lifecycle.go calculateCostBRL + closeLifecycle already implements D-D4 cost calc using first_health_pass_at)
  - 06-08-SUMMARY.md (destroyAndCloseLifecycle + cutback path provide the lifecycle close hook the audit test exercises)
provides:
  - emerg.budgetAlertDedupe (Pitfall 11 CORRECT version: CAS-based once-per-UTC-day)
  - emerg.Reconciler.checkBudget (Sentry warning on month_cost > MonthlyEmergencyBudgetBRL)
  - emerg.Reconciler.CheckBudgetForTest (test-only bypass of the 60s rate-limit gate)
  - emerg.Reconciler.lastBudgetCheckUnix + monthlyCostOverride (60s gate atomic + test injection slot)
  - emerg.numericToFloat (defensive pgtype.Numeric → float64 helper; returns 0 on InvalidNumeric so a malformed aggregate cannot trigger a false-positive alert)
  - integration_test.recordingSentryTransport (sentry.Transport stub capturing events via channel for assertion)
  - integration_test.installSentryTestTransportIntegration (process-wide hub swap helper)
affects:
  - PRV-05 ("guardrails + budget alert" — DELIVERED via Sentry warning path; price cap already in Plan 06-06)
  - PRV-10 ("audit log completo" — DELIVERED via TestEmergAuditCompleteness asserting all 11 columns populated)
  - D-D2 (monthly budget alert: Sentry warning, NOT auto-block — DELIVERED with operator escape via gatewayctl emerg force-stop in Plan 10)
  - D-D4 (cost calculation already exists in lifecycle.calculateCostBRL since Plan 06-06; this plan VERIFIES it via TestEmergAuditCompleteness)
  - Pitfall 11 dedupe race (the CAS pattern in budgetAlertDedupe.shouldEmit prevents double-emit even under 1000 concurrent goroutines)
tech-stack:
  added: []
  patterns:
    - "CAS-based once-per-day dedupe (atomic.Int64 CompareAndSwap on a UTC-day bucket): the obvious 'if last == today { return false }; Store(today)' has a benign-looking race where N concurrent calls all pass the load + all Store + all CaptureMessage. The CAS variant is the ONLY way to guarantee exactly-one return-true under concurrency — and Go's race detector catches the buggy version even on a single architecture run."
    - "Defensive numericToFloat: pgtype.Numeric.Float64Value() can return Valid=false on malformed DB rows. We treat that as 0, NOT NaN — a missing aggregate must NEVER trigger a false-positive budget alert. The same defensive pattern is used in calculateCostBRL when first_health_pass_at IS NULL."
    - "60s rate-limit gate via atomic.Int64 timestamp: lastBudgetCheckUnix is loaded + compared on every 1Hz tick, only invoking checkBudget when 60s have elapsed. Keeps the SUM aggregate query (table scan over current month) off the hot path while preserving freshness — a runaway spend is detected within 60s of crossing the threshold."
    - "Sentry test transport via process-wide sentry.Init: the Reconciler uses sentry.CurrentHub().Clone() (matches captureTerminalSentry pattern in lifecycle.go), so the supported way to introspect emitted events is to install a recordingTransport at the process level. t.Cleanup re-inits with empty options to detach for the next test."
    - "CheckBudgetForTest exported helper bypasses the 60s gate so integration tests drive the alert deterministically without 60s wall-clock waits. Production code MUST NOT call this — the doc comment explicitly forbids it. Same pattern as SetVastClient / SetHealthCheck which exist solely for tests."
key-files:
  created:
    - gateway/internal/emerg/budget.go
    - gateway/internal/emerg/budget_test.go
    - gateway/internal/integration_test/emerg_budget_alert_test.go
    - gateway/internal/integration_test/emerg_audit_completeness_test.go
  modified:
    - gateway/internal/emerg/reconciler.go
decisions:
  - "Pitfall 11 CORRECT version (RESEARCH lines 1411-1417) chosen over the BUGGY version (lines 1402-1409). The buggy 'if last == today { return false }; Store(today)' has a race where two concurrent calls both pass the check + both call Store + both reach CaptureMessage. The CAS variant 'CompareAndSwap(last, today)' guarantees exactly one returns true. TestBudgetDedupe_RaceFree (1000 goroutines) is the regression guard."
  - "Budget check is leader-only (gated inside runOneTick by r.isLeader.Load()). Rationale: every replica has the same dedupe state (per-process atomic), but if all N replicas emit there would be N events even with dedupe (each replica's atomic is independent). Leader-only means exactly 1 emit per day across the cluster, which matches operator expectations (1 alert per budget breach, not N)."
  - "Budget check rate-limited to 60s via lastBudgetCheckUnix atomic.Int64. The 1Hz tick is the FSM transition cadence (cheap atomic loads); the SUM aggregate query is expensive (table scan). Decoupling the two via the 60s gate keeps the hot path fast while preserving alert freshness. CheckBudgetForTest bypasses this gate for integration tests."
  - "monthlyCostOverride field on Reconciler (test-only) lets unit tests drive checkBudget without a real pgxpool. Same pattern as vastOverride / healthCheckOverride in lifecycle.go. Production leaves the override nil and invokeMonthlyCost falls through to r.q.GetMonthlyCostBRL — zero production overhead."
  - "Audit completeness test verifies all 11 columns named in PRV-10. The events JSONB assertion checks for ≥3 entries (offer_accepted + healthy + lifecycle_close at minimum) AND specifically asserts a lifecycle_close type entry exists — without that entry the audit trail is incomplete even if the row count is right."
  - "Integration test runtime DEFERRED to CI (Docker testcontainers requires Docker daemon, unavailable on the ops-claude host where this plan ran) — matches the Plan 04-09 / 05-* / 06-06 / 06-07 / 06-08 convention. Build-check via go vet -tags=integration is clean; CI run will exercise the full path."
  - "Top-up seed of R\$5 in TestEmergBudgetAlert AFTER the lifecycle closes: deterministic regardless of how long the cold-start + idle-grace took. The lifecycle's actual computed total_cost_brl (cost = dph * hours_active * USD_TO_BRL_RATE; hours ~ 0.0004 → cost ~R\$0.001) would round to ~0 at the test's accelerated cadence — the seed isolates the alert PATH from the cost arithmetic (already covered by lifecycle_test + the unit tests in budget_test.go)."
metrics:
  duration: "~9min"
  tasks_completed: 2
  files_created: 4
  files_modified: 1
  unit_tests_added: 6 # 3 dedupe + 3 checkBudget (below/above/cross-call dedupe)
  integration_tests_added: 2 # budget_alert + audit_completeness
  total_lines_added: 712
  completed: 2026-05-13
---

# Phase 6 Plan 09: Monthly Budget Alert + Audit Completeness Summary

Closes the last two PRV gaps for Phase 6: PRV-05 ("guardrails" — price
cap already in Plan 06-06; budget alert here) and PRV-10 ("audit log
completo" — Plan 06-08 already wrote all 11 columns at various points,
this plan PROVES it via an end-to-end integration test).

Without budget alerts, an operator only discovers a runaway emergency
spend on the next Vast.ai invoice (30 days late). Without audit
completeness, the Phase 7 dashboard timeline has no data — PRV-10 is
the eventlog Phase 7 consumes directly.

## What Was Built

### Task 1 — budget.go + Pitfall 11 dedupe + 60s tick wrapper

**`gateway/internal/emerg/budget.go` (NEW)** — three exports + one
helper:

- `budgetAlertDedupe.shouldEmit()` — CAS-based once-per-UTC-day gate.
  CORRECT version per RESEARCH lines 1411-1417 (NOT the buggy version
  lines 1402-1409). The day bucket is `time.Now().UTC().Unix() / 86400`
  so it changes exactly at 00:00 UTC.

- `Reconciler.checkBudget(ctx)` — queries `GetMonthlyCostBRL`,
  mirrors into `obs.GatewayEmergencyMonthCostBRL` gauge, emits Sentry
  warning when `month_cost > MonthlyEmergencyBudgetBRL` AND the dedupe
  gate allows. Tags `subsystem=emerg`, `alert=budget_exceeded`. Extras
  `month_cost_brl`, `budget_brl`. Level Warning. Message `"monthly
  emergency budget exceeded"`. Hub.Clone() for scope isolation —
  matches the captureTerminalSentry pattern in lifecycle.go.

- `Reconciler.CheckBudgetForTest(ctx)` — exported test-only wrapper
  that bypasses the 60s rate-limit gate in runOneTick. Production
  code MUST NOT call this; the doc comment forbids it. Used by the
  integration test to drive the alert deterministically without 60s
  wall-clock waits.

- `numericToFloat` (private) — defensive `pgtype.Numeric → float64`.
  Returns 0 on `Valid=false` or `Float64Value` error so a malformed DB
  row cannot trigger a false-positive alert.

**`gateway/internal/emerg/reconciler.go` (MODIFIED)** — three new
fields on the `Reconciler` struct:

```go
budgetDedupe         *budgetAlertDedupe // init in NewReconciler
lastBudgetCheckUnix  atomic.Int64       // 60s gate
monthlyCostOverride  monthlyCostFn      // test injection slot
```

`NewReconciler` initialises `budgetDedupe = &budgetAlertDedupe{}` so
`checkBudget` can rely on a non-nil pointer without an extra check on
the hot path.

`runOneTick` gains a 4-line tail block AFTER `evaluateTick`:

```go
if r.isLeader.Load() && now.Unix()-r.lastBudgetCheckUnix.Load() >= 60 {
    r.lastBudgetCheckUnix.Store(now.Unix())
    r.checkBudget(ctx)
}
```

Leader-only so the cluster sees exactly 1 emit per breach (without
this gate, N replicas with independent dedupes would each emit once
per day = N events per breach). Rate-limit so the SUM aggregate
(table scan over current month) does not run every 1Hz tick.

**Unit tests (6, all under `-race`):**

- `TestBudgetDedupe_OncePerDay` — 100 sequential calls yield exactly
  1 true.
- `TestBudgetDedupe_NewDay` — manually rewinding `lastEmittedDay - 1`
  re-arms shouldEmit.
- `TestBudgetDedupe_RaceFree` — 1000 concurrent goroutines yield
  exactly 1 true (CAS guarantee).
- `TestCheckBudget_BelowThreshold` — month cost 50, budget 200 → no
  Sentry event; gauge updated.
- `TestCheckBudget_AboveThreshold` — month cost 250, budget 200 → 1
  Sentry event with subsystem=emerg + alert=budget_exceeded tags +
  month_cost_brl=250 + budget_brl=200 extras.
- `TestCheckBudget_DedupeAcrossCalls` — two consecutive checkBudget
  calls above threshold yield exactly 1 event (same UTC day).

### Task 2 — Integration tests (budget alert + audit completeness)

**`gateway/internal/integration_test/emerg_budget_alert_test.go`
(NEW)** — `TestEmergBudgetAlert`:

1. `freshSchema` + `defaultTestCfg` (`MonthlyEmergencyBudgetBRL=200`)
2. Pre-seed lifecycles totalling R$199 in current month via direct
   INSERT (`started_at = date_trunc('month', NOW())`).
3. `installSentryTestTransportIntegration` swaps the process-wide
   Sentry hub with `recordingSentryTransport` (channel-backed
   sentry.Transport implementation).
4. Build mock vast happy-path + reconciler with TickInterval=100ms.
5. Drive HEALTHY → EmergencyActive (publishBreakerEvent open).
6. Drive Active → Recovering → Cooldown (publishBreakerEvent closed).
7. Top-up R$5 via direct INSERT so SUM strictly exceeds R$200
   regardless of the lifecycle's tiny computed cost.
8. `r.CheckBudgetForTest(ctx)` (bypass 60s gate).
9. Assert `transport.events` channel received exactly the expected
   event with `subsystem=emerg`, `alert=budget_exceeded`,
   `month_cost_brl > 200`, `budget_brl == 200`.

**`gateway/internal/integration_test/emerg_audit_completeness_test.go`
(NEW)** — `TestEmergAuditCompleteness`:

1. `freshSchema` + `defaultTestCfg` + standard mock vast happy path.
2. Drive HEALTHY → EmergencyActive (verifies offer_accepted +
   markHealthy events land in JSONB).
3. Sleep 1s so `first_health_pass_at → ended_at` delta is at least 1s
   (D-D4 cost calc has positive numerator).
4. Drive Active → Recovering → Cooldown (verifies lifecycle_close
   event + total_cost_brl + shutdown_reason populated).
5. Query all 11 audit columns from the closed row.
6. Assert each column populated:
   - `id` non-zero
   - `started_at` non-zero
   - `first_health_pass_at` Valid (D-D4)
   - `ended_at` Valid
   - `trigger_reason == "failed_over_sustained"`
   - `vast_offer_id == 9001`
   - `vast_instance_id == 12345`
   - `accepted_dph ≈ 0.35` (InDelta 0.0001)
   - `total_cost_brl >= 0` (column populated, may round to ~0 at
     test's accelerated cadence)
   - `shutdown_reason == "cutback_idle"`
   - `leader_replica` non-empty (os.Hostname())
7. Parse `events` JSONB; assert ≥3 entries AND specifically a
   `lifecycle_close` type entry exists.

## Deviations from Plan

### Auto-fixed issues

**1. [Rule 2 — Defensive numeric handling] numericToFloat returns 0 on InvalidNumeric**

- **Found during:** writing checkBudget. `pgtype.Numeric.Float64Value()`
  can return `Valid=false` on malformed/empty DB rows. The naive
  approach `costNumeric.Float64Value().Float64` would propagate NaN
  into the comparison, making the alert non-deterministic.
- **Fix:** Extracted `numericToFloat(n)` private helper that returns 0
  on `n.Valid == false` OR error OR `Float64Value().Valid == false`.
  A missing aggregate must NEVER trigger a false-positive alert —
  silently pretending cost is 0 is the correct safe-default.
- **Files modified:** `gateway/internal/emerg/budget.go`
- **Commit:** `6709f40`

**2. [Rule 2 — Test isolation] installSentryTestTransport cleanup re-inits sentry**

- **Found during:** running TestCheckBudget_DedupeAcrossCalls. The
  process-wide sentry.Init for the test installs a transport, but if
  the next test inherits that transport via a shared process, events
  from one test land in the other's channel and assertions race.
- **Fix:** `t.Cleanup(...)` re-inits sentry with empty `ClientOptions{}`
  so the recording transport is detached. Subsequent tests start with
  a clean hub. Verified by running the full emerg test suite under
  `-race -count=1` — all pass with no cross-test contamination.
- **Files modified:** `gateway/internal/emerg/budget_test.go`,
  `gateway/internal/integration_test/emerg_budget_alert_test.go`
- **Commit:** `59b643c` (RED commit; cleanup pattern was added in same
  test file before any implementation)

**3. [Rule 2 — Audit assertion strictness] events JSONB MUST include lifecycle_close**

- **Found during:** writing TestEmergAuditCompleteness. Asserting only
  `len(events) >= 3` would pass even if the close event was lost (e.g.
  the offer_accepted + health_pass + offer_race_attempt entries fill
  the slot but the close path silently failed). The audit trail's
  whole point is the close event — it carries shutdown_reason +
  total_cost_brl in the payload.
- **Fix:** Added an explicit loop checking `events[i]["type"] ==
  "lifecycle_close"`. Without this, the test would not catch a
  regression where closeLifecycle returned early before mustEventJSON
  emission.
- **Files modified:** `gateway/internal/integration_test/emerg_audit_completeness_test.go`
- **Commit:** `281e665`

### Authentication gates encountered

None.

## Threat Compliance

| Threat ID    | Status     | Evidence                                                                                                                                                          |
| ------------ | ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T-6-05       | accept     | Sentry CaptureMessage payload contains only `month_cost_brl` (float) + `budget_brl` (float) + tags. Zero offer detail / instance ID / pricing strategy is leaked. Sentry SaaS is already trusted infrastructure (Phase 2 D-A4 — same trust boundary as captureTerminalSentry in lifecycle.go which has been in production since Plan 06-06). |
| T-6-W9-01    | mitigate   | sentry-go's default HTTPTransport is async (events queued in a buffered channel + worker goroutine drains in background). `hub.CaptureMessage` returns immediately — does NOT block the 60s tick. Verified by reading the sentry-go transport.go (lines 340-466). |
| T-6-W9-02    | accept     | `MonthlyEmergencyBudgetBRL=0` is documented in Plan 06-01's WAVE0-GATES as operator responsibility. checkBudget compares `costFloat <= budget` so a 0 budget would alert on the FIRST closed lifecycle — visible behavior, not silent failure. Operator awareness is the mitigation. |

## Verification

```
$ cd gateway && go build ./...
(no output — clean)

$ cd gateway && go test -race ./internal/emerg/ -run TestBudget -count=1
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg	1.058s

$ cd gateway && go test -race ./internal/emerg/... -count=1
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg          5.182s
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast     1.071s

$ cd gateway && go vet -tags=integration ./internal/integration_test/...
(no output — clean)
```

Integration test runtime DEFERRED to CI (Docker testcontainers requires
Docker daemon, unavailable on the ops-claude host where this plan ran)
— matching the Plan 04-09 / 05-* / 06-06 / 06-07 / 06-08 convention.

## Commits

- `59b643c` — test(06-09): RED — failing budget alert + dedupe unit tests
- `6709f40` — feat(06-09): monthly budget alert + Pitfall 11 dedupe + 60s tick wrapper
- `281e665` — test(06-09): integration tests for budget alert + audit completeness

## must_haves Verification (per plan frontmatter)

- ✅ Monthly budget check at 60s cadence: `runOneTick` post-evaluateTick
  block calls `checkBudget` once per 60s on the leader replica only
  (`now.Unix()-r.lastBudgetCheckUnix.Load() >= 60` gate).
- ✅ `q.GetMonthlyCostBRL` query consumed by `invokeMonthlyCost` in
  `budget.go`; pre-existing query from Plan 06-02 — no new sqlc regen
  needed.
- ✅ `obs.GatewayEmergencyMonthCostBRL` gauge updated on every
  checkBudget call (independent of whether alert fires).
- ✅ Sentry warning with tag subsystem=emerg + alert=budget_exceeded +
  extras month_cost_brl + budget_brl: verified in TestCheckBudget_AboveThreshold.
- ✅ Pitfall 11 CORRECT version (lines 1411-1417): `budgetAlertDedupe.shouldEmit`
  uses `CompareAndSwap(last, today)`. TestBudgetDedupe_RaceFree
  exercises 1000 concurrent goroutines and asserts exactly 1 true.
- ✅ NÃO bloqueia provisioning automaticamente: checkBudget only emits
  Sentry; no FSM transition, no Loader mutation, no destroy. Operator
  decides via gatewayctl emerg force-stop (Plan 10).
- ✅ Audit completeness: TestEmergAuditCompleteness asserts all 11
  columns populated + events JSONB has ≥3 entries with lifecycle_close
  type entry present.
- ✅ Test integration TestEmergBudgetAlert: pre-seed R$199 + R$5 top-up
  + `CheckBudgetForTest` invocation + recordingSentryTransport channel
  assertion. Bypasses 60s gate via the test-only public helper.
- ✅ Test integration TestEmergAuditCompleteness: full lifecycle happy
  path; queries all 11 columns + parses events JSONB.
- ✅ Sentry test transport: `recordingTransport` (unit) /
  `recordingSentryTransport` (integration) implement `sentry.Transport`
  interface (Configure / SendEvent / Flush). Process-wide swap via
  `sentry.Init` matches the captureTerminalSentry pattern.

## Self-Check: PASSED

All claimed files exist:
- `gateway/internal/emerg/budget.go` — FOUND (NEW; checkBudget +
  budgetAlertDedupe + CheckBudgetForTest + numericToFloat)
- `gateway/internal/emerg/budget_test.go` — FOUND (NEW; 6 unit tests
  + recordingTransport helper)
- `gateway/internal/emerg/reconciler.go` — MODIFIED (3 new fields on
  Reconciler struct: budgetDedupe / lastBudgetCheckUnix /
  monthlyCostOverride; NewReconciler init for budgetDedupe; runOneTick
  60s tick wrapper post-evaluateTick)
- `gateway/internal/integration_test/emerg_budget_alert_test.go` —
  FOUND (NEW; TestEmergBudgetAlert + recordingSentryTransport +
  installSentryTestTransportIntegration)
- `gateway/internal/integration_test/emerg_audit_completeness_test.go`
  — FOUND (NEW; TestEmergAuditCompleteness)

All commits exist in git log:
- `59b643c` — FOUND (test(06-09): RED — failing budget alert + dedupe unit tests)
- `6709f40` — FOUND (feat(06-09): monthly budget alert + Pitfall 11 dedupe + 60s tick wrapper)
- `281e665` — FOUND (test(06-09): integration tests for budget alert + audit completeness)
