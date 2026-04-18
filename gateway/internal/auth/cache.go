// Package auth (cache.go): Redis-backed positive + negative caches for
// the verification hot path. CONTEXT.md D-A2 plus the Codex [HIGH] 02-03
// negative-cache amendment.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// cacheTTL is the positive result cache duration. 60s balances sub-ms
	// hot path with revocation propagation (CONTEXT.md D-A2).
	cacheTTL = 60 * time.Second

	// negCacheTTL is the negative-cache duration. Shorter than the positive
	// cacheTTL (60s) so a newly-issued key propagates within seconds.
	// Codex review [HIGH] 02-03 — formalized D-A2 amendment.
	negCacheTTL = 5 * time.Second
)

// cacheEntry is the JSON payload stored in Redis. Includes Status so
// revoked keys are cached as such (avoids argon2-reverifying a known
// revoked key 10x/second during the 60s window).
type cacheEntry struct {
	TenantID  string    `json:"tenant_id"`
	APIKeyID  string    `json:"api_key_id"`
	DataClass DataClass `json:"data_class"`
	Status    string    `json:"status"` // "active" | "revoked"
	KeyPrefix string    `json:"key_prefix"`
}

// cacheKeyFor returns the Redis positive-cache key for a raw API key.
// The key namespace `gw:apikey:` is shared with idempotency / rate-limiter
// keys (all `gw:*`).
func cacheKeyFor(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return "gw:apikey:" + hex.EncodeToString(sum[:])
}

// negCacheKeyFor returns the Redis negative-cache key for an unknown raw API
// key. Distinct namespace from the positive cache so neither poisons the
// other.
func negCacheKeyFor(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return "gw:apikey:neg:" + hex.EncodeToString(sum[:])
}

func (v *Verifier) cacheGet(ctx context.Context, rawKey string) (cacheEntry, bool, error) {
	if v.redis == nil {
		return cacheEntry{}, false, nil
	}
	raw, err := v.redis.Get(ctx, cacheKeyFor(rawKey)).Bytes()
	if errors.Is(err, redis.Nil) {
		return cacheEntry{}, false, nil
	}
	if err != nil {
		return cacheEntry{}, false, err
	}
	var e cacheEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return cacheEntry{}, false, err
	}
	return e, true, nil
}

func (v *Verifier) cachePut(ctx context.Context, rawKey string, e cacheEntry) error {
	if v.redis == nil {
		return nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return v.redis.Set(ctx, cacheKeyFor(rawKey), b, cacheTTL).Err()
}

// negCacheCheck returns (true, nil) if the key is in the negative cache.
func (v *Verifier) negCacheCheck(ctx context.Context, rawKey string) (bool, error) {
	if v.redis == nil {
		return false, nil
	}
	n, err := v.redis.Exists(ctx, negCacheKeyFor(rawKey)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// negCachePut records an unknown raw key in the negative cache for TTL 5s.
func (v *Verifier) negCachePut(ctx context.Context, rawKey string) error {
	if v.redis == nil {
		return nil
	}
	return v.redis.Set(ctx, negCacheKeyFor(rawKey), "1", negCacheTTL).Err()
}
