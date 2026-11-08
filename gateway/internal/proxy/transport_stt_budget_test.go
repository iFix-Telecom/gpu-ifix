package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Quick 261108-sgr: STT header wait sized per request + slow timeout cascades
// without a breaker penalty.

func TestSTTHeaderBudget(t *testing.T) {
	cases := []struct {
		name string
		secs float64
		want time.Duration
	}{
		{"no stamp → floor", 0, STTHeaderBudgetFloor},
		{"short voice note", 10, 26 * time.Second},
		{"5 min call", 300, 55 * time.Second},
		{"17 min call", 1027, 25*time.Second + 102700*time.Millisecond},
		{"very long → cap", 5000, STTHeaderBudgetCap},
	}
	for _, c := range cases {
		ctx := context.Background()
		if c.secs > 0 {
			ctx = withSTTAudioUpperSeconds(ctx, c.secs)
		}
		if got := sttHeaderBudget(ctx); got != c.want {
			t.Errorf("%s: budget(%v s) = %v, want %v", c.name, c.secs, got, c.want)
		}
	}
}

func TestSTTAudioUpperSeconds(t *testing.T) {
	// Non-WAV: 8 kbps over-estimate. 435,200 bytes of 8 kbps mp3 = 435.2 s
	// (07/11/2026 11:25 recording, billed by Groq as 435 s).
	mp3 := make([]byte, 435200)
	if got := sttAudioUpperSeconds(mp3, 27.2); got < 435 || got > 436 {
		t.Fatalf("mp3 upper = %v, want ~435.2", got)
	}
	// WAV: the exact header-derived length wins.
	wav := append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 1000)...)
	if got := sttAudioUpperSeconds(wav, 12.5); got != 12.5 {
		t.Fatalf("wav upper = %v, want 12.5 (derived)", got)
	}
}

// A slow STT upstream cascades, but its error must not count on the breaker.
func TestSTTSlowTimeoutSkipsBreaker(t *testing.T) {
	hung := errors.New("http2: timeout awaiting response headers")
	req := (&http.Request{}).WithContext(context.Background())

	_, err := NewSTTFallthroughTransport(errRoundTripper{err: hung}).RoundTrip(req)
	if !errors.Is(err, errDialFailedFallthrough) {
		t.Fatalf("slow STT must cascade, got %v", err)
	}
	if countsAsBreakerFailure(err) {
		t.Fatalf("slow STT must NOT count as breaker failure: %v", err)
	}

	// A dial failure is a real outage: cascades AND counts.
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	_, dErr := NewSTTFallthroughTransport(errRoundTripper{err: dialErr}).RoundTrip(req)
	if !errors.Is(dErr, errDialFailedFallthrough) || !countsAsBreakerFailure(dErr) {
		t.Fatalf("dial failure must cascade and count, got %v (counts=%v)", dErr, countsAsBreakerFailure(dErr))
	}

	// Chat keeps counting a header timeout as a failure.
	_, cErr := NewChatFallthroughTransport(errRoundTripper{err: hung}).RoundTrip(req)
	if !errors.Is(cErr, errDialFailedFallthrough) || !countsAsBreakerFailure(cErr) {
		t.Fatalf("chat header timeout must cascade and count, got %v", cErr)
	}
}

// blockingRT waits for its request context and reports how it ended.
type blockingRT struct{ delay time.Duration }

func (b blockingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case <-time.After(b.delay):
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"text":"ok"}`))}, nil
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

func budgetRT(base http.RoundTripper, budget time.Duration) fallthroughRoundTripper {
	return fallthroughRoundTripper{
		base:           base,
		preByteTimeout: true,
		slowNotFailure: true,
		headerBudget:   func(context.Context) time.Duration { return budget },
	}
}

func TestBudgetTransport_TimesOutAndCascadesWithoutPenalty(t *testing.T) {
	req := (&http.Request{}).WithContext(context.Background())
	start := time.Now()
	resp, err := budgetRT(blockingRT{delay: time.Second}, 30*time.Millisecond).RoundTrip(req)
	if resp != nil {
		t.Fatalf("timed-out RoundTrip must return nil response")
	}
	var slow sttSlowTimeoutError
	if !errors.As(err, &slow) || slow.budget != 30*time.Millisecond {
		t.Fatalf("want sttSlowTimeoutError{30ms}, got %v", err)
	}
	if !errors.Is(err, errDialFailedFallthrough) || countsAsBreakerFailure(err) {
		t.Fatalf("budget timeout must cascade without breaker penalty, got %v", err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("budget not enforced: took %v", el)
	}
}

func TestBudgetTransport_FastAnswerPassesAndBodyReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond) // body arrives AFTER the budget
		_, _ = io.WriteString(w, `{"text":"ok"}`)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := budgetRT(http.DefaultTransport, 40*time.Millisecond).RoundTrip(req)
	if err != nil {
		t.Fatalf("headers arrived in time, got err %v", err)
	}
	body, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if rerr != nil || string(body) != `{"text":"ok"}` {
		t.Fatalf("body read must not be cut by the header budget: body=%q err=%v", body, rerr)
	}
}

func TestBudgetTransport_CallerCancelDoesNotCascade(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	req := (&http.Request{}).WithContext(ctx)
	_, err := budgetRT(blockingRT{delay: time.Second}, time.Second).RoundTrip(req)
	if errors.Is(err, errDialFailedFallthrough) {
		t.Fatalf("caller cancel must not cascade, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestBudgetTransport_DialFailureStillCounts(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	req := (&http.Request{}).WithContext(context.Background())
	_, err := budgetRT(errRoundTripper{err: dialErr}, time.Second).RoundTrip(req)
	if !errors.Is(err, errDialFailedFallthrough) || !countsAsBreakerFailure(err) {
		t.Fatalf("dial failure must cascade and count, got %v", err)
	}
}
