---
phase: 11
plan: 11-06
slug: load-test-live-uat
status: preflight-passed-awaiting-dry-run-checkpoint
date: 2026-06-11
operator: pedro (orchestrator-driven, parallel executor agent)
spend_usd: 0.00
---

# Phase 11 PRD-01 Load-Test Evidence

> Re-attempt 3 — 2026-06-11. Prior attempts (2026-05-27, 2026-05-28) DEFERRED at
> the fixture-availability gate (`bd_ai_gateway_prod` had ~57 replayable rows vs
> the ≥1000 gate). The primary reconciler silent-hang blocker was RESOLVED via
> commit `01e7558` (quick 260527-wgs). **This attempt: the data-volume blocker
> is also resolved — a real (non-synthesized) 1207-row fixture was exported from
> a genuine prod peak window.** History from the two prior attempts is preserved
> in the "Prior Attempts" appendix at the bottom of this file.

## Metadata

- Date: 2026-06-11
- Operator host: ops-claude (162.55.92.154 — whitelisted in DO Trusted Sources)
- Gateway target URL: https://ai-gateway.converse-ai.app
- Prod gateway host: n8n-ia-vm, stack `/opt/ai-gateway-prod`
- Audit DB (correct target): `bd_ai_gateway_prod.ai_gateway.audit_log` via env
  `AI_GATEWAY_PG_DSN` (NOT `DASHBOARD_DATABASE_URL` → that points to
  `bd_ai_dashboard_prod`, which has no `audit_log` table — corrected this session)
- Fixture path: `/tmp/load-fixture.jsonl` (1207 rows, LOCAL/gitignored — never committed)

## Task 11-06-01 — 11-01 Deliverables Preflight (PASS, all 5 sub-checks exit 0)

| Sub-check | Result | Evidence |
|-----------|--------|----------|
| 1. audit-log-export.py importable + JOIN present | PASS | `--help` exits 0, 8 args matched; `LEFT JOIN ai_gateway.audit_log_content c` at line 319 |
| 2. load-replay.py importable + 7 args + env keys + multipart | PASS | `--help` exits 0, all 7 args present; `IFIX_KEY_` 3 matches; `files = {...}` multipart at line 398 |
| 3. report schema Draft 2020-12 valid + panic gate `[boolean,null]` + gates_external_inputs | PASS | `jsonschema.Draft202012Validator.check_schema` OK; assertions pass |
| 4. STT fixture OGG signature | PASS | `Ogg data, Opus audio, version 0.1, mono, 16000 Hz` |
| 5. fixture dir marker + gitignore | PASS | `.planning/load-test-fixtures/.gitignore` present; `.gitignore` carries `load-test-fixtures/*.jsonl` |

Runtime deps on host confirmed importable: `jsonschema`, `httpx 0.28.1`, `psycopg`,
`structlog`, `jq 1.6`. `python` binary absent — used `python3` (Python 3.11.2).

Zero Vast spend, zero HTTPS calls to prod gateway in this task.

## Task 11-06-02 — Fixture Export + Row-Count + Route-Mix Gate (PASS — REAL prod fixture)

**Decisive change vs prior attempts:** prod `audit_log` has accumulated real
tenant traffic. The export is genuine prod data — NOT the x32 synthesis the
2026-05-27 attempt was forced into.

Prod audit_log volume survey (last 30 days, `AI_GATEWAY_PG_DSN` →
`bd_ai_gateway_prod.ai_gateway.audit_log`):

- Total rows: 1587
- Route distribution: `/v1/chat/completions` 774, `/v1/embeddings` 425,
  `/v1/audio/transcriptions` 304, `/v1/health/upstreams` 63, `gatewayctl_breaker` 21
- Busiest contiguous window: 2026-05-28 22:00–24:00 UTC (1207 rows)

Chosen export window: **2026-05-28T22:00:00Z → 2026-05-29T00:00:00Z** (2 h).
`audit-log-export.py` emitted **1207 rows, 0 errors, 0 skipped**.

Gate results (`/tmp/load-fixture.jsonl`):

| Gate | Threshold | Actual | Verdict |
|------|-----------|--------|---------|
| Row count | ≥ 1000 | 1207 | PASS |
| Route: chat | present | 704 | PASS |
| Route: embed | present | 401 | PASS |
| Route: STT | present | 101 | PASS |
| Chat streaming shape (`_sanitized_body.stream==true`) | ≥ 1 | 100 | PASS |
| Chat tool-call shape (`tools`/`tool_choice`) | ≥ 1 | 203 | PASS |
| Sensitive rows | 0 | 0 | PASS (Pitfall 1) |
| `_replay_api_key` field rows | 0 | 0 | PASS |
| Raw key material (`ifix_sk_`/`sk-or-`/`Bearer `) in fixture bytes | 0 | 0 | PASS |
| NULL tenant_id rows dropped | n/a | 0 | PASS (no rows lost) |

Tenants in fixture (NAME only — no key values): `converseai` (data_class=normal).
Required env var: `IFIX_KEY_CONVERSEAI`. Step E preflight Python check exits 0
(env var exported in operator shell; sourced from local-only `~/.claude/CLAUDE.md`,
never written to any committed file).

**Single-tenant caveat:** the peak window is 100% `converseai` (normal) traffic —
this is the Phase 11.2 STT-cascade UAT traffic plus organic chat/embed. Per D-01 +
Open Question #1 the baseline excludes sensitive tenants by design, so a
single-normal-tenant fixture satisfies the PRD-01 baseline contract. It does mean
the replay exercises one tenant's auth path repeatedly rather than a multi-tenant
mix; documented as a known characteristic, not a gate failure.

## Task 11-06-03 — DRY-RUN — CHECKPOINT (awaiting operator GO/NO-GO)

Read-only preflight already gathered (no Vast spend incurred):

- Primary FSM state: **`asleep`** (lifecycle_id empty, no Vast instance running —
  operator must `gatewayctl primary force-up` before the DRY-RUN per checkpoint Step 1a).
- Prod gateway reachable; `/v1/health/upstreams` with tenant Bearer returns HTTP 503
  (expected while primary asleep) and lists 8 upstreams:
  `local-embed, local-llm, local-stt, local-tts, openai-embed, openai-whisper,
  openrouter-chat, voice-api-piper`. (Topology grew since plan authoring — Phase
  06.7 added TTS, Phase 11.2 added STT cascade rows; the plan's expected 6-upstream
  list is a subset.)
- Tenant key `IFIX_KEY_CONVERSEAI` authenticates against prod (401→authenticated;
  503 is breaker state, not auth failure).

**This is a blocking human-verify checkpoint. Execution PAUSED here.** The
operator must acknowledge the $5 Vast spend cap, force-up the primary
(2×RTX 3090, allowlist 43803,55158, cap $0.60), run the 2-min DRY-RUN, and
return a GO/NO-GO signal. Tasks 11-06-04 (30-min run) and 11-06-05 (evidence
authoring + commit) run after GO.

## Secret Hygiene Attestation (interim — tasks 01–02)

ATTESTATION: This evidence file contains ZERO raw API keys, ZERO Authorization
header values, ZERO request bodies, ZERO response bodies, ZERO DSN strings, ZERO
PII. All tenant references use slug labels (`converseai`) or env-var NAMES
(`IFIX_KEY_CONVERSEAI`). The audit DSN was bound to a shell variable read over
SSH from the prod `.env` and never printed. Verified by: pedro (executor agent)
at 2026-06-11T19:20Z.

---

## Prior Attempts (appendix — preserved from earlier sessions)

### Attempt 1 — 2026-05-27 (BLOCKED on primary reconciler tech debt)

11-06 ABORTED before DRY-RUN. Two blockers: (1) dev audit_log volume insufficient
(201 rows over 5 weeks; only 32 usable → x32 synthesis to 1024 rows, artificial
baseline); (2) primary reconciler hung silently between offer-pick and
lifecycle-create DB write (~$0.04 orphan spend before manual destroy). Blocker (2)
RESOLVED 2026-05-27 via commit `01e7558` (3-strike confirm + BestEffortDestroy).
Also discovered + fixed n8n-ia-vm Phase 06.7 config drift (13 missing env vars,
appended from dev `.env`; backup `/opt/ai-gateway-prod/.env.bak-11-06-uat`).

### Attempt 2 — 2026-05-28T20:55Z (DEFERRED on data volume)

Stage-1 deliverable preflight 5/5 PASS. Fixture-availability GATE FAILED:
`bd_ai_gateway_prod` had ~57 replayable rows over 7 days vs the ≥1000 + 5-route-class
gate (prod cutover was only ~2 days prior; tenant integration in early ramp).
Decision: defer 11-06 + 11-07 live UATs to a future session; re-export once prod
accumulates ≥1000 rows in a window. **No Vast spend incurred.**

### Attempt 3 — 2026-06-11 (THIS session)

Data-volume blocker RESOLVED — real 1207-row fixture exported from the
2026-05-28 22:00–24:00 UTC prod peak window. See tasks 11-06-01 / 11-06-02 above.
Paused at the Task 11-06-03 DRY-RUN human-verify checkpoint (blocking — requires
operator Vast spend-cap acknowledgement + force-up + GO/NO-GO).
