// Package proxy (streaming.go): peeks request body for "stream": true so
// the dispatcher can apply CONTEXT.md D-B4 (sensitive + streaming = fail
// fast pre-flight, no retry loop). Restores the body so downstream
// handlers can re-read it.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// IsStreamingRequest peeks at the request body to detect `"stream": true`
// without permanently consuming it. Restores the body into a fresh
// ReadCloser before returning so downstream handlers (Director, proxy)
// can read it again.
//
// Returns false on nil body, malformed JSON, or "stream" missing/false.
// Conservative: when in doubt, treat as non-streaming so the dispatcher
// can apply normal retry semantics.
func IsStreamingRequest(r *http.Request) bool {
	if r.Body == nil {
		return false
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	v, ok := m["stream"].(bool)
	return ok && v
}
