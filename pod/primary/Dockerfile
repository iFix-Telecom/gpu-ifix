# pod/primary/Dockerfile — Phase 11.1 shrink (STT off-pod).
#
# Phase 11.1 (2026-06-04) removed the tier-0 STT venv and the rollback-only
# embed venv: STT now resolves via the tier-1 OpenAI fallback chain through
# the gateway. This collapses the pod stack to llama LLM + chatterbox TTS +
# dcgm metrics + health-bridge probe. Pre-shrink commentary on the prior
# venv layout and pip pinning is preserved in git history (commit pre-Phase 11.1).
#
# DinD REJECTED per 06.6-SPIKE-dind-privileged.md — overlayfs in nested
# namespace fails on Vast.ai 4090 hosts (Linux kernel limit, not bypassable
# even with --privileged:1 + iptables=false workaround). supervisord as
# PID 1 replaces DinD (06.6-WAVE0-GATES.md Decision 2 LOCKED).
#
# Hybrid build:
#   - llama.cpp + dcgm-exporter SHA-pinned upstream (binaries portable,
#     multi-stage COPY safe).
#   - Chatterbox TTS installed via pip into a dedicated venv at
#     /opt/chatterbox-venv (torch 2.7.1 cu128 trio + chatterbox-tts 0.1.7).
#     SHA-pin invariant is preserved for the non-substitutable component
#     (llama.cpp b9191 — required for Qwen3.6 SSM tensor support per Wave 0
#     spike Round 3).
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

# Stage 2: Final image — llama.cpp:server-cuda-b9191 carries Qwen3.6 SSM
# support (Wave 0 UPGRADED from b9128 per 06.6-SPIKE-qwen3.6-jinja.md Round 3
# tensor-blk-64 SSM pass). All 4 services co-locate here under supervisord.
FROM ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3 AS final

# Install supervisord (PID 1) + aria2 (multi-stream weight download from
# MinIO — single-stream mc cp empirically failed Wave 0 spike Round 1
# with EOF on 16GB GGUF) + curl/ca-certificates (mc client download from
# https://dl.min.io/ at onstart time) + Python 3.11 (for the Chatterbox TTS
# venv installed below) + build-essential (some PyTorch wheels need it).
RUN apt-get update && apt-get install -y --no-install-recommends \
        supervisor aria2 ca-certificates curl \
        python3 python3-venv python3-pip git build-essential \
    && rm -rf /var/lib/apt/lists/*

# Chatterbox Multilingual TTS — Phase 06.7 Plan 05 (Wave 1).
#
# Isolated /opt/chatterbox-venv per GATE 2: chatterbox-tts==0.1.7 pins
# torch==2.6.0 + transformers==5.2.0, which historically could not share the
# now-removed tier-0 STT/embed torch stacks (those gated transformers<4.49 for
# optimum.bettertransformer). A dedicated venv keeps the torch stack isolated.
#
# RTX 5090 (Blackwell, sm_120) fix: chatterbox-tts pins torch==2.6.0+cu124, but
# cu124 ships NO sm_120 kernels — the model load crashes with "no kernel image
# is available for execution on the device" (the cu124 trio was validated on a
# 4090/sm_89; Phase 6.6 moved the pod to a 5090). Blackwell support starts at
# torch 2.7+/cu128. So install chatterbox-tts FIRST (pulls its transformers==5.2.0
# + the rest), THEN UPGRADE the version-aligned cu128 trio. Do NOT use --no-deps:
# the cu128 torch wheels need the matching nvidia-cuda-*-cu12 12.8 runtime
# (esp. nvidia-cuda-cupti-cu12) — skipping deps makes torch fall back to the base
# image's CUDA 12.6 libcupti, which lacks `cuptiActivityEnableDriverApi` and
# crashes the import. --index-url cu128 serves torch + the nvidia 12.8 libs;
# --extra-index-url PyPI serves the pure-python deps (numpy stays <2 per chatterbox).
# The torch/torchvision/torchaudio trio MUST stay version-aligned or the chatterbox
# import breaks via `torchvision::nms`. pip warns about chatterbox's torch==2.6.0
# pin — expected and benign; the 2.6->2.7 inference API chatterbox uses is stable.
#
# boto3 is the pinned S3 client the shim uses for the lazy reference-WAV fetch
# from MinIO (zero-shot voice survival). fastapi+uvicorn serve the shim.
RUN python3 -m venv /opt/chatterbox-venv \
    && /opt/chatterbox-venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/chatterbox-venv/bin/pip install --no-cache-dir \
         "chatterbox-tts==0.1.7" \
         "boto3==1.35.99" \
         "fastapi==0.115.6" "uvicorn==0.34.0" \
    && /opt/chatterbox-venv/bin/pip install --no-cache-dir --upgrade \
         --index-url https://download.pytorch.org/whl/cu128 \
         --extra-index-url https://pypi.org/simple \
         "torch==2.7.1" "torchvision==0.22.1" "torchaudio==2.7.1" \
    # resemble-perth (chatterbox watermarker dep) imports `pkg_resources`, which
    # setuptools 81+ removed. Pin <81 LAST (the torch --upgrade above can pull
    # setuptools 82) so pkg_resources stays present, else PerthImplicitWatermarker
    # is None and chatterbox load crashes with "NoneType object is not callable".
    && /opt/chatterbox-venv/bin/pip install --no-cache-dir "setuptools<81"

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

# Chatterbox TTS shim served by [program:chatterbox] (uvicorn on :8003,
# directory=/opt/chatterbox-data). The shim imports torch/chatterbox/torchaudio
# lazily at model-load time, so it lives standalone here.
COPY pod/primary/chatterbox_server.py /opt/chatterbox-data/chatterbox_server.py

# Pre-create runtime directories referenced by supervisord.conf + onstart.
# /weights/qwen is the mount-target for the aria2c-downloaded GGUF artifact;
# /app/templates is the optional B2 Jinja fallback mount target (default
# empty per WAVE0-GATES Decision 3 B1 embedded). Phase 11.1: /weights/bge-m3
# pre-create removed — BGE-M3 embed is off-pod (Phase 06.7 D-03) and the
# pod's onstart `mkdir -p` recreates the directory at runtime if a future
# config re-enables on-pod BGE-M3 downloads.
RUN mkdir -p /var/log /weights/qwen /app/templates \
        /opt/chatterbox-data/models /opt/chatterbox-data/voices

# Disable inherited HEALTHCHECK from llama.cpp base (which curls :8080/health,
# but our supervisord-managed services bind to :8000/:8003/:9100/:9400 — never
# :8080). Continuous healthcheck failure caused Vast.ai's worker agent to reap
# the container as exited within 30-60s of start across all 8 UAT attempts on
# 2026-05-18 (gsd-debug session primary-uat-custom-image-vast-runtype-args).
# The gateway reconciler does the real 4-endpoint health-poll via Vast-exposed
# host ports (gateway/internal/primary/reconciler.go evaluateProvisioning).
HEALTHCHECK NONE

LABEL org.opencontainers.image.source="https://github.com/IfixTelecom/gpu-ifix"
LABEL org.opencontainers.image.description="Phase 11.1 primary pod — supervisord + llama.cpp b9191 + chatterbox-tts + dcgm (STT off-pod via tier-1 OpenAI; embed off-pod per Phase 06.7 D-03)"

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
