# SEED-006 — `smoke-sensitive-failover.py` audit-DB query races the audit writer flush

**Planted:** 2026-05-28
**Discovered during:** Phase 11 audit-pipeline RES-08 fix validation (`smoke-sensitive-failover.py` post-23bbe01 deploy)
**Status:** shipped 2026-05-28 — quick `260528-h8z` (commit `2d30a60`), validated against prod 2026-05-28T20:48Z (smoke `audit_decision.audit_log_row_found=True` within poll deadline; 4/4 GREEN)
**Related:** [[audit-blocked-sensitive-override-not-propagated]] debug session; [[SEED-005-health-endpoint-snapshot-ignores-force-override]]; quick `260528-h8z-smoke-audit-query-retry-loop`

## Problem

`scripts/integration-smoke/smoke-sensitive-failover.py:798` calls `query_audit(cfg.pg_dsn, request_id)` **once**, immediately after the streaming_fail_fast request returns (~280 ms after the fail_closed request). The audit writer (`gateway/internal/audit/writer.go:24-25`) flushes on a `flushBatchSize=500` / `flushInterval=1*time.Second` rule — whichever fires first. For a smoke that emits 2 requests total, the batch threshold never fires; the smoke is racing the 1-second timer.

When the race is lost, `query_audit` returns `audit_log_row_found=false` even though the gateway behavior is correct AND the row lands in PostgreSQL ~200–800 ms later. The smoke then reports `never_external=false` + `audit_decision=false` and exits 1, masking a passing RES-08 gate as a failure.

## Empirical evidence

```
$ # Smoke run 14:02:50–14:03:00 UTC against gateway rev 23bbe01:
$ cat /tmp/smoke-sensitive-failover-report.json | jq '{git_sha, audit_decision, never_external}'
{
  "git_sha": "23bbe01",
  "audit_decision": {"audit_log_row_found": false, "ok": false, ...},
  "never_external": {"audit_upstream": "", "ok": false, ...}
}

$ # Direct DB query against bd_ai_gateway_prod a few seconds later:
$ ts=2026-05-28 14:02:58.99 | request_id=019e6ee5-4230... | upstream=blocked_sensitive
$ ts=2026-05-28 14:02:54.80 | request_id=019e6ee5-31d0... | upstream=blocked_sensitive
```

Both rows land — smoke just queried too early.

## Why It Was Missed

- Integration tests (`gateway/internal/integration_test/sensitive_block_test.go`) wire the audit writer with a `t.Cleanup` `writer.Close()` that drains the queue synchronously, so tests never see the flush race.
- The smoke was authored against a stage where the dispatcher emitted the audit row synchronously (pre-Phase 2 writer refactor). The pipeline changed; the smoke's query timing was not revisited.
- Local manual runs against `/opt/ai-gateway-dev/` typically saw an existing row from a prior run match the request_id by coincidence (UUIDv7 prefix collisions over short windows are nonexistent, so this only worked when the writer happened to be on the edge of its tick).

## Scope of Fix

Modify `query_audit` to poll with a bounded retry, in line with the existing `ensure_tier0_open` pattern. ~10 LOC:

```python
def query_audit(pg_dsn: str, request_id: str,
                deadline_s: float = 5.0,
                interval_s: float = 0.25) -> dict[str, Any]:
    """Poll ai_gateway.audit_log up to deadline_s for the request_id.

    The audit writer flushes on a 1s/500-row rule; small smokes (2-3 reqs)
    only land via the 1s timer. We poll past one full flush cycle so a
    correctly-emitted row is observable before declaring it missing.
    """
    end = time.monotonic() + deadline_s
    while True:
        result = _query_audit_once(pg_dsn, request_id)
        if result["audit_log_row_found"] or "error" in result:
            return result
        if time.monotonic() >= end:
            return result
        time.sleep(interval_s)
```

Pull the existing single-shot query into `_query_audit_once`; everything else (gate derivation, threat-T-09-07 `COUNT(*)`-only contract) is unchanged.

### Alternative: trigger explicit flush

Add a `/admin/audit/flush` endpoint that drains the writer queue synchronously. Smoke calls it after the second request before querying. Heavier — adds a new admin surface only used by tests. **Not recommended.**

## Test Plan (when promoted)

- Unit (smoke): mock psycopg cursor that returns `None` on first 2 calls + the row on the 3rd; assert `query_audit` returns `audit_log_row_found=true` within `deadline_s`.
- Integration: run the smoke twice in succession against a force-open-driven dev gateway — current behavior: ~50% flake on the 2nd run. With the fix: 0 flake observed in 10 consecutive runs.
- Regression: streaming_fail_fast gate timing is independent of this change (it does not query DB).

## Acceptance criterion

`smoke-sensitive-failover.py` against a known-good gateway (the row IS in DB) returns all 4 gates GREEN 10/10 times in a tight loop without any operator intervention between runs.

## Files

- `scripts/integration-smoke/smoke-sensitive-failover.py:568-631` (`query_audit` body)
- `scripts/integration-smoke/smoke-sensitive-failover.py:796-800` (call site — no change needed once internals retry)
- `gateway/internal/audit/writer.go:24-25` (referenced flush constants — no change)
