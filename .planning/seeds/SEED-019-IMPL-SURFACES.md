# SEED-019 Parts 2+3 — Code Surface Map (research 2026-06-17)

Feeds the phase plan to make STT-from-pod VRAM-driven (remove manual
`PRIMARY_POD_SERVE_STT` flag). Companion to
`SEED-019-multi-pod-shape-cascade-vram-adaptive-stt.md`.

## POD SIDE (Part 2 — VRAM-adaptive whisper + report device)

1. **`pod/primary/supervisord.conf:44-82`** `[program:speaches]`.
   - `:45` uvicorn `speaches.main:create_app --port 8001`.
   - `:46` the pin: `environment=HF_HUB_CACHE="/weights/whisper",WHISPER__INFERENCE_DEVICE="cpu"` — carries TWO vars; can't just delete the line (loses HF_HUB_CACHE).
   - `:60-61` Speaches defaults `inference_device=auto` → picks CUDA automatically when present. So VRAM-adaptive path can OMIT the device var for auto-CUDA, OR set `cuda` + a `WHISPER__DEVICE_INDEX` (no device_index env exists today).
   - **Gotcha `:24-28`:** conf is baked into image at build (Dockerfile COPY); runtime override unsupported. VRAM→device must be plumbed as a CONTAINER ENV, not a conf edit.

2. **`pod/onstart.sh`** (single file; no `pod/primary/onstart.sh`).
   - `:100-105` runs `docker compose up -d` (NOT supervisord directly) with `--env-file "${ENV_FILE}"`. Whisper starts in a container consuming the baked conf.
   - `:86` pre-compose point to add `nvidia-smi --query-gpu=memory.total` detection (NO nvidia-smi call exists in repo today). Result → plumb into container via env-file.
   - `:31-56` env preflight/defaults (place to derive `WHISPER_DEVICE`/`WHISPER_DEVICE_INDEX`).
   - `:107-130` readiness gate polls `${READINESS_URL:=http://127.0.0.1:9100/health/ready}`.

3. **Pod health surface:** health-bridge Go svc on **:9100** (`pod/health-bridge/`).
   - **`pod/health-bridge/handlers.go:58-79`** `aggregateResponse` struct (status/services/uptime_s/timestamp) → **natural place to add `whisper_device`**. Routes `:88-102`.
   - `pod/health-bridge/probes.go:121-174` `probeSTT` posts WAV to `:8001/v1/audio/transcriptions`; knows STT up but NOT device.
   - **Gotcha:** speaches' own `:8001/health` is 3rd-party — can't add `whisper_device` there. Either gateway probes health-bridge `:9100`, or parses speaches another way.

## GATEWAY SIDE (Part 3 — conditional STT override)

4. **3 override sites in `gateway/internal/primary/reconciler.go`** (all gated by `r.cfg.PrimaryPodServeSTT`, commit 4021901):
   - Site A `evaluateReady` re-assert loop `:443-456`, gate `:448-450` (`if role=="stt" && !PrimaryPodServeSTT { continue }`), `OverrideTier0` `:452`.
   - Site B `markReady` `:871-878`, gate `:875`. llm `:871`/tts `:878` unconditional.
   - Site C `recoverOpenLifecycle` `:1629-1634`, gate `:1631`.
   - Clear paths (`startDrain :824-828`, `closeLifecycle :980-985`) call `RestoreTier0("stt")` unconditionally — safe no-op, no gate needed.
   - **Whatever replaces the flag must produce a per-lifecycle bool available at all 3 sites.**

5. **Config `PrimaryPodServeSTT`:** struct `gateway/internal/config/config.go:252`; env load `:517` (`boolOr(getenv, true)`). Read sites: reconciler `:448/:875/:1631` only. Test fixture `gateway/internal/primary/lifecycle_test.go:52`.

6. **Pod metadata capture:** `gateway/internal/primary/lifecycle.go:112-117` `type primaryPodURLs struct { LLM; STT; TTS; DCGM string }` → **add `WhisperDevice string` / `STTOnGPU bool` here**. Built by `buildPodURLs` reconciler `:1532-1538` (prov) + `:1595` (recovery); stored `r.activePodURLs.Store` `:862`/`:1626`. **Gateway captures ONLY urls + health pass/fail today — zero pod metadata.** Must add a probe reading device (health-bridge `:9100/health/ready` aggregate is best channel) and stash in struct.

7. **Override mechanism `gateway/internal/upstreams/loader.go`:** `OverrideTier0` `:361-372` (stores `tier0Override[role]`), `RestoreTier0` `:382-391`, `Tier0OverrideURL` `:402-412`. Slots `:98`/`:284`. STT cascade `:320`: `ResolveAllTier1("stt") → [gemini-stt(10), groq-whisper(15), openai-whisper(20)]`. **Skipping `OverrideTier0("stt",podURL)` is exactly what routes STT to gemini-stt — no other change needed.**

## EXISTING TESTS
- `gateway/internal/primary/reconciler_test.go:642-717` `TestEvaluateProvisioning_PrimaryPodServeSTT` (flag=false → no stt override; flag=true → all 3).
- `reconciler_test.go:605-637` baseline all-3-roles.
- `lifecycle_test.go:42-52` `cfgWithDefaults` (`PrimaryPodServeSTT:true`).
- These pin to the config flag → rework to drive off per-lifecycle device signal.

## NET / hard problems
- **Part 2:** whisper starts via docker-compose consuming build-baked conf → device decision plumbed as container env from onstart (nvidia-smi → env-file), not a conf edit. Pin device_index off the Qwen GPU to dodge OOM.
- **Part 3:** gateway captures zero pod metadata at Ready → add device signal (via health-bridge `aggregateResponse`) into `primaryPodURLs`, replace flag at reconciler `:448/:875/:1631`.
