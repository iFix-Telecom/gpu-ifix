#!/usr/bin/env bash
# pod/scripts/upload-weights.sh — one-shot MinIO weight upload (operator action).
#
# Run this ONCE (or when weights need rotating per D-06 versioning).
# Requires ~25 GB free disk + fast upstream + mc + jq + curl.
#
# Usage:
#   MINIO_ENDPOINT=https://... \
#   MINIO_ACCESS_KEY=... \
#   MINIO_SECRET_KEY=... \
#   MINIO_BUCKET=ifix-ai-weights \
#   ./pod/scripts/upload-weights.sh [--weights-version v1.0.0] [--workdir /tmp/weights-stage] [--hf-token TOKEN]
#
# At the end, prints three WEIGHTS_*_SHA256 values to paste into GH Secrets.
#
# --- CANDIDATE vs CANONICAL (Phase 06.8 STT fix iii) -------------------------
# The whisper artifact is now produced in HuggingFace-hub-cache layout (a top-
# level `models--Systran--faster-whisper-large-v3/` dir with refs/main +
# snapshots/<hash>/) so that speaches' get_model_card_data_or_raise() gate can
# resolve it via HF_HUB_CACHE=/weights/whisper (see pod/primary/supervisord.conf).
#
# Because every MinIO key embeds ${WEIGHTS_VERSION}, you can stage a CANDIDATE
# whisper tarball WITHOUT mutating the canonical one that live consumers depend on:
#
#   ./upload-weights.sh --weights-version v1.0.0-hf-cache-candidate
#       -> writes ONLY whisper-large-v3/v1.0.0-hf-cache-candidate/model.tar.gz
#          (Qwen + BGE-M3 are SKIPPED on a candidate run — they stay pinned to
#           the canonical whisper-large-v3/v1.0.0/... keys, untouched.)
#
#   ./upload-weights.sh         (default WEIGHTS_VERSION=v1.0.0)
#       -> writes all three canonical artifacts as before.
#
# Plan 02 points the validation pod at the candidate key, proves transcription
# 200 live, THEN promotes (copies candidate -> canonical). This script never
# overwrites canonical whisper until that validation passes.

set -euo pipefail

: "${MINIO_ENDPOINT:?missing}" "${MINIO_ACCESS_KEY:?missing}" "${MINIO_SECRET_KEY:?missing}" "${MINIO_BUCKET:?missing}"

WEIGHTS_VERSION="v1.0.0"
WORKDIR="/tmp/ifix-weights-stage"
HF_TOKEN="${HF_TOKEN:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --weights-version) WEIGHTS_VERSION="$2"; shift 2;;
    --workdir)         WORKDIR="$2";         shift 2;;
    --hf-token)        HF_TOKEN="$2";        shift 2;;
    *) echo "unknown arg $1" >&2; exit 2;;
  esac
done

log() { printf '[%s] [upload-weights] %s\n' "$(date -Iseconds)" "$*" >&2; }

# A run is a "candidate" run when WEIGHTS_VERSION is not the canonical v1.0.0.
# On a candidate run we (re)produce ONLY the whisper artifact under the
# candidate key and SKIP Qwen + BGE-M3 entirely so they stay pinned to the
# canonical v1.0.0 keys their live consumers expect.
CANONICAL_VERSION="v1.0.0"
IS_CANDIDATE_RUN="false"
if [[ "${WEIGHTS_VERSION}" != "${CANONICAL_VERSION}" ]]; then
  IS_CANDIDATE_RUN="true"
  log "CANDIDATE run (version=${WEIGHTS_VERSION}): only whisper will be (re)produced; Qwen + BGE-M3 SKIPPED (canonical ${CANONICAL_VERSION} untouched)."
fi

# --- prerequisites --------------------------------------------------------
for bin in mc jq curl sha256sum tar; do
  command -v "$bin" >/dev/null 2>&1 || { log "missing required binary: $bin"; exit 1; }
done

mkdir -p "${WORKDIR}"
cd "${WORKDIR}"

log "version=${WEIGHTS_VERSION} workdir=${WORKDIR}"

# --- configure mc alias ---------------------------------------------------
mc alias set ifix "${MINIO_ENDPOINT}" "${MINIO_ACCESS_KEY}" "${MINIO_SECRET_KEY}" >/dev/null
mc mb --ignore-existing "ifix/${MINIO_BUCKET}" >/dev/null

# --- helpers --------------------------------------------------------------
hf_download() {
  local repo="$1" filename="$2" dest="$3"
  local url="https://huggingface.co/${repo}/resolve/main/${filename}"
  local args=(-fsSL -o "${dest}" "${url}")
  if [[ -n "${HF_TOKEN}" ]]; then
    args=(-H "Authorization: Bearer ${HF_TOKEN}" "${args[@]}")
  fi
  log "HF get ${repo}/${filename}"
  curl "${args[@]}"
}

# hf_download_checked(repo, filename, dest): download via hf_download then assert
# the file is present + non-empty + log its sha256. A truncated/corrupt/empty
# download ABORTS the script so a partial file can never enter the tarball
# (set -e already propagates curl failure; this adds an explicit non-empty
# guard for the case where curl exits 0 but writes a 0-byte / partial file).
hf_download_checked() {
  local repo="$1" filename="$2" dest="$3"
  hf_download "${repo}" "${filename}" "${dest}" \
    || { log "FATAL: download failed for ${repo}/${filename}"; exit 1; }
  if [[ ! -s "${dest}" ]]; then
    log "FATAL: corrupt/empty download (zero bytes) for ${repo}/${filename} -> ${dest}"
    exit 1
  fi
  local sum
  sum="$(sha256sum "${dest}" | awk '{print $1}')"
  log "verified ${filename}: $(wc -c < "${dest}") bytes sha256=${sum:0:12}..."
}

upload_with_sidecar() {
  local local_path="$1" s3_key="$2"
  log "computing sha256 ${local_path}"
  local sum
  sum="$(sha256sum "${local_path}" | awk '{print $1}')"
  log "uploading ${local_path} -> s3://${MINIO_BUCKET}/${s3_key} (${sum:0:12}...)"
  mc cp --quiet "${local_path}" "ifix/${MINIO_BUCKET}/${s3_key}"
  printf '%s' "${sum}" | mc pipe --quiet "ifix/${MINIO_BUCKET}/${s3_key}.sha256"
  printf '%s' "${sum}"
}

# --- 1) Qwen 3.5 27B Q4_K_M GGUF (single file) ---------------------------
# Canonical-only: skipped on a candidate run so v1.0.0/model.gguf is never touched.
QWEN_SHA="(skipped on candidate run)"
if [[ "${IS_CANDIDATE_RUN}" == "false" ]]; then
  log "=== Qwen 3.5 27B Q4_K_M ==="
  QWEN_FILE="${WORKDIR}/qwen.gguf"
  if [[ ! -f "${QWEN_FILE}" ]]; then
    hf_download "unsloth/Qwen3.5-27B-GGUF" "Qwen3.5-27B-Q4_K_M.gguf" "${QWEN_FILE}"
  fi
  QWEN_SHA=$(upload_with_sidecar "${QWEN_FILE}" "qwen3.5-27b-Q4_K_M/${WEIGHTS_VERSION}/model.gguf")
else
  log "skip Qwen (candidate run): canonical qwen3.5-27b-Q4_K_M/${CANONICAL_VERSION}/model.gguf untouched"
fi

# --- 2) Whisper large-v3 (HF-hub-cache layout tarball) -------------------
# Phase 06.8 STT fix iii: the tarball top-level entry MUST be
#   models--Systran--faster-whisper-large-v3/
# so that onstart.go's `tar -xzf /weights/whisper/model.tar.gz -C /weights/whisper`
# yields /weights/whisper/models--Systran--faster-whisper-large-v3/{refs,snapshots}
# and speaches (HF_HUB_CACHE=/weights/whisper) resolves it via its model-card gate.
# README.md is MANDATORY: speaches' get_model_card_data_or_raise() 500s with
# MODEL_CARD_DOESNT_EXISTS without snapshots/<hash>/README.md (Pitfall 1).
# Files in snapshots/ are REAL copies, never symlinks (Pitfall 2) — tar/untar
# cannot break the chain.
# Resulting on-disk path inside HF_HUB_CACHE:
#   models--Systran--faster-whisper-large-v3/snapshots/<hash>/{model.bin,...,README.md}
log "=== Whisper large-v3 (HF-hub-cache layout) ==="
WHISPER_REPO="Systran/faster-whisper-large-v3"
# Resolve the REAL HF commit hash of main (Assumption A1: real hash is zero-risk;
# Plan 02 validates in-pod whether an arbitrary hash would also work).
WHISPER_HASH="$(curl -fsSL "https://huggingface.co/api/models/${WHISPER_REPO}" | jq -r '.sha')"
if [[ -z "${WHISPER_HASH}" || "${WHISPER_HASH}" == "null" ]]; then
  log "FATAL: could not resolve HF commit hash for ${WHISPER_REPO}"
  exit 1
fi
log "resolved ${WHISPER_REPO} main commit hash: ${WHISPER_HASH}"

WHISPER_STAGE="${WORKDIR}/whisper-stage"
WHISPER_CACHE_DIR="${WHISPER_STAGE}/models--Systran--faster-whisper-large-v3"
WHISPER_SNAP_DIR="${WHISPER_CACHE_DIR}/snapshots/${WHISPER_HASH}"
rm -rf "${WHISPER_STAGE}"
mkdir -p "${WHISPER_CACHE_DIR}/refs" "${WHISPER_SNAP_DIR}"
# refs/main content = the commit hash, NO trailing newline.
printf '%s' "${WHISPER_HASH}" > "${WHISPER_CACHE_DIR}/refs/main"

# Download each file straight into snapshots/<hash>/ as a real file copy.
# README.md is MANDATORY (the old script never fetched it); every file is
# integrity-checked (non-empty + sha256 logged) before bundling.
for f in model.bin config.json tokenizer.json vocabulary.json preprocessor_config.json README.md; do
  hf_download_checked "${WHISPER_REPO}" "${f}" "${WHISPER_SNAP_DIR}/${f}"
done

WHISPER_TAR="${WORKDIR}/whisper.tar.gz"
# Tar from the stage ROOT so the top-level entry is models--Systran--…/ (NOT flat).
tar -C "${WHISPER_STAGE}" -czf "${WHISPER_TAR}" .
WHISPER_SHA=$(upload_with_sidecar "${WHISPER_TAR}" "whisper-large-v3/${WEIGHTS_VERSION}/model.tar.gz")

# --- 3) BGE-M3 (HF cache tarball) ----------------------------------------
# Canonical-only: skipped on a candidate run so v1.0.0/model.tar.gz is never touched.
BGE_SHA="(skipped on candidate run)"
if [[ "${IS_CANDIDATE_RUN}" == "false" ]]; then
  log "=== BGE-M3 ==="
  BGE_DIR="${WORKDIR}/bge-m3"
  mkdir -p "${BGE_DIR}"
  for f in config.json tokenizer.json tokenizer_config.json sentencepiece.bpe.model \
           pytorch_model.bin model.safetensors sentence_bert_config.json; do
    if [[ ! -f "${BGE_DIR}/${f}" ]]; then
      hf_download "BAAI/bge-m3" "${f}" "${BGE_DIR}/${f}" || log "note: ${f} not found (skipping)"
    fi
  done
  BGE_TAR="${WORKDIR}/bge-m3.tar.gz"
  tar -C "${BGE_DIR}" -czf "${BGE_TAR}" .
  BGE_SHA=$(upload_with_sidecar "${BGE_TAR}" "bge-m3/${WEIGHTS_VERSION}/model.tar.gz")
else
  log "skip BGE-M3 (candidate run): canonical bge-m3/${CANONICAL_VERSION}/model.tar.gz untouched"
fi

# --- final instructions ---------------------------------------------------
cat <<EOF

====================================================================
  Upload complete. Paste these into GitHub Secrets for smoke.yml
  (Repo Settings > Secrets and variables > Actions > New repository secret)
====================================================================

  WEIGHTS_QWEN_SHA256    = ${QWEN_SHA}
  WEIGHTS_WHISPER_SHA256 = ${WHISPER_SHA}
  WEIGHTS_BGE_M3_SHA256  = ${BGE_SHA}

  Whisper repo commit hash baked into the cache layout:
    ${WHISPER_HASH}  (models--Systran--faster-whisper-large-v3/refs/main)

  Also set (one-time):
    MINIO_ENDPOINT  = ${MINIO_ENDPOINT}
    MINIO_BUCKET    = ${MINIO_BUCKET}
    MINIO_ACCESS_KEY = (the key you used above)
    MINIO_SECRET_KEY = (the secret you used above)

  Object keys for this run (version=${WEIGHTS_VERSION}):
    whisper-large-v3/${WEIGHTS_VERSION}/model.tar.gz   (HF-hub-cache layout)
EOF
if [[ "${IS_CANDIDATE_RUN}" == "false" ]]; then
cat <<EOF
    qwen3.5-27b-Q4_K_M/${WEIGHTS_VERSION}/model.gguf
    bge-m3/${WEIGHTS_VERSION}/model.tar.gz
EOF
else
cat <<EOF
    (Qwen + BGE-M3 SKIPPED on this candidate run; canonical ${CANONICAL_VERSION} keys untouched)

  Candidate run: point the validation pod (Plan 02) at the candidate whisper key
  via PRIMARY_WHISPER_WEIGHTS_KEY=whisper-large-v3/${WEIGHTS_VERSION}/model.tar.gz
  (+ matching PRIMARY_WHISPER_WEIGHTS_SHA256). Promote to canonical only AFTER
  live transcription 200 passes.
EOF
fi
cat <<EOF

  Next step: trigger smoke.yml manually:
    gh workflow run smoke.yml -f image_tag=develop
====================================================================
EOF
