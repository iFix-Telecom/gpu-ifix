// Package proxy (openrouter_director.go): Director extension for the
// openrouter-chat upstream. Wraps BuildDirector by injecting the
// `Authorization: Bearer <key>` header AND rewriting the
// /v1/chat/completions request body to add
// {"provider":{"order":[...],"allow_fallbacks":<bool>}} per CONTEXT.md
// D-C2 / 03-WAVE0-GATES.md (D-C1 amendment: Novita pin, NOT Fireworks).
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// BuildOpenRouterDirector returns a Director for the openrouter-chat
// upstream. Extends proxy.BuildDirector by:
//
//   - injecting `Authorization: Bearer <authBearer>` header from the
//     resolved upstreams.UpstreamConfig.AuthBearer (env-resolved at
//     loader.Refresh time), and
//   - rewriting the /v1/chat/completions request body to add
//     {"provider":{"order":[...],"allow_fallbacks":<bool>}} via JSON
//     body rewrap (Pattern 6 in 03-RESEARCH.md), preserving all other
//     fields byte-for-byte.
//
// Non-chat paths (e.g. an /v1/embeddings request mistakenly routed
// through this director) pass through unchanged — the body is left as-is.
//
// providerOrder MUST be the resolved order list (e.g. ["novita"]); the
// caller resolves UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER via config.
// allowFallbacks reflects UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS (default
// false per D-C2 / D-C4 — no fallback of fallback for chat).
func BuildOpenRouterDirector(upstream *url.URL, authBearer string, providerOrder []string, allowFallbacks bool) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r) // strips client auth, propagates X-Request-ID, rewrites URL
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
		}
		if r.URL.Path != "/v1/chat/completions" {
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
		patched, err := injectProviderOrder(body, providerOrder, allowFallbacks)
		if err != nil {
			// Parse failure → leave body unchanged. The upstream may still
			// 4xx the request; the breaker's IsSuccessful filter (D-A4)
			// classifies 4xx as non-failure so the breaker stays CLOSED
			// and the operator sees a normal client error in the audit log.
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}
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
