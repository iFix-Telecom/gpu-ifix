//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// upstreamMock bundles an httptest.Server with a thread-safe hit counter.
// All Phase 3 resilience tests reuse this shape so assertions on
// "tier-1 was/wasn't called" stay readable.
type upstreamMock struct {
	server *httptest.Server
	hits   *atomic.Int64
}

// newSuccessMock returns a 200 OK mock that increments hits on every call.
func newSuccessMock(t *testing.T) *upstreamMock {
	t.Helper()
	hits := &atomic.Int64{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"pong"}}]}`))
	}))
	t.Cleanup(s.Close)
	return &upstreamMock{server: s, hits: hits}
}

// newFailMock returns a server that always 500s and counts hits.
func newFailMock(t *testing.T) *upstreamMock {
	t.Helper()
	hits := &atomic.Int64{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(500)
	}))
	t.Cleanup(s.Close)
	return &upstreamMock{server: s, hits: hits}
}

// newCountingMockWithHandler wraps a custom http.HandlerFunc with hit
// counting. Test-specific behaviors (e.g. "first 3 requests 500, then 200")
// pass their own logic via fn.
func newCountingMockWithHandler(t *testing.T, fn http.HandlerFunc) *upstreamMock {
	t.Helper()
	hits := &atomic.Int64{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fn(w, r)
	}))
	t.Cleanup(s.Close)
	return &upstreamMock{server: s, hits: hits}
}

// driveBreaker fires N requests through the breaker.Set so its in-process
// gobreaker counters reach the trip threshold. Returns once the breaker
// transitions to OPEN (or after `attempts` tries — caller checks state).
func driveBreaker(t *testing.T, bs *breaker.Set, name string, status int, attempts int) {
	t.Helper()
	for i := 0; i < attempts; i++ {
		_, _ = bs.Execute(name, func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: status, Msg: "synthetic-trip"}
		})
		cb, _ := bs.Get(name)
		if cb != nil && cb.State() == gobreaker.StateOpen {
			return
		}
	}
}

// waitForBreakerState polls until the named breaker reaches the target
// state or the deadline elapses. Returns true if the state was observed,
// false on timeout.
func waitForBreakerState(bs *breaker.Set, name string, want gobreaker.State, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		cb, _ := bs.Get(name)
		if cb != nil && cb.State() == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	cb, _ := bs.Get(name)
	return cb != nil && cb.State() == want
}

// resilienceLoader builds an in-memory upstreams.Loader for the
// dispatcher tests that don't need testcontainer Postgres for the
// upstreams table itself (loader is purely in-memory). Tier-0 + tier-1
// for one role with the given URLs.
func resilienceLoader(role, t0Name, t0URL, t1Name, t1URL string) *upstreams.Loader {
	cfgs := []upstreams.UpstreamConfig{
		{Name: t0Name, Role: role, Tier: 0, URL: t0URL, Enabled: true},
	}
	if t1URL != "" {
		cfgs = append(cfgs, upstreams.UpstreamConfig{
			Name: t1Name, Role: role, Tier: 1, URL: t1URL, Enabled: true,
		})
	}
	return upstreams.NewLoaderInMemory(cfgs...)
}

// quietRedisClient mints a client against a Redis address. Caller closes.
func quietRedisClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr})
}

// mustWait waits up to `within` for cond() to return true. t.Fatal's the
// test (with msg) if the condition never holds.
func mustWait(t *testing.T, _ context.Context, within time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout (%s): %s", within, msg)
}
