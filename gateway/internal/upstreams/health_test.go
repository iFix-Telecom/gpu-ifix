package upstreams

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthHandler_AggregateHealthy(t *testing.T) {
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"healthy","services":{"llm":{"status":"healthy"},"stt":{"status":"healthy"},"embed":{"status":"healthy"}}}`))
	}))
	defer bridge.Close()

	h := NewHealthHandler(bridge.URL, discardLogger())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `"status":"healthy"`) {
		t.Errorf("body=%s", b)
	}
}

func TestHealthHandler_AggregateFailed(t *testing.T) {
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"status":"failed","services":{"llm":{"status":"healthy"},"stt":{"status":"unhealthy"},"embed":{"status":"healthy"}}}`))
	}))
	defer bridge.Close()

	h := NewHealthHandler(bridge.URL, discardLogger())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d; want 503", resp.StatusCode)
	}
}

func TestHealthHandler_UpstreamUnreachable(t *testing.T) {
	// Point at a definitely-closed port.
	h := NewHealthHandler("http://127.0.0.1:1", discardLogger())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d; want 503", resp.StatusCode)
	}
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env["status"] != "failed" {
		t.Errorf("status=%v; want failed", env["status"])
	}
}

func TestHealthHandler_Cache5Seconds(t *testing.T) {
	var hits atomic.Int32
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"status":"healthy","hits":%d}`, hits.Load())
	}))
	defer bridge.Close()

	h := NewHealthHandler(bridge.URL, discardLogger())
	srv := httptest.NewServer(h)
	defer srv.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hit %d times across 3 rapid requests; want 1 (cache)", hits.Load())
	}
}

func TestHealthHandler_CacheExpiresAfterTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TTL expiry test in short mode")
	}
	var hits atomic.Int32
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer bridge.Close()

	h := NewHealthHandler(bridge.URL, discardLogger())
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	time.Sleep(cacheTTL + 500*time.Millisecond)
	resp, err = http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 2 {
		t.Fatalf("upstream hit %d times after TTL expiry; want 2", hits.Load())
	}
}
