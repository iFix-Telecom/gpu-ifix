---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 05
subsystem: gateway/emerg
tags: [go, pubsub, breaker-subscriber, trigger, fsm, blocker-2-fix]
requires:
  - 06-01-SUMMARY.md (config.ProvisionTriggerFailedOverSeconds)
  - 06-02-SUMMARY.md (emergency_lifecycles table + ListLiveEmergencyLifecycles + InsertEmergencyLifecycle)
  - 06-03-SUMMARY.md (emerg.FSM + redisx.EmergEvent + SubscribeEmergEvents + PublishEmergEvent)
  - 06-04-SUMMARY.md (emerg.Reconciler + IsLeader + Run loop + leader election)
provides:
  - emerg.localLlmTracker (per-replica local-llm breaker mirror)
  - emerg.Subscribe / emerg.SubscribeEmergCommands (Pub/Sub consumer goroutines)
  - emerg.evaluateHealthy (trigger gate; HEALTHY → EMERGENCY_PROVISIONING)
  - emerg.applyEmergCommand (force-provision/force-destroy dispatcher; leader-only)
  - emerg.ActiveLifecycle (in-memory snapshot type — Plan 06 startProvisioning is first writer)
  - integration_test.publishBreakerEvent (helper for Plans 06-08 to drive trigger paths)
affects:
  - PRV-04 ("emergency provisioning trigger fires on local-llm sustained OPEN")
  - SC-1 (sustained local-llm OPEN ≥ threshold → FSM reaches EMERGENCY_PROVISIONING)
  - BLOCKER 2 (gatewayctl force-provision/force-destroy commands now consumed end-to-end)
tech-stack:
  added: []
  patterns:
    - "Per-replica in-memory tracker (atomic.Value + atomic.Int64) — no cross-replica state sync; each replica observes the breaker channel independently"
    - "Idempotent OPEN handling: openSince only set on FIRST CLOSED→OPEN transition; resends do NOT reset the sustained timer"
    - "Reconnect-with-1s-backoff Pub/Sub loop replicated 1:1 from breaker/subscribe.go"
    - "W11 ordering invariant: Subscribe goroutines spawned BEFORE ticker (Pub/Sub at-most-once mitigation)"
    - "Leader-only filter applied BEFORE type switch in applyEmergCommand — no per-type bypass possible"
    - "INSERT-first then transition for force-provision: partial unique index is the safety net at DB layer"
key-files:
  created:
    - gateway/internal/emerg/tracker.go
    - gateway/internal/emerg/tracker_test.go
    - gateway/internal/emerg/subscribe.go
    - gateway/internal/emerg/subscribe_test.go
    - gateway/internal/integration_test/emerg_trigger_test.go
    - gateway/internal/integration_test/emerg_force_command_test.go
  modified:
    - gateway/internal/emerg/reconciler.go
decisions:
  - "Tracker uses atomic.Value (string) + atomic.Int64 instead of mutex — read on every reconciler tick (1 Hz prod / 10 Hz tests), write only on Pub/Sub event (rare). Avoids contention on the hot path."
  - "OPEN event idempotency: openSince only stored on the FIRST OPEN transition (when openSince==0). A duplicate OPEN publish (Pitfall 3 mitigation: pub/sub resend on reconnect) does NOT reset the sustained timer."
  - "Subscribe + SubscribeEmergCommands spawned BEFORE the ticker in Run() — W11 invariant. Pub/Sub is at-most-once with no replay; spawning subscriber-first ensures the SUBSCRIBE registration completes before any state-change publish from the same reconciler's FSM transitions."
  - "Leader-only filter in applyEmergCommand runs BEFORE the type switch so every command type observes identical filtering. No risk of a per-type bypass introducing a subtle PRV-03 violation later."
  - "destroyAndCloseLifecycle ships as a logging-only stub in Plan 06-05 — Plan 06-08 will replace the body with the real Vast.ai destroy_instance + CloseEmergencyLifecycle path. Signature is stable so handleForceDestroy does not change. The integration test for the active-lifecycle force-destroy path is deferred to Plan 06-08 alongside the helper."
  - "Integration test runtime DEFERRED to CI per Phase 4/5 convention (Docker not available on ops-claude host). Build + vet under -tags=integration both clean locally."
metrics:
  duration: "8 min"
  tasks_completed: 3
  files_created: 6
  files_modified: 1
  unit_tests_added: 10
  integration_tests_added: 6
  total_lines_added: 1255
  completed: 2026-05-13
---

# Phase 6 Plan 05: Emergency Trigger + Force-Command Consumer Summary

Detects sustained `local-llm` breaker OPEN via gw:breaker:events Pub/Sub (D-C2 single-signal source) and advances the FSM HEALTHY → EMERGENCY_PROVISIONING when ≥ `PROVISION_TRIGGER_FAILED_OVER_SECONDS` (D-C1 default 120s); also wires the gw:emerg:events consumer so gatewayctl `force-provision` / `force-destroy` commands (Plan 06-10) become functional end-to-end (BLOCKER 2 fix 2026-05-13).

## What Was Built

**localLlmTracker** (`gateway/internal/emerg/tracker.go`, 105 lines):

- `localLlmTracker` struct: `state atomic.Value` (string `"closed" | "half-open" | "open"`) + `openSince atomic.Int64` (unix-seconds at most-recent CLOSED→OPEN).
- `newLocalLlmTracker()` — initialises state="closed", openSince=0.
- `ApplyEvent(ev redisx.BreakerEvent)` — D-C2 filter: drops events where `Upstream != "local-llm"`. On OPEN: store state="open"; only set openSince if currently 0 (idempotent on duplicate publish). On HALF_OPEN/CLOSED: store new state + reset openSince=0.
- `SustainedFailedOverSeconds() int64` — returns `time.Now().Unix() - openSince` when state=="open" AND openSince>0; 0 otherwise. Defensive double-check guards against the (impossible-with-current-code) case where state and openSince are inconsistent.
- `State() string` — read accessor for gatewayctl emerg-state + tests.

**Pub/Sub consumers** (`gateway/internal/emerg/subscribe.go`, 105 lines):

- `(r *Reconciler) Subscribe(ctx)` — consumes `gw:breaker:events`, dispatches to `r.tracker.ApplyEvent(ev)`. Reconnect-with-1s-backoff loop replicated 1:1 from `breaker/subscribe.go`. Malformed JSON dropped with Warn log (Threat T-6-W5-02).
- `(r *Reconciler) SubscribeEmergCommands(ctx)` — consumes `gw:emerg:events`, dispatches to `r.applyEmergCommand`. Same reconnect pattern.

**Reconciler extensions** (`gateway/internal/emerg/reconciler.go`, +175 lines):

- `Reconciler.tracker *localLlmTracker` field — initialised in `NewReconciler`.
- `Reconciler.activeLifecycle atomic.Pointer[ActiveLifecycle]` field — populated by `handleForceProvision` (and Plan 06-06 `startProvisioning` once that lands).
- `ActiveLifecycle{ID, VastInstanceID, StartedUnix}` — exported snapshot type for the in-flight emergency lifecycle.
- `Run(ctx)` — `go r.Subscribe(ctx)` + `go r.SubscribeEmergCommands(ctx)` spawned BEFORE the ticker is constructed (W11 ordering invariant: Pub/Sub is at-most-once with no replay).
- `evaluateTick(ctx, now, log)` upgraded from no-op stub to a state dispatcher; routes `StateHealthy` → `evaluateHealthy`. Other 6 states log at Debug (Plans 06-08 extend).
- `evaluateHealthy(ctx, now, log)` — trigger gate. Reads `tracker.SustainedFailedOverSeconds()`; below threshold → return. Above threshold → D-C5 reconciler check (`q.ListLiveEmergencyLifecycles`); if any live row → log Error + return. Otherwise: `Transition(Healthy, FailedOver, "local_llm_open_sustained")` then `Transition(FailedOver, EmergencyProvisioning, "trigger_failed_over_sustained")`. Plan 06-06 picks up the new state on the next tick to call `startProvisioning`.
- `applyEmergCommand(ctx, ev, log)` — leader-only filter (`!r.isLeader.Load() → return Debug log`) BEFORE the type switch. Cases: `force_provision_request → handleForceProvision`, `force_destroy_request → handleForceDestroy`, `transition|cancel_in_flight|lifecycle_close → return` (visibility-only), `default → Debug log`.
- `handleForceProvision(ctx, ev, log)` — D-C5 pre-check then `InsertEmergencyLifecycle{TriggerReason: "manual_force", LeaderReplica: replicaID}`. On success: store `activeLifecycle` pointer, advance FSM HEALTHY → FAILED_OVER → EMERGENCY_PROVISIONING with reason `"manual_force_provision:<reason>"`.
- `handleForceDestroy(ctx, ev, log)` — when `activeLifecycle == nil` → Warn + return. Otherwise call `destroyAndCloseLifecycle(ctx, lc, "manual")` then transition FSM → COOLDOWN.
- `destroyAndCloseLifecycle(ctx, lc, reason)` — Plan 06-05 ships a logging-only stub. Plan 06-08 will replace the body with the real Vast.ai destroy_instance + CloseEmergencyLifecycle implementation. Signature is stable so `handleForceDestroy` does not need to change.

## Tests

**Unit tests** (10 total, all passing under `-race`):

`internal/emerg/tracker_test.go` (6 tests):
- `TestTracker_OpenSince` — duplicate OPEN does NOT reset openSince (idempotent).
- `TestTracker_OpenToClose` — CLOSED resets openSince → SustainedFailedOverSeconds=0.
- `TestTracker_HalfOpenResets` — HALF_OPEN counts as recovery, resets openSince.
- `TestTracker_IgnoresOtherUpstreams` — local-stt, local-embed, openrouter-* events are dropped (D-C2).
- `TestTracker_SustainedFailedOver` — SustainedFailedOverSeconds arithmetic with openSince in the past.
- `TestTracker_NoSinceWhenClosed` — defensive: state=closed + openSince>0 → returns 0.

`internal/emerg/subscribe_test.go` (4 tests):
- `TestSubscribe_AppliesLocalLlmEvent` — published OPEN → tracker converges to state=open with openSince>0 within 2s.
- `TestSubscribe_IgnoresNonLocalLlm` — published OPEN for local-stt → tracker stays closed.
- `TestSubscribe_MalformedPayloadDoesNotCrash` — raw garbage publish followed by valid OPEN → loop survives, tracker converges (Threat T-6-W5-02).
- `TestSubscribeEmergCommands_NonLeaderIgnores` — non-leader receives force_provision_request → FSM stays HEALTHY, activeLifecycle stays nil.

**Integration tests** (6 total, build+vet clean under `-tags=integration`; runtime deferred to CI):

`internal/integration_test/emerg_trigger_test.go` (3 tests + helper):
- `publishBreakerEvent(t, rdb, upstream, state)` helper.
- `TestEmergTriggerSustained` — leader + sustained OPEN publish → FSM reaches EMERGENCY_PROVISIONING within 5s (PRV-04 / SC-1).
- `TestEmergTriggerTransient` — OPEN → CLOSED at 200ms → FSM stays HEALTHY (transient does not trigger).
- `TestEmergTriggerNoSpawnIfLiveLifecycle` — pre-seeded unclosed lifecycle → D-C5 check blocks trigger; FSM stays HEALTHY despite sustained OPEN.

`internal/integration_test/emerg_force_command_test.go` (3 tests):
- `TestEmergReconcilerHandlesForceProvisionEvent` — leader consumes force_provision_request → 1 lifecycle row INSERTed with trigger_reason=manual_force + FSM advances.
- `TestEmergReconcilerForceProvisionRejectedNonLeader` — 2 reconcilers race → exactly 1 lifecycle row INSERTed (PRV-03 single-leader filter), exactly 1 FSM advanced.
- `TestEmergReconcilerForceDestroyNoOpWhenIdle` — force_destroy with no active lifecycle → no FSM mutation, 0 rows touched.

## Deferred to Plan 06-08

`TestEmergReconcilerHandlesForceDestroyEvent` (active-lifecycle force-destroy end-to-end) is deferred to Plan 06-08, which owns the `destroyAndCloseLifecycle` helper implementation. Plan 06-05 ships a logging-only stub for that helper so the subscriber wiring + leader-only filter + no-op-when-idle path are testable in isolation.

The integration test for Plan 08 should:
1. Pre-seed FSM in EMERGENCY_ACTIVE + populate `r.activeLifecycle` via the lifecycle insert path.
2. Publish `EmergEvent{Type:"force_destroy_request"}`.
3. Within 3s: assert `mockVast.destroyHits == 1` + DB row closed with `shutdown_reason='manual'` + FSM == StateCooldown.

## Deviations from Plan

### Auto-fixed issues

**1. [Rule 2 - Critical] Added `ActiveLifecycle` exported type + `activeLifecycle atomic.Pointer` field on Reconciler**
- **Found during:** Task 3 — `handleForceDestroy` needs a place to read the live lifecycle from, and the plan referenced `r.activeLifecycle.Load()` without specifying where it gets stored.
- **Issue:** Without an `activeLifecycle` field, `handleForceDestroy` cannot resolve the destroy target; without an exported `ActiveLifecycle` type, Plan 06-06's `startProvisioning` has no surface to populate.
- **Fix:** Added `Reconciler.activeLifecycle atomic.Pointer[ActiveLifecycle]` + exported `ActiveLifecycle{ID, VastInstanceID, StartedUnix}` struct. Plan 06-06 writers can populate via `r.activeLifecycle.Store(...)` without a method call.
- **Files modified:** gateway/internal/emerg/reconciler.go
- **Commit:** 3d8bfc0

### Authentication gates encountered

None.

## Threat Compliance

| Threat ID | Status | Evidence |
|-----------|--------|----------|
| T-6-W5-01 (DoS — slow Redis blocks subscriber) | mitigated | Subscribe runs in its own goroutine; reconnect-with-1s-backoff; ps.Channel() is non-blocking. |
| T-6-W5-02 (Tampering — malformed payload) | mitigated | `TestSubscribe_MalformedPayloadDoesNotCrash` proves loop survives raw garbage; subsequent valid event is consumed. |
| T-6-W5-03 (Information disclosure — Debug logs include payload) | accepted | Phase 3 BreakerEvent only contains upstream + state — no PII. Debug log unchanged from breaker/subscribe.go. |

## Verification

```
$ cd gateway && go test -race ./internal/emerg/...
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg	3.254s
?   	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast	[no test files]

$ cd gateway && go build -tags=integration ./...
(no output — clean)

$ cd gateway && go vet -tags=integration ./internal/integration_test/
(no output — clean)
```

Integration test runtime deferred to CI (Docker unavailable on ops-claude host).

## Commits

- `3d8bfc0` — feat(06-05): emerg subscribe + localLlmTracker (PRV-04 Task 1)
- `7d1c3e9` — test(06-05): integration — emerg trigger sustained vs transient (Task 2)
- `b3546e5` — test(06-05): integration — gw:emerg:events force-* commands (BLOCKER 2 / Task 3)

## Self-Check: PASSED

All claimed files exist:
- gateway/internal/emerg/tracker.go — FOUND
- gateway/internal/emerg/tracker_test.go — FOUND
- gateway/internal/emerg/subscribe.go — FOUND
- gateway/internal/emerg/subscribe_test.go — FOUND
- gateway/internal/integration_test/emerg_trigger_test.go — FOUND
- gateway/internal/integration_test/emerg_force_command_test.go — FOUND
- gateway/internal/emerg/reconciler.go — MODIFIED

All commits exist in git log:
- 3d8bfc0 — FOUND
- 7d1c3e9 — FOUND
- b3546e5 — FOUND
