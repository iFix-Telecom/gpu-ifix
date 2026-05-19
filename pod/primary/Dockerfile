# pod/primary/Dockerfile — Phase 6.6 Strategy α LOCKED (Wave 0).
#
# DinD REJECTED per 06.6-SPIKE-dind-privileged.md — overlayfs in nested
# namespace fails on Vast.ai 4090 hosts (Linux kernel limit, not bypassable
# even with --privileged:1 + iptables=false workaround). supervisord as
# PID 1 replaces DinD (06.6-WAVE0-GATES.md Decision 2 LOCKED).
#
# Hybrid build:
#   - llama.cpp + dcgm-exporter SHA-pinned upstream (binaries portable,
#     multi-stage COPY safe).
#   - Speaches + Infinity installed via pip into dedicated venvs at
#     /opt/{speaches,infinity}-venv. Plan 04 originally specified multi-stage
#     COPY of /app from upstream speaches/infinity images, but (a) speaches
#     payload lives at /home/ubuntu/speaches/ (no /app), and (b) Python venvs
#     have absolute paths baked into shebangs + pyvenv.cfg + *.dist-info/RECORD
#     manifests, so naive relocation breaks runtime. pip install in the final
#     stage is the canonical install path. SHA-pin invariant is preserved for
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

# Stage 2: Final image — llama.cpp:server-cuda-b9191 carries Qwen3.6 SSM
# support (Wave 0 UPGRADED from b9128 per 06.6-SPIKE-qwen3.6-jinja.md Round 3
# tensor-blk-64 SSM pass). All 4 services co-locate here under supervisord.
FROM ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3 AS final

# Install supervisord (PID 1) + aria2 (multi-stream weight download from
# MinIO — single-stream mc cp empirically failed Wave 0 spike Round 1
# with EOF on 16GB GGUF) + curl/ca-certificates (mc client download from
# https://dl.min.io/ at onstart time) + Python 3.11 (for Speaches/Infinity
# pip install below) + build-essential (some PyTorch wheels need it).
RUN apt-get update && apt-get install -y --no-install-recommends \
        supervisor aria2 ca-certificates curl \
        python3 python3-venv python3-pip git build-essential \
    && rm -rf /var/lib/apt/lists/*

# Install Speaches + Infinity into dedicated venvs. Versions track the
# upstream image tags previously SHA-pinned in Plan 04: Speaches 0.9.0rc3
# (was ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3) and Infinity
# 0.0.77 (was michaelf34/infinity:0.0.77). Base image is Ubuntu 24.04 Noble
# so python3 resolves to python3.12 (both wheels support 3.10+).
#
# Pin notes (UAT 2026-05-18 attempt 11 root causes):
#  - onnx-asr<0.10 — speaches 0.9.0rc3 imports `NemoConformerTdt` from
#    `onnx_asr.models`, removed in onnx-asr 0.10+ (attempt 10).
#  - optimum<2.0 — infinity-emb 0.0.77 imports `optimum.bettertransformer`
#    which was REMOVED in optimum 2.0+. The previous form
#    `optimum[bettertransformer]` was a no-op because that extras key does
#    not exist; pip silently fell back to plain optimum at the latest
#    version (2.1.0 today) and Infinity crash-looped on import (attempt 11).
RUN python3 -m venv /opt/speaches-venv \
    && /opt/speaches-venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/speaches-venv/bin/pip install --no-cache-dir \
         "git+https://github.com/speaches-ai/speaches@v0.9.0-rc.3" \
    && /opt/speaches-venv/bin/pip install --no-cache-dir \
         "onnx-asr<0.10.0" \
    && python3 -m venv /opt/infinity-venv \
    && /opt/infinity-venv/bin/pip install --no-cache-dir --upgrade pip \
    && /opt/infinity-venv/bin/pip install --no-cache-dir "infinity-emb[all]==0.0.77" \
    && /opt/infinity-venv/bin/pip install --no-cache-dir \
         "optimum<2.0"

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
       > /opt/speaches-data/realtime-console/dist/index.html

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

# Pre-create runtime directories referenced by supervisord.conf + onstart.
# /weights/* are mount-targets for aria2c-downloaded GGUF/tarball artifacts;
# /app/templates is the optional B2 Jinja fallback mount target (default
# empty per WAVE0-GATES Decision 3 B1 embedded).
RUN mkdir -p /var/log /weights/qwen /weights/whisper /weights/bge-m3 /app/templates

# Disable inherited HEALTHCHECK from llama.cpp base (which curls :8080/health,
# but our supervisord-managed services bind to :8000/:8001/:8002/:9400 — never
# :8080). Continuous healthcheck failure caused Vast.ai's worker agent to reap
# the container as exited within 30-60s of start across all 8 UAT attempts on
# 2026-05-18 (gsd-debug session primary-uat-custom-image-vast-runtype-args).
# The gateway reconciler does the real 4-endpoint health-poll via Vast-exposed
# host ports (gateway/internal/primary/reconciler.go evaluateProvisioning).
HEALTHCHECK NONE

LABEL org.opencontainers.image.source="https://github.com/IfixTelecom/gpu-ifix"
LABEL org.opencontainers.image.description="Phase 6.6 primary pod — supervisord + llama.cpp b9191 + speaches + infinity + dcgm"

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
