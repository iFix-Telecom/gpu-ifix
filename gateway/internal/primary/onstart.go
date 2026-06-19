package primary

// primaryLlamaArgsDefault is the canonical llama-server CLI invocation for
// the primary pod's [program:llama] supervisord child (06.6-WAVE0-GATES.md
// Decision 2 + Decision 3 B1 GGUF-embedded Jinja LOCKED). It mirrors the
// command line baked into pod/primary/supervisord.conf at image build
// time — kept here as the source of truth so lifecycle_test.go can assert
// the two stay in sync.
//
// Operator override via PRIMARY_LLAMA_ARGS env CSV replaces this slice
// wholesale (Cfg.PrimaryLlamaArgs). NOTE: this override is preserved on
// the signature of buildPrimaryOnstart for future B2 fallback wiring, but
// in Strategy alpha LOCKED the supervisord.conf inside the custom image
// is the actual runtime command — the override is not yet plumbed into
// supervisord (deferred per CONTEXT.md Deferred Ideas to a future B2
// LLAMA_EXTRA_ARGS-via-env path that regenerates supervisord.conf inside
// the pod).
//
// Per Wave 0 Decision 3 B1 embedded LOCKED, the chat-template flag is
// intentionally absent: Qwen3.6 GGUF carries the chat_template, and the
// jinja flag alone extracts the PEG-native parser per
// 06.6-SPIKE-qwen3.6-jinja.md Round 3.
var primaryLlamaArgsDefault = []string{
	"--host", "0.0.0.0",
	"--port", "8000",
	"-m", "/weights/qwen/model.gguf",
	"-ngl", "99",
	"-np", "2",
	"--ctx-size", "16384",
	"--jinja",
}

// primaryOnstartHead is the inline bash bootstrap script (raw-string Go
// const per Pitfall #9 — ZERO format-string shell quoting; see verify
// grep gates on this file). Executes inside the custom
// converseai-primary-pod container (pod/primary/Dockerfile) before the
// final `exec /usr/bin/supervisord -n -c
// /etc/supervisor/conf.d/services.conf` line, which is appended at
// runtime by buildPrimaryOnstart.
//
// # Wave 0 LOCKED invariants
//
//   - DinD path is fully rejected. The 4 services are supervisord child
//     processes spawned from supervisord.conf (baked into the image at
//     build time). DinD was empirically refused in
//     06.6-SPIKE-dind-privileged.md (overlayfs in nested namespace fails).
//   - PID 1 = supervisord. The trailing `exec /usr/bin/supervisord -n ...`
//     line appended by buildPrimaryOnstart replaces the bash interpreter
//     with the supervisor so Vast.ai's crash detection observes the real
//     PID 1 state (Pitfall #2 invariant).
//
// # Reviews #7 shell hardening (06.6-REVIEWS.md)
//
//   - bash strict mode `set -euo` (the `x` xtrace variant is intentionally
//     omitted — xtrace would leak MinIO credentials into the pod log
//     via `mc alias set`).
//   - All `$VAR` env expansions are quoted ("$VAR", not $VAR).
//   - 10 required env vars (MinIO 4 + weights 3*2) are guarded with
//     `: "${VAR:?required}"` so missing env triggers immediate non-zero
//     exit BEFORE any download, MinIO alias setup, or supervisord exec.
//   - `set -e` propagates aria2c / sha256sum / tar failures so a single
//     bad weight download aborts the pod (T-06.6-02 mitigation). The 3
//     parallel weight downloads are `wait`ed PER-PID (CR-02 6.6.Y review):
//     a multi-id `wait` returns only the LAST id's status, so a failed
//     Qwen/bge-m3 download would otherwise be swallowed — each PID is
//     waited individually and any non-zero aborts before supervisord exec.
//
// # Behaviour
//
//  1. precondition env-var guards (10x `:?required`)
//  2. optional sshd install when POD_DEBUG_SSH_PUBLIC_KEY is non-empty
//  3. install mc client via curl if missing (aria2 is baked into the
//     custom image via pod/primary/Dockerfile apt install)
//  4. configure mc alias `ifix` against MINIO_ENDPOINT
//  5. spawn 3 parallel weight downloads (Qwen GGUF + Whisper tarball +
//     BGE-M3 tarball) with aria2c multi-stream + sha256sum -c verify
//  6. optional Jinja fetch when PRIMARY_QWEN_JINJA_KEY non-empty (B2
//     fallback; default empty per Decision 3 B1 embedded)
//  7. `wait` on the 3 download PIDs
//  8. extract whisper + bge-m3 tarballs into /weights/{whisper,bge-m3}/
//  9. (appended by buildPrimaryOnstart) exec /usr/bin/supervisord -n
//
// Length budget: ~1.4KB head + ~80 chars exec line = ~1.5KB total. Vast
// onstart hard limit is much higher (16KB+), so length is not a constraint
// here — but TestPrimaryOnstartLengthBelowLimit asserts < 14KB as a
// regression net.
const primaryOnstartHead = `#!/bin/bash
# Onstart trace: every step writes a marker to /tmp/onstart.log so SSH
# inspection can see how far the script got even if it later exits.
exec > >(tee -a /tmp/onstart.log) 2>&1
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: enter (PID $$)"

# Setup sshd FIRST (before env checks) so the operator can SSH in during
# the boot window even if a later env check fails.
if [ -n "${POD_DEBUG_SSH_PUBLIC_KEY:-}" ]; then
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: setting up sshd"
  mkdir -p /root/.ssh /run/sshd
  printf '%s\n' "$POD_DEBUG_SSH_PUBLIC_KEY" > /root/.ssh/authorized_keys
  chmod 700 /root/.ssh
  chmod 600 /root/.ssh/authorized_keys
  if ! command -v sshd >/dev/null 2>&1; then
    apt-get update -qq && apt-get install -y -qq openssh-server >/dev/null
  fi
  sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config 2>/dev/null || true
  /usr/sbin/sshd
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: sshd started"
fi

set -euo pipefail

echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: checking env vars"
: "${MINIO_ENDPOINT:?required}"
: "${MINIO_BUCKET:?required}"
: "${MINIO_ACCESS_KEY:?required}"
: "${MINIO_SECRET_KEY:?required}"
: "${PRIMARY_QWEN_WEIGHTS_KEY:?required}"
: "${PRIMARY_QWEN_WEIGHTS_SHA256:?required}"
# Phase 11.2 D-B5'/6.6.Y-06 D-03: PRIMARY_WHISPER_WEIGHTS_* restored (tier-0 Whisper STT back on-pod).
: "${PRIMARY_WHISPER_WEIGHTS_KEY:?required}"
: "${PRIMARY_WHISPER_WEIGHTS_SHA256:?required}"
: "${PRIMARY_BGEM3_WEIGHTS_KEY:?required}"
: "${PRIMARY_BGEM3_WEIGHTS_SHA256:?required}"
# Chatterbox TTS model — pre-provisioned HF-cache snapshot (replaces the
# runtime huggingface.co fetch that crash-looped on hosts without an HF route).
: "${PRIMARY_CHATTERBOX_WEIGHTS_KEY:?required}"
: "${PRIMARY_CHATTERBOX_WEIGHTS_SHA256:?required}"
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: env vars OK"

echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: installing mc if missing"
# mc is baked into the pod image (pod/primary/Dockerfile) so this branch is a
# fallback only. The runtime fetch from dl.min.io previously had no timeout —
# when dl.min.io throttled to ~45 KB/s (2026-06-13) the pod hung here forever,
# supervisord never exec'd, and every cold-start died on health_timeout. The
# --max-time/--retry bound makes a slow mirror fail fast and loud instead.
if ! command -v mc >/dev/null 2>&1; then
  curl -fsSL --connect-timeout 15 --max-time 120 --retry 3 --retry-delay 5 \
    https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc
  chmod +x /usr/local/bin/mc
fi
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: mc ready"

mc alias set ifix "$MINIO_ENDPOINT" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: minio alias set"

download_with_verify() {
  local key="$1" target="$2" sha="$3"
  local url
  url=$(mc share download --expire=2h "ifix/$MINIO_BUCKET/$key" | awk -F': ' '/^Share/{print $2}')
  aria2c --max-tries=50 --retry-wait=15 --timeout=60 --connect-timeout=30 \
         --max-connection-per-server=16 --split=16 \
         --min-split-size=1M --continue=true --dir="$(dirname "$target")" --out="$(basename "$target")" "$url"
  echo "$sha  $target" | sha256sum -c -
}

mkdir -p /weights/qwen /weights/bge-m3 /weights/whisper /app/templates /opt/chatterbox-data/models/hub

echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: spawning 4 parallel downloads"
download_with_verify "$PRIMARY_QWEN_WEIGHTS_KEY" "/weights/qwen/model.gguf" "$PRIMARY_QWEN_WEIGHTS_SHA256" &
QWEN_PID=$!
download_with_verify "$PRIMARY_BGEM3_WEIGHTS_KEY" "/weights/bge-m3/model.tar.gz" "$PRIMARY_BGEM3_WEIGHTS_SHA256" &
BGE_PID=$!
download_with_verify "$PRIMARY_WHISPER_WEIGHTS_KEY" "/weights/whisper/model.tar.gz" "$PRIMARY_WHISPER_WEIGHTS_SHA256" &
WHISPER_PID=$!
# Chatterbox TTS HF-cache snapshot (models--ResembleAI--chatterbox/...).
download_with_verify "$PRIMARY_CHATTERBOX_WEIGHTS_KEY" "/opt/chatterbox-data/models/cache.tar.gz" "$PRIMARY_CHATTERBOX_WEIGHTS_SHA256" &
CHATTERBOX_PID=$!

if [ -n "${PRIMARY_QWEN_JINJA_KEY:-}" ]; then
  : "${PRIMARY_QWEN_JINJA_SHA256:?required when PRIMARY_QWEN_JINJA_KEY is set}"
  download_with_verify "$PRIMARY_QWEN_JINJA_KEY" "/app/templates/qwen3.6.jinja" "$PRIMARY_QWEN_JINJA_SHA256"
fi

echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: waiting for 4 downloads"
# CR-02 (6.6.Y review): bash 'wait' with multiple IDs returns ONLY the last
# id's exit status, so a failed Qwen/bge-m3 download or SHA-256 mismatch was
# silently swallowed and supervisord exec'd with a missing/corrupt weight.
# Wait each PID individually and fail the whole onstart (no supervisord exec)
# if ANY download/verify failed — restores the T-06.6-02 integrity fail-fast
# for all 4 weights (chatterbox TTS added 2026-06-13).
FAIL=
wait "$QWEN_PID" || FAIL=1
wait "$BGE_PID" || FAIL=1
wait "$WHISPER_PID" || FAIL=1
wait "$CHATTERBOX_PID" || FAIL=1
if [ -n "$FAIL" ]; then
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: FATAL — weight download/verify failed; aborting before supervisord"
  exit 1
fi
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: 4 downloads complete; extracting tarballs"

tar -xzf /weights/bge-m3/model.tar.gz -C /weights/bge-m3
tar -xzf /weights/whisper/model.tar.gz -C /weights/whisper
# Chatterbox HF-cache: from_pretrained() calls snapshot_download() WITHOUT an
# explicit cache_dir, so huggingface_hub resolves the cache at $HF_HOME/hub
# (HF_HOME is /opt/chatterbox-data/models per supervisord.conf). Extract the
# models--ResembleAI--chatterbox/ tree into that hub/ subdir — extracting it
# one level up (…/models/) makes offline snapshot_download miss it and the
# chatterbox child crash-loops with LocalEntryNotFoundError. With
# HF_HUB_OFFLINE=1 from_pretrained() then reads this cache and never contacts
# huggingface.co.
tar -xzf /opt/chatterbox-data/models/cache.tar.gz -C /opt/chatterbox-data/models/hub
rm -f /opt/chatterbox-data/models/cache.tar.gz
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: extraction done; exec supervisord"

# --- SEED-019 part 2: VRAM-adaptive whisper device (Block A) -----------------
# The pod is the source of truth for whether its whisper runs on GPU. Sum total
# VRAM across all visible GPUs via nvidia-smi (present on every CUDA Vast host;
# the image ships no calculator binary — Dockerfile uses awk only). On a >=30000 MiB
# shape (2x3090=48, 5090=32) export cuda + pin WHISPER__DEVICE_INDEX.
# SEED-019 part 4 (2026-06-19): qwen is now PINNED to GPU0 (supervisord:
# --split-mode none --main-gpu 0), so on MULTI-GPU shapes the LAST card is
# Qwen-free — dedicate it to whisper (WHISPER__DEVICE_INDEX = NUM_GPUS-1) → no
# shared-card CUDA OOM. The old "max-free at onstart" pick was unreliable: at
# onstart (before llama loads) every card reads ~empty, tying to GPU0, the very
# card qwen then loads onto → OOM (UAT B "instance terminal", 2026-06-19). On a
# single-GPU >=30GB shape (5090=32) there is no second card, so whisper shares
# GPU0 with qwen (32GB has the headroom). Below threshold (1x3090=24) export cpu
# so the gateway fail-safes STT to tier-1 gemini instead of slow CPU whisper.
# These exports happen BEFORE exec supervisord so [program:speaches] inherits
# them (supervisord.conf no longer pins WHISPER__INFERENCE_DEVICE — env
# inheritance Option A, fail-open to speaches default auto/0 if ever unset).
WHISPER_GPU_THRESHOLD_MIB=30000
TOTAL_VRAM_MIB=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END{print s}')
NUM_GPUS=$(nvidia-smi --query-gpu=index --format=csv,noheader 2>/dev/null | awk 'END{print NR}')
if [ -z "${TOTAL_VRAM_MIB:-}" ]; then
  # nvidia-smi absent or empty → fail-safe to cpu (gateway routes STT to gemini).
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: nvidia-smi unavailable; whisper device=cpu (fail-safe)"
  export WHISPER__INFERENCE_DEVICE=cpu
  export WHISPER_DEVICE=cpu
elif [ "$TOTAL_VRAM_MIB" -ge "$WHISPER_GPU_THRESHOLD_MIB" ]; then
  # qwen pinned to GPU0; dedicate the LAST card to whisper on multi-GPU shapes
  # (Qwen-free), else card 0 on a single-GPU >=30GB shape.
  if [ "${NUM_GPUS:-1}" -ge 2 ]; then
    WHISPER_IDX=$(( NUM_GPUS - 1 ))
  else
    WHISPER_IDX=0
  fi
  export WHISPER__INFERENCE_DEVICE=cuda
  export WHISPER__DEVICE_INDEX="${WHISPER_IDX}"
  export WHISPER_DEVICE=cuda
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: total VRAM ${TOTAL_VRAM_MIB} MiB >= ${WHISPER_GPU_THRESHOLD_MIB} across ${NUM_GPUS} GPU(s); qwen pinned GPU0; whisper device=cuda index=${WHISPER__DEVICE_INDEX}"
else
  export WHISPER__INFERENCE_DEVICE=cpu
  export WHISPER_DEVICE=cpu
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: total VRAM ${TOTAL_VRAM_MIB} MiB < ${WHISPER_GPU_THRESHOLD_MIB}; whisper device=cpu"
fi

# --- SEED-019 part 2: :9100 device-report responder (Block B) ----------------
# Write the device JSON and stand a minimal static responder on 0.0.0.0:9100
# serving GET /whisper_device. The gateway (Plan 14-01) reads this at pod-Ready
# and whitelists exactly {cuda,cpu}; anything else / unreachable → no STT
# override → gemini-stt (fail-safe). python3 is baked into the image
# (Dockerfile). The launch is wrapped in '|| true' so a transient :9100 bind
# failure never aborts the pod under set -e — the gateway already fail-safes on
# an unreachable :9100 (threat T-14-06).
printf '{"whisper_device":"%s"}' "$WHISPER_DEVICE" > /weights/whisper/whisper_device.json
{ nohup python3 -c '
import json, http.server, socketserver
PATH = "/weights/whisper/whisper_device.json"
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/whisper_device":
            try:
                with open(PATH, "rb") as f:
                    body = f.read()
            except OSError:
                body = b"{\"whisper_device\":\"cpu\"}"
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_response(404)
            self.end_headers()
    def log_message(self, *a):
        pass
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("0.0.0.0", 9100), H) as s:
    s.serve_forever()
' >/tmp/devreport.log 2>&1 & } || true
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: device-report responder on :9100 (whisper_device=${WHISPER_DEVICE})"

`

// buildPrimaryOnstart appends the trailing `exec /usr/bin/supervisord -n
// -c /etc/supervisor/conf.d/services.conf` line to primaryOnstartHead.
// supervisord becomes PID 1 in the container (Pitfall #2 invariant +
// 06.6-WAVE0-GATES.md Decision 2 LOCKED), spawning the 4 child processes
// declared in pod/primary/supervisord.conf (baked into the custom image
// at build time).
//
// llamaArgs and jinjaPath are CURRENTLY UNUSED in Strategy alpha LOCKED —
// the supervisord.conf inside the custom image is the source of truth
// for the llama-server command at runtime, and B1 GGUF-embedded Jinja
// needs no template path. The params are preserved on the signature so
// lifecycle_test.go can assert primaryLlamaArgsDefault stays aligned
// with supervisord.conf and so a future B2 fallback (runtime
// LLAMA_EXTRA_ARGS env override that regenerates supervisord.conf
// inside the pod) can wire them in without a signature break.
//
// No format-string templating is used here — the raw-string + simple
// concatenation preserves the Pitfall #9 invariant (shell quoting bugs
// at template-expansion time are notoriously hard to spot in code review).
func buildPrimaryOnstart(llamaArgs []string, jinjaPath string) string {
	_ = llamaArgs
	_ = jinjaPath
	return primaryOnstartHead + "exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/services.conf\n"
}
