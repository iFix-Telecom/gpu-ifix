---
phase: 11-prod-hardening
plan: 11-01
subsystem: testing
tags: [load-test, audit-log, replay, jsonschema, httpx, asyncio, psycopg, prd-01]

# Dependency graph
requires:
  - phase: 10-prod-deploy-ai-gateway
    provides: ai_gateway.audit_log + audit_log_content schemas live in DO prod; tenants table seeded
  - phase: 08-client-integration-converseai-chat-ifix
    provides: scripts/integration-smoke/ family (smoke-*.py async httpx + structlog + jsonschema pattern); fixtures/whatsapp-sample.ogg already tracked
provides:
  - scripts/integration-smoke/audit-log-export.py — PRD-01 fixture generator (audit_log + audit_log_content JOIN, sanitized JSONL output)
  - scripts/integration-smoke/load-replay.py — sustained load replay engine (async httpx, env-var tenant keys, D-04 SLO gates)
  - scripts/integration-smoke/load-replay-report-schema.json — Draft 2020-12 contract for the load test report
  - .planning/load-test-fixtures/ directory marker + gitignore patterns for sanitized JSONL + LGPD PDFs
affects: [11-06 load-test-execution, 11-07 chaos-primary-kill, 11-08 chaos-openrouter-down]

# Tech tracking
tech-stack:
  added: []  # Zero new runtime deps — Phase 11 reuses httpx + structlog + jsonschema + psycopg from requirements.txt
  patterns:
    - "audit_log → audit_log_content composite-PK JOIN as the canonical body source for replay"
    - "Tenant API keys resolved at REPLAY time from env IFIX_KEY_<TENANT_SLUG_UPPER> (zero secret-at-rest in fixtures)"
    - "STT replay attaches canonical stub OGG fixture (audio bytes never reconstructed from DB per D-B6)"
    - "External-observation gates (zero_5xx_panic) typed [boolean,null] with sibling gates_external_inputs audit-trail block"

key-files:
  created:
    - scripts/integration-smoke/audit-log-export.py
    - scripts/integration-smoke/load-replay.py
    - scripts/integration-smoke/load-replay-report-schema.json
    - .planning/load-test-fixtures/.gitignore
  modified:
    - .gitignore

key-decisions:
  - "Body source-of-truth = ai_gateway.audit_log_content.prompt (JSONB), accessed via LEFT JOIN on (request_id, ts) composite PK — verified against migrations 0003 + 0004 (reviews HIGH #1)."
  - "Tenant API keys NEVER stored in fixtures; resolve at runtime from env IFIX_KEY_<TENANT_SLUG_UPPER>; missing env var = hard exit 2 BEFORE any traffic flows (reviews MEDIUM #2)."
  - "STT replay uses canonical fixture scripts/integration-smoke/fixtures/whatsapp-sample.ogg (Ogg/Opus, 16 kHz mono) — audio bytes are never persisted per D-B6 invariant, so this is the only legitimate source (reviews MEDIUM #3)."
  - "zero_5xx_panic is an external observation populated by the operator post-run from a Sentry query + gateway-log grep; engine writes null; all_passed is null until every gate is non-null (reviews LOW #4)."
  - "Optional --speedup multiplier divides replay delays for densifying traffic when the audit_log window is too quiet to hit saturation (reviews LOW #5)."

patterns-established:
  - "Pattern A secret-once for psycopg DSN: --dsn or env DASHBOARD_DATABASE_URL/AI_GATEWAY_DB_URL, no committed default, never logged."
  - "Pattern B 8-section spine (Module docstring → Constants → Config+CLI → Helpers → Audit-DB → Gates+exit → Orchestration → main) — extends the smoke-*.py family convention."
  - "Pattern C schema-validate tail: load sibling schema with Draft202012Validator, write report ALWAYS (even on validation failure for forensics), exit 1 only on schema-invalid (WR-05)."

requirements-completed: [PRD-01]

# Metrics
duration: 25min
completed: 2026-05-27
---

# Phase 11 Plan 01: Load-Test Scaffolding Summary

**PRD-01 load-test scaffolding shipped: audit_log → JSONL exporter (audit_log_content JOIN), async httpx replay engine with env-var tenant keys + multipart STT replay + --speedup, and Draft 2020-12 report schema with external-observation gates — all four cross-AI review findings closed; zero live spend.**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-05-27T17:48:27Z (orchestrator launch)
- **Completed:** 2026-05-27T18:13:00Z
- **Tasks:** 3 / 3
- **Files created:** 4
- **Files modified:** 1

## Accomplishments

- `scripts/integration-smoke/audit-log-export.py` (472 lines, 17.5 KB) — composite-PK JOIN of `ai_gateway.audit_log` with `audit_log_content.prompt` retrieves the body source; sanitizer placeholders tool_calls arguments + drops audio/file URLs + emits STT stub marker; PRD-01 baseline excludes `data_class='sensitive'` rows at the SQL level.
- `scripts/integration-smoke/load-replay.py` (660 lines, 23 KB) — async httpx replay with `asyncio.Semaphore(max_concurrency)`, original-timing preservation via `asyncio.sleep(_replay_delay_s / args.speedup)`, multipart/form-data STT replay using `scripts/integration-smoke/fixtures/whatsapp-sample.ogg`, startup env-var resolution gate, and D-04 SLO gate computation with `zero_5xx_panic=None` (operator-flipped).
- `scripts/integration-smoke/load-replay-report-schema.json` (125 lines, 5.7 KB) — JSON Schema Draft 2020-12 with `additionalProperties:false` at every nested object, 6-gate `gates` block with verbatim D-04 names, sibling `gates_external_inputs` audit-trail block.
- `.planning/load-test-fixtures/.gitignore` (directory marker) + 2-block append to root `.gitignore` (load-test JSONL + LGPD PDF patterns) above the existing `.claude/` block.

## Task Commits

Each task committed atomically:

1. **Task 11-01-01: audit-log-export.py** — `f337e87` (feat)
2. **Task 11-01-02: load-replay.py + schema + fixture** — `d9ccd19` (feat)
3. **Task 11-01-03: gitignore fixtures + legal** — `a921643` (chore)

## Cross-AI Review Closure Tags

- **closes reviews HIGH #1** (`f337e87`): exporter SELECT JOINs `ai_gateway.audit_log_content.prompt AS body` on composite PK `(request_id, ts)` per migration 0004 (no FK; JOIN purely on PK match). Verified by grep `LEFT JOIN ai_gateway\.audit_log_content` ≥1.
- **closes reviews MEDIUM #2** (`f337e87` + `d9ccd19`): JSONL fixtures carry only `tenant_slug`; load-replay.py resolves `IFIX_KEY_<TENANT_SLUG_UPPER>` at startup; missing/empty env var → hard exit 2 BEFORE any HTTP traffic. Verified by `! grep -E '_replay_api_key|"api_key"\s*:|"Authorization"\s*:' audit-log-export.py` and `grep -E "IFIX_KEY_" load-replay.py`.
- **closes reviews MEDIUM #3** (`d9ccd19`): `/v1/audio/transcriptions` replays as multipart/form-data with the canonical fixture OGG; httpx `files=` computes the multipart boundary; JSON Content-Type does NOT apply to STT. Verified by `grep -E "files\s*="` ≥1 + `file fixtures/whatsapp-sample.ogg | grep -iE "ogg|opus"`.
- **closes reviews LOW #4** (`d9ccd19`): `zero_5xx_panic` typed `[boolean, null]`; engine writes None; `gates_external_inputs.sentry_query_url` + `log_grep_command` documented; `all_passed` is None until every gate is non-None.
- **closes reviews LOW #5** (`d9ccd19`): `--speedup` flag wired into delay computation; `--speedup <= 0` rejected with exit 2.

## Sample `--help` Output (load-replay.py first line)

```
usage: load-replay.py [-h] [--gateway-url GATEWAY_URL] [--fixture FIXTURE]
                      [--duration DURATION]
                      [--max-concurrency MAX_CONCURRENCY] [--out OUT]
                      [--speedup SPEEDUP] [--audio-stub AUDIO_STUB]
```

## Files Created/Modified

- `scripts/integration-smoke/audit-log-export.py` — PRD-01 fixture generator (NEW, executable, 472 lines).
- `scripts/integration-smoke/load-replay.py` — async httpx replay engine (NEW, executable, 660 lines).
- `scripts/integration-smoke/load-replay-report-schema.json` — Draft 2020-12 report contract (NEW, 125 lines).
- `.planning/load-test-fixtures/.gitignore` — directory marker with `*.jsonl` (NEW, 1 line).
- `.gitignore` — append 2 Phase-11 pattern blocks ABOVE the trailing `.claude/` block (MODIFIED, +6 lines).

## Decisions Made

- **STT multipart implementation:** Use httpx `files=` kwarg with the audio bytes read into memory (44 KB OGG/Opus) rather than streaming via `open(...)` — keeps the request fully self-contained per replay record and avoids file-descriptor lifecycle confusion under bounded concurrency.
- **Speedup applies only to inter-record delay, not to HTTP timeout:** `httpx.Timeout(60.0, connect=10.0)` is preserved verbatim regardless of `--speedup`. Compressing the replay window must not compress per-request budget — that would conflate two independent dimensions.
- **`error_rate_lt_1pct` excludes 503 from numerator:** `(count(status_code >= 500 or < 0) - count(status_code == 503)) / total < 0.01`. Per D-04 "<1% non-503" — sensitive-tenant 503s are expected per RES-08 and must not pollute the gate.
- **Gates default-safe when route absent:** if the fixture window had no `/v1/chat/completions` traffic, `p95_chat_ms_le_5000=True` (the limit is trivially satisfied by an empty distribution). Operator inspects `summary.routes[route].n` to confirm coverage.
- **`http2=False` for httpx client:** project does not declare `h2` as a runtime dep in `scripts/integration-smoke/requirements.txt`. Defaulting to HTTP/1.1 keeps the script working out of the box; can be promoted to HTTP/2 in 11-06 if observed connection pooling becomes a bottleneck.

## Deviations from Plan

None — plan executed exactly as written. The plan body was thorough and pre-resolved every contract detail (column names verified against migrations, fixture path tracked, schema shape locked, gate names verbatim D-04). All 26 acceptance criteria across the 3 tasks pass; the plan-level `<verification>` block runs clean.

## Issues Encountered

None during implementation. Two minor adjustments during task 1 implementation:

1. The acceptance criterion "Pattern B 8-section spine: grep ≥8 section markers" required adding explicit `# Module docstring` and `# main` comment lines (the docstring block above them counts conceptually but the grep is literal). Fixed in-place during task 1 — no commit-level deviation.
2. The negative-grep acceptance criterion `! grep -E '_replay_api_key|"api_key"\s*:|"Authorization"\s*:'` initially tripped on the docstring prose that mentioned the forbidden field name `_replay_api_key`. Resolved by rephrasing the docstring to use the equivalent phrase "replay-key field" — no semantic loss.

## User Setup Required

None for this plan. Live UAT (PRD-01 actual execution) lives in plan 11-06 and will require:

- ops-claude env vars `IFIX_KEY_CONVERSEAI`, `IFIX_KEY_CHAT_IFIX`, `IFIX_KEY_CAMPANHAS`, `IFIX_KEY_VOICE_API` (the 4 normal-class tenants).
- `DASHBOARD_DATABASE_URL` or `AI_GATEWAY_DB_URL` (prod DSN, mode-600 .env on ops-claude) to run the exporter.
- Cloudflare DNS already in place from Phase 10; no DNS changes.

## Next Plan Readiness

- 11-06 (load-test execution) can consume both scripts as-is. The PRD-01 fixture-window choice (peak ~14-15 BRT from audit_log) is operator discretion in 11-06.
- 11-02 / 11-07 / 11-08 are independent and unblocked.

## Self-Check

Verified after writing SUMMARY.md:

- `scripts/integration-smoke/audit-log-export.py` — FOUND (commit `f337e87`).
- `scripts/integration-smoke/load-replay.py` — FOUND (commit `d9ccd19`).
- `scripts/integration-smoke/load-replay-report-schema.json` — FOUND (commit `d9ccd19`).
- `.planning/load-test-fixtures/.gitignore` — FOUND (commit `a921643`).
- `.gitignore` — MODIFIED (commit `a921643`).
- All 3 task commits verified in `git log --oneline -5`.

## Self-Check: PASSED

---
*Phase: 11-prod-hardening*
*Plan: 11-01 load-test-scaffolding*
*Completed: 2026-05-27*
