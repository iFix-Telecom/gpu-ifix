//go:build integration

// Phase 3 Plan 03-07 Task 2 — sensitive tenant block end-to-end.
//
// Two scenarios:
//  1. Non-streaming sensitive request, tier-0 OPEN → SensitiveRetry exhausts
//     → 503 with envelope code "upstream_unavailable_for_sensitive_tenant"
//     + Retry-After: 30 + audit_log row with upstream='blocked_sensitive'
//     + NO audit_log_content row (D-B3 + D-B2).
//  2. Streaming sensitive request, tier-0 OPEN → fail-fast in <500ms
//     (D-B4 — no retry loop pre-flight).
//
// Both scenarios assert tier-1 (external) hits == 0 (sensitive NEVER goes
// external — LGPD).
package integration

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// TestIntegration_SensitiveTenantBlockedFromExternalOnFailover proves
// SC-3 — sensitive tenant + tier-0 OPEN + non-stream → 503 with
// blocked_sensitive audit + no content + tier-1 NEVER touched.
func TestIntegration_SensitiveTenantBlockedFromExternalOnFailover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	// Seed sensitive tenant + key (we use the IDs in the auth context).
	tenantID, apiKeyID, _ := seedTenantAndKey(t, ctx, pool, "sensitive-tenant", auth.DataClassSensitive)

	tier0 := newFailMock(t)
	tier1 := newSuccessMock(t)

	loader := resilienceLoader("llm",
		"local-llm", tier0.server.URL,
		"openrouter-chat", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names(),
	)

	// Trip tier-0 breaker BEFORE the sensitive request lands.
	driveBreaker(t, bs, "local-llm", 500, 5)
	if got := bs.Snapshot()["local-llm"]; got != "open" {
		t.Fatalf("tier-0 breaker = %q, want open before sensitive request", got)
	}

	// Audit writer + middleware (so audit_log + audit_log_content reflect
	// the sensitive-block path).
	auditWriter := audit.NewWriter(pool, discardLogger())
	writerCtx, writerCancel := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		auditWriter.Run(writerCtx)
	}()

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm":       newPanicProxy(t, "tier-0 must not be dispatched while OPEN"),
			"openrouter-chat": newPanicProxy(t, "tier-1 must not be dispatched for sensitive tenants"),
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	// Wrap dispatcher in httpx.RequestID + audit.Middleware so:
	//   - the request_id is generated and threaded into the audit Event
	//   - the dispatcher's auditctx override flows through to audit.Middleware
	wrapped := httpx.RequestID(audit.Middleware(auditWriter, discardLogger())(disp))

	rw := httptest.NewRecorder()
	r := makeSensitiveAuthedRequest(t, tenantID, apiKeyID,
		`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`,
		false /* non-streaming */)

	wrapped.ServeHTTP(rw, r)

	// Pull the gateway-issued request id from the response header
	// (httpx.RequestID always sets it).
	reqID := rw.Header().Get("X-Request-ID")
	if reqID == "" {
		t.Fatal("X-Request-ID response header missing")
	}

	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Errorf("body missing sensitive code: %s", rw.Body.String())
	}
	if got := rw.Header().Get("Retry-After"); got != "30" {
		t.Errorf("Retry-After = %q, want 30", got)
	}
	if tier1.hits.Load() != 0 {
		t.Errorf("tier-1 hits = %d; sensitive must NOT route external (LGPD)",
			tier1.hits.Load())
	}

	// Drain audit writer so the audit row hits Postgres.
	writerCancel()
	select {
	case <-auditDone:
	case <-time.After(5 * time.Second):
		t.Fatal("audit writer did not drain within 5s")
	}

	// audit_log row must show upstream='blocked_sensitive' for this request.
	var upstream string
	rid := uuid.Must(uuid.Parse(reqID))
	if err := pool.QueryRow(ctx,
		"SELECT upstream FROM ai_gateway.audit_log WHERE request_id=$1", rid,
	).Scan(&upstream); err != nil {
		t.Fatalf("audit_log lookup: %v", err)
	}
	if upstream != audit.UpstreamBlockedSensitive {
		t.Errorf("audit_log.upstream = %q, want %q",
			upstream, audit.UpstreamBlockedSensitive)
	}

	// audit_log_content must NOT have a row for this request (D-B2).
	var contentRows int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM ai_gateway.audit_log_content WHERE request_id=$1", rid,
	).Scan(&contentRows); err != nil {
		t.Fatalf("audit_log_content count: %v", err)
	}
	if contentRows != 0 {
		t.Errorf("audit_log_content rows = %d; sensitive must NOT persist content (D-B2)", contentRows)
	}
	_ = tenantID
}

// TestIntegration_SensitiveStreamingFailFast proves D-B4 — sensitive +
// streaming + tier-0 OPEN must 503 in <500ms (no retry loop pre-flight).
func TestIntegration_SensitiveStreamingFailFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)
	tenantID, apiKeyID, _ := seedTenantAndKey(t, ctx, pool, "sensitive-stream", auth.DataClassSensitive)

	tier0 := newFailMock(t)
	tier1 := newSuccessMock(t)

	loader := resilienceLoader("llm",
		"local-llm", tier0.server.URL,
		"openrouter-chat", tier1.server.URL,
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second},
		loader.Names(),
	)
	driveBreaker(t, bs, "local-llm", 500, 5)

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "llm",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-llm":       newPanicProxy(t, "tier-0 must not be dispatched while OPEN"),
			"openrouter-chat": newPanicProxy(t, "tier-1 must not be dispatched for sensitive tenants"),
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	rw := httptest.NewRecorder()
	r := makeSensitiveAuthedRequest(t, tenantID, apiKeyID,
		`{"model":"qwen","stream":true,"messages":[]}`, true)

	start := time.Now()
	disp.ServeHTTP(rw, r)
	elapsed := time.Since(start)

	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "upstream_unavailable_for_sensitive_tenant") {
		t.Errorf("body missing sensitive code: %s", rw.Body.String())
	}
	// D-B4 — must be fail-fast (well under the 200ms first SensitiveRetry sleep).
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want < 500ms (fail-fast pre-flight per D-B4)", elapsed)
	}
	if tier1.hits.Load() != 0 {
		t.Errorf("tier-1 hits = %d; sensitive streaming must NOT route external",
			tier1.hits.Load())
	}
}

// makeSensitiveAuthedRequest builds an httptest.NewRequest with auth ctx
// pre-populated for a sensitive tenant + the requested streaming flag.
func makeSensitiveAuthedRequest(t *testing.T, tenantID, apiKeyID uuid.UUID, body string, _ bool) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	ctx := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  tenantID.String(),
		APIKeyID:  apiKeyID.String(),
		DataClass: auth.DataClassSensitive,
	})
	return r.WithContext(ctx)
}

// newPanicProxy returns a handler that fails the test if invoked. Useful
// when the test asserts a particular upstream MUST NOT be dispatched.
func newPanicProxy(t *testing.T, msg string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Errorf("guarded proxy invoked: %s", msg)
	})
}
