package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// runEffortDirector monta o director do OpenRouter com alias→alvo e devolve
// o body que sairia para o upstream.
func runEffortDirector(t *testing.T, target string, body string, lookup TenantPrefsLookup, ctx context.Context) map[string]json.RawMessage {
	t.Helper()
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	upstream, _ := url.Parse("https://openrouter.example/api")
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"qwen", "openrouter-chat"}: target,
	})
	director := BuildOpenRouterDirector(upstream, "sk-or-v1-test", []string{"novita"}, false, resolver, "openrouter-chat", discardLogger(), lookup)
	_, out := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", []byte(body), nil, ctx)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("body de saída inválido: %v (%s)", err, out)
	}
	return m
}

func reasoningOf(t *testing.T, m map[string]json.RawMessage) map[string]any {
	t.Helper()
	raw, ok := m["reasoning"]
	if !ok {
		t.Fatalf("reasoning ausente no body de saída")
	}
	var r map[string]any
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("reasoning inválido: %v", err)
	}
	return r
}

const haikuTarget = "anthropic/claude-haiku-5.5"

func TestOpenRouterDirector_Effort_DefaultHighParaAnthropic(t *testing.T) {
	m := runEffortDirector(t, haikuTarget, `{"model":"qwen","messages":[{"role":"user","content":"oi"}]}`, nil, nil)
	r := reasoningOf(t, m)
	if r["effort"] != "high" || len(r) != 1 {
		t.Errorf("reasoning=%v, want {effort:high}", r)
	}
	if _, ok := m["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort não deveria sair")
	}
}

func TestOpenRouterDirector_Effort_ClienteDesligaThinking(t *testing.T) {
	m := runEffortDirector(t, haikuTarget, `{"model":"qwen","messages":[{"role":"user","content":"oi"}],"reasoning":{"enabled":false}}`, fakeEffortLookup{effortTestTenant: "max"}, effortCtx())
	r := reasoningOf(t, m)
	if r["enabled"] != false || len(r) != 1 {
		t.Errorf("reasoning=%v, want {enabled:false}", r)
	}
}

func TestOpenRouterDirector_Effort_ClienteReasoningEffortLow(t *testing.T) {
	m := runEffortDirector(t, haikuTarget, `{"model":"qwen","messages":[{"role":"user","content":"oi"}],"reasoning_effort":"low"}`, nil, nil)
	r := reasoningOf(t, m)
	if r["effort"] != "low" || len(r) != 1 {
		t.Errorf("reasoning=%v, want {effort:low}", r)
	}
	if _, ok := m["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort não deveria sair")
	}
}

func TestOpenRouterDirector_Effort_TenantOverride(t *testing.T) {
	m := runEffortDirector(t, haikuTarget, `{"model":"qwen","messages":[{"role":"user","content":"oi"}]}`, fakeEffortLookup{effortTestTenant: "medium"}, effortCtx())
	r := reasoningOf(t, m)
	if r["effort"] != "medium" {
		t.Errorf("reasoning=%v, want {effort:medium}", r)
	}
}

func TestOpenRouterDirector_Effort_NaoAnthropicIntacto(t *testing.T) {
	m := runEffortDirector(t, "deepseek/deepseek-v4-flash", `{"model":"qwen","messages":[{"role":"user","content":"oi"}],"reasoning_effort":"low","reasoning":{"effort":"max","exclude":true}}`, fakeEffortLookup{effortTestTenant: "medium"}, effortCtx())
	if !bytes.Equal(m["reasoning_effort"], []byte(`"low"`)) {
		t.Errorf("reasoning_effort=%s, want \"low\"", m["reasoning_effort"])
	}
	if !bytes.Equal(m["reasoning"], []byte(`{"effort":"max","exclude":true}`)) {
		t.Errorf("reasoning=%s alterado", m["reasoning"])
	}
}

func TestOpenRouterDirector_Effort_NaoAnthropicSemReasoningNaoInjeta(t *testing.T) {
	m := runEffortDirector(t, "deepseek/deepseek-v4-flash", `{"model":"qwen","messages":[{"role":"user","content":"oi"}]}`, nil, nil)
	if _, ok := m["reasoning"]; ok {
		t.Errorf("reasoning não deveria ser injetado em alvo não-Anthropic")
	}
}
