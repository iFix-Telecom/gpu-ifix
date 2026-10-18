// openrouter_fallback_models_test.go — quick 261018: o director do OpenRouter
// injeta "models": [target, ...fallback_models] quando a linha
// (alias, upstream) tem fallback_models, mantendo model = target.
package proxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

func fallbackDirector(t *testing.T, upstreamName string, setup func(r *models.Resolver)) func(*http.Request) {
	t.Helper()
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	upstream, _ := url.Parse("https://openrouter.example/api")
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"jev-alias", "openrouter-chat"}:     "deepseek/deepseek-v4-flash",
		{"jev-alias", "openrouter-deepseek"}: "deepseek/deepseek-v4-flash:nitro",
		{"plain", "openrouter-chat"}:         "deepseek/deepseek-v4-flash",
	})
	if setup != nil {
		setup(resolver)
	}
	return BuildOpenRouterDirector(upstream, "sk-test", []string{"novita"}, false, resolver, upstreamName, discardLogger(), nil)
}

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, b)
	}
	return m
}

func TestOpenRouterDirector_InjectsFallbackModels(t *testing.T) {
	d := fallbackDirector(t, "openrouter-chat", func(r *models.Resolver) {
		r.SetFallbackModelsForTesting("jev-alias", "openrouter-chat", []string{"google/gemini-2.5-flash", "openai/gpt-5-mini"})
	})
	_, out := applyDirector(t, d, http.MethodPost, "/v1/chat/completions", "application/json",
		[]byte(`{"model":"jev-alias","messages":[{"role":"user","content":"oi"}]}`), nil, nil)
	m := decodeBody(t, out)
	if m["model"] != "deepseek/deepseek-v4-flash" {
		t.Fatalf("model = %v, want target", m["model"])
	}
	list, _ := m["models"].([]any)
	want := []string{"deepseek/deepseek-v4-flash", "google/gemini-2.5-flash", "openai/gpt-5-mini"}
	if len(list) != len(want) {
		t.Fatalf("models = %v, want %v", list, want)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Fatalf("models[%d] = %v, want %v", i, list[i], want[i])
		}
	}
	// provider continua sendo injetado (PASS 2 intocado).
	if _, ok := m["provider"].(map[string]any); !ok {
		t.Fatalf("provider lost: %s", out)
	}
	if _, ok := m["messages"].([]any); !ok {
		t.Fatalf("messages lost: %s", out)
	}
}

func TestOpenRouterDirector_FallbackModelsPerUpstreamRow(t *testing.T) {
	// Lista só na linha openrouter-chat → o director do openrouter-deepseek
	// (mesmo alias) não injeta nada.
	d := fallbackDirector(t, "openrouter-deepseek", func(r *models.Resolver) {
		r.SetFallbackModelsForTesting("jev-alias", "openrouter-chat", []string{"google/gemini-2.5-flash"})
	})
	_, out := applyDirector(t, d, http.MethodPost, "/v1/chat/completions", "application/json",
		[]byte(`{"model":"jev-alias","messages":[]}`), nil, nil)
	m := decodeBody(t, out)
	if _, ok := m["models"]; ok {
		t.Fatalf("models injected on a row without fallback_models: %s", out)
	}
	if m["model"] != "deepseek/deepseek-v4-flash:nitro" {
		t.Fatalf("model = %v", m["model"])
	}
}

func TestOpenRouterDirector_NoFallbackModelsLeavesBodyAlone(t *testing.T) {
	d := fallbackDirector(t, "openrouter-chat", nil)
	_, out := applyDirector(t, d, http.MethodPost, "/v1/chat/completions", "application/json",
		[]byte(`{"model":"plain","messages":[]}`), nil, nil)
	if _, ok := decodeBody(t, out)["models"]; ok {
		t.Fatalf("models must not appear without fallback_models: %s", out)
	}
	// Cliente que manda "models" sem lista configurada: passa como antes.
	_, out = applyDirector(t, d, http.MethodPost, "/v1/chat/completions", "application/json",
		[]byte(`{"model":"plain","models":["client/x"],"messages":[]}`), nil, nil)
	list, _ := decodeBody(t, out)["models"].([]any)
	if len(list) != 1 || list[0] != "client/x" {
		t.Fatalf("client models changed without config: %v", list)
	}
}

func TestOpenRouterDirector_FallbackModelsOverrideClientModels(t *testing.T) {
	d := fallbackDirector(t, "openrouter-chat", func(r *models.Resolver) {
		r.SetFallbackModelsForTesting("jev-alias", "openrouter-chat", []string{"google/gemini-2.5-flash"})
	})
	_, out := applyDirector(t, d, http.MethodPost, "/v1/chat/completions", "application/json",
		[]byte(`{"model":"jev-alias","models":["client/x"],"messages":[]}`), nil, nil)
	list, _ := decodeBody(t, out)["models"].([]any)
	if len(list) != 2 || list[0] != "deepseek/deepseek-v4-flash" || list[1] != "google/gemini-2.5-flash" {
		t.Fatalf("gateway policy must win over client models: %v", list)
	}
}

func TestInjectFallbackModels_NoModelErrors(t *testing.T) {
	if _, err := injectFallbackModels([]byte(`{"messages":[]}`), []string{"a/b"}); err == nil {
		t.Fatal("expected error when body has no model")
	}
	if _, err := injectFallbackModels([]byte(`not json`), []string{"a/b"}); err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}
