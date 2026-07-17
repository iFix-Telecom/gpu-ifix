# Phase 21: Strip TTS from primary pod + enable whisper on GPU (1x3090/24GB) - Context

**Gathered:** 2026-07-17
**Status:** Ready for planning
**Source:** Live investigation session (STT-on-pod diagnosis 2026-07-17)

<domain>
## Phase Boundary

Reverte a Phase 06.7 (embed→tts swap) no eixo TTS: remove o Chatterbox TTS do pod primary e habilita o whisper (STT) na GPU num shape de 1×3090 (24GB). Resultado: STT roteia LOCAL (tier-0 `local-stt`) em vez de tier-1 (gemini-stt/openai-whisper).

**Em escopo:** pod image (supervisord.conf, Dockerfile, onstart.go threshold), gateway roster tier-0 {llm,stt,tts}→{llm,stt} + health-gate 4→3 endpoints, testes, DB upstream `local-tts`.

**FORA de escopo:** trocar arquitetura pra `ifix-ai-pod` (compose — não bootável pelo gateway atual). Reintroduzir TTS. STT streaming/real-time (SEED-009). Mexer no fluxo de embed (já off-pod desde 06.7, static tier-0 row).
</domain>

<decisions>
## Implementation Decisions (LOCKED nesta sessão)

### Motivação (por que fazer)
- TTS/Chatterbox = ZERO uso em 90 dias (FATO: `billing_events`, nenhum upstream tts/chatterbox jamais; 30d rotas = chat 25678 / stt 20293 / embed 200). É peso morto comendo ~5GB VRAM.
- Valor real NÃO é custo (STT tier-1 = ~R$6,6/mês, medido em `cost_external_brl` — ninharia). É **LGPD** (áudio de call de cliente hoje vai pro Gemini/OpenAI) + latência.

### VRAM (o que cabe)
- Orçamento MEDIDO (FATO, 06.8-RESEARCH spike machine 43803, 2×3090): Qwen ~18GB (llama.cpp -ngl 99 tensor-split) + whisper +4GB (12247→16302 MiB medido) = 22GB. O 3º serviço (TTS ~5GB) empurrava pra ~27GB — daí o threshold 30000.
- Qwen+whisper = 22GB → **cabe em 24GB com ~2GB de margem** (apertado).

### Pod image (converseai-primary-pod, imagem única supervisord)
- Remover `[program:chatterbox]` do `pod/primary/supervisord.conf` (linha ~107).
- Remover venv chatterbox do `pod/primary/Dockerfile` (~2.8GB de imagem).
- `gateway/internal/primary/onstart.go`: remover download+extração do peso chatterbox; baixar `WHISPER_GPU_THRESHOLD_MIB` de 30000 pra ~22000 (24576 passa a qualificar → `whisper device=cuda`). Escolher 22000 (não 24000) pra margem contra variação de leitura de nvidia-smi.

### Gateway (reverter roster)
- `lifecycle.go`: remover campo `TTS` de primaryPodURLs, `podTTSURL`, `roleURL "tts"`, port-forward `-p 8003:8003`, drain counting de local-tts; health-check de 4→3 endpoints (LLM+STT+DCGM, sem TTS).
- `reconciler.go`: markReady + recoverOpenLifecycle sem `OverrideTier0("tts")`; evaluateReady re-assert loop {llm,stt} (sem tts); buildPodURLs sem TTS.
- Roster dinâmico tier-0 volta a {llm, stt} (como era antes da 06.7).

### Testes (GW-02)
- A Phase 06.7 atualizou ~6 arquivos de teste pro contrato tts/8003 (reconciler_test, lifecycle_test, primary_probe_test, primary_supervisord_test, primary_restart_recovery_test). Reverter todos pro contrato {llm,stt} / 3-endpoint.

### DB
- `ai_gateway.upstreams`: `UPDATE SET enabled=false` (ou DELETE) na row `local-tts`.

### Claude's Discretion
- Manter ou não os assets/venv do chatterbox na imagem como código morto vs remoção total (recomendo remoção pra economizar ~2.8GB de imagem + ~5GB de download de peso no coldstart).
- Valor exato do threshold (22000 recomendado; range seguro 22000-23000).
- Se desabilita (enabled=false, reversível) vs DELETE a row local-tts (recomendo enabled=false — reversível).
</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Pod image
- `pod/primary/supervisord.conf` — `[program:chatterbox]` a remover; `[program:llama|speaches|dcgm]` a manter
- `pod/primary/Dockerfile` — stage/venv chatterbox
- `gateway/internal/primary/onstart.go` — `WHISPER_GPU_THRESHOLD_MIB=30000` (linha ~256), download/extração chatterbox

### Gateway
- `gateway/internal/primary/lifecycle.go` — primaryPodURLs.TTS, podTTSURL, roleURL, buildCreateRequest (port 8003), buildPodURLs, 4-endpoint health
- `gateway/internal/primary/reconciler.go` — markReady, recoverOpenLifecycle, evaluateReady re-assert, 4-endpoint gate

### Referência da mudança que se reverte
- `.planning/phases/06.7-primary-pod-tts-swap-embed-for-kani-tts-2-pt-gpu-move-bge-m3/06.7-08-SUMMARY.md` — o swap embed→tts (o que reverter, exceto o embed que fica off-pod)
- `.planning/phases/06.8-multi-pod-gpu-topology-sizing-stt-fix/06.8-STT-LIVE-VALIDATION.md` — validação STT na GPU (em 48GB — precisa nova em 24GB)
- `.planning/phases/14-*/` (SEED-019) — origem do threshold VRAM-adaptive
- memory `stt-cpu-and-billing-columns` — diagnóstico completo desta sessão

### Deploy
- Rebuild pod: `.github/workflows/build-primary-pod.yml` (fire on push `pod/primary/**`)
- Rebuild gateway + PUT stack Portainer 38 (memory `gateway-prod-deploy-mechanism`)
</canonical_refs>

<specifics>
## Specific Ideas

- Prod hoje: `PRIMARY_TEMPLATE_IMAGE=ghcr.io/ifixtelecom/converseai-primary-pod:main`, onstart gerado em Go (`buildPrimaryOnstart`), `exec supervisord`. Reconciler provisiona shape RTX 3090 (shape0/shape1), cap_primary=0.20.
- O gate atual: `reconciler.go:927` só toma `OverrideTier0("stt")` se `whisper_device=="cuda"`. Com threshold baixado, o pod 1×3090 passa a reportar cuda → gate abre → STT local.
</specifics>

<deferred>
## Deferred Ideas

- STT streaming/real-time (SEED-009) — arquitetura diferente, fora daqui.
- Trocar pra `ifix-ai-pod` (compose) — exigiria reescrever o path de provisioning (DinD rejeitado). Não fazer.
</deferred>

---

*Phase: 21-strip-tts-from-primary-pod-enable-whisper-on-gpu-for-1x3090-*
*Context gathered: 2026-07-17 via live investigation session*
