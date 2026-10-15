package proxy

import (
	"encoding/json"
	"testing"
)

func acDecode(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	return m
}

func acMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var m struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m.Messages
}

func TestAnthropicCompat(t *testing.T) {
	t.Run("assistant_texto_ganha_user_final", func(t *testing.T) {
		in := []byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"user","content":"oi"},{"role":"assistant","content":"olá"}]}`)
		msgs := acMessages(t, normalizeAnthropicChatBody(in))
		if len(msgs) != 3 {
			t.Fatalf("len(messages)=%d, want 3", len(msgs))
		}
		last := msgs[2]
		if last["role"] != "user" || last["content"] != AnthropicPrefillUserText {
			t.Fatalf("última mensagem = %v, want user %q", last, AnthropicPrefillUserText)
		}
	})

	t.Run("assistant_com_tool_calls_inalterado", func(t *testing.T) {
		in := []byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"user","content":"clima?"},{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"c","arguments":"{}"}}]}]}`)
		if out := normalizeAnthropicChatBody(in); string(out) != string(in) {
			t.Fatalf("body alterado: %s", out)
		}
	})

	t.Run("ultimo_tool_ou_user_inalterado", func(t *testing.T) {
		for _, in := range [][]byte{
			[]byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"user","content":"oi"}]}`),
			[]byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"user","content":"a"},{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"c","arguments":"{}"}}]},{"role":"tool","tool_call_id":"1","content":"sol"}]}`),
		} {
			if out := normalizeAnthropicChatBody(in); string(out) != string(in) {
				t.Fatalf("body alterado: %s", out)
			}
		}
	})

	t.Run("assistant_com_tool_calls_vazio_e_prefill", func(t *testing.T) {
		in := []byte(`{"model":"anthropic/claude-haiku-5.5","messages":[{"role":"user","content":"oi"},{"role":"assistant","content":"x","tool_calls":[]}]}`)
		msgs := acMessages(t, normalizeAnthropicChatBody(in))
		if len(msgs) != 3 || msgs[2]["role"] != "user" || msgs[2]["content"] != AnthropicPrefillUserText {
			t.Fatalf("esperava user sintético no fim; msgs=%v", msgs)
		}
	})

	t.Run("alvo_nao_anthropic_byte_identico", func(t *testing.T) {
		in := []byte(`{"model":"deepseek/deepseek-v4.1-flash:exacto","temperature":0.7,"messages":[{"role":"user","content":"oi"},{"role":"assistant","content":"olá"}]}`)
		if out := normalizeAnthropicChatBody(in); string(out) != string(in) {
			t.Fatalf("body alterado: %s", out)
		}
	})

	t.Run("sampling_removido_demais_preservados", func(t *testing.T) {
		in := []byte(`{"model":"anthropic/claude-haiku-5.5","temperature":0,"top_p":1,"frequency_penalty":0,"presence_penalty":0,"top_k":40,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"tool_choice":"auto","stream":true,"reasoning_effort":"high","response_format":{"type":"json_object"},"messages":[{"role":"user","content":"oi"}]}`)
		inM := acDecode(t, in)
		outM := acDecode(t, normalizeAnthropicChatBody(in))
		for _, k := range anthropicRejectedSamplingParams {
			if _, ok := outM[k]; ok {
				t.Errorf("campo %q deveria ter sido removido", k)
			}
		}
		for _, k := range []string{"tools", "tool_choice", "stream", "reasoning_effort", "response_format", "model", "messages"} {
			if string(outM[k]) != string(inM[k]) {
				t.Errorf("campo %q alterado: got %s want %s", k, outM[k], inM[k])
			}
		}
	})

	t.Run("json_invalido_devolve_original", func(t *testing.T) {
		in := []byte(`{not json`)
		if out := normalizeAnthropicChatBody(in); string(out) != string(in) {
			t.Fatalf("body inválido alterado: %s", out)
		}
	})

	t.Run("isAnthropicTarget_nomes", func(t *testing.T) {
		cases := map[string]bool{
			"claude-haiku-5-5":                    true,
			"anthropic/claude-haiku-5.5":          true,
			"deepseek/deepseek-v4.1-flash:exacto": false,
			"maestro-summary":                     false,
			"":                                    false,
		}
		for model, want := range cases {
			if got := isAnthropicTarget(model); got != want {
				t.Errorf("isAnthropicTarget(%q)=%v, want %v", model, got, want)
			}
		}
	})

	t.Run("normalizeTrailingAssistant_vazio_inalterado", func(t *testing.T) {
		out, changed := normalizeTrailingAssistant(nil)
		if changed || len(out) != 0 {
			t.Fatalf("slice vazio não deveria mudar")
		}
	})
}
