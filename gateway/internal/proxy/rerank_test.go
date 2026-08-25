// Package proxy (rerank_test.go): quick 260825 — NewRerankProxy passthrough
// contract. Mirrors the embeddings proxy test shape: JSON body forwarded
// unchanged to <upstream>/v1/rerank, client auth stripped by BuildDirector,
// JSON response relayed verbatim; invalid upstream URLs fail construction.
package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRerankProxy_PassthroughJSON(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}],"model":"bge-reranker-v2-m3"}`))
	}))
	defer upstream.Close()

	rp, err := NewRerankProxy(upstream.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewRerankProxy: %v", err)
	}

	body := `{"model":"bge-reranker-v2-m3","query":"balcão","documents":["doc a","doc b"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/rerank", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Client tenant bearer MUST be stripped by BuildDirector before the
	// request reaches the upstream (same contract as chat/embed/tts).
	req.Header.Set("Authorization", "Bearer ifix_sk_client_key")
	rec := httptest.NewRecorder()
	rp.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/rerank" {
		t.Errorf("upstream path = %q, want /v1/rerank", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("upstream Authorization = %q, want stripped (empty)", gotAuth)
	}
	if gotBody["query"] != "balcão" {
		t.Errorf("forwarded query = %v, want balcão", gotBody["query"])
	}
	docs, ok := gotBody["documents"].([]any)
	if !ok || len(docs) != 2 {
		t.Errorf("forwarded documents = %v, want 2 items", gotBody["documents"])
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	results, ok := out["results"].([]any)
	if !ok || len(results) != 2 {
		t.Errorf("response results = %v, want 2 items relayed verbatim", out["results"])
	}
}

func TestNewRerankProxy_InvalidURL(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRerankProxy("://not-a-url", log); err == nil {
		t.Error("NewRerankProxy(://not-a-url) = nil error, want parse failure")
	}
	if _, err := NewRerankProxy("no-scheme-host", log); err == nil {
		t.Error("NewRerankProxy(no-scheme-host) = nil error, want invalid-url failure")
	}
}
