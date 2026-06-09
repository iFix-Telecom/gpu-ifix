// Package proxy (sensitive.go): bounded retry loop for sensitive-tenant
// requests when the primary upstream's breaker is OPEN. Per CONTEXT.md
// D-B1 the loop is 3 attempts at 200ms / 800ms / 3s (~4s total) and
// MUST NOT route to external upstreams (T-03-06-01) — sensitive tenants
// are LGPD-bound to the local pod.
//
// Goroutine-safety: every sleep selects on ctx.Done so a client
// disconnect during the wait returns ctx.Err() and exits cleanly.
// Pitfall 5 (sensitive retry leaks goroutines) is regression-tested
// in TestSensitiveRetry_ClientDisconnectExits.
package proxy

import (
	"context"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// sensitiveRetryDelays are the 3 attempt delays per CONTEXT.md D-B1.
// Total budget ~4s (200ms + 800ms + 3s). Catches typical pod restart
// micro-blips without holding the HTTP request open for an absurd
// duration.
var sensitiveRetryDelays = []time.Duration{
	200 * time.Millisecond,
	800 * time.Millisecond,
	3 * time.Second,
}

// SensitiveRetry awaits a CLOSED transition for the named upstream
// breaker over 3 exponentially-spaced attempts. Returns:
//
//   - (true, nil) when the breaker transitions to CLOSED mid-loop
//     (gobreaker auto-recovered from OPEN→HALF_OPEN→CLOSED via
//     a successful probe — Wave 3 plan 03-05).
//   - (false, ErrSensitiveRetryExhausted) when all 3 delays elapsed
//     without recovery; caller maps to 503.
//   - (false, ctx.Err()) on client disconnect during a sleep
//     (Pitfall 5 — no goroutine leak).
//
// Bumps obs.SensitiveRetryTotal{outcome=closed|exhausted|canceled}
// for ops dashboard visibility.
func SensitiveRetry(ctx context.Context, bs *breaker.Set, upstreamName string) (bool, error) {
	for _, d := range sensitiveRetryDelays {
		select {
		case <-ctx.Done():
			obs.SensitiveRetryTotal.WithLabelValues("canceled").Inc()
			return false, ctx.Err()
		case <-time.After(d):
		}
		// Use EffectiveState so a Redis-backed operator force-open keeps
		// the loop in retry-and-fail-closed mode even when the natural
		// gobreaker FSM has not observed enough failures to trip. Without
		// this, sensitive requests escape to dispatcher.dispatchTo while
		// the operator believes tier-0 is being held OPEN — surfaced
		// during the SEED-005 sanity rerun (request_id 019e7008-3cc0
		// emitted 502 upstream_unreachable instead of 503
		// upstream_unavailable_for_sensitive_tenant). Same symmetry as
		// EffectiveState use in dispatcher.go:211.
		if _, ok := bs.Get(upstreamName); !ok {
			continue
		}
		if bs.EffectiveState(upstreamName) == gobreaker.StateClosed {
			obs.SensitiveRetryTotal.WithLabelValues("closed").Inc()
			return true, nil
		}
	}
	obs.SensitiveRetryTotal.WithLabelValues("exhausted").Inc()
	return false, ErrSensitiveRetryExhausted
}
