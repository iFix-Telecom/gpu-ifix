// Package idempotency implements Stripe-style Idempotency-Key semantics
// for POST /v1/chat/completions (non-streaming). Scope is (tenant_id,
// key); values carry the full response body + whitelisted headers + a
// sha256-of-canonical-body request hash so the middleware can detect
// body-reuse-with-same-key as a 422 conflict (CONTEXT.md D-C1..D-C5).
package idempotency

import "errors"

var (
	// ErrConflict — same key was used with a different body — HTTP 422 (D-C3).
	ErrConflict = errors.New("idempotency: key reused with different body")
	// ErrStreamingNotSupported — Idempotency-Key + `"stream":true` — HTTP 400 (D-C4).
	ErrStreamingNotSupported = errors.New("idempotency: streaming requests not supported")
	// ErrUnsupportedRoute — Idempotency-Key on non-chat route — HTTP 400 (D-C4).
	ErrUnsupportedRoute = errors.New("idempotency: route does not support Idempotency-Key")
	// ErrInFlightTimeout — winner did not complete within wait-poll budget (30s);
	// loser returns HTTP 409 Conflict + Retry-After (Codex review [MEDIUM] 02-06).
	ErrInFlightTimeout = errors.New("idempotency: in-flight winner did not complete in budget")
)
