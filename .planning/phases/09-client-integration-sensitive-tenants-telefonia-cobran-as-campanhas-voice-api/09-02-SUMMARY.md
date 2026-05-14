---
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
plan: 02
subsystem: integration-smoke
tags: [res-08, sensitive, failover, smoke-test, lgpd, audit]
requires:
  - "scripts/integration-smoke/smoke-chat-ifix.py (skeleton analog)"
  - "scripts/integration-smoke/report-schema.json (schema analog)"
  - "gateway/internal/proxy/sensitive.go (RES-08 server-side contract)"
  - "ai_gateway.audit_log + audit_log_content (audit-DB read target)"
provides:
  - "scripts/integration-smoke/smoke-sensitive-failover.py (RES-08 fail-closed black-box smoke)"
  - "scripts/integration-smoke/sensitive-failover-report-schema.json (JSON Schema for the smoke report)"
affects:
  - "scripts/integration-smoke/requirements.txt (added psycopg[binary])"
tech-stack:
  added:
    - "psycopg[binary]>=3.1.0 — audit-DB reader (the /admin/audit endpoint is event_kind-filtered and cannot surface request-level blocked_sensitive rows)"
  patterns:
    - "smoke-script contract: distinct per-gate exit code + schema-validated JSON report"
    - "secret-once discipline: --api-key / --pg-dsn no committed default, never logged, never in report"
    - "induced-failure HARD pre-condition: poll /v1/health/upstreams until tier-0 shows open before evaluating gates"
key-files:
  created:
    - "scripts/integration-smoke/smoke-sensitive-failover.py"
    - "scripts/integration-smoke/sensitive-failover-report-schema.json"
  modified:
    - "scripts/integration-smoke/requirements.txt"
decisions:
  - "Induced-failure trigger: --induce-failure-via operator-prestep (default) — gatewayctl has NO breaker force-open subcommand (gateway/cmd/gatewayctl/upstreams.go exposes only list/update/enable/disable), so the gatewayctl mode errors out telling the operator to use operator-prestep"
  - "Audit verification via direct DB read (AI_GATEWAY_PG_DSN), not the /admin/audit endpoint — confirmed /admin/audit filters event_kind IS NOT NULL and cannot surface request-level blocked_sensitive rows"
  - "audit_log_content gate uses SELECT COUNT(*) ONLY — never selects content columns (threat T-09-07: no sensitive prompt/response body is pulled into the smoke process)"
metrics:
  duration: "~12 min"
  tasks_completed: 2
  files_changed: 3
  completed_date: 2026-05-14
---

# Phase 9 Plan 02: Sensitive-Failover Smoke Test Summary

RES-08 sensitive-class failover smoke — `smoke-sensitive-failover.py` + its
`sensitive-failover-report-schema.json` — the black-box proof that a
`data_class: sensitive` tenant request, during an induced tier-0 upstream
failure, fails closed with a 503 envelope, is audited as `blocked_sensitive`
with zero `audit_log_content` rows, and is NEVER proxied to OpenAI/OpenRouter.

## What Was Built

### Task 1 — `sensitive-failover-report-schema.json` (commit `1066489`)

JSON Schema draft 2020-12 mirroring the Phase-8 `report-schema.json` shape
strictly:
- `$id` = `https://ifixtelecom.com.br/schemas/integration-smoke/sensitive-failover-report/1.0.0`
- `additionalProperties: false` on the root AND every sub-object
- `schema_version` is a `const` `1.0.0`; `git_sha` optional with pattern `^[0-9a-f]{7,40}$`
- per-check objects `fail_closed`, `never_external`, `audit_decision` (all required)
  + `streaming_fail_fast` (optional), each with `{status_code, ok}` plus
  check-specific fields (`retry_after`/`envelope_code`, `audit_upstream`,
  `audit_log_row_found`/`audit_log_content_rows`, `elapsed_ms`)
- `gates` object requiring `fail_closed`, `never_external`, `audit_decision`,
  `all_passed` booleans + an optional `streaming_fail_fast` boolean

### Task 2 — `smoke-sensitive-failover.py` (commit `7ce79fc`)

Copies the `smoke-chat-ifix.py` skeleton (module docstring + exit-code table,
`Config` dataclass, `parse_args`, `main_async`, report-write + schema-validate +
git_sha tail, `main()` with `sys.exit(asyncio.run(...))`). Encodes the
`sensitive_block_test.go` assertions as black-box gates:

1. **fail_closed** — sensitive `POST /v1/chat/completions` (non-streaming) while
   tier-0 is OPEN → asserts `503` + body contains
   `upstream_unavailable_for_sensitive_tenant` + `Retry-After: 30`; captures the
   `X-Request-ID` response header.
2. **never_external** — the captured `X-Request-ID`, looked up in
   `ai_gateway.audit_log`, must have `upstream = 'blocked_sensitive'` — the
   black-box equivalent of the in-process test's `tier1.hits.Load() == 0`.
3. **audit_decision** — an `audit_log` row must exist for the request_id AND
   `SELECT COUNT(*) FROM ai_gateway.audit_log_content` must be `0` (D-B2).
4. **streaming_fail_fast** (optional, `--skip-streaming-gate`) — sensitive +
   `stream:true` 503s in `< 500ms`.

Induced-failure step (`--induce-failure-via {operator-prestep,gatewayctl}`,
default `operator-prestep`): prints the exact operator pre-step to trip the
tier-0 LLM breaker, then polls `GET /v1/health/upstreams` until `local-llm`
shows `state=open` (bounded 30s). If it never opens, the smoke writes an
unevaluated all-false report and exits `1` — the gates are never evaluated
against a healthy upstream.

Exit-code contract: `0` all gates passed; `2` fail_closed; `3` never_external;
`4` audit_decision; `5` streaming_fail_fast; `6` multiple; `1` fallback /
pre-condition not met. Report validated (warn-don't-fail) against the Task 1
schema.

`psycopg[binary]>=3.1.0` added to `requirements.txt`.

## Verification

- Task 1: `sensitive-failover-report-schema.json` is valid JSON, draft 2020-12,
  with `additionalProperties:false` on root + gates, `schema_version` const,
  `git_sha` pattern — automated check passed.
- Task 2: `py_compile` passes; all grep checks pass
  (`upstream_unavailable_for_sensitive_tenant`, `blocked_sensitive`,
  `audit_log_content`, `X-Request-ID`, `--induce-failure-via`, `--api-key`,
  `all_passed`); `--help` shows `--gateway-url`, `--api-key`, `--out`,
  `--pg-dsn`, `--induce-failure-via`; `--api-key` and `--pg-dsn` have no
  committed default — argparse exits `2` with NO network/DB call when absent.
- Schema conformance: 3 synthetic report shapes (all-pass with streaming,
  all-pass without streaming, unevaluated) validate against the Task 1 schema.
- `psycopg` present in `requirements.txt`.

## Deviations from Plan

None — plan executed exactly as written. The `--induce-failure-via` strategy
selection (a documented executor decision point in the plan's `<interfaces>`)
resolved to `operator-prestep` as the default after inspecting
`gateway/cmd/gatewayctl/upstreams.go` and confirming no breaker force-open
subcommand exists; the `gatewayctl` mode is kept as an explicit branch that
errors out honestly rather than silently falling through.

## Threat Mitigations Applied

- **T-09-06** (sensitive tenant key leak): key comes only from
  `--api-key`/`SMOKE_API_KEY`, no committed default, argparse-errors with no
  network/DB call when absent, never passed to `log()`, never written to the
  report (`target` permits only `gateway_url` + `tenant` per the schema's
  `additionalProperties: false`).
- **T-09-07** (reading sensitive content while verifying the audit gate): the
  `audit_log_content` query is `SELECT COUNT(*)` ONLY — never selects content
  columns.
- **T-09-08** (false GREEN): the induced-failure step is a HARD pre-condition —
  the smoke polls `/v1/health/upstreams` and only proceeds once `local-llm`
  shows `open`; otherwise it writes an all-false report and exits `1`.
- **T-09-09** (DSN leak): the DSN comes from `--pg-dsn`/`AI_GATEWAY_PG_DSN`,
  never logged, never written to the report; audit-query errors strip the DSN.

## Self-Check: PASSED

- `scripts/integration-smoke/sensitive-failover-report-schema.json` — FOUND
- `scripts/integration-smoke/smoke-sensitive-failover.py` — FOUND
- `scripts/integration-smoke/requirements.txt` (modified) — FOUND
- commit `1066489` — FOUND
- commit `7ce79fc` — FOUND
