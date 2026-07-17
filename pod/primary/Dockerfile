# pod/primary/Dockerfile — Phase 11.2 re-add Whisper local (tier-0 STT restored).
#
# Phase 11.2 (2026-06-07) RESTORES the [program:speaches] venv stage removed
# by Phase 11.1 (commit 8acb991). Tier-0 local STT is back (Speaches +
# faster-whisper-large-v3) alongside the tier-1 cascade (gemini-stt →
# groq-whisper → openai-whisper). The Phase 11.1 embed venv removal (D-A2)
# stays — embed remains off-pod per Phase 06.7 D-03 (Infinity venv NOT
# restored). Pre-shrink commentary on the prior venv layout and pip pinning
# is preserved in git history (commit 8acb991^).
#
# DinD REJECTED per 06.6-SPIKE-dind-privileged.md — overlayfs in nested
# namespace fails on Vast.ai 4090 hosts (Linux kernel limit, not bypassable
# even with --privileged:1 + iptables=false workaround). supervisord as
# PID 1 replaces DinD (06.6-WAVE0-GATES.md Decision 2 LOCKED).
#
# Hybrid build:
#   - llama.cpp + dcgm-exporter SHA-pinned upstream (binaries portable,
#     multi-stage COPY safe).
#   - Speaches installed via pip into a dedicated venv at /opt/speaches-venv
#     (faster-whisper==1.1.1 pinned + silero v5 ONNX vendored from upstream
#     speaches image via multi-stage COPY). D-B1 LOCKED image tag for the
#     silero-asset source: ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3.
#   - Phase 21: Chatterbox TTS venv REMOVED (zero prod use; freed ~5GB VRAM so
#     whisper runs on-GPU in a 24GB shape). SHA-pin invariant is preserved for
#     the non-substitutable component (llama.cpp b9191 — required for Qwen3.6
#     SSM tensor support per Wave 0 spike Round 3).
#
# All SHA-pinned FROM digests are LITERAL per WAVE0-GATES.md Decision 1
# captured 2026-05-17. Dockerfile FROM does not support env substitution,
# so the digests live inline. Image bumps require a follow-up
# WAVE0-GATES.md amendment + operator sign-off (see image bump policy
# documented in .github/workflows/build-primary-pod.yml).
#
# Build context: repo root.
# Build cmd: docker buildx build -f pod/primary/Dockerfile -t \
#   ghcr.io/ifixtelecom/converseai-primary-pod:vX .

# Stage 1: DCGM exporter (GPU metrics) — copy dcgm-exporter binary into /usr/bin/
# (statically linked, portable across distros — multi-stage COPY safe).
FROM nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless@sha256:60d3b00ac80b4ae77f94dae2f943685605585ad9e92fdccda3154d009ae317cc AS dcgm-stage

# Stage 1b: Upstream speaches image — source of silero_encoder_v5.onnx +
# silero_decoder_v5.onnx ONNX files. Speaches 0.9.0rc3 code (executors/
# silero_vad_v5.py) loads these from faster_whisper's assets dir, but
# neither faster-whisper 1.0.x nor 1.1.x ship the v5 split assets — upstream
# speaches Dockerfile vendors them manually. We multi-stage COPY rather
# than fetch from a public URL because the Silero VAD repo removed the v5
# files from src/silero_vad/data/ (only v4 + v6 remain). UAT 15 (2026-05-19)
# /v1/audio/transcriptions crashed with `onnxruntime.NoSuchFile` until
# these were vendored. D-B1 LOCKED — do NOT bump tag without phase amendment.
FROM ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3 AS speaches-assets

# Stage 2: Final image — llama.cpp:server-cuda-b9191 carries Qwen3.6 SSM
# support (Wave 0 UPGRADED from b9128 per 06.6-SPIKE-qwen3.6-jinja.md Round 3
# tensor-blk-64 SSM pass). The 3 services (llm/stt/dcgm) co-locate here under supervisord (Phase 21: tts removed).
FROM ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3 AS final

# Install supervisord (PID 1) + aria2 (multi-stream weight download from
# MinIO — single-stream mc cp empirically failed Wave 0 spike Round 1
# with EOF on 16GB GGUF) + curl/ca-certificates + Python 3.11 (for
# Speaches/Chatterbox TTS venvs installed below) + build-essential (some
# PyTorch wheels need it) + openssh-server (debug SSH during cold-start —
# baked in so onstart skips the ~3min apt-get install on every boot).
RUN apt-get update && apt-get install -y --no-install-recommends \
        supervisor aria2 ca-certificates curl openssh-server \
        python3 python3-venv python3-pip git build-essential \
    && rm -rf /var/lib/apt/lists/*

# Bake the MinIO client (mc) into the image. Previously onstart.sh fetched it
# from https://dl.min.io/ at boot, but that host is throttled (~45 KB/s for the
# ~28MB binary as of 2026-06-13) and the onstart curl had no timeout/fallback —
# so the pod hung in "installing mc" forever, supervisord never exec'd, llama
# never came up, and the gateway killed the pod on health_timeout (every
# cold-start failed). Baking mc removes the runtime download + single point of
# failure. Pinned release for reproducibility; verified executable at build.
ARG MC_RELEASE=RELEASE.2025-08-13T08-35-41Z
RUN curl -fsSL "https://dl.min.io/client/mc/release/linux-amd64/mc.${MC_RELEASE}" \
        -o /usr/local/bin/mc \
    && chmod +x /usr/local/bin/mc \
    && /usr/local/bin/mc --version

# Speaches STT — Phase 11.2 RESTORED (re-add tier-0 local STT).
#
# Pin notes (UAT 2026-05-18 attempt 11 root causes — preserved verbatim):
#  - onnx-asr<0.10 — speaches 0.9.0rc3 imports `NemoConformerTdt` from
#    `onnx_asr.models`, removed in onnx-asr 0.10+ (attempt 10).
#  - gradio<6.0 — speaches 0.9.0rc3 builds its UI with Gradio 5.x API
#    (`ChatInterface(type=...)`); Gradio 6.0 removed that keyword and the
#    factory crashed with
#    `ChatInterface.__init__() got an unexpected keyword argument 'type'`
#    on attempt 12. Pinning gradio<6.0 keeps the speaches UI factory happy.
#  - faster-whisper==1.1.1 — UAT 17 (2026-05-19) caught TypeError in
#    `faster_whisper/transcribe.py:429`: `int() argument must be a string,
#    a bytes-like object or a real number, not 'list'`. Trace: speaches
#    `silero_vad_v5.merge_segments()` returns dicts with keys
#    `{start, end, segments}` where `segments` is a list of (start,end)
#    tuples. faster-whisper 1.2.x added strict clip_timestamps validation
#    that iterates dict.items() and casts each value to int(v * rate),
#    crashing on the list. Upstream speaches Docker image ships with
#    faster-whisper 1.1.1 which tolerates the extra key. Match that
#    version exactly. Earlier UAT 15 tried `<1.1` but speaches needs
#    `BatchedInferencePipeline` (added in 1.1.0), so we're sandwiched —
#    pin to the exact 1.1.1. silero v5 assets are vendored independently
#    via multi-stage COPY from speaches-assets stage below.
RUN python3 -m venv /opt/speaches-venv \
    && /opt/speaches-venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/speaches-venv/bin/pip install --no-cache-dir \
         "git+https://github.com/speaches-ai/speaches@v0.9.0-rc.3" \
    && /opt/speaches-venv/bin/pip install --no-cache-dir \
         "onnx-asr<0.10.0" "gradio<6.0" "faster-whisper==1.1.1"

# Pre-create the realtime-console stub directory speaches 0.9.0rc3 mounts
# at /v1/realtime via StaticFiles(directory="realtime-console/dist",
# html=True). The path is RELATIVE to the process working directory, so
# supervisord [program:speaches] is launched with `directory=/opt/speaches-data`
# (see supervisord.conf). Without this dir + an index.html the speaches
# app errors at boot with `RuntimeError: Directory 'realtime-console/dist'
# does not exist` (UAT 2026-05-18 attempt 11 speaches crash-loop root cause).
# The stub satisfies the StaticFiles invariant; we do NOT expose the
# realtime console — gateway routes only /v1/audio/transcriptions to STT.
RUN mkdir -p /opt/speaches-data/realtime-console/dist \
    && echo '<!DOCTYPE html><html><body>realtime-console stub (not served)</body></html>' \
       > /opt/speaches-data/realtime-console/dist/index.html \
    && echo '{}' > /opt/speaches-data/model_aliases.json

# Vendor silero v5 encoder + decoder ONNX assets from upstream speaches
# image. speaches/executors/silero_vad_v5.py loads these from
# `faster_whisper.utils.get_assets_path()` (which resolves to
# /opt/speaches-venv/lib/python3.12/site-packages/faster_whisper/assets/).
# faster-whisper 1.0.x ships silero_vad.onnx and 1.1.x ships
# silero_vad_v6.onnx — NEITHER ships the v5 split files speaches needs at
# transcription time. UAT 15 (2026-05-19) confirmed this is the root
# cause of `Internal Server Error` on POST /v1/audio/transcriptions.
COPY --from=speaches-assets \
    /home/ubuntu/speaches/.venv/lib/python3.12/site-packages/faster_whisper/assets/silero_encoder_v5.onnx \
    /opt/speaches-venv/lib/python3.12/site-packages/faster_whisper/assets/silero_encoder_v5.onnx
COPY --from=speaches-assets \
    /home/ubuntu/speaches/.venv/lib/python3.12/site-packages/faster_whisper/assets/silero_decoder_v5.onnx \
    /opt/speaches-venv/lib/python3.12/site-packages/faster_whisper/assets/silero_decoder_v5.onnx

# Phase 21: Chatterbox Multilingual TTS venv REMOVED. Zero prod use + ~5GB VRAM
# forced whisper to CPU on a 24GB shape. Dropping it (~2.8GB image + the whole
# torch 2.7.1 cu128 trio) frees the GPU so speaches/whisper runs on-GPU in
# 1x3090. STT tier-0 local instead of the tier-1 gemini/openai cascade.

# Copy dcgm-exporter binary AND the libdcgm.so.4 family + metric configs
# from stage 1. The distroless dcgm-exporter image bundles libdcgm under
# /usr/lib/x86_64-linux-gnu/. Copying only the binary leaves it unable to
# resolve libdcgm.so.4 at runtime — fails with `the libdcgm.so.4 library
# was not found. Install Data Center GPU Manager (DCGM).` and supervisord
# crash-loops it (UAT 2026-05-18 attempt 11 dcgm root cause).
COPY --from=dcgm-stage /usr/bin/dcgm-exporter /usr/bin/dcgm-exporter
COPY --from=dcgm-stage /usr/lib/x86_64-linux-gnu/libdcgm.so.4 /usr/lib/x86_64-linux-gnu/
COPY --from=dcgm-stage /usr/lib/x86_64-linux-gnu/libdcgm.so.4.5.3 /usr/lib/x86_64-linux-gnu/
COPY --from=dcgm-stage /usr/lib/x86_64-linux-gnu/libdcgmmodule*.so.4 /usr/lib/x86_64-linux-gnu/
COPY --from=dcgm-stage /usr/lib/x86_64-linux-gnu/libdcgmmodule*.so.4.5.3 /usr/lib/x86_64-linux-gnu/
COPY --from=dcgm-stage /etc/dcgm-exporter /etc/dcgm-exporter
RUN ldconfig

# Bake supervisord config into the image. Onstart bash does NOT regenerate
# this file at runtime (Strategy α LOCKED — runtime override deferred to
# CONTEXT.md Deferred Ideas as B2 fallback).
COPY pod/primary/supervisord.conf /etc/supervisor/conf.d/services.conf

# Phase 21: chatterbox_server.py shim COPY removed (TTS program dropped).

# Pre-create runtime directories referenced by supervisord.conf + onstart.
# /weights/qwen is the mount-target for the aria2c-downloaded GGUF artifact;
# /weights/whisper is the extract target for the speaches HF-cache-shaped
# tarball (Phase 06.8 fix-iii — HF_HUB_CACHE in supervisord.conf MUST equal
# this path; see Pitfall 4 in 11.2-RESEARCH.md).
# /app/templates is the optional B2 Jinja fallback mount target (default
# empty per WAVE0-GATES Decision 3 B1 embedded). Phase 11.1: /weights/bge-m3
# pre-create removed — BGE-M3 embed is off-pod (Phase 06.7 D-03) and the
# pod's onstart `mkdir -p` recreates the directory at runtime if a future
# config re-enables on-pod BGE-M3 downloads.
RUN mkdir -p /var/log /weights/qwen /weights/whisper /app/templates

# Disable inherited HEALTHCHECK from llama.cpp base (which curls :8080/health,
# but our supervisord-managed services bind to :8000/:8001/:9100/:9400 —
# never :8080). Continuous healthcheck failure caused Vast.ai's worker agent to reap
# the container as exited within 30-60s of start across all 8 UAT attempts on
# 2026-05-18 (gsd-debug session primary-uat-custom-image-vast-runtype-args).
# The gateway reconciler does the real 4-endpoint health-poll via Vast-exposed
# host ports (gateway/internal/primary/reconciler.go evaluateProvisioning).
HEALTHCHECK NONE

LABEL org.opencontainers.image.source="https://github.com/IfixTelecom/gpu-ifix"
LABEL org.opencontainers.image.description="Phase 21 primary pod — supervisord + llama.cpp b9191 + speaches (whisper on GPU) + dcgm. STT tier-0 local; tier-1 cascade gemini->groq->openai-whisper. embed off-pod (06.7 D-03); TTS/chatterbox removed (Phase 21)."

# Override inherited ENTRYPOINT from llama.cpp base (which is `/app/llama-server`)
# with `/bin/bash` so the supervisord-driven primary pod boots via the bash
# bootstrap script the gateway sends as Args=["-c", "<onstart>"]. Without
# this override the container exits immediately because llama-server cannot
# parse `/bin/bash -c <script>` as flags (UAT 2026-05-18 attempts 3-7).
# An empty `ENTRYPOINT []` does NOT work on Vast.ai's runtype=args because
# Vast appends Args directly without injecting Onstart as a separate executable
# (attempt 6 also failed with empty entrypoint).
ENTRYPOINT ["/bin/bash"]
CMD []

# Entrypoint contract: Strategy B runtype=args (Vast.ai accepts /bin/bash
# as onstart_cmd and Args=["-c", <script>] as the bash payload). The
# gateway-side onstart script (gateway/internal/primary/onstart.go
# primaryOnstartHead) finishes with:
#   exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/services.conf
# replacing PID 1 with supervisord (Pitfall #2 invariant — Vast crash
# detection observes the real PID 1 state).
