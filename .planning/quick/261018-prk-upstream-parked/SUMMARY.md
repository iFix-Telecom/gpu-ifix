# Quick 261018-prk — upstream tier-0 "parked" + incidente de emergência preso

ClickUp: [17u65kmhck0](https://app.clickup.com/t/17u65kmhck0) (subtarefa do card de desligamento dos pods, 86akuaa73).

## Problema (fatos, log do gateway de 18/10)

1. **E-mails de breaker.** `rerank-gpu`, `local-stt` e `kokoro-tts` (tier-0 da 3060, desligada em 14/10, `url_override=http://127.0.0.1:1`) precisam ficar enabled — sem tier-0 o dispatcher responde 503 "No primary upstream configured for role". A sonda falhava a cada ~30 s, o breaker abria e o alerter mandava e-mail Brevo (warning) a cada 5 min por upstream (`dedupTTL`). 18/10: 228–229 e-mails por upstream (~35/h no total).
2. **Alerta crítico "Emergency pod FSM → emergency_provisioning" a cada restart.** O espelho Redis `gw:emerg:state` estava em `emergency_provisioning` desde 15/10 00:53 BRT (`entered_at=1792036403` = início do lifecycle 967, encerrado 44 s depois com `cancelled_in_flight`). O FSM em memória estava `healthy`; o `reconcile-boot` lia o espelho e reenviava o alerta crítico com bypass de dedup a cada restart (4× em 18/10).

## Decisão / mudança

- **Espelho de emergência (dado, sem código):** `HSET gw:emerg:state state healthy lifecycle_id "" pod_url "" pod_instance_id "" entered_at <agora>` (mesmo formato de `WriteEmergState`). Restart das 20:40 logou `reconcile-boot: emergency FSM in a benign state`.
- **`circuit_config.parked` (PR #24, squash `ca026a7`):** tier-0 estacionado não é sondado; o dispatcher vai direto ao tier-1 sem discar nem tocar no breaker; sensível recebe o 503 RES-08 na hora; `dispatchPinned` não o inclui; `/health` mostra `parked`; override de emergência ativo vence. `gatewayctl upstreams update --name=X --parked=true|false` (merge no JSONB, NOTIFY recarrega sem restart).

## Deploy (18/10)

- 20:40:44 stack 38 → `develop-ca026a7@sha256:3520d76d…` (snapshot em `ops-claude:~/gw-parked-17u65kmhck0/`).
- 20:41:23 `--parked=true` em `rerank-gpu`, `local-stt`, `kokoro-tts` (`embed-gpu` já está disabled; `circuit_config` dos quatro era `{}`).
- Smoke: rerank 200 (1,28 s), TTS 200 (1,95 s), STT 200 (1,93 s, transcrição correta).
- 20:41:30–20:45: 0 e-mails, 63 sondas puladas, 1 transição residual `open→half-open` por upstream às 20:41:49 (cooldown anterior; info, sem e-mail).

## Hipóteses

- HIPÓTESE: o cancelamento em voo do provisionamento (`cancelled_in_flight`) não regrava o espelho `gw:emerg:state`. Não verificado no código; se for isso, o espelho volta a travar no próximo cancelamento. Evidência que resolve: ler o caminho de cancelamento em `internal/emerg/reconciler.go` e se ele chama `WriteEmergState`.

## Ao religar a 3060

`gatewayctl upstreams update --name=<rerank-gpu|local-stt|kokoro-tts> --parked=false` — senão o pod sobe e não recebe tráfego.

## Rollback

- Só o campo: `--parked=false` nos três (volta o comportamento antigo, com e-mails).
- Imagem: stack 38 → `develop-2fee2ca@sha256:0bd7bcbf…` (o campo `parked` é ignorado pela imagem antiga).
