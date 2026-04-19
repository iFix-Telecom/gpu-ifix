//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestIntegration_07_UpstreamHealth verifies the /v1/health/upstreams
// handler coalesces requests within its 5s cache window.
func TestIntegration_07_UpstreamHealth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = freshSchema(t, ctx)

	var hits int64
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"healthy","services":{"llm":{"status":"healthy","latency_ms":40},"stt":{"status":"healthy","latency_ms":200},"embed":{"status":"healthy","latency_ms":30}},"uptime_s":100}`))
	}))
	defer bridge.Close()

	h := upstreams.NewHealthHandler(bridge.URL, discardLogger())
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Three rapid requests: only one should hit the bridge (5s cache).
	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("iter %d got %d want 200", i, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Errorf("bridge hits got %d want 1 (cache should coalesce)", n)
	}

	// Wait past cache TTL and retry.
	time.Sleep(5500 * time.Millisecond)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if n := atomic.LoadInt64(&hits); n != 2 {
		t.Errorf("post-TTL hits got %d want 2", n)
	}
}
