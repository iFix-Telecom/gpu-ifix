# Phase 21 — HANDOFF (retomar em outra sessão)

## ✅ COMPLETA — 2026-07-18

Todos os gates passaram + deployado em prod. Resumo:

- **VRAM-GATE ✅ (live 1×3090, Vast 45232952/manual 35215243):** Qwen 19662MiB + whisper cuda 4050MiB = **23727/24576 MiB, ~849MiB margem**, sem CUDA OOM, llama não crasha, transcrição na GPU. onstart log `total VRAM 24576 >= 22000 ... whisper device=cuda`. Margem real ~849MiB (< ~2GB do comentário; fina mas estável — sem folga p/ aumentar ctx qwen ou re-adicionar TTS).
- **Integration tests ✅ (commit `5824b0a`):** c2e6408 mudou o contrato p/ 3-endpoint mas deixou 5 integration tests (testcontainers) assertando 4-endpoint/4-service → `build-gateway` falhava e gateava o push da imagem. Alinhados (probe/supervisord/restart_recovery → llm/stt/dcgm, 2 overrides). Unit flake `TestChatProxy_SSEStreamingFlushesPerChunk` (rerun resolveu).
- **DB-01 ✅:** `gatewayctl upstreams disable --name local-tts` (enabled=false, persistiu redeploy). kokoro-tts tier1 segue enabled (fora de escopo).
- **Gateway deploy ✅:** stack Portainer 38 → `ifix-ai-gateway:develop-1f6e6a5@sha256:4e0f29bd4075…` (threshold 22000 + roster {llm,stt} + 3-endpoint + fix FF-02 abaixo). `/health`=200. (Antes `develop-5824b0a`.)
- **Fix FF-02 ✅ (commit `1f6e6a5`, pós-smoke):** `expectedWeightFiles` 4→3. c2e6408 tirou o download do chatterbox (3 downloads agora: qwen+whisper+bge-m3) mas deixou 4 → o detector de stall FF-02 (`okCount >= expectedWeightFiles`) nunca disarmava (okCount peak 3<4) → após downloads, bytes congelam → **regime-3 stall false-trip mata pod saudável** (+ `downloadDoneAt` nunca ancora o port-bind budget). Só host rápido (Ready dentro do stall budget) escapa — o smoke NL mascarou. Testes FF-02 alinhados (ThreeOfFourOk→TwoOfThreeOk, FourthOk→ThirdOk).
- **Pod template ✅:** `PRIMARY_TEMPLATE_IMAGE` `:main` → `converseai-primary-pod@sha256:f14c603675b7…` (digest validado, mesma convenção digest-pin do gateway).
- **Smoke STT-local ✅:** edge `/v1/audio/transcriptions` (tenant transcricao-voip, model=whisper) → HTTP 200 `{"text":"Thank you."}` 1.0s → `billing_events`: `route=stt upstream=emergency_pod_stt audio_seconds=3 cost_external_brl=0` (servido pelo pod local, in-house LGPD OK). Override log: `role=stt` + `role=llm` (2, SEM tts) = roster Phase 21 confirmado em prod com pod real.
- **Teardown ✅:** pod force-down, 0 instâncias Vast, config knobs temporários restaurados (cap_primary 0.2, schedule_disabled false, force_machine_id 0).

**GOTCHAs desta sessão (p/ próximas provisões manuais):**
- `PRIMARY_POD_SCHEDULE_DISABLED` no **env do stack = true** mas **DB pod_config (autoritativo) = false**. Sábado = schedule_days mon-fri → schedule drena force-up'd pod se `schedule_disabled=false`. Pra segurar pod fora de janela: PATCH `schedule_disabled=true` via `/admin/primary/config`.
- Provisão market_cheapest pega hosts CN baratos ruins (IP blocklist / download lento / TCP-unreachable nas portas mapeadas). **Solução:** `force_machine_id` (PATCH config) pinando machine US/EU verificado com net alto (achado via Vast bundles API `inet_down desc`, filtrar geo≠CN). NL machine 123798 subiu Ready em ~4.5min.
- `gatewayctl upstreams list` mostra `local-stt` probe=**failed** mesmo com pod servindo (probe bate no env estático, não na URL de override). Confirmar serving por `billing_events.upstream=emergency_pod_stt`, não pela probe.
- PATCH admin config: body `{"kind":"config","field":"<f>","value":<v>}` (um campo por call).

---

**Estado 2026-07-17 (histórico):** código DONE + commitado + pushado. Parado no **VRAM-GATE** (teste live de hardware). Falta: validação OOM em 1×3090 → DB-01 → promover imagem → deploy gateway → smoke.

## O que já foi feito (commit `c2e6408`, branch develop, PUSHADO)

Reverte o eixo TTS da Phase 06.7. Removeu Chatterbox TTS do pod (zero uso 90d, comia ~5GB VRAM forçando whisper→cpu) e habilitou whisper na GPU em 24GB.

- **Pod image:** `supervisord.conf` sem `[program:chatterbox]`; `Dockerfile` sem venv chatterbox/shim/dirs; `onstart.go` sem download chatterbox + threshold `WHISPER_GPU_THRESHOLD_MIB` **30000→22000**.
- **Gateway:** roster `{llm,stt,tts}→{llm,stt}`, health-gate **4→3 endpoints** (llm/stt/dcgm), sem override/restore/roster/buildPodURLs tts, sem `-p 8003`, sem env chatterbox. `lifecycle.go`+`reconciler.go`.
- **Testes:** contrato revertido pra `{llm,stt}`/3-endpoint. **TODOS VERDES** (build + `go test ./internal/primary/ ./internal/emerg/vast/` + integration vet + gofmt).
- CI de `c2e6408`: `build-primary-pod` + `build-gateway` disparados no push (constroem `:develop`; prod usa `:main`, intacto).

## ⛔ VRAM-GATE (próximo passo — precisa hardware live + decisão humana)

**Objetivo:** provar Qwen+whisper juntos na GPU de 1×3090 (24GB) SEM CUDA OOM (~22GB, margem ~2GB). Foi OOM que a UAT-B pegou (2026-06-19).

1. Esperar `build-primary-pod` de `c2e6408` terminar (tag `converseai-primary-pod:develop-c2e6408`).
2. Provisionar um pod 1×3090 com essa imagem. **Como:** temporariamente apontar o gateway pra imagem nova via `pod_config`/env `PRIMARY_TEMPLATE_IMAGE=...:develop-c2e6408` num gateway de teste, OU provisionar manual via API Vast com o onstart. (Decidir o mecanismo — não regredir prod que roda `:main`.)
3. Validar ao vivo: `nvidia-smi` durante uma transcrição → whisper na GPU (VRAM sobe ~+4GB), `whisper_device=cuda` no `:9100`, llama NÃO crasha, pod chega a Ready (3-endpoint). Fetch onstart log: deve logar `total VRAM 24576 >= 22000 ... whisper device=cuda`.
4. Se OOM/crash → reverter (threshold volta 30000 OU só rodar em shape ≥30GB). Não promover.

## Pós-gate (quando VALIDAR ok)

5. **DB-01:** `UPDATE ai_gateway.upstreams SET enabled=false WHERE name='local-tts'` no DB `bd_ai_gateway` (reversível). NOTIFY hot-reload.
6. Promover imagem pod pra `:main` (o reconciler provisiona `PRIMARY_TEMPLATE_IMAGE=converseai-primary-pod:main`) — rebuild/retag `:main` OU apontar o template pro tag validado.
7. Rebuild+deploy gateway (PUT stack Portainer 38, digest-pinado — ver memory `gateway-prod-deploy-mechanism`).
8. Smoke: STT via edge `/v1/audio/transcriptions` → servido pelo **pod local** (não gemini-stt). Confirmar em `billing_events`: `upstream` deixa de ser gemini-stt no volume novo.

## Contexto (LOCKED)

- Decisões completas: `21-CONTEXT.md` (mesma pasta).
- Diagnóstico do episódio: memory `stt-cpu-and-billing-columns`.
- Custo STT tier-1 hoje = ~R$6,6/mês (ninharia); valor real = LGPD (áudio de call in-house) + latência.
- Deixado inerte (fora de escopo): `/v1/audio/speech` serving path (main.go tts role proxy) — zero tráfego.
- Harness NÃO tem subagents GSD → execução foi inline (não via execute-phase).

## Texto pra retomar em nova sessão

> Continua a Phase 21 do gpu-ifix (strip TTS + whisper GPU 24GB). Lê `.planning/phases/21-strip-tts-from-primary-pod-enable-whisper-on-gpu-for-1x3090-/21-HANDOFF.md`. Código já commitado+pushado (`c2e6408`, develop). Estamos no VRAM-GATE: checa se o build-primary-pod de c2e6408 terminou, e conduz o teste live de VRAM em 1×3090 (Qwen+whisper na GPU sem OOM). Se passar, segue o pós-gate (DB-01 disable local-tts, promover imagem, deploy gateway stack 38, smoke STT-local).
