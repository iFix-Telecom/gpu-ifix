---
status: EXECUTED-PASS
phase: 12-gateway-resilience-remediation
plan: 05
slug: prod-chaos-gate
date: 2026-06-13
target: ai-gateway-prod (n8n-ia-vm, /opt/ai-gateway-prod)
gates: [D-16, D-18, RES-08, RES-11, RES-12, RES-13, CAP-01]
---

# Phase 12 Plan 05 — PROD Chaos Gate (acceptance gate)

> The phase's acceptance gate: re-run the 11-07 chaos recipe against the **PROD**
> gateway and assert the D-18 hard gate — **zero connection-class 502
> `upstream_unreachable`** in the kill window. This is the live proof that
> RES-11 + RES-12 + RES-13 make the gateway survive a primary death autonomously
> in production, the precise failure the 100×502 of 11-07 produced.

## Prod upgrade prerequisite (executed 2026-06-13)

The prod gateway was **224 commits behind** (revision `e483d2bf`, 2026-05-28) — it
predated RES-11/12/13 entirely and ran the legacy env shape format. Upgrading it was
a prerequisite for this gate:

- Migrated `/opt/ai-gateway-prod/.env`: removed legacy `PRIMARY_VAST_PRICE_CAP_DPH` /
  `PRIMARY_GPU_NAME` / `PRIMARY_NUM_GPUS` (hard-fail on the new build) → per-shape
  canonical vars; added the 4 weight SHAs (qwen/whisper/bgem3/chatterbox) + the
  `PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS=3600` + chatterbox key.
- Deployed `develop-3eea608` (the same image validated in dev). Boot crash-looped on
  `column "tier_priority" does not exist` (schema behind) → applied the pending goose
  migrations (up to version 29, validated in dev). **~1.5min prod downtime**
  (17:23–17:24Z); rollback was staged (`ifix-ai-gateway:rollback-pre12`) but the
  fix-forward migration resolved it. Gateway came up healthy on `develop-3eea608`.
- Primary shape set to **1×RTX 3090** (operator decision: match the dev-validated
  shape/concept rather than the prior 1×5090; cheaper, identical chaos semantics).

## Pre-flight (captured BEFORE kill)

- **PF-1 Vast credit:** $17.45 → sufficient ✅
- **PF-2 tier-1:** OpenRouter `/models` HTTP 200 (fallback target confirmed LIVE at
  kill time — zero-502 attribution valid) ✅
- **PF-3 new image:** gateway revision `3eea608`; pod image `converseai-primary-pod:develop`
  with mc + openssh + chatterbox-offline baked ✅

## Chaos run

- Primary pod: Vast machine 129536 (California, US, 1×RTX 3090, $0.143/h), instance 40853997.
- **KILL T+0 = 2026-06-13T20:44:26Z** (Vast API DELETE during ~12-concurrency load
  with a sensitive-tenant stream).

## Results

```
S1 RES-11 death detection            [x] PASS
S2 RES-13 zero connection-class 502  [x] PASS  (D-18 HARD GATE)
S3 RES-08/D-10 sensitive 503         [x] PASS
S5 cleanup + spend                   [x] PASS
```

### S2 — D-18 HARD GATE (authoritative audit_log query)
```
SELECT count(*) FROM ai_gateway.audit_log
WHERE ts >= '2026-06-13T20:44:26Z'::timestamptz
  AND error_code='upstream_unreachable' AND data_class='normal';
-- result: 0   (MUST be 0)  ✅
```
Kill-window breakdown: normal **200 via openrouter-chat = 109**, normal 200 via
emergency_pod_llm = 6, sensitive **503 blocked_sensitive = 13**. **ZERO
`upstream_unreachable`.** vs 11-07: 100× `upstream_unreachable` 502, zero failover.

### S1 — RES-11 death detection
Death poll on Ready tick returned `no_such_instance`; 3-strike confirm
(strike 1→2→3, confirm_at=3) → `primary death confirmed on Ready tick cause=not_found`
(instance 40853997, lifecycle 29) → drain complete (inflight=0) → Destroying → asleep.
FSM reached asleep ~41s after T+0; death CONFIRMED ~3s after T+0. Autonomous, no
operator force-down — the SEED-011 fix proven in prod.

### S3 — RES-08 / D-10 sensitive
13 sensitive-tenant requests returned 503 `blocked_sensitive`; ZERO sensitive rows
routed to an external tier-1 upstream. Hard gate D-10 holds live in prod.

### S5 — cleanup + spend
Instance 40853997 GONE (0 orphan instances post-kill). Session Vast spend ≈ $1.94
total (dev 12-04 ~$1.49 + prod 12-05 ~$0.45); prod credit 17.89 → 17.44.

## Sign-off

```
Operator: Claude (autonomous exec, user-authorized)    Date: 2026-06-13 (UTC)
Overall verdict (D-16/D-18 prod gate): ALL PASS — RES-11+RES-12+RES-13 survive a
primary death autonomously in production with zero connection-class 502.
CAP-01 saturation decision doc shipped: docs/CAP-01-saturation-decision.md
```

## Post-run prod state note

Prod gateway is now on `develop-3eea608` with RES-11/12/13. The primary shape was
switched to 1×3090 during this gate (was 1×5090). **OPEN DECISION for the operator:**
keep prod at 1×3090 (cheaper, dev-aligned) or revert to 1×5090 (full GPU offload for
whisper). The `.env` backup `/opt/ai-gateway-prod/.env.bak-pre12-upgrade-2026-06-13T2019Z`
holds the pre-upgrade 5090 config.

## Cross-ref
- Dev gate: `12-04-DEV-CHAOS-UAT.md` (all gates PASS).
- Baseline: `.planning/phases/11-prod-hardening/11-06-EVIDENCE.md`.
- CAP-01: `docs/CAP-01-saturation-decision.md`.
