-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 06.9 follow-up — swap tier-1 OpenRouter chat target from
-- `qwen/qwen3.5-27b` (Wave 0 Gate A pin, Novita provider) to
-- `deepseek/deepseek-v4-flash:nitro` (validated 2026-05-24 against live
-- OpenRouter: HTTP 200, canonical `deepseek/deepseek-v4-flash-20260423`,
-- routed via SiliconFlow).
--
-- Decision context (operator authorized 2026-05-24):
--   Q1 (alias semantics): keep alias=`qwen` — transparent to client, fallback
--     swaps the upstream target without changing the API contract.
--   Q2 (delivery): new migration 0027 (NOT amend of 0026) — preserves PR
--     history + lets ops audit the swap as its own commit.
--   Q3 (provider order): OpenRouter chooses (no env override of
--     `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER`). Schema row carries target
--     only; provider routing is OpenRouter's job. Operator removes the
--     `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=novita` line from .env if
--     present (manual cleanup, not gated by the migration).
--
-- Why the target switched:
--   `:nitro` suffix is OpenRouter's high-performance routing variant. The
--   `deepseek/deepseek-v4-flash` family is significantly cheaper (~10x) than
--   `qwen/qwen3.5-27b` for the throughput tier-1 fallback covers and serves
--   our latency budget for in-conversation overflow. Q3 PR #1 made the
--   per-upstream rewrite mechanism work; this migration changes the value it
--   rewrites to.
--
-- Idempotency: UPDATE is naturally idempotent. The WHERE clause keys on
--   (alias, upstream_name) which is the composite PK from migration 0026 —
--   no concurrent-update concerns. The R3 Down guard pattern from 0026 is
--   not needed here because we're modifying a value, not changing the
--   schema shape.

-- Step 1 — UPDATE the seeded tier-1 OpenRouter chat target.
UPDATE ai_gateway.model_aliases
   SET target = 'deepseek/deepseek-v4-flash:nitro'
 WHERE alias = 'qwen'
   AND upstream_name = 'openrouter-chat'
   AND target = 'qwen/qwen3.5-27b';

-- Step 2 — defensive assertion: confirm the update touched exactly 1 row.
-- This catches the case where an operator manually swapped the value before
-- this migration ran (would leave 0 rows updated; we'd silently no-op).
DO $$
DECLARE
    matched INTEGER;
BEGIN
    SELECT count(*) INTO matched
      FROM ai_gateway.model_aliases
     WHERE alias = 'qwen'
       AND upstream_name = 'openrouter-chat'
       AND target = 'deepseek/deepseek-v4-flash:nitro';
    IF matched <> 1 THEN
        RAISE EXCEPTION 'Migration 0027 invariant violated: expected exactly 1 row (qwen, openrouter-chat) with target=deepseek/deepseek-v4-flash:nitro, found %. Investigate manually before retrying.', matched;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Reverse the Up: restore the qwen/qwen3.5-27b target so migration 0026's
-- shipped default is recoverable. Same idempotency + assertion pattern.

UPDATE ai_gateway.model_aliases
   SET target = 'qwen/qwen3.5-27b'
 WHERE alias = 'qwen'
   AND upstream_name = 'openrouter-chat'
   AND target = 'deepseek/deepseek-v4-flash:nitro';

DO $$
DECLARE
    matched INTEGER;
BEGIN
    SELECT count(*) INTO matched
      FROM ai_gateway.model_aliases
     WHERE alias = 'qwen'
       AND upstream_name = 'openrouter-chat'
       AND target = 'qwen/qwen3.5-27b';
    IF matched <> 1 THEN
        RAISE EXCEPTION 'Migration 0027 Down invariant violated: expected exactly 1 row (qwen, openrouter-chat) with target=qwen/qwen3.5-27b, found %. Investigate manually before retrying.', matched;
    END IF;
END $$;
-- +goose StatementEnd
