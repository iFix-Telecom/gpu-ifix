-- name: ListEnabledUpstreams :many
-- Hot-path load at boot and on LISTEN/NOTIFY (CONTEXT.md D-D2). Returns
-- all enabled rows ordered by (role, tier, tier_priority) so the Loader
-- can deterministically build tier-0/tier-1 maps.
-- Phase 11.2 (D-B5′/D-B6′): tier_priority widens (role,tier) for the STT
-- multi-tier-1 cascade.
SELECT id, name, role, tier, tier_priority, url_env, auth_bearer_env, enabled, weight,
       circuit_config, last_probe_at, last_probe_ms, last_probe_status,
       last_probe_error, created_at, updated_at
FROM ai_gateway.upstreams
WHERE enabled = TRUE
ORDER BY role, tier, tier_priority;

-- name: ListAllUpstreams :many
-- Admin surface (gatewayctl upstreams list). Returns every row regardless
-- of enabled state so the operator can re-enable disabled upstreams.
SELECT id, name, role, tier, tier_priority, url_env, auth_bearer_env, enabled, weight,
       circuit_config, last_probe_at, last_probe_ms, last_probe_status,
       last_probe_error, created_at, updated_at
FROM ai_gateway.upstreams
ORDER BY role, tier, tier_priority;

-- name: GetUpstreamByName :one
-- Used by gatewayctl upstreams update/enable/disable to verify the name
-- exists before mutating.
SELECT id, name, role, tier, tier_priority, url_env, auth_bearer_env, enabled, weight,
       circuit_config, last_probe_at, last_probe_ms, last_probe_status,
       last_probe_error, created_at, updated_at
FROM ai_gateway.upstreams
WHERE name = $1;

-- name: UpdateUpstreamProbe :exec
-- Written by the probe goroutine every probe cycle (CONTEXT.md D-A2).
-- Batched via buffered channel + 1s flush (see gateway/internal/upstreams/probe.go).
-- Trigger 0009_upstreams_notify_trigger.sql does NOT fire on this UPDATE because
-- the WHEN clause only watches config columns (Pitfall 7).
UPDATE ai_gateway.upstreams
SET last_probe_at = $2,
    last_probe_ms = $3,
    last_probe_status = $4,
    last_probe_error = $5,
    updated_at = NOW()
WHERE name = $1;

-- name: UpdateUpstreamAdmin :exec
-- Called by gatewayctl upstreams update <name> --tier=N --enabled=true
-- --circuit-failures=5. Triggers NOTIFY via notify_upstreams_changed() (migration 0009)
-- which the listen goroutine consumes to reload config.
-- Fields passed as NULL are left unchanged (COALESCE). Explicit ::int / ::boolean
-- casts force sqlc to generate nullable pgtype.Int4 / pgtype.Bool params so the
-- caller can pass NULL via the typed wrappers (CONTEXT.md D-D2 / Pattern admin CLI).
UPDATE ai_gateway.upstreams
SET tier = COALESCE(sqlc.narg('tier')::int, tier),
    enabled = COALESCE(sqlc.narg('enabled')::boolean, enabled),
    circuit_config = COALESCE(sqlc.narg('circuit_config')::jsonb, circuit_config),
    updated_at = NOW()
WHERE name = sqlc.arg('name');

-- name: SetUpstreamEnabled :exec
-- Shortcut for enable/disable subcommands.
UPDATE ai_gateway.upstreams
SET enabled = $2,
    updated_at = NOW()
WHERE name = $1;
