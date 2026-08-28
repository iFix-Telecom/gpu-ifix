// dispatcher_pinned_test.go — model-pinned routing (quick 260828).
//
// A model whose alias has model_aliases rows is served ONLY by the upstreams
// named in those rows; a model without rows keeps the plain tier cascade.
package proxy

import (
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

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// stubPinner is a fixed alias → upstream-names map.
type stubPinner map[string][]string

func (s stubPinner) PinnedUpstreams(alias string) []string { return s[alias] }

type pinnedFixture struct {
	breakerSet *breaker.Set
	tier0Hits  *int64
	tier1Hits  *int64
	mux        http.Handler
	cleanup    func()
}

func newPinnedFixture(t *testing.T, pins ModelPinner) *pinnedFixture {
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
			"primary-llm":  newPassthroughProxy(t, t0srv.URL),
			"fallback-llm": newPassthroughProxy(t, t1srv.URL),
		},
		Pins: pins,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := NewDispatcher(cfg)

	return &pinnedFixture{
		breakerSet: bs,
		tier0Hits:  &t0Hits,
		tier1Hits:  &t1Hits,
		mux:        mux,
		cleanup: func() {
			t0srv.Close()
			t1srv.Close()
			_ = rdb.Close()
			mr.Close()
		},
	}
}

// Pinned to the tier-1 name only → tier-1 serves even with tier-0 CLOSED.
func TestDispatcher_PinnedToFallbackSkipsHealthyTier0(t *testing.T) {
	f := newPinnedFixture(t, stubPinner{"deepseek-flash": {"fallback-llm"}})
	defer f.cleanup()

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"deepseek-flash"}`, auth.DataClassNormal))

	if rw.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", rw.Code, rw.Body.String())
	}
	if got := atomic.LoadInt64(f.tier1Hits); got != 1 {
		t.Fatalf("tier-1 hits = %d, want 1", got)
	}
	if got := atomic.LoadInt64(f.tier0Hits); got != 0 {
		t.Fatalf("tier-0 hits = %d, want 0 (pin must skip healthy tier-0)", got)
	}
}

// Pinned to the tier-0 name only → tier-0 serves; tier-0 breaker OPEN must
// NOT fall through to an unpinned tier-1 (503 pinned_upstreams_unavailable).
func TestDispatcher_PinnedToPrimaryNoUnpinnedFallback(t *testing.T) {
	f := newPinnedFixture(t, stubPinner{"qwen-pod": {"primary-llm"}})
	defer f.cleanup()

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"qwen-pod"}`, auth.DataClassNormal))
	if rw.Code != 200 {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	if got := atomic.LoadInt64(f.tier0Hits); got != 1 {
		t.Fatalf("tier-0 hits = %d, want 1", got)
	}

	tripBreaker(t, f.breakerSet, "primary-llm")
	rw = httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"qwen-pod"}`, auth.DataClassNormal))
	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (pin must not leak to unpinned tier-1)", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "pinned_upstreams_unavailable") {
		t.Fatalf("body = %s, want pinned_upstreams_unavailable", rw.Body.String())
	}
	if got := atomic.LoadInt64(f.tier1Hits); got != 0 {
		t.Fatalf("tier-1 hits = %d, want 0", got)
	}
}

// A model with NO alias rows keeps the plain cascade (tier-0 serves).
func TestDispatcher_UnpinnedModelKeepsPlainCascade(t *testing.T) {
	f := newPinnedFixture(t, stubPinner{"deepseek-flash": {"fallback-llm"}})
	defer f.cleanup()

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"anything-else"}`, auth.DataClassNormal))

	if rw.Code != 200 {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	if got := atomic.LoadInt64(f.tier0Hits); got != 1 {
		t.Fatalf("tier-0 hits = %d, want 1 (plain cascade)", got)
	}
}

// Sensitive tenant pinned to an external-only roster → RES-08 block, zero hits.
func TestDispatcher_PinnedExternalOnlySensitiveBlocks(t *testing.T) {
	f := newPinnedFixture(t, stubPinner{"deepseek-flash": {"fallback-llm"}})
	defer f.cleanup()

	rw := httptest.NewRecorder()
	f.mux.ServeHTTP(rw, makeRequest(t, `{"model":"deepseek-flash"}`, auth.DataClassSensitive))

	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 sensitive block", rw.Code)
	}
	if got := atomic.LoadInt64(f.tier1Hits); got != 0 {
		t.Fatalf("tier-1 hits = %d, want 0 (RES-08 hard gate)", got)
	}
}
