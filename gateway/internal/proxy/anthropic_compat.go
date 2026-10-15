// Package proxy (anthropic_compat.go): normalizações de body de chat
// compartilhadas pelos caminhos que mandam para modelos Anthropic — o passo ②
// (OpenRouter, alvo anthropic/claude-haiku-5.5, PASS 5 do
// BuildOpenRouterDirector) e o tradutor do passo ① (API nativa, plano 23-08).
//
// Fonte (Phase 23, HAIKU-01):
//   - Prefill: Haiku 5.5 devolve 400 "does not support assistant message
//     prefill" quando a conversa termina numa fala do assistant (fato F5 do
//     23-RESEARCH: ≈19 erros em 20 h, chat-ifix via Maestro "analista
//     silencioso"). Decisão do 23-CONTEXT: o gateway anexa uma mensagem de
//     usuário neutra, só para alvos Anthropic, sem depender do Maestro.
//   - Sampling: temperature/top_p/top_k/frequency_penalty/presence_penalty
//     são rejeitados pelo modelo (migration guide; Pitfall 2 do research).
//
// Tudo é best-effort: qualquer falha de parse devolve o body original, sem
// panic. Nunca loga o body (R12).
package proxy

import (
	"encoding/json"
	"strings"
)

// AnthropicPrefillUserText é o conteúdo do turno user sintético anexado
// quando a conversa termina numa fala do assistant (neutro, PT-BR).
const AnthropicPrefillUserText = "Prossiga."

// anthropicRejectedSamplingParams são os campos top-level removidos do body
// quando o alvo resolvido é Anthropic.
var anthropicRejectedSamplingParams = []string{"temperature", "top_p", "top_k", "frequency_penalty", "presence_penalty"}

// isAnthropicTarget diz se o modelo (já reescrito para o alvo do upstream)
// é Anthropic: prefixo OpenRouter "anthropic/" ou nome nativo "claude-".
func isAnthropicTarget(model string) bool {
	return strings.HasPrefix(model, "anthropic/") || strings.HasPrefix(model, "claude-")
}

// normalizeTrailingAssistant anexa {"role":"user","content":"Prossiga."}
// quando a última mensagem é assistant SEM tool_calls não-vazio (prefill).
// Assistant com tool_calls é tool loop em andamento e fica intacto, assim
// como último turno user/tool/system. Devolve (msgs, alterou).
func normalizeTrailingAssistant(msgs []map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
	if len(msgs) == 0 {
		return msgs, false
	}
	last := msgs[len(msgs)-1]
	var role string
	if err := json.Unmarshal(last["role"], &role); err != nil || role != "assistant" {
		return msgs, false
	}
	if raw, ok := last["tool_calls"]; ok {
		var calls []json.RawMessage
		if err := json.Unmarshal(raw, &calls); err == nil && len(calls) > 0 {
			return msgs, false
		}
	}
	content, _ := json.Marshal(AnthropicPrefillUserText)
	return append(msgs, map[string]json.RawMessage{
		"role":    json.RawMessage(`"user"`),
		"content": content,
	}), true
}

// normalizeAnthropicChatBody aplica as normalizações Anthropic (sampling +
// prefill) quando o `model` do body — já reescrito pelo PASS 1, ou seja, o
// alvo e não o alias — é Anthropic. Alvo não-Anthropic, body sem mudança
// necessária ou erro de parse → devolve `body` intacto (byte-idêntico).
func normalizeAnthropicChatBody(body []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	var model string
	if err := json.Unmarshal(m["model"], &model); err != nil || !isAnthropicTarget(model) {
		return body
	}
	changed := false
	for _, k := range anthropicRejectedSamplingParams {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}
	if raw, ok := m["messages"]; ok {
		var msgs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &msgs); err == nil {
			if out, appended := normalizeTrailingAssistant(msgs); appended {
				mb, err := json.Marshal(out)
				if err != nil {
					return body
				}
				m["messages"] = mb
				changed = true
			}
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
