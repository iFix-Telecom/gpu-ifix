// Package proxy (openai_embed_director.go): Director extension for the
// openai-embed fallback upstream. Rewrites the request body to use
// `model="text-embedding-3-small"` + `dimensions=1024` so the embedding
// vectors retain BGE-M3 dimensional parity with the local fallback (BGE-M3
// produces 1024-d vectors natively; OpenAI text-embedding-3-small can
// project to a configurable dimension, defaulting to 1536 — we cap at
// 1024 to match the rest of the embedding pipeline).
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// BuildOpenAIEmbedDirector returns a Director for the openai-embed
// fallback upstream. Wraps BuildDirector by:
//
//   - injecting `Authorization: Bearer <authBearer>` from the resolved
//     upstreams.UpstreamConfig.AuthBearer, and
//   - rewriting the request body: sets model="text-embedding-3-small"
//     and dimensions=1024 (BGE-M3 parity). All other fields including
//     `input` are preserved byte-identical via map[string]json.RawMessage.
func BuildOpenAIEmbedDirector(upstream *url.URL, authBearer string) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r)
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
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
		patched, err := openaiEmbedRewrite(body)
		if err != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(patched))
		r.ContentLength = int64(len(patched))
		r.Header.Set("Content-Length", strconv.Itoa(len(patched)))
	}
}

// openaiEmbedRewrite swaps model + dimensions in the JSON body. Other
// fields (input, encoding_format, user, etc.) survive byte-for-byte
// because we unmarshal into map[string]json.RawMessage.
func openaiEmbedRewrite(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	modelBytes, err := json.Marshal("text-embedding-3-small")
	if err != nil {
		return nil, err
	}
	dimBytes, err := json.Marshal(1024)
	if err != nil {
		return nil, err
	}
	m["model"] = modelBytes
	m["dimensions"] = dimBytes
	return json.Marshal(m)
}
