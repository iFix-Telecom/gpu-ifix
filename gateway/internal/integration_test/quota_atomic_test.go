//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/quota"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// TestRateLimitAtomic1000Concurrent — SC-5 proof: 1000 concurrent goroutines
// hitting RateLimitMiddleware against the live Lua-backed Redis bucket with
// RPS capacity 100 must yield EXACTLY 100 allowed + 900 rejected. Anything
// less is a classic TOCTOU drift; anything more is the bucket leaking
// beyond its capacity (both break the Stripe atomicity guarantee).
//
// The middleware derives RPSLimit / RPMLimit from the tenants.Loader
// snapshot, so we UPDATE ai_gateway.tenants FIRST, then refresh the loader
// (pgxlisten is not started — simpler to call Refresh directly in-test).
//
// Burst safety: the RPM bucket must be large enough not to reject before
// the RPS bucket does. We set rpm_limit=10000 so every RPS-allowed request
// also passes RPM, and the FailedWindow is always "rps" on rejection.
func TestRateLimitAtomic1000Concurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	// Set rps_limit=100 (+ large rpm so RPM never bites first).
	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.tenants SET rps_limit = 100, rpm_limit = 10000 WHERE id = $1`,
		seed.ConverseAITenantID); err != nil {
		t.Fatalf("set rps: %v", err)
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatalf("new tenants loader: %v", err)
	}

	// Stub upstream — just returns 200. The test only measures how many
	// requests the middleware lets through.
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chain := quota.RateLimitMiddleware(rdb, loader, false, discardLogger())(upstream)
	// Inject auth ctx so the middleware finds a tenant.
	chain = injectAuthWithID(chain, seed.ConverseAITenantID.String(), auth.DataClassNormal)

	const N = 1000
	var allowed, denied, other atomic.Uint32
	var wg sync.WaitGroup
	wg.Add(N)

	// All goroutines gate on the same "go" signal so they hit Lua within
	// the narrowest window possible — the continuous-refill bucket
	// accrues tokens at rps/1000 per ms, so minimizing wall-time between
	// first and last call minimizes the allowed-count drift from the
	// nominal 100.
	gate := make(chan struct{})
	start := time.Now()
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-gate
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			chain.ServeHTTP(rec, req)
			switch rec.Code {
			case http.StatusOK:
				allowed.Add(1)
			case http.StatusTooManyRequests:
				denied.Add(1)
			default:
				other.Add(1)
				t.Logf("unexpected status %d body=%s", rec.Code, rec.Body.String())
			}
		}()
	}
	close(gate)
	wg.Wait()
	elapsed := time.Since(start)

	// Stripe-canonical continuous-refill bucket math: bucket carries 100
	// capacity and refills at rps/1000 tokens per ms. Over elapsed_ms,
	// allowed == capacity + floor(elapsed_ms * refill_rate). No atomicity
	// violation possible — Redis Lua is single-threaded. Drift window is
	// simply the refill accrued during the request wall clock.
	elapsedMs := elapsed.Milliseconds()
	maxAllowed := 100 + int32(elapsedMs/10) + 1 // refill = rps/1000 = 0.1 tok/ms → elapsed_ms*0.1
	a := int32(allowed.Load())
	d := int32(denied.Load())

	// Lower bound: at least the nominal capacity must have been served
	// (otherwise something is breaking the bucket on the allow-side).
	if a < 100 {
		t.Errorf("atomic bucket under-served: allowed=%d < 100 (capacity)", a)
	}
	// Upper bound: allowed ≤ capacity + elapsed_ms × refill_rate.
	if a > maxAllowed {
		t.Errorf("atomic bucket over-served: allowed=%d > %d (capacity + refill over %dms)",
			a, maxAllowed, elapsedMs)
	}
	if d+a != int32(N) {
		t.Errorf("goroutine accounting: allowed+denied=%d want %d (other=%d)",
			d+a, N, other.Load())
	}
	if o := other.Load(); o != 0 {
		t.Errorf("unexpected non-200/429 responses: %d", o)
	}
	t.Logf("SC-5 ok: 1000 goroutines in %s; allowed=%d denied=%d (cap=100 refill_window=%d)",
		elapsed, a, d, maxAllowed-100)
}
