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

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
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

// ---------------------------------------------------------------------------
// Phase 23-04 Task 3 — override de schedule/shed passa pela cascata.
// ---------------------------------------------------------------------------

// overrideReq builds a request carrying a schedule/shed upstream override (and
// optionally the shed_saturated decision).
func overrideReq(t *testing.T, body, override string, shed bool) *http.Request {
	t.Helper()
	r := makeRequest(t, body, auth.DataClassNormal)
	ctx := auditctx.WithUpstreamOverride(r.Context(), override)
	if shed {
		ctx = auditctx.WithShedDecision(ctx, auditctx.UpstreamShedSaturatedValue)
	}
	return r.WithContext(ctx)
}

// threeExternal is the Phase 23 chain: local pod + ①②③.
func threeExternal(s1, s2, s3 int) []upSpec {
	return []upSpec{
		{name: "local-llm", tier: 0, prio: 0, status: 200, body: `{"upstream":"local"}`},
		{name: "anthropic-haiku", tier: 1, prio: 0, status: s1, body: `{"error":{"message":"h"}}`},
		{name: "openrouter-chat", tier: 1, prio: 10, status: s2, body: `{"upstream":"chat"}`},
		{name: "openrouter-deepseek", tier: 1, prio: 20, status: s3, body: `{"upstream":"deepseek"}`},
	}
}

var pinsAll = stubPinner{"m": {"anthropic-haiku", "openrouter-chat", "openrouter-deepseek"}}

// Pinned model + override: ① 529 → ② serves.
func TestOverride_PinnedCascades529ToNext(t *testing.T) {
	f := newStatusCascadeFixture(t, pinsAll, threeExternal(529, 200, 200)...)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"m"}`, "anthropic-haiku", false))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"chat"`) {
		t.Fatalf("got %d %s, want 200 from openrouter-chat", rw.Code, rw.Body.String())
	}
	if f.hit("anthropic-haiku") != 1 {
		t.Fatalf("haiku hits = %d, want 1", f.hit("anthropic-haiku"))
	}
}

// Pinned + override, all externals fail by status → last real error.
func TestOverride_PinnedAllStatusFailReturnsLastError(t *testing.T) {
	specs := threeExternal(529, 503, 502)
	specs[3].body = `{"error":{"message":"last"}}`
	f := newStatusCascadeFixture(t, pinsAll, specs...)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"m"}`, "anthropic-haiku", false))
	if rw.Code != 502 || rw.Body.String() != `{"error":{"message":"last"}}` {
		t.Fatalf("got %d %s, want 502 last error", rw.Code, rw.Body.String())
	}
}

// Pinned + override, all externals OPEN → off_hours_upstream_unavailable.
func TestOverride_PinnedAllOpenOffHoursEnvelope(t *testing.T) {
	f := newStatusCascadeFixture(t, pinsAll, threeExternal(200, 200, 200)...)
	for _, n := range []string{"anthropic-haiku", "openrouter-chat", "openrouter-deepseek"} {
		tripBreaker(t, f.bs, n)
	}
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"m"}`, "anthropic-haiku", false))
	if rw.Code != 503 || !strings.Contains(rw.Body.String(), "off_hours_upstream_unavailable") {
		t.Fatalf("got %d %s, want 503 off_hours_upstream_unavailable", rw.Code, rw.Body.String())
	}
	if f.hit("local-llm") != 0 {
		t.Fatalf("local hits = %d, want 0", f.hit("local-llm"))
	}
}

// Pinned model including local-llm + override → tier-0 skipped.
func TestOverride_PinnedSkipsTier0(t *testing.T) {
	pins := stubPinner{"m": {"local-llm", "openrouter-chat"}}
	f := newStatusCascadeFixture(t, pins, threeExternal(200, 200, 200)...)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"m"}`, "openrouter-chat", false))
	if rw.Code != 200 || f.hit("local-llm") != 0 || f.hit("openrouter-chat") != 1 {
		t.Fatalf("got %d local=%d chat=%d, want chat only", rw.Code, f.hit("local-llm"), f.hit("openrouter-chat"))
	}
}

// Unpinned model + override = ① (529) → ② serves via dispatchOverrideCascade.
func TestOverride_UnpinnedCascadesTier1(t *testing.T) {
	f := newStatusCascadeFixture(t, pinsAll, threeExternal(529, 200, 200)...)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"other"}`, "anthropic-haiku", false))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"chat"`) {
		t.Fatalf("got %d %s, want 200 from openrouter-chat", rw.Code, rw.Body.String())
	}
	if f.hit("local-llm") != 0 {
		t.Fatalf("local hits = %d, want 0", f.hit("local-llm"))
	}
}

// Unpinned + override, all status fail → last error; all OPEN → off-hours.
func TestOverride_UnpinnedExhaustion(t *testing.T) {
	specs := threeExternal(529, 503, 504)
	specs[3].body = `{"error":{"message":"last"}}`
	f := newStatusCascadeFixture(t, nil, specs...)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"other"}`, "anthropic-haiku", false))
	if rw.Code != 504 || rw.Body.String() != `{"error":{"message":"last"}}` {
		t.Fatalf("status-fail: got %d %s, want 504 last error", rw.Code, rw.Body.String())
	}

	g := newStatusCascadeFixture(t, nil, threeExternal(200, 200, 200)...)
	for _, n := range []string{"anthropic-haiku", "openrouter-chat", "openrouter-deepseek"} {
		tripBreaker(t, g.bs, n)
	}
	rw = httptest.NewRecorder()
	g.mux.ServeHTTP(rw, overrideReq(t, `{"model":"other"}`, "anthropic-haiku", false))
	if rw.Code != 503 || !strings.Contains(rw.Body.String(), "off_hours_upstream_unavailable") {
		t.Fatalf("all-open: got %d %s, want off_hours_upstream_unavailable", rw.Code, rw.Body.String())
	}
}

// Override naming a non-tier-1 upstream → single-upstream dispatchOverride.
func TestOverride_NonTier1NameKeepsSingleUpstream(t *testing.T) {
	specs := append(threeExternal(200, 200, 200),
		upSpec{name: "last-resort", tier: 2, prio: 0, status: 503, body: `{"error":{"message":"lr"}}`})
	f := newStatusCascadeFixture(t, nil, specs...)
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"other"}`, "last-resort", false))
	// Single upstream, no dispatchResult → the interceptor declines and the
	// upstream's 503 reaches the client verbatim; nothing else is tried.
	if rw.Code != 503 || f.hit("last-resort") != 1 || f.hit("anthropic-haiku")+f.hit("openrouter-chat") != 0 {
		t.Fatalf("got %d lr=%d, want single-upstream dispatch", rw.Code, f.hit("last-resort"))
	}
}

// shed_saturated: the ONLY tier-1 OPEN → D-D1 envelope unchanged.
func TestOverride_ShedSingleTier1OpenEmitsDD1(t *testing.T) {
	f := newStatusCascadeFixture(t, nil,
		upSpec{name: "local-llm", tier: 0, status: 200, body: `{}`},
		upSpec{name: "openrouter-chat", tier: 1, status: 200, body: `{}`},
	)
	tripBreaker(t, f.bs, "openrouter-chat")
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"other"}`, "openrouter-chat", true))
	if rw.Code != 503 || !strings.Contains(rw.Body.String(), "all_chat_upstreams_saturated") || rw.Header().Get("Retry-After") != "30" {
		t.Fatalf("got %d %s RA=%q, want D-D1", rw.Code, rw.Body.String(), rw.Header().Get("Retry-After"))
	}
}

// shed_saturated: override ① OPEN but ② CLOSED → no D-D1, ② serves.
func TestOverride_ShedFirstOpenSecondServes(t *testing.T) {
	f := newStatusCascadeFixture(t, nil, threeExternal(200, 200, 200)...)
	tripBreaker(t, f.bs, "anthropic-haiku")
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, overrideReq(t, `{"model":"other"}`, "anthropic-haiku", true))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"chat"`) {
		t.Fatalf("got %d %s, want 200 from openrouter-chat", rw.Code, rw.Body.String())
	}
}

// Phase 23-04 Task 2: a tier-1 candidate without a registered proxy (dormant
// upstream) is skipped by cascadeTier1 instead of committing a terminal 503.
func TestCascadeTier1_SkipsCandidateWithoutProxy(t *testing.T) {
	f := newStatusCascadeFixture(t, nil,
		upSpec{name: "local", tier: 0, prio: 0, status: 200, body: `{}`},
		upSpec{name: "x", tier: 1, prio: 0, status: 200, body: `{"upstream":"x"}`, noProxy: true},
		upSpec{name: "y", tier: 1, prio: 10, status: 200, body: `{"upstream":"y"}`},
	)
	tripBreaker(t, f.bs, "local")
	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"any"}`, auth.DataClassNormal))
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"y"`) {
		t.Fatalf("got %d %s, want 200 from y", rw.Code, rw.Body.String())
	}
	if f.hit("x") != 0 {
		t.Fatalf("x hits = %d, want 0", f.hit("x"))
	}
}
