# syntax=docker/dockerfile:1.7
#
# pod/Dockerfile — Ifix AI Pod (Phase 1, ghcr.io/ifixtelecom/ifix-ai-pod)
#
# Purpose: pinned llama.cpp upstream image (CUDA backend pre-installed, layout
# preserved) + bundled Qwen 3.5 27B tool-calling Jinja template (plan 02).
# Weights are NEVER embedded (D-01) — downloaded by onstart.sh from MinIO and
# bind-mounted at `/weights` by docker-compose at runtime.
#
# History: revisions prior to 2026-05-13 used a 2-stage build with
# `COPY --from=llama-bin /app/llama-server /usr/local/bin/` + flat lib copy.
# That broke ggml backend plugin discovery in llama.cpp ≥b9128 (Phase 5 UAT
# regression — backends require the upstream `/app/` layout). Single-stage
# `FROM upstream` trades ~4 GB image bloat (2 GB → 6 GB) for a working loader.
#
# Refs:
#   - .planning/phases/01-gpu-pod-image-smoke-test/01-03-PLAN.md
#   - .planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md D-01/D-07/D-08
# =========================================================================

# Pin to a specific upstream build. `:server-cuda` floating tag has caused
# Vast.ai pull timeouts; pinned tags are reproducible across CI + pod deploys.
FROM ghcr.io/ggml-org/llama.cpp:server-cuda-b9128 AS runtime

ENV DEBIAN_FRONTEND=noninteractive \
    TZ=America/Sao_Paulo \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# Upstream image already has libggml*/libllama*/libmtmd* + llama-server in /app/
# with the layout that ggml_backend_load_all_from_path expects. Just add tini
# + curl/ca-certs for the onstart weight download path.
RUN apt-get update \
  && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      tzdata \
      tini \
  && ln -fs /usr/share/zoneinfo/America/Sao_Paulo /etc/localtime \
  && dpkg-reconfigure -f noninteractive tzdata \
  && rm -rf /var/lib/apt/lists/*

# Bundle the patched Jinja template (plan 02 D-08)
COPY pod/templates/qwen3.5-27b-tool-calling.jinja /app/templates/qwen3.5-27b-tool-calling.jinja
COPY pod/templates/qwen3.5-27b-tool-calling.jinja.sha256 /app/templates/qwen3.5-27b-tool-calling.jinja.sha256

# Phase 6 emergency-pod LLM-only bootstrap. Phase 1 compose overrides CMD,
# so embedding this script does not change the multi-service flow. Reading
# this script as image CMD lets emergency pods (created by the gateway
# reconciler without SSH access) self-bootstrap qwen weights from MinIO.
COPY pod/scripts/emerg-bootstrap.sh /usr/local/bin/emerg-bootstrap.sh
RUN chmod +x /usr/local/bin/emerg-bootstrap.sh

# Sanity: verify template integrity at build time (fails the build if drift)
RUN EXPECTED="$(tr -d '[:space:]' < /app/templates/qwen3.5-27b-tool-calling.jinja.sha256)" \
  && ACTUAL="$(sha256sum /app/templates/qwen3.5-27b-tool-calling.jinja | awk '{print $1}')" \
  && test "$EXPECTED" = "$ACTUAL" || (echo "FATAL: template SHA-256 drift — expected $EXPECTED got $ACTUAL"; exit 1)

# Prepare weights mountpoint (weights bind-mounted at runtime, not copied)
RUN mkdir -p /weights/qwen /weights/whisper /weights/bge-m3

EXPOSE 8000
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/bin/tini", "--"]
# Default CMD: emergency-pod bootstrap — downloads qwen weights if missing,
# then execs llama-server with the same Phase 1 D-07/D-08 flags. Phase 1
# compose service `llama` overrides this CMD with the explicit llama-server
# invocation, so multi-service deployments are unaffected.
CMD ["/usr/local/bin/emerg-bootstrap.sh"]
