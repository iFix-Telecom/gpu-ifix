-- name: ListActiveKeysByTenant :many
-- Diagnostic / admin-surface query. NOT used on the hot request path — hot
-- path uses GetActiveKeyByLookupHash, which hits the UNIQUE index and returns
-- at most one row (Codex review [HIGH] 02-03).
SELECT id, tenant_id, key_hash, key_lookup_hash, key_prefix, status, data_class, created_at, revoked_at, last_used_at
FROM ai_gateway.api_keys
WHERE tenant_id = $1 AND status = 'active'
ORDER BY last_used_at DESC NULLS LAST;

-- name: GetActiveKeyByLookupHash :one
-- HOT PATH query (Codex review [HIGH] 02-03). Given the SHA-256 of a raw key,
-- return the single active row (or no rows). 02-03's Verify then runs
-- argon2id.ComparePasswordAndHash on exactly that row's key_hash — never on
-- a scan. The UNIQUE INDEX on key_lookup_hash guarantees ≤1 candidate row
-- regardless of the total active-key count.
SELECT id, tenant_id, key_hash, key_lookup_hash, key_prefix, status, data_class, created_at, revoked_at, last_used_at
FROM ai_gateway.api_keys
WHERE key_lookup_hash = $1 AND status = 'active'
LIMIT 1;

-- name: ListActiveKeysAll :many
-- Legacy / diagnostic path. RETAINED for: (a) admin-tooling listing all keys;
-- (b) backfill / repair migration that recomputes key_lookup_hash if it ever
-- gets corrupted. MUST NOT be called on the request hot path — 02-03 Verify
-- uses GetActiveKeyByLookupHash exclusively.
SELECT id, tenant_id, key_hash, key_lookup_hash, key_prefix, status, data_class
FROM ai_gateway.api_keys
WHERE status = 'active';

-- name: GetAPIKeyByID :one
SELECT id, tenant_id, key_hash, key_lookup_hash, key_prefix, status, data_class, created_at, revoked_at, last_used_at
FROM ai_gateway.api_keys
WHERE id = $1;

-- name: TouchKeyLastUsed :exec
-- NOT called per-request by 02-03 (Codex review [MEDIUM] 02-03 — TouchKeyLastUsed
-- is debounced via an in-memory buffer flushing every 60s or on shutdown). This
-- sqlc query remains the low-level write path used by that buffer.
UPDATE ai_gateway.api_keys SET last_used_at = NOW() WHERE id = $1;
