//go:build integration

// Phase 06.9 Plan 05b Task 1 — R4 local-tier byte-identical pass-through tests.
//
// Per the Plan 03 R4 audit decision (and the comment at cmd/gateway/main.go
// :552-557), the local-tier proxies do NOT call the resolver and do NOT
// touch body.model — the alias the client sent is what the local pod
// receives. These tests lock that contract in CI.
//
// Tests (2):
//
//  1. TestIntegration_LocalLLMPassThroughByteIdentical
//     - tier-0 = newSuccessMockCapturing registered as "local-llm"; tier-0 is
//       healthy so no breaker tripped; dispatcher routes to tier-0.
//     - POST /v1/chat/completions {"model":"qwen","messages":[...],
//       "temperature":0.7} → tier-0 receives the EXACT same body bytes
//       (bytes.Equal(originalBody, mock.LastBody())); model field stays
//       "qwen"; temperature + messages preserved exactly.
//
//  2. TestIntegration_LocalEmbedPassThroughByteIdentical
//     - tier-0 = newSuccessMockCapturing registered as "local-embed"; tier-0
//       is healthy; dispatcher routes to tier-0.
//     - POST /v1/embeddings {"model":"bge-m3","input":["x","y"]} → tier-0
//       (local-embed) sees identical body, model="bge-m3", NO dimensions
//       injected (the local embed proxy does NOT touch body — pass-through
//       per main.go:557 NewEmbeddingsProxy contract).
//
// Together these prove removing models.Handler did NOT regress tier-0 routing.
// If a future refactor ever introduces a body-mutating step in the local-tier
// path, these tests fail loudly.
package integration

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// TestIntegration_LocalLLMPassThroughByteIdentical — local-llm tier-0 sees
// byte-identical body when the breaker stays CLOSED (no tier-1 routing).
// Locks the "local tier never rewrites" contract per Plan 03 R4 audit.
func TestIntegration_LocalLLMPassThroughByteIdentical(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	tier0 := newSuccessMockCapturing(t)
	// tier-1 omitted on purpose — local tier is healthy; dispatcher should
	// never invoke tier-1. We register a sentinel that fails if hit.
	tier1Sentinel := newFailMock(t)

	loader := resilienceLoader("llm",
		"local-llm", tier0.server.URL,
		"openrouter-chat", tier1Sentinel.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)

	// Build a tier-0 proxy via the classifying proxy (same shape as
	// fallback_routing_test). 2xx responses do NOT increment the breaker,
	// so the breaker stays CLOSED and the dispatcher routes to tier-0.
	t0Proxy := newClassifyingProxy(t, tier0.server.URL, bs, "local-llm")
	t1Proxy := newClassifyingProxy(t, tier1Sentinel.server.URL, bs, "openrouter-chat")

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm":       t0Proxy,
			"openrouter-chat": t1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	// Build the exact request body the client would send. The byte-identical
	// assertion below compares this exact buffer with what tier-0 received.
	originalBody := `{"model":"qwen","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`
	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(originalBody))
	r.Header.Set("Content-Type", "application/json")
	ctxAuth := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	})
	r = r.WithContext(ctxAuth)
	disp.ServeHTTP(rw, r)

	// Dispatcher should route to tier-0 (breaker CLOSED) — tier-1 must NEVER
	// see this request.
	if got := tier1Sentinel.hits.Load(); got != 0 {
		t.Fatalf("tier-1 sentinel hits = %d; want 0 (breaker CLOSED, traffic must stay tier-0)", got)
	}
	if got := tier0.hits.Load(); got < 1 {
		t.Fatalf("tier-0 hits = %d; want >= 1 (dispatcher must route to local-llm). status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	captured := tier0.LastBody()
	if !bytes.Equal([]byte(originalBody), captured) {
		t.Errorf("LOCAL-LLM R4 REGRESSION: tier-0 received a body that differs from the client body.\n"+
			"  want (client sent): %s\n"+
			"  got  (tier-0 saw):  %s",
			originalBody, string(captured))
	}

	// Defensive content-type assertion: local tier preserves Content-Type
	// (the chat proxy's BuildDirector does NOT touch the header).
	if ct := tier0.LastContentType(); ct != "application/json" {
		t.Errorf("tier-0 Content-Type = %q, want application/json", ct)
	}

	t.Logf("R4 LOCAL-LLM VERIFIED: %d bytes forwarded byte-identical; tier-1 untouched", len(captured))
}

// TestIntegration_LocalEmbedPassThroughByteIdentical — local-embed tier-0
// sees byte-identical body. NO dimensions injection on the local path —
// dimensions=1024 is a tier-1 director concern only (EMBED-OAI-FIX), the
// local pod consumes its own native model and doesn't need the parity
// hint.
func TestIntegration_LocalEmbedPassThroughByteIdentical(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	tier0 := newSuccessMockCapturing(t)
	tier1Sentinel := newFailMock(t)

	loader := resilienceLoader("embed",
		"local-embed", tier0.server.URL,
		"openai-embed", tier1Sentinel.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	t0Proxy := newClassifyingProxy(t, tier0.server.URL, bs, "local-embed")
	t1Proxy := newClassifyingProxy(t, tier1Sentinel.server.URL, bs, "openai-embed")

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "embed",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-embed":  t0Proxy,
			"openai-embed": t1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	originalBody := `{"model":"bge-m3","input":["x","y"]}`
	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(originalBody))
	r.Header.Set("Content-Type", "application/json")
	ctxAuth := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	})
	r = r.WithContext(ctxAuth)
	disp.ServeHTTP(rw, r)

	if got := tier1Sentinel.hits.Load(); got != 0 {
		t.Fatalf("tier-1 sentinel hits = %d; want 0 (breaker CLOSED on local-embed)", got)
	}
	if got := tier0.hits.Load(); got < 1 {
		t.Fatalf("tier-0 hits = %d; want >= 1 (dispatcher must route to local-embed). status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	captured := tier0.LastBody()
	if !bytes.Equal([]byte(originalBody), captured) {
		t.Errorf("LOCAL-EMBED R4 REGRESSION: tier-0 received a body that differs from the client body.\n"+
			"  want (client sent): %s\n"+
			"  got  (tier-0 saw):  %s",
			originalBody, string(captured))
	}

	// Defensive: NO dimensions key injected by the local path (the tier-1
	// director adds dimensions=1024; the local path must NOT). The simplest
	// way to verify is the bytes.Equal check above — if the body matches
	// exactly, no key was added. We still spot-check the substring as
	// defensive insurance in case the bytes.Equal regression is masked.
	if bytes.Contains(captured, []byte(`"dimensions"`)) {
		t.Errorf("LOCAL-EMBED R4 REGRESSION: tier-0 body contains 'dimensions' key — only the tier-1 director should inject this. body=%s",
			string(captured))
	}

	if ct := tier0.LastContentType(); ct != "application/json" {
		t.Errorf("tier-0 Content-Type = %q, want application/json", ct)
	}

	t.Logf("R4 LOCAL-EMBED VERIFIED: %d bytes forwarded byte-identical; tier-1 untouched", len(captured))
}
