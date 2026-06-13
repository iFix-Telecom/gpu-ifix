package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// NewChatProxy constructs the reverse proxy for POST /v1/chat/completions.
// FlushInterval: -1 is MANDATORY here — without it SSE chunks buffer and
// clients don't see tokens arrive as they're generated. Only chat gets
// per-chunk flush (embeddings + audio use default buffered behavior per
// Codex review [MEDIUM] 02-04).
//
// Interceptors plug into ModifyResponse via the formal extension point
// defined in interceptor.go (Codex review [HIGH/MEDIUM] 02-05). Plan 02-05
// passes its AuditInterceptor here; 02-04 callers that need no hooks pass
// nothing and get a no-op ModifyResponse.
func NewChatProxy(upstreamURL string, log *slog.Logger, interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy/chat: parse %q: %w", upstreamURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy/chat: invalid upstream url %q (missing scheme or host)", upstreamURL)
	}

	rp := &httputil.ReverseProxy{
		Director:      BuildDirector(u),
		FlushInterval: -1, // per-chunk flush for SSE (chat-only)
		// RES-13 / Plan 12-03: wrap the base Transport with
		// fallthroughRoundTripper so a pre-byte connection-class dial failure
		// surfaces errDialFailedFallthrough, which the sentinel-aware
		// ErrorHandler suppresses → the dispatcher re-routes to tier-1.
		Transport: fallthroughRoundTripper{base: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second, // cold first-token <=20s
			// No ReadTimeout on transport — SSE streams are open-ended.
		}},
		ErrorHandler:   ErrorHandler("llm", log),
		ModifyResponse: ComposeInterceptors(interceptors...), // Codex [HIGH/MEDIUM] 02-05
	}
	return rp, nil
}
