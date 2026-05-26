---
phase: 10-prod-deploy-ai-gateway
plan: 05
artifact: pre-cut-release-checklist
audience: operator
companion_script: scripts/deploy/cut-release.sh
runbook: gateway/docs/RUNBOOK-DEPLOY.md
---

# Pre-Cut-Release Checklist (v1.0.0)

This is the **pre-flight document** the operator reads BEFORE invoking
`scripts/deploy/cut-release.sh`. The script itself enforces (1) clean working
tree, (2) develop tip CI green, (3) tag does not already exist, (4) FF-only
merge into main, (5) GHA tag-build green. This checklist documents the
operator-side context, prerequisites, and sign-off lines that the script
cannot enforce (e.g. "Wave 0–2 plans all GREEN", "REQUIREMENTS.md remap
committed").

**Decisions in force:**

- **D-12** (10-CONTEXT.md): branch strategy = promote `develop` → `main` as
  part of Phase 10. GHA workflows already build `:main`, `:latest`, and
  `:v1.0.0` image tags from `main` / `refs/tags/v*`; prod stack pins
  `:v1.0.0` (not floating `:main`) so rollback is to a previous tag.
- **D-13** (10-CONTEXT.md): first cut release = `v1.0.0`. Annotated tag bound
  to the commit on `main` produced by the FF-only merge. Signed if GPG is
  configured on the host running the script (else annotated only).
- **Pitfall 5** (10-RESEARCH.md): the tag MUST be pushed BEFORE the deploy so
  `GATEWAY_VERSION=v1.0.0` propagates into the binary via the
  `-ldflags -X .../obs.BuildVersion=v1.0.0` build-arg → Sentry tags Release
  `v1.0.0` in the `production` environment. The cut-release script enforces
  tag-first; if you bring up the stack via `docker compose up -d` BEFORE
  running this script, the binary will be tagged `develop-<sha>` and Sentry's
  releases tab will not show `v1.0.0`.

**Companion script:** `scripts/deploy/cut-release.sh` (Plan 10-05 Task 1).
The script is idempotent at the tag-existence guard — re-running after a
successful run aborts with a clear "tag already exists" message rather than
re-tagging the same SHA.

**Estimated wall time:** ~8 min total. Breakdown:
- FF merge + tag push: instant (<5 s).
- GHA `build-gateway.yml` tag-build: ~5–7 min (Go build + integration tests +
  image push to ghcr.io). Runs in parallel with `build-dashboard.yml`.
- GHA `build-dashboard.yml` tag-build: ~3 min.
- Final `docker pull` smoke for both images: ~30 s.

---

## Gates (all must PASS before invoking `cut-release.sh`)

Sign each line with `PASS`/`FAIL`, operator initials, and date. A single `FAIL`
aborts the cut-release — remediate first, do NOT manually bypass any gate.

- [ ] **Develop tip CI green** — `gh run list --limit 5 --branch develop
      --workflow build-gateway.yml` shows latest run as `success`; same for
      `build-dashboard.yml`. If either is `failure`/`cancelled`/`in_progress`,
      wait for green or fix develop first. (RESEARCH §How To #6 — the GHA
      tag-build re-runs from `refs/tags/v1.0.0`, but cutting from a broken
      develop is a forewarning that the tag-build will also fail and the
      operator will end up with a stuck tag.)
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

- [ ] **No uncommitted local changes** — `git status` clean. If not, commit
      to a feature branch or move the work to another worktree before
      retrying. Do NOT `git stash` inside a worktree (CLAUDE.md
      destructive-git-prohibition).
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

- [ ] **`v1.0.0` tag does not exist locally or on origin** — both commands
      MUST be empty:
      ```bash
      git tag -l v1.0.0
      git ls-remote --tags origin refs/tags/v1.0.0
      ```
      If the local tag is present (e.g. from an aborted prior session), delete
      with `git tag -d v1.0.0`. If the remote tag is present, the release was
      already cut — DO NOT re-cut; either accept the existing tag (Plan 10-06
      can proceed against the already-published image) or pick a new
      `RELEASE_TAG` (`RELEASE_TAG=v1.0.1 scripts/deploy/cut-release.sh`).
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

- [ ] **Main not diverged from develop (or operator already rebased)** —
      `git log --oneline main..develop` lists only commits that are forward of
      `main`; the reverse `git log --oneline develop..main` is empty. If main
      HAS commits not on develop (someone landed a hotfix on main without
      back-merging to develop), the operator must rebase develop on main
      before retrying. The cut-release script will refuse the FF-only merge
      in this state.
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

- [ ] **All Phase 10 Wave 0–2 plans GREEN** — verify all required Plan
      artifacts exist:
      ```bash
      ls -1 \
        gateway/docker-compose.prod.yml \
        gateway/.env.prod.example \
        scripts/deploy/preflight.sh \
        scripts/deploy/bootstrap-postgres.sh \
        scripts/deploy/migrate-dashboard.sh \
        scripts/deploy/cf-dns-create.sh \
        .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml \
        gateway/docs/RUNBOOK-DEPLOY.md
      ```
      All 8 paths MUST resolve to existing files. Missing files → Plan 10-01
      / 10-02 / 10-03 / 10-04 incomplete; do NOT cut the release.
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

- [ ] **REQUIREMENTS.md remap committed** — Phase 10's PRD remap (deferred
      AI Gateway requirements pointed at Phase 11) must be on develop tip:
      `grep -c 'Phase 11: prod-hardening' .planning/REQUIREMENTS.md` ≥ 5.
      If the count is < 5, the remap was not finished — do NOT cut the
      release (binding `v1.0.0` to an incomplete PRD trail breaks audit).
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

- [ ] **`gh` CLI authenticated** — `gh auth status` shows `Logged in to
      github.com`. The cut-release script aborts at startup if `gh` is
      unauthenticated, but confirming here saves a 1-min round trip through
      the precondition gates.
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

- [ ] **GPG signing key available (optional, D-13)** — `gpg
      --list-secret-keys` shows at least one secret key. If present, the
      cut-release script automatically adds `-s` to the `git tag -a`
      invocation. If absent, the tag is annotated only (no signature). Both
      are acceptable per D-13 ("annotated, signed if GPG configured").
      Operator: explicitly accept the unsigned-annotated path if no GPG key.
      `[ ] PASS  [ ] FAIL · Operator: ___ · Date: ___`

---

## Execution

Once all gates above are PASS:

```bash
cd /home/pedro/projetos/pedro/gpu-ifix
RELEASE_TAG=v1.0.0 scripts/deploy/cut-release.sh
```

Wait for the final summary line:

```
Release v1.0.0 cut and published.
```

The script blocks on `gh run watch` until BOTH `build-gateway.yml` and
`build-dashboard.yml` reach `success` and publish images to ghcr.io. Total
expected wall time ~8 min. Do not Ctrl-C the script after the tag push — if
the GHA build fails, the tag will be on origin but no image will exist; the
operator must decide whether to delete the tag and re-cut (`git tag -d
v1.0.0 && git push origin :refs/tags/v1.0.0`) or fix-forward on develop and
cut `v1.0.1` instead.

After "Release v1.0.0 cut and published":

1. Confirm both images visible at
   `https://github.com/orgs/IfixTelecom/packages` (gateway + dashboard, both
   tagged `v1.0.0` + `latest` + `v1.0.0-<sha>`).
2. Proceed to **Plan 10-06 HUMAN-UAT** — operator runs
   `scripts/deploy/preflight.sh` (Gate B capacity probe), then
   `gateway/docs/RUNBOOK-DEPLOY.md` Steps 1–7 (First-Time Bring-Up).

## Abort / Rollback (if the cut-release fails mid-flight)

| Failure point | Effect | Recovery |
|---------------|--------|----------|
| Guard 1 (uncommitted) | No mutation | Commit / stage / move to feature branch, retry. |
| Guard 2 (develop CI red) | No mutation | Fix develop, wait for green CI, retry. |
| Guard 3 (tag exists) | No mutation | Either accept existing tag OR `git tag -d v1.0.0 && git push origin :refs/tags/v1.0.0`, retry. |
| Section 4 — `git merge --ff-only` rejected | Local main may be ahead of remote main (FF was attempted post-pull); no push yet | `git checkout main && git reset --hard origin/main`, rebase develop on main, retry. |
| Section 4 — `git push origin main` rejected | Tag exists locally but not pushed; main not promoted on origin | `git tag -d v1.0.0`, re-pull main, investigate race, retry. |
| Section 5 — `gh run watch` returns non-zero | Tag IS on origin; image may NOT be on ghcr.io | Inspect failed run via `gh run view <id> --log`; either fix-forward on develop and cut `v1.0.1`, OR delete tag (`git push origin :refs/tags/v1.0.0`) and re-cut after fix. |
| Section 6 — `docker pull` 404 despite green GHA | Race between GHCR registry and GHA report | Wait 30 s, retry `docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0` manually. |

---

_Companion artifacts: `scripts/deploy/cut-release.sh` (the executable
script), `gateway/docs/RUNBOOK-DEPLOY.md` §Cut-Release Procedure (the prose
playbook this checklist front-loads), `.planning/phases/10-prod-deploy-ai-gateway/10-CONTEXT.md` D-12 + D-13 (the decisions this implements)._
