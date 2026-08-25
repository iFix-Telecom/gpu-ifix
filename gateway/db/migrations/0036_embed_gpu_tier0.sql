-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick 260825-anq — embed on the unified 3060 pod, mirroring the 0035 rerank
-- topology. Re-tier the embed role:
--
--     tier=0 tier_priority=0  embed-gpu    (NEW — unified Vast pod RTX 3060,
--                                           Infinity bge-m3 dims 1024,
--                                           UPSTREAM_EMBED_GPU_URL)
--     tier=1 tier_priority=10 local-embed  (demoted 0→1 — worker-vm CPU
--                                           Infinity, SAME model/dims 1024;
--                                           prio 10 follows the 0029 STT
--                                           cascade convention for the
--                                           primary tier-1)
--     tier=2 tier_priority=0  openai-embed (demoted 1→2 — OUT of the cascade:
--                                           ResolveAllTier1 filters tier==1
--                                           strictly, so a tier-2 row is
--                                           never resolved by dispatch)
--
-- Engine note: both serving tiers are Infinity (michaelf34/infinity) with
-- served-model-name bge-m3 (dims 1024) — the gateway proxies the body
-- unchanged (passthrough, no director rewrite). openai-embed serves
-- text-embedding-3-small (dims 1536 ≠ 1024): letting it back into the
-- cascade would silently corrupt 1024-dim indexes with 1536-dim vectors —
-- a 503 with pod+CPU both down is strictly better. Row + env are RETAINED
-- on purpose; manual re-enable (accepting the dims hazard) is:
--     UPDATE ai_gateway.upstreams SET tier = 1 WHERE name = 'openai-embed';
--
-- No DDL: the role CHECK already admits 'embed' (0007) and tier has no
-- CHECK, so tier=2 is valid without constraint changes. model_aliases is
-- untouched (embed-gpu is passthrough without a director; the existing
-- bge-m3→openai-embed alias keeps serving only the tier-2 director).
--
-- Idempotency: the UPDATEs carry a WHERE on the pre-migration state (no-op
-- on re-run) and the INSERT is ON CONFLICT (name) DO NOTHING. Order matters
-- for UNIQUE(role, tier, tier_priority) (0029): local-embed must vacate
-- (embed,0,0) BEFORE embed-gpu is inserted into that slot. openai-embed is
-- demoted first for clarity/safety even though local-embed lands on prio 10.
UPDATE ai_gateway.upstreams SET tier = 2
    WHERE name = 'openai-embed' AND tier = 1;

UPDATE ai_gateway.upstreams SET tier = 1, tier_priority = 10
    WHERE name = 'local-embed' AND tier = 0;

INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env) VALUES
    ('embed-gpu', 'embed', 0, 0, 'UPSTREAM_EMBED_GPU_URL', NULL)
ON CONFLICT (name) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Symmetric revert in reverse order: drop the seeded embed-gpu row first
-- (vacating (embed,0,0)), then restore local-embed to tier-0 and
-- openai-embed to tier-1.
--
-- WARNING (operator): rows created manually beyond the seeded ones (e.g. an
-- extra embed row parked on (embed,0,0) or (embed,1,0)) will collide with
-- UNIQUE(role, tier, tier_priority) during this revert — clean those
-- manually before a full down-migration (same caveat as the 0035 Down).
DELETE FROM ai_gateway.upstreams WHERE name = 'embed-gpu';

UPDATE ai_gateway.upstreams SET tier = 0, tier_priority = 0
    WHERE name = 'local-embed' AND tier = 1;

UPDATE ai_gateway.upstreams SET tier = 1
    WHERE name = 'openai-embed' AND tier = 2;
-- +goose StatementEnd
