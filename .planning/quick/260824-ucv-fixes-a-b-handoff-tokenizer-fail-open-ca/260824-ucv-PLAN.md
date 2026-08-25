---
phase: quick-260824-ucv
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/obs/metrics.go
  - gateway/internal/proxy/overcontext.go
  - gateway/internal/proxy/overcontext_test.go
  - gateway/internal/proxy/chat.go
  - gateway/internal/proxy/dynamic_override.go
  - gateway/internal/proxy/errors.go
  - gateway/internal/proxy/dispatcher.go
  - gateway/internal/proxy/dispatcher_test.go
  - gateway/internal/proxy/tokencount.go
  - gateway/internal/proxy/tokencount_test.go
autonomous: true
requirements: [FIX-A, FIX-B]

must_haves:
  truths:
    - "Tenant normal com request over-context contra o tier-0 recebe 200 servido pelo tier-1 (openrouter-chat), nunca mais um 400 exceed_context_size_error cru"
    - "Tenant sensitive com request over-context recebe erro explícito e NENHUM byte do payload sai para provider externo"
    - "Toda cascata por over-context incrementa gateway_over_context_cascaded_total{tenant,upstream}"
    - "O guard RES-07 tokeniza contra o tier-0 EFETIVO (pod dinâmico quando OverrideTier0 ativo), com UPSTREAM_LLM_URL só como fallback"
    - "Over-cap detectado pré-dispatch (tenant normal) vai direto ao tier-1 sem gastar a chamada no pod"
    - "Um 400 de over-context NÃO abre o breaker do tier-0 (não é degradação do upstream)"
    - "Requests SSE (stream:true) nunca sofrem failover pós-resposta do upstream (D-07 preservado)"
    - "Cascata STT (errUpstreamRetryable de audio.go / gemini_stt_director.go) continua funcionando sem alteração"
  artifacts:
    - path: "gateway/internal/proxy/overcontext.go"
      provides: "sentinel errOverContextFallthrough + chatOverContextInterceptor + classificador de envelope"
      contains: "errOverContextFallthrough"
    - path: "gateway/internal/proxy/overcontext_test.go"
      provides: "cobertura de elegibilidade do interceptor (streaming/sensitive/override/não-over-context)"
    - path: "gateway/internal/obs/metrics.go"
      provides: "OverContextCascadedTotal counter vec"
      contains: "gateway_over_context_cascaded_total"
  key_links:
    - from: "gateway/internal/proxy/chat.go"
      to: "chatOverContextInterceptor"
      via: "ComposeInterceptors prepend"
      pattern: "chatOverContextInterceptor"
    - from: "gateway/internal/proxy/dynamic_override.go"
      to: "chatOverContextInterceptor"
      via: "role == \"llm\" prepend (emergency_pod_llm é quem serve o chat hoje)"
      pattern: "role == \"llm\""
    - from: "gateway/internal/proxy/dispatcher.go"
      to: "TokenCounter.Enforce"
      via: "passa t0.URL resolvido (Resolve movido para ANTES do Enforce)"
      pattern: "Enforce\\(.*t0\\.URL"
    - from: "gateway/internal/proxy/dispatcher.go"
      to: "cascadeTier1"
      via: "over-cap pré-dispatch de tenant normal"
      pattern: "ErrContextLengthExceeded"
---

<objective>
Fazer o ai-gateway cumprir o contrato de fallback na classe "over-context" (HANDOFF-tokenizer-fail-open-pod-ctx-16k.md, Fixes A + B):

- **A (recuperação):** um 400 `exceed_context_size_error` vindo do tier-0 vira sinal de ROTEAMENTO — cascata para tier-1 — em vez de ser repassado cru ao cliente.
- **B (prevenção):** o guard RES-07 volta a funcionar tokenizando contra o tier-0 EFETIVO (pod dinâmico) em vez do `UPSTREAM_LLM_URL` estático morto; over-cap detectado pré-dispatch de tenant normal roteia direto ao tier-1 em vez de 400.

Purpose: hoje não há nem prevenção (guard inerte por fail-open contra `172.18.0.1:18000` que não escuta) nem recuperação (chat.go não tem classificador de status) — 63 `exceed_context_size_error` em 2h matando turnos do Maestro.
Output: interceptor + sentinel novos, métrica de cascata, guard religado, testes cobrindo os 4 caminhos (normal/sensitive × pré-dispatch/pós-400).

FORA DO ESCOPO (não implementar): Fix C (alertas de guard inerte), Fix D (ctx do pod), Fix E (provisionamento fora da janela), deploy.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/debug/HANDOFF-tokenizer-fail-open-pod-ctx-16k.md
@gateway/internal/proxy/dispatcher.go
@gateway/internal/proxy/errors.go
@gateway/internal/proxy/audio.go
@gateway/internal/proxy/tokencount.go
@gateway/internal/proxy/chat.go
@gateway/internal/proxy/dynamic_override.go

<interfaces>
<!-- Contratos já existentes no código. Use direto, não explore o codebase. -->

gateway/internal/proxy/interceptor.go:
  type ProxyResponseInterceptor interface { Intercept(resp *http.Response) error }
  func ComposeInterceptors(ics ...ProxyResponseInterceptor) func(*http.Response) error
  // envelopa o erro com fmt.Errorf("proxy interceptor #%d: %w", i, err) → errors.Is atravessa

gateway/internal/proxy/errors.go:
  var errUpstreamRetryable = errors.New("proxy: upstream retryable error, fall through to next candidate")
  var errDialFailedFallthrough = errors.New(...)
  var ErrContextLengthExceeded = errors.New("proxy: context length exceeded")
  func ErrorHandler(upstreamName string, log *slog.Logger) func(http.ResponseWriter, *http.Request, error)
  // no sentinel: se dispatchResultFrom(ctx) != nil → res.fallthrough_=true, res.wrote=false, res.err=err, SEM escrever

gateway/internal/proxy/dispatcher.go:
  type dispatchResult struct { fallthrough_ bool; wrote bool; err error }
  func dispatchResultFrom(ctx context.Context) *dispatchResult   // nil no caminho dispatchOverride
  func (cfg DispatcherConfig) dispatchTo(w, r, name string, streaming bool, log) dispatchResult
  func (cfg DispatcherConfig) cascadeTier1(w, r, streaming bool, restoreBody func(), log) bool
  func (cfg DispatcherConfig) recordUpstreamFailure(name string)
  func prepareReplayBody(r *http.Request) (func(), bool)

gateway/internal/proxy/tokencount.go:
  const ChatContextCap = 32768; const EmbedContextCap = 8192
  func NewTokenCounter(rdb *redis.Client, llmURL string, log *slog.Logger) *TokenCounter
  func (t *TokenCounter) Enforce(ctx, body []byte, model string, cap int) (int, error)  // ASSINATURA MUDA nesta task
  func tokenCacheKey(model, bodyHash string) string                                     // ASSINATURA MUDA nesta task

gateway/internal/upstreams/loader.go:
  func (l *Loader) Resolve(role string, tier int) (UpstreamConfig, bool)   // honra OverrideTier0 quando tier==0
  func (l *Loader) ResolveAllTier1(role string) []UpstreamConfig
  type UpstreamConfig struct { Name, Role, URL string; Tier int; IsEmergency bool; ... }

gateway/internal/auth/context.go:
  type AuthContext struct { TenantID, APIKeyID string; DataClass DataClass; KeyPrefix string }
  func FromContext(ctx context.Context) (AuthContext, bool)
  const auth.DataClassSensitive

gateway/internal/auditctx:
  func BillingUpstreamFrom(ctx context.Context) string   // nome do upstream despachado (setado em dispatchTo)

gateway/internal/obs/metrics.go (padrão a seguir):
  var DialFallthroughTotal = promauto.NewCounterVec(
    prometheus.CounterOpts{Name: "gateway_dial_fallthrough_total", Help: "..."},
    []string{"role", "outcome"},
  )
</interfaces>

**Envelope real do llama-server (medido em prod, HANDOFF linha 10):**
`{"error":{"code":400,"message":"request (18068 tokens) exceeds the available context size (16384 tokens), try increasing it","type":"exceed_context_size_error"}}`

**FATOS já verificados (não re-investigar):**
- `emergency_pod_llm` (construído em `cmd/gateway/main.go:664-671` via `proxy.NewDynamicOverrideProxy("llm", ...)`) é quem serve o chat hoje — 17 dispatches vs 12 openrouter-chat. O interceptor TEM que entrar nesse caminho, não só no `NewChatProxy` estático.
- `resp.Request.Context()` é o padrão já usado por interceptores para ler ctx (`interceptor_usage.go:105`, `toolcall.go:70`).
- `ToolCallTerminalGuard` (toolcall.go:203) envolve os proxies de chat; com o ErrorHandler suprimindo o write, o guard não escreve nada (o flag de tool_call só é setado em stream SSE com tool_calls).
- `ChatContextCap = 32768` (per-slot; pod serve 32k/slot desde hoje).
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Fix A — classificar over-context do tier-0 como sinal de roteamento (interceptor + sentinel + métrica)</name>
  <files>gateway/internal/obs/metrics.go, gateway/internal/proxy/overcontext.go, gateway/internal/proxy/overcontext_test.go, gateway/internal/proxy/chat.go, gateway/internal/proxy/dynamic_override.go, gateway/internal/proxy/errors.go, gateway/internal/proxy/dispatcher.go, gateway/internal/proxy/dispatcher_test.go</files>
  <behavior>
    - overcontext_test: 400 + envelope `type:"exceed_context_size_error"` + non-streaming + tenant normal + dispatchResult presente no ctx → Intercept devolve erro com `errors.Is(err, errOverContextFallthrough)` E `errors.Is(err, errUpstreamRetryable)`.
    - overcontext_test: mesmo caso com `stream:true` (flag streaming stampada = true) → Intercept devolve nil (D-07).
    - overcontext_test: mesmo caso com `DataClass == auth.DataClassSensitive` → nil (RES-08).
    - overcontext_test: mesmo caso SEM dispatchResult no ctx (caminho dispatchOverride) → nil (ninguém pode re-rotear; um sentinel viraria 502).
    - overcontext_test: 400 com envelope `invalid_request_error` genérico → nil; 200 → nil.
    - overcontext_test: o corpo da resposta continua legível byte-idêntico depois do Intercept (o peek restaura via io.MultiReader).
    - dispatcher_test (TestDispatcher_OverContext400CascadesToTier1): tier-0 mock responde 400 com o envelope llama.cpp, tier-1 responde 200 → cliente recebe 200 com o corpo do tier-1; `Breaker.EffectiveState("primary-llm")` continua Closed (breaker NÃO pode abrir por over-context, mesmo com ConsecutiveFailures:1); `testutil.ToFloat64(obs.OverContextCascadedTotal.WithLabelValues(tenantID, "primary-llm")) == 1`.
    - dispatcher_test (TestDispatcher_OverContext400_SensitiveNoCascade): mesmo cenário com tenant sensitive → cliente recebe o 400 cru do tier-0 e o servidor tier-1 registra ZERO hits.
  </behavior>
  <action>
Criar `gateway/internal/proxy/overcontext.go` (package proxy) com:

1. `var errOverContextFallthrough = fmt.Errorf("proxy: tier-0 over-context, route to tier-1: %w", errUpstreamRetryable)` — envolve o sentinel existente DE PROPÓSITO: o `ErrorHandler` (errors.go:86) já reconhece `errUpstreamRetryable` via `errors.Is`, então a supressão do write + `fallthrough_` saem de graça, enquanto o dispatcher consegue distinguir a CAUSA com `errors.Is(res.err, errOverContextFallthrough)`. Não alterar a condição do ErrorHandler; apenas atualizar o godoc de `errUpstreamRetryable` em errors.go dizendo que agora também é emitido pelo caminho de chat over-context (non-streaming, pré-byte) além do STT.

2. Flag de streaming em ctx: `type overContextStreamingKey struct{}`, `func withStreamingFlag(ctx context.Context, streaming bool) context.Context` e `func streamingFlagFrom(ctx context.Context) (streaming bool, stamped bool)`. Stampar em `dispatchTo` (dispatcher.go), que já recebe `streaming bool`, junto do `withDispatchResult`. O caminho `dispatchOverride` NÃO stampa — e por isso é inelegível (regra 3 abaixo já cobre, o stamp ausente é a segunda trava).

3. `type chatOverContextInterceptor struct{ log *slog.Logger }` implementando `Intercept(resp *http.Response) error`. Ordem de avaliação (curto-circuita barato antes de tocar no corpo):
   a. `resp == nil || resp.Request == nil` → nil.
   b. `resp.StatusCode != 400 && resp.StatusCode != 413` → nil.
   c. `ctx := resp.Request.Context()`; `dispatchResultFrom(ctx) == nil` → nil (proxy standalone/override: ninguém re-roteia; emitir o sentinel viraria um 502 pior que o 400 atual).
   d. `streaming, stamped := streamingFlagFrom(ctx)`; `!stamped || streaming` → nil (D-07: SSE nunca sofre failover pós-resposta).
   e. `ac, ok := auth.FromContext(ctx)`; `!ok || ac.DataClass == auth.DataClassSensitive` → nil (RES-08 — gate PRIMÁRIO; sensitive nunca vaza payload pro tier-1 externo).
   f. Peek do corpo: ler no máximo 8 KiB com `io.LimitReader`, e SEMPRE restaurar `resp.Body` com um `io.ReadCloser` que combina `io.MultiReader(bytes.NewReader(peek), rest)` + `Close` delegando ao body original (contrato de interceptor.go: nunca fechar o body). Restaurar mesmo quando a classificação der false.
   g. `isOverContextBody(peek)` → false → nil.
   h. Match: incrementar `obs.OverContextCascadedTotal.WithLabelValues(ac.TenantID, auditctx.BillingUpstreamFrom(ctx)).Inc()`, logar `Warn` ("tier-0 over-context; routing to tier-1", com request_id, upstream, status) e devolver `errOverContextFallthrough`.

4. `func isOverContextBody(b []byte) bool`: decodificar `{"error":{"type":"...","code":"...","message":"..."}}` com um struct tolerante (`Code json.RawMessage` — o llama.cpp manda `code` NUMÉRICO 400, a OpenAI manda string; NÃO usar `string` cru senão o Unmarshal falha e o classificador vira no-op). Retorna true quando `type == "exceed_context_size_error"` OU `code == "context_length_exceeded"` (após unquote da RawMessage, ignorando valores numéricos) OU `strings.Contains(strings.ToLower(message), "exceeds the available context size")` OU `strings.Contains(strings.ToLower(message), "maximum context length")`. Falha de parse → false (fail-safe: mantém o comportamento atual de passthrough).

Wiring (SÓ nos proxies de TIER-0 de chat — openrouter-chat NÃO recebe o interceptor; um over-context no tier-1 não tem para onde cascatear e o 400 dele é informação legítima pro cliente. Documentar isso em comentário):
- `chat.go` `NewChatProxy`: `ModifyResponse: ComposeInterceptors(append([]ProxyResponseInterceptor{chatOverContextInterceptor{log: log}}, interceptors...)...)` — prepend, mesmo racional do STT em audio.go (uma resposta que vai cascatear nunca pode passar pelo billing/audit).
- `dynamic_override.go` `NewDynamicOverrideProxy`: ao lado do bloco existente `if role == "stt" {...}`, adicionar `if role == "llm" { interceptors = append([]ProxyResponseInterceptor{chatOverContextInterceptor{log: log}}, interceptors...) }` — este é o `emergency_pod_llm`, o upstream que EMITE o erro em produção.

Dispatcher (dispatcher.go):
- Em `dispatchTo`, stampar a flag: `r = r.WithContext(withStreamingFlag(r.Context(), streaming))`.
- No ramo tier-0 CLOSED, após `if !res.fallthrough_ || res.wrote { return }`: calcular `overCtx := errors.Is(res.err, errOverContextFallthrough)` e chamar `cfg.recordUpstreamFailure(t0.Name)` APENAS quando `!overCtx`. Comentar o porquê: over-context não é degradação do upstream; abrir o breaker do pod por causa de requests grandes mandaria TODO o tráfego (inclusive o que cabe) para o provider pago.
- Em `cascadeTier1`, mesma regra: no `if res.fallthrough_ && !res.wrote`, pular `recordUpstreamFailure(t1.Name)` quando `errors.Is(res.err, errOverContextFallthrough)`.
- O hard-gate sensitive existente (`if sensitive { ...writeSensitiveBlock... }`) permanece INTACTO como defesa em profundidade — com o gate (e) do interceptor, um tenant sensitive nunca chega lá por over-context, mas a trava dupla fica.
- Não mexer na contabilidade de `DialFallthroughTotal`; `OverContextCascadedTotal` é o contador autoritativo desta classe.

obs/metrics.go — adicionar junto ao bloco Phase 12:
```
var OverContextCascadedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "gateway_over_context_cascaded_total",
		Help: "Requests that exceeded the tier-0 context window and were routed to tier-1 (external, paid), by tenant and the tier-0 upstream that could not serve them. ALERTABLE: sustained growth = invisible external spend.",
	},
	[]string{"tenant", "upstream"},
)
```
Label `upstream` = SEMPRE o upstream tier-0 que não conseguiu servir (nunca o tier-1 que serviu) — vale para os dois caminhos (interceptor e pré-dispatch da Task 2). Documentar essa semântica no godoc.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && gofmt -l gateway/internal/proxy gateway/internal/obs && go build ./... && go test ./gateway/internal/proxy/... -run 'OverContext|Chat|Dispatcher' -count=1</automated>
  </verify>
  <done>Interceptor emite errOverContextFallthrough só para (400/413 + envelope over-context + non-streaming + non-sensitive + dispatcher-driven); cascata tier-1 acontece; breaker do tier-0 não abre; métrica incrementa; sensitive recebe o 400 sem tráfego externo.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Fix B — tokenizar contra o tier-0 efetivo e rotear over-cap pré-dispatch</name>
  <files>gateway/internal/proxy/tokencount.go, gateway/internal/proxy/tokencount_test.go, gateway/internal/proxy/dispatcher.go, gateway/internal/proxy/dispatcher_test.go</files>
  <behavior>
    - tokencount_test: `Enforce(..., tokenizeURL)` com URL não-vazia bate no servidor passado, não no `llmURL` do construtor; com `tokenizeURL == ""` cai no `llmURL` estático (fallback).
    - tokencount_test: mesmo body + mesmo model contra DUAS URLs diferentes (uma devolvendo under-cap, outra over-cap) → cada URL produz seu próprio resultado (a chave de cache inclui a URL; sem cross-contamination entre tokenizers).
    - tokencount_test: `/tokenize` inalcançável continua fail-open `(0, nil)` (política preservada).
    - dispatcher_test: tenant NORMAL over-cap COM tier-1 disponível → servidor tier-1 recebe o request, tier-0 recebe ZERO hits, resposta 200, `OverContextCascadedTotal{tenant,primary-llm}` incrementa.
    - dispatcher_test: tenant SENSITIVE over-cap → 400 `context_length_exceeded`, tier-1 com ZERO hits.
    - dispatcher_test: tenant NORMAL over-cap SEM nenhum tier-1 no loader → 400 `context_length_exceeded` (preserva o comportamento atual quando não há para onde cascatear).
    - dispatcher_test: o `Enforce` recebe a URL do tier-0 resolvido — com `loader.OverrideTier0("llm", podURL)` ativo, o servidor de tokenize consultado é o do pod, não o estático.
  </behavior>
  <action>
`tokencount.go`:
- Mudar a assinatura para `func (t *TokenCounter) Enforce(ctx context.Context, body []byte, model string, cap int, tokenizeURL string) (int, error)`. Dentro: `effective := tokenizeURL; if effective == "" { effective = t.llmURL }`; guarda de fail-open vira `if t.rdb == nil || effective == "" { return 0, nil }`; o POST vai para `effective + "/tokenize"`.
- Chave de cache passa a incluir a URL do tokenizer: `func tokenCacheKey(model, urlFingerprint, bodyHash string) string` → `"gw:tokenize:" + model + ":" + urlFingerprint + ":" + bodyHash`, com `urlFingerprint` = primeiros 8 hex de `sha256(effective)`. Racional (mesmo do Pitfall 6 que já motivou incluir o model): o llama-server do pod e o local-llm estático são tokenizers distintos; compartilhar slot aprovaria silenciosamente request over-cap.
- Atualizar o godoc do pacote/tipo: o tokenizer segue o tier-0 EFETIVO passado pelo dispatcher (que já honra `OverrideTier0`); `llmURL` do construtor é só fallback de boot. A política fail-open continua INTACTA e é intencional — o que muda é CONTRA QUEM se tokeniza. NÃO alterar `NewTokenCounter` (assinatura preservada → `cmd/gateway/main.go:809` fica intocado).

`dispatcher.go` (dentro do handler de `NewDispatcher`), reordenar os passos 1–3:
- Mover o bloco "3. Resolve tier-0" (`t0, ok := cfg.Loader.Resolve(cfg.Role, 0)` + o 503 `upstream_unavailable`) para ANTES do bloco "1. Token-count enforcement". Mover também `sensitive := ac.DataClass == auth.DataClassSensitive` para junto (é derivado só de `ac`). Deixar `t0State := cfg.Breaker.EffectiveState(t0.Name)` e o bloco `t0.IsEmergency → RegisterTraffic` onde estão (depois do enforcement). Renumerar os comentários dos passos e atualizar o comentário-cabeçalho do arquivo (linhas 1-25) que hoje documenta a ordem antiga e o cap "chat=16k" — corrigir para 32k e para a ordem nova.
- Passar a URL resolvida: `cfg.TokenCounter.Enforce(r.Context(), body, modelName, cfg.ContextCap, t0.URL)`.
- Trocar o tratamento do `ErrContextLengthExceeded` (hoje 400 seco para todo mundo) por:
  1. `sensitive` → mantém o 400 `invalid_request_error`/`context_length_exceeded` + `log.Warn` ("over-context blocked for sensitive tenant; no external routing"). Nenhum tráfego externo. Return.
  2. `len(cfg.Loader.ResolveAllTier1(cfg.Role)) == 0` → mantém o 400 (sem fallback configurado, o erro explícito é a melhor resposta; preserva a semântica do embed, cujo cap BGE-M3 8192 é físico). Return.
  3. Caso restante (tenant normal + tier-1 existe) → `obs.OverContextCascadedTotal.WithLabelValues(ac.TenantID, t0.Name).Inc()`; `log.Warn` com tokens contados, cap e upstream pulado; `streaming := IsStreamingRequest(r)`; `restoreBody, _ := prepareReplayBody(r)`; `cfg.cascadeTier1(w, r, streaming, restoreBody, log)`; return.
     Nota explícita em comentário: aqui `streaming == true` É permitido — D-07 só proíbe failover DEPOIS que bytes foram para o cliente, e neste ponto nada foi escrito (o tier-0 nem foi chamado).
     Segunda nota: neste caminho `EmergTraffic.RegisterTraffic()` não é chamado, de propósito — o pod não serviu esse request, então ele não deve contar para o idle-grace.
- Capturar a contagem retornada pelo `Enforce` (hoje descartada em `_`) para poder logar `tokens`/`cap`.

`dispatcher_test.go`:
- Reescrever `TestDispatcher_OverContextCapReturns400` nos três casos do bloco `<behavior>` (sensitive → 400; normal com tier-1 → cascata; normal sem tier-1 → 400). Usar `upstreams.NewLoaderInMemory` só com a linha tier-0 no caso "sem tier-1". O helper `makeRequest(t, body, dataClass)` e a fixture `newDispatcherFixture(t, "llm")` já existem.
- Adicionar o caso com `f.loader.OverrideTier0("llm", tokenizeSrv.URL)` provando que o tokenize consultado segue o override (registrar `emergency_pod_llm` no mapa `Proxies`).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && gofmt -l gateway/internal/proxy && go build ./... && go test ./gateway/internal/proxy/... -count=1</automated>
  </verify>
  <done>Guard tokeniza contra o tier-0 efetivo (pod quando override ativo), chave de cache separa tokenizers, over-cap de tenant normal com tier-1 vai direto à cascata sem tocar o pod, sensitive e "sem tier-1" continuam 400 explícito.</done>
</task>

<task type="auto">
  <name>Task 3: Gates repo-wide + varredura de regressão (STT, integração, call sites)</name>
  <files>gateway/internal/integration_test/, gateway/internal/proxy/</files>
  <action>
Fechar o blast radius das duas mudanças de assinatura e provar que nada mais quebrou:

1. `grep -rn "\.Enforce(" gateway --include=*.go` e `grep -rn "tokenCacheKey(" gateway --include=*.go` — corrigir TODO call site remanescente (esperado: `dispatcher.go` + `tokencount_test.go`; `cmd/gateway/main.go` NÃO deve aparecer porque `NewTokenCounter` não mudou — se aparecer, algo saiu do plano).
2. Rodar a suíte inteira do gateway + a suíte de integração com build tag (o check local do executor NÃO cobre `-tags integration`; o CI é o gate real — memória `gateway-integration-tests-not-in-executor-check`).
3. Regressão STT obrigatória (HANDOFF item 5): confirmar que `sttRetryableStatusInterceptor` e o director do Gemini seguem intactos e verdes — `audio.go` e `gemini_stt_director.go` NÃO podem ter sido tocados (`git diff --stat` deve listar apenas os arquivos de `files_modified` deste plano).
4. `go vet ./...` limpo nos pacotes tocados.

Se algum teste de integração exercitar `NewChatProxy` (ex.: `integration_test/tool_call_partial_test.go`, `goroutine_leak_test.go`), verificar que o interceptor novo não altera o comportamento deles (respostas 200/SSE → interceptor devolve nil no passo (b)).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && gofmt -l gateway | tee /dev/stderr | wc -l | grep -qx 0 && go vet ./gateway/internal/proxy/... ./gateway/internal/obs/... && go test ./gateway/... -count=1 && go test -tags integration ./gateway/internal/... -count=1 && git diff --stat --name-only | grep -vE '^(gateway/internal/(proxy|obs)/|\.planning/)' | wc -l | grep -qx 0</automated>
  </verify>
  <done>gofmt limpo, vet limpo, suíte completa + integração verdes, diff restrito aos arquivos previstos, STT intocado.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| tenant → gateway | corpo do request (prompt/tools) do cliente autenticado |
| gateway → tier-1 externo (OpenRouter) | payload do tenant SAI da infra iFix — fronteira LGPD (RES-08) |
| tier-0 (pod Vast) → gateway | corpo da resposta de erro é parseado pelo interceptor novo |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-ucv-01 | Information Disclosure | caminho novo de cascata por over-context | mitigate | Gate duplo: (e) no `chatOverContextInterceptor` (`ac.DataClass == DataClassSensitive` → nil, sentinel nunca emitido) + hard-gate `sensitive` já existente no dispatcher antes de `cascadeTier1`; teste obrigatório afirmando ZERO hits no servidor tier-1 para tenant sensitive (Task 1 e Task 2) |
| T-ucv-02 | Denial of Service (financeiro) | cascata silenciosa para provider pago | mitigate | `gateway_over_context_cascaded_total{tenant,upstream}` incrementada em TODA cascata desde o primeiro deploy + `log.Warn` com tokens/cap/upstream; label `upstream` = tier-0 que não serviu |
| T-ucv-03 | Tampering | parse do envelope de erro do upstream no interceptor | mitigate | Peek limitado a 8 KiB (`io.LimitReader`), decode tolerante (`code` como `json.RawMessage`), falha de parse → false (passthrough, fail-safe); body restaurado byte-idêntico via `io.MultiReader` |
| T-ucv-04 | Repudiation | resposta parcialmente escrita re-roteada (D-07) | mitigate | Sentinel só é emitido quando a flag de streaming está stampada E é false; `ModifyResponse` roda pré-byte; `dispatchResultFrom(ctx) == nil` (override/standalone) também bloqueia |
| T-ucv-05 | Denial of Service | breaker do tier-0 aberto por requests grandes → todo o tráfego migra pro pago | mitigate | `recordUpstreamFailure` pulado quando `errors.Is(res.err, errOverContextFallthrough)`, nos dois laços (tier-0 CLOSED e `cascadeTier1`); teste afirma breaker Closed após over-context com `ConsecutiveFailures: 1` |

Sem instalação de pacotes (npm/pip/cargo/go get) neste plano — nenhum gate de legitimidade de pacote aplicável.
</threat_model>

<verification>
1. `gofmt -l gateway` vazio; `go build ./...`; `go vet` limpo.
2. `go test ./gateway/... -count=1` e `go test -tags integration ./gateway/internal/... -count=1` verdes.
3. Testes novos cobrindo os 4 quadrantes: {pré-dispatch, pós-400} × {normal, sensitive}.
4. `git diff --name-only` restrito a `gateway/internal/proxy/*` + `gateway/internal/obs/metrics.go` (+ `.planning/`). `audio.go` e `gemini_stt_director.go` intocados.
5. Validação em prod é do orquestrador (fora do escopo deste plano): após deploy, repro do HANDOFF — request ~20k tokens com tenant `chat-ifix` e pod servindo → 200 por `openrouter-chat`; tenant `telefonia` mesmo payload → erro explícito sem tráfego externo; `docker logs` sem `tokencount /tokenize request failed` apontando para `172.18.0.1:18000`.
</verification>

<success_criteria>
- Nenhum `exceed_context_size_error` do tier-0 chega cru a um tenant normal em requisição non-streaming.
- Nenhum payload de tenant sensitive é roteado ao tier-1 por over-context (nem pré-dispatch, nem pós-400).
- `gateway_over_context_cascaded_total` existe, tem labels `{tenant,upstream}` e incrementa nos DOIS caminhos.
- `TokenCounter.Enforce` tokeniza contra a URL do tier-0 resolvido; fail-open preservado; cache separado por tokenizer.
- Cascata STT inalterada; suíte completa + integração verdes.
</success_criteria>

<output>
Create `.planning/quick/260824-ucv-fixes-a-b-handoff-tokenizer-fail-open-ca/260824-ucv-SUMMARY.md` when done
</output>
