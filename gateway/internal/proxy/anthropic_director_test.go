// anthropic_director_test.go — Phase 23-12: passo ① (BuildAnthropicProxy)
// ponta a ponta contra um fake da Anthropic em httptest + miniredis. Nenhuma
// chave real: as chaves são fictícias (sk-test-*).
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// ---------------------------------------------------------------------------
// Fake Anthropic (Messages API)
// ---------------------------------------------------------------------------

type anthDirCall struct {
	path   string
	header http.Header
	body   []byte
}

// anthDirFake registra cada request e responde via handle (por chave).
type anthDirFake struct {
	mu     sync.Mutex
	calls  []anthDirCall
	handle func(w http.ResponseWriter, r *http.Request, key string)
	srv    *httptest.Server
}

func newAnthDirFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, key string)) *anthDirFake {
	t.Helper()
	f := &anthDirFake{handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, anthDirCall{path: r.URL.Path, header: r.Header.Clone(), body: b})
		f.mu.Unlock()
		f.handle(w, r, r.Header.Get("x-api-key"))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *anthDirFake) callsSnapshot() []anthDirCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]anthDirCall(nil), f.calls...)
}

func (f *anthDirFake) lastBody(t *testing.T) map[string]any {
	t.Helper()
	calls := f.callsSnapshot()
	if len(calls) == 0 {
		t.Fatal("fake Anthropic não recebeu request")
	}
	var m map[string]any
	if err := json.Unmarshal(calls[len(calls)-1].body, &m); err != nil {
		t.Fatalf("body enviado não é JSON: %v (%s)", err, calls[len(calls)-1].body)
	}
	return m
}

func replyJSON(status int, body string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// ---------------------------------------------------------------------------
// Montagem do proxy
// ---------------------------------------------------------------------------

type anthDirEnv struct {
	mr   *miniredis.Miniredis
	pool *KeyPool
	cfg  AnthropicProxyConfig
}

func newAnthDirEnv(t *testing.T, upstream string, keys ...string) *anthDirEnv {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close(); mr.Close() })
	if len(keys) == 0 {
		keys = []string{testKeyA}
	}
	var raw []string
	for i, k := range keys {
		raw = append(raw, "conta"+string(rune('a'+i))+"="+k)
	}
	ak, err := ParseAnthropicKeys(strings.Join(raw, ","))
	if err != nil {
		t.Fatalf("ParseAnthropicKeys: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := NewKeyPool(ak, rdb, nil, nil, log)
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &anthDirEnv{mr: mr, pool: pool, cfg: AnthropicProxyConfig{
		Upstream:     u,
		Pool:         pool,
		Resolver:     models.NewResolverForTesting(map[[2]string]string{{"deepseek-flash", "anthropic-haiku"}: "claude-haiku-5-5"}),
		UpstreamName: "anthropic-haiku",
		Log:          log,
	}}
}

func (e *anthDirEnv) proxy(t *testing.T) http.Handler {
	t.Helper()
	rp, err := BuildAnthropicProxy(e.cfg)
	if err != nil {
		t.Fatalf("BuildAnthropicProxy: %v", err)
	}
	return rp
}

const anthDirChatBody = `{"model":"deepseek-flash","messages":[{"role":"system","content":"Você é útil."},{"role":"user","content":"oi"}],"temperature":0,"stream":false}`

func anthDirRequest(t *testing.T, body string, res *dispatchResult) *http.Request {
	t.Helper()
	r := makeRequestTenant(t, body, auth.DataClassNormal, "tenant-anth")
	r.Header.Set("Authorization", "Bearer ifix_sk_x")
	if res != nil {
		r = r.WithContext(withDispatchResult(r.Context(), res))
	}
	return r
}

func outputEffort(m map[string]any) any {
	oc, _ := m["output_config"].(map[string]any)
	if oc == nil {
		return nil
	}
	return oc["effort"]
}

func jsonEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatalf("got não é JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("want não é JSON: %v", err)
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if !bytes.Equal(ga, gb) {
		t.Fatalf("JSON diferente:\n got  %s\n want %s", ga, gb)
	}
}

// ---------------------------------------------------------------------------
// Testes
// ---------------------------------------------------------------------------

func TestBuildAnthropicProxy_JSONHappyPath(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(200, string(readFixture(t, "resp_text.json"))))
	env := newAnthDirEnv(t, fake.srv.URL)
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("resposta não é JSON: %v", err)
	}
	if out["object"] != "chat.completion" || out["choices"] == nil {
		t.Fatalf("resposta não é chat.completion OpenAI: %s", rec.Body.String())
	}

	calls := fake.callsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", c.path)
	}
	if got := c.header.Get("x-api-key"); got != testKeyA {
		t.Errorf("x-api-key = %q, want chave do pool", got)
	}
	if got := c.header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := c.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization do cliente vazou: %q", got)
	}
	if got := c.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	body := fake.lastBody(t)
	if body["model"] != "claude-haiku-5-5" {
		t.Errorf("model = %v", body["model"])
	}
	if outputEffort(body) != "high" {
		t.Errorf("effort = %v, want high (default)", outputEffort(body))
	}
	if _, ok := body["temperature"]; ok {
		t.Error("temperature não deveria ir à Anthropic")
	}
	if _, ok := body["system"]; !ok {
		t.Error("system top-level ausente")
	}
	if _, ok := body["cache_control"]; !ok {
		t.Error("cache_control top-level ausente")
	}
}

func TestBuildAnthropicProxy_ModelFallbackWhenNoAlias(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(200, string(readFixture(t, "resp_text.json"))))
	env := newAnthDirEnv(t, fake.srv.URL)
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, `{"model":"qwen","messages":[{"role":"user","content":"oi"}]}`, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if m := fake.lastBody(t)["model"]; m != "claude-haiku-5-5" {
		t.Fatalf("model = %v, want fallback claude-haiku-5-5", m)
	}
}

func TestBuildAnthropicProxy_SSE(t *testing.T) {
	sse := readFixture(t, "stream_text.sse")
	fake := newAnthDirFake(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write(sse)
	})
	env := newAnthDirEnv(t, fake.srv.URL)
	rec := httptest.NewRecorder()
	body := strings.Replace(anthDirChatBody, `"stream":false`, `"stream":true`, 1)
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, body, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"chat.completion.chunk"`) {
		t.Fatalf("sem chunks OpenAI: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("sem [DONE]: %s", out)
	}
	if strings.Contains(out, "Raciocínio interno") {
		t.Fatal("thinking vazou para o cliente")
	}
	if fake.lastBody(t)["stream"] != true {
		t.Error("stream:true não foi repassado")
	}
}

func TestBuildAnthropicProxy_Effort(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		tenant     string
		wantEffort any
		wantThink  bool
	}{
		{"tenant low", `{"model":"deepseek-flash","messages":[{"role":"user","content":"oi"}]}`, "low", "low", false},
		{"cliente medium vence tenant", `{"model":"deepseek-flash","reasoning_effort":"medium","messages":[{"role":"user","content":"oi"}]}`, "low", "medium", false},
		{"cliente reasoning enabled false", `{"model":"deepseek-flash","reasoning":{"enabled":false},"messages":[{"role":"user","content":"oi"}]}`, "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newAnthDirFake(t, replyJSON(200, string(readFixture(t, "resp_text.json"))))
			env := newAnthDirEnv(t, fake.srv.URL)
			env.cfg.Tenants = fakeEffortLookup{"tenant-anth": tc.tenant}
			rec := httptest.NewRecorder()
			env.proxy(t).ServeHTTP(rec, anthDirRequest(t, tc.body, nil))
			if rec.Code != 200 {
				t.Fatalf("status = %d", rec.Code)
			}
			b := fake.lastBody(t)
			if got := outputEffort(b); got != tc.wantEffort {
				t.Errorf("effort = %v, want %v", got, tc.wantEffort)
			}
			th, _ := b["thinking"].(map[string]any)
			if tc.wantThink && (th == nil || th["type"] != "disabled") {
				t.Errorf("thinking = %v, want disabled", b["thinking"])
			}
			if !tc.wantThink && th != nil {
				t.Errorf("thinking inesperado: %v", th)
			}
			if _, ok := b["reasoning"]; ok {
				t.Error("campo reasoning do cliente vazou para a Anthropic")
			}
			if _, ok := b["reasoning_effort"]; ok {
				t.Error("campo reasoning_effort do cliente vazou para a Anthropic")
			}
		})
	}
}

func TestBuildAnthropicProxy_529Fallthrough(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(529, bodyOverloaded))
	env := newAnthDirEnv(t, fake.srv.URL)
	res := &dispatchResult{}
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, res))
	if !res.fallthrough_ || res.wrote {
		t.Fatalf("fallthrough_=%v wrote=%v, want true/false", res.fallthrough_, res.wrote)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("nada deveria ser escrito ao cliente: %s", rec.Body.String())
	}
}

func TestBuildAnthropicProxy_LastBodyIsOpenAIEnvelope(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(529, bodyOverloaded))
	env := newAnthDirEnv(t, fake.srv.URL)
	res := &dispatchResult{}
	env.proxy(t).ServeHTTP(httptest.NewRecorder(), anthDirRequest(t, anthDirChatBody, res))
	if res.lastStatus != 529 {
		t.Fatalf("lastStatus = %d, want 529", res.lastStatus)
	}
	if res.lastContentType != "application/json" {
		t.Fatalf("lastContentType = %q", res.lastContentType)
	}
	jsonEqual(t, res.lastBody, `{"error":{"message":"Overloaded","type":"overloaded_error","code":"overloaded_error"}}`)
}

// ① é o único candidato com proxy (② sem proxy): o cliente recebe o 529 com
// envelope OpenAI, não o JSON cru da Anthropic.
func TestBuildAnthropicProxy_DispatchPinnedOnlyCandidateGetsEnvelope(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(529, bodyOverloaded))
	env := newAnthDirEnv(t, fake.srv.URL)
	rp := env.proxy(t)

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close(); mr.Close() })
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "anthropic-haiku", Role: "llm", Tier: 1, TierPriority: 0, URL: fake.srv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "openrouter-haiku", Role: "llm", Tier: 1, TierPriority: 10, URL: "http://127.0.0.1:1", Enabled: true},
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bs := breaker.NewSet(rdb, log, breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	mux := NewDispatcher(DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{"anthropic-haiku": rp},
		Pins:    stubPinner{"deepseek-flash": {"anthropic-haiku", "openrouter-haiku"}},
		Log:     log,
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, nil))
	if rec.Code != 529 {
		t.Fatalf("status = %d, want 529 (body=%s)", rec.Code, rec.Body.String())
	}
	jsonEqual(t, rec.Body.Bytes(), `{"error":{"message":"Overloaded","type":"overloaded_error","code":"overloaded_error"}}`)
}

func TestBuildAnthropicProxy_400PrefillTranslatedNoFallthrough(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(400, bodyPrefill))
	env := newAnthDirEnv(t, fake.srv.URL)
	res := &dispatchResult{}
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, res))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if res.fallthrough_ {
		t.Fatal("400 não deveria cascatear")
	}
	var env2 struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env2); err != nil || env2.Error.Type != "invalid_request_error" {
		t.Fatalf("envelope OpenAI inválido: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("JSON cru da Anthropic chegou ao cliente: %s", rec.Body.String())
	}
}

func TestBuildAnthropicProxy_KeySwap(t *testing.T) {
	okBody := string(readFixture(t, "resp_text.json"))
	fake := newAnthDirFake(t, func(w http.ResponseWriter, r *http.Request, key string) {
		if key == testKeyA {
			replyJSON(400, bodyCreditLow)(w, r, key)
			return
		}
		replyJSON(200, okBody)(w, r, key)
	})
	env := newAnthDirEnv(t, fake.srv.URL, testKeyA, testKeyB)
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, &dispatchResult{}))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	calls := fake.callsSnapshot()
	if len(calls) != 2 || calls[0].header.Get("x-api-key") != testKeyA || calls[1].header.Get("x-api-key") != testKeyB {
		t.Fatalf("ordem de chaves inesperada: %d calls", len(calls))
	}
	if !bytes.Equal(calls[0].body, calls[1].body) {
		t.Fatal("body reenviado à 2ª chave diferente do 1º")
	}
}

// bodyCapture é um interceptor extra que lê (e restaura) o body.
type bodyCapture struct {
	mu   sync.Mutex
	body []byte
}

func (b *bodyCapture) Intercept(resp *http.Response) error {
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	b.mu.Lock()
	b.body = raw
	b.mu.Unlock()
	return nil
}

func TestBuildAnthropicProxy_ExtraInterceptorsSeeTranslatedBody(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(200, string(readFixture(t, "resp_text.json"))))
	env := newAnthDirEnv(t, fake.srv.URL)
	cap1 := &bodyCapture{}
	env.cfg.Interceptors = []ProxyResponseInterceptor{cap1}
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	cap1.mu.Lock()
	defer cap1.mu.Unlock()
	if !bytes.Contains(cap1.body, []byte(`"choices"`)) {
		t.Fatalf("interceptor extra não viu o body traduzido: %s", cap1.body)
	}
}

func TestBuildAnthropicProxy_InvalidJSONNotSent(t *testing.T) {
	cases := map[string]string{
		"json inválido":    `{"model":"deepseek-flash","messages":[`,
		"messages ausente": `{"model":"deepseek-flash"}`,
		"messages vazio":   `{"model":"deepseek-flash","messages":[]}`,
		"body vazio":       ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newAnthDirFake(t, replyJSON(200, `{}`))
			env := newAnthDirEnv(t, fake.srv.URL)
			res := &dispatchResult{}
			rec := httptest.NewRecorder()
			env.proxy(t).ServeHTTP(rec, anthDirRequest(t, body, res))
			if n := len(fake.callsSnapshot()); n != 0 {
				t.Fatalf("fake Anthropic recebeu %d requests, want 0", n)
			}
			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if res.fallthrough_ {
				t.Fatal("400 local não deveria cascatear")
			}
			jsonEqual(t, rec.Body.Bytes(), `{"error":{"message":"invalid chat completion request","type":"invalid_request_error","code":"invalid_request_error"}}`)
		})
	}
}

func TestBuildAnthropicProxy_ToolLoopSkip(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(200, `{}`))
	env := newAnthDirEnv(t, fake.srv.URL)
	env.cfg.ToolLoopMode = ToolLoopModeSkip
	res := &dispatchResult{}
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, string(readFixture(t, "req_openai_tool_loop.json")), res))
	if n := len(fake.callsSnapshot()); n != 0 {
		t.Fatalf("fake Anthropic recebeu %d requests, want 0", n)
	}
	if !res.fallthrough_ || res.wrote {
		t.Fatalf("fallthrough_=%v wrote=%v, want true/false", res.fallthrough_, res.wrote)
	}
	var sk breakerSkipper
	if !errors.As(res.err, &sk) || !sk.SkipBreaker() {
		t.Fatalf("erro %v deveria ter SkipBreaker()=true", res.err)
	}
	if countsAsBreakerFailure(res.err) {
		t.Fatal("skip de tool loop não pode contar no breaker")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("nada deveria ser escrito: %s", rec.Body.String())
	}
}

// Com skip, request que NÃO é continuação vai normalmente à Anthropic.
func TestBuildAnthropicProxy_ToolLoopSkipOnlyContinuation(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(200, string(readFixture(t, "resp_text.json"))))
	env := newAnthDirEnv(t, fake.srv.URL)
	env.cfg.ToolLoopMode = ToolLoopModeSkip
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, &dispatchResult{}))
	if rec.Code != 200 || len(fake.callsSnapshot()) != 1 {
		t.Fatalf("status=%d calls=%d", rec.Code, len(fake.callsSnapshot()))
	}
}

func TestBuildAnthropicProxy_ToolLoopDisable(t *testing.T) {
	fake := newAnthDirFake(t, replyJSON(200, string(readFixture(t, "resp_tool_use.json"))))
	env := newAnthDirEnv(t, fake.srv.URL)
	env.cfg.ToolLoopMode = ToolLoopModeDisable
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, string(readFixture(t, "req_openai_tool_loop.json")), nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	b := fake.lastBody(t)
	th, _ := b["thinking"].(map[string]any)
	if th == nil || th["type"] != "disabled" {
		t.Fatalf("thinking = %v, want disabled", b["thinking"])
	}
	if outputEffort(b) != nil {
		t.Fatalf("effort = %v, want ausente no modo disable", outputEffort(b))
	}
}

func TestBuildAnthropicProxy_HeaderTimeoutFallsThrough(t *testing.T) {
	fake := newAnthDirFake(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		select {
		case <-time.After(time.Second):
		case <-r.Context().Done():
			return
		}
		replyJSON(200, `{}`)(w, r, "")
	})
	env := newAnthDirEnv(t, fake.srv.URL)
	env.cfg.HeaderTimeout = 200 * time.Millisecond
	res := &dispatchResult{}
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, res))
	if !res.fallthrough_ || res.wrote {
		t.Fatalf("fallthrough_=%v wrote=%v err=%v, want true/false", res.fallthrough_, res.wrote, res.err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("nada deveria ser escrito: %s", rec.Body.String())
	}
	if keys := env.mr.Keys(); len(keys) != 0 {
		t.Fatalf("timeout não pode marcar chave: %v", keys)
	}
}

func TestBuildAnthropicProxy_UpstreamDownFallsThroughWithoutMarkingKey(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // porta fechada
	env := newAnthDirEnv(t, "http://"+addr)
	res := &dispatchResult{}
	rec := httptest.NewRecorder()
	env.proxy(t).ServeHTTP(rec, anthDirRequest(t, anthDirChatBody, res))
	if !res.fallthrough_ || res.wrote {
		t.Fatalf("fallthrough_=%v wrote=%v err=%v", res.fallthrough_, res.wrote, res.err)
	}
	if keys := env.mr.Keys(); len(keys) != 0 {
		t.Fatalf("falha de rede não pode marcar chave: %v", keys)
	}
	for _, st := range env.pool.Snapshot(context.Background()) {
		if st.State != "ok" {
			t.Fatalf("chave %s ficou %s", st.Label, st.State)
		}
	}
}

func TestBuildAnthropicProxy_ConfigValidation(t *testing.T) {
	if _, err := BuildAnthropicProxy(AnthropicProxyConfig{}); err == nil {
		t.Fatal("config vazia deveria falhar")
	}
	u, _ := url.Parse("https://api.anthropic.com")
	if _, err := BuildAnthropicProxy(AnthropicProxyConfig{Upstream: u}); err == nil {
		t.Fatal("pool nil deveria falhar")
	}
}
