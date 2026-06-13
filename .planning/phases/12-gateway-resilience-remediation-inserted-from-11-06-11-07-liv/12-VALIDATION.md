---
phase: 12
slug: gateway-resilience-remediation
status: draft
nyquist_compliant: true
wave_0_complete: true
created: 2026-06-12
updated: 2026-06-13
---

# Phase 12 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Updated 2026-06-13 to incorporate cross-AI review feedback (12-REVIEWS.md): new ErrorHandler/control-flow + body-replay tests (12-03), billing-stop suppression + strike-reset tests (12-02), and health backward-compat tests (12-01).

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (go 1.24.9) |
| **Config file** | `gateway/go.mod` |
| **Quick run command** | `cd gateway && go test ./internal/primary/ ./internal/upstreams/ ./internal/proxy/ ./internal/breaker/ ./internal/alert/` |
| **Full suite command** | `cd gateway && go build ./... && go test ./...` |
| **Estimated runtime** | ~60 seconds |

---

## Sampling Rate

- **After every task commit:** Run quick run command (packages touched by the task)
- **After every plan wave:** Run full suite command
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 120 seconds

---

## Per-Task Verification Map

> Sourced from RESEARCH.md `## Validation Architecture` Wave 0 map and the per-task `<behavior>`/`<verify>` blocks in plans 12-01..12-05, reconciled with 12-REVIEWS.md feedback.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 12-01-T1 | 12-01 | 1 | RES-12 | T-12-02 | Prober + health resolve tier-0 via Resolve(role,0); dead static row not flapped under override; aggregate stays healthy with live pod; health payload changes are ADDITIVE/backward-compatible | unit | `cd gateway && go test ./internal/upstreams/ -run 'TestProbe_HonorsTier0Override\|TestProbe_TierGatingPreserved\|TestHealth_OverrideEffectiveTier0\|TestHealth_BackwardCompatNoOverride\|TestHealth_OverrideFieldsAreAdditive'` | probe_test.go / health_test.go | ⬜ pending |
| 12-01-T2 | 12-01 | 1 | RES-12 / D-13 | T-12-01 | Programmatic force-CLOSE write; EffectiveState honors State=="closed" → StateClosed; force-OPEN unchanged; TTL round-trips + short-close/long-open semantics documented | unit | `cd gateway && go test ./internal/breaker/ -run 'TestForceOverride_CloseShortCircuits\|TestForceOverride_OpenStillWorks\|TestForceOverride_WriteCloseRoundTrips\|TestForceOverride_DeleteClearsOverride'` | force_override_test.go | ⬜ pending |
| 12-01-T3 | 12-01 | 1 | RES-11 / RES-13 | — | DialFallthroughTotal + PrimaryDeathDetectedTotal counters defined/registered (single-owner for Wave-2); alertable label values documented | build | `cd gateway && go build ./internal/obs/ && go vet ./internal/obs/` | metrics.go | ⬜ pending |
| 12-02-T1 | 12-02 | 2 | RES-11 / D-05 | T-12-05 | Ready-tick Vast poll + 3-strike confirm; empty trackedID reconciled from open lifecycle row; transient exited does not drain; strike counters reset on enter-Ready | unit | `cd gateway && go test ./internal/primary/ -run 'TestEvaluateReady_EmptyTrackedIDReconciles\|TestEvaluateReady_DeathDetection\|TestEvaluateReady_TransientExitedDoesNotDrain\|TestEvaluateReady_NotFound3StrikeDrains\|TestEvaluateReady_HealthyNoop\|TestEvaluateReady_StrikesResetOnEnterReady'` | reconciler_test.go | ⬜ pending |
| 12-02-T2 | 12-02 | 2 | RES-11 / D-01 | T-12-04 | Confirmed death drains + force-opens local-* (long TTL) before destroy; billing-stop records a durable suppression marker + evaluateAsleep SKIPS re-provision while active (full Ready→Draining→Asleep→evaluateAsleep path); A1 resolved from committed 11-06 evidence + fallback; distinct death event | unit | `cd gateway && go test ./internal/primary/ -run 'TestDeath_HostYankDrainsAndForceOpens\|TestDeath_BillingStopRecordsSuppression\|TestEvaluateAsleep_BillingStopSuppressesReprovision\|TestDeath_BreakersForceOpenedBeforeDestroy\|TestDeath_BillingStopFallbackSignal'` | reconciler_test.go | ⬜ pending |
| 12-02-T3 | 12-02 | 2 | RES-11 / D-03 | T-12-06 | Alerter subscribes PrimaryEventsChannel; severityForPrimary → critical with distinct billing-stop vs host-death title; malformed → error not panic | unit | `cd gateway && go test ./internal/alert/ -run 'TestSeverityForPrimary_BillingStopCritical\|TestSeverityForPrimary_HostDeathCritical\|TestSeverityForPrimary_Malformed\|TestSeverityFor_RoutesPrimaryChannel'` | severity_test.go | ⬜ pending |
| 12-02-T4 | 12-02 | 2 | RES-12 / D-13 | T-12-14 | markReady force-closes stale local-* breakers on pod Ready (SHORT TTL, best-effort, after OverrideTier0) — symmetric to D-04 force-open (long TTL) | unit | `cd gateway && go test ./internal/primary/ -run 'TestMarkReady_ResetsStaleBreakers\|TestMarkReady_ForceCloseAfterOverrideTier0\|TestMarkReady_ForceCloseBestEffort'` | reconciler_test.go | ⬜ pending |
| 12-03-T1 | 12-03 | 2 | RES-13 / D-06 | T-12-11 | fallthroughRoundTripper signals errDialFailedFallthrough ONLY on pre-byte connection-class dial errors; response-timeout/5xx pass through | unit | `cd gateway && go test ./internal/proxy/ -run 'TestIsConnectionClass_DialRefused\|TestIsConnectionClass_DNSError\|TestIsConnectionClass_ResponseTimeout\|TestIsConnectionClass_Nil\|TestFallthroughRoundTripper_SignalsOnDial'` | transport_test.go | ⬜ pending |
| 12-03-T2 | 12-03 | 2 | RES-13 / D-07 / D-08 / D-10 | T-12-08 / T-12-11 | Custom ErrorHandler suppresses sentinel write (ZERO writes before tier-1 dispatch, proven by wrapped ResponseWriter) + preserves 502 for other errors; dispatchResult carries fallthrough back; connection-class dial failure falls normal tenant through tier-1 cascade (200); sensitive → 503 NEVER tier-1; timeout/5xx no fallthrough; body replayed across cascade hops incl. GetBody==nil; over-cap STT skips fallthrough | integration | `cd gateway && go test ./internal/proxy/ -run 'TestErrorHandler_SuppressesSentinelNoWrite\|TestErrorHandler_PreservesNonSentinel502\|TestDispatcher_NoWriteBeforeTier1Dispatch\|TestDispatcher_DialFailureFallsThrough\|TestDispatcher_CascadeOnDialFailure\|TestDispatcher_SensitiveNeverFallsThrough\|TestDispatcher_StreamingFallsThroughPreByte\|TestDispatcher_ResponseTimeoutDoesNotFallThrough\|TestDispatcher_BodyReplayedAcrossCascade\|TestDispatcher_GetBodyNilBuffered\|TestDispatcher_STTOverCapSkipsFallthrough'` | dispatcher_test.go / transport_test.go | ⬜ pending |
| 12-04-T1 | 12-04 | 3 | RES-11/12/13 | T-12-12 / T-12-17 | Dev chaos UAT scenario sheet authored (S1-S5 with PASS/FAIL slots + authoritative audit_log query + tier-1/Vast-credit preflight record) | doc | `test -f .../12-04-DEV-CHAOS-UAT.md` | 12-04-DEV-CHAOS-UAT.md | ⬜ pending |
| 12-04-T2 | 12-04 | 3 | RES-13 / D-16 / D-18 | T-12-12 / T-12-13 / T-12-17 | Dev chaos kill: tier-1/credit preflight recorded; Ready→Draining→Asleep death detection; zero connection-class 502 (audit_log); sensitive 503 | manual (HUMAN-UAT) | — | — | ⬜ pending |
| 12-05-T1 | 12-05 | 4 | RES-13 / D-16 / D-18 | T-12-18 | Prod chaos re-run (11-07 recipe): tier-1/credit preflight recorded; zero connection-class 502 during kill window; CAP-01 saturation baseline doc | manual (HUMAN-UAT) | — | — | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

> **Wave 0 test stubs are written INLINE via `tdd="true"` on each code-producing task (RED test before implementation), NOT as a separate Wave 0 plan.** Each Wave-1/Wave-2 task in plans 12-01/12-02/12-03 carries a `<behavior>` block enumerating the RED tests it must author and pass; the executor writes the failing test first, then the implementation. There is no standalone Wave 0 plan to create. The boxes below are covered by the corresponding task IDs in the Per-Task Verification Map.

- [x] Unit test stubs for Ready-tick death classification + enter-Ready strike reset (`internal/primary/`) — 12-02-T1 (inline, tdd=true)
- [x] Unit test stubs for billing-stop suppression marker + evaluateAsleep skip (`internal/primary/`) — 12-02-T2 (inline, tdd=true)
- [x] Unit test stubs for prober Resolve parity + health backward-compat (`internal/upstreams/`) — 12-01-T1 (inline, tdd=true)
- [x] Unit test stubs for connection-class detection + ErrorHandler sentinel suppression + body replay (`internal/proxy/`) — 12-03-T1/T2 (inline, tdd=true)
- [x] Unit test stubs for breaker force-close + markReady reset (`internal/breaker/`, `internal/primary/`) — 12-01-T2, 12-02-T4 (inline, tdd=true)
- [x] Unit test stubs for severityForPrimary alert classification (`internal/alert/`) — 12-02-T3 (inline, tdd=true)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Dev chaos kill zero-502 rehearsal | RES-11/12/13 (D-16) | Requires live Vast pod kill on dev + real traffic; destructive + cheap-pod spend | Execute 12-04-DEV-CHAOS-UAT.md: RECORD tier-1/credit preflight, provision cheapest qualified pod (D-17), load at ~20 concurrency, Vast API DELETE, sign S1-S5 |
| Prod chaos re-run zero-502 gate | RES-13 (D-16/D-18) | Requires live Vast pod kill + real traffic; destructive + costs ~$0.80-1.50 | Re-run 11-07 chaos recipe: RECORD tier-1/credit preflight, provision qualified pod (D-17), load via 11-07 recipe, Vast API DELETE, assert zero `upstream_unreachable` 502s T+0..end; 503 `sensitive_block` expected |
| Billing-stop critical alert fan-out | RES-11 (D-03) | Alert delivery (Chatwoot+ClickUp+Brevo) end-to-end needs live channels | Trigger detection in dev with mocked Vast status `exited`/`intended_status=stopped`; confirm critical alert with "Vast account sem crédito" title |
| `gatewayctl primary state` coherence | RES-11 (D-05) | CLI output inspection against live routing table | After force-up + kill cycle, `pod_url`/`lifecycle_id` must match proxy routing table |
</content>
