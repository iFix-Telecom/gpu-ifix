// Package proxy (anthropic_translate_sse.go): tradutor de stream SSE da
// Messages API (passo ①, Phase 23 HAIKU-01) para chunks
// `chat.completion.chunk` OpenAI terminando em `data: [DONE]`.
//
// Conversão (eventos documentados em build-with-claude/streaming):
//   - message_start        → 1º chunk delta{role:"assistant"}; guarda id, model, usage de entrada
//   - content_block_start  → tool_use: delta.tool_calls[{index:k,id,type,function{name,arguments:""}}]
//     (k = contador próprio de tool_use, NÃO o índice do bloco); text com texto inicial → delta.content
//   - text_delta           → delta.content
//   - input_json_delta     → delta.tool_calls[{index:k,function{arguments:partial}}]
//   - thinking/redacted_thinking, thinking_delta, signature_delta, ping → descartados
//   - message_delta        → chunk com finish_reason; usage cumulativo vira ESTADO (último valor)
//   - message_stop         → UM chunk de usage (choices:[]) + `data: [DONE]`
//   - error (pós-200)      → chunk {"error":{message,type,code}} + `data: [DONE]` — sem cascata,
//     os bytes já saíram para o cliente (D-07)
//
// Bordas: aceita `\r\n`, `\r` e `\n`; várias linhas `data:` do mesmo evento são
// unidas com `\n`; EOF antes de message_stop → chunk de erro sintético
// `upstream_stream_truncated`; erro de leitura não-EOF → `upstream_read_error`;
// em ambos segue o usage (se message_start chegou), `[DONE]` e io.EOF — o erro
// NÃO é propagado (com erro o ReverseProxy aborta sem fechar o SSE e o cliente
// fica pendurado). Frame pendente > 1 MiB → `upstream_frame_too_large` (T-23-19).
// `[DONE]` nunca é emitido duas vezes. Nunca loga body.
package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"
)

// anthSSEFrameCap é o maior frame SSE pendente (sem terminador) aceito.
const anthSSEFrameCap = 1 << 20

// anthSSEReadChunk é o tamanho de cada leitura do upstream.
const anthSSEReadChunk = 32 << 10

type anthSSEEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string     `json:"id"`
		Model string     `json:"model"`
		Usage *anthUsage `json:"usage"`
	} `json:"message"`
	Index        int `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta *struct {
		Type        string  `json:"type"`
		Text        string  `json:"text"`
		PartialJSON string  `json:"partial_json"`
		StopReason  *string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicSSETranslator implementa io.ReadCloser.
type anthropicSSETranslator struct {
	upstream io.ReadCloser
	log      *slog.Logger

	in      bytes.Buffer // bytes normalizados (LF) ainda sem frame completo
	out     bytes.Buffer // chunks OpenAI prontos para Read
	lastCR  bool         // último byte de entrada foi \r (CRLF dividido entre leituras)
	readBuf []byte

	finished bool // não ler mais do upstream
	done     bool // `[DONE]` já emitido

	msgID    string
	model    string
	created  int64
	started  bool
	usage    anthUsageState
	toolIdx  int
	blockMap map[int]int // índice do bloco → índice do tool_call OpenAI
}

// NewAnthropicSSETranslator embrulha o body SSE da Messages API e devolve um
// ReadCloser que produz chunks OpenAI. Close delega ao upstream (contrato de
// interceptor.go).
func NewAnthropicSSETranslator(upstream io.ReadCloser) io.ReadCloser {
	return &anthropicSSETranslator{
		upstream: upstream,
		log:      slog.Default(),
		created:  time.Now().Unix(),
		blockMap: map[int]int{},
	}
}

func (t *anthropicSSETranslator) Close() error { return t.upstream.Close() }

func (t *anthropicSSETranslator) Read(p []byte) (int, error) {
	for t.out.Len() == 0 && !t.finished {
		t.fill()
	}
	if t.out.Len() > 0 {
		return t.out.Read(p)
	}
	return 0, io.EOF
}

// fill faz uma leitura do upstream e processa os frames completos.
func (t *anthropicSSETranslator) fill() {
	if t.readBuf == nil {
		t.readBuf = make([]byte, anthSSEReadChunk)
	}
	n, err := t.upstream.Read(t.readBuf)
	if n > 0 {
		t.appendNormalized(t.readBuf[:n])
		t.processFrames()
		if !t.finished && t.in.Len() > anthSSEFrameCap {
			t.log.Warn("anthropic sse: frame acima do limite", "msg_id", t.msgID, "limit_bytes", anthSSEFrameCap)
			t.in.Reset()
			t.terminate("upstream SSE frame exceeds size limit", "upstream_frame_too_large", true)
		}
	}
	if t.finished || err == nil {
		return
	}
	if errors.Is(err, io.EOF) {
		// Último frame sem terminador: processa o que sobrou.
		if len(bytes.TrimSpace(t.in.Bytes())) > 0 {
			t.in.WriteString("\n\n")
			t.processFrames()
		}
		if !t.finished {
			t.log.Warn("anthropic sse: stream terminou antes de message_stop", "msg_id", t.msgID)
			t.terminate("upstream stream ended before message_stop", "upstream_stream_truncated", true)
		}
		return
	}
	t.log.Warn("anthropic sse: erro de leitura do upstream", "msg_id", t.msgID, "err", err.Error())
	t.terminate("upstream stream read error", "upstream_read_error", true)
}

// appendNormalized converte \r\n e \r em \n, tolerando CRLF dividido entre
// leituras.
func (t *anthropicSSETranslator) appendNormalized(b []byte) {
	for _, c := range b {
		switch {
		case c == '\r':
			t.in.WriteByte('\n')
			t.lastCR = true
		case c == '\n' && t.lastCR:
			t.lastCR = false
		default:
			t.in.WriteByte(c)
			t.lastCR = false
		}
	}
}

// processFrames consome todos os frames completos (`\n\n`) do buffer.
func (t *anthropicSSETranslator) processFrames() {
	for !t.finished {
		data := t.in.Bytes()
		i := bytes.Index(data, []byte("\n\n"))
		if i < 0 {
			return
		}
		frame := make([]byte, i)
		copy(frame, data[:i])
		t.in.Next(i + 2)
		t.handleFrame(frame)
	}
}

func (t *anthropicSSETranslator) handleFrame(frame []byte) {
	var eventName string
	var dataLines [][]byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value := line, []byte(nil)
		if k := bytes.IndexByte(line, ':'); k >= 0 {
			field, value = line[:k], line[k+1:]
			value = bytes.TrimPrefix(value, []byte(" "))
		}
		switch string(field) {
		case "event":
			eventName = string(value)
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if len(dataLines) == 0 {
		return
	}
	payload := bytes.Join(dataLines, []byte("\n"))

	var ev anthSSEEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		if eventName == "error" {
			t.terminate("upstream error", "upstream_error", false)
		}
		return
	}
	if ev.Type == "" {
		ev.Type = eventName
	}
	t.handleEvent(&ev)
}

func (t *anthropicSSETranslator) handleEvent(ev *anthSSEEvent) {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			t.msgID = ev.Message.ID
			t.model = ev.Message.Model
			t.usage.merge(ev.Message.Usage)
		}
		t.started = true
		t.emitChoice(map[string]any{"role": "assistant", "content": ""}, nil)

	case "content_block_start":
		cb := ev.ContentBlock
		if cb == nil {
			return
		}
		switch cb.Type {
		case "tool_use":
			k := t.toolIdx
			t.toolIdx++
			t.blockMap[ev.Index] = k
			t.emitChoice(map[string]any{"tool_calls": []any{map[string]any{
				"index": k,
				"id":    cb.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      cb.Name,
					"arguments": "",
				},
			}}}, nil)
		case "text":
			if cb.Text != "" {
				t.emitChoice(map[string]any{"content": cb.Text}, nil)
			}
		}

	case "content_block_delta":
		d := ev.Delta
		if d == nil {
			return
		}
		switch d.Type {
		case "text_delta":
			if d.Text != "" {
				t.emitChoice(map[string]any{"content": d.Text}, nil)
			}
		case "input_json_delta":
			k, ok := t.blockMap[ev.Index]
			if !ok || d.PartialJSON == "" {
				return
			}
			t.emitChoice(map[string]any{"tool_calls": []any{map[string]any{
				"index":    k,
				"function": map[string]any{"arguments": d.PartialJSON},
			}}}, nil)
		}

	case "message_delta":
		t.usage.merge(ev.Usage)
		if ev.Delta != nil && ev.Delta.StopReason != nil {
			if fr := anthMapStopReason(*ev.Delta.StopReason); fr != "" {
				t.emitChoice(map[string]any{}, fr)
			}
		}

	case "message_stop":
		t.emitUsage()
		t.emitDone()

	case "error":
		msg, typ := "upstream error", "upstream_error"
		if ev.Error != nil {
			if ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			if ev.Error.Type != "" {
				typ = ev.Error.Type
			}
		}
		t.log.Warn("anthropic sse: evento error após 200", "msg_id", t.msgID, "type", typ)
		t.emitError(msg, typ)
		t.emitDone()

	default:
		// ping, content_block_stop e eventos desconhecidos: descartados.
	}
}

// terminate encerra o stream com chunk de erro, usage (opcional, só se
// message_start chegou) e `[DONE]`.
func (t *anthropicSSETranslator) terminate(msg, typ string, withUsage bool) {
	if t.done {
		t.finished = true
		return
	}
	t.emitError(msg, typ)
	if withUsage {
		t.emitUsage()
	}
	t.emitDone()
}

func (t *anthropicSSETranslator) emitChoice(delta map[string]any, finish any) {
	t.emitJSON(map[string]any{
		"id":      t.msgID,
		"object":  "chat.completion.chunk",
		"created": t.created,
		"model":   t.model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
	})
}

func (t *anthropicSSETranslator) emitUsage() {
	if !t.started {
		return
	}
	t.emitJSON(map[string]any{
		"id":      t.msgID,
		"object":  "chat.completion.chunk",
		"created": t.created,
		"model":   t.model,
		"choices": []any{},
		"usage":   t.usage.openAI(),
	})
}

func (t *anthropicSSETranslator) emitError(msg, typ string) {
	t.emitJSON(map[string]any{"error": map[string]any{
		"message": msg,
		"type":    typ,
		"code":    typ,
	}})
}

func (t *anthropicSSETranslator) emitDone() {
	t.finished = true
	if t.done {
		return
	}
	t.done = true
	t.out.WriteString("data: [DONE]\n\n")
}

func (t *anthropicSSETranslator) emitJSON(v any) {
	if t.done {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	t.out.WriteString("data: ")
	t.out.Write(b)
	t.out.WriteString("\n\n")
}
