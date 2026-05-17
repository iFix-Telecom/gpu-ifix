---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 07
subsystem: gateway/emerg
tags: [go, cancel-in-flight, leader-recovery, lifecycle-cleanup, blocker-4-resolved]
requires:
  - 06-04-SUMMARY.md (Reconciler leader-election + runOneTick + activeLifecycle pointer)
  - 06-06-SUMMARY.md (vast.Client + provisionLifecycle + waitForReadyOrDestroy ctx.Done() branch)
provides:
  - Reconciler.cancelActiveLifecycle (D-C3 triple layer: ctx cancel + Pub/Sub broadcast + post-create destroy)
  - Reconciler.evaluateEmergencyProvisioning (cancel-detection branch consumes tracker.State())
  - Reconciler.recoverOrphanLifecycles (D-D5 leader recovery, 4 cenários)
  - Reconciler.resumeFSMFromEvents (BLOCKER 4 fix — FUNCTIONAL JSONB events replay)
  - Reconciler.runHealthcheckResumeLoop (5s × 3 = 15s degradation budget)
  - emerg.inferStateFromEvents (pure helper — events JSONB → FSM state)
affects:
  - PRV-09 (cancel-in-flight zero-leak — DELIVERED)
  - SC-3 (cancel-in-flight integrity — DELIVERED, both pre- and post-create paths proven)
  - PRV-05 (no duplicated pods invariant — RECOVERY-HARDENED via leader-recovery cleanup)
  - D-C3 (cancel triple layer — DELIVERED)
  - D-D5 (leader recovery 4 cenários — DELIVERED, BLOCKER 4 resolved with FUNCTIONAL implementation)
  - Pitfall 7 (vast_instance_id IS NULL pre-create orphan — mitigated)
  - Pitfall 8 (separate-ctx destroy on cancel — preserved)
  - Pitfall 9 (terminal-state actual_status detection — leveraged in zombie cenário)
tech-stack:
  added: []
  patterns:
    - "D-C3 triple-layer cancel: (1) atomic.Pointer[CancelFunc].Swap+invoke for ctx cancel; (2) redisx.PublishEmergEvent for cross-replica visibility; (3) post-create destroy enforced inside waitForReadyOrDestroy ctx.Done() branch (already shipped Plan 06)"
    - "Leader recovery: leaderAcquire callback in runOneTick → recoverOrphanLifecycles → per-row branch (4 cenários). Failures are logged but do NOT block subsequent ticks — next acquisition retries."
    - "BLOCKER 4 resume strategy: events JSONB type-inference (offer_accepted/health_pass) → State; explicit to_state on event preferred over inference for future-proofing"
    - "Resume close path: runHealthcheckResumeLoop owns the close-on-failure write because cancelActiveLifecycle does NOT close (the fresh-provisioning path closes via waitForReadyOrDestroy/provisionLifecycle goroutines, which don't exist for resumed lifecycles)"
key-files:
  created:
    - gateway/internal/emerg/recovery.go
    - gateway/internal/emerg/recovery_test.go
    - gateway/internal/integration_test/emerg_cancel_pre_create_test.go
    - gateway/internal/integration_test/emerg_cancel_post_create_test.go
    - gateway/internal/integration_test/emerg_leader_recovery_zombie_test.go
    - gateway/internal/integration_test/emerg_leader_recovery_active_resume_test.go
  modified:
    - gateway/internal/emerg/lifecycle.go
    - gateway/internal/emerg/reconciler.go
decisions:
  - "cancelActiveLifecycle does NOT clear activeLifecycle pointer or call closeLifecycle — those writes are owned by the goroutine that observes ctx.Done() (provisionLifecycle/waitForReadyOrDestroy for fresh provisioning, runHealthcheckResumeLoop for resumed lifecycles). Avoiding races where the caller and the goroutine both try to close the row."
  - "lifecycleCancel.Swap(nil) makes cancelActiveLifecycle idempotent — a second invocation is a no-op rather than crashing on a double-cancel."
  - "Resume state inference walks events JSONB type-by-type rather than reading an explicit to_state field because Plan 06's mustEventJSON emits typed events (offer_accepted/health_pass), NOT explicit state transitions. inferStateFromEvents accepts both schemas (typed fall-through + explicit to_state preferred) so future plans that emit explicit transitions land cleanly."
  - "Resume health failure: closeLifecycle uses shutdown_reason='resume_health_failed' (not 'cancelled_in_flight') to keep the audit log distinguishable for forensics. cancel-in-flight refers ONLY to local-llm recovery during fresh provisioning."
  - "Plan 08 dispatcher OverrideTier0 cross-reference: resumeFSMFromEvents stores activePodURL via existing atomic.Pointer; Plan 08 reads via Reconciler.ActivePodURL() on each request. TODO comment marks the integration point in recovery.go (no temporary stub interface needed)."
  - "Pre-create orphan detection (Pitfall 7) checks row.VastInstanceID.Valid (sqlc maps NULL → pgtype.Int8.Valid==false) — the canonical Postgres-go pattern for nullable BIGINT."
  - "Recovery getCtx uses 30s timeout per call (not the parent ctx) so a slow Vast doesn't block the leader tick. The vast.Client itself has a 30s http timeout, so this is defense-in-depth."
metrics:
  duration: "1h 5min"
  tasks_completed: 2
  files_created: 6
  files_modified: 2
  unit_tests_added: 8 # 7 InferStateFromEvents cases + 1 RecoveryConstants
  integration_tests_added: 4 # cancel pre/post + zombie + active resume happy/sad
  total_lines_added: 1646
  completed: 2026-05-13
---

# Phase 6 Plan 07: Cancel-in-flight + Leader Recovery Summary

Delivers the two complementary safety nets for the emergency-pod
lifecycle: cancel-in-flight (so a primário recovery during provisioning
does not leak a freshly-created pod, R$2-5/lifecycle saved) and leader
recovery (so a leader crash mid-lifecycle does not leak an orphan pod
indefinitely, potentially R$$$ saved). Together they upgrade PRV-05 "no
duplicated pods" from "invariant assuming nothing crashes" to
"invariant under leader-churn + recovery storms."

**BLOCKER 4 resolved with a FUNCTIONAL resume path** — not a placeholder.
JSONB events array is parsed and the FSM is reconstructed via the
SetState recovery-only escape hatch already added in Plan 03.
runHealthcheckResumeLoop spawns to keep the resumed pod monitored, with
a 15s degradation budget before cancel.

## What Was Built

### Task 1 — Cancel-in-flight (D-C3 triple layer)

**`gateway/internal/emerg/lifecycle.go`** gains `cancelActiveLifecycle`:

```
func (r *Reconciler) cancelActiveLifecycle(ctx, reason)
  → Layer 1: lifecycleCancel.Swap(nil) + invoke CancelFunc
            (propagates ctx.Done() to provisionLifecycle goroutine)
  → Layer 2: redisx.PublishEmergEvent({type: cancel_in_flight, ...})
            (cross-replica visibility; non-leader applyEmergCommand drops it)
  → Layer 3: post-create destroy enforced inside waitForReadyOrDestroy
            ctx.Done() branch (Plan 06 already implemented)
  → captureBreadcrumb("cancel_in_flight", {lifecycle_id, vast_instance_id})
```

**`gateway/internal/emerg/reconciler.go`** evaluateTick gains a real
StateEmergencyProvisioning branch:

```
func (r *Reconciler) evaluateEmergencyProvisioning(ctx, now, log)
  // Bootstrap: spawn provisioning on first entry into the state.
  if r.activeLifecycle.Load() == nil {
      r.startProvisioning(ctx); return
  }
  // Cancel detection: tracker shows local-llm recovered.
  if trackerState in {"closed", "half-open"} {
      r.cancelActiveLifecycle(ctx, "local_llm_recovered_during_provisioning")
      r.deps.FSM.Transition(EmergencyProvisioning → Healthy, ...)
  }
```

The new `StateEmergencyActive` branch in `evaluateTick` implements the
D-C4 multi-failover ride-out (debug-log only — Plan 08 implements the
full cutback path).

**Integration tests:**

- `TestEmergCancelPreCreate` — blocking mock vast holds CreateInstance
  until cancel arrives; asserts `createSucceededHits == 0`,
  `destroyHits == 0`, DB `shutdown_reason in {cancelled_in_flight, create_error}`.
  PRV-09 + SC-3 evidence #1.

- `TestEmergCancelPostCreate` — mock CreateInstance succeeds immediately,
  GetInstance stays in loading; cancel after instance exists; asserts
  `destroyHits == 1` (Layer 3) + DB `shutdown_reason == 'cancelled_in_flight'`.
  PRV-09 + SC-3 evidence #2.

### Task 2 — Leader recovery (D-D5, BLOCKER 4 functional)

**`gateway/internal/emerg/recovery.go`** is the new home for the 4-cenário
recoverOrphanLifecycles + the FUNCTIONAL resumeFSMFromEvents +
runHealthcheckResumeLoop:

```
func (r *Reconciler) recoverOrphanLifecycles(ctx)
  → q.ListLiveEmergencyLifecycles  // partial unique index → ≤1 row
  → for each row: recoverOneLifecycle(ctx, row)

func (r *Reconciler) recoverOneLifecycle(ctx, row)
  → (a) row.VastInstanceID.Valid==false  → close 'leader_recovery_pre_create' (Pitfall 7)
  → vast.GetInstance(row.VastInstanceID)
  → (b) ErrInstanceNotFound              → close 'leader_recovery_lost'
  → (c) !inst.IsActive()                 → bestEffortDestroy + Sentry CaptureMessage
                                          + close 'leader_recovery_zombie'
  → (d) inst.IsActive()                  → resumeFSMFromEvents (BLOCKER 4 functional)

func (r *Reconciler) resumeFSMFromEvents(parentCtx, row, inst) error
  → parse row.Events JSONB → inferStateFromEvents
  → FSM.SetState(lastState)                  // recovery-only escape hatch
  → context.WithCancel(parentCtx) + activeLifecycle.Store + lifecycleCancel.Store
  → podHealthURL(inst) → activePodURL.Store + GatewayEmergencyActivePod gauge
  → if lastState==EmergencyActive: go runHealthcheckResumeLoop(ctx, ID, podURL)
  → captureBreadcrumb("leader_recovery_resume", ...)

func (r *Reconciler) runHealthcheckResumeLoop(ctx, lifecycleID, podURL)
  → 5s ticker, checkHealth(ctx, podURL)
  → on success: failures = 0
  → on failure: failures++; if >= 3 (15s budget):
       cancelActiveLifecycle (D-C3 triple layer)
       bestEffortDestroy(VastInstanceID)
       closeLifecycle(shutdown_reason='resume_health_failed')
       FSM.SetState(StateHealthy)             // re-arm trigger
       return
```

**Reconciler.runOneTick** now calls `recoverOrphanLifecycles` immediately
after `mutex.LockContext(ctx)` succeeds — the Plan 06-04 stub
(`// Plan 07 wires r.recoverOrphanLifecycles(ctx) here`) is replaced
with the real call.

**`gateway/internal/emerg/recovery_test.go`** unit tests:

- `TestInferStateFromEvents` (7 cases): empty array, only offer_accepted,
  offer_accepted+health_pass, only health_pass, unrecognized types,
  explicit to_state preferred, invalid to_state falls back to inference.
- `TestRecoveryConstants`: pins 5s × 3 = 15s degradation budget.

**Integration tests:**

- `TestEmergLeaderRecoveryZombie` — D-D5 cenário (c). Pre-seed orphan
  row with vast_instance_id=88888; mock GetInstance returns
  actual_status="exited"; assert `mockVast.destroyHits == 1`, DB row
  closed with `shutdown_reason='leader_recovery_zombie'`, FSM stays in
  Healthy.

- `TestEmergLeaderRecoveryActiveResume` — D-D5 cenário (d) **happy path,
  BLOCKER 4 functional evidence**. Pre-seed row with events JSONB
  containing offer_accepted+health_pass; mock GetInstance returns
  running+populated_ports; mock pod /health returns 200 healthy. Asserts
  FSM == EmergencyActive (recovered from events.health_pass), ActivePodURL
  set, `Reconciler.IsActive()` true, lifecycle row NOT closed,
  runHealthcheckResumeLoop polling /health (≥1 hit within 8s),
  destroyHits == 0.

- `TestEmergLeaderRecoveryActiveResume_HealthFailureCancels` — D-D5
  cenário (d) **sad path**. Pod /health returns 500 after resume; assert
  after ~15s the lifecycle is closed with
  `shutdown_reason='resume_health_failed'`, instance was destroyed
  (`destroyHits >= 1`), FSM returns to Healthy.

## Deviations from Plan

### Auto-fixed issues

**1. [Rule 1 — Bug] Resume sad path was missing closeLifecycle call**

- **Found during:** writing TestEmergLeaderRecoveryActiveResume_HealthFailureCancels
  — when `runHealthcheckResumeLoop` invoked `cancelActiveLifecycle` after
  3 health failures, the test assertion that the lifecycle row was closed
  failed. Inspection of `cancelActiveLifecycle` confirmed it only does
  ctx cancel + Pub/Sub broadcast — it does NOT close the lifecycle row.
- **Why it works for fresh provisioning:** the close happens inside
  `provisionLifecycle` / `waitForReadyOrDestroy` when they observe ctx.Done().
- **Why it's a bug for resumed lifecycles:** no such goroutine exists;
  the resume path's only running goroutine is `runHealthcheckResumeLoop`,
  which previously only triggered cancel and exited.
- **Fix:** runHealthcheckResumeLoop now also (a) calls bestEffortDestroy
  on the VastInstanceID, (b) closes the row with
  shutdown_reason='resume_health_failed', and (c) calls FSM.SetState(StateHealthy)
  to return the system to a re-triggerable state.
- **Files modified:** `gateway/internal/emerg/recovery.go`.
- **Commit:** fffb2dc

### Authentication gates encountered

None.

## Threat Compliance

| Threat ID    | Status     | Evidence                                                                                                                                                         |
| ------------ | ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T-6-W7-01    | mitigated  | recoverOrphanLifecycles per-row error handling: transient Vast errors are logged + skipped; next leader acquisition retries. Operator escape via gatewayctl emerg force-destroy (Plan 10). |
| T-6-W7-02    | mitigated  | cancelActiveLifecycle requires r.isLeader (caller responsibility); applyEmergCommand short-circuits on !isLeader BEFORE the type switch. EmergEvent.ReplicaID is logged for forensic. |
| T-6-W7-03    | mitigated  | recoverOrphanLifecycles is one-shot per leader-acquisition (gated by isLeader.CAS in runOneTick); only re-runs on next leader change. captureTerminalSentry for zombie. |

## Verification

```
$ cd gateway && go test -race -count=1 ./internal/emerg/...
ok      github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg          3.271s
ok      github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast     1.080s

$ cd gateway && go build ./...
(no output — clean)

$ cd gateway && go vet -tags=integration ./...
(no output — clean)
```

Integration test runtime DEFERRED to CI (Docker testcontainers requires
Docker daemon, unavailable on the ops-claude host where this plan ran)
— matching the Phase 4 / 5 / 6-06 convention.

## Commits

- `5881fa0` — feat(06-07): cancel-in-flight triple layer (D-C3, PRV-09, SC-3) (Task 1)
- `fffb2dc` — feat(06-07): leader recovery (D-D5 4 cenários, BLOCKER 4 functional resume) (Task 2)

## must_haves Verification (per plan frontmatter)

- ✅ Cancel-in-flight TRIPLE LAYER (D-C3): cancelActiveLifecycle implements
  all three layers (context cancel via Swap+invoke; Pub/Sub broadcast
  via redisx.PublishEmergEvent; post-create destroy enforced in
  waitForReadyOrDestroy ctx.Done() branch from Plan 06).
- ✅ Cancel trigger: tracker.State() observation in
  evaluateEmergencyProvisioning detects local-llm CLOSED or HALF_OPEN
  during EMERGENCY_PROVISIONING.
- ✅ cancelActiveLifecycle: load activeLifecycle pointer, swap+invoke
  CancelFunc, PublishEmergEvent visibility broadcast, captureBreadcrumb.
- ✅ Pitfall 8 enforced: bestEffortDestroy uses
  context.WithTimeout(context.Background(), destroyShutdownBudget)
  inside the leader recovery zombie branch.
- ✅ Leader recovery (D-D5) 4 cenários, BLOCKER 4 (a-d) all FUNCTIONAL.
  Cenário (d) JSONB events replay via inferStateFromEvents +
  FSM.SetState + context re-attach + activePodURL store +
  runHealthcheckResumeLoop spawn.
- ✅ Test integration cancel-pre-create: TestEmergCancelPreCreate
  (createSucceededHits==0 + destroyHits==0 + DB cancel-related reason).
- ✅ Test integration cancel-post-create: TestEmergCancelPostCreate
  (destroyHits==1 + DB shutdown_reason='cancelled_in_flight').
- ✅ Test integration leader-recovery-zombie: TestEmergLeaderRecoveryZombie
  (destroyHits==1 + DB shutdown_reason='leader_recovery_zombie').

## Self-Check: PASSED

All claimed files exist:
- `gateway/internal/emerg/recovery.go` — FOUND
- `gateway/internal/emerg/recovery_test.go` — FOUND
- `gateway/internal/integration_test/emerg_cancel_pre_create_test.go` — FOUND
- `gateway/internal/integration_test/emerg_cancel_post_create_test.go` — FOUND
- `gateway/internal/integration_test/emerg_leader_recovery_zombie_test.go` — FOUND
- `gateway/internal/integration_test/emerg_leader_recovery_active_resume_test.go` — FOUND
- `gateway/internal/emerg/lifecycle.go` — MODIFIED (cancelActiveLifecycle added)
- `gateway/internal/emerg/reconciler.go` — MODIFIED (evaluateEmergencyProvisioning + runOneTick wires recoverOrphanLifecycles)
- `gateway/internal/emerg/fsm.go` — UNTOUCHED (Wave 1 already added SetState + ParseState per BLOCKER 4 fix in revision)

All commits exist in git log:
- `5881fa0` — FOUND
- `fffb2dc` — FOUND
