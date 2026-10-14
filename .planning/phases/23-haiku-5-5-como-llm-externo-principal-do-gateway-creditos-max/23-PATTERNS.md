# Phase 23: Haiku 5.5 como LLM externo principal do gateway — Pattern Map

**Mapeado em:** 2026-10-14
**Arquivos analisados:** 30 (novos + modificados)
**Analogs encontrados:** 28 / 30

> Todos os trechos abaixo foram lidos nesta sessão (FATO, com arquivo:linha). Onde o analog é parcial, está marcado.
> **Correção ao RESEARCH (FATO):** `providerForUpstream` NÃO está em `cmd/gateway/main.go` — está em `gateway/internal/proxy/interceptor_usage.go:379`.

## File Classification

| Arquivo novo/modificado | Papel | Fluxo | Analog mais próximo | Qualidade |
|---|---|---|---|---|
| `gateway/internal/proxy/anthropic_translate_req.go` (novo) | utility (transform puro) | transform | `internal/proxy/openrouter_director.go` (`injectAnthropicCacheControl`, `injectProviderPrefs`) | role-match |
| `gateway/internal/proxy/anthropic_translate_resp.go` (novo) | utility (transform) | transform (JSON) | `internal/proxy/gemini_stt_director.go` (modifyResponse, l.237-295) | exato (tradução de envelope de provedor → OpenAI) |
| `gateway/internal/proxy/anthropic_translate_sse.go` (novo) | utility (io.ReadCloser) | streaming | `internal/proxy/interceptor_usage.go` `usageTeeReader` (l.590-690) + `toolcall.go` `toolCallTee` | role-match |
| `gateway/internal/proxy/anthropic_director.go` (novo) | middleware (Director) | request-response | `internal/proxy/openrouter_director.go` `BuildOpenRouterDirector` + `gemini_stt_director.go` (troca de path/header) | exato |
| `gateway/internal/proxy/anthropic_keypool.go` (novo) | middleware (RoundTripper) + store | request-response + estado Redis | `internal/proxy/transport.go` `fallthroughRoundTripper` + `internal/breaker/force_override.go` | role-match (composição de 2) |
| `gateway/internal/proxy/chat_retryable_status.go` (novo) | middleware (interceptor) | request-response | `internal/proxy/audio.go` `STTRetryableStatusInterceptor` (l.49-71) + `overcontext.go` `chatOverContextInterceptor` (l.79-140) | exato |
| `gateway/internal/proxy/anthropic_compat.go` (novo) | utility | transform | `openrouter_director.go` `injectAnthropicCacheControl` (l.251-304) | exato |
| `gateway/internal/proxy/openrouter_director.go` (mod) | middleware | request-response | ele mesmo (PASS 1-4) — adicionar PASS 5 effort + prefill | — |
| `gateway/internal/proxy/openrouter_tts_director.go` (novo) | middleware (Director) | request-response | `internal/proxy/openai_embed_director.go` (força campo + reescreve model) | exato |
| `gateway/internal/proxy/interceptor_usage.go` (mod) | service (billing) | event-driven | ele mesmo: `sseUsageFrame` l.576, `FinalizeRequest` l.280-330, `providerForUpstream` l.379, `isSelfHostedUpstream` l.561 | — |
| `gateway/internal/proxy/dispatcher.go` (mod) | controller (dispatch) | request-response | ele mesmo: `dispatchResult` l.87, `dispatchPinned` l.524-615 | — |
| `gateway/internal/proxy/errors.go` (mod, opcional) | utility | — | ele mesmo: `ErrorHandler` sentinel-aware | — |
| `gateway/internal/upstreams/probe.go` (mod) | service | request-response | ele mesmo: caso especial `voice-api-piper` (l.299) | exato |
| `gateway/internal/upstreams/loader.go` (mod, Padrão 6) | store | CRUD (snapshot) | ele mesmo: `newSnapshot` l.35-49 | — |
| `gateway/cmd/gateway/main.go` (mod) | config/wiring | — | `buildOpenRouterChatProxy` l.1893-1932 + `buildGeminiSTTProxy` l.1976-2026 + bloco `llmRoleProxies` l.758-784 | exato |
| `gateway/internal/config/config.go` (mod) | config | — | ele mesmo l.423 (`csvOr`), l.471 (`atoiOr`) | exato |
| `gateway/db/migrations/0041_*.sql` (novo) | migration | — | `0036_embed_gpu_tier0.sql` (upstreams re-tier) + `0037_model_aliases_provider_prefs.sql` (coluna em tenants + trigger) + `0012_create_prices_and_fx.sql` (CHECK de unit) | exato |
| `gateway/db/migrations/0042_aux_pod_lifecycles.sql` (novo, se separado) | migration | — | `0023_primary_lifecycles.sql` | exato |
| `gateway/db/queries/billing.sql` (mod) | model (sqlc) | CRUD | ele mesmo: `InsertBillingEvent` l.1-25, `SumBillingAllTenantsRange` l.95-107 | — |
| `gateway/db/queries/tenants_admin.sql` (mod) | model (sqlc) | CRUD | ele mesmo: `GetTenantConfig`/`ListTenantsForLoader`/`UpdateTenantProviderPrefs` | — |
| `gateway/internal/tenants/loader.go` (mod) | store | lookup | `ProviderPrefsByTenantID` l.209-228 | exato |
| `gateway/internal/billing/flusher.go` (mod) | model | batch | `type Event` l.28-44 | — |
| `gateway/cmd/gatewayctl/prices.go` (mod) | CLI | CRUD | ele mesmo l.59-73 (validação de `--unit`) | — |
| `gateway/internal/admin/anthropic_keys_http.go` (novo) | controller (admin read-only) | request-response | `internal/admin/upstreams_admin_http.go` `List` l.111-127 | exato |
| `gateway/internal/admin/economy.go` (mod) | controller | request-response | ele mesmo (l.37-200) | — |
| `dashboard/src/lib/gateway.ts` (mod) | model (tipos espelho) | — | ele mesmo l.285-330 | — |
| `dashboard/src/components/economy-panel.tsx` (mod) | component | — | ele mesmo l.60-95 (`KpiCard`) | — |
| `ops/vast-3060/unified3060.py` (mod) | utility/ops | batch | `real_cost()` l.284; `cmd_start` l.791 / `cmd_stop` l.1062 | parcial |
| `scripts/price-sync/gateway-price-sync.sh` (mod) | ops | batch | ele mesmo l.239-242 (`gatewayctl prices set`) | — |
| Testes unit (`anthropic_*_test.go`, `chat_retryable_status_test.go`, `openrouter_tts_director_test.go`) | test | — | `internal/proxy/openrouter_cache_control_test.go` | exato |
| Integration `haiku_chain_test.go`, `TestSensitiveNeverExternalHaiku` | test | — | `integration_test/fallback_routing_test.go` + `sensitive_block_test.go` | exato |
| bump `migration_0026_test.go` / `migration_0029_test.go` | test | — | eles mesmos (l.93-118 / l.183-192) | exato |

---

## Pattern Assignments

### `gateway/internal/proxy/anthropic_director.go` (Director, request-response)

**Analog:** `gateway/internal/proxy/openrouter_director.go`

**Imports** (l.24-38):
```go
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)
```

**Esqueleto do Director** (l.75-108) — base + filtro de path + leitura do body:
```go
func BuildOpenRouterDirector(upstream *url.URL, authBearer string, providerOrder []string,
	allowFallbacks bool, resolver *models.Resolver, upstreamName string,
	log *slog.Logger, tenantPrefs TenantPrefsLookup) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r) // strips client auth, propagates X-Request-ID, rewrites URL
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			return
		}
		if r.Body == nil {
			return
		}
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil || len(body) == 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}
```
Para o ①: NÃO setar `Authorization` (a chave `x-api-key` vem do KeyPoolTransport); setar `anthropic-version: 2023-06-01`.

**Reescrita de path + header de provedor** — copiar de `gemini_stt_director.go` l.150-158 e l.211-234:
```go
r.Header.Del("Authorization")
if apiKey != "" {
	// CRITICAL: header name is "x-goog-api-key" NOT "X-API-Key"
	// — the latter is stripped by BuildDirector (director.go:32).
	r.Header.Set("x-goog-api-key", apiKey)
}
...
r.URL.Path = newPath
r.URL.RawPath = ""
r.Body = io.NopCloser(bytes.NewReader(jsonBody))
r.ContentLength = int64(len(jsonBody))
r.Header.Set("Content-Type", "application/json")
r.Header.Set("Content-Length", strconv.Itoa(len(jsonBody)))
```
ATENÇÃO (FATO, comentário l.155-156): `BuildDirector` remove `X-API-Key` do cliente → o `x-api-key` da Anthropic precisa ser setado DEPOIS de `base(r)` (no RoundTripper, que roda depois do Director — ok).

**Resolução de modelo + precedência tenant (effort)** — copiar `resolveProviderPrefs` (l.229-241) para `resolveEffort`:
```go
func resolveProviderPrefs(ctx context.Context, tenantPrefs TenantPrefsLookup, resolver *models.Resolver, alias, upstreamName string) []byte {
	if tenantPrefs != nil {
		if ac, ok := auth.FromContext(ctx); ok {
			if p := tenantPrefs.ProviderPrefsByTenantID(ac.TenantID); p != nil {
				return p
			}
		}
	}
	if resolver == nil || alias == "" {
		return nil
	}
	return resolver.ProviderPrefs(alias, upstreamName)
}
```
Decisão LOCKED (CONTEXT): precedência = **cliente explícito (off/low/medium) > tenant > default high**. Interface análoga: `TenantEffortLookup{ EffortByTenantID(string) string }`.

**Logging R12** (l.117-126): só `upstream` + `error_class`, nunca o body.

---

### `gateway/internal/proxy/anthropic_translate_req.go` + `anthropic_compat.go` (transform)

**Analog:** `openrouter_director.go` `injectProviderPrefs` (l.204-211) e `injectAnthropicCacheControl` (l.251-304)

**Padrão de leitura tolerante (map[string]json.RawMessage)** (l.204-211):
```go
func injectProviderPrefs(body []byte, prefs []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["provider"] = json.RawMessage(prefs)
	return json.Marshal(m)
}
```

**Iterar mensagens por role + best-effort (body intacto em falha)** (l.251-290):
```go
var msgs []map[string]json.RawMessage
if err := json.Unmarshal(m["messages"], &msgs); err != nil || len(msgs) == 0 {
	return body
}
for i := len(msgs) - 1; i >= 0 && !(doneSystem && doneConv); i-- {
	var role string
	_ = json.Unmarshal(msgs[i]["role"], &role)
	isSystem := role == "system" || role == "developer"
	...
```
**Content string vs array de blocos** — `markLastTextBlock` (l.309-334) é o molde para converter `content` OpenAI em blocos Messages (string → `[{type:text,text}]`).

**Normalização de prefill (compartilhada ① e ②):** função pura `normalizeTrailingAssistant(body) []byte` no estilo de `injectAnthropicCacheControl` — gate por modelo `anthropic/` (②) ou sempre no ①; best-effort devolvendo o body original em erro. Chamar como PASS novo em `BuildOpenRouterDirector` depois do PASS 4 (l.171).

---

### `gateway/internal/proxy/anthropic_translate_resp.go` (transform JSON não-streaming)

**Analog:** `gemini_stt_director.go` modifyResponse (l.237-295)

**Ler body, traduzir envelope de erro, devolver sentinel, reescrever body + headers**:
```go
modifyResponse := func(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return err
	}
	var gresp geminiResponse
	_ = json.Unmarshal(body, &gresp) // tolerate parse miss; handled below
	if gresp.Error != nil {
		oa, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": gresp.Error.Message,
				"type":    "upstream_error",
				"code":    gresp.Error.Status,
			},
		})
		resp.Body = io.NopCloser(bytes.NewReader(oa))
		resp.ContentLength = int64(len(oa))
		resp.StatusCode = http.StatusBadGateway
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Length", strconv.Itoa(len(oa)))
		return errUpstreamRetryable
	}
	...
	out, _ := json.Marshal(map[string]string{"text": sb.String()})
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return nil
}
```
Diferença obrigatória: chat pode ser streaming → o tradutor JSON só roda quando `Content-Type` é `application/json`; SSE vai para `anthropic_translate_sse.go`. 400 de validação NÃO devolve `errUpstreamRetryable` (RESEARCH tabela de erros).

**Composição com usage** — copiar o closure "traduz primeiro, depois usage" de `main.go` l.1997-2002:
```go
ModifyResponse: func(resp *http.Response) error {
	if err := modifyResponse(resp); err != nil {
		return err
	}
	return usageInterceptor.Intercept(resp)
},
```
Para o ① a ordem fica: `chatRetryableStatus` → tradutor (JSON|SSE) → `auditInterceptor` → `toolCallInterceptor` → `usageInterceptor` (o toolcall tee procura `"tool_calls"` — só funciona sobre o body JÁ traduzido).

---

### `gateway/internal/proxy/anthropic_translate_sse.go` (io.ReadCloser, streaming)

**Analog:** `interceptor_usage.go` `usageTeeReader` (l.590-690)

**Estrutura de wrapper + Close delegando** (l.605-623):
```go
func (r *usageTeeReader) Read(p []byte) (int, error) {
	n, err := r.upstream.Read(p)
	if n > 0 {
		_, _ = r.buf.Write(p[:n])
		r.scanFrames()
	}
	return n, err
}
func (r *usageTeeReader) Close() error {
	r.scanFrames()
	closeErr := r.upstream.Close()
	...
```
**Framing SSE (`\n\n`, prefixo `data: `)** (l.651-668):
```go
for {
	data := r.buf.Bytes()
	i := bytes.Index(data, []byte("\n\n"))
	if i < 0 {
		return
	}
	frame := make([]byte, i)
	copy(frame, data[:i])
	r.buf.Next(i + 2)
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimPrefix(line, []byte("data: "))
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
```
Diferença: o tradutor NÃO repassa bytes crus — acumula frames Anthropic (`event:` + `data:`) e emite chunks OpenAI num buffer de saída que o `Read(p)` drena. Contrato do interceptor (`interceptor.go` l.14-16): "Wrapping resp.Body with a custom io.ReadCloser is fine; the wrapper's Close MUST delegate to the original." Ajustar `Content-Type: text/event-stream` e remover `Content-Length`.

---

### `gateway/internal/proxy/anthropic_keypool.go` (RoundTripper + estado Redis)

**Analog 1 (transporte):** `transport.go` `fallthroughRoundTripper` (l.34-74)
```go
type fallthroughRoundTripper struct {
	base http.RoundTripper
	preByteTimeout bool
}
func (f fallthroughRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f.base.RoundTrip(r)
	if err != nil && (isConnectionClass(err) || (f.preByteTimeout && isPreByteTimeout(r.Context(), err))) {
		return nil, errDialFailedFallthrough
	}
	return resp, err
}
```
Para o pool: bufferizar o body (`r.GetBody` ou bytes lidos uma vez), `for _, k := range pool.Ordered()` → clonar request com `x-api-key`, chamar `base.RoundTrip`, se `classifyAnthropicKeyError` ≠ `notKeyProblem` → `io.Copy(io.Discard, resp.Body)`, marcar, próxima. Esgotou → `return nil, fmt.Errorf("...: %w", errUpstreamRetryable)` (o `ErrorHandler` em `errors.go` já suprime a escrita para `errors.Is(err, errUpstreamRetryable)`). Envolver também com `fallthroughRoundTripper` para falhas de dial.

**Analog 2 (estado no Redis):** `internal/breaker/force_override.go`
- Prefixo de chave como constante exportada (l.71-78):
```go
const ForceOverrideKeyPrefix = "gw:breaker:force:"
func ForceOverrideKey(upstreamName string) string {
	return ForceOverrideKeyPrefix + upstreamName
}
```
→ `const AnthropicKeyStatePrefix = "gw:anthropic:key:"` + fingerprint (sha256[:8]).
- Valor JSON com struct (l.94-99) `{State, TTLSec, SetBy, SetAt}` → `{status, until, reason}`.
- Leitura com `redis.Nil` = "sem marcação" (l.115-124) e escrita com TTL (l.185-200):
```go
raw, gerr := rdb.Get(ctx, key).Result()
if gerr != nil {
	if errors.Is(gerr, redis.Nil) {
		return "", 0, false, nil
	}
	return "", 0, false, fmt.Errorf("breaker force-override GET: %w", gerr)
}
...
if err := rdb.Set(ctx, ForceOverrideKey(upstreamName), string(buf), ttl).Err(); err != nil {
```
Usar `redis.Cmdable` como parâmetro (testável com miniredis, mesmo padrão de `breaker_test.go` l.224).

**Classificador:** usar o esboço do RESEARCH §Code Examples (l.399-441). Peek limitado do body de erro — copiar o limite de `audio.go` (`sttErrorPeekMax = 2 << 10`) / `overcontext.go` (`overContextPeekBytes = 8 << 10`).

**Env das chaves:** `UPSTREAM_LLM_ANTHROPIC_API_KEYS="rotulo=<chave>@<dia>,..."` — parse no estilo `csvOr` de `config.go` l.423. Nunca logar a chave (só rótulo + fingerprint).

---

### `gateway/internal/proxy/chat_retryable_status.go` (interceptor de cascata por status)

**Analog principal:** `audio.go` l.49-71
```go
func STTRetryableStatusInterceptor() ProxyResponseInterceptor {
	return sttRetryableStatusInterceptor{}
}
func (sttRetryableStatusInterceptor) Intercept(resp *http.Response) error {
	if resp == nil || !isRetryableSTTStatus(resp.StatusCode) {
		return nil
	}
	head := peekSTTErrorBody(resp)
	return &sttUpstreamStatusError{
		status:  resp.StatusCode,
		excerpt: sttErrorExcerpt(head),
		oom: strings.Contains(strings.ToLower(string(head)), "out of memory"),
	}
}
```
O tipo de erro deve satisfazer `errors.Is(err, errUpstreamRetryable)` (mesmo mecanismo de `sttUpstreamStatusError`, comentário l.84-94) e carregar `status` + `excerpt` para o "último erro real" no esgotamento.

**Gates de segurança (copiar de `overcontext.go` l.87-133):**
```go
if resp == nil || resp.Request == nil {
	return nil
}
...
// (c) Nobody would re-route: ... the ErrorHandler would turn our sentinel into a 502
if dispatchResultFrom(ctx) == nil {
	return nil
}
...
peek, readErr := io.ReadAll(io.LimitReader(resp.Body, overContextPeekBytes))
orig := resp.Body
resp.Body = restoredBody{Reader: io.MultiReader(bytes.NewReader(peek), orig), orig: orig}
```
Diferença intencional: aqui o gate de streaming (`streamingFlagFrom`) NÃO bloqueia — status ≠ 2xx chega antes de qualquer byte (RESEARCH Padrão 3). Status alvo: {402, 429, 500, 502, 503, 504, 524, 529}; nunca 400 genérico.

**Regra do último salto (FATO, `main.go` l.2054-2057 comentário em `buildGroqWhisperProxy`/`STTRetryableStatusInterceptor` godoc):** "The FINAL candidate (openai-whisper) deliberately does NOT compose it" → `openrouter-deepseek` (③) NÃO compõe o interceptor, OU o dispatcher passa a escrever o último erro guardado (decisão do planner).

---

### `gateway/internal/proxy/dispatcher.go` (mod)

**Pontos de extensão (FATO):**
- `dispatchResult` l.87-91: `{fallthrough_ bool; wrote bool; err error}` → adicionar campos do último erro (status/body) se o planner escolher repassar o erro real.
- Loop `dispatchPinned` l.560-602: `res := cfg.dispatchTo(...); if res.fallthrough_ && !res.wrote { cfg.recordFallthroughFailure(...); continue }`. Exceção de breaker já existe para over-context (l.595-597) — usar o mesmo `if !errors.Is(res.err, X)` para não abrir o breaker do ① por esgotamento de chave.
- Esgotamento l.610-614 (`pinned_upstreams_unavailable` 503) é onde entra a escrita do último erro real.
- RES-08: l.562-566 + l.604-607 (`writeSensitiveBlock`) — sem código novo; os dois upstreams novos são tier 1.

---

### `gateway/cmd/gateway/main.go` (wiring)

**Analog:** `buildOpenRouterChatProxy` (l.1901-1932) — parametrizar o nome (hoje literal `"openrouter-chat"` em 2 lugares):
```go
rp := &httputil.ReverseProxy{
	Director: proxy.BuildOpenRouterDirector(parsed, u.AuthBearer,
		cfg.UpstreamOpenRouterProviderOrder, cfg.UpstreamOpenRouterAllowFallbacks,
		resolver, "openrouter-chat", log, tenantPrefs),
	FlushInterval: -1, // SSE streaming
	Transport: &http.Transport{
		MaxIdleConns:          50,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
	ErrorHandler:   proxy.ErrorHandler("openrouter-chat", log),
	ModifyResponse: proxy.ComposeInterceptors(auditInterceptor, toolCallInterceptor, usageInterceptor),
}
```
(Pitfall 4: `ResponseHeaderTimeout` ≥ 120 s para os upstreams Haiku — vir de env, padrão `TTSResponseHeaderTimeout` em `tts.go` l.40-43 + `config.go` l.471.)

**Registro condicional** (l.770-784):
```go
if u, ok := loader.Get("openrouter-chat"); ok && u.URL != "" {
	orChatProxy, perr := buildOpenRouterChatProxy(u, cfg, log,
		auditInterceptor, toolCallInterceptor, usageInterceptor, resolver, tenantsLoader)
	if perr != nil {
		log.Warn("build openrouter-chat proxy", "err", perr)
	} else {
		llmRoleProxies["openrouter-chat"] = proxy.ToolCallTerminalGuard(
			orChatProxy, toolCallInterceptor, "openrouter-chat", "/v1/chat/completions",
		)
	}
}
```
Replicar para `anthropic-haiku` (só registra se houver chaves → ① dormente) e `openrouter-deepseek`.

**Proxy com Director+ModifyResponse em par + transporte embrulhado** — `buildGeminiSTTProxy` l.1976-2026 é o molde para `buildAnthropicProxy` (Director e tradutor vêm de um único `Build...` que devolve o par; `Transport: proxy.NewSTTFallthroughTransport(&http.Transport{...})` → trocar por `proxy.NewAnthropicKeyPoolTransport(...)`).

**TTS** (l.868-904): `ttsRoleProxies["kokoro-tts"] = proxy.NewDynamicTTSTargetProxy(...)` — registrar `ttsRoleProxies["openrouter-tts"]` com Director próprio + `usageInterceptor`.

---

### `gateway/internal/proxy/openrouter_tts_director.go` (Director TTS)

**Analog:** `openai_embed_director.go` (127 linhas)

**Director com bearer + reescrita** (corpo do builder):
```go
base := BuildDirector(upstream)
return func(r *http.Request) {
	base(r)
	if authBearer != "" {
		r.Header.Set("Authorization", "Bearer "+authBearer)
	}
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(r.Body)
	...
	patched, perr := openaiEmbedRewrite(body, resolver, upstreamName)
	...
	r.Body = io.NopCloser(bytes.NewReader(patched))
	r.ContentLength = int64(len(patched))
	r.Header.Set("Content-Length", strconv.Itoa(len(patched)))
}
```
**Rewrite que força campo** (`openaiEmbedRewrite`):
```go
var m map[string]json.RawMessage
if err := json.Unmarshal(body, &m); err != nil {
	return nil, err
}
var alias string
if rawAlias, ok := m["model"]; ok {
	_ = json.Unmarshal(rawAlias, &alias)
}
target := resolver.Resolve(alias, upstreamName)
modelBytes, err := json.Marshal(target)
...
m["model"] = modelBytes
m["dimensions"] = dimBytes
return json.Marshal(m)
```
Trocar `dimensions` por: `response_format` = `"mp3"` **só se ausente** (Pitfall 13) + mapa de `voice`. Struct de leitura já existe: `ttsSpeechRequest` em `tts.go` (Input/Voice/ResponseFormat). Timeout: `TTSResponseHeaderTimeout` (`tts.go` l.40-43).

---

### `gateway/internal/upstreams/probe.go` (mod — Pitfall 10)

**Analog:** caso especial por nome, mesma função `dispatch` (l.290-305):
```go
if u.Name == "voice-api-piper" {
	base := strings.TrimRight(u.URL, "/")
	body := []byte(`{"text":"ping"}`)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/tts", bytes.NewReader(body))
```
Adicionar no `case "llm"` (l.271-277) um `if u.Name == "anthropic-haiku"` → `GET <url>/v1/models` com `x-api-key` da 1ª chave válida, e PULAR o `req.Header.Set("Authorization", "Bearer "+u.AuthBearer)` de l.350-352 (AuthBearer aqui é a lista inteira de chaves). TTS `openrouter-tts`: probe genérico de l.~305 (`POST /v1/audio/speech`) já serve se a URL incluir `/api`.

---

### `gateway/internal/upstreams/loader.go` (mod — Padrão 6, risco latente)

**FATO (l.42-47):** `s.byRoleTier[RoleTier{Role: u.Role, Tier: u.Tier}] = u` — último escrito vence. Correção sugerida: só sobrescrever se `u.TierPriority` for menor que o já presente (preservar o 1º por prioridade). Testar no padrão de `loader_test.go`.

---

### `gateway/db/migrations/0041_haiku_chain.sql` (migration)

**Analog 1 — re-tier de upstreams com ordem segura para `UNIQUE(role,tier,tier_priority)`:** `0036_embed_gpu_tier0.sql`
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;
UPDATE ai_gateway.upstreams SET tier = 1, tier_priority = 10
    WHERE name = 'local-embed' AND tier = 0;

INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env) VALUES
    ('embed-gpu', 'embed', 0, 0, 'UPSTREAM_EMBED_GPU_URL', NULL)
ON CONFLICT (name) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;
DELETE FROM ai_gateway.upstreams WHERE name = 'embed-gpu';
UPDATE ai_gateway.upstreams SET tier = 0, tier_priority = 0
    WHERE name = 'local-embed' AND tier = 1;
-- +goose StatementEnd
```
(UPDATE com WHERE no estado pré-migration = idempotente; Down em ordem reversa.)

**Analog 2 — coluna nova em `tenants` (`llm_effort`) com trigger de hot-reload:** `0037_model_aliases_provider_prefs.sql` — `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` + `COMMENT ON COLUMN` + **DROP/CREATE do trigger `tenants_update_notify` com a coluna nova no WHEN** (o WHEN é filtrado por coluna; sem isso o loader não recarrega). Down restaura o trigger no formato 0037 ANTES do DROP COLUMN.

**Analog 3 — CHECK de `prices.unit`:** `0012_create_prices_and_fx.sql` l.10:
```sql
unit TEXT NOT NULL CHECK (unit IN ('input_token', 'output_token', 'audio_second', 'embed_request')),
```
→ DROP CONSTRAINT + ADD com `cache_read_token`, `cache_write_token`, `cache_write_1h_token` (consultar nome real da constraint em `pg_constraint` antes).

**`billing_events`:** `ALTER TABLE ai_gateway.billing_events ADD COLUMN ... DEFAULT 0` no pai particionado (propaga). Memória `billing-events-partition-gap-aug2026`: conferir `internal/db/partitions.go`.

**Bump de testes:** `integration_test/migration_0026_test.go` l.108-118 (`Down(13)` → `Down(14)` + comentário "quick/phase 23 HEAD bump: Down(13)→Down(14) when 0041 landed") e `migration_0029_test.go` l.183-192 (mesmo padrão). Se houver 0042, +1 em cada.

---

### `gateway/db/migrations/0042_aux_pod_lifecycles.sql` (se separado)

**Analog:** `0023_primary_lifecycles.sql` — `CREATE TABLE IF NOT EXISTS` com `BIGSERIAL`, `started_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`, `ended_at`, `vast_instance_id BIGINT`, `accepted_dph NUMERIC(6,4)`, `total_cost_brl NUMERIC(10,4)` + índices `idx_*_started_at DESC` e parcial `WHERE ended_at IS NULL` + `COMMENT ON TABLE`. Adicionar coluna `pod TEXT NOT NULL` (ex.: `vast-3060`).

---

### `gateway/internal/proxy/interceptor_usage.go` (mod — custo real + cache)

**Frame de usage a estender** (l.576-583):
```go
type sseUsageFrame struct {
	Model string `json:"model,omitempty"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}
```
→ adicionar `Cost *float64 json:"cost"` e `PromptTokensDetails *struct{ CachedTokens int "cached_tokens"; CacheWriteTokens int "cache_write_tokens" }`. Gravar nos dois consumidores: `ExtractFromBody` (l.189-207) e `scanFrames` (l.670-685), e no `usageJSONBuffer.Close` (l.~749).

**Gate de custo por upstream** (l.297-304):
```go
if isSelfHostedUpstream(upstream) {
	costPhantom = priceTokens(u, model, "openrouter-fireworks", tokensIn, tokensOut, audioSeconds, embedsCount)
} else {
	costExternal = priceTokens(u, model, providerForUpstream(upstream), tokensIn, tokensOut, audioSeconds, embedsCount)
}
```
→ ramo novo: se `usage.cost` reportado presente → `cost_external_brl = cost × fx` e `cost_source='provider_reported'`; senão `priceTokens` com unidades de cache + linha `:long` se `in+cache_read+cache_write > 100000`.

**`providerForUpstream`** (l.379-390) — adicionar `case "openrouter-deepseek": return "openrouter-fireworks"`, `case "anthropic-haiku": return "anthropic"`, `case "openrouter-tts": return "openrouter"`.

**`priceTokens`** (l.339-365) — padrão de dimensão opcional via `hasPrice` (l.360-362) para as unidades de cache.

**Painel "coberto por créditos" (LOCKED):** ① grava `cost_external_brl` a preço de lista com `upstream=anthropic-haiku`; separação é feita na query do economy.

---

### `gateway/internal/billing/flusher.go` + `db/queries/billing.sql`

**Event** (flusher.go l.28-44) — adicionar `TokensCacheRead int`, `TokensCacheWrite int`, `CostSource string`.
**InsertBillingEvent** (billing.sql l.11-21) — acrescentar colunas no INSERT e nos `$n`; regenerar sqlc (`internal/db/gen/billing.sql.go`).
**Economy** (billing.sql l.95-107) — novas queries no mesmo estilo gateway-wide (sem `tenant_id`):
```sql
SELECT
    COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS phantom_brl,
    COALESCE(SUM(cost_external_brl), 0)::numeric(20,6)      AS external_brl,
    COUNT(*) FILTER (WHERE cost_local_phantom_brl > 0)::bigint AS local_requests,
    COUNT(*)::bigint                                        AS total_requests
FROM ai_gateway.billing_events
WHERE ts >= $1
  AND ts <  $2;
```
→ acrescentar `SUM(tokens_in+tokens_out) FILTER (WHERE cost_local_phantom_brl > 0)`, total de tokens, e `SUM(cost_external_brl) FILTER (WHERE upstream='anthropic-haiku')` (coberto por créditos).

---

### `gateway/internal/admin/economy.go` (mod)

**Padrão:** interface `economyQueries` (l.75-79) para injeção de fake em teste + `newEconomyHandlerWithQueries` (l.99-104). Cada query nova entra na interface; erro → `httpx.WriteOpenAIError(... "api_error", "billing_query_failed", "")` + `obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx").Inc()` (l.168-174). Campos novos em `EconomySummary` (l.54-62) — ponteiro `*float64` quando denominador pode ser zero (padrão de `PctServidoLocal`).

---

### `gateway/internal/admin/anthropic_keys_http.go` (novo, GET read-only)

**Analog:** `upstreams_admin_http.go` `List` (l.111-127):
```go
func (h *UpstreamAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListAllUpstreams(r.Context())
	if err != nil {
		h.log.Error("ListAllUpstreams failed", "err", err)
		httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "upstreams_list_failed", "")
		obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "5xx").Inc()
		return
	}
	...
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
	obs.GatewayAdminRequests.WithLabelValues(upstreamsAdminRoute, "2xx").Inc()
}
```
Registro: `adminRouter.Method(http.MethodGet, "/economy", px.adminEconomyHandler)` (`main.go` l.1802) → `"/anthropic/keys"`. Saída: rótulo + fingerprint + estado + until (NUNCA a chave).

---

### `gateway/internal/tenants/loader.go` + `db/queries/tenants_admin.sql` (effort por tenant)

**Analog:** `ProviderPrefsByTenantID` (loader.go l.209-228):
```go
func (l *Loader) ProviderPrefsByTenantID(tenantID string) []byte {
	if l == nil || tenantID == "" {
		return nil
	}
	id, err := uuid.Parse(tenantID)
	if err != nil {
		return nil
	}
	cfg, err := l.Get(id)
	if err != nil || len(cfg.ProviderPrefs) == 0 {
		return nil
	}
	...
}
```
→ `LLMEffortByTenantID(tenantID string) string`. SQL: adicionar `llm_effort` em `GetTenantConfig` e `ListTenantsForLoader` (tenants_admin.sql l.1-27) + `UpdateTenantLLMEffort :execrows` copiando `UpdateTenantProviderPrefs` (l.29-36).

---

### `gateway/cmd/gatewayctl/prices.go` (mod)

l.59 e l.71-73: ampliar a lista aceita em `--unit` com `cache_read_token|cache_write_token|cache_write_1h_token`; espelhar o comentário de `internal/billing/prices.go` l.13.

---

### `scripts/price-sync/gateway-price-sync.sh` (mod)

l.239-242 — padrão de gravação:
```bash
gatewayctl prices set -model "$gw_key" -provider "$PHANTOM_PROVIDER" \
  -unit input_token -usd "$prompt" -notes "phantom=$or_slug, $NOTES_TAG"
```
Adicionar linhas `-provider anthropic` para `claude-haiku-5-5` e `claude-haiku-5-5:long` + unidades de cache. Respeitar `DRY_RUN=1` (l.47, l.97-104).

---

### `ops/vast-3060/unified3060.py` (mod — custo da 3060)

FATO: `real_cost()` (l.284-295) devolve `{hourly, storage_h, total, ...}`; estado só em `STATE_PATH = "/var/lib/vast-3060/state.json"` (l.58, `save_state` l.120-124); entradas `cmd_start` (l.791) e `cmd_stop` (l.1062). Gravar INSERT/UPDATE em `aux_pod_lifecycles` nesses dois pontos. **NÃO SEI** como o script acessa o Postgres hoje (não lido) — o planner deve verificar se há credencial de DB no host ops-claude ou se a gravação passa por endpoint admin do gateway.

---

### `dashboard/src/lib/gateway.ts` + `economy-panel.tsx`

- Tipos espelho campo-a-campo (gateway.ts l.285-330, comentário "mirror the Go handler … FIELD-FOR-FIELD"); `number | null` para `*float64`.
- Fetch: `fetchEconomy` → `proxyGet<EconomyResponse>("economy", { from, to })` (l.550-551). Gotcha RSC (memória `dashboard-rsc-proxy-fetch-gotcha`).
- Cards: `KpiCard caption/value/hint` (economy-panel.tsx l.66-95) — card "Custo OpenRouter" (hint "Fallback (pod down)") precisa reescrita de texto; adicionar card "Haiku coberto por créditos".
- Teste: `dashboard/src/lib/gateway.test.ts` (vitest).

---

## Shared Patterns

### Sentinel de cascata pré-byte
**Fonte:** `gateway/internal/proxy/errors.go` (`errUpstreamRetryable`, `ErrorHandler`)
**Aplicar a:** keypool, chat_retryable_status, tradutor JSON (erro de provedor)
```go
if errors.Is(err, errDialFailedFallthrough) || errors.Is(err, errUpstreamRetryable) {
	if res := dispatchResultFrom(r.Context()); res != nil {
		res.fallthrough_ = true
		res.wrote = false
		res.err = err
```
Erros novos devem ENVOLVER (`%w`) `errUpstreamRetryable` — nunca criar sentinel paralelo (ex.: `errOverContextFallthrough` em `overcontext.go` l.40).

### Composição de interceptors
**Fonte:** `interceptor.go` l.27-39 (`ComposeInterceptors`, ordem preservada, para no 1º erro). Interceptors não fecham o body; wrappers delegam `Close`.

### Body tolerante a campos desconhecidos
**Fonte:** `openrouter_director.go` l.184-211 — sempre `map[string]json.RawMessage`; em erro de parse, devolver o body original (best-effort).

### Logging sem body/segredo (R12)
**Fonte:** `openrouter_director.go` l.117-126 — `log.Debug(..., "upstream", name, "error_class", fmt.Sprintf("%T", err))`.

### Lookup por tenant via auth context
**Fonte:** `auth.FromContext(ctx)` → `ac.TenantID` (`openrouter_director.go` l.231). RES-08 vem de `api_keys.data_class` (FATO F10 do research).

### Config por env
**Fonte:** `internal/config/config.go` l.423 (`csvOr`) e l.471 (`atoiOr`); comentário de struct com nome do env + default.

### Teste unitário de transform
**Fonte:** `internal/proxy/openrouter_cache_control_test.go` — entrada JSON literal, helper `t.Helper()` que faz unmarshal, asserts por campo, caso "não-alvo não é tocado" comparando string do body.

### Teste de integração da cadeia
**Fonte:** `integration_test/fallback_routing_test.go` l.30-100 — `//go:build integration`, `freshSchema(t, ctx)` → rdb, `resilienceLoader("llm", nome, url, ...)`, `breaker.NewSet(rdb, ..., breaker.Options{ConsecutiveFailures: 2,...}, loader.Names())`, `proxy.NewDispatcher(proxy.DispatcherConfig{Role, Loader, Breaker, Proxies, Log})`, mocks `httptest`. RES-08: `sensitive_block_test.go` l.38 (`makeSensitiveAuthedRequest`, `newPanicProxy` para provar que externo nunca é tocado).

## No Analog Found

| Arquivo | Papel | Fluxo | Motivo |
|---|---|---|---|
| Conversão de eventos SSE Messages → chunks OpenAI (lógica interna de `anthropic_translate_sse.go`) | utility | streaming | Nenhum tradutor SSE→SSE existe; o framing é copiável (`usageTeeReader`), a máquina de estados (índice de tool_use, `input_json_delta`, `message_delta`) segue RESEARCH Padrão 1 §Streaming |
| Gravação de lifecycle da 3060 a partir de `unified3060.py` | ops | batch | Script Python sem acesso a DB verificado nesta sessão (NÃO SEI o caminho de escrita) |

## Metadata

**Escopo de busca:** `gateway/internal/{proxy,upstreams,admin,breaker,tenants,billing,config,integration_test}`, `gateway/cmd/{gateway,gatewayctl}`, `gateway/db/{migrations,queries}`, `dashboard/src/{lib,components}`, `ops/vast-3060`, `scripts/price-sync`
**Arquivos lidos/inspecionados:** ~30
**Data:** 2026-10-14
