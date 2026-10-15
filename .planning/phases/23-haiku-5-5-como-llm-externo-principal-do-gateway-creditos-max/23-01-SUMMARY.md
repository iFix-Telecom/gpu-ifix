---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 01
subsystem: gateway/proxy
tags: [anthropic, haiku-5.5, openrouter, prefill, sampling, hotfix]
requires: []
provides:
  - "anthropic_compat.go: AnthropicPrefillUserText, isAnthropicTarget, normalizeTrailingAssistant, anthropicRejectedSamplingParams, normalizeAnthropicChatBody"
  - "PASS 5 no BuildOpenRouterDirector (depois do PASS 4 de cache_control)"
affects: [23-02, 23-08, 23-09]
tech-stack:
  added: []
  patterns: ["parse tolerante map[string]json.RawMessage; sem mudança → body byte-idêntico"]
key-files:
  created:
    - gateway/internal/proxy/anthropic_compat.go
    - gateway/internal/proxy/anthropic_compat_test.go
    - gateway/internal/proxy/openrouter_prefill_test.go
  modified:
    - gateway/internal/proxy/openrouter_director.go
decisions:
  - "Body só é re-serializado se algo mudou: alvo Anthropic sem sampling e sem prefill também sai byte-idêntico"
  - "PASS 5 depois do PASS 4: breakpoint de cache no último turno real; 'Prossiga.' sai sem cache_control"
metrics:
  duration: "~10 min"
  completed: 2026-10-15
requirements: [HAIKU-01, HAIKU-07]
---

# Phase 23 Plan 01: normalização Anthropic de prefill e sampling (PASS 5) Summary

Para alvos `anthropic/*` e `claude-*` no openrouter-chat, o gateway agora acrescenta um turno user `"Prossiga."` quando a conversa termina numa fala do assistant sem tool_calls, e remove `temperature`, `top_p`, `top_k`, `frequency_penalty` e `presence_penalty`. Isso fica no PASS 5 do `BuildOpenRouterDirector`, que roda depois do PASS 4 de cache. Para alvos que não são Anthropic, o body sai idêntico byte a byte.

## Commits (para o hotfix isolado do 23-02)

| Task | Tipo | SHA | Mensagem |
|------|------|-----|----------|
| 1 | RED | 88a78fd | test(23-01): add failing test for Anthropic prefill/sampling normalization |
| 1 | GREEN | e7d1cb7 | feat(23-01): normalização Anthropic de prefill e sampling (anthropic_compat.go) |
| 2 | RED | 344f7a9 | test(23-01): add failing test for PASS 5 prefill normalization in OpenRouter director |
| 2 | GREEN | 77875ae | feat(23-01): PASS 5 de normalização Anthropic no BuildOpenRouterDirector |

Os 4 commits mexem apenas nos 4 arquivos que o plano lista. Nenhum deles tem migration, então dá para aplicar com cherry-pick isolado.

## Verificação (FATOS, saída local)

- `gofmt -l .` (gateway) não retornou nada.
- `go build ./...` e `go vet ./internal/proxy/` passaram sem erro.
- `go test ./internal/proxy/ -run TestAnthropicCompat -count=1 -v` mostra 9 subtestes `--- PASS`.
- `go test ./internal/proxy/ -count=1` deu `ok` com o pacote inteiro, sem regressão em `openrouter_director_test.go` e `openrouter_cache_control_test.go`.
- No `openrouter_director.go`, `injectAnthropicCacheControl(patched)` está na linha 171 e `normalizeAnthropicChatBody(patched)` na linha 185, ou seja, a ordem é PASS 4 e depois PASS 5.
- `TestOpenRouterDirector_PrefillCacheBreakpointOnRealTurn` confirma que o cache_control fica no system e no assistant original, e que o "Prossiga." sai com content string, sem cache_control.
- `TestOpenRouterDirector_PrefillNormalized` confirma que ContentLength e o header Content-Length são iguais a `len(body final)`.

Não houve mudança em produção neste plano. O deploy fica para o 23-02.

## Deviations from Plan

Nenhuma no código: o plano foi executado como escrito. Um detalhe de implementação que está dentro do escopo: o body só é re-serializado quando há remoção de sampling ou turno anexado. Por isso um alvo Anthropic que já está em ordem também sai byte-idêntico, o que é exigido pelo teste de tool loop.

Observação de processo: o hook `clickup-link-enforce.sh` avisou que não há tarefa ClickUp vinculada no worktree. Não criei vínculo porque isso é responsabilidade do orquestrador.

## Threat Flags

Nenhuma. T-23-01 foi mitigado: JSON inválido devolve o body original, com teste cobrindo. T-23-02 também: o código novo não loga nada.

## TDD Gate Compliance

As duas tasks têm o commit `test(...)` (RED, falha confirmada) seguido de `feat(...)` (GREEN). Não houve refactor.

## Self-Check: PASSED
