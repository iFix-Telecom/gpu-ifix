// Package proxy (transport.go): a RoundTripper wrapper that detects pre-byte
// connection-class dial failures and converts them into a typed sentinel
// (errDialFailedFallthrough) so the dispatcher can fall through to the tier-1
// cascade instead of writing a 502 with the breaker still CLOSED (RES-13,
// Plan 12-03 / D-06).
//
// WHY at the RoundTrip level (Pitfall 2): httputil.ReverseProxy.ServeHTTP does
// NOT return the transport error to the caller — when Transport.RoundTrip
// returns an error it invokes p.ErrorHandler(w, r, err) and returns. The
// DEFAULT ErrorHandler writes a 502. Intercepting at RoundTrip lets the
// fallthrough signal be produced BEFORE any byte is written; the
// sentinel-aware ErrorHandler (errors.go) then suppresses the write so nothing
// reaches the client and the dispatcher can re-dispatch.
//
// Classification is strictly DIAL-PHASE (pre-byte). A response-header timeout
// or a mid-response read failure (post-connection) is NOT connection-class and
// passes through unchanged — preserving D-06 (timeouts/5xx do not fall
// through) and D-07 (never re-dispatch after any byte was written).
package proxy

import (
	"errors"
	"net"
	"net/http"
	"syscall"
)

// fallthroughRoundTripper wraps a base RoundTripper. On a pre-byte
// connection-class dial error it returns (nil, errDialFailedFallthrough);
// every other outcome (success, post-dial timeout, 5xx, any non-dial error)
// passes through unchanged.
type fallthroughRoundTripper struct {
	base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (f fallthroughRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f.base.RoundTrip(r)
	if err != nil && isConnectionClass(err) {
		// Pre-byte dial failure: substitute the typed sentinel so the
		// ErrorHandler suppresses the 502 write and the dispatcher re-routes.
		return nil, errDialFailedFallthrough
	}
	return resp, err
}

// isConnectionClass reports whether err is a PRE-BYTE connection-class
// failure: a dial-phase network error where no bytes could have been written
// to (or read from) the client yet. It extends breaker.IsSuccessful's
// network-error reasoning rather than forking a new taxonomy — but it is
// STRICTER: it returns true ONLY for dial-phase signals so a post-connection
// response-header timeout (which breaker.IsSuccessful also counts as a
// failure) is excluded here. This strictness is the D-06 / Pitfall 2
// guarantee: a mid-response failure must NEVER be treated as connection-class.
//
//   - *net.OpError with Op=="dial"  → true  (dial phase = pre-byte, A3)
//   - syscall.ECONNREFUSED          → true  (connection refused at dial)
//   - *net.DNSError                 → true  (name resolution failed pre-dial)
//   - any other (incl. post-dial read/write OpErrors, timeouts, 5xx) → false
func isConnectionClass(err error) bool {
	if err == nil {
		return false
	}
	// DNS resolution failure: always pre-dial.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// Connection refused: the dial reached the host but the port had no
	// listener — pre-byte by definition.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	// Dial-phase OpError. Op=="dial" is the canonical pre-byte signal; any
	// other Op ("read"/"write") is post-connection and MUST NOT classify as
	// connection-class (D-06: a mid-response read timeout is not a dial fault).
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	return false
}
