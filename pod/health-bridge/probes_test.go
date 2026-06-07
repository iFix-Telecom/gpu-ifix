package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestProbeSTT_Success — Phase 11.2 Plan 05: verbatim restore from
// 39bec50^:pod/health-bridge/probes_test.go. Asserts probeSTT POSTs
// multipart/form-data to /v1/audio/transcriptions and parses 200 +
// {"text":"silence"} as StatusHealthy.
func TestProbeSTT_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("expected multipart, got %s", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "silence"})
	}))
	defer srv.Close()

	r := probeSTT(context.Background(), newHTTPClient(), srv.URL, discardLogger())
	if r.Status != StatusHealthy {
		t.Errorf("got %q want healthy; err=%q", r.Status, r.Error)
	}
}

func TestProbeLLM_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "x",
			"object":  "chat.completion",
			"created": 1,
			"model":   "qwen",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "pong"}, "finish_reason": "stop"}},
		})
	}))
	defer srv.Close()

	r := probeLLM(context.Background(), newHTTPClient(), srv.URL, discardLogger())
	if r.Status != StatusHealthy {
		t.Errorf("got status %q want healthy; err=%q", r.Status, r.Error)
	}
	if r.LatencyMs < 0 {
		t.Errorf("negative latency %d", r.LatencyMs)
	}
}

func TestProbeLLM_NonOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := probeLLM(context.Background(), newHTTPClient(), srv.URL, discardLogger())
	if r.Status != StatusFailed {
		t.Errorf("got %q want failed", r.Status)
	}
	if !strings.Contains(r.Error, "status 500") {
		t.Errorf("err=%q, expected contains 'status 500'", r.Error)
	}
}

func TestProbeLLM_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than probeTimeout to force context deadline
		time.Sleep(probeTimeout + 500*time.Millisecond)
	}))
	defer srv.Close()

	r := probeLLM(context.Background(), newHTTPClient(), srv.URL, discardLogger())
	if r.Status != StatusFailed {
		t.Errorf("got %q want failed", r.Status)
	}
}

func TestProbeEmbed_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]any{{"object": "embedding", "index": 0, "embedding": []float32{0.1, 0.2}}},
			"model":  "BAAI/bge-m3",
		})
	}))
	defer srv.Close()

	r := probeEmbed(context.Background(), newHTTPClient(), srv.URL, discardLogger())
	if r.Status != StatusHealthy {
		t.Errorf("got %q want healthy; err=%q", r.Status, r.Error)
	}
}

func TestProbeEmbed_Malformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"garbage":true}`))
	}))
	defer srv.Close()

	r := probeEmbed(context.Background(), newHTTPClient(), srv.URL, discardLogger())
	if r.Status != StatusFailed {
		t.Errorf("got %q want failed", r.Status)
	}
	if !strings.Contains(r.Error, "no embeddings returned") {
		t.Errorf("err=%q, expected contains 'no embeddings returned'", r.Error)
	}
}

func TestState_ConcurrentSet(t *testing.T) {
	s := NewState()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			up := []string{UpstreamLLM, UpstreamSTT, UpstreamEmbed}[i%3]
			s.Set(up, ProbeResult{Status: StatusHealthy, LatencyMs: int64(i)})
		}(i)
	}
	wg.Wait()
	snap := s.Snapshot()
	if len(snap) != 3 {
		t.Errorf("expected 3 entries got %d", len(snap))
	}
}

func TestState_SnapshotIsolation(t *testing.T) {
	s := NewState()
	s.Set(UpstreamLLM, ProbeResult{Status: StatusHealthy})
	snap := s.Snapshot()
	snap[UpstreamLLM] = ProbeResult{Status: StatusFailed}
	got, _ := s.Get(UpstreamLLM)
	if got.Status != StatusHealthy {
		t.Errorf("snapshot mutation leaked; got=%q want healthy", got.Status)
	}
}

func TestClassifyLatency_Degraded(t *testing.T) {
	r := ClassifyLatency(ProbeResult{Status: StatusHealthy, LatencyMs: DegradationLatencyMs + 1})
	if r.Status != StatusDegraded {
		t.Errorf("got %q want degraded", r.Status)
	}
}

func TestAggregateStatus(t *testing.T) {
	s := NewState()
	s.Set(UpstreamLLM, ProbeResult{Status: StatusHealthy})
	s.Set(UpstreamSTT, ProbeResult{Status: StatusHealthy})
	s.Set(UpstreamEmbed, ProbeResult{Status: StatusHealthy})
	if s.AggregateStatus() != StatusHealthy {
		t.Errorf("all healthy aggregate want healthy")
	}
	s.Set(UpstreamSTT, ProbeResult{Status: StatusDegraded})
	if s.AggregateStatus() != StatusDegraded {
		t.Errorf("one degraded want degraded")
	}
	s.Set(UpstreamLLM, ProbeResult{Status: StatusFailed})
	if s.AggregateStatus() != StatusFailed {
		t.Errorf("one failed want failed")
	}
}
