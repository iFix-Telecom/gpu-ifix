---
phase: 11-prod-hardening
plan: 11-08
subsystem: chaos-engineering
tags: [chaos, iptables, netns, RES-08, breaker, sensitive-tenant, smoke-test, audit-pipeline]

# Dependency graph
requires:
  - phase: 11-prod-hardening
    provides: "11-04: smoke-sensitive-failover.py forced-open race fix (D-18.1) consumed by Segment B"
  - phase: 10-prod-deploy-ai-gateway
    provides: prod gateway on n8n-ia-vm + bd_ai_gateway_prod audit schema
  - phase: 06.9-cutover-validation
    provides: gatewayctl breaker force-open subcommand (Segment B induce mode)
provides:
  - "scripts/chaos/openrouter-iptables-drop.sh: container-netns-scoped OpenRouter egress DROP with host iptables sha256 equality guard (exit 3 on mismatch)"
  - "PRD-03 live validation: breaker openrouter-chat opens by NATURAL observation under packet drop"
  - "RES-08 invariant proven live: sensitive tenants get 503 upstream_unavailable_for_sensitive_tenant; zero external routing"
  - "Allowed-error-class budget verified T+0..T+122s: ZERO 500 panic, ZERO 502 bad_gateway"
  - "Audit label gap discovery → fixed in-session via 5-PR chain (upstream='blocked_sensitive' on RES-08 503 path)"
affects: [11-09-incident-runbook, 11-10-cascade-close]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Chaos rules applied ONLY inside container netns via nsenter --target $PID --net; host OUTPUT chain never touched (sha256 pre==post hard gate)"
    - "Natural-observation segment NEVER co-mingled with forced-open segment (separate runs, full cleanup between)"
    - "DNS re-resolve loop during chaos window (CHAOS_RERESOLVE_LOOP=1) so Cloudflare IP rotation cannot leak egress mid-test"

key-files:
  created:
    - scripts/chaos/openrouter-iptables-drop.sh
    - .planning/phases/11-prod-hardening/11-08-EVIDENCE.md
  modified: []

key-decisions:
  - "Tier-2 OpenAI fallthrough declared OUT OF SCOPE for this plan (per plan Pitfall) — Segment A validates tier-1 drop behavior with tier-0 asleep, yielding controlled 503 upstream_unavailable for normal tenants"
  - "Initial 'audit pipeline silent since 2026-05-25' diagnosis REFUTED as diagnostic target mismatch — correct DB is bd_ai_gateway_prod (post Phase 10-02 cutover), legacy bd_ai_gateway frozen by design"
  - "Real residual gap (audit upstream='llm' instead of 'blocked_sensitive' on RES-08 503) fixed in-session via 5-PR chain #8 fdc44cf / #9 23bbe01 / #11 7d0b345+2d30a60 / #12 1365b75 / #13 bdd29b4 — Segment B re-validated 4/4 GREEN 2026-05-28T20:48Z, flipping PRD-03 passed_partial → passed"

patterns-established:
  - "Chaos scripts MUST capture host-level firewall state hash pre-apply and verify equality post-cleanup as a CRITICAL exit-3 gate"
  - "Audit-pipeline diagnostics MUST target bd_ai_gateway_prod (ai_gateway schema), never the legacy bd_ai_gateway"

requirements-completed: [PRD-03]

# Metrics
duration: "~2h live chaos (Segment A 122s window + Segment B smoke + audit drill-down) + 5-PR fix chain follow-up"
completed: 2026-05-28
---

# Phase 11 Plan 08: chaos-openrouter-drop Summary

**Live PRD-03 chaos test on prod: container-netns iptables DROP of OpenRouter egress → breaker `openrouter-chat` opened by natural observation, RES-08 sensitive-block 503 verified live with zero 500/502, host iptables proven untouched via sha256 equality. Segment B smoke initially 2/4 exposed the audit `blocked_sensitive` label gap — fixed in-session (5-PR chain), re-validated 4/4 GREEN, PRD-03 flipped to `passed`.**

## What was done

- **Segment A (natural observation) — 3/3 gates PASS:** `openrouter-iptables-drop.sh apply` installed per-IP + CF CIDR DROP rules inside the ai-gateway container netns only (122s window, DNS re-resolve loop active). Breaker went closed → open without any force-open. Sensitive tenant (telefonia): HTTP 503 `upstream_unavailable_for_sensitive_tenant` + retry_after=30 (RES-08). Zero 500 panic / zero 502 in the error budget window. Cleanup verified: 0 residual tagged rules, host OUTPUT sha256 pre == post.
- **Segment B (forced-open smoke regression):** ran to completion without hanging — proves the 11-04 D-18.1 `OPEN_LIKE_STATES` race fix live. Initial 2/4 gates (audit_decision + never_external FAIL) traced to the audit label gap, NOT an RES-08 violation (zero content rows confirmed sensitive payload never stored).
- **Audit drill-down:** original "audit silent since 2026-05-25" claim refuted — wrong DB target (legacy `bd_ai_gateway` vs prod `bd_ai_gateway_prod`). Prod audit healthy (99 rows, continuous writes). Real gap: RES-08 503 path tagged `upstream='llm'` instead of `'blocked_sensitive'`.
- **Fix chain (addendum 2026-05-28T20:48Z):** 5 PRs landed (#8 fdc44cf, #9 23bbe01, #11 7d0b345+2d30a60, #12 1365b75, #13 bdd29b4); live smoke re-run 4/4 GREEN for both streaming + non-streaming sensitive paths; `audit_upstream='blocked_sensitive'` confirmed in `bd_ai_gateway_prod`.

## Deviations

- Segment B required two sessions: initial run surfaced the audit label gap; the 4/4 GREEN came after the same-day 5-PR fix chain (recorded in 11-VERIFICATION.md addendum).
- Tier-2 fallthrough validation deferred by design (plan Pitfall) — not a gap.

## Self-Check: PASSED

- scripts/chaos/openrouter-iptables-drop.sh exists, 0755, `bash -n` clean ✓
- 11-08-EVIDENCE.md committed (43f0e04) ✓
- PRD-03 = passed in 11-VERIFICATION.md prds_status ✓
- Zero `ifix_sk_` literals in evidence (env-var labels only) ✓
