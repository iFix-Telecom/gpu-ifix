# SEED-019 — Multi-pod shape cascade (2×3090 → 1×5090 → 1×3090-no-STT) + VRAM-adaptive whisper + conditional STT override

**Decided:** 2026-06-17 (operator). Supersedes the single PRIMARY/FALLBACK shape pair with a priority cascade, and resolves the STT-latency problem (whisper on CPU times out at 60s on real audio).
**Related:** [[SEED-018-gateway-stt-model-not-rewritten-override-sends-literal-alias]] (STT override; the conditional-override piece builds on it), [[SEED-014]] (pod shape/provisioning), [[primary-gpu-shape-06.8-final]] (prior shape decisions).

## Problem

The prod pod runs `WHISPER__INFERENCE_DEVICE="cpu"` (supervisord.conf:46, pinned in UAT 17 for the 24 GB 4090). On the current 1×3090 (24 GB) shape, whisper can't fit on GPU (Qwen 16 + bge 2 + KV 2 + chatterbox ~4 + whisper 3-4 ≈ 28 GB > 24). CPU whisper-large-v3 runs ~0.3× realtime → a 3 s clip exceeds the 60 s gateway STT timeout → **502/503 on every real-audio request**. LLM/TTS/embed (GPU) are fine.

## Decided shape cascade (priority order)

Validated Vast pricing (US/CA cheapest, 2026-06-17):

| Prio | Shape | $/h | VRAM | STT on GPU? (needs ~28 GB) |
|------|-------|-----|------|----------------------------|
| 1 | **2×3090** | $0.229 | 48 GB (2×24) | ✅ whisper on GPU1, lots of headroom |
| 2 | **1×5090** | $0.459 | 32 GB | ✅ 28/32 fits |
| 3 | **1×3090 (no-STT)** | $0.130 | 24 GB | ❌ STT disabled → routes to gemini-stt |

- **1×4090 dropped** (operator): 24 GB = same wall as 3090 (no GPU STT), and at $0.378/h it's pricier than 2×3090 ($0.229) which DOES fit STT — strictly dominated. (Also 2×4090 ≈ $0.75 > 1×5090 $0.46.)
- 2×3090 is both the cheapest STT-capable shape AND the most VRAM — the right #1.

## Implementation (3 parts)

1. **Gateway — N-level shape cascade.** Today the reconciler/Vast search has 2 levels (`PRIMARY_VAST_*_PRIMARY` / `_FALLBACK`). Extend to an ordered list of shapes (gpu_name, num_gpus, price_cap, allowlist/blocklist per shape). Provision tries shape 1; on no-offer/health-fail, advances to shape 2, then 3. Per-shape price caps: 2×3090 ~$0.40, 1×5090 ~$0.80, 1×3090 ~$0.30.

2. **Pod — VRAM-adaptive whisper (one image, all shapes).** onstart detects total VRAM (`nvidia-smi --query-gpu=memory.total`): if ≥ ~30 GB (2×3090=48, 5090=32) → set `WHISPER__INFERENCE_DEVICE=cuda` + pin `device_index` to the GPU with headroom (NOT the one holding Qwen → avoid CUDA OOM); if < 30 GB → disable whisper (don't start the speaches whisper model / don't advertise STT). Remove the hardcoded `WHISPER__INFERENCE_DEVICE="cpu"` from supervisord.conf:46. Pin device to dodge the 1×3090 OOM that forced CPU originally.

3. **Gateway — conditional STT override (extends SEED-018).** Only `OverrideTier0("stt", podURL)` when the pod actually serves whisper (VRAM-capable shape). On the no-STT shape, do NOT override STT → it falls to tier-1 gemini-stt. Needs the pod to signal STT-availability (e.g. a readiness/health field, or the gateway probes :8001 for the model) rather than the current hardcoded llm/stt/tts override set (reconciler.go).

## Validation (real Vast UAT)

- Per shape: provision → assert 4-endpoint Ready → STT real-audio (~10 s clip) returns < ~5 s on 2×3090/5090 (GPU); on 1×3090 assert STT routes to gemini (200, not pod).
- Assert no CUDA OOM on 2×3090 (whisper device-pinned off the Qwen GPU).
- Cost: confirm cascade picks 2×3090 first; falls to 5090 then 3090 only on no-offer.

## Sequencing

- **Immediate mitigation (decouple from the project):** ship the conditional-STT-override piece first as a flag (`PRIMARY_POD_SERVE_STT=false` on the current 1×3090) → STT routes to gemini NOW, unblocking the 60 s-timeout failures while parts 1+2 are built.
- Then parts 1 (cascade) + 2 (VRAM-adaptive pod) + flip part 3 to VRAM-driven.

Scope = phase-sized (gateway code + pod image + 3-shape live UAT on real Vast spend).
