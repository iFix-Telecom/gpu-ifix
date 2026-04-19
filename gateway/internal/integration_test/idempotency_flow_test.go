//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
)

// TestIntegration_04_IdempotencyFlow verifies end-to-end replay + conflict
// semantics via a real Redis container + fake upstream handler.
//
//   - 1st POST: cache miss → upstream hit; upstreamHits=1.
//   - 2nd POST same key+body: replay → X-Idempotency-Replayed=true,
//     upstreamHits still 1.
//   - 3rd POST same key, different body: 422 idempotency_conflict.
//   - 4th POST new key + stream:true: 400 idempotency_key_unsupported_stream.
func TestIntegration_04_IdempotencyFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	store := idempotency.NewStore(rdb)

	var upstreamHits int64
	// Fake "upstream" handler counts invocations and echoes body.
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"echoed":`+string(body)+`}`)
	})

	// Stack: (fake auth inject) → idempotency → upstream.
	h := idempotency.Middleware(store, discardLogger())(upstream)
	h = injectAuth(h, "11111111-1111-1111-1111-111111111111", auth.DataClassNormal)
	h = httpx.RequestID(h)

	srv := httptest.NewServer(h)
	defer srv.Close()

	// 1. First POST — cache miss.
	body1 := `{"model":"qwen","messages":[{"role":"user","content":"hello"}]}`
	resp1, err := http.DefaultClient.Do(newIdemRequest(srv.URL, body1, "my-key-1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp1.Body.Close()
	if resp1.StatusCode != 200 {
		t.Errorf("first status got %d want 200", resp1.StatusCode)
	}
	if atomic.LoadInt64(&upstreamHits) != 1 {
		t.Errorf("upstream hits got %d want 1", upstreamHits)
	}

	// 2. Second POST — same key + body → replay.
	resp2, err := http.DefaultClient.Do(newIdemRequest(srv.URL, body1, "my-key-1"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("replay status got %d want 200", resp2.StatusCode)
	}
	if resp2.Header.Get("X-Idempotency-Replayed") != "true" {
		t.Errorf("replay header missing")
	}
	if atomic.LoadInt64(&upstreamHits) != 1 {
		t.Errorf("upstream hit on replay: got %d want 1", upstreamHits)
	}

	// 3. Third POST — same key, DIFFERENT body → 422.
	body2 := `{"model":"qwen","messages":[{"role":"user","content":"different"}]}`
	resp3, err := http.DefaultClient.Do(newIdemRequest(srv.URL, body2, "my-key-1"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("body-mismatch got %d want 422", resp3.StatusCode)
	}
	var env struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.NewDecoder(resp3.Body).Decode(&env)
	if env.Error.Code != "idempotency_key_reused_with_different_body" {
		t.Errorf("code got %q", env.Error.Code)
	}

	// 4. Fourth POST — new key + stream:true → 400.
	streamBody := `{"model":"qwen","stream":true,"messages":[]}`
	resp4, err := http.DefaultClient.Do(newIdemRequest(srv.URL, streamBody, "my-key-2"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Errorf("stream got %d want 400", resp4.StatusCode)
	}
}

// TestIntegration_04b_IdempotencyReplayAuditFlag verifies the cross-plan
// contract B2: when idempotency.Middleware replays a cached response, the
// outer audit.Middleware MUST record `idempotency_replayed=true` in
// ai_gateway.audit_log. Uses the real audit.Middleware (audit writer
// flushing into Postgres) wrapping idempotency.Middleware wrapping a fake
// upstream.
func TestIntegration_04b_IdempotencyReplayAuditFlag(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer parentCancel()
	pool, rdb := freshSchema(t, parent)

	// Seed a dedicated tenant so the SELECT tenant_id filter returns the
	// right row without crossing the converseai default.
	tenantID, _, _ := seedTenantAndKey(t, parent, pool, "tenant-audit-A", auth.DataClassNormal)

	store := idempotency.NewStore(rdb)
	auditW := audit.NewWriter(pool, discardLogger())
	runCtx, cancelRun := context.WithCancel(parent)
	runDone := make(chan struct{})
	go func() { auditW.Run(runCtx); close(runDone) }()

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","choices":[]}`)
	})

	// Chain order MATCHES production: audit (outer) → idempotency → upstream.
	chain := audit.Middleware(auditW, discardLogger())(
		idempotency.Middleware(store, discardLogger())(upstream))
	chain = injectAuthWithID(chain, tenantID.String(), auth.DataClassNormal)
	chain = httpx.RequestID(chain)

	srv := httptest.NewServer(chain)
	defer srv.Close()

	body := `{"model":"qwen","messages":[{"role":"user","content":"replay"}]}`

	// First POST — cache miss; audit row has idempotency_replayed=false.
	resp1, err := http.DefaultClient.Do(newIdemRequest(srv.URL, body, "audit-replay-key"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp1.Body.Close()

	// Second POST — cache hit; idempotency.Middleware calls
	// setter.SetIdempotencyReplayed(true) BEFORE writing the cached body.
	resp2, err := http.DefaultClient.Do(newIdemRequest(srv.URL, body, "audit-replay-key"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()

	if resp2.Header.Get("X-Idempotency-Replayed") != "true" {
		t.Fatal("replay header missing — preconditions fail")
	}

	// Drain audit writer: cancel ctx → Run() flushes buffered events before returning.
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("audit writer did not drain within 10s")
	}

	// The second audit row (most recent) must carry idempotency_replayed=true.
	var replayed bool
	err = pool.QueryRow(parent, `
        SELECT idempotency_replayed FROM ai_gateway.audit_log
        WHERE tenant_id = $1
        ORDER BY ts DESC LIMIT 1`, tenantID).Scan(&replayed)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if !replayed {
		t.Error("audit_log.idempotency_replayed=false on replayed request — B2 regression")
	}

	// And the FIRST row should be idempotency_replayed=false.
	var firstReplayed bool
	err = pool.QueryRow(parent, `
        SELECT idempotency_replayed FROM ai_gateway.audit_log
        WHERE tenant_id = $1
        ORDER BY ts ASC LIMIT 1`, tenantID).Scan(&firstReplayed)
	if err != nil {
		t.Fatalf("audit query first row: %v", err)
	}
	if firstReplayed {
		t.Error("first (winner) audit_log row incorrectly marked idempotency_replayed=true")
	}
}

func newIdemRequest(url, body, key string) *http.Request {
	req, _ := http.NewRequest("POST", url+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	return req
}

// injectAuth wraps a handler with a ctx carrying a fake AuthContext. Used
// ONLY in integration tests to bypass the real auth.Middleware which
// requires a live Postgres-backed Verifier.
func injectAuth(next http.Handler, tenantID string, dc auth.DataClass) http.Handler {
	return injectAuthWithID(next, tenantID, dc)
}

// injectAuthWithID parameterizes the tenant UUID so tests that need the
// UUID to match a real Postgres row (e.g. TestIntegration_04b) can supply it.
func injectAuthWithID(next http.Handler, tenantID string, dc auth.DataClass) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac := auth.AuthContext{
			TenantID:  tenantID,
			APIKeyID:  uuid.Nil.String(),
			DataClass: dc,
			KeyPrefix: "ifix_sk_****test",
		}
		ctx := auth.WithContext(r.Context(), ac)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
