//go:build integration

// Phase 3 Plan 03-07 Task 2 — fallback routing wall-time observation.
//
// SC-1 budget: client-observable failover ≤10s. This test wires the
// production proxy.NewDispatcher with two mock httptest backends (tier-0
// always 500, tier-1 always 200) + a real breaker.Set with a fast
// ConsecutiveFailures=2 to trip in microseconds, and asserts the wall
// time from the first request to the first tier-1 hit is well under 10s.
package integration

import (
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
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestIntegration_FailoverToTier1WithinObservedWindow proves SC-1: when
// tier-0 is dead, the dispatcher routes to tier-1 within ≤10s of the
// first request landing. This is the headline resilience guarantee.
func TestIntegration_FailoverToTier1WithinObservedWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	tier0 := newFailMock(t)
	tier1 := newSuccessMock(t)

	loader := resilienceLoader("llm",
		"local-llm", tier0.server.URL,
		"openrouter-chat", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)

	// Build proxies that translate 5xx into a breaker.HTTPError so the
	// breaker increments ConsecutiveFailures (D-A4 IsSuccessful filter).
	t0Proxy := newClassifyingProxy(t, tier0.server.URL, bs, "local-llm")
	t1Proxy := newClassifyingProxy(t, tier1.server.URL, bs, "openrouter-chat")

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

	// Fire requests on a 100ms cadence and stop the timer when the first
	// tier-1 hit lands.
	start := time.Now()
	deadline := start.Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rw := httptest.NewRecorder()
		r := makeAuthedRequest(`{"model":"qwen","messages":[]}`, auth.DataClassNormal)
		disp.ServeHTTP(rw, r)
		if tier1.hits.Load() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	elapsed := time.Since(start)

	if tier1.hits.Load() == 0 {
		t.Fatalf("tier-1 never received a request after %v; tier-0 hits = %d",
			elapsed, tier0.hits.Load())
	}
	if elapsed > 10*time.Second {
		t.Errorf("failover wall time = %v, want <= 10s (SC-1)", elapsed)
	}
	// Sanity: tier-0 must have received at least the trip threshold (2)
	// before the breaker opened.
	if got := tier0.hits.Load(); got < 2 {
		t.Errorf("tier-0 hits = %d; want >= 2 (trip threshold)", got)
	}
	t.Logf("SC-1: failover observed in %v (tier-0 hits=%d, tier-1 hits=%d)",
		elapsed, tier0.hits.Load(), tier1.hits.Load())
}

// newClassifyingProxy is the test-side equivalent of the production
// reverse proxy: forwards to `target`, translates 5xx into the
// breaker.HTTPError contract that breaker.IsSuccessful classifies as a
// failure (D-A4). Without this translation the dispatcher's
// proxy.ServeHTTP wrapper around httputil.ReverseProxy would never
// notify the breaker — the failover loop would spin forever.
//
// We use breaker.Set.Execute so the breaker's ConsecutiveFailures
// counter actually increments on every 5xx. This mirrors what
// PR 03-05's probe loop does.
func newClassifyingProxy(t *testing.T, target string, bs *breaker.Set, name string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := bs.Execute(name, func() (*http.Response, error) {
			req, herr := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.Path, r.Body)
			if herr != nil {
				return nil, herr
			}
			res, derr := client.Do(req)
			if derr != nil {
				return nil, derr
			}
			if res.StatusCode >= 500 {
				res.Body.Close()
				return nil, &breaker.HTTPError{Status: res.StatusCode, Msg: "upstream 5xx"}
			}
			return res, nil
		})
		if err != nil {
			// Translate breaker error back into a 5xx so the dispatcher's
			// caller observes a real upstream failure (the dispatcher
			// itself sees 200 OK from this handler — the breaker decision
			// is what matters for tier selection on the NEXT request).
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
	})
}

// makeAuthedRequest builds an authenticated httptest request.
func makeAuthedRequest(body string, dc auth.DataClass) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ctx := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: dc,
	})
	return r.WithContext(ctx)
}

// discardWriter is io.Writer that drops everything; used to silence
// dispatcher slog output in tests.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Compile-time assertion: upstreams.NewLoaderInMemory exists and
// the breaker.Set API used above hasn't drifted.
var _ = upstreams.NewLoaderInMemory
