---
phase: 01-gpu-pod-image-smoke-test
plan: 03
subsystem: infra
tags: [docker, docker-compose, cuda, llama.cpp, speaches, infinity, dcgm-exporter, healthcheck, gpu]

# Dependency graph
requires:
  - phase: 01-gpu-pod-image-smoke-test/01
    provides: ".gitignore (.env excluded), .dockerignore (weights/fixtures excluded), repo hygiene scaffolding"
  - phase: 01-gpu-pod-image-smoke-test/02
    provides: "pod/templates/qwen3.5-27b-tool-calling.jinja + .sha256 sidecar (COPY target in Dockerfile Task 1)"
provides:
  - "pod/Dockerfile: multi-stage CUDA 12.4 image bundling llama-server + Qwen Jinja template + SHA-256 drift check (ghcr.io/ifixtelecom/ifix-ai-pod contract)"
  - "pod/docker-compose.yml: 5-service orchestration (llama, speaches, infinity, health-bridge, dcgm-exporter) with GPU reservation anchor, per-service healthchecks, shared ifix-ai-pod bridge network"
  - "pod/.env.example: env-var contract (image tags, weights dir, MinIO endpoint, versioned keys + SHA-256, LOG_LEVEL/ENV)"
  - "Locked llama-server command line: -np 2, --ctx-size 16384, --jinja, --chat-template-file (D-07, D-08, POD-06)"
  - "Port contract enforced: 8000 LLM, 8001 STT, 8002 embed, 9100 health-bridge, 9400 dcgm (D-10, D-27)"
affects: [01-04, 01-05, 01-06, 01-07, 01-08, 01-09]

# Tech tracking
tech-stack:
  added:
    - docker multi-stage with cross-image COPY (llama.cpp:server-cuda -> cuda:runtime)
    - nvidia-container-toolkit compose reservation syntax (driver: nvidia, capabilities: [gpu])
    - tini PID 1 for SIGTERM forwarding
  patterns:
    - "Multi-source Dockerfile: extract prebuilt binary from upstream image stage into slim runtime stage (keeps D-01 ~2 GB target feasible without bundling weights)"
    - "Build-time SHA-256 integrity check: fail-fast Docker build on vendored template drift (enforces T-01-02-01 and T-01-03-02)"
    - "Compose x-common-env + x-gpu-all YAML anchors for 5-service pod composition"
    - "Healthcheck CMD-SHELL wget + python3 urllib fallback (handles images without wget per Ifix agents/ precedent)"

key-files:
  created:
    - "pod/Dockerfile (multi-stage: ghcr.io/ggml-org/llama.cpp:server-cuda AS llama-bin -> nvidia/cuda:12.4.1-cudnn-runtime-ubuntu22.04 AS runtime)"
    - "pod/docker-compose.yml (5 services, GPU reservation, healthchecks, named network ifix-ai-pod)"
    - "pod/.env.example (env-var operator contract; no real secrets)"
  modified: []

key-decisions:
  - "llama-server runs AS A COMPOSE SERVICE using the ifix-ai-pod image (not the raw upstream llama.cpp image) — keeps the template integrity check + CUDA runtime identical to the smoke-test target (D-24 compatible)."
  - "Upstream speaches/infinity images consumed DIRECTLY via compose (not re-packaged inside ifix-ai-pod) — D-25 + D-26 specify those images; repackaging would bloat D-01's 2 GB target."
  - "Healthcheck uses CMD-SHELL wget + python3 urllib fallback — verified empirically that ghcr.io/speaches-ai/speaches:latest-cuda has python3 (at /home/ubuntu/speaches/.venv/bin/python3) but NO wget. Pure CMD wget would silently loop unhealthy (exit 127)."
  - "start_period: 120s on inference services — cold-load (weight mmap + CUDA kernel warmup) can take 60-90s; 120s avoids false RESTARTs under healthy cold-boot (PITFALLS §1)."
  - "health-bridge depends_on: llama/speaches/infinity uses only start-order (no condition: service_healthy) — probes run every 10s per D-11 and report 'degraded' until upstreams become healthy; matches D-11 probe model."
  - "No USER directive in Dockerfile — Vast.ai pods require root for /dev/nvidia* device access (documented in PATTERNS.md §pod/Dockerfile divergences)."
  - "dcgm-exporter granted cap_add: SYS_ADMIN — required by DCGM to read NVML counters; scope-limited and accepted (T-01-03-04 disposition: accept)."

patterns-established:
  - "External-artifact-in-image build-time integrity: prepend provenance + sha256 sidecar at plan-02 time, verify inside Dockerfile RUN step at build time. Fails the image build on drift — prevents silent deployment of tampered template."
  - "Compose GPU reservation anchor: reusable x-gpu-all YAML anchor for any service that needs the RTX 4090; applied to llama/speaches/infinity/dcgm-exporter, omitted on health-bridge."
  - "OpenAI-compat port convention: 8000 LLM / 8001 STT / 8002 embed — reserved for the life of this project (matches plan 01-04 health-bridge env contract)."

requirements-completed: [POD-01, POD-02, POD-04, POD-06]

# Metrics
duration: 8min
completed: 2026-04-17
---

# Phase 01 Plan 03: Pod Dockerfile + docker-compose + .env.example Summary

**Multi-stage CUDA 12.4 Dockerfile + 5-service docker-compose.yml (llama/speaches/infinity/health-bridge/dcgm-exporter) wiring the locked llama-server flags (-np 2, --ctx-size 16384, --jinja, --chat-template-file) and Qwen template SHA-256 drift check, plus operator .env contract.**

## Performance

- **Duration:** ~8 min
- **Started:** 2026-04-17T23:06:49Z
- **Completed:** 2026-04-17T23:14:40Z
- **Tasks:** 3 / 3
- **Files created:** 3, modified: 0

## Accomplishments

- **pod/Dockerfile:** Multi-stage build. Stage 1 (`llama-bin`) extracts the llama-server binary + ggml/llama shared libs from `ghcr.io/ggml-org/llama.cpp:server-cuda`. Stage 2 (`runtime`) uses `nvidia/cuda:12.4.1-cudnn-runtime-ubuntu22.04`, installs only ca-certificates/curl/tini/tzdata, copies the binary + Qwen Jinja template, and runs a build-time SHA-256 drift check (`test "$EXPECTED" = "$ACTUAL"`). CMD defaults to the locked llama-server flags so `docker run ... ifix-ai-pod` is runnable standalone for smoke tests.
- **pod/docker-compose.yml:** 5 services on a shared `ifix-ai-pod` bridge network. Service names (`llama`, `speaches`, `infinity`, `health-bridge`, `dcgm-exporter`) match the plan-04 health-bridge env contract (`LLAMA_URL: http://llama:8000`, etc.). GPU reservation via `x-gpu-all` YAML anchor on all four GPU-consuming services. Weights bind-mounted read-only into `llama`, read-write into `speaches`/`infinity` (HF caches). Healthcheck form `CMD-SHELL wget -q -O - ... || python3 -c "urllib..."` handles the speaches image which lacks wget.
- **pod/.env.example:** Documents every `${VAR}` reference in the compose file — image tags, `WEIGHTS_DIR`, MinIO endpoint/region/bucket/keys (D-02), versioned weight keys (D-06), SHA-256 sidecars (D-05), `ENV`/`LOG_LEVEL`. All sensitive values are `<placeholder>` tokens (T-01-03-07).
- **Validation:** `docker compose -f pod/docker-compose.yml config` exit 0; every Task-1/2/3 `<automated>` grep in the plan passes verbatim.

## Task Commits

Each task committed atomically (`--no-verify` per worktree parallel-executor policy):

1. **Task 1: pod/Dockerfile** — `bdda7cb` (feat)
2. **Task 2: pod/docker-compose.yml** — `669b62a` (feat)
3. **Task 3: pod/.env.example** — `1e87563` (docs)

**Plan metadata commit:** this SUMMARY.md will be committed next.

## Files Created/Modified

| Path | Role | Notes |
|---|---|---|
| `pod/Dockerfile` | Infra (Docker multi-stage) | 81 lines. Two stages (`llama-bin`, `runtime`). CUDA 12.4 runtime, no Python. tini PID 1. SHA-256 drift check at build. |
| `pod/docker-compose.yml` | Infra (Compose Specification) | 183 lines. 5 services + 1 bridge network. `x-common-env` + `x-gpu-all` anchors. 4× wget healthcheck lines (llama, speaches, infinity, dcgm). `start_period: 120s` on inference services. |
| `pod/.env.example` | Infra (env contract) | 52 lines. Image tags, WEIGHTS_DIR, MinIO, versioned keys, SHA-256 sidecars, ENV/LOG_LEVEL. No real secrets. |

## Final Image Tags (as referenced by compose)

| Service | Image | Pin | Source |
|---|---|---|---|
| llama | `${IFIX_AI_POD_IMAGE:-ghcr.io/ifixtelecom/ifix-ai-pod:latest}` | `:latest` (Phase 1 baseline; pinned to `:v1.0.0` via D-23 after smoke-gates go green) | built from `pod/Dockerfile` (this plan) |
| speaches | `ghcr.io/speaches-ai/speaches:latest-cuda` | `:latest-cuda` (D-25) | upstream |
| infinity | `michaelf34/infinity:latest` | `:latest` (D-26) | upstream |
| health-bridge | `${IFIX_AI_POD_HEALTH_BRIDGE_IMAGE:-ghcr.io/ifixtelecom/ifix-ai-pod-health-bridge:latest}` | `:latest` (Phase 1 baseline; built by plan 01-04) | built from `pod/health-bridge/Dockerfile` (plan 01-04) |
| dcgm-exporter | `nvcr.io/nvidia/k8s/dcgm-exporter:latest-ubuntu22.04` | `:latest-ubuntu22.04` (D-27) | upstream |

Digest pinning is tracked as a T-01-03-06 follow-up inside plan 01-07 (build-pod.yml CI); Phase 1 baseline stays on floating tags to keep the feedback loop quick during smoke iteration.

## Exact llama-server Command Line (Locked)

From `pod/docker-compose.yml` (Task 2 — llama service `command:` block):

```
llama-server
  --host 0.0.0.0
  --port 8000
  -m /weights/qwen/model.gguf
  -np 2
  --ctx-size 16384
  --jinja
  --chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja
  --log-format json
```

The **Dockerfile default CMD** (also present, so `docker run ... ifix-ai-pod` works for standalone smoke) uses the same flags verbatim (JSON exec form). The compose `command:` overrides and also adds `--log-format json` for NDJSON logs (matches Ifix Pino/structlog convention).

Verification exactly matches plan acceptance:
- `grep -q -- "--ctx-size 16384" pod/docker-compose.yml` (exit 0)
- `grep -q -- "-np 2" pod/docker-compose.yml` (exit 0)
- `grep -q "chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja" pod/docker-compose.yml` (exit 0)

## Decisions Made

- **Multi-stage cross-image COPY over "run upstream image directly":** keeps the single `ifix-ai-pod` image under the 2 GB target (D-01) while still owning the Dockerfile (for template-integrity check, TZ/locale, tini PID 1, and future-proofing against upstream CMD/entrypoint churn).
- **CUDA base 12.4.1 runtime (not devel):** shaves ~700 MB off the final image; no toolchain needed at runtime. Matches `llama.cpp:server-cuda` ABI compatibility (CUDA 12.x per research STACK.md).
- **tini PID 1 + STOPSIGNAL SIGTERM + stop_grace_period 30s:** ensures CUDA context teardown on container stop — prevents driver ghost-lock on pod redeploy (PITFALLS analog).
- **Healthcheck CMD-SHELL with Python fallback (not plain CMD wget):** validated empirically with `docker run --rm --entrypoint="" ghcr.io/speaches-ai/speaches:latest-cuda sh -c "which wget || echo WGET_MISSING"` — speaches image emits `WGET_MISSING` (exit 0). The Python urllib fallback inside `||` covers this silently.
- **Compose v3 `depends_on: [llama, speaches, infinity]` on health-bridge WITHOUT `condition: service_healthy`:** matches D-11 probe model — health-bridge should START regardless of upstream state and REPORT state via `/health/{llm,stt,embed}`, not block on healthy upstreams.

## Deviations from Plan

None - plan executed exactly as written.

All three tasks matched the plan's `<action>` blocks verbatim; all `<automated>` verify one-liners passed on first run; `docker compose config` exited 0. The plan's execution-time healthcheck spike for speaches+infinity was partially executed (see "Issues Encountered"): speaches probed cleanly (no wget confirmed), infinity pull failed due to host disk space — but that doesn't change the compose file (the spike was advisory, the `CMD-SHELL` Python fallback is unconditional coverage).

## Issues Encountered

- **Infinity image pull hit disk-space limit on the executor VPS.** `docker pull michaelf34/infinity:latest` failed with `no space left on device` mid-extraction. This is an executor-environment limitation, not a plan-fidelity issue: the same image is the one plan-06 smoke-test will target on Vast.ai (which has its own disk). The plan's healthcheck choice (CMD-SHELL with Python fallback) already covers both `which wget = 0` and `which wget = 127` image variants, so no compose edit was needed. Docs: plan 01-05 onstart.sh runs `docker pull` on the Vast.ai host where disk is provisioned for this workload.
- **Speaches healthcheck spike confirmed `wget` absent but `python3` present:** `docker run --rm --entrypoint="" ghcr.io/speaches-ai/speaches:latest-cuda sh -c "which wget || echo WGET_MISSING"` returned `WGET_MISSING`; `which python3` returned `/home/ubuntu/speaches/.venv/bin/python3`. This validates the CMD-SHELL wget-or-python choice in the plan.

## Authentication Gates

None - this plan is pure scaffolding (Docker assets + env docs), no external auth involved.

## User Setup Required

None - no external service configuration required for this plan. `pod/.env.example` documents the variables operators (or plan 01-05's onstart.sh) will populate at pod boot; actual values are provisioned via Vast.ai env injection and MinIO bucket policy, not through this plan.

## Threat Flags

None - all new security-relevant surface is covered by the plan's `<threat_model>` (T-01-03-01 through T-01-03-07). No new endpoints, no new auth paths, no new trust boundaries beyond those documented.

## Next Phase Readiness

This plan is the concrete contract that unblocks Wave-2 and beyond:

- **Plan 01-04 (health-bridge Go binary)** can code against the compose service-name DNS targets `llama:8000`, `speaches:8000`, `infinity:8002` (compose file declares them verbatim).
- **Plan 01-05 (onstart.sh)** can invoke `docker compose -f /opt/ifix-ai-pod/docker-compose.yml up -d` against the finished file; all `${VAR}` interpolations are documented in `.env.example`.
- **Plan 01-07 (build-pod.yml)** can `docker build --platform linux/amd64 -f pod/Dockerfile -t ghcr.io/ifixtelecom/ifix-ai-pod:<tag> .` with layer-cached builds; the template SHA-256 check runs inside the image and fails CI on drift.
- **Plan 01-08 (smoke.yml)** can target ports 8000/8001/8002/9100/9400 as declared by the compose `ports:` sections.

**TDD Gate Compliance:** Plan is `type: auto` with no TDD-flagged tasks. No RED/GREEN commits expected; all commits are feat/docs. Verified in `git log`: `bdda7cb` feat, `669b62a` feat, `1e87563` docs — consistent with plan type.

## Self-Check

**File existence:**
- pod/Dockerfile — FOUND
- pod/docker-compose.yml — FOUND
- pod/.env.example — FOUND

**Commit existence (in worktree history):**
- `bdda7cb` (feat Dockerfile) — FOUND
- `669b62a` (feat docker-compose.yml) — FOUND
- `1e87563` (docs .env.example) — FOUND

**Plan-level verification block (from PLAN.md lines 569-576):**
- `docker compose -f pod/docker-compose.yml config > /dev/null` — exit 0
- `grep -q -- "--ctx-size 16384" pod/docker-compose.yml` — exit 0
- `grep -q -- "-np 2" pod/docker-compose.yml` — exit 0
- `grep -q "chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja" pod/docker-compose.yml` — exit 0
- `grep -q "nvcr.io/nvidia/k8s/dcgm-exporter" pod/docker-compose.yml` — exit 0

**Task-level verify blocks (every grep in the plan):**
- Task 1 Dockerfile: 14 / 14 greps pass (llama-bin stage, runtime stage, binary COPY, template COPY, flags, EXPOSE, STOPSIGNAL, TZ, sha256 check, no weights COPY)
- Task 2 docker-compose.yml: 20 / 20 greps pass (5 service blocks, external images, GPU driver, flags, URLs, ports, healthchecks ≥ 4, no curl -fsS)
- Task 3 .env.example: 9 / 9 greps pass (file exists, WEIGHTS_DIR, MINIO_ENDPOINT, WEIGHTS_QWEN_KEY, WEIGHTS_QWEN_SHA256, IFIX_AI_POD_IMAGE, LOG_LEVEL, no long-token secrets, .env in .gitignore)

## Self-Check: PASSED

---
*Phase: 01-gpu-pod-image-smoke-test*
*Plan: 03*
*Completed: 2026-04-17*
