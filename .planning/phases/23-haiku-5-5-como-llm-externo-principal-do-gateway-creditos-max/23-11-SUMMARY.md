---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 11
subsystem: gateway/admin
tags: [economy, billing, aux-pod-lifecycles, sqlc, haiku, 3060]
requires: [23-03, 23-19]
provides:
  - "sqlc SumEconomyTokensRange (tokens local/total, créditos Haiku, caixa externo)"
  - "sqlc StartAuxPodLifecycle / GetAuxPodLifecycle / CloseAuxPodLifecycle / ListAuxPodLifecyclesInRange"
  - "AuxPodLifecycleHandler (POST /admin/aux-pod-lifecycles) — NewAuxPodLifecycleHandler(q, cfg, log)"
  - "EconomySummary: pct_servido_local_tokens, custo_creditos_haiku_brl, custo_externo_caixa_brl, vast_3060_brl"
affects: [23-14, 23-15, 23-16]
tech-stack:
  added: []
  patterns: ["upsert idempotente ON CONFLICT com LEAST/COALESCE", "close guardado por ended_at IS NULL + :execrows para distinguir retry", "helper lifecycleAccrual compartilhado 3090/3060"]
key-files:
  created:
    - gateway/db/queries/aux_pod_lifecycles.sql
    - gateway/internal/db/gen/aux_pod_lifecycles.sql.go
    - gateway/internal/admin/aux_pod_lifecycles_http.go
    - gateway/internal/admin/aux_pod_lifecycles_http_test.go
    - gateway/internal/integration_test/aux_pod_lifecycles_queries_test.go
  modified:
    - gateway/db/queries/billing.sql
    - gateway/sqlc.yaml
    - gateway/internal/db/gen/billing.sql.go
    - gateway/internal/db/gen/querier.go
    - gateway/internal/admin/economy.go
    - gateway/internal/admin/economy_test.go
decisions:
  - "pct_servido_local_tokens segue a escala 0–1 do pct_servido_local existente (o plano citava 25.0 como exemplo; o próprio plano mandava conferir e seguir a escala atual)"
  - "GetAuxPodLifecycle devolve também started_at e dph_usd (o plano listava só id, ended_at) — necessários para calcular dph × horas no stop sem total_cost_usd"
  - "ListAuxPodLifecyclesInRange filtra por started_at em [from,to), igual ao ListPrimaryLifecyclesInRange (um lifecycle = um dia BRT)"
  - "Stop com at anterior ao started_at é ajustado para started_at (custo 0) em vez de 400, para o publisher best-effort não ficar repetindo para sempre"
  - "horas_pod_up continua contando só o primary (3090); a 3060 entra no custo (vast_brl e série diária), não nas horas"
  - "Linha aux fechada sem total_cost_brl usa total_cost_usd × USDToBRLRate"
metrics:
  duration: "~30min"
  completed: 2026-10-15
  tasks: 2
  files: 11
---

# Phase 23 Plan 11: backend do painel Economia fidedigno — Summary

O `/admin/economy` agora entrega o % servido local por tokens, o custo do passo ① (Haiku) separado como "coberto por créditos", o custo externo em caixa sem ele e o custo da 3060 somado ao Vast (e separado em `vast_3060_brl`). O `POST /admin/aux-pod-lifecycles` é o caminho de escrita do custo da 3060 pelo `unified3060.py`, idempotente nos dois eventos.

## Tarefas

| Task | Nome | Commits | Arquivos |
|------|------|---------|----------|
| 1 | Queries sqlc + POST /admin/aux-pod-lifecycles | 3abe55a (RED), 170d936 (GREEN) | aux_pod_lifecycles.sql, billing.sql, sqlc.yaml, gen/*, aux_pod_lifecycles_http.go + test |
| 2 | /admin/economy com tokens, créditos, caixa e 3060 | a86c101 (RED), c88c958 (GREEN) | economy.go, economy_test.go, integration_test/aux_pod_lifecycles_queries_test.go |

## Contrato HTTP

`POST /admin/aux-pod-lifecycles` body `{"pod","event":"start"|"stop","vast_instance_id","dph_usd"?,"at"? RFC3339,"total_cost_usd"?}`:
- 200 `{"id","event"}`; stop repetido → 200 `{"id","event":"stop","already_closed":true}` sem alterar custo
- 400 em JSON inválido, campo desconhecido, body > 64 KiB, pod vazio/> 64 chars, event inválido, instance ≤ 0, dph/total negativos, at não-RFC3339
- 404 só em stop sem start daquele (pod, vast_instance_id)
- O handler não valida X-Admin-Key; a rota precisa ser montada dentro do adminRouter no 23-14 (T-23-34). O teste `TestAuxPodLifecycle_NoOwnAuth` documenta isso.

## Verificação (FATOS, com fonte)

- `go test ./gateway/internal/admin/ ./gateway/internal/db/... -count=1` → ok (10 testes AuxPod, 5 testes novos de Economy, os de antes seguem verdes).
- Integração com Postgres real (testcontainers via sudo): `-run 'TestAuxPodLifecycleQueries|TestSumEconomyTokensRange'` → `ok 9.859s`. Ela confirma o ON CONFLICT com LEAST/COALESCE, o close repetido com 0 linhas afetadas e custo intacto, o ErrNoRows de instância inexistente e os agregados de tokens/créditos/caixa.
- `sqlc diff` sem saída; `gofmt -l gateway` sem saída; `go build ./...` e `go vet -tags=integration ./internal/integration_test/` OK.
- Greps de aceite: `pct_servido_local_tokens`, `custo_creditos_haiku_brl` e `vast_3060_brl` aparecem uma vez cada em economy.go; `already_closed` está no handler; `GetAuxPodLifecycle` aparece uma vez no .sql.

## Desvios do plano

### Corrigidos automaticamente

**1. [Rule 2 - Faltava algo crítico] GetAuxPodLifecycle devolve started_at + dph_usd**
- **Problema:** o contrato da query era `SELECT id, ended_at`, mas o stop sem `total_cost_usd` precisa de started_at e dph para calcular o custo.
- **Correção:** a query passou a devolver `id, started_at, ended_at, dph_usd`.
- **Commit:** 3abe55a

**2. [Rule 2] Teste de integração da semântica SQL**
- O plano só pedia testes unitários com fake. Incluí `aux_pod_lifecycles_queries_test.go` para provar o comportamento do ON CONFLICT e do close no Postgres real, já que o fake só imita esse comportamento.
- **Commit:** c88c958

## Known Stubs

Nenhum. A rota ainda não está registrada no router; isso cabe ao 23-14, conforme o plano.

## Threat Flags

Nenhum além do que o threat_model já cobre (T-23-34 depende do 23-14 montar a rota no adminRouter).

## Self-Check: PASSED

- Arquivos criados existem: aux_pod_lifecycles.sql, gen/aux_pod_lifecycles.sql.go, aux_pod_lifecycles_http.go, aux_pod_lifecycles_http_test.go, aux_pod_lifecycles_queries_test.go.
- Commits 3abe55a, 170d936, a86c101, c88c958 aparecem em `git log`.
