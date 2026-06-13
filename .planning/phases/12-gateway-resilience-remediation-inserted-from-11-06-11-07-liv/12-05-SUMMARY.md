---
phase: 12-gateway-resilience-remediation
plan: 05
subsystem: infra
tags: [chaos-testing, prod, vast, capacity, saturation, failover]

requires:
  - phase: 12-04
    provides: dev chaos PASS (D-16 dev-first gate cleared)
provides:
  - Prod chaos acceptance gate PASS — RES-11/12/13 survive a primary death autonomously in production (zero connection-class 502)
  - Prod gateway upgraded 224 commits (e483d2bf → 3eea608) with RES-11/12/13 + schema migration to version 29
  - CAP-01 saturation decision doc (D-19 doc-only)
affects: []

tech-stack:
  added: []
  patterns: [prod env migrated legacy→per-shape; prod aligned to 1x3090 dev shape]

key-files:
  created:
    - docs/CAP-01-saturation-decision.md
    - .planning/phases/12-gateway-resilience-remediation-inserted-from-11-06-11-07-liv/12-05-PROD-CHAOS-GATE.md
  modified:
    - /opt/ai-gateway-prod/.env (n8n-ia-vm; not in repo — legacy→per-shape migration)

key-decisions:
  - "Prod chaos PASSED the D-18 hard gate: 0 upstream_unreachable 502 (vs 100x in 11-07)"
  - "CAP-01: adopt concurrency cap + admission control as primary lever; multi-GPU is scale-out path; SLO/pacing realism companion"
  - "Prod primary shape switched 5090→3090 to match dev (operator decision); revert path preserved in .env backup"

patterns-established:
  - "Prod gateway upgrade requires env migration (legacy→per-shape) + goose migrate before the new image boots"
---

## What was delivered

The phase acceptance gate: the 11-07 chaos recipe re-run against **production**
(`ai-gateway-prod`, n8n-ia-vm), proving RES-11/12/13 work together on a real Vast
kill in prod. Full signed results in `12-05-PROD-CHAOS-GATE.md`.

### Prod chaos result (D-18 hard gate)

| Gate | 11-07 (broken) | 12-05 prod (fixed) |
|------|----------------|--------------------|
| RES-11 death detection | FSM `ready` 25+min on dead pod | confirmed in ~3s (3-strike not_found) → drain → asleep |
| RES-13 zero-502 (D-18) | 100× `upstream_unreachable` | **0** — 109 normal reqs served 200 via tier-1 |
| RES-08 sensitive | n/a | 13 sensitive → 503 `blocked_sensitive`, never tier-1 |

### Prerequisite: prod gateway upgrade (224 commits)

Prod was on `e483d2bf` (2026-05-28), predating RES-11/12/13. Upgrade steps:
- Migrated `.env` legacy→per-shape (removed `PRIMARY_VAST_PRICE_CAP_DPH` et al that
  hard-fail the new build; added 4 weight SHAs + cold-start budget + chatterbox).
- Deployed `develop-3eea608`; hit `column "tier_priority" does not exist` → applied
  pending goose migrations to version 29. ~1.5min prod downtime, rollback staged.
- Prod primary shape switched to 1×3090 (match dev; operator decision).

### CAP-01 saturation decision (D-19 doc-only)

`docs/CAP-01-saturation-decision.md` analyzes the 11-06 baseline (chat **p95 21.7s**
@ concurrency 50 on 1×5090 — a single-GPU queueing-saturation signature). Decision:
adopt concurrency cap + admission control as the primary lever (bound the queue,
fast-shed with 429 instead of silent 20s degradation), keep multi-GPU as the
scale-out path, and instrument real prod concurrency before paying for B. Doc-only;
implementation is a future phase.

### Verification

Live D-18 gate: authoritative audit_log query returns 0 connection-class 502 for
normal tenants in the kill window. 0 orphan instances post-kill. Session Vast spend
≈ $1.94.

## Self-Check: PASSED
