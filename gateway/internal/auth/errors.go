// Package auth implements multi-tenant API-key authentication: argon2id
// verification against Postgres, Redis-backed 60s hot-path cache, and a
// chi-compatible HTTP middleware. Follows CONTEXT.md decisions D-A1..D-A5
// (format ifix_sk_, argon2id + cache, multiple active keys per tenant,
// Authorization > X-API-Key precedence).
package auth

import "errors"

var (
	// ErrMissingAPIKey is returned by Verifier.Verify when the request carries
	// no Authorization: Bearer header AND no X-API-Key header. Surfaces as a
	// 401 with code "no_api_key" through the OpenAI envelope.
	ErrMissingAPIKey = errors.New("auth: no API key in request")
	// ErrInvalidAPIKey is returned when the raw key parses but is not found
	// in the api_keys table OR fails argon2id comparison. Surfaces as 401
	// with code "invalid_api_key".
	ErrInvalidAPIKey = errors.New("auth: API key not found")
	// ErrRevokedAPIKey is returned when the raw key resolves to a row whose
	// status is "revoked". Distinct from invalid so operators can see the
	// difference in logs and clients see a precise error code.
	ErrRevokedAPIKey = errors.New("auth: API key revoked")
	// ErrMalformedKey is returned when the raw key fails the syntactic
	// IsWellFormedKey check (wrong prefix, wrong length, or out-of-alphabet
	// character). Constant-time reject — no DB or argon2 call.
	ErrMalformedKey = errors.New("auth: malformed API key")
)

// DataClass is the tenant-level LGPD classification derived from the
// api_keys row and propagated via request context (CONTEXT.md D-B2).
// Downstream audit writers consult this to decide whether to persist
// prompt/response payloads in audit_log_content.
type DataClass string

const (
	// DataClassNormal allows audit_log_content persistence.
	DataClassNormal DataClass = "normal"
	// DataClassSensitive forbids audit_log_content persistence (LGPD).
	DataClassSensitive DataClass = "sensitive"
)
