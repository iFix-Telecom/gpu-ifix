package proxy

import (
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
