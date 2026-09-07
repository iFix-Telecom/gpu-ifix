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

# ---- speaches (STT/TTS) — loop supervisor: processo morreu em prod
# (2026-09-07, provavel OOM em maquina de 7GB RAM) e nada religava ----
export WHISPER_MODEL="${WHISPER_MODEL:-Systran/faster-whisper-large-v3}"
cd /home/ubuntu/speaches || cd /
UVICORN_BIN=$(command -v uvicorn || echo /home/ubuntu/speaches/.venv/bin/uvicorn)
if ! pgrep -f 'speaches-supervisor' >/dev/null; then
  nohup bash -c "exec -a speaches-supervisor bash -c 'while true; do
    if ! curl -sm3 -o /dev/null localhost:8000/health; then
      echo \"\$(date -Is) speaches down — subindo\" >> /root/unified-speaches.log
      \"$UVICORN_BIN\" --factory speaches.main:create_app --host 0.0.0.0 --port 8000 \
        >> /root/unified-speaches.log 2>&1
      echo \"\$(date -Is) speaches saiu rc=\$?\" >> /root/unified-speaches.log
    fi
    sleep 30
  done'" > /dev/null 2>&1 &
fi

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
if [ -x /opt/infinity/bin/infinity_emb ] && ! pgrep -f 'infinity-supervisor' >/dev/null; then
  nohup bash -c "exec -a infinity-supervisor bash -c 'while true; do
    if ! curl -sm3 -o /dev/null localhost:7998/health; then
      echo \"\$(date -Is) infinity down — subindo\" >> /root/unified-infinity.log
      /opt/infinity/bin/infinity_emb v2 \
        --model-id BAAI/bge-reranker-v2-m3 \
        --served-model-name bge-reranker-v2-m3 \
        --model-id BAAI/bge-m3 \
        --served-model-name bge-m3 \
        --engine torch --device cuda --dtype float16 \
        --url-prefix /v1 --host 0.0.0.0 --port 7998 \
        >> /root/unified-infinity.log 2>&1
      echo \"\$(date -Is) infinity saiu rc=\$?\" >> /root/unified-infinity.log
    fi
    sleep 30
  done'" > /dev/null 2>&1 &
fi

echo "onstart concluido $(date)"
