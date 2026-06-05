-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 11.1 — remove tier-0 Speaches/Whisper STT from gateway schema.
--
-- Source: 11.1-CONTEXT.md D-A4 (DELETE migration, not soft-disable) + D-A5
-- (DELETE (whisper, local-stt) alias; PRESERVE (whisper, openai-whisper)
-- → whisper-1 + upstreams.openai-whisper row); SEED-010 Mudança 9.
--
-- Why two DELETEs in one migration:
--   The two rows are coupled by the Phase 06.9 routing contract — when the
--   tier-0 upstream row goes away the matching alias row becomes unreachable
--   schema. Removing them atomically keeps the post-migration schema
--   internally consistent (every alias row points at a still-existing
--   upstream name).
--
-- Order rationale (per 11.1-RESEARCH.md Pitfall #3 — specific-to-broad):
--   1. DELETE the (whisper, local-stt) alias row first (composite-PK
--      specific row).
--   2. DELETE the local-stt upstream row second (name-unique broader row).
--   The order is arbitrary in terms of correctness (model_aliases.upstream_name
--   is plain TEXT with COMMENT documentation, NOT a FK — confirmed via
--   inspection of 0026's Step 6 COMMENT statement and absence of any FOREIGN
--   KEY clause across migrations 0007 / 0024 / 0026 that touch either table).
--   audit_log retains historical upstream_name='local-stt' strings untouched
--   (text-only, no FK) for forensic traceability.
--
-- D-A5 PRESERVATION (asserted by TestIntegration_Migration0028_Up_PreservesOpenAIWhisper):
--   ai_gateway.upstreams row name='openai-whisper' (tier 1) untouched.
--   ai_gateway.model_aliases row (alias='whisper', upstream_name='openai-whisper',
--   target='whisper-1') untouched — POST /v1/audio/transcriptions {"model":"whisper"}
--   continues resolving via tier-1 OpenAI directly with no breaker drive.

-- Step 1 — delete the alias row first (specific-to-broad).
DELETE FROM ai_gateway.model_aliases
    WHERE alias = 'whisper' AND upstream_name = 'local-stt';

-- Step 2 — delete the upstream row.
DELETE FROM ai_gateway.upstreams
    WHERE name = 'local-stt';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Restore the Phase 06.9 baseline shape for both rows. ON CONFLICT DO NOTHING
-- guarantees the Down is safe to re-run even if the operator manually
-- re-inserted one of the rows between Up and Down.
--
-- Shape sources (verified against the tree at this migration's authoring time):
--   - upstreams.local-stt: 0008_seed_upstreams.sql lines 5-12 — role='stt',
--     tier=0, url_env='UPSTREAM_STT_URL', auth_bearer_env=NULL. Columns added
--     by later migrations all have defaults (enabled, weight, circuit_config,
--     last_probe_*, created_at, updated_at — see 0007_create_upstreams.sql
--     CREATE TABLE definition + 0017's UPDATE to circuit_config which lands
--     on top of the row already created here).
--   - model_aliases (whisper, local-stt): 0026 Step 2 backfill maps role='stt'
--     to upstream_name='local-stt'; the row's target column carries
--     'Systran/faster-whisper-large-v3' (the Speaches default model wired in
--     the original 0009-era seed before the composite PK widening).

INSERT INTO ai_gateway.upstreams (name, role, tier, url_env, auth_bearer_env)
    VALUES ('local-stt', 'stt', 0, 'UPSTREAM_STT_URL', NULL)
    ON CONFLICT (name) DO NOTHING;

INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name)
    VALUES ('whisper', 'stt', 'Systran/faster-whisper-large-v3', 'local-stt')
    ON CONFLICT (alias, upstream_name) DO NOTHING;
-- +goose StatementEnd
