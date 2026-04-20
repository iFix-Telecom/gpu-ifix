package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

// reqCtxWithRequestID is a tiny helper. The tests inject a request_id
// directly via context.WithValue using the string-key idiom — we cannot
// import httpx's unexported requestIDKey and we don't want a real
// httpx.RequestID middleware just to verify the interceptor.
//
// Instead the tests build a Response with an explicit Request and
// override the registered flag map directly to assert the read-side
// behavior.

// TestToolCallInterceptor_NonSSEIsNoOp verifies that the interceptor
// passes through non-SSE responses without wrapping the body.
func TestToolCallInterceptor_NonSSEIsNoOp(t *testing.T) {
	ti := NewToolCallInterceptor()
	body := io.NopCloser(strings.NewReader(`{"choices":[]}`))
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
		Request:    httptest.NewRequest("POST", "/v1/chat/completions", nil),
	}
	if err := ti.Intercept(resp); err != nil {
		t.Fatalf("Intercept err: %v", err)
	}
	// resp.Body MUST be the original (no wrap).
	if resp.Body != body {
		t.Errorf("non-SSE body was wrapped — should pass through unchanged")
	}
}

// TestToolCallTee_DetectsToolCallsSubstring proves the head-buffer
// scan: as soon as `"tool_calls"` appears in the first 8KB the flag is
// set; subsequent reads do not re-evaluate (single-flip semantics).
func TestToolCallTee_DetectsToolCallsSubstring(t *testing.T) {
	chunk := []byte("event: data\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"x\"}]}}]}\n\n")
	upstream := io.NopCloser(bytes.NewReader(chunk))
	flag := &atomic.Bool{}
	tee := newToolCallTee(upstream, flag, 8192)
	buf := make([]byte, len(chunk))
	if _, err := tee.Read(buf); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read err: %v", err)
	}
	if !flag.Load() {
		t.Errorf("flag not set despite tool_calls substring in head")
	}
}

// TestToolCallTee_NoFlagWithoutSubstring confirms a vanilla SSE stream
// (no tool_calls) leaves the flag false — the dispatcher must NOT
// suppress retry / failover on regular content streams.
func TestToolCallTee_NoFlagWithoutSubstring(t *testing.T) {
	chunk := []byte("event: data\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	upstream := io.NopCloser(bytes.NewReader(chunk))
	flag := &atomic.Bool{}
	tee := newToolCallTee(upstream, flag, 8192)
	_, _ = io.ReadAll(tee)
	if flag.Load() {
		t.Errorf("flag set incorrectly on tool-call-free stream")
	}
}

// TestToolCallTee_HeadCappedAt8KB proves the memory-bound: a 16KB
// stream with the substring AFTER the 8KB cap does not set the flag.
// Threat T-03-06-07 — a malicious upstream cannot OOM the gateway by
// streaming an unbounded probe before the substring.
func TestToolCallTee_HeadCappedAt8KB(t *testing.T) {
	prefix := bytes.Repeat([]byte("a"), 9000)            // > 8 KB
	suffix := []byte(`"tool_calls":[{"id":"late"}]`)
	full := append(prefix, suffix...)
	upstream := io.NopCloser(bytes.NewReader(full))
	flag := &atomic.Bool{}
	tee := newToolCallTee(upstream, flag, 8192)
	_, _ = io.ReadAll(tee)
	if flag.Load() {
		t.Errorf("flag set despite substring beyond 8KB cap — head-cap broken")
	}
	// Head buffer MUST be at most 8KB.
	if len(tee.head) > 8192 {
		t.Errorf("head buffer = %d bytes, want <= 8192", len(tee.head))
	}
}

// TestToolCallInterceptor_FlagMapSetGetDel exercises the copy-on-write
// flag map: set, then get returns the same pointer, then del removes it.
func TestToolCallInterceptor_FlagMapSetGetDel(t *testing.T) {
	ti := NewToolCallInterceptor()
	id := uuid.New().String()
	flag := &atomic.Bool{}
	ti.flags.set(id, flag)
	if got := ti.Flag(id); got != flag {
		t.Errorf("Flag(%q) returned different pointer", id)
	}
	ti.Clear(id)
	if got := ti.Flag(id); got != nil {
		t.Errorf("Flag(%q) post-Clear = %v, want nil", id, got)
	}
}

// TestWriteSSEToolCallError_EmitsTerminalEvent checks the wire format
// of the terminal SSE event. The body MUST contain `event: error` +
// `data: {...code:"tool_call_partial_stream"}` so OpenAI-SDK clients
// surface the error via the standard parse path.
func TestWriteSSEToolCallError_EmitsTerminalEvent(t *testing.T) {
	rw := httptest.NewRecorder()
	WriteSSEToolCallError(rw, "test-rid", "local-llm", "/v1/chat/completions")
	body := rw.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("body missing 'event: error': %q", body)
	}
	if !strings.Contains(body, `"code":"tool_call_partial_stream"`) {
		t.Errorf("body missing tool_call_partial_stream code: %q", body)
	}
	if rw.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", rw.Header().Get("Content-Type"))
	}
}

// dummyCtxKey is required only because tests cannot reach into
// httpx.requestIDKey. ToolCallInterceptor.Intercept calls
// httpx.RequestIDFrom which returns "" without the real key. That's
// the documented behavior — Intercept short-circuits and the tee is
// not installed. This test asserts that contract.
func TestToolCallInterceptor_NoRequestIDSkipsInstall(t *testing.T) {
	ti := NewToolCallInterceptor()
	bodyContent := []byte("event: data\ndata: {\"choices\":[]}\n\n")
	body := io.NopCloser(bytes.NewReader(bodyContent))
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       body,
		Request:    httptest.NewRequest("POST", "/v1/chat/completions", nil),
		// no httpx.RequestID middleware ran on this request → empty rid
	}
	// Use a context.Background-rooted ctx so RequestIDFrom returns "".
	resp.Request = resp.Request.WithContext(context.Background())
	if err := ti.Intercept(resp); err != nil {
		t.Fatalf("Intercept err: %v", err)
	}
	if resp.Body != body {
		t.Errorf("body wrapped despite missing request_id — interceptor MUST skip install")
	}
}
