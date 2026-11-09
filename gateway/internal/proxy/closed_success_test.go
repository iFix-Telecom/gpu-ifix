// Package proxy (closed_success_test.go): quick 261108-pnd — a CLOSED fallback
// breaker records the successes of real traffic, so isolated failures spread
// over days no longer add up to an OPEN breaker.
package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// newClosedSuccessDispatcher: parked tier-0, gemini-stt (tier-1 prio 0)
// answering geminiStatus, groq-whisper (tier-1 prio 10) answering 200.
// Breaker trips after 2 consecutive failures; cooldown long enough to stay
// OPEN for the whole test.
func newClosedSuccessDispatcher(t *testing.T, geminiStatus *atomic.Int32) (http.Handler, *breaker.Set) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	})
	proxies := map[string]http.Handler{
		"local-stt": ok,
		"gemini-stt": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(int(geminiStatus.Load()))
			_, _ = w.Write([]byte(`{"text":"g"}`))
		}),
		"groq-whisper": ok,
	}
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-stt", Role: "stt", Tier: 0, URL: "http://127.0.0.1:1", Enabled: true,
			CircuitConfig: upstreams.CircuitConfig{Parked: true}},
		upstreams.UpstreamConfig{Name: "gemini-stt", Role: "stt", Tier: 1, TierPriority: 0, URL: "http://gemini", Enabled: true},
		upstreams.UpstreamConfig{Name: "groq-whisper", Role: "stt", Tier: 1, TierPriority: 10, URL: "http://groq", Enabled: true},
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: time.Hour},
		loader.Names(),
	)
	disp := NewDispatcher(DispatcherConfig{
		Role:    "stt",
		Loader:  loader,
		Breaker: bs,
		Proxies: proxies,
		Log:     discardLogger(),
	})
	return disp, bs
}

func failOnce(bs *breaker.Set, name string) {
	_, _ = bs.Execute(name, func() (*http.Response, error) {
		return nil, &breaker.HTTPError{Status: http.StatusBadGateway, Msg: "fail"}
	})
}

// failure → served 200 → failure: the success in between resets the streak,
// so gemini-stt stays CLOSED (before the fix it opened on the 2nd failure).
func TestDispatcher_ClosedSuccessResetsFailureStreak(t *testing.T) {
	status := &atomic.Int32{}
	status.Store(http.StatusOK)
	disp, bs := newClosedSuccessDispatcher(t, status)

	failOnce(bs, "gemini-stt")
	rw := httptest.NewRecorder()
	disp.ServeHTTP(rw, sttRequest())
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	failOnce(bs, "gemini-stt")
	if st := bs.EffectiveState("gemini-stt"); st != gobreaker.StateClosed {
		t.Fatalf("gemini-stt = %v after fail/200/fail, want closed (served 200 must reset the streak)", st)
	}
}

// A committed 5xx is not a success: it must not reset the streak.
func TestDispatcher_Closed5xxDoesNotReset(t *testing.T) {
	status := &atomic.Int32{}
	status.Store(http.StatusBadGateway)
	disp, bs := newClosedSuccessDispatcher(t, status)

	failOnce(bs, "gemini-stt")
	rw := httptest.NewRecorder()
	disp.ServeHTTP(rw, sttRequest())
	failOnce(bs, "gemini-stt")
	if st := bs.EffectiveState("gemini-stt"); st != gobreaker.StateOpen {
		t.Fatalf("gemini-stt = %v after fail/502/fail, want open (5xx must not count as success)", st)
	}
}

func TestStatusRecorder(t *testing.T) {
	rw := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: rw}
	_, _ = rec.Write([]byte("x"))
	rec.WriteHeader(http.StatusTeapot) // ignored after the implicit 200
	if rec.status != http.StatusOK {
		t.Fatalf("implicit status = %d, want 200", rec.status)
	}
	rec2 := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	rec2.WriteHeader(http.StatusTooManyRequests)
	if rec2.status != http.StatusTooManyRequests {
		t.Fatalf("explicit status = %d, want 429", rec2.status)
	}
	if http.NewResponseController(rec).Flush() != nil {
		t.Fatalf("Flush through ResponseController must reach the wrapped writer")
	}
}
