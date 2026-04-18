package audit

import (
	"bytes"
	"context"
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
