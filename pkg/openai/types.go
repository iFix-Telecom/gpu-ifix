// Package openai defines OpenAI-compatible request/response types shared
// between the pod health-bridge (Phase 1) and the Go gateway (Phase 2).
//
// Per D-13 (CONTEXT.md 01-CONTEXT.md), these structs live at repo root
// so both `pod/health-bridge/` and `gateway/` import the same contract
// via github.com/ifixtelecom/gpu-ifix/pkg/openai.
package openai

import "encoding/json"

// ChatCompletionRequest is the body of POST /v1/chat/completions.
type ChatCompletionRequest struct {
	Model       string                  `json:"model"`
	Messages    []ChatCompletionMessage `json:"messages"`
	Stream      bool                    `json:"stream,omitempty"`
	Tools       []Tool                  `json:"tools,omitempty"`
	MaxTokens   int                     `json:"max_tokens,omitempty"`
	Temperature *float64                `json:"temperature,omitempty"`
}

// ChatCompletionMessage is a single turn in a chat request or response.
//
// Content is omitempty because an assistant turn with tool_calls emits no
// textual content and the OpenAI wire format distinguishes "absent" from
// "empty string".
type ChatCompletionMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// Tool describes an OpenAI tool declaration in a request.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function definition inside a Tool. Parameters is kept
// as a raw JSON message so the gateway (Phase 2) can forward the arbitrary
// JSON Schema body without re-parsing.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is an OpenAI tool invocation produced by the model.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction captures the function name and arguments produced by the
// model. Arguments is a raw JSON string on the wire (OpenAI spec), so we
// keep it as a Go string to avoid lossy double-parsing.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatCompletionResponse is the body of a non-streaming POST /v1/chat/completions.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *Usage                 `json:"usage,omitempty"`
}

// ChatCompletionChoice is one of the `choices[]` entries in a response.
type ChatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      ChatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

// Usage is the token accounting block returned with most responses.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// EmbeddingRequest is the body of POST /v1/embeddings. OpenAI accepts both
// string and []string for Input; we canonicalize to []string at the gateway
// boundary so downstream code never has to union-type it.
type EmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbeddingResponse is the body of POST /v1/embeddings.
type EmbeddingResponse struct {
	Object string      `json:"object"`
	Data   []Embedding `json:"data"`
	Model  string      `json:"model"`
	Usage  *Usage      `json:"usage,omitempty"`
}

// Embedding is one vector entry inside an EmbeddingResponse.Data.
type Embedding struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// TranscriptionRequest holds the post-parse fields from a multipart
// POST /v1/audio/transcriptions request. The audio bytes themselves are
// streamed separately; only the metadata lives on this struct.
type TranscriptionRequest struct {
	Model    string `json:"model"`
	Language string `json:"language,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

// TranscriptionResponse is the body of POST /v1/audio/transcriptions.
type TranscriptionResponse struct {
	Text string `json:"text"`
}

// ErrorResponse is the OpenAI error envelope returned by all endpoints on
// non-2xx outcomes.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the body of ErrorResponse.Error.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}
