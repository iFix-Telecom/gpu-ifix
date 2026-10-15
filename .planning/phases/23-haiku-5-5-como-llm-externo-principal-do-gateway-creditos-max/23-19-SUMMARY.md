---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 19
subsystem: gateway/db + billing + tenants
tags: [sqlc, billing, tenants, haiku, effort, cache]
requires: [23-03]
provides:
  - "sqlc InsertBillingEvent com tokens_cache_read / tokens_cache_write / cost_source"
  - "sqlc UpdateTenantLLMEffort(ctx, UpdateTenantLLMEffortParams{LlmEffort pgtype.Text, Slug string}) (int64, error)"
  - "GetTenantConfig / ListTenantsForLoader com llm_effort"
  - "billing.Event.TokensCacheRead / TokensCacheWrite / CostSource"
  - "func (l *Loader) LLMEffortByTenantID(tenantID string) string"
affects: [23-09, 23-10, 23-11]
tech-stack:
  added: []
  patterns: ["mapeamento Event → params como função pura (insertParams) para teste sem banco", "string vazia → pgtype.Text NULL"]
key-files:
  created:
    - gateway/internal/billing/flusher_test.go
    - gateway/internal/integration_test/tenant_llm_effort_test.go
  modified:
    - gateway/db/queries/billing.sql
    - gateway/db/queries/tenants_admin.sql
    - gateway/internal/db/gen/billing.sql.go
    - gateway/internal/db/gen/tenants_admin.sql.go
    - gateway/internal/db/gen/models.go
    - gateway/internal/db/gen/querier.go
    - gateway/internal/billing/flusher.go
    - gateway/internal/billing/events.go
    - gateway/internal/tenants/config.go
    - gateway/internal/tenants/loader.go
    - gateway/internal/tenants/loader_test.go
decisions:
  - "cost_source entra como $17 posicional (sqlc não aceita misturar $n com sqlc.narg na mesma query); a coluna é TEXT NULL, então o tipo gerado já é pgtype.Text"
  - "UpdateTenantLLMEffort também atualiza updated_at, igual a UpdateTenantProviderPrefs"
  - "O mapeamento Event → InsertBillingEventParams foi extraído de insertOne para insertParams (puro), o que permite testar sem banco"
metrics:
  duration: "~20min"
  completed: 2026-10-15
  tasks: 2
  files: 13
---

# Phase 23 Plan 19: sqlc + billing.Event com cache + LLMEffortByTenantID — Summary

O esquema do 23-03 agora chega ao código. `InsertBillingEvent` grava `tokens_cache_read`, `tokens_cache_write` e `cost_source` a partir de `billing.Event`. Quando `CostSource` vem vazio, a coluna recebe NULL. As queries de tenants trazem `llm_effort`, e foi criada a `UpdateTenantLLMEffort :execrows`. O loader ganhou `LLMEffortByTenantID`, que devolve "" quando não há override.

## Tarefas

| Task | Nome | Commits | Arquivos |
|------|------|---------|----------|
| 1 | sqlc (InsertBillingEvent, llm_effort) e billing.Event | d2e1a45 (RED), c7d8140 (GREEN) | billing.sql, tenants_admin.sql, gen/*, flusher.go, events.go, flusher_test.go |
| 2 | LLMEffortByTenantID no loader de tenants | 3eda9b1 (RED), 3005459 (GREEN) | loader.go, config.go, loader_test.go, tenant_llm_effort_test.go |

## Verificação (FATOS, com fonte)

- `sqlc generate` (v1.30.0) seguido de `sqlc diff -f gateway/sqlc.yaml` terminou sem saída.
- `gofmt -l gateway/` não listou nenhum arquivo. `go vet -tags=integration ./gateway/internal/integration_test/` passou.
- `go test ./gateway/... -count=1` passou em todos os pacotes.
- RED confirmado nos dois ciclos: build failed com `undefined: insertParams` e `LLMEffortByTenantID undefined`.
- Integração, com `-run 'TenantLLMEffort|TestTenantsHotReload|Billing'`, deu PASS em `TestTenantLLMEffort_RoundTrip`. O teste cobre 'low', depois NULL → "", 'bogus' rejeitado pela CHECK, GetTenantConfig com o campo e slug inexistente → 0 linhas. Também passaram TestTenantsHotReload, TestTenantsHotReloadMode, TestBillingIdempotentReplay, TestBillingFlushNonStream, TestBillingFlushPartialSource e TestBillingReconcileDrift.
- Greps: `tokens_cache_read` aparece 1 vez em billing.sql. `UpdateTenantLLMEffort` aparece 1 vez em tenants_admin.sql. `func (l *Loader) LLMEffortByTenantID` aparece 1 vez em loader.go.

## Desvios do plano

### Corrigidos automaticamente

**1. [Rule 3 - Bloqueio] O mapeamento foi parar em `events.go`, não em `flusher.go`**
- **Problema:** o plano diz que a conversão Event → params fica em flusher.go, mas ela estava em `insertOne`, no arquivo events.go, e recebia um `*gen.Queries` concreto. Por isso não dava para testá-la sem banco.
- **Correção:** extraí `insertParams(e Event) gen.InsertBillingEventParams` (função pura) em events.go. `insertOne` passou a chamá-la, e o flusher_test.go testa essa função.
- **Commit:** c7d8140

**2. [Rule 3 - Bloqueio] O campo novo fica em `tenants/config.go`**
- **Problema:** `TenantConfig` é definido em config.go, arquivo que não estava na lista do plano.
- **Correção:** adicionei `LLMEffort string` lá.
- **Commit:** 3005459

**3. [Nota] Arquivos gerados a mais**
- `querier.go` ganhou `UpdateTenantLLMEffort`, porque `emit_interface` está ligado. `models.go` ganhou `AiGatewayAuxPodLifecycle` e os campos novos de 0041/0042, que o 23-03 não tinha regenerado. As duas mudanças vêm da regeneração, sem edição manual.

## Known Stubs

Nenhum. Os campos novos do Event ficam em zero ou vazio até os planos 23-09/10/11 preencherem.

## Self-Check: PASSED

- Os arquivos criados existem: gateway/internal/billing/flusher_test.go e gateway/internal/integration_test/tenant_llm_effort_test.go.
- Os commits d2e1a45, c7d8140, 3eda9b1 e 3005459 estão presentes no `git log`.
