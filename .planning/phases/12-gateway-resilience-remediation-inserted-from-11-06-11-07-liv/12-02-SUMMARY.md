---
phase: 12-gateway-resilience-remediation
plan: 02
subsystem: gateway-resilience
tags: [resilience, RES-11, death-detection, breaker, alert, D-01, D-03, D-04, D-05, D-13]
requires:
  - "breaker.WriteForceOverride open|closed (Plan 12-01 Task 2)"
  - "obs.PrimaryDeathDetectedTotal{cause} counter (Plan 12-01 Task 3)"
  - "redisx.PrimaryEvent + PrimaryEventsChannel (Phase 6.6)"
  - "vast.Instance.IsTerminal()/IntendedStatus/StatusMsg + ErrInstanceNotFound (Phase 6)"
provides:
  - "evaluateReady Ready-tick death poll (3-strike confirm on IsTerminal + ErrInstanceNotFound)"
  - "D-05 trackedID repair from open primary_lifecycles row when activeInstanceID==0"
  - "classifyDeath: billing_stopped (IntendedStatus==stopped + A1 fallback) vs host_death vs not_found"
  - "death-confirmed force-OPEN of local-* breakers (10min TTL) before destroy (D-04)"
  - "billing-stop suppression marker checked by evaluateAsleep (D-01, no provision-fail loop)"
  - "distinct cause-tagged primary_death_confirmed event (D-03)"
  - "Alerter PrimaryEventsChannel subscription + severityForPrimary critical fan-out (D-03/FINDING 1)"
  - "markReady force-CLOSE of stale local-* breakers (60s TTL) on pod Ready (D-13)"
affects:
  - "Plan 12-03 (dial fallthrough — RES-13 falls traffic to tier-1 when local-* breakers force-OPEN)"
  - "Plan 12-04/05 (chaos gate exercises the live Ready-tick death detection + alert)"
tech-stack:
  added: []
  patterns:
    - "Ready-tick death poll reusing the provisioning 3-strike confirm; strike counters persist on the Reconciler struct (deathStrikeMu-guarded) because each tick is a separate call"
    - "FSM-driven symmetric breaker control: long force-OPEN on death (D-04) + short force-CLOSE on Ready (D-13)"
    - "billing-stop suppression as a checked FLAG (atomic.Pointer[time.Time]) consulted by the existing schedule evaluator — NO new retry machinery"
key-files:
  created: []
  modified:
    - gateway/internal/primary/reconciler.go
    - gateway/internal/primary/lifecycle.go
    - gateway/internal/primary/reconciler_test.go
    - gateway/internal/primary/export_test.go
    - gateway/internal/alert/severity.go
    - gateway/internal/alert/alerter.go
    - gateway/internal/alert/severity_test.go
decisions:
  - "terminalConfirmStrikes promoted from a waitForReadyOrDestroy function-local const to a package-level const (=3), shared by the provisioning poll and the Ready-tick death poll (D-02)"
  - "billing-stop suppression is a reconciler-held atomic.Pointer[time.Time] flag with a 6h safety window; normally consumed earlier by a successful provision (markReady) or operator force-up — NO new config knob added (keeps config_test out of scope)"
  - "death force-OPEN TTL = 10min (outlasts drain+destroy+re-provision); markReady force-CLOSE TTL = 60s (outlasts one probe cycle, hands back to observation)"
  - "handleConfirmedDeath force-OPENs the breakers BEFORE startDrain so the dead-address window is closed before BestEffortDestroy"
metrics:
  duration: "~55min"
  completed: "2026-06-12"
  tasks: 4
  files: 7
---

# Phase 12 Plan 02: Primary Ready-Tick Death Detection + FSM-Driven Breaker Control + Distinct Billing-Stop Alert Summary

RES-11 autonomous failover: the primary reconciler now polls Vast on every Ready tick, confirms a dead pod via a 3-strike confirm on both `IsTerminal()` and `ErrInstanceNotFound`, repairs a lost trackedID from the open lifecycle row (D-05), force-OPENs the `local-*` breakers before destroy (D-04), arms a durable billing-stop suppression marker that makes `evaluateAsleep` skip re-provision (D-01), publishes a distinct cause-tagged death event the Alerter now consumes (D-03/FINDING 1), and force-CLOSEs the stale `local-*` breakers when a fresh pod goes Ready (D-13) — completing the symmetric breaker-control loop. Full RED/GREEN TDD coverage.

## What Shipped

### Task 1 — Ready-tick death poll + 3-strike confirm + D-05 trackedID repair (TDD)
- `evaluateReady` (reconciler.go) now calls `pollDeathOnReadyTick` BEFORE the schedule-drain check, so a confirmed death drains immediately even inside the peak window. The poll runs regardless of `PRIMARY_POD_SCHEDULE_DISABLED` (a Ready pod under the soak gate is just as mortal — mirrors the Pitfall #11 re-assert).
- `pollDeathOnReadyTick` ports the proven 3-strike confirm from `waitForReadyOrDestroy`. Strike counters (`terminalStrikes`/`notFoundStrikes`) live on the Reconciler struct under `deathStrikeMu` because each Ready tick is a SEPARATE call (unlike the in-loop provisioning poll). They reset on any healthy/non-terminal observation AND on the Provisioning→Ready transition in `markReady` (Gemini suggestion — clean per-lifecycle start).
- **D-05 repair** (Pitfall 1, the prerequisite that defeated the 11-07 reproduction): when `activeInstanceID==0` but a pod is routing (`activePodURLs` set), the poll reads `GetOpenPrimaryLifecycle` → `open.VastInstanceID.Int64` → `Store(...)` before polling — no silent no-op on a lost id.
- `classifyDeath` returns `billing_stopped | host_death | not_found`.
- `terminalConfirmStrikes` promoted to a package-level const (=3), shared by both polls.

### Task 2 — death-confirmed path: drain + force-open + suppression + alert event (TDD)
- `handleConfirmedDeath`: (1) force-OPENs `local-llm`/`local-stt`/`local-tts` (10min TTL, documented) BEFORE `startDrain` reaches the destroy path — closes the dead-address window (D-04); (2) `startDrain` advances Ready→Draining + RestoreTier0 (reuses the existing FSM path — NO new state); (3) for a billing-stop, arms a durable suppression marker (`billingSuppressedAt`); (4) publishes a distinct `primary_death_confirmed` event tagged with the cause (D-03); (5) increments `PrimaryDeathDetectedTotal{cause}`.
- **D-01 suppression**: `evaluateAsleep` checks `billingSuppressionActive(now)` and SKIPS re-provision while the marker is active — a zero-credit pod death no longer enters a provision-fail loop (Codex HIGH / Pitfall 5). It is a checked FLAG, not retry machinery. The marker clears on a successful provision (`markReady`) or operator force-up (`handleForceUpRequest`). Host-yank records NO marker → re-provisions naturally.
- **A1 resolved from committed 11-06 evidence** (no live billing-stopped instance): primary signal `IntendedStatus=="stopped"`; fallback `ActualStatus=="exited" && StatusMsg` contains `credit`/`account`/`saldo`. Both implemented; the A1 evidence comment is present.

### Task 3 — Alerter consumes PrimaryEvents: severityForPrimary critical fan-out (TDD)
- `alerter.go` Subscribe adds `redisx.PrimaryEventsChannel` (FINDING 1 — it was consumed by nobody; `primary_death_confirmed` was published but never paged).
- `severityFor` routes `PrimaryEventsChannel → severityForPrimary`.
- `severityForPrimary` maps `primary_death_confirmed` to `SeverityCritical` with a DISTINCT title: billing-stop → "Vast account sem crédito — primary billing-stopped" (operator action = ADD CREDIT), host-death/not_found → "Primary pod morto (host-yank/404)". Fingerprint `primary:death:<reason>` for dedup; non-death events → info. Body carries only cause + lifecycle id (no tenant payload / secrets — V7 / T-12-06). Reuses the existing critical fan-out (Chatwoot+ClickUp+Brevo), no new infra.

### Task 4 — D-13 markReady force-CLOSE of stale local-* breakers (TDD)
- `markReady` force-CLOSEs `local-llm`/`local-stt`/`local-tts` (60s SHORT TTL, documented) AFTER the OverrideTier0 block — symmetric to Task 2's D-04 force-OPEN. A re-provisioned pod never inherits an OPEN breaker left from probing the previous dead URL (Pitfall 4 / SEED-012); the short TTL hands control back to observation quickly. Best-effort: a Redis error logs a warning but never blocks Provisioning→Ready. Final markReady order: reset strikes → clear suppression → force-close → publish `primary_ready`.

## Tests

primary (reconciler_test.go / export_test.go):
- `TestEvaluateReady_EmptyTrackedIDReconciles`, `TestEvaluateReady_DeathDetection`, `TestEvaluateReady_TransientExitedDoesNotDrain`, `TestEvaluateReady_NotFound3StrikeDrains`, `TestEvaluateReady_HealthyNoop`, `TestEvaluateReady_StrikesResetOnEnterReady` (Task 1)
- `TestDeath_HostYankDrainsAndForceOpens`, `TestDeath_BillingStopRecordsSuppression`, `TestEvaluateAsleep_BillingStopSuppressesReprovision`, `TestDeath_BreakersForceOpenedBeforeDestroy`, `TestDeath_BillingStopFallbackSignal` (Task 2)
- `TestMarkReady_ResetsStaleBreakers`, `TestMarkReady_ForceCloseAfterOverrideTier0`, `TestMarkReady_ForceCloseBestEffort` (Task 4)

alert (severity_test.go):
- `TestSeverityForPrimary_BillingStopCritical`, `TestSeverityForPrimary_HostDeathCritical`, `TestSeverityForPrimary_Malformed`, `TestSeverityFor_RoutesPrimaryChannel` (Task 3)

Verification (all green):
- `go build ./...` exit 0
- `go test ./internal/primary/ ./internal/alert/ -count=1` — all pass
- `go test ./internal/...` — all pass (proxy, the main breaker/Resolve consumer, included)

Acceptance greps:
- `grep -c "GetInstance" reconciler.go` = 5 (inside evaluateReady's call graph)
- `grep -c "terminalConfirmStrikes = 3" reconciler.go` = 1
- `grep -c "WriteForceOverride" reconciler.go` = 2 (death long-open + markReady short-close)
- `grep -c "primary_death_confirmed" reconciler.go` = 2
- `grep -i "A1" reconciler.go` = 3 (evidence comment present)
- `grep -c "StateDead\|StateFailed" fsm.go` = 0 (no new FSM state)
- `grep -c "PrimaryEventsChannel" alerter.go` = 1
- `grep -c "func severityForPrimary" severity.go` = 1
- `grep -c "Vast account sem crédito" severity.go` = 1

## Deviations from Plan

Two in-scope files were modified beyond the plan's `files_modified` list, both inside the same `internal/primary/` subsystem and required to land the plan as written:

**1. [Rule 3 - Blocking] gateway/internal/primary/lifecycle.go**
- **Issue:** The Reconciler struct (and Deps) live in `lifecycle.go`, not `reconciler.go`. The Ready-tick death poll needs persisted strike counters + a `deathStrikeMu` + the billing-stop suppression marker as struct fields.
- **Fix:** Added `deathStrikeMu sync.Mutex`, `terminalStrikes int`, `notFoundStrikes int`, `billingSuppressedAt atomic.Pointer[time.Time]` to the Reconciler struct; added the `sync` import.
- **Commit:** f0d2252 (Task 1) / f146944 (Task 2)

**2. [Rule 3 - Blocking] gateway/internal/primary/export_test.go**
- **Issue:** The unit tests need test-only accessors for the unexported death-poll seam + strike counters + suppression marker.
- **Fix:** Added `classifyDeathOnReadyTickForTest`, `terminalStrikesForTest`, `notFoundStrikesForTest`, `billingSuppressionActiveForTest`, `armBillingSuppressionForTest`, `clearBillingSuppressionForTest` (test build only — production binary unaffected).
- **Commit:** f0d2252 (Task 1) / f146944 (Task 2)

No architectural changes (Rule 4) were needed. No new FSM state, no new config knob, no new package installs (go.mod unchanged — T-12-SC satisfied).

## TDD Gate Compliance

This plan is `type: execute` with per-task `tdd="true"`. For each task the failing tests and the implementation were authored and verified in the same working session (RED tests written first, confirmed to drive undefined symbols / failing assertions, then GREEN). The four task commits each bundle the task's tests + implementation:
- Task 1: f0d2252
- Task 2: f146944
- Task 3: 780b537
- Task 4: d2583af

Each commit was verified green via the task's scoped `go test -run` filter plus the full `./internal/primary/` + `./internal/alert/` suites before proceeding.

## Known Stubs

None. The two Plan 12-01 counters this plan consumes (`PrimaryDeathDetectedTotal`) are now incremented (Task 2). No placeholder data, no hardcoded empty values, no TODO/FIXME introduced.

## Threat Flags

None. All new surface is covered by the plan's `<threat_model>` (T-12-04 billing-stop loop → D-01 suppression marker + `TestEvaluateAsleep_BillingStopSuppressesReprovision`; T-12-05 transient-exited false positive → 3-strike + `TestEvaluateReady_TransientExitedDoesNotDrain`; T-12-06 alert leaks → cause+lifecycle-id-only body; T-12-14 stale force-CLOSE → 60s short TTL + markReady-only). No new network endpoints, auth paths, or schema changes were introduced.

## Self-Check: PASSED

- All 7 modified files present on disk (7/7 FOUND).
- All 4 commit hashes present in git history (f0d2252, f146944, 780b537, d2583af).
- `go build ./...` exit 0; `go test ./internal/primary/ ./internal/alert/` green; full `./internal/...` green.
