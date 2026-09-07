#!/bin/bash
# Pod unificado 3060: speaches (STT/TTS :8000) + Infinity rerank+embed (:7998)
# Roda a cada boot do pod. Logs: /root/unified-*.log
#
# Infinity instala PINADO de /root/infinity-freeze.txt. O provisioner
# (unified3060.build_onstart) PREPENDE heredocs escrevendo o freeze e o
# disk-guard antes deste corpo — onstart auto-contido, SEM depender de
# ssh/scp (2026-09-07: key ssh nao injeta em instancia nova pos-create;
# attach via API deu success mas o sshd do pod nao reconhece sem reboot).
# O pip "solto" (infinity-emb[all] + transformers git) quebrou em 2026-09-02:
# colpali-engine novo -> ResolutionImpossible. torch +cu121 nao existe no
# PyPI -> extra-index obrigatorio.
set -x
exec > /root/onstart.log 2>&1

# ---- disk-guard (escrito pelo prologo do provisioner) ----
if [ -f /root/disk-guard.sh ]; then
  chmod +x /root/disk-guard.sh
  pgrep -f 'bash /root/disk-guard.sh' >/dev/null || \
    setsid /root/disk-guard.sh </dev/null >/dev/null 2>&1 &
fi

# ---- speaches (STT/TTS) ----
export WHISPER_MODEL="${WHISPER_MODEL:-Systran/faster-whisper-large-v3}"
cd /home/ubuntu/speaches || cd /
UVICORN_BIN=$(command -v uvicorn || echo /home/ubuntu/speaches/.venv/bin/uvicorn)
nohup "$UVICORN_BIN" --factory speaches.main:create_app --host 0.0.0.0 --port 8000 \
  > /root/unified-speaches.log 2>&1 &

# ---- Infinity rerank+embed (dual-model, MESMO processo) ----
if [ ! -x /opt/infinity/bin/infinity_emb ]; then
  if [ -f /root/infinity-freeze.txt ]; then
    python3 -m venv /opt/infinity
    /opt/infinity/bin/pip install --no-cache-dir --no-deps \
      --extra-index-url https://download.pytorch.org/whl/cu121 \
      -r /root/infinity-freeze.txt >> /root/unified-pipinstall.log 2>&1
  else
    echo "infinity-freeze.txt ausente — Infinity adiado (provisioner instala)"
  fi
fi
export HF_HOME=/root/.cache/huggingface
[ -x /opt/infinity/bin/infinity_emb ] && nohup /opt/infinity/bin/infinity_emb v2 \
  --model-id BAAI/bge-reranker-v2-m3 \
  --served-model-name bge-reranker-v2-m3 \
  --model-id BAAI/bge-m3 \
  --served-model-name bge-m3 \
  --engine torch --device cuda --dtype float16 \
  --url-prefix /v1 --host 0.0.0.0 --port 7998 \
  > /root/unified-infinity.log 2>&1 &

echo "onstart concluido $(date)"
