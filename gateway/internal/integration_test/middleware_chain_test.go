//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/idempotency"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/quota"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// TestMiddlewareChainRateLimitBeforeQuota verifies the D-D1 chain order
// contract at code level: RateLimitMiddleware runs BEFORE QuotaMiddleware,
// and replay-flagged requests skip rate-limit but still hit quota.
//
// The test drives three request shapes through a minimal chain:
//
//  1. Normal request → both middlewares consume
//  2. Rate-limit-exhausted request → 429, does NOT reach quota checker
//  3. idempotency.WithReplay-flagged request → skips rate-limit; quota still
//     runs (Stripe semantics, D-D1 "replay consumes quota").
func TestMiddlewareChainRateLimitBeforeQuota(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)
	seed := seedPhase4(t, ctx, pool)

	// rps=1 so the SECOND real request must be rejected. quota limit wide
	// open so it never bites first.
	if _, err := pool.Exec(ctx,
		`UPDATE ai_gateway.tenants SET rps_limit = 1, rpm_limit = 1, daily_quota_tokens = 100000000 WHERE id = $1`,
		seed.ConverseAITenantID); err != nil {
		t.Fatal(err)
	}

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	loader, err := tenants.NewLoader(ctx, pool, loc, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	checker := quota.NewQuotaChecker(gen.New(pool), discardLogger())

	quotaRan := 0
	counter := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Build chain: rate-limit → quota → counter. Matches main.go order.
	chain := quota.QuotaMiddleware(checker, loader, discardLogger())(
		countingMiddleware(&quotaRan)(counter),
	)
	chain = quota.RateLimitMiddleware(rdb, loader, false, discardLogger())(chain)
	chain = injectAuthWithID(chain, seed.ConverseAITenantID.String(), auth.DataClassNormal)

	// 1) First request passes both → 200.
	rec1 := httptest.NewRecorder()
	chain.ServeHTTP(rec1, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	if rec1.Code != http.StatusOK {
		t.Errorf("request 1: want 200, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	if quotaRan != 1 {
		t.Errorf("quota counter after request 1: want 1, got %d", quotaRan)
	}

	// 2) Second request — rps=1 exhausted; rate-limit must 429 BEFORE
	// reaching quota. We assert quotaRan didn't advance.
	rec2 := httptest.NewRecorder()
	chain.ServeHTTP(rec2, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("request 2: want 429, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if quotaRan != 1 {
		t.Errorf("quota MUST NOT run after rate-limit 429; counter=%d (chain order broken)",
			quotaRan)
	}

	// 3) Replay-flagged request — D-D1: skips rate-limit, still hits quota.
	// Wrap handler that sets WithReplay BEFORE the chain runs.
	chainWithReplay := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chain.ServeHTTP(w, r.WithContext(idempotency.WithReplay(r.Context())))
	})
	rec3 := httptest.NewRecorder()
	chainWithReplay.ServeHTTP(rec3, httptest.NewRequest("POST", "/v1/chat/completions", nil))
	if rec3.Code != http.StatusOK {
		t.Errorf("replay request: want 200 (rate-limit skipped, quota passes), got %d body=%s",
			rec3.Code, rec3.Body.String())
	}
	if quotaRan != 2 {
		t.Errorf("replay request must still run quota; counter=%d (want 2)", quotaRan)
	}
}

// countingMiddleware increments *counter on each call — used to assert
// "downstream handler was reached" without wiring real DB writes.
func countingMiddleware(counter *int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*counter++
			next.ServeHTTP(w, r)
		})
	}
}
