// Package proxy (toolcall.go): SSE response interceptor that detects
// tool_call emission in the first 8KB of a streamed response. When the
// upstream then disconnects mid-stream the proxy MUST NOT failover —
// per RES-06 / SC-4 a partial tool_call already on the client wire
// poisons retry semantics (the second request would re-execute side
// effects). Instead the proxy emits a terminal SSE error event with
// code "tool_call_partial_stream" so the client knows the call is
// non-replayable.
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// ToolCallInterceptor wraps SSE response bodies with a tee reader that
// scans the first toolCallScanCap bytes for the substring `"tool_calls"`.
// When detected, the interceptor sets a per-request flag that the
// ReverseProxy ErrorHandler reads on upstream-disconnect to decide
// whether to emit the terminal SSE error event.
//
// Flag storage: copy-on-write map keyed by gateway-authoritative
// request_id (UUIDv7 from httpx.RequestID). Sized for thousands of
// concurrent in-flight chat requests; allocations are bounded by
// (set + del) per request.
type ToolCallInterceptor struct {
	flags *toolCallFlags
}

const toolCallScanCap = 8192

type toolCallFlags struct {
	mu sync.Mutex
	// inner is replaced via atomic Store on every set/del so reads via
	// Load() are race-free without holding mu.
	m atomic.Pointer[flagMap]
}

type flagMap struct {
	inner map[string]*atomic.Bool
}

// NewToolCallInterceptor constructs the interceptor with an empty flag
// map. One instance per gateway process; threadsafe.
func NewToolCallInterceptor() *ToolCallInterceptor {
	tf := &toolCallFlags{}
	tf.m.Store(&flagMap{inner: make(map[string]*atomic.Bool)})
	return &ToolCallInterceptor{flags: tf}
}

// Intercept satisfies ProxyResponseInterceptor. For non-SSE responses
// it's a no-op (tool_calls only stream over SSE). For SSE bodies it
// installs a tee reader and registers the flag.
func (t *ToolCallInterceptor) Intercept(resp *http.Response) error {
	if !IsSSEResponse(resp) {
		return nil
	}
	if resp.Request == nil {
		return nil
	}
	reqID := httpx.RequestIDFrom(resp.Request.Context())
	if reqID == "" {
		return nil // no correlation possible; skip
	}
	flag := &atomic.Bool{}
	t.flags.set(reqID, flag)
	resp.Body = newToolCallTee(resp.Body, flag, toolCallScanCap)
	return nil
}

// Flag returns the per-request flag pointer. Used by the ReverseProxy
// ErrorHandler in chat.go (or wrapping handler) to decide whether to
// emit the terminal SSE error event on upstream disconnect.
func (t *ToolCallInterceptor) Flag(reqID string) *atomic.Bool {
	return t.flags.get(reqID)
}

// Clear removes the flag for a request when it terminates cleanly.
// Called by the request's outer middleware on response close.
func (t *ToolCallInterceptor) Clear(reqID string) {
	t.flags.del(reqID)
}

// set installs a flag pointer for reqID. Copy-on-write so concurrent
// readers see a consistent map snapshot without holding a lock.
func (tf *toolCallFlags) set(reqID string, f *atomic.Bool) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	old := tf.m.Load()
	next := &flagMap{inner: make(map[string]*atomic.Bool, len(old.inner)+1)}
	for k, v := range old.inner {
		next.inner[k] = v
	}
	next.inner[reqID] = f
	tf.m.Store(next)
}

func (tf *toolCallFlags) get(reqID string) *atomic.Bool {
	return tf.m.Load().inner[reqID]
}

func (tf *toolCallFlags) del(reqID string) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	old := tf.m.Load()
	if _, ok := old.inner[reqID]; !ok {
		return
	}
	next := &flagMap{inner: make(map[string]*atomic.Bool, len(old.inner))}
	for k, v := range old.inner {
		if k == reqID {
			continue
		}
		next.inner[k] = v
	}
	tf.m.Store(next)
}

// toolCallTee is an io.ReadCloser that forwards reads to the upstream
// body while inspecting the head of the stream for the "tool_calls"
// substring. Synchronous — no goroutines; head buffer capped at
// toolCallScanCap (8KB) to bound memory per stream (Threat T-03-06-07).
type toolCallTee struct {
	upstream io.ReadCloser
	flag     *atomic.Bool
	head     []byte
	cap      int
}

func newToolCallTee(r io.ReadCloser, flag *atomic.Bool, cap int) *toolCallTee {
	return &toolCallTee{upstream: r, flag: flag, cap: cap}
}

func (t *toolCallTee) Read(p []byte) (int, error) {
	n, err := t.upstream.Read(p)
	if n > 0 && len(t.head) < t.cap {
		remaining := t.cap - len(t.head)
		take := n
		if take > remaining {
			take = remaining
		}
		t.head = append(t.head, p[:take]...)
		if !t.flag.Load() && bytes.Contains(t.head, []byte(`"tool_calls"`)) {
			t.flag.Store(true)
		}
	}
	return n, err
}

func (t *toolCallTee) Close() error {
	return t.upstream.Close()
}

// WriteSSEToolCallError emits the terminal SSE error event when a stream
// is interrupted after a tool call was detected. Called from the
// ReverseProxy ErrorHandler. Header writes are best-effort: the proxy
// may have already written headers/chunks; in that case we just append
// the event frame and the client will see it as the last SSE message
// before the stream ends.
func WriteSSEToolCallError(w http.ResponseWriter, reqID, upstream, route string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: error\n")
	_, _ = io.WriteString(w,
		`data: {"error":{"type":"api_error","code":"tool_call_partial_stream",`+
			`"message":"Primary upstream disconnected after tool call emission; `+
			`retry with a separate idempotency key."}}`+"\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	obs.ToolCallPartialTotal.WithLabelValues(route, upstream).Inc()
	_ = reqID // accepted for future structured logging; unused at write time
}

// Compile-time assertion: ToolCallInterceptor satisfies ProxyResponseInterceptor.
var _ ProxyResponseInterceptor = (*ToolCallInterceptor)(nil)
