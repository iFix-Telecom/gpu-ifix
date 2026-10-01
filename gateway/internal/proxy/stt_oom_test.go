package proxy

import (
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
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// Quick 261001-fjk (card 86akreh6u): speaches on the 3060 pod answers HTTP 500
// "RuntimeError: CUDA failed with error out of memory" for long audio. The
// cascade must keep working (decisão Pedro 2026-08-27), but the OOM must NOT
// push the local-stt breaker toward OPEN, and the fallthrough error must carry
// the upstream status + a body excerpt so the log says WHY it cascaded.

const speachesOOMBody = `{"detail":"RuntimeError: CUDA failed with error out of memory\nTraceback (most recent call last): ..."}`

func respWithBody(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func TestSTTInterceptor_OOMIsRetryableAndResourceExhausted(t *testing.T) {
	err := sttRetryableStatusInterceptor{}.Intercept(respWithBody(500, speachesOOMBody))
	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("OOM 500 must stay retryable (cascade unchanged); got %v", err)
	}
	if !errors.Is(err, errSTTResourceExhausted) {
		t.Fatalf("OOM 500 must classify as resource exhausted; got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") || !strings.Contains(strings.ToLower(msg), "out of memory") {
		t.Fatalf("error message must carry status + body excerpt; got %q", msg)
	}
	if strings.Contains(msg, "\n") {
		t.Fatalf("body excerpt must be single-line; got %q", msg)
	}
}

func TestSTTInterceptor_OOMCaseInsensitive(t *testing.T) {
	err := sttRetryableStatusInterceptor{}.Intercept(respWithBody(500, `CUDA OUT OF MEMORY`))
	if !errors.Is(err, errSTTResourceExhausted) {
		t.Fatalf("case-insensitive OOM match failed; got %v", err)
	}
}

func TestSTTInterceptor_Generic500NotResourceExhausted(t *testing.T) {
	err := sttRetryableStatusInterceptor{}.Intercept(respWithBody(500, `{"detail":"internal error"}`))
	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("500 must be retryable; got %v", err)
	}
	if errors.Is(err, errSTTResourceExhausted) {
		t.Fatalf("generic 500 must NOT classify as resource exhausted")
	}
}

func TestSTTInterceptor_400MessageCarriesStatusAndBody(t *testing.T) {
	err := sttRetryableStatusInterceptor{}.Intercept(respWithBody(400, `{"detail":"OSError: [Errno 28] No space left on device"}`))
	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("400 must be retryable; got %v", err)
	}
	if errors.Is(err, errSTTResourceExhausted) {
		t.Fatalf("disk-full 400 is not a GPU OOM")
	}
	msg := err.Error()
	if !strings.Contains(msg, "400") || !strings.Contains(msg, "No space left on device") {
		t.Fatalf("message must carry status + body; got %q", msg)
	}
	// The composed log line must still show the sentinel text.
	wrapped := ComposeInterceptors(sttRetryableStatusInterceptor{})(respWithBody(400, `boom`))
	if !errors.Is(wrapped, errUpstreamRetryable) || !strings.Contains(wrapped.Error(), "boom") {
		t.Fatalf("composed error lost status/body or sentinel: %v", wrapped)
	}
}

func TestSTTInterceptor_ExcerptTruncated(t *testing.T) {
	long := strings.Repeat("x", 5000)
	err := sttRetryableStatusInterceptor{}.Intercept(respWithBody(502, long))
	var se *sttUpstreamStatusError
	if !errors.As(err, &se) {
		t.Fatalf("want *sttUpstreamStatusError, got %T", err)
	}
	if len(se.excerpt) > sttErrorExcerptMax {
		t.Fatalf("excerpt len=%d > %d", len(se.excerpt), sttErrorExcerptMax)
	}
	if se.status != 502 {
		t.Fatalf("status=%d want 502", se.status)
	}
}

func TestSTTInterceptor_BodyRestored(t *testing.T) {
	big := speachesOOMBody + strings.Repeat("y", 10000) // > 2 KiB peek
	resp := respWithBody(500, big)
	if err := (sttRetryableStatusInterceptor{}).Intercept(resp); err == nil {
		t.Fatalf("want error")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(got) != big {
		t.Fatalf("restored body differs: len=%d want %d", len(got), len(big))
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestSTTInterceptor_NilBodyAndSuccess(t *testing.T) {
	ic := sttRetryableStatusInterceptor{}
	if err := ic.Intercept(&http.Response{StatusCode: 503}); !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("nil body 503: want retryable, got %v", err)
	}
	if err := ic.Intercept(respWithBody(200, speachesOOMBody)); err != nil {
		t.Fatalf("200 must never error even if body mentions OOM; got %v", err)
	}
	if err := ic.Intercept(nil); err != nil {
		t.Fatalf("nil resp: want nil, got %v", err)
	}
}

// --- dispatcher-level ---

// newSTTStatusProxy is the production STT wiring in miniature: fallthrough
// transport + sentinel-aware ErrorHandler + the STT status interceptor.
func newSTTStatusProxy(t *testing.T, target, name string) http.Handler {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse %q: %v", target, err)
	}
	return &httputil.ReverseProxy{
		Director: BuildDirector(u),
		Transport: fallthroughRoundTripper{base: &http.Transport{
			ResponseHeaderTimeout: 2 * time.Second,
		}},
		ModifyResponse: ComposeInterceptors(sttRetryableStatusInterceptor{}),
		ErrorHandler:   ErrorHandler(name, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}
}

type sttOOMFixture struct {
	mux     http.Handler
	bs      *breaker.Set
	t0Hits  *int64
	fbHits  *int64
	cleanup func()
}

// newSTTOOMFixture: tier-0 local-stt is LIVE and answers status/body; tier-1
// openai-whisper is healthy.
func newSTTOOMFixture(t *testing.T, t0Status int, t0Body string) *sttOOMFixture {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	var t0Hits, fbHits int64
	t0srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&t0Hits, 1)
		w.WriteHeader(t0Status)
		_, _ = w.Write([]byte(t0Body))
	}))
	fbsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&fbHits, 1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"text":"served by openai-whisper"}`))
	}))
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-stt", Role: "stt", Tier: 0, URL: t0srv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "openai-whisper", Role: "stt", Tier: 1, TierPriority: 20, URL: fbsrv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	mux := NewDispatcher(DispatcherConfig{
		Role: "stt", Loader: loader, Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-stt":      newSTTStatusProxy(t, t0srv.URL, "local-stt"),
			"openai-whisper": newSTTStatusProxy(t, fbsrv.URL, "openai-whisper"),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return &sttOOMFixture{
		mux: mux, bs: bs, t0Hits: &t0Hits, fbHits: &fbHits,
		cleanup: func() {
			t0srv.Close()
			fbsrv.Close()
			_ = rdb.Close()
			mr.Close()
		},
	}
}

func makeSTTReq(t *testing.T, sensitive bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("multipart-audio-bytes"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	dc := auth.DataClassNormal
	if sensitive {
		dc = auth.DataClassSensitive
	}
	return r.WithContext(auth.WithContext(r.Context(), auth.AuthContext{
		TenantID: "tenant-1", APIKeyID: "key-1", DataClass: dc,
	}))
}

func consecutiveFailures(t *testing.T, bs *breaker.Set, name string) uint32 {
	t.Helper()
	cb, ok := bs.Get(name)
	if !ok || cb == nil {
		t.Fatalf("breaker %q missing", name)
	}
	return cb.Counts().ConsecutiveFailures
}

func TestDispatcher_STTOOMCascadesWithoutBreakerPenalty(t *testing.T) {
	f := newSTTOOMFixture(t, 500, speachesOOMBody)
	defer f.cleanup()
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeSTTReq(t, false))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "served by openai-whisper") {
		t.Fatalf("OOM must cascade to tier-1; status=%d body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.t0Hits) != 1 || atomic.LoadInt64(f.fbHits) != 1 {
		t.Fatalf("hits t0=%d fb=%d want 1/1", atomic.LoadInt64(f.t0Hits), atomic.LoadInt64(f.fbHits))
	}
	if n := consecutiveFailures(t, f.bs, "local-stt"); n != 0 {
		t.Fatalf("OOM must NOT record a local-stt breaker failure; ConsecutiveFailures=%d", n)
	}
}

func TestDispatcher_STTGeneric500StillPenalizesBreaker(t *testing.T) {
	f := newSTTOOMFixture(t, 500, `{"detail":"internal error"}`)
	defer f.cleanup()
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeSTTReq(t, false))
	if rw.Code != 200 {
		t.Fatalf("generic 500 must cascade; status=%d", rw.Code)
	}
	if n := consecutiveFailures(t, f.bs, "local-stt"); n == 0 {
		t.Fatalf("generic 500 must record a local-stt breaker failure; ConsecutiveFailures=0")
	}
}

func TestDispatcher_STTOOMSensitiveStillBlocked(t *testing.T) {
	f := newSTTOOMFixture(t, 500, speachesOOMBody)
	defer f.cleanup()
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeSTTReq(t, true))
	if rw.Code != http.StatusServiceUnavailable ||
		!strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Fatalf("RES-08: sensitive + OOM must 503-block; status=%d body=%s", rw.Code, rw.Body.String())
	}
	if atomic.LoadInt64(f.fbHits) != 0 {
		t.Fatalf("sensitive tenant must never reach tier-1; fb hits=%d", atomic.LoadInt64(f.fbHits))
	}
}

// Tier-1 cascade: an OOM from a NON-final tier-1 candidate also cascades
// without a breaker penalty on that candidate.
func TestDispatcher_STTOOMInTier1CascadeNoPenalty(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	t0 := closedPortURL(t)
	oomSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(speachesOOMBody))
	}))
	defer oomSrv.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer okSrv.Close()
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-stt", Role: "stt", Tier: 0, URL: t0, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb-oom", Role: "stt", Tier: 1, TierPriority: 10, URL: oomSrv.URL, Enabled: true},
		upstreams.UpstreamConfig{Name: "fb-ok", Role: "stt", Tier: 1, TierPriority: 20, URL: okSrv.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 5, Cooldown: 30 * time.Second}, loader.Names())
	mux := NewDispatcher(DispatcherConfig{
		Role: "stt", Loader: loader, Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-stt": newFallthroughProxy(t, t0, "local-stt"),
			"fb-oom":    newSTTStatusProxy(t, oomSrv.URL, "fb-oom"),
			"fb-ok":     newSTTStatusProxy(t, okSrv.URL, "fb-ok"),
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rw := httptest.NewRecorder()
	mux.ServeHTTP(rw, makeSTTReq(t, false))
	if rw.Code != 200 {
		t.Fatalf("status=%d want 200; body=%s", rw.Code, rw.Body.String())
	}
	if n := consecutiveFailures(t, bs, "fb-oom"); n != 0 {
		t.Fatalf("tier-1 OOM must not penalize breaker; ConsecutiveFailures=%d", n)
	}
	// The dead tier-0 (dial failure) is real degradation and still counts.
	if n := consecutiveFailures(t, bs, "local-stt"); n == 0 {
		t.Fatalf("tier-0 dial failure must still record a breaker failure")
	}
}
