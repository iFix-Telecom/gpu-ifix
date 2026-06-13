---
task_id: 260528-h8z
slug: smoke-audit-query-retry-loop
mode: quick
type: execute
wave: 1
status: complete
branch: gsd/phase-06.9-close
commit: 2d30a604784d69ea7f4a245d4fec2273f93c433c
seed: .planning/seeds/SEED-006-smoke-audit-query-flush-race.md
files_modified:
  - scripts/integration-smoke/smoke-sensitive-failover.py
date: 2026-05-28
---

# SEED-006 promotion — `smoke-sensitive-failover.py` audit-DB poll wrapper

## One-liner

Refactored `query_audit` in `smoke-sensitive-failover.py` into a bounded-deadline
poll wrapper (5.0s deadline / 0.25s interval) around a new `_query_audit_once`
helper, so the smoke stops racing the audit-writer's 1s flush timer and reports
honest gates against a correct gateway. No production code changed.

## What changed (single file, single commit)

`scripts/integration-smoke/smoke-sensitive-failover.py` (+74 / −22 LOC):

1. **New module constants** placed directly below the existing `INDUCE_POLL_*`
   block, with a comment citing SEED-006 and the writer flush rule:
   - `AUDIT_POLL_DEADLINE_S = 5.0`
   - `AUDIT_POLL_INTERVAL_S = 0.25`

2. **New `_query_audit_once(pg_dsn, request_id) -> dict[str, Any]`** helper at
   module scope. Contains the original single-shot psycopg query body verbatim:
   - `SELECT upstream FROM ai_gateway.audit_log WHERE request_id = %s`
   - `SELECT COUNT(*) FROM ai_gateway.audit_log_content WHERE request_id = %s`
     (T-09-07 COUNT(*)-only contract preserved verbatim with its comment)
   - `except Exception` path uses `f"audit-DB query failed: {str(e)[:300]}"`
     (T-09-09 DSN-never-in-error-strings preserved verbatim with its comment)
   - Final `result["ok"]` derivation using `AUDIT_UPSTREAM_BLOCKED`.

3. **`query_audit` body replaced** with a polling wrapper. New signature is
   source-compatible with the existing call site at line 850 (was line 798
   pre-edit) — defaults from the new constants:

   ```python
   def query_audit(
       pg_dsn: str,
       request_id: str,
       deadline_s: float = AUDIT_POLL_DEADLINE_S,
       interval_s: float = AUDIT_POLL_INTERVAL_S,
   ) -> dict[str, Any]:
   ```

   Behavior:
   - `if not request_id:` short-circuits BEFORE the poll loop (a missing
     correlation id does not waste 5s of retries).
   - `end = time.monotonic() + deadline_s`; loop calls `_query_audit_once`:
     - row found → return immediately
     - `"error" in result` → return immediately (genuine connection failures
       are NOT retried — matches SEED-006's recommended fix)
     - `time.monotonic() >= end` → return last result (gate fails honestly)
     - otherwise `time.sleep(interval_s)` and loop again
   - Uses `time.monotonic()` (not `time.time()`) so wall-clock jumps cannot
     break the deadline. The `time` module was already imported — no new
     imports added.

## Guardrails honored

- Single-file blast.
- No `asyncio` in `query_audit` (sync `time.monotonic()` + `time.sleep`).
- No DSN in any error string (`str(e)[:300]` cap preserved).
- `COUNT(*)`-only query on `audit_log_content` preserved verbatim (T-09-07).
- `SCHEMA_VERSION = "1.0.0"` unchanged; result dict shape unchanged.
- Call site still reads `query_audit(cfg.pg_dsn, request_id)` — new params have
  defaults sourced from the module constants.
- No edits to `gateway/`, no edits to other smoke scripts, no new files.

## Verification

- **Static gate 1 (import-sanity):** module parses cleanly under
  `importlib.util.spec_from_file_location` + `sys.modules['s'] = m` register.
  The `sys.modules` register is needed because the module uses
  `@dataclasses.dataclass` at line 197 (stdlib quirk; predates this refactor).
  Output: `OK`.
- **Static gate 2 (inspect.signature):** `query_audit` signature is
  `(pg_dsn, request_id, deadline_s=5.0, interval_s=0.25)`; constants exposed;
  `_query_audit_once` callable. Output: `SIGNATURE OK`.
- **Call-site:** `grep -n 'query_audit(cfg.pg_dsn, request_id)'` returns
  line 850 (drifted from 798 by +52 lines of new wrapper + helper + docstring),
  call expression byte-identical.
- **Git hygiene:** single commit, `+74 / -22`, fast-forward push, no `--force`.

## Behavioral acceptance (operator-run, pending)

Per SEED-006 acceptance: rerun `smoke-sensitive-failover.py` against the prod
gateway state from the post-23bbe01 run (2026-05-28 14:02:50 UTC, which
produced `audit_log_row_found=false`) and observe all 4 gates GREEN within
~1–2 seconds of the poll loop starting.

Recipe (operator runs from `ops-claude` — has Tailscale subnet route to
10.10.10.0/24, can reach prod DSN):

```bash
SMOKE_GATEWAY_URL=https://ai-gateway.converse-ai.app \
SMOKE_API_KEY=ifix_sk_hu6h3ggws6sfqwqgsfhbyonkq72jzbnj \
AI_GATEWAY_PG_DSN='postgres://doadmin:<...>@db-grupoifix-...:25060/bd_ai_gateway_prod?sslmode=require' \
GATEWAYCTL_SSH_HOST=n8n-ia-vm \
SMOKE_INDUCE_FAILURE_VIA=gatewayctl \
SMOKE_GATEWAYCTL=/gatewayctl \
python3 scripts/integration-smoke/smoke-sensitive-failover.py
```

Expect `gates: {fail_closed: true, never_external: true, audit_decision: true,
streaming_fail_fast: true, all_passed: true}`. Pre-condition note: SEED-005 also
needs to be deployed before the smoke can pass on a force-open-driven path
(without SEED-005, `ensure_tier0_open` still fails because
`/v1/health/upstreams` returns half-open). Both seeds are on
`gsd/phase-06.9-close` waiting on develop merge.

## Files

- `scripts/integration-smoke/smoke-sensitive-failover.py` (+74 / −22)

## Follow-up

None. SEED-006 fully closed by this change. The writer flush-interval question
(whether to lower `flushInterval=1s` for snappier audit observability under
low-volume traffic) stays deferred — no new seed needed, the smoke-side fix
removes the only production-observable symptom.

- Operator: merge `gsd/phase-06.9-close` → develop (covers both SEED-005 and
  SEED-006 + their docs), deploy, run sanity recipe above.
- On green: mark both `.planning/seeds/SEED-005-...md` and
  `.planning/seeds/SEED-006-...md` as `status: shipped`.
