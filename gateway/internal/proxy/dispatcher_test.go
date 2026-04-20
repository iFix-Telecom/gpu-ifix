package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// dispatcherFixture wires a 2-tier loader + breaker.Set + 2 mock backend
// proxies. The mocks count hits per tier so tests can assert the
// dispatcher chose the correct upstream.
type dispatcherFixture struct {
	loader     *upstreams.Loader
	breakerSet *breaker.Set
	tier0Hits  *int64
	tier1Hits  *int64
	mux        http.Handler
	cleanup    func()
}

func newDispatcherFixture(t *testing.T, role string) *dispatcherFixture {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	var t0Hits, t1Hits int64
	t0srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&t0Hits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"upstream":"tier-0"}`))
	}))
	t1srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&t1Hits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"upstream":"tier-1"}`))
	}))

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "primary-" + role, Role: role, Tier: 0, URL: t0srv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "fallback-" + role, Role: role, Tier: 1, URL: t1srv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names())

	// Construct simple proxies (just forward to the mock backends).
	t0Proxy := newPassthroughProxy(t, t0srv.URL)
	t1Proxy := newPassthroughProxy(t, t1srv.URL)

	cfg := DispatcherConfig{
		Role:    role,
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"primary-" + role:  t0Proxy,
			"fallback-" + role: t1Proxy,
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	cleanup := func() {
		t0srv.Close()
		t1srv.Close()
		_ = rdb.Close()
		mr.Close()
	}
	return &dispatcherFixture{
		loader:     loader,
		breakerSet: bs,
		tier0Hits:  &t0Hits,
		tier1Hits:  &t1Hits,
		mux:        mux,
		cleanup:    cleanup,
	}
}

// newPassthroughProxy is a minimal http.Handler that forwards to the
// given URL. Used in dispatcher tests where we don't need the full
// ReverseProxy machinery — just hit-counting + 200 OK.
func newPassthroughProxy(t *testing.T, target string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.Path, r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

// tripBreaker forces the named breaker into OPEN.
func tripBreaker(t *testing.T, bs *breaker.Set, name string) {
	t.Helper()
	for i := 0; i < 5; i++ {
		_, _ = bs.Execute(name, func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: 500, Msg: "boom"}
		})
		cb, _ := bs.Get(name)
		if cb != nil && cb.State() == gobreaker.StateOpen {
			return
		}
	}
	t.Fatal("failed to trip breaker after 5 attempts")
}

// makeRequest builds an authenticated request body+ctx ready for dispatch.
func makeRequest(t *testing.T, body string, dataClass auth.DataClass) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ctx := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "tenant-1",
		APIKeyID:  "key-1",
		DataClass: dataClass,
	})
	return r.WithContext(ctx)
}

// TestDispatcher_Tier0ClosedDispatchesPrimary: happy path — breaker
// CLOSED, request lands on tier-0.
func TestDispatcher_Tier0ClosedDispatchesPrimary(t *testing.T) {
	f := newDispatcherFixture(t, "llm")
	defer f.cleanup()

	rw := httptest.NewRecorder()
	r := makeRequest(t, `{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`, auth.DataClassNormal)
	f.mux.ServeHTTP(rw, r)

	if rw.Code != 200 {
		t.Errorf("status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.tier0Hits) != 1 {
		t.Errorf("tier-0 hits = %d, want 1", atomic.LoadInt64(f.tier0Hits))
	}
	if atomic.LoadInt64(f.tier1Hits) != 0 {
		t.Errorf("tier-1 hits = %d, want 0", atomic.LoadInt64(f.tier1Hits))
	}
}

// TestDispatcher_Tier0OpenNormalFallsBackToTier1: when tier-0 is OPEN,
// normal tenants get routed to tier-1 (D-A2 + D-C4 normal-tenant path).
func TestDispatcher_Tier0OpenNormalFallsBackToTier1(t *testing.T) {
	f := newDispatcherFixture(t, "llm")
	defer f.cleanup()

	tripBreaker(t, f.breakerSet, "primary-llm")

	rw := httptest.NewRecorder()
	r := makeRequest(t, `{"model":"qwen","messages":[]}`, auth.DataClassNormal)
	f.mux.ServeHTTP(rw, r)

	if rw.Code != 200 {
		t.Errorf("status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.tier0Hits) != 0 {
		t.Errorf("tier-0 hits = %d, want 0 (breaker OPEN)", atomic.LoadInt64(f.tier0Hits))
	}
	if atomic.LoadInt64(f.tier1Hits) != 1 {
		t.Errorf("tier-1 hits = %d, want 1 (fallback)", atomic.LoadInt64(f.tier1Hits))
	}
}

// TestDispatcher_Tier0OpenSensitiveStreamFailsFast: D-B4 — sensitive +
// streaming + tier-0 OPEN → 503 immediate, no retry, no external call.
func TestDispatcher_Tier0OpenSensitiveStreamFailsFast(t *testing.T) {
	f := newDispatcherFixture(t, "llm")
	defer f.cleanup()
	tripBreaker(t, f.breakerSet, "primary-llm")

	rw := httptest.NewRecorder()
	// stream:true triggers fail-fast pre-flight.
	r := makeRequest(t, `{"model":"qwen","stream":true,"messages":[]}`, auth.DataClassSensitive)

	start := time.Now()
	f.mux.ServeHTTP(rw, r)
	elapsed := time.Since(start)

	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rw.Code, rw.Body.String())
	}
	if rw.Header().Get("Retry-After") != "30" {
		t.Errorf("Retry-After = %q, want 30", rw.Header().Get("Retry-After"))
	}
	if !strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Errorf("body missing sensitive-block code: %s", rw.Body.String())
	}
	// MUST be fail-fast — well under 200ms first sleep.
	if elapsed > 100*time.Millisecond {
		t.Errorf("elapsed = %v, want < 100ms (fail-fast pre-flight)", elapsed)
	}
	// External tier-1 must NOT be called for sensitive.
	if atomic.LoadInt64(f.tier1Hits) != 0 {
		t.Errorf("tier-1 hits = %d, want 0 (sensitive blocked from external)",
			atomic.LoadInt64(f.tier1Hits))
	}
	// Audit override stamped via the request context (mutated in place).
	if got := auditctx.UpstreamOverrideFrom(r.Context()); got != UpstreamBlockedSensitiveValue {
		t.Errorf("audit override = %q, want %q", got, UpstreamBlockedSensitiveValue)
	}
}

// TestDispatcher_Tier0OpenSensitiveNonStreamRetriesAndBlocks: D-B1 —
// sensitive + non-streaming + tier-0 OPEN → SensitiveRetry (~4s) →
// 503 with audit stamp.
func TestDispatcher_Tier0OpenSensitiveNonStreamRetriesAndBlocks(t *testing.T) {
	f := newDispatcherFixture(t, "llm")
	defer f.cleanup()
	tripBreaker(t, f.breakerSet, "primary-llm")

	rw := httptest.NewRecorder()
	r := makeRequest(t, `{"model":"qwen","messages":[]}`, auth.DataClassSensitive)

	start := time.Now()
	f.mux.ServeHTTP(rw, r)
	elapsed := time.Since(start)

	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Errorf("body missing sensitive-block code: %s", rw.Body.String())
	}
	// Total budget ~4s plus dispatch overhead.
	if elapsed < 3900*time.Millisecond {
		t.Errorf("elapsed = %v, want >= 3.9s (full retry budget)", elapsed)
	}
	if atomic.LoadInt64(f.tier1Hits) != 0 {
		t.Errorf("tier-1 hits = %d, want 0 (sensitive never falls back to external)",
			atomic.LoadInt64(f.tier1Hits))
	}
}

// TestDispatcher_NoAuthContextReturnsUnauthorized covers the
// pre-condition: dispatcher only runs after auth.Middleware. If for
// some misconfiguration the auth context is missing, return 401
// rather than nil-deref.
func TestDispatcher_NoAuthContextReturnsUnauthorized(t *testing.T) {
	f := newDispatcherFixture(t, "llm")
	defer f.cleanup()

	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"qwen"}`))
	// no auth.WithContext call
	f.mux.ServeHTTP(rw, r)

	if rw.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rw.Code)
	}
}

// TestDispatcher_OverContextCapReturns400 verifies token-cap enforcement
// hooks correctly into the dispatcher: an over-cap body returns 400
// invalid_request_error/context_length_exceeded BEFORE breaker check.
func TestDispatcher_OverContextCapReturns400(t *testing.T) {
	f := newDispatcherFixture(t, "llm")
	defer f.cleanup()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	// Mock /tokenize that returns 16385 tokens (over cap).
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Build response inline — 16385 tokens.
		out := bytes.NewBufferString(`{"tokens":[`)
		for i := 0; i < ChatContextCap+1; i++ {
			if i > 0 {
				out.WriteByte(',')
			}
			out.WriteByte('1')
		}
		out.WriteString(`]}`)
		_, _ = w.Write(out.Bytes())
	}))
	defer tokenizeSrv.Close()

	tc := NewTokenCounter(rdb, tokenizeSrv.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := DispatcherConfig{
		Role:         "llm",
		Loader:       f.loader,
		Breaker:      f.breakerSet,
		TokenCounter: tc,
		ContextCap:   ChatContextCap,
		Proxies: map[string]http.Handler{
			"primary-llm":  newPassthroughProxy(t, "http://localhost:0"),
			"fallback-llm": newPassthroughProxy(t, "http://localhost:0"),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	disp := NewDispatcher(cfg)

	rw := httptest.NewRecorder()
	r := makeRequest(t, `{"model":"qwen","messages":[{"role":"user","content":"long..."}]}`,
		auth.DataClassNormal)
	disp.ServeHTTP(rw, r)

	if rw.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "context_length_exceeded") {
		t.Errorf("body missing context_length_exceeded: %s", rw.Body.String())
	}
}

// Compile-time assertion: ctx import unused if dispatcher tests above
// don't use context directly. Pin the import to silence unused-import.
var _ = context.Background
