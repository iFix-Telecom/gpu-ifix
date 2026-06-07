#!/usr/bin/env bash
# Phase 11.2 Plan 08 — Wave 6 UAT driver (D-B13).
#
# Implements the 6 mandatory cenários per CONTEXT D-B13 cascade STT 3-upstream
# + RES-08 sensitive hard-block. Each function runs the curl + assertions
# inline; --dry-run shows what each scenario would do without executing.
#
# Targets:
#   Gateway URL : https://ai-gateway-dev.converse-ai.app  (override via GATEWAY_URL)
#   Admin URL   : same host, /admin/audit endpoint (X-Admin-Key for upstream check)
#   API keys    : NORMAL_KEY + SENSITIVE_KEY env vars (mandatory).
#   Audit DB    : direct psql to ai_gateway DB (AI_GATEWAY_PG_DSN) — used for
#                 per-request upstream verification (admin/audit only emits
#                 state-changes, not request rows).
#
# Required env:
#   NORMAL_KEY              ifix_sk_... — converseai (normal) tenant key
#   SENSITIVE_KEY           ifix_sk_... — telefonia (sensitive) tenant key
#   ADMIN_KEY               ifix_admin_... — used for breaker/audit state checks
#   AI_GATEWAY_PG_DSN       postgres://... — DB for audit_log SELECT
#
# Usage:
#   ./uat-11.2.sh --all                  # run all 6 scenarios
#   ./uat-11.2.sh --scenario <name>      # run one
#   ./uat-11.2.sh --scenario <name> --dry-run
#   ./uat-11.2.sh --list                 # print scenario names + exit
#   ./uat-11.2.sh --cleanup              # restore breakers to closed; pod force-down
#
# Scenario name list (CONTEXT D-B13 order):
#   1. pod-on-local-stt
#   2. pod-off-gemini
#   3. gemini-open-groq-takeover
#   4. gemini-groq-open-openai-takeover
#   5. sensitive-pod-on
#   6. sensitive-pod-off-503

set -euo pipefail

readonly GATEWAY_URL="${GATEWAY_URL:-https://ai-gateway-dev.converse-ai.app}"
readonly AUDIT_DB="${AI_GATEWAY_PG_DSN:-}"
readonly NORMAL_KEY="${NORMAL_KEY:?NORMAL_KEY env var required (converseai tenant — normal class)}"
readonly SENSITIVE_KEY="${SENSITIVE_KEY:?SENSITIVE_KEY env var required (telefonia-uat tenant — sensitive class)}"
readonly ADMIN_KEY="${ADMIN_KEY:?ADMIN_KEY env var required (X-Admin-Key for breaker/pod state)}"

# How the operator addresses the gateway container's gatewayctl. Default
# matches the dev stack on vps-ifix-vm. Operator can override.
readonly GATEWAYCTL_CMD="${GATEWAYCTL_CMD:-ssh vps-ifix-vm /usr/bin/docker exec ai-gateway-dev /gatewayctl}"

# Fixture paths — written by generate_*_wav helpers if missing.
readonly FIXTURE_DIR="${FIXTURE_DIR:-/tmp/uat-11.2-fixtures}"
readonly WAV_SILENCE_1S="${FIXTURE_DIR}/silence-1s.wav"
readonly WAV_SPEECH_5S="${FIXTURE_DIR}/speech-5s.wav"

readonly SCENARIOS=(
  pod-on-local-stt
  pod-off-gemini
  gemini-open-groq-takeover
  gemini-groq-open-openai-takeover
  sensitive-pod-on
  sensitive-pod-off-503
)

DRY_RUN=0
KEEP_POD_STATE=0

# Map scenario name → expected upstream in audit_log (sentinel "n/a" for 503).
declare -A EXPECTED_UPSTREAM=(
  [pod-on-local-stt]="local-stt"
  [pod-off-gemini]="gemini-stt"
  [gemini-open-groq-takeover]="groq-whisper"
  [gemini-groq-open-openai-takeover]="openai-whisper"
  [sensitive-pod-on]="local-stt"
  [sensitive-pod-off-503]="n/a"
)

# -----------------------------------------------------------------------------
# Helpers
# -----------------------------------------------------------------------------

log()  { printf '[uat] %s\n' "$*" >&2; }
err()  { printf '[uat][ERR] %s\n' "$*" >&2; }
pass() { printf '[uat][PASS] %s\n' "$*" >&2; }
fail() { printf '[uat][FAIL] %s\n' "$*" >&2; }

ensure_fixtures() {
  mkdir -p "$FIXTURE_DIR"
  if [[ ! -f "$WAV_SILENCE_1S" ]]; then
    log "generating 1s silence WAV at $WAV_SILENCE_1S"
    ffmpeg -hide_banner -loglevel error -f lavfi -i anullsrc=r=16000:cl=mono \
      -t 1 -ar 16000 -ac 1 "$WAV_SILENCE_1S"
  fi
  if [[ ! -f "$WAV_SPEECH_5S" ]]; then
    log "generating 5s speech WAV at $WAV_SPEECH_5S (espeak fallback to sine if absent)"
    if command -v espeak >/dev/null 2>&1; then
      espeak -w /tmp/_uat_speech.wav -s 150 \
        "This is a Phase 11.2 UAT speech sample for STT cascade verification." 2>/dev/null
      ffmpeg -hide_banner -loglevel error -y -i /tmp/_uat_speech.wav \
        -ar 16000 -ac 1 -t 5 "$WAV_SPEECH_5S"
      rm -f /tmp/_uat_speech.wav
    else
      # Sine sweep fallback — Gemini/Whisper will likely return empty text but
      # 200 success still proves cascade routing.
      ffmpeg -hide_banner -loglevel error -y -f lavfi \
        -i "sine=frequency=440:duration=5" -ar 16000 -ac 1 "$WAV_SPEECH_5S"
    fi
  fi
}

# Issue a POST /v1/audio/transcriptions with the given key + WAV. Writes:
#   $BODY_FILE      response body
#   $HEADERS_FILE   response headers
#   $REQUEST_ID_FILE  X-Request-ID (or dry-run sentinel)
#   $STATUS_FILE    HTTP status (3-digit) on a line by itself
# These are persisted via files because the helper runs in a subshell and
# `set -u` would otherwise trip on $REQUEST_ID in the caller.
post_transcription() {
  local key="$1" wav="$2"
  BODY_FILE="${BODY_FILE:-$(mktemp)}"
  HEADERS_FILE="${HEADERS_FILE:-$(mktemp)}"
  STATUS_FILE="${STATUS_FILE:-$(mktemp)}"
  REQUEST_ID_FILE="${REQUEST_ID_FILE:-$(mktemp)}"
  : > "$REQUEST_ID_FILE"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  would POST $GATEWAY_URL/v1/audio/transcriptions  key=${key:0:14}…  file=$wav  model=whisper"
    echo "dryrun-$(date +%s)-$RANDOM" > "$REQUEST_ID_FILE"
    echo "200" > "$STATUS_FILE"
    return 0
  fi
  local status
  status=$(curl -sS -X POST "$GATEWAY_URL/v1/audio/transcriptions" \
    -H "Authorization: Bearer $key" \
    -D "$HEADERS_FILE" \
    -F "file=@${wav};type=audio/wav" \
    -F "model=whisper" \
    -o "$BODY_FILE" \
    -w "%{http_code}") || status="000"
  echo "$status" > "$STATUS_FILE"
  grep -i '^x-request-id:' "$HEADERS_FILE" 2>/dev/null \
    | tr -d '\r' | awk -F': ' '{print $2}' | head -1 > "$REQUEST_ID_FILE"
}

# Convenience wrappers around the files post_transcription writes.
last_status()     { cat "$STATUS_FILE" 2>/dev/null || echo "000"; }
last_request_id() { cat "$REQUEST_ID_FILE" 2>/dev/null || echo ""; }

# Query audit_log for the upstream column matching the request_id (if known)
# or the latest transcription row. Echoes the upstream string.
audit_query_upstream() {
  local request_id="${1:-}"
  if [[ "$DRY_RUN" == 1 ]]; then
    echo "(dry-run: would SELECT upstream FROM audit_log WHERE request_id='$request_id')"
    return 0
  fi
  if [[ -z "$AUDIT_DB" ]]; then
    err "AI_GATEWAY_PG_DSN required to query audit_log; cannot verify upstream"
    echo ""
    return 0
  fi
  if [[ -n "$request_id" ]]; then
    psql "$AUDIT_DB" -At -c \
      "SELECT COALESCE(upstream,'') FROM ai_gateway.audit_log WHERE request_id='${request_id}' ORDER BY ts DESC LIMIT 1;" \
      2>/dev/null
  else
    psql "$AUDIT_DB" -At -c \
      "SELECT COALESCE(upstream,'') FROM ai_gateway.audit_log WHERE route='/v1/audio/transcriptions' ORDER BY ts DESC LIMIT 1;" \
      2>/dev/null
  fi
}

audit_query_data_class() {
  local request_id="$1"
  [[ -z "$AUDIT_DB" || "$DRY_RUN" == 1 ]] && { echo "?"; return; }
  psql "$AUDIT_DB" -At -c \
    "SELECT COALESCE(data_class::text,'?') FROM ai_gateway.audit_log WHERE request_id='${request_id}' ORDER BY ts DESC LIMIT 1;" \
    2>/dev/null
}

wait_for_primary_ready() {
  local max_wait="${1:-1200}" elapsed=0 state
  log "waiting up to ${max_wait}s for primary state=Ready"
  while (( elapsed < max_wait )); do
    state=$($GATEWAYCTL_CMD primary state 2>/dev/null | awk '$1=="state"{print $2; exit}')
    if [[ "$state" == "Ready" ]]; then
      log "primary Ready after ${elapsed}s"
      return 0
    fi
    sleep 15
    elapsed=$(( elapsed + 15 ))
    [[ $((elapsed % 60)) -eq 0 ]] && log "  …still waiting (state=$state, elapsed=${elapsed}s)"
  done
  err "timed out waiting for primary Ready (last state=$state)"
  return 1
}

wait_for_primary_asleep() {
  local max_wait="${1:-300}" elapsed=0 state
  log "waiting up to ${max_wait}s for primary state=asleep"
  while (( elapsed < max_wait )); do
    state=$($GATEWAYCTL_CMD primary state 2>/dev/null | awk '$1=="state"{print $2; exit}')
    if [[ "$state" == "asleep" ]]; then
      log "primary asleep after ${elapsed}s"
      return 0
    fi
    sleep 10
    elapsed=$(( elapsed + 10 ))
  done
  err "timed out waiting for primary asleep (last state=$state)"
  return 1
}

force_open_breaker() {
  local upstream="$1" ttl="${2:-300s}"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  would gatewayctl breaker force-open --upstream $upstream --ttl $ttl"
    return 0
  fi
  $GATEWAYCTL_CMD breaker force-open --upstream "$upstream" --ttl "$ttl" >&2
}

force_close_breaker() {
  local upstream="$1"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  would gatewayctl breaker force-close --upstream $upstream"
    return 0
  fi
  $GATEWAYCTL_CMD breaker force-close --upstream "$upstream" >&2 || true
}

primary_force_up() {
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  would gatewayctl primary force-up"
    return 0
  fi
  $GATEWAYCTL_CMD primary force-up >&2 || true
}

primary_force_down() {
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  would gatewayctl primary force-down"
    return 0
  fi
  $GATEWAYCTL_CMD primary force-down >&2 || true
}

# Assertion helpers
assert_http_status() {
  local got="$1" want="$2" name="$3"
  if [[ "$got" == "$want" ]]; then
    pass "$name — HTTP $got (expected $want)"
    return 0
  fi
  fail "$name — HTTP $got (expected $want)"
  if [[ -n "${BODY_FILE:-}" && -f "$BODY_FILE" ]]; then
    err "  body: $(head -c 500 "$BODY_FILE")"
  fi
  return 1
}

assert_audit_upstream() {
  local request_id="$1" want="$2" name="$3"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "$name — (dry-run: would assert audit_log.upstream='$want' for request_id=$request_id)"
    return 0
  fi
  local got
  got=$(audit_query_upstream "$request_id")
  if [[ "$got" == "$want" ]]; then
    pass "$name — audit_log.upstream='$got' (expected '$want')"
    return 0
  fi
  fail "$name — audit_log.upstream='$got' (expected '$want') request_id=$request_id"
  return 1
}

assert_body_contains() {
  local needle="$1" name="$2"
  if grep -q "$needle" "$BODY_FILE" 2>/dev/null; then
    pass "$name — body contains '$needle'"
    return 0
  fi
  fail "$name — body missing '$needle'"
  err "  body: $(head -c 500 "$BODY_FILE")"
  return 1
}

# -----------------------------------------------------------------------------
# Scenarios
# -----------------------------------------------------------------------------

scenario_pod_on_local_stt() {
  echo "[scenario] pod-on-local-stt — D-B13 #1"
  ensure_fixtures
  if [[ "$DRY_RUN" != 1 ]]; then
    primary_force_up
    wait_for_primary_ready || return 1
  fi
  post_transcription "$NORMAL_KEY" "$WAV_SILENCE_1S"
  assert_http_status "$(last_status)" "200" "pod-on-local-stt" || return 1
  sleep 2  # let async audit writer flush
  assert_audit_upstream "$(last_request_id)" "local-stt" "pod-on-local-stt" || return 1
}

scenario_pod_off_gemini() {
  echo "[scenario] pod-off-gemini — D-B13 #2"
  ensure_fixtures
  if [[ "$DRY_RUN" != 1 ]]; then
    primary_force_down
    wait_for_primary_asleep || return 1
  fi
  post_transcription "$NORMAL_KEY" "$WAV_SPEECH_5S"
  assert_http_status "$(last_status)" "200" "pod-off-gemini" || return 1
  sleep 2
  assert_audit_upstream "$(last_request_id)" "gemini-stt" "pod-off-gemini" || return 1
  # Plausibility: text field exists and is non-empty length-bounded
  if [[ "$DRY_RUN" != 1 ]]; then
    local text_len
    text_len=$(jq -r '.text // "" | length' "$BODY_FILE" 2>/dev/null || echo "0")
    if (( text_len > 0 && text_len < 5000 )); then
      pass "pod-off-gemini — text length=$text_len (plausibility check)"
    else
      log "pod-off-gemini — WARN text length=$text_len (sine fallback may yield empty)"
    fi
  fi
}

scenario_gemini_open_groq_takeover() {
  echo "[scenario] gemini-open-groq-takeover — D-B13 #3"
  ensure_fixtures
  # Pre: pod already OFF from scenario 2 (or operator ran scenario 1+force-down).
  force_open_breaker gemini-stt 300s
  sleep 2
  post_transcription "$NORMAL_KEY" "$WAV_SPEECH_5S"
  assert_http_status "$(last_status)" "200" "gemini-open-groq-takeover" || { force_close_breaker gemini-stt; return 1; }
  sleep 2
  assert_audit_upstream "$(last_request_id)" "groq-whisper" "gemini-open-groq-takeover" || { force_close_breaker gemini-stt; return 1; }
  force_close_breaker gemini-stt
}

scenario_gemini_groq_open_openai_takeover() {
  echo "[scenario] gemini-groq-open-openai-takeover — D-B13 #4"
  ensure_fixtures
  force_open_breaker gemini-stt 300s
  force_open_breaker groq-whisper 300s
  sleep 2
  post_transcription "$NORMAL_KEY" "$WAV_SPEECH_5S"
  assert_http_status "$(last_status)" "200" "gemini-groq-open-openai-takeover" \
    || { force_close_breaker gemini-stt; force_close_breaker groq-whisper; return 1; }
  sleep 2
  assert_audit_upstream "$(last_request_id)" "openai-whisper" "gemini-groq-open-openai-takeover" \
    || { force_close_breaker gemini-stt; force_close_breaker groq-whisper; return 1; }
  force_close_breaker gemini-stt
  force_close_breaker groq-whisper
}

scenario_sensitive_pod_on() {
  echo "[scenario] sensitive-pod-on — D-B13 #5"
  ensure_fixtures
  if [[ "$DRY_RUN" != 1 ]]; then
    primary_force_up
    wait_for_primary_ready || return 1
  fi
  post_transcription "$SENSITIVE_KEY" "$WAV_SILENCE_1S"
  assert_http_status "$(last_status)" "200" "sensitive-pod-on" || return 1
  sleep 2
  assert_audit_upstream "$(last_request_id)" "local-stt" "sensitive-pod-on" || return 1
  if [[ "$DRY_RUN" == 1 ]]; then
    log "sensitive-pod-on — (dry-run: would assert audit_log.data_class='sensitive')"
  else
    local dc
    dc=$(audit_query_data_class "$(last_request_id)")
    if [[ "$dc" == "sensitive" ]]; then
      pass "sensitive-pod-on — audit_log.data_class='sensitive'"
    else
      fail "sensitive-pod-on — audit_log.data_class='$dc' (expected 'sensitive')"
      return 1
    fi
  fi
}

scenario_sensitive_pod_off_503() {
  echo "[scenario] sensitive-pod-off-503 — D-B13 #6 (RES-08 hard-block)"
  ensure_fixtures
  if [[ "$DRY_RUN" == 1 ]]; then
    primary_force_down
    post_transcription "$SENSITIVE_KEY" "$WAV_SILENCE_1S"
    log "  (dry-run: live call would assert HTTP 503 + body contains 'upstream_unavailable_for_sensitive_tenant')"
    return 0
  fi
  primary_force_down
  wait_for_primary_asleep || return 1
  post_transcription "$SENSITIVE_KEY" "$WAV_SILENCE_1S"
  assert_http_status "$(last_status)" "503" "sensitive-pod-off-503" || return 1
  assert_body_contains "upstream_unavailable_for_sensitive_tenant" "sensitive-pod-off-503" || return 1
}

cleanup() {
  log "cleanup — restoring all breakers to closed + pod force-down"
  if [[ "$DRY_RUN" != 1 ]]; then
    for u in gemini-stt groq-whisper openai-whisper local-stt; do
      $GATEWAYCTL_CMD breaker force-close --upstream "$u" >&2 || true
    done
    [[ "$KEEP_POD_STATE" == 1 ]] || $GATEWAYCTL_CMD primary force-down >&2 || true
  fi
}

# -----------------------------------------------------------------------------
# Dispatcher
# -----------------------------------------------------------------------------

run_scenario() {
  local name="$1"
  case "$name" in
    pod-on-local-stt)                 scenario_pod_on_local_stt ;;
    pod-off-gemini)                   scenario_pod_off_gemini ;;
    gemini-open-groq-takeover)        scenario_gemini_open_groq_takeover ;;
    gemini-groq-open-openai-takeover) scenario_gemini_groq_open_openai_takeover ;;
    sensitive-pod-on)                 scenario_sensitive_pod_on ;;
    sensitive-pod-off-503)            scenario_sensitive_pod_off_503 ;;
    *)
      echo "Unknown scenario: $name" >&2
      echo "Valid: ${SCENARIOS[*]}" >&2
      return 2
      ;;
  esac
}

usage() {
  cat <<EOF
Phase 11.2 UAT driver — D-B13 6 cenários

Usage:
  $0 --all [--dry-run]                Run all 6 scenarios in order
  $0 --scenario <name> [--dry-run]    Run one scenario
  $0 --list                           List scenario names
  $0 --cleanup                        Reset breakers + primary force-down
  $0 --keep-pod-state                 (modifier) Skip pod force-down in cleanup

Required env:
  NORMAL_KEY        converseai tenant key (data_class=normal)
  SENSITIVE_KEY     telefonia-uat tenant key (data_class=sensitive)
  ADMIN_KEY         X-Admin-Key (unused at curl-time; reserved for future endpoints)
  AI_GATEWAY_PG_DSN postgres://... for audit_log verification

Targets:
  Gateway URL : $GATEWAY_URL
  gatewayctl  : $GATEWAYCTL_CMD

Scenarios (D-B13):
$(for s in "${SCENARIOS[@]}"; do echo "  - $s  → expects audit_log.upstream='${EXPECTED_UPSTREAM[$s]}'"; done)
EOF
}

main() {
  local args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run)         DRY_RUN=1; shift ;;
      --keep-pod-state)  KEEP_POD_STATE=1; shift ;;
      *)                 args+=("$1"); shift ;;
    esac
  done
  set -- "${args[@]}"

  if [[ $# -eq 0 ]]; then
    usage
    exit 0
  fi

  case "$1" in
    --all)
      local failures=0
      for s in "${SCENARIOS[@]}"; do
        run_scenario "$s" || failures=$(( failures + 1 ))
      done
      cleanup
      if (( failures > 0 )); then
        err "$failures scenario(s) FAILED"
        exit 1
      fi
      pass "all 6 scenarios PASSED"
      ;;
    --scenario)
      [[ $# -ge 2 ]] || { echo "missing scenario name" >&2; usage; exit 2; }
      run_scenario "$2"
      ;;
    --list)
      printf '%s\n' "${SCENARIOS[@]}"
      ;;
    --cleanup)
      cleanup
      ;;
    -h|--help)
      usage
      ;;
    *)
      echo "Unknown flag: $1" >&2
      usage
      exit 2
      ;;
  esac
}

main "$@"
