package proxy

// Fixtures em testdata/anthropic/resp_*.json seguem o formato de resposta da
// Messages API documentado em https://platform.claude.com/docs/en/api/messages
// (content[] com blocos thinking/text/tool_use, stop_reason, usage com
// cache_creation_input_tokens/cache_read_input_tokens).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

type oaUsageT struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

type oaCompletionT struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage oaUsageT `json:"usage"`
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/anthropic/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func TestAnthropicTranslateResponse_Text(t *testing.T) {
	out, err := TranslateMessagesResponseJSON(readFixture(t, "resp_text.json"))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if bytes.Contains(out, []byte("thinking")) || bytes.Contains(out, []byte("signature")) {
		t.Fatalf("thinking vazou para o cliente: %s", out)
	}
	var c oaCompletionT
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.ID != "msg_01XFDUDYJgAACzvnptvVoYEL" || c.Object != "chat.completion" || c.Model != "claude-haiku-5-5" || c.Created == 0 {
		t.Fatalf("cabeçalho errado: %+v", c)
	}
	if len(c.Choices) != 1 {
		t.Fatalf("choices: %d", len(c.Choices))
	}
	ch := c.Choices[0]
	if ch.Message.Role != "assistant" || ch.Message.Content == nil || *ch.Message.Content != "Olá" {
		t.Fatalf("message errada: %+v", ch.Message)
	}
	if len(ch.Message.ToolCalls) != 0 {
		t.Fatalf("tool_calls inesperado")
	}
	if bytes.Contains(out, []byte(`"tool_calls"`)) {
		t.Fatalf("chave tool_calls não deve aparecer sem tool_use: %s", out)
	}
	if ch.FinishReason != "stop" {
		t.Fatalf("finish_reason=%q", ch.FinishReason)
	}
	u := c.Usage
	if u.PromptTokens != 1000 || u.CompletionTokens != 20 || u.TotalTokens != 1020 ||
		u.PromptTokensDetails.CachedTokens != 900 || u.PromptTokensDetails.CacheWriteTokens != 0 {
		t.Fatalf("usage errado: %+v", u)
	}
	if !bytes.Contains(out, []byte(`"cache_write_tokens":0`)) {
		t.Fatalf("cache_write_tokens deve estar presente mesmo zerado: %s", out)
	}
}

func TestAnthropicTranslateResponse_ToolUse(t *testing.T) {
	out, err := TranslateMessagesResponseJSON(readFixture(t, "resp_tool_use.json"))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	var c oaCompletionT
	if err := json.Unmarshal(out, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ch := c.Choices[0]
	if ch.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q", ch.FinishReason)
	}
	if ch.Message.Content == nil || *ch.Message.Content != "Vou consultar." {
		t.Fatalf("content=%v", ch.Message.Content)
	}
	if len(ch.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls=%d", len(ch.Message.ToolCalls))
	}
	tc := ch.Message.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Type != "function" || tc.Function.Name != "get_x" || tc.Function.Arguments != `{"a":1}` {
		t.Fatalf("tool_call errado: %+v", tc)
	}
	if c.Usage.PromptTokens != 1500 || c.Usage.PromptTokensDetails.CacheWriteTokens != 1200 || c.Usage.TotalTokens != 1545 {
		t.Fatalf("usage errado: %+v", c.Usage)
	}
}

func TestAnthropicTranslateResponse_ToolUseNoText(t *testing.T) {
	in := `{"id":"msg_x","type":"message","role":"assistant","model":"claude-haiku-5-5",
	"content":[{"type":"tool_use","id":"toolu_9","name":"f","input":{}}],
	"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":2}}`
	out, err := TranslateMessagesResponseJSON([]byte(in))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if !bytes.Contains(out, []byte(`"content":null`)) {
		t.Fatalf("content deve ser null sem texto: %s", out)
	}
	if !bytes.Contains(out, []byte(`"arguments":"{}"`)) {
		t.Fatalf("arguments vazio deve ser {}: %s", out)
	}
}

func TestAnthropicTranslateResponse_FinishReasonMap(t *testing.T) {
	cases := map[string]string{
		"end_turn":                      "stop",
		"stop_sequence":                 "stop",
		"max_tokens":                    "length",
		"model_context_window_exceeded": "length",
		"tool_use":                      "tool_calls",
		"refusal":                       "content_filter",
		"pause_turn":                    "stop",
	}
	for in, want := range cases {
		body := `{"id":"msg_1","model":"claude-haiku-5-5","content":[{"type":"text","text":"x"}],"stop_reason":"` + in + `","usage":{"input_tokens":1,"output_tokens":1}}`
		out, err := TranslateMessagesResponseJSON([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		var c oaCompletionT
		_ = json.Unmarshal(out, &c)
		if got := c.Choices[0].FinishReason; got != want {
			t.Errorf("stop_reason %s → %q, want %q", in, got, want)
		}
	}
}

func TestAnthropicTranslateResponse_Malformed(t *testing.T) {
	if _, err := TranslateMessagesResponseJSON([]byte(`{not json`)); err == nil {
		t.Fatalf("esperava erro em JSON malformado")
	}
}

func TestAnthropicTranslateError_Envelope(t *testing.T) {
	out := TranslateAnthropicErrorJSON([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`))
	want := `{"error":{"code":"rate_limit_error","message":"slow","type":"rate_limit_error"}}`
	if string(out) != want {
		t.Fatalf("got %s want %s", out, want)
	}
	out = TranslateAnthropicErrorJSON([]byte(strings.Repeat("x", 5000)))
	var env struct {
		Error struct{ Message, Type, Code string } `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("envelope inválido: %v", err)
	}
	if env.Error.Message != "upstream error" || len(out) > 2048 {
		t.Fatalf("corpo não parseável deve virar 'upstream error' sem ecoar: %s", out)
	}
}

func newResp(status int, ct string, body []byte) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	return &http.Response{
		StatusCode:    status,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func TestAnthropicTranslateInterceptor_Error429(t *testing.T) {
	resp := newResp(429, "application/json", []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`))
	if err := AnthropicTranslateInterceptor().Intercept(resp); err != nil {
		t.Fatalf("intercept: %v", err)
	}
	if resp.StatusCode != 429 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	var got, want map[string]any
	_ = json.Unmarshal(b, &got)
	_ = json.Unmarshal([]byte(`{"error":{"message":"slow","type":"rate_limit_error","code":"rate_limit_error"}}`), &want)
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("body=%s", b)
	}
	if resp.Header.Get("Content-Length") != strconv.Itoa(len(b)) || resp.ContentLength != int64(len(b)) {
		t.Fatalf("content-length desalinhado")
	}
}

func TestAnthropicTranslateInterceptor_JSON200(t *testing.T) {
	resp := newResp(200, "application/json", readFixture(t, "resp_text.json"))
	if err := AnthropicTranslateInterceptor().Intercept(resp); err != nil {
		t.Fatalf("intercept: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Length") != strconv.Itoa(len(b)) || resp.ContentLength != int64(len(b)) {
		t.Fatalf("Content-Length %s vs %d", resp.Header.Get("Content-Length"), len(b))
	}
	if !bytes.Contains(b, []byte(`"chat.completion"`)) {
		t.Fatalf("não traduziu: %s", b)
	}
}

func TestAnthropicTranslateInterceptor_MalformedJSON200(t *testing.T) {
	resp := newResp(200, "application/json", []byte(`{broken`))
	if err := AnthropicTranslateInterceptor().Intercept(resp); err != nil {
		t.Fatalf("intercept: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte(`"error"`)) {
		t.Fatalf("body=%s", b)
	}
}
