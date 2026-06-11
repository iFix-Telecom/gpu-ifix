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
//     bad weight download aborts the pod (T-06.6-02 mitigation).
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
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: env vars OK"

echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: installing mc if missing"
if ! command -v mc >/dev/null 2>&1; then
  curl -sSL https://dl.min.io/client/mc/release/linux-amd64/mc -o /usr/local/bin/mc
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

mkdir -p /weights/qwen /weights/bge-m3 /weights/whisper /app/templates

echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: spawning 3 parallel downloads"
download_with_verify "$PRIMARY_QWEN_WEIGHTS_KEY" "/weights/qwen/model.gguf" "$PRIMARY_QWEN_WEIGHTS_SHA256" &
QWEN_PID=$!
download_with_verify "$PRIMARY_BGEM3_WEIGHTS_KEY" "/weights/bge-m3/model.tar.gz" "$PRIMARY_BGEM3_WEIGHTS_SHA256" &
BGE_PID=$!
download_with_verify "$PRIMARY_WHISPER_WEIGHTS_KEY" "/weights/whisper/model.tar.gz" "$PRIMARY_WHISPER_WEIGHTS_SHA256" &
WHISPER_PID=$!

if [ -n "${PRIMARY_QWEN_JINJA_KEY:-}" ]; then
  : "${PRIMARY_QWEN_JINJA_SHA256:?required when PRIMARY_QWEN_JINJA_KEY is set}"
  download_with_verify "$PRIMARY_QWEN_JINJA_KEY" "/app/templates/qwen3.6.jinja" "$PRIMARY_QWEN_JINJA_SHA256"
fi

echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: waiting for 3 downloads"
wait "$QWEN_PID" "$BGE_PID" "$WHISPER_PID"
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: 3 downloads complete; extracting tarballs"

tar -xzf /weights/bge-m3/model.tar.gz -C /weights/bge-m3
tar -xzf /weights/whisper/model.tar.gz -C /weights/whisper
echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] onstart: extraction done; exec supervisord"

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
