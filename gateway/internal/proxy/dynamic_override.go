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
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
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
	// Fix B (Phase 22): for the STT override (emergency_pod_stt), a retryable
	// upstream status (404 model-not-found, 408/425/429, 5xx) must cascade to
	// the next candidate — same as the static NewAudioProxy path. Prepended so a
	// failed pod response never bills and re-routes instead of being returned.
	if role == "stt" {
		interceptors = append([]ProxyResponseInterceptor{sttRetryableStatusInterceptor{}}, interceptors...)
	}
	// quick 260824-ucv Fix A: this constructor produces "emergency_pod_llm" —
	// the upstream that ACTUALLY serves chat in production and therefore the
	// one that emitted the 63 exceed_context_size_error in 2h (HANDOFF Part 2).
	// A tier-0 over-context 400/413 must cascade to tier-1 instead of reaching
	// the client. Prepended so a cascading response never bills.
	if role == "llm" {
		interceptors = append([]ProxyResponseInterceptor{chatOverContextInterceptor{log: log}}, interceptors...)
	}
	return &httputil.ReverseProxy{
		Director:      dynamicOverrideDirector(overrideURL),
		FlushInterval: flushInterval,
		// RES-13 / Plan 12-03: wrap so a pre-byte dial failure to the live
		// pod surfaces the sentinel the ErrorHandler suppresses → fallthrough.
		Transport:      fallthroughRoundTripper{base: transport},
		ErrorHandler:   ErrorHandler(role, log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
}

// dynamicOverrideSTTDirector returns a ReverseProxy Director for the STT
// primary/emergency override path (emergency_pod_stt). It first resolves the
// override target host via the same overrideURL() + url.Parse + BuildDirector
// sequence as dynamicOverrideDirector (URL/Host rewrite + client-auth strip +
// X-Request-ID), THEN — when the request is multipart/form-data — rewrites the
// "model" form field via the resolver against "local-stt".
//
// quick 260617-jod (SEED-018): the override pod runs the SAME Speaches that
// local-stt points at. The synthetic override name (emergency_pod_stt) is NOT in
// model_aliases, so resolving against it would miss → passthrough → literal
// "whisper" → 404. Resolving against "local-stt" hits the schema alias row
// (migration 0029) and yields Systran/faster-whisper-large-v3.
//
// The success / parse-error / duplicate-400 switch mirrors
// BuildOpenAIWhisperDirector: on parse error OR duplicate-model, the ORIGINAL
// body is forwarded unchanged (never 500) — the WhisperAbortGuard is not on this
// path, so a duplicate just forwards unchanged and the pod rejects it. Audio
// file bytes pass through byte-identical via rewriteMultipartModelViaResolver's
// io.Copy.
func dynamicOverrideSTTDirector(overrideURL func() (string, bool), resolver *models.Resolver, log *slog.Logger) func(*http.Request) {
	base := dynamicOverrideDirector(overrideURL)
	return func(r *http.Request) {
		base(r)
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") || r.Body == nil {
			return
		}
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil || len(body) == 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}
		newBody, newCT, statusCode, rerr := rewriteMultipartModelViaResolver(body, ct, resolver, "local-stt")
		switch {
		case statusCode == http.StatusBadRequest:
			// Duplicate-model: no WhisperAbortGuard on the override path. Forward
			// the ORIGINAL body unchanged — the pod will reject; gateway won't 500.
			if log != nil {
				log.Warn("override_stt_director duplicate model field; forwarding original",
					"upstream", "local-stt")
			}
			rewriteRequestBody(r, body)
		case rerr != nil:
			// Parse error — pass through original body unchanged.
			if log != nil {
				log.Debug("override_stt_director multipart parse failed; forwarding original",
					"upstream", "local-stt")
			}
			rewriteRequestBody(r, body)
		default:
			r.Body = io.NopCloser(bytes.NewReader(newBody))
			r.ContentLength = int64(len(newBody))
			r.Header.Set("Content-Type", newCT)
			r.Header.Set("Content-Length", strconv.Itoa(len(newBody)))
		}
	}
}

// NewDynamicOverrideSTTProxy builds the tier-0 STT reverse proxy whose target is
// resolved per request from overrideURL (the live primary/emergency pod URL) AND
// whose multipart "model" form field is rewritten via the resolver against
// "local-stt" (quick 260617-jod / SEED-018). It is the STT-aware sibling of
// NewDynamicOverrideProxy; the llm/tts override paths keep using the plain
// NewDynamicOverrideProxy (their bodies are JSON and must not be touched).
//
// role is fixed to "stt" for the ErrorHandler. flushInterval is 0 (transcription
// returns a single buffered JSON body). transport tunes timeouts. interceptors
// run in ModifyResponse exactly like the other proxies.
func NewDynamicOverrideSTTProxy(overrideURL func() (string, bool), flushInterval time.Duration, transport *http.Transport, resolver *models.Resolver, log *slog.Logger, interceptors ...ProxyResponseInterceptor) http.Handler {
	// Fix B (Phase 22) — quick 260723-sgx: same retryable-status cascade as the
	// generic NewDynamicOverrideProxy (L58-60) and the static NewAudioProxy. The
	// Phase 22 fix landed only on the generic constructor; this STT-aware sibling
	// (the one actually registered as "emergency_pod_stt", main.go) was missed —
	// a pod 5xx (e.g. CUDA OOM on long audio) was committed raw to the client
	// instead of cascading to gemini-stt/openai-whisper. Prepended so a failed
	// pod response never bills and re-routes instead of being returned.
	interceptors = append([]ProxyResponseInterceptor{sttRetryableStatusInterceptor{}}, interceptors...)
	return &httputil.ReverseProxy{
		Director:      dynamicOverrideSTTDirector(overrideURL, resolver, log),
		FlushInterval: flushInterval,
		// RES-13 / Plan 12-03: wrap so a pre-byte dial failure to the live
		// pod surfaces the sentinel the ErrorHandler suppresses → fallthrough.
		Transport:      fallthroughRoundTripper{base: transport},
		ErrorHandler:   ErrorHandler("stt", log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
}
