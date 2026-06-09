---
phase: 11
plan: 11-07
slug: chaos-kill-primary-live-uat
status: artifact-shipped-live-deferred
date: 2026-05-27
operator: pedro (orchestrator-driven)
spend_usd: 0.00
---

# Phase 11 PRD-02 Chaos Primary Kill — Artifact shipped, live UAT deferred

## Summary

Plan 11-07 ships the chaos hook script `scripts/chaos/vast-delete.sh` as
the primary deliverable. The live chaos UAT (mid-load Vast API DELETE +
90s observation + cleanup) **was NOT executed in this session** because the
chaos path depends on plan 11-06 having put the primary into `ready`/`verifying`
state, and 11-06 is blocked on a primary reconciler silent-hang tech debt
(see `11-06-EVIDENCE.md`).

Re-run the chaos UAT once tech debt #1 (reconciler silent hang) is fixed
and 11-06 captures a baseline.

## Artifact: scripts/chaos/vast-delete.sh

**Location**: `/home/pedro/projetos/pedro/gpu-ifix/scripts/chaos/vast-delete.sh`
**Permissions**: 0755 (executable)
**Shebang**: `#!/usr/bin/env bash`
**Strict mode**: `set -euo pipefail` (Pattern E)
**Lint**: `bash -n` clean

### Reviews-folded contract (all closed at the artifact level)

| Review finding | How the artifact closes it |
|----------------|----------------------------|
| `[reviews HIGH #4]` no raw secrets | `VAST_AI_API_KEY` referenced ONLY as env var; argv never carries the token; log prints `${TOKEN:0:4}****` prefix-only attribution; tenant keys referenced as `IFIX_KEY_*` env-var labels |
| `[reviews MEDIUM #1]` FIXED 90s observe-then-intervene | Hard-coded `CHAOS_OBSERVE_SECONDS=90` default; polling loop tries every 5s for the full window; the script does NOT issue `force-up`; final-state decision branch logs `MANUAL_INTERVENTION_REQUIRED` (with the exact ssh command operator must run) only when FSM did not auto-recover |
| `[reviews MEDIUM #2]` allowed-error-class budget | Final log block prompts operator to check Sentry breadcrumbs for 500 panic / 502 bad_gateway during `T+0..T+60s` (hard gate). 503 transient / 503 breaker_open / 504 timeout permitted budgets documented in the plan + verified post-run |
| `[reviews LOW #3]` JSON-preferred instance-id extraction | `resolve_instance_id()` tries `gatewayctl primary state --json \| jq -re '.pod_instance_id // .instance_id'` first; on `--json` unsupported or empty, falls back to `awk '/^pod_instance_id/{print $2}'` with a WARN log line |
| `[reviews LOW #5]` Vast DELETE 404 idempotent + connect-timeout + 1 retry | `curl -X DELETE --connect-timeout 10 --max-time 30`; 200/202/204 = ack; 404 = idempotent gone (log + treat as success); 5xx = sleep 2s + retry once; non-5xx non-success = die exit 3 |

### Self-test (dry-run on ops-claude, primary asleep, no Vast calls)

```
VAST_AI_API_KEY=dummy-token-for-help-only ./scripts/chaos/vast-delete.sh --dry-run --allow-no-primary
```

Output verified:
- Token prefix-only attribution (`dumm****`) — no raw token in log
- Primary state read via SSH to `n8n-ia-vm` → `asleep` (correct, FSM was reset after 11-06 abort)
- JSON path tried first → falls back to text grep with WARN log
- Instance ID `capture-only` placeholder (no Vast DELETE issued)
- 90s observation window ran fully, snapshots written to `/tmp/chaos-snapshots.*`
- Final-state branch logged final state correctly

`bash -n` clean. `--help` parses inline doc header (lines 2–28).

## Live chaos UAT — DEFERRED

### Why deferred

Plan 11-07 task 06 ("Issue Vast DELETE under sustained load") requires:
1. Plan 11-06 to have established baseline (P95 + per-upstream attribution captured)
2. Primary pod in `state=ready` with live load flowing
3. Active `pod_instance_id` to DELETE

Constraint at session end:
- Primary reconciler silent-hang prevents reaching `ready` (see `11-06-EVIDENCE.md`)
- 11-06 deferred until reconciler fixed → 11-07 chain inherits same defer

### Re-run protocol (once reconciler fixed)

1. Fix primary reconciler silent-hang (carry-forward tech debt #1).
2. Re-run 11-06 — establish baseline + verify primary reaches ready under sustained load.
3. With primary still ready + load flowing in another terminal, execute:
   ```bash
   VAST_AI_API_KEY=<from CLAUDE.md> ./scripts/chaos/vast-delete.sh
   ```
4. Script will:
   - Read primary FSM state via `gatewayctl primary state` (or `--json` if supported)
   - Resolve `pod_instance_id`
   - `curl -X DELETE` against Vast API with retry on 5xx
   - Poll FSM + `local-llm` breaker state every 5s for 90s
   - Log AUTO_RECOVERY vs MANUAL_INTERVENTION_REQUIRED decision
5. Operator then fills the EVIDENCE table (per the plan's task 08):
   - Timeline of FSM transitions (Ready → Draining → Destroying → Asleep OR Ready → Draining → Recovering → Ready)
   - DELETE response code (200 / 202 / 204 / 404 idempotent)
   - 90s polling snapshot table (script writes this automatically to `$SNAPSHOTS_FILE`)
   - Time-to-breaker-OPEN (T+N seconds)
   - P95 latency during chaos window (compared against PRD-02 budget)
   - Allowed-error-class histogram (count by HTTP status) — ZERO 500 + ZERO 502 required
   - `auto_recovery` vs `manual_intervention` flag with timestamp
   - Vast cleanup confirmation: `curl /api/v0/instances/` returns the instance is gone

## Gates verdict (capture-only)

| Gate | Status | Note |
|------|--------|------|
| script_exists_and_executable | PASS | `scripts/chaos/vast-delete.sh` 0755, `bash -n` clean |
| reviews_HIGH_4_no_secrets | PASS | `grep -rE 'ifix_sk_\|Bearer [a-f0-9]{60}' scripts/chaos/vast-delete.sh` returns 0 matches |
| reviews_MEDIUM_1_90s_observe | PASS | hard-coded `CHAOS_OBSERVE_SECONDS=90`, no early force-up path in script |
| reviews_MEDIUM_2_error_budget | DEFERRED | depends on live run; documented as final-log prompt |
| reviews_LOW_3_json_extraction | PASS | `resolve_instance_id` tries `--json` first, awk fallback |
| reviews_LOW_5_idempotent_delete | PASS | `vast_delete()` handles 200/202/204/404/5xx-retry/other-fail explicitly |
| live_chaos_executed | DEFERRED | gated on tech debt #1 (reconciler hang) + 11-06 baseline |

## Secret hygiene attestation

Zero raw API keys, Authorization headers, request bodies, response bodies, DSNs,
or PII in this evidence file. All Vast API references use `${VAST_AI_API_KEY}`
env-var label. The script's token-prefix log line emits only first 4 characters
followed by `****`.

Pre-commit grep gate:
```
grep -rE 'ifix_sk_[a-z0-9]{20,}|Bearer [a-f0-9]{60}' scripts/chaos/vast-delete.sh \
  .planning/phases/11-prod-hardening/11-07-EVIDENCE.md
```
returns 0 matches (verified manually before commit).
