// Package proxy (stt_request_audio_middleware.go): request-audio duration
// stamping middleware.
//
// RequestAudioSecondsMiddleware parses a /v1/audio/transcriptions multipart
// request ONCE, derives the audio duration from the uploaded "file" part via
// DeriveAudioSeconds, and stamps it onto the request context with
// auditctx.WithRequestAudioSeconds. The Phase 16 billing producer
// (interceptor_usage.go applyAudioEmbedUsage) reads it as the ELSE branch of
// LOCKED CONTEXT DECISION #2 — used when the upstream response carries no
// `duration` field (the default {"text":"..."} transcription shape).
//
// REPLAY CONTRACT (T-16-07): consuming the body here would drain it before
// the reverse proxy forwards it AND before the dispatcher's prepareReplayBody
// replays it across the tier-1 cascade (dispatcher.go keys on r.GetBody). The
// middleware therefore:
//   - bounds the read to maxSTTBodyBuffer (25 MiB) via io.LimitReader — one
//     full in-RAM copy on the STT hot path, accepted per the threat model;
//   - restores BOTH r.Body AND r.GetBody from the buffered bytes, and syncs
//     ContentLength, so every downstream consumer (proxy forward + dispatcher
//     replay) sees the audio byte-identical.
//
// Non-multipart / non-transcription requests pass through untouched (no read,
// no stamp). An over-cap body (declared Content-Length > 25 MiB) is left
// untouched — it cannot meter via request-derivation but it also must not be
// buffered (memory bound), and the dispatcher already exempts it from replay.
package proxy

import (
	"bytes"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
)

// RequestAudioSecondsMiddleware returns an http middleware that stamps the
// request-derived audio duration onto the context for /v1/audio/transcriptions
// multipart POSTs. log may be nil (falls back to slog.Default()).
//
// defaultLang (STT_DEFAULT_LANGUAGE, e.g. "pt"): when non-empty, a
// `language=<defaultLang>` form field is injected into any transcription
// request that omits one, so whisper/gemini don't auto-detect the language
// (which mis-transcribes short/robotic pt-BR audio as English/Italian — the
// Phase 22 chatifix dictation bug). Empty disables the injection.
func RequestAudioSecondsMiddleware(log *slog.Logger, defaultLang string) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "STT_REQUEST_AUDIO")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seconds, ok := deriveRequestAudioSeconds(r, log, defaultLang)
			if ok && seconds > 0 {
				ctx := auditctx.WithRequestAudioSeconds(r.Context(), seconds)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// deriveRequestAudioSeconds reads the multipart "file" part (bounded), derives
// the duration, and restores r.Body + r.GetBody byte-identical. Returns
// (seconds, true) only when it parsed an audio part; (0, false) for
// pass-through routes / non-multipart / over-cap / parse failure.
//
// On every path that consumed r.Body it sets r.Body + r.GetBody back so the
// downstream proxy + dispatcher replay see the original bytes.
func deriveRequestAudioSeconds(r *http.Request, log *slog.Logger, defaultLang string) (float64, bool) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return 0, false
	}
	if !isAudioTranscriptionsPath(r.URL.Path) {
		return 0, false
	}
	ct := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return 0, false
	}
	boundary := params["boundary"]
	if boundary == "" {
		return 0, false
	}

	// Over-cap by declared length → do NOT buffer (memory bound, T-16-07).
	// The dispatcher already exempts >25 MiB bodies from replay buffering.
	if r.ContentLength > maxSTTBodyBuffer {
		return 0, false
	}

	// Read the whole body BOUNDED to maxSTTBodyBuffer (+1 to detect overflow).
	buf, rerr := io.ReadAll(io.LimitReader(r.Body, maxSTTBodyBuffer+1))
	_ = r.Body.Close()
	// Inject a default `language` field BEFORE restoring the body, so the pod +
	// every tier-1 upstream (and the dispatcher replay) forward it. Only when the
	// body was fully & cleanly buffered — never touch an over-cap / errored read.
	if rerr == nil && defaultLang != "" && int64(len(buf)) <= maxSTTBodyBuffer {
		if newBuf, changed := injectDefaultMultipartField(buf, boundary, "language", defaultLang); changed {
			buf = newBuf
			log.Debug("stt default language injected (client omitted it)", "language", defaultLang)
		}
	}
	// Always restore the body byte-identical (or with the injected field).
	restoreRequestBody(r, buf)
	if rerr != nil {
		log.Debug("stt request-audio read failed; forwarding unstamped", "err", rerr)
		return 0, false
	}
	if int64(len(buf)) > maxSTTBodyBuffer {
		// Streamed past the cap mid-read → too big to derive; forward as-is.
		log.Debug("stt request-audio body over cap; forwarding unstamped",
			"cap_bytes", maxSTTBodyBuffer)
		return 0, false
	}

	fileBytes, fileMime, ok := extractMultipartFile(buf, boundary)
	if !ok || len(fileBytes) == 0 {
		return 0, false
	}

	seconds := DeriveAudioSeconds(fileBytes, fileMime)
	if seconds > 0 {
		log.Debug("stt request-audio duration derived",
			"seconds", seconds, "file_bytes", len(fileBytes), "mime", fileMime)
	}
	return seconds, seconds > 0
}

// restoreRequestBody sets BOTH r.Body and r.GetBody from buf so the dispatcher
// replay (which keys on r.GetBody) and the immediate proxy forward both read
// the original bytes. Syncs ContentLength. Mirrors director.go
// rewriteRequestBody + dispatcher.go prepareReplayBody's GetBody contract.
func restoreRequestBody(r *http.Request, buf []byte) {
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	r.Header.Set("Content-Length", strconv.Itoa(len(buf)))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
}

// multipartHasField reports whether the buffered multipart body already carries
// a form field with the given name (e.g. the client-supplied "language").
func multipartHasField(buf []byte, boundary, name string) bool {
	mr := multipart.NewReader(bytes.NewReader(buf), boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			return false
		}
		fn := part.FormName()
		_ = part.Close()
		if fn == name {
			return true
		}
	}
}

// injectDefaultMultipartField appends a simple text form field just before the
// closing multipart delimiter, preserving every existing part (incl. the binary
// "file" audio part) byte-identical and keeping the SAME boundary — so the
// Content-Type header stays valid and the file bytes are untouched. Returns
// (buf, false) when the field already exists or the closing delimiter is absent.
func injectDefaultMultipartField(buf []byte, boundary, name, value string) ([]byte, bool) {
	if multipartHasField(buf, boundary, name) {
		return buf, false
	}
	closing := []byte("--" + boundary + "--")
	idx := bytes.LastIndex(buf, closing)
	if idx < 0 {
		return buf, false
	}
	part := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" +
		value + "\r\n"
	out := make([]byte, 0, len(buf)+len(part))
	out = append(out, buf[:idx]...)
	out = append(out, part...)
	out = append(out, buf[idx:]...)
	return out, true
}

// extractMultipartFile walks the buffered multipart body and returns the
// bytes + Content-Type of the "file" part (the audio payload). Mirrors the
// audit middleware's files["file"][0] selection, but reads from an in-memory
// buffer so the request body is untouched. Bounded by the already-capped buf.
func extractMultipartFile(buf []byte, boundary string) (data []byte, fileMime string, ok bool) {
	mr := multipart.NewReader(bytes.NewReader(buf), boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			return nil, "", false
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		fileMime = part.Header.Get("Content-Type")
		b, rerr := io.ReadAll(part)
		_ = part.Close()
		if rerr != nil {
			return nil, "", false
		}
		return b, fileMime, true
	}
}
