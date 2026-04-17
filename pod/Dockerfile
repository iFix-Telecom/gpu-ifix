# syntax=docker/dockerfile:1.7
#
# pod/Dockerfile — Ifix AI Pod (Phase 1, ghcr.io/ifixtelecom/ifix-ai-pod)
#
# Purpose: slim (~2 GB target, D-01) CUDA image bundling the llama-server
# binary and the Qwen 3.5 27B tool-calling Jinja template (plan 01-02).
# Weights are NEVER embedded (D-01) — they are downloaded by onstart.sh
# (plan 01-05) from MinIO and bind-mounted at `/weights` by docker-compose.
#
# Flags locked by docker-compose.yml command override (D-07, D-08, POD-06):
#   -np 2 --ctx-size 16384 --jinja --chat-template-file <template>
#
# Refs:
#   - .planning/phases/01-gpu-pod-image-smoke-test/01-03-PLAN.md
#   - .planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md D-01/D-07/D-08/D-24
#   - .planning/phases/01-gpu-pod-image-smoke-test/01-PATTERNS.md §pod/Dockerfile
# =========================================================================

# =========================================================================
# Stage 1: llama-bin — extract the llama-server binary from the upstream
#                     CUDA image. Keeps our final image small (D-01).
# =========================================================================
FROM ghcr.io/ggml-org/llama.cpp:server-cuda AS llama-bin

# (nothing to do; we only COPY from this stage in the final stage)

# =========================================================================
# Stage 2: runtime — CUDA 12.4 runtime + curl/ca-certs for weight download
#                    and health probes. No Python, no pip.
# =========================================================================
FROM nvidia/cuda:12.4.1-cudnn-runtime-ubuntu22.04 AS runtime

# Ifix convention: TZ + locale + minimal apt surface
ENV DEBIAN_FRONTEND=noninteractive \
    TZ=America/Sao_Paulo \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    PYTHONUNBUFFERED=1

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      tzdata \
      tini \
  && ln -fs /usr/share/zoneinfo/America/Sao_Paulo /etc/localtime \
  && dpkg-reconfigure -f noninteractive tzdata \
  && rm -rf /var/lib/apt/lists/*

# Copy llama-server binary from upstream CUDA image
COPY --from=llama-bin /app/llama-server /usr/local/bin/llama-server
# Copy shared libs the llama-server binary needs (ggml runtimes, BLAS, etc.)
COPY --from=llama-bin /app/libggml*.so /usr/local/lib/
COPY --from=llama-bin /app/libllama*.so /usr/local/lib/
RUN ldconfig

# Bundle the patched Jinja template into the image (from plan 02)
COPY pod/templates/qwen3.5-27b-tool-calling.jinja /app/templates/qwen3.5-27b-tool-calling.jinja
COPY pod/templates/qwen3.5-27b-tool-calling.jinja.sha256 /app/templates/qwen3.5-27b-tool-calling.jinja.sha256

# Sanity: verify template integrity at build time (fails the build if drift)
RUN EXPECTED="$(tr -d '[:space:]' < /app/templates/qwen3.5-27b-tool-calling.jinja.sha256)" \
  && ACTUAL="$(sha256sum /app/templates/qwen3.5-27b-tool-calling.jinja | awk '{print $1}')" \
  && test "$EXPECTED" = "$ACTUAL" || (echo "FATAL: template SHA-256 drift — expected $EXPECTED got $ACTUAL"; exit 1)

# Prepare weights mountpoint (weights bind-mounted at runtime, not copied)
RUN mkdir -p /weights/qwen /weights/whisper /weights/bge-m3 /app/templates

EXPOSE 8000
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/bin/tini", "--"]
# Default CMD is the llama-server invocation (D-07, D-08, POD-06).
# Per-service compose files override this for speaches/infinity/etc.
CMD ["llama-server", \
     "--host", "0.0.0.0", \
     "--port", "8000", \
     "-m", "/weights/qwen/model.gguf", \
     "-np", "2", \
     "--ctx-size", "16384", \
     "--jinja", \
     "--chat-template-file", "/app/templates/qwen3.5-27b-tool-calling.jinja"]
