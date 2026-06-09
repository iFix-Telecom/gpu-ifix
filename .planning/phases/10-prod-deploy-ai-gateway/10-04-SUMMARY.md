---
phase: 10-prod-deploy-ai-gateway
plan: 04
subsystem: docs + planning-state
tags: [phase-10, prod-deploy, runbook, requirements-remap, phase-11-placeholder, d-16, d-18, prd-04-partial]
dependency_graph:
  requires:
    - 10-CONTEXT.md (D-16 split, D-18 PRD-04 partial)
    - 10-RESEARCH.md (§How To #8 RUNBOOK structure)
    - 10-PATTERNS.md (Pattern 4 RUNBOOK family analog)
    - gateway/docs/RUNBOOK-FAILOVER.md (analog for header convention)
    - gateway/docs/RUNBOOK-PRIMARY-POD.md (analog for header convention)
    - scripts/deploy/*.sh (4 deploy scripts referenced step-by-step in RUNBOOK)
    - .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml (edge Traefik route — referenced in Step 1)
  provides:
    - gateway/docs/RUNBOOK-DEPLOY.md (PRD-04 partial — operator deploy + cut-release playbook)
    - .planning/REQUIREMENTS.md §Traceability (D-16 remap PRD-01/02/03/05/06 → Phase 11; PRD-04 split partial/full)
    - .planning/ROADMAP.md (Phase 10 req line aligned with verify gate; Phase 11 placeholder already in place from prior plan-phase work)
  affects:
    - Plan 10-06 HUMAN-UAT (operator reads RUNBOOK-DEPLOY.md during the live deploy)
    - Phase 11 planning (Phase 11 placeholder + 5 deferred PRD IDs are now first-class entries in ROADMAP.md + REQUIREMENTS.md)
tech-stack:
  added: []
  patterns:
    - "Operator-runbook header convention (Triggers/Preconditions/Steps/Verification/Rollback) — mirror Phase 3/6/7 runbook structure"
    - "Idempotent docs-only edits — Task 3 honored the precheck NO-OP for populate+append; only the verify-gate alignment required edit"
    - "Pitfall index cross-reference inline citation — Pitfalls 1-8 cited by ID at the relevant step"
key-files:
  created:
    - gateway/docs/RUNBOOK-DEPLOY.md
  modified:
    - .planning/REQUIREMENTS.md
    - .planning/ROADMAP.md
decisions:
  - "D-16 (split): PRD-01, PRD-02, PRD-03, PRD-05, PRD-06 remapped from Phase 10 → Phase 11: prod-hardening — fully reflected in REQUIREMENTS.md §Traceability + ROADMAP.md Phase 11 placeholder."
  - "D-18 (PRD-04 partial): Phase 10 ships RUNBOOK-DEPLOY.md only (the deploy playbook), full incident-response runbook deferred to Phase 11. REQUIREMENTS.md §Traceability now has TWO PRD-04 rows reflecting this split."
  - "Rule-3 fix during execution: ROADMAP.md Phase 10 Requirements line trimmed from verbose `PRD-04 (partial — RUNBOOK-DEPLOY.md only per D-18)` → terse `PRD-04 (partial)` to satisfy the verify-gate literal-substring assertion. Verbose qualifier preserved (a) as an HTML comment immediately below the line + (b) as the Status column in REQUIREMENTS.md §Traceability. Source-of-truth invariant maintained: REQUIREMENTS.md is the canonical qualifier home; ROADMAP.md gives a summary."
metrics:
  duration_minutes: ~25
  tasks_completed: 3
  files_created: 1
  files_modified: 2
  commits: 3
  lines_added: 484
  lines_removed: 11
completed: 2026-05-26
---

# Phase 10 Plan 04: RUNBOOK-DEPLOY + REQUIREMENTS remap + ROADMAP Phase 11 placeholder Summary

**One-liner:** PRD-04 (partial) satisfied — operator-facing `gateway/docs/RUNBOOK-DEPLOY.md` shipped (471 lines, 5 standard headers, references all 4 `scripts/deploy/*.sh` step-by-step) — and the D-16 requirements split is now fully reflected in both `.planning/REQUIREMENTS.md` and `.planning/ROADMAP.md`.

## What Was Built

### Task 1 — `gateway/docs/RUNBOOK-DEPLOY.md` (NEW, 471 lines, commit `92f85ba`)

Operator-facing runbook structured to mirror the existing Phase 3 + Phase 6 + Phase 7 runbooks. Five standard `##` headers per the verify-gate contract: **Triggers / Preconditions / Steps / Verification / Rollback**. Sub-procedures live under `### Steps — ...` so the file stays scannable without exploding the top-level outline.

Content:

- **Triggers** — 4 entry points: first-time bring-up, roll-forward `:v1.0.X`, rollback to previous tag, cut-release procedure.
- **Preconditions** — 8 verifiable checks (SSH aliases, `~/.claude/CLAUDE.md` open, DO Postgres + Sentry + GH PAT access, GHA build green, dev stack still running for rollback).
- **Steps — First-Time Bring-Up** — 7 sub-steps each invoking a Wave 0/1 deploy script:
  1. `scripts/deploy/preflight.sh` + scp compose/env to `n8n-ia-vm:/opt/ai-gateway-prod/` + scp edge route to `vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/`. **Pitfall 3 reminder: DNS comes LATER in Step 6.**
  2. `scripts/deploy/bootstrap-postgres.sh` creates `bd_ai_gateway_prod` + `bd_ai_dashboard_prod`. **Pitfall 2 reminder: hardcoded schema name `ai_gateway` → isolate by DB name.**
  3. First `docker compose up -d` with `AI_GATEWAY_MIGRATE_ON_BOOT=true` — tails goose-applied log + runs `/gateway --self-check` + verifies internal-Traefik discovery.
  4. Flip `MIGRATE_ON_BOOT=false` + force-recreate. **Pitfall 8 reminder.**
  5. `scripts/deploy/migrate-dashboard.sh` runs Better Auth `cli migrate` against `bd_ai_dashboard_prod` via the prod dashboard image (T-10-02-05: version-pin via Docker run).
  6. `scripts/deploy/cf-dns-create.sh` POSTs the 2 A records → first `:443` triggers TLS-ALPN-01 challenge → verify `acme.json` post-issuance. **Pitfalls 3 + 4 reminders.**
  7. `provision-tenants.sh` (idempotent) + `--mint-keys` (run-once) against prod DSN; 6 destination apps + 1 admin key documented in a per-tenant table.
- **Steps — Roll-Forward** — sed image tag + `docker compose pull && up -d`; verify boot log shows new `build_version`; Sentry releases tab gate.
- **Steps — Rollback** — sed reverse + `--force-recreate`; **INT-06 timed gate: < 5 min** end-to-end; schema-down caveat (CHANGELOG flag "rollback-safe?").
- **Steps — Cut-Release Procedure** — `develop` → `main` `--ff-only` + signed `git tag -a v1.0.0` + `git push origin v1.0.0` + `gh run watch`. D-11 note: prod webhook secret intentionally UNSET; operator deploys manually.
- **Verification** — 7-item post-deploy checklist (`/health`, `/v1/health/upstreams` 6 upstreams, `gatewayctl upstreams list`, Sentry releases, dashboard `:443`, 4 smoke reports, audit log row count).
- **Rollback (escape hatch)** — 3 tiers: (1) **DNS panic-revert** via Cloudflare API DELETE (TTL 300s ~5 min); (2) **container-only revert** via image-tag swap; (3) **full revert** by flipping client `gateway_base_url` back to `https://gateway-dev.ifixtelecom.com.br` (the dev stack at `/opt/ai-gateway-dev/` on `vps-ifix-vm` is deliberately kept running per Phase 10 cutover policy).
- **Postmortem stub** — template only (Date / Trigger / Detection / Mitigation / Recovery / Action items).
- **Pitfall Index** — table cross-referencing Pitfalls 1-8 to the specific Step that mitigates each.

### Task 2 — `.planning/REQUIREMENTS.md` §Traceability remap (MODIFIED, commit `5782023`)

The §Traceability table previously mapped all 7 Phase 10 IDs (`INT-06, PRD-01..PRD-07`) to "Phase 10: Production Hardening & GA". After this edit:

| ID | Old phase | New phase | Reason |
|----|-----------|-----------|--------|
| INT-06 | Phase 10: Production Hardening & GA | Phase 10: **prod-deploy-ai-gateway** | Stays on Phase 10. Phase slug now matches the actual phase directory. |
| PRD-01 | Phase 10: Production Hardening & GA | **Phase 11: prod-hardening** | Load test — D-16 move. |
| PRD-02 | Phase 10: Production Hardening & GA | **Phase 11: prod-hardening** | Chaos test — D-16 move. |
| PRD-03 | Phase 10: Production Hardening & GA | **Phase 11: prod-hardening** | Chaos test — D-16 move. |
| PRD-04 | Phase 10: Production Hardening & GA | **Phase 10 (partial) + Phase 11 (full)** | D-18 split: Phase 10 ships RUNBOOK-DEPLOY.md, Phase 11 ships full incident runbook. |
| PRD-05 | Phase 10: Production Hardening & GA | **Phase 11: prod-hardening** | LGPD legal sign-off — D-16 move. |
| PRD-06 | Phase 10: Production Hardening & GA | **Phase 11: prod-hardening** | Dashboard SSO hardening — D-16 move. |
| PRD-07 | Phase 10: Production Hardening & GA | Phase 10: **prod-deploy-ai-gateway** | Stays on Phase 10. Phase slug rename. |

Audit footer added immediately below the table: `<!-- 2026-05-26: Phase 10 plan-phase per D-16 split PRD-01/02/03/05/06 from Phase 10 → Phase 11; PRD-04 split into partial (Phase 10 RUNBOOK-DEPLOY.md) + full (Phase 11 incident runbook). -->` — git-blame trail preserved.

Coverage block updated to reflect that PRD-04 maps to two phases (still counted once in the 70 v1 reqs total).

### Task 3 — `.planning/ROADMAP.md` Phase 10 Requirements alignment (MODIFIED, commit `c5b65ce`)

Per the orchestrator's idempotency precheck, ROADMAP.md was already at target state for Phase 10 goal populated + Phase 11 placeholder appended (both done during the earlier plan-phase work). The precheck returned NO-OP for populate+append.

However the Task 3 verify gate asserts the literal substring `INT-06, PRD-04 (partial), PRD-07` in the Phase 10 Requirements line, and the current text was the more verbose `PRD-04 (partial — RUNBOOK-DEPLOY.md only per D-18)` — a superset that failed the literal grep. Per Rule 3 (auto-fix blocking issue), the line was trimmed to the gate-required terse form, with the verbose qualifier preserved as:

1. An HTML comment immediately below the line in ROADMAP.md.
2. The Status column text in REQUIREMENTS.md §Traceability (`Pending — RUNBOOK-DEPLOY.md only`).

Source-of-truth invariant maintained: REQUIREMENTS.md is the canonical home of the qualifier; ROADMAP.md gives a summary that satisfies the verify gate.

## Verification Results

End-of-plan gate (all 4 checks GREEN):

| Gate | Check | Result |
|------|-------|--------|
| Task 1 | `RUNBOOK-DEPLOY.md` exists; ≥5 standard `##` headers; ≥200 lines; all 4 `scripts/deploy/*.sh` referenced | PASS (headers=5, lines=471) |
| Task 1 | `ai-gateway-prod.yml` referenced; `v1.0.0` literal; `bd_ai_gateway_prod` literal | PASS |
| Task 2 | Exactly 5 rows map PRD-0{1,2,3,5,6} → Phase 11; PRD-04 split row present in both phases; INT-06 + PRD-07 on Phase 10; audit footer dated 2026-05-26 cites D-16 | PASS |
| Task 3 | Phase 10 goal no longer `[To be planned]`; Phase 11 placeholder present with the 6 deferred PRD IDs; 10-01..10-06 plan list bookends; `INT-06, PRD-04 (partial), PRD-07` substring present | PASS |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking Issue] Task 3 verify-gate literal-substring mismatch**

- **Found during:** Task 3 (after idempotency precheck returned NO-OP for populate+append).
- **Issue:** The ROADMAP.md Phase 10 Requirements line was already populated as `INT-06, PRD-04 (partial — RUNBOOK-DEPLOY.md only per D-18), PRD-07` (verbose qualifier inside the parentheses). The verify gate `grep -q 'INT-06, PRD-04 (partial), PRD-07' .planning/ROADMAP.md` requires the literal substring `PRD-04 (partial), PRD-07` — the verbose form is a superset and fails as a literal substring.
- **Fix:** Trimmed the parenthetical to `(partial)`. Moved the verbose qualifier ("RUNBOOK-DEPLOY.md only per D-18; full incident runbook deferred to Phase 11. See REQUIREMENTS.md §Traceability for the partial/full split.") into an HTML comment on the line below. Verify gate now GREEN. Semantic content preserved: the verbose qualifier is in REQUIREMENTS.md Status column + ROADMAP.md HTML comment.
- **Files modified:** `.planning/ROADMAP.md` (+2 lines, -1 line).
- **Commit:** `c5b65ce`.
- **Rationale:** the orchestrator's objective explicitly said "the `<verify><automated>` step still asserts both anchors and will pass" — by current literal-substring contract this was false until the trim. Rule 3 applies (blocking issue prevents task completion). No architectural change.

### Idempotency NO-OP

Task 3's `<action>` block specifies an idempotency precheck: if Phase 10 goal is already populated AND Phase 11 placeholder is already present, skip the populate + append (this work was already done during the earlier plan-phase pass). The precheck returned both anchors present, so populate+append was correctly skipped. Only the Rule-3 verify-gate-alignment fix was applied. Log line written per precheck contract: `Task 3: ROADMAP.md already at target state — skipping populate + append; verify gate is the authoritative check.`

## Authentication Gates

None — all changes were docs-only edits within the repo. No external credentials touched.

## Known Stubs

None — RUNBOOK-DEPLOY.md uses `<PASS>` and `<previous>` as placeholder text inside code fences (as the threat model T-10-04-03 mandates — never include real secret values in the runbook). The Postmortem section is intentionally a template stub awaiting first-incident fill-in.

## Threat Flags

No new threat surface. RUNBOOK-DEPLOY.md is operator-facing documentation; it references existing scripts/secrets but introduces no new network endpoints, auth paths, or schema changes. The threat-model items T-10-04-01..04 (Step ordering, audit trail, secret values in fences, REQUIREMENTS↔ROADMAP drift) are all mitigated per the plan's threat register.

## Commits

| Hash | Type | Message |
|------|------|---------|
| `92f85ba` | docs | add RUNBOOK-DEPLOY.md (PRD-04 partial — DEPLOY only) |
| `5782023` | docs | remap REQUIREMENTS.md §Traceability per D-16 + D-18 |
| `c5b65ce` | docs | align ROADMAP.md Phase 10 Requirements line with verify gate |

## Files Touched

| Path | Status | Net lines |
|------|--------|-----------|
| `gateway/docs/RUNBOOK-DEPLOY.md` | created | +471 |
| `.planning/REQUIREMENTS.md` | modified | +13 / -10 |
| `.planning/ROADMAP.md` | modified | +2 / -1 |

## Next Steps

- **Plan 10-05** (Wave 3, blocked on Wave 2 = this plan) — develop→main promotion + `v1.0.0` tag + GHA build verify. RUNBOOK-DEPLOY.md §Cut-Release Procedure is its operator playbook.
- **Plan 10-06** (Wave 4, blocked on Wave 3) — HUMAN-UAT executes the full RUNBOOK-DEPLOY.md First-Time Bring-Up (Steps 1-7), 8 smoke scenarios S1-S8, S9 per-tenant smokes, S10 rollback drill, S11 Sentry verification, and the 4 cascade-close commits for Phase 02/03/04/05 (RESEARCH §How To #9).
- **Phase 11** (placeholder created) — once Phase 10 closes, `/gsd:plan-phase 11 --research-phase 11` will be the entry point. The 6 deferred PRD IDs are first-class entries in `.planning/REQUIREMENTS.md` + `.planning/ROADMAP.md` and will not be flagged as uncovered by `/gsd:verify-work` against Phase 10.

## Self-Check: PASSED

- File `gateway/docs/RUNBOOK-DEPLOY.md` exists: FOUND
- File `.planning/REQUIREMENTS.md` exists: FOUND (modified)
- File `.planning/ROADMAP.md` exists: FOUND (modified)
- Commit `92f85ba`: FOUND
- Commit `5782023`: FOUND
- Commit `c5b65ce`: FOUND
