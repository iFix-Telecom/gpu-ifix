#!/usr/bin/env bash
# scripts/integration-smoke/provision-tenants.sh — idempotent Phase-8 tenant seed.
#
# Wraps the compiled `gatewayctl` CLI (tenant create / key create /
# admin-key create) to provision the two Phase-8 client tenants in the
# gateway DB: `converseai` (covers converseai-v4 api + agents) and
# `chat-ifix`. Both data_class=normal (08-CONTEXT.md `## Decisions`).
#
# Idempotency:
#   - `gatewayctl tenant create` is idempotent here: a "slug already exists"
#     stderr + exit 1 is treated as success (the tenant is already
#     provisioned) — the script continues.
#   - `gatewayctl key create` / `admin-key create` are NOT idempotent: every
#     call mints a NEW row. The key-mint steps are therefore gated behind the
#     explicit `--mint-keys` opt-in flag. Run the script once WITHOUT it to
#     create the tenants, then once WITH `--mint-keys` to mint the keys.
#
# Secrets: raw API keys are printed to stdout EXACTLY ONCE via a final
# instructions block. They are NEVER passed to log() (which writes to stderr,
# which an operator may redirect to a file). The keys are not re-derivable.
#
# Usage:
#   AI_GATEWAY_PG_DSN=postgres://... \
#     ./scripts/integration-smoke/provision-tenants.sh \
#       [--gatewayctl PATH] [--mint-keys] [--dry-run]
#
#   --gatewayctl PATH  path to the compiled gatewayctl binary, or a wrapper
#                      such as a `docker exec ifix-ai-gateway /gatewayctl`
#                      shim (default: `gatewayctl` on PATH)
#   --mint-keys        opt-in: also mint the 2 tenant API keys + the dashboard
#                      admin key (NON-idempotent — pass exactly once)
#   --dry-run          print the gatewayctl commands that WOULD run, execute
#                      nothing, touch no DB
#
# Env:
#   AI_GATEWAY_PG_DSN (required) — Postgres DSN the wrapped gatewayctl reads
#                                  to reach the gateway DB (loadAndPool /
#                                  gateway/internal/config/config.go)

set -euo pipefail

log() { printf '[%s] [provision-tenants] %s\n' "$(date -Iseconds)" "$*" >&2; }

# --- required env ---------------------------------------------------------
: "${AI_GATEWAY_PG_DSN:?missing}"

# --- defaults + arg parse -------------------------------------------------
GATEWAYCTL="gatewayctl"
MINT_KEYS=0
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --gatewayctl) GATEWAYCTL="$2"; shift 2;;
    --mint-keys)  MINT_KEYS=1;     shift 1;;
    --dry-run)    DRY_RUN=1;       shift 1;;
    *) log "unknown arg $1"; exit 2;;
  esac
done

# --- prerequisites --------------------------------------------------------
for bin in grep date; do
  command -v "$bin" >/dev/null 2>&1 || { log "missing required binary: $bin"; exit 1; }
done
# The gatewayctl entry may be multi-word (e.g. "docker exec ... /gatewayctl");
# only verify the leading executable.
GATEWAYCTL_BIN="${GATEWAYCTL%% *}"
if [[ "$DRY_RUN" -eq 0 ]]; then
  command -v "$GATEWAYCTL_BIN" >/dev/null 2>&1 \
    || { log "missing gatewayctl executable: $GATEWAYCTL_BIN (pass --gatewayctl PATH)"; exit 1; }
fi

# --- tenant model (08-CONTEXT.md `## Decisions`) --------------------------
# Exactly two Phase-8 tenants, both data_class=normal.
#   slug | display name
TENANT_SLUGS=("converseai" "chat-ifix")
TENANT_NAMES=("ConverseAI v4" "Chat Ifix")
DATA_CLASS="normal"

# --- helpers --------------------------------------------------------------
# run_gatewayctl: echoes the command under --dry-run, otherwise executes it.
# Captures combined stdout+stderr into GW_OUT and the exit code into GW_RC.
GW_OUT=""
GW_RC=0
run_gatewayctl() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '[dry-run] would run: %s %s\n' "$GATEWAYCTL" "$*"
    GW_OUT=""
    GW_RC=0
    return 0
  fi
  set +e
  GW_OUT="$($GATEWAYCTL "$@" 2>&1)"
  GW_RC=$?
  set -e
}

# --- 1) idempotent tenant create (always runs, both tenants) --------------
log "provisioning ${#TENANT_SLUGS[@]} tenants (data_class=${DATA_CLASS})"
for i in "${!TENANT_SLUGS[@]}"; do
  slug="${TENANT_SLUGS[$i]}"
  name="${TENANT_NAMES[$i]}"
  run_gatewayctl tenant create --name "$name" --slug "$slug"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    continue
  fi
  if [[ "$GW_RC" -eq 0 ]]; then
    log "tenant '$slug' created"
  elif [[ "$GW_RC" -eq 1 ]] && printf '%s' "$GW_OUT" | grep -q 'already exists'; then
    # Idempotency signal: re-run on an already-provisioned tenant is OK.
    log "tenant '$slug' already provisioned — OK"
  else
    log "tenant '$slug' create failed (exit $GW_RC): $GW_OUT"
    exit 1
  fi
done

# --- 2) guarded key mint (only under --mint-keys) -------------------------
if [[ "$MINT_KEYS" -eq 0 ]]; then
  log "skipping key mint — 'key create' / 'admin-key create' are NOT idempotent."
  log "re-run ONCE with --mint-keys to mint the tenant keys + dashboard admin key."
  exit 0
fi

log "minting tenant API keys + dashboard admin key (--mint-keys)"

# parse_key: extracts the `key=<raw>` value from a gatewayctl stdout block.
parse_key() {
  printf '%s' "$1" | grep '^key=' | head -n1 | cut -d= -f2-
}

CONVERSEAI_KEY=""
CHAT_IFIX_KEY=""
ADMIN_KEY=""

# converseai tenant key
run_gatewayctl key create --tenant converseai --data-class "$DATA_CLASS"
if [[ "$DRY_RUN" -eq 0 ]]; then
  [[ "$GW_RC" -eq 0 ]] || { log "key create (converseai) failed (exit $GW_RC): $GW_OUT"; exit 1; }
  CONVERSEAI_KEY="$(parse_key "$GW_OUT")"
  [[ -n "$CONVERSEAI_KEY" ]] || { log "key create (converseai): no key= line in output"; exit 1; }
  log "converseai tenant key minted"
fi

# chat-ifix tenant key
run_gatewayctl key create --tenant chat-ifix --data-class "$DATA_CLASS"
if [[ "$DRY_RUN" -eq 0 ]]; then
  [[ "$GW_RC" -eq 0 ]] || { log "key create (chat-ifix) failed (exit $GW_RC): $GW_OUT"; exit 1; }
  CHAT_IFIX_KEY="$(parse_key "$GW_OUT")"
  [[ -n "$CHAT_IFIX_KEY" ]] || { log "key create (chat-ifix): no key= line in output"; exit 1; }
  log "chat-ifix tenant key minted"
fi

# dashboard admin key
run_gatewayctl admin-key create --label "phase-8-dashboard"
if [[ "$DRY_RUN" -eq 0 ]]; then
  [[ "$GW_RC" -eq 0 ]] || { log "admin-key create failed (exit $GW_RC): $GW_OUT"; exit 1; }
  ADMIN_KEY="$(parse_key "$GW_OUT")"
  [[ -n "$ADMIN_KEY" ]] || { log "admin-key create: no key= line in output"; exit 1; }
  log "dashboard admin key minted"
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  log "dry-run complete — no keys minted, no DB writes"
  exit 0
fi

# --- final instructions: surface raw keys to stdout EXACTLY ONCE ----------
# These values are intentionally printed to stdout (not log()) so they are
# never written to a stderr-redirected log file. They are not re-derivable.
cat <<EOF

====================================================================
  Tenant keys minted. Copy these into the respective Portainer stack
  env vars NOW — they are shown ONCE and are NEVER re-derivable.
  Do NOT commit them to git.
====================================================================

  converseai tenant key  (converseai-v4 api + agents)
    -> OPENAI_API_KEY / gateway base_url key in stack 'converseai-v4-dev'
    ${CONVERSEAI_KEY}

  chat-ifix tenant key   (campanhas-chatifix backend — WhatsApp audio STT)
    -> gateway api_key env var in the chat-ifix backend stack
    ${CHAT_IFIX_KEY}

  dashboard admin key    (label: phase-8-dashboard)
    -> X-Admin-Key for the Phase 7 observability dashboard
    ${ADMIN_KEY}

  If you lose any of these, revoke the row (gatewayctl key revoke /
  admin-key revoke) and re-run this script with --mint-keys to issue a
  fresh one.
====================================================================
EOF

log "done — 2 tenant keys + 1 admin key minted and surfaced above"
