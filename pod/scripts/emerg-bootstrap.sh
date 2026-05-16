#!/bin/bash
# pod/scripts/emerg-bootstrap.sh — Phase 6 emergency-pod LLM-only bootstrap.
#
# The Phase 1 multi-service stack (llama + whisper + embed + health-bridge +
# dcgm) is launched on the Vast.ai HOST by smoke.yml via SSH/SCP of
# docker-compose.yml + onstart.sh. Phase 6 reconciler creates instances
# without SSH access, so this script ships INSIDE the pod image and runs
# as the container CMD: download Qwen weights from MinIO if missing, then
# exec llama-server with the same flags Phase 1 compose uses for the
# "llama" service.
#
# Whisper + embed are intentionally omitted from emergency pods (CONTEXT.md
# D-C2 — local-llm chat is the only signal source). Phase 3 fallback
# handles STT/embed via OpenAI/OpenRouter while emergency is active.
#
# Env contract (passed via Vast.ai CreateRequest.Env from lifecycle.go):
#   MINIO_ENDPOINT, MINIO_ACCESS_KEY, MINIO_SECRET_KEY, MINIO_BUCKET,
#   WEIGHTS_QWEN_KEY (object path), WEIGHTS_QWEN_SHA256 (hex digest).

set -euo pipefail

WEIGHTS_PATH=/weights/qwen/model.gguf
mkdir -p "$(dirname "$WEIGHTS_PATH")"

if [[ ! -f "$WEIGHTS_PATH" ]]; then
  : "${MINIO_ENDPOINT:?missing MINIO_ENDPOINT}"
  : "${MINIO_ACCESS_KEY:?missing MINIO_ACCESS_KEY}"
  : "${MINIO_SECRET_KEY:?missing MINIO_SECRET_KEY}"
  : "${MINIO_BUCKET:?missing MINIO_BUCKET}"
  : "${WEIGHTS_QWEN_KEY:?missing WEIGHTS_QWEN_KEY}"
  : "${WEIGHTS_QWEN_SHA256:?missing WEIGHTS_QWEN_SHA256}"

  echo "[emerg-bootstrap] downloading qwen weights from MinIO..."
  if ! command -v mc >/dev/null 2>&1; then
    curl -sLo /usr/local/bin/mc "https://dl.min.io/client/mc/release/linux-amd64/mc"
    chmod +x /usr/local/bin/mc
  fi
  mc alias set ifix "$MINIO_ENDPOINT" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null
  mc cp "ifix/${MINIO_BUCKET}/${WEIGHTS_QWEN_KEY}" "$WEIGHTS_PATH"

  ACTUAL=$(sha256sum "$WEIGHTS_PATH" | awk '{print $1}')
  if [[ "$ACTUAL" != "$WEIGHTS_QWEN_SHA256" ]]; then
    echo "[emerg-bootstrap] FATAL: SHA-256 mismatch on $WEIGHTS_PATH" >&2
    echo "  expected: $WEIGHTS_QWEN_SHA256" >&2
    echo "  actual:   $ACTUAL" >&2
    exit 1
  fi
  echo "[emerg-bootstrap] weights ok ($(du -h "$WEIGHTS_PATH" | awk '{print $1}'))"
else
  echo "[emerg-bootstrap] weights already present, skipping download"
fi

echo "[emerg-bootstrap] launching llama-server..."
exec /app/llama-server \
  --host 0.0.0.0 \
  --port 8000 \
  -m "$WEIGHTS_PATH" \
  -ngl 99 \
  -np 2 \
  --ctx-size 16384 \
  --jinja \
  --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja
