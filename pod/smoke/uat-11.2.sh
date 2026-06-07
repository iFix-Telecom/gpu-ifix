#!/usr/bin/env bash
# Phase 11.2 Plan 01 — Wave 6 UAT driver skeleton (D-B13).
#
# OWNER: Plan 06 (final wiring) — stubs each of 6 mandatory cenários per CONTEXT D-B13.
# Each scenario currently `echo TODO` + `exit 0`; Wave 6 fills in actual curl/jq logic.
#
# Targets:
#   Gateway URL: https://ai-gateway-dev.ifixtelecom.com.br
#   API keys: provided via NORMAL_KEY + SENSITIVE_KEY env vars.
#   Operator: see CLAUDE.md "API Keys" section for current tenant keys.
#
# Usage:
#   ./uat-11.2.sh --all                       # run all 6 scenarios
#   ./uat-11.2.sh --scenario <name>           # run one
#   ./uat-11.2.sh --list                      # print scenario names + exit
#
# Scenario name list (CONTEXT D-B13 order):
#   1. pod-on-local-stt
#   2. pod-off-gemini
#   3. gemini-open-groq-takeover
#   4. gemini-groq-open-openai-takeover
#   5. sensitive-pod-on
#   6. sensitive-pod-off-503

set -euo pipefail

readonly GATEWAY_URL="${GATEWAY_URL:-https://ai-gateway-dev.ifixtelecom.com.br}"
# Tenant API keys MUST be provided via env (never hardcoded — public repo).
# Operator: export NORMAL_KEY=ifix_sk_... + SENSITIVE_KEY=ifix_sk_... before invoking.
readonly NORMAL_KEY="${NORMAL_KEY:?NORMAL_KEY env var required (converseai tenant key — see CLAUDE.md API Keys section)}"
readonly SENSITIVE_KEY="${SENSITIVE_KEY:?SENSITIVE_KEY env var required (telefonia tenant key — RES-08 sensitive)}"

readonly SCENARIOS=(
  pod-on-local-stt
  pod-off-gemini
  gemini-open-groq-takeover
  gemini-groq-open-openai-takeover
  sensitive-pod-on
  sensitive-pod-off-503
)

# -----------------------------------------------------------------------------
# Scenario stubs (Wave 6 will fill in actual curl + assertion logic)
# -----------------------------------------------------------------------------

scenario_pod_on_local_stt() {
  echo "[scenario] pod-on-local-stt — D-B13 #1"
  echo "  TODO: pod primary ON; POST 1s WAV w/ NORMAL_KEY; expect 200 + audit_log.upstream=local-stt"
  return 0
}

scenario_pod_off_gemini() {
  echo "[scenario] pod-off-gemini — D-B13 #2"
  echo "  TODO: pod primary OFF; POST 5s WAV w/ NORMAL_KEY; expect 200 + text plausível + audit_log.upstream=gemini-stt"
  return 0
}

scenario_gemini_open_groq_takeover() {
  echo "[scenario] gemini-open-groq-takeover — D-B13 #3"
  echo "  TODO: gatewayctl breaker force-open gemini-stt; pod OFF; POST WAV w/ NORMAL_KEY; expect 200 + audit_log.upstream=groq-whisper"
  return 0
}

scenario_gemini_groq_open_openai_takeover() {
  echo "[scenario] gemini-groq-open-openai-takeover — D-B13 #4"
  echo "  TODO: force-open gemini-stt + groq-whisper; pod OFF; POST WAV w/ NORMAL_KEY; expect 200 + audit_log.upstream=openai-whisper"
  return 0
}

scenario_sensitive_pod_on() {
  echo "[scenario] sensitive-pod-on — D-B13 #5"
  echo "  TODO: pod ON; POST WAV w/ SENSITIVE_KEY (telefonia); expect 200 + audit_log.upstream=local-stt"
  return 0
}

scenario_sensitive_pod_off_503() {
  echo "[scenario] sensitive-pod-off-503 — D-B13 #6 (RES-08 hard-block)"
  echo "  TODO: pod OFF; POST WAV w/ SENSITIVE_KEY; expect 503 + body upstream_unavailable_for_sensitive_tenant"
  return 0
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
Phase 11.2 UAT driver — stubs (Plan 06 wires final curl+jq logic)

Usage:
  $0 --all                       Run all 6 scenarios in order
  $0 --scenario <name>           Run one scenario
  $0 --list                      List scenario names

Targets:
  Gateway URL : $GATEWAY_URL
  Normal key  : ${NORMAL_KEY:0:14}…
  Sensitive   : ${SENSITIVE_KEY:0:14}…

Scenarios (D-B13):
$(for s in "${SCENARIOS[@]}"; do echo "  - $s"; done)
EOF
}

main() {
  if [[ $# -eq 0 ]]; then
    usage
    exit 0
  fi

  case "$1" in
    --all)
      for s in "${SCENARIOS[@]}"; do
        run_scenario "$s"
      done
      ;;
    --scenario)
      [[ $# -ge 2 ]] || { echo "missing scenario name" >&2; usage; exit 2; }
      run_scenario "$2"
      ;;
    --list)
      printf '%s\n' "${SCENARIOS[@]}"
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
