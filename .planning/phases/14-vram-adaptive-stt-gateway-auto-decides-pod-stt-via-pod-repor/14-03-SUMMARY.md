# 14-03 SUMMARY — Rollout + live UAT (VRAM-adaptive STT)

**Status:** complete (both live UATs green)
**Date:** 2026-06-19

## What was done

Task 1 — built/promoted the rebuilt primary-pod image + authored `pod/smoke/uat-14.sh` (commit `e42beec`).
Task 2 (checkpoint) — ran the 2 live Vast UATs end-to-end.
Task 3 — this verdict + STATE/ROADMAP advance.

## Live UAT evidence

### UAT A — 1×3090 (24 GB) — STT-SHAPE-3090 + STT-MIGRATE — GREEN
- Pod: machine 51946 (Algeria), `whisper_device=cpu`.
- Gateway did NOT set the STT tier-0 override (only llm + tts overrides logged).
- STT served by **tier-1 gemini-stt**, HTTP 200, ~2.27s (`dispatching role=stt upstream=gemini-stt`).
- force-down clean, 0 orphan.

### UAT B — 2×3090 (48 GB) — STT-SHAPE-GPU — GREEN
- Pod: machine 43803 (Estonia), `whisper_device=cuda`.
- GPU placement: **GPU0 = qwen 21.4 GB (pinned), GPU1 = whisper 266 MB** (onstart: `qwen pinned GPU0; whisper device=cuda index=1`).
- STT served by **pod local-stt GPU** (`emergency_pod_stt`, tier-0 override → `http://<pod>:…`), HTTP 200.
- Latency: cold 5.7s (first-call model warmup), **warm 1.28–1.38s** (<5s).
- **CUDA OOM = 0** in speaches.log.
- force-down clean, 0 orphan.

## Deviations (Rule 1 — significant)

1. **2-GPU CUDA OOM bug found + fixed (commit `5840236`).** UAT B "instance terminal" traced to whisper landing on the qwen-contended GPU. Old onstart picked `WHISPER__DEVICE_INDEX` by max-free VRAM *at onstart* — before llama loads both cards read empty → tied to GPU0 → whisper collided with qwen → OOM. Fix: `supervisord.conf` pins qwen with `--split-mode none --main-gpu 0` (Q4 ~17 GB fits one 24 GB card; layer-split pools VRAM but never parallelizes compute, so no throughput loss); `onstart.go` dedicates the LAST card to whisper (`NUM_GPUS-1`) on multi-GPU shapes. `lifecycle_test.go` updated. Both pod + gateway images rebuilt.

2. **Weights store migrated to Cloudflare R2 (cold-start fix).** Single-region MinIO (BR) and a Contabo EU mirror were both slow to far pods (BR→US ~10 MB/s; EU→US 1.1 MB/s, stalled) → repeated cold-start-budget timeouts. Moved the 4 weight prefixes to R2 (`ai-gateway-weights`, global anycast, egress-free). Dev stack 34 `MINIO_*` now points to R2. R2 served the EE pod at **37 MiB/s**. R2 S3 creds derived from the CF R2 API token (AccessKeyId = token id, Secret = sha256(token value)) — no dashboard needed; recorded in global CLAUDE.md.

3. **qwen key mismatch caught.** First R2 mirror copied `qwen3.5-27b-Q4_K_M` but the pod's `PRIMARY_QWEN_WEIGHTS_KEY` is `qwen3.6-27b-Q4_K_M` → FATAL onstart loop. Re-mirrored qwen3.6; deleted the stray 3.5 from R2.

4. **Cold-start budget raised** `PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS=3600` (was 2400) on dev — MinIO/large-image pulls need the headroom.

5. **Flaky-host blocklist grew** during UAT: `97968, 39772, 16146, 48612` (DOA / CDI-broken / port-bind). Reinforces the SEED below.

## Image / config final state
- `PRIMARY_TEMPLATE_IMAGE = ghcr.io/ifixtelecom/converseai-primary-pod@sha256:59c6191fc1e637ad6de9d46007e3c6c2356ee191e2f3b2c1d571b6cfa69e3a64` (carries the 2-GPU fix).
- Gateway image `latest-dev` rev `5840236`.
- `PRIMARY_POD_SERVE_STT` removed from dev stack 34 (FLAG-REMOVE ✓).

## Vast spend
~$1.50–2.00 across the 1×3090 UAT A run + ~7 2×3090 UAT B attempts (most died on host roulette before completing). All pods force-downed; 0 orphans.

## SEEDs / follow-ups
- **SEED — global auto-blocklist on provision failure.** Gateway should auto-blocklist a `machine_id` on DOA / CDI-unresolvable / port-bind-timeout / instance-terminal, in a blocklist **shared** across primary + fallback + emergency (today manual + per-subsystem `PRIMARY_VAST_MACHINE_BLOCKLIST`). 6 hosts blocklisted by hand this session.
- **Prod parity TODO:** migrate prod gateway weights to R2 + raise prod cold-start budget + 2-GPU fix is already in the images (promote when prod redeploys).
- **Operator restore:** prod pod schedule was disabled (`PRIMARY_POD_SCHEDULE_DISABLED=true` on n8n-ia-vm `/opt/ai-gateway-prod/.env`) during this session — re-enable to `false` when prod STT scheduling is wanted again.
