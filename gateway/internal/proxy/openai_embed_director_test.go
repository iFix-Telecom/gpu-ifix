package proxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestOpenAIEmbedDirector_ModelAndDimensions verifies the body rewrite:
// model becomes "text-embedding-3-small" and dimensions becomes 1024
// (BGE-M3 parity). The "input" field MUST survive byte-for-byte.
func TestOpenAIEmbedDirector_ModelAndDimensions(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenAIEmbedDirector(upstream, "sk-openai-test")

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
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenAIEmbedDirector(upstream, "sk-openai-xyz")

	body := []byte(`{"input":"x"}`)
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/embeddings", "application/json", body, nil, nil)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-openai-xyz" {
		t.Errorf("Authorization = %q, want Bearer sk-openai-xyz", got)
	}
}
