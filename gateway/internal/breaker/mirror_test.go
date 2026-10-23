package breaker

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func TestOnStateChangePublishesToRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewSet(rdb, discardLogger(),
		Options{ConsecutiveFailures: 3, Cooldown: 100 * time.Millisecond},
		[]string{"local-llm"},
	)
	// Trip
	for i := 0; i < 3; i++ {
		_, _ = s.Execute("local-llm", func() (*http.Response, error) {
			return nil, &HTTPError{Status: 503, Msg: "x"}
		})
	}
	// Wait for the goroutine publisher to write to Redis. Quick 261022: the
	// hash now exists from boot (state=closed), so wait for the transition's
	// state, not for the key.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if mr.HGet("gw:breaker:local-llm", "state") == "open" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mr.Exists("gw:breaker:local-llm") {
		t.Fatal("expected gw:breaker:local-llm hash in Redis after trip")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	fields := rdb.HGetAll(ctx, "gw:breaker:local-llm").Val()
	if fields["state"] != "open" {
		t.Fatalf("state = %q, want open", fields["state"])
	}
}

func TestSubscribeAppliesRemoteEvent(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	s := NewSet(rdb, discardLogger(),
		Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
		[]string{"openrouter-chat"},
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Subscribe(ctx)
	time.Sleep(50 * time.Millisecond) // let subscription register
	// Another "replica" publishes OPEN.
	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "openrouter-chat", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	// Wait for apply.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		called := false
		_, err := s.Execute("openrouter-chat", func() (*http.Response, error) {
			called = true
			return nil, nil
		})
		if err != nil && !called {
			// short-circuited → remote event applied
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("subscribe did not apply remote OPEN event within 2s")
}

// TestPublishFailure_DoesNotBlockBreaker simulates Redis going down
// mid-stream by closing the miniredis server BEFORE we trip the
// breaker. The local gobreaker MUST still transition to OPEN —
// CONTEXT.md D-D1 promises Redis-down does not stop breakers.
func TestPublishFailure_DoesNotBlockBreaker(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	// Use a Cooldown long enough that the post-trip wait does not
	// expire it — we are asserting the in-process state transition
	// happened, not the cooldown semantics. 5s is comfortably above
	// the 200ms goroutine-settle wait below.
	s := NewSet(rdb, discardLogger(),
		Options{ConsecutiveFailures: 3, Cooldown: 5 * time.Second},
		[]string{"local-llm"},
	)
	mr.Close() // simulate Redis down
	// Trip anyway — breaker must still transition to OPEN in-process.
	for i := 0; i < 3; i++ {
		_, _ = s.Execute("local-llm", func() (*http.Response, error) {
			return nil, &HTTPError{Status: 503, Msg: "x"}
		})
	}
	// Allow the publishTransition goroutine to fail and return.
	time.Sleep(200 * time.Millisecond)
	cb, _ := s.Get("local-llm")
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("breaker must open even with Redis down (D-D1 fallback): got %v", cb.State())
	}
}
