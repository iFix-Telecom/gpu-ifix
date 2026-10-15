package proxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// prefillBody: conversa que termina numa fala do assistant (prefill) com
// temperature — formato que gera 400 no Haiku 5.5 (F5 do 23-RESEARCH).
const prefillBody = `{"model":"deepseek-flash","temperature":0.3,"messages":[{"role":"system","content":"regras"},{"role":"user","content":"oi"},{"role":"assistant","content":"resumo parcial"}]}`

func prefillDirector(t *testing.T, target string) func(*http.Request) {
	t.Helper()
	t.Setenv("UPSTREAM_LLM_OPENROUTER_MODEL", "")
	upstream, _ := url.Parse("https://openrouter.ai/api")
	resolver := models.NewResolverForTesting(map[[2]string]string{
		{"deepseek-flash", "openrouter-chat"}: target,
	})
	return BuildOpenRouterDirector(upstream, "sk-or-v1-test", []string{"novita"}, false, resolver, "openrouter-chat", discardLogger(), nil)
}

func TestOpenRouterDirector_PrefillNormalized(t *testing.T) {
	director := prefillDirector(t, "anthropic/claude-haiku-5.5")
	req, out := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", []byte(prefillBody), nil, nil)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if _, ok := m["temperature"]; ok {
		t.Fatalf("temperature deveria ter sido removido: %s", out)
	}
	msgs := ccMessages(t, out)
	last := msgs[len(msgs)-1]
	if last["role"] != "user" || last["content"] != AnthropicPrefillUserText {
		t.Fatalf("última mensagem = %v, want user %q", last, AnthropicPrefillUserText)
	}
	if req.ContentLength != int64(len(out)) || req.Header.Get("Content-Length") != strconv.Itoa(len(out)) {
		t.Fatalf("Content-Length %d/%q != len(body) %d", req.ContentLength, req.Header.Get("Content-Length"), len(out))
	}
}

func TestOpenRouterDirector_NonAnthropicUntouched(t *testing.T) {
	director := prefillDirector(t, "deepseek/deepseek-v4.1-flash:exacto")
	_, out := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", []byte(prefillBody), nil, nil)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("body inválido: %v", err)
	}
	if string(m["temperature"]) != "0.3" {
		t.Fatalf("temperature perdido em alvo não-Anthropic: %s", out)
	}
	msgs := ccMessages(t, out)
	if len(msgs) != 3 || msgs[2]["role"] != "assistant" {
		t.Fatalf("mensagens alteradas em alvo não-Anthropic: %v", msgs)
	}
}

// PASS 4 (cache) roda antes do PASS 5: o breakpoint da conversa fica no
// último turno real (assistant) e o "Prossiga." sintético sai sem
// cache_control — senão o prefixo nunca seria reaproveitado no turno seguinte.
func TestOpenRouterDirector_PrefillCacheBreakpointOnRealTurn(t *testing.T) {
	director := prefillDirector(t, "anthropic/claude-haiku-5.5")
	_, out := applyDirector(t, director, http.MethodPost, "/v1/chat/completions", "application/json", []byte(prefillBody), nil, nil)

	msgs := ccMessages(t, out)
	if len(msgs) != 4 {
		t.Fatalf("len(messages)=%d, want 4; body=%s", len(msgs), out)
	}
	if !hasCC(msgs[0]) {
		t.Fatal("system sem cache_control")
	}
	if hasCC(msgs[1]) {
		t.Fatal("user intermediário não deveria ser marcado")
	}
	if !hasCC(msgs[2]) {
		t.Fatal("breakpoint da conversa deveria ficar no assistant original")
	}
	synth := msgs[3]
	if synth["content"] != AnthropicPrefillUserText {
		t.Fatalf("mensagem sintética com content inesperado (cache_control?): %v", synth["content"])
	}
}
