//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestIntegration_07_UpstreamHealth verifies the Phase 3 refactored
// /v1/health/upstreams handler. The handler no longer proxies to the
// pod's :9100 bridge — Phase 3 derives state in-process from the
// upstreams loader + breaker.Set (CONTEXT.md D-D1). This integration
// test wires both real components against testcontainer Postgres +
// Redis and asserts the response body shape + 2s cache TTL.
func TestIntegration_07_UpstreamHealth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)
	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	t.Setenv("UPSTREAM_STT_URL", "http://local-stt:8001")
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "https://openrouter.ai/api/v1")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", "or-test")
	t.Setenv("UPSTREAM_STT_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_STT_OPENAI_AUTH_BEARER", "oa-test")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER", "oa-embed")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
		loader.Names())

	h := upstreams.NewHealthHandler(loader, bs, discardLogger())
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Baseline: every breaker CLOSED → status=ok with 200.
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("baseline: status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env["status"] != "ok" {
		t.Errorf("baseline status = %v, want ok", env["status"])
	}
	ups, _ := env["upstreams"].(map[string]any)
	// Phase 11.1: migration 0028 removed local-stt — 6 → 5.
	// Phase 11.2: migration 0029 re-added local-stt (+gemini-stt + groq-whisper),
	// but gemini-stt/groq-whisper rows have url_env_var = UPSTREAM_STT_FALLBACK_{1,2}_URL
	// which is unset in CI integration → loader skips them. local-stt uses
	// UPSTREAM_STT_URL (set by fixture) so it loads → 5 + 1 = 6.
	if got := len(ups); got != 6 {
		t.Errorf("upstreams count = %d, want 6", got)
	}

	// Trip the local-llm breaker. Per the cache TTL (2s), the next GET
	// MUST still return ok (cached). After the TTL the new state
	// (degraded — tier-1 openrouter-chat is still CLOSED) must surface.
	for i := 0; i < 3; i++ {
		_, _ = bs.Execute("local-llm", func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: 503, Msg: "trip"}
		})
	}
	time.Sleep(50 * time.Millisecond)

	// Cached: still ok.
	resp2, _ := http.Get(srv.URL)
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	var env2 map[string]any
	_ = json.Unmarshal(body2, &env2)
	if env2["status"] != "ok" {
		t.Errorf("cached status = %v, want ok (within 2s TTL)", env2["status"])
	}

	// Past TTL: degraded.
	time.Sleep(2100 * time.Millisecond)
	resp3, _ := http.Get(srv.URL)
	body3, _ := io.ReadAll(resp3.Body)
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("post-TTL status code = %d, want 200 (degraded keeps 200)", resp3.StatusCode)
	}
	var env3 map[string]any
	_ = json.Unmarshal(body3, &env3)
	if env3["status"] != "degraded" {
		t.Errorf("post-TTL status = %v, want degraded", env3["status"])
	}
}
