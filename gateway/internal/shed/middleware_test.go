// Package shed (middleware_test.go): unit tests for the Phase 5 shed
// middleware decision tree (CONTEXT.md D-B4). Each branch (01..10) is
// exercised in isolation with an in-memory fake tenant lookup so we
// avoid the cost of standing up a real tenants.Loader/Postgres pool.
//
// Coverage budget: every branch in shed.middleware.go must have at
// least one Test* function that drives the middleware to that exact
// decision and asserts both the wire response (status, Retry-After,
// envelope code) AND the context stamps (auditctx.ShedDecisionFromContext,
// auditctx.UpstreamOverrideFromContext) consumed by audit / dispatcher.
//
// Integration coverage against real Postgres/Redis lives in the
// integration_test package (Plan 08).
package shed

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// --- Test doubles (interfaces matching MiddlewareDeps surface) ---

// fakeUpstreamLoader implements UpstreamResolver. Resolve returns
// (UpstreamConfig, true) for known (role, tier) pairs, (zero, false)
// otherwise.
type fakeUpstreamLoader struct {
	byRole map[string]map[int]upstreams.UpstreamConfig
}

func (f *fakeUpstreamLoader) Resolve(role string, tier int) (upstreams.UpstreamConfig, bool) {
	if m, ok := f.byRole[role]; ok {
		if u, ok2 := m[tier]; ok2 {
			return u, true
		}
	}
	return upstreams.UpstreamConfig{}, false
}

// fakeTenantLookup implements TenantLookup. nil snapshot or missing ID
// yields an error (mirrors the real *tenants.Loader behaviour).
type fakeTenantLookup struct {
	snap map[uuid.UUID]tenants.TenantConfig
}

func (f *fakeTenantLookup) Get(id uuid.UUID) (tenants.TenantConfig, error) {
	if f == nil || f.snap == nil {
		return tenants.TenantConfig{}, errors.New("tenants: not found")
	}
	cfg, ok := f.snap[id]
	if !ok {
		return tenants.TenantConfig{}, errors.New("tenants: not found")
	}
	return cfg, nil
}

func newUpstream(name, role string, tier int) upstreams.UpstreamConfig {
	return upstreams.UpstreamConfig{Name: name, Role: role, Tier: tier}
}

func authRequest(method, path string, tenant uuid.UUID, dataClass string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ac := auth.AuthContext{
		TenantID:  tenant.String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClass(dataClass),
	}
	ctx := auth.WithContext(r.Context(), ac)
	return r.WithContext(ctx)
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// buildDeps wires a MiddlewareDeps with the requested FSM state,
// preloaded inflight count for the dummy tenant, dataClass, and an
// optional tier-1 upstream registered.
//
// Returns (deps, request, recorder) ready for h.ServeHTTP.
func buildDeps(t *testing.T, fsmState State, inflight int64, dataClass string, t1Available bool) (MiddlewareDeps, *http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	tu := dummyTenantUUID()
	ten := &fakeTenantLookup{snap: map[uuid.UUID]tenants.TenantConfig{
		tu: {ID: tu, Slug: "tester", DataClass: dataClass, LocalInflightMaxLLM: 4},
	}}
	byRole := map[string]map[int]upstreams.UpstreamConfig{
		"llm": {0: newUpstream("local-llm", "llm", 0)},
	}
	if t1Available {
		byRole["llm"][1] = newUpstream("openrouter-chat", "llm", 1)
	}
	l := &fakeUpstreamLoader{byRole: byRole}

	// Build FSM and force the state we want to test.
	s := NewSet(nil, silentLogger(), Options{DefaultArmSeconds: 1, DefaultRecoverSeconds: 1})
	s.Rebuild([]string{"local-llm"})
	if fsm, ok := s.Get("local-llm"); ok {
		fsm.Transition(fsmState, "test")
	}
	reg := NewInflightRegistry([]string{"local-llm"})
	for i := int64(0); i < inflight; i++ {
		reg.Inc("local-llm", tu)
	}
	d := MiddlewareDeps{
		Loader:   l,
		Tenants:  ten,
		Set:      s,
		Inflight: reg,
		Latency:  map[string]*LatencyRing{"local-llm": NewLatencyRing(100)},
	}

	r := authRequest("POST", "/v1/chat/completions", tu, dataClass)
	w := httptest.NewRecorder()
	return d, r, w
}

func dummyTenantUUID() uuid.UUID {
	u, _ := uuid.Parse("11111111-1111-1111-1111-111111111111")
	return u
}

// --- Helper coverage ---

func TestDefaultClassifyRoute(t *testing.T) {
	t.Helper()
	cases := []struct {
		path string
		want string
	}{
		{"/v1/chat/completions", "llm"},
		{"/v1/completions", "llm"},
		{"/v1/audio/transcriptions", "stt"},
		{"/v1/embeddings", "embed"},
		{"/admin/anything", ""},
		{"/health", ""},
	}
	for _, c := range cases {
		if got := defaultClassifyRoute(c.path); got != c.want {
			t.Errorf("defaultClassifyRoute(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestResolveCapForRole(t *testing.T) {
	tc := tenants.TenantConfig{
		LocalInflightMaxLLM:   7,
		LocalInflightMaxSTT:   3,
		LocalInflightMaxEmbed: 11,
	}
	if got := resolveCapForRole(tc, "llm"); got != 7 {
		t.Errorf("llm cap = %d, want 7", got)
	}
	if got := resolveCapForRole(tc, "stt"); got != 3 {
		t.Errorf("stt cap = %d, want 3", got)
	}
	if got := resolveCapForRole(tc, "embed"); got != 11 {
		t.Errorf("embed cap = %d, want 11", got)
	}
	if got := resolveCapForRole(tc, "unknown"); got != 0 {
		t.Errorf("unknown role cap = %d, want 0", got)
	}
}

func TestDefaultCapForRole(t *testing.T) {
	if got := defaultCapForRole("llm"); got != 4 {
		t.Errorf("llm default = %d, want 4", got)
	}
	if got := defaultCapForRole("stt"); got != 2 {
		t.Errorf("stt default = %d, want 2", got)
	}
	if got := defaultCapForRole("embed"); got != 8 {
		t.Errorf("embed default = %d, want 8", got)
	}
	if got := defaultCapForRole("unknown"); got != 1 {
		t.Errorf("unknown default = %d, want 1", got)
	}
}

// --- Branch tests (D-B4 decision tree) ---

// Branch 01: auth missing → next (defensive fallthrough).
func TestMiddleware_Branch01_AuthMissing(t *testing.T) {
	d, _, _ := buildDeps(t, StateOff, 0, "normal", true)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil) // no auth ctx
	w := httptest.NewRecorder()
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("auth missing should fall through to next")
	}
}

// Branch 02: tenantID parse fail → next (defensive).
func TestMiddleware_Branch02_TenantIDParseFail(t *testing.T) {
	d, _, _ := buildDeps(t, StateOff, 0, "normal", true)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ac := auth.AuthContext{TenantID: "not-a-uuid", APIKeyID: uuid.New().String(), DataClass: "normal"}
	r = r.WithContext(auth.WithContext(r.Context(), ac))
	w := httptest.NewRecorder()
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("malformed tenant_id should fall through to next")
	}
}

// Branch 03: tenant loader error → next (snapshot missing).
func TestMiddleware_Branch03_TenantLoaderError(t *testing.T) {
	tu := dummyTenantUUID()
	ten := &fakeTenantLookup{snap: nil} // empty / nil snapshot
	l := &fakeUpstreamLoader{byRole: map[string]map[int]upstreams.UpstreamConfig{
		"llm": {0: newUpstream("local-llm", "llm", 0)},
	}}
	s := NewSet(nil, silentLogger(), Options{})
	s.Rebuild([]string{"local-llm"})
	d := MiddlewareDeps{
		Loader:   l,
		Tenants:  ten,
		Set:      s,
		Inflight: NewInflightRegistry([]string{"local-llm"}),
		Latency:  map[string]*LatencyRing{},
	}
	r := authRequest("POST", "/v1/chat/completions", tu, "normal")
	w := httptest.NewRecorder()
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("tenant loader error should fall through to next")
	}
}

// Branch 04: schedule already overrode (peak off-hours) → noop + stamp.
func TestMiddleware_Branch04_PeakOffhours(t *testing.T) {
	d, r, w := buildDeps(t, StateOn, 10, "normal", true)
	// Pretend schedule middleware already wrote an override
	r = r.WithContext(auditctx.WithUpstreamOverride(r.Context(), "openrouter-chat"))
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		if shed := auditctx.ShedDecisionFromContext(req.Context()); shed != "skipped_peak_offhours" {
			t.Errorf("expected shed_decision=skipped_peak_offhours, got %q", shed)
		}
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("peak-offhours should call next with stamp")
	}
}

// Branch 05: route does not classify (no role) → next without altering ctx.
func TestMiddleware_Branch05_UnmappedRoute(t *testing.T) {
	d, _, w := buildDeps(t, StateOff, 0, "normal", true)
	tu := dummyTenantUUID()
	r := authRequest("POST", "/admin/health", tu, "normal") // unmapped path
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		if v := auditctx.UpstreamOverrideFromContext(req.Context()); v != "" {
			t.Errorf("unmapped route must not stamp override; got %q", v)
		}
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("unmapped route should pass through")
	}
}

// Branch 06: tier-0 upstream not configured → next (dispatcher handles).
func TestMiddleware_Branch06_NoTier0Upstream(t *testing.T) {
	tu := dummyTenantUUID()
	ten := &fakeTenantLookup{snap: map[uuid.UUID]tenants.TenantConfig{
		tu: {ID: tu, Slug: "tester", DataClass: "normal", LocalInflightMaxLLM: 4},
	}}
	// Loader has NO tier-0 mapping for llm.
	l := &fakeUpstreamLoader{byRole: map[string]map[int]upstreams.UpstreamConfig{}}
	s := NewSet(nil, silentLogger(), Options{})
	d := MiddlewareDeps{
		Loader:   l,
		Tenants:  ten,
		Set:      s,
		Inflight: NewInflightRegistry(nil),
		Latency:  map[string]*LatencyRing{},
	}
	r := authRequest("POST", "/v1/chat/completions", tu, "normal")
	w := httptest.NewRecorder()
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("missing tier-0 upstream should pass through to dispatcher")
	}
}

// Branch 07: FSM=Off → trackAndPass (calls next, ctx stamped passed).
func TestMiddleware_Branch07_FSMOff(t *testing.T) {
	d, r, w := buildDeps(t, StateOff, 100, "normal", true)
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		if v := auditctx.UpstreamOverrideFromContext(req.Context()); v != "" {
			t.Errorf("FSM=Off must not stamp override; got %q", v)
		}
		if shed := auditctx.ShedDecisionFromContext(req.Context()); shed != "passed" {
			t.Errorf("FSM=Off must stamp shed_decision=passed; got %q", shed)
		}
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("FSM=Off should pass through")
	}
}

// Branch 08: FSM=On + tenant inflight < cap → trackAndPass (slot livre).
func TestMiddleware_Branch08_FSMOn_UnderCap(t *testing.T) {
	d, r, w := buildDeps(t, StateOn, 1, "normal", true) // cap=4, inflight=1
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		if v := auditctx.UpstreamOverrideFromContext(req.Context()); v != "" {
			t.Errorf("under-cap must not divert; got override %q", v)
		}
		if shed := auditctx.ShedDecisionFromContext(req.Context()); shed != "passed" {
			t.Errorf("under-cap must stamp shed_decision=passed; got %q", shed)
		}
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("under-cap should still call next")
	}
}

// Branch 09: FSM=On + normal tenant + cap exceeded + tier-1 available → divert.
func TestMiddleware_Branch09_FSMOn_NormalCapped(t *testing.T) {
	d, r, w := buildDeps(t, StateOn, 10, "normal", true) // inflight=10 > cap=4
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		got := auditctx.UpstreamOverrideFromContext(req.Context())
		if got != "openrouter-chat" {
			t.Errorf("expected override=openrouter-chat, got %q", got)
		}
		if shed := auditctx.ShedDecisionFromContext(req.Context()); shed != auditctx.UpstreamShedSaturatedValue {
			t.Errorf("expected shed_decision=%q, got %q", auditctx.UpstreamShedSaturatedValue, shed)
		}
	}))
	h.ServeHTTP(w, r)
	if !called {
		t.Fatal("normal-capped must call next with override stamped for tier-1 dispatch")
	}
}

// Branch 10a: FSM=On + sensitive tenant + cap exceeded → 503 + Retry-After:5.
func TestMiddleware_Branch10a_FSMOn_SensitiveCapped(t *testing.T) {
	d, r, w := buildDeps(t, StateOn, 10, "sensitive", true)
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	h.ServeHTTP(w, r)
	if called {
		t.Fatal("sensitive must not reach next")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "5" {
		t.Errorf("expected Retry-After=5, got %q", ra)
	}
	if !strings.Contains(w.Body.String(), "upstream_saturated_for_sensitive_tenant") {
		t.Errorf("expected sensitive code, body=%s", w.Body.String())
	}
}

// Branch 10b: FSM=On + normal capped + tier-1 unavailable → 503 all_chat_upstreams_saturated.
func TestMiddleware_Branch10b_FSMOn_NoTier1(t *testing.T) {
	d, r, w := buildDeps(t, StateOn, 10, "normal", false) // tier-1 absent
	called := false
	h := Middleware(d, silentLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	h.ServeHTTP(w, r)
	if called {
		t.Fatal("tier-1 unavailable must not reach next (middleware writes 503)")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "30" {
		t.Errorf("expected Retry-After=30, got %q", ra)
	}
	if !strings.Contains(w.Body.String(), "all_chat_upstreams_saturated") {
		t.Errorf("expected all_chat_upstreams_saturated; body=%s", w.Body.String())
	}
}

// Compile-time guard: *tenants.Loader must satisfy TenantLookup so
// production main.go can pass the real loader without an adapter.
var _ TenantLookup = (*tenants.Loader)(nil)

// Compile-time guard: *upstreams.Loader must satisfy UpstreamResolver.
var _ UpstreamResolver = (*upstreams.Loader)(nil)

// Sanity check: untyped helpers compile and ctx package is wired.
func TestPackageImportsCompile(t *testing.T) {
	_ = context.Background()
	_ = auditctx.WithShedDecision
}
