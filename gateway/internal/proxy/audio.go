package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

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
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy/audio: parse %q: %w", upstreamURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy/audio: invalid upstream url %q", upstreamURL)
	}
	rp := &httputil.ReverseProxy{
		Director: BuildOpenAIWhisperDirector(u, "", resolver, "local-stt", log),
		// FlushInterval deliberately omitted (default 0 = buffered)
		// RES-13 / Plan 12-03: wrap the base Transport with
		// fallthroughRoundTripper so a pre-byte connection-class dial failure
		// surfaces errDialFailedFallthrough, which the sentinel-aware
		// ErrorHandler suppresses → the dispatcher re-routes to tier-1
		// (over-cap STT bodies are exempt from fallthrough — see dispatcher).
		Transport: fallthroughRoundTripper{base: &http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}},
		ErrorHandler:   ErrorHandler("stt", log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
	return rp, nil
}
