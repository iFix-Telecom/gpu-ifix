// Package proxy (halfopen_test.go): quick 261022 — a HALF_OPEN fallback is no
// longer a dead end. CLOSED candidates still win; with none left, a HALF_OPEN
// tier-1 takes the request as a trial and a served response closes its
// breaker; enabled tier-2 rows are the last resort before the 503.
package proxy

import (
	"net/http"
	"net/http/httptest"
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

type halfOpenFixture struct {
	disp http.Handler
	bs   *breaker.Set
	hits map[string]*atomic.Int32
}

// newHalfOpenDispatcher wires an STT-shaped role: parked tier-0, two tier-1
// (gemini-stt prio 0, groq-whisper prio 10) and a tier-2 openai-whisper.
// cooldown 10 ms makes a tripped breaker turn HALF_OPEN almost at once; an
// hour keeps it OPEN for the whole test.
func newHalfOpenDispatcher(t *testing.T, cooldown time.Duration) halfOpenFixture {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	names := []string{"local-stt", "gemini-stt", "groq-whisper", "openai-whisper"}
	hits := make(map[string]*atomic.Int32, len(names))
	proxies := make(map[string]http.Handler, len(names))
	for _, n := range names {
		c := &atomic.Int32{}
		hits[n] = c
		proxies[n] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"ok"}`))
		})
	}
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-stt", Role: "stt", Tier: 0, URL: "http://127.0.0.1:1", Enabled: true,
			CircuitConfig: upstreams.CircuitConfig{Parked: true}},
		upstreams.UpstreamConfig{Name: "gemini-stt", Role: "stt", Tier: 1, TierPriority: 0, URL: "http://gemini", Enabled: true},
		upstreams.UpstreamConfig{Name: "groq-whisper", Role: "stt", Tier: 1, TierPriority: 10, URL: "http://groq", Enabled: true},
		upstreams.UpstreamConfig{Name: "openai-whisper", Role: "stt", Tier: 2, URL: "http://openai", Enabled: true},
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: cooldown},
		loader.Names(),
	)
	disp := NewDispatcher(DispatcherConfig{
		Role:    "stt",
		Loader:  loader,
		Breaker: bs,
		Proxies: proxies,
		Log:     discardLogger(),
	})
	return halfOpenFixture{disp: disp, bs: bs, hits: hits}
}

func tripToHalfOpen(t *testing.T, bs *breaker.Set, names ...string) {
	t.Helper()
	for _, n := range names {
		_, _ = bs.Execute(n, func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: http.StatusServiceUnavailable, Msg: "trip"}
		})
	}
	time.Sleep(20 * time.Millisecond)
	for _, n := range names {
		if st := bs.EffectiveState(n); st != gobreaker.StateHalfOpen {
			t.Fatalf("%s = %v after trip+cooldown, want half-open", n, st)
		}
	}
}

func sttRequest() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", strings.NewReader("audio"))
	r.Header.Set("Content-Type", "application/octet-stream")
	return r.WithContext(auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	}))
}

// Reproduces 2026-10-22: every tier-1 HALF_OPEN, no probe running. The request
// must be served (by the first HALF_OPEN tier-1, not 503) and the served
// response must close that breaker.
func TestDispatcher_HalfOpenTier1ServesAndCloses(t *testing.T) {
	f := newHalfOpenDispatcher(t, 10*time.Millisecond)
	tripToHalfOpen(t, f.bs, "gemini-stt", "groq-whisper")

	rw := httptest.NewRecorder()
	f.disp.ServeHTTP(rw, sttRequest())
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rw.Code, rw.Body.String())
	}
	if got := f.hits["gemini-stt"].Load(); got != 1 {
		t.Fatalf("gemini-stt hits = %d, want 1 (first HALF_OPEN tier-1 takes the trial); groq=%d openai=%d local=%d",
			got, f.hits["groq-whisper"].Load(), f.hits["openai-whisper"].Load(), f.hits["local-stt"].Load())
	}
	if got := f.hits["openai-whisper"].Load(); got != 0 {
		t.Fatalf("tier-2 hits = %d, want 0 (HALF_OPEN tier-1 comes first)", got)
	}
	if st := f.bs.EffectiveState("gemini-stt"); st != gobreaker.StateClosed {
		t.Fatalf("gemini-stt after served trial = %v, want closed", st)
	}
}

// A CLOSED candidate keeps priority over a HALF_OPEN one, even one with a
// lower tier_priority; the HALF_OPEN breaker is left alone.
func TestDispatcher_ClosedBeatsHalfOpen(t *testing.T) {
	f := newHalfOpenDispatcher(t, 10*time.Millisecond)
	tripToHalfOpen(t, f.bs, "gemini-stt")

	rw := httptest.NewRecorder()
	f.disp.ServeHTTP(rw, sttRequest())
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	if f.hits["groq-whisper"].Load() != 1 || f.hits["gemini-stt"].Load() != 0 {
		t.Fatalf("hits groq=%d gemini=%d, want 1/0", f.hits["groq-whisper"].Load(), f.hits["gemini-stt"].Load())
	}
	if st := f.bs.EffectiveState("gemini-stt"); st != gobreaker.StateHalfOpen {
		t.Fatalf("gemini-stt = %v, want still half-open", st)
	}
}

// Every tier-1 OPEN → the enabled tier-2 serves instead of a 503.
func TestDispatcher_Tier2LastResort(t *testing.T) {
	f := newHalfOpenDispatcher(t, time.Hour)
	for _, n := range []string{"gemini-stt", "groq-whisper"} {
		_, _ = f.bs.Execute(n, func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: http.StatusServiceUnavailable, Msg: "trip"}
		})
		if st := f.bs.EffectiveState(n); st != gobreaker.StateOpen {
			t.Fatalf("%s = %v, want open", n, st)
		}
	}

	rw := httptest.NewRecorder()
	f.disp.ServeHTTP(rw, sttRequest())
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rw.Code, rw.Body.String())
	}
	if got := f.hits["openai-whisper"].Load(); got != 1 {
		t.Fatalf("openai-whisper hits = %d, want 1", got)
	}
}
