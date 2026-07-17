# Phase 21 — HANDOFF (retomar em outra sessão)

**Estado 2026-07-17:** código DONE + commitado + pushado. Parado no **VRAM-GATE** (teste live de hardware). Falta: validação OOM em 1×3090 → DB-01 → promover imagem → deploy gateway → smoke.

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
