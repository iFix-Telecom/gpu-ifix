package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// NewAudioProxy constructs the reverse proxy for POST /v1/audio/transcriptions.
// Multipart body preservation: BuildDirector does NOT touch Content-Type,
// so the boundary parameter survives. ResponseHeaderTimeout is 60s for
// Whisper. NO FlushInterval override — audio transcription never streams
// (Speaches returns the full JSON body in one response). Codex review
// [MEDIUM] 02-04 scope change. Body cap is enforced by `http.MaxBytesHandler`
// in cmd/gateway — we don't re-cap here.
func NewAudioProxy(upstreamURL string, log *slog.Logger, interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy/audio: parse %q: %w", upstreamURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy/audio: invalid upstream url %q", upstreamURL)
	}
	rp := &httputil.ReverseProxy{
		Director: BuildDirector(u),
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
