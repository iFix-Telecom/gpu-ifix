package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// ctxWithRequestID runs the httpx.RequestID middleware on a noop handler so
// tests receive a ctx carrying the authoritative gateway request id. If
// clientID is non-empty it is sent as the inbound X-Request-ID header — the
// middleware keeps it in ctx as client_request_id but the gateway still
// generates its own UUIDv7 as the authoritative id.
func ctxWithRequestID(t *testing.T, clientID string) context.Context {
	t.Helper()
	var captured context.Context
	h := httpx.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r.Context()
	}))
	req := httptest.NewRequest("GET", "/", nil)
	if clientID != "" {
		req.Header.Set("X-Request-ID", clientID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return captured
}

func TestBuildDirector_RewritesURL(t *testing.T) {
	up, _ := url.Parse("http://pod.internal:8000")
	d := BuildDirector(up)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"qwen"}`))
	req = req.WithContext(ctxWithRequestID(t, ""))
	d(req)
	if req.URL.Scheme != "http" {
		t.Errorf("scheme got %q want http", req.URL.Scheme)
	}
	if req.URL.Host != "pod.internal:8000" {
		t.Errorf("host got %q want pod.internal:8000", req.URL.Host)
	}
	if req.URL.Path != "/v1/chat/completions" {
		t.Errorf("path got %q want /v1/chat/completions", req.URL.Path)
	}
	if req.Host != "pod.internal:8000" {
		t.Errorf("req.Host got %q want pod.internal:8000", req.Host)
	}
}

func TestBuildDirector_StripsAuthHeaders(t *testing.T) {
	up, _ := url.Parse("http://up")
	d := BuildDirector(up)
	req := httptest.NewRequest("POST", "/v1/embeddings", nil)
	req.Header.Set("Authorization", "Bearer ifix_sk_secret")
	req.Header.Set("X-API-Key", "ifix_sk_secret2")
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("Proxy-Authorization", "Bearer proxy")
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctxWithRequestID(t, ""))
	d(req)
	for _, h := range []string{"Authorization", "X-API-Key", "Cookie", "Proxy-Authorization"} {
		if v := req.Header.Get(h); v != "" {
			t.Errorf("header %s got %q, expected empty (stripped)", h, v)
		}
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type got %q want application/json (must survive director)", req.Header.Get("Content-Type"))
	}
}

func TestBuildDirector_StripsClientAcceptEncoding(t *testing.T) {
	// Regression: client-supplied Accept-Encoding survived BuildDirector
	// and reached the upstream, which caused http.Transport to pass the
	// gzipped response body through unchanged. The audit middleware's
	// jsonb capture choked on gzip magic byte 0x8b, rolling back the entire
	// audit_log row (SQLSTATE 22021). Stripping the header lets the Transport
	// negotiate compression on its own and auto-decompress before the body
	// reaches downstream consumers.
	up, _ := url.Parse("http://up")
	d := BuildDirector(up)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req = req.WithContext(ctxWithRequestID(t, ""))
	d(req)
	if v := req.Header.Get("Accept-Encoding"); v != "" {
		t.Errorf("Accept-Encoding got %q, expected empty (stripped so Transport can negotiate + auto-decompress)", v)
	}
}

func TestBuildDirector_PropagatesGatewayRequestID(t *testing.T) {
	up, _ := url.Parse("http://up")
	d := BuildDirector(up)
	ctx := ctxWithRequestID(t, "")
	gwID := httpx.RequestIDFrom(ctx)
	if gwID == "" {
		t.Fatalf("middleware did not generate a request id")
	}
	req := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	d(req)
	if got := req.Header.Get("X-Request-ID"); got != gwID {
		t.Errorf("X-Request-ID forwarded got %q want %q (gateway's)", got, gwID)
	}
}

func TestBuildDirector_OverwritesClientRequestID(t *testing.T) {
	up, _ := url.Parse("http://up")
	d := BuildDirector(up)
	clientID := uuid.New().String()
	ctx := ctxWithRequestID(t, clientID)
	gwID := httpx.RequestIDFrom(ctx)
	if gwID == clientID {
		t.Fatalf("setup invalid: middleware should generate a fresh id, got same as client")
	}
	req := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	req.Header.Set("X-Request-ID", clientID) // simulate client-provided header
	d(req)
	if req.Header.Get("X-Request-ID") != gwID {
		t.Errorf("director did not overwrite client-supplied X-Request-ID with gateway id; got %q", req.Header.Get("X-Request-ID"))
	}
}

func TestIsSSEResponse(t *testing.T) {
	cases := []struct {
		name string
		ct   string
		want bool
	}{
		{"sse-plain", "text/event-stream", true},
		{"sse-charset", "text/event-stream; charset=utf-8", true},
		{"json", "application/json", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if c.ct != "" {
				resp.Header.Set("Content-Type", c.ct)
			}
			if got := IsSSEResponse(resp); got != c.want {
				t.Errorf("IsSSEResponse(%q) got %v want %v", c.ct, got, c.want)
			}
		})
	}
	if IsSSEResponse(nil) != false {
		t.Error("IsSSEResponse(nil) should be false")
	}
}
