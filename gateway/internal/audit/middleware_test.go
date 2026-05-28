package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// captureWriter is a Writer subclass whose Enqueue pushes into a slice
// instead of the async channel. Lets tests assert what Middleware built.
type captureWriter struct {
	Writer
	mu     sync.Mutex
	events []Event
}

func (c *captureWriter) Enqueue(e Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// harness builds the middleware with the capture writer. Returns the
// http.Handler (auth.WithContext → requestID → middleware → handler chain).
func harness(t *testing.T, ac auth.AuthContext, handler http.HandlerFunc) (*captureWriter, http.Handler) {
	t.Helper()
	cw := &captureWriter{}
	mw := Middleware(&cw.Writer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// We replace the writer's Enqueue path via a thin wrapper: route through
	// a test-only setter that swaps the backing slice. Simpler: define a
	// custom EnqueueFn on Writer via package internal test hook (see
	// enqueue.go). Here we rely on Middleware calling w.Enqueue — we pass
	// &cw.Writer; cw.Enqueue shadows but is not called unless the code
	// calls cw.Enqueue directly. Middleware takes *Writer; shadowing won't
	// intercept. So we use a test-only flusher model.
	//
	// We bypass by setting a capture hook on the Writer struct.
	cw.Writer.enqueueHook = func(e Event) { cw.Enqueue(e) }

	h := mw(handler)
	// Inject AuthContext and request-id via a chain.
	chain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithContext(r.Context(), ac)
		// Put a request-id in ctx via httpx.RequestID middleware wrapper.
		httpx.RequestID(h).ServeHTTP(w, r.WithContext(ctx))
	})
	// Simple context wrapper added via httpx.RequestID above.
	_ = context.TODO
	return cw, chain
}

func TestMiddleware_BuildsEvent_Normal(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassNormal,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"x":1}`))
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if len(cw.events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(cw.events))
	}
	ev := cw.events[0]
	if ev.Route != "/v1/chat/completions" {
		t.Errorf("route=%q", ev.Route)
	}
	if ev.DataClass != "normal" {
		t.Errorf("data_class=%q", ev.DataClass)
	}
	if ev.StatusCode != 200 {
		t.Errorf("status=%d", ev.StatusCode)
	}
	if !bytes.Contains(ev.Response, []byte(`{"x":1}`)) {
		t.Errorf("response not captured: %q", ev.Response)
	}
	if !bytes.Contains(ev.Prompt, []byte(`{"q":1}`)) {
		t.Errorf("prompt not captured: %q", ev.Prompt)
	}
}

func TestMiddleware_BuildsEvent_Sensitive(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassSensitive,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"secret":"xx"}`))
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if len(cw.events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(cw.events))
	}
	ev := cw.events[0]
	if ev.Prompt != nil {
		t.Errorf("prompt must be nil for sensitive; got %q", ev.Prompt)
	}
	if ev.Response != nil {
		t.Errorf("response must be nil for sensitive; got %q", ev.Response)
	}
}

func TestMiddleware_IdempotencyReplayedPropagated(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassNormal,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if setter, ok := w.(IdempotencyReplayedSetter); ok {
			setter.SetIdempotencyReplayed(true)
		}
		_, _ = w.Write([]byte(`{"ok":1}`))
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !cw.events[0].IdempotencyReplayed {
		t.Errorf("expected IdempotencyReplayed=true on Event")
	}
}

func TestMiddleware_SSEStreamSetsFlag(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassNormal,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: hi\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if !cw.events[0].Stream {
		t.Errorf("expected Stream=true on SSE response")
	}
}

// TestMiddleware_SSEStreamResponseExtractsLastChunk locks the SEED for
// the audit-content SSE-as-JSONB rollback bug surfaced 2026-05-28 during
// the Phase 11 11-06 corpus exerciser run: SSE responses (from OpenRouter
// streaming + similar) were being stored verbatim into
// audit_log_content.response, a JSONB column, which postgres rejects with
// SQLSTATE 22P02 and rolls back the entire flush batch (taking the
// audit_log envelope rows with it — ~17% audit data loss on chat traffic
// in the exerciser run). The fix in middleware.go:96 extracts the LAST
// `data: {...}` chunk before `[DONE]` so the response column holds a
// JSONB-valid payload (typically the chunk carrying usage + finish_reason
// when openrouter_director's stream_options.include_usage=true is in
// effect).
func TestMiddleware_SSEStreamResponseExtractsLastChunk(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassNormal,
	}
	// Realistic OpenRouter-shape SSE wire format: PROCESSING preamble,
	// delta chunks, a final usage+finish_reason chunk, then [DONE].
	sse := "" +
		": OPENROUTER PROCESSING\n\n" +
		"data: {\"id\":\"gen-1\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n" +
		"data: {\"id\":\"gen-1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":1,\"total_tokens\":9}}\n\n" +
		"data: [DONE]\n\n"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sse))
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if len(cw.events) != 1 {
		t.Fatalf("want 1 captured event, got %d", len(cw.events))
	}
	e := cw.events[0]
	if !e.Stream {
		t.Errorf("want Stream=true on SSE response")
	}
	if e.Response == nil {
		t.Fatalf("want non-nil Response (last SSE chunk), got nil — audit_log_content row would be skipped")
	}
	// Postgres-JSONB validity is the load-bearing assertion. json.Valid
	// is the same check the audit writer would face indirectly via the
	// driver. If this fails, the next audit flush batch rolls back.
	if !json.Valid(e.Response) {
		t.Fatalf("Response is not valid JSON: %q", string(e.Response))
	}
	// And the contracted payload — the LAST non-DONE chunk — should
	// contain the usage totals + finish_reason.
	respStr := string(e.Response)
	if !bytes.Contains(e.Response, []byte("\"finish_reason\":\"stop\"")) {
		t.Errorf("want finish_reason=stop in extracted chunk, got %q", respStr)
	}
	if !bytes.Contains(e.Response, []byte("\"completion_tokens\":1")) {
		t.Errorf("want usage.completion_tokens=1 in extracted chunk, got %q", respStr)
	}
}

// TestMiddleware_SSEStreamWithNoParseableChunkReturnsNil locks the
// fallback path: when the SSE body has no `data: {...}` line that
// json.Valid accepts (malformed upstream, truncated response, etc.),
// extractLastSSEChunk returns nil so the audit writer's len-zero
// short-circuit skips audit_log_content insertion rather than handing
// invalid bytes to postgres.
func TestMiddleware_SSEStreamWithNoParseableChunkReturnsNil(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassNormal,
	}
	// Pure preamble + DONE, no valid data: JSON chunks.
	sse := ": OPENROUTER PROCESSING\n\ndata: [DONE]\n\n"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sse))
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if len(cw.events) != 1 {
		t.Fatalf("want 1 captured event, got %d", len(cw.events))
	}
	if cw.events[0].Response != nil {
		t.Errorf("want nil Response when SSE body has no JSON chunks, got %q", string(cw.events[0].Response))
	}
}

func TestMiddleware_CapturesAtMost128KB(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassNormal,
	}
	huge := bytes.Repeat([]byte("Z"), 200*1024)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(huge)
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	ev := cw.events[0]
	if len(ev.Response) != contentCapBytes {
		t.Errorf("expected Response len=%d; got %d", contentCapBytes, len(ev.Response))
	}
	if !ev.Truncated {
		t.Errorf("expected Truncated=true")
	}
}
