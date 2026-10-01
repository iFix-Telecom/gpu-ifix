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

# ---- cuda compat guard (ANTES de qualquer consumidor de libcuda) ----
# Evidencia 2026-09-30 (ClickUp 86akr57nj): a imagem base CUDA 12.6 traz
# /etc/ld.so.conf.d/00-compat-*.conf -> libcuda.so.1 resolve pra
# /usr/local/cuda-12.6/compat (libcuda 560) em hosts GeForce com driver 535
# -> torch.cuda.is_available() False (erro 804, forward-compat nao suportado
# em GeForce) -> Infinity morre ("infinity health timeout 30min"). Mover o conf
# + ldconfig foi provado como fix. So remove quando driver do host < major da
# compat. Nunca falha o onstart (sem set -e, todo comando guardado). Sem probe
# de torch aqui: no 1o boot os venvs ainda nao existem neste ponto.
cuda_compat_guard() {
  local host_major conf dir lib ver compat_major changed=0
  if ! command -v nvidia-smi >/dev/null 2>&1; then
    echo "cuda-compat: nvidia-smi unavailable, skipping"; return 0
  fi
  host_major=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -1 | cut -d. -f1 | tr -dc 0-9)
  if [ -z "$host_major" ]; then
    echo "cuda-compat: host driver version unreadable, skipping"; return 0
  fi
  for conf in /etc/ld.so.conf.d/*compat*.conf; do
    [ -f "$conf" ] || continue
    compat_major=""
    while IFS= read -r dir; do
      dir=$(echo "$dir" | sed 's/#.*//' | tr -d '[:space:]')
      [ -n "$dir" ] && [ -d "$dir" ] || continue
      for lib in "$dir"/libcuda.so.*; do
        [ -e "$lib" ] || continue
        ver=${lib##*/libcuda.so.}
        case "$ver" in [0-9]*.*) compat_major=${ver%%.*}; break ;; esac
      done
      [ -n "$compat_major" ] && break
    done < "$conf"
    if [ -z "$compat_major" ]; then
      echo "cuda-compat: keeping $conf (no versioned libcuda found)"
      continue
    fi
    if [ "$host_major" -lt "$compat_major" ] 2>/dev/null; then
      echo "cuda-compat: host driver $host_major < compat $compat_major -> removing $conf"
      mkdir -p /root/ldbak && mv -f "$conf" /root/ldbak/ && changed=1
    else
      echo "cuda-compat: keeping $conf (host driver $host_major >= compat $compat_major)"
    fi
  done
  if [ "$changed" = 1 ]; then
    ldconfig || echo "cuda-compat: ldconfig failed (rc=$?)"
  fi
  return 0
}
cuda_compat_guard || true

# ---- disk-guard (escrito pelo prologo do provisioner) ----
if [ -f /root/disk-guard.sh ]; then
  chmod +x /root/disk-guard.sh
  pgrep -f 'bash /root/disk-guard.sh' >/dev/null || \
    setsid /root/disk-guard.sh </dev/null >/dev/null 2>&1 &
fi

# ---- speaches (STT/TTS) — loop supervisor: processo morreu em prod
# (2026-09-07, provavel OOM em maquina de 7GB RAM) e nada religava ----
export WHISPER_MODEL="${WHISPER_MODEL:-Systran/faster-whisper-large-v3}"
# int8_float16: pesos do large-v3 ~3G→~1,5G na VRAM. Com float16 o batched
# decode (batch 8) de audio longo estourava os ~2,5G livres da 3060 (12G
# dividida com Infinity + XTTS): CUDA OOM → 500 → gateway cascateava p/ gemini
# (46% dos minutos de STT externo). Medido 2026-10-01 audio 13:14: fp16 = 500
# pico 11867MiB; int8 = 200 pico 10843MiB, texto identico (ClickUp 86akreh6u).
export WHISPER__COMPUTE_TYPE="${WHISPER__COMPUTE_TYPE:-int8_float16}"
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

# ---- XTTS-v2 (TTS pt-BR :8021, decisao Pedro 2026-09-07) ----
# venv PROPRIO: coqui-tts exige transformers >=4.54 <5, conflita com o pin
# git do Infinity. Server /root/xtts-server.py vem do prologo (build_onstart).
if [ ! -f /opt/xtts/.ok ]; then
  python3 -m venv /opt/xtts
  /opt/xtts/bin/pip install --no-cache-dir --retries 10 --timeout 60 \
    "coqui-tts==0.27.5" "transformers==4.57.1" \
    "torch==2.5.1" "torchaudio==2.5.1" \
    --extra-index-url https://download.pytorch.org/whl/cu121 \
    >> /root/unified-pip-xtts.log 2>&1 && touch /opt/xtts/.ok
fi
if [ -f /opt/xtts/.ok ] && [ -f /root/xtts-server.py ] && \
   ! pgrep -f 'xtts-superviso[r]' >/dev/null; then
  nohup bash -c "exec -a xtts-supervisor bash -c 'while true; do
    if ! curl -sm3 -o /dev/null localhost:8021/health; then
      echo \"\$(date -Is) xtts down — subindo\" >> /root/unified-xtts.log
      COQUI_TOS_AGREED=1 /opt/xtts/bin/python /root/xtts-server.py \
        >> /root/unified-xtts.log 2>&1
      echo \"\$(date -Is) xtts saiu rc=\$?\" >> /root/unified-xtts.log
    fi
    sleep 30
  done'" > /dev/null 2>&1 &
fi

echo "onstart concluido $(date)"
