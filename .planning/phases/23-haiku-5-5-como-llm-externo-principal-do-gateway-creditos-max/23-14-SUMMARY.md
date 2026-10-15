---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 14
subsystem: gateway/cmd + gateway/config + gateway/upstreams + gateway/proxy + integration_test
tags: [anthropic, haiku, openrouter, tts, wiring, res-08, integration, admin]
requires: [23-04, 23-05, 23-07, 23-10, 23-11, 23-12]
provides:
  - Binário com a cadeia ① anthropic-haiku → ② openrouter-chat → ③ openrouter-deepseek e openrouter-tts registrados
  - proxy.ExternalChatInterceptors (helper único de composição: main + integração)
  - Rotas GET /admin/anthropic/keys e POST /admin/aux-pod-lifecycles no adminRouter autenticado
  - Probes sem custo para anthropic-haiku (GET /v1/models com x-api-key) e openrouter-tts (GET /v1/models?output_modalities=speech)
  - Envs UPSTREAM_LLM_ANTHROPIC_URL/_API_KEYS, UPSTREAM_LLM_EXTERNAL_HEADER_TIMEOUT_SECONDS, ANTHROPIC_KEY_RENEWAL_TZ, ANTHROPIC_TOOL_LOOP_MODE, UPSTREAM_TTS_OPENROUTER_URL, OPENROUTER_TTS_DEFAULT_MODEL/_VOICE/_VOICE_MAP
affects: [23-15, 23-16, 23-17, 23-18]
tech-stack:
  added: []
  patterns:
    - "① e ③ registrados a partir do config (não do loader): a linha nasce enabled=false e o roteamento segue governado pela linha"
    - "Candidato dormente (linha habilitada, sem proxy) loga WARN uma vez por upstream e Debug depois"
key-files:
  created:
    - gateway/internal/upstreams/probe_haiku_test.go
    - gateway/cmd/gateway/main_haiku_test.go
    - gateway/internal/proxy/dispatcher_dormant_test.go
    - gateway/internal/integration_test/haiku_chain_test.go
    - gateway/internal/integration_test/haiku_sensitive_test.go
  modified:
    - gateway/internal/config/config.go
    - gateway/internal/config/config_test.go
    - gateway/internal/upstreams/probe.go
    - gateway/cmd/gateway/main.go
    - gateway/internal/proxy/chat_retryable_status.go
    - gateway/internal/proxy/chat_retryable_status_test.go
    - gateway/internal/proxy/dispatcher.go
decisions:
  - "ExternalChatInterceptors devolve ProxyResponseInterceptor (contrato do plano); o main usa .Intercept como ModifyResponse"
  - "ANTHROPIC_KEY_RENEWAL_TZ inválido não derruba o boot: WARN e fallback America/Sao_Paulo"
  - "openrouter-tts registrado só se loader.Get tiver URL (linha nasce enabled na 0041; o env entra no 23-17)"
  - "Erro de parse das chaves ou de URL do ① → log ERROR sem valor, upstream fica sem proxy (dormente); sem URL ou sem chaves → INFO 'anthropic-haiku dormant (no keys)'"
metrics:
  duration: ~75min
  completed: 2026-10-15
  tasks: 3
  files: 12
---

# Phase 23 Plan 14: wiring da cadeia Haiku no binário + integração ①→②→③ e RES-08 Summary

O gateway agora monta os três passos LLM externos e o TTS do OpenRouter no boot. O ① anthropic-haiku sobe sozinho quando há URL e chaves no env e fica dormente sem elas. O ② openrouter-chat cascateia por status e por timeout de cabeçalho. O ③ openrouter-deepseek é o salto final e devolve o próprio erro. As rotas admin novas estão no mesmo gate X-Admin-Key, e a cadeia e o RES-08 têm testes de integração com os construtores de produção.

## O que foi feito

**Task 1: config + probe** (`6cba00e` RED, `cea95e4` GREEN)
- `config.go`: 9 campos novos com default no código: header timeout 120 s, TZ `America/Sao_Paulo`, tool loop `omit`. Valor inválido em `ANTHROPIC_TOOL_LOOP_MODE` vira `omit` e gera WARN. O config não loga o valor das chaves (há teste para isso).
- `probe.go`: `anthropic-haiku` → `GET <url>/v1/models` com `x-api-key` igual à 1ª chave (o rótulo e o `@dia` são removidos), `anthropic-version: 2023-06-01` e sem `Authorization` (T-23-48). Sem chave utilizável, o probe não envia request e grava o status `config`, que o breaker trata como sucesso. `openrouter-tts` → `GET /v1/models?output_modalities=speech`, sem síntese paga. `openrouter-deepseek` usa o mesmo probe do openrouter-chat. O parse fica no helper local `firstAnthropicKeySecret`, para não criar o ciclo upstreams→proxy.

**Task 2: main.go** (`821fb5a`)
- `proxy.ExternalChatInterceptors(final, rest...)`: com `final=false` compõe `ChatRetryableStatusInterceptor` primeiro; com `final=true` só `rest`. Tem 3 testes unitários.
- `buildOpenRouterChatProxy(u, name, headerTimeout, final, ...)`: com `final=false` o transporte é embrulhado em `NewChatFallthroughTransport`. O ② usa o timeout configurável (saiu o literal de 30 s).
- O ③ `openrouter-deepseek` é montado a partir de `cfg.UpstreamOpenRouterChatURL/AuthBearer`, com `final=true`.
- O ① usa `ParseAnthropicKeys`, `NewKeyPool` (com o mesmo client Redis do breaker) e `BuildAnthropicProxy` com `ToolLoopMode: cfg.AnthropicToolLoopMode`, embrulhado em `ToolCallTerminalGuard`. O log de boot traz só a contagem de chaves e os rótulos+fingerprint (T-23-44).
- `openrouter-tts`: `BuildOpenRouterTTSDirector` + `NewChatFallthroughTransport` (com o timeout TTS existente) + `ExternalChatInterceptors(false, usage)`.
- `GET /admin/anthropic/keys` (com pool nil responde `configured:false`) e `POST /admin/aux-pod-lifecycles` ficam dentro do `adminRouter`. O teste `TestAdminHaikuRoutesRequireKey` (main_haiku_test.go, com o buildRouter real e o cache do Verifier em miniredis) confere 401 sem chave e com chave errada, e 200 com chave válida.

**Desvio Rule 2** (`1cdf0da`): WARN de candidato dormente. Ver os desvios abaixo.

**Task 3: integração** (`e22d02b`)
- `haiku_chain_test.go`: tem 14 testes `TestHaikuChain_*` (A, B, C, D, E, F, G, G2, H, I, J com 2 subcasos, K, L, M) e `TestHaikuChain_TTSStatusCascade` com 4 subcasos. O loader lê o Postgres; ① e ③ são habilitados por `gen.SetUpstreamEnabled`; os aliases `deepseek-flash` e `anthropic/claude-haiku-5.5` são pinados nos 3 upstreams; o estado do banco compartilhado é restaurado no Cleanup. A composição usa `proxy.ExternalChatInterceptors` (4 ocorrências), sem réplica.
- `haiku_sensitive_test.go`: `TestSensitiveNeverExternalHaiku`. Antes do request, o teste afirma que `ResolveAllTier1("llm")` contém ①②③ e que `ResolveAllTier1("tts")` contém openrouter-tts. Cenários cobertos:
  - pod com breaker OPEN e alias pinado: 503 RES-08;
  - modelo sem pino com stream: 503 RES-08;
  - TTS: 503 RES-08;
  - local-llm desabilitado com alias pinado: 503 RES-08.
  
  Todos os proxies externos são panic proxies.

## Verificação

FATOS:
- `gofmt -l gateway`: sem saída.
- `go build ./gateway/...` ok. `go vet ./gateway/cmd/gateway/` ok. `go vet -tags=integration ./gateway/internal/integration_test/` ok, ou seja, os testes de integração compilam.
- `go test ./gateway/... -race -count=1`: todos os pacotes ok, sem falha.
- Critérios por grep conferidos em main.go:
  - `llmRoleProxies["anthropic-haiku"]` = 1, `["openrouter-deepseek"]` = 1;
  - `loader.Get("anthropic-haiku"|"openrouter-deepseek")` = 0;
  - `ttsRoleProxies["openrouter-tts"]` = 1, `"/anthropic/keys"` = 1, `"/aux-pod-lifecycles"` = 1;
  - `ResponseHeaderTimeout: 30 * time.Second`: 2 → 1;
  - `ExternalChatInterceptors(` = 2, `NewChatFallthroughTransport(` = 2, `AnthropicToolLoopMode` = 2.
- Validação dos testes de integração fora do Docker: copiei `haiku_chain_test.go` para um pacote scratch temporário (`p2314scratch`, já apagado), trocando só o loader do Postgres por `NewLoaderInMemory` e o resolver por um construtor scratch com as mesmas linhas de alias. Os 14 `TestHaikuChain_*`, os 4 subcasos de TTS e uma réplica in-memory dos 4 cenários RES-08 passaram (`-race`). Foi essa validação que achou um bug no próprio teste: `breaker.NewSet(nil, ...)` dá panic em `EffectiveState` com o `*redis.Client` nil. Agora os testes usam miniredis ou o Redis do freshSchema.

NÃO RODADO:
- A suíte `-tags=integration` real (testcontainers). O sandbox da worktree recusou `sudo -n env ... go test` e `sudo -n -E go test`, e o usuário não tem acesso direto ao socket do Docker (`permission denied`). O orquestrador precisa rodar, depois do merge: `TestHaikuChain`, `TestSensitiveNeverExternalHaiku`, `Sensitive`, `FallbackRouting`, `ModelAlias`, `TTS`, `Migration0041` e a suíte completa. Partes que só a execução real exercita:
  - `upstreams.NewLoader` lendo as linhas da 0041 com `url_env` setado via `t.Setenv`;
  - `SetUpstreamEnabled` e a restauração;
  - o snapshot e a restauração de `model_aliases`, incluindo a coluna `provider_prefs` com NULL;
  - `seedTenantAndKey`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing critical] WARN por request com ① dormente**
- **Encontrado durante:** Task 3 (caso G).
- **Problema:** com a linha `anthropic-haiku` habilitada e sem chaves, `dispatchPinned`, `cascadeTier1` e o cascade de override logavam `WARN "... has no registered proxy"` em todo request. Isso contrariava o must-have "sem WARN por request".
- **Correção:** `logNoProxySkip` em `dispatcher.go` emite o 1º pulo de cada upstream como WARN e os seguintes como Debug (`sync.Map` por processo). Teste `TestLogNoProxySkip_WarnOncePerUpstream`.
- **Arquivos:** dispatcher.go e dispatcher_dormant_test.go (fora do `files_modified` do plano). **Commit:** `1cdf0da`.

**2. [Rule 1 - Bug no teste] breaker.Set com Redis nil**
- O rascunho do teste usava `breaker.NewSet(nil, ...)`, que dá panic em `EffectiveState`. O problema foi pego na validação scratch, antes do commit, e corrigido com miniredis e com o Redis do freshSchema.

### Ajustes de estrutura (sem mudança de comportamento)
- Os testes de probe foram para `probe_haiku_test.go` e não para `probe_test.go`, por causa dos imports próprios.
- O teste de 401 das rotas admin ficou em `main_haiku_test.go` (package main): o pacote de integração não alcança o `buildRouter`. O plano previa esse caminho como alternativa.
- O pino de TTS no teste de integração usa um `ModelPinner` fake (`staticPins`). FATO (migration 0005, l.7): `model_aliases.upstream` tem `CHECK (upstream IN ('llm','stt','embed'))`, então uma linha de alias com role `tts` não pode ser gravada. Por isso o caso pinado do TTS usa o fake, e o 4º subcaso cobre o caminho real sem pino (`cascadeTier1` por prioridade).
- No teste, o openrouter-tts é montado sem `usageInterceptor`, que exige billing. A composição continua sendo a do main (`ExternalChatInterceptors(false, ...)`).

## Observações para os planos seguintes

- HIPÓTESE: o pino `tts-1 → [openrouter-tts, kokoro-tts]` via `model_aliases` não é possível em prod por causa do CHECK da 0005, então o TTS em prod depende da ordem `tier_priority` do tier 1 (`cascadeTier1`, com openrouter-tts na prio 0 pela 0041). Para confirmar, basta ver se algum plano (23-15/17) pretende gravar alias de TTS. Se pretender, precisa de migration ampliando o CHECK.
- `url_override` via admin não vale para ① e ③, que usam a URL do env. A limitação foi aceita no plano.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície além do `<threat_model>`. Cobertura por teste:
- T-23-44: o log de boot traz só rótulos e fingerprint; os testes do config e do probe confirmam que nenhum log contém a chave.
- T-23-45: `TestSensitiveNeverExternalHaiku`.
- T-23-46: `TestAdminHaikuRoutesRequireKey`.
- T-23-47: probes via GET sem custo.
- T-23-48: `TestProbe_AnthropicHaiku_GetModelsWithXAPIKey` confirma que não vai `Authorization`.
- T-23-73: casos K e L e os subcasos de TTS.

## TDD Gate Compliance

Task 1 (`tdd="true"`): RED `6cba00e` (o teste de probe falhou por `firstAnthropicKeySecret` indefinido), GREEN `cea95e4`. Os testes de config entraram junto com o GREEN, sem commit RED separado. Os testes de `ExternalChatInterceptors` falharam primeiro (símbolo indefinido) e foram commitados com a implementação em `821fb5a`.

## Self-Check: PASSED

- FOUND: gateway/internal/integration_test/haiku_chain_test.go, haiku_sensitive_test.go, gateway/internal/upstreams/probe_haiku_test.go, gateway/cmd/gateway/main_haiku_test.go, gateway/internal/proxy/dispatcher_dormant_test.go
- FOUND: 6cba00e, cea95e4, 821fb5a, 1cdf0da, e22d02b
