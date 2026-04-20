package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newCounterTestEnv builds the standard mock /tokenize server + miniredis
// pair used by all TokenCounter tests. tokenizeFn lets the caller decide
// what tokens to return per request body. hits counts how many times the
// /tokenize endpoint was actually invoked (cache effectiveness assertions).
func newCounterTestEnv(t *testing.T, tokenizeFn func([]byte) []int) (*TokenCounter, *miniredis.Miniredis, *int64, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokenize" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt64(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		toks := tokenizeFn(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": toks})
	}))
	tc := NewTokenCounter(rdb, srv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cleanup := func() {
		srv.Close()
		_ = rdb.Close()
		mr.Close()
	}
	return tc, mr, &hits, cleanup
}

// TestCounter_CacheHit verifies that two Enforce calls with the same
// (body, model) only hit /tokenize once — the second read comes from
// Redis. Cache key MUST include the model per Pitfall 6.
func TestCounter_CacheHit(t *testing.T) {
	tc, _, hits, cleanup := newCounterTestEnv(t, func(_ []byte) []int { return make([]int, 100) })
	defer cleanup()

	body := []byte(`{"messages":[{"role":"user","content":"ping"}]}`)
	if _, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap); err != nil {
		t.Fatalf("first call err: %v", err)
	}
	if _, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap); err != nil {
		t.Fatalf("second call err: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("/tokenize hits = %d, want 1 (cache miss expected on second call)", got)
	}
}

// TestCounter_CacheMissDifferentModel proves Pitfall 6 mitigation: the
// same body with two different models keys to two cache slots and
// invokes /tokenize twice. Without the model in the key the second
// model would silently inherit the first's count — a tokenizer
// mismatch could approve over-cap requests.
func TestCounter_CacheMissDifferentModel(t *testing.T) {
	tc, _, hits, cleanup := newCounterTestEnv(t, func(_ []byte) []int { return make([]int, 50) })
	defer cleanup()

	body := []byte(`{"messages":[{"role":"user","content":"ping"}]}`)
	if _, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap); err != nil {
		t.Fatalf("qwen call err: %v", err)
	}
	if _, err := tc.Enforce(context.Background(), body, "llama-3", ChatContextCap); err != nil {
		t.Fatalf("llama-3 call err: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != 2 {
		t.Errorf("/tokenize hits = %d, want 2 (different model = different cache key)", got)
	}
}

// TestCounter_OverCapReturnsContextLengthExceeded verifies the dispatcher
// gating signal: any count > cap returns ErrContextLengthExceeded so the
// dispatcher can map to HTTP 400 invalid_request_error / context_length_exceeded.
func TestCounter_OverCapReturnsContextLengthExceeded(t *testing.T) {
	tc, _, _, cleanup := newCounterTestEnv(t, func(_ []byte) []int {
		// Return 16385 tokens — one over ChatContextCap.
		return make([]int, ChatContextCap+1)
	})
	defer cleanup()

	body := []byte(`{"messages":[{"role":"user","content":"long..."}]}`)
	n, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap)
	if !errors.Is(err, ErrContextLengthExceeded) {
		t.Fatalf("err = %v, want ErrContextLengthExceeded", err)
	}
	if n != ChatContextCap+1 {
		t.Errorf("count = %d, want %d", n, ChatContextCap+1)
	}
}

// TestCounter_FailOpenOnTokenizeError verifies that /tokenize transport
// failures do NOT block requests. The dispatcher proceeds with count=0
// and the breaker on local-llm catches actual upstream outage.
func TestCounter_FailOpenOnTokenizeError(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Server that always returns 500 — simulates llama-server in a
	// transient bad state.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tc := NewTokenCounter(rdb, srv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := []byte(`{"messages":[{"role":"user","content":"ping"}]}`)
	n, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap)
	if err != nil {
		t.Fatalf("err = %v, want nil (fail-open)", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0 (fail-open)", n)
	}
}

// TestCounter_EmbedInputArrayConcatenated verifies that /v1/embeddings
// "input": [array] bodies are extracted into newline-joined text before
// /tokenize. Single-string "input" works via the same path.
func TestCounter_EmbedInputArrayConcatenated(t *testing.T) {
	var captured atomic.Pointer[string]
	tc, _, _, cleanup := newCounterTestEnv(t, func(b []byte) []int {
		var m map[string]string
		_ = json.Unmarshal(b, &m)
		s := m["content"]
		captured.Store(&s)
		return make([]int, 10)
	})
	defer cleanup()

	body := []byte(`{"input":["alpha","beta","gamma"],"model":"bge-m3"}`)
	if _, err := tc.Enforce(context.Background(), body, "bge-m3", EmbedContextCap); err != nil {
		t.Fatalf("err: %v", err)
	}
	got := captured.Load()
	if got == nil {
		t.Fatal("/tokenize never called")
	}
	if *got == "" || (*got != "alpha\nbeta\ngamma\n") {
		t.Errorf("captured content = %q, want \"alpha\\nbeta\\ngamma\\n\"", *got)
	}
}

// TestCounter_NilRedisOrEmptyURLFailsOpen guarantees the boot-time
// fail-open: tests that wire a TokenCounter with no /tokenize URL
// (config not yet loaded, etc.) get (0, nil) and proceed.
func TestCounter_NilRedisOrEmptyURLFailsOpen(t *testing.T) {
	tc := NewTokenCounter(nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := tc.Enforce(context.Background(), []byte(`{}`), "qwen", ChatContextCap)
	if err != nil || n != 0 {
		t.Fatalf("nil-redis: got (%d, %v), want (0, nil)", n, err)
	}
}
