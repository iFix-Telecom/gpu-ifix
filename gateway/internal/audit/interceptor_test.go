package audit

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// warnLogger satisfies the minimal Warn interface used by AuditInterceptor.
type warnLogger struct{}

func (warnLogger) Warn(msg string, args ...any) {}

func TestAuditInterceptor_SSEWrapsBody(t *testing.T) {
	w := &Writer{} // no DB path exercised here
	ai := NewAuditInterceptor(w, nil, warnLogger{})
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(bytes.NewReader([]byte("data: hello\n\n"))),
	}
	if err := ai.Intercept(resp); err != nil {
		t.Fatalf("Intercept: %v", err)
	}
	if _, ok := resp.Body.(*TeeBody); !ok {
		t.Fatalf("expected resp.Body wrapped in *TeeBody; got %T", resp.Body)
	}
}

func TestAuditInterceptor_NonSSENoOp(t *testing.T) {
	w := &Writer{}
	ai := NewAuditInterceptor(w, nil, warnLogger{})
	body := io.NopCloser(bytes.NewReader([]byte(`{"ok":1}`)))
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   body,
	}
	if err := ai.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	if resp.Body != body {
		t.Fatalf("non-SSE body pointer changed; expected no-op")
	}
}

func TestAuditInterceptor_OnCloseFires(t *testing.T) {
	w := &Writer{}
	var gotID atomic.Value
	gotID.Store("")
	var count atomic.Int32
	ai := NewAuditInterceptor(w, func(requestID string) {
		gotID.Store(requestID)
		count.Add(1)
	}, warnLogger{})
	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"abc-123"},
		},
		Body: io.NopCloser(bytes.NewReader([]byte("data: x\n\n"))),
	}
	if err := ai.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if count.Load() != 1 {
		t.Fatalf("onDisconnect fired %d times; expected 1", count.Load())
	}
	if gotID.Load().(string) != "abc-123" {
		t.Fatalf("onDisconnect got %q; expected abc-123", gotID.Load())
	}
}

func TestAuditInterceptor_IdempotentClose(t *testing.T) {
	w := &Writer{}
	var count atomic.Int32
	ai := NewAuditInterceptor(w, func(string) { count.Add(1) }, warnLogger{})
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(bytes.NewReader([]byte("data: x\n\n"))),
	}
	_ = ai.Intercept(resp)
	_ = resp.Body.Close()
	_ = resp.Body.Close()
	_ = resp.Body.Close()
	if count.Load() != 1 {
		t.Fatalf("expected onDisconnect fired exactly 1; got %d", count.Load())
	}
}

func TestAuditInterceptor_SatisfiesProxyInterface(t *testing.T) {
	var _ proxy.ProxyResponseInterceptor = (*AuditInterceptor)(nil)
}
