# Primary Pod Runbook — Phase 6.6 (Vast.ai Scheduled Auto-provisioning, Strategy α — supervisord 4-service single-container, Wave 0 LOCKED)

**Owner:** IFIX Platform Engineering
**Last updated:** 2026-05-17 (Phase 6.6 plan 06.6-11; revised per checker + Wave 0 supervisord lock 2026-05-17)
**Stack:** `ai-gateway-dev` / `ai-gateway-prod` (Portainer)
**Phase reference (active):** `.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-CONTEXT.md`
**Phase reference (sibling — emergency-pod):** `.planning/phases/06-emergency-pod-template-refactor/06-CONTEXT.md` + [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md)
**Wave 0 operator-locked decisions:** [`06.6-WAVE0-GATES.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-WAVE0-GATES.md)
**DinD rejection evidence:** [`06.6-SPIKE-dind-privileged.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-SPIKE-dind-privileged.md)

> **Wave 0 LOCKED (2026-05-17).** The primary pod runs the custom multi-stage image
> `ghcr.io/ifixtelecom/converseai-primary-pod:vX@sha256:...` (built by
> `.github/workflows/build-primary-pod.yml` from `pod/primary/Dockerfile`).
> Inside the container, **`supervisord` is PID 1** managing 4 co-located child
> processes (llama-server b9191 + speaches + infinity + dcgm-exporter) that
> share the container's network namespace, GPU device, and filesystem.
> **DinD design REJECTED** per Spike Task 1 ([overlayfs in nested namespace
> fails on Vast.ai hosts](#dind-rejection-evidence) — Linux kernel limit, not
> bypassable). Health-bridge Phase 1 service was **DROPPED per CONTEXT.md
> D-05** — gateway Phase 3 probe loop (`ProbeIntervalSeconds=10`) substitutes
> it. See [Architecture](#architecture-wave-0-locked) and [Image Build &
> Bump Policy](#image-build--bump-policy).

This runbook covers the Phase 6.6 primary-pod schedule-driven auto-provisioning
subsystem (`gateway/internal/primary/` + `gateway/cmd/gatewayctl/primary.go`).
Read this when:

- The primary `local-llm` / `local-embed` upstreams should be served by an
  Ifix-owned Vast.ai pod during peak hours (default 08:00–22:00 BRT mon–fri)
  but are not. _(Phase 11.1: STT tier-0 removed; primary pod now serves 2
  local roles — LLM + embed. STT resolves via tier-1 `openai-whisper`
  regardless of primary state.)_
- A Sentry alert fires with tag `subsystem:primary` (any of:
  `alert:budget_exceeded`, `shutdown_reason:provision_failure`,
  `shutdown_reason:health_timeout`, `state:draining` stuck >>grace).
- Cost dashboards show unexplained Vast.ai charges attributed to the primary
  reconciler (`event_kind=primary_lifecycle_close` in `audit_log`).
- Post-incident review of a schedule-driven provisioning / drain cycle.
- Operator needs to bump the upstream LLM image (e.g. b9191 → b9234) — see
  [Image Build & Bump Policy](#image-build--bump-policy).

Sibling runbooks:

- [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md) — Phase 6 reactive
  emergency pod (breaker-driven, NOT schedule-driven). Phase 6.6 and Phase 6
  COEXIST per [Pitfall #11](#pitfall-11--emergencyprimary-coexistence): when
  primary marks Ready, emerg force-destroys (if active).
- [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) — Phase 3 circuit-breaker +
  tier-0 ↔ tier-1 fallback. Outside peak hours (or during primary drain),
  apps fall through to fallback chain (OpenRouter LLM + OpenAI Whisper STT +
  OpenAI embeddings).
- [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md) — Phase 4
  per-tenant rate-limit + quota + billing.

---

## Overview

The Phase 6.6 primary pod is a **schedule-driven** (NOT failover-driven) Vast.ai
pod that serves the 3 local AI roles (LLM + STT + Embed) during business hours
to replace OpenRouter + OpenAI fallback usage for cost optimization.

| Aspect | Primary Pod (Phase 6.6) | Emergency Pod (Phase 6) |
|--------|-------------------------|--------------------------|
| Trigger | **Schedule** (peak-hour) | **Breaker OPEN sustained** |
| Subsystem tag | `subsystem:primary` | `subsystem:emerg` |
| Reconciler | `gateway/internal/primary/` | `gateway/internal/emerg/` |
| FSM states | 5 (Asleep / Provisioning / Ready / Draining / Destroying) | 7 (Healthy → Degraded → Failed_over → Provisioning → Active → Recovering → Cooldown) |
| Pod image | **Custom** `ghcr.io/ifixtelecom/converseai-primary-pod:vX@sha256:...` (4 services baked) | Upstream `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` (1 service) |
| Onstart shape | Strategy B (`Runtype=args` + `/bin/bash -c <inline>`) | Same |
| Services served | LLM + Embed (2 roles — Phase 11.1 dropped STT tier-0) | LLM only |
| Cost model | ~14h × 22 days × ~$0.40/h ≈ **$130/mo nominal** | Per-incident; ~$2/incident |
| Budget env | `MONTHLY_PRIMARY_BUDGET_BRL=800` | `MONTHLY_EMERGENCY_BUDGET_BRL=200` |
| Drain semantics | Schedule window exit → 5min grace ramp-down | Cutback grace after primary recovers |

**Precedence per [Pitfall #11](#pitfall-11--emergencyprimary-coexistence):**
when both reconcilers want a pod at the same time, **primary takes over** and
emerg force-destroys via the `gw:primary:events` Pub/Sub channel (`Type=primary_ready`
event → emerg subscriber → force-destroy if EmergencyActive).

---

## Architecture (Wave 0 LOCKED)

The primary pod runs as **a single Vast.ai container** with **`supervisord` as
PID 1** managing 4 co-located child processes. This replaces the rejected DinD
design (see [DinD rejection evidence](#dind-rejection-evidence)) while
preserving Strategy B's intent (4 services in 1 pod for low intra-pod latency,
shared GPU, shared filesystem).

```
Vast.ai 4090 host (~$0.30-0.40/h)
└── docker container (Runtype=args, Entrypoint=/bin/bash, Args=["-c", onstart])
    │
    │ onstart bash (gateway/internal/primary/onstart.go primaryOnstartHead)
    │   1. env-var preconditions (10x `: "${VAR:?required}"` — reviews #7)
    │   2. optional sshd install (POD_DEBUG_SSH_PUBLIC_KEY)
    │   3. mc + alias setup (MinIO weight source)
    │   4. parallel aria2c downloads (Qwen GGUF 16GB + Whisper 3GB + BGE-M3 2GB)
    │      with sha256sum -c verify
    │   5. tar -xzf whisper + bge-m3 archives
    │   6. exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/services.conf
    │
    └── PID 1 = supervisord (replaces bash via `exec`)
         │
         ├── [program:llama]    /app/llama-server --host 0.0.0.0 --port 8000
         │                       -m /weights/qwen/model.gguf -ngl 99 -np 2
         │                       --ctx-size 16384 --jinja
         │                       (Qwen3.6 27B Q4_K_M + B1 embedded Jinja)
         │
         ├── [program:speaches] /app/speaches-bin/speaches --host 0.0.0.0
         │                       --port 8001 --model-dir /weights/whisper
         │                       (Whisper-Large-v3)
         │
         ├── [program:infinity] /app/infinity-bin/infinity_emb --port 8002
         │                       --model-name-or-path /weights/bge-m3
         │                       (BGE-M3 embeddings; CPU/GPU autodetect)
         │
         └── [program:dcgm]      /usr/bin/dcgm-exporter --address 0.0.0.0:9400
                                  (GPU metrics for Phase 5 saturation FSM)

         All 4 children share:
           - Container's network namespace (localhost ports 8000/8001/8002/9400)
           - GPU device (4090)
           - Filesystem (/weights/*, /app/*, /var/log/*)
```

**Vast.ai host port mapping:** the gateway requests 4 port mappings in
`vast.CreateRequest.Env`:
```json
{
  "-p 8000:8000": "1",
  "-p 8001:8001": "1",
  "-p 8002:8002": "1",
  "-p 9400:9400": "1"
}
```
Vast.ai exposes each as a public `<pod-ip>:<host-port>` mapping that the
gateway reads from the `/instances/<id>` response. The gateway-side
`primary.Reconciler` POSTs `loader.OverrideTier0("llm"|"stt"|"embed", <url>)`
once all 4 endpoints pass health probes.

### Component cross-references

- **`pod/primary/Dockerfile`** — multi-stage build (4 SHA-pinned FROMs:
  speaches, infinity, dcgm, llama.cpp:b9191 final). See
  [Image Build & Bump Policy](#image-build--bump-policy).
- **`pod/primary/supervisord.conf`** — 4 `[program:*]` blocks with
  `autorestart=true`, `redirect_stderr=true`, priorities 100/200/300/400.
  Baked into image at build time.
- **`.github/workflows/build-primary-pod.yml`** — buildx + push to
  `ghcr.io/ifixtelecom/converseai-primary-pod`.
- **`gateway/internal/primary/onstart.go`** — `primaryOnstartHead` raw-string
  Go const (Pitfall #9 — ZERO format-string templating) +
  `buildPrimaryOnstart` (appends `exec /usr/bin/supervisord …`).
- **`gateway/internal/primary/lifecycle.go`** — `buildCreateRequest` with
  3 SHA fail-fast sentinels (`ErrMissingQwenSHA` / `ErrMissingWhisperSHA` /
  `ErrMissingBGEM3SHA` per reviews #6).
- **`gateway/internal/primary/schedule.go`** — pure-Go `ScheduleRule`,
  `IsInPeak`, `ShouldBeProvisioned`, `NextTransition`.
- **`gateway/internal/primary/fsm.go`** — atomic.Int32 5-state FSM with CAS
  Transition + onChange callback (publishes `Type="transition"` events on
  `gw:primary:events`).
- **`gateway/internal/primary/reconciler.go`** — `evaluateTick` dispatcher
  (one branch per state); `startDrain(reason="schedule_window_exited")`;
  `evaluateDraining` waits inflight==0 OR grace elapsed →
  `Transition(Draining, Destroying, "drain_complete")`.

### Phase 1 health-bridge — DROPPED (D-05 traceability)

The Phase 1 architecture had a 5th service inside the pod: a custom Go
`pod/health-bridge/` (~10MB binary) that aggregated the other services'
`/health` endpoints into one mega-endpoint. **Phase 6.6 D-05 DROPS this
service entirely** ([CONTEXT.md D-05](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-CONTEXT.md)).

Why: Phase 3 introduced gateway-side `ProbeIntervalSeconds=10` active health
checks with latency tracking + breaker FSM (`gateway/internal/breaker/`). The
gateway probes each role independently (`/v1/models` for LLM, `/health` for
Speaches, `/health` for Infinity, `/metrics` for DCGM). The Phase 1
health-bridge duplicated this logic inside the pod with worse visibility
(no per-role latency, no breaker awareness). Removing it:

- Cuts ~10MB image weight + 1 supervisord child process.
- Eliminates a CI artifact (`pod/health-bridge/` Go module).
- Forces the gateway to be the single source of truth for upstream health
  (which is where saturation FSM + breaker FSM already live).

**Operational consequence:** when debugging an "endpoint up but role unhealthy"
state, the operator no longer queries `/health-bridge/all` — instead they
inspect `gateway/internal/breaker` state per role via the existing
`/v1/health/upstreams` admin endpoint (Phase 3 deliverable).

### DinD rejection evidence

The original Phase 6.6 plan called for DinD (`dockerd` running inside the
container + `docker compose up` for the 4 services). Wave 0 Task 1 spike
([`06.6-SPIKE-dind-privileged.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-SPIKE-dind-privileged.md))
empirically refused this design on Vast.ai hosts even with `--privileged:1`:

- `--privileged:1` IS accepted by Vast.ai env (passes through to the
  container).
- `dockerd` boots inside the container only after `iptables=false +
  bridge=none` workaround (host `nf_tables` manipulation blocked).
- **Even with the workaround, overlayfs mount/unmount in the nested
  namespace fails** — Linux kernel namespace limitation, not bypassable.

Result: **DinD is fundamentally impossible on Vast.ai**. supervisord with
4 child processes sharing the container's namespace is the chosen
replacement — same intent (4 services in 1 pod), no nested-docker fragility.

The custom multi-stage Docker image is built **once at CI time** (GHA workflow
`.github/workflows/build-primary-pod.yml`); the Vast.ai container does NOT
build images at runtime.

---

## Image Build & Bump Policy

**Wave 0 LOCKED (2026-05-17, [WAVE0-GATES Decision 1](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-WAVE0-GATES.md#decision-1--image-sha-pins-t-066-sc-mitigation)):**

The primary pod image is `ghcr.io/ifixtelecom/converseai-primary-pod:vX@sha256:...`,
built by `.github/workflows/build-primary-pod.yml` from `pod/primary/Dockerfile`
with **4 SHA-pinned upstream sources** captured 2026-05-17:

| Slot | Upstream image | @sha256 digest |
|------|----------------|----------------|
| LLM | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9191` | `sha256:cb375311f4170bb1aa18840e946f64f99e6094b90bde69dcb6e0a62a183d7ba3` |
| STT | `ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3` | `sha256:5c6206a349e90b9a6782342917e72f84fc7cb60e8afd540f6aa625831ac1fd0f` |
| Embed | `michaelf34/infinity:0.0.77` | `sha256:11e8b3921b9f1a58965afaad4a844c435c9807cbc82c51e47cb147b7d977fc88` |
| GPU metrics | `nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless` | `sha256:60d3b00ac80b4ae77f94dae2f943685605585ad9e92fdccda3154d009ae317cc` |

**Why b9191 (NOT b9128):** Phase 6 emergency pod uses b9128 (Qwen3.5, pure
Transformer). Phase 6.6 primary pod uses **b9191** because b9128 fails to
load Qwen3.6 27B GGUF with `missing tensor 'blk.64.ssm_conv1d.weight'` —
Qwen3.6 hybrid Transformer+SSM not registered in older builds. b9191
includes upstream PRs #23121 + #22384 ([SPIKE-qwen3.6-jinja.md
Round 3](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-SPIKE-qwen3.6-jinja.md)
empirically validated end-to-end tool-calling on b9191).

### GHA build workflow

`.github/workflows/build-primary-pod.yml` triggers on:

- Push to `develop` filtered to `pod/primary/**` + the workflow file itself
- Push to `v*` semver tags (releases)
- Manual `workflow_dispatch`

Tag strategy (mirrors `build-gateway.yml` + the deprecated `build-pod.yml`):

- `dev-{sha}` on `develop` push (immutable per-commit)
- `develop` floating tag on `develop` push
- `v{major}.{minor}.{patch}` on semver tag push
- `{major}.{minor}` on semver tag push (rolling minor)

Pushes to `ghcr.io/ifixtelecom/converseai-primary-pod` with `linux/amd64`
platform + GHA cache scoped to `converseai-primary-pod`.

### Bumping an upstream service

To bump (e.g.) `llama.cpp:server-cuda-b9191` → `server-cuda-b9234`:

```bash
# 1. Find the desired tag on ggml-org GHCR:
#    https://github.com/ggml-org/llama.cpp/pkgs/container/llama.cpp
#    Filter "server-cuda-*"

# 2. Capture the new digest for immutability:
docker manifest inspect ghcr.io/ggml-org/llama.cpp:server-cuda-b9234 \
  | jq -r '.manifests[] | select(.platform.architecture=="amd64").digest'
# Outputs: sha256:abc123...

# 3. Update pod/primary/Dockerfile final FROM line:
#    FROM ghcr.io/ggml-org/llama.cpp:server-cuda-b9234@sha256:abc123... AS final

# 4. Update WAVE0-GATES.md Decision 1 with the new digest + add an amendment
#    block dated to today + operator-signed.

# 5. Bump pod/primary/Dockerfile semver tag (in commit message) — vX.Y.Z+1.

# 6. Push to develop — GHA workflow triggers; new image at
#    ghcr.io/ifixtelecom/converseai-primary-pod:dev-{sha}.

# 7. Update Portainer stack ai-gateway-dev:
#    PRIMARY_TEMPLATE_IMAGE=ghcr.io/ifixtelecom/converseai-primary-pod:dev-{sha}
#    (or vX.Y.Z+1 once tagged).

# 8. UAT via short manual cycle:
#    docker exec ai-gateway-dev_gateway /gatewayctl primary force-up --reason "image_bump_uat"
#    Verify 4 endpoints up + tool-calling smoke.
#    docker exec ai-gateway-dev_gateway /gatewayctl primary force-down
```

**Image bump policy (Wave 0 LOCKED):** any bump to one of the 4 SHA-pinned
FROM stages requires a follow-up `WAVE0-GATES.md` amendment + operator
sign-off. This is the operator-locked guarantee — no in-flight bumps to the
production stack without the audit trail.

### Image SHA-pin enforcement

The gateway side enforces the b9191 default in `config.go` defaults
(verified by `gateway/internal/config/config_test.go`
`TestPrimaryTemplateImage_DefaultPinnedTo_b9191`). Operator can override
`PRIMARY_TEMPLATE_IMAGE` env to a different ref for rollback or A/B testing.

---

## Environment Variables

24 `PRIMARY_*` env vars drive the primary reconciler + 6 reused (MinIO + Vast +
debug + FX). For each: name, type, default, description, whether a value
change requires the gateway container to restart.

### PRIMARY_* (24 fields)

| Env name | Type | Default | Description | Restart required? |
|----------|------|---------|-------------|-------------------|
| `PRIMARY_TEMPLATE_IMAGE` | string | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb37...` | Default upstream LLM image ref (built into the multi-stage Dockerfile FROM). Wave 0 Decision 1 SHA pin. Operator override possible — see [Image Build & Bump Policy](#image-build--bump-policy). | Yes |
| `PRIMARY_SPEACHES_IMAGE` | string | `ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3@sha256:5c62...` | **Build-time only.** Consumed by `pod/primary/Dockerfile` FROM line; NOT passed to runtime pod env. | Yes (CI rebuild needed) |
| `PRIMARY_INFINITY_IMAGE` | string | `michaelf34/infinity:0.0.77@sha256:11e8...` | **Build-time only.** Same as above. | Yes (CI rebuild needed) |
| `PRIMARY_DCGM_IMAGE` | string | `nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless@sha256:60d3...` | **Build-time only.** Same as above. | Yes (CI rebuild needed) |
| `PRIMARY_QWEN_WEIGHTS_KEY` | string | `qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf` | MinIO object key for Qwen3.6 GGUF (~17GB). | Yes |
| `PRIMARY_QWEN_WEIGHTS_SHA256` | string | `a7cbd3ecc0e3f9b333edee61ae66bc87ed713c5d49587a8355814722ed329e0f` | sha256 digest of the Qwen GGUF — onstart `sha256sum -c` checks. Wave 0 verified. | Yes |
| `PRIMARY_QWEN_JINJA_KEY` | string | `""` (empty) | **Wave 0 Decision 3 B1 GGUF-embedded LOCKED.** Empty = `--jinja` flag alone extracts the PEG-native parser from GGUF chat_template. Operator override allowed for future B2 MinIO fallback (would also need `PRIMARY_QWEN_JINJA_SHA256` set). | Yes |
| `PRIMARY_QWEN_JINJA_SHA256` | string | `""` (empty) | Required when `PRIMARY_QWEN_JINJA_KEY` is non-empty. | Yes |
| `PRIMARY_LLAMA_ARGS` | CSV | `""` (empty → use `primaryLlamaArgsDefault` const) | Override llama-server CLI args wholesale. Default args MUST NOT include `--chat-template-file` per B1 embedded LOCKED. **Note:** in Strategy α LOCKED the supervisord.conf inside the custom image is the actual runtime command; this override is reserved for a future B2 fallback that regenerates supervisord.conf at runtime. | Yes |
| `PRIMARY_WHISPER_WEIGHTS_KEY` | string | `whisper-large-v3/v1.0.0/model.tar.gz` | MinIO object key for Whisper tar.gz. | Yes |
| `PRIMARY_WHISPER_WEIGHTS_SHA256` | string | `""` (empty → **FAIL-FAST**) | **Reviews #6 FAIL-FAST.** Empty value causes `buildPrimaryCreateRequest` to return `ErrMissingWhisperSHA`; reconciler logs + enters Cooldown WITHOUT calling Vast.ai. Operator MUST set this before deploy. | Yes |
| `PRIMARY_BGEM3_WEIGHTS_KEY` | string | `bge-m3/v1.0.0/model.tar.gz` | MinIO object key for BGE-M3 tar.gz. | Yes |
| `PRIMARY_BGEM3_WEIGHTS_SHA256` | string | `""` (empty → **FAIL-FAST**) | Reviews #6 — same fail-fast policy as Whisper. | Yes |
| `PRIMARY_VAST_NUM_GPUS_PRIMARY` | int | `1` | **Canonical shape-0 (PRIMARY) GPU count.** Searched first. 1×RTX 3090. | Yes |
| `PRIMARY_VAST_NUM_GPUS_FALLBACK` | int | `2` | **Canonical shape-1 (FALLBACK) GPU count.** Standing config (2×RTX 3090, allowlist 43803/55158). Searched when shape-0 returns zero qualified offers. | Yes |
| `PRIMARY_VAST_GPU_NAME_PRIMARY` | string | `RTX 3090` | Canonical shape-0 GPU model. | Yes |
| `PRIMARY_VAST_GPU_NAME_FALLBACK` | string | `RTX 3090` | Canonical shape-1 GPU model. | Yes |
| `PRIMARY_VAST_PRICE_CAP_PRIMARY` | float | `0.30` | Canonical shape-0 price cap (USD/hour). Epsilon-comparison `cap+0.0001` per Pitfall 5. | Yes |
| `PRIMARY_VAST_PRICE_CAP_FALLBACK` | float | `0.60` | Canonical shape-1 price cap (USD/hour). The standing 2×3090 cap. | Yes |
| `PRIMARY_VAST_REJECT_PRIVATE_IP` | bool | `true` | **Phase 6.6.Y Option A.** Drops offers advertising an RFC1918 `public_ipaddr` (10/8, 172.16/12, 192.168/16) at offer time, pre-provision. Cheap guard for the RFC1918 subclass only — the timeout-on-public-IP failure mode is caught by `PRIMARY_PUBLIC_PORT_BIND_BUDGET_SECONDS`. | Yes |
| `PRIMARY_PUBLIC_PORT_BIND_BUDGET_SECONDS` | int | `120` | **Phase 6.6.Y Option B (D-02).** Runtime fail-fast budget for the pod to become gateway-observably reachable (URLs probe, NOT the Vast `ports` map — a populated ports map with `actual_status=running` is the lie characterized in 6.6.Y-01 SPIKE-EVIDENCE). On expiry the reconciler destroys the pod with `closure_reason=public_port_bind_timeout`. Operator-tunable per D-02. | Yes |
| ~~deprecated primary aliases~~ | — | — | **REMOVED (Phase 6.6.Y-02).** The three deprecated primary alias vars (the old unprefixed num-gpus / gpu-name / dph-cap names) now HARD-FAIL gateway boot. Use the canonical `PRIMARY_VAST_*_PRIMARY` / `PRIMARY_VAST_*_FALLBACK` rows above. The unprefixed emerg-pod `VAST_PRICE_CAP_DPH` is a different subsystem and is retained. | — |
| `PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS` | int | `2400` (40 min) | Wave 0 Decision 6. Reconciler treats > 40min as provision failure → enter Cooldown. Generous margin for slow inet hosts + multi-stage image pull + aria2c weight download + supervisord init. | Yes |
| `PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS` | int | `300` (5 min) | Cooldown gate after a provision failure. Mirrors emerg's `PROVISION_FAILURE_COOLDOWN_SECONDS=60`, scaled-up for the schedule-cadence (primary should NOT thrash on schedule peaks if Vast/MinIO is degraded). Plan 06.6-03 revision. | Yes |
| `MONTHLY_PRIMARY_BUDGET_BRL` | float | `800.0` | Pitfall #12 — separate budget from emerg (~R$130/mo nominal, gives 5x headroom for soak phase). Audit cost separately as `event_kind=primary_lifecycle_close`. | Yes |
| `PRIMARY_POD_SCHEDULE_TIMEZONE` | IANA tz | `America/Sao_Paulo` | **Fail-fast** at boot if invalid (Pitfall #4 — silent UTC fallback would shift the window by 3+ hours). Brazil's tz is DST-immune (no DST since 2019). | Yes |
| `PRIMARY_POD_SCHEDULE_UP_HOUR` | int 0..23 | `8` | Peak-window start hour-of-day in `PRIMARY_POD_SCHEDULE_TIMEZONE`. | Yes |
| `PRIMARY_POD_SCHEDULE_DOWN_HOUR` | int 0..23 | `22` | Peak-window end hour-of-day. When `DownHour <= UpHour`, the window wraps across midnight (reviews #5 day-filter wrap-originator semantics). | Yes |
| `PRIMARY_POD_SCHEDULE_DAYS` | CSV | `mon,tue,wed,thu,fri` | 3-letter lowercase day-of-week abbreviations. Unknown tokens silently dropped. | Yes |
| `PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS` | int | `300` (5 min) | Drain timeout. Reconciler waits up to this for in-flight requests to complete before destroying the pod. | Yes |
| `PRIMARY_POD_SCHEDULE_DISABLED` | bool | `true` | **Wave 0 Decision 5 soak gate.** Operator manual-flips to `false` AFTER the Plan 06.6-11 Live UAT GREEN. While true, schedule ticks are short-circuited (`force-up`/`force-down` still work — reviews #2 — so the operator can drive manual cycles during UAT). | Yes |
| `PRIMARY_POD_SCHEDULE_PROVISION_LEAD_SECONDS` | int | `1800` (30 min) | **Reviews #8 pre-warm offset.** `ShouldBeProvisioned` returns true for this many seconds BEFORE `UpHour` so the pod is Ready by `UpHour` given a 25–30min cold-start. | Yes |

### Reused / shared env vars (6 fields)

| Env name | Default | Description |
|----------|---------|-------------|
| `VAST_AI_API_KEY` | `""` (empty disables Phase 6.6 cleanly, like Phase 6) | Vast.ai API bearer. Same key used by emerg + primary. |
| `USD_TO_BRL_RATE` | `5.0` | FX rate; operator updates quarterly. Same value used by emerg + primary cost calc. |
| `MINIO_ENDPOINT` | `https://s3.ifixtelecom.com.br` | MinIO base URL for weight pulls. |
| `MINIO_BUCKET` | `ai-gateway` | Bucket name for `mc share download`. |
| `MINIO_ACCESS_KEY` | (from secret store) | MinIO AK. |
| `MINIO_SECRET_KEY` | (from secret store) | MinIO SK. |
| `POD_DEBUG_SSH_PUBLIC_KEY` | `""` (empty in production) | Reused from Phase 6 D-12. Production-mode empty (least-privilege). |

> **Note** on `PRIMARY_*_IMAGE` (lines 2-4 above): these 3 env vars are
> **read at CI build time only** (consumed by `pod/primary/Dockerfile` FROM
> lines). They are **NOT** passed to the runtime pod env. The container
> already carries the extracted binaries at `/app/speaches-bin`,
> `/app/infinity-bin`, `/usr/bin/dcgm-exporter`. Changing them at the
> gateway env layer has NO runtime effect — the CI image must be rebuilt
> with the new digest.

---

## Schedule Behavior

The schedule is **env-var-driven** (NOT cron-file-driven, NOT hot-reloadable).
Changes to any `PRIMARY_POD_SCHEDULE_*` env var require a gateway container
restart to take effect.

### IsInPeak semantics

`ScheduleRule.IsInPeak(now)` reports whether `now` falls inside the active
window:

- **Disabled kill-switch** (`Disabled=true`) → always `false`.
- **Simple intra-day window** (`UpHour < DownHour`, e.g. UP=8 DOWN=22): require
  `Days[weekday]` AND `UpHour <= hour < DownHour`.
- **Overnight wrap** (`UpHour >= DownHour`, e.g. UP=22 DOWN=8):
  - Case A — `now` in `[UpHour, 24)`: use TODAY's day-bit (today is the
    wrap originator).
  - Case B — `now` in `[0, DownHour)`: use YESTERDAY's day-bit (yesterday is
    the wrap originator — **reviews #5 day-filter fix**).

The reviews #5 fix is load-bearing: without it, `Days={mon: true}` with
`UP=22 DOWN=8` would silently treat Tuesday 02:00 as NOT in peak (consulting
Tuesday's bit), even though the Monday 22:00 → Tuesday 08:00 wrap window IS
active because Monday's bit IS enabled.

### Pre-warm offset (ShouldBeProvisioned)

`ScheduleRule.ShouldBeProvisioned(now)` is the trigger consumed by
`reconciler.evaluateAsleep`:

- Returns `true` throughout the active peak window (`IsInPeak`).
- Returns `true` for `ProvisionLeadS` seconds (default 1800 = 30min)
  **BEFORE** the next `up` transition.

This is the **reviews #8 pre-warm offset** — separates "should be Ready" from
"should be Provisioned". With a 25–30min cold-start budget, the reconciler
must start provisioning ~30min before `UpHour` so the pod is Ready by `UpHour`.

### DST immunity

Brazil dropped DST in 2019; `America/Sao_Paulo` is UTC-3 year-round. The
schedule arithmetic uses `time.LoadLocation(cfg.PrimaryPodScheduleTimezone)`
and is DST-aware via stdlib `time` — Northern-Hemisphere tz operators get
the safety for free.

### Kill-switch (operator soak gate)

`PRIMARY_POD_SCHEDULE_DISABLED=true` (default per Wave 0 Decision 5)
short-circuits `IsInPeak` AND `ShouldBeProvisioned` to `false` regardless of
clock. **Operator manual-flips to `false` AFTER** the Plan 06.6-11 Live UAT
is GREEN.

While `DISABLED=true`, **`gatewayctl primary force-up` / `force-down` STILL
WORK** (per reviews #2: `Start()` runs unconditionally; the gate only
short-circuits schedule ticks). This lets the operator drive manual cycles
during UAT without flipping the kill-switch.

### Hot-reload NOT supported

Editing `PRIMARY_POD_SCHEDULE_*` env vars in Portainer + clicking "Update the
stack" recreates the gateway container, which re-reads the env at boot. This
is by design — `ScheduleRule` is immutable post-`ParseScheduleEnv`, so a
re-parse at runtime would require explicit hot-reload plumbing that we
intentionally avoided (operator restart is cheap; bug surface from race
conditions is not).

### FSM transition surface — what the operator can grep

When the FSM transitions state (e.g. Ready→Draining at schedule window
exit), the reconciler emits 3 observable artifacts:

1. **`gw:primary:events` Redis Pub/Sub channel** — a `PrimaryEvent` is
   PUBLISHED with `Type="transition"` and `Reason=<reason>`. Subscribe via
   `docker exec infra-redis-1 redis-cli SUBSCRIBE gw:primary:events`.
   Also a typed event like `Type="draining_started"` fires from `startDrain`.

2. **`primary_lifecycles` DB row events JSONB column** — every transition
   appends `{ts, kind, reason, ...}` to the row's `events` JSONB array.
   Query via `gatewayctl primary lifecycles --since 1h --format=json | jq '.[0].Events'`.

3. **`gw:primary:state` Redis Hash mirror** — the latest state is mirrored
   for cross-replica visibility. Query via `gatewayctl primary state`.

**Key transition reasons to grep for:**

| FSM transition | Reason string | Triggered by |
|----------------|---------------|--------------|
| Asleep → Provisioning | `schedule_pre_warm` OR `manual_force_up:<operator-reason>` | `evaluateAsleep` finds `ShouldBeProvisioned` true OR `handleForceUpRequest` |
| Provisioning → Ready | `all_probes_passed` | `markReady` after 4-service health probes green |
| Ready → Draining | `schedule_window_exited` OR `operator_force_down:...` OR `pod_unhealthy` | `evaluateReady` finds `IsInPeak` false OR `handleForceDownRequest` |
| Draining → Destroying | `drain_complete` | `evaluateDraining` finds inflight==0 OR grace elapsed |
| Destroying → Asleep | `destroy_complete` | `evaluateDestroying` after vast.DestroyInstance success |

Container log emission: the reconciler also emits structured `slog` lines
like `"primary drain complete; transitioning to Destroying" inflight=N
elapsed_seconds=N grace_seconds=N` (Info level) on `Draining→Destroying`,
and `"primary force-down: initiating drain by operator request"` (Info level)
on operator force-down. These are useful for the [Plan 06.6-11 Live UAT
Scenario 5 drain assertions](#troubleshooting).

---

## Operator CLI — `gatewayctl primary`

Five FUNCTIONAL subcommands (Plan 06.6-09 deliverable). Reviews #3 closure:
`force-up` / `force-down` publish-side wired to `handleForceUpRequest` /
`handleForceDownRequest` consume-side in Plan 06.6-06a.

### `gatewayctl primary state [--format=table|json]`

Read-only snapshot of `gw:primary:state` Hash mirrored from the leader's FSM.

```
$ docker exec ai-gateway-dev_gateway /gatewayctl primary state
KEY              VALUE
state            ready
lifecycle_id     7
pod_url          http://155.93.41.22:42813
pod_instance_id  36912004
entered_at       1747400123

$ docker exec ai-gateway-dev_gateway /gatewayctl primary state --format=json
{
  "state": "ready",
  "lifecycle_id": "7",
  "pod_url": "http://155.93.41.22:42813",
  "pod_instance_id": "36912004",
  "entered_at": "1747400123"
}
```

Empty hash (`{}` JSON, `"(no state mirrored — reconciler may be in ASLEEP)"`
table) is normal at boot — the FSM only mirrors on the FIRST transition.

### `gatewayctl primary force-up [--reason "<text>"]`

PUBLISH a typed `PrimaryEvent{Type:"force_up_request"}` on `gw:primary:events`.
The reconciler subscriber consumes leader-only and drives the FSM
`Asleep → Provisioning` with `Reason="manual_force_up:<reason>"`.

```
$ docker exec ai-gateway-dev_gateway /gatewayctl primary force-up --reason "uat-day1"
force-up request published; reconciler tick (~1s) consumes event and starts provisioning.
Run `gatewayctl primary state` to confirm the FSM transition.
```

Works even with `PRIMARY_POD_SCHEDULE_DISABLED=true` (reviews #2 — `Start()`
runs unconditionally).

### `gatewayctl primary force-down [--reason "<text>"]`

PUBLISH `PrimaryEvent{Type:"force_down_request"}`. The reconciler:

- From `StateProvisioning`: cancels the in-flight provisioning lifecycle.
- From `StateReady`: initiates `startDrain(reason="operator_force_down:...")`.
- From `StateDraining` / `StateDestroying`: noop (logs Info).

```
$ docker exec ai-gateway-dev_gateway /gatewayctl primary force-down --reason "uat-rollback"
force-down request published; reconciler leader consumes event and initiates drain or cancellation.
Run `gatewayctl primary state` to confirm the FSM transition.
```

### `gatewayctl primary schedule`

**Pure-function pre-flight** — NO Redis/Postgres round-trip. Loads config from
env, parses schedule rule, prints every resolved field INCLUDING
`ProvisionLeadSeconds` (reviews #8 surfacing for operator verify against the
25–30min cold-start reality).

```
$ docker exec ai-gateway-dev_gateway /gatewayctl primary schedule
Timezone:                  America/Sao_Paulo
UpHour:                    8
DownHour:                  22
Days:                      [mon tue wed thu fri]
GraceRampDownSeconds:      300
ProvisionLeadSeconds:      1800             # kicks off provisioning 1800 seconds before UpHour (pre-warm offset)
Disabled:                  true
Next transition:           2026-05-19T08:00:00-03:00 (up)
Should be provisioned now: false
```

Returns exit 1 on `ParseScheduleEnv` error (typically invalid timezone —
Pitfall #4 fail-fast applies).

### `gatewayctl primary lifecycles [--since 7d] [--limit 20] [--format=table|json]`

Query `ai_gateway.primary_lifecycles` for recent rows. Default `--limit` is
20 (vs emerg's 50) — primary lifecycles are at most 1/day under normal
operation. `--since` accepts `Nd` / `Nh` / `Nm` / standard Go durations.

The tabwriter includes a **DRAIN** column (Phase 6.6-new vs emerg) between
STARTED and ENDED, so the operator sees the 3-phase lifecycle
(started → drain → ended) at-a-glance.

```
$ docker exec ai-gateway-dev_gateway /gatewayctl primary lifecycles --since 7d
ID  STARTED               DRAIN                 ENDED                 TRIGGER         VAST_OFFER  VAST_INST  DPH     COST_BRL  SHUTDOWN              REPLICA
7   2026-05-18T08:00:00Z  2026-05-18T22:00:00Z  2026-05-18T22:05:00Z  schedule_up     45112233    36912004   0.3850  26.7345   schedule_drain        ai-gateway-dev-1
6   2026-05-17T07:30:00Z  -                     2026-05-17T07:32:14Z  manual_force_up -           -          -       -         cancelled_in_flight   ai-gateway-dev-1
```

Cross-reference: `gatewayctl emerg force-provision` / `gatewayctl emerg
force-destroy` (Phase 6 deliverable, preserved unchanged) operate on the
separate `emerg` namespace.

---

## Deployment Checklist

### Pre-deploy

1. **CI green** — `build-primary-pod.yml` workflow on `develop` succeeded;
   image `ghcr.io/ifixtelecom/converseai-primary-pod:dev-{sha}` published.
   Verify with `gh run list --workflow=build-primary-pod.yml --limit=1`.
2. **Migrations** — `gateway/db/migrations/0023_primary_lifecycles.sql`
   committed and present in the gateway image. Boot runs it via
   `AI_GATEWAY_MIGRATE_ON_BOOT=true`.
3. **MinIO weights uploaded + sha256 captured** — `pod/scripts/upload-weights.sh`
   pushed Qwen3.6 GGUF, Whisper tarball, BGE-M3 tarball. Each `sha256sum`
   captured for the `PRIMARY_*_WEIGHTS_SHA256` env vars (Whisper + BGE-M3
   are FAIL-FAST — empty value rejects buildCreateRequest).
4. **Vast.ai account funded** — ≥ R$200 (~$40) free balance for the soak +
   first scheduled month.

### Deploy via Portainer

1. Open Portainer: <https://portainer3.ifixtelecom.com.br>.
2. Stacks → `ai-gateway-dev` (or `ai-gateway-prod`) → Editor.
3. Add/update the **Phase 6.6 env vars** (24 `PRIMARY_*` + reuse) in the
   stack environment block. See [Environment Variables](#environment-variables).
   - **Soak gate posture (Wave 0 Decision 5):** set
     `PRIMARY_POD_SCHEDULE_DISABLED=true` initially.
   - **FAIL-FAST values:** `PRIMARY_WHISPER_WEIGHTS_SHA256` +
     `PRIMARY_BGEM3_WEIGHTS_SHA256` MUST be populated (empty rejects
     buildCreateRequest).
   - **SHA-pinned image:** `PRIMARY_TEMPLATE_IMAGE` defaults to the b9191
     pin; you can override to `ghcr.io/ifixtelecom/converseai-primary-pod:dev-{sha}`
     if the multi-stage image is the desired runtime (recommended for the
     production stack — direct upstream b9191 is the fallback path).
4. Hit **Update the stack** → webhook → Portainer recreates the gateway
   container with the new env.
5. Watch container creation:
   `ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev'`.

### Post-deploy

- [ ] **Container running:**
      `ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev_gateway --format "{{.Status}}"'`
      shows `Up N seconds (healthy)`.
- [ ] **Primary reconciler started:**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep "primary schedule loop started"'`
      shows the boot line (or `"primary schedule loop: schedule disabled"` if
      `PRIMARY_POD_SCHEDULE_DISABLED=true` — expected at soak gate).
- [ ] **Migration 0023 applied:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "\d ai_gateway.primary_lifecycles"'`
      shows the table with `drain_started_at` column + 4 indexes (partial
      unique `primary_live_singleton` + 3 standard).
- [ ] **FSM at ASLEEP:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl primary state --format=json'`
      returns `{}` (empty mirror at boot — ASLEEP is initial; FSM only
      mirrors on first transition).
- [ ] **Schedule pre-flight sane:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl primary schedule'`
      shows the resolved schedule with `ProvisionLeadSeconds: 1800` + `Next
      transition` + `Should be provisioned now: false` (kill-switch on).
- [ ] **Sentry only emits on transitions:** at idle no `subsystem:primary`
      events should appear in Sentry.

### Soak gate exit sequence (Wave 0 Decision 5)

1. Stack deployed with `PRIMARY_POD_SCHEDULE_DISABLED=true` (initial).
2. Execute [Plan 06.6-11 Live UAT](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-HUMAN-UAT.md)
   — operator manually drives `force-up`/`force-down` cycles + validates
   4-service health + tool-calling + STT + embed + drain + Pitfall #11.
3. After all 6 UAT scenarios PASS:
   - Update Portainer stack env: `PRIMARY_POD_SCHEDULE_DISABLED=false`.
   - Hit "Update the stack" → restart gateway → schedule ticks begin firing.
4. Monitor Sentry for 24h post-flip for unexpected `subsystem:primary` events.

---

## Troubleshooting

### Cold-start gap (~25-30min real, 40min budget)

**Symptom:** `gatewayctl primary state` shows `state=provisioning` for
25-30min after `force-up` or schedule pre-warm.

**Expected behavior — NOT a bug.** Cold-start budget is 2400s = 40min per
Wave 0 Decision 6. Breakdown:

- Image pull (uncached host worst-case): ~3min
- Aria2c download Qwen3.6 16GB GGUF: ~3-5min on Iceland (3.5 Gbps inet) hosts,
  budget more for slower hosts
- Speaches Whisper-Large-v3 + Infinity BGE-M3 model download (~5GB total):
  ~2-3min in parallel with Qwen
- llama-server model load (Qwen3.6 27B → VRAM): ~30-60s
- Supervisord process spawn + health checks (4 services): ~30s
- Buffer: 5min

**Total realistic: 15-25min. 40min budget = generous margin.**

This is the **SC-2 gap inherited from Phase 6** ([06-06-SUMMARY.md SC-2
known limitation](../../.planning/phases/06-emergency-pod-template-refactor/06-06-SUMMARY.md)).
Architectural follow-ups deferred per [CONTEXT.md Deferred Ideas](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-CONTEXT.md):

- Vast.ai persistent volumes + host pin (mitigate weight re-download every
  lifecycle) — same gap Phase 6 enfrentou.
- Pre-warm schedule offset already wired (`PROVISION_LEAD_SECONDS=1800` per
  reviews #8) — when schedule mode is active, this gap is invisible because
  provisioning starts 30min before `UpHour`.

**Operator action:** none if the elapsed time is < 40min. If > 40min, the
reconciler will transition to Cooldown with `shutdown_reason='provision_failure'`
— investigate via the *Schedule won't fire / provisioning timeout* sub-section.

### Schedule won't fire

**Symptom:** `gatewayctl primary state` stays at `(no state mirrored …
ASLEEP)` past `UpHour - ProvisionLeadSeconds` and no `force_up_request`
event was published.

**Diagnosis:**

```bash
# 1. Is the kill-switch flipped on?
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl primary schedule'
# Look for "Disabled: true" — soak gate is on.

# 2. Is the timezone parseable?
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 10m 2>&1 | grep "primary schedule parse failed"'
# Pitfall #4 — bad PRIMARY_POD_SCHEDULE_TIMEZONE value crashes the gateway at boot.

# 3. Is the reconciler the cluster leader?
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 10m 2>&1 | grep "acquired primary leadership"'
# Single-replica should always see this within ~30s of boot. If absent, the
# redsync lock is contended (multi-replica race), or Redis is unreachable.

# 4. Did a recent provision failure trip the cooldown gate?
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl primary lifecycles --since 6h --format=json | jq ".[] | select(.ShutdownReason==\"provision_failure\")"'
# If yes, PrimaryProvisionFailureCooldownSeconds (default 300s) is gating new attempts.

# 5. Should the reconciler be provisioning right now?
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl primary schedule'
# Look for "Should be provisioned now: false" with a "Next transition: …" line
# in the past — that's a logic bug.
```

**Action:**

- **Kill-switch on:** flip `PRIMARY_POD_SCHEDULE_DISABLED=false` via Portainer
  + restart gateway (post-UAT soak gate exit per Wave 0 Decision 5).
- **TZ parse failed:** check env value matches a valid IANA tz name
  (`America/Sao_Paulo`, not `BRT`). Restart gateway.
- **Leadership not acquired:** check `infra-redis-1` health (`docker logs
  infra-redis-1 --tail 100`). If single-replica, restart the gateway
  container to force fresh leader election.
- **Cooldown gate active:** wait `PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS`
  (default 300s) for the gate to release, OR force-flip via
  `gatewayctl primary force-up --reason "manual_clear_cooldown"` (the force
  path bypasses the gate — see `handleForceUpRequest`).

### Drain hangs

**Symptom:** `gatewayctl primary state` shows `state=draining` for longer
than `PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS + buffer` (default 5min
+ 60s).

**Expected:** the reconciler `evaluateDraining` should advance to
`StateDestroying` when EITHER inflight==0 OR `elapsed >= grace`. So
drain-stuck is a real bug.

**Diagnosis:**

```bash
# Reconciler log: drain complete should fire at most grace_seconds after drain start
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 15m 2>&1 | grep -E "primary drain|primary startDrain|drain_complete"'

# Inflight counts per role — should drop to 0 over the grace window
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics | grep shed_inflight'
```

**Action:**

- If inflight > 0 after `grace_seconds`: investigate why requests are still
  in-flight — likely an external client holding a long stream. Force destroy
  via `gatewayctl primary force-down --reason "manual_drain_force"`
  (the second force-down from `StateDraining` is a noop — instead use the
  emerg-style escalation: stop the stream client OR raise grace temporarily).
- If inflight == 0 but reconciler not advancing: the leader lock may have
  churned (`evaluateDraining` runs only on the leader). Check leadership
  logs.

### Emergency precedence (Pitfall #11)

**Symptom:** at peak hour, both emerg and primary pods are running on Vast.ai
(check the Vast UI). Cost monitoring shows 2 simultaneous bills.

**Expected:** when primary marks Ready, the emerg subscriber fires
`force-destroy` automatically (Pitfall #11 Option B per RESEARCH.md). Both
running for >30s after primary Ready is a real bug.

**Diagnosis:**

```bash
# 1. Both reconcilers state
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json'
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl primary state --format=json'

# 2. Look for the primary_ready event consumption in emerg subscriber
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 30m 2>&1 | grep -E "emerg.*primary_ready|emerg.*force-destroy.*pitfall_11"'

# 3. Vast UI confirmation
# https://cloud.vast.ai/instances/  — expect ONLY 1 instance (primary)
```

**Action:**

- If emerg pod still active after primary Ready + 30s: manually run
  `gatewayctl emerg force-destroy`. File an issue against the emerg `gw:primary:events`
  subscriber wiring (Plan 06.6-06b cross-package adapter).
- Document the cost waste in the next-day cost review.

### Supervisord child crash diagnostics (Wave 0 NEW)

**Symptom:** Vast.ai dashboard shows `actual_status=running` but one or more
gateway probes fail (e.g. `/v1/models` returns 200 but `/v1/embeddings`
times out — Infinity child crashed). Gateway breaker FSM eventually OPENS
on the affected role; fallback chain redirects.

**Diagnosis (when `POD_DEBUG_SSH_PUBLIC_KEY` is set, dev only):**

```bash
# SSH into the pod (the onstart bash sets up sshd if POD_DEBUG_SSH_PUBLIC_KEY is non-empty)
INST=$(ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -t -c "SELECT vast_instance_id FROM ai_gateway.primary_lifecycles WHERE ended_at IS NULL;" | xargs')
POD_IP=$(vastai show instance $INST | jq -r '.public_ipaddr')

# Per-child logs (supervisord baked these paths into supervisord.conf)
ssh root@$POD_IP 'tail -200 /var/log/supervisord.log'
ssh root@$POD_IP 'tail -200 /var/log/llama.log'
ssh root@$POD_IP 'tail -200 /var/log/speaches.log'
ssh root@$POD_IP 'tail -200 /var/log/infinity.log'
ssh root@$POD_IP 'tail -200 /var/log/dcgm.log'

# supervisord process state — should show 4 RUNNING entries (llama / speaches / infinity / dcgm)
ssh root@$POD_IP 'supervisorctl status'
```

**Diagnosis (production mode, `POD_DEBUG_SSH_PUBLIC_KEY=""`):**

SSH is intentionally unavailable in production (least-privilege per D-12).
Forensics path:

- `vastai logs <instance_id>` — captures supervisord stdout/stderr (the
  union of all 4 children's stdout since they `redirect_stderr=true`).
- `vastai show instance <instance_id>` — status + image manifest + port
  mappings.
- Gateway-side: `/v1/health/upstreams` admin endpoint shows per-role
  breaker state + last probe RTT + last error message.

**Action:**

- **supervisord crashed child but autorestart**: supervisord will autorestart
  the child (`autorestart=true` in supervisord.conf). The child's log file
  is preserved in `/var/log/<child>.log` for forensics. The gateway breaker
  FSM may briefly OPEN until the child re-stabilizes — fallback chain
  handles the gap.
- **GPU silently fallback to CPU**: real risk per CONTEXT.md D-06.2. Gateway
  Phase 3 probe loop P95 latency breaker FSM detects this (CPU inference is
  ~50x slower) and OPENs the breaker → fallback chain redirects. No
  onstart instrumentation; acceptable 5-10s detection delay.
- **Recurrent crash**: investigate root cause in the child's log. Common
  causes: VRAM exhaustion (Pitfall #13 — 4-service VRAM budget is tight on
  4090, ~24GB total; KV growth under burst can OOM llama-server), MinIO
  weight download corruption (caught by `sha256sum -c` in onstart, but a
  late-failing read could still corrupt the tar extract). Force-destroy +
  re-provision usually clears transient issues.

### Pod-level GPU silently fallback to CPU

**Symptom:** `dcgm-exporter` `:9400/metrics` shows `DCGM_FI_DEV_GPU_UTIL{...}=0`
while gateway logs show llama-server inferencing successfully. Means: the
service is running on CPU, not GPU. Real risk per CONTEXT.md D-06.2.

**Action:** the gateway Phase 3 P95 latency breaker FSM will catch this
within 5-10s (CPU inference is ~50x slower → P95 exceeds threshold →
breaker OPENs) and the fallback chain redirects. No manual intervention
needed for the user-facing path. For root-cause, SSH into the pod (dev)
and check `nvidia-smi` + `/var/log/llama.log` for CUDA init errors.

### Cooldown gate from prior failure

**Symptom:** `gatewayctl primary lifecycles --since 6h --format=json` shows
a recent row with `shutdown_reason='provision_failure'` and the reconciler
won't re-attempt within the cooldown window.

**Expected:** `PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS` (default 300s)
gates re-attempts after a provision failure. This prevents thrashing on a
persistent Vast/MinIO/network issue.

**Action:**

- Wait the cooldown window OR force-clear via `gatewayctl primary force-up`
  (the force path is gate-bypassing per `handleForceUpRequest`).

---

## Shape pair model

The primary reconciler searches Vast in two shape passes (Phase 6.6.Y canonical migration):

- **Shape 0 — PRIMARY (searched first):** `PRIMARY_VAST_NUM_GPUS_PRIMARY=1`,
  `PRIMARY_VAST_GPU_NAME_PRIMARY=RTX 3090`, `PRIMARY_VAST_PRICE_CAP_PRIMARY=0.30`.
  A single 1×RTX 3090 instance is the cheapest viable shape and is tried first.
- **Shape 1 — FALLBACK (the standing config):** `PRIMARY_VAST_NUM_GPUS_FALLBACK=2`,
  `PRIMARY_VAST_GPU_NAME_FALLBACK=RTX 3090`, `PRIMARY_VAST_PRICE_CAP_FALLBACK=0.60`.
  The validated 2×RTX 3090 single-pod shape (allowlist hosts 43803/55158). Searched
  when the shape-0 pass returns zero qualified offers.

> The standing 2×3090 config has always been the FALLBACK shape (shape 1), not the
> primary — set the canonical `PRIMARY_VAST_*_FALLBACK` per-shape names. The deprecated
> primary aliases now **HARD-FAIL gateway boot** (plan 6.6.Y-02); see the removal note
> in the env table above for the exact names.

**Cold-start closure:** when a provisioned pod never becomes gateway-observably reachable
within `PRIMARY_PUBLIC_PORT_BIND_BUDGET_SECONDS` (default 120s), the reconciler destroys
it with `closure_reason=public_port_bind_timeout`. The Vast `ports` map is NOT a readiness
signal (6.6.Y-01 SPIKE-EVIDENCE: `actual_status=running` + populated `ports` while the host
stayed externally unreachable across n=2 sampled hosts); readiness gates on the URLs probe.

## Cost Budget Monitoring

- **DPH caps (per-shape, Phase 6.6.Y canonical):** shape-0 PRIMARY
  `PRIMARY_VAST_PRICE_CAP_PRIMARY=0.30` (1×RTX 3090 ≈ R$1.5/hour at
  `USD_TO_BRL_RATE=5.0`); shape-1 FALLBACK `PRIMARY_VAST_PRICE_CAP_FALLBACK=0.60`
  (2×RTX 3090 ≈ R$3/hour). Epsilon-comparison `cap+0.0001` per Pitfall 5.
- **Monthly budget:** `MONTHLY_PRIMARY_BUDGET_BRL=800` (default).
  Nominal usage `14h × 22 weekdays × $0.60 × 5 BRL/USD ≈ R$924/month` at the
  fallback cap; the shape-0 1×3090 path runs roughly half that.
- **Cost calculation:** `total_cost_brl = accepted_dph × hours_active × USD_TO_BRL_RATE`
  where `hours_active = (ended_at - first_health_pass_at) / 3600`
  (cold-start time is EXCLUDED from the audit — D-D4 parity with emerg).
- **Audit:** `audit_log` records `event_kind=primary_lifecycle_close` for
  every completed primary lifecycle (parity with emerg's
  `emergency_lifecycle_close`). Pitfall #12 separation: primary cost is
  tracked + budgeted independently of emergency cost.

### Sentry alert dedupe

Budget alerts fire on `MONTHLY_PRIMARY_BUDGET_BRL` exceedance with tags
`subsystem:primary`, `alert:budget_exceeded`, level=Warning. **Dedupe gate
24h** — exactly 1 alert per UTC day across the cluster (parity with emerg's
Pitfall 11 dedupe). The alert is **non-blocking** (D-D2 parity): provisioning
continues. To hard-stop: set `MONTHLY_PRIMARY_BUDGET_BRL=0` AND
`PRIMARY_POD_SCHEDULE_DISABLED=true` AND restart the gateway.

### Querying monthly cost

```bash
# Via gatewayctl
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl primary lifecycles --since 30d --format=json' \
  | jq '[.[] | .TotalCostBrl // 0] | add'

# Or directly via SQL (matches reconciler's checkBudget aggregate)
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
SELECT COALESCE(SUM(total_cost_brl), 0) AS month_cost_brl
  FROM ai_gateway.primary_lifecycles
 WHERE started_at >= date_trunc('"'"'month'"'"', NOW())
   AND ended_at IS NOT NULL;
"'
```

### Quarterly USD_TO_BRL_RATE update

Shared with emerg — see [RUNBOOK-EMERGENCY-POD.md → Quarterly
USD_TO_BRL_RATE update](./RUNBOOK-EMERGENCY-POD.md#quarterly-usd_to_brl_rate-update).

---

## Pitfall #11 — Emergency/Primary Coexistence

Phase 6 (emergency) and Phase 6.6 (primary) BOTH provision Vast.ai pods.
Without coordination, a double-spawn at peak transition is possible: emerg
provisioned a pod at 07:55 because primary was Asleep AND breaker OPEN, and
primary then provisions at 08:00 — double cost for ~20-30min cold-start
gap.

**RESEARCH.md Pitfall #11 Option B is the LOCKED solution:**

- Primary takes over (it's the intended source).
- emerg subscribes to a new `gw:primary:events` channel.
- On `type=primary_ready` event, emerg `force-destroy`s if its FSM is in
  `EmergencyActive`. Force-destroy completes within ~30s.

Implementation:

- `gateway/internal/primary/reconciler.go markReady()` PUBLISHes
  `PrimaryEvent{Type:"primary_ready", LifecycleID: ...}` on `gw:primary:events`.
- `gateway/internal/emerg/reconciler.go` adds a subscriber to
  `gw:primary:events` (separate from `gw:emerg:events`). On `Type=primary_ready`,
  if `emerg.FSM.State()==EmergencyActive`, fires force-destroy with
  `shutdown_reason='pitfall_11_primary_took_over'`.

The Live UAT [Scenario 6](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-HUMAN-UAT.md)
explicitly tests this coexistence path.

---

## Security Notes

- **`POD_DEBUG_SSH_PUBLIC_KEY` MUST be empty in production.** Setting it
  injects an SSH key into the pod, enabling operator interactive debug
  via `ssh root@<pod-ip>`. This is **never** appropriate for production
  traffic (LGPD considerations). Operator must NEVER commit a `.env`
  file with this key populated.
- **supervisord co-located children share the container's namespace.** No
  nested DinD escape surface (that was rejected per Spike Task 1). T-06.6-05
  REMOVED from the Phase 6.6 threat register.
- **Custom image SHA pin enforcement (T-06.6-SC mitigation).** The 4 upstream
  SHA-pinned FROM digests are captured in `WAVE0-GATES.md` Decision 1 +
  enforced by `pod/primary/Dockerfile` literal FROM lines. Image bumps
  require WAVE0-GATES amendment + operator sign-off (audit trail).
- **MinIO credentials in pod env.** `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY`
  are passed to the pod env so onstart can `mc alias set ifix …`. Reviews #7
  shell hardening: `set -euo pipefail` (NO `-x`/`-euxo` — xtrace would leak
  the AK/SK into the pod log). All `$MINIO_*` expansions are double-quoted
  to prevent argument injection.
- **`PRIMARY_*_WEIGHTS_SHA256` is public-grade integrity.** Logging these
  values is safe — they are the published hash of MinIO objects, not
  secrets.

---

## Phase 6.6 References

- [`06.6-CONTEXT.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-CONTEXT.md) — D-01 through D-12 decisions; Strategy B-revised baseline.
- [`06.6-RESEARCH.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-RESEARCH.md) — 13 Pitfalls + decisions resolved (Item 1..N) + req→test map.
- [`06.6-PATTERNS.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-PATTERNS.md) — code patterns (raw-string Go onstart, sha256 verify reuse, supervisord supervisor-of-trees idiom).
- [`06.6-WAVE0-GATES.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-WAVE0-GATES.md) — operator-locked decisions: 4 image @sha256 + supervisord LOCKED + B1 embedded Jinja + 40GB disk + soak gate + 40min cold-start budget.
- [`06.6-SPIKE-dind-privileged.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-SPIKE-dind-privileged.md) — DinD FAIL evidence (overlayfs mount permission denied) + supervisord re-eval.
- [`06.6-SPIKE-qwen3.6-jinja.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-SPIKE-qwen3.6-jinja.md) — Qwen3.6 + b9191 PASS evidence (chat_format peg-native, tool_calls structured correct).
- [`06.6-VALIDATION.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-VALIDATION.md) — Wave 0..5 verification gates.
- [`06.6-HUMAN-UAT.md`](../../.planning/phases/06.6-primary-pod-refactor-strategy-b-full-stack-upstream-images-i/06.6-HUMAN-UAT.md) — Live Vast.ai UAT script (Plan 06.6-11 Task 2 deliverable).
- Sibling: [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md), [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md).
- Vast.ai API docs: <https://docs.vast.ai/> (canonical host
  `https://console.vast.ai/api/v0`).
- supervisord docs: <http://supervisord.org/>.

---

*Last updated: 2026-05-17 (Phase 6.6 plan 06.6-11; revised per checker + Wave 0 supervisord lock 2026-05-17)*
