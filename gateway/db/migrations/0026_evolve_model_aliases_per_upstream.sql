-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 06.9 — widen ai_gateway.model_aliases PK from (alias) to
-- (alias, upstream_name) so tier-1 providers (openrouter-chat /
-- openai-whisper / openai-embed) can carry distinct target slugs
-- alongside the existing tier-0 rows (local-llm / local-stt / local-embed).
--
-- Source: 06.9-CONTEXT.md D-01..D-04 + 06.9-PATTERNS.md (verbatim template
-- lines 42-86) + 06.9-RESEARCH.md (recipe lines 173-251) + 06.9-REVIEWS.md
-- (R3 Down-guard requirement) + 06.9-01-PLAN.md (acceptance criteria).
--
-- Migration number = 0026 (computed at execution time per reviews consensus
-- action #10:
--   LAST_NUM=$(ls gateway/db/migrations/ | sort -V | tail -1 | grep -oE '^[0-9]+')
--   NEXT_NUM=$(printf "%04d" $((10#$LAST_NUM + 1)))
-- Latest migration in tree at exec time: 0025_create_voices.sql).
--
-- Schema design notes:
--   - `upstream` column stays (CHECK IN ('llm','stt','embed')) — it is the
--     ROLE tag, used by audit queries + the existing resolver code path.
--   - NEW column `upstream_name` carries the upstream NAME ('local-llm',
--     'openrouter-chat', etc.) — composite PK with `alias`.
--   - Backfill MUST run BEFORE re-adding PK so the (alias, upstream_name)
--     uniqueness constraint never violates mid-statement.
--
-- Idempotency: ADD COLUMN IF NOT EXISTS + ON CONFLICT (alias, upstream_name)
-- DO NOTHING + WHERE upstream_name IS NULL make a re-run safe.

-- Step 1 — add column nullable so existing rows are visible for backfill.
ALTER TABLE ai_gateway.model_aliases
    ADD COLUMN IF NOT EXISTS upstream_name TEXT;

-- Step 2 — backfill existing rows with their tier-0 upstream name based on
-- the role. Only touches rows that have not been backfilled yet (idempotent).
--   role='llm'   → upstream_name='local-llm'
--   role='stt'   → upstream_name='local-stt'
--   role='embed' → upstream_name='local-embed'
UPDATE ai_gateway.model_aliases
SET upstream_name = CASE upstream
    WHEN 'llm'   THEN 'local-llm'
    WHEN 'stt'   THEN 'local-stt'
    WHEN 'embed' THEN 'local-embed'
END
WHERE upstream_name IS NULL;

-- Step 3 — enforce NOT NULL post-backfill.
ALTER TABLE ai_gateway.model_aliases
    ALTER COLUMN upstream_name SET NOT NULL;

-- Step 4 — drop old single-column PK, add composite PK (alias, upstream_name).
-- Order matters: backfill (Step 2) MUST be complete before the new PK is
-- enforced, otherwise an empty-string upstream_name on >1 row would violate
-- uniqueness (currently safe — 3 distinct aliases — but the explicit ordering
-- is self-documenting and safe under concurrent operator-edited deployments).
ALTER TABLE ai_gateway.model_aliases
    DROP CONSTRAINT IF EXISTS model_aliases_pkey;
ALTER TABLE ai_gateway.model_aliases
    ADD CONSTRAINT model_aliases_pkey PRIMARY KEY (alias, upstream_name);

-- Step 5 — seed the 3 tier-1 rows. ON CONFLICT (alias, upstream_name) DO
-- NOTHING makes the migration idempotent across re-runs.
INSERT INTO ai_gateway.model_aliases (alias, upstream, target, upstream_name) VALUES
    ('qwen',    'llm',   'qwen/qwen3.5-27b',       'openrouter-chat'),
    ('whisper', 'stt',   'whisper-1',              'openai-whisper'),
    ('bge-m3',  'embed', 'text-embedding-3-small', 'openai-embed')
ON CONFLICT (alias, upstream_name) DO NOTHING;

-- Step 6 — R11 column comment (per REVIEWS.md MEDIUM concern + 06.9-01-PLAN
-- acceptance criteria). Documents the canonical values + extensibility
-- contract directly on the schema so future operators/analysts don't have
-- to grep the codebase to know what values to expect.
COMMENT ON COLUMN ai_gateway.model_aliases.upstream_name IS
    'Phase 06.9: upstream NAME (not role). Canonical values: local-llm, local-stt, local-embed (tier-0); openrouter-chat, openai-whisper, openai-embed (tier-1). New tier-1 providers add new values as schema rows.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- R3 Down behavior on production-shaped data (per REVIEWS.md HIGH concern
-- option (b) — explicit failure beats silent data loss):
--   1. DELETE the 3 seeded tier-1 rows ONLY.
--   2. GUARD: before restoring PK on (alias), scan for any remaining
--      duplicate-alias rows across upstreams. If found, RAISE EXCEPTION
--      with an operator-actionable message naming the offending aliases
--      and upstreams. This catches the case where an operator added rows
--      via `gatewayctl model-alias set` between Up and Down — silently
--      dropping data on Down would be worse than aborting loudly.
--   3. Only if guard passed: drop composite PK + restore PK on (alias).
--   4. DOES NOT DROP COLUMN upstream_name — preserve per-upstream data
--      for re-Up idempotency and to protect tier-0 backfilled data +
--      any operator-edited rows. The column stays NOT NULL with no PK
--      participation.
--   5. Recovery procedure when guard trips: operator manually DELETEs the
--      unwanted duplicate-alias rows, then re-runs `goose down -1`.

-- Step 1 — delete the 3 seeded tier-1 rows.
DELETE FROM ai_gateway.model_aliases
    WHERE (alias, upstream_name) IN
          (('qwen','openrouter-chat'), ('whisper','openai-whisper'), ('bge-m3','openai-embed'));

-- Step 2 — guard against operator-created duplicate-alias rows. Aborts the
-- Down with an explicit, actionable RAISE EXCEPTION rather than silently
-- losing data when the PK on (alias) is restored over duplicate aliases.
DO $$
DECLARE
    dup_aliases TEXT;
BEGIN
    SELECT string_agg(alias || ' across {' || array_to_string(arr, ',') || '}', '; ')
      INTO dup_aliases
      FROM (
        SELECT alias, array_agg(upstream_name ORDER BY upstream_name) AS arr
          FROM ai_gateway.model_aliases
         GROUP BY alias
        HAVING count(*) > 1
      ) d;
    IF dup_aliases IS NOT NULL THEN
        RAISE EXCEPTION 'Phase 06.9 migration 0026 Down aborted: duplicate-alias rows exist across upstreams. Manual cleanup required for: %. To recover: DELETE the unwanted rows, then re-run goose down -1.', dup_aliases;
    END IF;
END $$;

-- Step 3 — restore PK on (alias). Only reaches here if the guard passed.
ALTER TABLE ai_gateway.model_aliases
    DROP CONSTRAINT IF EXISTS model_aliases_pkey;
ALTER TABLE ai_gateway.model_aliases
    ADD CONSTRAINT model_aliases_pkey PRIMARY KEY (alias);

-- Step 4 — upstream_name column DELIBERATELY preserved. Re-Up is idempotent;
-- operator-edited tier-0 data + any rows that survived Step 1 retain their
-- upstream identity. Manual drop if ever needed:
--   ALTER TABLE ai_gateway.model_aliases DROP COLUMN upstream_name;
-- +goose StatementEnd
