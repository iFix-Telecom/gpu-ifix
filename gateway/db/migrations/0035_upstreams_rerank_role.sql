-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick 260825 — rerank role. Relax the upstreams role CHECK to admit 'rerank'
-- and seed the two rerank upstream rows the loader resolves (Resolve('rerank', tier)).
-- Mold: 0024_upstreams_tts_role.sql (same CHECK-relax + seed shape).
--
-- Engine note: both tiers are Infinity (michaelf34/infinity) servers speaking
-- POST /v1/rerank {model,query,documents[]} -> {results:[{index,relevance_score}]}
-- with served-model-name bge-reranker-v2-m3:
--   - tier-0 rerank-gpu: the unified Vast pod (STT+TTS+rerank on one RTX 3060),
--     UPSTREAM_RERANK_URL (e.g. http://<pod-ip>:<mapped-7998>).
--   - tier-1 rerank-cpu: the worker-vm ai-gateway-rerank swarm service,
--     UPSTREAM_RERANK_FALLBACK_URL (e.g. http://10.10.10.50:7998).
-- The gateway proxies the body unchanged (no director rewrite — both tiers
-- serve the same model name), so this migration only plumbs role + rows.
--
-- Idempotency: DROP CONSTRAINT IF EXISTS + ON CONFLICT (name) DO NOTHING make a
-- re-run safe. rerank:0 and rerank:1 do not violate UNIQUE(role, tier) (0007:23)
-- because no other row uses ('rerank', *).
ALTER TABLE ai_gateway.upstreams DROP CONSTRAINT IF EXISTS upstreams_role_check;
ALTER TABLE ai_gateway.upstreams ADD CONSTRAINT upstreams_role_check
    CHECK (role IN ('llm','stt','embed','tts','rerank'));

INSERT INTO ai_gateway.upstreams (name, role, tier, url_env, auth_bearer_env) VALUES
    ('rerank-gpu', 'rerank', 0, 'UPSTREAM_RERANK_URL',          NULL),
    ('rerank-cpu', 'rerank', 1, 'UPSTREAM_RERANK_FALLBACK_URL', NULL)
ON CONFLICT (name) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Remove the two seeded rerank rows, then re-narrow the CHECK back to the
-- pre-0035 four roles.
--
-- WARNING (operator): re-narrowing the CHECK will FAIL if ANY rerank row
-- remains beyond the two seeded rows below (same caveat as 0024 Down) — an
-- environment that added EXTRA user-created rerank rows must clean those
-- manually before a full down-migration.
DELETE FROM ai_gateway.upstreams
    WHERE name IN ('rerank-gpu','rerank-cpu');

ALTER TABLE ai_gateway.upstreams DROP CONSTRAINT IF EXISTS upstreams_role_check;
ALTER TABLE ai_gateway.upstreams ADD CONSTRAINT upstreams_role_check
    CHECK (role IN ('llm','stt','embed','tts'));
-- +goose StatementEnd
