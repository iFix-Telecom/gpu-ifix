# HANDOFF — Fase 06.7 (gpu-ifix): primary pod TTS swap + UAT ao vivo

**Data:** 2026-05-21 · **Repo:** `/home/pedro/projetos/pedro/gpu-ifix` · **Branch:** `develop`

## Estado atual
Limpo. Nenhum pod rodando, **Vast=0**, sem gasto. Todos os fixes commitados/pushados no `develop`.

## UAT ao vivo (Vast 5090) — 5/6 PASS pelo gateway
- ✅ **S1** Chatterbox carrega no 5090 (~22GB VRAM com LLM+STT+TTS, 10GB livre)
- ✅ **S2** voice-clone zero-shot (WAV 24kHz)
- ✅ **S3** voz sobrevive troca de pod (refetch do WAV no MinIO/S3, sem `.pt` por-voz; apenas `.pt` do modelo base)
- ✅ **S4** Piper fallback (WAV 16kHz) · ✅ **S5** embed 24/7 (1024-dim)
- ✅ Validação extra pelo gateway: LLM `/v1/chat/completions` ("Brasília", GPU ~77 tok/s) + TTS + embed OK
- ⏸ **S6** (Pitfall #11) deferido — emerg vai ser redesenhado (ver SEED-002)
- ❌ **STT `/v1/audio/transcriptions` = HTTP 500** — bug de config do POD: speaches faz `scan_cache_dir` em `/root/.cache/huggingface/hub` (não existe) → falha ao resolver model. **Roteamento do gateway OK** (chega no `emergency_pod_stt`). Fix: setar `HF_HUB_CACHE`/cache_dir ou model name correto no pod. NÃO é bug do gateway.

Registro detalhado: `.planning/phases/06.7-*/06.7-HUMAN-UAT.md`.

## Fixes shipados nesta sessão (~14, no develop)
1. CI: sqlc gen sync + gofmt main.go
2. Piper adapter manda **JSON** em `/tts` (voice-api Piper usa json, não form)
3. torch **cu128** (RTX 5090 Blackwell sm_120 — cu124 não tem kernel)
4. **CUPTI**: cu128 precisa do runtime nvidia-cuda-*-cu12 12.8 (removido `--no-deps`)
5. **setuptools<81**: resemble-perth usa `pkg_resources` (removido em setuptools 81+) → PerthImplicitWatermarker None
6. audit não trunca mais o multipart das rotas `/v1/audio/*` (isAudioRoute = prefixo)
7. **Proxies dinâmicos `emergency_pod_{tts,llm,stt}`** (`NewDynamicOverrideProxy`): loader.Resolve com override devolve nome `emergency_pod_<role>`, que não tinha proxy registrado → 503. Antes só funcionava pod-direct, não pelo gateway.
8. Seleção de host Vast: removido `verified` + `geolocation` EU; cap `PRIMARY_VAST_PRICE_CAP_DPH` 2.20→1.00; novo env `PRIMARY_VAST_MACHINE_BLOCKLIST` + log de machine_id

## Catálogo de hosts Vast CDI-quebrados
Blocklist atual (env no `.env` de `/opt/ai-gateway-dev/`): **`36773,53128,38389,51466`**.
Multi-GPU com CDI quebrado — falham no container create com "unresolvable CDI devices gpu=N" (alguns até gpu=0). Blocklist é env, atualizável sem rebuild (`docker compose up -d`).

## Dados de custo Vast (qualificado p/ cu128: reliab≥0.99, cuda≥12.8, driver≥570, inet≥500)
| GPU | VRAM | $/h mín | Ofertas |
|-----|------|---------|---------|
| RTX 5090 | 32GB | **$0.40** | 15 (melhor custo single-pod) |
| RTX 4090 | 24GB | $0.14 | 15 (não cabe stack completo) |
| L40S | 45GB | $1.21 | 1 |
| A6000 / A40 / 6000 Ada | 48GB | — | 0 qualificadas |

## DECISÕES ABERTAS
1. **Custo (prioridade do operador):** single 5090 ($0.40/h, cabe tudo) **vs** split 2×4090 ($0.28/h, ~30% menos + redundância). Falta validar se **Qwen 27B Q4 cabe sozinho num 4090 24GB** (~18GB LLM + KV cache de 16k ctx pode apertar). Couber → split é o menor custo; senão LLM precisa de 5090 e split fica mais caro.
   - Split: Pod A = LLM (Qwen, ~18GB) no 4090; Pod B = STT+TTS (~8GB) num 4090/GPU menor.
   - Carga medida no 5090: ~22GB com os 3 serviços juntos.
2. **STT HF_HUB_CACHE** — fix de config do pod speaches (separado do gateway).
3. **SEED-002** (`.planning/seeds/SEED-002-*.md`): redesenho travado — **emerg = pod-completo hot-standby no 5090** (4 serviços, config idêntica ao primary: imagem+weights+onstart+GPU+cap+blocklist+filtro) + **embed volta pro pod tier-0 GPU / CPU 24/7 tier-1** (reverte D-03) + tier-1 por papel (LLM→OpenRouter, STT→OpenAI Whisper, TTS→Piper, embed→CPU) + setar envs externos. Vira fase planejada (`/gsd:plan-phase`).

## Infra / como operar
- Gateway dev: `/opt/ai-gateway-dev/docker-compose.yml` na **vps-ifix-vm** (compose, NÃO Portainer). Env no `.env` ali.
- Provisionar: `ssh vps-ifix-vm 'docker exec ai-gateway-dev /gatewayctl primary force-up --reason X'`; estado: `... primary state`; derrubar: `... primary force-down`.
- Pod SSH: via Vast public IP + porta 22/tcp mapeada (`curl .../instances/`).
- MinIO: alias `mc` `ifix` no ops-claude (`mc ls ifix/ai-gateway/voices/`).
- API key teste: `ifix_sk_3oesqm2qoiy3igzcrgtznalzmlgcbvxv` (tenant converseai).
- Build: push no `develop` → GHA `build-gateway.yml` / `build-primary-pod.yml` → webhook. Gateway dev NÃO puxa sozinho (compose) → após build: `cd /opt/ai-gateway-dev && docker compose pull && docker compose up -d`.
- CI flaky conhecido: `TestHandleForceUpRequest_TransitionsAsleepToProvisioning` (corrida; `gh run rerun --failed` resolve).

## Próximos passos sugeridos
1. Validar Qwen-only num 4090 (1 pod ~$0.14, medir VRAM) → decidir single vs split.
2. Fix STT `HF_HUB_CACHE` no pod.
3. `/gsd:plan-phase` do SEED-002.
4. Registrar `06.7-09-SUMMARY.md` + `06.7-VERIFICATION.md` (5/6 PASS).
