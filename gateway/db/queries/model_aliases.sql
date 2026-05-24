-- name: ListModelAliases :many
SELECT alias, upstream, target, upstream_name FROM ai_gateway.model_aliases ORDER BY alias, upstream_name;

-- name: GetModelAlias :one
SELECT alias, upstream, target, upstream_name FROM ai_gateway.model_aliases WHERE alias = $1 AND upstream_name = $2;

-- name: UpsertModelAlias :exec
-- Phase 06.9 R7 (REVIEWS.md): used by Plan 04's gatewayctl model-alias CLI.
-- Keeping the data-access via sqlc (rather than ad-hoc SQL in the CLI) keeps
-- a single source of truth on the composite PK semantic + UPSERT shape.
INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name)
VALUES ($1, $2, $3, $4)
ON CONFLICT (alias, upstream_name) DO UPDATE SET target = EXCLUDED.target;

-- name: DeleteModelAlias :exec
-- Phase 06.9 R7 (REVIEWS.md): used by Plan 04's gatewayctl model-alias CLI.
-- Composite PK delete — alias alone is no longer unique post-0026.
DELETE FROM ai_gateway.model_aliases WHERE alias = $1 AND upstream_name = $2;
