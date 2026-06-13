---
phase: 11
plan: 11-07
subsystem: ai-gateway / chaos-engineering
tags: [chaos, prd-02, failover, seed-011, vast, reconciler]
requires: [11-06]
provides: [chaos-vast-delete-script, prd-02-chaos-evidence, seed-011-confirmation]
affects: [11-09-runbook-incidents, gateway-primary-reconciler]
tech-stack:
  added: []
  patterns: [pattern-e-bash, observe-first-window, dispatcher-attribution-via-audit]
key-files:
  created:
    - .planning/phases/11-prod-hardening/11-07-EVIDENCE.md
  modified:
    - scripts/chaos/vast-delete.sh
decisions:
  - "PRD-02 chaos result recorded as-is (HARD-GATE FAIL); no retry-shopping — the failure IS the deliverable"
  - "Failover evidenced via audit_log.upstream column, not breaker state (prober bug makes /v1/health/upstreams unreliable)"
  - "Instance id sourced from Vast API list (gatewayctl display empty — SEED-011 secondary bug)"
metrics:
  duration_min: 35
  completed: 2026-06-12
  tasks: 3
  files: 2
  vast_spend_usd: 0.20
---

# Phase 11 Plan 11-07: Chaos Kill Primary Live UAT Summary

Live mid-load Vast API DELETE of the active primary (instance 40697682) executed
against prod; the gateway FAILED to fail over and served 100× HTTP 502 in the
T+0..T+60s window, reproducing SEED-011 live — a HARD-GATE failure that is the
intended chaos deliverable, with zero panics and clean instance cleanup.

## What was done

| Task | Outcome | Commit |
|------|---------|--------|
| 11-07-01 — Author/extend `vast-delete.sh` | Script already shipped (ffcee00); extended with literal `OBSERVE-FIRST` tags, `OPEN_AT` tracking, explicit `DELETE_STATUS`, parseable Pattern E stdout summary | c12a22c |
| 11-07-02 — Execute PRD-02 chaos mid-load | Load warmed (chat 133×200 pre-chaos), DELETE 40697682 → HTTP 200; 90s OBSERVE-FIRST window; SEED-011 reproduced; post-90s force-down/force-up recovery | (live run; evidence in 4cadfff) |
| 11-07-03 — Commit evidence + grep gate | `11-07-EVIDENCE.md` extended (prior deferral preserved in appendix); real-secret gate clean | 4cadfff |

## Deliverable status

- **vast-delete.sh:** ~10.7 KB, `bash -n` clean, executable. JSON-preference wired (yes —
  tries `gatewayctl primary state --json` then awk text fallback). 404 idempotency wired
  (yes — `idempotent_already_deleted`). 90s OBSERVE-FIRST wired (yes — `CHAOS_OBSERVE_SECONDS=90`,
  literal `OBSERVE-FIRST` tags, no auto-force-up). `--connect-timeout 10 --max-time 30` + 1× 5xx retry.
- **Pattern E summary block added this session:** `delete_status` / `open_at` /
  `vast_instance_id` / `observe_window_s` / `auto_recovery` / `fsm_at_t90s` / `breaker_at_t90s`.

## Chaos timeline (UTC)

- T+0 (12:13:50Z): DELETE instance 40697682 → HTTP 200 `{"success":true}` (`delete_status=killed`); Vast `gone` instantly.
- T+0..T+16s: 54 chat 200 (in-flight to pod IP before host network teardown).
- T+0..T+57s: 100× HTTP 502 `upstream_unreachable` (chat upstream=llm, STT upstream=stt); time-to-first-502 = 0s.
- T+0..T+90s: FSM stayed `ready` every sample — NEVER auto-advanced (no death detection).
- T+90s: OBSERVE-FIRST decision = `auto_recovery=false`, `manual_intervention=true`.
- 12:16:52Z: `force-down` → FSM ready→draining→asleep (<5s).
- 12:17:11Z: `force-up` → FSM asleep→provisioning; new instance 40703493 (machine 31810, $0.48/h) booting.

## Gates verdict

| Gate | Threshold | Measured | Verdict |
|------|-----------|----------|---------|
| HTTP 500 panic (T+0..T+60s) | 0 (HARD GATE) | 0 | **PASS** |
| HTTP 502 bad_gateway (T+0..T+60s) | 0 (HARD GATE) | 100 | **FAIL** |
| Tier-1 takeover (dispatcher) | required | none (upstream stayed dead `llm`/`stt`) | **FAIL** |
| OBSERVE-FIRST 90s before intervention | required | honored (no force-up before t+90s) | **PASS** |
| Vast cleanup (instance count for killed primary) | 0 | 0 (no orphan) | **PASS** |
| Zero raw secrets in plan/evidence | required | clean (real-secret gate) | **PASS** |

**auto_recovery vs manual_intervention:** `manual_intervention=true` at t+90s+Δ
(12:16:52Z, ~182s post-DELETE) — FSM never auto-recovered; force-down/force-up required.

## Critical finding (the deliverable)

**SEED-011 reproduced live under a real DELETE.** The steady-state Ready reconciler did
not detect the destroyed instance (its tracked `pod_instance_id` was already empty from
the 11-06 force-up — the SEED-011 secondary display/state-hash bug), so FSM stayed `ready`
and the dispatcher kept routing LLM/STT to the dead pod IP, returning sustained 502
`upstream_unreachable` with NO tier-1 OpenRouter failover. The `force-down` path advanced
the FSM correctly (proving the machinery works) — the missing piece is the autonomous
death-detection trigger for destroyed / `actual_status in {exited,stopped}` / empty-tracked-id
primaries. HIGH/CRITICAL; canonical RUNBOOK-INCIDENTS class-1 incident for 11-09 ingest.

## Deviations from plan

1. **[Rule 1 / adaptation] Instance id from Vast API, not gatewayctl** — SEED-011 empty
   display defeated `resolve_instance_id()`; id 40697682 sourced from the Vast instance list
   per orchestrator live-state guidance. No script edit required.
2. **[Rule 1 / adaptation] Failover evidenced via `audit_log.upstream`** — prober bug makes
   breaker state / `/v1/health/upstreams` unreliable; the audit dispatcher attribution
   (`upstream` + `status_code` + `error_code`) is the authoritative signal.
3. **[Plan deviation] Load at `--max-concurrency 20`** (vs 50) to keep the pre-chaos baseline
   clean; chaos 502 signal is concurrency-independent.
4. **[Observed-system, NOT fixed]** No gateway code edited — live reproduction of a
   pre-existing bug, surfaced as the deliverable.

## Spend

Vast ≈ $0.20 this run (killed pod fraction + re-up pod time). Cumulative Phase 11 ≈ $0.82
(11-06 $0.62 + this $0.20). Within $5 cap.

## Final instance state (for orchestrator cleanup awareness)

- Killed primary 40697682: **destroyed (count=0, no orphan)**.
- New recovery primary **40703493** (machine 31810, $0.48/h): `actual_status=loading`,
  FSM `provisioning` at SUMMARY-write time — healthy boot (image pull + weight load in
  progress), NOT a CDI fault. Expected to reach `ready` within a few minutes via the
  standard 11-06 force-up path. This is an INTENTIONAL running instance (post-chaos
  recovery), not a leaked orphan.

## Sanitization attestation

Zero raw API keys, Bearer tokens, Authorization headers, request/response bodies, DSNs,
or PII in `11-07-EVIDENCE.md` or `vast-delete.sh`. Vast key as `${VAST_AI_API_KEY}`,
tenant key as `${IFIX_KEY_CONVERSEAI}`. Real-secret grep gate clean (the only regex matches
are the documented gate-pattern strings themselves, not values).

## Cross-ref

- Evidence: `.planning/phases/11-prod-hardening/11-07-EVIDENCE.md`
- Script: `scripts/chaos/vast-delete.sh`
- Seed (now CONFIRMED): `.planning/seeds/SEED-011-fsm-ready-with-stopped-instance.md`
- Baseline: `.planning/phases/11-prod-hardening/11-06-EVIDENCE.md`

## Self-Check: PASSED

- Files exist: `11-07-EVIDENCE.md`, `11-07-SUMMARY.md`, `scripts/chaos/vast-delete.sh` — all FOUND.
- Commits exist: `c12a22c` (script extend), `4cadfff` (evidence) — both FOUND in git history.
- Real-secret gate: CLEAN across script + evidence + summary.
- STATE.md / ROADMAP.md: NOT modified (orchestrator owns those writes).
