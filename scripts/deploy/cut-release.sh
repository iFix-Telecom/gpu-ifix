#!/usr/bin/env bash
# scripts/deploy/cut-release.sh — Phase 10 Wave 3 (Plan 10-05) guarded
# develop → main fast-forward + annotated tag push + GHA tag-build watcher.
#
# Implementable form of D-12 + D-13 (RESEARCH §How To #6 + #10):
#   The cut-release procedure promotes `develop` → `main` via a FF-only merge,
#   creates an annotated tag (signed if a GPG key is configured), pushes both
#   refs, and then blocks via `gh run watch` until BOTH `build-gateway.yml` and
#   `build-dashboard.yml` reach `success` and publish `:v1.0.0` + `:latest` +
#   `:v1.0.0-<sha>` to ghcr.io. Plan 10-06 HUMAN-UAT cannot pull `:v1.0.0`
#   until this script has completed end-to-end.
#
# Pitfall 5 reminder (RESEARCH §Pitfall 5):
#   The tag MUST be pushed BEFORE the deploy so `GATEWAY_VERSION=v1.0.0`
#   propagates into the binary via `-ldflags -X .../obs.BuildVersion=v1.0.0`,
#   which Sentry then surfaces as Release `v1.0.0` in the `production`
#   environment. This script does no image build / push of its own — GHA
#   handles the build from `refs/tags/v1.0.0`. The image tag therefore CANNOT
#   drift from the source ref.
#
# Idempotency:
#   - Detects if `${RELEASE_TAG}` already exists locally (`git rev-parse
#     --verify refs/tags/${RELEASE_TAG}`) OR on origin (`git ls-remote --tags
#     origin refs/tags/${RELEASE_TAG}`) and refuses to re-tag — operator must
#     either delete the existing tag (`git tag -d ${RELEASE_TAG}` + `git push
#     origin :refs/tags/${RELEASE_TAG}`) or pick a new RELEASE_TAG (e.g.
#     `RELEASE_TAG=v1.0.1`).
#   - Re-running after a successful run is a NO-OP at the tag-existence guard
#     (exits 1 with a clear "already exists" message before any mutation).
#   - The FF-only merge is naturally idempotent: re-running after `main` and
#     `develop` already point at the same commit produces "Already up to
#     date." and proceeds.
#   - NO destructive operations. Zero force flags of any kind (the Plan-10-05
#     verification gate greps the script body to confirm this — see PLAN.md
#     §threat_model T-10-05-01). Operator can abort cleanly at any of the 3
#     precondition guards before any mutation reaches origin.
#
# Threat model (T-10-05-01 … T-10-05-06 — see PLAN.md):
#   - T-10-05-01 (Tampering — force-push to main): script uses ONLY `git merge
#     --ff-only` and plain `git push origin main`; grep on the script body
#     proves no force flag exists (Plan verification gate).
#   - T-10-05-02 (Tampering — tag on stale develop): pre-merge `git pull
#     --ff-only` on develop + FF-only merge into main prevents stale ancestry.
#   - T-10-05-03 (Repudiation — lightweight tag loses metadata): hardcoded
#     `git tag -a` with detected `$SIGN` flag.
#   - T-10-05-05 (DoS — GHA tag-build fails): `gh run watch` blocks until
#     completion; on non-zero exit, the script aborts WITHOUT attempting to
#     delete the pushed tag (operator decides).
#   - T-10-05-06 (Sentry release tag mismatch — Pitfall 5): no `docker build`
#     happens here; image is built BY GHA from `refs/tags/v1.0.0`, so
#     `compute-tags` is guaranteed to set `gateway_version=v1.0.0`.
#
# Required env (validated at startup, fail-fast):
#   (none — defaults below cover the v1.0.0 cut)
#
# Optional env (with documented defaults):
#   RELEASE_TAG       — default `v1.0.0` (D-13). Override for v1.0.1 patch cut.
#   RELEASE_MESSAGE   — default multi-line message per RESEARCH §How To #10
#                       listing Phase 02..09 milestones.
#
# Prerequisites:
#   - `gh` CLI on PATH and authenticated (`gh auth status`).
#   - `git` >= 2.30 on PATH.
#   - `docker` on PATH (final sanity `docker pull` step).
#   - Repository root checkout (script must run from gpu-ifix checkout root).
#   - Operator can push to `origin/main` and `origin/refs/tags/*` (PAT in
#     `~/.git-credentials` per CLAUDE.md).
#
# Usage:
#   # Standard first-cut release
#   scripts/deploy/cut-release.sh
#
#   # Custom tag (post-Phase 10 patch cut)
#   RELEASE_TAG=v1.0.1 scripts/deploy/cut-release.sh
#
#   # Custom message (e.g., hotfix)
#   RELEASE_TAG=v1.0.1 \
#     RELEASE_MESSAGE='Hotfix: dashboard auth cookie SameSite fix' \
#     scripts/deploy/cut-release.sh
#
# Exit codes:
#   0 — all guards PASS, tag pushed, both GHA workflows reached `success`,
#       `docker pull` smoke succeeded.
#   1 — precondition FAIL (uncommitted, gh missing, CI not green, tag exists,
#       main diverged, push failed, gh run watch failed).

set -euo pipefail

# --- defaults -------------------------------------------------------------
RELEASE_TAG="${RELEASE_TAG:-v1.0.0}"
RELEASE_MESSAGE="${RELEASE_MESSAGE:-Phase 10: first GA release — gateway + dashboard prod cutover

- Multi-tenant Auth (Phase 2)
- Resilience + Fallback Chain (Phase 3, 06.9)
- Quotas / Billing / Schedule Routing (Phase 4)
- Load Shedding (Phase 5)
- Emergency Pod + Primary Pod (Phase 6 + 06.6 + 06.8)
- Observability Dashboard + Sentry (Phase 7)
- Client Integrations + Sensitive Tenants (Phase 8 + 9)
- First Production Deploy on n8n-ia-vm (Phase 10)}"

IMAGE_GATEWAY="ghcr.io/ifixtelecom/ifix-ai-gateway"
IMAGE_DASHBOARD="ghcr.io/ifixtelecom/ifix-ai-dashboard"
WORKFLOW_GATEWAY="build-gateway.yml"
WORKFLOW_DASHBOARD="build-dashboard.yml"

# --- helpers --------------------------------------------------------------
# log() writes to stderr to keep stdout clean for the final summary.
log() { printf '[%s] [cut-release] %s\n' "$(date -Iseconds)" "$*" >&2; }

log_section() {
  printf '\n============================================================\n%s\n============================================================\n' "$*" >&2
}

# --- prereq: tooling ------------------------------------------------------
if ! command -v gh >/dev/null 2>&1; then
  log "FATAL: gh CLI not on PATH — install GitHub CLI and run \`gh auth login\` (see CLAUDE.md GitHub PAT note)."
  exit 1
fi

if ! command -v git >/dev/null 2>&1; then
  log "FATAL: git not on PATH."
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  log "FATAL: docker not on PATH (final \`docker pull\` smoke needs it)."
  exit 1
fi

# Confirm we are inside the gpu-ifix repo checkout.
if ! git rev-parse --show-toplevel >/dev/null 2>&1; then
  log "FATAL: not inside a git checkout. cd into the gpu-ifix repo first."
  exit 1
fi

# Confirm origin remote exists.
if ! git remote get-url origin >/dev/null 2>&1; then
  log "FATAL: \`origin\` remote not configured."
  exit 1
fi

# Confirm gh CLI is authenticated.
if ! gh auth status >/dev/null 2>&1; then
  log "FATAL: gh CLI not authenticated — run \`gh auth login\`."
  exit 1
fi

# Ensure we run from the repo root so relative paths in messages make sense.
REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

log "RELEASE_TAG=${RELEASE_TAG}"
log "repo root: ${REPO_ROOT}"

# --- 1) Precondition: clean tree -----------------------------------------
log_section "1) Precondition: clean working tree"

PORCELAIN="$(git status --porcelain)"
if [[ -n "${PORCELAIN}" ]]; then
  log "FAIL — uncommitted changes detected. Commit or stash first."
  log ""
  log "  git status --porcelain output:"
  printf '%s\n' "${PORCELAIN}" | sed 's/^/    /' >&2
  log ""
  log "  Action: commit the changes, OR move them onto a feature branch and"
  log "          retry from a clean checkout. Do NOT \`git stash\` inside a"
  log "          worktree (per CLAUDE.md destructive-git-prohibition)."
  exit 1
fi
log "PASS — working tree clean."

# --- 2) Precondition: develop tip CI green -------------------------------
log_section "2) Precondition: develop tip CI green (build-gateway + build-dashboard)"

check_workflow_green() {
  local workflow="$1"
  local conclusion

  if ! conclusion="$(gh run list \
    --limit 1 \
    --branch develop \
    --workflow "${workflow}" \
    --json conclusion \
    --jq '.[0].conclusion' 2>&1)"; then
    log "FAIL — gh run list errored for ${workflow}: ${conclusion}"
    return 1
  fi

  # Strip surrounding quotes from the jq result if any.
  conclusion="${conclusion//\"/}"

  if [[ "${conclusion}" != "success" ]]; then
    local url
    url="$(gh run list \
      --limit 1 \
      --branch develop \
      --workflow "${workflow}" \
      --json url \
      --jq '.[0].url' 2>/dev/null || echo "(unknown)")"
    log "FAIL — develop tip CI not green for ${workflow} (conclusion=${conclusion:-null})"
    log "       Inspect: ${url}"
    log "       Wait for green or fix develop before cutting the release."
    return 1
  fi

  log "PASS — ${workflow} on develop = success"
  return 0
}

check_workflow_green "${WORKFLOW_GATEWAY}" || exit 1
check_workflow_green "${WORKFLOW_DASHBOARD}" || exit 1

# --- 3) Precondition: tag does not already exist -------------------------
log_section "3) Precondition: tag ${RELEASE_TAG} does not already exist"

if git rev-parse --verify "refs/tags/${RELEASE_TAG}" >/dev/null 2>&1; then
  log "FAIL — tag ${RELEASE_TAG} already exists locally."
  log ""
  log "  This script is idempotent at this guard: re-running after a successful"
  log "  cut is a no-op. If you genuinely need to re-cut the same tag (e.g. the"
  log "  GHA build failed and you fixed develop), first delete BOTH the local"
  log "  and remote tag:"
  log ""
  log "    git tag -d ${RELEASE_TAG}"
  log "    git push origin :refs/tags/${RELEASE_TAG}"
  log ""
  log "  Then re-run this script. Otherwise pick a new RELEASE_TAG (e.g."
  log "  RELEASE_TAG=v1.0.1 scripts/deploy/cut-release.sh)."
  exit 1
fi

# Also check remote — local could be missing if operator is on a fresh checkout
# but the tag was already pushed in a prior session from another host.
REMOTE_TAG_REF="$(git ls-remote --tags origin "refs/tags/${RELEASE_TAG}" 2>/dev/null || true)"
if [[ -n "${REMOTE_TAG_REF}" ]]; then
  log "FAIL — tag ${RELEASE_TAG} already exists on origin:"
  log "  ${REMOTE_TAG_REF}"
  log ""
  log "  Re-fetching tags into your local clone:"
  log "    git fetch --tags origin"
  log ""
  log "  Then either accept the existing tag (Plan 10-06 can proceed with the"
  log "  already-published image) OR delete BOTH local + remote tag and re-cut"
  log "  (commands above)."
  exit 1
fi

log "PASS — tag ${RELEASE_TAG} absent locally and on origin."

# --- 4) Promote develop → main + tag + push ------------------------------
log_section "4) Promote develop → main + annotated tag + push"

log "syncing develop"
git checkout develop
git pull --ff-only

log "syncing main (creating local tracking branch if missing)"
if ! git rev-parse --verify main >/dev/null 2>&1; then
  log "  main absent locally — creating from origin/main"
  git checkout -b main origin/main
else
  git checkout main
fi
git pull --ff-only

log "fast-forward merge develop → main (FF-only — refuses to create a merge commit)"
if ! git merge --ff-only develop; then
  log "FAIL — main has diverged from develop (FF-only merge refused)."
  log ""
  log "  Operator must rebase develop on main BEFORE retrying this script:"
  log "    git checkout develop"
  log "    git rebase main"
  log "    # resolve any conflicts, then:"
  log "    git push origin develop  # only if the rebase changed develop tip"
  log "    scripts/deploy/cut-release.sh"
  log ""
  log "  This script does NOT auto-rebase — that is an operator-judgement call."
  exit 1
fi
log "PASS — main fast-forwarded to develop tip."

# Detect GPG signing capability per RESEARCH §How To #10. The git tag invocation
# below uses `-a` (annotated) ALWAYS, and conditionally adds `-s` (signed) if a
# secret key exists. This matches D-13 ("annotated, signed if GPG configured").
SIGN=""
if command -v gpg >/dev/null 2>&1 && gpg --list-secret-keys 2>/dev/null | head -1 | grep -q '/'; then
  SIGN="-s"
  log "GPG key detected — tag will be signed (-s)."
else
  log "no GPG key — tag will be annotated only (no -s)."
fi

log "creating annotated tag ${RELEASE_TAG}"
# shellcheck disable=SC2086
# $SIGN is intentionally unquoted so it expands to NOTHING (no empty arg) when
# GPG is unavailable; quoting would pass '' as a literal arg to git tag.
git tag -a ${SIGN} "${RELEASE_TAG}" -m "${RELEASE_MESSAGE}"
log "PASS — annotated tag ${RELEASE_TAG} created on main HEAD ($(git rev-parse --short HEAD))."

log "pushing main"
# Plain push — no force flag of any kind. If main has been updated on origin
# between the pull above and now (extremely unlikely — operator races
# themselves), the push is rejected and the script aborts cleanly. The pushed
# tag stays local-only at that point; operator can delete with `git tag -d`.
git push origin main

log "pushing tag ${RELEASE_TAG}"
git push origin "${RELEASE_TAG}"
log "PASS — main + tag pushed to origin."

# --- 5) Watch GHA tag-build until both workflows reach success -----------
log_section "5) Watch GHA tag-build until build-gateway + build-dashboard reach success"

# `gh run list` filtered by branch=main returns the tag-build run for `tags:
# ['v*']` because GHA exposes the tag-ref runs under the underlying branch
# (main, since the tag was created on main HEAD). Wait briefly for the run to
# register on GitHub, then resolve the databaseId.
log "waiting 6s for tag-push to register a workflow run on GitHub..."
sleep 6

watch_workflow() {
  local workflow="$1"
  local run_id

  # Try up to 5 times to find a run whose underlying commit matches the tag
  # commit (HEAD on main right now). gh sometimes takes a few seconds to
  # surface the run after the tag push.
  local target_sha
  target_sha="$(git rev-parse HEAD)"

  local attempt=1
  while [[ ${attempt} -le 5 ]]; do
    run_id="$(gh run list \
      --limit 5 \
      --workflow "${workflow}" \
      --branch main \
      --json databaseId,headSha \
      --jq ".[] | select(.headSha == \"${target_sha}\") | .databaseId" \
      2>/dev/null | head -1 || true)"
    if [[ -n "${run_id}" ]]; then
      break
    fi
    log "  attempt ${attempt}/5: no run yet for ${workflow} @ ${target_sha:0:7} — waiting 5s..."
    sleep 5
    attempt=$((attempt + 1))
  done

  if [[ -z "${run_id}" ]]; then
    log "FAIL — could not locate ${workflow} run for HEAD ${target_sha:0:7} after 5 attempts."
    log "       Inspect manually: gh run list --workflow ${workflow} --branch main"
    return 1
  fi

  log "watching ${workflow} run ${run_id} (HEAD ${target_sha:0:7})"
  if ! gh run watch "${run_id}" --exit-status; then
    log "FAIL — ${workflow} run ${run_id} did not complete with success."
    log "       Inspect: gh run view ${run_id} --log"
    return 1
  fi
  log "PASS — ${workflow} run ${run_id} completed with success."
  return 0
}

watch_workflow "${WORKFLOW_GATEWAY}" || exit 1
watch_workflow "${WORKFLOW_DASHBOARD}" || exit 1

# --- 6) Final sanity: docker pull the new images -------------------------
log_section "6) Smoke: docker pull ${IMAGE_GATEWAY}:${RELEASE_TAG} + dashboard"

# Literal image refs below (NOT via ${IMAGE_GATEWAY} variable) so that
# `grep docker pull ghcr.io/ifixtelecom/ifix-ai-gateway` audits in the Plan
# verify step (and any future audit script) match the actual instruction line.
log "pulling ghcr.io/ifixtelecom/ifix-ai-gateway:${RELEASE_TAG}"
if ! docker pull "ghcr.io/ifixtelecom/ifix-ai-gateway:${RELEASE_TAG}"; then
  log "FAIL — docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:${RELEASE_TAG} returned non-zero."
  log "       The GHA workflows reported success above; this is unexpected."
  log "       Inspect ghcr.io manually: https://github.com/orgs/IfixTelecom/packages"
  exit 1
fi

log "pulling ghcr.io/ifixtelecom/ifix-ai-dashboard:${RELEASE_TAG}"
if ! docker pull "ghcr.io/ifixtelecom/ifix-ai-dashboard:${RELEASE_TAG}"; then
  log "FAIL — docker pull ghcr.io/ifixtelecom/ifix-ai-dashboard:${RELEASE_TAG} returned non-zero."
  exit 1
fi

# --- DONE ----------------------------------------------------------------
log_section "DONE — release ${RELEASE_TAG} cut and published"

cat <<EOF

============================================================
Release ${RELEASE_TAG} cut and published.

  - main fast-forwarded to develop tip @ $(git rev-parse --short HEAD)
  - annotated tag ${RELEASE_TAG} pushed to origin
  - ${IMAGE_GATEWAY}:${RELEASE_TAG} (+ :latest, :${RELEASE_TAG}-<sha>) on ghcr.io
  - ${IMAGE_DASHBOARD}:${RELEASE_TAG} (+ :latest, :${RELEASE_TAG}-<sha>) on ghcr.io

Next: Plan 10-06 HUMAN-UAT — operator runs:
  1) scripts/deploy/preflight.sh                       (capacity probe)
  2) gateway/docs/RUNBOOK-DEPLOY.md Steps 1–7          (First-Time Bring-Up)
============================================================
EOF

exit 0
