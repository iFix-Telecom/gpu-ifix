package proxy

// Fixtures testdata/anthropic/stream_*.sse seguem o formato de eventos
// documentado em https://platform.claude.com/docs/en/build-with-claude/streaming
// (message_start, content_block_start/delta/stop, text_delta,
// input_json_delta, thinking_delta, signature_delta, message_delta com usage
// cumulativo, message_stop, ping, error).

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeUpstream é um ReadCloser que entrega o conteúdo em pedaços de `step`
// bytes (0 = tudo de uma vez), opcionalmente devolve readErr no fim e
// registra Close.
type fakeUpstream struct {
	data    []byte
	step    int
	readErr error
	closed  atomic.Bool
}

func (f *fakeUpstream) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		if f.readErr != nil {
			return 0, f.readErr
		}
		return 0, io.EOF
	}
	n := len(p)
	if f.step > 0 && n > f.step {
		n = f.step
	}
	if n > len(f.data) {
		n = len(f.data)
	}
	copy(p, f.data[:n])
	f.data = f.data[n:]
	return n, nil
}

func (f *fakeUpstream) Close() error { f.closed.Store(true); return nil }

// drain lê o tradutor com buffer de `bufSize` bytes por Read.
func drain(t *testing.T, r io.Reader, bufSize int) string {
	t.Helper()
	var out bytes.Buffer
	p := make([]byte, bufSize)
	for i := 0; i < 1_000_000; i++ {
		n, err := r.Read(p)
		out.Write(p[:n])
		if err == io.EOF {
			return out.String()
		}
		if err != nil {
			t.Fatalf("erro de leitura propagado: %v", err)
		}
	}
	t.Fatalf("tradutor não terminou")
	return ""
}

func translateSSE(t *testing.T, in string) string {
	t.Helper()
	return drain(t, NewAnthropicSSETranslator(&fakeUpstream{data: []byte(in)}), 32*1024)
}

// frames separa a saída em payloads de `data: ` (sem o prefixo).
func frames(t *testing.T, out string) []string {
	t.Helper()
	if !strings.HasSuffix(out, "\n\n") {
		t.Fatalf("saída não termina em frame completo: %q", out)
	}
	var res []string
	for _, f := range strings.Split(strings.TrimSuffix(out, "\n\n"), "\n\n") {
		if !strings.HasPrefix(f, "data: ") || strings.Contains(f, "\n") {
			t.Fatalf("frame inválido: %q", f)
		}
		res = append(res, strings.TrimPrefix(f, "data: "))
	}
	return res
}

type oaChunkT struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string  `json:"role"`
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      *string `json:"name"`
					Arguments *string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsageT `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func parseChunks(t *testing.T, fs []string) []oaChunkT {
	t.Helper()
	if len(fs) == 0 || fs[len(fs)-1] != "[DONE]" {
		t.Fatalf("último frame deve ser [DONE]: %v", fs)
	}
	var cs []oaChunkT
	for _, f := range fs[:len(fs)-1] {
		if f == "[DONE]" {
			t.Fatalf("[DONE] duplicado")
		}
		var c oaChunkT
		if err := json.Unmarshal([]byte(f), &c); err != nil {
			t.Fatalf("chunk inválido %q: %v", f, err)
		}
		cs = append(cs, c)
	}
	return cs
}

func countUsage(cs []oaChunkT) (n int, last *oaUsageT) {
	for i := range cs {
		if cs[i].Usage != nil {
			n++
			last = cs[i].Usage
		}
	}
	return
}

func TestAnthropicSSE_Text(t *testing.T) {
	out := translateSSE(t, string(readFixture(t, "stream_text.sse")))
	if strings.Contains(out, "thinking") || strings.Contains(out, "ping") || strings.Contains(out, "signature") || strings.Contains(out, "Raciocínio") {
		t.Fatalf("thinking/ping vazou: %s", out)
	}
	cs := parseChunks(t, frames(t, out))
	if len(cs) != 6 {
		t.Fatalf("esperava 6 chunks (role, 3 content, finish, usage), veio %d: %s", len(cs), out)
	}
	for _, c := range cs {
		if c.ID != "msg_s1" || c.Object != "chat.completion.chunk" || c.Model != "claude-haiku-5-5" {
			t.Fatalf("cabeçalho de chunk errado: %+v", c)
		}
	}
	if cs[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("1º chunk deve ter role assistant")
	}
	want := []string{"Olá", ", ", "mundo"}
	for i, w := range want {
		d := cs[1+i].Choices[0].Delta
		if d.Content == nil || *d.Content != w {
			t.Fatalf("content[%d]=%v want %q", i, d.Content, w)
		}
	}
	fr := cs[4].Choices[0].FinishReason
	if fr == nil || *fr != "stop" {
		t.Fatalf("finish_reason=%v", fr)
	}
	if len(cs[5].Choices) != 0 || cs[5].Usage == nil {
		t.Fatalf("último chunk deve ser usage com choices:[]")
	}
	if !strings.Contains(frames(t, out)[5], `"choices":[]`) {
		t.Fatalf("usage chunk deve ter choices:[] literal")
	}
	u := cs[5].Usage
	if u.PromptTokens != 450 || u.CompletionTokens != 12 || u.TotalTokens != 462 || u.PromptTokensDetails.CachedTokens != 400 {
		t.Fatalf("usage=%+v", u)
	}
}

func TestAnthropicSSE_ToolUse(t *testing.T) {
	out := translateSSE(t, string(readFixture(t, "stream_tool_use.sse")))
	cs := parseChunks(t, frames(t, out))
	args := map[int]string{}
	ids := map[int]string{}
	names := map[int]string{}
	var finish string
	for _, c := range cs {
		for _, ch := range c.Choices {
			for _, tc := range ch.Delta.ToolCalls {
				if tc.ID != "" {
					if _, dup := ids[tc.Index]; dup {
						t.Fatalf("id repetido no index %d", tc.Index)
					}
					ids[tc.Index] = tc.ID
					if tc.Type != "function" || tc.Function.Name == nil || tc.Function.Arguments == nil || *tc.Function.Arguments != "" {
						t.Fatalf("1º chunk do tool %d incompleto: %+v", tc.Index, tc)
					}
					names[tc.Index] = *tc.Function.Name
				}
				if tc.Function.Arguments != nil {
					args[tc.Index] += *tc.Function.Arguments
				}
			}
			if ch.FinishReason != nil {
				finish = *ch.FinishReason
			}
		}
	}
	if len(ids) != 2 || ids[0] != "toolu_a" || ids[1] != "toolu_b" {
		t.Fatalf("ids=%v (index deve ser contador próprio 0,1)", ids)
	}
	if names[0] != "get_weather" || names[1] != "get_time" {
		t.Fatalf("names=%v", names)
	}
	var a0, a1 map[string]string
	if err := json.Unmarshal([]byte(args[0]), &a0); err != nil || a0["location"] != "São Paulo" {
		t.Fatalf("args[0]=%q err=%v", args[0], err)
	}
	if err := json.Unmarshal([]byte(args[1]), &a1); err != nil || a1["tz"] != "BRT" {
		t.Fatalf("args[1]=%q err=%v", args[1], err)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish=%q", finish)
	}
	if !strings.Contains(out, `"tool_calls"`) {
		t.Fatalf("toolCallTee procura a substring \"tool_calls\"")
	}
	if n, u := countUsage(cs); n != 1 || u.PromptTokens != 472 || u.CompletionTokens != 89 {
		t.Fatalf("usage n=%d u=%+v", n, u)
	}
}

func TestAnthropicSSE_ErrorEvent(t *testing.T) {
	out := translateSSE(t, string(readFixture(t, "stream_error.sse")))
	cs := parseChunks(t, frames(t, out))
	last := cs[len(cs)-1]
	if last.Error == nil || last.Error.Type != "overloaded_error" || last.Error.Message != "Overloaded" || last.Error.Code != "overloaded_error" {
		t.Fatalf("chunk de erro ausente/errado: %s", out)
	}
	if !strings.Contains(out, "Parcial") {
		t.Fatalf("conteúdo anterior ao erro perdido")
	}
}

var createdRe = regexp.MustCompile(`"created":\d+`)

func normCreated(s string) string { return createdRe.ReplaceAllString(s, `"created":0`) }

func TestAnthropicSSE_OneByteReads(t *testing.T) {
	for _, fx := range []string{"stream_text.sse", "stream_tool_use.sse", "stream_error.sse"} {
		in := readFixture(t, fx)
		whole := translateSSE(t, string(in))
		tiny := drain(t, NewAnthropicSSETranslator(&fakeUpstream{data: in, step: 1}), 1)
		if normCreated(whole) != normCreated(tiny) {
			t.Fatalf("%s: saída difere com leitura de 1 byte\nwhole=%s\ntiny=%s", fx, whole, tiny)
		}
	}
}

func TestAnthropicSSE_CloseDelegates(t *testing.T) {
	up := &fakeUpstream{data: readFixture(t, "stream_text.sse")}
	tr := NewAnthropicSSETranslator(up)
	_ = drain(t, tr, 64)
	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !up.closed.Load() {
		t.Fatalf("Close não delegou ao upstream")
	}
}

// truncatedText corta stream_text.sse logo após o 2º text_delta.
func truncatedText(t *testing.T) string {
	t.Helper()
	full := string(readFixture(t, "stream_text.sse"))
	i := strings.Index(full, `"text":", "}}`)
	if i < 0 {
		t.Fatalf("fixture mudou")
	}
	j := strings.Index(full[i:], "\n\n")
	return full[:i+j+2]
}

func TestAnthropicSSE_TruncatedEOF(t *testing.T) {
	out := translateSSE(t, truncatedText(t))
	cs := parseChunks(t, frames(t, out))
	// role, "Olá", ", ", erro, usage
	if len(cs) != 5 {
		t.Fatalf("chunks=%d: %s", len(cs), out)
	}
	if cs[3].Error == nil || cs[3].Error.Type != "upstream_stream_truncated" || cs[3].Error.Code != "upstream_stream_truncated" ||
		cs[3].Error.Message != "upstream stream ended before message_stop" {
		t.Fatalf("erro sintético errado: %+v", cs[3].Error)
	}
	if cs[4].Usage == nil || cs[4].Usage.PromptTokens != 450 {
		t.Fatalf("usage pós-truncamento ausente: %s", out)
	}
}

func TestAnthropicSSE_TruncatedBeforeMessageStart(t *testing.T) {
	out := translateSSE(t, "")
	cs := parseChunks(t, frames(t, out))
	if len(cs) != 1 || cs[0].Error == nil || cs[0].Error.Type != "upstream_stream_truncated" {
		t.Fatalf("saída=%s", out)
	}
	if n, _ := countUsage(cs); n != 0 {
		t.Fatalf("sem message_start não deve haver usage")
	}
}

func TestAnthropicSSE_ReadError(t *testing.T) {
	up := &fakeUpstream{data: []byte(truncatedText(t)), readErr: errors.New("connection reset")}
	out := drain(t, NewAnthropicSSETranslator(up), 4096) // drain falha se o erro propagar
	cs := parseChunks(t, frames(t, out))
	var errChunk *oaChunkT
	for i := range cs {
		if cs[i].Error != nil {
			errChunk = &cs[i]
		}
	}
	if errChunk == nil || errChunk.Error.Type != "upstream_read_error" || errChunk.Error.Code != "upstream_read_error" {
		t.Fatalf("saída=%s", out)
	}
	if strings.Contains(out, "connection reset") {
		t.Fatalf("detalhe interno do erro de leitura vazou para o cliente")
	}
}

func TestAnthropicSSE_CRLF(t *testing.T) {
	for _, fx := range []string{"stream_text.sse", "stream_tool_use.sse"} {
		lf := string(readFixture(t, fx))
		crlf := strings.ReplaceAll(lf, "\n", "\r\n")
		a := normCreated(translateSSE(t, lf))
		b := normCreated(translateSSE(t, crlf))
		c := normCreated(drain(t, NewAnthropicSSETranslator(&fakeUpstream{data: []byte(crlf), step: 1}), 3))
		if a != b || a != c {
			t.Fatalf("%s: CRLF difere\nlf=%s\ncrlf=%s\ncrlf1=%s", fx, a, b, c)
		}
	}
}

func TestAnthropicSSE_MultiLineData(t *testing.T) {
	in := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\n" +
		"data: \"message\":{\"id\":\"msg_m\",\"model\":\"claude-haiku-5-5\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\n" +
		"data:\"delta\":{\"type\":\"text_delta\",\"text\":\"oi\"}}\n\n" +
		": comentário SSE ignorado\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	cs := parseChunks(t, frames(t, translateSSE(t, in)))
	if len(cs) != 3 || cs[0].ID != "msg_m" || cs[1].Choices[0].Delta.Content == nil || *cs[1].Choices[0].Delta.Content != "oi" {
		t.Fatalf("multi data: %+v", cs)
	}
}

func TestAnthropicSSE_CumulativeUsage(t *testing.T) {
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_c\",\"model\":\"claude-haiku-5-5\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":5,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":null},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"input_tokens\":20,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":3,\"output_tokens\":12}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	out := translateSSE(t, in)
	cs := parseChunks(t, frames(t, out))
	n, u := countUsage(cs)
	if n != 1 {
		t.Fatalf("usage emitido %d vezes", n)
	}
	if u.CompletionTokens != 12 || u.PromptTokens != 30 || u.PromptTokensDetails.CachedTokens != 7 || u.PromptTokensDetails.CacheWriteTokens != 3 || u.TotalTokens != 42 {
		t.Fatalf("usage=%+v", u)
	}
	if cs[len(cs)-1].Usage == nil {
		t.Fatalf("usage deve ser o último chunk antes do [DONE]")
	}
	finishes := 0
	for _, c := range cs {
		for _, ch := range c.Choices {
			if ch.FinishReason != nil {
				finishes++
				if *ch.FinishReason != "length" {
					t.Fatalf("finish=%q", *ch.FinishReason)
				}
			}
		}
	}
	if finishes != 1 {
		t.Fatalf("finish_reason emitido %d vezes", finishes)
	}
}

func TestAnthropicSSE_FrameTooLarge(t *testing.T) {
	in := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_big\",\"model\":\"m\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"data: " + strings.Repeat("x", 2<<20)
	out := translateSSE(t, in)
	cs := parseChunks(t, frames(t, out))
	var found bool
	for _, c := range cs {
		if c.Error != nil && c.Error.Type == "upstream_frame_too_large" {
			found = true
		}
	}
	if !found || len(out) > 4096 {
		t.Fatalf("frame gigante deve virar erro curto: len=%d", len(out))
	}
}

func TestAnthropicTranslateInterceptor_SSE(t *testing.T) {
	resp := newResp(200, "text/event-stream; charset=utf-8", readFixture(t, "stream_text.sse"))
	if err := AnthropicTranslateInterceptor().Intercept(resp); err != nil {
		t.Fatalf("intercept: %v", err)
	}
	if resp.Header.Get("Content-Length") != "" || resp.ContentLength != -1 {
		t.Fatalf("Content-Length deve ser removido em SSE")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.HasSuffix(b, []byte("data: [DONE]\n\n")) || !bytes.Contains(b, []byte("chat.completion.chunk")) {
		t.Fatalf("saída SSE: %s", b)
	}
}
