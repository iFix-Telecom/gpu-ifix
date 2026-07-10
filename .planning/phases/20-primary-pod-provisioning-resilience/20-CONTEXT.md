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

2. **REUSA `CountConsecutiveFailedPrimaryProvisions` (BL-01).** Já existe (`primary_lifecycles`, DB-backed) + política `fail_streak<2` mercado / `>=2` allowlist (quick 260702-nse / Phase 17). NÃO criar contador in-memory novo — era proposta original, DESCARTADA por redundância. Gap = auto-POPULAR as listas, não contar.

3. **Auto-blocklist na falha, auto-allowlist no sucesso.** Falha (regime 1/2/3, estado terminal, budget estourado) → machine_id → blocklist **+ remove da allowlist**. Sucesso (first_health_pass) → machine_id → allowlist **+ remove do blocklist**. Não são listas independentes (host bom-que-degradou não pode ficar nas duas).

4. **Blocklist: banir quando fail_streak≥2** (não na 1ª — evita banir host por hiccup transitório). Persiste em `pod_config.vast_machine_blocklist` (durável, dashboard vê, reusa `UpdatePodConfigFieldBlocklist`). **Sem TTL/expiry agora** — prune manual; marcar `ponytail:` com upgrade path (expiry 24h se a lista apodrecer).

5. **Allowlist: cap ~20, FIFO (dropa mais antigo) + dedup.** Query `UpdatePodConfigFieldAllowlist` espelha a de blocklist. Precisa remove-from-list nas duas.

6. **Budgets são dashboard-editáveis (CFG-01/UI-01).** `created_budget_s` + `progress_stall_budget_s` viram campos em `pod_config` com min/max (molde EXATO dos `coldstart_budget_s`/`port_bind_budget_s` existentes) + 2 cases PATCH em `config_write.go` + 2 sliders no dashboard (molde Phase 17). `coldstart_budget_s` continua como teto absoluto, inalterado.

7. **`download-weights.sh` dropa `mc cp --quiet` (OBS-01).** `--quiet` (linha 55) mata o progresso mid-file → um Qwen de 20GB baixa com 1 linha antes + 1 depois → regime 3 fica CEGO durante o arquivo. Sem heartbeat mid-file, FF-02 não detecta stall dentro de um arquivo grande. Dropar --quiet (ou emitir progresso periódico) é o habilitador de FF-02.

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
