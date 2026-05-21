// Package proxy (dynamic_override.go) — Phase 06.7.
//
// Dynamic tier-0 override proxies. When the primary/emergency reconciler calls
// loader.OverrideTier0(role, podURL), loader.Resolve(role, 0) returns the
// synthetic upstream name "emergency_pod_<role>" with the live pod URL. The
// dispatcher then looks up cfg.Proxies["emergency_pod_<role>"] — so a proxy MUST
// be registered under that name for every dynamic-override role (llm, stt, tts),
// or the dispatcher 503s with "Upstream proxy not registered".
//
// Unlike the static tier-0 proxies (NewChatProxy/NewAudioProxy/NewTTSProxy, whose
// Director is fixed at boot to UPSTREAM_*_URL), these read the override URL from
// the loader on EVERY request — the Vast pod's public URL changes per lifecycle.
package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// dynamicOverrideDirector returns a ReverseProxy Director that resolves the
// target host from overrideURL() per request, then applies the standard
// BuildDirector rewrites (URL/Host, strip client auth, set X-Request-ID). When
// overrideURL returns ok=false or an unparseable URL, the request host is left
// empty so the transport fails fast and ErrorHandler emits an OpenAI-shaped 502.
func dynamicOverrideDirector(overrideURL func() (string, bool)) func(*http.Request) {
	return func(r *http.Request) {
		target, ok := overrideURL()
		if !ok || target == "" {
			return
		}
		u, err := url.Parse(target)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return
		}
		BuildDirector(u)(r)
	}
}

// NewDynamicOverrideProxy builds a tier-0 reverse proxy whose target is resolved
// per request from overrideURL (the live pod URL for role). role labels the
// ErrorHandler. flushInterval mirrors the static proxy's streaming behavior
// (-1 for SSE/chat, 0 for buffered audio/tts). transport tunes timeouts.
// interceptors run in ModifyResponse exactly like the static proxies.
func NewDynamicOverrideProxy(role string, overrideURL func() (string, bool), flushInterval time.Duration, transport *http.Transport, log *slog.Logger, interceptors ...ProxyResponseInterceptor) http.Handler {
	return &httputil.ReverseProxy{
		Director:       dynamicOverrideDirector(overrideURL),
		FlushInterval:  flushInterval,
		Transport:      transport,
		ErrorHandler:   ErrorHandler(role, log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
}
