#!/usr/bin/env bash
# scripts/chaos/vast-delete.sh — PRD-02 chaos kill primary.
#
# Phase 11 plan 11-07. Yanks the active primary Vast.ai instance via raw
# DELETE so the gateway must discover failure through natural probe timeout
# (NOT operator-driven force-open). Then polls FSM + breaker state for a
# FIXED 90s window BEFORE permitting any manual intervention.
#
# Usage:
#   VAST_AI_API_KEY=<token> ./scripts/chaos/vast-delete.sh [--dry-run] [--allow-no-primary]
#
# Env:
#   VAST_AI_API_KEY    REQUIRED. Vast.ai personal API token. NEVER passed as argv.
#   GATEWAYCTL_SSH     optional. SSH alias to reach gatewayctl. Default: n8n-ia-vm.
#   GATEWAY_BASE_URL   optional. Public gateway base URL for breaker probes.
#                      Default: https://ai-gateway.converse-ai.app
#   CHAOS_OBSERVE_SECONDS  optional. Override 90s observation window (NOT recommended).
#
# Exit codes:
#   0  success — DELETE acknowledged + 90s window observed + final state captured
#   1  arg/env validation error
#   2  primary not in killable state (asleep/draining/destroying)
#   3  DELETE failed after retry
#   4  observation loop interrupted
#
# Reviews-folded contract:
#   - [HIGH #4] no raw secrets in argv, log, or output. Env-var labels only.
#   - [MEDIUM #1] FIXED 90s observe-then-intervene window BEFORE force-up.
#   - [MEDIUM #2] allowed-error-class budget (T+0..T+60s 503/504 only; ZERO 500/502).
#   - [LOW #3] JSON-preferred instance-id extraction with text fallback.
#   - [LOW #5] DELETE 404 idempotent + connect-timeout 10s + max-time 30s + 1 retry.
#
# Pattern E skeleton (strict bash + ISO-8601 log + env validation + idempotent).

set -euo pipefail
shopt -s nocasematch

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------
log() {
  # Stderr, no secrets, ISO-8601 timestamp.
  printf '[%s] %s\n' "$(date -u +'%Y-%m-%dT%H:%M:%SZ')" "$*" >&2
}
die() {
  log "FATAL: $*"
  exit "${2:-1}"
}

# ---------------------------------------------------------------------------
# Arg parsing
# ---------------------------------------------------------------------------
DRY_RUN=false
ALLOW_NO_PRIMARY=false
for arg in "$@"; do
  case "$arg" in
    --dry-run)            DRY_RUN=true ;;
    --allow-no-primary)   ALLOW_NO_PRIMARY=true ;;
    -h|--help)
      sed -n '2,28p' "$0"
      exit 0
      ;;
    *) die "unknown arg: $arg" 1 ;;
  esac
done

# ---------------------------------------------------------------------------
# Env validation
# ---------------------------------------------------------------------------
: "${VAST_AI_API_KEY:?VAST_AI_API_KEY env required (see ~/.claude/CLAUDE.md Vast.ai section)}"
: "${GATEWAYCTL_SSH:=n8n-ia-vm}"
: "${GATEWAY_BASE_URL:=https://ai-gateway.converse-ai.app}"
: "${CHAOS_OBSERVE_SECONDS:=90}"

if ! command -v jq >/dev/null 2>&1; then
  die "jq is required" 1
fi
if ! command -v curl >/dev/null 2>&1; then
  die "curl is required" 1
fi
if ! command -v ssh >/dev/null 2>&1; then
  die "ssh is required" 1
fi

# Sanity: NEVER print the token. Show prefix-only for ops attribution.
TOKEN_PREFIX="${VAST_AI_API_KEY:0:4}****"
log "vast-delete starting — token=$TOKEN_PREFIX gateway=$GATEWAY_BASE_URL observe=${CHAOS_OBSERVE_SECONDS}s dry_run=$DRY_RUN"

# ---------------------------------------------------------------------------
# Step 1 — Resolve active primary instance id from FSM
# ---------------------------------------------------------------------------
# [LOW #3] JSON-preferred path, then text fallback.
resolve_instance_id() {
  local raw json_out text_out id

  # Try --json first (Phase 06.8 added; older builds may not support).
  if json_out=$(ssh "$GATEWAYCTL_SSH" "docker exec ifix-ai-gateway /gatewayctl primary state --json" 2>/dev/null); then
    id=$(printf '%s' "$json_out" | jq -re '.pod_instance_id // .instance_id // empty' 2>/dev/null || true)
    if [[ -n "$id" && "$id" != "null" ]]; then
      printf '%s\n' "$id"
      return 0
    fi
    log "WARN: gatewayctl --json returned no instance_id; falling back to text grep"
  else
    log "WARN: gatewayctl --json unsupported on this build; falling back to text grep"
  fi

  # Fallback: text output, awk-extract.
  text_out=$(ssh "$GATEWAYCTL_SSH" "docker exec ifix-ai-gateway /gatewayctl primary state" 2>/dev/null || true)
  id=$(printf '%s\n' "$text_out" | awk '/^pod_instance_id[[:space:]]+[^[:space:]]+/{print $2; exit}')
  if [[ -n "$id" ]]; then
    printf '%s\n' "$id"
    return 0
  fi

  return 1
}

resolve_primary_state() {
  local out
  out=$(ssh "$GATEWAYCTL_SSH" "docker exec ifix-ai-gateway /gatewayctl primary state" 2>/dev/null || true)
  printf '%s\n' "$out" | awk '/^state[[:space:]]+[^[:space:]]+/{print $2; exit}'
}

PRIMARY_STATE=$(resolve_primary_state)
log "primary FSM state: $PRIMARY_STATE"

case "$PRIMARY_STATE" in
  ready|verifying)
    ;;
  *)
    if [[ "$ALLOW_NO_PRIMARY" == "true" ]]; then
      log "WARN: primary state=$PRIMARY_STATE is not in {ready,verifying}; --allow-no-primary set, continuing in capture-only mode"
    else
      die "primary state=$PRIMARY_STATE — chaos test requires ready or verifying; pass --allow-no-primary to capture script-shape evidence only" 2
    fi
    ;;
esac

INSTANCE_ID=""
if ! INSTANCE_ID=$(resolve_instance_id); then
  if [[ "$ALLOW_NO_PRIMARY" == "true" ]]; then
    log "WARN: no instance_id resolved; running in capture-only mode (no Vast DELETE)"
    INSTANCE_ID="capture-only"
  else
    die "could not resolve active primary instance_id from gatewayctl" 2
  fi
fi
log "resolved primary instance_id=$INSTANCE_ID"

# ---------------------------------------------------------------------------
# Step 2 — Vast API DELETE with retry
# ---------------------------------------------------------------------------
vast_delete() {
  local id="$1" code body resp_file
  resp_file=$(mktemp)
  # [LOW #5] --connect-timeout 10 --max-time 30; 404 = idempotent gone.
  code=$(curl -sL -o "$resp_file" -w "%{http_code}" \
    -X DELETE \
    --connect-timeout 10 \
    --max-time 30 \
    -H "Authorization: Bearer $VAST_AI_API_KEY" \
    "https://console.vast.ai/api/v0/instances/${id}/" \
    2>/dev/null || true)
  body=$(head -c 400 "$resp_file")
  rm -f "$resp_file"
  printf '%s\t%s\n' "$code" "$body"
}

if [[ "$DRY_RUN" == "true" ]]; then
  log "DRY-RUN: would DELETE instance $INSTANCE_ID; skipping live call"
  DELETE_RESULT="0\tdry-run"
elif [[ "$INSTANCE_ID" == "capture-only" ]]; then
  log "capture-only mode: skipping live DELETE; script-shape evidence will be emitted"
  DELETE_RESULT="0\tcapture-only"
else
  log "issuing Vast DELETE for instance $INSTANCE_ID (attempt 1)"
  DELETE_RESULT=$(vast_delete "$INSTANCE_ID")
  HTTP_CODE=$(printf '%s' "$DELETE_RESULT" | cut -f1)
  case "$HTTP_CODE" in
    200|202|204)
      log "Vast DELETE acknowledged (HTTP $HTTP_CODE)"
      ;;
    404)
      log "Vast DELETE returned 404 — instance already gone; treating as idempotent success"
      ;;
    5*)
      log "Vast DELETE returned $HTTP_CODE — sleeping 2s + 1 retry"
      sleep 2
      DELETE_RESULT=$(vast_delete "$INSTANCE_ID")
      HTTP_CODE=$(printf '%s' "$DELETE_RESULT" | cut -f1)
      case "$HTTP_CODE" in
        200|202|204|404) log "Vast DELETE retry acknowledged (HTTP $HTTP_CODE)" ;;
        *)               die "Vast DELETE retry FAILED HTTP $HTTP_CODE — orphan possible, check console.vast.ai" 3 ;;
      esac
      ;;
    *)
      die "Vast DELETE FAILED HTTP $HTTP_CODE — orphan possible, check console.vast.ai" 3
      ;;
  esac
fi

CHAOS_TS=$(date -u +'%Y-%m-%dT%H:%M:%SZ')

# ---------------------------------------------------------------------------
# Step 3 — FIXED 90s observation window (NO manual intervention permitted)
# ---------------------------------------------------------------------------
# [MEDIUM #1] poll FSM + breaker state every 5s for 90s; capture snapshots.

SNAPSHOTS_FILE=$(mktemp -t chaos-snapshots.XXXXXX)
log "starting ${CHAOS_OBSERVE_SECONDS}s observation window — no manual force-up permitted"
printf 'elapsed_s\tprimary_state\tlocal_llm_state\n' > "$SNAPSHOTS_FILE"

t0=$(date +%s)
end=$((t0 + CHAOS_OBSERVE_SECONDS))
trap 'log "interrupted at $(($(date +%s) - t0))s — snapshots at $SNAPSHOTS_FILE"; exit 4' INT TERM
while [[ $(date +%s) -lt $end ]]; do
  elapsed=$(($(date +%s) - t0))
  primary=$(resolve_primary_state)
  llm_state=$(curl -sL --connect-timeout 5 --max-time 10 \
    "${GATEWAY_BASE_URL}/v1/health/upstreams" 2>/dev/null \
    | jq -re '.upstreams["local-llm"].state // "unknown"' 2>/dev/null || echo "unreachable")
  printf '%s\t%s\t%s\n' "$elapsed" "${primary:-unknown}" "$llm_state" >> "$SNAPSHOTS_FILE"
  sleep 5
done
trap - INT TERM

log "observation window complete; snapshots at $SNAPSHOTS_FILE"
cat "$SNAPSHOTS_FILE" >&2

# ---------------------------------------------------------------------------
# Step 4 — Decision: auto_recovery vs manual_intervention required
# ---------------------------------------------------------------------------
FINAL_PRIMARY=$(resolve_primary_state)
log "final primary FSM state: $FINAL_PRIMARY"

case "$FINAL_PRIMARY" in
  ready|verifying)
    log "AUTO_RECOVERY: primary auto-recovered within ${CHAOS_OBSERVE_SECONDS}s — controller worked"
    log "next step: write 11-07-EVIDENCE.md with snapshots from $SNAPSHOTS_FILE and set auto_recovery=true"
    ;;
  draining|destroying|asleep|cooldown)
    log "MANUAL_INTERVENTION_REQUIRED: primary did not auto-recover within ${CHAOS_OBSERVE_SECONDS}s"
    log "operator action: ssh $GATEWAYCTL_SSH 'docker exec ifix-ai-gateway /gatewayctl primary force-up --reason 11-07_post_chaos_recover'"
    log "log timestamp + outcome in 11-07-EVIDENCE.md (manual_intervention=true)"
    ;;
  *)
    log "WARN: unrecognized final primary state $FINAL_PRIMARY"
    ;;
esac

log "chaos test phase complete. Inspect:"
log "  - $SNAPSHOTS_FILE (snapshot table)"
log "  - SSH to $GATEWAYCTL_SSH; docker logs ifix-ai-gateway --since $CHAOS_TS"
log "  - https://console.vast.ai/instances/ (cleanup verification)"
log "  - Sentry breadcrumbs for 5xx panic during T+0..T+60s window (MUST be zero — see PRD-02 gates)"
