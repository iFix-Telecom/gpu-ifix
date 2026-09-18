package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"syscall"
	"testing"
)

// errRoundTripper returns a fixed (resp, err) pair so we can drive
// fallthroughRoundTripper through every classification branch without a
// live socket.
type errRoundTripper struct {
	resp *http.Response
	err  error
}

func (e errRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return e.resp, e.err
}

// TestIsConnectionClass_DialRefused: a real closed-port dial produces a
// *net.OpError with Op=="dial" → connection-class (pre-byte). We force the
// error shape directly (a connection-refused dial OpError) rather than
// relying on a flaky live socket.
func TestIsConnectionClass_DialRefused(t *testing.T) {
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: syscall.ECONNREFUSED,
	}
	if !isConnectionClass(opErr) {
		t.Fatalf("dial OpError(ECONNREFUSED) should classify as connection-class")
	}
	// Bare ECONNREFUSED (no OpError wrapper) is still connection-class.
	if !isConnectionClass(syscall.ECONNREFUSED) {
		t.Fatalf("bare ECONNREFUSED should classify as connection-class")
	}
}

// TestIsConnectionClass_DNSError: a DNS resolution failure is pre-byte.
func TestIsConnectionClass_DNSError(t *testing.T) {
	dnsErr := &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}
	if !isConnectionClass(dnsErr) {
		t.Fatalf("net.DNSError should classify as connection-class")
	}
}

// TestIsConnectionClass_ResponseTimeout: a response-header timeout happens
// AFTER the connection is established (post-dial). It MUST NOT be classified
// as connection-class — this protects D-06's pre-byte-only contract.
func TestIsConnectionClass_ResponseTimeout(t *testing.T) {
	// A net.OpError whose Op is "read" (post-dial) with a timeout must NOT
	// be treated as connection-class.
	readTimeout := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: timeoutErr{},
	}
	if isConnectionClass(readTimeout) {
		t.Fatalf("a post-dial read timeout must NOT classify as connection-class")
	}
	// A bare context.DeadlineExceeded-style timeout error must NOT classify.
	if isConnectionClass(timeoutErr{}) {
		t.Fatalf("a bare timeout error must NOT classify as connection-class")
	}
}

// timeoutErr implements net.Error with Timeout()==true, Op-less.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestIsConnectionClass_Nil: nil → false.
func TestIsConnectionClass_Nil(t *testing.T) {
	if isConnectionClass(nil) {
		t.Fatalf("nil error must classify as false")
	}
}

// TestFallthroughRoundTripper_SignalsOnDial: RoundTrip over a base
// transport that returns a dial OpError → returns errDialFailedFallthrough
// (nil response); a successful RoundTrip passes through unchanged.
func TestFallthroughRoundTripper_SignalsOnDial(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	frt := fallthroughRoundTripper{base: errRoundTripper{err: dialErr}}

	resp, err := frt.RoundTrip(&http.Request{})
	if !errors.Is(err, errDialFailedFallthrough) {
		t.Fatalf("dial failure should surface errDialFailedFallthrough, got %v", err)
	}
	if resp != nil {
		t.Fatalf("dial failure should return nil response, got %v", resp)
	}

	// A successful RoundTrip passes through unchanged.
	okResp := &http.Response{StatusCode: 200}
	frtOK := fallthroughRoundTripper{base: errRoundTripper{resp: okResp}}
	gotResp, gotErr := frtOK.RoundTrip(&http.Request{})
	if gotErr != nil {
		t.Fatalf("successful RoundTrip should pass nil error, got %v", gotErr)
	}
	if gotResp != okResp {
		t.Fatalf("successful RoundTrip should pass the response unchanged")
	}

	// A non-connection-class error (e.g. a post-dial read timeout) passes
	// through unchanged (NOT replaced by the sentinel).
	readTimeout := &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}
	frtTO := fallthroughRoundTripper{base: errRoundTripper{err: readTimeout}}
	_, toErr := frtTO.RoundTrip(&http.Request{})
	if errors.Is(toErr, errDialFailedFallthrough) {
		t.Fatalf("post-dial read timeout must NOT be replaced by the sentinel")
	}
	if !errors.Is(toErr, readTimeout) {
		t.Fatalf("post-dial read timeout should pass through unchanged, got %v", toErr)
	}
}

// TestIsPreByteTimeout classifies the timeouts that may cascade to the next
// STT candidate, and the cancellations that may not.
func TestIsPreByteTimeout(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"http2 header timeout", context.Background(), errors.New("http2: timeout awaiting response headers"), true},
		{"net/http header timeout", context.Background(), errors.New("net/http: timeout awaiting response headers"), true},
		{"bare net.Error timeout", context.Background(), timeoutErr{}, true},
		{"read timeout OpError", context.Background(), &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}, true},
		{"nil error", context.Background(), nil, false},
		{"non-timeout error", context.Background(), errors.New("upstream said no"), false},
		{"context deadline", context.Background(), context.DeadlineExceeded, false},
		{"context canceled", context.Background(), context.Canceled, false},
		{"caller already gone", canceledCtx(), errors.New("http2: timeout awaiting response headers"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPreByteTimeout(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("isPreByteTimeout(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestSTTFallthroughTransport_HeaderTimeoutCascades is the OPERACOES-26927
// regression: a hung gemini-stt reported a header timeout and the cascade
// stopped, returning a terminal 502 while groq/openai were never tried.
func TestSTTFallthroughTransport_HeaderTimeoutCascades(t *testing.T) {
	hung := errors.New("http2: timeout awaiting response headers")
	req := (&http.Request{}).WithContext(context.Background())

	sttRT := NewSTTFallthroughTransport(errRoundTripper{err: hung})
	resp, err := sttRT.RoundTrip(req)
	if !errors.Is(err, errDialFailedFallthrough) {
		t.Fatalf("STT header timeout should cascade, got %v", err)
	}
	if resp != nil {
		t.Fatalf("cascading RoundTrip should return nil response, got %v", resp)
	}

	// D-06 unchanged for every other role: the default wrapper still lets a
	// header timeout through as-is, because chat may already have flushed SSE.
	chatRT := fallthroughRoundTripper{base: errRoundTripper{err: hung}}
	if _, chatErr := chatRT.RoundTrip(req); errors.Is(chatErr, errDialFailedFallthrough) {
		t.Fatalf("non-STT transport must NOT cascade on a header timeout")
	}

	// A dial failure still cascades on the STT wrapper too.
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	if _, dErr := NewSTTFallthroughTransport(errRoundTripper{err: dialErr}).RoundTrip(req); !errors.Is(dErr, errDialFailedFallthrough) {
		t.Fatalf("dial failure should still cascade on the STT wrapper, got %v", dErr)
	}

	// Success passes through untouched.
	okResp := &http.Response{StatusCode: 200}
	gotResp, gotErr := NewSTTFallthroughTransport(errRoundTripper{resp: okResp}).RoundTrip(req)
	if gotErr != nil || gotResp != okResp {
		t.Fatalf("successful RoundTrip should pass through, got resp=%v err=%v", gotResp, gotErr)
	}
}
