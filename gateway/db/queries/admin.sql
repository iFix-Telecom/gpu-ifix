-- name: CreateTenant :one
INSERT INTO ai_gateway.tenants (slug, name) VALUES ($1, $2)
RETURNING id, slug, name, created_at, updated_at;

-- name: GetTenantBySlug :one
SELECT id, slug, name, created_at, updated_at FROM ai_gateway.tenants WHERE slug = $1;

-- name: ListTenants :many
SELECT id, slug, name, created_at, updated_at FROM ai_gateway.tenants ORDER BY created_at DESC;

-- name: InsertAPIKey :one
-- key_lookup_hash is the SHA-256 (raw bytes) of the full raw key. Computed by
-- auth.GenerateAPIKey in 02-03; stored indexed for fast lookup on the hot path
-- (Codex review [HIGH] 02-03).
INSERT INTO ai_gateway.api_keys (tenant_id, key_hash, key_lookup_hash, key_prefix, data_class)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, tenant_id, key_hash, key_lookup_hash, key_prefix, status, data_class, created_at, revoked_at, last_used_at;

-- name: RevokeAPIKey :exec
UPDATE ai_gateway.api_keys
SET status = 'revoked', revoked_at = NOW()
WHERE id = $1 AND status = 'active';
