---
title: Primary provisioning — allowlist is a preference not a hard filter → retries on broken hosts burn budget
trigger_condition: when touching the primary reconciler provisioning/offer-selection path (e.g. Phase 17 pod-config work, or any cost-optimization pass), OR when prod primary_lifecycles shows repeated first_health_pass_at=NULL lifecycles
planted_date: 2026-06-29
severity: MED
---

# SEED-020 — allowlist preference vs hard filter; health_timeout host burns full coldstart budget

## Finding (PROD, 2026-06-29)

The primary reconciler's `PRIMARY_VAST_MACHINE_ALLOWLIST` (prod=`7970,12863`) is a **preference pass, not a hard filter** (`gateway/internal/primary/reconciler.go` ~:1206-1259): it tries allowlisted offers first, and **if none is below the price cap it broadens to the full global qualified search**, picking the cheapest offer anywhere. On 2026-06-29 the good allowlisted machine (12863) had no offer below cap from ~08:30 until ~10:29 BRT, so the loop broadened and repeatedly selected the cheapest GLOBAL offers — which were broken hosts:

- **machine 104433** ($0.124/h): `OCI runtime create failed: error modifying OCI specification` — broken nvidia-container-toolkit/CDI on the host; container dies <30s.
- **machine 56883** ($0.122/h): container starts but **never health-passes** → burns the FULL 3600s coldstart budget (~R$0.61) before `health_timeout`.

9 failed lifecycles, ~R$0.70 wasted (R$0.61 of it the single health_timeout host). Diagnosed via `primary_lifecycles.first_health_pass_at IS NULL` (never healthy = boot failure/retry, NOT the pre-warm flap — ShouldStayUp fix a2cd691 is intact). Both hosts blocklisted in prod `.env` as the immediate fix.

## Two structural gaps worth fixing

1. **Allowlist should be a HARD filter during the lead/prewarm window** (or always) — broadening to un-vetted global hosts is what lands on broken hardware. Trade-off: a hard filter risks "no offer at all" when allowlisted machines are unavailable → would need a fallback policy (wait + retry on allowlist only, vs broaden with a tighter vetting). Decide the policy.

2. **The `health_timeout` path lets a non-OCI-failing-but-never-healthy host burn the entire 3600s coldstart budget** (R$0.61 each). An early-abort for hosts that haven't even started model load (e.g. no `/health` TCP within N seconds, or no model-load progress within a shorter sub-budget) would cap this dominant waste. The OCI-error retries are cheap (die in seconds); the health_timeout host is the real money sink.

## Pointers

- Manual mitigation today = blocklist append (see memory [[vast-flaky-host-za-97968]] for the recipe + the running list of bad hosts).
- This overlaps Phase 17 (dashboard pod-config control): once blocklist/allowlist/cap are DB-backed + dashboard-editable, the allowlist-policy decision becomes a config knob, but the hard-filter-vs-broaden LOGIC still needs a code change here.
- Evidence: `primary_lifecycles` rows 123-132 (2026-06-29), `reconciler.go:1206-1259` (offer selection), `:484` (ShouldStayUp, NOT the cause).
