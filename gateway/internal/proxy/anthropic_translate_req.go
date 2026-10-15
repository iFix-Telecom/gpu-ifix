// Package proxy (anthropic_translate_req.go): tradutor de ENTRADA do passo ①
// da cadeia Haiku (Phase 23, HAIKU-01/HAIKU-05) — body OpenAI
// chat/completions → body Anthropic Messages (`POST /v1/messages`).
//
// Referências:
//   - 23-RESEARCH §Padrão 1 (tabela de mapeamento) e §Padrão 5 (effort/cache).
//   - 23-CONTEXT, decisão de discrição: tradutor próprio sobre
//     httputil.ReverseProxy, sem SDK como transporte. O endpoint
//     OpenAI-compatível da Anthropic foi descartado (não cacheia e ignora
//     reasoning_effort).
//   - Pontos confirmados na doc oficial (URLs no cabeçalho de
//     anthropic_translate_req_test.go): `tool_choice.disable_parallel_tool_use`,
//     `output_config.format`, `output_config.effort`, `thinking:{type:disabled}`
//     aceito no Haiku 5.5 com effort ≤ high, regra de posição das mensagens
//     system no meio da conversa, cache_control top-level.
//
// Saída por ALLOWLIST (T-23-25): só os campos montados aqui chegam à
// Anthropic; qualquer outro campo do cliente (temperature, top_p, seed,
// reasoning, provider, metadata, …) é descartado. Função pura: não faz I/O e
// nunca loga o body (R12).
package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
)

// Modos de tratamento da continuação de tool loop (revisão cross-AI, item 4
// do 23-REVIEWS). O gate ao vivo do 23-18 escolhe o modo; a env
// ANTHROPIC_TOOL_LOOP_MODE (23-14) chega aqui via AnthropicRequestOptions.
const (
	// ToolLoopModeOmit (padrão, "" equivale): continuação sai sem blocos
	// thinking (o cliente OpenAI não os tem) e com o effort normal.
	// HIPÓTESE H10 do research: a Anthropic aceita a omissão sem 400.
	ToolLoopModeOmit = "omit"
	// ToolLoopModeDisable: na continuação, thinking:{type:"disabled"} e sem
	// output_config.effort. FATO: Haiku 5.5 aceita disabled com effort ≤ high
	// (platform.claude.com/docs/en/build-with-claude/thinking); sem effort o
	// default é medium.
	ToolLoopModeDisable = "disable"
	// ToolLoopModeSkip é tratado pelo dispatcher (23-12): a continuação nem
	// chega ao translator. Se chegar, o translator age como omit.
	ToolLoopModeSkip = "skip"
)

// AnthropicJSONObjectInstruction é anexada ao system quando o cliente pede
// response_format json_object (sem equivalente na Messages API).
const AnthropicJSONObjectInstruction = "Responda somente com um objeto JSON válido."

// anthropicEffortLevels são os valores aceitos em output_config.effort.
var anthropicEffortLevels = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// anthropicDefaultMaxTokens é usado quando opts.DefaultMaxTokens ≤ 0.
const anthropicDefaultMaxTokens = 16000

// AnthropicRequestOptions configura a tradução. Valores do 23-12: Model
// "claude-haiku-5-5", DefaultMaxTokens 16000, ThinkingHeadroom 4096,
// MaxTokensCap 128000, Cache true, ToolLoopMode da env.
type AnthropicRequestOptions struct {
	// Model é o ID nativo enviado à Anthropic (substitui o alias do cliente).
	Model string
	// Effort ∈ {low,medium,high,xhigh,max} → output_config.effort;
	// "off" → thinking disabled sem effort; "" → não setar (default medium).
	Effort string
	// DefaultMaxTokens é usado quando o cliente não informa max_tokens.
	DefaultMaxTokens int
	// ThinkingHeadroom é somado ao max_tokens do cliente quando o thinking
	// pode rodar (Pitfall 3: thinking conta dentro de max_tokens).
	ThinkingHeadroom int
	// MaxTokensCap é o teto final (T-23-26). ≤ 0 = sem teto.
	MaxTokensCap int
	// Cache liga cache_control top-level + breakpoint no fim do system.
	Cache bool
	// ToolLoopMode: "" / "omit" / "disable" (ver constantes).
	ToolLoopMode string
}

var (
	errAnthropicMessagesMissing = errors.New("anthropic translate: messages ausente ou vazio")
)

// IsToolLoopContinuation diz se o body é o request seguinte de um tool loop:
// a ÚLTIMA mensagem assistant tem tool_calls não-vazio e todas as mensagens
// depois dela (≥ 1) são role "tool". JSON inválido → false.
func IsToolLoopContinuation(body []byte) bool {
	var m struct {
		Messages []struct {
			Role      string            `json:"role"`
			ToolCalls []json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	last := -1
	for i := len(m.Messages) - 1; i >= 0; i-- {
		if m.Messages[i].Role == "assistant" {
			last = i
			break
		}
	}
	if last < 0 || len(m.Messages[last].ToolCalls) == 0 || last == len(m.Messages)-1 {
		return false
	}
	for _, msg := range m.Messages[last+1:] {
		if msg.Role != "tool" {
			return false
		}
	}
	return true
}

// aBlock é um bloco de conteúdo Messages (map → json.Marshal ordena chaves,
// saída determinística para golden/cache).
type aBlock = map[string]any

type aMsg struct {
	role    string
	content []aBlock
}

// TranslateOpenAIToMessages traduz um body OpenAI chat/completions para o
// body da Anthropic Messages API. Erro só para JSON inválido ou messages
// ausente/vazio; demais problemas de forma são tolerados (best-effort).
func TranslateOpenAIToMessages(body []byte, opts AnthropicRequestOptions) ([]byte, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	rawMsgs, ok := in["messages"]
	if !ok {
		return nil, errAnthropicMessagesMissing
	}
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(rawMsgs, &msgs); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, errAnthropicMessagesMissing
	}
	continuation := IsToolLoopContinuation(body)

	// Prefill (F5): conversa terminando em assistant sem tool_calls ganha
	// turno user sintético — mesma regra do passo ② (anthropic_compat.go).
	msgs, _ = normalizeTrailingAssistant(msgs)

	// System/developer iniciais → system top-level.
	var system []aBlock
	start := 0
	for ; start < len(msgs); start++ {
		role := rawString(msgs[start]["role"])
		if role != "system" && role != "developer" {
			break
		}
		system = append(system, textBlocks(msgs[start]["content"])...)
	}

	conv := &msgConverter{}
	for i := start; i < len(msgs); i++ {
		m := msgs[i]
		role := rawString(m["role"])
		switch role {
		case "system", "developer":
			if b := textBlocks(m["content"]); len(b) > 0 {
				conv.pendingSys = append(conv.pendingSys, b)
			}
			continue
		case "user":
			conv.resolveSystem("user")
			conv.pushUser(userBlocks(m["content"]))
		case "tool":
			conv.resolveSystem("user")
			conv.pushUser([]aBlock{toolResultBlock(m)})
		case "assistant":
			conv.resolveSystem("assistant")
			conv.push("assistant", assistantBlocks(m))
		default:
			slog.Default().Debug("anthropic_translate: role desconhecido descartado", "role", role)
		}
	}
	conv.resolveSystem("")
	system = append(system, conv.extraSystem...)
	out := conv.finish()

	// Saída por allowlist.
	res := map[string]any{
		"model":    opts.Model,
		"messages": renderMsgs(out),
	}

	// tools / tool_choice / parallel_tool_calls.
	tools := translateTools(in["tools"])
	if len(tools) > 0 {
		res["tools"] = tools
		if tc := translateToolChoice(in["tool_choice"], in["parallel_tool_calls"]); tc != nil {
			res["tool_choice"] = tc
		}
	}

	// response_format.
	outputConfig := map[string]any{}
	switch format, schema := responseFormat(in["response_format"]); format {
	case "json_schema":
		outputConfig["format"] = map[string]any{"type": "json_schema", "schema": schema}
	case "json_object":
		system = append(system, aBlock{"type": "text", "text": AnthropicJSONObjectInstruction})
	}

	// effort / thinking (com a regra do ToolLoopMode).
	thinkingDisabled := false
	effort := ""
	mode := opts.ToolLoopMode
	switch mode {
	case "", ToolLoopModeOmit, ToolLoopModeDisable, ToolLoopModeSkip:
	default:
		slog.Default().Warn("anthropic_translate: ToolLoopMode desconhecido, usando omit", "mode", mode)
	}
	switch {
	case mode == ToolLoopModeDisable && continuation:
		thinkingDisabled = true
	case opts.Effort == "off":
		thinkingDisabled = true
	case anthropicEffortLevels[opts.Effort]:
		effort = opts.Effort
	case opts.Effort != "":
		slog.Default().Warn("anthropic_translate: effort inválido ignorado", "effort", opts.Effort)
	}
	if thinkingDisabled {
		res["thinking"] = map[string]any{"type": "disabled"}
	} else if effort != "" {
		outputConfig["effort"] = effort
	}
	if len(outputConfig) > 0 {
		res["output_config"] = outputConfig
	}

	// max_tokens (obrigatório na Anthropic).
	maxTokens, clientSet := clientMaxTokens(in)
	if !clientSet {
		maxTokens = opts.DefaultMaxTokens
		if maxTokens <= 0 {
			maxTokens = anthropicDefaultMaxTokens
		}
	} else if !thinkingDisabled && effort != "low" && opts.ThinkingHeadroom > 0 {
		maxTokens += opts.ThinkingHeadroom
	}
	if opts.MaxTokensCap > 0 && maxTokens > opts.MaxTokensCap {
		maxTokens = opts.MaxTokensCap
	}
	if maxTokens < 1 {
		maxTokens = 1
	}
	res["max_tokens"] = maxTokens

	// stop → stop_sequences.
	if stops := stopSequences(in["stop"]); len(stops) > 0 {
		res["stop_sequences"] = stops
	}

	// Cache: automático top-level + breakpoint explícito no fim do system.
	if opts.Cache {
		res["cache_control"] = map[string]any{"type": "ephemeral"}
		if len(system) > 0 {
			system[len(system)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		}
	}
	if len(system) > 0 {
		res["system"] = system
	}

	var stream bool
	if err := json.Unmarshal(in["stream"], &stream); err == nil && stream {
		res["stream"] = true
	}

	return json.Marshal(res)
}

// msgConverter monta a lista de turnos Messages, mesclando turnos
// consecutivos do mesmo role e posicionando mensagens system do meio.
type msgConverter struct {
	out         []aMsg
	pendingSys  [][]aBlock
	prefixUser  []aBlock
	extraSystem []aBlock
}

func (c *msgConverter) lastRole() string {
	if len(c.out) == 0 {
		return ""
	}
	return c.out[len(c.out)-1].role
}

// push anexa blocos; turno consecutivo do mesmo role (exceto system) é
// mesclado no anterior.
func (c *msgConverter) push(role string, blocks []aBlock) {
	if len(blocks) == 0 {
		return
	}
	if role != "system" && c.lastRole() == role {
		c.out[len(c.out)-1].content = append(c.out[len(c.out)-1].content, blocks...)
		return
	}
	c.out = append(c.out, aMsg{role: role, content: blocks})
}

func (c *msgConverter) pushUser(blocks []aBlock) {
	if len(c.prefixUser) > 0 {
		blocks = append(c.prefixUser, blocks...)
		c.prefixUser = nil
	}
	c.push("user", blocks)
}

// resolveSystem decide o destino das mensagens system acumuladas, dado o
// role do próximo turno ("" = fim da conversa). Regra de posição (doc
// mid-conversation-system-messages): nativa só logo após um turno user
// (inclui user com tool_result) e se for a última ou seguida de assistant.
// Fora da regra: dobra no turno user adjacente (anterior, senão o próximo).
// Sem user adjacente: bloco extra do system top-level.
func (c *msgConverter) resolveSystem(next string) {
	if len(c.pendingSys) == 0 {
		return
	}
	groups := c.pendingSys
	c.pendingSys = nil
	prevUser := c.lastRole() == "user"
	switch {
	case prevUser && (next == "assistant" || next == ""):
		for _, g := range groups {
			c.out = append(c.out, aMsg{role: "system", content: g})
		}
	case prevUser:
		for _, g := range groups {
			c.out[len(c.out)-1].content = append(c.out[len(c.out)-1].content, g...)
		}
	case next == "user":
		for _, g := range groups {
			c.prefixUser = append(c.prefixUser, g...)
		}
	default:
		for _, g := range groups {
			c.extraSystem = append(c.extraSystem, g...)
		}
	}
}

// finish aplica as garantias finais: tool_result primeiro em cada turno user
// (exigência da Messages API), conversa não-vazia e sem terminar em
// assistant sem tool_use (prefill → 400).
func (c *msgConverter) finish() []aMsg {
	if len(c.prefixUser) > 0 {
		c.out = append(c.out, aMsg{role: "user", content: c.prefixUser})
		c.prefixUser = nil
	}
	for i := range c.out {
		if c.out[i].role == "user" {
			c.out[i].content = toolResultsFirst(c.out[i].content)
		}
	}
	needPrefillUser := len(c.out) == 0
	if !needPrefillUser {
		last := c.out[len(c.out)-1]
		if last.role == "assistant" && !hasBlockType(last.content, "tool_use") {
			needPrefillUser = true
		}
	}
	if needPrefillUser {
		c.out = append(c.out, aMsg{role: "user", content: []aBlock{{"type": "text", "text": AnthropicPrefillUserText}}})
	}
	return c.out
}

func renderMsgs(msgs []aMsg) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{"role": m.role, "content": m.content})
	}
	return out
}

func hasBlockType(blocks []aBlock, typ string) bool {
	for _, b := range blocks {
		if b["type"] == typ {
			return true
		}
	}
	return false
}

func toolResultsFirst(blocks []aBlock) []aBlock {
	if !hasBlockType(blocks, "tool_result") {
		return blocks
	}
	sorted := make([]aBlock, 0, len(blocks))
	for _, b := range blocks {
		if b["type"] == "tool_result" {
			sorted = append(sorted, b)
		}
	}
	for _, b := range blocks {
		if b["type"] != "tool_result" {
			sorted = append(sorted, b)
		}
	}
	return sorted
}

func rawString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// contentPart é uma parte de content array OpenAI.
type contentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ImageURL json.RawMessage `json:"image_url"`
}

func parseContent(raw json.RawMessage) (string, []contentPart, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil, false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, nil, true
		}
		return "", nil, false
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, false
	}
	return "", parts, true
}

// textBlocks converte content (string ou partes) em blocos text, ignorando
// partes não-texto e textos vazios (a Anthropic rejeita text vazio).
func textBlocks(raw json.RawMessage) []aBlock {
	s, parts, ok := parseContent(raw)
	if !ok {
		return nil
	}
	if parts == nil {
		if s == "" {
			return nil
		}
		return []aBlock{{"type": "text", "text": s}}
	}
	var out []aBlock
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			out = append(out, aBlock{"type": "text", "text": p.Text})
		}
	}
	return out
}

// userBlocks converte content de user: texto + imagens.
func userBlocks(raw json.RawMessage) []aBlock {
	s, parts, ok := parseContent(raw)
	if !ok {
		return nil
	}
	if parts == nil {
		if s == "" {
			return nil
		}
		return []aBlock{{"type": "text", "text": s}}
	}
	var out []aBlock
	for _, p := range parts {
		switch p.Type {
		case "text":
			if p.Text != "" {
				out = append(out, aBlock{"type": "text", "text": p.Text})
			}
		case "image_url":
			if b := imageBlock(p.ImageURL); b != nil {
				out = append(out, b)
			}
		default:
			slog.Default().Debug("anthropic_translate: parte de content descartada", "type", p.Type)
		}
	}
	return out
}

// imageBlock: data URL base64 → source base64; http(s) → source url.
func imageBlock(raw json.RawMessage) aBlock {
	var url string
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.URL != "" {
		url = obj.URL
	} else {
		url = rawString(raw)
	}
	if url == "" {
		return nil
	}
	if strings.HasPrefix(url, "data:") {
		meta, data, found := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
		media, isB64 := strings.CutSuffix(meta, ";base64")
		if !found || !isB64 || media == "" || data == "" {
			slog.Default().Debug("anthropic_translate: data URL de imagem não-base64 descartada")
			return nil
		}
		return aBlock{"type": "image", "source": map[string]any{"type": "base64", "media_type": media, "data": data}}
	}
	return aBlock{"type": "image", "source": map[string]any{"type": "url", "url": url}}
}

// toolResultBlock converte uma mensagem role tool em bloco tool_result.
func toolResultBlock(m map[string]json.RawMessage) aBlock {
	b := aBlock{"type": "tool_result", "tool_use_id": rawString(m["tool_call_id"])}
	s, parts, ok := parseContent(m["content"])
	if !ok {
		return b
	}
	if parts == nil {
		if s != "" {
			b["content"] = s
		}
		return b
	}
	if tb := textBlocks(m["content"]); len(tb) > 0 {
		b["content"] = tb
	}
	return b
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// assistantBlocks: texto (se houver) + tool_use por tool_call.
func assistantBlocks(m map[string]json.RawMessage) []aBlock {
	out := textBlocks(m["content"])
	var calls []openAIToolCall
	if raw, ok := m["tool_calls"]; ok {
		if err := json.Unmarshal(raw, &calls); err != nil {
			slog.Default().Debug("anthropic_translate: tool_calls ilegível descartado")
			calls = nil
		}
	}
	for _, tc := range calls {
		out = append(out, aBlock{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Function.Name,
			"input": toolInput(tc.Function.Arguments),
		})
	}
	return out
}

// toolInput converte `arguments` (string JSON, ou objeto já decodificado)
// num objeto JSON. Inválido / não-objeto → {} (T-23-27, sem panic).
func toolInput(raw json.RawMessage) json.RawMessage {
	empty := json.RawMessage(`{}`)
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return empty
	}
	candidate := raw
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return empty
		}
		candidate = bytes.TrimSpace([]byte(s))
	}
	if len(candidate) == 0 || candidate[0] != '{' || !json.Valid(candidate) {
		if len(candidate) > 0 {
			slog.Default().Debug("anthropic_translate: arguments inválido virou {}")
		}
		return empty
	}
	return json.RawMessage(candidate)
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict"`
	} `json:"function"`
}

func translateTools(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var tools []openAITool
	if err := json.Unmarshal(raw, &tools); err != nil {
		slog.Default().Debug("anthropic_translate: tools ilegível descartado")
		return nil
	}
	var out []map[string]any
	for _, t := range tools {
		if t.Function.Name == "" {
			continue
		}
		schema := bytes.TrimSpace(t.Function.Parameters)
		if len(schema) == 0 || bytes.Equal(schema, []byte("null")) {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tool := map[string]any{"name": t.Function.Name, "input_schema": json.RawMessage(schema)}
		if t.Function.Description != "" {
			tool["description"] = t.Function.Description
		}
		if t.Function.Strict != nil && *t.Function.Strict {
			tool["strict"] = true
		}
		out = append(out, tool)
	}
	return out
}

// translateToolChoice mapeia tool_choice OpenAI → Anthropic e aplica
// parallel_tool_calls:false como disable_parallel_tool_use.
func translateToolChoice(raw, parallel json.RawMessage) map[string]any {
	var choice map[string]any
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		kind := ""
		name := ""
		if raw[0] == '"' {
			kind = rawString(raw)
		} else {
			var obj struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if err := json.Unmarshal(raw, &obj); err == nil {
				kind = obj.Type
				name = obj.Function.Name
			}
		}
		switch kind {
		case "auto":
			choice = map[string]any{"type": "auto"}
		case "none":
			choice = map[string]any{"type": "none"}
		case "required":
			choice = map[string]any{"type": "any"}
		case "function":
			if name != "" {
				choice = map[string]any{"type": "tool", "name": name}
			}
		}
	}
	var par bool
	if err := json.Unmarshal(parallel, &par); err == nil && !par {
		if choice == nil {
			choice = map[string]any{"type": "auto"}
		}
		if choice["type"] != "none" {
			choice["disable_parallel_tool_use"] = true
		}
	}
	return choice
}

// responseFormat devolve ("json_schema", schema) / ("json_object", nil) / ("", nil).
func responseFormat(raw json.RawMessage) (string, json.RawMessage) {
	if len(raw) == 0 {
		return "", nil
	}
	var rf struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &rf); err != nil {
		return "", nil
	}
	switch rf.Type {
	case "json_schema":
		s := bytes.TrimSpace(rf.JSONSchema.Schema)
		if len(s) == 0 || bytes.Equal(s, []byte("null")) {
			return "", nil
		}
		return "json_schema", json.RawMessage(s)
	case "json_object":
		return "json_object", nil
	}
	return "", nil
}

// clientMaxTokens: max_completion_tokens tem precedência sobre max_tokens.
func clientMaxTokens(in map[string]json.RawMessage) (int, bool) {
	for _, k := range []string{"max_completion_tokens", "max_tokens"} {
		var f float64
		if err := json.Unmarshal(in[k], &f); err == nil && f > 0 {
			return int(f), true
		}
	}
	return 0, false
}

// stopSequences: `stop` string ou array → stop_sequences (sem vazios).
func stopSequences(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if s := rawString(raw); s != "" {
		list = []string{s}
	} else if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	out := list[:0]
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
