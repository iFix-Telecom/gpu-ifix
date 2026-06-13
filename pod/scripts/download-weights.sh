#!/usr/bin/env bash
# pod/scripts/download-weights.sh
# Parallel MinIO download + SHA-256 validation for the pod's three weights (D-02..D-06).
#
# Usage:
#   ./pod/scripts/download-weights.sh <weights-dir>
#
# Env vars required:
#   MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY, MINIO_BUCKET
#   WEIGHTS_QWEN_KEY,    WEIGHTS_QWEN_SHA256
#   WEIGHTS_WHISPER_KEY, WEIGHTS_WHISPER_SHA256
#   WEIGHTS_BGE_M3_KEY,  WEIGHTS_BGE_M3_SHA256
# (Phase 11.2 restored WEIGHTS_WHISPER_* after Phase 11.1 stripped it.)
#
# Exit codes:
#   0 — all weights downloaded, verified, extracted
#   1 — missing env var
#   2 — download failure
#   3 — checksum mismatch (corrupted or tampered — D-05 abort)
#   4 — extraction failure
#   5 — mc (MinIO client) install failure

set -euo pipefail

WEIGHTS_DIR="${1:-/weights}"
: "${MINIO_ENDPOINT:?missing}" "${MINIO_ACCESS_KEY:?missing}" "${MINIO_SECRET_KEY:?missing}" "${MINIO_BUCKET:?missing}"
: "${WEIGHTS_QWEN_KEY:?missing}" "${WEIGHTS_QWEN_SHA256:?missing}"
: "${WEIGHTS_WHISPER_KEY:?missing}" "${WEIGHTS_WHISPER_SHA256:?missing}"
: "${WEIGHTS_BGE_M3_KEY:?missing}" "${WEIGHTS_BGE_M3_SHA256:?missing}"

# --- logging helper -------------------------------------------------------
log() { printf '[%s] [download-weights] %s\n' "$(date -Iseconds)" "$*"; }

# --- ensure mc (MinIO client) is available --------------------------------
ensure_mc() {
  if command -v mc >/dev/null 2>&1; then return 0; fi
  log "installing mc (MinIO client)..."
  curl -fsSL https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc || exit 5
  chmod +x /usr/local/bin/mc
}

ensure_mc
mc alias set ifix "${MINIO_ENDPOINT}" "${MINIO_ACCESS_KEY}" "${MINIO_SECRET_KEY}" >/dev/null

mkdir -p "${WEIGHTS_DIR}/qwen" "${WEIGHTS_DIR}/whisper" "${WEIGHTS_DIR}/bge-m3" "${WEIGHTS_DIR}/.tmp"

# --- download one object from MinIO, check sha256 -------------------------
# $1 = s3 key (relative to bucket)
# $2 = local destination path
# $3 = expected SHA-256 (64 hex)
download_and_verify() {
  local key="$1" dest="$2" expected="$3"
  local url="ifix/${MINIO_BUCKET}/${key}"
  log "fetching ${key} -> ${dest}"
  if ! mc cp --quiet "${url}" "${dest}"; then
    log "ERROR: mc cp failed for ${key}"
    return 2
  fi
  local actual
  actual="$(sha256sum "${dest}" | awk '{print $1}')"
  if [[ "${actual}" != "${expected}" ]]; then
    log "FATAL: sha256 mismatch for ${dest}"
    log "  expected: ${expected}"
    log "  actual:   ${actual}"
    return 3
  fi
  log "ok ${dest} (sha256=${actual:0:12}...)"
  return 0
}

# --- kick off 3 parallel downloads ----------------------------------------
log "starting parallel downloads (D-03)"

QWEN_DEST="${WEIGHTS_DIR}/qwen/model.gguf"
WHISPER_DEST="${WEIGHTS_DIR}/.tmp/whisper.tar.gz"
BGE_DEST="${WEIGHTS_DIR}/.tmp/bge-m3.tar.gz"

download_and_verify "${WEIGHTS_QWEN_KEY}"    "${QWEN_DEST}"    "${WEIGHTS_QWEN_SHA256}"    &
PID_QWEN=$!
download_and_verify "${WEIGHTS_WHISPER_KEY}" "${WHISPER_DEST}" "${WEIGHTS_WHISPER_SHA256}" &
PID_WHISPER=$!
download_and_verify "${WEIGHTS_BGE_M3_KEY}"  "${BGE_DEST}"     "${WEIGHTS_BGE_M3_SHA256}"  &
PID_BGE=$!

# wait for all 3 — fail on any non-zero
FAIL=0
for pid in "$PID_QWEN" "$PID_WHISPER" "$PID_BGE"; do
  if ! wait "$pid"; then
    log "download/verify failed for pid $pid"
    FAIL=1
  fi
done
if [[ "$FAIL" -ne 0 ]]; then
  log "aborting: one or more weights failed download or checksum"
  exit 3
fi

# --- extract tarballs (Whisper + BGE-M3) ----------------------------------
# Phase 06.8 fix-iii / Pitfall 4: extract target MUST be /weights/whisper
# so it aligns with HF_HUB_CACHE in pod/primary/supervisord.conf
# [program:speaches] environment. The tarball is pre-shaped HF cache
# (models--Systran--faster-whisper-large-v3/{refs,snapshots/<hash>/}).
log "extracting whisper tarball"
tar -xzf "${WHISPER_DEST}" -C "${WEIGHTS_DIR}/whisper" || exit 4

log "extracting bge-m3 tarball"
tar -xzf "${BGE_DEST}" -C "${WEIGHTS_DIR}/bge-m3" || exit 4

# --- cleanup --------------------------------------------------------------
rm -rf "${WEIGHTS_DIR}/.tmp"

log "all weights present and verified in ${WEIGHTS_DIR}"
log "inventory:"
ls -lh "${WEIGHTS_DIR}/qwen/" "${WEIGHTS_DIR}/whisper/" "${WEIGHTS_DIR}/bge-m3/" 2>&1 | sed 's/^/  /'
