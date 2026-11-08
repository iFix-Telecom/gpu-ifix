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
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// fallthroughRoundTripper wraps a base RoundTripper. On a pre-byte
// connection-class dial error it returns (nil, errDialFailedFallthrough);
// every other outcome (success, post-dial timeout, 5xx, any non-dial error)
// passes through unchanged.
type fallthroughRoundTripper struct {
	base http.RoundTripper

	// preByteTimeout widens the classification to include a response-header
	// timeout. It is opt-in: STT (NewSTTFallthroughTransport) and the tier-1
	// chat upstreams (NewChatFallthroughTransport, Phase 23-12). Both
	// constructors document why it is pre-byte-safe.
	preByteTimeout bool

	// slowNotFailure (STT only, quick 261108-sgr): a pre-byte timeout still
	// falls through, but as sttSlowTimeoutError, whose SkipBreaker() keeps it
	// out of the breaker. Slow is not down: the upstream answers short audio
	// in ~2 s and only long recordings outlast the header wait.
	slowNotFailure bool

	// headerBudget, when set, replaces the base transport's fixed
	// ResponseHeaderTimeout with a per-request wait derived from the request
	// context (sttHeaderBudget). The base's own ResponseHeaderTimeout should be
	// set just above the budget cap as a safety net.
	headerBudget func(context.Context) time.Duration
}

// NewChatFallthroughTransport envolve base para os upstreams de chat tier-1
// (passo ① anthropic-haiku no 23-12; ② e openrouter-tts no wiring do 23-14):
// timeout de cabeçalho também vira errDialFailedFallthrough e a cascata segue
// para o próximo candidato.
//
// Por que é pré-byte também com SSE: RoundTrip só devolve erro ANTES de
// receber qualquer header de resposta; o httputil.ReverseProxy ainda não
// escreveu nada ao cliente (o stream só abre depois do 200 com headers). O
// "timeouts não cascateiam" do D-06 vale para falha DEPOIS dos headers (leitura
// do corpo no meio do stream), que este wrapper não toca. Cancelamento do
// próprio cliente nunca cascateia (isPreByteTimeout).
//
// Decisão sobre o timeout de 120 s (MEDIUM do Gemini na revisão cross-AI):
// hoje o openrouter-chat usa http.Transport puro, então um ResponseHeaderTimeout
// estourado vira 502 terminal e a cascata nem acontece. Mantém-se 120 s
// (resposta não-streaming com effort high pode demorar) e passa-se a cascatear
// quando estoura. Pior caso documentado: ① e ② presos 120 s cada antes do ③.
func NewChatFallthroughTransport(base http.RoundTripper) http.RoundTripper {
	return fallthroughRoundTripper{base: base, preByteTimeout: true}
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
//
// Quick 261108-sgr: the timeout cascades WITHOUT a breaker penalty
// (sttSlowTimeoutError). Before, every slow long recording recorded a failure,
// CLOSED successes never reset the count, and every third slow request opened
// the gemini-stt breaker — sending ALL STT to the paid groq-whisper for the
// cooldown window. Real errors (dial, 5xx, provider envelope) still count.
func NewSTTFallthroughTransport(base http.RoundTripper) http.RoundTripper {
	return fallthroughRoundTripper{base: base, preByteTimeout: true, slowNotFailure: true}
}

// NewSTTBudgetFallthroughTransport is NewSTTFallthroughTransport with the
// response-header wait sized per request from the audio length
// (sttHeaderBudget) instead of one fixed value for every recording.
//
// Quick 261108-sgr: gemini-stt waited a fixed 25 s. Short audio answers in
// ~2 s (p95 4.5 s), but a 17-minute call outlasted the 25 s and was sent again
// to groq-whisper, the paid fallback. The budget grows with the audio so the
// primary gets the time it needs, while a hung upstream on a short request is
// still abandoned after the 25 s floor.
func NewSTTBudgetFallthroughTransport(base http.RoundTripper) http.RoundTripper {
	return fallthroughRoundTripper{
		base:           base,
		preByteTimeout: true,
		slowNotFailure: true,
		headerBudget:   sttHeaderBudget,
	}
}

// STT header-wait budget (quick 261108-sgr). Worst-case STT chain:
// 150 s (gemini-stt) + 60 s (groq-whisper) = 210 s, under the 290 s audio
// write timeout and the 300 s timeout of the n8n recording processor.
const (
	STTHeaderBudgetFloor          = 25 * time.Second
	STTHeaderBudgetCap            = 150 * time.Second
	sttHeaderBudgetPerAudioSecond = 100 * time.Millisecond
)

// sttHeaderBudget returns floor + 0.1 s per second of audio, capped. The
// audio length is the conservative upper estimate stamped by the STT request
// middleware (withSTTAudioUpperSeconds); without it the floor applies.
func sttHeaderBudget(ctx context.Context) time.Duration {
	secs := sttAudioUpperSecondsFrom(ctx)
	if secs <= 0 {
		return STTHeaderBudgetFloor
	}
	b := STTHeaderBudgetFloor + time.Duration(secs*float64(sttHeaderBudgetPerAudioSecond))
	if b > STTHeaderBudgetCap {
		return STTHeaderBudgetCap
	}
	return b
}

type sttAudioUpperSecondsKey struct{}

// withSTTAudioUpperSeconds stamps a conservative (upper) audio duration used
// only to size the header wait — never for billing.
func withSTTAudioUpperSeconds(ctx context.Context, secs float64) context.Context {
	return context.WithValue(ctx, sttAudioUpperSecondsKey{}, secs)
}

func sttAudioUpperSecondsFrom(ctx context.Context) float64 {
	v, _ := ctx.Value(sttAudioUpperSecondsKey{}).(float64)
	return v
}

// sttSlowTimeoutError is the fallthrough sentinel for an STT upstream that
// sent no response header in time. errors.Is matches errDialFailedFallthrough,
// so the ErrorHandler and the cascade treat it exactly like a dial failure;
// SkipBreaker keeps it from counting as a breaker failure.
type sttSlowTimeoutError struct {
	budget time.Duration // 0 = the base transport's fixed timeout fired
}

func (e sttSlowTimeoutError) Error() string {
	if e.budget > 0 {
		return fmt.Sprintf("proxy: stt upstream sent no response headers within %s, fall through without breaker penalty", e.budget)
	}
	return "proxy: stt upstream response-header timeout, fall through without breaker penalty"
}

func (sttSlowTimeoutError) Is(target error) bool { return target == errDialFailedFallthrough }

// SkipBreaker implements breakerSkipper (dispatcher.go).
func (sttSlowTimeoutError) SkipBreaker() bool { return true }

// RoundTrip implements http.RoundTripper.
func (f fallthroughRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.headerBudget != nil {
		return f.roundTripWithBudget(r)
	}
	resp, err := f.base.RoundTrip(r)
	if err != nil {
		if ferr := f.classify(r.Context(), err, 0); ferr != nil {
			return nil, ferr
		}
	}
	return resp, err
}

// classify maps a base RoundTrip error to the fallthrough sentinel, or nil
// when it must pass through unchanged (post-dial failure, caller gone, or a
// timeout on a transport without preByteTimeout).
func (f fallthroughRoundTripper) classify(ctx context.Context, err error, budget time.Duration) error {
	if isConnectionClass(err) {
		// Pre-byte dial failure: substitute the typed sentinel so the
		// ErrorHandler suppresses the 502 write and the dispatcher re-routes.
		return errDialFailedFallthrough
	}
	if f.preByteTimeout && isPreByteTimeout(ctx, err) {
		if f.slowNotFailure {
			return sttSlowTimeoutError{budget: budget}
		}
		return errDialFailedFallthrough
	}
	return nil
}

// roundTripWithBudget runs the base RoundTrip under a per-request header
// wait. A timer cancels a child context when the budget elapses before the
// response headers arrive; on success the child context lives on until the
// response body is closed, so reading the body is never cut by the budget.
func (f fallthroughRoundTripper) roundTripWithBudget(r *http.Request) (*http.Response, error) {
	parent := r.Context()
	budget := f.headerBudget(parent)
	ctx, cancel := context.WithCancel(parent)

	var mu sync.Mutex
	headersIn, fired := false, false
	timer := time.AfterFunc(budget, func() {
		mu.Lock()
		defer mu.Unlock()
		if headersIn {
			return
		}
		fired = true
		cancel()
	})

	resp, err := f.base.RoundTrip(r.WithContext(ctx))

	mu.Lock()
	headersIn = true
	timedOut := fired
	mu.Unlock()
	timer.Stop()

	if timedOut {
		if resp != nil {
			_ = resp.Body.Close()
		}
		cancel()
		if parent.Err() != nil {
			// The caller gave up at the same moment: never cascade for a
			// response nobody will read (same rule as isPreByteTimeout).
			return nil, parent.Err()
		}
		return nil, sttSlowTimeoutError{budget: budget}
	}
	if err != nil {
		cancel()
		if ferr := f.classify(parent, err, 0); ferr != nil {
			return nil, ferr
		}
		return nil, err
	}
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnCloseBody releases the budget's child context once the response
// body is closed.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
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
	// net/http and x/net/http2 both report the header wait with this phrase and
	// neither exports the error value, so the string is the only stable handle.
	// Checked BEFORE the context sentinels (23-12): net/http's HTTP/1 errTimeout
	// implements Is(context.DeadlineExceeded) == true, so the sentinel check
	// below would wrongly classify a real ResponseHeaderTimeout as a caller
	// deadline. The caller's own deadline is already excluded by ctx.Err().
	if strings.Contains(err.Error(), "timeout awaiting response headers") {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
