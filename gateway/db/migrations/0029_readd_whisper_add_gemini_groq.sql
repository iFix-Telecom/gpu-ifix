-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 11.2 — re-add tier-0 Speaches/Whisper STT (revert 0028) AND add a
-- 3-upstream tier-1 STT cascade ordered by tier_priority:
--
--   role=stt:
--     tier=0 tier_priority=0  local-stt        (Phase 11.2 D-B6′ step 3 — revert 0028)
--     tier=1 tier_priority=10 gemini-stt       (D-B6′ step 4 — primary fallback, $0.05/h batch)
--     tier=1 tier_priority=15 groq-whisper     (D-B6′ step 5 — second fallback, OpenAI-compat)
--     tier=1 tier_priority=20 openai-whisper   (D-B6′ step 6 — safety net, already exists)
--
-- Source map:
--   - 11.2-CONTEXT.md D-B5′ / D-B6′ / D-B7 / D-B8 / D-B11 / D-B12
--   - 11.2-RESEARCH.md §Code Examples lines 587-661 (Up/Down draft) + §Pattern 3
--   - 11.2-PATTERNS.md lines 130-156 (6-step Up draft) — pattern-mapper confirmed
--     `circuit_config` JSONB already exists since 0007:14, so we UPDATE (not
--     ADD COLUMN) on the gemini-stt row to set cooldown_s=120 (D-B11).
--   - 0028 DOWN lines 64-70 = verbatim INSERT template for local-stt + alias.
--   - 0007:23 = origin UNIQUE(role,tier) constraint that we widen below.
--
-- Order rationale (additive, fail-safe):
--   1. Add tier_priority column (default 0 — every existing row gets a
--      non-NULL value so the UNIQUE swap in step 2 is sound).
--   2. Swap UNIQUE(role,tier) → UNIQUE(role,tier,tier_priority) BEFORE
--      inserting any new row at the same (role,tier) as an existing one
--      (would otherwise violate the old constraint).
--   3-5. INSERT the three new rows + their (whisper, *) aliases
--        with ON CONFLICT DO NOTHING so re-running Up is idempotent.
--   6. UPDATE openai-whisper.tier_priority=20 last so the row keeps its
--      tier-1 position with the lowest precedence in the cascade.

-- Step 1 — add tier_priority column (idempotent; additive).
ALTER TABLE ai_gateway.upstreams
    ADD COLUMN IF NOT EXISTS tier_priority INT NOT NULL DEFAULT 0;

-- Step 2 — swap the UNIQUE constraint to include tier_priority. The
-- guarded DROP IF EXISTS keeps the migration re-runnable on a partially
-- migrated database (e.g. after a failed previous Up).
ALTER TABLE ai_gateway.upstreams
    DROP CONSTRAINT IF EXISTS upstreams_role_tier_key;
ALTER TABLE ai_gateway.upstreams
    DROP CONSTRAINT IF EXISTS upstreams_role_tier_priority_key;
ALTER TABLE ai_gateway.upstreams
    ADD CONSTRAINT upstreams_role_tier_priority_key UNIQUE (role, tier, tier_priority);

-- Step 3 — revert 0028: re-INSERT local-stt upstream + (whisper, local-stt)
-- alias verbatim from 0028 DOWN block (lines 64-70).
INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env)
    VALUES ('local-stt', 'stt', 0, 0, 'UPSTREAM_STT_URL', NULL)
    ON CONFLICT (name) DO NOTHING;

INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name)
    VALUES ('whisper', 'stt', 'Systran/faster-whisper-large-v3', 'local-stt')
    ON CONFLICT (alias, upstream_name) DO NOTHING;

-- Step 4 — INSERT gemini-stt upstream + alias at tier_priority=10 (primary
-- tier-1 fallback). Env vars are slot-named (D-B7): the operator can swap
-- providers without renaming envs.
INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env)
    VALUES ('gemini-stt', 'stt', 1, 10,
            'UPSTREAM_STT_FALLBACK_1_URL', 'UPSTREAM_STT_FALLBACK_1_AUTH_BEARER')
    ON CONFLICT (name) DO NOTHING;

INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name)
    VALUES ('whisper', 'stt', 'gemini-2.5-flash-lite', 'gemini-stt')
    ON CONFLICT (alias, upstream_name) DO NOTHING;

-- D-B11: 120s cooldown when Gemini breaker trips (2x rolling-1min window) so
-- the cascade falls through to groq-whisper for the cooldown window.
-- `circuit_config` JSONB column already exists since 0007:14 — only UPDATE
-- needed (no ADD COLUMN).
UPDATE ai_gateway.upstreams
    SET circuit_config = '{"cooldown_s":120}'::jsonb
    WHERE name = 'gemini-stt';

-- Step 5 — INSERT groq-whisper upstream + alias at tier_priority=15. Groq's
-- `/openai/v1/audio/transcriptions` is OpenAI-compatible (D-B8), so it reuses
-- the existing `BuildOpenAIWhisperDirector` with a different URL+bearer.
INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env)
    VALUES ('groq-whisper', 'stt', 1, 15,
            'UPSTREAM_STT_FALLBACK_2_URL', 'UPSTREAM_STT_FALLBACK_2_AUTH_BEARER')
    ON CONFLICT (name) DO NOTHING;

INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name)
    VALUES ('whisper', 'stt', 'whisper-large-v3', 'groq-whisper')
    ON CONFLICT (alias, upstream_name) DO NOTHING;

-- Step 6 — demote openai-whisper to the lowest tier-1 precedence so the
-- cascade hits gemini-stt (10) → groq-whisper (15) → openai-whisper (20).
UPDATE ai_gateway.upstreams
    SET tier_priority = 20
    WHERE name = 'openai-whisper';

COMMENT ON COLUMN ai_gateway.upstreams.tier_priority IS
    'Phase 11.2: ordering within (role, tier). Lower wins. Tier-0 rows always 0. '
    'Tier-1 with multiple providers: (stt,1,10)=gemini-stt primary fallback, '
    '(stt,1,15)=groq-whisper secondary, (stt,1,20)=openai-whisper safety net.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Symmetric removal of the rows added in Up. ON CONFLICT-free DELETEs are
-- idempotent via WHERE clauses so re-running Down after a partial rollback
-- is safe.

-- Aliases first (specific-to-broad ordering matching 0028 Up rationale).
DELETE FROM ai_gateway.model_aliases
    WHERE (alias, upstream_name) IN (
        ('whisper', 'gemini-stt'),
        ('whisper', 'groq-whisper'),
        ('whisper', 'local-stt')
    );

-- Upstream rows.
DELETE FROM ai_gateway.upstreams WHERE name = 'gemini-stt';
DELETE FROM ai_gateway.upstreams WHERE name = 'groq-whisper';
DELETE FROM ai_gateway.upstreams WHERE name = 'local-stt';

-- Restore openai-whisper to a non-promoted tier_priority (matches the
-- column default — single tier-1 STT row again, same shape as post-0028).
UPDATE ai_gateway.upstreams
    SET tier_priority = 0
    WHERE name = 'openai-whisper';

-- NOTE: We deliberately KEEP the tier_priority column + the
-- UNIQUE(role,tier,tier_priority) constraint — both are additive and other
-- rows may depend on the new shape. To fully roll back column + constraint
-- (manual operator action):
--   ALTER TABLE ai_gateway.upstreams DROP CONSTRAINT upstreams_role_tier_priority_key;
--   ALTER TABLE ai_gateway.upstreams ADD CONSTRAINT upstreams_role_tier_key UNIQUE (role, tier);
--   ALTER TABLE ai_gateway.upstreams DROP COLUMN tier_priority;
-- +goose StatementEnd
