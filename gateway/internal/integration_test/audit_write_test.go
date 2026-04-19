//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// TestIntegration_03_AuditWrite exercises the full async audit pipeline:
// 10 Enqueued events (5 normal with prompt/response, 5 sensitive without)
// → flusher writes all 10 rows to audit_log + exactly 5 rows to
// audit_log_content (sensitive excluded per D-B2).
func TestIntegration_03_AuditWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	q := gen.New(pool)
	tenant, err := q.GetTenantBySlug(ctx, "converseai")
	if err != nil {
		t.Fatal(err)
	}

	w := audit.NewWriter(pool, discardLogger())
	writerCtx, writerCancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		w.Run(writerCtx)
		close(runDone)
	}()

	// Enqueue 10 events: 5 normal (with prompt/response), 5 sensitive.
	start := time.Now()
	for i := 0; i < 10; i++ {
		dc := string(auth.DataClassNormal)
		var prompt, resp []byte
		if i < 5 {
			prompt = []byte(`{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`)
			resp = []byte(`{"id":"x","choices":[]}`)
		} else {
			dc = string(auth.DataClassSensitive)
			// no prompt/response
		}
		w.Enqueue(audit.Event{
			TS:         time.Now(),
			RequestID:  uuid.Must(uuid.NewV7()),
			TenantID:   tenant.ID,
			DataClass:  dc,
			Route:      "/v1/chat/completions",
			Method:     "POST",
			Upstream:   "llm",
			StatusCode: 200,
			LatencyMs:  int64(50 + i*10),
			Prompt:     prompt,
			Response:   resp,
		})
	}
	// Wait for flush — the async flusher flushes every 1s or at 500 rows.
	deadline := time.Now().Add(5 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM ai_gateway.audit_log WHERE ts >= $1", start).Scan(&got)
		if got == 10 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != 10 {
		t.Fatalf("audit_log rows got %d want 10", got)
	}

	// Content rows: exactly 5 normal.
	var contentCount int
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM ai_gateway.audit_log_content").Scan(&contentCount)
	if contentCount != 5 {
		t.Errorf("audit_log_content rows got %d want 5 (sensitive tenants excluded)", contentCount)
	}

	// Sanity: the 5 normal rows have prompt/response non-null.
	var nonNullRows int
	_ = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ai_gateway.audit_log_content WHERE prompt IS NOT NULL AND response IS NOT NULL`,
	).Scan(&nonNullRows)
	if nonNullRows != 5 {
		t.Errorf("non-null prompt/response rows got %d want 5", nonNullRows)
	}

	// Cleanly drain the writer before returning so t.Cleanup doesn't race
	// with pending goroutines.
	writerCancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Error("audit writer did not drain within 5s")
	}
}
