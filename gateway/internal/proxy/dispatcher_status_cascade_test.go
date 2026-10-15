// dispatcher_status_cascade_test.go — Phase 23-04: cascata por status HTTP
// entre candidatos (ChatRetryableStatusInterceptor), devolução do último erro
// real, SkipBreaker e caminhos de override (schedule/shed).
package proxy

import (
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
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// upSpec describes one upstream of a status-cascade fixture.
type upSpec struct {
	name     string
	tier     int
	prio     int
	status   int    // response status the httptest upstream answers
	body     string // response body
	noProxy  bool   // do not register a proxy for this upstream
	skipErr  bool   // interceptor emits a SkipBreaker()=true error instead
	noStatus bool   // do NOT compose ChatRetryableStatusInterceptor (final candidate)
}

type statusCascadeFixture struct {
	bs   *breaker.Set
	hits map[string]*int64
	cfg  DispatcherConfig
	mux  http.Handler
}

// skipBreakerErr models the 23-05 "chaves esgotadas" error: cascades like a
// retryable status but must NOT count toward the breaker.
type skipBreakerErr struct{}

func (skipBreakerErr) Error() string        { return "all keys exhausted" }
func (skipBreakerErr) SkipBreaker() bool    { return true }
func (skipBreakerErr) Is(target error) bool { return target == errUpstreamRetryable }

type skipBreakerIC struct{}

func (skipBreakerIC) Intercept(*http.Response) error { return skipBreakerErr{} }

// newStatusReverseProxy wires a real ReverseProxy with the given interceptors
// + the sentinel-aware ErrorHandler (production shape of a tier-1 chat proxy).
func newStatusReverseProxy(t *testing.T, name, target string, ics ...ProxyResponseInterceptor) http.Handler {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse %s: %v", target, err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &httputil.ReverseProxy{
		Rewrite:        func(pr *httputil.ProxyRequest) { pr.SetURL(u) },
		ModifyResponse: ComposeInterceptors(ics...),
		ErrorHandler:   ErrorHandler(name, log),
	}
}

func newStatusCascadeFixture(t *testing.T, pins ModelPinner, specs ...upSpec) *statusCascadeFixture {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close(); mr.Close() })

	hits := map[string]*int64{}
	proxies := map[string]http.Handler{}
	var cfgs []upstreams.UpstreamConfig
	for _, s := range specs {
		s := s
		n := new(int64)
		hits[s.name] = n
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt64(n, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.body))
		}))
		t.Cleanup(srv.Close)
		cfgs = append(cfgs, upstreams.UpstreamConfig{
			Name: s.name, Role: "llm", Tier: s.tier, TierPriority: s.prio, URL: srv.URL, Enabled: true,
		})
		if s.noProxy {
			continue
		}
		var ics []ProxyResponseInterceptor
		switch {
		case s.skipErr:
			ics = append(ics, skipBreakerIC{})
		case !s.noStatus:
			ics = append(ics, ChatRetryableStatusInterceptor())
		}
		proxies[s.name] = newStatusReverseProxy(t, s.name, srv.URL, ics...)
	}
	loader := upstreams.NewLoaderInMemory(cfgs...)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names())
	cfg := DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: proxies,
		Pins:    pins,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return &statusCascadeFixture{bs: bs, hits: hits, cfg: cfg, mux: NewDispatcher(cfg)}
}

func (f *statusCascadeFixture) hit(name string) int64 { return atomic.LoadInt64(f.hits[name]) }

// [A,B]: A 429 → B 200 serves; A's breaker took 1 failure (opens at 1).
func TestDispatchPinned_StatusCascade429ToNext(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner{"m": {"a", "b"}},
		upSpec{name: "a", tier: 1, prio: 0, status: 429, body: `{"error":{"message":"rl"}}`},
		upSpec{name: "b", tier: 1, prio: 10, status: 200, body: `{"upstream":"b"}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"b"`) {
		t.Fatalf("got %d %s, want 200 from b", rw.Code, rw.Body.String())
	}
	if f.hit("a") != 1 || f.hit("b") != 1 {
		t.Fatalf("hits a=%d b=%d, want 1/1", f.hit("a"), f.hit("b"))
	}
	if st := f.bs.EffectiveState("a"); st != gobreaker.StateOpen {
		t.Fatalf("breaker a = %v, want OPEN (1 failure recorded)", st)
	}
}

// stream:true — a 503 still cascades (status arrives pre-byte).
func TestDispatchPinned_StatusCascadeStreaming(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner{"m": {"a", "b"}},
		upSpec{name: "a", tier: 1, prio: 0, status: 503, body: `{"error":{"message":"busy"}}`},
		upSpec{name: "b", tier: 1, prio: 10, status: 200, body: `{"upstream":"b"}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m","stream":true}`, auth.DataClassNormal))
	if rw.Code != 200 || f.hit("b") != 1 {
		t.Fatalf("got %d (b hits %d), want 200 from b", rw.Code, f.hit("b"))
	}
}

// 400 is NOT retryable: reaches the client as-is, B never called.
func TestDispatchPinned_StatusCascade400NoCascade(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner{"m": {"a", "b"}},
		upSpec{name: "a", tier: 1, prio: 0, status: 400, body: `{"error":{"message":"bad"}}`},
		upSpec{name: "b", tier: 1, prio: 10, status: 200, body: `{"upstream":"b"}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if rw.Code != 400 || f.hit("b") != 0 {
		t.Fatalf("got %d (b hits %d), want 400 and no cascade", rw.Code, f.hit("b"))
	}
}

// Both 503 → client gets the LAST real error (B's), not pinned_upstreams_unavailable.
func TestDispatchPinned_StatusCascadeLastErrorReturned(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner{"m": {"a", "b"}},
		upSpec{name: "a", tier: 1, prio: 0, status: 503, body: `{"error":{"message":"from-a"}}`},
		upSpec{name: "b", tier: 1, prio: 10, status: 503, body: `{"error":{"message":"x"}}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if rw.Code != 503 {
		t.Fatalf("status = %d, want 503", rw.Code)
	}
	if rw.Body.String() != `{"error":{"message":"x"}}` {
		t.Fatalf("body = %s, want B's body", rw.Body.String())
	}
	if ct := rw.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

// SkipBreaker()=true on A → B serves, A's breaker untouched.
func TestDispatchPinned_StatusCascadeSkipBreaker(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner{"m": {"a", "b"}},
		upSpec{name: "a", tier: 1, prio: 0, status: 429, body: `{}`, skipErr: true},
		upSpec{name: "b", tier: 1, prio: 10, status: 200, body: `{"upstream":"b"}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if rw.Code != 200 || f.hit("b") != 1 {
		t.Fatalf("got %d, want 200 from b", rw.Code)
	}
	if st := f.bs.EffectiveState("a"); st != gobreaker.StateClosed {
		t.Fatalf("breaker a = %v, want CLOSED (SkipBreaker)", st)
	}
}

// No status error recorded (all candidates OPEN) → generic pinned 503 kept.
func TestDispatchPinned_StatusCascadeNoStatusKeepsGeneric(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner{"m": {"a", "b"}},
		upSpec{name: "a", tier: 1, prio: 0, status: 200, body: `{}`},
		upSpec{name: "b", tier: 1, prio: 10, status: 200, body: `{}`},
	)
	tripBreaker(t, f.bs, "a")
	tripBreaker(t, f.bs, "b")
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassNormal))
	if rw.Code != 503 || !strings.Contains(rw.Body.String(), "pinned_upstreams_unavailable") {
		t.Fatalf("got %d %s, want generic pinned 503", rw.Code, rw.Body.String())
	}
}

// Sensitive tenant pinned to externals that fail by status → RES-08 block
// (sensitive never even dispatches to tier-1).
func TestDispatchPinned_StatusCascadeSensitiveStillBlocked(t *testing.T) {
	f := newStatusCascadeFixture(t, stubPinner{"m": {"a", "b"}},
		upSpec{name: "a", tier: 1, prio: 0, status: 503, body: `{}`},
		upSpec{name: "b", tier: 1, prio: 10, status: 200, body: `{}`},
	)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"m"}`, auth.DataClassSensitive))
	if rw.Code != 503 || !strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Fatalf("got %d %s, want sensitive block", rw.Code, rw.Body.String())
	}
	if f.hit("a")+f.hit("b") != 0 {
		t.Fatalf("external hits = %d, want 0", f.hit("a")+f.hit("b"))
	}
}
