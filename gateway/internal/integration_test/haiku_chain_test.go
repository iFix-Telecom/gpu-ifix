//go:build integration

// Phase 23-14 — cadeia ① anthropic-haiku → ② openrouter-chat → ③
// openrouter-deepseek (HAIKU-03) e cascata do openrouter-tts (HAIKU-08) com
// os construtores REAIS.
//
// O que é de produção aqui: proxy.BuildAnthropicProxy (①), BuildOpenRouterDirector
// (② e ③), BuildOpenRouterTTSDirector, o helper de composição
// proxy.ExternalChatInterceptors (o MESMO que o main usa), o transporte
// proxy.NewChatFallthroughTransport, proxy.NewDispatcher com Pins = resolver
// e o loader lendo a tabela upstreams do Postgres (linhas da 0041 habilitadas
// via SetUpstreamEnabled, como o 23-16 faz em prod com `gatewayctl upstreams
// enable`). Só os provedores são fakes (httptest).
package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// ---------------------------------------------------------------------------
// Respostas dos fakes
// ---------------------------------------------------------------------------

const (
	anthMsgOK      = `{"id":"msg_01","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"resposta-haiku"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":2}}`
	anthOverloaded = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	anthRateLimit  = `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`
	anthCreditLow  = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`

	oaiOKDois  = `{"id":"chatcmpl-2","object":"chat.completion","created":1,"model":"m2","choices":[{"index":0,"message":{"role":"assistant","content":"resposta-dois"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	oaiOKTres  = `{"id":"chatcmpl-3","object":"chat.completion","created":1,"model":"m3","choices":[{"index":0,"message":{"role":"assistant","content":"resposta-tres"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	oaiSSEDois = "data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"stream-dois\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	chainBody = `{"model":"deepseek-flash","messages":[{"role":"system","content":"Você é útil."},{"role":"user","content":"oi"}],"stream":false}`

	chainTestKeys      = "a=sk-ant-int-AAAA@5,b=sk-ant-int-BBBB"
	chainHeaderTimeout = 200 * time.Millisecond
)

// ---------------------------------------------------------------------------
// Fake de provedor
// ---------------------------------------------------------------------------

type replyFn func(w http.ResponseWriter, r *http.Request)

type chainFake struct {
	srv    *httptest.Server
	hits   atomic.Int32
	mu     sync.Mutex
	bodies [][]byte
	keys   []string
}

func newChainFake(t *testing.T, reply replyFn) *chainFake {
	t.Helper()
	f := &chainFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, b)
		f.keys = append(f.keys, r.Header.Get("x-api-key"))
		f.mu.Unlock()
		f.hits.Add(1)
		reply(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *chainFake) lastBody(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatalf("fake não recebeu request")
	}
	var m map[string]any
	if err := json.Unmarshal(f.bodies[len(f.bodies)-1], &m); err != nil {
		t.Fatalf("body do fake não é JSON: %v (%s)", err, f.bodies[len(f.bodies)-1])
	}
	return m
}

func jsonReply(status int, body string) replyFn {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func jsonReplyH(status int, body string, h map[string]string) replyFn {
	return func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range h {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func sseReply(stream string) replyFn {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, stream)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}
}

// holdHeaders segura os headers além do timeout do teste; libera cedo se o
// cliente (o gateway) desistir.
func holdHeaders(next replyFn) replyFn {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Banco: linhas da 0041 habilitadas + aliases pinados (estado restaurado)
// ---------------------------------------------------------------------------

type savedAlias struct {
	alias, upstream, upstreamName, target string
	prefs                                 []byte
}

// pinAliases insere as 3 linhas (①②③) para cada alias e restaura o estado
// anterior no Cleanup (o Postgres é compartilhado pelo pacote).
func pinAliases(t *testing.T, ctx context.Context, pool *pgxpool.Pool, aliases ...string) {
	t.Helper()
	var saved []savedAlias
	rows, err := pool.Query(ctx, `SELECT alias, upstream, upstream_name, target, provider_prefs
		FROM ai_gateway.model_aliases WHERE alias = ANY($1)`, aliases)
	if err != nil {
		t.Fatalf("snapshot model_aliases: %v", err)
	}
	for rows.Next() {
		var s savedAlias
		if err := rows.Scan(&s.alias, &s.upstream, &s.upstreamName, &s.target, &s.prefs); err != nil {
			rows.Close()
			t.Fatalf("scan model_aliases: %v", err)
		}
		saved = append(saved, s)
	}
	rows.Close()
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := pool.Exec(cctx, `DELETE FROM ai_gateway.model_aliases WHERE alias = ANY($1)`, aliases); err != nil {
			t.Errorf("cleanup aliases: %v", err)
		}
		for _, s := range saved {
			if _, err := pool.Exec(cctx, `INSERT INTO ai_gateway.model_aliases (alias, upstream, upstream_name, target, provider_prefs)
				VALUES ($1,$2,$3,$4,$5)`, s.alias, s.upstream, s.upstreamName, s.target, s.prefs); err != nil {
				t.Errorf("restore alias %s/%s: %v", s.alias, s.upstreamName, err)
			}
		}
	})
	if _, err := pool.Exec(ctx, `DELETE FROM ai_gateway.model_aliases WHERE alias = ANY($1)`, aliases); err != nil {
		t.Fatalf("limpar aliases: %v", err)
	}
	for _, a := range aliases {
		for _, row := range [][2]string{
			{"anthropic-haiku", "claude-haiku-5-5"},
			{"openrouter-chat", "anthropic/claude-haiku-5.5"},
			{"openrouter-deepseek", "deepseek/deepseek-v4-flash"},
		} {
			if _, err := pool.Exec(ctx, `INSERT INTO ai_gateway.model_aliases (alias, upstream, upstream_name, target)
				VALUES ($1,'llm',$2,$3)`, a, row[0], row[1]); err != nil {
				t.Fatalf("insert alias %s/%s: %v", a, row[0], err)
			}
		}
	}
}

// setUpstreamEnabled muda o flag via a MESMA query do gatewayctl/admin e
// restaura o valor original no Cleanup.
func setUpstreamEnabled(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, enabled bool) {
	t.Helper()
	var orig bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM ai_gateway.upstreams WHERE name=$1`, name).Scan(&orig); err != nil {
		t.Fatalf("upstream %s ausente: %v", name, err)
	}
	q := gen.New(pool)
	if err := q.SetUpstreamEnabled(ctx, gen.SetUpstreamEnabledParams{Name: name, Enabled: enabled}); err != nil {
		t.Fatalf("SetUpstreamEnabled(%s,%v): %v", name, enabled, err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := q.SetUpstreamEnabled(cctx, gen.SetUpstreamEnabledParams{Name: name, Enabled: orig}); err != nil {
			t.Errorf("restore enabled %s: %v", name, err)
		}
	})
}

// haikuChainDB prepara o banco como em prod pós-23-16: ① e ③ habilitados,
// envs de URL presentes (o loader pula linha sem url_env) e aliases pinados.
func haikuChainDB(t *testing.T, ctx context.Context) (*pgxpool.Pool, *models.Resolver) {
	t.Helper()
	pool, _ := freshSchema(t, ctx)
	// As URLs do loader só decidem se a linha entra no roster; os proxies do
	// teste apontam para os fakes (no main, ① e ③ também vêm do config).
	t.Setenv("UPSTREAM_LLM_ANTHROPIC_URL", "http://127.0.0.1:1")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "http://127.0.0.1:1")
	setUpstreamEnabled(t, ctx, pool, "anthropic-haiku", true)
	setUpstreamEnabled(t, ctx, pool, "openrouter-chat", true)
	setUpstreamEnabled(t, ctx, pool, "openrouter-deepseek", true)
	pinAliases(t, ctx, pool, "deepseek-flash", "anthropic/claude-haiku-5.5")
	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver refresh: %v", err)
	}
	return pool, resolver
}

// ---------------------------------------------------------------------------
// Montagem da cadeia (espelho do main.go; composição via helper de produção)
// ---------------------------------------------------------------------------

type chainSpec struct {
	anth, or2, or3 replyFn // nil → upstream sem proxy registrado
	anthKeys       string  // "" → chainTestKeys
}

type chainEnv struct {
	disp           http.Handler
	bs             *breaker.Set
	anth, or2, or3 *chainFake
}

func chainLogger() *slog.Logger { return slog.New(slog.NewTextHandler(discardWriter{}, nil)) }

// newExternalChatProxy espelha buildOpenRouterChatProxy do main: mesmo
// director, mesmo helper de interceptors e o mesmo transporte por `final`.
func newExternalChatProxy(t *testing.T, rawURL, name string, final bool, resolver *models.Resolver, tc *proxy.ToolCallInterceptor) http.Handler {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url %s: %v", rawURL, err)
	}
	log := chainLogger()
	var tr http.RoundTripper = &http.Transport{ResponseHeaderTimeout: chainHeaderTimeout}
	if !final {
		tr = proxy.NewChatFallthroughTransport(tr)
	}
	rp := &httputil.ReverseProxy{
		Director:       proxy.BuildOpenRouterDirector(u, "sk-or-int", []string{"novita"}, false, resolver, name, log, nil),
		FlushInterval:  -1,
		Transport:      tr,
		ErrorHandler:   proxy.ErrorHandler(name, log),
		ModifyResponse: proxy.ExternalChatInterceptors(final, tc).Intercept,
	}
	return proxy.ToolCallTerminalGuard(rp, tc, name, "/v1/chat/completions")
}

func buildChain(t *testing.T, ctx context.Context, pool *pgxpool.Pool, resolver *models.Resolver, spec chainSpec) *chainEnv {
	t.Helper()
	log := chainLogger()
	loader, err := upstreams.NewLoader(ctx, pool, log)
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	env := &chainEnv{}
	// Redis isolado por subteste: breaker (force-override) e estado do pool de
	// chaves não vazam entre casos.
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	env.bs = breaker.NewSet(rdb, log, breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	tc := proxy.NewToolCallInterceptor()
	proxies := map[string]http.Handler{}

	if spec.anth != nil {
		env.anth = newChainFake(t, spec.anth)
		raw := spec.anthKeys
		if raw == "" {
			raw = chainTestKeys
		}
		keys, err := proxy.ParseAnthropicKeys(raw)
		if err != nil || len(keys) == 0 {
			t.Fatalf("ParseAnthropicKeys: %v (n=%d)", err, len(keys))
		}
		kp := proxy.NewKeyPool(keys, rdb, time.Now, time.UTC, log)
		au, _ := url.Parse(env.anth.srv.URL)
		rp, err := proxy.BuildAnthropicProxy(proxy.AnthropicProxyConfig{
			Upstream:      au,
			Pool:          kp,
			Resolver:      resolver,
			UpstreamName:  "anthropic-haiku",
			HeaderTimeout: chainHeaderTimeout,
			Interceptors:  []proxy.ProxyResponseInterceptor{tc},
			Log:           log,
		})
		if err != nil {
			t.Fatalf("BuildAnthropicProxy: %v", err)
		}
		proxies["anthropic-haiku"] = proxy.ToolCallTerminalGuard(rp, tc, "anthropic-haiku", "/v1/chat/completions")
	}
	if spec.or2 != nil {
		env.or2 = newChainFake(t, spec.or2)
		proxies["openrouter-chat"] = newExternalChatProxy(t, env.or2.srv.URL+"/api", "openrouter-chat", false, resolver, tc)
	}
	if spec.or3 != nil {
		env.or3 = newChainFake(t, spec.or3)
		proxies["openrouter-deepseek"] = newExternalChatProxy(t, env.or3.srv.URL+"/api", "openrouter-deepseek", true, resolver, tc)
	}
	env.disp = proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: env.bs,
		Proxies: proxies,
		Pins:    resolver,
		Log:     log,
	})
	return env
}

func (e *chainEnv) serve(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.disp.ServeHTTP(rec, makeAuthedRequest(body, auth.DataClassNormal))
	return rec
}

func hitsOf(f *chainFake) int32 {
	if f == nil {
		return 0
	}
	return f.hits.Load()
}

func expectServedBy(t *testing.T, rec *httptest.ResponseRecorder, marker string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), marker) {
		t.Fatalf("resposta não veio do passo esperado (%s): %s", marker, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Cadeia ①→②→③
// ---------------------------------------------------------------------------

func TestHaikuChain_A_HaikuServes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(200, anthMsgOK), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
	})
	rec := env.serve(chainBody)
	expectServedBy(t, rec, "resposta-haiku")
	if hitsOf(env.or2) != 0 || hitsOf(env.or3) != 0 {
		t.Fatalf("② ou ③ chamados com ① servindo: or2=%d or3=%d", hitsOf(env.or2), hitsOf(env.or3))
	}
	if !strings.Contains(rec.Body.String(), `"chat.completion"`) {
		t.Fatalf("① não devolveu envelope OpenAI: %s", rec.Body.String())
	}
}

// (b): 529 em ① cascateia para ②.
func TestHaikuChain_B_Overloaded529CascadesTo2(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(529, anthOverloaded), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
	})
	rec := env.serve(chainBody)
	expectServedBy(t, rec, "resposta-dois")
	if hitsOf(env.anth) != 1 {
		t.Errorf("① hits = %d, want 1 (529 não troca chave)", hitsOf(env.anth))
	}
	if hitsOf(env.or3) != 0 {
		t.Errorf("③ chamado com ② servindo")
	}
}

// (i) replay: depois da falha do ①, ② recebe o body OpenAI ORIGINAL do
// cliente, nunca o body Messages traduzido para a Anthropic.
func TestHaikuChain_I_ReplaySendsOriginalOpenAIBodyTo2(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(529, anthOverloaded), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
	})
	expectServedBy(t, env.serve(chainBody), "resposta-dois")
	// O ① recebeu o formato Messages (system top-level) — prova que houve
	// tradução no 1º passo.
	if _, ok := env.anth.lastBody(t)["system"]; !ok {
		t.Fatalf("① não recebeu body Messages (sem system top-level)")
	}
	b := env.or2.lastBody(t)
	msgs, _ := b["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("② sem messages: %v", b)
	}
	if first, _ := msgs[0].(map[string]any); first["role"] != "system" {
		t.Errorf("② messages[0].role = %v, want system (body OpenAI original)", first["role"])
	}
	for _, k := range []string{"output_config", "system", "thinking", "cache_control"} {
		if _, ok := b[k]; ok {
			t.Errorf("② recebeu campo %q do body traduzido para a Anthropic", k)
		}
	}
	if b["model"] != "anthropic/claude-haiku-5.5" {
		t.Errorf("② model = %v, want anthropic/claude-haiku-5.5 (linha do alias)", b["model"])
	}
}

func TestHaikuChain_C_CreditTooLowAllKeysCascadesWithoutBreakerFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(400, anthCreditLow), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
	})
	rec := env.serve(chainBody)
	expectServedBy(t, rec, "resposta-dois")
	if got := hitsOf(env.anth); got != 2 {
		t.Errorf("① hits = %d, want 2 (uma por chave)", got)
	}
	cb, ok := env.bs.Get("anthropic-haiku")
	if !ok || cb == nil {
		t.Fatalf("breaker de anthropic-haiku ausente")
	}
	if c := cb.Counts(); c.TotalFailures != 0 {
		t.Fatalf("breaker de ① contou %d falha(s); créditos esgotados não é falha do upstream", c.TotalFailures)
	}
}

func TestHaikuChain_D_RateLimitThen503ReachesDeepseek(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReplyH(429, anthRateLimit, map[string]string{"retry-after": "30"}),
		or2:  jsonReply(503, `{"error":{"message":"provider down","type":"server_error"}}`),
		or3:  jsonReply(200, oaiOKTres),
	})
	rec := env.serve(chainBody)
	expectServedBy(t, rec, "resposta-tres")
	if hitsOf(env.or2) != 1 {
		t.Errorf("② hits = %d, want 1", hitsOf(env.or2))
	}
	if m := env.or3.lastBody(t)["model"]; m != "deepseek/deepseek-v4-flash" {
		t.Errorf("③ model = %v", m)
	}
}

func TestHaikuChain_E_AllFailReturnsLastBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	const bodyX = `{"error":{"message":"corpo-X do deepseek","type":"server_error","code":"x"}}`
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(529, anthOverloaded),
		or2:  jsonReply(502, `{"error":{"message":"bad gateway dois"}}`),
		or3:  jsonReply(503, bodyX),
	})
	rec := env.serve(chainBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != bodyX {
		t.Fatalf("body = %s, want o corpo X do ③", rec.Body.String())
	}
}

func TestHaikuChain_F_StreamingCascadeNoBytesFromHaiku(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(529, anthOverloaded), or2: sseReply(oaiSSEDois), or3: jsonReply(200, oaiOKTres),
	})
	rec := env.serve(strings.Replace(chainBody, `"stream":false`, `"stream":true`, 1))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "stream-dois") || !strings.Contains(out, "[DONE]") {
		t.Fatalf("SSE de ② ausente: %s", out)
	}
	if strings.Contains(strings.ToLower(out), "overloaded") {
		t.Fatalf("bytes de ① vazaram para o cliente: %s", out)
	}
	if hitsOf(env.anth) != 1 {
		t.Errorf("① hits = %d, want 1", hitsOf(env.anth))
	}
}

func TestHaikuChain_G_NoKeysHaikuDormant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	// ① sem proxy registrado = main sem chaves (linha habilitada no banco).
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
	})
	for i := 0; i < 3; i++ {
		expectServedBy(t, env.serve(chainBody), "resposta-dois")
	}
	if hitsOf(env.or3) != 0 {
		t.Errorf("③ chamado com ② servindo")
	}
}

func TestHaikuChain_G2_DeepseekDisabledReturns2Error(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	setUpstreamEnabled(t, ctx, pool, "openrouter-deepseek", false)
	const bodyY = `{"error":{"message":"corpo-Y do openrouter-chat","type":"server_error"}}`
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(529, anthOverloaded), or2: jsonReply(503, bodyY), or3: jsonReply(200, oaiOKTres),
	})
	rec := env.serve(chainBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != bodyY {
		t.Fatalf("body = %s, want o 503 do ②", rec.Body.String())
	}
	if hitsOf(env.or3) != 0 {
		t.Fatalf("③ desabilitado foi chamado %d vez(es)", hitsOf(env.or3))
	}
}

func TestHaikuChain_H_DirectModelNameAlias(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(529, anthOverloaded), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
	})
	rec := env.serve(strings.Replace(chainBody, `"deepseek-flash"`, `"anthropic/claude-haiku-5.5"`, 1))
	expectServedBy(t, rec, "resposta-dois")
	if hitsOf(env.anth) != 1 {
		t.Errorf("① hits = %d, want 1 (modelo pinado nos 3 passos)", hitsOf(env.anth))
	}
	if m := env.anth.lastBody(t)["model"]; m != "claude-haiku-5-5" {
		t.Errorf("① model = %v, want claude-haiku-5-5", m)
	}
	if m := env.or2.lastBody(t)["model"]; m != "anthropic/claude-haiku-5.5" {
		t.Errorf("② model = %v", m)
	}
}

func TestHaikuChain_J_ReasoningOffSameDecisionBothSteps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	body := strings.Replace(chainBody, `"stream":false`, `"stream":false,"reasoning":{"enabled":false}`, 1)

	t.Run("haiku_serve", func(t *testing.T) {
		env := buildChain(t, ctx, pool, resolver, chainSpec{
			anth: jsonReply(200, anthMsgOK), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
		})
		expectServedBy(t, env.serve(body), "resposta-haiku")
		b := env.anth.lastBody(t)
		th, _ := b["thinking"].(map[string]any)
		if th == nil || th["type"] != "disabled" {
			t.Errorf("① thinking = %v, want disabled (regra de off do 23-08)", b["thinking"])
		}
		if oc, _ := b["output_config"].(map[string]any); oc != nil && oc["effort"] != nil {
			t.Errorf("① output_config.effort = %v, want ausente com reasoning off", oc["effort"])
		}
		if _, ok := b["reasoning"]; ok {
			t.Error("campo reasoning do cliente vazou para a Anthropic")
		}
	})
	t.Run("cascata_para_2", func(t *testing.T) {
		env := buildChain(t, ctx, pool, resolver, chainSpec{
			anth: jsonReply(529, anthOverloaded), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
		})
		expectServedBy(t, env.serve(body), "resposta-dois")
		b := env.or2.lastBody(t)
		rs, _ := b["reasoning"].(map[string]any)
		if rs == nil || rs["enabled"] != false {
			t.Errorf("② reasoning = %v, want {enabled:false}", b["reasoning"])
		}
	})
}

func TestHaikuChain_K_HaikuHeaderTimeoutCascades(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: holdHeaders(jsonReply(200, anthMsgOK)), or2: jsonReply(200, oaiOKDois), or3: jsonReply(200, oaiOKTres),
	})
	start := time.Now()
	rec := env.serve(chainBody)
	expectServedBy(t, rec, "resposta-dois")
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("cascata levou %v; timeout de cabeçalho do teste é %v", el, chainHeaderTimeout)
	}
}

func TestHaikuChain_L_OpenRouterHeaderTimeoutCascadesTo3(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{
		anth: jsonReply(529, anthOverloaded),
		or2:  holdHeaders(jsonReply(200, oaiOKDois)),
		or3:  jsonReply(200, oaiOKTres),
	})
	start := time.Now()
	rec := env.serve(chainBody)
	expectServedBy(t, rec, "resposta-tres")
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("cascata levou %v", el)
	}
}

func TestHaikuChain_M_OnlyHaikuOverloadedGetsOpenAIEnvelope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, resolver := haikuChainDB(t, ctx)
	env := buildChain(t, ctx, pool, resolver, chainSpec{anth: jsonReply(529, anthOverloaded)})
	rec := env.serve(chainBody)
	if rec.Code != 529 {
		t.Fatalf("status = %d, want 529; body=%s", rec.Code, rec.Body.String())
	}
	var env2 struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env2); err != nil {
		t.Fatalf("body não é JSON: %v (%s)", err, rec.Body.String())
	}
	if env2.Error.Type != "overloaded_error" {
		t.Errorf("error.type = %q, want overloaded_error", env2.Error.Type)
	}
	if env2.Type == "error" {
		t.Errorf("cliente recebeu o JSON cru da Anthropic (type=error): %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TTS: openrouter-tts → kokoro-tts
// ---------------------------------------------------------------------------

type staticPins map[string][]string

func (p staticPins) PinnedUpstreams(alias string) []string { return p[alias] }

const ttsKokoroAudio = "ID3-kokoro-audio-bytes"

func kokoroReply() replyFn {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ttsKokoroAudio)
	}
}

type ttsEnv struct {
	disp       http.Handler
	bs         *breaker.Set
	or, kokoro *chainFake
}

// buildTTSChain monta openrouter-tts como no main (BuildOpenRouterTTSDirector
// + ExternalChatInterceptors(false, ...) + NewChatFallthroughTransport) e
// kokoro-tts com o construtor passthrough de produção (NewTTSProxy).
func buildTTSChain(t *testing.T, orReply replyFn, pinned bool) *ttsEnv {
	t.Helper()
	log := chainLogger()
	env := &ttsEnv{or: newChainFake(t, orReply), kokoro: newChainFake(t, kokoroReply())}
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-tts", Role: "tts", Tier: 0, URL: "http://127.0.0.1:1", Enabled: true},
		upstreams.UpstreamConfig{Name: "openrouter-tts", Role: "tts", Tier: 1, TierPriority: 0, URL: env.or.srv.URL + "/api", Enabled: true},
		upstreams.UpstreamConfig{Name: "kokoro-tts", Role: "tts", Tier: 1, TierPriority: 10, URL: env.kokoro.srv.URL, Enabled: true},
	)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	env.bs = breaker.NewSet(rdb, log, breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second}, loader.Names())
	orURL, _ := url.Parse(env.or.srv.URL + "/api")
	orTTS := &httputil.ReverseProxy{
		Director: proxy.BuildOpenRouterTTSDirector(orURL, "sk-or-int", nil, "openrouter-tts",
			"openai/gpt-4o-mini-tts", "", proxy.ParseTTSVoiceMap("pm_alex=onyx"), log),
		Transport: proxy.NewChatFallthroughTransport(&http.Transport{
			ResponseHeaderTimeout: 2 * time.Second,
		}),
		ModifyResponse: proxy.ExternalChatInterceptors(false).Intercept,
		ErrorHandler:   proxy.ErrorHandler("openrouter-tts", log),
	}
	kokoro, err := proxy.NewTTSProxy(env.kokoro.srv.URL, log)
	if err != nil {
		t.Fatalf("NewTTSProxy: %v", err)
	}
	var pins proxy.ModelPinner = staticPins{}
	if pinned {
		pins = staticPins{"tts-1": {"openrouter-tts", "kokoro-tts"}}
	} else {
		// Tier 0 desligado: cascadeTier1 percorre o tier 1 por prioridade.
		driveBreaker(t, env.bs, "local-tts", 500, 3)
	}
	env.disp = proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "tts",
		Loader:  loader,
		Breaker: env.bs,
		Proxies: map[string]http.Handler{
			"local-tts":      newPanicProxy(t, "local-tts não deve ser chamado"),
			"openrouter-tts": orTTS,
			"kokoro-tts":     kokoro,
		},
		Pins: pins,
		Log:  log,
	})
	return env
}

func serveTTS(disp http.Handler, model string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech",
		strings.NewReader(`{"model":"`+model+`","input":"olá, tudo bem?","voice":"pm_alex"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	}))
	rec := httptest.NewRecorder()
	disp.ServeHTTP(rec, r)
	return rec
}

func TestHaikuChain_TTSStatusCascade(t *testing.T) {
	expectKokoro := func(t *testing.T, env *ttsEnv, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "audio/mpeg" {
			t.Errorf("Content-Type = %q, want audio/mpeg", ct)
		}
		if rec.Body.String() != ttsKokoroAudio {
			t.Fatalf("corpo = %q, want só o áudio do kokoro (nenhum byte do openrouter-tts)", rec.Body.String())
		}
		if env.or.hits.Load() != 1 || env.kokoro.hits.Load() != 1 {
			t.Errorf("hits or=%d kokoro=%d, want 1/1", env.or.hits.Load(), env.kokoro.hits.Load())
		}
	}

	t.Run("429_cascateia", func(t *testing.T) {
		env := buildTTSChain(t, jsonReply(429, `{"error":{"message":"rate limited upstream"}}`), true)
		expectKokoro(t, env, serveTTS(env.disp, "tts-1"))
		// O openrouter-tts recebeu o body reescrito pelo director de produção.
		b := env.or.lastBody(t)
		if b["model"] != "openai/gpt-4o-mini-tts" || b["voice"] != "onyx" || b["response_format"] != "mp3" {
			t.Errorf("body openrouter-tts = %v", b)
		}
	})
	t.Run("503_cascateia", func(t *testing.T) {
		env := buildTTSChain(t, jsonReply(503, `{"error":{"message":"down"}}`), true)
		expectKokoro(t, env, serveTTS(env.disp, "tts-1"))
	})
	t.Run("400_nao_cascateia", func(t *testing.T) {
		const bad = `{"error":{"message":"voz inválida","type":"invalid_request_error"}}`
		env := buildTTSChain(t, jsonReply(400, bad), true)
		rec := serveTTS(env.disp, "tts-1")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if env.kokoro.hits.Load() != 0 {
			t.Fatalf("kokoro chamado em 400 (não deveria cascatear)")
		}
	})
	t.Run("sem_pino_tier0_off_502_cascateia", func(t *testing.T) {
		env := buildTTSChain(t, jsonReply(502, `{"error":{"message":"bad gateway"}}`), false)
		expectKokoro(t, env, serveTTS(env.disp, "modelo-sem-pino"))
	})
}
