---
task_id: 260528-h8z
slug: smoke-audit-query-retry-loop
mode: quick
type: execute
wave: 1
depends_on: []
files_modified:
  - scripts/integration-smoke/smoke-sensitive-failover.py
autonomous: true
seed: .planning/seeds/SEED-006-smoke-audit-query-flush-race.md
branch: gsd/phase-06.9-close

must_haves:
  truths:
    - "`smoke-sensitive-failover.py` imports cleanly after the refactor (no syntax errors, no broken signatures)."
    - "`query_audit(pg_dsn, request_id)` keeps the same return shape and is still callable with the existing positional args at line 798."
    - "`query_audit` accepts new optional kwargs `deadline_s` (default 5.0) and `interval_s` (default 0.25) sourced from module-level constants."
    - "Polling loop is sync (uses `time.monotonic()` + `time.sleep`); `asyncio` is NOT introduced into `query_audit`."
    - "Genuine connection errors (psycopg `except Exception`) short-circuit the poll — they are NOT retried."
    - "Missing `request_id` short-circuit still fires BEFORE the poll loop (no wasted retries on a missing correlation id)."
    - "T-09-07 (SELECT COUNT(*) only on `audit_log_content`) and T-09-09 (DSN never in error strings or logs) contracts are preserved."
    - "No production code (gateway/) is touched — this is smoke-script-only."
  artifacts:
    - path: "scripts/integration-smoke/smoke-sensitive-failover.py"
      provides: "Refactored `query_audit` polling wrapper + `_query_audit_once` helper + `AUDIT_POLL_DEADLINE_S` / `AUDIT_POLL_INTERVAL_S` constants."
      contains: "def _query_audit_once"
    - path: "scripts/integration-smoke/smoke-sensitive-failover.py"
      provides: "Module-level audit poll constants near the existing INDUCE_POLL_* block."
      contains: "AUDIT_POLL_DEADLINE_S"
  key_links:
    - from: "scripts/integration-smoke/smoke-sensitive-failover.py:~798 (call site)"
      to: "query_audit(pg_dsn, request_id)"
      via: "positional args, defaults supplied by new constants"
      pattern: "query_audit\\(cfg\\.pg_dsn, request_id\\)"
    - from: "query_audit polling loop"
      to: "_query_audit_once"
      via: "function call inside `while time.monotonic() < end` loop"
      pattern: "_query_audit_once\\(pg_dsn, request_id\\)"
---

<objective>
Promote SEED-006 to executable code: refactor `query_audit` in `smoke-sensitive-failover.py` into a bounded-deadline polling wrapper around a new `_query_audit_once` helper, so the smoke stops racing the audit-writer's 1s flush timer and reports honest gates against a correct gateway.

Purpose: A correctly-emitted `audit_log` row was being missed because the smoke queried ~280 ms after the request, but the audit writer flushes on a 1s timer (or 500-row batch, which never trips on a 2-request smoke). The race produced false RED gates on a passing RES-08 implementation.

Output: One modified file (`scripts/integration-smoke/smoke-sensitive-failover.py`) with the same public signature for `query_audit` plus two new optional kwargs sourced from constants. No production-code change.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/seeds/SEED-006-smoke-audit-query-flush-race.md
@scripts/integration-smoke/smoke-sensitive-failover.py

<interfaces>
<!-- Existing surface that MUST be preserved. Executor does not need to read more of the file. -->

`query_audit` current public signature (line 568):
```python
def query_audit(pg_dsn: str, request_id: str) -> dict[str, Any]:
```

Current call site (line 798, unchanged after refactor):
```python
audit = query_audit(cfg.pg_dsn, request_id)
```

Return shape (preserved):
```python
{
  "ok": bool,
  "audit_log_row_found": bool,
  "audit_upstream": str,             # "" when row absent or upstream IS NULL
  "audit_log_content_rows": int,     # -1 when never queried
  "error": str,                      # OPTIONAL — present only on failure paths
}
```

Existing imports already in scope (lines 101-127): `time`, `psycopg`, `from typing import Any`. Do NOT add new imports.

Existing module-level constants near line 175:
```python
INDUCE_POLL_TIMEOUT_S = 30.0
INDUCE_POLL_INTERVAL_S = 2.0
```
Place the new audit constants next to these.

Existing constants referenced by `query_audit`'s `ok` computation:
- `AUDIT_UPSTREAM_BLOCKED` (used on current line 628; do not redefine).
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Refactor query_audit into _query_audit_once + poll wrapper, add module constants</name>
  <files>scripts/integration-smoke/smoke-sensitive-failover.py</files>
  <action>
Make three minimal, additive edits to `scripts/integration-smoke/smoke-sensitive-failover.py`. Do NOT touch any other file.

1) Near the existing `INDUCE_POLL_TIMEOUT_S = 30.0` / `INDUCE_POLL_INTERVAL_S = 2.0` block (~line 175), add two new module-level constants directly below them:

   - `AUDIT_POLL_DEADLINE_S = 5.0` — total time we are willing to poll for the audit row before declaring it missing. Sized to cover one full audit-writer flush cycle (`flushInterval = 1 * time.Second` in `gateway/internal/audit/writer.go`) plus DB write latency, with margin.
   - `AUDIT_POLL_INTERVAL_S = 0.25` — sleep between polls. Yields up to 20 attempts within the deadline, cheap on a 2-statement query.

   Include a one-line comment above the constants citing SEED-006 and the writer flush rule so a future reader understands the magic numbers.

2) Extract the existing single-shot query body (currently lines ~598-624, the `try: with psycopg.connect ... except ... return result` block plus the `result["ok"] = (...)` derivation that immediately follows) into a new module-level helper:

   ```python
   def _query_audit_once(pg_dsn: str, request_id: str) -> dict[str, Any]:
   ```

   Move into this helper:
   - The `result: dict[str, Any] = {...}` initializer block (current lines 586-591).
   - The full `try / except Exception` psycopg block including the `SELECT upstream FROM ai_gateway.audit_log ...` query, the `SELECT COUNT(*) FROM ai_gateway.audit_log_content ...` query, and the existing T-09-09-compliant error string (`f"audit-DB query failed: {str(e)[:300]}"` — do not include the DSN).
   - The final `result["ok"] = (...)` derivation using `AUDIT_UPSTREAM_BLOCKED`.

   Do NOT move the `if not request_id:` short-circuit into the helper — that belongs in the wrapper (the wrapper must short-circuit BEFORE polling so a missing correlation id does not waste 5 seconds of retries).

   Preserve every contract comment from the original `query_audit` docstring/body verbatim:
   - The `T-09-07` `SELECT COUNT(*)` contract comment above the second cursor.execute.
   - The `T-09-09` "do NOT include the DSN" comment above the `except` block's error assignment.
   - The "upstream is a nullable column; render NULL as ''" comment.

3) Replace the body of `query_audit` with the bounded polling wrapper. New signature:

   ```python
   def query_audit(
       pg_dsn: str,
       request_id: str,
       deadline_s: float = AUDIT_POLL_DEADLINE_S,
       interval_s: float = AUDIT_POLL_INTERVAL_S,
   ) -> dict[str, Any]:
   ```

   Behavior:
   - Keep the original docstring and add a short paragraph explaining the polling rationale (race with audit-writer's 1s flush timer per SEED-006).
   - First: the `if not request_id:` short-circuit returning the same `{"ok": False, "audit_log_row_found": False, "audit_upstream": "", "audit_log_content_rows": -1, "error": "no X-Request-ID captured ..."}` shape as today (copy the exact error string).
   - Then: compute `end = time.monotonic() + deadline_s`. Loop:
     - `result = _query_audit_once(pg_dsn, request_id)`
     - If `result["audit_log_row_found"]` is truthy → return `result` (success short-circuit).
     - If `"error" in result` → return `result` (genuine connection failures are NOT retried — fail fast; this matches SEED-006's recommended fix exactly).
     - If `time.monotonic() >= end` → return `result` (final attempt, deadline elapsed; `result["ok"]` will be False, gate fails honestly).
     - Otherwise `time.sleep(interval_s)` and loop again.

   Use `time.monotonic()` (NOT `time.time()`) so wall-clock jumps do not affect the deadline. The `time` module is already imported at line 110 — do NOT add a redundant import.

Implementation guard-rails (per the user request's scope fences):
- Do NOT modify the call site at line ~798; the new defaults make it source-compatible.
- Do NOT introduce `asyncio` — the design comment at line 797 ("sync psycopg is fine here") is intentional and stays. The wrapper remains sync.
- Do NOT touch `ensure_tier0_open` (SEED-005 territory, already shipped in 7d0b345).
- Do NOT touch `gateway/internal/audit/writer.go` flush constants (separate throughput concern).
- Do NOT change the JSON report schema_version (stays `1.0.0`) or any field name in the result dict.
- Do NOT add a new pytest harness — `scripts/integration-smoke/` has no adjacent `tests/` directory (verified: only `__pycache__/` is adjacent). Validation path is the operator-run integration smoke against the live gateway.
- Do NOT log the DSN or include it in any error string. Reuse the existing `str(e)[:300]` truncation.

After the edits, run a syntax+import sanity check from the repo root:

```bash
python3 -c "import importlib.util, sys; spec = importlib.util.spec_from_file_location('s', 'scripts/integration-smoke/smoke-sensitive-failover.py'); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m); print('OK')"
```

Then verify the public signature is intact AND the new kwargs are wired:

```bash
python3 -c "
import importlib.util
spec = importlib.util.spec_from_file_location('s', 'scripts/integration-smoke/smoke-sensitive-failover.py')
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
import inspect
sig = inspect.signature(m.query_audit)
params = list(sig.parameters.keys())
assert params == ['pg_dsn', 'request_id', 'deadline_s', 'interval_s'], params
assert sig.parameters['deadline_s'].default == m.AUDIT_POLL_DEADLINE_S
assert sig.parameters['interval_s'].default == m.AUDIT_POLL_INTERVAL_S
assert m.AUDIT_POLL_DEADLINE_S == 5.0
assert m.AUDIT_POLL_INTERVAL_S == 0.25
assert callable(m._query_audit_once)
print('SIGNATURE OK')
"
```

Both commands must print their success string. If either fails, fix the source — do not paper over with try/except.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix &amp;&amp; python3 -c "import importlib.util, sys; spec = importlib.util.spec_from_file_location('s', 'scripts/integration-smoke/smoke-sensitive-failover.py'); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m); import inspect; sig = inspect.signature(m.query_audit); params = list(sig.parameters.keys()); assert params == ['pg_dsn', 'request_id', 'deadline_s', 'interval_s'], params; assert sig.parameters['deadline_s'].default == m.AUDIT_POLL_DEADLINE_S; assert sig.parameters['interval_s'].default == m.AUDIT_POLL_INTERVAL_S; assert m.AUDIT_POLL_DEADLINE_S == 5.0; assert m.AUDIT_POLL_INTERVAL_S == 0.25; assert callable(m._query_audit_once); print('OK')"</automated>
    <human-check>Operator (not the executor) reruns `smoke-sensitive-failover.py` against the prod gateway state that produced `audit_log_row_found=false` in the post-23bbe01 run (14:02:50 UTC, 2026-05-28). Expected: all 4 gates GREEN within ~1-2 s of the poll loop starting. This is documented in the SUMMARY's verification section per the seed's "Acceptance criterion".</human-check>
  </verify>
  <done>
- `_query_audit_once(pg_dsn, request_id) -> dict[str, Any]` exists at module scope and contains the original single-shot query body + result-ok derivation, with the T-09-07 and T-09-09 contract comments preserved verbatim.
- `query_audit` signature is `(pg_dsn, request_id, deadline_s=AUDIT_POLL_DEADLINE_S, interval_s=AUDIT_POLL_INTERVAL_S)` and the body is the polling wrapper described above.
- `AUDIT_POLL_DEADLINE_S = 5.0` and `AUDIT_POLL_INTERVAL_S = 0.25` defined as module constants near the existing `INDUCE_POLL_*` constants with a comment citing SEED-006.
- The pre-flight `if not request_id:` short-circuit returns BEFORE the poll loop (verified by reading the new function body).
- Connection-error path (`"error" in result`) short-circuits the poll loop (no retry on genuine failures).
- Existing call site `query_audit(cfg.pg_dsn, request_id)` at line ~798 is unchanged.
- No edits outside `scripts/integration-smoke/smoke-sensitive-failover.py`.
- The verify automated command prints `OK`.
  </done>
</task>

<task type="auto">
  <name>Task 2: Commit and push to gsd/phase-06.9-close</name>
  <files>(git only — no source edits)</files>
  <action>
Stage only the modified file:

```bash
git add scripts/integration-smoke/smoke-sensitive-failover.py
```

Confirm `git status` shows EXACTLY one modified path (`scripts/integration-smoke/smoke-sensitive-failover.py`) and zero untracked Python files inside `scripts/integration-smoke/`. If anything else is staged or new, unstage / clean it before committing — the seed promotion is scoped to one file.

Create the commit on the current branch `gsd/phase-06.9-close` (do NOT create or switch branches) using the project's conventional-commit style observed in recent history (`fix(11-02): …`, `fix(11): …`). Subject pattern: `fix(smoke): poll audit-DB to absorb writer 1s flush race (SEED-006)`. Body should:
- One line: what changed (refactored `query_audit` into poll-with-deadline wrapper + `_query_audit_once` helper).
- One line: why (audit writer flushes on 1s timer / 500-row batch; 2-request smoke loses the race).
- One line: scope fence (no production-code change; gateway flush constants untouched).
- One line: `Refs: .planning/seeds/SEED-006-smoke-audit-query-flush-race.md`.
- Trailer: `Co-Authored-By: Claude Opus 4.7 &lt;noreply@anthropic.com&gt;`

Use a HEREDOC for the commit message (project convention). Do NOT use `--amend`. Do NOT use `--no-verify` (the user has not requested skipping hooks).

After the commit lands, push with explicit upstream (the branch is already tracking origin per the gitStatus header showing recent pushes):

```bash
git push origin gsd/phase-06.9-close
```

If the push is rejected for non-fast-forward, STOP and surface the conflict — do NOT force-push (user has not requested it; main-branch rules in CLAUDE.md forbid unsolicited force pushes).

Final sanity:
```bash
git log -1 --stat
git status
```
The log line must show exactly the smoke file modified. `git status` must be clean (no uncommitted residual).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix &amp;&amp; git log -1 --pretty=format:'%s' | grep -E '^fix\(smoke\):.*SEED-006' &amp;&amp; git log -1 --name-only --pretty=format:'' | grep -v '^$' | sort -u | diff - &lt;(echo 'scripts/integration-smoke/smoke-sensitive-failover.py') &amp;&amp; git status --porcelain | wc -l | grep -q '^0$' &amp;&amp; echo OK</automated>
  </verify>
  <done>
- Exactly one commit added to `gsd/phase-06.9-close` whose subject matches `fix(smoke): poll audit-DB ... SEED-006` and whose only modified path is `scripts/integration-smoke/smoke-sensitive-failover.py`.
- Working tree clean (`git status` empty).
- `git push origin gsd/phase-06.9-close` succeeded without `--force` or `--force-with-lease`.
- `git log -1` shows the new commit at `HEAD` with the Co-Authored-By trailer.
  </done>
</task>

</tasks>

<verification>
Overall quick-task verification:

1. **Static (executor-run)** — the inspect/signature check in Task 1's verify block prints `OK`. This proves: the module parses, both new constants exist with the documented defaults, `query_audit`'s public signature matches the new shape, and `_query_audit_once` is callable.

2. **Git hygiene (executor-run)** — Task 2's verify block confirms exactly one commit was created with the expected subject pattern and only the smoke file as a modified path.

3. **Behavioral (operator-run, post-merge, NOT executor)** — rerun `smoke-sensitive-failover.py` against the same prod gateway state from the post-23bbe01 run that produced `audit_log_row_found=false` at 2026-05-28 14:02:50 UTC (see SEED-006 "Empirical evidence" block). Expected outcome: all 4 gates GREEN within 5 s, with the new poll loop completing in ~1-2 s in practice (the writer's worst-case flush latency is one `flushInterval = 1s` tick + write commit). This is documented in the SUMMARY's verification section per the seed's "Acceptance criterion"; it is NOT a gate the Claude executor runs (the executor is offline from the prod gateway).
</verification>

<success_criteria>
- Single file modified: `scripts/integration-smoke/smoke-sensitive-failover.py`.
- Single commit on `gsd/phase-06.9-close` with subject `fix(smoke): poll audit-DB to absorb writer 1s flush race (SEED-006)`.
- `python3 -c "import importlib.util ... print('OK')"` import-sanity command passes.
- Inspect-signature check confirms `query_audit(pg_dsn, request_id, deadline_s=5.0, interval_s=0.25)` and presence of `_query_audit_once`, `AUDIT_POLL_DEADLINE_S`, `AUDIT_POLL_INTERVAL_S`.
- Push to `origin/gsd/phase-06.9-close` succeeds without force.
- Zero edits to `gateway/`, zero edits to other smoke scripts, no new files in `scripts/integration-smoke/` beyond the existing set.
- T-09-07 (COUNT(*)-only on `audit_log_content`) and T-09-09 (DSN never logged) contracts preserved verbatim in `_query_audit_once`.
</success_criteria>

<output>
Write `.planning/quick/260528-h8z-smoke-audit-query-retry-loop/SUMMARY.md` when done. The SUMMARY must include:

- The two commands actually run (the import-sanity one-liner and the inspect-signature one-liner) and their printed output.
- The commit SHA and `git log -1 --stat` output.
- A one-paragraph note that operator-run validation (rerun against the post-23bbe01 prod state) is the closing acceptance step per SEED-006, and that this is NOT something the executor can run from the planning host (the gateway is only addressable from inside the ifix-prod-01 NAT or via the `n8n-ia-vm` SSH alias).
- A `## Follow-ups` section listing zero items (the seed is fully closed; the writer flush-interval question SEED-006 explicitly defers stays deferred — no new seed needed).
</output>
