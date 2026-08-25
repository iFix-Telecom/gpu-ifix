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
	if _, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, ""); err != nil {
		t.Fatalf("first call err: %v", err)
	}
	if _, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, ""); err != nil {
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
	if _, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, ""); err != nil {
		t.Fatalf("qwen call err: %v", err)
	}
	if _, err := tc.Enforce(context.Background(), body, "llama-3", ChatContextCap, ""); err != nil {
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
	n, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, "")
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
	n, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, "")
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
	if _, err := tc.Enforce(context.Background(), body, "bge-m3", EmbedContextCap, ""); err != nil {
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
	n, err := tc.Enforce(context.Background(), []byte(`{}`), "qwen", ChatContextCap, "")
	if err != nil || n != 0 {
		t.Fatalf("nil-redis: got (%d, %v), want (0, nil)", n, err)
	}
}

// ---------------------------------------------------------------------------
// quick 260824-ucv — Fix B: tokenize against the EFFECTIVE tier-0.
// The constructor URL (UPSTREAM_LLM_URL) pins the STATIC local-llm, which was
// dead all day while the dynamic pod served chat → every /tokenize dialed a
// closed port → fail-open → the RES-07 guard was inert.
// ---------------------------------------------------------------------------

// newTokenizeServer returns a /tokenize mock answering a fixed token count,
// plus its hit counter.
func newTokenizeServer(t *testing.T, tokens int) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokenize" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, tokens)})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestCounter_TokenizeURLOverridesConstructor: the per-request URL wins. This
// is the whole point of Fix B — the dispatcher passes the RESOLVED tier-0 URL
// (the live pod when OverrideTier0 is active) and the boot-time constructor URL
// is demoted to a fallback.
func TestCounter_TokenizeURLOverridesConstructor(t *testing.T) {
	staticSrv, staticHits := newTokenizeServer(t, 10)
	podSrv, podHits := newTokenizeServer(t, ChatContextCap+1)

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tc := NewTokenCounter(rdb, staticSrv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := []byte(`{"messages":[{"role":"user","content":"huge"}]}`)

	n, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, podSrv.URL)
	if !errors.Is(err, ErrContextLengthExceeded) {
		t.Fatalf("err = %v, want ErrContextLengthExceeded (pod tokenizer said over-cap)", err)
	}
	if n != ChatContextCap+1 {
		t.Errorf("count = %d, want %d", n, ChatContextCap+1)
	}
	if got := atomic.LoadInt64(podHits); got != 1 {
		t.Errorf("pod /tokenize hits = %d, want 1", got)
	}
	if got := atomic.LoadInt64(staticHits); got != 0 {
		t.Errorf("static /tokenize hits = %d, want 0 (constructor URL must not be consulted)", got)
	}
}

// TestCounter_EmptyTokenizeURLFallsBackToConstructor: with no resolved URL the
// boot-time constructor URL is still used (fallback preserved).
func TestCounter_EmptyTokenizeURLFallsBackToConstructor(t *testing.T) {
	tc, _, hits, cleanup := newCounterTestEnv(t, func(_ []byte) []int { return make([]int, 42) })
	defer cleanup()

	if _, err := tc.Enforce(context.Background(),
		[]byte(`{"messages":[{"role":"user","content":"ping"}]}`), "qwen", ChatContextCap, ""); err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := atomic.LoadInt64(hits); got != 1 {
		t.Errorf("constructor /tokenize hits = %d, want 1", got)
	}
}

// TestCounter_CacheKeySeparatesTokenizers: same body, same model, two DIFFERENT
// tokenizer endpoints → two cache slots. Sharing one slot would let a small
// count produced by the static local-llm silently approve a request that does
// not fit the pod (same class of bug as Pitfall 6, one level up).
func TestCounter_CacheKeySeparatesTokenizers(t *testing.T) {
	underSrv, underHits := newTokenizeServer(t, 10)
	overSrv, overHits := newTokenizeServer(t, ChatContextCap+1)

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tc := NewTokenCounter(rdb, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := []byte(`{"messages":[{"role":"user","content":"same bytes"}]}`)

	if n, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, underSrv.URL); err != nil || n != 10 {
		t.Fatalf("under-cap tokenizer: got (%d, %v), want (10, nil)", n, err)
	}
	n, err := tc.Enforce(context.Background(), body, "qwen", ChatContextCap, overSrv.URL)
	if !errors.Is(err, ErrContextLengthExceeded) {
		t.Fatalf("over-cap tokenizer: err = %v, want ErrContextLengthExceeded (cache must not be shared)", err)
	}
	if n != ChatContextCap+1 {
		t.Errorf("count = %d, want %d", n, ChatContextCap+1)
	}
	if got := atomic.LoadInt64(underHits); got != 1 {
		t.Errorf("under /tokenize hits = %d, want 1", got)
	}
	if got := atomic.LoadInt64(overHits); got != 1 {
		t.Errorf("over /tokenize hits = %d, want 1", got)
	}
}

// TestCounter_UnreachableTokenizeURLFailsOpen: the fail-open policy is
// UNCHANGED by Fix B — only the target of the tokenization moved. An
// unreachable resolved URL still yields (0, nil).
func TestCounter_UnreachableTokenizeURLFailsOpen(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tc := NewTokenCounter(rdb, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := tc.Enforce(context.Background(),
		[]byte(`{"messages":[{"role":"user","content":"ping"}]}`),
		"qwen", ChatContextCap, "http://127.0.0.1:1/dead")
	if err != nil || n != 0 {
		t.Fatalf("got (%d, %v), want (0, nil) fail-open", n, err)
	}
}
