// Package proxy (anthropic_translate_resp.go): tradução da SAÍDA do passo ①
// (Anthropic Messages API nativa → formato OpenAI chat.completion), Phase 23
// HAIKU-01.
//
// Clientes do gateway (n8n, Maestro, converseai) falam OpenAI. O usage
// traduzido usa a MESMA forma do OpenRouter (prompt_tokens_details.
// cached_tokens / cache_write_tokens) para que um único parser de billing
// atenda os passos ① e ②. O toolCallInterceptor e o usageInterceptor operam
// sobre o body JÁ traduzido, por isso este interceptor roda antes deles.
//
// Mapeamento stop_reason → finish_reason:
//
//	end_turn | stop_sequence | pause_turn       → stop
//	max_tokens | model_context_window_exceeded  → length
//	tool_use                                    → tool_calls
//	refusal                                     → content_filter
//
// HIPÓTESE (A2 do 23-RESEARCH): refusal→content_filter e pause_turn→stop são
// decisões de mapeamento sem equivalente oficial documentado; a evidência que
// resolveria é um cliente real reagindo a esses valores.
//
// Usage (fórmula de total de entrada citada em api/rate-limits):
//
//	prompt_tokens     = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
//	completion_tokens = output_tokens
//	total_tokens      = prompt_tokens + completion_tokens
//	prompt_tokens_details = {cached_tokens: cache_read, cache_write_tokens: cache_creation}
//
// Blocos thinking/redacted_thinking (e a signature) são descartados — o
// raciocínio interno nunca chega ao cliente (T-23-21). Nunca loga body.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// anthErrorBodyCap limita quanto do corpo de erro upstream é lido para a
// tradução. Erros Anthropic são pequenos; o cap protege contra upstream
// hostil.
const anthErrorBodyCap = 64 << 10

// anthJSONBodyCap limita o corpo de resposta não-streaming lido para
// tradução (respostas de chat cabem com folga; max_tokens 128k ≈ < 1 MiB).
const anthJSONBodyCap = 8 << 20

// anthUsage é o usage da Messages API (cumulativo nos eventos de stream).
type anthUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
}

// anthUsageState guarda o último valor visto de cada campo de usage.
type anthUsageState struct {
	input, output, cacheRead, cacheCreation int
}

// merge sobrescreve apenas os campos presentes em u (valores cumulativos:
// o último ganha, nunca soma).
func (s *anthUsageState) merge(u *anthUsage) {
	if u == nil {
		return
	}
	if u.InputTokens != nil {
		s.input = *u.InputTokens
	}
	if u.OutputTokens != nil {
		s.output = *u.OutputTokens
	}
	if u.CacheReadInputTokens != nil {
		s.cacheRead = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		s.cacheCreation = *u.CacheCreationInputTokens
	}
}

// openAI devolve o usage no formato OpenAI/OpenRouter.
func (s anthUsageState) openAI() map[string]any {
	prompt := s.input + s.cacheRead + s.cacheCreation
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": s.output,
		"total_tokens":      prompt + s.output,
		"prompt_tokens_details": map[string]any{
			"cached_tokens":      s.cacheRead,
			"cache_write_tokens": s.cacheCreation,
		},
	}
}

// anthMapStopReason converte stop_reason Anthropic em finish_reason OpenAI.
// Valor vazio devolve "" (chamador decide null); desconhecido → stop.
func anthMapStopReason(r string) string {
	switch r {
	case "":
		return ""
	case "end_turn", "stop_sequence", "pause_turn":
		return "stop"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

type anthRespBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthRespMessage struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Model      string          `json:"model"`
	Content    []anthRespBlock `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      *anthUsage      `json:"usage"`
}

// anthToolArguments serializa o input do tool_use como string JSON compacta
// (OpenAI exige arguments string). Input ausente/null vira "{}".
func anthToolArguments(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, t); err != nil {
		return "{}"
	}
	return buf.String()
}

// TranslateMessagesResponseJSON converte uma resposta não-streaming da
// Messages API em chat.completion OpenAI. Erro de parse → error (o
// interceptor responde 502 com envelope OpenAI).
func TranslateMessagesResponseJSON(body []byte) ([]byte, error) {
	var m anthRespMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("anthropic response parse: %w", err)
	}
	if m.Type == "error" {
		return nil, fmt.Errorf("anthropic response: error envelope em resposta 2xx")
	}

	var text strings.Builder
	hasText := false
	toolCalls := make([]map[string]any, 0)
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			hasText = true
			text.WriteString(b.Text)
		case "tool_use":
			toolCalls = append(toolCalls, map[string]any{
				"id":   b.ID,
				"type": "function",
				"function": map[string]any{
					"name":      b.Name,
					"arguments": anthToolArguments(b.Input),
				},
			})
		default:
			// thinking, redacted_thinking, server tools etc.: descartados.
		}
	}

	msg := map[string]any{"role": "assistant"}
	if hasText {
		msg["content"] = text.String()
	} else {
		msg["content"] = nil
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	var finish any
	if fr := anthMapStopReason(m.StopReason); fr != "" {
		finish = fr
	}

	var us anthUsageState
	us.merge(m.Usage)

	out := map[string]any{
		"id":      m.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   m.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": us.openAI(),
	}
	return json.Marshal(out)
}

// TranslateAnthropicErrorJSON converte o envelope de erro Anthropic
// `{type:"error",error:{type,message}}` em `{"error":{message,type,code}}`
// OpenAI. Corpo não-parseável vira message "upstream error" — o corpo cru
// nunca é ecoado.
func TranslateAnthropicErrorJSON(body []byte) []byte {
	var env struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	msg, typ := "upstream error", "upstream_error"
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil {
		if env.Error.Message != "" {
			msg = env.Error.Message
			if len(msg) > 2048 {
				msg = msg[:2048]
			}
		}
		if env.Error.Type != "" {
			typ = env.Error.Type
		}
	}
	out, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": msg, "type": typ, "code": typ},
	})
	return out
}

// anthropicTranslateInterceptor implementa ProxyResponseInterceptor.
type anthropicTranslateInterceptor struct{}

// AnthropicTranslateInterceptor devolve o interceptor que traduz a resposta
// do upstream Anthropic (passo ①) para o formato OpenAI:
//   - 2xx + application/json  → TranslateMessagesResponseJSON (parse falho → 502 envelope OpenAI)
//   - 2xx + text/event-stream → NewAnthropicSSETranslator (remove Content-Length)
//   - não-2xx                 → TranslateAnthropicErrorJSON, status PRESERVADO
//
// Nunca devolve errUpstreamRetryable: a cascata por status é do
// ChatRetryableStatusInterceptor, que roda DEPOIS deste e lê o envelope
// OpenAI deixado em resp.Body (bytes.Reader).
func AnthropicTranslateInterceptor() ProxyResponseInterceptor {
	return anthropicTranslateInterceptor{}
}

func (anthropicTranslateInterceptor) Intercept(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, anthErrorBodyCap))
		_ = resp.Body.Close()
		if err != nil {
			body = nil
		}
		anthSetBody(resp, TranslateAnthropicErrorJSON(body))
		return nil
	}

	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		// Tradução SSE ligada na Task 2 (NewAnthropicSSETranslator).
		return nil
	case strings.HasPrefix(ct, "application/json"):
		body, err := io.ReadAll(io.LimitReader(resp.Body, anthJSONBodyCap+1))
		_ = resp.Body.Close()
		var out []byte
		if err == nil && len(body) <= anthJSONBodyCap {
			out, err = TranslateMessagesResponseJSON(body)
		} else if err == nil {
			err = fmt.Errorf("anthropic response exceeds %d bytes", anthJSONBodyCap)
		}
		if err != nil {
			resp.StatusCode = http.StatusBadGateway
			resp.Status = strconv.Itoa(http.StatusBadGateway) + " " + http.StatusText(http.StatusBadGateway)
			out, _ = json.Marshal(map[string]any{"error": map[string]any{
				"message": "invalid upstream response",
				"type":    "upstream_invalid_response",
				"code":    "upstream_invalid_response",
			}})
		}
		anthSetBody(resp, out)
		return nil
	default:
		return nil
	}
}

// anthSetBody troca o body por um bytes.Reader e alinha os headers.
func anthSetBody(resp *http.Response, b []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
	resp.Header.Del("Content-Encoding")
}
