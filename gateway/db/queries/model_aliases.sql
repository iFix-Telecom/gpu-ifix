-- name: ListModelAliases :many
SELECT alias, upstream, target, created_at FROM ai_gateway.model_aliases ORDER BY alias;

-- name: GetModelAlias :one
SELECT alias, upstream, target, created_at FROM ai_gateway.model_aliases WHERE alias = $1;
