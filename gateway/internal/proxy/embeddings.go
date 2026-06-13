package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// NewEmbeddingsProxy constructs the reverse proxy for POST /v1/embeddings.
// NO FlushInterval override — embeddings never stream; default (0 =
// buffered) is correct and reduces test flakiness by ensuring the whole
// response body lands in one Write from the client POV. Codex review
// [MEDIUM] 02-04 scope change.
func NewEmbeddingsProxy(upstreamURL string, log *slog.Logger, interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy/embeddings: parse %q: %w", upstreamURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy/embeddings: invalid upstream url %q", upstreamURL)
	}
	rp := &httputil.ReverseProxy{
		Director: BuildDirector(u),
		// FlushInterval deliberately omitted (default 0 = buffered)
		// RES-13 / Plan 12-03: fallthroughRoundTripper surfaces pre-byte
		// dial failures as the sentinel the ErrorHandler suppresses so the
		// dispatcher re-routes to tier-1.
		Transport: fallthroughRoundTripper{base: &http.Transport{
			MaxIdleConns:          50,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		}},
		ErrorHandler:   ErrorHandler("embed", log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
	return rp, nil
}
