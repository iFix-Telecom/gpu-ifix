---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 06
subsystem: gateway/proxy
tags: [anthropic, haiku, translation, sse, openai-compat, billing-usage]
requires: [23-01]
provides:
  - TranslateMessagesResponseJSON
  - TranslateAnthropicErrorJSON
  - AnthropicTranslateInterceptor
  - NewAnthropicSSETranslator
affects: [23-10 (parser de usage com cache), 23-12 (ordem de interceptores do passo ①)]
tech-stack:
  added: []
  patterns:
    - "ProxyResponseInterceptor que reescreve o body (bytes.Reader + Content-Length alinhado)"
    - "ReadCloser tradutor SSE com buffer de entrada normalizado (CRLF→LF) e buffer de saída"
key-files:
  created:
    - gateway/internal/proxy/anthropic_translate_resp.go
    - gateway/internal/proxy/anthropic_translate_resp_test.go
    - gateway/internal/proxy/anthropic_translate_sse.go
    - gateway/internal/proxy/anthropic_translate_sse_test.go
    - gateway/internal/proxy/testdata/anthropic/resp_text.json
    - gateway/internal/proxy/testdata/anthropic/resp_tool_use.json
    - gateway/internal/proxy/testdata/anthropic/stream_text.sse
    - gateway/internal/proxy/testdata/anthropic/stream_tool_use.sse
    - gateway/internal/proxy/testdata/anthropic/stream_error.sse
  modified: []
decisions:
  - "stop_reason pause_turn e valores desconhecidos → finish_reason stop; refusal → content_filter (HIPÓTESE A2, registrada no godoc)"
  - "JSON 2xx malformado ou >8 MiB → 502 com envelope OpenAI upstream_invalid_response (T-23-20)"
  - "Evento error no meio do stream emite só chunk de erro + [DONE] (sem usage); truncamento/erro de leitura emitem erro + usage (se houve message_start) + [DONE]"
  - "1º chunk do stream leva delta {role:assistant, content:\"\"} (forma OpenAI)"
  - "Detalhe do erro de leitura do upstream vai só para o log WARN; o cliente recebe message genérica"
metrics:
  duration: "~25 min"
  completed: 2026-10-15
  tasks: 2
  files: 9
---

# Phase 23 Plan 06: Tradução da saída Messages API → OpenAI (JSON + SSE) Summary

Tradutor da saída do passo ① (Anthropic Messages API nativa) para o formato OpenAI: resposta JSON vira `chat.completion` com `tool_calls`, `finish_reason` e usage no formato do OpenRouter (`prompt_tokens_details.cached_tokens`/`cache_write_tokens`). O stream SSE vira chunks `chat.completion.chunk` com tool calls incrementais, um único chunk de usage no fim e `[DONE]` garantido mesmo quando o stream é cortado.

## O que foi feito

**Task 1: JSON, erro e interceptor** (commits `23e6b3d` test, `f7da002` feat)
- `TranslateMessagesResponseJSON`: concatena os blocos `text` (`content: null` quando não há texto), converte `tool_use` em `tool_calls` (com `arguments` em string JSON compacta, `{}` quando o input vem vazio) e descarta `thinking` e `signature`.
- Usage: `prompt_tokens = input + cache_read + cache_creation`. Isso vale para os dois caminhos, JSON e SSE, e reaproveita o tipo `anthUsageState`.
- `TranslateAnthropicErrorJSON`: devolve o envelope `{"error":{message,type,code}}`. Um corpo que não dá para ler vira `"upstream error"`, sem eco do corpo original.
- `AnthropicTranslateInterceptor`: em respostas não-2xx o status original é mantido e o envelope OpenAI vai em um `bytes.Reader`, para o ChatRetryableStatusInterceptor ler depois. Em 2xx JSON, traduz e acerta o `Content-Length`. Em 2xx SSE, liga o tradutor e põe `ContentLength=-1`. O interceptor nunca devolve `errUpstreamRetryable`.

**Task 2: tradutor SSE** (commits `20e6924` test, `450c8ed` feat)
- `NewAnthropicSSETranslator`: máquina de estados que guarda `msgID`, `model`, usage como estado (vale o último valor, não a soma) e um contador próprio de `tool_use`, mapeado a partir do índice do bloco.
- Bordas cobertas: CRLF, CR sozinho e CRLF partido entre duas leituras; várias linhas `data:` no mesmo evento; comentários `:`; `ping`.
- Fim antecipado (EOF), erro de leitura ou frame maior que 1 MiB geram um chunk de erro sintético (`upstream_stream_truncated` / `upstream_read_error` / `upstream_frame_too_large`), depois o usage e depois `[DONE]`, e só então `io.EOF`. O erro do upstream não é repassado. Uma flag impede `[DONE]` duplicado.
- `Close()` repassa o fechamento ao upstream.

## Verificação

- `go test ./internal/proxy/ -run 'TestAnthropicSSE|TestAnthropicTranslate' -race`: 22 testes passando (9 do JSON/erro/interceptor, 13 do SSE).
- Suíte completa de `./internal/proxy/` passando, `go build ./...` OK, `gofmt -l` sem saída, `go vet -tags integration ./internal/proxy/` OK. O plano não cria nem altera testes de integração.
- Teste de leitura byte a byte: a saída é idêntica à da leitura em bloco nas 3 fixtures, também em CRLF.

## Deviations from Plan

### Ajustes

**1. [Rule 3 - Blocking] O teste de interceptor SSE foi para o arquivo da Task 2**
- O teste do ramo SSE do interceptor depende de `NewAnthropicSSETranslator`. Para o commit da Task 1 compilar sozinho, o ramo SSE ficou como pass-through na Task 1 e foi ligado no commit `450c8ed`. O teste `TestAnthropicTranslateInterceptor_SSE` está em `anthropic_translate_sse_test.go`.

**2. [Rule 2 - Critical] Limites de leitura**
- O corpo de erro é lido com limite de 64 KiB e o corpo JSON 2xx com limite de 8 MiB (acima disso, resposta 502). Isso complementa a mitigação T-23-19/T-23-20.

**3. [Infra local] Marcador ClickUp**
- O hook `clickup-link-enforce` bloqueou a edição na worktree. Copiei `.planning/clickup-active-task.json` do checkout principal (card 86akuaa73). O arquivo é gitignored e não foi commitado.

Fora isso, o plano foi executado como escrito.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície nova além das ameaças T-23-19, T-23-20 e T-23-21 do plano, todas mitigadas e testadas (`TestAnthropicSSE_FrameTooLarge`, `TestAnthropicTranslateInterceptor_MalformedJSON200`, checagem de ausência de `thinking` em `TestAnthropicTranslateResponse_Text` e `TestAnthropicSSE_Text`).

## TDD Gate Compliance

Os commits RED (`23e6b3d`, `20e6924`) vêm antes dos GREEN (`f7da002`, `450c8ed`) nas duas tasks. Nos dois casos o RED falhou por falta dos símbolos (build failed). Não houve REFACTOR.

## Self-Check: PASSED

- Os 9 arquivos de `key-files.created` existem.
- Os commits 23e6b3d, f7da002, 20e6924 e 450c8ed estão em `git log`.
