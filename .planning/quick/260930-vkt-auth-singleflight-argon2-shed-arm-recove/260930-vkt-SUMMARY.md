---
phase: quick-260930-vkt
plan: 01
status: complete-pending-integration-run
subsystem: gateway (auth, shed, integration tests, CI)
tags: [auth, argon2, singleflight, cache, shed, fsm, integration-tests, ci]
clickup: 86akrbgbt
requirements: [CLICKUP-86akrbgbt]
requires: []
provides:
  - "auth.Verifier: L1 -> Redis -> neg -> singleflight(DB+argon2)"
  - "shed.Set.ApplyConfigs wired at boot + onUpstreamsReload"
  - "gatewayctl integration tests in CI"
affects: [gateway auth hot path, shed FSM tunables, build-gateway.yml]
tech-stack:
  added: [golang.org/x/sync/singleflight (already in go.mod, first use)]
  patterns: [singleflight with WithoutCancel ctx, positive-only TTL L1 cache]
key-files:
  created:
    - gateway/internal/auth/l1cache.go
    - gateway/internal/auth/l1cache_test.go
    - gateway/internal/auth/apikey_singleflight_test.go
    - gateway/internal/shed/set_apply_test.go
    - gateway/cmd/gatewayctl/seed_restore_integration_test.go
  modified:
    - gateway/internal/auth/apikey.go
    - gateway/internal/auth/apikey_test.go
    - gateway/internal/shed/set.go
    - gateway/cmd/gateway/main.go
    - gateway/internal/integration_test/helpers_shed_test.go
    - gateway/internal/integration_test/setup_test.go
    - gateway/internal/integration_test/shed_edge_cases_test.go
    - gateway/internal/integration_test/shed_sc2_hysteresis_test.go
    - gateway/cmd/gatewayctl/model_alias_integration_test.go
    - gateway/cmd/gatewayctl/primary_lifecycles_integration_test.go
    - gateway/cmd/gatewayctl/upstreams_test.go
    - .github/workflows/build-gateway.yml
decisions:
  - "Redis hit NÃO popula o L1 (janela de revogação D-A2 continua ≤ 60s)"
  - "Waiter do singleflight respeita o próprio ctx (DoChan + select); trabalho compartilhado roda com WithoutCancel + 5s"
  - "shed_arm/recover <= 0 → default do Set (30/60)"
  - "model_aliases restaurado via snapshot jsonb da 1ª chamada (column-agnostic) em vez de copiar INSERTs de 5 migrations"
  - "Partições de teste via db.EnsurePartitions (mesma função do boot), mês anterior até +3"
metrics:
  completed: 2026-10-01
  tasks: 3
  commits: 3
---

# Quick 260930-vkt: auth singleflight + L1, shed arm/recover wiring, testes de integração

Corrige o stampede de argon2id no auth (singleflight por key + L1 in-process de 30s que segura o tráfego com o Redis fora), faz `shed_arm_seconds`/`shed_recover_seconds` chegarem à FSM (no boot e em todo reload) e conserta os testes de integração apodrecidos, incluindo os de gatewayctl, que agora rodam no CI. Sem push e sem deploy.

**Worktree:** `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-af6a4cf30e3ded5b4`
**Branch:** `worktree-agent-af6a4cf30e3ded5b4` | **Base esperada:** `c80c4e9c44750d6f63018f9cb1db17c56f5dc56a` (worktree resetado para ela; antes estava em `5553bd4`)

## Commits

| Task | Hash | Mensagem |
|------|------|----------|
| 1 | `b3e6e0a` | fix(260930-vkt): auth singleflight + L1 in-process cache to stop argon2 stampede |
| 2 | `3b4b6e3` | fix(260930-vkt): apply shed_arm/recover_seconds from circuit_config to FSMs |
| 3 | `4a08139` | test(260930-vkt): fix rotten integration tests + run gatewayctl integration in CI |

## Mudanças de comportamento (para revisão de prod)

1. **Auth (`apikey.go` / `l1cache.go`)**: a ordem de busca agora é L1 → Redis positivo → Redis negativo → singleflight(DB + argon2 + cachePut + l1.put + touch).
   - Requests concorrentes com a MESMA key em cache miss fazem 1 lookup no DB e 1 argon2. Keys diferentes não são agrupadas (a chave do singleflight é o sha256 hex completo).
   - O L1 guarda só entradas `active` que vieram do caminho autoritativo. TTL 30s, máximo 10k entradas. Hit no Redis NÃO grava no L1. Nenhum erro, key revogada ou key desconhecida entra no L1.
   - **Janela de revogação:** continua ≤ 60s. Pior caso: Redis entrega a entrada até o fim do TTL dela; depois disso o L1 só foi gravado pelo caminho do DB, então expira em ≤ 30s após aquele lookup.
   - Uma réplica que acabou de revalidar no DB pode servir a key revogada por até 30s pelo L1. Antes isso vinha do Redis compartilhado; hoje vem do L1 local. Nos dois casos fica dentro de D-A2.
   - O trabalho compartilhado roda com `context.WithoutCancel` + timeout de 5s: se o líder cancelar, os waiters não falham. Cada waiter ainda sai com `ctx.Err()` se o ctx dele for cancelado.
   - Mudança de memória: ~10k × ~200B por processo no máximo.
2. **Shed (`set.go` / `main.go`)**: `Set.ApplyConfigs` é chamado depois de `shedSet.Rebuild` no boot e dentro de `onUpstreamsReload` (que serve tanto o LISTEN quanto o backstop de 60s, sob `reloadMu`).
   - Valor 0 ou ausente → 30/60. O estado da FSM é preservado (D-C5).
   - Prod hoje: segundo o diagnóstico do debugger (não reverifiquei no banco de prod), não há rows com shed_arm/recover custom → comportamento inalterado.
   - **Isso muda o comportamento de qualquer ambiente que tenha valores custom**: o seed de teste usa 1s/2s e passa a valer de fato.
3. **CI**: novo step `Integration tests (gatewayctl)` no job `integration-test`. O `timeout-minutes: 15` do job não foi alterado.

## Testes executados (FATOS — rodei e vi o resultado)

- `gofmt -l gateway` → vazio.
- `go build ./gateway/...`, `go vet ./gateway/...`, `go vet -tags=integration ./gateway/...`, `go vet -tags=integration,integration_slow ./gateway/internal/integration_test/...` → OK.
- `go test -count=1 ./gateway/... ./pkg/openai/...` → EXIT=0, 29 pacotes ok.
- `go test -race -count=1 ./gateway/internal/shed/... ./gateway/internal/upstreams/... ./gateway/cmd/gateway/...` → ok.
- `go test -race -count=1 -skip 'TestGenerateAPIKey_UniquePer1000|TestGenerateAPIKey_Format' ./gateway/internal/auth/...` → ok (36s). Inclui os 6 testes novos do Verifier e os 5 do L1.
- **Observação:** `go test -race ./gateway/internal/auth/...` sem `-skip` estoura o timeout default de 10m dentro do argon2.
  - Causa: `TestGenerateAPIKey_UniquePer1000` (1000 hashes argon2) leva 156.9s **sem** race (medido no `-v`). Esse teste está em `argon2_test.go`, que esta task não alterou.
  - Recomendação para o orquestrador: rodar `-race` no auth com o `-skip` acima, ou com `-timeout 30m`.
- Docker/testcontainers **não foram executados** (restrição da task).

## Comandos de integração para o orquestrador (com Docker)

```bash
cd <worktree>
CI_ALLOW_TIGHT_SHED_TIMING=1 go test -tags=integration ./gateway/internal/integration_test/... -count=1 -v -timeout=15m
CI_ALLOW_TIGHT_SHED_TIMING=1 go test -tags=integration,integration_slow ./gateway/internal/integration_test/... -run TestSC2 -count=1 -v -timeout=10m
go test -tags=integration ./gateway/cmd/gatewayctl/... -count=1 -v -timeout=10m
# alvos específicos:
CI_ALLOW_TIGHT_SHED_TIMING=1 go test -tags=integration ./gateway/internal/integration_test/... -run 'TestSensitiveSaturated503|TestTier1UnavailableShedded503|TestSC1|TestAdminUsageResponseShape|TestBillingReconcileDrift|TestIntegration_10_PartitionAutomation' -count=1 -v
```
(Os testes de shed só pulam se `CI=true` e `CI_ALLOW_TIGHT_SHED_TIMING` != 1. Localmente, sem `CI`, eles rodam de qualquer forma.)

## Testes de integração alterados, SEM execução real (pendentes de Docker)

| Teste | Mudança |
|-------|---------|
| `newShedStack` (todos os SC/edge) | `UPDATE tenants SET rps_limit=1000, rpm_limit=60000` + warm-up serial de auth por tenant (vindos do scratch do debugger) |
| `TestSensitiveSaturated503` | cap=1 para telefonia antes do boot; FSM forçada ON via `gw:shed:force:local-llm`; tier-0 com 1.5s; 1 request em background ocupa o slot; a 2ª exige 503 + `Retry-After: 5` + `upstream_saturated_for_sensitive_tenant` + audit `shed_blocked_sensitive`; a 1ª exige 200 |
| `TestTier1UnavailableShedded503` | llm tier≥1 desabilitado antes do boot (reabilitado no Cleanup); cap=1 para converseai; force ON; a 2ª request exige 503 + `Retry-After: 30` + `all_chat_upstreams_saturated` + audit `shed_tier1_unavailable`. Asserção agora **estrita** |
| `TestSC2_HysteresisNoFlapping` | conta só os eventos de `local-llm`, decodifica o `ShedEvent`, loga histograma por reason/estado; limite 4×6+2=26; falha explicitamente com o histograma |
| `freshSchema` (integration_test + gatewayctl) | `db.EnsurePartitions(prev month, 4)` → cobre o mês anterior até +3 |
| gatewayctl `freshSchema` | restaura `model_aliases` a partir de snapshot jsonb tirado na 1ª chamada |
| `TestModelAliasGet_ReturnsSpecificRow` | chaves `alias` / `upstream_name` |
| `TestRunPrimaryLifecyclesIntegration_FetchesFromDB`, `TestRunPrimaryLifecycles_RespectsLimitFlag` | só a linha mais recente fica aberta (`primary_live_singleton`) |

## Achados

### FATOS (com fonte)

- **Partição de billing_events (pedido extra do orquestrador):** a migration 0010 cria partições a partir de `DATE_TRUNC('month', CURRENT_DATE)` do Postgres (`gateway/db/migrations/0010_create_billing_events.sql:39-46`). Os dois testes semeiam `ts` como "hoje em America/Sao_Paulo" às 04:00–08:00 SP (`admin_usage_test.go:36-42`, `billing_reconcile_test.go:31-34`). Na execução de 2026-10-01 01:44Z o "hoje" em SP ainda era 30/09, e a partição de setembro não existe num container criado em outubro → 23514. Correção: chamar `db.EnsurePartitions` (a mesma função do boot, `main.go:225`) a partir do 1º dia do mês anterior em ambos os `freshSchema`. Grep feito: nenhum outro teste insere direto em `billing_events`/`audit_log`.
- O caminho D-D1 (Branch 10b) só dispara quando `Loader.Resolve(role, 1)` não acha upstream habilitado (`shed/middleware.go`, Branch 09/10b). Breaker aberto no tier-1 não leva ao 10b — por isso o teste antigo nunca era determinístico.
- Com `gw:shed:force` ativo, o ticker pula a avaliação de sinais (`shed/tick.go:125-161`), então a FSM fica em On de forma determinística.
- `InflightOverMax` usa `globalInflight >= InflightMax` (`shed/tick.go:188`).
- A tabela `ai_gateway.upstreams` não é truncada pelo `freshSchema` do integration_test → todo teste que mexe nela precisa restaurar (o D-D1 restaura no Cleanup).
- **SC2 — classificação por reason: NÃO SEI / dado insuficiente.** O log do scratch (`fy_sc2.log`) tem só o total (111 transições em 120s). O gateway rodou com `LOG_LEVEL=warn` e as transições da FSM não aparecem no log (0 linhas `SHED_FSM`). `fy_TestSC2_HysteresisNoFlapping.log` é uma execução vazia ("no tests to run"). Também não foi possível classificar retroativamente o counter antigo, que só contava mensagens. O teste novo produz o histograma na próxima execução.
- A asserção antiga do SC2 (≤4 no total) estava com a aritmética errada: fases de 10s > arm 1s / recover 2s → a máquina correta faz 4 transições por ciclo de 20s → ~24 em 120s.

### HIPÓTESES (cada uma com a evidência que resolveria)

- HIPÓTESE (SC2, flapping real): 111 ≫ 26 indica bounce On↔Recovering dentro da fase alta. Mecanismo plausível pelo código:
  - On→Recovering não tem espera (`fsm.go`, "signal_dropped").
  - Recovering não shedda (middleware Branch 07) e volta direto para On ("signal_returned_during_recover").
  - Com o cap do tenant em 4 e `shed_inflight_max` = 4, o inflight do tier-0 oscila 3↔4 entre os ticks de 100ms enquanto a FSM shedda o excedente.
  - **Resolve:** rodar o SC2 novo. Se `signal_returned_during_recover` e `signal_dropped` dominarem o histograma, é flapping da FSM (correção em fsm.go/tick.go = decisão do Pedro, não feita aqui). Se o excesso vier de outros upstreams ou de "undecodable", era artefato do counter (agora filtrado).
  - **Expectativa:** o SC2 provavelmente vai FALHAR, de propósito, com o histograma.
- HIPÓTESE: o warm-up + rate limit corrigem o SC1 (o scratch do debugger indicava isso; não executei). Resolve: rodar SC1.
- HIPÓTESE: com tier-0 a 1.5s nos testes D-B3/D-D1, uma sonda do prober (intervalo 10s) dentro da janela não abre o breaker do local-llm. Se abrir, a 1ª request falha a asserção de 200 e/ou o D-B3 vê `upstream_unavailable_for_sensitive_tenant`. Resolve: rodar os 2 testes; se falharem por isso, aumentar o timeout do prober no env do `bootGateway`.
- HIPÓTESE: desabilitar o llm tier-1 não quebra o boot do gateway (o config trata tier-1 como opcional, `config.go` D-D4). Resolve: rodar o `TestTier1UnavailableShedded503`.
- HIPÓTESE: o job de CI cabe em 15 min com o step novo (gatewayctl levou 30s no `ctl.log`; a suíte de integração completa não foi medida aqui). Resolve: primeira execução do CI.
- HIPÓTESE: `jsonb_populate_recordset(NULL::ai_gateway.model_aliases, …)` + `INSERT … SELECT *` restaura todas as colunas (inclusive `provider_prefs` e `created_at`), já que a tabela não tem coluna identity/generated (conferido em 0005/0026/0037). Resolve: `TestModelAliasList_Returns6Rows` passar depois dos testes que fazem set/delete.

## Deviations from Plan

- **[Rule 3] Testes novos em arquivos separados:** `apikey_singleflight_test.go` e `set_apply_test.go` em vez de anexar a `apikey_test.go`/`set_test.go`. Motivo: o sandbox do worktree bloqueou o append via heredoc. Mesmo pacote, sem impacto. `apikey_test.go` ganhou só `gate`/`entered` no `fakeQueries`.
- **[Rule 1] Pedido extra do orquestrador:** partições do billing_events nos `freshSchema` (descrito acima), incluído no commit da T3.
- **[Rule 2] Restauração de `upstreams` no Cleanup** do teste D-D1, para não vazar `enabled=false` para outros testes do container compartilhado.
- **model_aliases:** snapshot/restore em vez de "copiar o INSERT da migration". O seed final vem de 5 migrations (0005/0026/0028/0029/0037), e o snapshot sobrevive a migrations futuras.
- **Não feito:** reescrever o SC2 com outra cadência. Por decisão do plano, a FSM não foi alterada e a asserção não foi afrouxada.

## Known Stubs

Nenhum.

## Threat Flags

Nenhum além do threat model (T-vkt-01..06, todos mitigados conforme o plano).

## Self-Check: PASSED

- Arquivos criados existem: l1cache.go, l1cache_test.go, apikey_singleflight_test.go, set_apply_test.go, seed_restore_integration_test.go.
- Commits `b3e6e0a`, `3b4b6e3`, `4a08139` presentes em `git log c80c4e9..HEAD`.
- `grep ApplyConfigs gateway/cmd/gateway/main.go` → chamada via `applyShedConfigs` no boot + em `onUpstreamsReload`.
