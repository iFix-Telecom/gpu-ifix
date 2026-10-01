package breaker

import (
	"net/http"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"
)

// Quick 260930-uru: Reset puts an OPEN breaker back to CLOSED, drops the
// remoteOpen overlay and leaves the operator force cache intact.
func TestReset_OpenBecomesClosed(t *testing.T) {
	s, _ := newTestSet(t, []string{"local-stt", "kokoro-tts"}, Options{ConsecutiveFailures: 3, Cooldown: time.Hour})
	fail := func() (*http.Response, error) { return nil, &HTTPError{Status: 503, Msg: "upstream 503"} }
	for i := 0; i < 3; i++ {
		_, _ = s.Execute("local-stt", fail)
	}
	cb, _ := s.Get("local-stt")
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("precondition: want OPEN, got %v", cb.State())
	}
	s.applyRemoteEvent(makeRemoteEvent("local-stt", "open", time.Now().Unix()))
	s.forceCache.set("local-stt", forceCacheEntry{})

	s.Reset("local-stt")

	cb2, ok := s.Get("local-stt")
	if !ok || cb2.State() != gobreaker.StateClosed {
		t.Fatalf("after Reset want CLOSED, got %v ok=%v", cb2.State(), ok)
	}
	if cb2.Counts().ConsecutiveFailures != 0 {
		t.Fatalf("after Reset counts must be zero, got %+v", cb2.Counts())
	}
	s.mu.RLock()
	_, remote := s.remoteOpen["local-stt"]
	s.mu.RUnlock()
	if remote {
		t.Fatal("after Reset remoteOpen entry must be gone")
	}
	if _, ok := s.forceCache.get("local-stt"); !ok {
		t.Fatal("Reset must not touch the operator force cache")
	}
	// A successful call goes through (no short-circuit).
	called := false
	if _, err := s.Execute("local-stt", func() (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200}, nil
	}); err != nil || !called {
		t.Fatalf("Execute after Reset: called=%v err=%v", called, err)
	}
}

func TestReset_UnknownNameNoop(t *testing.T) {
	s, _ := newTestSet(t, []string{"local-stt"}, fastOpts())
	s.Reset("does-not-exist")
	if _, ok := s.Get("does-not-exist"); ok {
		t.Fatal("Reset must not create breakers for unknown names")
	}
}
