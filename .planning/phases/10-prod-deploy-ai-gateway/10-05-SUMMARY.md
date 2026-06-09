---
phase: 10-prod-deploy-ai-gateway
plan: 05
subsystem: infra
tags: [release, develop-main, v1.0.0, gha, ghcr, cut-release, sentry-release, ff-only-merge, annotated-tag, gpg-sign, idempotent-script]

# Dependency graph
requires:
  - phase: 10-prod-deploy-ai-gateway/10-01
    provides: scripts/deploy/preflight.sh — n8n-ia-vm capacity probe invoked by 10-06 Gate B AFTER the release is cut
  - phase: 10-prod-deploy-ai-gateway/10-02
    provides: scripts/deploy/bootstrap-postgres.sh — invoked AFTER 10-05 publishes the :v1.0.0 image
  - phase: 10-prod-deploy-ai-gateway/10-03
    provides: gateway/docker-compose.prod.yml + .env.prod.example pinning image:v1.0.0 — concrete consumer of this plan's output
  - phase: 10-prod-deploy-ai-gateway/10-04
    provides: gateway/docs/RUNBOOK-DEPLOY.md §Cut-Release Procedure — prose playbook this plan front-loads into a runnable script + checklist
provides:
  - scripts/deploy/cut-release.sh — guarded operator-runnable develop→main FF + annotated tag + push + `gh run watch` + post-build `docker pull` smoke
  - .planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md — 8-gate pre-cut-release operator checklist with sign-off lines
affects:
  - phase: 10-prod-deploy-ai-gateway/10-06
    provides: image :v1.0.0 must exist on ghcr.io before Gate C HUMAN-UAT can pull it
  - phase: 11-prod-hardening (future)
    provides: cut-release.sh is reusable for v1.0.1 / v1.1.0 / v1.x.y patch + minor releases via RELEASE_TAG env var

# Tech tracking
tech-stack:
  added: []   # no new runtime deps; the script orchestrates pre-existing tooling (git, gh CLI, docker, gpg)
  patterns:
    - "Idempotent operator-runnable script with 3 precondition guards before any mutation"
    - "Force-flag-free git push (security gate verified by grep on script body — T-10-05-01)"
    - "`gh run watch` per-workflow with explicit headSha match to disambiguate concurrent tag-builds"
    - "Pre-flight checklist with explicit `[ ] PASS [ ] FAIL · Operator: ___ · Date: ___` sign-off lines per gate (matches 06.9-HUMAN-UAT.md style)"

key-files:
  created:
    - scripts/deploy/cut-release.sh
    - .planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md
  modified: []

key-decisions:
  - "Annotated tag with conditional -s signing flag — script auto-detects GPG availability (matches D-13 'signed if GPG configured, else annotated only')"
  - "Separate `git push origin main` + `git push origin v1.0.0` (NOT a single combined push) — main first so the tag's underlying commit is reachable from origin/main before the tag ref lands"
  - "`gh run watch` resolves the run via headSha match (not just 'last run on main') — avoids racing with an unrelated push to main between the tag push and the watch invocation"
  - "Zero force flags of any kind; the verification gate greps the script body to prove this (T-10-05-01) — even the threat-model comments rephrase the literal flag strings"
  - "Idempotent at the tag-existence guard — re-running after a successful cut aborts cleanly with a clear remediation message"
  - "Literal image refs in the final `docker pull` smoke (not `${IMAGE_GATEWAY}` interpolation) so the PLAN verify grep + any future audit script matches the actual instruction line"

patterns-established:
  - "Pattern: cut-release script + companion checklist — script enforces mechanical preconditions, checklist captures operator-side context the script can't introspect (Wave completeness, REQUIREMENTS.md remap, gh-auth status, GPG acceptance)"
  - "Pattern: Pitfall 5 enforcement — tag-before-anything is structural (GHA builds from refs/tags/v*, so GATEWAY_VERSION cannot drift)"
  - "Pattern: idempotent retry — every guard rejects with a remediation hint, no auto-cleanup of partial state (operator decides whether to delete a stuck tag or fix-forward)"

requirements-completed: []   # Per Plan frontmatter: PRD-07 is operator-verified during 10-06; release-mechanics support has no PRD ID

# Metrics
duration: 5m 25s
completed: 2026-05-26
---

# Phase 10 Plan 05: Cut-Release Mechanics Summary

**Guarded operator-runnable `scripts/deploy/cut-release.sh` (develop→main FF + annotated tag + `gh run watch` + docker-pull smoke) plus an 8-gate pre-cut-release checklist with sign-off lines per D-12 + D-13 + Pitfall 5.**

## Performance

- **Duration:** 5m 25s
- **Started:** 2026-05-26T09:01:10Z
- **Completed:** 2026-05-26T09:06:35Z
- **Tasks:** 2 / 2
- **Files modified:** 2 (both created)

## Accomplishments

- Authored `scripts/deploy/cut-release.sh` (407 lines, executable) implementing the D-12 develop→main promotion and D-13 first-cut tag `v1.0.0`. Six guarded sections gated by `log_section` banners: (1) clean working tree, (2) develop tip CI green on `build-gateway.yml` + `build-dashboard.yml`, (3) tag absent locally + on origin, (4) FF-only merge + annotated tag (conditionally signed) + separate pushes, (5) `gh run watch` per workflow with headSha-match disambiguation, (6) `docker pull` smoke against ghcr.io.
- Authored `.planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md` (181 lines, 8 sign-off gates) — pre-flight operator checklist citing D-12 / D-13 / Pitfall 5, with an Abort/Rollback table mapping every failure point to a concrete recovery path.
- All verification gates GREEN: `bash -n` clean, zero force flags (T-10-05-01 mitigation verifiable by grep), default `RELEASE_TAG=v1.0.0` (D-13), checklist ≥7 gates + ≥40 lines + all required citation tokens.

## Task Commits

Each task committed atomically:

1. **Task 1: Author scripts/deploy/cut-release.sh** — `b53e3a6` (feat)
2. **Task 2: Author 10-05-RELEASE-CHECKLIST.md** — `935883b` (docs)

_Plan metadata commit (SUMMARY.md) follows after self-check._

## Files Created/Modified

- `scripts/deploy/cut-release.sh` (407 lines, mode 0755) — Guarded develop→main fast-forward + annotated tag + `gh run watch` + post-build `docker pull` smoke. Idempotent at the tag-existence guard. Zero force flags.
- `.planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md` (181 lines) — Pre-cut-release operator checklist with 8 sign-off gates + Execution section + Abort/Rollback table.

## Decisions Made

1. **Annotated tag with conditional `-s`** — D-13 specifies "annotated, signed if GPG configured". The script detects GPG availability via `command -v gpg && gpg --list-secret-keys | head -1 | grep -q '/'` and sets `SIGN=-s` or `SIGN=""` accordingly. The `git tag -a ${SIGN} ...` invocation leaves `$SIGN` unquoted intentionally so an empty `SIGN` expands to no argument (quoting would pass `''` as a literal arg to git tag).
2. **Separate `git push` for main and tag** — pushing main first ensures the tag's underlying commit is reachable from `origin/main` BEFORE the tag ref lands. Combined `git push origin main v1.0.0` would work too but loses the operator's ability to abort cleanly after the main push if the tag push gets rejected.
3. **`gh run watch` with explicit headSha match** — `gh run list --branch main --limit 5 --json databaseId,headSha` filtered through `jq` to match the local HEAD avoids racing with an unrelated push to main. Up to 5 attempts × 5 s wait covers the GHA-registration delay (typically <10 s post-push).
4. **Zero force flags as a structural invariant** — the Plan verification gate greps the script body for `--force` / `--force-with-lease`. The script's threat-model comments were rephrased to avoid the literal flag strings (e.g., "zero force flags of any kind" rather than "no --force, no --force-with-lease") so the grep stays definitive without false-positives. This is the mitigation for T-10-05-01 (accidental force-push to main).
5. **Literal image refs in `docker pull` smoke** — the Plan verify line requires the literal string `docker pull ghcr.io/ifixtelecom/ifix-ai-gateway` in the script. The implementation uses literal refs (not `${IMAGE_GATEWAY}` variable interpolation) for both gateway and dashboard images so any future audit grep matches the actual instruction.
6. **Pre-flight checklist as a separate artifact** — the script enforces 5 mechanical preconditions; the checklist captures the operator-side context the script can't introspect (Wave 0–2 plan completeness, REQUIREMENTS.md remap committed, gh-auth status, GPG acceptance). Sign-off lines follow the `[ ] PASS [ ] FAIL · Operator: ___ · Date: ___` pattern from 06.9-HUMAN-UAT.md so cross-phase audit tooling stays consistent.

## Deviations from Plan

None — plan executed exactly as written. Two verification-loop iterations on Task 1:

1. Initial implementation used `${IMAGE_GATEWAY}` variable interpolation for `docker pull`; the Plan verify line requires the literal `docker pull ghcr.io/ifixtelecom/ifix-ai-gateway` string. Changed to literal refs.
2. Initial threat-model comments contained the literal strings `--force` and `--force-with-lease`; the Plan verify line uses `! grep -qE '\-\-force\b|--force-with-lease'` which matches inside comments too. Rephrased comments to "zero force flags of any kind" (the verify gate is a security gate — it must stay definitive).

Both adjustments were verify-driven (Plan acceptance criteria), not deviation-rule auto-fixes.

## Issues Encountered

None.

## User Setup Required

None — the authored artifacts are inert. The operator runs `cut-release.sh` during Plan 10-06 HUMAN-UAT Gate C. CLAUDE.md confirms `gh` CLI is available + GitHub PAT is configured in `~/.git-credentials` on ops-claude, which is where the operator runs the script.

## Next Phase Readiness

- **Plan 10-06 (HUMAN-UAT) ready** — the script + checklist together give the operator a single-command path to publish `:v1.0.0` to ghcr.io. 10-06 Gate C invokes the script; on success, Steps 1–7 of `RUNBOOK-DEPLOY.md` First-Time Bring-Up proceed against the published image.
- **No blockers.** The release authoring is fully autonomous; the live cut is operator-driven by design (D-12 + Pitfall 5 + the destructive-git-prohibition in CLAUDE.md all forbid an executor agent from touching live remote refs).
- **Future reuse** — the script is parametric on `RELEASE_TAG` + `RELEASE_MESSAGE`. Phase 11 patch cuts (`v1.0.1`, `v1.0.2`) and minor cuts (`v1.1.0`) reuse the script verbatim. The checklist's `[ ] PASS [ ] FAIL · Operator: ___ · Date: ___` format gives every future cut its own audit trail.

## Self-Check: PASSED

**Files verified to exist:**
- `scripts/deploy/cut-release.sh` — FOUND (407 lines, mode 0755, `bash -n` clean)
- `.planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md` — FOUND (181 lines)

**Commits verified to exist:**
- `b53e3a6` — FOUND (Task 1: `feat(10-05): add scripts/deploy/cut-release.sh`)
- `935883b` — FOUND (Task 2: `docs(10-05): add 10-05-RELEASE-CHECKLIST.md`)

**Plan verification gate (end-of-plan):**
- [x] `scripts/deploy/cut-release.sh` executable + passes `bash -n` + zero `--force` / `--force-with-lease` flags
- [x] `10-05-RELEASE-CHECKLIST.md` present with 8 sign-off gates (≥7 required) + 181 lines (≥40 required)
- [x] Default `RELEASE_TAG` is `v1.0.0` per D-13

---
*Phase: 10-prod-deploy-ai-gateway*
*Plan: 05*
*Completed: 2026-05-26*
