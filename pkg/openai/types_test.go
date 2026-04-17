package openai_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

// TestChatCompletionRequest_RoundTrip ensures that a minimal chat request
// (model + one user message) survives a JSON marshal → unmarshal cycle
// byte-for-byte at the struct-equality level.
func TestChatCompletionRequest_RoundTrip(t *testing.T) {
	want := openai.ChatCompletionRequest{
		Model: "qwen",
		Messages: []openai.ChatCompletionMessage{
			{Role: "user", Content: "hi"},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got openai.ChatCompletionRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch: got %+v want %+v (raw=%s)", got, want, string(data))
	}
}

// TestToolCall_RoundTrip verifies that a response containing a tool_call
// preserves the function.arguments field as a raw JSON string (not an object)
// — that is the OpenAI wire format and what downstream consumers (the gateway
// in Phase 2) rely on.
func TestToolCall_RoundTrip(t *testing.T) {
	args := `{"x":1}`
	want := openai.ChatCompletionResponse{
		ID:      "resp-1",
		Object:  "chat.completion",
		Created: 1_700_000_000,
		Model:   "qwen",
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role: "assistant",
					ToolCalls: []openai.ToolCall{
						{
							ID:   "call_abc",
							Type: "function",
							Function: openai.ToolCallFunction{
								Name:      "get_weather",
								Arguments: args,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got openai.ChatCompletionResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch: got %+v want %+v (raw=%s)", got, want, string(data))
	}

	// Explicit assertion: arguments remained a JSON string, not expanded to an object.
	if got.Choices[0].Message.ToolCalls[0].Function.Arguments != args {
		t.Errorf("arguments drifted: got %q want %q",
			got.Choices[0].Message.ToolCalls[0].Function.Arguments, args)
	}
}

// TestChatCompletionMessage_OmitemptyContent verifies that a message with
// empty Content (assistant emitting only tool_calls) marshals WITHOUT a
// `content` key. Needed because downstream consumers differentiate "empty
// string" from "absent" at the OpenAI wire level.
func TestChatCompletionMessage_OmitemptyContent(t *testing.T) {
	msg := openai.ChatCompletionMessage{
		Role: "assistant",
		ToolCalls: []openai.ToolCall{
			{
				ID:   "c1",
				Type: "function",
				Function: openai.ToolCallFunction{
					Name:      "f",
					Arguments: "{}",
				},
			},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	raw := string(data)
	if strings.Contains(raw, `"content"`) {
		t.Errorf("omitempty violation: JSON should not contain \"content\" key; got %s", raw)
	}
	if !strings.Contains(raw, `"tool_calls"`) {
		t.Errorf("expected tool_calls in output; got %s", raw)
	}
}

// TestErrorResponse_OmitsEmptyCode ensures the OpenAI error envelope does not
// serialize the optional `code` field when it is empty.
func TestErrorResponse_OmitsEmptyCode(t *testing.T) {
	er := openai.ErrorResponse{
		Error: openai.ErrorDetail{
			Message: "oops",
			Type:    "invalid_request",
		},
	}

	data, err := json.Marshal(er)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	raw := string(data)
	if strings.Contains(raw, `"code"`) {
		t.Errorf("omitempty violation: JSON should not contain \"code\" key; got %s", raw)
	}
	want := `{"error":{"message":"oops","type":"invalid_request"}}`
	if raw != want {
		t.Errorf("unexpected serialization: got %s want %s", raw, want)
	}
}

// TestEmbeddingResponse_RoundTrip asserts that a 3-dim float32 embedding
// survives a JSON round-trip without precision loss in the slice length or
// the expected values (at float32 precision).
func TestEmbeddingResponse_RoundTrip(t *testing.T) {
	want := openai.EmbeddingResponse{
		Object: "list",
		Model:  "bge-m3",
		Data: []openai.Embedding{
			{
				Object:    "embedding",
				Index:     0,
				Embedding: []float32{0.1, 0.2, 0.3},
			},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got openai.EmbeddingResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch: got %+v want %+v (raw=%s)", got, want, string(data))
	}

	if len(got.Data) != 1 || len(got.Data[0].Embedding) != 3 {
		t.Fatalf("embedding shape drift: got Data=%d dims=%d want 1/3",
			len(got.Data), len(got.Data[0].Embedding))
	}

	for i, v := range []float32{0.1, 0.2, 0.3} {
		if got.Data[0].Embedding[i] != v {
			t.Errorf("embedding[%d] mismatch: got %v want %v", i, got.Data[0].Embedding[i], v)
		}
	}
}
