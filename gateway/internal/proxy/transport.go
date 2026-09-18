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
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"
)

// fallthroughRoundTripper wraps a base RoundTripper. On a pre-byte
// connection-class dial error it returns (nil, errDialFailedFallthrough);
// every other outcome (success, post-dial timeout, 5xx, any non-dial error)
// passes through unchanged.
type fallthroughRoundTripper struct {
	base http.RoundTripper

	// preByteTimeout widens the classification to include a response-header
	// timeout. It is opt-in and MUST only be enabled for non-streaming roles
	// (STT today). See NewSTTFallthroughTransport for why that is pre-byte-safe.
	preByteTimeout bool
}

// NewSTTFallthroughTransport wraps base for the STT role, where a
// response-header timeout ALSO falls through to the next candidate.
//
// Why STT may widen what transport.go's package doc calls strictly dial-phase:
// RoundTrip only returns an error BEFORE any response header was received, and
// STT is never streamed (audio.go omits FlushInterval; the upstream answers with
// one buffered JSON body). So at the moment RoundTrip fails, httputil.ReverseProxy
// has not written a single byte to the client and re-dispatching stays within D-07.
// D-06's "timeouts do not fall through" still holds for chat, where the SSE tee
// may already have flushed.
//
// Real incident (OPERACOES-26927, 2026-09-17): local-stt failed retryable, the
// cascade advanced to gemini-stt, gemini hung and the transport reported
// "http2: timeout awaiting response headers" after 25s. That error was not
// connection-class, so the cascade stopped there and the client got a terminal
// 502 — groq-whisper and openai-whisper were never tried despite being enabled
// and probing ok. Three n8n retries burned 213s on a recording that the same
// Gemini model transcribed in 21s.
func NewSTTFallthroughTransport(base http.RoundTripper) http.RoundTripper {
	return fallthroughRoundTripper{base: base, preByteTimeout: true}
}

// RoundTrip implements http.RoundTripper.
func (f fallthroughRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f.base.RoundTrip(r)
	if err != nil && (isConnectionClass(err) || (f.preByteTimeout && isPreByteTimeout(r.Context(), err))) {
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

// isPreByteTimeout reports whether err is an upstream timeout that happened
// before any response header arrived — the transport waited and gave up, so
// nothing reached the client and the next candidate may still serve the request.
//
// A caller-side cancellation is explicitly NOT one of these: if the client hung
// up or its own deadline fired, retrying against another upstream only burns
// budget for a response nobody will read.
func isPreByteTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	// net/http and x/net/http2 both report the header wait with this phrase and
	// neither exports the error value, so the string is the only stable handle.
	if strings.Contains(err.Error(), "timeout awaiting response headers") {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
