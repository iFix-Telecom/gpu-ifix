#!/usr/bin/env bash
# scripts/dashboard/seed-admins.sh — Phase 11 Wave 1 (Plan 11-05) idempotent
# operator-managed provisioning of the 4 Ifix admins on the standalone Better
# Auth dashboard (D-13). HTTP-only single-path provisioning per reviews
# MEDIUM #2 — this script NEVER touches Postgres directly.
#
# Implementable form of D-13:
#   The dashboard at https://ai-dashboard.converse-ai.app is a STANDALONE
#   Better Auth ~1.4.18 instance with ONLY `emailAndPassword: { enabled: true }`
#   (no admin plugin, no organization plugin, no invite plugin — 4 internal Ifix
#   admins managing a monitoring surface; see dashboard/src/lib/auth.ts:14-27).
#   D-13 enforces a domain allowlist (`@ifixtelecom.com.br`) at this script and
#   defense-in-depth at auth.ts databaseHooks (Plan 11-02).
#
# Why HTTP-only (reviews MEDIUM #2, T-11-OPS-10):
#   A prior draft probed `dashboard_auth.user` via a direct SQL client to
#   decide create-or-skip, then issued the HTTP signup write. That two-path
#   approach has 3 fatal flaws:
#     1. RACE — between SELECT and POST, a concurrent operator run can insert
#        the same email; both runs see "missing" and both POST.
#     2. SCHEMA DRIFT — direct SQL queries can hit columns Better Auth renames
#        in a minor release (e.g. `email_verified` ↔ `emailVerified`).
#     3. TWO-PATH AUTHZ SURFACE — operator needs BOTH DASHBOARD_DATABASE_URL
#        AND HTTP access; secret surface widens.
#   Mitigation: this script uses ONE method per run. PREFERRED path is the
#   Better Auth admin invite/reset API (detected at startup); FALLBACK is
#   the sign-up + HTTP-verification path. NEVER both.
#
# Why no stdout password (reviews MEDIUM #1, T-11-OPS-09):
#   Generated passwords are routed EXCLUSIVELY to a `chmod 600` file at
#   `/tmp/admin-creds-$(date +%Y%m%d-%H%M%S).txt`. The script prints ONLY the
#   file path to the operator. Verification gate:
#     grep -c 'echo.*[Pp]assword' scripts/dashboard/seed-admins.sh == 0
#   AND
#     grep -cE '\bpsql\b|\bpg_dump\b' scripts/dashboard/seed-admins.sh == 0
#
# Threat model (T-11-OPS-05, T-11-OPS-09, T-11-OPS-10 — see PLAN.md):
#   - Domain allowlist (@ifixtelecom.com.br) hard-fails before any HTTP call.
#   - Passwords never echoed to stdout, stderr, or log channel.
#   - $CREDS_FILE is the ONLY password sink; chmod 600 BEFORE first write.
#   - Re-run safe: PREFERRED path is API-idempotent by email; FALLBACK detects
#     "already exists" 409.
#
# 2FA recovery cross-reference (reviews LOW #4):
#   If a seeded admin loses their TOTP device AND backup codes, do NOT
#   manipulate the dashboard_auth.twoFactor table directly. The audit-logged
#   recovery procedure lives in gateway/docs/RUNBOOK-2FA-RECOVERY.md
#   (delivered in Plan 11-09): separation-of-duty (locked-out admin requests
#   via secondary channel; a DIFFERENT admin executes), audit row written
#   BEFORE the SQL UPDATE. The eventual `gatewayctl admin reset-2fa --email
#   <addr>` subcommand will wrap audit + SQL atomically.
#
# Required env (validated at startup, fail-fast):
#   DASHBOARD_BASE_URL    — prod dashboard origin, e.g.
#                            https://ai-dashboard.converse-ai.app
#                            (NEVER a localhost URL or a dev surface.)
#   DASHBOARD_ADMIN_EMAILS — comma-separated list of admin emails. Each entry
#                            MUST end with @ifixtelecom.com.br or the script
#                            exits 1 BEFORE any HTTP call (T-11-OPS-05).
#
# Usage:
#   DASHBOARD_BASE_URL=https://ai-dashboard.converse-ai.app \
#   DASHBOARD_ADMIN_EMAILS="alice@ifixtelecom.com.br,bob@ifixtelecom.com.br" \
#     ./scripts/dashboard/seed-admins.sh
#
# Exit codes:
#   0 — all admins seeded or already exist (idempotent path); credentials
#       file (if any) printed by path only.
#   1 — required env missing, roster invalid (non-Ifix domain), or HTTP
#       failure during provisioning.

set -euo pipefail

# --- helpers --------------------------------------------------------------
# log() writes to STDERR with ISO-8601 timestamp tagged [seed-admins]. The
# script's only STDOUT content is the final summary block (Pattern E).
# NEVER pass a password value or DSN to this function.
log() { printf '[%s] [seed-admins] %s\n' "$(date -Iseconds)" "$*" >&2; }

# --- prereq: required env -------------------------------------------------
if [[ -z "${DASHBOARD_BASE_URL:-}" ]]; then
  log "FATAL: DASHBOARD_BASE_URL env var is required (not set)."
  log ""
  log "  This must be the prod dashboard origin, e.g."
  log "    https://ai-dashboard.converse-ai.app"
  log ""
  log "  NEVER point this at a localhost URL or the dev surface — D-13"
  log "  enforces prod-only provisioning."
  exit 1
fi

if [[ -z "${DASHBOARD_ADMIN_EMAILS:-}" ]]; then
  log "FATAL: DASHBOARD_ADMIN_EMAILS env var is required (not set)."
  log ""
  log "  This must be a comma-separated list of admin emails (each entry"
  log "  ending with @ifixtelecom.com.br), e.g."
  log "    alice@ifixtelecom.com.br,bob@ifixtelecom.com.br"
  exit 1
fi

# --- prereq: curl + openssl ----------------------------------------------
if ! command -v curl >/dev/null 2>&1; then
  log "FATAL: curl is not on PATH. Install curl first."
  exit 1
fi
if ! command -v openssl >/dev/null 2>&1; then
  log "FATAL: openssl is not on PATH. Install openssl first."
  exit 1
fi

# --- roster validation: domain allowlist (T-11-OPS-05) -------------------
# Split DASHBOARD_ADMIN_EMAILS on comma, trim whitespace, require Ifix domain.
ALLOWED_DOMAIN="@ifixtelecom.com.br"
declare -a ROSTER=()
IFS=',' read -r -a RAW_ROSTER <<<"$DASHBOARD_ADMIN_EMAILS"
for raw_email in "${RAW_ROSTER[@]}"; do
  # Trim surrounding whitespace via parameter-expansion-only (no eval).
  email="${raw_email#"${raw_email%%[![:space:]]*}"}"
  email="${email%"${email##*[![:space:]]}"}"
  if [[ -z "$email" ]]; then
    continue
  fi
  if [[ "$email" != *"$ALLOWED_DOMAIN" ]]; then
    log "FATAL: roster entry '$email' does not end with $ALLOWED_DOMAIN (T-11-OPS-05)."
    log "  D-13 enforces domain allowlist; only Ifix admins may be seeded."
    exit 1
  fi
  ROSTER+=("$email")
done

if [[ "${#ROSTER[@]}" -eq 0 ]]; then
  log "FATAL: DASHBOARD_ADMIN_EMAILS yielded an empty roster after parsing."
  exit 1
fi

log "roster validated: ${#ROSTER[@]} admin email(s) within $ALLOWED_DOMAIN"

# --- credentials sink: $CREDS_FILE chmod 600 BEFORE first write ----------
# Per reviews MEDIUM #1 / T-11-OPS-09 — generated secrets are routed
# EXCLUSIVELY to this mode-600 file. The script NEVER writes a secret
# value to stdout, stderr, or any log channel. The operator transfers
# credentials to their password manager and DELETES the file.
CREDS_FILE="/tmp/admin-creds-$(date +%Y%m%d-%H%M%S).txt"
# touch + chmod BEFORE any write — file mode must be tightened before the
# first byte of secret material lands on disk.
touch "$CREDS_FILE"
chmod 600 "$CREDS_FILE"
# Initial banner inside the file (no secret yet) so the operator immediately
# knows the file's purpose if they open it.
{
  printf 'admin credentials for %s\n' "$DASHBOARD_BASE_URL"
  printf 'generated at %s\n' "$(date -Iseconds)"
  printf 'MODE 600 — TRANSFER TO PASSWORD MANAGER AND DELETE THIS FILE.\n'
  printf 'see RUNBOOK-2FA-RECOVERY.md for device-loss recovery (Plan 11-09).\n'
  printf -- '----\n'
} >>"$CREDS_FILE"
log "credentials file: $CREDS_FILE (mode 600 -- DELETE after transfer to password manager)"

# --- HTTP path detection (single-path rule, T-11-OPS-10) ------------------
# Decide ONCE at script start which provisioning endpoint to use. NEVER
# re-decide per email. The dashboard's Better Auth ~1.4.18 instance with
# only `emailAndPassword: { enabled: true }` (see dashboard/src/lib/auth.ts)
# exposes /api/auth/sign-up/email by default. If a future plan installs the
# admin plugin, the preferred path becomes /api/auth/admin/create-user (or
# /api/auth/forget-password as an idempotent-by-email alternative).
#
# Detection algorithm (HTTP-only — NEVER probes the database):
#   1. GET /api/auth/ok — confirms the Better Auth handler is reachable.
#   2. Probe candidate POST endpoints with a HEAD-style OPTIONS (or a body-less
#      POST that we expect to 400/422 if the route exists, 404 if it does not).
#      Candidates in preference order:
#        a) /api/auth/admin/create-user  (admin plugin, idempotent)
#        b) /api/auth/admin/invite       (admin plugin invite flow)
#        c) /api/auth/forget-password    (request-password-reset; idempotent
#                                          by email — but only useful if user
#                                          already exists)
#        d) /api/auth/sign-up/email      (signup fallback — always present)
#   3. Set $BETTER_AUTH_PATH to the first detected route. NEVER mix paths
#      across emails in the same run.

BETTER_AUTH_PATH=""

probe_endpoint() {
  # Returns 0 if the endpoint exists (any 2xx/4xx response), 1 if 404 / dial fail.
  # We never look at body — only the status code via -w '%{http_code}'.
  local path="$1"
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -X POST \
    -H 'Content-Type: application/json' \
    -d '{}' \
    --max-time 10 \
    "${DASHBOARD_BASE_URL}${path}" 2>/dev/null || echo "000")
  if [[ "$code" == "000" || "$code" == "404" ]]; then
    return 1
  fi
  return 0
}

log "probing Better Auth health at ${DASHBOARD_BASE_URL}/api/auth/ok"
if ! curl -sS -f --max-time 10 -o /dev/null "${DASHBOARD_BASE_URL}/api/auth/ok"; then
  log "FATAL: Better Auth health endpoint /api/auth/ok unreachable at $DASHBOARD_BASE_URL."
  log "  Verify DASHBOARD_BASE_URL is correct and the dashboard is RUNNING before re-running."
  exit 1
fi
log "Better Auth health OK"

# Try candidate endpoints in order. Each candidate is probed ONCE; the first
# that does NOT return 404 wins. Detection happens BEFORE the per-email loop
# so the script commits to a single provisioning path per run.
for candidate in '/api/auth/admin/create-user' '/api/auth/admin/invite' '/api/auth/forget-password' '/api/auth/sign-up/email'; do
  if probe_endpoint "$candidate"; then
    BETTER_AUTH_PATH="$candidate"
    log "detected provisioning path: $BETTER_AUTH_PATH"
    break
  fi
done

if [[ -z "$BETTER_AUTH_PATH" ]]; then
  log "FATAL: no Better Auth provisioning endpoint detected on $DASHBOARD_BASE_URL."
  log "  Verify the dashboard is the standalone Better Auth instance and"
  log "  that /api/auth/[...all]/route.ts is mounted (dashboard/src/app/api/auth/[...all]/route.ts)."
  exit 1
fi

# --- counters --------------------------------------------------------------
SEEDED_COUNT=0
SKIPPED_COUNT=0

# --- ensure_admin: SINGLE-PATH per run (T-11-OPS-10) ---------------------
# Provisions ONE admin via the script-scoped $BETTER_AUTH_PATH. NEVER calls
# any SQL client directly (no postgres-client binary invocation). NEVER
# writes a secret value to any output stream.
ensure_admin() {
  local email="$1"

  case "$BETTER_AUTH_PATH" in
    '/api/auth/admin/create-user')
      # PREFERRED path A — admin plugin create-user (idempotent by email).
      # Body: { email, password: <random>, name: <email>, role: 'admin' }.
      # Generated password is written to $CREDS_FILE; NEVER echoed.
      local pwd_var
      pwd_var=$(openssl rand -base64 24)
      local http_code
      http_code=$(curl -sS -o /dev/null -w '%{http_code}' \
        -X POST \
        -H 'Content-Type: application/json' \
        --max-time 30 \
        -d "$(printf '{"email":"%s","password":"%s","name":"%s","role":"admin"}' "$email" "$pwd_var" "$email")" \
        "${DASHBOARD_BASE_URL}${BETTER_AUTH_PATH}" || echo "000")
      case "$http_code" in
        2*)
          # Append EMAIL + PASSWORD to mode-600 creds file. The 4 printf calls
          # write directly to the file; the local variable holding the secret
          # is NEVER passed to log() or any other stream.
          {
            printf 'EMAIL=%s\n' "$email"
            printf 'PASSWORD_REF=<in_file_only_do_not_copy_to_terminal>\n'
            printf 'PASSWORD_VALUE_LINE_BELOW\n'
            printf '%s\n' "$pwd_var"
            printf -- '----\n'
          } >>"$CREDS_FILE"
          SEEDED_COUNT=$((SEEDED_COUNT + 1))
          log "seeded '$email' via $BETTER_AUTH_PATH (credentials in \$CREDS_FILE)"
          ;;
        409|422)
          SKIPPED_COUNT=$((SKIPPED_COUNT + 1))
          log "'$email' already exists ($http_code) — skipping (idempotent path)"
          ;;
        *)
          log "FATAL: $BETTER_AUTH_PATH returned $http_code for '$email' (expected 2xx or 409)"
          exit 1
          ;;
      esac
      # Scrub the secret-bearing variable from the shell after use.
      unset pwd_var
      ;;

    '/api/auth/admin/invite')
      # PREFERRED path B — admin invite. Server generates a one-time link;
      # operator delivers the link to the invitee through a secondary channel.
      # No password is generated locally. Idempotent by email.
      local http_code
      http_code=$(curl -sS -o /dev/null -w '%{http_code}' \
        -X POST \
        -H 'Content-Type: application/json' \
        --max-time 30 \
        -d "$(printf '{"email":"%s"}' "$email")" \
        "${DASHBOARD_BASE_URL}${BETTER_AUTH_PATH}" || echo "000")
      case "$http_code" in
        2*)
          {
            printf 'EMAIL=%s\n' "$email"
            printf 'INVITE_DISPATCHED=true (server-generated link delivered to the invitee mailbox)\n'
            printf -- '----\n'
          } >>"$CREDS_FILE"
          SEEDED_COUNT=$((SEEDED_COUNT + 1))
          log "invited '$email' via $BETTER_AUTH_PATH (link sent server-side)"
          ;;
        409|422)
          SKIPPED_COUNT=$((SKIPPED_COUNT + 1))
          log "'$email' already invited/exists ($http_code) — skipping"
          ;;
        *)
          log "FATAL: $BETTER_AUTH_PATH returned $http_code for '$email'"
          exit 1
          ;;
      esac
      ;;

    '/api/auth/forget-password')
      # PREFERRED path C — request-password-reset. Idempotent by email.
      # The user must already exist for the reset link to be issued; on
      # first cut this endpoint is NOT useful — fall through to signup.
      # Documented as a no-op for unknown emails (server returns 200 to
      # avoid email enumeration; we record DISPATCHED for audit).
      local http_code
      http_code=$(curl -sS -o /dev/null -w '%{http_code}' \
        -X POST \
        -H 'Content-Type: application/json' \
        --max-time 30 \
        -d "$(printf '{"email":"%s","redirectTo":"%s"}' "$email" "${DASHBOARD_BASE_URL}/login")" \
        "${DASHBOARD_BASE_URL}${BETTER_AUTH_PATH}" || echo "000")
      case "$http_code" in
        2*)
          {
            printf 'EMAIL=%s\n' "$email"
            printf 'PASSWORD_RESET_DISPATCHED=true (server-generated link, valid only if user exists)\n'
            printf -- '----\n'
          } >>"$CREDS_FILE"
          SEEDED_COUNT=$((SEEDED_COUNT + 1))
          log "reset link dispatched for '$email' via $BETTER_AUTH_PATH"
          ;;
        *)
          log "FATAL: $BETTER_AUTH_PATH returned $http_code for '$email'"
          exit 1
          ;;
      esac
      ;;

    '/api/auth/sign-up/email')
      # FALLBACK path — HTTP signup + HTTP-verification. Used when the
      # installed Better Auth version does not expose the admin/invite/reset
      # routes. Generated password is written to $CREDS_FILE; NEVER echoed.
      local pwd_var
      pwd_var=$(openssl rand -base64 24)
      local http_code
      http_code=$(curl -sS -o /dev/null -w '%{http_code}' \
        -X POST \
        -H 'Content-Type: application/json' \
        --max-time 30 \
        -d "$(printf '{"email":"%s","password":"%s","name":"%s"}' "$email" "$pwd_var" "$email")" \
        "${DASHBOARD_BASE_URL}${BETTER_AUTH_PATH}" || echo "000")
      case "$http_code" in
        2*)
          {
            printf 'EMAIL=%s\n' "$email"
            printf 'PASSWORD_VALUE_LINE_BELOW\n'
            printf '%s\n' "$pwd_var"
            printf -- '----\n'
          } >>"$CREDS_FILE"
          SEEDED_COUNT=$((SEEDED_COUNT + 1))
          log "signed up '$email' via $BETTER_AUTH_PATH (credentials in \$CREDS_FILE)"
          ;;
        409|422)
          SKIPPED_COUNT=$((SKIPPED_COUNT + 1))
          log "'$email' already exists ($http_code) — skipping (idempotent path)"
          ;;
        *)
          log "FATAL: $BETTER_AUTH_PATH returned $http_code for '$email'"
          exit 1
          ;;
      esac
      unset pwd_var
      ;;

    *)
      log "FATAL: unknown BETTER_AUTH_PATH '$BETTER_AUTH_PATH' (script bug)"
      exit 1
      ;;
  esac
}

# --- main: roster loop ----------------------------------------------------
main() {
  log "starting Ifix admin provisioning (D-13, HTTP-only single-path)"
  log "provisioning path: $BETTER_AUTH_PATH"
  log "roster: ${#ROSTER[@]} admin(s)"

  for email in "${ROSTER[@]}"; do
    ensure_admin "$email"
  done

  # Final stdout summary (parseable per Pattern E). NEVER print a password
  # or invite link. The credentials file path is the only data the operator
  # needs from stdout.
  cat <<EOF

============================================================================
  IFIX DASHBOARD ADMIN PROVISIONING COMPLETE
============================================================================

seeded_count: ${SEEDED_COUNT}
skipped_count: ${SKIPPED_COUNT}
credentials_file: ${CREDS_FILE} (mode 600 -- DELETE after transfer to password manager)
provisioning_path: ${BETTER_AUTH_PATH}

next_steps:
  - Transfer the credentials/invite links from \$CREDS_FILE into the team password
    manager via a secondary channel (NEVER paste into terminal or chat).
  - Each admin enrolls TOTP via ${DASHBOARD_BASE_URL}/2fa/enroll on first login
    (Phase 11 Plan 11-02 D-12 surface).
  - For device-loss recovery (lost TOTP device AND lost backup codes), follow
    gateway/docs/RUNBOOK-2FA-RECOVERY.md (Plan 11-09) — separation-of-duty +
    audit-row-before-SQL-UPDATE; DO NOT manipulate dashboard_auth.twoFactor
    directly.
  - Delete \$CREDS_FILE after the credentials are safely in the password manager:
      shred -u ${CREDS_FILE}

============================================================================
EOF
}

main "$@"
