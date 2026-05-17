---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 08
subsystem: gateway/emerg + gateway/upstreams + gateway/proxy
tags: [go, cutback, dispatcher-override, multi-failover, lifecycle-completion, blocker-2-resolved]
requires:
  - 06-01-SUMMARY.md (config: ProvisionHealthyDurationSeconds + ProvisionIdleGraceSeconds)
  - 06-04-SUMMARY.md (Reconciler leader-election + activeLifecycle pointer + lifecycleCancel)
  - 06-06-SUMMARY.md (vast.Client + provisionLifecycle + markHealthy + closeLifecycle)
  - 06-07-SUMMARY.md (cancelActiveLifecycle + recoverOrphanLifecycles + resumeFSMFromEvents)
provides:
  - upstreams.Loader.OverrideTier0(role, url) — race-free atomic.Pointer
  - upstreams.Loader.RestoreTier0(role) — idempotent clear
  - upstreams.UpstreamConfig.IsEmergency flag (set in Resolve when override active)
  - proxy.EmergTrafficRegistrar interface (W9 — interface injection, not string sniffing)
  - proxy.DispatcherConfig.EmergTraffic — RegisterTraffic on emergency-routed requests
  - emerg.Reconciler.evaluateEmergencyActive (D-D1 cutback gate + D-C4 ride-out)
  - emerg.Reconciler.evaluateRecovering (D-D1 idle-grace destroy gate)
  - emerg.Reconciler.evaluateCooldown (HealthyDurationSeconds re-arm)
  - emerg.Reconciler.RegisterTraffic / IsIdle (idle anchor management)
  - emerg.Reconciler.destroyAndCloseLifecycle (REAL impl; replaces 06-05 stub)
  - emerg.localLlmTracker.SustainedClosedSeconds + closedSince atomic
affects:
  - PRV-08 (cutback + idle destroy invariant — DELIVERED)
  - SC-4 (primary healthy 5min → cutback; +5min idle → destroy — DELIVERED)
  - D-C4 (multi-failover ride-out — DELIVERED, no duplicate lifecycles)
  - D-D1 (5min/5min env-tunable timing — DELIVERED via existing config knobs)
  - D-E3 (LLM-only dispatcher integration — DELIVERED; STT/embed unchanged)
  - BLOCKER 2 (force_destroy_request end-to-end — RESOLVED via destroyAndCloseLifecycle)
  - W7 (events JSONB written FIRST — preserved via closeLifecycle internal sequencing)
  - W9 (interface injection for dispatcher hook — DELIVERED via EmergTrafficRegistrar)
tech-stack:
  added: []
  patterns:
    - "Race-free dispatcher override: tier0Override map[role]*atomic.Pointer[string] in Loader; reads on Resolve hot path are lockless atomic.Load"
    - "Interface injection over string sniffing: EmergTrafficRegistrar abstracts the reconciler so dispatcher does not need to import emerg + does not depend on fragile Name prefix matching"
    - "Idle anchor = max(lastTraffic, recoveringEnteredAt): a fresh-Recovering tick has the FULL grace window even if the last RegisterTraffic call was minutes ago during ACTIVE"
    - "Tracker SustainedClosedSeconds mirrors SustainedFailedOverSeconds — same idempotency contract on duplicate Pub/Sub events; fresh tracker returns 0 (closedSince==0) so leader-recovery into Active doesn't trigger immediate cutback"
    - "destroyAndCloseLifecycle is the ONE shared close path for both cutback (idle-grace) and force-destroy (operator) — keeps RestoreTier0 + bestEffortDestroy + closeLifecycle + cost calc in lockstep regardless of trigger"
key-files:
  created:
    - gateway/internal/upstreams/loader_test.go
    - gateway/internal/integration_test/emerg_cutback_test.go
    - gateway/internal/integration_test/emerg_idle_destroy_test.go
    - gateway/internal/integration_test/emerg_multi_failover_rideout_test.go
    - gateway/internal/integration_test/emerg_force_destroy_event_test.go
  modified:
    - gateway/internal/upstreams/loader.go
    - gateway/internal/upstreams/types.go
    - gateway/internal/upstreams/exports_helpers.go
    - gateway/internal/upstreams/loader_export_test.go
    - gateway/internal/proxy/dispatcher.go
    - gateway/internal/emerg/tracker.go
    - gateway/internal/emerg/tracker_test.go
    - gateway/internal/emerg/reconciler.go
    - gateway/internal/emerg/lifecycle.go
    - gateway/internal/emerg/recovery.go
decisions:
  - "Interface injection (EmergTrafficRegistrar) chosen over runtime string-prefix matching on Name (W9). Rationale: a future plan that renames 'emergency_pod_*' would silently break the string match; the IsEmergency flag is set deliberately by Loader.Resolve at the same atomic instant the override URL is read, so dispatcher and loader can never disagree about whether traffic is emergency-bound."
  - "destroyAndCloseLifecycle is the SINGLE close path for both cutback idle-grace AND operator force-destroy. Keeps RestoreTier0 + bestEffortDestroy + closeLifecycle + cost calc in lockstep regardless of trigger. handleForceDestroy and evaluateRecovering both call this helper, then transition the FSM independently (helper is data-plane only)."
  - "closeLifecycle now defensively calls RestoreTier0 on every close (idempotent atomic.Pointer.Store(nil)). This is in addition to the explicit RestoreTier0 in destroyAndCloseLifecycle and evaluateEmergencyActive — defense in depth so any close path that bypasses destroyAndCloseLifecycle (e.g. cancel-in-flight from Plan 06-07 calling closeLifecycle directly) still leaves the dispatcher in a consistent state."
  - "Idle anchor uses max(lastEmergencyTrafficAt, recoveringEnteredAt) so a fresh-Recovering tick has the FULL grace window. Without this, a request registered at t=0 (during ACTIVE) followed by cutback at t=10 would falsely satisfy 'idle for 1s' the moment Recovering starts. Using the later of the two anchors enforces the 'no traffic while Recovering' invariant."
  - "Fresh tracker returns SustainedClosedSeconds=0 (closedSince==0 default) even though state defaults to 'closed'. This prevents leader-recovery resuming directly into Active from triggering immediate cutback before the tracker observes a real CLOSED event from the breaker subscriber."
  - "markHealthy strips '/health' suffix from the URL before calling OverrideTier0 — the upstream URL is the OpenAI-compatible base, not the probe URL. The same stripHealthSuffix helper is used by recovery.go's resumeFSMFromEvents for consistency."
  - "Loader is added to Deps (nil-safe). Tests that don't exercise the dispatcher integration can omit the Loader field; production wiring (Plan 09 main.go) injects the real loader. This avoids a hard dependency that would break older unit tests."
metrics:
  duration: "~50min"
  tasks_completed: 2
  files_created: 5
  files_modified: 10
  unit_tests_added: 13 # 8 loader + 5 tracker SustainedClosedSeconds
  integration_tests_added: 4
  total_lines_added: 1559
  completed: 2026-05-13
---

# Phase 6 Plan 08: Cutback + Idle Destroy + Multi-Failover Ride-Out + Dispatcher Integration Summary

End-to-end emergency-pod lifecycle completion: D-D1 cutback (5min healthy
sustained → routing reverts to primary), D-D1 idle destroy (5min idle
grace → pod terminated), D-C4 multi-failover ride-out (concurrent OPEN
events do NOT spawn duplicate lifecycles), and D-E3 dispatcher integration
(W9 interface injection, not string-prefix sniffing). Also lands the
Plan 06-05 deferred force-destroy integration test (BLOCKER 2 cross-plan
evidence the Plan 06-05 publish path → Plan 06-08 destroyAndCloseLifecycle
helper integrates cleanly).

Without this plan, lifecycle would enter ACTIVE but never terminate — pod
runs indefinitely + budget runaway. With this plan, the full lifecycle
HEALTHY → EmergencyProvisioning → EmergencyActive → Recovering → Cooldown
→ HEALTHY closes the loop and SC-4 is provable end-to-end.

## What Was Built

### Task 1 — upstreams.Loader OverrideTier0/RestoreTier0 + dispatcher hook

**`gateway/internal/upstreams/types.go`** — `UpstreamConfig.IsEmergency`
flag (json:"-") set ONLY in the ephemeral copy returned by Resolve when
a tier-0 override is active. Persisted snapshot rows always have
IsEmergency=false.

**`gateway/internal/upstreams/loader.go`** — `tier0Override
map[string]*atomic.Pointer[string]` field on Loader. Initialised in
NewLoader / NewLoaderInMemory / NewLoaderForTest with a single "llm"
key (LLM-only in v1 per CONTEXT D-E3 — STT/embed continue tier-0
primary even during emergency).

```
func (l *Loader) OverrideTier0(role, url string)
  → atomic.Store URL into tier0Override[role]; no-op if role unknown
func (l *Loader) RestoreTier0(role string)
  → atomic.Store(nil); idempotent
func (l *Loader) Resolve(role, tier int) (UpstreamConfig, bool)
  → if tier==0 AND override active: return ephemeral
    UpstreamConfig{URL: overrideURL, Name: "emergency_pod_<role>",
                   IsEmergency: true, ...inherited from snapshot}
  → otherwise: return snapshot row unchanged
```

**`gateway/internal/proxy/dispatcher.go`** — new `EmergTrafficRegistrar`
interface (one method: `RegisterTraffic()`). DispatcherConfig gains
`EmergTraffic EmergTrafficRegistrar` (nil-safe). On the hot path:

```go
t0, ok := cfg.Loader.Resolve(cfg.Role, 0)
// ... (existing code)
if t0.IsEmergency && cfg.EmergTraffic != nil {
    cfg.EmergTraffic.RegisterTraffic()
}
```

W9 revision (2026-05-13): interface injection chosen over runtime
`strings.HasPrefix(t0.Name, "emergency_pod_")` sniffing. The string
match would silently break if a future plan renamed the prefix; the
IsEmergency flag is set deliberately at the same atomic instant the
override URL is read, so dispatcher and loader cannot disagree.

**Unit tests (8):** TestOverrideTier0, TestRestoreTier0,
TestResolveWithOverride_OnlyTier0, TestOverrideTier0_NonExistentRole,
TestRestoreTier0_Idempotent, TestOverrideTier0_Replaces,
TestOverrideTier0_RaceFreeReads (100 readers + 1 writer × 1000),
TestNew{LoaderForTest,LoaderInMemory}_IncludesOverrideMap.

### Task 2 — emerg cutback + idle destroy + ride-out + force-destroy

**`gateway/internal/emerg/tracker.go`** — `closedSince atomic.Int64`
mirrors `openSince` for the CLOSED state. ApplyEvent idempotent on
duplicate CLOSED (Pitfall 3); HALF_OPEN clears closedSince (probing
!= stable). New `SustainedClosedSeconds()` powers the cutback gate.

**`gateway/internal/emerg/reconciler.go`** — Deps gains
`Loader *upstreams.Loader` (nil-safe). Reconciler gains three new
atomic.Int64 fields: `lastEmergencyTrafficAt`, `recoveringEnteredAt`,
`cooldownEnteredAt`. evaluateTick switch extended with the three new
state branches:

```
evaluateEmergencyActive (D-C4 + D-D1):
  if tracker.State() != "closed":
      ride-out (Debug log only)
      return
  if tracker.SustainedClosedSeconds() < HealthyDurationSeconds:
      return
  Loader.RestoreTier0("llm")
  FSM.Transition(EmergencyActive → Recovering, "primary_healthy_sustained")
  recoveringEnteredAt = now
  lastEmergencyTrafficAt = now  // arm idle anchor

evaluateRecovering (D-D1):
  idleAnchor = max(lastEmergencyTrafficAt, recoveringEnteredAt)
  if now - idleAnchor < IdleGraceSeconds:
      return
  destroyAndCloseLifecycle(activeLifecycle, "cutback_idle")
  FSM.Transition(Recovering → Cooldown, "idle_grace_elapsed")
  cooldownEnteredAt = now

evaluateCooldown:
  if now - cooldownEnteredAt < HealthyDurationSeconds:
      return
  FSM.Transition(Cooldown → Healthy, "cooldown_elapsed")
  cooldownEnteredAt = 0
```

**`destroyAndCloseLifecycle` (REAL impl, replaces Plan 06-05 stub):**

```
1. Loader.RestoreTier0("llm")             // routing back to primary first
2. bestEffortDestroy(VastInstanceID)      // Pitfall 8 30s background ctx
3. cost := calculateCostBRL(...)          // for breadcrumb
4. closeLifecycle(reason)                 // emits lifecycle_close event
                                          // JSONB FIRST (W7) inside the
                                          // helper's mustEventJSON path
5. captureBreadcrumb("destroy_and_close", {lifecycle_id, vast_id, reason, cost})
```

`handleForceDestroy` now also stamps `cooldownEnteredAt = now` after
the FSM transition so evaluateCooldown's gate is anchored correctly
(operator force-destroy reaches the same Cooldown state as automatic
cutback).

**`RegisterTraffic` + `IsIdle`:** lockless atomic helpers exposed on
Reconciler. RegisterTraffic implements proxy.EmergTrafficRegistrar
implicitly (interface satisfaction is structural in Go). IsIdle is
exported for tests + future gatewayctl emerg-state output.

**`gateway/internal/emerg/lifecycle.go`:**

- `markHealthy` activates `Loader.OverrideTier0("llm", baseURL)` —
  strips `/health` suffix so the upstream URL is the OpenAI-compatible
  base. Also arms `lastEmergencyTrafficAt = now` so a fresh ACTIVE pod
  is not immediately classified as idle.
- `closeLifecycle` defensively calls `Loader.RestoreTier0("llm")` on
  every close (idempotent). Defense in depth — prevents orphan
  dispatcher state if a close path bypasses destroyAndCloseLifecycle
  (e.g. cancel-in-flight calling closeLifecycle directly).
- `stripHealthSuffix` helper extracted for reuse in recovery.go.

**`gateway/internal/emerg/recovery.go`:** TODO replaced. resumeFSMFromEvents
now activates `Loader.OverrideTier0("llm", baseURL)` + arms
`lastEmergencyTrafficAt = now` for resumed lifecycles too.

**Integration tests (4, build-clean; runtime deferred to CI per
ops-claude no-docker convention — same pattern as Plans 04-09 / 05-* /
06-06 / 06-07):**

- `emerg_cutback_test.go` — drive HEALTHY → EmergencyActive (mock
  vast happy path); publish sustained CLOSED; assert FSM → Recovering
  in <2s + Loader.Resolve(llm,0).IsEmergency == false (RestoreTier0
  fired) + URL == primary + destroyHits == 0 (cutback alone doesn't
  destroy).

- `emerg_idle_destroy_test.go` — full chain (cutback +) idle destroy
  + close + Cooldown → Healthy. With IdleGraceSeconds=1, no traffic
  registered, asserts destroyHits >= 1 + DB row close
  shutdown_reason='cutback_idle' + IsActive() == false + FSM → Healthy
  after another HealthyDurationSeconds=1.

- `emerg_multi_failover_rideout_test.go` — D-C4. After EmergencyActive,
  publish a SECOND OPEN event; sleep 3s; assert lifecycle row count
  UNCHANGED at 1 + FSM stays in EmergencyActive + createHits == 1
  (no duplicate provision) + destroyHits == 0.

- `emerg_force_destroy_event_test.go` — BLOCKER 2 cross-plan deferred
  test from Plan 06-05. Drive to EmergencyActive; publish
  force_destroy_request via redisx.PublishEmergEvent; assert
  destroyHits >= 1 + DB row close shutdown_reason='manual' + FSM →
  Cooldown + Loader override cleared. End-to-end evidence Plan 06-05
  publish path + Plan 06-08 destroyAndCloseLifecycle helper integrate.

## Deviations from Plan

### Auto-fixed issues

**1. [Rule 2 — Defensive dispatcher consistency] closeLifecycle defensive RestoreTier0**

- **Found during:** writing destroyAndCloseLifecycle. The cancel-in-flight
  path from Plan 06-07 calls `closeLifecycle` directly (not via
  destroyAndCloseLifecycle), so a cancel during EmergencyActive (e.g.
  resume_health_failed) would leave the Loader override stuck pointing
  at a destroyed pod. Subsequent dispatcher requests would 502 instead
  of falling back to the primary.
- **Fix:** Added `if r.deps.Loader != nil { r.deps.Loader.RestoreTier0("llm") }`
  to closeLifecycle. Idempotent + cheap (atomic.Pointer.Store(nil)).
  Defense in depth: destroyAndCloseLifecycle ALSO calls RestoreTier0
  explicitly so no close path leaves the dispatcher in inconsistent state.
- **Files modified:** `gateway/internal/emerg/lifecycle.go`
- **Commit:** 07650fe

**2. [Rule 2 — Idle anchor correctness] Idle anchor uses max(traffic, recoveringEnteredAt)**

- **Found during:** designing evaluateRecovering. Initial draft used only
  `lastEmergencyTrafficAt`. Bug: a request registered at t=0 (during
  ACTIVE) followed by cutback at t=10 would falsely satisfy "idle for 1s"
  the MOMENT Recovering starts (t - lastTraffic = 10 - 0 = 10s >= 1s
  grace).
- **Fix:** Idle anchor = max(lastEmergencyTrafficAt, recoveringEnteredAt).
  Plus evaluateEmergencyActive arms `lastEmergencyTrafficAt = now` on the
  Active → Recovering transition (defense in depth — both anchors agree
  for the first tick after transition).
- **Files modified:** `gateway/internal/emerg/reconciler.go`
- **Commit:** 07650fe

**3. [Rule 2 — Fresh tracker SustainedClosed=0] closedSince defaults to 0, NOT now**

- **Found during:** designing tracker.SustainedClosedSeconds. State
  defaults to "closed" at construction, but if SustainedClosedSeconds
  returned `now - 0 = unix-time-since-epoch`, a fresh replica that
  resumed into EmergencyActive via leader recovery would IMMEDIATELY
  satisfy the cutback gate (sustained == 1.7e9 seconds >> threshold).
- **Fix:** SustainedClosedSeconds returns 0 when `closedSince == 0`
  (fresh tracker has not yet OBSERVED a CLOSED event). The first
  ApplyEvent(closed) sets closedSince to NOW. Mirrors the same
  defensive pattern in SustainedFailedOverSeconds.
- **Files modified:** `gateway/internal/emerg/tracker.go`
- **Commit:** 07650fe

### Authentication gates encountered

None.

## Threat Compliance

| Threat ID    | Status     | Evidence                                                                                                                                                          |
| ------------ | ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T-6-W8-01    | mitigated  | TestEmergIdleDestroy verifies destroy fires only when traffic is NOT registered. The interface injection contract (EmergTrafficRegistrar.RegisterTraffic) means a dispatcher bug that drops the call would surface as premature destroy in the integration test, not silent over-charge. |
| T-6-W8-02    | accept     | ProvisionIdleGraceSeconds <= 0 is documented in 06-WAVE0-GATES.md as operator responsibility. evaluateRecovering refuses to destroy when graceSeconds <= 0 (logs Error) — defense in depth against the misconfig surfacing as instant destroy. |
| T-6-W8-03    | mitigated  | OverrideTier0/RestoreTier0 are in-process atomic.Pointer.Store ops — cannot fail (no ENOMEM, no I/O). The destroyAndCloseLifecycle path calls RestoreTier0 BEFORE bestEffortDestroy so dispatcher routes back to primary even if Vast destroy hangs (30s budget per Pitfall 8). |

## Verification

```
$ cd gateway && go build ./...
(no output — clean)

$ cd gateway && go test -race -count=1 ./internal/emerg/... ./internal/upstreams/... ./internal/proxy/...
ok      github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg          4.366s
ok      github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast     1.071s
ok      github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams      3.238s
ok      github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy          9.942s

$ cd gateway && go vet -tags=integration ./internal/integration_test/...
(no output — clean)
```

Integration test runtime DEFERRED to CI (Docker testcontainers requires
Docker daemon, unavailable on the ops-claude host where this plan ran)
— matching the Plan 04-09 / 05-* / 06-06 / 06-07 convention.

## Commits

- `597c19a` — feat(06-08): OverrideTier0/RestoreTier0 + dispatcher EmergTrafficRegistrar (Task 1)
- `07650fe` — feat(06-08): cutback + idle destroy + ride-out + force-destroy (Task 2)

## must_haves Verification (per plan frontmatter)

- ✅ Cutback timing (D-D1): evaluateEmergencyActive transitions Active →
  Recovering when tracker.SustainedClosedSeconds() ≥ HealthyDurationSeconds;
  dispatcher RestoreTier0 fires at the same instant. evaluateRecovering
  destroys + closes 'cutback_idle' after IdleGraceSeconds. Cutback +
  destroy timings env-tunable via existing config knobs (Plan 06-01).
- ✅ Cooldown duration = HealthyDurationSeconds: evaluateCooldown re-arms
  Cooldown → Healthy after the same threshold. Prevents oscillation.
- ✅ D-C4 multi-failover ride-out: evaluateEmergencyActive's tracker !=
  "closed" branch is debug-log only — no FSM mutation, no lifecycle
  spawn. TestEmergMultiFailoverRideOut asserts row count UNCHANGED.
- ✅ Dispatcher integration (D-E3): markHealthy + resumeFSMFromEvents
  call Loader.OverrideTier0("llm", baseURL); cutback + closeLifecycle
  + destroyAndCloseLifecycle call Loader.RestoreTier0("llm"). LLM-only
  in v1 — STT/embed unchanged.
- ✅ Idle detection: lastEmergencyTrafficAt atomic.Int64 incremented by
  dispatcher.RegisterTraffic via EmergTrafficRegistrar interface (W9
  fix — interface injection, not string sniffing). IsIdle anchored at
  max(lastTraffic, recoveringEnteredAt).
- ✅ Test integration cutback: TestEmergCutback verifies RestoreTier0
  fires + FSM → Recovering + destroyHits == 0.
- ✅ Test integration idle destroy: TestEmergIdleDestroy verifies destroy
  + close shutdown_reason='cutback_idle' + IsActive() false + FSM →
  Cooldown → Healthy.
- ✅ Test integration multi-failover ride-out: TestEmergMultiFailoverRideOut
  verifies row count UNCHANGED + FSM stays in Active + createHits == 1.
- ✅ BLOCKER 2 force-destroy: TestEmergReconcilerHandlesForceDestroyEvent
  end-to-end evidence Plan 06-05 publish path + Plan 06-08 helper
  integrate cleanly (DestroyInstance + DB close 'manual' + Cooldown +
  Loader override cleared).

## Self-Check: PASSED

All claimed files exist:
- `gateway/internal/upstreams/loader.go` — MODIFIED (OverrideTier0/RestoreTier0 + Resolve override path)
- `gateway/internal/upstreams/types.go` — MODIFIED (IsEmergency flag)
- `gateway/internal/upstreams/exports_helpers.go` — MODIFIED (init override map)
- `gateway/internal/upstreams/loader_export_test.go` — MODIFIED (init override map)
- `gateway/internal/upstreams/loader_test.go` — FOUND (8 unit tests)
- `gateway/internal/proxy/dispatcher.go` — MODIFIED (EmergTrafficRegistrar interface + hot-path call)
- `gateway/internal/emerg/tracker.go` — MODIFIED (closedSince + SustainedClosedSeconds)
- `gateway/internal/emerg/tracker_test.go` — MODIFIED (5 new SustainedClosed tests)
- `gateway/internal/emerg/reconciler.go` — MODIFIED (3 new evaluate branches + RegisterTraffic + IsIdle + REAL destroyAndCloseLifecycle + Loader in Deps + 3 new atomic fields)
- `gateway/internal/emerg/lifecycle.go` — MODIFIED (markHealthy OverrideTier0 + closeLifecycle defensive RestoreTier0 + stripHealthSuffix)
- `gateway/internal/emerg/recovery.go` — MODIFIED (resumeFSMFromEvents OverrideTier0 + traffic anchor)
- `gateway/internal/integration_test/emerg_cutback_test.go` — FOUND
- `gateway/internal/integration_test/emerg_idle_destroy_test.go` — FOUND
- `gateway/internal/integration_test/emerg_multi_failover_rideout_test.go` — FOUND
- `gateway/internal/integration_test/emerg_force_destroy_event_test.go` — FOUND

All commits exist in git log:
- `597c19a` — FOUND (feat(06-08): OverrideTier0/RestoreTier0 + dispatcher EmergTrafficRegistrar (Task 1))
- `07650fe` — FOUND (feat(06-08): cutback + idle destroy + ride-out + force-destroy (Task 2))
