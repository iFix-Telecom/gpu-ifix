// Package proxy implements single-upstream reverse proxies for the three
// OpenAI-compatible routes: /v1/chat/completions, /v1/embeddings, and
// /v1/audio/transcriptions. All three share Director behavior (auth-header
// stripping + X-Request-ID propagation) and ErrorHandler behavior
// (OpenAI envelope 502). SSE streaming is enabled for chat via
// FlushInterval: -1 on the ReverseProxy. Multipart is preserved for audio
// by leaving Content-Type untouched.
//
// Phase 3 introduces failover / circuit breakers; this package gets a
// new `NewChainProxy` constructor there and the single-upstream proxies
// become the primary tier of that chain.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// clientAuthHeaders are stripped before any outbound request to upstream.
// The pod trusts the gateway, not the caller — letting client headers
// through would defeat the gateway as a trust boundary. Case variants
// (x-api-key, X-Api-Key) are handled by Go's canonical MIME header
// matching automatically at Header.Del time.
var clientAuthHeaders = []string{
	"Authorization",
	"X-API-Key",
	"Cookie",
	"Proxy-Authorization",
}

// BuildDirector returns a Director function suitable for
// httputil.ReverseProxy for a single upstream URL.
func BuildDirector(upstream *url.URL) func(*http.Request) {
	return func(r *http.Request) {
		// Rewrite URL to upstream.
		r.URL.Scheme = upstream.Scheme
		r.URL.Host = upstream.Host
		// Join upstream.Path with the inbound request path so upstreams
		// that live behind a base prefix (e.g. OpenRouter at
		// `https://openrouter.ai/api`) receive the request at
		// `<upstream.Path>/v1/chat/completions`. Local pods configure
		// their URL with an empty path (`http://host:port`), in which
		// case the inbound `/v1/...` path is preserved 1:1.
		//
		// path.Join collapses duplicate slashes; if the inbound was the
		// root path, preserve an explicit trailing slash to keep
		// directory-style endpoints addressable.
		if upstream.Path != "" && upstream.Path != "/" {
			joined := path.Join(upstream.Path, r.URL.Path)
			if strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(joined, "/") {
				joined += "/"
			}
			r.URL.Path = joined
		}
		r.Host = upstream.Host

		// Strip client auth headers so pod never sees them.
		for _, h := range clientAuthHeaders {
			r.Header.Del(h)
		}

		// Replace whatever X-Request-ID came in (possibly client-supplied)
		// with the gateway's authoritative request id so pod logs correlate
		// on OUR id — not on a client-controlled value.
		if rid := httpx.RequestIDFrom(r.Context()); rid != "" {
			r.Header.Set("X-Request-ID", rid)
		} else {
			r.Header.Del("X-Request-ID")
		}
	}
}

// injectStreamOptionsIncludeUsage rewrites a /v1/chat/completions JSON body
// to guarantee `"stream_options": {"include_usage": true}` when the client
// asked for `"stream": true` (Pitfall 5 — usage chunks are required for
// cost attribution on streaming responses; OpenRouter/OpenAI omit the
// final usage block unless this flag is set).
//
// Semantics:
//   - `stream=false` or absent → body returned unchanged.
//   - `stream=true` with no stream_options → injects {"include_usage":true}.
//   - `stream=true` with a stream_options object already present →
//     sets include_usage=true only if absent, preserving any other
//     client-supplied fields.
//   - Malformed JSON → returned unchanged (upstream will 4xx; breaker's
//     IsSuccessful treats 4xx as non-failure so breaker stays CLOSED).
//
// Callers are expected to run this AFTER any other body rewrites (e.g.
// OpenRouter's provider.order injection) so they share one allocation
// cycle — see BuildOpenRouterDirector.
func injectStreamOptionsIncludeUsage(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	streamRaw, ok := m["stream"]
	if !ok {
		return body
	}
	var streaming bool
	if err := json.Unmarshal(streamRaw, &streaming); err != nil || !streaming {
		return body
	}
	// Merge into an existing stream_options object if present, otherwise
	// insert a fresh one.
	var opts map[string]json.RawMessage
	if existing, present := m["stream_options"]; present {
		if err := json.Unmarshal(existing, &opts); err != nil || opts == nil {
			opts = map[string]json.RawMessage{}
		}
	} else {
		opts = map[string]json.RawMessage{}
	}
	if _, hasInclude := opts["include_usage"]; !hasInclude {
		opts["include_usage"] = json.RawMessage("true")
	} else {
		// Already set by client; do not override.
		return body
	}
	optsBytes, err := json.Marshal(opts)
	if err != nil {
		return body
	}
	m["stream_options"] = optsBytes
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// rewriteRequestBody replaces r.Body with a fresh io.NopCloser over the
// given bytes AND syncs ContentLength + Content-Length header so upstream
// sees consistent metadata. Shared helper for the director body-rewrite
// paths (OpenRouter provider.order + stream_options.include_usage).
func rewriteRequestBody(r *http.Request, b []byte) {
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	r.Header.Set("Content-Length", strconv.Itoa(len(b)))
}
