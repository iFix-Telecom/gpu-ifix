// Package proxy (tts_request_chars_middleware.go): TTS input-character
// stamping middleware (quick-261007-t9f).
//
// TTSRequestCharsMiddleware reads the POST /v1/audio/speech JSON body ONCE
// (bounded), counts the characters (runes) of the `input` field and stamps the
// count on the request context via auditctx.WithRequestTTSChars. The usage
// interceptor's TTS branch records that count as tokens_in on route "tts":
// on this route one "token" = one input character (D-P1), priced under the
// tts-1 reference model (ttsBillingModel). The speech response is binary, so
// the request is the only place the billable unit can be measured.
//
// Body contract (mirrors stt_request_audio_middleware.go):
//   - read is bounded to ttsCharsBodyCap (64 KiB, T-t9f-01);
//   - within the cap, r.Body AND r.GetBody are restored from the buffered
//     bytes so the reverse proxy and the dispatcher's tier fallback replay see
//     the body byte-identical;
//   - over the cap, nothing is stamped and r.Body becomes buffered-prefix +
//     remaining stream (io.MultiReader) — never truncated, never fully
//     buffered here (the dispatcher handles replay buffering on its own).
//
// The middleware never writes an error response: parse failures are logged at
// Debug and the request proceeds unchanged.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
)

// ttsCharsBodyCap bounds the bytes read to count TTS characters.
const ttsCharsBodyCap = 64 * 1024

// TTSRequestCharsMiddleware returns the middleware described in the file doc.
// log may be nil (falls back to slog.Default()).
func TTSRequestCharsMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "TTS_REQUEST_CHARS")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if chars := stampTTSChars(r, log); chars > 0 {
				r = r.WithContext(auditctx.WithRequestTTSChars(r.Context(), chars))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// stampTTSChars reads the bounded body, restores it and returns the rune count
// of `input` (0 when absent / invalid / over the cap).
func stampTTSChars(r *http.Request, log *slog.Logger) int64 {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return 0
	}
	if r.ContentLength > ttsCharsBodyCap {
		return 0
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, ttsCharsBodyCap+1))
	if int64(len(buf)) > ttsCharsBodyCap {
		// Over the cap: forward prefix + the unread remainder untouched.
		rest := r.Body
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(buf), rest), rest}
		log.Debug("tts body over cap; chars not stamped", "cap", ttsCharsBodyCap)
		return 0
	}
	_ = r.Body.Close()
	restoreRequestBody(r, buf)
	if err != nil {
		log.Debug("tts body read failed; chars not stamped", "err", err)
		return 0
	}
	var req ttsSpeechRequest
	if jerr := json.Unmarshal(buf, &req); jerr != nil {
		log.Debug("tts body is not valid JSON; chars not stamped", "err", jerr)
		return 0
	}
	return int64(utf8.RuneCountInString(req.Input))
}
