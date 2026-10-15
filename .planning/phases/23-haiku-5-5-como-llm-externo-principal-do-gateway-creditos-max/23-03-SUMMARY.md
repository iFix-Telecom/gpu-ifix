---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 03
subsystem: gateway/db
tags: [migrations, goose, upstreams, billing, tenants, haiku]
requires: [23-01]
provides:
  - "upstreams anthropic-haiku (llm,1,0,enabled=false), openrouter-chat (llm,1,10), openrouter-deepseek (llm,1,20,enabled=false), openrouter-tts (tts,1,0)"
  - "tenants.llm_effort + trigger tenants_update_notify com a coluna"
  - "prices.unit aceita cache_read_token / cache_write_token / cache_write_1h_token"
  - "billing_events.tokens_cache_read / tokens_cache_write / cost_source"
  - "tabela ai_gateway.aux_pod_lifecycles"
affects: [23-04, 23-10, 23-12, 23-14, 23-15, 23-16, 23-19, 23-20]
tech-stack:
  added: []
  patterns: ["guarda DO/RAISE EXCEPTION antes do primeiro UPDATE", "deslocamento de prioridade em dois passos (+1000/-990) contra UNIQUE não-deferrable", "conferência final de definição após ON CONFLICT (name) DO NOTHING"]
key-files:
  created:
    - gateway/db/migrations/0041_haiku_chain.sql
    - gateway/db/migrations/0042_aux_pod_lifecycles.sql
    - gateway/internal/integration_test/migration_0041_test.go
  modified:
    - gateway/internal/db/migrate_test.go
    - gateway/internal/integration_test/migration_0026_test.go
    - gateway/internal/integration_test/migration_0029_test.go
    - gateway/internal/integration_test/migration_0038_test.go
    - gateway/internal/integration_test/migration_0039_test.go
    - gateway/internal/integration_test/upstreams_loader_test.go
decisions:
  - "O deslocamento tts e o INSERT de openrouter-tts só rodam se openrouter-tts ainda não existe (re-Up após Down não desloca duas vezes); o Down só desfaz o deslocamento se de fato apagou a linha"
  - "A CHECK de prices.unit é localizada no pg_constraint e recriada com o nome prices_unit_check (o mesmo nome gerado pelo Postgres para a CHECK inline de 0012)"
  - "Índice parcial idx_aux_pod_lifecycles_open indexa (pod) WHERE ended_at IS NULL"
metrics:
  duration: "~35min"
  completed: 2026-10-15
  tasks: 2
  files: 9
---

# Phase 23 Plan 03: migrations da cadeia Haiku, effort, cache e aux_pod_lifecycles — Summary

Duas migrations goose. A 0041 monta a cadeia LLM tier 1 na ordem anthropic-haiku(0) → openrouter-chat(10) → openrouter-deepseek(20), com os dois novos nascendo `enabled=false`, e põe openrouter-tts na frente do tier 1 de tts sem assumir prioridade fixa. Também cria `tenants.llm_effort` (com NOTIFY de hot-reload), as units de cache em `prices` e as colunas de cache e `cost_source` em `billing_events`. A 0042 cria `aux_pod_lifecycles`. Divergências de prod falham com mensagem própria, em três guardas `RAISE EXCEPTION`.

## Tarefas

| Task | Nome | Commit | Arquivos |
|------|------|--------|----------|
| 1 | Migrations 0041 + 0042 com Down simétrico e guardas | 0a9e43d | 0041_haiku_chain.sql, 0042_aux_pod_lifecycles.sql, internal/db/migrate_test.go |
| 2 | Testes de migration + bumps de Down | ad56b97 | migration_0041_test.go, migration_0026/0029/0038/0039_test.go, upstreams_loader_test.go |

## Verificação (FATOS, com fonte)

- `go test -tags=integration ./internal/integration_test/ -run 'Migration|Migrate' -count=1 -v` mostrou PASS nos 5 testes novos (`TestMigration0041_UpDown`, `_TTSDivergentPriorities`, `_OccupiedLLMSlotFails`, `_OccupiedSlot10Fails`, `_SameNameWrongDefinitionFails`) e em todos os testes de migration que já existiam.
- A suíte de integração inteira (`-run '.'`) passou: `ok ... 318.985s`.
- `go test ./internal/db/` passou (lista de migrations embutidas atualizada).
- `gofmt -l gateway/internal/` sem saída; `go build ./...` OK.
- Critérios de grep: `ON CONFLICT (name) DO NOTHING` ×3, nenhum `ON CONFLICT DO NOTHING` sem alvo, `RAISE EXCEPTION` ×3, `,false)` ×2, `definicao divergente` ×1, `llm_effort IS DISTINCT FROM` ×1, `cache_write_1h_token` presente. O 1º RAISE fica na linha 46, o `SET tier_priority = 10` na 51 e o INSERT de anthropic-haiku na 55.

Bumps de Down aplicados (todos falhavam com o HEAD novo, conforme a 1ª execução):
- migration_0026_test.go: Down(13)→Down(15) e Down(15)→Down(17)
- migration_0029_test.go: Down(12)→Down(14)
- migration_0038_test.go: Down(3)→Down(5)
- migration_0039_test.go: Down(2)→Down(4)

Os testes que fazem `Down(1)` relativo ao HEAD seguido de `Up` (0028/0029 roundtrip, migrate_test) não precisaram de bump e passaram.

## Desvios do plano

### Corrigidos automaticamente

**1. [Rule 3 - Bloqueio] Lista de migrations embutidas em `internal/db/migrate_test.go`**
- **Problema:** o teste unitário compara a lista exata de arquivos embutidos e quebraria com 0041/0042.
- **Correção:** os dois nomes foram acrescentados à lista.
- **Commit:** 0a9e43d

**2. [Rule 1 - Bug] `resetUpstreamsTable` reabilitava os upstreams novos**
- **Problema:** o helper fazia `SET enabled = TRUE` em todas as linhas. Com isso, openrouter-deepseek virava tier 1 llm habilitado, e `TestIntegration_LoaderReloadWithin1sOfAdminUpdate` falhava: depois de desligar o openrouter-chat, `Resolve("llm",1)` continuava achando um tier 1. A falha foi reproduzida isoladamente.
- **Correção:** o helper agora devolve o estado pós-migration, com `enabled = (name NOT IN ('anthropic-haiku','openrouter-deepseek'))`.
- **Arquivo:** gateway/internal/integration_test/upstreams_loader_test.go
- **Commit:** ad56b97

**3. [Ajuste] Idempotência do deslocamento tts**
- O plano pede deslocamento incondicional. Implementei o deslocamento, a guarda ≥ 990 e o INSERT dentro de `IF NOT EXISTS (openrouter-tts)`. Assim, um re-Up depois de rollback parcial ou um rerun manual não desloca o tier duas vezes. O Down espelha a mesma condição pelo `ROW_COUNT` do DELETE.

## Observações

- Na 2ª execução da suíte inteira apareceram falhas que mudavam de uma execução para outra (Billing*, Emerg*, Migration*). Load average estava ~11, com outros executores rodando em paralelo. Os mesmos grupos passaram isolados e a 3ª execução da suíte inteira passou. HIPÓTESE: as falhas vêm de contenção de recursos e não de 0041/0042. Evidência que resolveria: rodar a suíte num host ocioso (CI).
- O hook `clickup-link-enforce.sh` avisou a cada edição que falta tarefa ClickUp vinculada na `.planning` da worktree. Como executor de worktree, não rodei o wizard: o vínculo da fase cabe ao orquestrador.
- Prod não foi tocado. O dry-run contra o dump de prod fica no 23-15 e a habilitação dos upstreams no 23-16.

## Threat Flags

Nenhuma superfície nova além do `<threat_model>`. T-23-07, T-23-08, T-23-09 e T-23-66 foram mitigados e estão cobertos por testes.

## Self-Check: PASSED

- FOUND: gateway/db/migrations/0041_haiku_chain.sql
- FOUND: gateway/db/migrations/0042_aux_pod_lifecycles.sql
- FOUND: gateway/internal/integration_test/migration_0041_test.go
- FOUND: commit 0a9e43d
- FOUND: commit ad56b97
