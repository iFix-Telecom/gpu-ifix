package proxy

import (
	"encoding/json"
	"testing"
)

func ccMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var m struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m.Messages
}

func hasCC(msg map[string]any) bool {
	blocks, ok := msg["content"].([]any)
	if !ok || len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if _, ok := b.(map[string]any)["cache_control"]; ok {
			return true
		}
	}
	return false
}

func TestInjectAnthropicCacheControl_SystemAndLastTurn(t *testing.T) {
	in := []byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"system","content":"regras"},{"role":"user","content":"oi"},{"role":"assistant","content":"olá"},{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}}]}],"temperature":0}`)
	msgs := ccMessages(t, injectAnthropicCacheControl(in))
	if !hasCC(msgs[0]) {
		t.Fatal("system sem cache_control")
	}
	if hasCC(msgs[1]) || hasCC(msgs[2]) {
		t.Fatal("turnos intermediários não deveriam ser marcados")
	}
	last := msgs[3]["content"].([]any)
	if _, ok := last[0].(map[string]any)["cache_control"]; !ok {
		t.Fatal("último bloco de texto da última mensagem sem cache_control")
	}
	if _, ok := last[1].(map[string]any)["cache_control"]; ok {
		t.Fatal("bloco de imagem não deveria ser marcado")
	}
}

func TestInjectAnthropicCacheControl_NonAnthropicUntouched(t *testing.T) {
	in := []byte(`{"model":"deepseek/deepseek-v4.1-flash:exacto","messages":[{"role":"system","content":"x"}]}`)
	if out := injectAnthropicCacheControl(in); string(out) != string(in) {
		t.Fatalf("body alterado: %s", out)
	}
}

func TestInjectAnthropicCacheControl_ClientCacheControlWins(t *testing.T) {
	in := []byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"system","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]},{"role":"user","content":"oi"}]}`)
	if out := injectAnthropicCacheControl(in); string(out) != string(in) {
		t.Fatalf("body alterado: %s", out)
	}
}

func TestInjectAnthropicCacheControl_ToolLastSkipsToConversation(t *testing.T) {
	in := []byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"user","content":"clima?"},{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"c","arguments":"{}"}}]},{"role":"tool","tool_call_id":"1","content":"sol"}]}`)
	msgs := ccMessages(t, injectAnthropicCacheControl(in))
	if !hasCC(msgs[0]) {
		t.Fatal("user deveria ser marcado (assistant com content null não tem texto)")
	}
	if _, ok := msgs[2]["content"].(string); !ok {
		t.Fatal("mensagem tool não deveria ser tocada")
	}
}

func TestInjectAnthropicCacheControl_BadJSONUntouched(t *testing.T) {
	in := []byte(`{not json`)
	if out := injectAnthropicCacheControl(in); string(out) != string(in) {
		t.Fatal("body inválido alterado")
	}
}
