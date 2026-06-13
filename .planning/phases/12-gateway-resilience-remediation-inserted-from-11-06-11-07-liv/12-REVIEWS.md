---
phase: 12
reviewers: [gemini, codex]
reviewed_at: 2026-06-13T00:04:15Z
plans_reviewed: [12-01-PLAN.md, 12-02-PLAN.md, 12-03-PLAN.md, 12-04-PLAN.md, 12-05-PLAN.md]
---

# Cross-AI Plan Review — Phase 12

## Gemini Review

# Plan Review: Phase 12 — Gateway Resilience Remediation

This phase provides a comprehensive remediation strategy for three critical resilience gaps identified during the Phase 11 live UATs. The plans are surgically designed to wire existing primitives (FSM machinery, 3-strike logic, override resolution) into the specific paths currently lacking them.

### Summary
The implementation plans for Phase 12 are of **exceptional quality**, demonstrating deep codebase awareness and a disciplined response to live-proven failures. The wave structure is logical: Wave 1 repairs observability and lands shared infrastructure; Wave 2 implements the core autonomous failover logic (FSM death detection and request-path fallthrough); and Waves 3/4 provide a rigorous dev-then-prod chaos gate. The plans effectively address the "silent killers" of the Phase 11 UAT—specifically the empty-trackedID bug (D-05) and the prober's ignorance of overrides (RES-12)—ensuring that the system's internal state, observability surfaces, and request routing finally agree on the truth.

---

### Strengths
*   **Prerequisite Prioritization:** Plan 12-02 correctly identifies that the empty-trackedID bug (**D-05**) is a hard prerequisite for death detection and fixes it first.
*   **Architectural Correctness (RES-13):** Intercepting dial failures at the `RoundTrip` level rather than in the `ErrorHandler` is the correct Go idiom to prevent "superfluous WriteHeader" errors and corrupted SSE streams.
*   **Symmetry in Breaker Control:** Implementing both force-OPEN on death (**D-04**) and force-CLOSE on `markReady` (**D-13**) ensures the breaker state transitions deterministically with the FSM, preventing a new pod from inheriting a stale "OPEN" state.
*   **Disciplined Alerting:** The plans include the necessary wiring to consume `PrimaryEvents` in the Alerter (Finding 1), enabling operator-actionable titles like "Vast account sem crédito" which distinguishes billing stops from host deaths.
*   **Safe Body Buffering:** Plan 12-03 addresses the risk of OOM on large STT uploads by mandating a check against the authoritative audio proxy ceiling before hardcoding a buffer cap.
*   **Authoritative Evidence:** The chaos UATs (12-04/05) correctly gate on `audit_log` error codes rather than aggregate latency or breaker state, ensuring the validation signal is not muddied by saturation or prober flaps.

---

### Concerns
*   **Assumption A1 (Vast Billing Signal): [LOW]** The exact field for identifying a billing stop is unconfirmed.
    *   *Mitigation:* Plan 12-02 Task 2 explicitly requires live inspection of the JSON payload and provides a substring-match fallback on the `status_msg` field.
*   **Prober/Health Consistency: [LOW]** A partial implementation of RES-12 (fixing `probe.go` but not `health.go`) would leave the system in a confusing state.
    *   *Mitigation:* Plan 12-01 Task 1 explicitly bundles both call-site swaps and adds a `TestHealth_OverrideEffectiveTier0` gate.
*   **Chaos Pod Selection Cost: [LOW]** Chaos kills are destructive and can burn budget if premium shapes are used.
    *   *Mitigation:* Decisions **D-16** and **D-17** mandate "price-first" selection and a dev-first rehearsal to minimize spend.

---

### Suggestions
*   **Prometheus Metric Placement:** Plan 12-01 Task 3 defines the counters, but ensure the labels (`tier1_served`, `chain_exhausted`) are documented in the Prometheus scraping config or dashboard instructions if they are meant to drive alerts.
*   **FSM Counter Persistence:** In Plan 12-02 Task 1, ensure the strike counters are reset not only on healthy observations but also explicitly when the reconciler transitions *into* the `Ready` state from `Provisioning` to ensure a clean start for every pod lifecycle.
*   **Documentation Alignment:** After Plan 12-01 ships the "override flag" in `/v1/health/upstreams`, the Dashboard UI (Phase 07) should be updated in its own phase to visualize this flag, rather than relying on raw breaker counts.

---

### Risk Assessment: LOW
The overall risk is low because the plans are **remediation-focused** and rely on established patterns already proven in the codebase.
*   **Justification:** The core logic (3-strike confirm, Resolve cascade) is already in-tree. The most complex new code (the RoundTripper) is isolated and heavily tested with specific failure-injection cases. The hard gate of "zero connection-class 502s" in prod provides an unambiguous success signal. The plans avoid scope creep by strictly deferring the CAP-01 implementation.

---

## Codex Review

## Summary

The Phase 12 plan set is strong overall: it is evidence-driven, maps directly to the three live-proven resilience failures, preserves the sensitive-tenant invariant, and has sensible wave ordering: observability/parity first, then FSM and dispatcher fixes, then dev/prod chaos gates. The plans are unusually explicit about known pitfalls, test expectations, and human UAT boundaries. Main risks are implementation complexity in RES-13, a few dependency/interface ambiguities between plans, and possible over-specification in tests that may force brittle implementation details rather than behavior.

## Strengths

- Clear traceability from requirements to plans: RES-11, RES-12, RES-13, CAP-01 are all covered.
- Good wave ordering: Plan 12-01 unblocks shared primitives and health truth before the more dangerous fixes.
- Strong preservation of RES-08: sensitive tenants never route to external tier-1 is repeatedly called out and tested.
- Good recognition that D-05 tracked instance repair is load-bearing, not cosmetic.
- Strong UAT discipline: dev chaos before prod chaos, price-first pod selection, audit_log as the authoritative zero-502 evidence.
- Good separation between automated code work and destructive/manual validation.
- Good attention to false positives: 3-strike death confirmation, no new FSM state, no retry after bytes written.
- CAP-01 is correctly fenced as doc-only, avoiding accidental load-shedding scope creep.

## Concerns

- **HIGH: RES-13 dispatcher design may be under-specified technically.** `httputil.ReverseProxy.ServeHTTP` does not naturally return transport errors to `dispatchTo`; a RoundTripper sentinel alone is not enough unless the proxy `ErrorHandler` captures the error into dispatcher-visible state without writing. The plan says “detect when ServeHTTP’s underlying RoundTrip returned errDialFailedFallthrough,” but does not define the exact ResponseWriter/ErrorHandler control flow needed to prevent the default 502.

- **HIGH: request body replay is a major risk.** Buffering bodies for retry, especially multipart STT and streaming/SSE contexts, is complex. The plan correctly calls for caps, but does not specify what happens when `GetBody` is already nil, whether middleware has already consumed body, or how the original body is restored before tier-0 dispatch and again before tier-1 cascade.

- **HIGH: billing-stop “no re-provision” policy is ambiguous.** The plan says billing-stop should not trigger re-provision, but also says death path calls `startDrain` and existing schedule loop decides. Unless a durable suppression/cooldown state is added or an existing gate is explicitly identified, the schedule loop may still provision immediately after Asleep during peak.

- **MEDIUM: force-close semantics can mask real failures.** A `"closed"` force override short-circuits breaker state. Even with a short TTL, it may briefly send traffic to a pod that passed provisioning but fails immediately afterward. This is probably acceptable, but the TTL and interaction with the Ready death poll should be specified.

- **MEDIUM: Plan 12-01 scope is broad for Wave 1.** It includes prober parity, health payload changes, breaker force-close machinery, and metrics. That is pragmatic for dependency ownership, but it increases blast radius before the core RES-11/RES-13 fixes.

- **MEDIUM: health payload compatibility risk.** Adding override fields and “overridden/standby” rows may break clients or dashboards if they expect a fixed schema/status set. The plan does not call for backward compatibility tests or versioning.

- **MEDIUM: tests may overfit implementation details.** Grep-based acceptance criteria like exact function names, string counts, or comments may fail valid implementations and encourage mechanical compliance.

- **MEDIUM: Plan 12-02 depends on live Vast JSON inspection but is marked autonomous.** If A1 requires a live billing-stopped instance or captured payload that is not available locally, the plan is not fully autonomous.

- **LOW: metrics are defined before behavior is wired.** This is fine for avoiding conflicts, but unused exported counters may temporarily exist after Plan 12-01. Not a functional problem.

- **LOW: CAP-01 doc may be too late.** It is in Wave 4, but the saturation decision could inform chaos concurrency choices. The plan already uses ~20 concurrency, so this is low risk.

## Suggestions

- Define the RES-13 control flow more concretely:
  - custom `ErrorHandler` should detect `errDialFailedFallthrough`
  - suppress writes for that sentinel
  - store/carry the signal back to dispatcher safely
  - ensure normal `ErrorHandler` behavior remains for all other errors

- Add a small explicit abstraction for dispatch result, for example `dispatchResult{fallthrough bool, wrote bool, err error}`, instead of relying on implicit side effects from `ServeHTTP`.

- Add tests proving no response was committed before fallthrough:
  - wrapped ResponseWriter records `WriteHeader`/`Write`
  - tier-0 dial failure must show zero writes before tier-1 dispatch

- Make billing-stop suppression explicit:
  - add a named state flag/cooldown/reason remembered by the reconciler, or
  - point to the exact existing provision gate that will block billing-stop reprovision
  - test a full Ready → Draining → Asleep → evaluateAsleep path for billing-stop

- Specify force-close TTL values:
  - short TTL for `"closed"` after markReady, e.g. 30-60s
  - longer TTL for `"open"` after death, e.g. until expected destroy/reprovision window
  - document why each is safe

- For health response changes, add backward compatibility acceptance:
  - existing fields remain unchanged
  - new fields are additive
  - existing dashboards/clients tolerate `overridden` or the chosen status value

- Split Plan 12-01 if execution risk is a concern:
  - 12-01A: prober/health parity
  - 12-01B: breaker force-close + metrics
  This is optional; current ownership rationale is valid.

- Reclassify the A1 live inspection in Plan 12-02 as a human checkpoint or replace it with committed evidence from 11-06/11-07 if available.

- Add explicit tests for `gatewayctl primary state` coherence, since D-05 is called out as operator-critical but most tests focus on internal `activeInstanceID`.

- In chaos sheets, require evidence that OpenRouter/tier-1 was healthy before the kill. Otherwise a zero-502 failure could be misattributed to RES-13 when the fallback target was unavailable.

## Per-Plan Assessment

### 12-01

Risk: **MEDIUM**

Good foundational plan. It addresses RES-12 directly and wisely includes shared primitives for later waves. Main concern is scope concentration: health schema changes plus breaker semantics plus metrics in one plan. The force-close `"closed"` override is the riskiest piece because it changes routing truth, not just observability.

### 12-02

Risk: **HIGH**

This is the most important FSM fix and mostly well designed. The 3-strike approach, D-05 repair, alert wiring, and breaker force-open are appropriate. The biggest gap is billing-stop suppression: the plan needs a concrete mechanism proving the schedule loop will not reprovision after a billing-stop. The live Vast-field dependency also weakens its autonomous claim.

### 12-03

Risk: **HIGH**

This plan tackles the hardest request-path behavior. The intent is correct: only pre-byte connection-class errors should fall through, sensitive tenants must not, and tier-1 cascade should reuse existing ordering. The technical mechanism needs sharper specification because `ReverseProxy` error handling can easily write a response before dispatcher logic can recover. Body buffering and replay are also high-risk.

### 12-04

Risk: **LOW-MEDIUM**

Strong dev UAT plan. It validates the combined behavior before prod and uses the right evidence source. Main improvement is to require tier-1 health preflight and make alert delivery optional vs required if alert channels are rate-limited or unavailable in dev.

### 12-05

Risk: **MEDIUM**

Good final gate and CAP-01 closure. The zero-502 audit_log gate is the right acceptance criterion. Risk comes from prod destructiveness and dependency on external Vast/tier-1 conditions. The sheet should explicitly record tier-1 health and current Vast credit before the kill.

## Overall Risk Assessment

**Overall risk: MEDIUM-HIGH.**

The plan quality is high and the scope maps well to the phase goals, but the implementation includes two genuinely tricky areas: `ReverseProxy` dial-failure interception with safe body replay, and billing-stop suppression without creating a provision loop. If those two are tightened before execution, risk drops to **MEDIUM**. The validation strategy is strong enough to catch most integration failures before prod.

---

## Consensus Summary

Two independent reviewers (Gemini, Codex). Gemini rates overall risk LOW; Codex rates MEDIUM-HIGH. The divergence is concentrated in RES-13 implementation mechanics and the billing-stop re-provision policy — Gemini judged the plans' intent sufficient, Codex dug into `httputil.ReverseProxy` control-flow specifics and found the plans under-specified there.

### Agreed Strengths
- **Wave ordering is correct** — Wave 1 lands shared primitives + observability truth (RES-12, force-close, metrics) before the dangerous FSM/dispatcher fixes; dev chaos before prod chaos (D-16).
- **D-05 trackedID repair correctly treated as load-bearing prerequisite**, not cosmetic, and sequenced first in Plan 12-02.
- **RES-08/D-10 sensitive-tenant invariant preserved and repeatedly tested** (hard gate in 12-03).
- **Breaker control symmetry** (force-OPEN on death D-04 / force-CLOSE on markReady D-13) prevents stale state inheritance.
- **Chaos UATs gate on `audit_log` error codes** (authoritative evidence), price-first pod selection (D-17), CAP-01 correctly fenced doc-only (D-19).
- **3-strike confirmation** avoids false-positive death detection on transient Vast states.

### Agreed Concerns
1. **A1 billing-stop signal unconfirmed** (Gemini LOW / Codex MEDIUM): both flag the live-Vast-JSON dependency. Codex goes further — Plan 12-02 is marked `autonomous: true` but A1 confirmation may require a live billing-stopped instance; suggests reclassifying the A1 inspection as human checkpoint or using committed 11-06 evidence.
2. **STT body buffering for retry is risky** (Gemini noted mitigation exists / Codex HIGH): Codex wants explicit handling for `GetBody == nil`, body already consumed by middleware, and restore-before-tier-0 + restore-before-tier-1 sequencing.

### Codex-Only HIGH Concerns (not raised by Gemini — investigate before execution)
1. **RES-13 ReverseProxy control flow under-specified**: a RoundTripper sentinel alone is insufficient — `ServeHTTP` does not return transport errors to the dispatcher; the `ErrorHandler` must detect `errDialFailedFallthrough`, suppress the default 502 write, and carry the signal back to dispatch logic. Suggests an explicit `dispatchResult{fallthrough, wrote, err}` abstraction + a wrapped-ResponseWriter test proving zero writes before tier-1 dispatch.
2. **Billing-stop "no re-provision" policy ambiguous**: D-01 relies on "schedule loop decides", but without a durable suppression/cooldown state or an explicitly identified existing gate, the schedule loop may immediately re-provision after Asleep during peak window (provision-fail loop with zero credit). Needs a concrete mechanism + a full Ready→Draining→Asleep→evaluateAsleep billing-stop test.

### Divergent Views
- **Overall risk**: Gemini LOW vs Codex MEDIUM-HIGH. Codex's rating is driven by the two HIGH concerns above; it states risk drops to MEDIUM if both are tightened before execution.
- **Plan 12-01 scope**: Codex flags Wave-1 scope concentration (parity + health schema + force-close + metrics) as MEDIUM blast-radius; Gemini sees the bundling as a strength (single-owner avoids conflicts). Codex's split suggestion (12-01A/B) marked optional.
- **Health payload compatibility**: Codex wants additive-only backward-compat acceptance criteria for `/v1/health/upstreams` schema changes; Gemini only suggests dashboard follow-up in a future phase.
- **Force-close TTL semantics**: Codex wants explicit TTL values specified (e.g., 30-60s closed after markReady vs longer open-after-death) and documented rationale; Gemini does not raise it.
- **Chaos preflight**: Codex wants tier-1 (OpenRouter) health + Vast credit recorded in the UAT sheet BEFORE the kill so a non-zero-502 result isn't misattributed; Gemini silent.
