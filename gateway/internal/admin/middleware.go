// Package admin (middleware.go): X-Admin-Key bcrypt-backed auth middleware.
// All /admin/* routes are gated by Middleware; missing or invalid keys
// return 401 with the standard OpenAI error envelope (httpx.WriteOpenAIError).
//
// Verify flow:
//  1. Redis cache fast-path (60s TTL; mirrors gateway/internal/auth/cache.go).
//  2. DB lookup via SHA-256 lookup hash (admin_keys table, UNIQUE index → ≤1 row).
//  3. bcrypt.CompareHashAndPassword — constant-time verify.
//  4. Cache positive result for 60s.
//
// Auth is replaced by Better Auth/SSO in Phase 10 (PRD-06). Table
// admin_keys persists for fallback.
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
	// adminCacheTTL mirrors auth.cacheTTL (60s). Short enough that a
	// revoked key propagates quickly; long enough to avoid bcrypt per
	// admin request (bcrypt cost 10 ~ 50ms — acceptable for low-freq
	// admin path but still heavy).
	adminCacheTTL = 60 * time.Second
)

// AdminContext is the per-request admin identity injected by Middleware.
// Handlers recover it via FromContext.
type AdminContext struct {
	AdminKeyID uuid.UUID
	Label      string
	KeyPrefix  string
}

// adminCacheEntry is the JSON payload stored in Redis for positive
// verify results. Fields mirror AdminContext but with string IDs so
// json.Marshal is unambiguous.
type adminCacheEntry struct {
	AdminKeyID string `json:"admin_key_id"`
	Label      string `json:"label"`
	KeyPrefix  string `json:"key_prefix"`
}

// adminQueries isolates the sqlc surface so tests can inject a fake
// without a real pgxpool.
type adminQueries interface {
	GetAdminKeyByLookupHash(ctx context.Context, keyLookupHash []byte) (gen.AiGatewayAdminKey, error)
}

// Verifier performs X-Admin-Key verification. One instance per gateway
// process; threadsafe. Test wiring: pass nil for queries AND nil for
// rdb and the Verifier will always return ErrMissingAdminKey on empty
// key input and ErrInvalidAdminKey on non-empty input (defensive —
// middleware_test.go exercises only the missing-header branch).
type Verifier struct {
	q   adminQueries
	rdb redis.UniversalClient
	log *slog.Logger
}

// NewVerifier wires the queries + redis client + logger. Queries can be
// nil in test wiring (every non-empty key returns ErrInvalidAdminKey).
// rdb can be nil (cache skipped; DB fetch on every request).
func NewVerifier(q adminQueries, rdb redis.UniversalClient, log *slog.Logger) *Verifier {
	if log == nil {
		log = slog.Default()
	}
	return &Verifier{q: q, rdb: rdb, log: log.With("module", "ADMIN")}
}

// Verify performs cache → DB → bcrypt. Returns ErrMissingAdminKey on
// empty input, ErrInvalidAdminKey on any negative outcome.
func (v *Verifier) Verify(ctx context.Context, rawKey string) (AdminContext, error) {
	if rawKey == "" {
		return AdminContext{}, ErrMissingAdminKey
	}

	// 1) Redis cache fast-path.
	if v.rdb != nil {
		if raw, err := v.rdb.Get(ctx, adminCacheKeyFor(rawKey)).Bytes(); err == nil {
			var entry adminCacheEntry
			if jerr := json.Unmarshal(raw, &entry); jerr == nil {
				id, perr := uuid.Parse(entry.AdminKeyID)
				if perr == nil {
					return AdminContext{
						AdminKeyID: id,
						Label:      entry.Label,
						KeyPrefix:  entry.KeyPrefix,
					}, nil
				}
			}
		}
	}

	// 2) DB lookup by SHA-256 lookup hash.
	if v.q == nil {
		return AdminContext{}, ErrInvalidAdminKey
	}
	sum := sha256.Sum256([]byte(rawKey))
	row, err := v.q.GetAdminKeyByLookupHash(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminContext{}, ErrInvalidAdminKey
		}
		return AdminContext{}, err
	}

	// 3) bcrypt verify (constant-time).
	if err := bcrypt.CompareHashAndPassword([]byte(row.KeyHash), []byte(rawKey)); err != nil {
		return AdminContext{}, ErrInvalidAdminKey
	}

	// 4) Cache positive result.
	if v.rdb != nil {
		entry := adminCacheEntry{
			AdminKeyID: row.ID.String(),
			Label:      row.Label,
			KeyPrefix:  row.KeyPrefix,
		}
		if data, jerr := json.Marshal(entry); jerr == nil {
			_ = v.rdb.Set(ctx, adminCacheKeyFor(rawKey), data, adminCacheTTL).Err()
		}
	}

	return AdminContext{
		AdminKeyID: row.ID,
		Label:      row.Label,
		KeyPrefix:  row.KeyPrefix,
	}, nil
}

// adminCacheKeyFor returns the Redis cache key for a raw admin key. The
// `gw:admin:` namespace is distinct from `gw:apikey:` (auth/cache.go) so
// neither poisons the other.
func adminCacheKeyFor(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "gw:admin:" + hex.EncodeToString(sum[:])
}

// Middleware enforces X-Admin-Key on /admin/* routes. Returns 401 +
// OpenAI error envelope on missing/invalid; otherwise injects
// AdminContext into request context for downstream handlers.
func Middleware(v *Verifier, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := r.Header.Get("X-Admin-Key")
			ac, err := v.Verify(r.Context(), rawKey)
			if err != nil {
				code := "invalid_admin_key"
				msg := "Invalid X-Admin-Key."
				if errors.Is(err, ErrMissingAdminKey) {
					code = "missing_admin_key"
					msg = "Missing X-Admin-Key header."
				}
				httpx.WriteOpenAIError(w, http.StatusUnauthorized,
					"authentication_error", code, msg)
				obs.GatewayAdminRequests.WithLabelValues(r.URL.Path, "4xx").Inc()
				return
			}
			ctx := contextWithAdmin(r.Context(), ac)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type ctxKey struct{}

func contextWithAdmin(ctx context.Context, a AdminContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

// FromContext recovers the AdminContext set by Middleware. Handlers
// should call this to access admin identity for audit logging.
func FromContext(ctx context.Context) (AdminContext, bool) {
	a, ok := ctx.Value(ctxKey{}).(AdminContext)
	return a, ok
}
