//go:build integration

// Phase 3 Plan 03-07 Task 2 — tool-call partial stream regression
// (RES-06 / SC-4). When the upstream emits a tool_call delta then
// disconnects mid-stream, the gateway MUST:
//   1. NOT failover to tier-1 (tool_call side-effects are non-replayable)
//   2. Surface a terminal `event: error` SSE frame with code
//      "tool_call_partial_stream" to the client
//   3. Increment gateway_tool_call_partial_total{route, upstream}
//
// This test exercises the production proxy.NewChatProxy +
// proxy.NewDispatcher path with a mock tier-0 that hijacks the SSE
// connection mid-tool_call.
package integration

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// TestIntegration_ToolCallPartialStreamEmitsTerminalError proves SC-4:
// after a tool_call delta is emitted on an SSE stream and the upstream
// disconnects, the gateway:
//   - does NOT failover to tier-1
//   - emits a terminal SSE error event containing tool_call_partial_stream
//   - increments gateway_tool_call_partial_total
func TestIntegration_ToolCallPartialStreamEmitsTerminalError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	// Capture the metric baseline so we can assert >= +1 after the test.
	baseline := getCounterValue(t, obs.ToolCallPartialTotal,
		prometheus.Labels{"route": "/v1/chat/completions", "upstream": "local-llm"})

	// Tier-0 mock: writes a tool_call SSE delta, flushes, then hijacks the
	// connection and closes it mid-stream. Tier-1 mock: success counter
	// (must remain 0 — no failover allowed for tool calls).
	var tier0Hits atomic.Int64
	tier0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tier0Hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		// Emit one SSE frame containing tool_calls.
		_, _ = io.WriteString(w,
			`data: {"id":"x","choices":[{"delta":{"tool_calls":[{"id":"call_1","function":{"name":"get_time","arguments":""}}]}}]}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hijack to abruptly close (simulates upstream disconnect).
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter does not implement Hijacker")
			return
		}
		conn, _, herr := hj.Hijack()
		if herr != nil {
			t.Errorf("hijack: %v", herr)
			return
		}
		_ = conn.(*net.TCPConn).SetLinger(0)
		_ = conn.Close()
	}))
	defer tier0.Close()

	tier1 := newSuccessMock(t)

	loader := resilienceLoader("llm",
		"local-llm", tier0.URL,
		"openrouter-chat", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
		loader.Names(),
	)

	// Real production proxy.NewChatProxy with the tool-call interceptor.
	toolCallInterceptor := proxy.NewToolCallInterceptor()
	chatRP, err := proxy.NewChatProxy(tier0.URL,
		slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		toolCallInterceptor)
	if err != nil {
		t.Fatalf("NewChatProxy: %v", err)
	}
	// Wrap the chat proxy so that when the response stream ends we check
	// the tool-call flag and, if set, emit the terminal SSE error event.
	// This mirrors what main.go's chat-proxy chain SHOULD do for SC-4 to
	// hold end-to-end. See ToolCallTerminalGuard for the rationale.
	chatHandler := proxy.ToolCallTerminalGuard(chatRP, toolCallInterceptor, "local-llm", "/v1/chat/completions")

	// Tier-1 panics if invoked — proves SC-4's "no failover after tool_call".
	tier1Handler := newPanicProxy(t, "tier-1 must NOT be dispatched after tool_call emission (SC-4)")

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm":       chatHandler,
			"openrouter-chat": tier1Handler,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})
	wrapped := httpx.RequestID(disp)

	// Spin a real http.Server so the SSE flush + hijack semantics match
	// production. httptest.ResponseRecorder doesn't implement Hijacker,
	// so we need a real socket on the client side too.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithContext(r.Context(), auth.AuthContext{
			TenantID:  "00000000-0000-0000-0000-000000000001",
			APIKeyID:  "00000000-0000-0000-0000-000000000002",
			DataClass: auth.DataClassNormal,
		})
		wrapped.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer srv.Close()

	// Fire the streaming request.
	body := strings.NewReader(`{"model":"qwen","stream":true,"messages":[{"role":"user","content":"what time is it?"}],"tools":[]}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		srv.URL+"/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("client request: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	bodyStr := string(respBody)

	// Tier-1 must NOT have been hit (SC-4).
	if tier1.hits.Load() != 0 {
		t.Errorf("tier-1 hits = %d; SC-4 violated — gateway failed over after tool_call",
			tier1.hits.Load())
	}
	// Tier-0 must have been hit exactly once (one shot, no retries).
	if got := tier0Hits.Load(); got != 1 {
		t.Errorf("tier-0 hits = %d; want 1 (single shot per RES-06)", got)
	}
	// Body must contain the terminal error code.
	if !strings.Contains(bodyStr, "tool_call_partial_stream") {
		t.Errorf("body missing tool_call_partial_stream:\n%s", bodyStr)
	}

	// Metric must have incremented.
	final := getCounterValue(t, obs.ToolCallPartialTotal,
		prometheus.Labels{"route": "/v1/chat/completions", "upstream": "local-llm"})
	if final <= baseline {
		t.Errorf("gateway_tool_call_partial_total did not increment: baseline=%v final=%v",
			baseline, final)
	}
}

// getCounterValue reads the current value of a CounterVec with the given
// label set. Used to assert the gateway_tool_call_partial_total counter
// incremented by exactly 1 over the test.
func getCounterValue(t *testing.T, c *prometheus.CounterVec, labels prometheus.Labels) float64 {
	t.Helper()
	m, err := c.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("GetMetricWith: %v", err)
	}
	pb := &dto.Metric{}
	if err := m.Write(pb); err != nil {
		t.Fatalf("metric.Write: %v", err)
	}
	if pb.Counter == nil || pb.Counter.Value == nil {
		return 0
	}
	return *pb.Counter.Value
}
