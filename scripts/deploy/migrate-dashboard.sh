#!/usr/bin/env bash
# scripts/deploy/migrate-dashboard.sh — Phase 10 Wave 1 (Plan 10-02) one-shot
# Better Auth schema migration runner for the prod dashboard database.
#
# Creates the four Better Auth tables — `user`, `session`, `account`,
# `verification` — under the `dashboard_auth` schema inside the prod
# database `bd_ai_dashboard_prod` (RESEARCH §How To #3 step 7).
#
# Runs the migration INSIDE the dashboard image so the @better-auth/cli
# version is pinned to the same release the dashboard container itself
# is going to use at runtime (T-10-02-05: prevents CLI version drift
# creating different table shapes between operator invocations).
#
# Prerequisites:
#   1. `scripts/deploy/bootstrap-postgres.sh` has already run, so the
#      database `bd_ai_dashboard_prod` exists on the DO managed
#      instance.
#   2. DASHBOARD_DATABASE_URL points at that database (the value the
#      bootstrap script printed for `.env`).
#   3. SSH alias `n8n-ia-vm` is configured + the prod image is pullable
#      via the docker daemon there (the operator's GHCR creds are
#      already populated; the prod stack uses the same daemon).
#   4. The Docker overlay network `intra` exists on n8n-ia-vm (Phase 10
#      Wave 0 capacity probe asserts this).
#
# Required env:
#   DASHBOARD_DATABASE_URL — prod DSN pointing at bd_ai_dashboard_prod, e.g.
#     postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_dashboard_prod?sslmode=require&options=-c%20search_path%3Ddashboard_auth
#
#   The simplest way for the operator to source this safely is:
#     set -a; source /opt/ai-gateway-prod/.env; set +a
#     DASHBOARD_DATABASE_URL="$DASHBOARD_DATABASE_URL" ./scripts/deploy/migrate-dashboard.sh
#
# Optional env:
#   DASHBOARD_IMAGE_TAG — image tag to run the migration with.
#     Defaults to `v1.0.0` (D-13: first cut release, immutable tag).
#     Override only if hot-fixing a migration in a patch release.
#   N8N_HOST            — SSH alias for the prod host. Default `n8n-ia-vm`.
#   DOCKER_NETWORK      — Docker overlay network on n8n-ia-vm where the
#                          intra Traefik discovers services. Default `intra`.
#
# Threat model (T-10-02-02, T-10-02-05 — see PLAN.md):
#   - The migration is the canonical Better Auth CLI; it does NOT run any
#     destructive statement. The plan's verify step greps the script body
#     for any deletion / truncation keyword to confirm none are present.
#   - DASHBOARD_IMAGE_TAG pins to `v1.0.0` (the immutable D-13 first
#     release). Two operators running this script at different points in
#     time get bit-identical CLI behaviour.
#   - The DSN is passed as a `-e` env var to the ephemeral container; it
#     is visible to `docker inspect` for the lifetime of the container,
#     which is bounded to a single CLI invocation (~seconds) and then
#     the container is removed via `--rm`. Acceptable for a one-shot
#     migration.
#
# Usage:
#   DASHBOARD_DATABASE_URL='postgres://doadmin:<PASS>@…/bd_ai_dashboard_prod?...' \
#     ./scripts/deploy/migrate-dashboard.sh
#
# Exit codes:
#   0 — migration ran AND the 4 expected dashboard_auth tables now exist
#   1 — env validation FAIL, migration FAIL, or sanity probe FAIL

set -euo pipefail

# --- helpers --------------------------------------------------------------
log() { printf '[%s] [migrate-dashboard] %s\n' "$(date -Iseconds)" "$*" >&2; }

# --- defaults --------------------------------------------------------------
DASHBOARD_IMAGE_TAG="${DASHBOARD_IMAGE_TAG:-v1.0.0}"
DASHBOARD_IMAGE_REPO="${DASHBOARD_IMAGE_REPO:-ghcr.io/ifixtelecom/ifix-ai-dashboard}"
N8N_HOST="${N8N_HOST:-n8n-ia-vm}"
DOCKER_NETWORK="${DOCKER_NETWORK:-intra}"

DASHBOARD_IMAGE="${DASHBOARD_IMAGE_REPO}:${DASHBOARD_IMAGE_TAG}"

# --- prereq: required env -------------------------------------------------
if [[ -z "${DASHBOARD_DATABASE_URL:-}" ]]; then
  log "FATAL: DASHBOARD_DATABASE_URL env var is required (not set)."
  log ""
  log "  This must be the prod DSN pointing at bd_ai_dashboard_prod, e.g."
  log "  the value emitted by scripts/deploy/bootstrap-postgres.sh:"
  log "    postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_dashboard_prod?sslmode=require&options=-c%20search_path%3Ddashboard_auth"
  log ""
  log "  Easiest way to source it safely from the prod .env:"
  log "    set -a; source /opt/ai-gateway-prod/.env; set +a"
  log "    DASHBOARD_DATABASE_URL=\"\$DASHBOARD_DATABASE_URL\" $0"
  log ""
  log "  WARNING: this script MUST run AFTER scripts/deploy/bootstrap-postgres.sh"
  log "  has created the database bd_ai_dashboard_prod."
  exit 1
fi

# --- prereq: ssh alias reachable ------------------------------------------
# Cheap probe so we fail fast on a typo in N8N_HOST or a stale ~/.ssh/config.
# `BatchMode=yes` so the probe never prompts interactively.
if ! ssh -o BatchMode=yes -o ConnectTimeout=5 "$N8N_HOST" 'echo ok' >/dev/null 2>&1; then
  log "FATAL: cannot ssh to '$N8N_HOST'. Check ~/.ssh/config + key authorization."
  exit 1
fi

# --- step 1: run the Better Auth migration --------------------------------
# Better Auth's `cli migrate` is idempotent — it inspects the live schema and
# only adds the missing tables/columns/indexes. Re-running on an already-
# migrated DB is a no-op (per Better Auth 1.4 CLI semantics).
#
# `--yes` accepts the diff plan without an interactive prompt (the CLI is
# friendly enough to ask "apply these N changes?" by default; we need a
# non-interactive run from an autonomous-style script).
#
# `--network ${DOCKER_NETWORK}` joins the ephemeral container to the prod
# overlay so DO Postgres egress traverses the same NAT path as the prod
# gateway. 162.55.92.154 is already in DO Trusted Sources (CLAUDE.md
# Hetzner topology) → no additional whitelist needed.
log "step 1/2: running '@better-auth/cli migrate --yes' against bd_ai_dashboard_prod"
log "  image:   ${DASHBOARD_IMAGE}"
log "  network: ${DOCKER_NETWORK} (on ${N8N_HOST})"

set +e
ssh "$N8N_HOST" "docker run --rm \
  --network ${DOCKER_NETWORK} \
  -e DASHBOARD_DATABASE_URL='${DASHBOARD_DATABASE_URL}' \
  ${DASHBOARD_IMAGE} \
  npx @better-auth/cli migrate --yes" >&2
MIGRATE_RC=$?
set -e

if [[ "$MIGRATE_RC" -ne 0 ]]; then
  log "FATAL: Better Auth migration FAILED (exit $MIGRATE_RC) — investigate before retrying."
  log "  Common root causes (RESEARCH §How To #3 + Pitfall 2):"
  log "    1. DASHBOARD_DATABASE_URL does NOT point at bd_ai_dashboard_prod"
  log "       (verify with: psql \"\$DASHBOARD_DATABASE_URL\" -tAc 'SELECT current_database()')."
  log "    2. Image tag ${DASHBOARD_IMAGE_TAG} not yet published to GHCR"
  log "       (verify with: ssh ${N8N_HOST} docker manifest inspect ${DASHBOARD_IMAGE})."
  log "    3. DO Trusted Sources missing 162.55.92.154 (egress IP of ${N8N_HOST})."
  log "    4. Database ${DASHBOARD_IMAGE} not yet created — re-run"
  log "       scripts/deploy/bootstrap-postgres.sh first."
  exit 1
fi

log "step 1/2 PASS: Better Auth CLI exited 0"

# --- step 2: sanity probe — 4 expected dashboard_auth tables --------------
# Verify the migration produced the exact table set we expect. We probe with
# a one-shot postgres:16-alpine container on the same overlay because the
# dashboard image does not ship psql, and we want a probe that does not depend
# on operator-side tools.
#
# Expected output: a single line "4" — the count of {user, session, account,
# verification} tables in the dashboard_auth schema.
log "step 2/2: sanity probe — counting dashboard_auth.{user,session,account,verification} tables"

set +e
COUNT=$(ssh "$N8N_HOST" "docker run --rm \
  --network ${DOCKER_NETWORK} \
  -e DASHBOARD_DATABASE_URL='${DASHBOARD_DATABASE_URL}' \
  postgres:16-alpine psql \"\$DASHBOARD_DATABASE_URL\" -tAc \
    \"SELECT count(*) FROM information_schema.tables WHERE table_schema='dashboard_auth' AND table_name IN ('user','session','account','verification')\"" 2>/dev/null)
PROBE_RC=$?
set -e

if [[ "$PROBE_RC" -ne 0 ]]; then
  log "FATAL: sanity probe container exited $PROBE_RC. The migration step succeeded,"
  log "       but verification could not run — check Docker daemon on $N8N_HOST."
  exit 1
fi

# Normalize whitespace from the docker output.
COUNT="$(printf '%s' "$COUNT" | tr -d '[:space:]')"
if [[ "$COUNT" != "4" ]]; then
  log "FATAL: sanity probe expected 4 tables, got '$COUNT'."
  log "       Better Auth CLI exited 0 but the dashboard_auth schema is incomplete."
  log "       Inspect the schema with:"
  log "         ssh $N8N_HOST docker run --rm --network ${DOCKER_NETWORK} \\"
  log "           -e DASHBOARD_DATABASE_URL='\${DASHBOARD_DATABASE_URL}' \\"
  log "           postgres:16-alpine psql \"\\\$DASHBOARD_DATABASE_URL\" -c '\\dt dashboard_auth.*'"
  exit 1
fi

log "step 2/2 PASS: 4 dashboard_auth tables present (user, session, account, verification)"
log ""
log "============================================================================"
log "  DASHBOARD MIGRATION COMPLETE"
log "============================================================================"
log ""
log "  bd_ai_dashboard_prod is ready for first operator signup via"
log "    https://ai-dashboard.converse-ai.app"
log ""
log "  Next step (Plan 10-06 HUMAN-UAT Step 6+): bring up the prod compose stack"
log "  on $N8N_HOST and verify dashboard signup populates dashboard_auth.user."
log "============================================================================"
