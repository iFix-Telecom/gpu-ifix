// Package proxy (parked_test.go): quick 261018-prk — a parked tier-0 (pod
// desligado de propósito) is never dialed: normal tenants go straight to
// tier-1 and the tier-0 breaker never moves (no transition → no alert);
// sensitive tenants get the RES-08 503 at once.
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

func newParkedDispatcher(t *testing.T) (http.Handler, *breaker.Set, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	var t0Hits, t1Hits atomic.Int32
	counting := func(hits *atomic.Int32) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[]}`))
		})
	}

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "rerank-gpu", Role: "rerank", Tier: 0, URL: "http://127.0.0.1:1", Enabled: true,
			CircuitConfig: upstreams.CircuitConfig{Parked: true}},
		upstreams.UpstreamConfig{Name: "rerank-cpu", Role: "rerank", Tier: 1, URL: "http://cpu", Enabled: true},
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	disp := NewDispatcher(DispatcherConfig{
		Role:    "rerank",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"rerank-gpu": counting(&t0Hits),
			"rerank-cpu": counting(&t1Hits),
		},
		Log: discardLogger(),
	})
	return disp, bs, &t0Hits, &t1Hits
}

func parkedRequest(dc auth.DataClass) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/rerank",
		strings.NewReader(`{"query":"q","documents":["a"]}`))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: dc,
	}))
}

func TestDispatcher_ParkedTier0GoesStraightToTier1(t *testing.T) {
	disp, bs, t0Hits, t1Hits := newParkedDispatcher(t)
	for i := 0; i < 3; i++ {
		rw := httptest.NewRecorder()
		disp.ServeHTTP(rw, parkedRequest(auth.DataClassNormal))
		if rw.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (body %s)", i, rw.Code, rw.Body.String())
		}
	}
	if got := t0Hits.Load(); got != 0 {
		t.Fatalf("parked tier-0 hits = %d, want 0", got)
	}
	if got := t1Hits.Load(); got != 3 {
		t.Fatalf("tier-1 hits = %d, want 3", got)
	}
	if st := bs.EffectiveState("rerank-gpu"); st != gobreaker.StateClosed {
		t.Fatalf("parked tier-0 breaker = %v, want closed (untouched)", st)
	}
}

func TestDispatcher_ParkedTier0SensitiveBlocked(t *testing.T) {
	disp, _, t0Hits, t1Hits := newParkedDispatcher(t)
	start := time.Now()
	rw := httptest.NewRecorder()
	disp.ServeHTTP(rw, parkedRequest(auth.DataClassSensitive))
	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rw.Code)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("sensitive block took %v, want immediate (no retry loop)", d)
	}
	if t0Hits.Load() != 0 || t1Hits.Load() != 0 {
		t.Fatalf("hits t0=%d t1=%d, want 0/0 (RES-08)", t0Hits.Load(), t1Hits.Load())
	}
}
