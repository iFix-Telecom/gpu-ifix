### Phase 06.8: Multi-pod GPU topology + sizing + STT fix (INSERTED)

**Goal:** Make the gateway provision + health-poll the primary pod across multiple GPU topologies (preferring a 2×RTX 3090 single-pod, ~60% cheaper than a single 5090 with deeper Vast inventory) via runtime env (PRIMARY_NUM_GPUS=2 + allowlist), prove it end-to-end with a live force-up UAT, and fix the STT model-resolution bug (whisper tarball → HF-hub-cache layout + HF_HUB_CACHE) that blocks /v1/audio/transcriptions on every topology. Decides the GPU shape the SEED-002 emergency hot-standby will mirror, so it runs before SEED-002.
**Requirements**: STT-FIX, GW-2GPU, LADDER
**Depends on:** Phase 6, Phase 06.6 (primary pod Strategy B image + reconciler), Phase 06.7 (STT/speaches stack)
**Plans:** 2/4 plans executed

Plans:

- [x] 06.8-01-PLAN.md — Wave 1: STT fix prep — regenerate whisper tarball in HF-hub-cache layout (upload-weights.sh) + HF_HUB_CACHE on [program:speaches] (supervisord.conf)
- [ ] 06.8-02-PLAN.md — Wave 2: STT live-pod validation gate (rebuild image, spin pod, assert /v1/audio/transcriptions 200, propagate new SHA) — CLAUDE.md anti-blind-commit gate
- [ ] 06.8-03-PLAN.md — Wave 3: Gateway 2×3090 live-UAT (A2 search pre-check + gatewayctl primary force-up + 4-endpoint health + nvidia-smi split) → SEED-002 shape input
- [x] 06.8-04-PLAN.md — Wave 1: Fallback topology ladder runbook + per-shape env presets (2×3090 → 5090 → Shape C deferred)
