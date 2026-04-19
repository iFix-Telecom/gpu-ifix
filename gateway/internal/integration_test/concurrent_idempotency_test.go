//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
)

// TestIntegration_09_ConcurrentIdempotency fires 10 goroutines POSTing the
// same body + same Idempotency-Key against the gateway with a fake upstream
// that sleeps 200ms. Asserts:
//   - Upstream sees exactly 1 hit (first-writer-wins serialization).
//   - 9 responses carry `X-Idempotency-Replayed: true`.
//   - All 10 complete within a bounded wall time (no 30s timeout).
//   - SELECT COUNT(*) FROM audit_log WHERE idempotency_replayed = true == 9.
//
// Codex review [MEDIUM] 02-06 + plan-checker integration_04b — the
// audit-replay flag is cross-plan contract B2.
func TestIntegration_09_ConcurrentIdempotency(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, parent)

	tenantID, _, _ := seedTenantAndKey(t, parent, pool, "concurrent-tenant", auth.DataClassNormal)

	store := idempotency.NewStore(rdb)
	auditW := audit.NewWriter(pool, discardLogger())
	runCtx, cancelRun := context.WithCancel(parent)
	runDone := make(chan struct{})
	go func() { auditW.Run(runCtx); close(runDone) }()

	// Fake upstream: sleeps 200ms then returns 200. All concurrent losers
	// must wait for the winner to complete via WaitForComplete → replay
	// the cached response.
	var upstreamHits int64
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&upstreamHits, 1)
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"id":"chatcmpl-concurrent","choices":[]}`)
	})

	chain := audit.Middleware(auditW, discardLogger())(
		idempotency.Middleware(store, discardLogger())(upstream))
	chain = injectAuthWithID(chain, tenantID.String(), auth.DataClassNormal)
	chain = httpx.RequestID(chain)

	srv := httptest.NewServer(chain)
	defer srv.Close()

	const N = 10
	body := `{"model":"qwen","messages":[{"role":"user","content":"concurrent"}]}`
	key := "concurrent-idem-key"

	var (
		wg          sync.WaitGroup
		statuses    = make([]int, N)
		replayFlags = make([]string, N)
		errors      = make([]error, N)
	)
	wg.Add(N)

	start := time.Now()
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions",
				bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errors[idx] = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			statuses[idx] = resp.StatusCode
			replayFlags[idx] = resp.Header.Get("X-Idempotency-Replayed")
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 1. All 10 finish within a reasonable time (well under wait-poll 30s).
	if elapsed > 10*time.Second {
		t.Errorf("concurrent wait elapsed %s > 10s budget", elapsed)
	}

	// 2. Exactly 1 upstream hit.
	if hits := atomic.LoadInt64(&upstreamHits); hits != 1 {
		t.Errorf("upstream hits got %d want 1", hits)
	}

	// 3. 9 responses carry replay header; 1 does not.
	var replayCount, freshCount int
	for i, f := range replayFlags {
		if errors[i] != nil {
			t.Errorf("goroutine %d errored: %v", i, errors[i])
			continue
		}
		if statuses[i] != 200 {
			t.Errorf("goroutine %d status=%d want 200", i, statuses[i])
		}
		if f == "true" {
			replayCount++
		} else {
			freshCount++
		}
	}
	if replayCount != 9 || freshCount != 1 {
		t.Errorf("replay distribution got %d replay + %d fresh want 9 + 1", replayCount, freshCount)
	}

	// 4. Drain audit writer and assert 9 rows have idempotency_replayed=true.
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("audit writer did not drain within 10s")
	}

	var replayedRows int
	err := pool.QueryRow(parent, `
		SELECT COUNT(*) FROM ai_gateway.audit_log
		WHERE tenant_id = $1 AND idempotency_replayed = true`, tenantID).Scan(&replayedRows)
	if err != nil {
		t.Fatalf("count replayed audit rows: %v", err)
	}
	if replayedRows != 9 {
		t.Errorf("audit_log rows with idempotency_replayed=true got %d want 9", replayedRows)
	}
}
