#!/usr/bin/env bash
# scripts/deploy/bootstrap-postgres.sh — Phase 10 Wave 1 (Plan 10-02) idempotent
# CREATE DATABASE wrapper for the two NEW prod databases on the existing
# DigitalOcean managed Postgres instance.
#
# Implementable form of D-05 + D-06 (RESEARCH §How To #3 + Pitfall 2):
#   The gateway hardcodes the Postgres schema name `ai_gateway` in 27 migration
#   files + gateway/internal/db/pool.go:35 (`SET search_path = ai_gateway, public`)
#   + every sqlc-generated query (`ai_gateway.api_key_status`, `ai_gateway.data_class`,
#   …). Renaming the schema is out of scope for Phase 10 (Phase 11+ refactor).
#   Production isolation is therefore achieved by a NEW DATABASE per environment,
#   reusing the same hardcoded schema name inside each database:
#     - bd_ai_gateway_prod    — gateway code path (schema `ai_gateway`)
#     - bd_ai_dashboard_prod  — dashboard Better Auth tables (schema `dashboard_auth`,
#                                created later by `scripts/deploy/migrate-dashboard.sh`)
#
# Idempotency:
#   - Probes `pg_database` for the target DB name BEFORE issuing CREATE DATABASE.
#   - A second invocation prints "already exists — skipping" for each DB and
#     still runs the sanity probe + final summary. Safe to re-run.
#   - NO destructive operations. The script never issues a deletion or
#     truncation statement, and never mutates roles. It is read-mostly plus
#     2 idempotent CREATE-DATABASE statements.
#
# Threat model (T-10-02-01, T-10-02-02, T-10-02-03 — see PLAN.md):
#   - DO_ADMIN_DSN carries the doadmin password. Script NEVER echoes the DSN to
#     stdout or stderr (operator should also `unset HISTFILE` or use a sourced
#     credentials file). Only DB names + probe results are logged.
#   - Both DB names are constants; the operator cannot pass a "DB to create"
#     parameter — eliminates injection risk via env var.
#   - Idempotent SELECT-then-CREATE path prevents accidental overwrite if a DB
#     with the same name already exists (the script does NOT inspect contents).
#
# Required env (validated at startup, fail-fast):
#   DO_ADMIN_DSN — superuser DSN against the DO managed instance, e.g.
#     postgres://doadmin:PASS@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/defaultdb?sslmode=require
#   Operator supplies this at runtime from password manager — NEVER committed.
#
# Prerequisites:
#   - psql v15+ on PATH (postgresql-client). Verified via `command -v psql`.
#   - 162.55.92.154 in DO Trusted Sources (already there per CLAUDE.md Hetzner
#     topology — egress NAT IP of the host hosting ops-claude / n8n-ia-vm).
#
# Usage:
#   DO_ADMIN_DSN='postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/defaultdb?sslmode=require' \
#     ./scripts/deploy/bootstrap-postgres.sh
#
# Exit codes:
#   0 — both DBs exist (created if missing, skipped if present), both probes PASS
#   1 — DO_ADMIN_DSN missing, psql missing, connectivity FAIL, or sanity probe FAIL

set -euo pipefail

# --- helpers --------------------------------------------------------------
# log() writes to stderr to keep stdout clean for the final summary block.
log() { printf '[%s] [bootstrap-postgres] %s\n' "$(date -Iseconds)" "$*" >&2; }

# --- defaults --------------------------------------------------------------
GATEWAY_DB="bd_ai_gateway_prod"
DASHBOARD_DB="bd_ai_dashboard_prod"

# --- prereq: required env -------------------------------------------------
# DO_ADMIN_DSN must point at the postgres admin DB on the DO instance with a
# user that holds CREATEDB (doadmin does, per Assumption A5 in RESEARCH).
# Fail fast with a SPECIFIC pointer rather than a bare "missing" — the operator
# might confuse this with the gateway's AI_GATEWAY_PG_DSN or with the
# dashboard's DASHBOARD_DATABASE_URL.
if [[ -z "${DO_ADMIN_DSN:-}" ]]; then
  log "FATAL: DO_ADMIN_DSN env var is required (not set)."
  log ""
  log "  This must be the doadmin DSN pointing at the DO managed instance's"
  log "  admin database, e.g.:"
  log "    postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/defaultdb?sslmode=require"
  log ""
  log "  NEVER commit this value. Source it from your password manager and"
  log "  consider \`unset HISTFILE\` before invoking this script so the DSN does"
  log "  not land in shell history (threat T-10-02-01)."
  exit 1
fi

# --- prereq: psql binary --------------------------------------------------
if ! command -v psql >/dev/null 2>&1; then
  log "FATAL: psql is not on PATH. Install postgresql-client v15+ first."
  log "  Debian/Ubuntu: sudo apt install postgresql-client"
  exit 1
fi

# --- helpers --------------------------------------------------------------
# admin_psql() runs psql against the DO admin DB (defaultdb) using the
# operator-supplied DSN. Wraps `psql "$DO_ADMIN_DSN" "$@"` so the DSN never
# appears in the script's flow control or in `set -x` traces.
admin_psql() {
  psql "$DO_ADMIN_DSN" "$@"
}

# rewrite_dsn_db() takes the admin DSN and rewrites the database path segment
# to a different database name (for the post-create sanity probe). The DSN
# format is:
#   postgres://USER:PASS@HOST:PORT/DBNAME?query
# We only touch the /DBNAME portion. Anchor on `://`, then preserve everything
# up to the LAST `/` before the `?`, then swap DBNAME, then re-append `?query`.
# Pure bash — no eval, no command substitution that could surface the DSN.
rewrite_dsn_db() {
  local dsn="$1" new_db="$2"
  # Split DSN at `?` so the query string is preserved verbatim.
  local prefix query
  if [[ "$dsn" == *'?'* ]]; then
    prefix="${dsn%%\?*}"
    query="?${dsn#*\?}"
  else
    prefix="$dsn"
    query=""
  fi
  # Strip the trailing /DBNAME from prefix; bash parameter expansion only.
  local base="${prefix%/*}"
  printf '%s/%s%s' "$base" "$new_db" "$query"
}

# ensure_database() is the heart of the script. For a given DB name:
#   1) Probe pg_database for an existing row.
#   2) If absent → issue CREATE DATABASE.
#   3) If present → skip with a log line.
#   4) Either way, run a sanity probe against the target DB to confirm it is
#      reachable and that `current_database()` matches.
#
# The function is idempotent + side-effect-free on re-run for existing DBs.
ensure_database() {
  local db="$1"

  log "checking '$db' on DO managed instance"
  # -tAc → tuples only, unaligned output, single command. Returns "1" if the
  # database row exists, empty otherwise. The DSN is passed via env to psql so
  # the connection happens against the admin DB. We do NOT use `\l` because
  # that is a meta-command and is harder to parse robustly.
  local existing
  existing="$(admin_psql -tAc "SELECT 1 FROM pg_database WHERE datname='${db}'")"

  if [[ -z "$existing" ]]; then
    log "creating database '$db' (no existing row in pg_database)"
    # The DB name is a constant in this script (not operator-supplied), so the
    # double-quoted identifier is safe. CREATE DATABASE cannot run inside a
    # transaction block on Postgres, so we issue it as a single statement.
    admin_psql -c "CREATE DATABASE \"${db}\""
    log "database '$db' created"
  else
    log "database '$db' already exists — skipping CREATE (Pitfall 2: idempotent path)"
  fi

  # Post-create sanity probe: reconnect to the target DB, run SELECT
  # current_database(), assert the returned value matches the requested DB
  # name. This catches the case where DO created the DB under an unexpected
  # name (we have never seen this happen, but the probe is cheap insurance).
  local target_dsn
  target_dsn="$(rewrite_dsn_db "$DO_ADMIN_DSN" "$db")"
  local current
  # The DSN is passed via env to psql; never echoed.
  current="$(psql "$target_dsn" -tAc "SELECT current_database()")"
  if [[ "$current" != "$db" ]]; then
    log "FATAL: sanity probe for '$db' returned current_database='$current' (expected '$db')"
    exit 1
  fi
  log "sanity probe PASS: '$db' is reachable and current_database() = '$db'"
}

# --- main -----------------------------------------------------------------
main() {
  log "starting prod Postgres bootstrap (D-05 + D-06 implementable form)"
  log "target databases: ${GATEWAY_DB}, ${DASHBOARD_DB}"
  log "schema names inside each DB are HARDCODED (ai_gateway / dashboard_auth) — not configurable"

  ensure_database "$GATEWAY_DB"
  ensure_database "$DASHBOARD_DB"

  log "all probes PASS — printing operator hand-off block to stdout"

  # Final summary block: prints the two DSN templates the operator must paste
  # into /opt/ai-gateway-prod/.env. We use the literal placeholder `<PASS>`
  # rather than echoing the password from DO_ADMIN_DSN — the operator already
  # holds the password in their password manager. Suppressing the password
  # here closes T-10-02-01 (DSN in shell history / log file leakage).
  cat <<EOF

============================================================================
  PROD POSTGRES BOOTSTRAP COMPLETE
============================================================================

Two databases now exist on the DO managed instance:

  - ${GATEWAY_DB}   (gateway schema = ai_gateway, populated by goose
                                  on first deploy with AI_GATEWAY_MIGRATE_ON_BOOT=true)
  - ${DASHBOARD_DB} (dashboard schema = dashboard_auth, populated by
                                  scripts/deploy/migrate-dashboard.sh after this)

Paste these two lines into /opt/ai-gateway-prod/.env on n8n-ia-vm
(replace <PASS> with the doadmin password from your password manager):

  AI_GATEWAY_PG_DSN=postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/${GATEWAY_DB}?sslmode=require
  DASHBOARD_DATABASE_URL=postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/${DASHBOARD_DB}?sslmode=require&options=-c%20search_path%3Ddashboard_auth

Next step (Plan 10-06 HUMAN-UAT Step 5):
  DASHBOARD_DATABASE_URL='<above>' ./scripts/deploy/migrate-dashboard.sh

============================================================================
EOF
}

main "$@"
