// Package auth (apikey.go): the chi-compatible Middleware + Verifier hot
// path. Codex review [HIGH] 02-03 redesign — at most ONE argon2id verify
// per request regardless of total active-key count.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// authQueries abstracts sqlc-generated methods used by Verify and the
// TouchBuffer flush loop. Hot path uses GetActiveKeyByLookupHash (UNIQUE
// index → ≤1 row). TouchKeyLastUsed is NOT called per-request — the
// TouchBuffer flush path calls it.
type authQueries interface {
	GetActiveKeyByLookupHash(ctx context.Context, keyLookupHash []byte) (gen.GetActiveKeyByLookupHashRow, error)
	TouchKeyLastUsed(ctx context.Context, id uuid.UUID) error
}

// Verifier resolves a raw API key to an AuthContext. The hot path runs at
// most ONE argon2 verify per request (Codex review [HIGH] 02-03).
//   - positive cache hit → 0 DB, 0 argon2
//   - negative cache hit → 0 DB, 0 argon2
//   - cache miss → 1 DB (GetActiveKeyByLookupHash, UNIQUE index → ≤1 row) + ≤1 argon2
type Verifier struct {
	pool     *pgxpool.Pool
	q        authQueries
	redis    *redis.Client
	log      *slog.Logger
	touchBuf *TouchBuffer
}

// NewVerifier wires the pool, queries, redis client, and the debounced
// TouchBuffer. The caller owns touchBuf lifecycle and MUST call
// `touchBuf.Run(ctx)` in a goroutine + cancel ctx on shutdown so the final
// flush runs.
func NewVerifier(pool *pgxpool.Pool, rdb *redis.Client, log *slog.Logger, touchBuf *TouchBuffer) *Verifier {
	if log == nil {
		log = slog.Default()
	}
	return &Verifier{
		pool:     pool,
		q:        gen.New(pool),
		redis:    rdb,
		log:      log.With("module", "AUTH"),
		touchBuf: touchBuf,
	}
}

// NewVerifierWithQueries is the test-injection constructor: callers supply
// a fake authQueries + TouchBuffer. Used by unit tests and the load test.
func NewVerifierWithQueries(q authQueries, rdb *redis.Client, log *slog.Logger, touchBuf *TouchBuffer) *Verifier {
	if log == nil {
		log = slog.Default()
	}
	return &Verifier{
		q:        q,
		redis:    rdb,
		log:      log.With("module", "AUTH"),
		touchBuf: touchBuf,
	}
}

// ExtractKey returns the raw key per D-A5: Authorization: Bearer takes
// precedence over X-API-Key. Trims exactly "Bearer " (case-sensitive per
// RFC 6750 preferred practice).
func ExtractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// enumString coerces sqlc's interface{} enum scan result to a Go string.
// pgx returns Postgres ENUM values as either string or []byte depending
// on the protocol path; both are accepted.
func enumString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

// Verify resolves rawKey to an AuthContext.
//
// Hot-path design (Codex review [HIGH] 02-03):
//  1. Malformed reject → 0 DB, 0 argon2.
//  2. Positive cache hit → 0 DB, 0 argon2.
//  3. Negative cache hit (D-A2 amendment, formalized) → 0 DB, 0 argon2.
//  4. Cache miss → GetActiveKeyByLookupHash returns ≤1 row via UNIQUE index
//     on key_lookup_hash. At most 1 argon2id verify regardless of total
//     active-key count.
//
// Timing-attack note: the SHA-256 + DB roundtrip happen for both known and
// unknown keys (UNIQUE index returns 0 or 1 rows in the same plan shape). An
// attacker cannot distinguish "unknown key" from "known key with bad hash"
// via timing without exploiting argon2 itself, which argon2id.Compare
// protects via constant-time compare.
func (v *Verifier) Verify(ctx context.Context, rawKey string) (AuthContext, error) {
	if rawKey == "" {
		return AuthContext{}, ErrMissingAPIKey
	}
	if !IsWellFormedKey(rawKey) {
		return AuthContext{}, ErrMalformedKey
	}

	// 1. Positive cache fast path.
	if hit, found, err := v.cacheGet(ctx, rawKey); err == nil && found {
		return hitToAuth(hit)
	} else if err != nil {
		v.log.WarnContext(ctx, "auth cache get failed", "err", err)
	}

	// 2. Negative cache fast path — formalized D-A2 amendment.
	// Unknown keys seen in the last 5s return 401 without DB/argon2. TTL is
	// deliberately shorter than positive cache (60s) so a newly-issued key
	// propagates quickly. Codex review [HIGH] 02-03.
	if hit, err := v.negCacheCheck(ctx, rawKey); err == nil && hit {
		return AuthContext{}, ErrInvalidAPIKey
	}

	// 3. Indexed lookup — 0 or 1 rows (UNIQUE index on key_lookup_hash).
	lookup := LookupHash(rawKey)
	row, err := v.q.GetActiveKeyByLookupHash(ctx, lookup)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = v.negCachePut(ctx, rawKey)
			return AuthContext{}, ErrInvalidAPIKey
		}
		return AuthContext{}, fmt.Errorf("auth: db lookup: %w", err)
	}

	// 4. At most 1 argon2 verify on the single candidate row. Mismatch here
	// is essentially impossible given the UNIQUE index on key_lookup_hash
	// (SHA-256 collision), but defense-in-depth: still reject + neg cache.
	match, vErr := VerifyHash(rawKey, row.KeyHash)
	if vErr != nil {
		v.log.ErrorContext(ctx, "argon2 verify error", "err", vErr, "api_key_id", row.ID.String())
		return AuthContext{}, ErrInvalidAPIKey
	}
	if !match {
		_ = v.negCachePut(ctx, rawKey)
		return AuthContext{}, ErrInvalidAPIKey
	}

	entry := cacheEntry{
		TenantID:  row.TenantID.String(),
		APIKeyID:  row.ID.String(),
		DataClass: DataClass(enumString(row.DataClass)),
		Status:    enumString(row.Status),
		KeyPrefix: row.KeyPrefix,
	}
	_ = v.cachePut(ctx, rawKey, entry)
	// Debounced touch (Codex review [MEDIUM] 02-03) — coalesce multiple
	// requests for the same key into one UPDATE flushed every 60s.
	if v.touchBuf != nil {
		v.touchBuf.Enqueue(row.ID)
	}
	return hitToAuth(entry)
}

func hitToAuth(e cacheEntry) (AuthContext, error) {
	switch e.Status {
	case "active":
		return AuthContext{
			TenantID:  e.TenantID,
			APIKeyID:  e.APIKeyID,
			DataClass: e.DataClass,
			KeyPrefix: e.KeyPrefix,
		}, nil
	case "revoked":
		return AuthContext{}, ErrRevokedAPIKey
	default:
		return AuthContext{}, ErrInvalidAPIKey
	}
}

// Middleware returns a chi-compatible middleware that enforces auth and
// threads AuthContext through the request. On failure, writes the OpenAI
// error envelope 401 (TEN-08 consistency).
func Middleware(v *Verifier, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := ExtractKey(r)
			ac, err := v.Verify(r.Context(), rawKey)
			if err != nil {
				status := http.StatusUnauthorized
				code := "invalid_api_key"
				msg := "The API key is invalid."
				switch {
				case errors.Is(err, ErrMissingAPIKey):
					code = "no_api_key"
					msg = "Missing API key. Provide Authorization: Bearer <key> or X-API-Key header."
				case errors.Is(err, ErrMalformedKey):
					code = "malformed_api_key"
					msg = "The API key is malformed. Expected format: ifix_sk_<32 chars>."
				case errors.Is(err, ErrRevokedAPIKey):
					code = "revoked_api_key"
					msg = "The API key has been revoked."
				}
				log.WarnContext(r.Context(), "auth rejected",
					"err", err.Error(),
					"request_id", httpx.RequestIDFrom(r.Context()),
					"key_prefix", safeKeyPrefix(rawKey),
				)
				httpx.WriteOpenAIError(w, status, "authentication_error", code, msg)
				return
			}
			ctx := WithContext(r.Context(), ac)
			// Enrich the request-scoped logger.
			if rl := httpx.LoggerFrom(ctx); rl != nil {
				ctx = httpx.WithLogger(ctx,
					rl.With("tenant_id", ac.TenantID, "api_key_id", ac.APIKeyID, "data_class", string(ac.DataClass)))
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// safeKeyPrefix returns a log-safe preview of the raw key (first 8 + last 4).
// NEVER log the whole raw key; log only `ifix_sk_****abcd`.
func safeKeyPrefix(raw string) string {
	if len(raw) < 12 {
		return "ifix_sk_****"
	}
	return KeyPrefix + "****" + raw[len(raw)-4:]
}
