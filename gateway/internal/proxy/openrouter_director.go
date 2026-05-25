// Package proxy (openrouter_director.go): Director extension for the
// openrouter-chat upstream. Wraps BuildDirector by injecting the
// `Authorization: Bearer <key>` header AND rewriting the
// /v1/chat/completions request body to:
//
//  1. Rewrite "model" alias to the per-upstream target via the resolver
//     (Phase 06.9 — closes OR-FIX). The env-override-wins precedence per
//     D-06 lives inside Resolver.Resolve (Plan 02). When
//     UPSTREAM_LLM_OPENROUTER_MODEL is set, models.RewriteJSONModel returns
//     the env-overridden target transparently; this director contains no
//     separate env-read logic.
//  2. Inject {"provider":{"order":[...],"allow_fallbacks":<bool>}} per
//     CONTEXT.md D-C2 / 03-WAVE0-GATES.md (D-C1 amendment: Novita pin).
//  3. Inject {"stream_options":{"include_usage":true}} when client asked
//     for streaming (Pitfall 5 — usage chunks for cost attribution).
//
// R5 honesty: this director performs THREE sequential JSON unmarshal+marshal
// passes (RewriteJSONModel → injectProviderOrder → injectStreamOptionsIncludeUsage).
// Each pass is well-scoped and idempotent. A future refactor may combine
// into a single helper if profiling identifies allocation pressure; for
// now the multi-pass shape is honest and explicit.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// BuildOpenRouterDirector returns a Director for the openrouter-chat
// upstream. Extends proxy.BuildDirector by:
//
//   - injecting `Authorization: Bearer <authBearer>` header from the
//     resolved upstreams.UpstreamConfig.AuthBearer (env-resolved at
//     loader.Refresh time),
//   - rewriting the /v1/chat/completions JSON body's "model" alias via
//     resolver.Resolve(alias, upstreamName) — env-override-wins per D-06
//     handled transparently inside Resolver.Resolve, and
//   - injecting {"provider":{"order":[...],"allow_fallbacks":<bool>}} +
//     {"stream_options":{"include_usage":true}} (when streaming).
//
// Non-chat paths (e.g. an /v1/embeddings request mistakenly routed
// through this director) pass through unchanged — the body is left as-is.
//
// providerOrder MUST be the resolved order list (e.g. ["novita"]); the
// caller resolves UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER via config.
// allowFallbacks reflects UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS (default
// false per D-C2 / D-C4 — no fallback of fallback for chat).
//
// resolver + upstreamName drive the per-upstream model rewrite. The
// upstreamName is compile-time-known at the wire-up site in main.go
// (always "openrouter-chat" for this director). The env-override-wins
// precedence per D-06 lives inside Resolver.Resolve — see Plan 02 /
// resolver.go for the env→schema→passthrough chain.
//
// log is used for R12 director error logging — DEBUG/WARN classes on
// rewrite failures, never logs the request body (sensitive).
func BuildOpenRouterDirector(
	upstream *url.URL,
	authBearer string,
	providerOrder []string,
	allowFallbacks bool,
	resolver *models.Resolver,
	upstreamName string,
	log *slog.Logger,
) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r) // strips client auth, propagates X-Request-ID, rewrites URL
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
		}
		// HasSuffix (not equality) so the check survives BuildDirector's
		// upstream.Path join — for `https://openrouter.ai/api`, the rewritten
		// path becomes `/api/v1/chat/completions`. The defensive intent is
		// "only run body rewrite on chat requests"; suffix match preserves
		// that semantic across upstream prefixes.
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			return
		}
		if r.Body == nil {
			return
		}
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil || len(body) == 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}

		// PASS 1 — model rewrite via resolver (Phase 06.9 OR-FIX).
		// RewriteJSONModel is best-effort: on parse error it returns the
		// original body bytes; on resolver miss it returns the original
		// body bytes too (alias unchanged). Either way the body is safe
		// to feed into PASS 2.
		rewritten, _, err1 := models.RewriteJSONModel(body, resolver, upstreamName)
		if err1 != nil {
			// R12: DEBUG log — parse failure is non-fatal; downstream may
			// still 4xx, which the breaker's IsSuccessful filter classifies
			// as non-failure. Do NOT log the body (sensitive).
			if log != nil {
				log.Debug("openrouter_director model rewrite parse failed",
					"upstream", upstreamName,
					"error_class", fmt.Sprintf("%T", err1),
				)
			}
			rewritten = body
		}

		// PASS 2 — provider.order injection (D-C2).
		patched, err2 := injectProviderOrder(rewritten, providerOrder, allowFallbacks)
		if err2 != nil {
			// R12: WARN log — provider.order injection is the load-bearing
			// pin; failing means the request goes to OpenRouter without
			// our provider preference. Still best-effort forwarding —
			// breaker filters 4xx, and the upstream may still succeed.
			if log != nil {
				log.Warn("openrouter_director injectProviderOrder failed",
					"upstream", upstreamName,
					"error_class", fmt.Sprintf("%T", err2),
				)
			}
			// Parse failure → leave body unchanged. The upstream may still
			// 4xx the request; the breaker's IsSuccessful filter (D-A4)
			// classifies 4xx as non-failure so the breaker stays CLOSED.
			rewriteRequestBody(r, rewritten)
			return
		}

		// PASS 3 — stream_options.include_usage injection (Pitfall 5).
		// OpenRouter mirrors OpenAI: the final {"usage":...} chunk is
		// omitted on streaming responses unless stream_options.include_usage
		// is set. Inject defensively when the client asked to stream but
		// did NOT set the option themselves.
		patched = injectStreamOptionsIncludeUsage(patched)

		r.Body = io.NopCloser(bytes.NewReader(patched))
		r.ContentLength = int64(len(patched))
		r.Header.Set("Content-Length", strconv.Itoa(len(patched)))
	}
}

// injectProviderOrder merges {"provider":{"order":...,"allow_fallbacks":...}}
// into an existing JSON request body, preserving other fields by
// unmarshaling into map[string]json.RawMessage (raw bytes survive). If
// the caller already set "provider" we overwrite it — gateway-injected
// pin always wins over client-supplied.
func injectProviderOrder(body []byte, order []string, allowFallbacks bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	providerBytes, err := json.Marshal(map[string]any{
		"order":           order,
		"allow_fallbacks": allowFallbacks,
	})
	if err != nil {
		return nil, err
	}
	m["provider"] = providerBytes
	return json.Marshal(m)
}
