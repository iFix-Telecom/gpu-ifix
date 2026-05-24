package models

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// RewriteJSONModel attempts to replace the "model" field in a JSON body
// with the resolved target. Returns the (possibly unchanged) body bytes,
// a bool indicating whether a "model" field was found, and any parse
// error. Callers that pass an unreadable body receive (body, false, err).
//
// Design choice: we unmarshal into map[string]json.RawMessage so all
// other fields pass through byte-for-byte. Only the model key is
// touched. Ordering is inevitably Go-map-nondeterministic on re-encode,
// but the OpenAI API is order-independent so downstream clients don't
// notice.
//
// Phase 06.9: the `upstream` arg is now the upstream NAME (e.g.
// "openrouter-chat", "local-llm"), NOT the role tag. Callers within
// Directors should pass the construction-time-known upstream name. The
// env-override-wins layer (D-06) lives inside Resolver.Resolve —
// RewriteJSONModel transparently receives the env-overridden target
// when applicable; no separate env-read logic is needed here.
func RewriteJSONModel(body []byte, resolver *Resolver, upstream string) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body, false, err
	}
	raw, ok := m["model"]
	if !ok {
		return body, false, nil
	}
	var alias string
	if err := json.Unmarshal(raw, &alias); err != nil {
		return body, false, err
	}
	target := resolver.Resolve(alias, upstream)
	if target == alias {
		// No rewrite needed — return the original body bytes so tests can
		// assert byte-equality for unknown aliases.
		return body, true, nil
	}
	newModel, err := json.Marshal(target)
	if err != nil {
		return body, true, err
	}
	m["model"] = newModel
	out, err := json.Marshal(m)
	if err != nil {
		return body, true, err
	}
	return out, true, nil
}

// Handler wraps an inner handler so that the incoming request body is
// read, JSON-rewritten for the model alias, and passed forward with a
// fresh body reader. Intended for /v1/chat/completions and
// /v1/embeddings. Audio (multipart) route skips this; aliasing for
// Whisper happens pod-side.
//
// Deprecated: use per-upstream resolution inside each tier-1 Director
// (see proxy/openrouter_director.go for the pattern). This middleware
// ran at request edge BEFORE dispatcher resolution, which collapses all
// per-upstream targets onto a single rewrite — incompatible with the
// Phase 06.9 per-upstream-name resolver. Plan 06.9-03 removes the
// main.go callers; the function body is preserved here for backward
// compatibility while downstream wiring is migrated.
func Handler(resolver *Resolver, upstream string, inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			inner.ServeHTTP(w, r)
			return
		}
		// Read up to the body cap enforced at server level (25 MiB).
		body, err := io.ReadAll(r.Body)
		if err != nil {
			inner.ServeHTTP(w, r) // best-effort; let proxy error surface
			return
		}
		rewritten, _, _ := RewriteJSONModel(body, resolver, upstream)
		r.Body = io.NopCloser(bytes.NewReader(rewritten))
		r.ContentLength = int64(len(rewritten))
		r.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
		inner.ServeHTTP(w, r)
	})
}
