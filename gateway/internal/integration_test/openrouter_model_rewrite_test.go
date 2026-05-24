//go:build integration

// Phase 06.9 Plan 05a Task 2 — OR-FIX end-to-end integration test.
//
// This test closes the test-shape gap that hid the OpenRouter 404 bug for
// months: the existing newSuccessMock returned 200 regardless of forwarded
// body, so tier-1 routing tests passed while production OpenRouter 404'd
// on the literal "qwen" alias. Plan 05a's newSuccessMockCapturing variant
// (resilience_helpers_test.go) captures the forwarded body BEFORE the
// 200 response so assertions can prove the model rewrite actually
// reached the upstream wire bytes.
//
// Flow:
//  1. freshSchema bootstraps Postgres with migration 0026 applied
//     (tier-1 seed rows: qwen/openrouter-chat → qwen/qwen3.5-27b, etc.)
//  2. Build a models.Resolver against the live pool, call Refresh once.
//  3. Wire tier0 := newFailMock (always 500), tier1 := newSuccessMockCapturing.
//  4. Build the OpenRouter director against tier1.server.URL with the
//     live resolver + "openrouter-chat" upstream name (matches the
//     production wiring in cmd/gateway/main.go).
//  5. Construct a proxy.NewDispatcher with role=llm + proxies map keyed
//     by upstream NAME (matching dispatcher's contract; see dispatcher.go
//     line 88 godoc).
//  6. Trip the local-llm breaker via driveBreaker so the dispatcher
//     routes to tier-1.
//  7. POST {"model":"qwen", ...} through the dispatcher; assert tier-1
//     received the request with body.model == "qwen/qwen3.5-27b" (the
//     schema-driven rewrite) — NOT the literal "qwen" that OpenRouter
//     404s on.
package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// TestIntegration_OpenRouterModelRewrite proves the per-upstream model
// rewrite landed end-to-end: a client POST with model="qwen" arrives at
// the tier-1 OpenRouter upstream with model="qwen/qwen3.5-27b" (the
// schema-driven target from migration 0026), NOT the literal alias.
// This is the regression test for OR-FIX — the bug that hid for months
// behind newSuccessMock returning 200 regardless of body.
func TestIntegration_OpenRouterModelRewrite(t *testing.T) {
	// Clear any per-instance env override so the schema row wins.
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	// Build a real Resolver against the migrated DB. Refresh once so the
	// in-memory cache is populated with the migration-0026 seed rows.
	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver.Refresh: %v", err)
	}

	tier0 := newFailMock(t)
	tier1 := newSuccessMockCapturing(t)

	// Build the production OpenRouter director against the capturing mock.
	tier1URL, _ := url.Parse(tier1.server.URL)
	director := proxy.BuildOpenRouterDirector(
		tier1URL,
		"sk-or-v1-test-bearer",
		[]string{"novita"}, // provider order (D-C2 Novita pin)
		false,              // allow_fallbacks (D-C4)
		resolver,
		"openrouter-chat", // upstream name — drives per-upstream resolver lookup
		discardLogger(),
	)
	tier1Proxy := &httputil.ReverseProxy{Director: director}

	// tier-0 proxy via the existing classifying proxy so 5xx → breaker increment.
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

	// Trip the local-llm breaker so the dispatcher routes to tier-1.
	driveBreaker(t, bs, "local-llm", 500, 3)

	// POST the alias-shaped request. Body carries model="qwen".
	rw := httptest.NewRecorder()
	r := makeAuthedRequest(`{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`,
		auth.DataClassNormal)
	disp.ServeHTTP(rw, r)

	// Sanity — tier-1 was hit.
	if got := tier1.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1 (dispatcher should route to OpenRouter "+
			"after local-llm breaker tripped). dispatcher status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	// THE assertion — the captured body's model field MUST be the schema-driven
	// target, NOT the alias the client sent. This is the regression that locks
	// OR-FIX in place.
	captured := tier1.LastBody()
	if len(captured) == 0 {
		t.Fatalf("tier-1 captured body is empty; expected JSON with model rewrite")
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body parse: %v; raw=%s", err, string(captured))
	}
	gotModel, _ := body["model"].(string)
	// Schema target after migration 0027: deepseek/deepseek-v4-flash:nitro.
	const schemaTarget = "deepseek/deepseek-v4-flash:nitro"
	if gotModel != schemaTarget {
		t.Errorf("OR-FIX REGRESSION: forwarded body model = %q, want %q (schema-driven rewrite). "+
			"The gateway is sending the alias literal to OpenRouter — this is the bug that 404'd in prod. "+
			"Full body: %s", gotModel, schemaTarget, string(captured))
	}

	// Defensive: provider.order should still be injected (the director runs the
	// model rewrite + provider.order in sequence; both must land).
	prov, ok := body["provider"].(map[string]any)
	if !ok {
		t.Errorf("provider object missing from forwarded body; got: %s", string(captured))
	} else {
		order, _ := prov["order"].([]any)
		if len(order) != 1 || order[0] != "novita" {
			t.Errorf("provider.order = %v, want [novita]", order)
		}
	}

	// Defensive: messages survived the rewrite passes byte-identical (or at
	// least preserved field-shape).
	if _, ok := body["messages"].([]any); !ok {
		t.Errorf("messages field lost during rewrite; body: %s", string(captured))
	}

	// Sanity log to make the test result self-explanatory in CI output.
	t.Logf("OR-FIX VERIFIED: tier-1 hits=%d, model rewritten to %q (was alias %q)",
		tier1.hits.Load(), gotModel, "qwen")
}

// Compile-time guards for the API surface this test depends on.
var (
	_ = proxy.BuildOpenRouterDirector
	_ = models.NewResolver
)
