---
phase: 10-prod-deploy-ai-gateway
plan: 06
subsystem: human-uat-author
tags: [phase-10, prod-deploy, human-uat, cascade-close, live-validation, sentry-bootstrap]
state: paused_at_checkpoint
checkpoint_type: human-verify
checkpoint_gate: blocking
requirements: [INT-06, PRD-07]
dependency_graph:
  requires:
    - "Wave 0–3 Phase 10 deliverables (10-01..10-05): compose stack file, env contract, 5 deploy scripts, edge Traefik route YAML, RUNBOOK-DEPLOY.md, RELEASE-CHECKLIST.md"
    - "Phase 06.9 close (commit e3be97b) — WARNING-5 positive-assertion grep pattern template"
  provides:
    - "10-HUMAN-UAT.md (operator-driven live UAT playbook with 6 Pre-UAT gates + 11 scenarios + 4 cascade-close commit stanzas + Summary/Sign-off/passed_partial/Gaps)"
  affects:
    - ".planning/phases/02-gateway-core-multi-tenant-auth/02-VERIFICATION.md (cascade close — operator-driven during Task 2 live UAT)"
    - ".planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md (cascade close — operator-driven)"
    - ".planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md (cascade close + status flip passed_partial → passed)"
    - ".planning/phases/05-load-shedding-saturation-aware-routing/05-VERIFICATION.md (cascade close — operator-driven)"
tech-stack:
  added: []
  patterns:
    - "HUMAN-UAT playbook structural mirror (06.9-HUMAN-UAT.md + 09-HUMAN-UAT.md analogs)"
    - "Cascade-close 4-commit recipe with WARNING-5 positive-assertion grep (Phase 06.9 e3be97b template)"
    - "BLOCKING Pre-UAT gates A-F with per-gate sign-off lines (R2 — must all PASS before scenarios)"
    - "Sentry release tagging (release=v1.0.0 environment=production) for D-14 evidence"
    - "Per-tenant golden-path smoke pattern (Phase 09 analog) for INT-06 evidence"
    - "Rollback drill timed pattern (RUNBOOK-DEPLOY.md §Rollback §3) for INT-06 evidence"
key-files:
  created:
    - ".planning/phases/10-prod-deploy-ai-gateway/10-HUMAN-UAT.md"
    - ".planning/phases/10-prod-deploy-ai-gateway/10-06-SUMMARY.md"
  modified: []
decisions:
  - "Plan 10-06 split into two autonomous tasks (1A skeleton + 1B append) + one blocking human-verify checkpoint (Task 2) — keeps each commit small and lets the operator pick up the playbook in two stages if needed"
  - "Cascade-close stanzas use a NEW key `gaps_closed_phase_10_2026_05_XX:` that co-exists with the prior `gaps_closed_2026_05_XX` from Phase 06.9 closeout (per RESEARCH §Pattern 4 — Phase 02 will have TWO entries after Cascade Commit 1)"
  - "Phase 04 cascade requires BOTH a status flip (passed_partial → passed) AND an evidence stanza; the other 3 phases require only the stanza (status is already `passed`)"
  - "S10 + S11 may DEFER under passed_partial fallback — first-release scenario (no previous tag) and Sentry project bootstrapping are common operator-side blockers"
  - "Sensitive tenants in S9 (telefonia + cobrancas) MUST get 503 sensitive_block envelope on external-route probe — RES-08 invariant validation under prod URL"
metrics:
  duration: "~45 min autonomous (Task 1A + Task 1B + SUMMARY); ~2-3 h additional during operator-driven UAT (Task 2 — blocking checkpoint)"
  completed_tasks: "2 / 3 (Task 1A + Task 1B autonomous; Task 2 awaits operator)"
  files_created: 2
  files_modified: 0
  commits: 2  # Task 1A skeleton commit + Task 1B append commit (plus this SUMMARY commit makes 3 total)
  completed_date: "2026-05-26"
---

# Phase 10 Plan 06: HUMAN-UAT + Cascade-Close Author — Summary (PAUSED AT CHECKPOINT)

> **State:** Plan execution paused at Task 2 (blocking human-verify checkpoint). Task 1A + Task 1B autonomous work complete; Task 2 is the operator-driven live UAT and the cascade-close commits land DURING Task 2 (not in this autonomous executor session).

Authoring of the operator-driven live HUMAN-UAT playbook for Phase 10 prod-deploy + 4 cascade-close commit stanzas. The autonomous executor produced the complete 1139-line playbook (Task 1A skeleton + Task 1B append); Task 2 is a BLOCKING checkpoint where the operator runs the playbook end-to-end against the live `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` prod stack (~2-3 h wall time, ~$0.10 LLM spend, $0 Vast/GPU spend).

## Outcome

| Field | Value |
|---|---|
| **Tasks completed (autonomous)** | 2 / 3 — Task 1A + Task 1B |
| **Task awaiting operator** | Task 2 — `checkpoint:human-verify` gate=blocking |
| **Files created** | 2 — `10-HUMAN-UAT.md` (1139 lines) + this `10-06-SUMMARY.md` |
| **Commits this plan (autonomous)** | 3 — Task 1A skeleton (`fdd07be`) + Task 1B append (`85940d8`) + this SUMMARY (final) |
| **Expected operator commits during Task 2** | 4 cascade-close commits (Phase 02 / 03 / 04 / 05) + 1 final HUMAN-UAT result commit |
| **autonomous frontmatter** | `false` (D-19 mandate — real deploy + real CF DNS POST + real https smoke + Sentry project creation cannot be done from an autonomous agent) |
| **Plan dependency wave** | Wave 4 (depends on 10-01..10-05 all shipped) |
| **Cascade targets** | 4 prior phase VERIFICATION.md files: Phase 02 SC-5 step 7, Phase 03 SC-1, Phase 04 SC-1+SC-2+SC-4 (status flip), Phase 05 SC-1 |

## Files Produced

| File | Purpose | Lines |
|---|---|---|
| `.planning/phases/10-prod-deploy-ai-gateway/10-HUMAN-UAT.md` | Operator-driven live UAT playbook — frontmatter + 6 Pre-UAT BLOCKING gates A-F + 11 scenarios S1-S11 + 4 cascade-close commit stanzas + Cleanup (MANDATORY) + Summary + Sign-off + passed_partial fallback + Gaps | 1139 |
| `.planning/phases/10-prod-deploy-ai-gateway/10-06-SUMMARY.md` | This summary documenting plan-paused-at-checkpoint state | (this file) |

## HUMAN-UAT.md Structure

The playbook follows the 06.9-HUMAN-UAT.md analog with Phase 10 adaptations (prod hostname `ai-gateway.converse-ai.app`, 11 scenarios instead of 6, 4 cascade-close commits instead of 3, $0.10 spend cap, $0 Vast/GPU). All sections from the planner spec are present:

1. **Header table** — Phase metadata, wall time 2-3 h, $0.10 LLM cap, $0 Vast/GPU, $2.00 R2 hard abort, OPENROUTER_HTTP_REFERER traceability
2. **REDACTION WARNING** — verbatim mirror of 06.9 (never paste real keys; UAT evidence committed to git)
3. **Pre-UAT Preconditions (BLOCKING gates A-F)** — each gate has its own `[ ] PASS [ ] FAIL · Evidence: ___ · Operator: ___ · Date: ___`:
   - **A — preflight.sh** (egress IP 162.55.92.154 + free RAM ≥ 4 GB + disk ≥ 20 GB + intra net + internal Traefik discovery)
   - **B — bootstrap-postgres.sh** (DO Postgres DBs `bd_ai_gateway_prod` + `bd_ai_dashboard_prod` exist + are empty)
   - **C — GHA :v1.0.0 image green** via `cut-release.sh v1.0.0` + `gh run list` + `docker pull` for both `ifix-ai-gateway` + `ifix-ai-dashboard`
   - **D — Edge Traefik route loaded** (rsync `ai-gateway-prod.yml` + tail edge logs for `router added`; Pitfall 9 YAML pre-flight via `python3 yaml.safe_load`)
   - **E — DNS resolves** (`cf-dns-create.sh` + `dig +short` @1.1.1.1 returns 162.55.92.154)
   - **F — TLS cert issued** (`curl -sS -I` returns `HTTP/2 200` + `acme.json` contains both hostnames; Pitfall 4/5 reminders)
4. **11 Scenarios** S1-S11, each with Setup / Probe / Expected / Common failure modes / Evidence box / Sign-off:
   - **S1** — Chat E2E under prod hostname → Phase 02 SC-5 step 7 cascade
   - **S2** — Tier-0 embed via colocated Infinity (wiring sanity)
   - **S3** — Whisper STT tier-1 (wiring sanity)
   - **S4** — Force-open primary breaker → tier-1 chat 200 → Phase 03 SC-1 cascade
   - **S5** — Rate-limit burst → Phase 04 SC-1 cascade
   - **S6** — billing_events row inserted → Phase 04 SC-2 cascade
   - **S7** — Peak schedule routing → Phase 04 SC-4 cascade
   - **S8** — vegeta burst 5 RPS × 30s → Phase 05 SC-4/SC-5 cascade
   - **S9** — Per-tenant golden-path smoke (6 tenants × 3 scripts) → INT-06 primary evidence
   - **S10** — Rollback drill timed (< 5 min each direction) → INT-06 secondary evidence (passed_partial fallback if first release)
   - **S11** — Sentry test error verified in UI (release=v1.0.0 environment=production) → D-14 primary evidence (passed_partial fallback if Sentry project not bootstrapped)
5. **Cascade-Close Commits** (4 inline stanzas, executed AFTER S1-S8 PASS):
   - **Cascade-1** — Phase 02 SC-5 step 7 re-verify under prod URL (no status flip; evidence stanza only)
   - **Cascade-2** — Phase 03 SC-1 re-verify under prod URL (no status flip; evidence stanza only)
   - **Cascade-3** — Phase 04 SC-1+SC-2+SC-4 (REQUIRED `sed -i` status flip `passed_partial → passed` + evidence stanza)
   - **Cascade-4** — Phase 05 SC-1 re-verify under prod URL (no status flip; evidence stanza only)
   - Each stanza includes: exact `sed -i` (where applicable) + exact yaml stanza body with `gaps_closed_phase_10_2026_05_XX:` key + exact git commit message + WARNING-5 positive-assertion grep recipe (`grep -E "gaps_closed_phase_10_2026" ...` — Phase 06.9 commit `e3be97b` template; Phase 04 additionally `grep -E "^status: passed$" ...`)
   - Master cascade verification block runs all 4 greps in a loop after individual commits land
6. **Cleanup (MANDATORY)** — `gatewayctl breaker force-close` for `local-llm` + `local-stt` + `local-embed`; `gatewayctl tenant set-mode uat10-test 24/7`; verify NO forced-open breakers remain; spend reconciliation
7. **Summary** — operator-fill table (wall time, $ spend, PASS/FAIL counts, cascade commits landed, gate failures, R2 abort flag, cleanup status)
8. **Sign-off** — 4 final-status checkboxes (PASSED / PASSED PARTIAL / FAILED / ABORTED) each with canonical resume-signal string
9. **passed_partial Fallback** — common S9/S10/S11 deferral patterns + sign-off rules (4 cascade commits MUST land; S9-S11 may defer)
10. **Gaps** — template + filled-gaps section (operator writes "None" if all PASS)

## Self-Check

```
✓ test -f .planning/phases/10-prod-deploy-ai-gateway/10-HUMAN-UAT.md                                 (FOUND)
✓ grep ^## Pre-UAT Preconditions ...10-HUMAN-UAT.md                                                  (FOUND)
✓ 6 Pre-UAT BLOCKING gates A-F (each with sign-off line)                                             (FOUND, 6/6)
✓ 11 scenarios S1-S11 (each with Setup / Probe / Expected / Evidence box / Sign-off)                 (FOUND, 11/11)
✓ Task 1A placeholder comment removed                                                                (REMOVED)
✓ 4 cascade-close commit stanzas inline (Phase 02/03/04/05)                                          (FOUND, 4/4)
✓ Phase 04 stanza includes sed -i status flip + dual-grep verification                               (FOUND)
✓ Master cascade verification loop block present                                                     (FOUND)
✓ Summary section present                                                                            (FOUND)
✓ Sign-off section present with 4 final-status checkboxes + resume signals                           (FOUND)
✓ passed_partial Fallback section present with S9/S10/S11 deferral patterns                          (FOUND)
✓ Gaps section present with template                                                                 (FOUND)
✓ gaps_closed_phase_10_2026 literal appears                                                          (FOUND)
✓ ifix-ai-gateway-prod Sentry project literal cited                                                  (FOUND)
✓ Line count 1139 >= 350                                                                             (PASS)
✓ Task 1A commit fdd07be exists                                                                      (FOUND)
✓ Task 1B commit 85940d8 exists                                                                      (FOUND)
```

## Self-Check: PASSED

## Deviations from Plan

None — Task 1A + Task 1B authored exactly as the plan specified. Task 2 is BLOCKING and is NOT executed in this autonomous session (D-19 mandate — real CF DNS POST + real https smoke + Sentry project bootstrap cannot be automated from an executor agent).

## Checkpoint Status

| Property | Value |
|---|---|
| **Task** | Task 2 of Plan 10-06 |
| **Type** | `checkpoint:human-verify` |
| **Gate** | `blocking` |
| **Depends on** | Task 1B (complete) |
| **Estimated operator wall time** | 2-3 h (single sitting) |
| **Estimated $ spend** | ≤ $0.10 OpenRouter + OpenAI; $0 Vast/GPU |
| **Resume signals accepted** | `approved — all 11 scenarios PASS, 4 cascade-close commits landed, run /gsd:verify-work 10` OR `partial — N PASS, M FAIL — see Gaps section; run /gsd:plan-phase 10 --gaps` OR `aborted — R2 spend cap hit at $X.XX after scenario S<n>; cleanup complete; investigate before re-running` |

## What the Operator Does Next (Task 2)

1. **Open `.planning/phases/10-prod-deploy-ai-gateway/10-HUMAN-UAT.md`** and read top-to-bottom.
2. **Pre-UAT Preconditions (Gates A-F)** — all 6 sign-offs PASS before proceeding. The 6 gates collectively drive the actual deploy (preflight → bootstrap-postgres → cut-release v1.0.0 → RUNBOOK Steps 1-7 → edge Traefik rsync → DNS flip → TLS first-issue).
3. **Scenarios S1-S11** — execute in order; sign each as PASS / FAIL / DEFERRED. Track cumulative spend; STOP at $2.00 (R2 hard abort) or $0.05 per call.
4. **Cascade-Close 4 commits** — after S1-S8 PASS, run the 4 commits (`docs(02-VERIFICATION): ...`, `docs(03-VERIFICATION): ...`, `docs(04-VERIFICATION): ...`, `docs(05-VERIFICATION): ...`). After each, run the positive-assertion grep recipe.
5. **Summary + Sign-off** — fill the Summary table; tick ONE of the 4 final-status checkboxes; provide the canonical resume signal.
6. **Cleanup MANDATORY** — verify `gatewayctl breaker list | grep -i forced` returns empty; primary FSM state unchanged; uat10-test back to 24/7.

**If any scenario FAILs:** operator marks FAIL + fills the Gaps section + commits `docs(10-HUMAN-UAT): record live UAT results — N PASS, M FAIL`, then runs `/gsd:plan-phase 10 --gaps` to generate gap-closure plans.

**If all 11 scenarios PASS:** operator commits `docs(10-HUMAN-UAT): all 11 scenarios PASS`, then runs `/gsd:verify-work 10` to author `10-VERIFICATION.md` and advance STATE.md.

## Commits This Plan (Autonomous Executor)

| # | Hash | Message | Files |
|---|---|---|---|
| 1 | `fdd07be` | `docs(10-06): author HUMAN-UAT.md skeleton — frontmatter + 6 Pre-UAT gates + 8 scenarios + cleanup` | `10-HUMAN-UAT.md` (skeleton, 686 lines) |
| 2 | `85940d8` | `docs(10-06): append HUMAN-UAT scenarios S9-S11 + 4 cascade-close stanzas + Summary/Sign-off/passed_partial/Gaps` | `10-HUMAN-UAT.md` (append +454 lines → 1139 total) |
| 3 | (this commit) | `docs(10-06): summary — Plan 10-06 paused at Task 2 human-verify checkpoint` | `10-06-SUMMARY.md` |

Total autonomous commits: 3. Task 2 (live UAT) will land 4 cascade-close commits + 1 final HUMAN-UAT result commit during the operator-driven session.

## Threat Surface Scan

No new security-relevant surface added by this plan — it is documentation-only. The HUMAN-UAT.md document itself carries an explicit REDACTION WARNING at the top mandating that operators never paste real Bearer tokens / OpenAI keys / OpenRouter keys / Cloudflare tokens into the evidence boxes (T-10-06-01 mitigation per the plan's threat register).

## Next

- Operator opens `.planning/phases/10-prod-deploy-ai-gateway/10-HUMAN-UAT.md` and runs the playbook end-to-end.
- After Task 2 sign-off, operator runs `/gsd:verify-work 10` (PASSED) OR `/gsd:plan-phase 10 --gaps` (PASSED PARTIAL / FAILED) per the resume signal.
- Phase 10 ROADMAP checkbox + STATE.md plan counter advance ONLY after `/gsd:verify-work 10` writes `10-VERIFICATION.md`.

---

*Plan 10-06 authored 2026-05-26 by gsd-executor (Claude Opus 4.7) in worktree `worktree-agent-a5c313f014bf3d8a6`.*
*Plan: `.planning/phases/10-prod-deploy-ai-gateway/10-06-PLAN.md` · Wave: 4 · Depends on: 10-01..10-05.*
*Analogs: `.planning/phases/06.9-openrouter-model-rewrite-per-upstream/06.9-HUMAN-UAT.md` (structural mirror) + `.planning/phases/09-.../09-HUMAN-UAT.md` (per-tenant smoke pattern) + Phase 06.9 commit `e3be97b` (cascade-close commit template with WARNING-5 positive-assertion grep).*
