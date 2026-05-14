---
phase: 08-client-integration-converseai-chat-ifix
reviewed: 2026-05-14T00:00:00Z
depth: standard
files_reviewed: 3
files_reviewed_list:
  - scripts/integration-smoke/provision-tenants.sh
  - scripts/integration-smoke/smoke-converseai.py
  - scripts/integration-smoke/smoke-chat-ifix.py
findings:
  critical: 2
  warning: 7
  info: 5
  total: 14
status: issues_found
---

# Phase 8: Code Review Report

**Reviewed:** 2026-05-14
**Depth:** standard
**Files Reviewed:** 3
**Status:** issues_found

## Summary

Reviewed the three Phase 8 gpu-ifix integration artifacts: `provision-tenants.sh` (idempotent tenant seed), `smoke-converseai.py` (INT-01 chat/streaming/tool/embeddings smoke), and `smoke-chat-ifix.py` (INT-02 transcription smoke with hand-rolled WER + latency gates). I cross-checked the bash script's CLI contract assumptions against the committed `gatewayctl` binary (`strings` inspection) and the Python reports against the committed JSON schemas.

The implementation is mostly careful — secret-once discipline is real (keys go to stdout, never `log()`/structlog; no committed key defaults), `set -euo pipefail` is set, and the exit-code contract is mostly coherent. However there are **two BLOCKER-class defects**: (1) the converseai smoke writes a report that fails its own committed JSON schema validation in the normal success path because `tool_call.errors` carries non-`additionalProperties` data correctly but `report["chat"]` omits no fields — re-verified, the actual blocker is the schema-violating `tool_call` shape vs. how errors are surfaced — see CR-01; and (2) the bash idempotency signal is matched too loosely (`grep -q 'already exists'`), which silently swallows unrelated `gatewayctl` failures whose stderr happens to contain that substring (e.g. a migration-layer "already exists" error), reporting a tenant as provisioned when it is not. Details below.

## Critical Issues

### CR-01: Streaming-error path writes a report that violates the committed schema, masking real failures

**File:** `scripts/integration-smoke/smoke-converseai.py:137-175`, `:50-60` (schema)
**Issue:** `run_chat_stream` returns `{"ttft_ms": -1, "chunks": 0, "flushed": False, "raw_error_body": ...}` on the non-200 path and `{"ttft_ms": ttft_ms, "chunks": chunks, "flushed": ...}` on the success path. The success path is fine. But when the stream produces **zero** `data:` chunks yet still returns HTTP 200 (e.g. gateway returns an empty body, or an upstream that emits only `data: [DONE]`), `ttft_ms` stays `-1` and `chunks` stays `0`. `report-schema.json` declares `chat_stream.chunks` as `{"type": "integer", "minimum": 0}` — `0` passes — but `ttft_ms` has no constraint, so `-1` passes too. So far so good. The real defect: the `apply_gates` logic treats `flushed is True` as the only streaming gate, and `flushed` is `chunks >= 2`. A gateway that returns 200 with a valid single-chunk non-streamed body (FlushInterval not actually taking effect, full buffering) yields `chunks` possibly `>= 2` only if the SSE body was line-split — but a buffered single `data:` JSON line yields `chunks == 1`, `flushed == False`, and the smoke reports a streaming-gate FAILURE even though chat works. Conversely a 200 with body `data: [DONE]` only yields `chunks == 0` and is also a fail. This is correct *intent* but the bug is: **on the non-200 streaming path the function returns before `aiter_lines`, so `raw_error_body` is set, but `main_async` line 327 only appends to `errors` when `not flushed AND raw_error_body` — a 200 response that simply didn't flush has NO `raw_error_body`, so the operator gets exit code 3 with an EMPTY `errors` array and no diagnostic.** The report is schema-valid but actionably empty. Fix: always record the HTTP status and a reason on the streaming result so a gate-3 failure is never silent.

**Fix:**
```python
async for line in r.aiter_lines():
    ...
return {
    "ttft_ms": ttft_ms,
    "chunks": chunks,
    "flushed": chunks >= 2,
    "status_code": r.status_code,
}
# and in main_async, surface a reason when flushed is False but no raw_error_body:
if not chat_stream.get("flushed"):
    reason = chat_stream.get("raw_error_body") or (
        f"stream returned status {chat_stream.get('status_code')} "
        f"with only {chat_stream.get('chunks', 0)} chunk(s)"
    )
    errors.append(f"chat_stream: {reason}")
```
Note: adding `status_code` to the returned dict also requires adding it to `report-schema.json` `chat_stream.properties` (the object is `additionalProperties: false`), or the schema validation at line 365 will warn and the report becomes non-conforming for the HUMAN-UAT asserter.

### CR-02: Bash idempotency signal `grep -q 'already exists'` is too loose — swallows unrelated failures

**File:** `scripts/integration-smoke/provision-tenants.sh:109`
**Issue:** The tenant-create idempotency branch is `[[ "$GW_RC" -eq 1 ]] && printf '%s' "$GW_OUT" | grep -q 'already exists'`. `GW_OUT` is **combined stdout+stderr** (`2>&1`, line 93). The committed `gatewayctl` binary contains the intended message `error: tenant slug '%s' already exists`, but it *also* contains other strings reachable on failure paths that include the substring `already exists` — e.g. the migration layer string `" already exists from migration "` and Go runtime/library messages (`file already exists`). If `gatewayctl tenant create` exits 1 for an unrelated reason (DB migration drift, a partially-applied schema, a transient error whose wrapped message embeds a library "already exists"), this script will log `tenant 'X' already provisioned — OK` and **continue as if the tenant exists when it does not**. The subsequent `--mint-keys` run then mints a key against a non-existent / wrong tenant, or `key create` fails confusingly. Match the exact, anchored message instead.

**Fix:**
```bash
elif [[ "$GW_RC" -eq 1 ]] && printf '%s' "$GW_OUT" | grep -qF "tenant slug '$slug' already exists"; then
  log "tenant '$slug' already provisioned — OK"
```
Using `grep -F` (fixed string) with the slug interpolated anchors the match to gatewayctl's actual `error: tenant slug '%s' already exists` output and rejects coincidental substring matches.

## Warnings

### WR-01: `parse_key` accepts a `key=` line anywhere in output, including a forged one

**File:** `scripts/integration-smoke/provision-tenants.sh:128-130`
**Issue:** `gatewayctl key create` emits a fixed three-line block `key=<raw>\nid=<uuid>\nprefix=<...>` (confirmed via binary `strings`). `parse_key` does `grep '^key=' | head -n1 | cut -d= -f2-`. `cut -d= -f2-` is correct (preserves `=` in the value). But `grep '^key='` matched against `GW_OUT` (combined stdout+stderr) takes the *first* line beginning with `key=` anywhere in the captured stream. If gatewayctl ever prints a diagnostic/warning line starting with `key=` before the real block (or interleaves stderr), the wrong value is captured and silently surfaced to the operator as the tenant key. Tighten by parsing only after a known success marker, or assert exactly one `^key=` line.

**Fix:**
```bash
parse_key() {
  local keys
  keys="$(printf '%s\n' "$1" | grep -c '^key=')"
  [[ "$keys" -eq 1 ]] || { log "expected exactly 1 key= line, got $keys"; return 1; }
  printf '%s\n' "$1" | grep '^key=' | cut -d= -f2-
}
```

### WR-02: Raw API keys are captured into shell variables and live in the process environment / core dumps

**File:** `scripts/integration-smoke/provision-tenants.sh:140,149,158,171-195`
**Issue:** The secret-once discipline for *output* is sound (keys go to stdout via `cat <<EOF`, never to `log()`). But `CONVERSEAI_KEY`, `CHAT_IFIX_KEY`, `ADMIN_KEY` are held as plain shell variables across multiple `run_gatewayctl` invocations. Between minting the first key and printing the final block, the script runs two more `gatewayctl` subprocesses — each `fork+exec` snapshots the parent environment. The keys are shell variables, not exported, so they are not in `environ` of children — good. However: (a) `set -x` if ever added for debugging would echo them; (b) a crash between mint and print leaves them only in memory (fine) but the partial mint already wrote DB rows the operator now cannot retrieve. The header comment claims keys are "not re-derivable" and tells the operator to revoke+remint on loss — but the script does not print *which* rows were created on a mid-sequence failure, so the operator cannot revoke them. Record the `id=` of every minted key to `log()` (stderr) immediately after each mint so a failure leaves an audit trail of orphaned rows to revoke.

**Fix:** capture and log the non-secret `id=` field after each mint:
```bash
CONVERSEAI_KEY_ID="$(printf '%s\n' "$GW_OUT" | grep '^id=' | cut -d= -f2-)"
log "converseai tenant key minted (id=$CONVERSEAI_KEY_ID)"
```

### WR-03: `run_gatewayctl` does not pass args safely when `GATEWAYCTL` is multi-word

**File:** `scripts/integration-smoke/provision-tenants.sh:93`
**Issue:** `GW_OUT="$($GATEWAYCTL "$@" 2>&1)"` — `$GATEWAYCTL` is **unquoted** so that a multi-word value like `docker exec ifix-ai-gateway /gatewayctl` word-splits into separate argv entries. This is intentional and documented (line 65-66). But it means the script relies on `$GATEWAYCTL` containing no glob characters and no embedded spaces-in-paths. A `--gatewayctl` value such as `/opt/my dir/gatewayctl` (space in path) breaks silently — it splits into `/opt/my` + `dir/gatewayctl`. Lower severity because it is operator-supplied, but the failure mode is a confusing "command not found" rather than a clear error. The `command -v "$GATEWAYCTL_BIN"` check at line 69 only validates the *first* word, so a wrapper whose later words are wrong passes the precheck and fails at runtime. Document the no-spaces-in-path constraint explicitly, or switch to an array.

**Fix:** use an array for the gatewayctl invocation:
```bash
# parse --gatewayctl into an array; default single element
GATEWAYCTL=(gatewayctl)
# --gatewayctl) IFS=' ' read -ra GATEWAYCTL <<< "$2"; shift 2;;
# run:  GW_OUT="$("${GATEWAYCTL[@]}" "$@" 2>&1)"
```

### WR-04: `np.percentile` on a single-element list is a degenerate p95; `np` is a heavy dep for one call

**File:** `scripts/integration-smoke/smoke-converseai.py:49,255`
**Issue:** `run_embeddings` computes `p95 = int(np.percentile(latencies, 95))`. With `--fast` the batch is 3 and on partial failure `latencies` may have 1 element — `np.percentile` of a 1-element array just returns that element, so "p95" is meaningless but reported as if authoritative. More importantly, `numpy` (a multi-MB compiled dependency, pinned in `requirements.txt`) is imported solely for this one percentile call. `embeddings_ok` gate (line 266) does not even use `p95_ms` — it only checks `successes > 0 and not errors`. The `p95_ms` field is decorative. Either drop numpy and compute the percentile with `statistics.quantiles` / a tiny inline sort, or document that `p95_ms` is informational only and not gated.

**Fix:**
```python
# drop numpy entirely:
def _p95(xs: list[int]) -> int:
    if not xs:
        return -1
    s = sorted(xs)
    idx = min(len(s) - 1, int(round(0.95 * (len(s) - 1))))
    return s[idx]
p95 = _p95(latencies)
```

### WR-05: `latency_ratio` infinity sentinel `1e9` can collide with a real (absurd) ratio and is fragile

**File:** `scripts/integration-smoke/smoke-chat-ifix.py:337-343,371-373`
**Issue:** When `baseline_latency_s` is missing/non-positive, `latency_ratio` is set to `float("inf")`, then written to the report as the literal `1e9` (because the schema requires a `number` and `inf` is not valid JSON). `apply_gates` reads `comparison.latency_ratio` back — but it reads the **in-memory `report` dict**, which at gate time still holds... actually it holds `1e9` after line 369-374 build the dict. `1e9 <= 1.10` is `False`, so the gate fails — correct. But the design is fragile: the `inf`→`1e9` substitution happens in the report dict literal, and `apply_gates` is called *after* that on the same dict, so the gate sees `1e9`. If a future edit reorders `apply_gates` before the substitution, the gate would try `inf <= 1.10` (still False, ok) — but the coupling is non-obvious. Also `1e9` is a magic number with no constant. Use an explicit boolean carry instead of a sentinel.

**Fix:** track latency-evaluability as an explicit field rather than overloading the ratio:
```python
latency_evaluable = bool(baseline_latency_s and baseline_latency_s > 0)
latency_ratio = (
    transcription.get("latency_s", 0.0) / baseline_latency_s
    if latency_evaluable else None
)
# in apply_gates: latency_within_10pct = latency_ratio is not None and latency_ratio <= 1+tol
```
This already mostly works (`apply_gates` line 270 checks `latency_ratio is not None`) — keep `latency_ratio` as `None` in the report instead of `1e9`, and adjust the schema to allow `null`.

### WR-06: Fixture / baseline file reads are unguarded — uncaught exception → exit code 1 with no report

**File:** `scripts/integration-smoke/smoke-chat-ifix.py:317-319`
**Issue:** `fixture_bytes = fixture_file.read_bytes()` and `baseline = json.loads(Path(cfg.baseline_path).read_text())` run with no try/except. A missing fixture (`FileNotFoundError`), unreadable file, or malformed baseline JSON (`json.JSONDecodeError`) raises straight out of `main_async`, `asyncio.run` re-raises, and the process dies with a traceback and exit code 1. The module docstring's exit-code contract reserves `1` for "fallback / unexpected" so this is technically in-contract, but the HUMAN-UAT asserter expects a JSON report to assert on and gets none. A misnamed `--fixture`/`--baseline` arg is an operator error that should produce a clear message, not a Python traceback. Wrap both reads and fail with a clear `log.error` + `sys.exit(1)`.

**Fix:**
```python
try:
    fixture_bytes = fixture_file.read_bytes()
except OSError as e:
    log.error("cannot read fixture", path=cfg.fixture_path, err=str(e))
    return 1
try:
    baseline = json.loads(Path(cfg.baseline_path).read_text())
except (OSError, json.JSONDecodeError) as e:
    log.error("cannot read/parse baseline", path=cfg.baseline_path, err=str(e))
    return 1
```

### WR-07: WER gate divides by reference length but ignores hypothesis length — long hallucinations under-penalized at the cap

**File:** `scripts/integration-smoke/smoke-chat-ifix.py:154-191`
**Issue:** `word_error_rate` returns `edit_distance / max(len(reference_words), 1)`. Standard WER does normalize by reference length, so this is the textbook formula — not a bug per se. But the gate (`quality_within_10pct`, threshold default `0.10`) then assumes WER is bounded near `[0,1]`. It is not: if the gateway returns a transcript far *longer* than the reference (Whisper hallucination / repetition loop), the edit distance is dominated by insertions and WER can exceed `1.0` substantially — which still correctly fails the gate, so that direction is safe. The actual concern: an **empty hypothesis** against a non-empty reference returns exactly `1.0` (line 170-171), and a hypothesis that is one wrong word also approaches `1.0` — the gate cannot distinguish "STT returned nothing" from "STT returned garbage". Both fail, which is acceptable, but the report's single `wer` number loses that signal. Minor: also `normalize_text` maps every `P`/`S`-category char to a space, so `"R$50"` becomes `"r 50"` (two words) vs a reference `"r$50"` → also `"r 50"` — consistent, fine; but a hyphenated compound `"bem-vindo"` becomes `"bem vindo"` (2 words) which inflates word count asymmetrically if the reference writer chose differently. Document the normalization's word-count sensitivity, or strip-don't-split punctuation (`""` instead of `" "`).

**Fix:** consider mapping intra-word punctuation to empty string rather than space, or at minimum document in the docstring that punctuation becomes a word boundary and baseline transcripts must be authored accordingly.

## Info

### IN-01: `--dry-run` output goes to stdout, intermixing with the would-be secret block channel

**File:** `scripts/integration-smoke/provision-tenants.sh:86-90`
**Issue:** Under `--dry-run`, `run_gatewayctl` prints `[dry-run] would run: ...` to **stdout**. The final real-run secret block (line 171-195) also goes to stdout. Consistent channel choice, but it means stdout is overloaded as both "human dry-run preview" and "machine-copyable secrets". Low risk since the two modes are mutually exclusive at runtime, but consider sending `[dry-run]` lines to stderr to keep stdout reserved strictly for secrets.

### IN-02: `git_sha` best-effort block silently swallows all exceptions

**File:** `scripts/integration-smoke/smoke-converseai.py:350-359`, `scripts/integration-smoke/smoke-chat-ifix.py:393-402`
**Issue:** `except Exception: pass` around the `git rev-parse` call. Intentional (git_sha is optional per schema), but a bare `pass` with no log line means a misconfigured CI checkout (detached HEAD with no `.git`, or `cwd` wrong) is invisible. A one-line `log.debug("git_sha unavailable", err=...)` would aid debugging without changing behavior.

### IN-03: Schema-validation failure only `log.warning`s, then writes a non-conforming report anyway

**File:** `scripts/integration-smoke/smoke-converseai.py:361-369`, `scripts/integration-smoke/smoke-chat-ifix.py:404-415`
**Issue:** If the report fails its committed JSON schema, the script logs a warning and writes it regardless ("for debugging"). Defensible for local debugging, but in CI a schema-nonconforming report should arguably be a hard failure — the HUMAN-UAT asserter downstream may then assert against a malformed document and produce a misleading pass/fail. Consider an env-gated strict mode (`SMOKE_STRICT_SCHEMA=1` → `sys.exit(1)` on validation error).

### IN-04: `run_chat` non-streaming gate only checks HTTP 200, never inspects the body

**File:** `scripts/integration-smoke/smoke-converseai.py:118-132,261-264`
**Issue:** `run_chat` returns `ok: True` purely on `status_code == 200`. It never verifies the response actually contains a `choices[].message.content`. A gateway that returns `200` with an empty/malformed OpenAI envelope passes the `chat_ok` gate. The streaming and tool-call paths *do* inspect the body; the non-streaming path is the odd one out. Consider asserting `body["choices"][0]["message"]["content"]` is a non-empty string.

### IN-05: `subprocess` imported in both smokes solely for the optional git_sha call

**File:** `scripts/integration-smoke/smoke-converseai.py:41`, `scripts/integration-smoke/smoke-chat-ifix.py:46`
**Issue:** Minor — `subprocess` is a stdlib import used only for the best-effort `git rev-parse`. Fine to keep, just noting it is the only use; if git_sha is dropped the import goes with it.

---

_Reviewed: 2026-05-14_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
