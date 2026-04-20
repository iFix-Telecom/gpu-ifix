package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// captureUpstream returns an httptest.Server that records the request
// (URL, headers, body) and replies 200. Returned cleanup closes the
// server.
func captureUpstream(t *testing.T) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	var captured *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Clone(context.Background())
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, captured, &body
}

// applyDirector simulates how httputil.ReverseProxy invokes the Director.
// We construct an outgoing request, run the Director against it, then
// dispatch via a default http.Client to capture the raw upstream view.
func applyDirector(t *testing.T, director func(*http.Request), method, path, contentType string, body []byte, clientHeaders http.Header, ctx context.Context) (*http.Request, []byte) {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://placeholder"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range clientHeaders {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	director(req)
	out, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(out))
	return req, out
}

// TestOpenRouterDirector_InjectsProvider verifies the body rewrap adds
// `"provider":{"order":["novita"],"allow_fallbacks":false}` (D-C2 +
// D-C1 amendment per 03-WAVE0-GATES.md — Novita pin, NOT Fireworks).
func TestOpenRouterDirector_InjectsProvider(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenRouterDirector(upstream, "sk-or-v1-test", []string{"novita"}, false)

	body := []byte(`{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`)
	_, patched := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", body, nil, nil)

	var m map[string]any
	if err := json.Unmarshal(patched, &m); err != nil {
		t.Fatalf("patched body not valid JSON: %v", err)
	}
	prov, ok := m["provider"].(map[string]any)
	if !ok {
		t.Fatalf("missing provider object; got body=%s", string(patched))
	}
	order, _ := prov["order"].([]any)
	if len(order) != 1 || order[0] != "novita" {
		t.Errorf("provider.order = %v, want [\"novita\"]", order)
	}
	if af, ok := prov["allow_fallbacks"].(bool); !ok || af {
		t.Errorf("provider.allow_fallbacks = %v, want false", prov["allow_fallbacks"])
	}
	// Original "messages" field MUST survive untouched (Threat T-03-06-02).
	if _, ok := m["messages"].([]any); !ok {
		t.Errorf("messages field lost during rewrap; got body=%s", string(patched))
	}
}

// TestOpenRouterDirector_InjectsAuthBearer asserts the exact
// Authorization header value matches `Bearer <key>`.
func TestOpenRouterDirector_InjectsAuthBearer(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenRouterDirector(upstream, "sk-or-v1-abc", []string{"novita"}, false)

	body := []byte(`{"model":"qwen","messages":[]}`)
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", body, nil, nil)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-or-v1-abc" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer sk-or-v1-abc")
	}
}

// TestOpenRouterDirector_StripsClientAuth ensures any Authorization /
// X-API-Key header sent by the client is removed (and replaced by the
// upstream-bound bearer if non-empty). Trust boundary preservation —
// pod/external upstreams MUST NOT see client credentials.
func TestOpenRouterDirector_StripsClientAuth(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenRouterDirector(upstream, "sk-or-v1-bound", []string{"novita"}, false)

	body := []byte(`{"model":"qwen"}`)
	clientHdrs := http.Header{}
	clientHdrs.Set("Authorization", "Bearer client-leaked-key")
	clientHdrs.Set("X-API-Key", "ifix_sk_leaked")
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", body, clientHdrs, nil)

	// Authorization is OVERWRITTEN by the bound bearer (good — client value gone).
	if got := req.Header.Get("Authorization"); got == "Bearer client-leaked-key" {
		t.Errorf("client Authorization survived: %q", got)
	}
	if req.Header.Get("X-API-Key") != "" {
		t.Errorf("X-API-Key = %q, want empty (stripped)", req.Header.Get("X-API-Key"))
	}
}

// TestOpenRouterDirector_OnlyRewritesChatCompletions verifies the path
// guard: a request misrouted to /v1/embeddings via this director leaves
// the body untouched.
func TestOpenRouterDirector_OnlyRewritesChatCompletions(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenRouterDirector(upstream, "sk-or-v1-test", []string{"novita"}, false)

	body := []byte(`{"input":"hello","model":"text-embedding-3-small"}`)
	_, out := applyDirector(t, director, http.MethodPost, "/v1/embeddings", "application/json", body, nil, nil)
	if !bytes.Equal(out, body) {
		t.Errorf("body changed for non-chat path: got %s want %s", string(out), string(body))
	}
}

// TestOpenRouterDirector_NoBearerSkipsHeader confirms an empty bearer
// (operator hasn't configured the env var yet) does NOT set Authorization
// — letting the request proceed and the upstream return 401 → which the
// breaker IsSuccessful filter (D-A4) correctly classifies as 4xx, NOT a
// failure. This avoids tripping the openrouter-chat breaker on a
// configuration mistake.
func TestOpenRouterDirector_NoBearerSkipsHeader(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenRouterDirector(upstream, "" /* empty bearer */, []string{"novita"}, false)

	body := []byte(`{"model":"qwen","messages":[]}`)
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", body, nil, nil)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty when bearer is missing", got)
	}
}
