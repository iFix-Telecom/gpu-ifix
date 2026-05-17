---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 03
subsystem: infra
tags: [go, fsm, redis, atomic, mirror, emergency-pod, redsync, pubsub]

# Dependency graph
requires:
  - phase: 06-01
    provides: emerg sentinel errors + Namespace const + redsync v4.16.0 in go.mod + obs.GatewayEmergencyState gauge
  - phase: 03
    provides: redisx/breaker.go template (Hash + Pub/Sub + 2s timeout)
  - phase: 05
    provides: shed/fsm.go template (atomic.Int32 + CAS + onChange + obs gauge)
provides:
  - emerg.FSM (7-state in-process state machine with atomic CAS, ParseState, SetState, onChange hook)
  - redisx.WriteEmergState / PublishEmergEvent / SubscribeEmergEvents (cross-replica mirror surface)
  - redisx.NewEmergRedsync factory (single point of truth for go-redsync v4 + goredis/v9 adapter)
  - redisx.EmergStateKey / EmergLockKey / EmergEventsChannel namespace constants
affects: [06-04 reconciler, 06-05 trigger evaluation, 06-06 lifecycle goroutine, 06-07 leader recovery, 06-08 gatewayctl emerg]

# Tech tracking
tech-stack:
  added: []  # all dependencies (redsync v4.16.0, miniredis v2.37.0, sentry-go v0.29.1) already in go.mod
  patterns:
    - "7-state FSM = atomic.Int32 + atomic.Int64 enteredAt + lockless State()/EnteredAt() reads"
    - "Transition CAS: Compare-And-Swap on (from, to); CAS-failure = silent noop (next tick re-evaluates)"
    - "SetState (forced, retry-loop CAS) for leader-recovery resume; Transition (caller-driven) for ticker"
    - "ParseState round-trip helper for resuming FSM from JSONB events log (Plan 07 dep)"
    - "obs gauge per transition: 1 on new state, 0 on every other label (7 Set() calls)"
    - "sentry.AddBreadcrumb on every transition (Category=emerg, Level=Info, Data={reason})"
    - "redisx mirror = single Hash key (5 fields) — NOT one-Hash-per-upstream like shed (PRV-05 invariant)"
    - "NewEmergRedsync factory wraps redsyncredis/v9 NewPool + redsync.New so callers stay namespace-clean"
    - "Reuse redisOpTimeout from shed.go (no redeclaration, package-level const shared)"
    - "Nil-rdb wiring guard returns fail-fast error (matches breaker.go + shed.go convention)"

key-files:
  created:
    - "gateway/internal/emerg/fsm.go (291 LOC: FSM + State + ParseState + Transition + SetState + commit side effects)"
    - "gateway/internal/emerg/fsm_test.go (308 LOC: 9 tests under -race covering all 7 states + CAS race + callback + gauge)"
    - "gateway/internal/redisx/emerg.go (159 LOC: namespace + EmergEvent + Write/Publish/Subscribe + NewEmergRedsync)"
    - "gateway/internal/redisx/emerg_test.go (186 LOC: 7 tests under -race using miniredis + redsync mutex contention)"
  modified: []

key-decisions:
  - "FSM struct does NOT carry cfg atomic.Pointer[Config] (Phase 6 tunables come from env vars only — no hot-reload surface). Add cfg in v2 if per-tenant emergency config arrives."
  - "SetState uses a CAS retry-loop (not single CAS) to converge regardless of in-flight tick-driven transitions — leader-recovery MUST succeed."
  - "Same-state Transition / SetState calls are filtered (no callback fire, no log emission) so callers can call unconditionally during resume without spurious events."
  - "Single Hash key gw:emerg:state (5 fields) instead of per-upstream Hashes (shed pattern) — PRV-05 guarantees ≤1 live emergency lifecycle."
  - "redisOpTimeout REUSED from shed.go (not redeclared). Diverging here would create silent invariant drift if shed.go ever raises the value."
  - "NewEmergRedsync wraps the goredis/v9 adapter so reconciler (Plan 04) imports only redisx + redsync, never redsyncredis/v9 directly."

patterns-established:
  - "Pattern: 7-state FSM with atomic CAS — sealed package-level const var allStates drives both gauge-reset loop + tests"
  - "Pattern: ParseState helper alongside String() for wire-format round-trip (mandatory for any FSM that persists state via JSONB)"
  - "Pattern: SetState retry-loop for forced transitions vs Transition single-CAS for caller-driven — semantic split prevents accidental forced overwrites"
  - "Pattern: commitTransitionSideEffects helper shared by Transition and SetState — single source of truth for gauge/log/breadcrumb/callback"
  - "Pattern: NewXxxRedsync factory in redisx package — keeps redsyncredis/v9 import isolated to one file"

requirements-completed: [PRV-02]

# Metrics
duration: 7min
completed: 2026-05-13
---

# Phase 6 Plan 03: emerg FSM 7-state + redisx mirror foundations Summary

**7-state in-process FSM (atomic CAS + onChange + ParseState + SetState) + redisx/emerg.go (Hash mirror + Pub/Sub + redsync factory) — the two foundation pieces Plans 04-08 build on.**

## Performance

- **Duration:** 7 min
- **Started:** 2026-05-13T22:00:01Z
- **Completed:** 2026-05-13T22:07:13Z
- **Tasks:** 2 (both TDD: RED → GREEN, no REFACTOR needed)
- **Files modified:** 4 (all new)

## Accomplishments

- 7-state emergency FSM (HEALTHY → DEGRADED → FAILED_OVER → EMERGENCY_PROVISIONING → EMERGENCY_ACTIVE → RECOVERING → COOLDOWN) with lockless atomic.Int32 hot-path reads
- ParseState helper resolving Plan 07's BLOCKER 4 dependency (resumeFSMFromEvents)
- SetState retry-loop CAS for forced state resumption during leader recovery
- redisx/emerg.go single-Hash mirror surface (5 fields) + Pub/Sub + redsync v4 factory
- Nil-rdb wiring guards on every helper (fail-fast at test time)
- 16 new tests passing under `-race` flag (9 FSM + 7 redisx)
- Shared `redisOpTimeout` const reused from shed.go (no divergence risk)

## Task Commits

Each task was TDD-committed atomically (RED → GREEN):

1. **Task 1 RED: failing FSM tests** — `e644bc4` (test)
2. **Task 1 GREEN: emerg FSM 7-state implementation** — `bc5c442` (feat)
3. **Task 2 RED: failing redisx/emerg tests** — `6388c2d` (test)
4. **Task 2 GREEN: redisx/emerg helpers + redsync factory** — `1681e78` (feat)

_REFACTOR phase skipped — both implementations were minimal-by-design (no extractable duplication; shared logic already encapsulated in `commitTransitionSideEffects`)._

## Files Created/Modified

- `gateway/internal/emerg/fsm.go` — 7-state FSM with atomic CAS, ParseState, SetState, onChange callback, obs gauge, sentry breadcrumbs
- `gateway/internal/emerg/fsm_test.go` — 9 tests under `-race`: state strings, ParseState round-trip, transitions sequence, gauge invariant, onChange capture, invalid CAS, race detector, SetState forced, concurrent State() reads
- `gateway/internal/redisx/emerg.go` — `EmergEventsChannel`, `EmergStateKey()`, `EmergLockKey()`, `EmergEvent` struct, `WriteEmergState`, `PublishEmergEvent`, `SubscribeEmergEvents`, `NewEmergRedsync` factory
- `gateway/internal/redisx/emerg_test.go` — 7 tests under `-race`: namespace constants, HSet round-trip, nil-client guards, Publish/Subscribe round-trip, redsync lock + contention + unlock, cancelled-ctx timeout enforcement

## Decisions Made

All decisions follow CONTEXT.md exactly. Two implementation refinements within Claude's discretion:

1. **`SetState` uses a CAS retry-loop, not single CAS.** Plan 07 leader-recovery MUST converge regardless of in-flight tick-driven transitions — a one-shot CAS could lose the race. The loop re-reads `from` on miss and re-checks `from == to` to avoid infinite spinning.
2. **Shared `commitTransitionSideEffects` helper between `Transition` and `SetState`.** Both call sites need identical post-CAS effects (gauge/log/breadcrumb/callback) so extracting prevents drift.

## Deviations from Plan

None — plan executed exactly as written.

The plan's required test name `TestFSMTransitions` exists. Plan also called for `TestFSMOnChangeCallback`, `TestFSMInvalidTransition`, `TestFSMTransitionCAS`, `TestFSMObsGaugeReset`, all present. Two extra tests added beyond the plan minimum (`TestFSMSetState` for the SetState helper, `TestFSMConcurrentReadDuringTransition` as race-detector smoke) — additions to validate Claude-discretion decisions, not deviations from spec.

## Issues Encountered

1. **Initial gauge-introspection over-engineering:** First draft of `TestFSMObsGaugeReset` rolled a custom `dto.Metric` writer to scrape Prometheus values. Caught during pre-commit review — `prometheus/client_golang/prometheus/testutil.ToFloat64` was already in use elsewhere in the codebase (`gateway/internal/obs/middleware_test.go:113`). Replaced the custom plumbing with the standard helper before committing the RED test. No production code or wasted commits.

## User Setup Required

None — no external service configuration required. Both files are in-process Go code; redsync + miniredis already declared in `go.mod`.

## Next Phase Readiness

**Plan 04 (reconciler) unblocked.** Required imports now available:

```go
import (
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"   // FSM, NewFSM, State enum
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"  // EmergStateKey, EmergLockKey, NewEmergRedsync, etc.
    "github.com/go-redsync/redsync/v4"                          // mutex options
)

fsm := emerg.NewFSM(log, reconciler.publishOnChange)            // onChange wires Pub/Sub + HSet + lifecycle audit
rs := redisx.NewEmergRedsync(rdb)
mtx := rs.NewMutex(redisx.EmergLockKey(),
    redsync.WithExpiry(30*time.Second),
    redsync.WithTries(1),
    redsync.WithRetryDelay(0),
)
```

**Plan 07 (leader recovery) BLOCKER 4 resolved.** `emerg.ParseState(stateStr)` is the API for rebuilding FSM state from `lifecycle.events` JSONB. `emerg.FSM.SetState(target, now, "leader_recovery_resume")` forces convergence.

**No blockers** for parallel Wave 1 plans depending on these foundations.

## Self-Check: PASSED

Verified files exist:
- `gateway/internal/emerg/fsm.go` — FOUND
- `gateway/internal/emerg/fsm_test.go` — FOUND
- `gateway/internal/redisx/emerg.go` — FOUND
- `gateway/internal/redisx/emerg_test.go` — FOUND

Verified commits exist (in git log):
- `e644bc4` (test FSM RED) — FOUND
- `bc5c442` (feat FSM GREEN) — FOUND
- `6388c2d` (test redisx RED) — FOUND
- `1681e78` (feat redisx GREEN) — FOUND

Verification commands passed:
- `cd gateway && go test -race ./internal/emerg/... ./internal/redisx/...` → PASS (16 emerg + redisx new tests, all green)
- `cd gateway && go build ./...` → exit 0 (no regressions in dispatcher, breaker, shed, agent, etc.)

## TDD Gate Compliance

Both tasks followed strict RED → GREEN gates. No GREEN commit landed before its corresponding RED commit failed verifiably. Gate sequence verified in git log:
- Task 1: `e644bc4` (test, build failed as expected) → `bc5c442` (feat, tests pass)
- Task 2: `6388c2d` (test, build failed as expected) → `1681e78` (feat, tests pass)

REFACTOR phase intentionally skipped — both implementations were minimal and Claude-reviewed for duplication before the GREEN commit.

---
*Phase: 06-auto-provisioning-emergency-pod-vast-ai*
*Completed: 2026-05-13*
