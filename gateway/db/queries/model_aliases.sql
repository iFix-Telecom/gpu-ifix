-- name: ListModelAliases :many
-- quick 261007-gyq: rows of one alias come out in ROUTING order (the order the
-- gateway tries upstreams: tier, then tier_priority), not alphabetical. The
-- LEFT JOIN only feeds ORDER BY — returned columns are unchanged. Aliases whose
-- upstream_name has no upstreams row sort last within the alias.
-- quick 261018: priority (per-row, nullable) leads the per-alias order — an
-- alias whose rows are all NULL keeps exactly the global order above; when
-- some rows carry priority, NULLs sort last and the global order breaks ties.
-- The resolver does NOT depend on this ORDER BY (it re-derives the priority
-- map itself); it only keeps `gatewayctl model-alias list` in routing order.
SELECT m.alias, m.upstream, m.target, m.upstream_name, m.provider_prefs, m.priority, m.fallback_models
FROM ai_gateway.model_aliases m
LEFT JOIN ai_gateway.upstreams u ON u.name = m.upstream_name
ORDER BY m.alias, m.priority NULLS LAST, u.tier NULLS LAST, u.tier_priority NULLS LAST, m.upstream_name;

-- name: GetModelAlias :one
SELECT alias, upstream, target, upstream_name, provider_prefs, priority, fallback_models FROM ai_gateway.model_aliases WHERE alias = $1 AND upstream_name = $2;

-- name: UpsertModelAlias :exec
-- Phase 06.9 R7 (REVIEWS.md): used by Plan 04's gatewayctl model-alias CLI.
-- Keeping the data-access via sqlc (rather than ad-hoc SQL in the CLI) keeps
-- a single source of truth on the composite PK semantic + UPSERT shape.
-- quick 260830-o2j: provider_prefs ($5, nullable jsonb) travels with the row —
-- NULL clears any previous per-model provider preference.
-- quick 261018: priority ($6, nullable int) and fallback_models ($7, nullable
-- text[]) follow the same full-row semantic — callers that want to KEEP the
-- current value read the row first and pass it back (gatewayctl/admin do).
INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name, provider_prefs, priority, fallback_models)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (alias, upstream_name) DO UPDATE
    SET target = EXCLUDED.target,
        provider_prefs = EXCLUDED.provider_prefs,
        priority = EXCLUDED.priority,
        fallback_models = EXCLUDED.fallback_models;

-- name: DeleteModelAlias :exec
-- Phase 06.9 R7 (REVIEWS.md): used by Plan 04's gatewayctl model-alias CLI.
-- Composite PK delete — alias alone is no longer unique post-0026.
DELETE FROM ai_gateway.model_aliases WHERE alias = $1 AND upstream_name = $2;
