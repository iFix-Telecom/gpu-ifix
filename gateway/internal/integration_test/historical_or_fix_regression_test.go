//go:build integration

// Phase 06.9 Plan 05b Task 1 — R13 historical-bug regression test.
//
// This is the test that PROVES the Phase 06.9 fix would have caught the
// original Phase 03 SC-1 failure mode in CI before it reached production.
//
// Historical bug (pre-Phase-06.9):
//   - Migration 0026 not applied; resolver schema lookup used single-column
//     PK keyed on alias only.
//   - Director rewrote body.model → resolver.Resolve("qwen") which returned
//     "qwen" (the tier-0 local target, NOT the OpenRouter-canonical slug).
//   - Gateway forwarded {"model":"qwen", ...} to OpenRouter.
//   - OpenRouter responded 404 + HTML (its real behavior for unknown slugs).
//   - SC-1 live UAT FAILED — failover did NOT recover; production was
//     effectively unavailable for tier-1 fallback during the incident.
//   - The unit tests passed all along because the fake mock returned 200
//     regardless of body — "fake upstream accepts any model" anti-pattern.
//
// This test uses Plan 05a's newSelectiveMock(t, []string{"qwen/qwen3.5-27b"})
// to reproduce the real OpenRouter shape: the fake upstream REJECTS
// {"model":"qwen"} with HTTP 404 + HTML body (matches real OpenRouter
// response shape on an unknown slug) but ACCEPTS {"model":"qwen/qwen3.5-27b"}
// with HTTP 200 + JSON envelope.
//
// Flow:
//
//  1. freshSchema + migration 0026 (resolver populated with
//     (qwen, openrouter-chat) → qwen/qwen3.5-27b).
//  2. tier0 = newFailMock — drives local-llm breaker OPEN.
//  3. tier1 = newSelectiveMock(t, []string{"qwen/qwen3.5-27b"}).
//  4. Wire OpenRouter director with resolver + "openrouter-chat".
//  5. Trip local-llm breaker via 3× failure observations.
//  6. POST /v1/chat/completions {"model":"qwen", ...}.
//  7. Assert: HTTP 200 (NOT 404). The director rewrote qwen → qwen/qwen3.5-27b
//     BEFORE forwarding; tier1's selective mock accepted; response body is JSON.
//
// Reverse sanity assertion inside the same test function: re-run with a
// newSelectiveMock(t, []string{"qwen-NEVER-MATCHES"}) and assert the gateway
// observes upstream rejection (the selective mock returns 404; the dispatcher
// propagates that status). This guards against false positives where the
// selective mock would have accepted ANY input.
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

// TestIntegration_HistoricalORFixRegression proves the Phase 06.9 fix
// would have caught the original failure mode where OpenRouter rejected
// model="qwen" with 404. The selective mock mimics that exact rejection
// shape; the director's per-upstream-name resolver rewrite saves the day.
func TestIntegration_HistoricalORFixRegression(t *testing.T) {
	// Clear env override so the schema row drives the rewrite (the historical
	// bug surfaced when there was NO env override; the bug was specifically
	// in the schema/resolver path).
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	resolver := models.NewResolver(pool, discardLogger())
	if err := resolver.Refresh(ctx); err != nil {
		t.Fatalf("resolver.Refresh: %v", err)
	}

	// --- Phase A: the fix-verified path ---
	// Selective mock ACCEPTS only "deepseek/deepseek-v4-flash:nitro" (the
	// schema target after migration 0027). Anything else gets 404 + HTML
	// (real OpenRouter shape on unknown slug).
	tier0Fail := newFailMock(t)
	const schemaTarget = "deepseek/deepseek-v4-flash:nitro"
	tier1Selective := newSelectiveMock(t, []string{schemaTarget})

	tier1URL, _ := url.Parse(tier1Selective.server.URL)
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
		"local-llm", tier0Fail.server.URL,
		"openrouter-chat", tier1Selective.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	t0Proxy := newClassifyingProxy(t, tier0Fail.server.URL, bs, "local-llm")

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

	// Trip local-llm breaker so the dispatcher routes to tier-1.
	driveBreaker(t, bs, "local-llm", 500, 3)

	// Client POSTs the alias literal — pre-fix this would have been
	// forwarded as-is to OpenRouter, which would respond 404.
	rw := httptest.NewRecorder()
	r := makeAuthedRequest(`{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`,
		auth.DataClassNormal)
	disp.ServeHTTP(rw, r)

	// The headline assertion: status 200 proves the director rewrote
	// "qwen" → schema target before forwarding, and the selective mock
	// accepted. Pre-fix this status would have been 404 + HTML.
	if rw.Code != http.StatusOK {
		t.Fatalf("R13 HISTORICAL-BUG REGRESSION: gateway status = %d, want 200.\n"+
			"  Pre-Phase-06.9 this would have been 404 because the gateway forwarded\n"+
			"  the literal alias 'qwen' to OpenRouter. The fix rewrites it to\n"+
			"  %q per the schema row before forwarding.\n"+
			"  tier-0 hits=%d  tier-1 hits=%d  body=%s",
			rw.Code, schemaTarget, tier0Fail.hits.Load(), tier1Selective.hits.Load(), rw.Body.String())
	}

	// Sanity: tier-1 received the request.
	if got := tier1Selective.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1 (dispatcher must route to OpenRouter "+
			"after local-llm breaker tripped)", got)
	}

	// Sanity: the captured body shows the schema-driven rewrite landed.
	captured := tier1Selective.LastBody()
	if len(captured) == 0 {
		t.Fatalf("tier-1 captured body is empty; expected JSON with rewritten model")
	}
	var body map[string]any
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("captured body parse: %v; raw=%s", err, string(captured))
	}
	gotModel, _ := body["model"].(string)
	if gotModel != schemaTarget {
		t.Errorf("R13 HISTORICAL-BUG REGRESSION: forwarded body model = %q, want %q. "+
			"The schema rewrite did not run before forwarding to OpenRouter — this is the bug "+
			"that 404'd in prod for months. Full body: %s",
			gotModel, schemaTarget, string(captured))
	}

	// Response body must be JSON (the selective mock returns
	// {"id":"gen-test", ...} when it accepts; it would return HTML if it
	// rejected). The selective mock's accept path always sets
	// Content-Type: application/json.
	respCT := rw.Header().Get("Content-Type")
	if respCT != "application/json" {
		t.Errorf("response Content-Type = %q, want application/json (selective mock accept path)", respCT)
	}

	t.Logf("R13 FIX VERIFIED: gateway responded 200 after rewrite; tier-1 selective mock accepted %q",
		gotModel)

	// --- Phase B: reverse sanity assertion ---
	// Wire a SECOND selective mock that accepts ONLY a slug we never send.
	// This proves the selective mock actually rejects (it's not a false-positive
	// 200-pass). The fix-path test would be meaningless if the selective mock
	// were just returning 200 for everything.
	rejectMock := newSelectiveMock(t, []string{"qwen-NEVER-MATCHES"})
	rejectURL, _ := url.Parse(rejectMock.server.URL)
	rejectDirector := proxy.BuildOpenRouterDirector(
		rejectURL,
		"sk-or-v1-test-bearer",
		[]string{"novita"},
		false,
		resolver,
		"openrouter-chat",
		discardLogger(),
	)
	rejectTier1Proxy := &httputil.ReverseProxy{Director: rejectDirector}

	rejectLoader := resilienceLoader("llm",
		"local-llm", tier0Fail.server.URL,
		"openrouter-chat", rejectMock.server.URL,
	)
	rejectBs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		rejectLoader.Names(),
	)
	rejectT0Proxy := newClassifyingProxy(t, tier0Fail.server.URL, rejectBs, "local-llm")

	rejectDisp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  rejectLoader,
		Breaker: rejectBs,
		Proxies: map[string]http.Handler{
			"local-llm":       rejectT0Proxy,
			"openrouter-chat": rejectTier1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})
	driveBreaker(t, rejectBs, "local-llm", 500, 3)

	rwReject := httptest.NewRecorder()
	rReject := makeAuthedRequest(`{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`,
		auth.DataClassNormal)
	rejectDisp.ServeHTTP(rwReject, rReject)

	// The reject mock returns 404 — the gateway should propagate the upstream
	// failure status. We assert NOT 200 (the reject mock should fire). If
	// the reverse-proxy chain somehow turned this into a different status
	// (e.g. 502 due to a transport-layer error), that's still NOT 200 and
	// passes the sanity check.
	if rwReject.Code == http.StatusOK {
		t.Errorf("R13 REVERSE SANITY: rejectMock should have rejected %q "+
			"(it only accepts 'qwen-NEVER-MATCHES'), but gateway returned 200. "+
			"The selective mock may be broken — re-check newSelectiveMock semantics. "+
			"tier-1 hits=%d  body=%s",
			schemaTarget, rejectMock.hits.Load(), rwReject.Body.String())
	}

	// Bonus assertion: rejectMock must have received the request (the
	// rewrite path ran, then was rejected by the upstream).
	if got := rejectMock.hits.Load(); got < 1 {
		t.Errorf("reverse sanity: rejectMock hits = %d; want >= 1 (request must have reached the mock)", got)
	}

	t.Logf("R13 REVERSE SANITY PASSED: selective mock rejected as expected; gateway status = %d",
		rwReject.Code)
}

// Compile-time guard.
var _ = proxy.BuildOpenRouterDirector
