package models

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRewriteJSONModel_ReplacesField(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}: "pod-qwen",
	})
	body := []byte(`{"model":"qwen","messages":[]}`)
	out, found, err := RewriteJSONModel(body, r, "llm")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if got["model"] != "pod-qwen" {
		t.Errorf("model=%v; want pod-qwen", got["model"])
	}
	if _, ok := got["messages"]; !ok {
		t.Errorf("messages field lost")
	}
}

func TestRewriteJSONModel_NoModelField(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{})
	body := []byte(`{"input":["x"]}`)
	out, found, err := RewriteJSONModel(body, r, "embed")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if found {
		t.Fatalf("expected found=false")
	}
	// Body should be byte-equal.
	if !bytes.Equal(out, body) {
		t.Errorf("body mutated: %q vs %q", out, body)
	}
}

func TestRewriteJSONModel_InvalidJSON(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{})
	body := []byte(`not json`)
	out, found, err := RewriteJSONModel(body, r, "llm")
	if err == nil {
		t.Fatal("expected error for non-JSON body")
	}
	if found {
		t.Errorf("found must be false on parse error")
	}
	if !bytes.Equal(out, body) {
		t.Errorf("body mutated on error")
	}
}

func TestRewriteJSONModel_UTF8Messages(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}: "pod-qwen",
	})
	body := []byte(`{"model":"qwen","messages":[{"role":"user","content":"olá 🔥"}]}`)
	out, _, err := RewriteJSONModel(body, r, "llm")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	msgs := got["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].(string)
	if content != "olá 🔥" {
		t.Errorf("utf8 corrupted: %q", content)
	}
}

func TestRewriteJSONModel_UnknownAliasPassThrough(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{})
	body := []byte(`{"model":"gpt-5","messages":[]}`)
	out, found, err := RewriteJSONModel(body, r, "llm")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !found {
		t.Fatalf("found must be true (model field exists)")
	}
	if !bytes.Equal(out, body) {
		t.Errorf("body must be unchanged for unknown alias; got %q", out)
	}
}

func TestHandler_RewritesBeforeInner(t *testing.T) {
	r := newResolverFromMap(map[aliasKey]string{
		{"qwen", "llm"}: "pod-qwen",
	})
	var seen []byte
	inner := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		seen = b
		w.WriteHeader(200)
	})
	h := Handler(r, "llm", inner)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"model":"qwen"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.Unmarshal(seen, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["model"] != "pod-qwen" {
		t.Errorf("inner saw model=%v; want pod-qwen", got["model"])
	}
}
