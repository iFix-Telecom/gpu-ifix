package proxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// TestOpenAIEmbedDirector_ModelAndDimensions verifies the body rewrite:
// model becomes "text-embedding-3-small" (sourced from the resolver in
// Phase 06.9 — was hard-coded pre-06.9) and dimensions becomes 1024
// (BGE-M3 parity, still hard-coded — invariant). The "input" field MUST
// survive byte-for-byte.
func TestOpenAIEmbedDirector_ModelAndDimensions(t *testing.T) {
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"bge-m3", "openai-embed"}: "text-embedding-3-small",
	})
	director := BuildOpenAIEmbedDirector(upstream, "sk-openai-test", resolver, "openai-embed", discardLogger())

	body := []byte(`{"input":["alpha","beta"],"model":"bge-m3","encoding_format":"float"}`)
	_, patched := applyDirector(t, director, http.MethodPost, "/v1/embeddings", "application/json", body, nil, nil)

	var m map[string]any
	if err := json.Unmarshal(patched, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got, _ := m["model"].(string); got != "text-embedding-3-small" {
		t.Errorf("model = %q, want text-embedding-3-small", got)
	}
	dims, _ := m["dimensions"].(float64)
	if int(dims) != 1024 {
		t.Errorf("dimensions = %v, want 1024", m["dimensions"])
	}
	// Input array preserved.
	in, _ := m["input"].([]any)
	if len(in) != 2 || in[0] != "alpha" || in[1] != "beta" {
		t.Errorf("input lost or mutated: %v", m["input"])
	}
	// encoding_format preserved (other arbitrary fields survive).
	if got, _ := m["encoding_format"].(string); got != "float" {
		t.Errorf("encoding_format = %q, want float", got)
	}
}

// TestOpenAIEmbedDirector_InjectsAuthBearer asserts Authorization is set.
func TestOpenAIEmbedDirector_InjectsAuthBearer(t *testing.T) {
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(nil)
	director := BuildOpenAIEmbedDirector(upstream, "sk-openai-xyz", resolver, "openai-embed", discardLogger())

	body := []byte(`{"input":"x"}`)
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/embeddings", "application/json", body, nil, nil)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-openai-xyz" {
		t.Errorf("Authorization = %q, want Bearer sk-openai-xyz", got)
	}
}

// --- Phase 06.9 Plan 03 NEW tests ---

// TestOpenAIEmbedDirector_RewritesModelFromResolver — director sources the
// model name from the resolver (NOT the hard-coded literal). Body's
// "input" field is preserved; "dimensions" is still 1024 (invariant).
func TestOpenAIEmbedDirector_RewritesModelFromResolver(t *testing.T) {
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"bge-m3", "openai-embed"}: "text-embedding-3-small",
	})
	director := BuildOpenAIEmbedDirector(upstream, "sk-openai-test", resolver, "openai-embed", discardLogger())

	body := []byte(`{"model":"bge-m3","input":["x"]}`)
	_, patched := applyDirector(t, director, http.MethodPost, "/v1/embeddings", "application/json", body, nil, nil)

	var m map[string]any
	if err := json.Unmarshal(patched, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got, _ := m["model"].(string); got != "text-embedding-3-small" {
		t.Errorf("model = %q, want text-embedding-3-small (from resolver)", got)
	}
	dims, _ := m["dimensions"].(float64)
	if int(dims) != 1024 {
		t.Errorf("dimensions = %v, want 1024", m["dimensions"])
	}
	in, _ := m["input"].([]any)
	if len(in) != 1 || in[0] != "x" {
		t.Errorf("input = %v, want [x]", m["input"])
	}
}

// TestOpenAIEmbedDirector_DimensionsStayHardcoded — even when the resolver
// returns a different model name, dimensions is still 1024. The dimensions
// hard-code is an invariant (BGE-M3 parity) — schema-driven model selection
// is independent of the dimensions injection.
func TestOpenAIEmbedDirector_DimensionsStayHardcoded(t *testing.T) {
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"bge-m3", "openai-embed"}: "text-embedding-3-large",
	})
	director := BuildOpenAIEmbedDirector(upstream, "sk-openai-test", resolver, "openai-embed", discardLogger())

	body := []byte(`{"model":"bge-m3","input":["a"]}`)
	_, patched := applyDirector(t, director, http.MethodPost, "/v1/embeddings", "application/json", body, nil, nil)

	var m map[string]any
	if err := json.Unmarshal(patched, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got, _ := m["model"].(string); got != "text-embedding-3-large" {
		t.Errorf("model = %q, want text-embedding-3-large (resolver target)", got)
	}
	dims, _ := m["dimensions"].(float64)
	if int(dims) != 1024 {
		t.Errorf("dimensions = %v, want 1024 (BGE-M3 parity invariant)", m["dimensions"])
	}
}

// TestOpenAIEmbedDirector_ResolverMissPassesAliasThrough — empty resolver;
// body keeps the alias "bge-m3" as model + still injects dimensions=1024.
// Upstream OpenAI will reject "bge-m3" as an unknown model with 400;
// breaker classifies 4xx as non-failure (D-A4).
func TestOpenAIEmbedDirector_ResolverMissPassesAliasThrough(t *testing.T) {
	t.Setenv("UPSTREAM_EMBED_OPENAI_MODEL", "")
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(nil)
	director := BuildOpenAIEmbedDirector(upstream, "sk-openai-test", resolver, "openai-embed", discardLogger())

	body := []byte(`{"model":"bge-m3","input":["x"]}`)
	_, patched := applyDirector(t, director, http.MethodPost, "/v1/embeddings", "application/json", body, nil, nil)

	var m map[string]any
	if err := json.Unmarshal(patched, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got, _ := m["model"].(string); got != "bge-m3" {
		t.Errorf("model = %q, want bge-m3 (alias unchanged on resolver miss)", got)
	}
	dims, _ := m["dimensions"].(float64)
	if int(dims) != 1024 {
		t.Errorf("dimensions = %v, want 1024 (still injected on resolver miss)", m["dimensions"])
	}
}
