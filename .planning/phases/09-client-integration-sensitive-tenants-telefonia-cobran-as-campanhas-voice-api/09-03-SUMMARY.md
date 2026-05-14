---
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
plan: 03
subsystem: docs
tags: [runbook, lgpd, compliance, sensitive-tenants, rollback]
requires:
  - RUNBOOK-CLIENT-INTEGRATION.md (Phase 8 skeleton)
  - RUNBOOK-FAILOVER.md (Symptom 3 cross-reference)
  - RES-08 sensitive-block mechanism (Phase 3)
provides:
  - gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md
  - gateway/docs/LGPD-SUBPROCESSORS.md
  - gateway/docs/LGPD-REVIEW-CHECKLIST.md
affects:
  - 09-HUMAN-UAT.md (drills the 4 rollback procedures; carries the LGPD sign-off checkpoint)
tech-stack:
  added: []
  patterns: [RUNBOOK section skeleton, 08-HUMAN-UAT checkbox idiom]
key-files:
  created:
    - gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md
    - gateway/docs/LGPD-SUBPROCESSORS.md
    - gateway/docs/LGPD-REVIEW-CHECKLIST.md
  modified: []
decisions:
  - "Rollback verify step is a psql audit_log row-count that must reach 0 — catches half-switched apps"
  - "Sensitive-tenant 503s during an outage documented as expected RES-08 behavior, not a bug"
  - "LGPD legal sign-off is an external gate; the checklist is the artifact, the signature is captured in 09-HUMAN-UAT"
metrics:
  duration: ~15min
  completed: 2026-05-14
---

# Phase 9 Plan 03: Sensitive-Tenant Runbook + LGPD Docs Summary

Delivered the three Phase-9 documentation artifacts — the sensitive-tenant client-integration runbook with 4 per-app <5-min rollback procedures, and the two LGPD compliance docs (sub-processor disclosure + review checklist) — covering the SC4 rollback-playbook and LGPD-review artifacts for INT-03, INT-04, INT-05.

## What Was Built

### Task 1 — `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` (commit e233f42)
Mirrors the Phase-8 `RUNBOOK-CLIENT-INTEGRATION.md` section skeleton (Read-this-when → Mental Model → Quick Diagnosis → Symptom blocks → ROLLBACK → Required Env Vars → Sensitive-tenant notes → Escalation → Related Docs):
- **Mental Model** documents the env-var contract + the data-class split: `telefonia` + `cobrancas` are `sensitive` (never fail over external), `campanhas` + `voice-api` are `normal` (can fail over). Includes an ASCII diagram of the two paths and the voice-api TTS-stays-local scope note.
- **Quick Diagnosis** has a per-tenant curl block, an `audit_log` per-slug query, a dedicated `upstream='blocked_sensitive'` sensitive-block check, and a `smoke-sensitive-failover.py` invocation.
- **Symptom 3** documents that sensitive-tenant 503s during an upstream outage are EXPECTED RES-08 behavior — cross-references `RUNBOOK-FAILOVER.md` Symptom 3; the mitigation is to restore the local upstream, not to "fix" the 503.
- **ROLLBACK** has exactly 4 `### To roll back` subsections (telefonia, cobrancas, campanhas, voice-api), each an env-var-revert + redeploy + psql `audit_log` row-count verify (must reach 0), with the <5-min-per-app budget + must-be-drilled requirement stated.
- **Sensitive-tenant notes** documents the `chk_sensitive_no_peak` triple-defense (sensitive tenants cannot run in peak mode) and that sensitive content is never persisted to `audit_log_content` (D-B2).

### Task 2 — `gateway/docs/LGPD-SUBPROCESSORS.md` + `gateway/docs/LGPD-REVIEW-CHECKLIST.md` (commit 3163137)
Both use the RUNBOOK markdown shell (dated `#` title, `##` sections).
- **LGPD-SUBPROCESSORS.md** — a `## Sub-processadores` table explicitly listing **OpenAI**, **OpenRouter**, **Vast.ai** with each one's role + data class it can receive; a `## Tenant data-class mapping` table for all 4 tenants; an explicit never-external guarantee for `sensitive` tenants; a `## Mechanism` section describing the RES-08 enforcement path.
- **LGPD-REVIEW-CHECKLIST.md** — a `## Checklist` of `- [ ]` items gating sensitive-tenant production activation (sub-processor disclosure reviewed, base legal documented, `smoke-sensitive-failover.py` pass, privacy-policy disclosure, retention posture, rollback drill) and a `## Sign-off` table for the Ifix legal reviewer.

## Deviations from Plan

None - plan executed exactly as written.

## Threat Model Coverage

All five `mitigate`-disposition threats from the plan's threat register are addressed:
- **T-09-11** (key-value disclosure): all three docs reference env var NAMES + the seed-script invocation only — no raw key values. The `> CONFIRM:` notes name env vars to verify, not secrets to embed.
- **T-09-12** (half-switched app): each of the 4 rollback subsections ends with a psql `audit_log` row-count verify that must reach 0.
- **T-09-13** (LGPD review repudiation): `LGPD-REVIEW-CHECKLIST.md` has a `## Sign-off` table (Reviewer/Role/Date/approval-reference) — the signed checklist is the attributable evidence.
- **T-09-14** (under-disclosure): `LGPD-SUBPROCESSORS.md` is built from the actual upstream topology with a per-tenant data-class mapping table that forces an explicit statement per tenant.
- **T-09-15** (wrong gateway URL): accepted — same posture as Phase 8; docs-only plan adds no new mitigation.

## Known Stubs

None — all three docs are complete content. The `> CONFIRM:` notes on env-var names are intentional operator-action markers (the 4 client repos are siblings not editable by this phase), not stubs.

## Self-Check: PASSED

- FOUND: gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md
- FOUND: gateway/docs/LGPD-SUBPROCESSORS.md
- FOUND: gateway/docs/LGPD-REVIEW-CHECKLIST.md
- FOUND: commit e233f42 (Task 1)
- FOUND: commit 3163137 (Task 2)
