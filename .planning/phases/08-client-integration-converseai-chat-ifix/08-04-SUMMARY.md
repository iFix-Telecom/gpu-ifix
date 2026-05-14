---
phase: 08-client-integration-converseai-chat-ifix
plan: 04
subsystem: client-integration
tags: [human-uat, runbook, rollback, deferred-uat, operator-doc]
requires:
  - "scripts/integration-smoke/provision-tenants.sh — Phase 8 plan 01"
  - "scripts/integration-smoke/smoke-converseai.py — Phase 8 plan 02"
  - "scripts/integration-smoke/smoke-chat-ifix.py + whatsapp-sample fixture/baseline — Phase 8 plan 03"
  - "gateway/docs/RUNBOOK-FAILOVER.md — structural analog (section skeleton)"
provides:
  - "gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md — operator runbook with the load-bearing <5-min per-app ROLLBACK procedure"
  - ".planning/phases/08-client-integration-converseai-chat-ifix/08-HUMAN-UAT.md — live-verification scenario sheet + sign-off table for SC1/SC2/SC3/SC4"
affects:
  - "Phase 8 close — the HUMAN-UAT sign-off table + overall phase-status line gate the live integration; passed_partial is the documented path while the gateway is undeployed"
tech-stack:
  added: []
  patterns:
    - "RUNBOOK section skeleton mirrored from RUNBOOK-FAILOVER.md (Read-this-when / Mental Model / Quick Diagnosis / Symptom blocks / ROLLBACK / Required Env Vars / Escalation / Related Docs)"
    - "HUMAN-UAT deferral pattern (autonomous build green; live credential/traffic verification behind a blocking checkpoint) — mirrors 03-08 / 04-09 / 06-11 / 07-09"
    - "per-app + per-consumer rollback procedure — env-var diff -> redeploy -> verify, with the audit_log verify step catching half-switched states"
key-files:
  created:
    - "gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md"
    - ".planning/phases/08-client-integration-converseai-chat-ifix/08-HUMAN-UAT.md"
  modified: []
decisions:
  - "Runbook written to gateway/docs/ (dominant convention — 4 of 5 runbooks live there) per 08-PATTERNS.md location decision"
  - "Env-var NAMES in the runbook + UAT use the standard OpenAI-SDK/LangChain names (OPENAI_BASE_URL, OPENAI_API_KEY, ANTHROPIC_*) with explicit > CONFIRM: notes — the converseai-v4 + campanhas-chatifix sibling repos are not edited by Phase 8, so the operator confirms the exact names against those repos before drilling the rollback"
  - "Task 3 (blocking checkpoint) executed as a DEFERRED HUMAN-VERIFY under autonomous orchestration — programmatic checks run + recorded; the live deployed-gateway UAT is documented as deferred, mirroring the Phases 1-7 human_needed / passed_partial pattern"
metrics:
  duration: "~10 min"
  completed: "2026-05-14"
  tasks: 2
  files: 2
---

# Phase 8 Plan 04: Client-Integration Runbook + HUMAN-UAT Summary

The phase-closing HUMAN-UAT plan — writes the operator runbook
(`RUNBOOK-CLIENT-INTEGRATION.md`, with the load-bearing <5-min per-app
ROLLBACK procedure) and the live-verification scenario sheet
(`08-HUMAN-UAT.md`, covering SC1-SC4), then defers the live integration UAT
behind the standard human-verify gate. The autonomous Phase 8 build
(08-01..08-03) ships the gpu-ifix-side artifacts and stays green — it is NOT
blocked by this plan or the un-deployed gateway.

## What Was Built

### Task 1 — `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` (commit `a9e2f3f`)

The operator runbook for the converseai + chat-ifix gateway integration,
mirroring the `RUNBOOK-FAILOVER.md` section skeleton:

- **Title + "Read this when:"** — trigger bullets: a client app being
  pointed at the gateway, a client integration misbehaving, the integration
  needing rollback, post-incident review. Includes the gateway-not-deployed
  gate note.
- **Mental Model (30 seconds)** — the env-var contract (`base_url` ->
  gateway, `api_key` -> per-tenant key), a compact client-app -> gateway ->
  upstream diagram, and the **two-consumers-one-tenant model**: ConverseAI
  v4's `apps/api` (Elysia/OpenAI-SDK) + `agents/` (Python/LangChain) both on
  the single `converseai` tenant; Chat Ifix's transcription backend in the
  `campanhas-chatifix` sibling repo on the `chat-ifix` tenant. Both tenants
  `data_class normal`.
- **Quick Diagnosis (~2 minutes)** — ordered `curl` of the gateway
  `/v1/chat/completions` + `/v1/audio/transcriptions` with each tenant key,
  the fuller `smoke-converseai.py` / `smoke-chat-ifix.py` checks, `psql`
  `audit_log` per-tenant attribution queries, and the Phase 7 dashboard
  tenant-table check.
- **Incident Response by Symptom** — five `### Symptom N` blocks (Likely
  cause / Diagnose / Mitigate / Recovery): 401 (wrong/unset api key), 429
  (tenant quota/rate-limit), non-incremental streaming (FlushInterval /
  proxy buffering), regressed transcription latency/quality (points at
  `smoke-chat-ifix.py` + the ±10% gates + the baseline-re-measure caveat),
  and the half-switched state (the ConverseAI two-consumer trap).
- **ROLLBACK procedure** — THE load-bearing section (SC3, <5 min). One
  numbered procedure per app, each env-var diff -> redeploy -> verify:
  *To roll back ConverseAI v4* covers **both** the `apps/api` and `agents/`
  consumers (a per-consumer env-var table); *To roll back Chat Ifix* via the
  `campanhas-chatifix` deploy flow. States the <5-min budget explicitly and
  that the procedure MUST be drilled in the HUMAN-UAT. The `audit_log`
  verify step catches a half-switched state before the stopwatch stops.
- **Required Env Vars** — a per-app `| Var | App/consumer | Required |
  Gateway value | Direct value | Purpose |` table covering the
  `*_BASE_URL` / `*_API_KEY` vars and both their gateway-vs-direct values.
- **Escalation** — 1st responder / escalation contact / per-severity comms.
- **Related Docs** — cross-links `08-CONTEXT.md`, `08-HUMAN-UAT.md`,
  `provision-tenants.sh`, both smoke scripts, `RUNBOOK-FAILOVER.md`,
  `RUNBOOK-QUOTAS-BILLING.md`, `RUNBOOK-OBSERVABILITY-ALERTING.md`,
  `CLAUDE.md` `## Dev Environment`.

### Task 2 — `08-HUMAN-UAT.md` (commit `98aac60`)

The live-verification scenario sheet, mirroring `06-HUMAN-UAT.md`:

- **YAML frontmatter** — `status: pending`, `phase`, `source`, `started` /
  `updated` / `operator` / `date_executed` blanks, `final_status: pending`.
- **## Prerequisites** — checkbox list: the gateway-not-deployed gate
  (blocked on Phase 6 emerg integration tests), `provision-tenants.sh
  --mint-keys` + capturing the three raw keys, the per-app env-var switch
  (ConverseAI v4 **both** consumers, Chat Ifix backend), the
  `requirements.txt` install, and the `baseline_latency_s` re-measure
  prerequisite for UAT-2.
- **## Tests** — four numbered `### N. UAT-N — <name> (SC-N)` blocks, each
  with Pre-conditions / Steps (fenced bash) / Expected / Pass-Fail:
  - **UAT-1 (SC1)** — ConverseAI env-var switch + `smoke-converseai.py`
    against the dev gateway; expects exit 0 + `gates.all_passed` true +
    `audit_log` attribution.
  - **UAT-2 (SC2)** — Chat Ifix env-var switch + `smoke-chat-ifix.py`;
    expects exit 0 + both `quality_within_10pct` AND `latency_within_10pct`
    gates passing vs the re-measured baseline.
  - **UAT-3 (SC3)** — the rollback drill: start a stopwatch, execute the
    runbook ROLLBACK procedure for BOTH apps, run both verify steps, stop
    the stopwatch; expects both apps verified rolled back in <5 min with a
    `measured_rollback_time` field.
  - **UAT-4 (SC4)** — open the Phase 7 dashboard, confirm `converseai` +
    `chat-ifix` render as separate tenant rows with independent latency
    (P50/P95/P99) + cost panels (verification of existing Phase 7 code).
- **## Sign-off** — a table (UAT / Scenario / SC / Result / Date / Operator
  / Notes) one row per scenario + an overall phase-status line.
- **## Final Sign-off** + **## passed_partial fallback** — documents the
  `passed_partial` path (undeployed gateway / unavailable credentials, with
  the autonomous build explicitly not blocked) and the `human_needed` path
  (a real defect, described precisely for a `/gsd-plan-phase --gaps` pass).

## Task 3 — Deferred / Human-Verify

Task 3 is a `checkpoint:human-verify` (`gate="blocking"`) — the live
integration UAT (env-var switch in the client repos, prod smoke, rollback
drill, dashboard cross-check). Under the autonomous orchestration, the
programmatically-checkable parts were run and recorded; the human-only live
UAT is deferred — mirroring the Phases 1-7 `human_needed` / `passed_partial`
pattern (03-08, 04-09, 06-11, 07-09) and the 08-CONTEXT.md `## Deferred Ideas`
double gate (the gateway itself is not deployed — build-gateway is blocked on
Phase 6 emergency-pod integration tests).

**Programmatic checks run + verified (all PASS):**

- Both artifacts exist and are committed (`a9e2f3f`, `98aac60`).
- `RUNBOOK-CLIENT-INTEGRATION.md` contains `ROLLBACK`, the `<5-min` budget,
  `converseai-v4-dev`, `campanhas-chatifix`, and the `Required Env Vars` /
  `BASE_URL` content (Task 1 `<verify>` chain).
- `08-HUMAN-UAT.md` contains `Sign-off`, `Prerequisites`, `passed_partial`,
  ≥4 `UAT-` blocks, and references both `smoke-converseai` + `smoke-chat-ifix`
  (Task 2 `<verify>` chain).
- key_links resolve: the HUMAN-UAT references `provision-tenants` +
  `smoke-converseai` + `smoke-chat-ifix`; the runbook references
  `converseai-v4-dev` + `campanhas-chatifix` + `Portainer` + `BASE_URL`.
- All referenced upstream artifacts exist on disk:
  `provision-tenants.sh`, `smoke-converseai.py`, `smoke-chat-ifix.py`,
  `fixtures/whatsapp-sample.ogg`, `fixtures/whatsapp-sample.baseline.json`.

**Human-only, deferred (cannot run autonomously):**

- Confirm the gateway is deployed to the `ai-gateway-dev` Portainer stack
  (currently blocked on Phase 6 emerg integration tests).
- Run `provision-tenants.sh --mint-keys` once + capture the three raw keys.
- Switch the `base_url`/`api_key` env vars in the `converseai-v4` (`apps/api`
  + `agents/`) and `campanhas-chatifix` sibling repos' deploy configs.
- Execute UAT-1..UAT-4 against the live deployed gateway + record the
  sign-off table + the overall phase-status line.
- Re-measure `whatsapp-sample.baseline.json` `baseline_latency_s` against
  the real direct integration before UAT-2.

The `08-HUMAN-UAT.md` `final_status` stays `pending` until an operator runs
the live scenarios. Per the autonomous-checkpoint-handling instruction, this
plan returns `PLAN COMPLETE` (not a checkpoint signal) — the live UAT is
tracked as a deferred item, consistent with every prior phase's live-UAT
deferral.

## Deviations from Plan

None — plan executed exactly as written. Tasks 1-2 (the doc-writing auto
tasks) were executed and committed atomically; Task 3 (the blocking
checkpoint) was handled as a deferred human-verify per the autonomous
orchestration instruction, with all programmatically-checkable parts run and
recorded above.

The plan's `<output>` block also names `08-VERIFICATION.md` — that artifact
is produced by the phase verifier / the operator after the live UAT sign-off
(it records the SC1-SC4 pass/fail evidence), not by this executor; it is
correctly absent until the deferred live UAT runs, mirroring the
`NN-VERIFICATION.md` timing in every prior phase.

## Threat Model Coverage

- **T-08-14 (Information Disclosure — keys leaking in the docs):** mitigated.
  `RUNBOOK-CLIENT-INTEGRATION.md` and `08-HUMAN-UAT.md` reference env var
  NAMES and the `provision-tenants.sh --mint-keys` invocation only — never
  raw key VALUES. The Required Env Vars tables list the var names + the
  *description* of the value ("the `converseai` tenant key"), not literals.
  Mirrors 07-09 threat T-07-33.
- **T-08-15 (Repudiation — no attributable record of the live UAT):**
  mitigated. `08-HUMAN-UAT.md` has a Sign-off table recording Result / Date
  / Operator per scenario + `operator` / `date_executed` frontmatter fields.
- **T-08-16 (Tampering — a half-switched client app after rollback):**
  mitigated. The runbook ROLLBACK procedure is per-app AND per-consumer (it
  explicitly covers both the ConverseAI v4 `apps/api` AND the `agents/`
  LangChain consumer), and UAT-3 drills the full procedure with both
  `audit_log` verify steps — a half-switched state shows non-zero rows and
  is caught before the stopwatch stops. Symptom 5 in the runbook documents
  the half-switched trap directly.
- **T-08-17 (Spoofing — a client app pointed at a wrong gateway URL):**
  accepted per plan. The operator sets the gateway URL; UAT-1/UAT-2 smoke
  failures + the UAT-4 dashboard cross-check (no traffic under the expected
  tenant) catch a wrong host. No new mitigation in scope.
- **T-08-18 (Elevation of Privilege — a normal-class key used where
  sensitive is required):** accepted per plan. Both Phase 8 tenants are
  `data_class normal`; sensitive-class tenants are Phase 9 behind LGPD
  review. No sensitive data flows through the Phase 8 integrations.

## Known Stubs

None in the artifacts produced by this plan. The `08-HUMAN-UAT.md` is a
scenario sheet with intentional operator-fill blanks (`operator`,
`date_executed`, the Sign-off table cells, `measured_rollback_time`) — those
are the designed shape of a HUMAN-UAT sheet, not stubs.

One pre-existing documented placeholder is referenced (not introduced) by
this plan: `whatsapp-sample.baseline.json` `baseline_latency_s` is a
conservative placeholder from plan 08-03 — the runbook (Symptom 4 caveat) and
`08-HUMAN-UAT.md` (UAT-2 prerequisite) both name the re-measurement as a
required HUMAN-UAT step, which is exactly the resolution path 08-03's SUMMARY
designated.

## Self-Check: PASSED

- FOUND: gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md
- FOUND: .planning/phases/08-client-integration-converseai-chat-ifix/08-HUMAN-UAT.md
- FOUND: commit a9e2f3f (Task 1)
- FOUND: commit 98aac60 (Task 2)
