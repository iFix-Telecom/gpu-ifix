# Phase 20: Primary-pod provisioning resilience — coldstart fast-fail + auto-blocklist/allowlist - Context

**Gathered:** 2026-07-10
**Status:** Ready for planning
**Source:** SEED-009 companion problem 1 (`.planning/seeds/SEED-009-auto-blocklist-vast-machine-on-repeated-port-bind-fail.md`) + discussão 2026-07-10 (economia do pod vs tokens)

## Motivação (business framing)

Pod NÃO é always-on — schedule-down pra economizar GPU (decisão do usuário 2026-07-10). Logo cold start ACONTECE todo ciclo e PRECISA ser resiliente. Objetivo: validar rápido bom/ruim do pod sem babá humana. Economia só existe se o pod absorver o gasto externo Gemini (~R$800/mês) + OpenRouter (~R$275/mês) — ver `memory coldstart-pod-economics`. STT (Whisper local, grátis) é o primeiro offload de menor risco. Esta phase é pré-requisito operacional: sem fast-fail, cada tentativa de subir o pod queima até 60min (coldstart_budget_s=3600) num container morto.

## Os 3 regimes de falha (do seed, observados 2026-07-06)

| Regime | Sintoma | Sinal externo | Budget |
|--------|---------|---------------|--------|
| 1. Host morto | preso em Vast `actual_status=created`, onstart mudo (Noruega 24953) | tempo-em-`created` via GetInstance | `created_budget_s` ~120s |
| 2. Pull lento | `actual_status=loading`, progride (host 43503, "Pulling" 15min) | actual_status avança | deixa correr até `coldstart_budget_s` (teto absoluto, já existe) |
| 3. Stall no download | container up, download de pesos congela DENTRO do onstart — invisível pro Vast | heartbeat do onstart log parou | `progress_stall_budget_s` ~120s |

Ordem no `pod/onstart.sh`: preflight → **download pesos (linha 97, BLOQUEANTE)** → `docker compose up` (linha 105, só aqui sobe health-bridge :9100) → espera /health/ready. **Download acontece ANTES de running / do health-bridge existir** → durante o download o único sinal externo é `actual_status` + o onstart log (`/var/log/onstart.log`, tee'd → console Vast).

## Decisões TRAVADAS

1. **Fonte de progresso = cadeia de fallback (FF-03).** Regimes 1/2: `status_msg`+`actual_status` do `GetInstance` (GRÁTIS, já buscamos no loop). Regime 3: heartbeat do onstart log via **(a) Vast logs API primário** (`PUT /api/v0/instances/request_logs/{id}/` → `result_url` → GET; endpoint confirmado existir + auth ok 2026-07-10, async ~segundos) **→ (b) SSH tail `/var/log/onstart.log` fallback** (vast-ai.sh já tem `ssh-exec`; SSH do host publica cedo, independe do onstart). Uma falha → tenta a outra. Ambas leem o mesmo log.
   - **SPLIT FORMAL pós-review codex (2026-07-10):** só o leg (a) Vast logs API entra NESTA phase; o leg (b) SSH-tail é **follow-up deferido** (`ponytail:` — sem SSH em Go hoje). Seguro porque a fix de telemetria (decisão 8 abaixo) faz `OnstartLog` vazio/fetch_error = **UNKNOWN, não mata** → logs-API caído só faz regime-3 correr até o `coldstart_budget_s` ceiling (default seguro), nunca false-kill. FF-03 = "logs API primário (phase 20) + SSH follow-up (deferido)".

2. **REUSA `CountConsecutiveFailedPrimaryProvisions` (BL-01).** Já existe (`primary_lifecycles`, DB-backed) + política `fail_streak<2` mercado / `>=2` allowlist (quick 260702-nse / Phase 17). NÃO criar contador in-memory novo — era proposta original, DESCARTADA por redundância. Gap = auto-POPULAR as listas, não contar.

3. **Auto-blocklist na falha, auto-allowlist no sucesso.** Falha (regime 1/2/3, estado terminal, budget estourado) → machine_id → blocklist **+ remove da allowlist**. Sucesso (first_health_pass) → machine_id → allowlist **+ remove do blocklist**. Não são listas independentes (host bom-que-degradou não pode ficar nas duas).

4. **Blocklist: banir quando fail_streak≥2** (não na 1ª — evita banir host por hiccup transitório). Persiste em `pod_config.vast_machine_blocklist` (durável, dashboard vê, reusa `UpdatePodConfigFieldBlocklist`). **Sem TTL/expiry agora** — prune manual; marcar `ponytail:` com upgrade path (expiry 24h se a lista apodrecer).

5. **Allowlist: cap ~20, FIFO (dropa mais antigo) + dedup.** Query `UpdatePodConfigFieldAllowlist` espelha a de blocklist. Precisa remove-from-list nas duas.

6. **Budgets são dashboard-editáveis (CFG-01/UI-01).** `created_budget_s` + `progress_stall_budget_s` viram campos em `pod_config` com min/max (molde EXATO dos `coldstart_budget_s`/`port_bind_budget_s` existentes) + 2 cases PATCH em `config_write.go` + 2 sliders no dashboard (molde Phase 17). `coldstart_budget_s` continua como teto absoluto, inalterado.

7. **`download-weights.sh` dropa `mc cp --quiet` (OBS-11).** `--quiet` (linha 55) mata o progresso mid-file → um Qwen de 20GB baixa com 1 linha antes + 1 depois → regime 3 fica CEGO durante o arquivo. Sem heartbeat mid-file, FF-02 não detecta stall dentro de um arquivo grande. (ID renomeado OBS-01→OBS-11: OBS-01 já é requirement Complete da Phase 7.)

## Decisões pós-review codex (2026-07-10 — HIGH risk, fold via --reviews)

8. **Heartbeat carrega BYTES, não liveness (OBS-11 + FF-02).** Dropar `--quiet` + tick periódico ainda emite timestamp fresco com bytes congelados → transfer travado "prova progresso" pra sempre → stall NUNCA dispara. O loop emite `[download-weights] progress key=<k> bytes=<N>` (N = tamanho atual do arquivo parcial, `stat -c%s` ou `mc --json`); progresso = bytes SOBEM entre amostras. FF-02 avança `lastProgressAt` só quando `bytes` aumentou.

9. **FF-02 escopado à FASE de download.** Arma no 1º `[download-weights] fetching`, atualiza no crescimento de bytes, **DESARMA** quando todos os arquivos logam `ok`/onstart passa do download. NÃO roda o timer de stall através de compose/model-startup/health-ready — startup lento saudável após download bem-sucedido NÃO pode tripar `progress_stall_timeout`.

10. **Telemetria-indisponível = UNKNOWN, não mata (FF-02/FF-03).** `OnstartLog` retorna STATUS (`available|not_ready|fetch_error|empty`), não só texto. Stall dispara SÓ com `available` + bytes provadamente parados além do budget. `fetch_error`/`empty` (logs API caído/atrasado) → UNKNOWN → NÃO fast-fail; corre até o `coldstart_budget_s` ceiling (default seguro). É o que torna o SSH-tail deferível (decisão 1).

11. **Classificação de falha antes do auto-blocklist (BL-01).** Blocklistar em QUALQUER erro do `waitForReadyOrDestroy` envenena o picker. Blocklista SÓ razões atribuíveis à máquina: `created_state_timeout`, terminal/offline (`exited`/`unknown`/`offline`), port-bind/reachability repetido. PULA (sem mutar lista): `cancelled_in_flight`, `health_timeout`, shutdown de operador/schedule, erros de imagem/config/R2/auth, telemetria-indisponível, **e `progress_stall_timeout` (EXCLUÍDO de propósito)**. Razão: stall de download tem raiz no store de pesos compartilhado (R2) + rede — numa queda global de R2 TODO host trava → blocklistar no stall cascataria envenenando o mercado inteiro. FF-02 ainda **mata** o pod no stall (economiza $), mas NÃO **parka** o host. `ponytail:` upgrade path = correlação cross-lifecycle (este host travou enquanto outros subiram ⇒ atribuível ao host). AL-01 (allowlist no sucesso) inalterado.

## Escopo (ganho honesto — NÃO vender demais)

Allowlist = **host confiável** (passa provisioning), **NÃO pesos quentes**. Disco é destruído no down-cycle (schedule-down) → pesos re-baixam mesmo em host da allowlist. Ganho = evitar host morto/lento (regime 1/2/3) + fast-fail rápido, **não** eliminar o cold-pull (tempo de download). Matar o download exigiria disco persistente = rejeitado pelo usuário.

## Escopar CONTRA (já entregue — não duplicar)

- **Phase 17** já tornou `blocklist/allowlist/coldstart_budget/port_bind_budget` editáveis no dashboard (pod_config já tem os min/max desses). Esta phase ADICIONA 2 campos, não recria.
- **Phase 6.6.Y** já fez fail-fast de endpoint-reachability (bounded wait). Verificar se cobre parte do regime 1/2 antes de adicionar.
- **Phase 12** (SEED-011/012) já fez death-detection no Ready-loop steady-state. Regime aqui é COLDSTART (pré-ready), distinto — confirmar não-overlap.
- `CountConsecutiveFailedPrimaryProvisions` + `allowlist_preferred` picker (260702-nse) — reusar.

## Pontos de código mapeados

> **CORREÇÃO (RESEARCH 2026-07-10):** o reconciler do primary é `gateway/internal/primary/` (reconciler.go, lifecycle.go), NÃO `emerg/` (esse é o emergency pod, outro path). Hook único BL-01+AL-01 = `primary/reconciler.go:1438` (`waitForReadyOrDestroy` return, `offer.MachineID` em escopo). FF-01 gap = `reconciler.go:1587` (`continue` nu no `created`). Ver 20-RESEARCH.md pro mapeamento file:line completo.

- `gateway/internal/primary/` (reconciler FSM do primary): `lifecycle.go` + `reconciler.go` (loop coldstart, desfecho), `emerg/vast/client.go` (add RequestLogs/FetchLogs — client Vast compartilhado), `emerg/vast/types.go` (Instance já tem StatusMsg + ActualStatus).
- `gateway/internal/db/gen/pod_config.sql.go` + migration (goose) + `config_write.go` (PATCH cases).
- Dashboard (Next.js, `dashboard/`) — sliders molde Phase 17 pod-config.
- `pod/scripts/download-weights.sh:55` (--quiet), `pod/onstart.sh` (ordem download→compose).
- `pod/scripts/vast-ai.sh` — `wait-running`, `ssh-exec`, `status` (referência de shape; o gateway Go é o que muda em prod, não o bash).

## Nyquist / validação

Fast-fail é lógica com timing — precisa teste. Regimes distinguíveis por `actual_status`. Validação real exige um coldstart vivo (Vast) — HUMAN-UAT provável (subir pod real, forçar regime 1 via host ruim / regime 3 via R2 cred quebrada, medir que mata em ~budget não em 3600s). Unit: contador de time-in-state + stall-detect + fallback-chain + list add/remove/dedup/cap.

## Claude's Discretion

- Wave/plan split.
- Nomes exatos dos campos min/max (seguir convenção existente).
- Formato do result_url da Vast logs API (validar shape no primeiro coldstart real).
- Se FF-03 (a) Vast logs API provar não-confiável no UAT, cair 100% no (b) SSH-tail.
