package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// recordingResponseWriter counts every WriteHeader / Write call so tests can
// assert ZERO writes before the tier-1 dispatch (Pitfall 2).
type recordingResponseWriter struct {
	header       http.Header
	wroteHeader  int32
	wroteBody    int32
	status       int
	body         bytes.Buffer
}

func newRecordingRW() *recordingResponseWriter {
	return &recordingResponseWriter{header: make(http.Header)}
}

func (rw *recordingResponseWriter) Header() http.Header { return rw.header }

func (rw *recordingResponseWriter) WriteHeader(status int) {
	atomic.AddInt32(&rw.wroteHeader, 1)
	rw.status = status
}

func (rw *recordingResponseWriter) Write(b []byte) (int, error) {
	atomic.AddInt32(&rw.wroteBody, 1)
	return rw.body.Write(b)
}

func (rw *recordingResponseWriter) headerCalls() int32 { return atomic.LoadInt32(&rw.wroteHeader) }
func (rw *recordingResponseWriter) bodyCalls() int32   { return atomic.LoadInt32(&rw.wroteBody) }

// newFallthroughProxy builds a real *httputil.ReverseProxy wired with the
// fallthroughRoundTripper + sentinel-aware ErrorHandler — the production
// wiring — pointing at target. A target whose host is a closed port yields a
// pre-byte dial failure → sentinel → fallthrough.
func newFallthroughProxy(t *testing.T, target string, upstreamName string) http.Handler {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target %q: %v", target, err)
	}
	rp := &httputil.ReverseProxy{
		Director: BuildDirector(u),
		Transport: fallthroughRoundTripper{base: &http.Transport{
			ResponseHeaderTimeout: 2 * time.Second,
		}},
		ErrorHandler: ErrorHandler(upstreamName, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
	return rp
}

// fallthroughFixture wires a tier-0 that dial-fails (closed port) and N
// tier-1 candidates, each a real fallthrough proxy. Tests control which
// tier-1 dial-fails by pointing it at a closed port.
type fallthroughFixture struct {
	loader     *upstreams.Loader
	breakerSet *breaker.Set
	mux        http.Handler
	t1aHits    *int64
	t1bHits    *int64
	cleanup    func()
}

// closedPortURL returns a URL whose port is guaranteed dead (a freshly
// closed httptest server).
func closedPortURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()
	return addr
}

func newFallthroughFixture(t *testing.T, role string, t1bDead bool, sensitive bool) *fallthroughFixture {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	var t1aHits, t1bHits int64
	// tier-0 always points at a dead port (dial fails).
	t0Dead := closedPortURL(t)

	// tier-1 candidate A: live unless we want a cascade.
	t1aSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&t1aHits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"upstream":"t1a"}`))
	}))
	t1bSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&t1bHits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"upstream":"t1b"}`))
	}))

	t1aURL := t1aSrv.URL
	if t1bDead {
		// Make t1a (the first/higher-priority candidate) dead so the cascade
		// has to advance to t1b.
		t1aSrv.Close()
		t1aURL = closedPortURL(t)
	}

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-" + role, Role: role, Tier: 0, TierPriority: 0, URL: t0Dead, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb1-" + role, Role: role, Tier: 1, TierPriority: 10, URL: t1aURL, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb2-" + role, Role: role, Tier: 1, TierPriority: 20, URL: t1bSrv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second},
		loader.Names())

	cfg := DispatcherConfig{
		Role:    role,
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-" + role: newFallthroughProxy(t, t0Dead, "local-"+role),
			"fb1-" + role:   newFallthroughProxy(t, t1aURL, "fb1-"+role),
			"fb2-" + role:   newFallthroughProxy(t, t1bSrv.URL, "fb2-"+role),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	cleanup := func() {
		if !t1bDead {
			t1aSrv.Close()
		}
		t1bSrv.Close()
		_ = rdb.Close()
		mr.Close()
	}
	return &fallthroughFixture{
		loader:     loader,
		breakerSet: bs,
		mux:        mux,
		t1aHits:    &t1aHits,
		t1bHits:    &t1bHits,
		cleanup:    cleanup,
	}
}

func makeNormalReq(t *testing.T, body string, sensitive bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	dc := auth.DataClassNormal
	if sensitive {
		dc = auth.DataClassSensitive
	}
	ctx := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID: "tenant-1", APIKeyID: "key-1", DataClass: dc,
	})
	return r.WithContext(ctx)
}

// TestErrorHandler_SuppressesSentinelNoWrite — the load-bearing Pitfall 2
// test. The sentinel-aware ErrorHandler invoked with errDialFailedFallthrough
// writes ZERO bytes and records fallthrough=true, wrote=false on the
// request-scoped dispatchResult.
func TestErrorHandler_SuppressesSentinelNoWrite(t *testing.T) {
	rw := newRecordingRW()
	res := &dispatchResult{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r = r.WithContext(withDispatchResult(r.Context(), res))

	h := ErrorHandler("local-llm", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h(rw, r, errDialFailedFallthrough)

	if rw.headerCalls() != 0 || rw.bodyCalls() != 0 {
		t.Fatalf("sentinel must suppress all writes; got WriteHeader=%d Write=%d",
			rw.headerCalls(), rw.bodyCalls())
	}
	if !res.fallthrough_ || res.wrote {
		t.Fatalf("dispatchResult should record fallthrough=true wrote=false; got %+v", res)
	}
}

// TestErrorHandler_PreservesNonSentinel502 — a non-sentinel error keeps the
// existing 502 write and records wrote=true.
func TestErrorHandler_PreservesNonSentinel502(t *testing.T) {
	rw := newRecordingRW()
	res := &dispatchResult{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r = r.WithContext(withDispatchResult(r.Context(), res))

	h := ErrorHandler("local-llm", slog.New(slog.NewTextHandler(io.Discard, nil)))
	h(rw, r, errors.New("some generic upstream error"))

	if rw.status != http.StatusBadGateway {
		t.Fatalf("non-sentinel error should write 502; got %d", rw.status)
	}
	if !strings.Contains(rw.body.String(), "upstream_unreachable") {
		t.Fatalf("non-sentinel error should write upstream_unreachable envelope; got %s", rw.body.String())
	}
	if res.fallthrough_ || !res.wrote {
		t.Fatalf("dispatchResult should record fallthrough=false wrote=true; got %+v", res)
	}
}

// TestDispatcher_NoWriteBeforeTier1Dispatch — a tier-0 dial failure for a
// normal tenant produces ZERO writes BEFORE the tier-1 dispatch is entered.
// We assert the final response is the tier-1 200 and that the recorder never
// saw a 502 written first (no double-write).
func TestDispatcher_NoWriteBeforeTier1Dispatch(t *testing.T) {
	f := newFallthroughFixture(t, "llm", false, false)
	defer f.cleanup()

	rw := newRecordingRW()
	f.mux.ServeHTTP(rw, makeNormalReq(t, `{"model":"qwen","messages":[]}`, false))

	if rw.status != 200 {
		t.Fatalf("status=%d want 200 (tier-1 served); body=%s", rw.status, rw.body.String())
	}
	if strings.Contains(rw.body.String(), "upstream_unreachable") {
		t.Fatalf("client must NOT see a 502 before tier-1 dispatch; body=%s", rw.body.String())
	}
}

// TestDispatcher_DialFailureFallsThrough — normal tenant, tier-0 dial-fails →
// tier-1 200 (not 502); DialFallthroughTotal{tier1_served} incremented;
// local breaker records a failure (D-09).
func TestDispatcher_DialFailureFallsThrough(t *testing.T) {
	f := newFallthroughFixture(t, "llm", false, false)
	defer f.cleanup()

	before := counterVal(t, "llm", "tier1_served")

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeNormalReq(t, `{"model":"qwen","messages":[]}`, false))

	if rw.Code != 200 {
		t.Fatalf("status=%d want 200; body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.t1aHits) != 1 {
		t.Fatalf("tier-1a hits=%d want 1 (fallthrough)", atomic.LoadInt64(f.t1aHits))
	}
	after := counterVal(t, "llm", "tier1_served")
	if after != before+1 {
		t.Fatalf("DialFallthroughTotal{tier1_served} = %v, want +1", after-before)
	}
	// D-09: a tier-0 dial failure records a failure on the local-llm breaker.
	// With ConsecutiveFailures=1 we'd open immediately; the fixture uses 5,
	// but a single recorded failure must move ConsecutiveFailures>0. We assert
	// via Counts() exposed through the breaker Get.
	cb, ok := f.breakerSet.Get("local-llm")
	if !ok || cb == nil {
		t.Fatalf("local-llm breaker missing")
	}
	if cb.Counts().ConsecutiveFailures == 0 {
		t.Fatalf("D-09: tier-0 dial failure must record a breaker failure; ConsecutiveFailures=0")
	}
}

// TestDispatcher_CascadeOnDialFailure — tier-0 AND first tier-1 dial-fail →
// second CLOSED candidate served.
func TestDispatcher_CascadeOnDialFailure(t *testing.T) {
	f := newFallthroughFixture(t, "llm", true /* t1a dead */, false)
	defer f.cleanup()

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeNormalReq(t, `{"model":"qwen","messages":[]}`, false))

	if rw.Code != 200 {
		t.Fatalf("status=%d want 200 (cascade to fb2); body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.t1bHits) != 1 {
		t.Fatalf("fb2 hits=%d want 1 (cascade advanced)", atomic.LoadInt64(f.t1bHits))
	}
}

// TestDispatcher_CascadeExhausted_502 — tier-0 + ALL tier-1 dial-fail → 503/502
// exhaustion envelope written exactly once; outcome=chain_exhausted.
func TestDispatcher_CascadeExhausted_502(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	t0 := closedPortURL(t)
	t1 := closedPortURL(t)
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-llm", Role: "llm", Tier: 0, URL: t0, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb1-llm", Role: "llm", Tier: 1, TierPriority: 10, URL: t1, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	cfg := DispatcherConfig{
		Role: "llm", Loader: loader, Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm": newFallthroughProxy(t, t0, "local-llm"),
			"fb1-llm":   newFallthroughProxy(t, t1, "fb1-llm"),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	before := counterVal(t, "llm", "chain_exhausted")
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, makeNormalReq(t, `{"model":"qwen","messages":[]}`, false))

	if rw.Code != http.StatusServiceUnavailable && rw.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want 503/502 on exhaustion; body=%s", rw.Code, rw.Body.String())
	}
	after := counterVal(t, "llm", "chain_exhausted")
	if after != before+1 {
		t.Fatalf("DialFallthroughTotal{chain_exhausted} = %v, want +1", after-before)
	}
}

// TestDispatcher_SensitiveNeverFallsThrough — HARD GATE. Sensitive tenant,
// tier-0 dial-fails → 503 sensitive block, NEVER a tier-1 dispatch;
// outcome=sensitive_blocked.
func TestDispatcher_SensitiveNeverFallsThrough(t *testing.T) {
	f := newFallthroughFixture(t, "llm", false, true)
	defer f.cleanup()

	before := counterVal(t, "llm", "sensitive_blocked")
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeNormalReq(t, `{"model":"qwen","messages":[]}`, true /* sensitive */))

	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Fatalf("sensitive block envelope missing; body=%s", rw.Body.String())
	}
	if atomic.LoadInt64(f.t1aHits) != 0 || atomic.LoadInt64(f.t1bHits) != 0 {
		t.Fatalf("sensitive tenant must NEVER hit tier-1; t1a=%d t1b=%d",
			atomic.LoadInt64(f.t1aHits), atomic.LoadInt64(f.t1bHits))
	}
	after := counterVal(t, "llm", "sensitive_blocked")
	if after != before+1 {
		t.Fatalf("DialFallthroughTotal{sensitive_blocked} = %v, want +1", after-before)
	}
}

// TestDispatcher_StreamingFallsThroughPreByte — an SSE request whose tier-0
// dials fail pre-byte falls through to tier-1.
func TestDispatcher_StreamingFallsThroughPreByte(t *testing.T) {
	f := newFallthroughFixture(t, "llm", false, false)
	defer f.cleanup()

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeNormalReq(t, `{"model":"qwen","stream":true,"messages":[]}`, false))

	if rw.Code != 200 {
		t.Fatalf("streaming pre-byte dial failure should fall through to tier-1; status=%d body=%s",
			rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.t1aHits) != 1 {
		t.Fatalf("tier-1a hits=%d want 1 (streaming fallthrough)", atomic.LoadInt64(f.t1aHits))
	}
}

// TestDispatcher_ResponseTimeoutDoesNotFallThrough — a tier-0 that CONNECTS
// then never responds (response-header timeout) does NOT fall through; the
// normal 502 path is taken (no tier-1 hit).
func TestDispatcher_ResponseTimeoutDoesNotFallThrough(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// tier-0 connects but hangs forever (no response header) → response-header
	// timeout, which is POST-dial and must NOT classify as connection-class.
	block := make(chan struct{})
	t0srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block // never responds within the ResponseHeaderTimeout
	}))
	defer t0srv.Close()
	defer close(block)

	var t1Hits int64
	t1srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&t1Hits, 1)
		w.WriteHeader(200)
	}))
	defer t1srv.Close()

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-llm", Role: "llm", Tier: 0, URL: t0srv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb1-llm", Role: "llm", Tier: 1, TierPriority: 10, URL: t1srv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())

	// Build a proxy whose Transport has a SHORT ResponseHeaderTimeout.
	mkProxy := func(target string) http.Handler {
		u, _ := url.Parse(target)
		return &httputil.ReverseProxy{
			Director: BuildDirector(u),
			Transport: fallthroughRoundTripper{base: &http.Transport{
				ResponseHeaderTimeout: 200 * time.Millisecond,
			}},
			ErrorHandler: ErrorHandler("local-llm", slog.New(slog.NewTextHandler(io.Discard, nil))),
		}
	}
	cfg := DispatcherConfig{
		Role: "llm", Loader: loader, Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm": mkProxy(t0srv.URL),
			"fb1-llm":   mkProxy(t1srv.URL),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, makeNormalReq(t, `{"model":"qwen","messages":[]}`, false))

	if atomic.LoadInt64(&t1Hits) != 0 {
		t.Fatalf("response-timeout must NOT fall through to tier-1; t1 hits=%d", atomic.LoadInt64(&t1Hits))
	}
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("response-timeout should write the normal 502; status=%d body=%s", rw.Code, rw.Body.String())
	}
}

// TestDispatcher_BodyReplayedAcrossCascade — a non-streaming request with a
// non-empty body whose tier-0 AND first tier-1 dial-fail → the SECOND tier-1
// candidate receives the SAME body bytes.
func TestDispatcher_BodyReplayedAcrossCascade(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const wantBody = `{"model":"qwen","messages":[{"role":"user","content":"replay-me"}]}`
	var gotBody atomic.Value

	t0 := closedPortURL(t)
	t1a := closedPortURL(t)
	t1bSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		w.WriteHeader(200)
	}))
	defer t1bSrv.Close()

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-llm", Role: "llm", Tier: 0, URL: t0, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb1-llm", Role: "llm", Tier: 1, TierPriority: 10, URL: t1a, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb2-llm", Role: "llm", Tier: 1, TierPriority: 20, URL: t1bSrv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	cfg := DispatcherConfig{
		Role: "llm", Loader: loader, Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm": newFallthroughProxy(t, t0, "local-llm"),
			"fb1-llm":   newFallthroughProxy(t, t1a, "fb1-llm"),
			"fb2-llm":   newFallthroughProxy(t, t1bSrv.URL, "fb2-llm"),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, makeNormalReq(t, wantBody, false))

	if rw.Code != 200 {
		t.Fatalf("status=%d want 200 (cascade to fb2); body=%s", rw.Code, rw.Body.String())
	}
	got, _ := gotBody.Load().(string)
	if got != wantBody {
		t.Fatalf("fb2 received body=%q, want %q (replay across cascade)", got, wantBody)
	}
}

// TestDispatcher_GetBodyNilBuffered — a request that arrives with GetBody==nil
// but a non-nil Body still replays across the cascade (dispatcher buffers and
// sets GetBody before the first dispatch).
func TestDispatcher_GetBodyNilBuffered(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	const wantBody = `{"model":"qwen","messages":[{"role":"user","content":"nil-getbody"}]}`
	var gotBody atomic.Value

	t0 := closedPortURL(t)
	t1a := closedPortURL(t)
	t1bSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		w.WriteHeader(200)
	}))
	defer t1bSrv.Close()

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-llm", Role: "llm", Tier: 0, URL: t0, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb1-llm", Role: "llm", Tier: 1, TierPriority: 10, URL: t1a, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb2-llm", Role: "llm", Tier: 1, TierPriority: 20, URL: t1bSrv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	cfg := DispatcherConfig{
		Role: "llm", Loader: loader, Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm": newFallthroughProxy(t, t0, "local-llm"),
			"fb1-llm":   newFallthroughProxy(t, t1a, "fb1-llm"),
			"fb2-llm":   newFallthroughProxy(t, t1bSrv.URL, "fb2-llm"),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	r := makeNormalReq(t, wantBody, false)
	// Simulate a body with no GetBody (streamed in without one).
	r.GetBody = nil
	r.Body = io.NopCloser(strings.NewReader(wantBody))

	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, r)

	if rw.Code != 200 {
		t.Fatalf("status=%d want 200 (GetBody==nil buffered + replayed); body=%s", rw.Code, rw.Body.String())
	}
	got, _ := gotBody.Load().(string)
	if got != wantBody {
		t.Fatalf("fb2 received body=%q, want %q (buffered replay)", got, wantBody)
	}
}

// TestDispatcher_STTOverCapSkipsFallthrough — a multipart STT body exceeding
// maxSTTBodyBuffer is NOT buffered and does NOT fall through; on a tier-0 dial
// failure the normal 502/503 path is taken (no tier-1 hit).
func TestDispatcher_STTOverCapSkipsFallthrough(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	t0 := closedPortURL(t)
	var t1Hits int64
	t1srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&t1Hits, 1)
		w.WriteHeader(200)
	}))
	defer t1srv.Close()

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-stt", Role: "stt", Tier: 0, URL: t0, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb1-stt", Role: "stt", Tier: 1, TierPriority: 10, URL: t1srv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	cfg := DispatcherConfig{
		Role: "stt", Loader: loader, Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-stt": newFallthroughProxy(t, t0, "local-stt"),
			"fb1-stt":   newFallthroughProxy(t, t1srv.URL, "fb1-stt"),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	// Build an over-cap body via Content-Length larger than maxSTTBodyBuffer.
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions",
		bytes.NewReader([]byte("x")))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	r.ContentLength = maxSTTBodyBuffer + 1
	ctx := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID: "tenant-1", APIKeyID: "key-1", DataClass: auth.DataClassNormal,
	})
	r = r.WithContext(ctx)

	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, r)

	if atomic.LoadInt64(&t1Hits) != 0 {
		t.Fatalf("over-cap STT body must NOT fall through; t1 hits=%d", atomic.LoadInt64(&t1Hits))
	}
	if rw.Code != http.StatusBadGateway && rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap STT dial failure should take normal 502/503; status=%d body=%s",
			rw.Code, rw.Body.String())
	}
}

// counterVal reads the current value of DialFallthroughTotal{role,outcome}.
func counterVal(t *testing.T, role, outcome string) float64 {
	t.Helper()
	m := &dto.Metric{}
	c, err := obs.DialFallthroughTotal.GetMetricWithLabelValues(role, outcome)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	if err := c.Write(m); err != nil {
		t.Fatalf("metric write: %v", err)
	}
	return m.GetCounter().GetValue()
}

// pin imports used only in some build configs.
var _ = context.Background
var _ = gobreaker.StateClosed
