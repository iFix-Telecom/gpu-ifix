---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 09
subsystem: gateway/proxy + gatewayctl
tags: [haiku, effort, reasoning, openrouter, tenant-override]
requires: [23-01 isAnthropicTarget, 23-19 LLMEffortByTenantID + UpdateTenantLLMEffort]
provides: [ResolveLLMEffort, TenantEffortLookup, DefaultLLMEffort, PASS 6 do director OpenRouter, gatewayctl tenant set-llm-effort]
affects: [23-12 (passo ① Anthropic direto reutiliza ResolveLLMEffort)]
tech-stack:
  added: []
  patterns: [type assertion TenantPrefsLookup → TenantEffortLookup sem mudar assinatura, núcleo de CLI testável com connect injetável]
key-files:
  created:
    - gateway/internal/proxy/llm_effort.go
    - gateway/internal/proxy/llm_effort_test.go
    - gateway/internal/proxy/openrouter_effort_test.go
    - gateway/cmd/gatewayctl/tenant_llm_effort.go
    - gateway/cmd/gatewayctl/tenant_llm_effort_test.go
  modified:
    - gateway/internal/proxy/openrouter_director.go
    - gateway/cmd/gatewayctl/tenant.go
decisions:
  - "Effort: cliente explícito (válido) > tenants.llm_effort (válido) > high; valores fora de off|low|medium|high|xhigh|max são ignorados em qualquer nível"
  - "Cliente: reasoning_effort precede reasoning.effort, que precede reasoning.enabled=false (off)"
  - "PASS 6 só para alvo Anthropic; para outros alvos os campos reasoning do cliente seguem byte a byte"
metrics:
  duration: ~15min
  completed: 2026-10-15
---

# Phase 23 Plan 09: política de effort do LLM (HAIKU-05) Summary

Política única de effort, com a precedência cliente explícito > override do tenant > `high`. No passo ② (OpenRouter) ela entra como PASS 6, que grava `reasoning` para alvos `anthropic/*`. O operador controla o override com `gatewayctl tenant set-llm-effort`.

## O que foi feito

- **`llm_effort.go`**: `TenantEffortLookup`, `DefaultLLMEffort = "high"`, allowlist `validLLMEfforts` e `ResolveLLMEffort(ctx, body, lookup) (effort, source)`, que devolve a fonte como `client`, `tenant` ou `default`. O tenant vem de `auth.FromContext`; sem auth ou sem lookup, essa camada é pulada. O helper interno `applyAnthropicEffort` faz o PASS 6: remove `reasoning_effort` e `reasoning` do cliente e grava `{"effort":e}`, ou `{"enabled":false}` quando o resultado é `off`.
- **`openrouter_director.go`**: o PASS 6 roda depois do PASS 5, com log Debug de `effort`, `effort_source` e `upstream`, sem body. A assinatura de `BuildOpenRouterDirector` não mudou; o lookup sai de `tenantPrefs.(TenantEffortLookup)`.
- **gatewayctl**: `tenant set-llm-effort --slug S (--effort V | --clear)`. A validação acontece antes de conectar ao banco. Mandar `--effort` e `--clear` juntos, ou nenhum dos dois, dá erro. Quando o UPDATE afeta 0 linhas, o comando sai com `tenant not found`. O comando foi registrado no switch e no Usage.

## Testes

- `TestResolveLLMEffort`: 12 casos (default, tenant, cliente vence tenant, enabled=false → off, xhigh, inválido → tenant ou default, tenant inválido, sem auth, precedência entre os campos do cliente).
- `TestOpenRouterDirector_Effort_*`: 6 casos (padrão high, cliente desliga thinking, `reasoning_effort` low, override do tenant, alvo deepseek intacto byte a byte, alvo deepseek sem injeção).
- `TestTenantSetLLMEffort_*`: 8 casos (set, clear, inválido sem tocar o banco e listando os valores aceitos, `--effort` + `--clear`, nenhum dos dois, sem slug, not found, erro do banco).
- `go test ./gateway/internal/proxy/ ./gateway/cmd/gatewayctl/ -count=1` verde; `gofmt -l gateway` limpo; `go build ./gateway/...` ok.

## Commits

| Hash | Mensagem |
|------|----------|
| 8949281 | test(23-09): testes de ResolveLLMEffort e PASS 6 do director (RED) |
| 3ea0394 | feat(23-09): ResolveLLMEffort + PASS 6 de effort no director do OpenRouter |
| 6e251dd | test(23-09): testes de gatewayctl tenant set-llm-effort (RED) |
| bf832a9 | feat(23-09): gatewayctl tenant set-llm-effort |

## Deviations from Plan

None - plan executed exactly as written. Para isolar o teste do banco, o núcleo do comando (`setTenantLLMEffort`) recebe uma função `connect` injetável. O plano previa um fake de queries, e foi isso que a função viabilizou.

## TDD Gate Compliance

RED (`test(...)`) e GREEN (`feat(...)`) presentes nas duas tasks. Não houve REFACTOR.

## Self-Check: PASSED
