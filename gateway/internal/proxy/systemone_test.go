// systemone_test.go — quick 261018: POST /v1/systemone (Jev) repassado ao
// OpenRouter com a credencial do upstream, auth por tenant, RES-08, timeout
// curto e billing a partir do usage {input_tokens, cost}.
package proxy

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

const jevBody = `{"model":"jev-1.13","state":"cliente quer comprar","questions":{"intencao":{"type":"choice","instructions":"qual?","criteria":{"compra":"quer comprar","duvida":"tem duvida"}}}}`

const jevResp = `{"answers":{"intencao":{"type":"choice","choice":"compra","confidence":0.77,"probabilities":{"compra":0.77,"duvida":0.23}}},"usage":{"input_tokens":321,"cost":0.0004}}`

type jevUpstream struct {
	srv      *httptest.Server
	hits     int64
	path     atomic.Value
	authz    atomic.Value
	clientK  atomic.Value
	body     atomic.Value
	delay    time.Duration
	status   int
	respBody string
}

func newJevUpstream(t *testing.T, status int, resp string, delay time.Duration) *jevUpstream {
	t.Helper()
	u := &jevUpstream{status: status, respBody: resp, delay: delay}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&u.hits, 1)
		u.path.Store(r.URL.Path)
		u.authz.Store(r.Header.Get("Authorization"))
		u.clientK.Store(r.Header.Get("X-Api-Key"))
		b, _ := io.ReadAll(r.Body)
		u.body.Store(string(b))
		if u.delay > 0 {
			select {
			case <-time.After(u.delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(u.status)
		_, _ = w.Write([]byte(u.respBody))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func jevRequest(t *testing.T, body string, dc auth.DataClass) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer ifix_sk_cliente")
	ctx := httpx.ContextWithRequestID(r.Context(), uuid.NewString())
	ctx = auth.WithContext(ctx, auth.AuthContext{
		TenantID:  uuid.NewString(),
		APIKeyID:  uuid.NewString(),
		DataClass: dc,
	})
	return r.WithContext(ctx)
}

func newJevHandler(t *testing.T, baseURL string, timeout time.Duration, ics ...ProxyResponseInterceptor) http.Handler {
	t.Helper()
	h, err := NewSystemOneHandler(SystemOneConfig{
		BaseURL:      baseURL,
		AuthBearer:   "sk-or-v1-upstream",
		Timeout:      timeout,
		Log:          discardLog(),
		Interceptors: ics,
	})
	if err != nil {
		t.Fatalf("NewSystemOneHandler: %v", err)
	}
	return h
}

func TestSystemOne_ForwardsBodyWithUpstreamCredential(t *testing.T) {
	up := newJevUpstream(t, 200, jevResp, 0)
	h := newJevHandler(t, up.srv.URL+"/api", time.Second)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, jevRequest(t, jevBody, auth.DataClassNormal))

	if rw.Code != 200 || rw.Body.String() != jevResp {
		t.Fatalf("got %d %s, want 200 passthrough", rw.Code, rw.Body.String())
	}
	if p := up.path.Load(); p != "/api/v1/systemone" {
		t.Fatalf("upstream path = %v, want /api/v1/systemone", p)
	}
	if a := up.authz.Load(); a != "Bearer sk-or-v1-upstream" {
		t.Fatalf("upstream Authorization = %v (client key must never leak)", a)
	}
	if b := up.body.Load(); b != jevBody {
		t.Fatalf("upstream body = %v, want verbatim", b)
	}
}

func TestSystemOne_SensitiveTenantBlocked(t *testing.T) {
	up := newJevUpstream(t, 200, jevResp, 0)
	h := newJevHandler(t, up.srv.URL+"/api", time.Second)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, jevRequest(t, jevBody, auth.DataClassSensitive))
	if rw.Code != http.StatusServiceUnavailable || !strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Fatalf("got %d %s, want RES-08 503", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(&up.hits) != 0 {
		t.Fatal("sensitive tenant reached the external upstream")
	}
}

func TestSystemOne_TimeoutReturns504NoRetry(t *testing.T) {
	up := newJevUpstream(t, 200, jevResp, 2*time.Second)
	h := newJevHandler(t, up.srv.URL+"/api", 150*time.Millisecond)
	rw := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rw, jevRequest(t, jevBody, auth.DataClassNormal))
	if rw.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d (%s), want 504", rw.Code, rw.Body.String())
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("took %v, timeout not enforced", el)
	}
	if n := atomic.LoadInt64(&up.hits); n != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1 (no cascade/retry)", n)
	}
}

func TestSystemOne_UpstreamErrorPassesThrough(t *testing.T) {
	up := newJevUpstream(t, 429, `{"error":{"message":"rate limited"}}`, 0)
	h := newJevHandler(t, up.srv.URL+"/api", time.Second)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, jevRequest(t, jevBody, auth.DataClassNormal))
	if rw.Code != 429 || !strings.Contains(rw.Body.String(), "rate limited") {
		t.Fatalf("got %d %s, want upstream 429 verbatim", rw.Code, rw.Body.String())
	}
	if n := atomic.LoadInt64(&up.hits); n != 1 {
		t.Fatalf("hits = %d, want 1", n)
	}
}

func TestSystemOne_RejectsBadBody(t *testing.T) {
	up := newJevUpstream(t, 200, jevResp, 0)
	h := newJevHandler(t, up.srv.URL+"/api", time.Second)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, jevRequest(t, `not json`, auth.DataClassNormal))
	if rw.Code != 400 {
		t.Fatalf("invalid JSON: status = %d, want 400", rw.Code)
	}

	rw = httptest.NewRecorder()
	big := `{"model":"jev-1.13","state":"` + strings.Repeat("x", SystemOneMaxBodyBytes) + `"}`
	h.ServeHTTP(rw, jevRequest(t, big, auth.DataClassNormal))
	if rw.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: status = %d, want 413", rw.Code)
	}
	if atomic.LoadInt64(&up.hits) != 0 {
		t.Fatal("rejected bodies must not reach the upstream")
	}
}

func TestSystemOne_RejectsInvalidBaseURL(t *testing.T) {
	if _, err := NewSystemOneHandler(SystemOneConfig{BaseURL: "openrouter.ai/api"}); err == nil {
		t.Fatal("expected error for base URL without scheme")
	}
}

// Billing: usage {input_tokens, cost} → tokens_in + custo reportado; model =
// model do corpo (a resposta não traz); route/upstream próprios.
func TestSystemOne_BillingFromReportedUsage(t *testing.T) {
	up := newJevUpstream(t, 200, jevResp, 0)
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, billing.NewStaticPricesLoader(nil), nil, nil, 5.0, discardLog())
	capt := &captureEnqueuer{}
	ix.flusher = capt
	h := newJevHandler(t, up.srv.URL+"/api", time.Second, ix)

	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, jevRequest(t, jevBody, auth.DataClassNormal))
	if rw.Code != 200 {
		t.Fatalf("status = %d", rw.Code)
	}
	if len(capt.events) != 1 {
		t.Fatalf("billing events = %d, want 1", len(capt.events))
	}
	ev := capt.events[0]
	if ev.Route != SystemOneBillingRoute || ev.Upstream != SystemOneUpstreamName {
		t.Fatalf("route/upstream = %s/%s", ev.Route, ev.Upstream)
	}
	if ev.Model != "jev-1.13" {
		t.Fatalf("model = %q, want jev-1.13 (from request body)", ev.Model)
	}
	if ev.TokensIn != 321 || ev.TokensOut != 0 {
		t.Fatalf("tokens = %d/%d, want 321/0", ev.TokensIn, ev.TokensOut)
	}
	if ev.CostSource != "provider_reported" || math.Abs(ev.CostExternalBRL-0.0004*5.0) > 1e-12 {
		t.Fatalf("cost = %v (%s), want provider_reported 0.002", ev.CostExternalBRL, ev.CostSource)
	}
	if ev.CostLocalPhantomBRL != 0 {
		t.Fatalf("phantom = %v, want 0 (external)", ev.CostLocalPhantomBRL)
	}
	if ev.Source != "final" {
		t.Fatalf("source = %s, want final", ev.Source)
	}
}

// O shape OpenAI (prompt_tokens) continua vencendo input_tokens.
func TestApplyUsageFrame_PromptTokensWinOverInputTokens(t *testing.T) {
	var f sseUsageFrame
	if err := json.Unmarshal([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":2,"input_tokens":999}}`), &f); err != nil {
		t.Fatal(err)
	}
	u := &billing.RequestUsage{}
	applyUsageFrame(u, &f)
	if u.TokensIn.Load() != 10 || u.TokensOut.Load() != 2 {
		t.Fatalf("tokens = %d/%d, want 10/2", u.TokensIn.Load(), u.TokensOut.Load())
	}
}

// Fallback de modelo do OpenRouter: o billing grava o `model` DEVOLVIDO na
// resposta (modelo efetivamente usado), não o alvo pedido.
func TestUsageJSON_BillsResponseModelNotRequested(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, billing.NewStaticPricesLoader(nil), nil, nil, 5.0, discardLog())
	capt := &captureEnqueuer{}
	ix.flusher = capt
	reqID := uuid.NewString()
	ctx := httpx.ContextWithRequestID(context.Background(), reqID)
	ctx = auditctx.WithBillingUpstream(ctx, "openrouter-chat")
	ctx = auditctx.WithBillingRoute(ctx, "chat")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/chat/completions", nil).WithContext(ctx)
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"model":"google/gemini-2.5-flash","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"cost":0.00001}}`)),
		Request: req,
	}
	if err := ix.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(capt.events) != 1 || capt.events[0].Model != "google/gemini-2.5-flash" {
		t.Fatalf("events = %+v, want model from response", capt.events)
	}
}
