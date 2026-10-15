---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 12
subsystem: gateway/proxy + gateway/admin
tags: [anthropic, haiku, reverseproxy, keypool, fallthrough, admin, tdd]
requires: [23-04, 23-05, 23-06, 23-08, 23-09]
provides:
  - BuildAnthropicProxy / AnthropicProxyConfig (consumido pelo wiring e pela integração do 23-14)
  - NewChatFallthroughTransport (① aqui; ② e openrouter-tts no 23-14)
  - NewAnthropicKeysHandler (rota registrada no adminRouter no 23-14)
affects: [23-14]
tech-stack:
  added: []
  patterns:
    - "Director marca o ctx (*r = *r.WithContext) e um RoundTripper guard responde localmente sem rede"
    - "Transport em camadas: guard → KeyPool → fallthrough pré-byte → http.Transport"
key-files:
  created:
    - gateway/internal/proxy/anthropic_director.go
    - gateway/internal/proxy/anthropic_director_test.go
    - gateway/internal/admin/anthropic_keys_http.go
    - gateway/internal/admin/anthropic_keys_http_test.go
  modified:
    - gateway/internal/proxy/transport.go
    - gateway/internal/proxy/transport_test.go
decisions:
  - "Modelo alvo: resolver.Resolve(alias, anthropic-haiku); sem linha (passthrough) e alias que não começa com claude- → claude-haiku-5-5"
  - "Body intraduzível (JSON inválido, messages ausente/vazio, body vazio) → 400 sintético local no formato Anthropic, que o tradutor de saída converte em invalid_request_error OpenAI; a Anthropic recebe 0 requests"
  - "Skip de tool loop = errAnthropicToolLoopSkip (Is errUpstreamRetryable, SkipBreaker true), sem rede"
  - "Resposta admin sempre traz configured (true com pool); datas em RFC3339 UTC; 405 com Allow: GET"
  - "isPreByteTimeout checa a frase de header timeout ANTES dos sentinels de context (bug do errTimeout HTTP/1, ver desvios)"
metrics:
  duration: ~30min
  completed: 2026-10-15
  tasks: 2
  files: 6
---

# Phase 23 Plan 12: BuildAnthropicProxy (passo ①) + GET /admin/anthropic/keys Summary

Construtor exportado do passo ①: chat/completions OpenAI entra, sai `POST /v1/messages` com `x-api-key` do pool, `anthropic-version: 2023-06-01`, effort resolvido (cliente > tenant > high) e `cache_control`. A resposta JSON ou SSE volta em formato OpenAI. 429/5xx/529, timeout de cabeçalho e falha de dial viram fallthrough pré-byte. Junto vem o endpoint admin read-only do pool de chaves. Tudo foi provado contra um fake da Anthropic (httptest + miniredis), sem chave real.

## O que foi feito

**Task 1: BuildAnthropicProxy** (`c4ac57e` RED, `154bb76` GREEN)
- `anthropic_director.go`: `AnthropicProxyConfig` e `BuildAnthropicProxy`. Valores padrão quando zerados: HeaderTimeout 120 s, DefaultMaxTokens 16000, ThinkingHeadroom 4096, MaxTokensCap 128000, UpstreamName `anthropic-haiku`.
  - Director: `BuildDirector` (remove Authorization/X-API-Key do cliente e Accept-Encoding) → filtro `/v1/chat/completions` → skip de tool loop → `ResolveLLMEffort` → `TranslateOpenAIToMessages` (Cache true, ToolLoopMode repassado) → path `/v1/messages`, headers e Content-Length. O Director não seta x-api-key, que fica a cargo do Transport. O log Debug leva upstream, effort e effort_source, nunca o body nem a chave.
  - Transport: `anthropicLocalGuard{next: NewAnthropicKeyPoolTransport(NewChatFallthroughTransport(&http.Transport{...ResponseHeaderTimeout: cfg.HeaderTimeout}), pool, log)}`.
  - ModifyResponse: `ComposeInterceptors(AnthropicTranslateInterceptor(), ChatRetryableStatusInterceptor(), cfg.Interceptors...)`. FlushInterval -1. ErrorHandler sentinel-aware.
- `transport.go`: `NewChatFallthroughTransport` (com o godoc da decisão dos 120 s e do pior caso ①+② presos 120 s cada) e a correção do `isPreByteTimeout` (ver desvios).

**Task 2: GET /admin/anthropic/keys** (`e00a710` RED, `f3a26fc` GREEN)
- `anthropic_keys_http.go`: `NewAnthropicKeysHandler(pool, log)` devolve `{"keys":[{label,fingerprint,state,until,next_probe_at,reason}],"configured":true}`. Com pool nil devolve `{"keys":[],"configured":false}`. Método diferente de GET dá 405. A métrica `GatewayAdminRequests` segue o molde do `List`. A rota é registrada no 23-14.

## Verificação

- FATO: `go test ./gateway/internal/proxy/ -count=1 -race` → ok (20.6 s, pacote inteiro).
- FATO: `go test ./gateway/internal/admin/ -count=1 -race` → ok.
- FATO: `gofmt -l gateway` sem saída, `go vet` limpo, `go build ./gateway/...` ok.
- FATO: no filtro `TestBuildAnthropicProxy|TestNewChatFallthroughTransport`, 26 PASS (contando subtestes). Os casos cobrem JSON, SSE, fallback de modelo, effort (tenant low, cliente medium, `reasoning.enabled:false` → thinking disabled sem effort), 529 com fallthrough, `LastBodyIsOpenAIEnvelope`, dispatchPinned com ① como único candidato com proxy entregando 529 em envelope OpenAI, 400 de prefill traduzido sem cascata, troca de chave com body idêntico, interceptor extra vendo `choices`, 4 formas de body intraduzível (0 requests e 400 local), skip de tool loop (0 requests, SkipBreaker, `countsAsBreakerFailure=false`), skip só para continuação, modo disable, timeout de cabeçalho (fallthrough e nenhuma chave marcada), porta fechada (fallthrough, Redis vazio, Snapshot ok) e validação de config.
- FATO: os critérios de aceite por grep conferem: BuildAnthropicProxy=1, `2023-06-01`=1, `ChatRetryableStatusInterceptor()`=1, ordem na mesma linha=1, `func NewChatFallthroughTransport`=1, uso no director=1, `errAnthropicToolLoopSkip`=2.
- NÃO RODADO: a suíte de integração (`-tags=integration`). O sandbox da worktree bloqueou `sudo -n env HOME=...`. Ela não faz parte do `<verify>` deste plano, mas a correção do `isPreByteTimeout` muda o STT. Ver pendências.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] O timeout de cabeçalho HTTP/1 real nunca cascateava (também no STT)**
- **Encontrado durante:** Task 1 (`TestBuildAnthropicProxy_HeaderTimeoutFallsThrough` falhou com `wrote=true`).
- **Causa (FATO, `net/http/transport.go` do go1.24.9, l.2709-2718):** o `errTimeout` ("net/http: timeout awaiting response headers") implementa `Is(err) == (err == context.DeadlineExceeded)`. O `isPreByteTimeout` testava `errors.Is(err, context.DeadlineExceeded)` antes da frase e devolvia false. O teste unitário antigo usava `errors.New(...)` com a mesma mensagem, então não reproduzia o tipo real.
- **Correção:** a frase passou a ser checada antes dos sentinels de context. O deadline do próprio chamador continua excluído pelo `ctx.Err() != nil`, que roda antes. Novo teste `TestNewChatFallthroughTransport_RealHTTP1HeaderTimeout` com socket real.
- **Efeito colateral:** o `NewSTTFallthroughTransport` passa a cascatear também o timeout de cabeçalho HTTP/1, que era o objetivo declarado do OPERACOES-26927. HIPÓTESE: até agora só o caso http2 (gemini-stt) cascateava em produção. Para confirmar, seria preciso ver nos logs de prod se os upstreams STT HTTP/1 já tiveram header timeout terminal.
- **Arquivos:** transport.go, transport_test.go. **Commit:** 154bb76.

**2. [Ajuste de teste] Helpers duplicados**
- `readFixture` (de anthropic_translate_resp_test.go) e `fakeEffortLookup` (de llm_effort_test.go) já existiam no pacote, então reutilizei os dois em vez de redeclarar.

### Detalhes de implementação não especificados
- Fallback de modelo: o resolver devolve o próprio alias quando não há linha, e um alias como `qwen` chegaria cru à Anthropic. Por isso o alvo só é aceito se diferir do alias ou se começar com `claude-`. Fora isso, vale `claude-haiku-5-5`.
- `http.Transport` sem `Proxy` (mesmo molde do openrouter-chat).

## Pendências

- Rodar a integração `-tags=integration -run 'STT|Fallthrough|Timeout'` fora do sandbox (orquestrador ou 23-14), para confirmar que nenhum teste de integração dependia do header timeout HTTP/1 NÃO cascatear no STT.
- Registro da rota `/admin/anthropic/keys` no adminRouter e o wiring do ① no main ficam com o 23-14.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície nova além do `<threat_model>`. Mitigações cobertas por teste: T-23-37 (`TestAnthropicKeysHandler_NeverLeaksKey`), T-23-38 (Authorization do cliente ausente e x-api-key do pool no fake), T-23-39 (timeout de cabeçalho cascateia), T-23-76 (0 requests no body intraduzível). T-23-40 (auth da rota) é responsabilidade do 23-14.

## TDD Gate Compliance

Task 1: RED `c4ac57e` → GREEN `154bb76`. Task 2: RED `e00a710` → GREEN `f3a26fc`. Os dois RED falharam por símbolos indefinidos. Não houve REFACTOR.

## Self-Check: PASSED

- FOUND: gateway/internal/proxy/anthropic_director.go, anthropic_director_test.go, gateway/internal/admin/anthropic_keys_http.go, anthropic_keys_http_test.go
- FOUND: c4ac57e, 154bb76, e00a710, f3a26fc
