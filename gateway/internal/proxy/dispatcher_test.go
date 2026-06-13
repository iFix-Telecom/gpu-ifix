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

// TestDispatcher_EmergencyOverride_503WhenProxyMissing locks the
// regression for tech debt #4 (Phase 06.6) / bug introduced by bda05fb.
//
// When loader.OverrideTier0(role, podURL) is active, Loader.Resolve
// returns the synthetic upstream Name="emergency_pod_<role>". If the
// dispatcher's Proxies map does not carry that key, dispatchTo falls
// through the !ok branch and emits 503 "Upstream proxy not registered".
//
// This test boots a fixture that intentionally omits emergency_pod_llm
// from Proxies, activates the override, and asserts the exact envelope.
// Companion to the happy-path test below; the pair guards against a
// silent refactor that drops the emergency_pod_<role> registration in
// cmd/gateway/main.go (the fix shipped in 30f90e7 + 12f7479).
func TestDispatcher_EmergencyOverride_503WhenProxyMissing(t *testing.T) {
	f := newDispatcherFixture(t, "llm")
	defer f.cleanup()

	// Activate the tier-0 override — Loader.Resolve now returns
	// Name="emergency_pod_llm". Fixture's Proxies map only contains
	// primary-llm + fallback-llm, so dispatchTo will 503.
	f.loader.OverrideTier0("llm", "http://fake-pod.invalid:8000")

	rw := httptest.NewRecorder()
	r := makeRequest(t, `{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`, auth.DataClassNormal)
	f.mux.ServeHTTP(rw, r)

	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "Upstream proxy not registered") {
		t.Errorf("body missing 'Upstream proxy not registered'; got: %s", rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "upstream_unavailable") {
		t.Errorf("body missing code 'upstream_unavailable'; got: %s", rw.Body.String())
	}
	// Neither tier-0 nor tier-1 backend should have been hit — the
	// dispatcher 503'd before any ServeHTTP forward.
	if atomic.LoadInt64(f.tier0Hits) != 0 {
		t.Errorf("tier-0 hits = %d, want 0", atomic.LoadInt64(f.tier0Hits))
	}
	if atomic.LoadInt64(f.tier1Hits) != 0 {
		t.Errorf("tier-1 hits = %d, want 0", atomic.LoadInt64(f.tier1Hits))
	}
}

// TestDispatcher_EmergencyOverride_200WhenProxyRegistered is the
// happy-path counterpart: with the override active AND an
// emergency_pod_llm proxy registered, the dispatcher routes traffic
// to it (the fix's intended behaviour, shipped in 30f90e7).
func TestDispatcher_EmergencyOverride_200WhenProxyRegistered(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	var emergHits int64
	emergSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&emergHits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"upstream":"emergency-pod"}`))
	}))
	defer emergSrv.Close()

	t0srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"upstream":"tier-0"}`))
	}))
	defer t0srv.Close()
	t1srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"upstream":"tier-1"}`))
	}))
	defer t1srv.Close()

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "primary-llm", Role: "llm", Tier: 0, URL: t0srv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "fallback-llm", Role: "llm", Tier: 1, URL: t1srv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names())

	cfg := DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"primary-llm":       newPassthroughProxy(t, t0srv.URL),
			"fallback-llm":      newPassthroughProxy(t, t1srv.URL),
			"emergency_pod_llm": newPassthroughProxy(t, emergSrv.URL),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	disp := NewDispatcher(cfg)

	loader.OverrideTier0("llm", emergSrv.URL)

	rw := httptest.NewRecorder()
	r := makeRequest(t, `{"model":"qwen","messages":[{"role":"user","content":"ping"}]}`, auth.DataClassNormal)
	disp.ServeHTTP(rw, r)

	if rw.Code != 200 {
		t.Errorf("status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(&emergHits) != 1 {
		t.Errorf("emergency-pod hits = %d, want 1", atomic.LoadInt64(&emergHits))
	}
	if !strings.Contains(rw.Body.String(), `"emergency-pod"`) {
		t.Errorf("body should come from emergency-pod backend; got: %s", rw.Body.String())
	}
}

// Compile-time assertion: ctx import unused if dispatcher tests above
// don't use context directly. Pin the import to silence unused-import.
var _ = context.Background

// ---------------------------------------------------------------------------
// Phase 11.2 Plan 01 — Wave 0 RED stubs for tier-1 cascade fallthrough (D-B5′).
// OWNER: Plan 06 (dispatcher.go :257-272 surgical replace per PATTERNS.md
// lines 181-208). Tests pin the iterate-`ResolveAllTier1` + breaker-check
// loop contract:
//   - gemini-stt OPEN, groq-whisper CLOSED, openai-whisper CLOSED → groq picks up (200)
//   - gemini-stt + groq OPEN, openai-whisper CLOSED → openai picks up (200)
//   - all 3 OPEN → 503 upstream_unavailable
// ---------------------------------------------------------------------------

// sttCascadeFixture wires a 4-upstream STT dispatcher: tier-0 (local-stt)
// always OPEN + 3 tier-1 candidates (gemini-stt prio=10, groq-whisper
// prio=15, openai-whisper prio=20). Each tier-1 has its own httptest
// backend so tests can assert which one received the request.
type sttCascadeFixture struct {
	loader     *upstreams.Loader
	breakerSet *breaker.Set
	mux        http.Handler
	localHits  *int64
	geminiHits *int64
	groqHits   *int64
	openaiHits *int64
	cleanup    func()
}

func newSTTCascadeFixture(t *testing.T) *sttCascadeFixture {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	var localHits, geminiHits, groqHits, openaiHits int64
	mk := func(counter *int64, label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt64(counter, 1)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"upstream":"` + label + `"}`))
		}))
	}
	localSrv := mk(&localHits, "local-stt")
	geminiSrv := mk(&geminiHits, "gemini-stt")
	groqSrv := mk(&groqHits, "groq-whisper")
	openaiSrv := mk(&openaiHits, "openai-whisper")

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-stt", Role: "stt", Tier: 0, TierPriority: 0, URL: localSrv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "gemini-stt", Role: "stt", Tier: 1, TierPriority: 10, URL: geminiSrv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "groq-whisper", Role: "stt", Tier: 1, TierPriority: 15, URL: groqSrv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "openai-whisper", Role: "stt", Tier: 1, TierPriority: 20, URL: openaiSrv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names())

	cfg := DispatcherConfig{
		Role:    "stt",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-stt":      newPassthroughProxy(t, localSrv.URL),
			"gemini-stt":     newPassthroughProxy(t, geminiSrv.URL),
			"groq-whisper":   newPassthroughProxy(t, groqSrv.URL),
			"openai-whisper": newPassthroughProxy(t, openaiSrv.URL),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	cleanup := func() {
		localSrv.Close()
		geminiSrv.Close()
		groqSrv.Close()
		openaiSrv.Close()
		_ = rdb.Close()
		mr.Close()
	}
	return &sttCascadeFixture{
		loader:     loader,
		breakerSet: bs,
		mux:        mux,
		localHits:  &localHits,
		geminiHits: &geminiHits,
		groqHits:   &groqHits,
		openaiHits: &openaiHits,
		cleanup:    cleanup,
	}
}

// makeSTTRequest builds an authenticated POST /v1/audio/transcriptions
// request (multipart body is opaque to the dispatcher — only the
// presence of the request matters).
func makeSTTRequest(t *testing.T) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions",
		strings.NewReader("not-actually-multipart"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	ctx := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "tenant-1",
		APIKeyID:  "key-1",
		DataClass: auth.DataClassNormal,
	})
	return r.WithContext(ctx)
}

// TestDispatcher_TierOneFallthrough_GeminiOpen_GroqClosed_Routes200 — D-B5′.
func TestDispatcher_TierOneFallthrough_GeminiOpen_GroqClosed_Routes200(t *testing.T) {
	f := newSTTCascadeFixture(t)
	defer f.cleanup()

	// Trip tier-0 + gemini-stt; leave groq-whisper + openai-whisper CLOSED.
	tripBreaker(t, f.breakerSet, "local-stt")
	tripBreaker(t, f.breakerSet, "gemini-stt")

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeSTTRequest(t))

	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.geminiHits) != 0 {
		t.Errorf("gemini-stt hits=%d, want 0 (breaker OPEN)", atomic.LoadInt64(f.geminiHits))
	}
	if atomic.LoadInt64(f.groqHits) != 1 {
		t.Errorf("groq-whisper hits=%d, want 1 (cascade fall-through)", atomic.LoadInt64(f.groqHits))
	}
	if atomic.LoadInt64(f.openaiHits) != 0 {
		t.Errorf("openai-whisper hits=%d, want 0 (groq picked up first)", atomic.LoadInt64(f.openaiHits))
	}
}

// TestDispatcher_TierOneFallthrough_GeminiAndGroqOpen_OpenAIWhisperClosed_Routes200 — D-B5′.
func TestDispatcher_TierOneFallthrough_GeminiAndGroqOpen_OpenAIWhisperClosed_Routes200(t *testing.T) {
	f := newSTTCascadeFixture(t)
	defer f.cleanup()

	tripBreaker(t, f.breakerSet, "local-stt")
	tripBreaker(t, f.breakerSet, "gemini-stt")
	tripBreaker(t, f.breakerSet, "groq-whisper")

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeSTTRequest(t))

	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.geminiHits) != 0 || atomic.LoadInt64(f.groqHits) != 0 {
		t.Errorf("gemini-stt + groq-whisper should be 0 (both OPEN); got gemini=%d groq=%d",
			atomic.LoadInt64(f.geminiHits), atomic.LoadInt64(f.groqHits))
	}
	if atomic.LoadInt64(f.openaiHits) != 1 {
		t.Errorf("openai-whisper hits=%d, want 1 (last-resort safety net)", atomic.LoadInt64(f.openaiHits))
	}
}

// TestDispatcher_TierOneFallthrough_AllOpen_Returns503 — D-B5′ + RES-08.
func TestDispatcher_TierOneFallthrough_AllOpen_Returns503(t *testing.T) {
	f := newSTTCascadeFixture(t)
	defer f.cleanup()

	tripBreaker(t, f.breakerSet, "local-stt")
	tripBreaker(t, f.breakerSet, "gemini-stt")
	tripBreaker(t, f.breakerSet, "groq-whisper")
	tripBreaker(t, f.breakerSet, "openai-whisper")

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeSTTRequest(t))

	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "upstream_unavailable") {
		t.Errorf("body missing upstream_unavailable: %s", rw.Body.String())
	}
	if atomic.LoadInt64(f.geminiHits)+atomic.LoadInt64(f.groqHits)+atomic.LoadInt64(f.openaiHits) != 0 {
		t.Errorf("expected 0 backend hits when all OPEN; got gemini=%d groq=%d openai=%d",
			atomic.LoadInt64(f.geminiHits), atomic.LoadInt64(f.groqHits), atomic.LoadInt64(f.openaiHits))
	}
}
