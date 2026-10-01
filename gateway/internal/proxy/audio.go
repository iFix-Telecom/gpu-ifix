package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// NewAudioProxy constructs the reverse proxy for POST /v1/audio/transcriptions.
// Multipart body preservation: the director streams the audio file part via
// io.Copy so the bytes survive byte-identical. ResponseHeaderTimeout is 60s for
// Whisper. NO FlushInterval override — audio transcription never streams
// (Speaches returns the full JSON body in one response). Codex review
// [MEDIUM] 02-04 scope change. Body cap is enforced by `http.MaxBytesHandler`
// in cmd/gateway — we don't re-cap here.
//
// quick 260617-jod (SEED-018): the Director is BuildOpenAIWhisperDirector with
// an EMPTY authBearer + upstreamName "local-stt". The empty bearer skips the
// Authorization injection (BuildOpenAIWhisperDirector L102 — local-stt Speaches
// has no bearer); the resolver rewrites the multipart "model" form field for the
// local-stt upstream ((whisper, local-stt) → Systran/faster-whisper-large-v3 via
// migration 0029), so bringing the primary pod up no longer regresses STT to a
// 404 "Model 'whisper' is not installed". On a resolver miss the alias passes
// through unchanged and the pod 4xx's (breaker classifies 4xx as non-failure).
func NewAudioProxy(upstreamURL string, log *slog.Logger, resolver *models.Resolver, interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error) {
	u, err := parseStaticUpstream("audio", upstreamURL)
	if err != nil {
		return nil, err
	}
	// Quick 260930-uru: alvo fixo delegado ao proxy dinâmico, que preserva
	// transport (RES-13 fallthrough), ErrorHandler("stt") e o
	// sttRetryableStatusInterceptor prepended (Fix B Phase 22: status >= 400
	// cascateia antes de qualquer billing). cmd/gateway usa NewDynamicAudioProxy.
	return NewDynamicAudioProxy(staticTarget(u), log, resolver, interceptors...), nil
}

// sttRetryableStatusInterceptor raises errUpstreamRetryable when an STT upstream
// returns a status that should cascade to the next candidate rather than being
// returned verbatim.
type sttRetryableStatusInterceptor struct{}

// STTRetryableStatusInterceptor exposes the cascade interceptor to cmd/gateway
// so NON-FINAL tier-1 STT proxies (groq-whisper) also fall through to the next
// candidate on an upstream error instead of committing it to the client. The
// FINAL candidate (openai-whisper) deliberately does NOT compose it: with no
// next hop, the sentinel would only swap the upstream's real error for the
// generic exhaustion envelope, destroying the diagnostic.
func STTRetryableStatusInterceptor() ProxyResponseInterceptor {
	return sttRetryableStatusInterceptor{}
}

func (sttRetryableStatusInterceptor) Intercept(resp *http.Response) error {
	if resp == nil || !isRetryableSTTStatus(resp.StatusCode) {
		return nil
	}
	head := peekSTTErrorBody(resp)
	return &sttUpstreamStatusError{
		status:  resp.StatusCode,
		excerpt: sttErrorExcerpt(head),
		// Classified on the full peeked head (≤2 KiB), not the 300-char
		// excerpt, so a long traceback prefix cannot hide the OOM marker.
		oom: strings.Contains(strings.ToLower(string(head)), "out of memory"),
	}
}

const (
	// sttErrorPeekMax bounds how much of an error body the interceptor reads
	// to classify/log it. Error bodies are small JSON envelopes; a hostile or
	// buggy upstream streaming megabytes must not be buffered (Codex-style
	// bounded read).
	sttErrorPeekMax = 2 << 10 // 2 KiB
	// sttErrorExcerptMax caps the excerpt embedded in the error message so a
	// log line stays readable.
	sttErrorExcerptMax = 300
)

// sttUpstreamStatusError is the fallthrough error the STT interceptor emits
// for a status >= 400 (quick 261001-fjk). It exists so the fallthrough log —
// previously only "proxy interceptor #0: upstream retryable error" — carries
// the upstream status and the head of its error body (e.g. the speaches
// "CUDA failed with error out of memory"), and so the dispatcher can tell a
// per-request GPU OOM apart from real upstream degradation.
//
// errors.Is ALWAYS matches errUpstreamRetryable (cascade + ErrorHandler write
// suppression unchanged — decisão Pedro 2026-08-27 "qualquer erro deveria ter
// acionado os fallbacks"), and additionally matches errSTTResourceExhausted
// when the excerpt mentions "out of memory".
type sttUpstreamStatusError struct {
	status  int
	excerpt string
	oom     bool
}

func (e *sttUpstreamStatusError) Error() string {
	if e.excerpt == "" {
		return fmt.Sprintf("%s (stt upstream status %d)", errUpstreamRetryable.Error(), e.status)
	}
	return fmt.Sprintf("%s (stt upstream status %d: %s)", errUpstreamRetryable.Error(), e.status, e.excerpt)
}

func (e *sttUpstreamStatusError) Unwrap() error { return errUpstreamRetryable }

func (e *sttUpstreamStatusError) Is(target error) bool {
	return target == errUpstreamRetryable || (e.oom && target == errSTTResourceExhausted)
}

// sttStatusFrom extracts the upstream status carried by an STT fallthrough
// error, or 0 when err is not one.
func sttStatusFrom(err error) int {
	var se *sttUpstreamStatusError
	if errors.As(err, &se) {
		return se.status
	}
	return 0
}

// peekSTTErrorBody reads up to sttErrorPeekMax bytes of resp.Body and restores
// resp.Body so the bytes remain readable downstream (peeked head + untouched
// rest, original Closer preserved). Returns the peeked head.
func peekSTTErrorBody(resp *http.Response) []byte {
	if resp.Body == nil || resp.Body == http.NoBody {
		return nil
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, sttErrorPeekMax))
	orig := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), orig), orig}
	return head
}

// sttErrorExcerpt renders head as a sanitized single-line excerpt of at most
// sttErrorExcerptMax bytes (whitespace/newlines collapsed, invalid UTF-8
// dropped) — safe to embed in a structured log field.
func sttErrorExcerpt(head []byte) string {
	one := strings.Join(strings.Fields(strings.ToValidUTF8(string(head), "")), " ")
	if len(one) > sttErrorExcerptMax {
		one = strings.ToValidUTF8(one[:sttErrorExcerptMax], "")
	}
	return one
}

// isRetryableSTTStatus reports whether an STT upstream status warrants a cascade
// to the next candidate: EVERY status >= 400, including client-error 4xx.
//
// Rationale (debug stt-400-disco-cheio, 2026-08-27): the original Phase 22
// policy kept 400/401/403/413/415/422 terminal on the theory that "a retry to
// another upstream can't fix a genuinely bad request". In production that
// assumption failed: the local-stt pod's disk filled up, its multipart spool
// (>1 MiB uploads) started raising OSError, and FastAPI translated that into
// HTTP 400 — a pure upstream-side fault wearing a client-error status. Every
// call recording >~65s lost its transcription for 3 days while healthy tier-1
// candidates sat idle. The gateway cannot reliably distinguish "bad request"
// from "upstream bug reported as 4xx", and it has already validated the audio
// itself (RequestAudioSecondsMiddleware parses the file to derive duration),
// so the false-positive cost of cascading a genuinely bad request (a few extra
// upstream attempts, then the exhaustion envelope) is far cheaper than the
// false-negative cost (silent loss of the request's result). Decisão Pedro
// 2026-08-27: "qualquer erro deveria ter acionado os fallbacks".
func isRetryableSTTStatus(code int) bool {
	return code >= 400
}
