//go:build integration

// Phase 06.9 Plan 05b Task 3 — BLOCKER-1 / D-06 end-to-end env-override-wins.
//
// Per D-06 (06.9-CONTEXT.md):
//
//   - `UPSTREAM_<UPPER_UPSTREAM>_MODEL` env vars are a COEQUAL operator
//     escape hatch alongside the gatewayctl-managed schema row.
//   - Resolver.Resolve consults os.Getenv FIRST (Plan 02 D-06 precedence
//     chain step 1); the schema row wins ONLY when the env var is unset
//     or empty.
//   - Empty-string env values are treated as unset.
//   - Env-override scope is per-upstream via upstreamEnvVarMap — an LLM
//     env override MUST NOT leak into the embed path.
//
// Plan 02 covered the precedence chain at the resolver-unit-test layer.
// This Plan 05b file covers the END-TO-END integration through dispatcher
// + director + upstream so the contract is locked in CI at the wire shape
// where it actually matters — operators set the env var, the wire bytes
// at the upstream MUST reflect the override.
//
// Tests (3):
//
//  1. TestIntegration_EnvOverrideWinsEndToEnd
//     - t.Setenv UPSTREAM_LLM_OPENROUTER_MODEL=qwen/custom-override-from-env.
//     - Resolver instantiated AFTER env set (the env-read-on-Resolve
//       happens every call so timing isn't load-bearing, but ordering
//       matches a real boot scenario).
//     - Trip local-llm breaker; tier-1 capturing mock receives body with
//       model="qwen/custom-override-from-env" — NOT the schema target
//       "qwen/qwen3.5-27b".
//
//  2. TestIntegration_EnvOverrideEmptyFallsBackToSchema
//     - t.Setenv UPSTREAM_LLM_OPENROUTER_MODEL="" (explicit empty string).
//     - Schema value "qwen/qwen3.5-27b" wins — confirms empty-string is
//       treated as unset (Plan 02 Test 8).
//
//  3. TestIntegration_EnvOverrideOnlyAffectsMappedUpstream
//     - t.Setenv UPSTREAM_LLM_OPENROUTER_MODEL=qwen/custom-override-from-env.
//     - POST /v1/embeddings → tier-1 embed receives model="text-embedding-3-small"
//       (the schema value; the LLM env var has NO effect on the embed
//       upstream's resolver lookup).
package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// TestIntegration_EnvOverrideWinsEndToEnd — D-06: env var TAKES PRECEDENCE
// over the schema row at resolver-lookup time. End-to-end through
// dispatcher → director → upstream wire bytes.
func TestIntegration_EnvOverrideWinsEndToEnd(t *testing.T) {
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "qwen/custom-override-from-env")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver.Refresh: %v", err)
	}

	tier0 := newFailMock(t)
	tier1 := newSuccessMockCapturing(t)

	tier1URL, _ := url.Parse(tier1.server.URL)
	director := proxy.BuildOpenRouterDirector(
		tier1URL,
		"sk-or-v1-test-bearer",
		[]string{"novita"},
		false,
		resolver,
		"openrouter-chat",
		discardLogger(),
	)
	tier1Proxy := &httputil.ReverseProxy{Director: director}

	loader := resilienceLoader("llm",
		"local-llm", tier0.server.URL,
		"openrouter-chat", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	t0Proxy := newClassifyingProxy(t, tier0.server.URL, bs, "local-llm")

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm":       t0Proxy,
			"openrouter-chat": tier1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	driveBreaker(t, bs, "local-llm", 500, 3)

	rw := httptest.NewRecorder()
	r := makeAuthedRequest(`{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`,
		auth.DataClassNormal)
	disp.ServeHTTP(rw, r)

	if got := tier1.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1 (dispatcher must route after breaker trip). status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	captured := tier1.LastBody()
	if len(captured) == 0 {
		t.Fatalf("tier-1 captured body is empty")
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body parse: %v; raw=%s", err, string(captured))
	}
	gotModel, _ := body["model"].(string)
	if gotModel != "qwen/custom-override-from-env" {
		t.Errorf("D-06 ENV-OVERRIDE-WINS REGRESSION: forwarded body model = %q, want %q (env override should win over schema). "+
			"Schema value is %q; if got=schema then env override did NOT win. Full body: %s",
			gotModel, "qwen/custom-override-from-env", "qwen/qwen3.5-27b", string(captured))
	}
	if gotModel == "qwen/qwen3.5-27b" {
		t.Errorf("D-06 PRECEDENCE FAILURE: forwarded model = schema value %q; env override was ignored", gotModel)
	}

	t.Logf("D-06 ENV-OVERRIDE-WINS VERIFIED end-to-end: forwarded model = %q (env), schema (%q) suppressed",
		gotModel, "qwen/qwen3.5-27b")
}

// TestIntegration_EnvOverrideEmptyFallsBackToSchema — Plan 02 Test 8:
// empty-string env var MUST be treated as unset; schema row wins.
func TestIntegration_EnvOverrideEmptyFallsBackToSchema(t *testing.T) {
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver.Refresh: %v", err)
	}

	tier0 := newFailMock(t)
	tier1 := newSuccessMockCapturing(t)

	tier1URL, _ := url.Parse(tier1.server.URL)
	director := proxy.BuildOpenRouterDirector(
		tier1URL,
		"sk-or-v1-test-bearer",
		[]string{"novita"},
		false,
		resolver,
		"openrouter-chat",
		discardLogger(),
	)
	tier1Proxy := &httputil.ReverseProxy{Director: director}

	loader := resilienceLoader("llm",
		"local-llm", tier0.server.URL,
		"openrouter-chat", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	t0Proxy := newClassifyingProxy(t, tier0.server.URL, bs, "local-llm")

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm":       t0Proxy,
			"openrouter-chat": tier1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	driveBreaker(t, bs, "local-llm", 500, 3)

	rw := httptest.NewRecorder()
	r := makeAuthedRequest(`{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`,
		auth.DataClassNormal)
	disp.ServeHTTP(rw, r)

	if got := tier1.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1", got)
	}
	var body map[string]any
	captured := tier1.LastBody()
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body parse: %v; raw=%s", err, string(captured))
	}
	gotModel, _ := body["model"].(string)
	if gotModel != "qwen/qwen3.5-27b" {
		t.Errorf("EMPTY-ENV FALLBACK REGRESSION: forwarded model = %q, want %q (empty env should be treated as unset → schema wins)",
			gotModel, "qwen/qwen3.5-27b")
	}

	t.Logf("EMPTY-ENV FALLBACK VERIFIED: empty env treated as unset; schema value %q used", gotModel)
}

// TestIntegration_EnvOverrideOnlyAffectsMappedUpstream — per-upstream
// scoping: the LLM env override MUST NOT leak into the embed lookup
// because the upstreamEnvVarMap has separate entries per upstream.
func TestIntegration_EnvOverrideOnlyAffectsMappedUpstream(t *testing.T) {
	// LLM env override is set; embed env override left UNSET (empty).
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "qwen/custom-override-from-env")
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver.Refresh: %v", err)
	}

	tier0Embed := newFailMock(t)
	tier1Embed := newSuccessMockCapturing(t)

	tier1URL, _ := url.Parse(tier1Embed.server.URL)
	director := proxy.BuildOpenAIEmbedDirector(
		tier1URL,
		"sk-openai-test-bearer",
		resolver,
		"openai-embed",
		discardLogger(),
	)
	tier1Proxy := &httputil.ReverseProxy{Director: director}

	loader := resilienceLoader("embed",
		"local-embed", tier0Embed.server.URL,
		"openai-embed", tier1Embed.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	t0Proxy := newClassifyingProxy(t, tier0Embed.server.URL, bs, "local-embed")

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "embed",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-embed":  t0Proxy,
			"openai-embed": tier1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	driveBreaker(t, bs, "local-embed", 500, 3)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"bge-m3","input":["x"]}`))
	r.Header.Set("Content-Type", "application/json")
	ctxAuth := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	})
	r = r.WithContext(ctxAuth)
	disp.ServeHTTP(rw, r)

	if got := tier1Embed.hits.Load(); got < 1 {
		t.Fatalf("tier-1 embed hits = %d; want >= 1. status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}
	captured := tier1Embed.LastBody()
	if len(captured) == 0 {
		t.Fatalf("tier-1 captured body empty")
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body parse: %v; raw=%s", err, string(captured))
	}
	gotModel, _ := body["model"].(string)
	if gotModel != "text-embedding-3-small" {
		t.Errorf("PER-UPSTREAM SCOPING REGRESSION: embed-tier-1 received model = %q, want %q. "+
			"LLM env override (UPSTREAM_LLM_OPENROUTER_MODEL) MUST NOT leak into embed-upstream resolver lookup. "+
			"If got = qwen/custom-override-from-env, the env override scoping is broken.",
			gotModel, "text-embedding-3-small")
	}
	if gotModel == "qwen/custom-override-from-env" {
		t.Errorf("CROSS-TIER LEAK: embed received LLM env override %q — upstreamEnvVarMap scoping is broken", gotModel)
	}

	t.Logf("PER-UPSTREAM SCOPING VERIFIED: LLM env override did NOT leak into embed path; embed got %q",
		gotModel)
}

// Compile-time guard for the API surface this test depends on.
var (
	_ = proxy.BuildOpenAIEmbedDirector
	_ = proxy.BuildOpenRouterDirector
	_ = models.NewResolver
)
