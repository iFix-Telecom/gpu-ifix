---
phase: 11
plan: 11-06
slug: load-test-live-uat
status: blocked-tech-debt
date: 2026-05-27
operator: pedro (orchestrator-driven)
spend_usd: 0.04
---

# Phase 11 PRD-01 Load-Test Evidence — BLOCKED on primary reconciler tech debt

## Summary

11-06 ABORTED before DRY-RUN. Two blockers surfaced:

1. **Dev audit_log volume insufficient for realistic baseline** — 201 rows over 5 weeks; only 32 rows usable (converseai tenant has env-var key). Plan gate requires ≥1000 rows with 5 route classes. Workaround attempted: filter+x32 synthesis → 1024 rows with all 5 routes, accepted as artificial baseline.

2. **Primary reconciler hangs silently between offer-pick and lifecycle-create DB write** (NEW tech debt, recurrence of Phase 06.6 class). Vast instance provisioned but no row written to `ai_gateway.primary_lifecycles`, FSM stuck at `provisioning` with empty `lifecycle_id`. ~$0.04 orphan spend (5min @ $0.485/hr) before manual destroy via Vast API.

## Task 06-01 Preflight (PASS)

All 5 sub-checks from 11-01 deliverables PASS:
- audit-log-export.py imports + has audit_log_content JOIN
- load-replay.py imports + 7 args + IFIX_KEY_ + multipart
- load-replay-report-schema.json Draft 2020-12 valid + panic gate `[boolean,null]` + gates_external_inputs present
- whatsapp-sample.ogg present, Opus 16kHz
- load-test-fixtures/.gitignore + .gitignore pattern present

## Task 06-02 Fixture Validation (PARTIAL — synthesized due to dev gateway low traffic)

Real audit_log export from `bd_ai_gateway_prod.ai_gateway.audit_log` (NOTE: initial probe used legacy `bd_ai_gateway` DB — confirmed false target via debug session `audit-pipeline-silent-since-2026-05-25.md`; numbers below are still valid for the dev-gateway-traffic-volume observation):
- Window: 2026-04-19T00:00Z → 2026-05-26T00:00Z (entire 5-week gateway lifetime)
- Total rows: 201 (10 dropped — system rows without tenant_slug)
- Tenants present: converseai-uat (155), converseai (32), uat02-test (14)
- Sensitive: 0 (correct — Pitfall 1 honored)
- Routes: chat 188 / speech 7 / voices 3 / embed 2 / STT 1

Operator only has env-var keys for `converseai` tenant (per `~/.claude/CLAUDE.md`). Filtered to 32 converseai rows, multiplied x32 → `/tmp/load-fixture.jsonl` 1024 rows. Sanitization invariants verified:
- Zero `ifix_sk_` / `sk-or-` / `Bearer ` substrings in fixture bytes
- Zero `data_class==sensitive` rows
- Zero `_replay_api_key` fields

Synthesized route mix:
| Route | Count |
|-------|-------|
| /v1/chat/completions | 608 |
| /v1/audio/speech | 224 |
| /v1/audio/voices | 96 |
| /v1/embeddings | 64 |
| /v1/audio/transcriptions | 32 |

This is artificial — replay loops the same 32 prompts 32× each. Useful as gateway functional verification under sustained load, NOT representative of prod traffic shape.

## Task 06-03 DRY-RUN ABORTED — primary reconciler bug

### Preflight findings on n8n-ia-vm (prod gateway stack at `/opt/ai-gateway-prod/`)

Initial primary state = asleep. Triggered `gatewayctl primary force-up --reason 11-06_load_test_uat`.

First force-up attempt FAILED: `primary provisionLifecycle returned error: "PRIMARY_WHISPER_WEIGHTS_SHA256 is empty — operator must set this env var explicitly (no default shipped)"`.

### Env drift detected — n8n-ia-vm Phase 06.7 config incomplete

Diffed `/opt/ai-gateway-prod/.env` (n8n-ia-vm, prod stack) vs `/opt/ai-gateway-dev/.env` (vps-ifix-vm, dev stack). 13 missing env vars on prod:

- PRIMARY_QWEN_WEIGHTS_SHA256
- PRIMARY_WHISPER_WEIGHTS_SHA256
- PRIMARY_BGEM3_WEIGHTS_SHA256
- PRIMARY_TEMPLATE_IMAGE (ghcr.io/ifixtelecom/converseai-primary-pod:develop)
- PRIMARY_HOST_ID (0)
- UPSTREAM_TTS_URL + UPSTREAM_TTS_PIPER_URL
- WEIGHTS_QWEN_KEY + SHA256
- WEIGHTS_WHISPER_KEY + SHA256
- WEIGHTS_BGE_M3_KEY + SHA256

Phase 06.7 (Kani-TTS swap, Embed CPU host) deploy on n8n-ia-vm was incomplete. Operator appended the missing vars from dev .env (backup: `/opt/ai-gateway-prod/.env.bak-11-06-uat`), then `docker compose up -d` recreated the container.

### Second force-up attempt — silent reconciler hang

```
22:01:42 INFO primary force-up: provisioning by operator request reason=11-06_load_test_uat_retry
22:01:42 INFO primary offer picked offer_id=31139421 machine_id=55158 host_id=167329 dph=0.482 geo="Germany, DE"
22:01:42  ← LAST PRIMARY LOG LINE
22:02:33  ↓ gateway still alive (breaker flap logs continue)
22:05:33  model aliases refreshed (gateway healthy)
```

Vast API: instance 38164478 ALIVE + RUNNING at $0.485/hr.

DB `ai_gateway.primary_lifecycles`: NO row created (`SELECT COUNT(*) WHERE started_at > NOW() - INTERVAL '30 minutes'` returned 0).

FSM frozen at:
```
state         provisioning
lifecycle_id  (empty)
pod_url       (empty)
entered_at    1779930102
```

### Root cause (hypothesis)

Code path between `primary_offer_picked` and lifecycle-row DB INSERT hangs/errors silently. Possible causes (each requires source debug; deferred):
1. `provisionLifecycle()` panics + recoverer swallows without log emit
2. Goroutine deadlock on a channel post-offer-pick
3. DB INSERT to `primary_lifecycles` errors but the error path doesn't write to gateway log
4. Vast API `instances/<offer_id>/launch` returns success but follow-up reconciler expects a different response shape

### Cleanup actions

- `curl -X DELETE` against Vast API for instance 38164478 → `{"success": true}` confirmed. 0 instances remaining.
- `gatewayctl primary force-down --reason 11-06_abort_orphan_destroyed` → FSM reset to `asleep`.
- Spend: ~$0.04 (instance ran ~5 min before destroy).

## Task 06-04 30-min sustained run — NOT EXECUTED (blocked on Task 06-03)

## Task 06-05 EVIDENCE.md (this file)

## Gates verdict

| Gate | Status | Note |
|------|--------|------|
| p95_chat_ms_le_5000 | NOT_RUN | DRY-RUN aborted |
| p95_embed_ms_le_1000 | NOT_RUN | DRY-RUN aborted |
| p95_stt_ms_le_10000 | NOT_RUN | DRY-RUN aborted |
| error_rate_lt_1pct | NOT_RUN | DRY-RUN aborted |
| zero_5xx_panic | NOT_RUN | DRY-RUN aborted |
| all_passed | false | — |

## Secret hygiene attestation

Zero raw API keys, Authorization headers, request bodies, response bodies, DSNs, or PII in this evidence file. All tenant references are slug labels only (`converseai`, `converseai-uat`, `uat02-test`). DSNs documented as host:port shapes without credentials. Vast API token used in cleanup is in `~/.claude/CLAUDE.md` (local-only, not committed).

## Carry-forward tech debt (for Phase 11 follow-up / v2)

1. **Primary reconciler silent hang post-offer-pick** (NEW, CRITICAL). When `provisionLifecycle` is invoked via force-up event, the path between offer-pick log and lifecycle-DB-INSERT silently stops. Vast instance gets created (so reconciler DID call Vast API and got success) but no DB row, no further logs, FSM never advances. Needs source-level debug: add tracing between offer-pick and lifecycle.Insert; suspect goroutine deadlock or unwrapped error in `gateway/internal/primary/lifecycle.go` provision path. Operator workaround until fix: monitor Vast UI directly + manual destroy when FSM hangs.

2. **Dev gateway low traffic** (KNOWN). `audit_log` in legacy `bd_ai_gateway` accumulates only 201 rows over 5 weeks → fixture x32 synthesis used. Re-run 11-06 once prod tenants (converseai, chat-ifix, telefonia, cobrancas, campanhas, voice-api) generate ≥1000 rows in a 1-hour window. NOTE: prod traffic now lands in `bd_ai_gateway_prod` (post-Phase 10 cutover) — re-export against the new DB once volume is sufficient.

3. **n8n-ia-vm Phase 06.7 config drift** (RESOLVED 2026-05-27T22:00Z). 13 env vars (WEIGHTS_*, PRIMARY_*_SHA256, UPSTREAM_TTS_*, PRIMARY_TEMPLATE_IMAGE, PRIMARY_HOST_ID) missing on n8n-ia-vm prod stack `.env`. Appended from vps-ifix-vm dev `.env` reference. Add a deploy preflight gate (scripts/deploy/preflight.sh from 11-05) that diffs stack .env against expected-key list per phase milestone.

## Outcome

11-06 BLOCKED until tech debt #1 is fixed. Phase 11 ships with 11-06 as deferred-evidence — gateway live load baseline + per-route per-upstream attribution NOT captured. PRD-01 traceability marks as **partial** (load-test infrastructure shipped via 11-01, scripts/integration-smoke/* + schema + fixture verified; live UAT deferred).

---

## Pre-flight re-attempt — 2026-05-28T20:55Z

**Trigger:** Operator requested live UAT execution after Phase 11 addendum closure (audit-pipeline 5-PR chain landed).

**Stage 1 (11-01 deliverable pre-flight):** ALL 5/5 PASS.

- `scripts/integration-smoke/load-replay-report-schema.json` present.
- `python3 scripts/integration-smoke/load-replay.py --help` exits 0 + lists all 7 required args.
- `python3 scripts/integration-smoke/audit-log-export.py --help` exits 0.
- `scripts/integration-smoke/fixtures/whatsapp-sample.ogg` present + OGG/Opus signature verified.
- `.planning/load-test-fixtures/.gitignore` present.

**Stage 1 fixture-availability check:** **GATE FAILED.**

`bd_ai_gateway_prod.ai_gateway.audit_log` volume by hour + route (last 7 days):

| Hour (UTC)              | Route                | Rows | Normal |
|-------------------------|----------------------|------|--------|
| 2026-05-26 20:00        | /v1/health/upstreams | 29   | 0      |
| 2026-05-26 20:00        | /v1/embeddings       | 20   | 20     |
| 2026-05-28 14:00        | /v1/health/upstreams | 16   | 0      |
| 2026-05-28 14:00        | /v1/chat/completions | 10   | 8      |
| 2026-05-26 16:00        | gatewayctl_breaker   | 8    | 8      |
| 2026-05-28 01:00        | /v1/health/upstreams | 7    | 6      |
| 2026-05-26 16:00        | /v1/chat/completions | 6    | 6      |
| 2026-05-26 20:00        | /v1/chat/completions | 6    | 4      |
| 2026-05-28 01:00        | /v1/chat/completions | 6    | 2      |
| 2026-05-26 19:00        | /v1/chat/completions | 5    | 5      |
| 2026-05-28 20:00        | /v1/chat/completions | 4    | 0      |

Aggregate replayable (chat + embed) across 7 days: ~57 rows. Plan gate `[reviews LOW #4]` requires **≥1000 rows AND 5 route classes (chat, embed, STT, tool-call, stream)**. Volume short by ~940 rows. STT, tool-call, and streaming route classes absent — zero rows for `/v1/audio/transcriptions`; no captured `tool_calls` in any normal chat row examined.

**Cause:** Prod cutover was 2026-05-26 ~11:18 UTC (~2 days before this re-attempt). Phase 11 PRD-01 assumed prod gateway would have accumulated days/weeks of real tenant traffic before the live UAT. Tenant integration (converseai, chat-ifix, telefonia, cobrancas, campanhas, voice-api) is in early ramp; combined volume insufficient.

**Decision:** Defer 11-06 + 11-07 live UATs to a future session. No Vast spend incurred. Re-export fixture once prod accumulates ≥1000 rows in a 1-hour replay window AND covers all 5 route classes. Rough estimate: 1–2 weeks of natural traffic growth, or operator-triggered synthetic warm-up if business deadline forces earlier execution (would document as `passed_partial` with synthetic-fixture caveat — see Open Question #1 baseline / Pitfall 1).

**Phase 11 closure impact:** Phase 11 status stays `passed_partial` (already so). PRD-01 + PRD-02 stay `passed_partial` (already so). The audit-pipeline label gap (the one substantive Phase 11 reopen this session) closed via the 5-PR chain (#8, #9, #11, #12, #13) — PRD-03 flipped to `passed`. Remaining live UATs are bounded by data accumulation, not by tech-debt or source-code blockers.
