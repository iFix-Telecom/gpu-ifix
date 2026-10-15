---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 20
subsystem: gateway-config-surfaces
tags: [gatewayctl, admin-api, dashboard, model-aliases, provider-prefs, openrouter]
requires: [23-01]
provides:
  - "upstreamNameRole com anthropic-haiku/openrouter-deepseek (llm) e openrouter-tts (tts)"
  - "providerPrefsUpstreams = {openrouter-chat, openrouter-deepseek} em gatewayctl, admin API e dashboard"
  - "fixture de paridade gateway/internal/admin/testdata/provider_prefs_upstreams.json"
affects: [23-16, 23-17]
tech-stack:
  added: []
  patterns:
    - "conjunto fechado de upstreams com prefs, espelhado em 3 superfícies e travado por fixture único"
    - "validação pré-DB do model-alias set extraída para função pura (validateModelAliasSet)"
key-files:
  created:
    - gateway/internal/admin/testdata/provider_prefs_upstreams.json
  modified:
    - gateway/cmd/gatewayctl/model_alias.go
    - gateway/cmd/gatewayctl/model_alias_test.go
    - gateway/internal/admin/model_aliases_admin_http.go
    - gateway/internal/admin/model_aliases_admin_http_test.go
    - dashboard/src/lib/provider-prefs.ts
    - dashboard/src/lib/admin-actions-core.ts
    - dashboard/src/lib/admin-actions.test.ts
    - dashboard/src/app/(dashboard)/modelos/modelos-controls.tsx
decisions:
  - "No TS, PROVIDER_PREFS_UPSTREAMS fica em provider-prefs.ts (módulo compartilhado) e é reexportado pelo admin-actions-core, que é server-only e não pode ser importado pelo componente cliente; assim cliente e servidor usam o mesmo Set"
  - "gatewayctl ganhou validateModelAliasSet (prefs → R10 → role) para testar sem Postgres; ordem das checagens e exit 2 preservados"
metrics:
  duration: "~20 min"
  completed: 2026-10-15
  tasks: 2
  files: 9
---

# Phase 23 Plan 20: upstreams novos e prefs no openrouter-deepseek nas ferramentas oficiais — Summary

Agora gatewayctl, a admin API e o dashboard /modelos aceitam linhas de alias em `anthropic-haiku`, `openrouter-deepseek` e `openrouter-tts`. As prefs de provedor OpenRouter passaram a valer para `openrouter-chat` e `openrouter-deepseek`, e as três superfícies conferem esse conjunto contra um fixture único.

## O que foi feito

**Task 1 (gatewayctl e admin API)**
- `upstreamNameRole` passou a incluir `anthropic-haiku: llm`, `openrouter-deepseek: llm` e `openrouter-tts: tts`.
- O literal `"openrouter-chat"` deu lugar ao conjunto `providerPrefsUpstreams` no CLI e na const `providerPrefsUpstream` do admin. As mensagens de erro listam os dois nomes em ordem: `openrouter-chat, openrouter-deepseek`.
- Toda a validação pré-DB do `set` (escopo e schema das prefs, R10 e role) foi para `validateModelAliasSet`, uma função pura.
- O texto de uso e os comentários "openrouter-chat only" foram atualizados.

**Task 2 (dashboard)**
- `PROVIDER_PREFS_UPSTREAMS` é um Set exportado por `provider-prefs.ts` e reexportado pelo `admin-actions-core.ts`.
- `upsertModelAliasCore` recusa prefs em upstream fora do conjunto antes de chamar o gateway.
- Em `modelos-controls.tsx`, o editor de prefs e o resumo da coluna aparecem para os dois upstreams. Os textos de ajuda foram atualizados. O default de upstream de um alias novo continua `openrouter-chat` (`DEFAULT_ALIAS_UPSTREAM`).

## Verificação (FATOS, saída dos comandos)
- `gofmt -l ./cmd/gatewayctl/ ./internal/admin/` → vazio; `go vet` (incluindo `-tags integration`) → limpo.
- `go test ./cmd/gatewayctl/ ./internal/admin/ -count=1` → ok nos dois pacotes. Há 7 casos novos no CLI e 4 no admin, entre eles `TestModelAliasSet_ProviderPrefsUpstreams_ParityFixture` e `TestProviderPrefsUpstreams_ParityFixture`.
- `npx vitest run src/lib/admin-actions.test.ts` → 29/29 (3 novos: openrouter-deepseek com prefs, anthropic-haiku com prefs recusado e paridade com o fixture); `npx tsc --noEmit` → exit 0.
- Greps de aceitação: as 3 entradas de role = 1 cada; `providerPrefsUpstreams` no admin = 10; `openrouter-deepseek` em admin-actions-core.ts = 2 e em modelos-controls.tsx = 5.

## TDD Gate Compliance
- RED: `3df5b38` (Go) e `13f9bbc` (vitest). Os dois falharam antes da implementação: compilação undefined no Go e 3 testes vermelhos no vitest.
- GREEN: `a89b6f9` (Go) e `6d52b19` (dashboard).

## Deviations from Plan

**1. [Rule 3 - Bloqueio] Set TS definido em provider-prefs.ts e não direto no admin-actions-core.ts**
- **Encontrado na:** Task 2
- **Problema:** `admin-actions-core.ts` começa com `import "server-only"`, então o componente cliente `modelos-controls.tsx` não pode importar o Set de lá. Duas cópias abririam espaço para deriva dentro do próprio dashboard.
- **Correção:** o Set vive em `dashboard/src/lib/provider-prefs.ts`, que o cliente já importava, e é reexportado pelo core. O teste de paridade importa pelo core.
- **Arquivo adicional:** `dashboard/src/lib/provider-prefs.ts`, fora da lista `files_modified` do plano.
- **Commit:** 6d52b19

## Observações
- O hook `clickup-link-enforce.sh` avisou a cada Edit que não há tarefa ClickUp vinculada nesta worktree. Como executor paralelo, não criei vínculo; fica com o orquestrador.
- Nada foi aplicado em prod. Só código e testes.

## Known Stubs
Nenhum.

## Self-Check: PASSED
- FOUND: gateway/internal/admin/testdata/provider_prefs_upstreams.json
- FOUND commits: 3df5b38, a89b6f9, 13f9bbc, 6d52b19
