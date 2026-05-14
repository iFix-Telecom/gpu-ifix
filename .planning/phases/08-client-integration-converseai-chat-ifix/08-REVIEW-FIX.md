---
phase: 08-client-integration-converseai-chat-ifix
fixed_at: 2026-05-14T00:00:00Z
review_path: .planning/phases/08-client-integration-converseai-chat-ifix/08-REVIEW.md
iteration: 1
findings_in_scope: 9
fixed: 9
skipped: 0
status: all_fixed
---

# Phase 8: Code Review Fix Report

**Fixed at:** 2026-05-14
**Source review:** .planning/phases/08-client-integration-converseai-chat-ifix/08-REVIEW.md
**Iteration:** 1

**Summary:**
- Findings in scope: 9 (2 BLOCKER + 7 WARNING; 5 INFO findings out of scope)
- Fixed: 9
- Skipped: 0

## Fixed Issues

### CR-02: Bash idempotency signal `grep -q 'already exists'` is too loose

**Files modified:** `scripts/integration-smoke/provision-tenants.sh`
**Commit:** d276019
**Applied fix:** Replaced the unanchored `grep -q 'already exists'` with `grep -qF "tenant slug '$slug' already exists"`. Verified against the gatewayctl Go source (`gateway/cmd/gatewayctl/tenant.go:80`) — the exact message is `error: tenant slug '%s' already exists`. The interpolated slug + fixed-string match rejects coincidental substring matches (migration-layer / Go-stdlib "already exists" strings) that would otherwise let `--mint-keys` mint a key against a non-existent tenant.

### CR-01: Streaming-error path writes a report that masks real failures

**Files modified:** `scripts/integration-smoke/smoke-converseai.py`, `scripts/integration-smoke/report-schema.json`
**Commit:** 9071cff
**Applied fix:** `run_chat_stream` now always carries `status_code` on its result (HTTP status on success/non-200, `-1` on exception). `main_async` synthesises a diagnostic from `status_code` + chunk count when a stream is `not flushed` but has no `raw_error_body` — so an HTTP-200-but-unflushed stream no longer produces exit code 3 with an empty `errors` array. `report-schema.json` `chat_stream` gains `status_code` as a required property (the object is `additionalProperties: false`, so the field had to be declared). Verified with an inline throwaway harness: the unflushed-200 path produces a non-empty `errors` entry and both the unflushed-200 and non-200 report shapes validate against the updated schema.

### WR-01: `parse_key` accepts a `key=` line anywhere in output

**Files modified:** `scripts/integration-smoke/provision-tenants.sh`
**Commit:** 0ecea56
**Applied fix:** `parse_key` now asserts exactly one `^key=` line in the mint output before extracting (gatewayctl emits `key=` on exactly one line per mint block — confirmed in `key.go:87` and `admin_key.go:129`). On any other count it logs an error and returns 1. `cut -d= -f2-` is retained to preserve any `=` inside the raw key value.

### WR-02: Minted key `id=` values never logged — un-revocable orphans

**Files modified:** `scripts/integration-smoke/provision-tenants.sh`
**Commit:** 79cc1f0
**Applied fix:** Added a `parse_id` helper that extracts the non-secret `id=<uuid>` field from each mint block. Each successful mint now logs `... key minted (id=<uuid>)` to stderr, so a mid-sequence failure leaves an audit trail of which DB rows were created and must be revoked. The raw `key=` value is still never logged — secret-once discipline preserved.

### WR-03: `run_gatewayctl` does not pass args safely when `GATEWAYCTL` is multi-word

**Files modified:** `scripts/integration-smoke/provision-tenants.sh`
**Commit:** a4515bd
**Applied fix:** `GATEWAYCTL` is now a bash array (`GATEWAYCTL=(gatewayctl)`), `--gatewayctl` is parsed via `IFS=' ' read -r -a`, and invocations use `"${GATEWAYCTL[@]}"` / `"${GATEWAYCTL[*]}"` — removing reliance on unquoted word-splitting. `GATEWAYCTL_BIN` derived as `${GATEWAYCTL[0]}`. The header comment documents that components must not contain spaces (the split is on spaces). Verified the array split produces 4 distinct argv entries for `docker exec ifix-ai-gateway /gatewayctl`.

### WR-04: `numpy` is a heavy dep for a single percentile call

**Files modified:** `scripts/integration-smoke/smoke-converseai.py`, `scripts/integration-smoke/requirements.txt`
**Commit:** 71b2607
**Applied fix:** Removed `import numpy as np` and the `numpy>=1.26.0` line from `requirements.txt`. Added a `_p95` exact-rank helper (returns `-1` for an empty list) and use it in `run_embeddings`. Docstrings note `p95_ms` is informational only — the `embeddings_ok` gate checks `successes > 0 and not errors`, never `p95_ms`. Verified `_p95` against empty / single-element / 10-element / unsorted inputs.

### WR-05: `latency_ratio` infinity sentinel `1e9` is fragile

**Files modified:** `scripts/integration-smoke/smoke-chat-ifix.py`, `scripts/integration-smoke/chat-ifix-report-schema.json`
**Commit:** 4ec5e23
**Applied fix:** Replaced the `float("inf")` / `1e9` sentinel with an explicit `latency_evaluable` boolean carry. When the baseline latency is missing/non-positive, `latency_ratio` stays `None` and is written to the report as JSON `null`. `apply_gates` already fails the latency gate on `latency_ratio is None`. `chat-ifix-report-schema.json` `comparison.latency_ratio` now allows `["number", "null"]`. Verified with an inline throwaway harness: `apply_gates` fails `latency_within_10pct` on a `None` ratio, and both `null` and numeric report shapes validate against the updated schema.

### WR-06: Fixture / baseline file reads are unguarded

**Files modified:** `scripts/integration-smoke/smoke-chat-ifix.py`
**Commit:** 999c505
**Applied fix:** Wrapped `fixture_file.read_bytes()` in `try/except OSError` and `json.loads(...read_text())` in `try/except (OSError, json.JSONDecodeError)`. On failure each logs a clear `log.error` with the path + error and returns `1` (the "fallback / unexpected" exit code per the module contract) instead of letting a raw traceback escape with no JSON report.

### WR-07: WER normalization word-boundary sensitivity is undocumented

**Files modified:** `scripts/integration-smoke/smoke-chat-ifix.py`
**Commit:** ab7a2e1
**Applied fix:** Documentation-only (per the review's recommended fix). The `normalize_text` docstring now documents that each P/S-category char becomes a SPACE, so intra-word punctuation (`bem-vindo`, `R$50`) becomes a word boundary — baseline transcripts must be authored to match the STT's spacing/hyphenation conventions. The `word_error_rate` docstring documents that the single `wer` number cannot distinguish an empty hypothesis from a one-wrong-word hypothesis (both ~1.0, both correctly fail the gate). No behavior change.

---

_Fixed: 2026-05-14_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
