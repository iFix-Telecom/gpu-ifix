#!/usr/bin/env bash
# Phase 14 Plan 03 — VRAM-adaptive STT live UAT driver (Wave 3).
#
# Proves the SEED-019 parts 2+3 change on real Vast hardware: the gateway
# auto-decides whether STT runs on the pod (GPU whisper) or fails over to
# tier-1 gemini-stt, driven entirely by the pod-reported device
# (GET :9100/whisper_device) — no operator flag, no gateway shape knowledge.
#
# Two shapes, opposite expectations (the bidirectional gate from CONTEXT):
#
#   shape=3090  (1x RTX 3090, 24 GB)  -> pod reports whisper_device=cpu
#                                         gateway does NOT set the stt override
#                                         STT served by tier-1 gemini-stt (~2-3s)
#                                         (STT-SHAPE-3090 + STT-MIGRATE)
#
#   shape=gpu   (>=30 GB: 2x3090=48 / 5090=32) -> pod reports whisper_device=cuda
#                                         gateway sets the stt tier-0 override
#                                         STT served by the pod whisper-GPU (<5s)
#                                         pod speaches.log has NO "CUDA out of memory"
#                                         (STT-SHAPE-GPU)
#
# This runner REUSES the established UAT operating recipe (mirrors
# pod/smoke/uat-11.2.sh): it drives the FSM with `gatewayctl primary
# force-up/force-down`, polls to Ready, asserts the device + STT route +
# latency band, greps the pod speaches.log for OOM, and force-downs +
# confirms BestEffortDestroy. It does NOT invent a new provision path.
#
# The >=30 GB shape requires an allowlist/shape override per the 06.8 ladder
# runbook (the gateway provisions on the env-pinned PRIMARY_VAST_* shape; pass
# the override env to the gateway container before force-up). This runner does
# NOT mutate the Portainer env — the operator flips PRIMARY_TEMPLATE_IMAGE to
# the Task-1 SHA and applies any shape override per the checkpoint instructions.
#
# COST SAFETY (MEMORY: check Vast credit FIRST):
#   - A Vast credit pre-check ABORTS before any provision when credit is low.
#   - Every shape force-downs at the end and asserts BestEffortDestroy
#     (no orphan pod burning money — threat T-14-08).
#
# Required env:
#   NORMAL_KEY              ifix_sk_... — converseai (normal) tenant key
#   VAST_AI_API_KEY         Vast.ai API key (credit pre-check + orphan audit)
#
# Optional env:
#   GATEWAY_URL             default https://ai-gateway-dev.converse-ai.app
#   GATEWAYCTL_CMD          default: ssh vps-ifix-vm docker exec ai-gateway-dev /gatewayctl
#   POD_HOST                pod public IP/host for the direct :9100 device probe
#                           + speaches.log fetch (printed by `gatewayctl primary
#                           state`); when unset the runner reads it from gatewayctl.
#   POD_9100_PORT           host port mapped to the pod's :9100 (from `primary state`)
#   POD_SSH                 ssh target for the pod (operator key) to tail speaches.log
#   VAST_MIN_CREDIT_USD     credit floor for the pre-check (default 2.00)
#   FIXTURE_DIR             default /tmp/uat-14-fixtures
#
# Usage:
#   ./uat-14.sh 3090                 # UAT A — 24 GB shape -> gemini-stt
#   ./uat-14.sh gpu                  # UAT B — >=30 GB shape -> pod GPU, no OOM
#   ./uat-14.sh 3090 --dry-run       # print the plan without provisioning
#   ./uat-14.sh --credit-check       # run ONLY the Vast credit pre-check + exit
#   ./uat-14.sh --cleanup            # force-down + assert no orphan + exit

set -euo pipefail

readonly GATEWAY_URL="${GATEWAY_URL:-https://ai-gateway-dev.converse-ai.app}"
readonly GATEWAYCTL_CMD="${GATEWAYCTL_CMD:-ssh vps-ifix-vm /usr/bin/docker exec ai-gateway-dev /gatewayctl}"
readonly VAST_MIN_CREDIT_USD="${VAST_MIN_CREDIT_USD:-2.00}"

readonly FIXTURE_DIR="${FIXTURE_DIR:-/tmp/uat-14-fixtures}"
readonly WAV_SPEECH_10S="${FIXTURE_DIR}/speech-10s.wav"

# Pod-side device-report + log paths (Plan 14-02 contract).
readonly DEVICE_REPORT_PATH="/whisper_device"   # GET :9100/whisper_device
readonly POD_SPEACHES_LOG="/var/log/speaches.log"

DRY_RUN=0

log()  { printf '[uat-14] %s\n' "$*" >&2; }
err()  { printf '[uat-14][ERR] %s\n' "$*" >&2; }
pass() { printf '[uat-14][PASS] %s\n' "$*" >&2; }
fail() { printf '[uat-14][FAIL] %s\n' "$*" >&2; }

# -----------------------------------------------------------------------------
# Vast credit pre-check — MEMORY rule: check Vast credit FIRST, abort if low.
# Aborts the whole UAT (exit 3) BEFORE any provision so we never spend on a
# zero-balance account / leave an orphan we can't afford to keep an eye on.
# -----------------------------------------------------------------------------
vast_credit_check() {
  local key="${VAST_AI_API_KEY:-}"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  (dry-run: would GET https://console.vast.ai/api/v0/users/current/ and assert credit >= \$$VAST_MIN_CREDIT_USD)"
    return 0
  fi
  if [[ -z "$key" ]]; then
    err "VAST_AI_API_KEY required for the Vast credit pre-check (MEMORY: check credit FIRST)"
    return 3
  fi
  local credit
  credit=$(curl -sS -H "Authorization: Bearer $key" \
    "https://console.vast.ai/api/v0/users/current/" 2>/dev/null \
    | jq -r '.credit // .balance // empty' 2>/dev/null)
  if [[ -z "$credit" ]]; then
    err "could not read Vast credit from /users/current/ — ABORTING before any provision (fail-closed)"
    return 3
  fi
  log "Vast credit = \$$credit (floor \$$VAST_MIN_CREDIT_USD)"
  if awk -v c="$credit" -v m="$VAST_MIN_CREDIT_USD" 'BEGIN{exit !(c+0 >= m+0)}'; then
    pass "Vast credit pre-check OK (\$$credit >= \$$VAST_MIN_CREDIT_USD)"
    return 0
  fi
  fail "Vast credit \$$credit below floor \$$VAST_MIN_CREDIT_USD — ABORTING UAT (no provision, no spend)"
  return 3
}

# Count live Vast instances (orphan audit after force-down).
vast_instance_count() {
  local key="${VAST_AI_API_KEY:-}"
  [[ -z "$key" || "$DRY_RUN" == 1 ]] && { echo "?"; return 0; }
  curl -sS -H "Authorization: Bearer $key" \
    "https://console.vast.ai/api/v0/instances/" 2>/dev/null \
    | jq -r '(.instances // []) | length' 2>/dev/null || echo "?"
}

# -----------------------------------------------------------------------------
# Fixtures — a real ~10s speech clip (espeak->ffmpeg; sine fallback).
# -----------------------------------------------------------------------------
ensure_fixtures() {
  mkdir -p "$FIXTURE_DIR"
  if [[ -f "$WAV_SPEECH_10S" ]]; then return 0; fi
  log "generating ~10s speech WAV at $WAV_SPEECH_10S"
  if command -v espeak >/dev/null 2>&1; then
    espeak -w /tmp/_uat14_speech.wav -s 145 \
      "This is a Phase 14 VRAM adaptive speech to text user acceptance test sample. \
       It exercises the gateway device gated transcription route end to end \
       across both the twenty four gigabyte and thirty gigabyte pod shapes." 2>/dev/null
    ffmpeg -hide_banner -loglevel error -y -i /tmp/_uat14_speech.wav \
      -ar 16000 -ac 1 -t 10 "$WAV_SPEECH_10S"
    rm -f /tmp/_uat14_speech.wav
  else
    ffmpeg -hide_banner -loglevel error -y -f lavfi \
      -i "sine=frequency=440:duration=10" -ar 16000 -ac 1 "$WAV_SPEECH_10S"
  fi
}

# -----------------------------------------------------------------------------
# FSM drivers (reuse the uat-11.2 recipe).
# -----------------------------------------------------------------------------
primary_force_up() {
  if [[ "$DRY_RUN" == 1 ]]; then log "  would gatewayctl primary force-up"; return 0; fi
  $GATEWAYCTL_CMD primary force-up >&2 || true
}

primary_force_down() {
  if [[ "$DRY_RUN" == 1 ]]; then log "  would gatewayctl primary force-down"; return 0; fi
  $GATEWAYCTL_CMD primary force-down >&2 || true
}

primary_state_field() {
  # echoes the value of the named field from `gatewayctl primary state`
  local field="$1"
  $GATEWAYCTL_CMD primary state 2>/dev/null | awk -v f="$field" '$1==f{print $2; exit}'
}

wait_for_primary_ready() {
  if [[ "$DRY_RUN" == 1 ]]; then log "  (dry-run: would poll gatewayctl primary state until Ready)"; return 0; fi
  local max_wait="${1:-1800}" elapsed=0 state
  log "waiting up to ${max_wait}s for primary state=Ready (MinIO is in BR — cold-start budget is large)"
  while (( elapsed < max_wait )); do
    state=$(primary_state_field state)
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
  if [[ "$DRY_RUN" == 1 ]]; then log "  (dry-run: would poll gatewayctl primary state until asleep)"; return 0; fi
  local max_wait="${1:-300}" elapsed=0 state
  log "waiting up to ${max_wait}s for primary state=asleep"
  while (( elapsed < max_wait )); do
    state=$(primary_state_field state)
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

# -----------------------------------------------------------------------------
# Device-report probe (Plan 14-02 contract): GET :9100/whisper_device.
# Resolves the pod host:port from gatewayctl when POD_HOST/POD_9100_PORT unset.
# -----------------------------------------------------------------------------
probe_whisper_device() {
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  would GET http://<pod>:<9100>${DEVICE_REPORT_PATH} and read .whisper_device"
    echo ""
    return 0
  fi
  local host="${POD_HOST:-$(primary_state_field pod_host)}"
  local port="${POD_9100_PORT:-$(primary_state_field device_report_port)}"
  if [[ -z "$host" || -z "$port" ]]; then
    err "cannot resolve pod :9100 endpoint (set POD_HOST + POD_9100_PORT from \`gatewayctl primary state\`)"
    echo ""
    return 0
  fi
  curl -sS --max-time 10 "http://${host}:${port}${DEVICE_REPORT_PATH}" 2>/dev/null \
    | jq -r '.whisper_device // empty' 2>/dev/null || echo ""
}

assert_whisper_device() {
  local want="$1" name="$2"
  local got
  got=$(probe_whisper_device)
  if [[ "$DRY_RUN" == 1 ]]; then
    log "$name — (dry-run: would assert whisper_device='$want')"
    return 0
  fi
  if [[ "$got" == "$want" ]]; then
    pass "$name — whisper_device='$got' (expected '$want')"
    return 0
  fi
  fail "$name — whisper_device='$got' (expected '$want')"
  return 1
}

# -----------------------------------------------------------------------------
# STT route probe — POST /v1/audio/transcriptions, capture status + latency.
# -----------------------------------------------------------------------------
post_transcription_timed() {
  local key="$1" wav="$2"
  STT_BODY="$(mktemp)"; STT_HEADERS="$(mktemp)"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "  would POST $GATEWAY_URL/v1/audio/transcriptions  key=${key:0:14}…  file=$wav"
    STT_STATUS="200"; STT_LATENCY="0.0"
    return 0
  fi
  local out
  out=$(curl -sS -X POST "$GATEWAY_URL/v1/audio/transcriptions" \
    -H "Authorization: Bearer $key" \
    -D "$STT_HEADERS" \
    -F "file=@${wav};type=audio/wav" \
    -F "model=whisper" \
    -o "$STT_BODY" \
    -w '%{http_code} %{time_total}') || out="000 0"
  STT_STATUS="${out%% *}"
  STT_LATENCY="${out##* }"
}

assert_stt_route() {
  # name, want_status, latency_lo, latency_hi (seconds), expected_upstream_hint
  local name="$1" want_status="$2" lo="$3" hi="$4" upstream_hint="$5"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "$name — (dry-run: would assert HTTP $want_status, latency in [$lo,$hi]s, served by $upstream_hint)"
    return 0
  fi
  local ok=0
  if [[ "$STT_STATUS" == "$want_status" ]]; then
    pass "$name — HTTP $STT_STATUS (expected $want_status)"
  else
    fail "$name — HTTP $STT_STATUS (expected $want_status); body: $(head -c 300 "$STT_BODY")"
    ok=1
  fi
  if awk -v t="$STT_LATENCY" -v lo="$lo" -v hi="$hi" 'BEGIN{exit !(t+0 >= lo+0 && t+0 <= hi+0)}'; then
    pass "$name — latency ${STT_LATENCY}s in band [${lo},${hi}]s (consistent with $upstream_hint)"
  else
    fail "$name — latency ${STT_LATENCY}s OUTSIDE band [${lo},${hi}]s (expected $upstream_hint)"
    ok=1
  fi
  log "$name — operator: confirm the serving upstream in the gateway logs is '$upstream_hint' (audit_log.upstream / 'tier-0 override' breadcrumb)"
  return $ok
}

# -----------------------------------------------------------------------------
# OOM grep — the >=30 GB shape MUST NOT log "CUDA out of memory" in speaches.log.
# Pulls the log via POD_SSH (operator key). Absent SSH -> operator runs the grep.
# -----------------------------------------------------------------------------
assert_no_cuda_oom() {
  local name="$1"
  if [[ "$DRY_RUN" == 1 ]]; then
    log "$name — (dry-run: would grep $POD_SPEACHES_LOG for 'CUDA out of memory' — MUST be ABSENT)"
    return 0
  fi
  if [[ -z "${POD_SSH:-}" ]]; then
    err "$name — POD_SSH unset; operator must run: grep -i 'CUDA out of memory' $POD_SPEACHES_LOG (MUST be empty)"
    return 0
  fi
  local hits
  hits=$($POD_SSH "grep -ci 'CUDA out of memory' $POD_SPEACHES_LOG 2>/dev/null || echo 0")
  if [[ "$hits" == "0" ]]; then
    pass "$name — no 'CUDA out of memory' in $POD_SPEACHES_LOG (device_index landed off the contended GPU)"
    return 0
  fi
  fail "$name — $hits 'CUDA out of memory' line(s) in $POD_SPEACHES_LOG (Pitfall 2: max-free pick collided with Qwen)"
  return 1
}

# -----------------------------------------------------------------------------
# Teardown — force-down + assert BestEffortDestroy left no orphan (T-14-08).
# -----------------------------------------------------------------------------
teardown_and_assert_destroy() {
  local name="$1"
  log "$name — tearing down: primary force-down"
  primary_force_down
  if [[ "$DRY_RUN" == 1 ]]; then
    log "$name — (dry-run: would wait asleep + assert Vast instance count == 0)"
    return 0
  fi
  wait_for_primary_asleep || true
  local n
  n=$(vast_instance_count)
  if [[ "$n" == "0" ]]; then
    pass "$name — BestEffortDestroy clean: 0 live Vast instances (no orphan)"
    return 0
  fi
  fail "$name — $n live Vast instance(s) after force-down — POSSIBLE ORPHAN, operator must DELETE via Vast UI/API"
  return 1
}

# -----------------------------------------------------------------------------
# Shape runners.
# -----------------------------------------------------------------------------
uat_a_3090() {
  echo "[uat] UAT A — 1x3090 (24 GB) -> gemini-stt  (STT-SHAPE-3090 + STT-MIGRATE)"
  vast_credit_check || return $?
  ensure_fixtures
  primary_force_up
  wait_for_primary_ready || { teardown_and_assert_destroy "uat-a-3090"; return 1; }
  local rc=0
  assert_whisper_device "cpu" "uat-a-3090" || rc=1
  post_transcription_timed "${NORMAL_KEY:?NORMAL_KEY required}" "$WAV_SPEECH_10S"
  # 24 GB pod reports cpu -> gateway must NOT override -> tier-1 gemini-stt (~2-3s).
  assert_stt_route "uat-a-3090" "200" "0.5" "8.0" "gemini-stt (tier-1, NOT the pod)" || rc=1
  teardown_and_assert_destroy "uat-a-3090" || rc=1
  return $rc
}

uat_b_gpu() {
  echo "[uat] UAT B — >=30 GB shape (2x3090 or 5090) -> pod whisper-GPU  (STT-SHAPE-GPU)"
  echo "[uat] PRECONDITION: operator applied the >=30GB allowlist/shape override per the 06.8 ladder runbook"
  vast_credit_check || return $?
  ensure_fixtures
  primary_force_up
  wait_for_primary_ready || { teardown_and_assert_destroy "uat-b-gpu"; return 1; }
  local rc=0
  assert_whisper_device "cuda" "uat-b-gpu" || rc=1
  post_transcription_timed "${NORMAL_KEY:?NORMAL_KEY required}" "$WAV_SPEECH_10S"
  # >=30 GB pod reports cuda -> gateway overrides STT -> pod local-stt GPU (<5s).
  assert_stt_route "uat-b-gpu" "200" "0.1" "5.0" "pod local-stt (whisper-GPU)" || rc=1
  assert_no_cuda_oom "uat-b-gpu" || rc=1
  teardown_and_assert_destroy "uat-b-gpu" || rc=1
  return $rc
}

usage() {
  cat <<EOF
Phase 14 VRAM-adaptive STT live UAT driver

Usage:
  $0 3090 [--dry-run]      UAT A — 1x3090 (24 GB): pod reports cpu, STT -> gemini-stt
  $0 gpu  [--dry-run]      UAT B — >=30 GB shape: pod reports cuda, STT -> pod GPU, no OOM
  $0 --credit-check        Run ONLY the Vast credit pre-check (MEMORY rule) + exit
  $0 --cleanup             primary force-down + assert no orphan Vast instance + exit

Required env:
  NORMAL_KEY        converseai tenant key (data_class=normal)
  VAST_AI_API_KEY   Vast.ai API key (credit pre-check + orphan audit)

Optional env:
  GATEWAY_URL       $GATEWAY_URL
  GATEWAYCTL_CMD    $GATEWAYCTL_CMD
  POD_HOST / POD_9100_PORT   pod :9100 device-report endpoint (else read from gatewayctl)
  POD_SSH           ssh target to grep $POD_SPEACHES_LOG for CUDA OOM
  VAST_MIN_CREDIT_USD  credit floor (default $VAST_MIN_CREDIT_USD)

Contract under test (Plan 14-01 reads / Plan 14-02 serves):
  GET :9100$DEVICE_REPORT_PATH -> {"whisper_device":"cuda"} (>=30GB) | {"whisper_device":"cpu"} (24GB)
  cpu  -> gateway does NOT override STT -> tier-1 gemini-stt
  cuda -> gateway overrides STT -> pod local-stt (GPU), MUST have no "CUDA out of memory"
EOF
}

main() {
  local args=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run) DRY_RUN=1; shift ;;
      *)         args+=("$1"); shift ;;
    esac
  done
  set -- "${args[@]}"

  [[ $# -eq 0 ]] && { usage; exit 0; }

  case "$1" in
    3090) uat_a_3090 ;;
    gpu)  uat_b_gpu ;;
    --credit-check) vast_credit_check ;;
    --cleanup)      teardown_and_assert_destroy "cleanup" ;;
    -h|--help)      usage ;;
    *) echo "Unknown arg: $1" >&2; usage; exit 2 ;;
  esac
}

main "$@"
