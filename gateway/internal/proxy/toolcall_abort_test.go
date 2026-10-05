package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sentry "github.com/getsentry/sentry-go"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// guardRecordingTransport captures Sentry events for ToolCallTerminalGuard
// tests (pattern copied from internal/emerg/budget_test.go).
type guardRecordingTransport struct {
	events chan *sentry.Event
}

func (t *guardRecordingTransport) Configure(_ sentry.ClientOptions) {}
func (t *guardRecordingTransport) SendEvent(event *sentry.Event) {
	select {
	case t.events <- event:
	default:
	}
}
func (t *guardRecordingTransport) Flush(_ time.Duration) bool { return true }
func (t *guardRecordingTransport) FlushWithContext(_ context.Context) bool {
	return true
}
func (t *guardRecordingTransport) Close() {}

func installGuardSentryTransport(t *testing.T) *guardRecordingTransport {
	t.Helper()
	tr := &guardRecordingTransport{events: make(chan *sentry.Event, 16)}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:       "https://public@sentry.example.com/1",
		Transport: tr,
	}); err != nil {
		t.Fatalf("sentry.Init: %v", err)
	}
	t.Cleanup(func() {
		sentry.Flush(2 * time.Second)
		_ = sentry.Init(sentry.ClientOptions{})
	})
	return tr
}

func serveGuardRecover(h http.Handler, w http.ResponseWriter, r *http.Request) (rec any) {
	defer func() { rec = recover() }()
	h.ServeHTTP(w, r)
	return nil
}

func TestToolCallTerminalGuard_AbortNotSentToSentry(t *testing.T) {
	tr := installGuardSentryTransport(t)
	tci := NewToolCallInterceptor()
	const reqID = "0192a7c0-0000-7000-8000-000000000001"
	flag := &atomic.Bool{}
	flag.Store(true)
	tci.flags.set(reqID, flag) // same as Intercept after seeing "tool_calls"

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[]}}]}\n\n"))
		panic(http.ErrAbortHandler)
	})
	h := ToolCallTerminalGuard(next, tci, "primary", "/v1/chat/completions")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(httpx.ContextWithRequestID(req.Context(), reqID))

	rec := serveGuardRecover(h, rr, req)
	if rec != http.ErrAbortHandler {
		t.Fatalf("recovered = %v, want http.ErrAbortHandler", rec)
	}
	if !strings.Contains(rr.Body.String(), `"code":"tool_call_partial_stream"`) {
		t.Fatalf("terminal SSE frame missing: %q", rr.Body.String())
	}
	if n := len(tr.events); n != 0 {
		t.Fatalf("sentry events = %d, want 0", n)
	}
}

func TestToolCallTerminalGuard_OtherPanicStillSentToSentry(t *testing.T) {
	tr := installGuardSentryTransport(t)
	tci := NewToolCallInterceptor()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	h := ToolCallTerminalGuard(next, tci, "primary", "/v1/chat/completions")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(httpx.ContextWithRequestID(req.Context(), "0192a7c0-0000-7000-8000-000000000002"))

	rec := serveGuardRecover(h, rr, req)
	if rec != "boom" {
		t.Fatalf("recovered = %v, want boom", rec)
	}
	if n := len(tr.events); n < 1 {
		t.Fatalf("sentry events = %d, want >= 1", n)
	}
}
