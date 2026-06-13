---
seed: SEED-010
title: Shrink primary pod — remove Speaches/Whisper STT tier-0
status: ready-to-promote
priority: medium
phase_target: 11.5 or 12
captured_at: 2026-06-04
captured_by: pedro
related: [SEED-002 (emergency hot-standby), SEED-009 (realtime voice), Phase 06.6/06.7/06.8 pod template]
---

# SEED-010 — Shrink primary pod removendo Speaches/Whisper STT

## Intent

Remover serviço Speaches (faster-whisper STT) do pod primary template. Tier-1 OpenAI whisper-1 cobre demanda batch atual; PROCESSADOR-IA workflow já roteia via gateway que cai natural pra tier-1 quando primary down. STT real-time (SEED-009) demandará arquitetura completamente diferente (streaming WebSocket), não justifica manter whisper batch no pod.

## Ganho esperado

| Métrica | Antes | Depois | Delta |
|---------|-------|--------|-------|
| Image size | ~10-12GB | ~7-8GB | -2-3GB venv+assets |
| Cold-start weights download | ~21GB | ~16GB | -5GB whisper-large-v3 |
| VRAM running | ~25-27GB | ~22-24GB | -3-4GB GPU offload |
| GPU requirement | 2× RTX 3090 (48GB) OR 1× 5090 (32GB) | **1× RTX 3090 (24GB) cabe** | -GPU |
| Custo Vast/h | $0.43-0.60 | ~$0.20-0.30 | **-50%** |
| Cold-start time | ~10-12min | ~7-8min | -3min |

Pod fica focado em LLM (Qwen3.6-27b) + TTS (Chatterbox multilingual) + DCGM exporter.

## Mudanças concretas

### Pod side
1. `pod/primary/Dockerfile`
   - Remover Stage 1b `speaches-assets` (FROM ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3)
   - Remover `/opt/speaches-venv` install (pip install speaches + onnx-asr + gradio + faster-whisper)
   - Remover COPY do silero_encoder_v5.onnx + silero_vad_v5.py assets
   - Remover mkdir `/opt/speaches-data/realtime-console/dist` + stub index.html
   - Avaliar limpar também `/opt/infinity-venv` (rollback dead-code, Phase 06.7 D-03)

2. `pod/primary/supervisord.conf`
   - Remover `[program:speaches]` block inteiro

3. `pod/onstart.sh`
   - Remover download/extração whisper tarball para `/weights/whisper`
   - Remover `mkdir /weights/whisper`

4. `pod/.env.example`
   - Remover `WHISPER_*` / `HF_HUB_CACHE` whisper-related vars

5. `pod/docker-compose.yml`
   - Remover port mapping `8001:8001` (Speaches HTTP)
   - Remover env vars whisper

6. `pod/weights/README.md` + `pod/README.md`
   - Docs update — STT não roda no pod

7. `pod/health-bridge/main.go`
   - Remover probe de `:8001/health` Speaches

### Gateway side
8. `ai_gateway.upstreams` SQL — `UPDATE SET enabled=false WHERE name='local-stt'` OU DELETE row inteira
9. Reconciler / scheduler — verificar se assume `local-stt` ainda existe (provavelmente sim, code path role=stt esperando tier-0)
10. Probe loop — confirmar que probe `local-stt` deixa de rodar quando upstream disabled
11. Audit — confirmar que requests `/v1/audio/transcriptions` continuam funcionando via tier-1 OpenAI direto (sem ficar travadas tentando tier-0)
12. `model_aliases` — manter row `(whisper, local-stt) → Systran/faster-whisper-large-v3` ou limpar?
13. `.env` prod ai-gateway — pode limpar `UPSTREAM_STT_URL=http://10.10.10.20:8001` (não mais usado)

### CI/CD
14. `.github/workflows/build-primary-pod.yml` (se existir) — verificar que build não testa speaches
15. Smoke tests pós-deploy — remover testes STT tier-0; manter testes STT tier-1 OpenAI

## Risk

| Risco | Mitigação |
|-------|-----------|
| Workflow PROCESSADOR-IA falhar quando primary up | Tier-1 fallback continua, mas latência primeira call +1-2s (breaker abre) — aceitável |
| Custo OpenAI whisper-1 sobe | Mensurar audit_log 7d antes/depois. $0.006/min vs Vast $0.20-0.60/h. Ponto de break-even ~30min STT/h pra valer manter pod |
| SEED-002 hot-standby reflete pod novo | Atualizar SEED-002 se Phase 11.5+ ainda na agenda |
| Probe / FSM contam STT no health check | Code review reconciler + scheduler antes deploy |

## Out of scope

- Não remove Chatterbox TTS — decisão separada após auditar volume TTS
- Não remove Infinity venv rollback (~600MB) — pode entrar no escopo se quiser limpar tudo

## Promotion checklist

- [ ] Phase 11 fechado (corpus + 11-06/07/08 deferred resolvidos OU mileston cap)
- [ ] Auditar volume STT prod 7d antes
- [ ] Definir phase: 11.5 dedicada ou consolidar com outra refactor
- [ ] Plan-phase via /gsd:plan-phase com Mudanças (1-15) acima como tasks

## References

- Pod template: `pod/primary/Dockerfile` + `pod/primary/supervisord.conf`
- Phase 06.6 (primary pod refactor Strategy B)
- Phase 06.7 (kani-tts swap, embed off-pod — D-03 padrão)
- Phase 06.8 (multi-GPU + STT fix)
- Gateway STT director: `gateway/internal/proxy/audio.go` + `openai_whisper_director.go`
