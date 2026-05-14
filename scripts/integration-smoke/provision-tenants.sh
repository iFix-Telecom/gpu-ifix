#!/usr/bin/env bash
# scripts/integration-smoke/provision-tenants.sh — idempotent Phase-8 + Phase-9
# tenant seed.
#
# Wraps the compiled `gatewayctl` CLI (tenant create / tenant set-quota /
# key create / admin-key create) to provision the four Phase-9 client tenants
# in the gateway DB with a PER-TENANT data_class:
#   - `telefonia`  — data_class=sensitive (Telefonia / NextBilling call audio
#                    is PII; never proxied to OpenAI/OpenRouter on failover)
#   - `cobrancas`  — data_class=sensitive (financial / collections data;
#                    RES-08 names Cobranças sensitive)
#   - `campanhas`  — data_class=normal    (marketing personalization, external
#                    failover allowed)
#   - `voice-api`  — data_class=normal    (LLM script generation; TTS stays
#                    local CPU)
# (09-CONTEXT.md `## Decisions`). This implements INT-03 (Telefonia sensitive),
# INT-04 (Cobranças + Campanhas), INT-05 (voice-api).
#
# Idempotency:
#   - `gatewayctl tenant create` is idempotent here: a "slug already exists"
#     stderr + exit 1 is treated as success (the tenant is already
#     provisioned) — the script continues.
#   - `gatewayctl tenant set-quota` is an idempotent UPDATE: it always runs
#     (NOT gated behind --mint-keys), and a re-run simply re-applies the same
#     quota. There is NO idempotency-OK case for its failure — `set-quota`
#     exiting 1 means `tenant %q not found`, which can only mean tenant-create
#     failed, so any non-zero exit is FATAL.
#   - `gatewayctl key create` / `admin-key create` are NOT idempotent: every
#     call mints a NEW row. The key-mint steps are therefore gated behind the
#     explicit `--mint-keys` opt-in flag. Run the script once WITHOUT it to
#     create the tenants + apply the quotas, then once WITH `--mint-keys` to
#     mint the keys.
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
#                      shim (default: `gatewayctl` on PATH). A multi-word
#                      value is split on spaces into an argv array — so the
#                      components themselves MUST NOT contain spaces (use a
#                      wrapper script on PATH if gatewayctl lives under a
#                      space-containing path).
#   --mint-keys        opt-in: also mint the 4 tenant API keys + the dashboard
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
# GATEWAYCTL is an array so a multi-word wrapper (e.g.
# `docker exec ifix-ai-gateway /gatewayctl`) is passed to exec as distinct
# argv entries without relying on unquoted word-splitting. The split is on
# spaces, so the components themselves must not contain spaces.
GATEWAYCTL=(gatewayctl)
MINT_KEYS=0
DRY_RUN=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --gatewayctl) IFS=' ' read -r -a GATEWAYCTL <<< "$2"; shift 2;;
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
# only verify the leading executable. (A wrapper whose later words are wrong
# still passes this precheck and fails at runtime — documented limitation.)
GATEWAYCTL_BIN="${GATEWAYCTL[0]}"
if [[ "$DRY_RUN" -eq 0 ]]; then
  command -v "$GATEWAYCTL_BIN" >/dev/null 2>&1 \
    || { log "missing gatewayctl executable: $GATEWAYCTL_BIN (pass --gatewayctl PATH)"; exit 1; }
fi

# --- tenant model (09-CONTEXT.md `## Decisions`) --------------------------
# Exactly four Phase-9 tenants with a PER-TENANT data_class. The parallel
# TENANT_DATA_CLASS array carries the data_class by index — `tenant create`
# itself takes no --data-class flag (data_class is carried by the KEY, not the
# tenant). TENANT_DATA_CLASS is the SINGLE SOURCE OF TRUTH for per-tenant
# data_class: the key-mint step below drives `mint_tenant_key` from this array
# by index (WR-04 — it is NOT duplicated as inline literals).
#   slug | display name | data_class
TENANT_SLUGS=("telefonia" "cobrancas" "campanhas" "voice-api")
TENANT_NAMES=("Telefonia / NextBilling" "Cobranças" "Campanhas" "voice-api")
TENANT_DATA_CLASS=("sensitive" "sensitive" "normal" "normal")

# --- per-tenant quota model (09-CONTEXT.md SC2) ---------------------------
# Only cobrancas + campanhas get quotas. Starting values are conservative
# per-tenant ceilings (a daily LLM-token budget + a requests-per-minute cap);
# audio/embed/monthly/rps flags are intentionally left off so they stay at the
# `-1` = unchanged sentinel. set-quota is an idempotent UPDATE, so re-running
# the script simply re-applies these.
QUOTA_TENANTS=("cobrancas" "campanhas")
QUOTA_DAILY_TOKENS=("2000000" "5000000")
QUOTA_RPM=("120" "300")

# --- parallel-array length guard (WR-03) ----------------------------------
# These two array groups are indexed in lockstep. `set -u` does NOT trip on an
# out-of-range index expansion — "${QUOTA_RPM[$i]}" for a missing index expands
# to empty, not an error — so a future edit that adds a tenant to one array but
# forgets a value array would silently run e.g. `set-quota --rpm ""` or mint a
# key with the wrong data_class. Assert lengths match before anything runs.
[[ ${#TENANT_SLUGS[@]} -eq ${#TENANT_NAMES[@]} && \
   ${#TENANT_SLUGS[@]} -eq ${#TENANT_DATA_CLASS[@]} ]] \
  || { log "FATAL: tenant arrays desynced (slugs/names/data_class length mismatch)"; exit 1; }
[[ ${#QUOTA_TENANTS[@]} -eq ${#QUOTA_DAILY_TOKENS[@]} && \
   ${#QUOTA_TENANTS[@]} -eq ${#QUOTA_RPM[@]} ]] \
  || { log "FATAL: quota arrays desynced (tenants/daily-tokens/rpm length mismatch)"; exit 1; }

# --- helpers --------------------------------------------------------------
# run_gatewayctl: echoes the command under --dry-run, otherwise executes it.
# Captures combined stdout+stderr into GW_OUT and the exit code into GW_RC.
GW_OUT=""
GW_RC=0
run_gatewayctl() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '[dry-run] would run: %s %s\n' "${GATEWAYCTL[*]}" "$*"
    GW_OUT=""
    GW_RC=0
    return 0
  fi
  set +e
  GW_OUT="$("${GATEWAYCTL[@]}" "$@" 2>&1)"
  GW_RC=$?
  set -e
}

# --- 1) idempotent tenant create (always runs, all four tenants) ----------
log "provisioning ${#TENANT_SLUGS[@]} tenants (per-tenant data_class)"
for i in "${!TENANT_SLUGS[@]}"; do
  slug="${TENANT_SLUGS[$i]}"
  name="${TENANT_NAMES[$i]}"
  run_gatewayctl tenant create --name "$name" --slug "$slug"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    continue
  fi
  if [[ "$GW_RC" -eq 0 ]]; then
    log "tenant '$slug' created"
  elif [[ "$GW_RC" -eq 1 ]] && printf '%s' "$GW_OUT" | grep -qF "tenant slug '$slug' already exists"; then
    # Idempotency signal: re-run on an already-provisioned tenant is OK.
    # Match gatewayctl's EXACT message (gateway/cmd/gatewayctl/tenant.go:80,
    # "error: tenant slug '%s' already exists") via grep -F + the interpolated
    # slug. An unanchored 'already exists' substring would also match unrelated
    # migration-layer / Go-stdlib failures and silently report a missing tenant
    # as provisioned — which would then let --mint-keys mint against nothing.
    log "tenant '$slug' already provisioned — OK"
  else
    log "tenant '$slug' create failed (exit $GW_RC): $GW_OUT"
    exit 1
  fi
done

# --- 2) per-tenant quotas (SC2 — ALWAYS runs, NOT gated by --mint-keys) ----
# `tenant set-quota` is an idempotent UPDATE — it is safe (and required) to run
# on every invocation, including a re-run without --mint-keys. Unlike
# tenant-create there is NO idempotency-OK failure case: set-quota exits 1 with
# `tenant %q not found`, which can only mean the tenant-create step above
# failed. Treat ANY non-zero exit as FATAL so a half-provisioned tenant halts
# the script instead of silently shipping quota-unbounded (threat T-09-02).
log "applying per-tenant quotas to ${#QUOTA_TENANTS[@]} tenants (cobrancas, campanhas)"
for i in "${!QUOTA_TENANTS[@]}"; do
  qslug="${QUOTA_TENANTS[$i]}"
  qtokens="${QUOTA_DAILY_TOKENS[$i]}"
  qrpm="${QUOTA_RPM[$i]}"
  run_gatewayctl tenant set-quota --tenant "$qslug" --daily-tokens "$qtokens" --rpm "$qrpm"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    continue
  fi
  if [[ "$GW_RC" -ne 0 ]]; then
    log "tenant set-quota ($qslug) failed (exit $GW_RC): $GW_OUT"
    log "a non-zero set-quota exit means the tenant does not exist — tenant-create failed; halting."
    exit 1
  fi
  log "tenant '$qslug' quota applied (daily-tokens=$qtokens rpm=$qrpm)"
done

# --- 3) guarded key mint (only under --mint-keys) -------------------------
if [[ "$MINT_KEYS" -eq 0 ]]; then
  log "skipping key mint — 'key create' / 'admin-key create' are NOT idempotent."
  log "re-run ONCE with --mint-keys to mint the 4 tenant keys + dashboard admin key."
  exit 0
fi

log "minting 4 tenant API keys + dashboard admin key (--mint-keys)"

# parse_key: extracts the `key=<raw>` value from a gatewayctl mint block.
# gatewayctl emits a fixed block where `key=` appears on EXACTLY ONE line
# (gateway/cmd/gatewayctl/key.go:87, admin_key.go:129). Assert that count is
# exactly 1 before extracting — taking the first `^key=` line anywhere in the
# combined stdout+stderr stream could surface a diagnostic/warning line as the
# tenant key. `cut -d= -f2-` preserves any `=` inside the raw key value.
parse_key() {
  local count
  count="$(printf '%s\n' "$1" | grep -c '^key=')"
  [[ "$count" -eq 1 ]] || { log "expected exactly 1 'key=' line in mint output, got $count"; return 1; }
  printf '%s\n' "$1" | grep '^key=' | cut -d= -f2-
}

# parse_id: extracts the non-secret `id=<uuid>` value from a gatewayctl mint
# block. Logged (to stderr) so a mid-sequence failure leaves an audit trail of
# which rows were created and must be revoked — the raw `key=` value is NEVER
# logged (secret-once discipline). Best-effort: returns empty if absent.
parse_id() {
  printf '%s\n' "$1" | grep '^id=' | head -n1 | cut -d= -f2-
}

# mint_tenant_key: mints one tenant key with the given per-tenant data_class
# and echoes the raw key to stdout (caller captures it). The raw key is NEVER
# passed to log() — only the non-secret id= is logged.
#
# WR-06: the caller wraps this in `$(...)` (TENANT_KEYS["$slug"]="$(...)") to
# capture the raw key. That command substitution also captures
# run_gatewayctl's `[dry-run] would run:` stdout line — so under --dry-run the
# would-run line is swallowed into the (then-discarded) assoc array instead of
# reaching the operator's terminal. Re-surface the intent on stderr via log()
# so --dry-run still previews the per-tenant `key create` step. (The captured
# run_gatewayctl printf is harmless: it lands in the substitution and is
# discarded at the dry-run exit; no real key is ever minted under --dry-run.)
mint_tenant_key() {
  local slug="$1" data_class="$2"
  run_gatewayctl key create --tenant "$slug" --data-class "$data_class"
  if [[ "$DRY_RUN" -eq 1 ]]; then
    log "[dry-run] would mint tenant key for '$slug' (data_class=$data_class)"
    return 0
  fi
  [[ "$GW_RC" -eq 0 ]] || { log "key create ($slug) failed (exit $GW_RC): $GW_OUT"; exit 1; }
  local k
  k="$(parse_key "$GW_OUT")"
  [[ -n "$k" ]] || { log "key create ($slug): no key= line in output"; exit 1; }
  log "$slug tenant key minted (data_class=$data_class id=$(parse_id "$GW_OUT"))"
  printf '%s' "$k"
}

# WR-04: mint one key per tenant by iterating the SAME arrays used for
# tenant-create, so the per-tenant data_class has exactly ONE source of truth
# (TENANT_DATA_CLASS) instead of being re-stated as inline literals that can
# desync. Keys land in an associative array keyed by slug.
declare -A TENANT_KEYS=()
ADMIN_KEY=""

for i in "${!TENANT_SLUGS[@]}"; do
  slug="${TENANT_SLUGS[$i]}"
  dc="${TENANT_DATA_CLASS[$i]}"
  TENANT_KEYS["$slug"]="$(mint_tenant_key "$slug" "$dc")"
done

# dashboard admin key
run_gatewayctl admin-key create --label "phase-9-sensitive"
if [[ "$DRY_RUN" -eq 0 ]]; then
  [[ "$GW_RC" -eq 0 ]] || { log "admin-key create failed (exit $GW_RC): $GW_OUT"; exit 1; }
  ADMIN_KEY="$(parse_key "$GW_OUT")"
  [[ -n "$ADMIN_KEY" ]] || { log "admin-key create: no key= line in output"; exit 1; }
  log "dashboard admin key minted (id=$(parse_id "$GW_OUT"))"
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  log "dry-run complete — no keys minted, no DB writes"
  exit 0
fi

# --- final instructions: surface raw keys to stdout EXACTLY ONCE ----------
# These values are intentionally printed to stdout (not log()) so they are
# never written to a stderr-redirected log file. They are not re-derivable.
# The two sensitive-tenant keys (telefonia, cobrancas) gate LGPD-relevant
# call-audio + financial data — copy them straight into Portainer, never commit.
cat <<EOF

====================================================================
  Tenant keys minted. Copy these into the respective client repo's
  Portainer stack env vars NOW — they are shown ONCE and are NEVER
  re-derivable. Do NOT commit them to git.
====================================================================

  telefonia tenant key   (data_class=sensitive — call-audio Whisper STT)
    -> client repo: fallback-register-ramais-nextbilling
       gateway api_key env var in that stack
    ${TENANT_KEYS[telefonia]}

  cobrancas tenant key   (data_class=sensitive — LLM personalization + embeds)
    -> client repo: cobrancas-api
       gateway api_key env var in that stack
    ${TENANT_KEYS[cobrancas]}

  campanhas tenant key   (data_class=normal — LLM + embeddings)
    -> client repo: campanhas-chatifix
       gateway api_key env var in that stack
    ${TENANT_KEYS[campanhas]}

  voice-api tenant key   (data_class=normal — LLM script generation)
    -> client repo: voice-api
       gateway api_key env var in that stack
    ${TENANT_KEYS[voice-api]}

  dashboard admin key    (label: phase-9-sensitive)
    -> X-Admin-Key for the Phase 7 observability dashboard
    ${ADMIN_KEY}

  If you lose any of these, revoke the row (gatewayctl key revoke /
  admin-key revoke) and re-run this script with --mint-keys to issue a
  fresh one.
====================================================================
EOF

log "done — 4 tenant keys + 1 admin key minted and surfaced above"
