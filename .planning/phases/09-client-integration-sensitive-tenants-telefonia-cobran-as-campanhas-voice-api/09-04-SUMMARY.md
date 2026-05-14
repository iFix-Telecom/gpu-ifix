---
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
plan: 04
subsystem: human-uat / phase-closing verification
tags: [human-uat, sensitive-tenants, lgpd, rollback-drill, res-08, deferred-gate]
requires:
  - .planning/phases/08-client-integration-converseai-chat-ifix/08-HUMAN-UAT.md (structural template)
  - scripts/integration-smoke/provision-tenants.sh (09-01 — seed script the UAT runs)
  - scripts/integration-smoke/smoke-sensitive-failover.py (09-02 — the RES-08 smoke UAT-1/UAT-2 invoke)
  - gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md (09-03 — the 4 ROLLBACK procedures UAT-4 drills)
  - gateway/docs/LGPD-REVIEW-CHECKLIST.md (09-03 — the checklist the Final Sign-off attaches)
provides:
  - .planning/phases/09-.../09-HUMAN-UAT.md — live-verification scenario sheet for SC1-SC4 + blocking LGPD legal sign-off
affects:
  - Phase 9 completion gate (autonomous: false — cannot reach COMPLETE without operator action + external legal sign-off)
  - Phase 10 / PRD-05 (carries the LGPD-review-documented-before-go-live evidence)
tech-stack:
  added: []
  patterns:
    - HUMAN-UAT deferred-gate pattern (mirrors 03-08 / 04-09 / 06-11 / 07-09 / 08-04)
    - blocking external-gate checkpoint (LGPD legal sign-off as an attributable Sign-off table)
key-files:
  created:
    - .planning/phases/09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api/09-HUMAN-UAT.md
    - .planning/phases/09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api/09-VERIFICATION.md
  modified: []
decisions:
  - "Task 2 (checkpoint:human-verify, gate=blocking) executed under /gsd-autonomous: programmatic parts verified, the live-UAT + external LGPD legal sign-off recorded as a deferred human-verify item — not blocked waiting for a human"
  - "UAT-1 + UAT-2 both invoke smoke-sensitive-failover.py with --pg-dsn against a data_class=sensitive key (telefonia, then cobrancas) — the cobrancas re-run gives cobrancas its own never-external proof"
  - "UAT-4 drills all 4 RUNBOOK ROLLBACK procedures with a per-app measured-time field — a single shared stopwatch would not satisfy the <5-min-per-app SC4 bar"
metrics:
  duration: ~20min
  completed: 2026-05-14
  tasks: 2
  files: 2
---

# Phase 9 Plan 04: Sensitive-Tenant HUMAN-UAT Scenario Sheet Summary

The phase-closing HUMAN-UAT plan — wrote the live-credential / live-traffic /
external-sign-off verification sheet for Phase 9's four sensitive/normal client
integrations (Telefonia, Cobranças, Campanhas, voice-api), mirroring the
08-04 / 07-09 / 06-11 / 04-09 / 03-08 deferred-UAT pattern, and gated the live
integration behind a blocking LGPD legal sign-off checkpoint. The autonomous
Phase 9 plans (09-01..09-03) ship the gpu-ifix-side artifacts and are green;
this plan adds the operator-executed evidence sheet that cannot run
autonomously.

## What Was Built

### Task 1 — `09-HUMAN-UAT.md` (commit `e4d45bc`)

Created `.planning/phases/09-.../09-HUMAN-UAT.md` mirroring `08-HUMAN-UAT.md`:

- **YAML frontmatter** — `status: pending`, `phase`, `source: [09-CONTEXT.md,
  09-04-PLAN.md Task 1, ROADMAP Phase 9 SC1-SC4]`, blank
  `started`/`updated`/`operator`/`date_executed`, `final_status: pending`.
- **Intro** — the live-integration framing + the "autonomous build NOT blocked"
  paragraph + the explicit **double gate** statement (gateway not deployed yet
  — blocked on Phase 6 emerg integration tests; LGPD legal sign-off is an
  external gate).
- **`## Prerequisites`** — `- [ ]` checkbox list: the gateway-deployed gate
  (currently Phase-6-emerg-blocked), the `provision-tenants.sh --mint-keys`
  run + the 5 keys captured, the per-app env switch for **all 4** repos
  (`fallback-register-ramais-nextbilling`, `cobrancas-api`,
  `campanhas-chatifix`, `voice-api`), the LGPD-checklist-worked-through-and-
  submitted item, `requirements.txt` install (incl. `psycopg`), and
  `AI_GATEWAY_PG_DSN` available for the smoke's audit gates.
- **`## Tests`** — 4 numbered `### N. UAT-N — <name> (SC-N)` blocks, each with
  Pre-conditions / Steps (fenced bash) / Expected / Pass-Fail:
  - **UAT-1 (SC1)** — Telefonia sensitive-failover smoke: switch the
    `fallback-register-ramais-nextbilling` STT config, trip the tier-0 breaker
    per the smoke's operator pre-step, run
    `smoke-sensitive-failover.py --gateway-url ... --api-key <telefonia
    sensitive key> --pg-dsn ... --out ...`; assert exit 0 +
    `gates.all_passed == true` (fail_closed 503 +
    `upstream_unavailable_for_sensitive_tenant` + `Retry-After: 30`;
    never_external `audit_log upstream='blocked_sensitive'`; audit_decision row
    found + zero `audit_log_content` rows).
  - **UAT-2 (SC2)** — Cobranças + Campanhas quotas + cost: switch both backends,
    drive LLM-personalization + embedding traffic, confirm the per-tenant
    quotas (2M/120rpm cobrancas, 5M/300rpm campanhas) are enforced and the
    Phase 7 dashboard / `/admin/usage` reports cost-per-request per tenant;
    re-runs the sensitive smoke with the **cobrancas** key for cobrancas'
    never-external proof.
  - **UAT-3 (SC3)** — voice-api LLM-via-gateway: switch the LLM
    script-generation config only, trigger a script-generation call, confirm it
    routes through the gateway (audit_log row under `voice-api`) AND TTS stays
    on local CPU.
  - **UAT-4 (SC4)** — per-app rollback drill: drill each of the 4
    `### To roll back` procedures in `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`,
    each timed with its own `measured_rollback_time_*` field, each verified via
    the `psql` `audit_log` row-count reaching 0; <5 min per app.
- **`## Summary`** / **`## Sign-off`** — a 4-row table (Result / Date / Operator
  / Notes) + an overall phase-status line (`passed` / `passed_partial` /
  `human_needed`).
- **`## Final Sign-off — LGPD legal review (BLOCKING, external gate)`** — a
  dedicated section: the operator attaches the Ifix-legal-signed
  `LGPD-REVIEW-CHECKLIST.md`, with a Reviewer/Role/Date/approval-reference
  table; states explicitly that sensitive tenants (telefonia, cobrancas) MUST
  NOT be activated in production until the signature exists (ROADMAP SC4 /
  PRD-05 gate).
- **`## passed_partial fallback`** — copied the `08-HUMAN-UAT.md` shape: if the
  gateway is not deployed OR the LGPD sign-off is pending, the affected
  scenarios are `passed_partial`, blocker in the Notes column; the autonomous
  build (09-01..09-03) is green and not blocked. A real defect → `human_needed`
  + describe in Gaps.
- **`## Gaps`** — empty section for the operator.

The plan's automated verification for Task 1 passed (file exists; `Sign-off`,
`Prerequisites`, `passed_partial`, `final_status`, `smoke-sensitive-failover`,
`provision-tenants`, `LGPD`, `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE`,
`telefonia`, `voice-api` all present; ≥4 `UAT-` occurrences).

### Task 2 — checkpoint:human-verify (gate=blocking) — executed as a deferred human-verify

Task 2 is the live-credential / live-traffic / external-sign-off checkpoint.
Running under `/gsd-autonomous`, the programmatically-checkable parts were run
and the human-only parts recorded as deferred (see "Deferred / Human-Verify"
below) — mirroring the Phases 1-8 `human_needed` deferral pattern (08-04 /
07-09 / 06-11).

Programmatic verification run for Task 2:
- `09-HUMAN-UAT.md` exists and is committed (`e4d45bc`).
- Every Phase 9 artifact the UAT consumes exists in the worktree:
  `provision-tenants.sh`, `smoke-sensitive-failover.py` +
  `sensitive-failover-report-schema.json`,
  `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`, `LGPD-SUBPROCESSORS.md`,
  `LGPD-REVIEW-CHECKLIST.md`.
- The 09-01/09-02/09-03 commits the UAT references all exist
  (`25ad726`, `1066489`, `7ce79fc`, `e233f42`, `3163137`).
- `09-VERIFICATION.md` written recording the programmatic results + the
  deferred live-UAT scope.

## Deferred / Human-Verify

Task 2's live execution is **deferred** — it requires a human operator, real
client-app credentials, a deployed gateway, and an external legal signature.
This is the established per-phase deferred-gate pattern (03-08, 04-09, 06-11,
07-09, 08-04).

**Deferred to a human operator:**
1. **The live UAT-1..UAT-4 execution** — gated on the gateway being deployed to
   the `ai-gateway-dev` Portainer stack (currently blocked on Phase 6
   emergency-pod integration tests, a separate debug session) AND the operator
   running `provision-tenants.sh --mint-keys` AND switching the `base_url`/
   `api_key` env vars in all 4 client sibling repos
   (`fallback-register-ramais-nextbilling`, `cobrancas-api`,
   `campanhas-chatifix`, `voice-api`). Until the gateway is deployed, all 4
   UATs are `passed_partial`.
2. **The external LGPD legal sign-off** — Ifix legal must work through and sign
   the `## Sign-off` table in `gateway/docs/LGPD-REVIEW-CHECKLIST.md`; the
   operator attaches the signed copy to the `## Final Sign-off` section of
   `09-HUMAN-UAT.md`. Sensitive tenants (telefonia, cobrancas) MUST NOT be
   activated in production until this signature exists — ROADMAP Phase 9 SC4 /
   PRD-05.

**Resume signal (from the plan):** the operator types "approved" with the
Sign-off table filled and the LGPD legal sign-off recorded, or describes the
defects found for a `/gsd-plan-phase --gaps` pass.

**Not blocked:** the autonomous Phase 9 build (09-01..09-03) is green — the
gpu-ifix-side scripts + docs all ship and are verified. Only the live,
deployed-gateway run + the external legal signature are deferred.

## Deviations from Plan

None — plan executed as written. Task 2's blocking checkpoint was handled per
the `/gsd-autonomous` deferred-human-verify protocol (the autonomous
orchestration does not block waiting for a human): the programmatic parts were
verified and the human-only live-UAT + external LGPD legal sign-off were
recorded as a deferred item, consistent with the Phases 1-8 `human_needed`
pattern.

## Threat Model Coverage

All five `mitigate`-disposition threats from the plan's `<threat_model>` are
addressed by `09-HUMAN-UAT.md`:

- **T-09-16 (Information Disclosure — keys in the sheet):** the sheet references
  env var NAMES + the `provision-tenants.sh --mint-keys` invocation only — never
  raw key VALUES. The Prerequisites instruct the operator to capture the keys
  from the script's stdout and paste them straight into the client-app deploy
  configs.
- **T-09-17 (Repudiation — unattributable LGPD review):** the `## Final Sign-off`
  section is a BLOCKING external gate with a Reviewer/Role/Date/approval-
  reference table; the per-UAT Sign-off table records Operator + Date; the
  frontmatter carries `operator` + `date_executed`.
- **T-09-18 (Tampering — half-switched rollback):** UAT-4 drills all 4
  per-app ROLLBACK procedures end-to-end, each with the `psql` `audit_log`
  row-count verify that must reach 0 — a half-switched state is caught before
  the per-app stopwatch stops.
- **T-09-19 (EoP — sensitive key minted `normal` by mistake):** UAT-1 (telefonia)
  and the UAT-2 cobrancas re-run both run `smoke-sensitive-failover.py` whose
  `never_external` + `audit_decision` gates assert
  `audit_log upstream='blocked_sensitive'` — a key minted `normal` by mistake
  fails those gates.
- **T-09-20 (Spoofing — wrong gateway URL):** accepted — same posture as
  Phase 8; a HUMAN-UAT plan adds no new mitigation, and a wrong host is caught
  by the per-app UAT smokes.

## Known Stubs

None — `09-HUMAN-UAT.md` is a complete operator-execution sheet. The blank
`_____` fields (operator, date, results, measured rollback times) and the
empty Sign-off / Final Sign-off / Gaps tables are intentional operator-fill
fields, not stubs — the same shape as `08-HUMAN-UAT.md`.

## Self-Check: PASSED

- `.planning/phases/09-.../09-HUMAN-UAT.md` — FOUND
- `.planning/phases/09-.../09-VERIFICATION.md` — FOUND
- `.planning/phases/09-.../09-04-SUMMARY.md` — FOUND
- commit `e4d45bc` (Task 1) — FOUND
- Phase 9 artifacts the UAT consumes (provision-tenants.sh,
  smoke-sensitive-failover.py + schema, RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md,
  LGPD-SUBPROCESSORS.md, LGPD-REVIEW-CHECKLIST.md) — all FOUND
- 09-01/09-02/09-03 commits (25ad726, 1066489, 7ce79fc, e233f42, 3163137) —
  all FOUND
