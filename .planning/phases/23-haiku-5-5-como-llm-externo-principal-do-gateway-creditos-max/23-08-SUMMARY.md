---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 08
subsystem: gateway/proxy
tags: [anthropic, haiku-5.5, translator, tool-use, prompt-caching, effort]
requires:
  - 23-01 (normalizeTrailingAssistant, AnthropicPrefillUserText)
provides:
  - TranslateOpenAIToMessages
  - AnthropicRequestOptions
  - IsToolLoopContinuation
  - ToolLoopModeOmit / ToolLoopModeDisable / ToolLoopModeSkip
affects:
  - 23-12 (Director do upstream anthropic-haiku consome o tradutor e IsToolLoopContinuation)
  - 23-14 (env ANTHROPIC_TOOL_LOOP_MODE → opts.ToolLoopMode)
  - 23-18 (gates g1–g5 usam os golden como corpo de request real)
tech-stack:
  added: []
  patterns: [map[string]json.RawMessage na entrada, allowlist de saída, golden files]
key-files:
  created:
    - gateway/internal/proxy/anthropic_translate_req.go
    - gateway/internal/proxy/anthropic_translate_req_test.go
    - gateway/internal/proxy/testdata/anthropic/req_openai_tool_loop.json
    - gateway/internal/proxy/testdata/anthropic/req_messages_tool_loop.golden.json
    - gateway/internal/proxy/testdata/anthropic/req_messages_tool_loop_disable.golden.json
  modified: []
decisions:
  - "Effort off = thinking:{type:disabled} sem output_config.effort (FATO: doc confirma no Haiku 5.5 com effort ≤ high); sem fallback low"
  - "Modo ToolLoopMode disable existe (mesmo FATO); na continuação não soma ThinkingHeadroom"
  - "System no meio: nativa pela regra de posição; fora dela dobra no user anterior, senão no próximo; sem user adjacente vira bloco extra do system top-level"
  - "Turnos consecutivos do mesmo role são mesclados; em turno user os tool_result vêm primeiro"
  - "stop → stop_sequences (sem strings vazias), adição à tabela do research"
metrics:
  duration: ~35min
  completed: 2026-10-15
  tasks: 2
  files: 5
---

# Phase 23 Plan 08: Tradutor OpenAI → Anthropic Messages (entrada do passo ①) Summary

Tradutor puro `TranslateOpenAIToMessages` com saída por allowlist. Ele monta system top-level com breakpoint de cache, tool_use/tool_result, tool_choice com `disable_parallel_tool_use`, `output_config.format`/`effort`, `thinking:disabled` para effort off e para o modo disable do tool loop, e max_tokens com headroom e teto. A cobertura é de golden files dos modos omit e disable e de 41 subtestes.

## Pontos incertos resolvidos na doc oficial (FATO, consultado em 2026-10-15)

| Ponto | Regra confirmada | Fonte |
|---|---|---|
| A1 parallel | `tool_choice.disable_parallel_tool_use: true` (auto/any/tool) | platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use |
| A1 structured outputs | `output_config.format = {type:"json_schema", schema}` | platform.claude.com/docs/en/build-with-claude/structured-outputs |
| Effort | `output_config.effort` ∈ low/medium/high/xhigh/max; default do Haiku 5.5 = medium | platform.claude.com/docs/en/build-with-claude/effort |
| Effort off | Haiku 5.5 aceita `thinking:{type:"disabled"}` com effort ≤ high; com xhigh ou max dá 400 | platform.claude.com/docs/en/build-with-claude/thinking |
| System no meio da conversa | Suportada no Haiku 5.5 sem beta. Precisa vir logo após um turno user (user com tool_result conta) e ser a última mensagem ou vir antes de um assistant | platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages |
| Cache | `cache_control` top-level automático, combinável com breakpoint explícito | platform.claude.com/docs/en/build-with-claude/prompt-caching |

Não houve fallback de effort off para "low". O modo `disable` existe de fato.

**HIPÓTESE H10 (segue aberta):** omitir os blocos thinking na continuação do tool loop não provoca 400. A doc pede que os blocos voltem intactos. O gate ao vivo do 23-18 decide entre omit, disable e skip antes de ligar o ①.

## Tasks

| Task | Nome | Commit |
|---|---|---|
| 1 | Golden e testes (RED) | aa7f8e5 |
| 2 | Implementar TranslateOpenAIToMessages (GREEN) | 0257f41 |

Verificação: `gofmt -l` limpo, `go vet` limpo, `go build ./...` ok. `go test ./internal/proxy/` passou no pacote inteiro. O plano não cria nem altera testes de integração.

## Deviations from Plan

1. **[Rule 1 - Bug] Fixture do tool loop termina em `tool`, não em `user`.** A behavior da Task 1 descrevia "+ user" no fim. Com isso a entrada deixaria de ser continuação, e o golden `_disable` não teria como divergir do omit. O fixture virou uma continuação de verdade, que é o corpo que o 23-18 usa nos gates. O caso "tool seguido de user" ganhou um subteste próprio: os blocos tool_result e o texto ficam num único turno user.
2. **[Rule 2 - Correção] Mensagem system depois de assistant no fim da conversa.** O caso `[system, user, assistant, system]` manda o system para o top-level, conforme o plano. Isso deixaria a conversa terminando em assistant, o que gera 400 de prefill. `finish()` passou a anexar o turno "Prossiga." também nesse caso e quando a lista de mensagens sai vazia.
3. **[Rule 2 - Correção] `tool_result` primeiro em cada turno user.** A Messages API exige essa ordem. O texto mesclado ou dobrado vai depois dos tool_result.
4. **[Rule 2] `stop` → `stop_sequences`.** O campo não estava na tabela do research e a allowlist o descartaria em silêncio, mudando o comportamento do cliente. Strings vazias são filtradas.
5. **Headroom com effort "".** O plano diz "effort ≠ off/low": com effort vazio o modelo roda no default medium e pensa, então o headroom é somado. No modo disable da continuação não há headroom, porque o thinking está desligado (golden `_disable`: max_tokens 1000).
6. **Processo:** o marcador `.planning/clickup-active-task.json` (gitignored, card 86akuaa73) foi copiado do repo principal para a worktree, porque o hook clickup-link bloqueava as edições. Não foi commitado.

## Known Stubs

Nenhum.

## Threat Flags

Nenhum. A superfície é a prevista no threat model: T-23-25 (allowlist), T-23-26 (teto de 128000), T-23-27 (arguments inválido vira `{}`) e T-23-75 (ToolLoopMode). Todas têm teste.

## Self-Check: PASSED

- Os 5 arquivos criados existem.
- Os commits aa7f8e5 e 0257f41 existem no branch.
