#!/usr/bin/env bash
# pod/onstart.sh
# Vast.ai onstart hook for ifix-ai-pod.
#
# Purpose: Download weights from MinIO (D-02, D-03) in parallel with docker
# compose startup; block until health-bridge reports readiness (D-04 target
# cold-start ≤5 min).
#
# Phase 11.2 (2026-06-07): STT tier-0 RESTORED — speaches + faster-whisper
# back on the pod. The Phase 11.1 STT-off-pod removal was reverted because
# the tier-1 OpenAI-only fallback proved too costly ($0.36/h). WHISPER weight
# download/extract is back; tier-1 cascade (gemini-stt → groq-whisper →
# openai-whisper) only fires when the pod is OFF (Phase 11.2 Plan 03 dispatcher).
#
# Env vars: see pod/.env.example. Injected by Vast.ai pod creation (plan 08
# smoke.yml sets them via the Vast.ai REST API).

set -euo pipefail

# --- log to stdout AND /var/log/onstart.log so Vast.ai console captures ---
mkdir -p /var/log
exec > >(tee -a /var/log/onstart.log) 2>&1

export TZ=America/Sao_Paulo
export DEBIAN_FRONTEND=noninteractive

SECONDS=0
section() { printf '\n======== [onstart] %s (t=%ss) ========\n' "$1" "$SECONDS"; }
log()     { printf '[%s] %s\n' "$(date -Iseconds)" "$*"; }

section "env preflight"
: "${IFIX_AI_POD_ROOT:=/opt/ifix-ai-pod}"
: "${WEIGHTS_DIR:=/weights}"
: "${COMPOSE_FILE:=${IFIX_AI_POD_ROOT}/docker-compose.yml}"
: "${ENV_FILE:=${IFIX_AI_POD_ROOT}/.env}"
: "${READINESS_URL:=http://127.0.0.1:9100/health/ready}"
: "${READINESS_TIMEOUT_SECONDS:=600}"

log "IFIX_AI_POD_ROOT = ${IFIX_AI_POD_ROOT}"
log "WEIGHTS_DIR      = ${WEIGHTS_DIR}"
log "COMPOSE_FILE     = ${COMPOSE_FILE}"
log "ENV_FILE         = ${ENV_FILE}"
log "READINESS_URL    = ${READINESS_URL}"
log "READINESS_TIMEOUT_SECONDS = ${READINESS_TIMEOUT_SECONDS}"

# Required secrets + versioned keys from pod creation env
: "${MINIO_ENDPOINT:?missing MINIO_ENDPOINT}"
: "${MINIO_ACCESS_KEY:?missing MINIO_ACCESS_KEY}"
: "${MINIO_SECRET_KEY:?missing MINIO_SECRET_KEY}"
: "${MINIO_BUCKET:?missing MINIO_BUCKET}"
: "${WEIGHTS_QWEN_KEY:?missing WEIGHTS_QWEN_KEY}"
: "${WEIGHTS_QWEN_SHA256:?missing WEIGHTS_QWEN_SHA256}"
: "${WEIGHTS_WHISPER_KEY:?missing WEIGHTS_WHISPER_KEY}"
: "${WEIGHTS_WHISPER_SHA256:?missing WEIGHTS_WHISPER_SHA256}"
: "${WEIGHTS_BGE_M3_KEY:?missing WEIGHTS_BGE_M3_KEY}"
: "${WEIGHTS_BGE_M3_SHA256:?missing WEIGHTS_BGE_M3_SHA256}"

section "install docker compose prerequisites (if missing)"
# Vast.ai base images typically include docker + compose; if not, bail loudly.
if ! command -v docker >/dev/null 2>&1; then
  log "FATAL: docker not found on host — Vast.ai image must include docker engine"
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  log "FATAL: docker compose plugin missing — install docker-compose-plugin in base image"
  exit 1
fi

section "materialize pod layout at ${IFIX_AI_POD_ROOT}"
mkdir -p "${IFIX_AI_POD_ROOT}"

# COMPOSE_FILE: if not already placed by Vast.ai image provisioning, fetch from the
# running container's baked-in copy. This script assumes the pod base image includes
# /opt/ifix-ai-pod/docker-compose.yml — it is COPIED by a companion provisioner
# step (document in pod/README.md); for Phase 1 smoke, the smoke.yml workflow
# scp's the file onto the host before invoking onstart.sh.
if [[ ! -f "${COMPOSE_FILE}" ]]; then
  log "FATAL: compose file not found at ${COMPOSE_FILE}"
  log "  — smoke.yml (plan 08) must upload docker-compose.yml + .env before invoking onstart"
  exit 1
fi
if [[ ! -f "${ENV_FILE}" ]]; then
  log "warning: ${ENV_FILE} not found — using only environment variables"
fi

section "download weights from MinIO in parallel (D-02, D-03)"
# Resolve download-weights.sh — same dir as onstart.sh OR /opt/ifix-ai-pod/scripts/
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DL_SCRIPT="${SCRIPT_DIR}/scripts/download-weights.sh"
if [[ ! -x "${DL_SCRIPT}" ]]; then
  DL_SCRIPT="${IFIX_AI_POD_ROOT}/scripts/download-weights.sh"
fi
if [[ ! -x "${DL_SCRIPT}" ]]; then
  log "FATAL: download-weights.sh not found (tried ${SCRIPT_DIR}/scripts/ and ${IFIX_AI_POD_ROOT}/scripts/)"
  exit 1
fi
"${DL_SCRIPT}" "${WEIGHTS_DIR}"
log "weight download finished (t=${SECONDS}s)"

section "docker compose up -d"
COMPOSE_ARGS=(-f "${COMPOSE_FILE}")
if [[ -f "${ENV_FILE}" ]]; then
  COMPOSE_ARGS+=(--env-file "${ENV_FILE}")
fi
docker compose "${COMPOSE_ARGS[@]}" up -d

section "wait for health-bridge readiness (D-11 aggregate)"
# Poll /health/ready until status != "unknown" (probes have run at least once).
# Note: health-bridge reports degraded during upstream cold load — that's fine,
# it means the probes are firing. We only require non-unknown.
DEADLINE=$(( SECONDS + READINESS_TIMEOUT_SECONDS ))
READY=0
body=""
while [[ ${SECONDS} -lt ${DEADLINE} ]]; do
  body="$(curl -fsS "${READINESS_URL}" 2>/dev/null || true)"
  status="$(printf '%s' "${body}" | grep -oE '"status":"[a-z]+"' | head -1 | cut -d'"' -f4 || true)"
  if [[ -n "${status}" && "${status}" != "unknown" ]]; then
    log "health-bridge aggregate status=${status} (t=${SECONDS}s)"
    READY=1
    break
  fi
  sleep 5
done

if [[ ${READY} -ne 1 ]]; then
  log "FATAL: health-bridge not ready within ${READINESS_TIMEOUT_SECONDS}s"
  log "last probed body: ${body:-<none>}"
  docker compose "${COMPOSE_ARGS[@]}" ps
  exit 1
fi

section "onstart complete (t=${SECONDS}s total)"
log "pod is serving on:"
log "  - llama LLM:       0.0.0.0:8000 (OpenAI-compat)"
log "  - speaches STT:    0.0.0.0:8001 (OpenAI-compat /v1/audio/transcriptions)"
log "  - chatterbox TTS:  0.0.0.0:8003 (OpenAI-compat /v1/audio/speech)"
log "  - health-bridge:   0.0.0.0:9100 (internal probes)"
log "  - dcgm-exporter:   0.0.0.0:9400 (Prometheus metrics)"
log ""
log "cold-start target was 3-5 min (D-04). Measured: ${SECONDS}s."
if [[ ${SECONDS} -gt 300 ]]; then
  log "WARN: cold-start exceeded 5 min target; investigate image pull or MinIO throughput"
fi
