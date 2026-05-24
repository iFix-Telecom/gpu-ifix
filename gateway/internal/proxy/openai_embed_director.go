// Package proxy (openai_embed_director.go): Director extension for the
// openai-embed fallback upstream. Phase 06.9 — the "model" field is now
// schema-driven (resolver.Resolve) instead of hard-coded; dimensions=1024
// remains hard-coded as an invariant (BGE-M3 parity — BGE-M3 produces
// 1024-d vectors natively; OpenAI text-embedding-3-small projects to a
// configurable dimension and we cap at 1024 to match the rest of the
// embedding pipeline).
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

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// BuildOpenAIEmbedDirector returns a Director for the openai-embed
// fallback upstream. Wraps BuildDirector by:
//
//   - injecting `Authorization: Bearer <authBearer>` from the resolved
//     upstreams.UpstreamConfig.AuthBearer,
//   - rewriting the request body's "model" alias via resolver.Resolve(alias,
//     upstreamName) — Phase 06.9 makes this schema-driven instead of the
//     pre-06.9 hard-coded literal; the env-override-wins precedence per
//     D-06 is inherited transparently from Resolver.Resolve (Plan 02).
//     When UPSTREAM_EMBED_OPENAI_MODEL is set, the resolver returns the
//     env-overridden target; this director contains no separate env-read
//     logic.
//   - injecting dimensions=1024 (BGE-M3 parity invariant — explicitly NOT
//     schema-driven).
//
// All other fields including `input` are preserved byte-identical via
// map[string]json.RawMessage.
//
// log is used for R12 director error logging — DEBUG class on rewrite
// failures; never logs the request body (sensitive).
func BuildOpenAIEmbedDirector(
	upstream *url.URL,
	authBearer string,
	resolver *models.Resolver,
	upstreamName string,
	log *slog.Logger,
) func(*http.Request) {
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
		patched, perr := openaiEmbedRewrite(body, resolver, upstreamName)
		if perr != nil {
			// R12: DEBUG log — parse failure is non-fatal; downstream may
			// still 4xx, which the breaker's IsSuccessful filter classifies
			// as non-failure. Do NOT log the body (sensitive).
			if log != nil {
				log.Debug("openai_embed_director rewrite skipped",
					"upstream", upstreamName,
					"reason", "parse_failed",
					"error_class", fmt.Sprintf("%T", perr),
				)
			}
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
//
// Phase 06.9 — model name is sourced from the resolver per the incoming
// alias (was hard-coded "text-embedding-3-small" pre-06.9). When no
// alias is present in the body, the resolver is called with an empty
// alias string; it returns "" (passthrough) which writes an empty model
// field. OpenAI will reject the request; breaker classifies 4xx as
// non-failure per D-A4.
//
// dimensions=1024 stays hard-coded — BGE-M3 parity invariant. Only "model"
// is schema-driven (env-override per D-06 honored transparently via
// Resolver.Resolve).
func openaiEmbedRewrite(body []byte, resolver *models.Resolver, upstreamName string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	// Extract the incoming alias from body.model (if present).
	var alias string
	if rawAlias, ok := m["model"]; ok {
		_ = json.Unmarshal(rawAlias, &alias)
	}
	target := resolver.Resolve(alias, upstreamName)
	modelBytes, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	// Phase 06.9 — dimensions hard-coded to 1024 (BGE-M3 parity invariant);
	// only `model` is schema-driven (env-override per D-06 honored
	// transparently via Resolver.Resolve).
	dimBytes, err := json.Marshal(1024)
	if err != nil {
		return nil, err
	}
	m["model"] = modelBytes
	m["dimensions"] = dimBytes
	return json.Marshal(m)
}
