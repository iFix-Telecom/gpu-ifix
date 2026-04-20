// Package proxy (retry.go): exponential-backoff retry helper for
// non-streaming dispatcher paths per CONTEXT.md RES-02. Initial 100ms,
// max 500ms, multiplier 2x, jitter 30%, total budget 1s. Retries on
// 502/503/504 and timeouts; honors upstream Retry-After via
// backoff.RetryAfter.
//
// Streaming paths MUST NOT call this — once headers + first chunk are
// on the wire we can't transparently retry.
//
// IMPORTANT — RES-02 deferred scope (Phase 5):
// The current Phase 3 dispatcher uses *httputil.ReverseProxy, whose
// ServeHTTP writes directly to the ResponseWriter with no retry-friendly
// return value. Wrapping ReverseProxy in backoff requires a buffering
// middleware that captures the response into a httptest.ResponseRecorder,
// classifies status, replays the request — that's a substantial refactor
// deferred to Phase 5 (saturation-aware routing adds the buffering layer
// for load-shedding anyway). For Phase 3, RES-02's primary intent
// (NON-streaming resilience via breaker + tier fallback) is satisfied
// via the dispatcher path; retry-within-same-upstream is the deferred
// piece. DoWithBackoff is shipped here so future code can adopt it
// without re-deriving the policy.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v5"
)

// DoWithBackoff executes op under the RES-02 retry policy. op MUST be
// idempotent — backoff retries on transient upstream failures by
// calling op again.
//
// Classification:
//   - HTTP 502 / 503 / 504 → retryable (Retry-After honored if present)
//   - context.DeadlineExceeded → retryable
//   - context.Canceled → permanent (client gave up)
//   - other errors → retryable (caller's budget bounds the loop)
//   - 2xx / 4xx → return as-is (no retry)
//
// On budget exhaustion the last attempt's error/response is returned
// to the caller via backoff.Retry's outer return.
func DoWithBackoff(ctx context.Context, op func() (*http.Response, error)) (*http.Response, error) {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 100 * time.Millisecond
	bo.MaxInterval = 500 * time.Millisecond
	bo.Multiplier = 2.0
	bo.RandomizationFactor = 0.3

	wrap := func() (*http.Response, error) {
		resp, err := op()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, backoff.Permanent(err)
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, err // retryable
			}
			return nil, err // generic transient
		}
		switch resp.StatusCode {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, perr := strconv.Atoi(ra); perr == nil && secs > 0 {
					_ = resp.Body.Close()
					return nil, backoff.RetryAfter(secs)
				}
			}
			_ = resp.Body.Close()
			return nil, fmt.Errorf("upstream %d", resp.StatusCode)
		default:
			return resp, nil
		}
	}

	return backoff.Retry(ctx, wrap,
		backoff.WithBackOff(bo),
		backoff.WithMaxElapsedTime(1*time.Second),
	)
}
