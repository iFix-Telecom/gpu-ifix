# Phase 14: vram-adaptive-stt — Research

**Researched:** 2026-06-17
**Domain:** Vast.ai GPU pod runtime (supervisord + speaches/faster-whisper) + Go gateway primary-pod reconciler
**Confidence:** HIGH (all 5 gaps resolved against live code + official docs; 4 OPEN QUESTIONS flagged for planner)

## Summary

The phase removes the manual `PRIMARY_POD_SERVE_STT` flag and makes the gateway decide STT-from-pod automatically from a pod-reported `whisper_device` field. The SEED-019-IMPL-SURFACES map is largely correct on the GATEWAY side (Part 3) but **materially wrong on the POD side (Part 2)**: it assumed whisper starts via `pod/onstart.sh` + `docker-compose.yml` consuming an `--env-file`. **That is NOT the production runtime path.** [VERIFIED: read gateway/internal/primary/onstart.go + lifecycle.go:375-449]

The real PROD pod boot path is: gateway `buildCreateRequest` (lifecycle.go:348) → Vast `runtype=args` with `Onstart="/bin/bash"`, `Args=["-c", <primaryOnstartHead+exec supervisord>]` → the bash script (onstart.go:`primaryOnstartHead`) downloads weights then `exec /usr/bin/supervisord -n -c /etc/supervisor/conf.d/services.conf` → supervisord spawns `[program:speaches]` from the **build-baked** `pod/primary/supervisord.conf`. The `pod/onstart.sh` + `pod/docker-compose.yml` files are a SEPARATE (local-dev/legacy) path that the gateway never invokes. **The plan must touch onstart.go's `primaryOnstartHead`, NOT `pod/onstart.sh`.**

**Primary recommendation:** Plumb the VRAM→device decision through `primaryOnstartHead` (gateway-built bash): add an `nvidia-smi` VRAM probe that `export`s `WHISPER__INFERENCE_DEVICE` + `WHISPER__DEVICE_INDEX` + a `WHISPER_DEVICE` summary var BEFORE `exec supervisord`. Remove the hardcoded `WHISPER__INFERENCE_DEVICE="cpu"` from the conf's `environment=` line so the inherited env wins (keep `HF_HUB_CACHE`). Source `whisper_device` into the health-bridge from a boot env var, expose it on `aggregateResponse`, and (because :9100 is NOT Vast-port-forwarded) add `-p 9100:9100` so the gateway can read it at Ready. Gate the 3 reconciler override sites on the reported device instead of the deleted config flag.

## User Constraints (from CONTEXT.md)

### Locked Decisions
- **Signal channel: pod self-reports device.** Pod reports `whisper_device` (`cuda` | `cpu`, or `stt_on_gpu` bool); gateway routes on that. Shape-agnostic, no VRAM math in the gateway. Rejected: shape-derivation, latency-probing. **Do NOT revisit.**
- **Part 2 (pod):** `nvidia-smi --query-gpu=memory.total` VRAM detect BEFORE compose/supervisord. Device decision plumbed as a CONTAINER ENV via the existing env mechanism. Remove the build-baked `supervisord.conf:46` CPU pin (keep `HF_HUB_CACHE`). Threshold VRAM ≥ ~30GB → whisper on GPU; <30GB → NOT on GPU. **A 24GB pod MUST NOT serve slow-CPU whisper the gateway then routes to.** On GPU shapes pin `device_index` to the GPU with headroom (NOT Qwen's). Pod surfaces `whisper_device` in health-bridge `aggregateResponse` on :9100. Speaches' own `:8001/health` CANNOT carry the field.
- **Part 3 (gateway):** probe health-bridge `:9100/health/ready` at pod-Ready to read `whisper_device`; store on `primaryPodURLs`. Swap the 3 reconciler override sites (`:448`/`:875`/`:1631`) off `r.cfg.PrimaryPodServeSTT` to a per-lifecycle "serves whisper on GPU" bool. Clear paths (`startDrain`/`closeLifecycle`) already call `RestoreTier0("stt")` unconditionally — no change. DELETE `Config.PrimaryPodServeSTT` (config.go:252 + :517). Remove `PRIMARY_POD_SERVE_STT` from prod `.env`. Rework tests `reconciler_test.go:642-717` + `lifecycle_test.go:52`.
- **Migration/rollback safety:** MISSING/unknown `whisper_device` (old pod image) → treat as "do NOT override STT → use gemini-stt" (fail-safe to cheap-fast tier-1, never slow CPU pod). Preserves today's prod behavior during the rollout window.

### Claude's Discretion (per CONTEXT.md)
- Final VRAM threshold value (~30GB) and the **disabled-vs-CPU** choice for <30GB shapes.

### Deferred Ideas (OUT OF SCOPE)
- SEED-019 part 1 (N-shape cascade 2×3090→5090→3090) — separate phase.
- Cold-start fail-fast / provisioning hang (Phase 6.6.Y/12) — separate.
- Any change to the tier-1 STT cascade (gemini-stt → groq-whisper → openai-whisper).

## Phase Requirements

No formal requirement IDs exist for this phase in `.planning/REQUIREMENTS.md` — SEED-019 parts 2+3 are not enumerated there. [VERIFIED: grep of REQUIREMENTS.md returned no STT-device/whisper-device/SERVE_STT requirement rows.] The phase is driven by SEED-019 + CONTEXT.md decisions. Adjacent existing requirements the work touches:

| ID | Description | Research Support |
|----|-------------|------------------|
| POD-03 | Health-bridge exposes `/health` per model (LLM, STT, embed) with real latency test | `aggregateResponse` (handlers.go:58-79) is the surface that gains `whisper_device` |
| POD-04 | Pod measures/exposes VRAM via dcgm-exporter :9400 | onstart adds a one-shot `nvidia-smi` VRAM read (distinct from dcgm continuous metrics) |
| RES-03 | Fallback chain: local-STT → (cascade) | The device-gate decides whether tier-0 local-STT is even advertised |

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| VRAM detection (`nvidia-smi`) | Pod onstart (gateway-built bash) | — | Only the pod host sees its own GPUs; gateway must stay shape-agnostic (LOCKED) |
| whisper device decision | Pod onstart | — | Source of truth is the pod; computed once at boot before supervisord exec |
| whisper device pin (`device_index`) | Pod (speaches env) | — | ctranslate2/faster-whisper `device_index` is per-process GPU selection |
| `whisper_device` reporting | Pod health-bridge :9100 | — | Aggregate health surface; speaches 3rd-party :8001 can't carry custom fields |
| reading reported device | Gateway reconciler at Ready | — | Gateway captures pod metadata into `primaryPodURLs` |
| STT tier-0 override gate | Gateway reconciler (3 sites) | upstreams.Loader | Override-or-skip decides pod-GPU-STT vs tier-1 gemini-stt |
| fail-safe on missing field | Gateway reconciler | — | Old pod image → no field → skip override → gemini-stt |

## Standard Stack

**No external packages added.** This phase is pure first-party code: Go (gateway reconciler + health-bridge), bash (gateway-built onstart), and an INI edit (supervisord.conf). VRAM detection uses `nvidia-smi` (already present in every CUDA Vast host + the llama.cpp base image). No npm/pip/cargo installs → **no Package Legitimacy Audit required.**

| Tool | Version | Purpose | Why Standard |
|------|---------|---------|--------------|
| `nvidia-smi --query-gpu` | bundled with NVIDIA driver | one-shot VRAM read at onstart | Already on every Vast CUDA host; canonical VRAM query [VERIFIED: present in base image, used in RUNBOOK-PRIMARY-POD.md:862] |
| speaches `WHISPER__*` env | speaches 0.9.0-rc.3 | whisper device + index config | pydantic-settings nested env; **both vars confirmed to exist** (see below) |
| faster-whisper / ctranslate2 `device_index` | faster-whisper==1.1.1 (pinned, Dockerfile:111) | per-process GPU selection | The mechanism speaches forwards to ctranslate2 |
| supervisord `%(ENV_x)s` / env inheritance | supervisor 4.x | propagate onstart env into `[program:speaches]` | Documented expansion + inheritance [CITED: supervisord.org/subprocess.html] |

### speaches WhisperConfig env vars (gap #1 — RESOLVED)

[CITED: https://speaches.ai/configuration/ — fetched 2026-06-17] The `WhisperConfig` object exposes all three relevant fields via pydantic double-underscore nesting:

| Field | Env var | Default |
|-------|---------|---------|
| `inference_device` | `WHISPER__INFERENCE_DEVICE` | `"auto"` |
| `device_index` | `WHISPER__DEVICE_INDEX` | `0` |
| `compute_type` | `WHISPER__COMPUTE_TYPE` | `"default"` |

**This corrects SEED-IMPL-SURFACES line 12** ("no device_index env exists today" / "WHISPER__DEVICE_INDEX (no device_index env exists today)"). `WHISPER__DEVICE_INDEX` **does exist** in speaches' schema. The supervisord comment block (conf:60-61) is also correct that `inference_device=auto` auto-picks CUDA when present — but auto does NOT let you pin WHICH GPU, so on multi-GPU shapes we must set `WHISPER__DEVICE_INDEX` explicitly.

### faster-whisper `device_index` semantics (gap #1 cont.)

[VERIFIED: faster-whisper README + SYSTRAN/faster-whisper#149] `WhisperModel(device="cuda", device_index=N)` selects a single GPU by index. `device_index` may be an int or a list; a list of indices is for data-parallel replicas (one model copy per GPU), NOT tensor-splitting. For our single-replica whisper, **`WHISPER__DEVICE_INDEX=<int>` selects exactly one GPU.** Passing `cuda:0`/`cuda:1` strings is UNSUPPORTED by ctranslate2 (errors "unsupported device"). Multi-GPU with mixed compute capabilities errors out — not a concern here (homogeneous 2×3090).

## Pod env plumbing mechanism (gap — the hard part — RESOLVED)

**The runtime fact that changes everything:** speaches is started by **supervisord** (`pod/primary/supervisord.conf [program:speaches]`, baked into the image, conf:44-82), launched via `exec /usr/bin/supervisord` at the tail of the gateway-built `primaryOnstartHead` (onstart.go:229). [VERIFIED: read onstart.go:87-230 + lifecycle.go:439-449 + Dockerfile:200,240-249]

There are TWO ways to get an onstart-computed env into the supervisord child, both standard [CITED: supervisord.org/subprocess.html — "Subprocesses will inherit the environment of the shell used to start the supervisord program"; "%(ENV_VARNAME)s ... all supervisord's environment variables prefixed with ENV_"]:

**Option A — env inheritance (RECOMMENDED, simplest):**
1. In `primaryOnstartHead` (onstart.go), before `exec supervisord`, compute and `export WHISPER__INFERENCE_DEVICE=… WHISPER__DEVICE_INDEX=…`.
2. supervisord inherits these from the bash that exec'd it.
3. `[program:speaches]` inherits supervisord's env — **UNLESS** the conf's `environment=` line overrides the same key. So **remove `WHISPER__INFERENCE_DEVICE="cpu"` from conf:46** (keep `HF_HUB_CACHE`). With the key absent from `environment=`, the inherited value wins. (`environment=` settings are additive/override per the supervisord docs — an explicit key there shadows inherited.)

**Option B — explicit `%(ENV_x)s` expansion:** keep the conf line as `environment=HF_HUB_CACHE="/weights/whisper",WHISPER__INFERENCE_DEVICE="%(ENV_WHISPER_DEVICE)s",WHISPER__DEVICE_INDEX="%(ENV_WHISPER_DEVICE_INDEX)s"`. Requires the onstart to ALWAYS export both vars (a missing `ENV_*` key makes supervisord fail config expansion at boot). More brittle than A.

**Recommendation: Option A.** It needs the conf:46 edit anyway (the LOCKED decision), and inheritance fails open (missing var → speaches default `auto`/`0`) rather than crashing supervisord. The onstart computes a definite value in all branches, so inheritance is deterministic.

### Concrete onstart logic to add (in onstart.go `primaryOnstartHead`, after weight extraction ~line 203, before the appended `exec supervisord`)

```bash
# VRAM-adaptive whisper device (SEED-019 part 2). nvidia-smi is present on
# every CUDA Vast host. Sum total VRAM across all visible GPUs.
TOTAL_VRAM_MIB=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits | paste -sd+ | bc)
GPU_COUNT=$(nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits | wc -l)
echo "[onstart] total VRAM ${TOTAL_VRAM_MIB} MiB across ${GPU_COUNT} GPU(s)"

WHISPER_GPU_THRESHOLD_MIB=30000   # ~30 GB (Claude's-discretion final value)
if [ "${TOTAL_VRAM_MIB}" -ge "${WHISPER_GPU_THRESHOLD_MIB}" ]; then
  export WHISPER__INFERENCE_DEVICE=cuda
  # Pick the GPU with the MOST free VRAM right now (see gap #2 + OPEN QUESTION 1).
  WHISPER_IDX=$(nvidia-smi --query-gpu=index,memory.free --format=csv,noheader,nounits \
    | sort -t, -k2 -n -r | head -1 | cut -d, -f1 | tr -d ' ')
  export WHISPER__DEVICE_INDEX="${WHISPER_IDX}"
  export WHISPER_DEVICE=cuda            # summary var the health-bridge reports
else
  # <30 GB: do NOT serve slow CPU whisper that the gateway would route to.
  # Discretion: disable speaches entirely OR pin cpu+report cpu so the gateway
  # fail-safes to gemini. Reporting whisper_device=cpu is sufficient for the
  # gateway gate; disabling the child also saves the boot cost. See OPEN Q2.
  export WHISPER__INFERENCE_DEVICE=cpu
  export WHISPER_DEVICE=cpu
fi
```

`bc` may not be installed — use a pure-bash sum or `awk` instead (the Dockerfile installs `python3` but not `bc`; prefer `awk '{s+=$1} END{print s}'`). [VERIFIED: Dockerfile:65-68 apt list has no `bc`.]

## device_index OFF the Qwen GPU (gap #2 — RESOLVED with a caveat)

The SEED says "pin whisper to the GPU NOT holding Qwen." **This framing is imprecise for the actual llama config.** [VERIFIED: supervisord.conf:37 + lifecycle.go onstart] llama-server runs `-ngl 99 -np 2` with NO `--tensor-split`, NO `--main-gpu`, NO `CUDA_VISIBLE_DEVICES` pin (grep confirmed none exist in pod/ or gateway/). llama.cpp's default `--split-mode layer` **distributes Qwen's layers across ALL visible GPUs** [CITED: github.com/ggml-org/llama.cpp/blob/master/docs/multi-gpu.md — "layer split mode distributes the model's transformer layers sequentially across hardware"]. So on 2×3090 Qwen occupies BOTH cards partially (~half each); neither GPU is empty.

**Correct approach:** pin whisper to the GPU with the MOST FREE VRAM (max `memory.free`), not "the non-Qwen GPU" (there is none). On 2×3090: Qwen GGUF ~16GB + KV split ≈ ~8-12GB per card → each card keeps ~12-16GB free, enough for whisper-large-v3 (~3-4GB). Picking the max-free index avoids the OOM that forced the original CPU pin on the single 24GB card.

**The detection approach:** `nvidia-smi --query-gpu=index,memory.free --format=csv` → sort by free desc → take top index → `WHISPER__DEVICE_INDEX`. This is the snippet above.

> **OPEN QUESTION 1 (sequencing):** onstart computes `memory.free` BEFORE `exec supervisord`, so at that instant llama has NOT loaded Qwen yet — both GPUs read ~full-free and the max-free pick is effectively arbitrary (likely index 0). On 2×3090 either index has enough headroom so this is *probably* fine, but it is NOT guaranteed to dodge OOM if both whisper and Qwen land on index 0. **Resolution options for the planner:** (a) accept arbitrary index on 2×3090 (headroom is large; lowest-risk, simplest); (b) pin Qwen explicitly with `--main-gpu 0 --split-mode none` (only fits if Qwen <24GB — it is ~16GB+KV, tight) and whisper to index 1 — deterministic but changes llama placement and KV headroom; (c) start supervisord, wait for llama `/health`, THEN measure free + restart only speaches with the index — most correct but adds boot complexity. **Recommend (a) for this phase** given 2×3090 headroom; flag (b)/(c) as follow-ups. The live UAT (no-OOM assertion) is the real gate.

## health-bridge `whisper_device` field (gap #3 — RESOLVED)

**Simplest reliable source: a boot env var the bridge reads in `loadConfig()`.** [VERIFIED: main.go:30-40 already loads all config from env; handlers.go:58-79 builds the aggregate.]

Plan:
1. onstart `export WHISPER_DEVICE=cuda|cpu` (already in the snippet above). Pass it into the health-bridge process. **Caveat:** the health-bridge runs as its OWN process. In the PROD supervisord image there is NO `[program:health-bridge]` in supervisord.conf (conf:8-9 explicitly says "health-bridge runs separately in the pod, not under supervisord"). [VERIFIED: supervisord.conf has only llama/speaches/chatterbox/dcgm.] **→ OPEN QUESTION 3:** how is the health-bridge actually started in the PROD supervisord image? It is a `[program:*]` neither in the conf nor visibly launched by onstart. (In the docker-compose path it's the `health-bridge` service, compose.yml:104 — but that path is not PROD.) The planner must locate the PROD launch site to inject `WHISPER_DEVICE` into its env. If health-bridge is added as a supervisord child in this phase, give it `environment=WHISPER_DEVICE="%(ENV_WHISPER_DEVICE)s"`.
2. health-bridge `loadConfig()` (main.go:30) gains `WhisperDevice: envOr("WHISPER_DEVICE", "")`.
3. `aggregateResponse` (handlers.go:59-64) gains `WhisperDevice string \`json:"whisper_device,omitempty"\``, populated in `handleAggregate` (handlers.go:67-78) from a value threaded through (either via closure capture of cfg, or a field on `State`). Simplest: capture `cfg.WhisperDevice` in the `handleAggregate` closure and set it on the struct. No probe of speaches needed — the device is known at boot, deterministic.

**Why not probe speaches for the device:** speaches `:8001` exposes no "which device am I on" endpoint; `GET /health` is a bare liveness 3rd-party route (SEED + handlers confirm). The boot env is authoritative because onstart is what decided the device. [VERIFIED]

## gateway probe of :9100 (gap #4 — RESOLVED, requires a port-forward add)

**Blocking fact:** the gateway only forwards container ports **8000, 8001, 8003, 9400** via the Vast `-p` env map (lifecycle.go:381-384). **Port 9100 (health-bridge) is NOT forwarded** → the gateway currently CANNOT reach `:9100/health/ready` from outside the pod. [VERIFIED: lifecycle.go:377-384 env map; podPortURL:464-476 only builds URLs for mapped ports.]

**Two options:**

**Option 1 (RECOMMENDED) — forward 9100 + probe the aggregate.**
1. Add `"-p 9100:9100": "1"` to the env map in `buildCreateRequest` (lifecycle.go:~384, next to the other 4 ports).
2. Add `podHealthBridgeURL(inst)` = `podPortURL(inst, "9100", "/health/ready")` (lifecycle.go, next to podSTTURL:488).
3. At Ready (markReady reconciler.go:~862, and recover :1595/:1626), after building `urls`, GET the :9100 aggregate, parse `whisper_device`, store as a new field on `primaryPodURLs` (lifecycle.go:112 struct → add `WhisperDevice string` / `STTOnGPU bool`).
4. The existing health gate already polls 4 endpoints — extend `buildPodURLs` + the recover health gate (reconciler.go:1596,1607-1611) to ALSO require the 9100 mapping (or treat a missing 9100 as fail-safe device="" → no STT override).

**Option 2 (no new port) — derive device from a speaches probe.** Not viable: speaches `:8001` can't report the device (gap #3). Reject.

**Mapping the gate to a per-lifecycle bool:** `STTOnGPU := (urls.WhisperDevice == "cuda")`. Missing/unknown/`"cpu"` → `false` → skip the `OverrideTier0("stt", …)` exactly as today's `!PrimaryPodServeSTT` branch does (reconciler.go:448-450,875,1631). This satisfies the LOCKED fail-safe (old pod image reports no field → device="" → false → gemini-stt).

## Architecture Patterns

### System Architecture Diagram

```
                     ┌─────────────────────────── POD (Vast.ai container) ───────────────────────────┐
 gateway             │                                                                                │
 buildCreateRequest  │  bash primaryOnstartHead (onstart.go)                                          │
 (lifecycle.go:348)  │    1. download+verify weights (Qwen/Whisper/BGE/Chatterbox)                    │
   Args=["-c",bash]──┼─►  2. nvidia-smi --query-gpu=memory.total/free  ◄── GAP #1/#2 (NEW)            │
   Env: -p 8000/8001 │    3. export WHISPER__INFERENCE_DEVICE / WHISPER__DEVICE_INDEX / WHISPER_DEVICE │
        /8003/9400   │    4. exec /usr/bin/supervisord ─────────────┐                                  │
   + -p 9100  GAP#4  │                                              ▼                                  │
                     │                       supervisord (PID 1, conf baked in image)                  │
                     │            ┌───────────┬─────────────┬────────────┬──────────┐                  │
                     │        [llama]:8000 [speaches]:8001 [chatterbox] [dcgm]:9400  │                  │
                     │            -ngl 99      WHISPER__INFERENCE_DEVICE inherited ◄── conf:46 edit     │
                     │          (split-mode    /WHISPER__DEVICE_INDEX (GAP #3 device)│                  │
                     │           layer, all    pinned to max-free GPU                │                  │
                     │           GPUs)                                               │                  │
                     │                                                                                │
                     │   health-bridge :9100  (started SEPARATELY — OPEN Q3)                          │
                     │     reads WHISPER_DEVICE env → aggregateResponse.whisper_device  ◄── GAP #3     │
                     └──────────────────────────────────┬─────────────────────────────────────────────┘
                                                         │ GET :9100/health/ready  (needs -p 9100)
                          ┌──────────────────────────────▼──────────────── GATEWAY (Part 3) ──────────┐
                          │ reconciler markReady / recoverOpenLifecycle:                                │
                          │   urls = buildPodURLs(inst) + WhisperDevice (from :9100)  ◄── GAP #4        │
                          │   STTOnGPU := urls.WhisperDevice == "cuda"                                  │
                          │   if STTOnGPU { Loader.OverrideTier0("stt", podURL) }  // else → gemini-stt │
                          │   (replaces  if r.cfg.PrimaryPodServeSTT  at :448 / :875 / :1631)           │
                          │   DELETE Config.PrimaryPodServeSTT (config.go:252,:517)                     │
                          └────────────────────────────────────────────────────────────────────────────┘
```

### Pattern 1: per-lifecycle device bool replaces config flag
**What:** Compute `STTOnGPU` once when `primaryPodURLs` is built, carry it on the struct, read it at all 3 override sites.
**When to use:** Every place that today reads `r.cfg.PrimaryPodServeSTT` (reconciler.go:448,875,1631).
**Example:**
```go
// lifecycle.go:112 — add field
type primaryPodURLs struct {
    LLM, STT, TTS, DCGM string
    WhisperDevice       string // "cuda" | "cpu" | "" (unknown/old image)
}
// reconciler.go:875 — replace flag gate
if urls.WhisperDevice == "cuda" {
    r.deps.Loader.OverrideTier0("stt", stripPrimaryReadinessSuffix(urls.STT))
}
```

### Anti-Patterns to Avoid
- **Editing `pod/onstart.sh`:** that file is NOT the PROD path. The gateway-built `primaryOnstartHead` in `gateway/internal/primary/onstart.go` is. Editing `pod/onstart.sh` ships nothing to prod.
- **Pinning whisper to "the non-Qwen GPU":** there is no non-Qwen GPU under `--split-mode layer`. Pin to max-free index.
- **Probing speaches :8001 for the device:** it can't report it. Use the boot env via health-bridge.
- **`%(ENV_x)s` for an optionally-unset var:** supervisord fails config expansion if `ENV_*` is absent. Use inheritance (Option A) so a missing var falls open to speaches' own defaults.
- **Format-string templating in `primaryOnstartHead`:** the file is a raw-string Go const by Pitfall #9 invariant (onstart.go:33-66) — append bash with plain concatenation, no `fmt.Sprintf`, no shell-quoting at template-expansion time. There are verify grep-gates on this file.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| VRAM query | parse `/proc` or nvml bindings | `nvidia-smi --query-gpu=memory.total/free --format=csv,noheader,nounits` | Already present; canonical; CSV is trivially parseable |
| whisper GPU pin | `CUDA_VISIBLE_DEVICES` juggling around supervisord | `WHISPER__DEVICE_INDEX` | speaches' own config field maps straight to ctranslate2 |
| env → speaches | regenerate supervisord.conf at runtime | env inheritance (remove conf CPU pin) | conf is build-baked + runtime-regen is the explicitly-deferred B2 path (conf:24-28) |
| device reporting | new HTTP endpoint on the pod | extend existing `aggregateResponse` on :9100 | health-bridge already aggregates + the gateway already pattern-probes pod ports |

**Key insight:** Every primitive this phase needs already exists in the codebase or the host. The work is wiring, not building.

## Runtime State Inventory

This is a code+config phase, not a rename/migration. Still, two runtime-state items matter:

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | None — no DB rows key on `PRIMARY_POD_SERVE_STT` (it's an env-loaded bool, not persisted). `primary_lifecycles` rows store no STT-device field. | none (a new device field is in-memory only on `primaryPodURLs`, not persisted unless the planner chooses to log it in event_json) |
| Live service config | **prod `.env` on the ai-gateway-dev stack has `PRIMARY_POD_SERVE_STT=false`** (set hand, rev 30f4c81, per CONTEXT.md:98). Stack `/opt/ai-gateway-dev/` is Portainer GitOps stack 34 (env via Portainer UI, NOT git). [VERIFIED: CONTEXT.md + MEMORY openrouter-token-and-stack-location] | DELETE `PRIMARY_POD_SERVE_STT` from the Portainer stack env at rollout (manual UI edit), AFTER the gateway no longer reads it |
| OS-registered state | None | none |
| Secrets/env vars | `PRIMARY_POD_SERVE_STT` is the only env var deleted. No secrets. New pod env `WHISPER_DEVICE`/`WHISPER__*` are computed in-pod, not gateway secrets. | code: delete config.go:252+:517; rollout: remove from Portainer |
| Build artifacts | The pod **image must be rebuilt** (supervisord.conf:46 edit is build-baked, Dockerfile:200 COPY). Old pods running the pre-edit image will report no `whisper_device` → fail-safe to gemini (LOCKED migration path covers this). | rebuild + push `converseai-primary-pod` image via `.github/workflows/build-primary-pod.yml`; the gateway change is independently deployable |

## Common Pitfalls

### Pitfall 1: Editing the wrong onstart
**What goes wrong:** Plan touches `pod/onstart.sh` / `pod/docker-compose.yml`; nothing reaches prod.
**Why:** PROD boots via gateway `buildCreateRequest`→`primaryOnstartHead`→`exec supervisord`. `pod/onstart.sh`+compose is a separate local-dev/legacy path.
**How to avoid:** All pod-boot bash edits go in `gateway/internal/primary/onstart.go`. The supervisord.conf edit goes in `pod/primary/supervisord.conf`.
**Warning sign:** a task references `--env-file` or `docker compose up` for the device decision.

### Pitfall 2: device measured before Qwen loads
**What goes wrong:** `memory.free` at onstart (pre-supervisord) shows both GPUs nearly empty; the max-free pick can collide with where llama later loads Qwen → CUDA OOM on the contended GPU.
**Why:** onstart runs before `exec supervisord`, so llama hasn't allocated yet.
**How to avoid:** see OPEN QUESTION 1. On 2×3090 the per-card headroom is large enough that even a collision fits; the live no-OOM UAT is the gate.
**Warning sign:** UAT shows `CUDA out of memory` in `/var/log/speaches.log` on a 2×3090.

### Pitfall 3: supervisord `%(ENV_x)s` crash on unset var
**What goes wrong:** Using `WHISPER__INFERENCE_DEVICE="%(ENV_WHISPER_DEVICE)s"` when onstart didn't export it → supervisord refuses to start (config expansion error) → whole pod dead.
**Why:** supervisord treats missing `ENV_*` expansion as fatal.
**How to avoid:** prefer env inheritance (Option A); if using `%(ENV_x)s`, guarantee the onstart exports the var in ALL branches.
**Warning sign:** pod exits at boot with a supervisord config-parse error.

### Pitfall 4: HF_HUB_CACHE lost when deleting conf:46
**What goes wrong:** Deleting the whole `environment=` line drops `HF_HUB_CACHE="/weights/whisper"` → speaches' `get_model_card_data_or_raise()` gate fails → STT crash-loops.
**Why:** conf:46 carries TWO vars on one line.
**How to avoid:** edit the line to keep `HF_HUB_CACHE`, only remove `WHISPER__INFERENCE_DEVICE` (or set it to the inherited/expanded value). [VERIFIED: conf:46 + conf:70-77 HF cache note]
**Warning sign:** `speaches.log` shows model-card/cache resolution errors on /v1/audio/transcriptions.

### Pitfall 5: forgetting the :9100 port-forward
**What goes wrong:** Gateway tries to read `:9100/health/ready` but the port was never forwarded → device always empty → STT never routes to a healthy GPU pod (always gemini), silently.
**Why:** env map (lifecycle.go:381-384) only forwards 8000/8001/8003/9400.
**How to avoid:** add `"-p 9100:9100": "1"`.
**Warning sign:** UAT on a 2×3090 routes STT to gemini despite the pod reporting `cuda` internally.

## Code Examples

### nvidia-smi VRAM read (CSV, sum + max-free index)
```bash
# Total VRAM across all GPUs (MiB)
nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits | awk '{s+=$1} END{print s}'
# Index of the GPU with the most free VRAM
nvidia-smi --query-gpu=index,memory.free --format=csv,noheader,nounits \
  | sort -t, -k2 -n -r | head -1 | cut -d, -f1 | tr -d ' '
```

### supervisord.conf:46 edit (Option A — drop the device pin, keep HF cache)
```ini
[program:speaches]
command=/opt/speaches-venv/bin/uvicorn speaches.main:create_app --host 0.0.0.0 --port 8001 --factory
; was: environment=HF_HUB_CACHE="/weights/whisper",WHISPER__INFERENCE_DEVICE="cpu"
environment=HF_HUB_CACHE="/weights/whisper"
; WHISPER__INFERENCE_DEVICE + WHISPER__DEVICE_INDEX now inherited from the
; onstart-exported env (SEED-019 part 2 VRAM-adaptive); speaches defaults
; inference_device=auto / device_index=0 if unset (fail-open).
```

### gateway gate replacement (reconciler.go:875)
```go
// Source: gateway/internal/primary/reconciler.go markReady
// SEED-019 part 2/3: gate on the pod-reported whisper device, not a config flag.
if urls.WhisperDevice == "cuda" {
    r.deps.Loader.OverrideTier0("stt", stripPrimaryReadinessSuffix(urls.STT))
} // else: STT falls through to tier-1 gemini-stt (fail-safe for cpu/unknown)
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `WHISPER__INFERENCE_DEVICE="cpu"` hardcoded in conf (UAT 17, 4090 24GB) | VRAM-adaptive device from onstart | this phase | shape-agnostic STT placement |
| Manual `PRIMARY_POD_SERVE_STT` env flag (commit 4021901) | gateway reads pod-reported `whisper_device` | this phase | no hand-set flag per shape |
| gateway captures zero pod metadata at Ready | gateway reads `:9100` aggregate into `primaryPodURLs` | this phase | first pod→gateway metadata channel |

**Deprecated/outdated by this phase:**
- `Config.PrimaryPodServeSTT` (config.go:252, :517) — deleted.
- `PRIMARY_POD_SERVE_STT` prod env (Portainer stack 34) — removed at rollout.
- supervisord.conf:46 `WHISPER__INFERENCE_DEVICE="cpu"` pin — removed.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `nvidia-smi` is on the PROD pod image (llama.cpp base + CUDA host) | Standard Stack | If absent, VRAM detect fails → must add to Dockerfile. LOW risk (CUDA hosts ship it; RUNBOOK:862 already references it). |
| A2 | On 2×3090, picking max-free index at pre-llama onstart time yields enough headroom to avoid whisper/Qwen OOM | gap #2 / Pitfall 2 | If wrong → CUDA OOM on UAT; mitigate via OPEN Q1 option (b)/(c). MEDIUM risk — the live no-OOM UAT is the gate. |
| A3 | health-bridge can be given a `WHISPER_DEVICE` env in the PROD supervisord image | gap #3 | Depends on OPEN Q3 (how health-bridge is launched in PROD). MEDIUM — must locate launch site. |
| A4 | `awk`/`sort`/`cut`/`tr` are available in the pod image for the bash math | Code Examples | LOW — coreutils present in the llama.cpp ubuntu base; `bc` is NOT (use awk). |

## Open Questions

1. **Whisper device_index sequencing vs llama load order.** onstart measures `memory.free` before supervisord starts llama, so the pick can't see Qwen's allocation. Options (a) accept arbitrary index on 2×3090 (recommended — large headroom), (b) pin Qwen `--main-gpu 0 --split-mode none` + whisper index 1 (deterministic, changes llama placement/KV), (c) start supervisord, await llama `/health`, then measure+restart speaches. **Recommend (a); live no-OOM UAT validates.**

2. **<30GB shape: disable speaches vs report cpu.** Claude's discretion per CONTEXT. Reporting `whisper_device=cpu` is sufficient for the gateway gate (→ gemini). Disabling the speaches child additionally saves boot cost + VRAM. **Recommend report cpu AND keep speaches up** (so the pod's :8001 probe doesn't fail the 4-endpoint health gate — disabling speaches would make `podSTTURL` health-check fail and could block Ready). **The planner must confirm the health gate still passes if speaches is disabled** — likely it must stay UP even on cpu.

3. **How is the health-bridge launched in the PROD (supervisord) image?** It is NOT a `[program:*]` in supervisord.conf (conf:8-9 explicitly excludes it) and is not visibly started by `primaryOnstartHead`. The docker-compose path has it as a service, but that's not PROD. The planner must locate the PROD launch site to inject `WHISPER_DEVICE` into its env (and to confirm :9100 is actually served in PROD at all — gap #4 depends on it). **This is the single biggest open item; resolve before planning gap #3/#4 tasks.**

4. **Is `:9100` reachable end-to-end in PROD once forwarded?** Adding `-p 9100:9100` forwards the container port, but only if a process actually listens on 9100 inside the container (ties to Q3). Confirm during planning, validate in UAT.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| `nvidia-smi` | onstart VRAM detect | ✓ (on Vast CUDA host) | driver-bundled | none needed |
| `awk`/`sort`/`cut`/`tr` | onstart bash math | ✓ (ubuntu base) | coreutils | none |
| `bc` | (avoided) | ✗ | — | use `awk` |
| Go toolchain | gateway + health-bridge build | ✓ (CI build-gateway / build-primary-pod) | repo go.mod | — |
| Vast.ai credit | live UAT | ⚠ check first | — | abort UAT if no credit (MEMORY: check Vast credit FIRST) |

## Validation Architecture

`.planning/config.json` — nyquist_validation treated as ENABLED (no explicit false found in scope).

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go `testing` + `testify/require` |
| Config file | `gateway/go.mod` (no separate test config) |
| Quick run command | `cd gateway && go test ./internal/primary/... ./internal/config/...` |
| Full suite command | `cd gateway && go test ./... && cd ../pod/health-bridge && go test ./...` |

### Phase Requirements → Test Map
| Behavior | Test Type | Automated Command | File Exists? |
|----------|-----------|-------------------|-------------|
| Gate STT override on `whisper_device=="cuda"` (rework of flag test) | unit | `go test ./internal/primary/ -run TestEvaluateProvisioning_WhisperDevice` | ❌ Wave 0 (rework reconciler_test.go:642-717) |
| `cuda` → all 3 overrides; `cpu`/`""` → llm+tts only | unit | same | ❌ Wave 0 |
| Missing field (old image) → fail-safe no-stt-override | unit | `go test ./internal/primary/ -run TestEvaluateProvisioning_WhisperDeviceMissing` | ❌ Wave 0 |
| `primaryPodURLs.WhisperDevice` populated from :9100 aggregate | unit | `go test ./internal/primary/ -run TestBuildPodURLs_WhisperDevice` | ❌ Wave 0 (add 9100 mapping to `runningInstanceWithAllPorts` fixture, reconciler_test.go:422) |
| `Config.PrimaryPodServeSTT` deleted (compile + no env read) | unit/compile | `go build ./... && go test ./internal/config/...` | ✅ (compile gate) |
| health-bridge `aggregateResponse.whisper_device` rendered from env | unit | `cd pod/health-bridge && go test -run TestAggregate_WhisperDevice` | ❌ Wave 0 (handlers test) |
| onstart VRAM→device bash logic (threshold + index pick) | unit (bash) | `bats`/shell test OR a Go test asserting `primaryOnstartHead` contains the nvidia-smi block + grep-gate | ❌ Wave 0 |
| 1×3090 → STT routes to gemini (200, not pod) | live UAT | manual Vast provision + real-audio POST | manual |
| ≥30GB shape (2×3090/5090) → STT on pod GPU <5s, no CUDA OOM | live UAT | manual Vast provision + 10s clip + `speaches.log` OOM grep | manual |

### Sampling Rate
- **Per task commit:** `cd gateway && go test ./internal/primary/... ./internal/config/...` (+ `pod/health-bridge` when touched)
- **Per wave merge:** full Go suite both modules
- **Phase gate:** full suite green + the 2 live Vast UATs (1×3090 gemini-route, ≥30GB pod-GPU no-OOM) before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] Rework `gateway/internal/primary/reconciler_test.go:642-717` — drive off `urls.WhisperDevice` instead of `cfg.PrimaryPodServeSTT`; add `cuda`/`cpu`/`""` cases.
- [ ] Update `runningInstanceWithAllPorts` fixture (reconciler_test.go:422) to include `"9100/tcp"` mapping + a fake :9100 aggregate response in the health-check stub.
- [ ] Remove `PrimaryPodServeSTT:true` from `cfgWithDefaults` (lifecycle_test.go:52) + `testCfg`.
- [ ] New `pod/health-bridge` test for `whisper_device` rendering.
- [ ] onstart bash assertion test (grep-gate on `primaryOnstartHead` for the nvidia-smi/export block — matches the existing Pitfall #9 grep-gate convention).

## Security Domain

`security_enforcement` not explicitly false → applicable. This phase adds no new auth/crypto/input surfaces.

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | — |
| V3 Session Management | no | — |
| V4 Access Control | no | — |
| V5 Input Validation | yes (low) | The gateway parses the pod's `:9100` JSON — validate `whisper_device` is one of `{cuda,cpu,""}`; treat any other value as unknown→fail-safe (no-override). Bound the response read (the health-bridge is first-party, but `io.LimitReader` the body as probes.go already does). |
| V6 Cryptography | no | — |

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Spoofed/garbage `whisper_device` from a compromised pod | Tampering | whitelist `{cuda,cpu}`; default-deny (unknown → gemini-stt). Pod is provisioned by the gateway itself on Vast (trust boundary already established for the 4 service ports). |
| :9100 exposed publicly via Vast port-forward | Info disclosure | health-bridge aggregate is non-sensitive (status + device string). Same exposure class as the already-forwarded :8001/:9400. No secrets in the body. |

## Sources

### Primary (HIGH confidence)
- Local code (VERIFIED by direct read): `gateway/internal/primary/onstart.go`, `lifecycle.go:90-538`, `reconciler.go:440-456,855-883,1525-1644`, `config/config.go:245-252,510-522`, `upstreams/loader.go:355-412`, `pod/primary/supervisord.conf`, `pod/primary/Dockerfile`, `pod/health-bridge/{handlers,probes,main,state}.go`, `pod/docker-compose.yml`, `pod/onstart.sh`, `pod/.env.example`, `reconciler_test.go:422-717`, `lifecycle_test.go:40-60`
- speaches configuration — https://speaches.ai/configuration/ (WHISPER__INFERENCE_DEVICE / WHISPER__DEVICE_INDEX / WHISPER__COMPUTE_TYPE confirmed)
- supervisord subprocess env — https://supervisord.org/subprocess.html (env inheritance + %(ENV_x)s)
- llama.cpp multi-GPU — https://github.com/ggml-org/llama.cpp/blob/master/docs/multi-gpu.md (default split-mode=layer distributes across all GPUs)

### Secondary (MEDIUM confidence)
- faster-whisper device_index semantics — https://github.com/SYSTRAN/faster-whisper + issue #149 (device_index list = data-parallel, not tensor split; cuda:N unsupported)
- CONTEXT.md / SEED-019 / SEED-019-IMPL-SURFACES / STATE.md / CLAUDE.md MEMORY notes (project decisions)

### Tertiary (LOW confidence)
- WebSearch summaries on speaches/pydantic nesting (corroborated by the official speaches docs fetch above)

## Metadata

**Confidence breakdown:**
- Gateway side (Part 3): HIGH — every file:line read directly; gate-swap is mechanical.
- Pod env plumbing (Part 2): HIGH on mechanism (supervisord inheritance + speaches env confirmed); MEDIUM on device_index sequencing (OPEN Q1) and health-bridge launch (OPEN Q3).
- Pitfalls: HIGH — derived from read code + UAT history in STATE.md.

**Research date:** 2026-06-17
**Valid until:** 2026-07-17 (stable repo; speaches image pin is frozen by D-B1, so the WHISPER__ schema won't drift)
