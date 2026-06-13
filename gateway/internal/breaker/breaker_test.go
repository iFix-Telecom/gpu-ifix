package breaker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// discardLogger returns a slog.Logger that swallows all output. Tests
// that exercise OnStateChange would otherwise spam the test runner.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestSet wires a miniredis instance + Set with the given options.
// Cleanup closes both the redis client and miniredis.
func newTestSet(t *testing.T, names []string, opts Options) (*Set, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewSet(rdb, discardLogger(), opts, names), mr
}

// fastOpts uses a 100ms cooldown to keep cooldown tests under 1s.
func fastOpts() Options {
	return Options{ConsecutiveFailures: 3, Cooldown: 100 * time.Millisecond}
}

func TestBreakerOpensAfter3Failures(t *testing.T) {
	s, _ := newTestSet(t, []string{"local-llm"}, fastOpts())
	fail := func() (*http.Response, error) { return nil, &HTTPError{Status: 503, Msg: "upstream 503"} }
	for i := 0; i < 3; i++ {
		_, err := s.Execute("local-llm", fail)
		if err == nil {
			t.Fatalf("attempt %d: expected error, got nil", i)
		}
	}
	// Give the OnStateChange goroutine a tick to commit.
	time.Sleep(20 * time.Millisecond)
	cb, _ := s.Get("local-llm")
	if got := cb.State(); got != gobreaker.StateOpen {
		t.Fatalf("want StateOpen, got %v", got)
	}
}

func TestBreakerDoesNotOpenOn4xx(t *testing.T) {
	s, _ := newTestSet(t, []string{"local-llm"}, fastOpts())
	clientErr := func() (*http.Response, error) { return nil, &HTTPError{Status: 400, Msg: "bad req"} }
	for i := 0; i < 10; i++ {
		_, _ = s.Execute("local-llm", clientErr)
	}
	cb, _ := s.Get("local-llm")
	if got := cb.State(); got != gobreaker.StateClosed {
		t.Fatalf("4xx must not open breaker: got %v", got)
	}
}

func TestBreakerDoesNotOpenOn429(t *testing.T) {
	s, _ := newTestSet(t, []string{"openrouter-chat"}, fastOpts())
	throttle := func() (*http.Response, error) { return nil, &HTTPError{Status: 429, Msg: "rate limited"} }
	for i := 0; i < 10; i++ {
		_, _ = s.Execute("openrouter-chat", throttle)
	}
	cb, _ := s.Get("openrouter-chat")
	if got := cb.State(); got != gobreaker.StateClosed {
		t.Fatalf("429 must not open breaker (D-A4): got %v", got)
	}
}

func TestBreakerDoesNotOpenOnCanceled(t *testing.T) {
	s, _ := newTestSet(t, []string{"local-llm"}, fastOpts())
	canceled := func() (*http.Response, error) { return nil, context.Canceled }
	for i := 0; i < 10; i++ {
		_, _ = s.Execute("local-llm", canceled)
	}
	cb, _ := s.Get("local-llm")
	if got := cb.State(); got != gobreaker.StateClosed {
		t.Fatalf("context.Canceled must not open breaker: got %v", got)
	}
}

func TestBreakerCooldownThenHalfOpenToClosed(t *testing.T) {
	s, _ := newTestSet(t, []string{"local-llm"}, fastOpts())
	// Trip
	for i := 0; i < 3; i++ {
		_, _ = s.Execute("local-llm", func() (*http.Response, error) { return nil, &HTTPError{Status: 503, Msg: "x"} })
	}
	time.Sleep(20 * time.Millisecond)
	cb, _ := s.Get("local-llm")
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("want open, got %v", cb.State())
	}
	// While open, Execute returns error without firing fn.
	called := false
	_, err := s.Execute("local-llm", func() (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200}, nil
	})
	if called {
		t.Fatal("fn called while breaker OPEN; must short-circuit")
	}
	if err == nil {
		t.Fatal("expected error while OPEN, got nil")
	}
	// Sleep past cooldown.
	time.Sleep(120 * time.Millisecond)
	// Next Execute transitions to HALF_OPEN; a success closes it.
	resp := &http.Response{StatusCode: 200, Body: http.NoBody}
	_, err = s.Execute("local-llm", func() (*http.Response, error) { return resp, nil })
	if err != nil {
		t.Fatalf("post-cooldown success: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if cb.State() != gobreaker.StateClosed {
		t.Fatalf("want closed after half-open success, got %v", cb.State())
	}
}

func TestRemoteOpenOverlayShortCircuits(t *testing.T) {
	s, _ := newTestSet(t, []string{"local-llm"}, Options{ConsecutiveFailures: 3, Cooldown: 500 * time.Millisecond})
	// Apply a remote "open" event.
	s.applyRemoteEvent(makeRemoteEvent("local-llm", "open", time.Now().Unix()))
	called := false
	_, err := s.Execute("local-llm", func() (*http.Response, error) {
		called = true
		return &http.Response{}, nil
	})
	if called {
		t.Fatal("remote open must short-circuit local Execute")
	}
	if !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("expected ErrBreakerOpen, got %v", err)
	}
}

func TestIsSuccessful(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is success", nil, true},
		{"context.Canceled is success (client gave up)", context.Canceled, true},
		{"400 is success (client error)", &HTTPError{Status: 400}, true},
		{"404 is success", &HTTPError{Status: 404}, true},
		{"429 is success (throttle, not health)", &HTTPError{Status: 429}, true},
		{"500 is failure", &HTTPError{Status: 500}, false},
		{"502 is failure", &HTTPError{Status: 502}, false},
		{"504 is failure", &HTTPError{Status: 504}, false},
		{"deadline exceeded is failure", context.DeadlineExceeded, false},
		{"unknown error is failure", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSuccessful(tc.err); got != tc.want {
				t.Fatalf("IsSuccessful(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestSnapshotReturnsAllStates(t *testing.T) {
	s, _ := newTestSet(t, []string{"a", "b", "c"}, fastOpts())
	// Trip b.
	for i := 0; i < 3; i++ {
		_, _ = s.Execute("b", func() (*http.Response, error) { return nil, &HTTPError{Status: 503, Msg: "x"} })
	}
	time.Sleep(20 * time.Millisecond)
	snap := s.Snapshot()
	if snap["a"] != "closed" || snap["c"] != "closed" || snap["b"] != "open" {
		t.Fatalf("snap = %+v", snap)
	}
}

// TestEffectiveStateSnapshot — Phase 06.9 (SEED-005): the new snapshot
// method honors operator force-override, emitting "forced-open" for any
// upstream with an active override at gw:breaker:force:{name}. Mirrors
// TestSnapshotReturnsAllStates for the natural-FSM paths and adds a
// force-override sub-test.
func TestEffectiveStateSnapshot(t *testing.T) {
	t.Run("natural closed", func(t *testing.T) {
		s, _ := newTestSet(t, []string{"a"}, fastOpts())
		snap := s.EffectiveStateSnapshot()
		if got := snap["a"]; got != "closed" {
			t.Fatalf("a: want closed, got %q (snap=%+v)", got, snap)
		}
	})

	t.Run("natural open", func(t *testing.T) {
		s, _ := newTestSet(t, []string{"b"}, fastOpts())
		for i := 0; i < 3; i++ {
			_, _ = s.Execute("b", func() (*http.Response, error) { return nil, &HTTPError{Status: 503, Msg: "x"} })
		}
		time.Sleep(20 * time.Millisecond)
		snap := s.EffectiveStateSnapshot()
		if got := snap["b"]; got != "open" {
			t.Fatalf("b: want open, got %q (snap=%+v)", got, snap)
		}
	})

	t.Run("force-override wins over natural closed", func(t *testing.T) {
		s, mr := newTestSet(t, []string{"c"}, fastOpts())
		ctx := context.Background()
		val := ForceOverrideValue{State: "open", TTLSec: 300, SetBy: "operator", SetAt: time.Now().UTC()}
		buf, _ := json.Marshal(val)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		if err := rdb.Set(ctx, ForceOverrideKey("c"), string(buf), 300*time.Second).Err(); err != nil {
			t.Fatalf("SET: %v", err)
		}
		s.RefreshForceOverride(ctx, "c")

		snap := s.EffectiveStateSnapshot()
		if got := snap["c"]; got != "forced-open" {
			t.Fatalf("c: want forced-open, got %q (snap=%+v)", got, snap)
		}
		// Sanity: legacy Snapshot() must still report the raw FSM (closed).
		raw := s.Snapshot()
		if got := raw["c"]; got != "closed" {
			t.Fatalf("legacy Snapshot must be unaffected: c=%q, want closed (snap=%+v)", got, raw)
		}
	})

	t.Run("force-override wins over natural open", func(t *testing.T) {
		s, mr := newTestSet(t, []string{"d"}, fastOpts())
		// First, drive d to natural OPEN.
		for i := 0; i < 3; i++ {
			_, _ = s.Execute("d", func() (*http.Response, error) { return nil, &HTTPError{Status: 503, Msg: "x"} })
		}
		time.Sleep(20 * time.Millisecond)
		// Sanity: raw FSM is OPEN before override is installed.
		if raw := s.Snapshot(); raw["d"] != "open" {
			t.Fatalf("precondition: want d=open before override, got %q", raw["d"])
		}

		// Install force-override.
		ctx := context.Background()
		val := ForceOverrideValue{State: "open", TTLSec: 300, SetBy: "operator", SetAt: time.Now().UTC()}
		buf, _ := json.Marshal(val)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		if err := rdb.Set(ctx, ForceOverrideKey("d"), string(buf), 300*time.Second).Err(); err != nil {
			t.Fatalf("SET: %v", err)
		}
		s.RefreshForceOverride(ctx, "d")

		snap := s.EffectiveStateSnapshot()
		if got := snap["d"]; got != "forced-open" {
			t.Fatalf("d: force-override must win over natural open: got %q (snap=%+v)", got, snap)
		}
	})
}

func TestRebuildPreservesState(t *testing.T) {
	s, _ := newTestSet(t, []string{"a", "b"}, fastOpts())
	for i := 0; i < 3; i++ {
		_, _ = s.Execute("a", func() (*http.Response, error) { return nil, &HTTPError{Status: 503, Msg: "x"} })
	}
	time.Sleep(20 * time.Millisecond)
	s.Rebuild([]string{"a", "c"})
	cbA, okA := s.Get("a")
	if !okA || cbA.State() != gobreaker.StateOpen {
		t.Fatal("Rebuild must preserve state of unchanged breakers")
	}
	if _, okB := s.Get("b"); okB {
		t.Fatal("Rebuild must drop removed breakers")
	}
	cbC, okC := s.Get("c")
	if !okC || cbC.State() != gobreaker.StateClosed {
		t.Fatal("Rebuild must add new breakers in CLOSED state")
	}
}

// makeRemoteEvent is a test-file-local helper that constructs a
// redisx.BreakerEvent. Kept inside the test file (not promoted to a
// shared helper) because production code never builds events outside
// publishTransition.
func makeRemoteEvent(upstream, state string, sinceUnix int64) redisx.BreakerEvent {
	return redisx.BreakerEvent{Upstream: upstream, State: state, SinceUnix: sinceUnix}
}
