---
status: passed
phase: 14-vram-adaptive-stt-gateway-auto-decides-pod-stt-via-pod-repor
verified: "2026-06-19"
verdict: passed
---

# Phase 14 VERIFICATION — VRAM-adaptive STT

**Goal:** the gateway auto-decides whether STT runs on the pod GPU or fails over to tier-1 gemini, driven entirely by the pod-reported device (`GET :9100/whisper_device`) — no operator flag, no gateway shape knowledge. Proven bidirectionally on real hardware.

## Verdict: PASSED

Both phase-gate live UATs green. The dead operator flag is removed. A real 2-GPU CUDA-OOM bug was found during UAT B and fixed (commit `5840236`).

## Per-requirement verdict

| Req ID | Verdict | Evidence |
|--------|---------|----------|
| STT-AUTO | ✅ | Pod self-decides device in `onstart.go` (VRAM probe); gateway reads `:9100/whisper_device` and routes — no operator flag. Plan 14-01/14-02 unit + integration tests green; both live UATs confirm. |
| STT-FAILSAFE | ✅ | 24 GB shape → `cpu` → gateway does NOT override → tier-1 gemini-stt (UAT A). `:9100` unreachable/non-{cuda,cpu} → no override (fail-safe path in 14-01). |
| STT-PROBE | ✅ | `:9100/whisper_device` responder live on both shapes; returned `cpu` (UAT A) and `cuda` (UAT B). |
| POD-VRAM | ✅ | `onstart`: `total VRAM 49152 MiB >= 30000 across 2 GPU(s)` → cuda; 24 GB → cpu. nvidia-smi confirmed placement. |
| STT-SHAPE-3090 | ✅ | UAT A: 1×3090, `whisper_device=cpu`, STT served by gemini-stt, HTTP 200 ~2.27s, no override. |
| STT-SHAPE-GPU | ✅ | UAT B: 2×3090, `whisper_device=cuda`, STT served by pod local-stt GPU, HTTP 200, warm 1.3s, **CUDA OOM = 0**, GPU0=qwen/GPU1=whisper. |
| STT-MIGRATE | ✅ | UAT A confirms the gateway routes the 24 GB shape to gemini-stt (the migration target) without the old pin. |
| FLAG-REMOVE | ✅ | `PRIMARY_POD_SERVE_STT` removed from ai-gateway-dev stack 34 (count=0). Plan 14-01 deleted the gateway read. |

## Key artifacts / final state
- Promoted pod image: `…/converseai-primary-pod@sha256:59c6191f…` (carries 2-GPU fix).
- Gateway `latest-dev` rev `5840236`.
- Weights store migrated to Cloudflare R2 (`ai-gateway-weights`) — global-fast cold-start (37 MiB/s to EE pod vs 1.1 MiB/s prior EU-to-far-pod).
- 2-GPU OOM fix: qwen pinned GPU0 (`--split-mode none --main-gpu 0`), whisper → last card (`NUM_GPUS-1`).

## Open follow-ups (do not block phase)
- SEED: global auto-blocklist on provision failure (6 hosts blocklisted by hand this session).
- Prod parity: R2 weights + cold-start budget + image promote on next prod redeploy.
- Operator: re-enable prod pod schedule (`PRIMARY_POD_SCHEDULE_DISABLED=false`) when wanted.

## Vast spend
~$1.50–2.00; all pods force-downed; 0 orphans.
