-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- ============================================================================
-- Phase 4 — Initial price + fx seed (D-B3 + D-B4)
--
-- Source of values: .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-WAVE0-GATES.md
--   Confirmed: 2026-04-21 (placeholder sign-off by Pedro)
--   Values taken from RESEARCH §State of the Art (Apr 2026 snapshot), NOT from
--   live OpenRouter/OpenAI dashboard fetch at gate open time.
--
-- !! UAT REVALIDATION REQUIRED !!
--   Plan 04-09 (Wave 6 human UAT) MUST revalidate A1/A2/A5 against live
--   dashboards (openrouter.ai/qwen/qwen3.5-27b providers tab, openai.com
--   pricing page) and emit migration 0016 (`ALTER prices` + `UPDATE fx_rates`)
--   if observed drift > 10%.
--
-- Per-tenant quota overrides: NONE at this time. Operator (A3 in gate doc)
-- approved the migration-0013 DEFAULT column values (10M daily tokens,
-- 300M monthly, 600 daily audio min, 18000 monthly, 100k daily embeds,
-- 3M monthly, rps=20, rpm=600) for the v1 rollout — refine per-tenant
-- later via `gatewayctl tenant set-quota`.
-- ============================================================================

-- ----------------------------------------------------------------------------
-- LLM prices: qwen/qwen3.5-27b via OpenRouter routed to Fireworks
--
-- A1/A2 (04-WAVE0-GATES.md):
--   Aggregate prompt     = $0.195 / 1M tokens → $0.000000195 / token
--   Aggregate completion = $1.56  / 1M tokens → $0.00000156  / token
--   Provider             = openrouter-fireworks (Phase 3 D-C1 pin; revalidar UAT)
--
-- Bifrost calculator shows $0.30/$2.40 (50% higher) — flagged as UAT revalidation
-- item in 04-WAVE0-GATES.md A1. If live dashboard agrees with Bifrost, 04-09
-- emits migration 0016.
-- ----------------------------------------------------------------------------
INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd, notes)
VALUES
    ('qwen3.5-27b', 'openrouter-fireworks', 'input_token',  0.00000020,
     'Phase 4 seed (04-WAVE0-GATES.md A1 $0.195/1M; stored rounded to numeric(12,8)). UAT revalidate 04-09.'),
    ('qwen3.5-27b', 'openrouter-fireworks', 'output_token', 0.00000156,
     'Phase 4 seed (04-WAVE0-GATES.md A1 $1.56/1M). UAT revalidate 04-09.')
ON CONFLICT (model, provider, unit, valid_from) DO NOTHING;

-- ----------------------------------------------------------------------------
-- STT: whisper-1 (OpenAI legacy endpoint — still active Apr 2026)
--
-- A5 (04-WAVE0-GATES.md):
--   $0.006 / minute → $0.0001 / second
-- ----------------------------------------------------------------------------
INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd, notes)
VALUES
    ('whisper-1', 'openai', 'audio_second', 0.00010000,
     'Phase 4 seed (04-WAVE0-GATES.md A5 $0.006/min). UAT revalidate 04-09.')
ON CONFLICT (model, provider, unit, valid_from) DO NOTHING;

-- ----------------------------------------------------------------------------
-- Embed: text-embedding-3-small — $0.02 / 1M tokens → $0.00000002 / token
--
-- Source: PROJECT.md / RESEARCH §State of the Art (Apr 2026 snapshot). Not
-- explicitly in 04-WAVE0-GATES.md A-table but required by the plan's truths
-- (Plan 04-03 must-have: >=4 price rows including embeddings). Operator
-- accepted via gate sign-off.
--
-- Second row (unit='embed_request') is the phantom-cost helper for D-B4:
--   phantom_cost_per_embed = avg_50_tokens × $0.00000002 = $0.00000100
-- Used when upstream is tier-0 local BGE-M3 (no real USD spend) to populate
-- usage_counters.cost_local_phantom_brl for the notional-OpenRouter report.
-- ----------------------------------------------------------------------------
INSERT INTO ai_gateway.prices (model, provider, unit, unit_cost_usd, notes)
VALUES
    ('text-embedding-3-small', 'openai', 'input_token',   0.00000002,
     'Phase 4 seed (PROJECT.md $0.02/1M Apr 2026). UAT revalidate 04-09.'),
    ('text-embedding-3-small', 'openai', 'embed_request', 0.00000100,
     'Phase 4 seed — phantom-cost helper = avg 50 tokens × $0.00000002 (D-B4 A6). UAT revalidate 04-09.')
ON CONFLICT (model, provider, unit, valid_from) DO NOTHING;

-- ============================================================================
-- FX seed — USD/BRL initial rate
--
-- 04-WAVE0-GATES.md §"FX rate inicial":
--   Par   = USD/BRL
--   Rate  = 5.10  (same as AI_GATEWAY_USD_BRL_RATE_DEFAULT config default)
--   Note  = operator adjusts via `gatewayctl prices set-fx` after deploy
-- ============================================================================
INSERT INTO ai_gateway.fx_rates (currency_pair, rate)
VALUES ('USD/BRL', 5.100000)
ON CONFLICT (currency_pair, valid_from) DO NOTHING;

-- ============================================================================
-- Per-tenant quota overrides (D-D Plumbing)
--
-- 04-WAVE0-GATES.md §A3 confirmed: operator KEPT all migration-0013 defaults:
--   daily_tokens=10_000_000, monthly_tokens=300_000_000,
--   daily_audio_min=600, monthly_audio_min=18_000,
--   daily_embeds=100_000, monthly_embeds=3_000_000,
--   rps=20, rpm=600
-- → No per-slug UPDATE required. This section is intentionally a no-op. Operator
--   refines per-tenant later via `gatewayctl tenant set-quota`.
-- ============================================================================
-- (intentional no-op; placeholder for future per-slug overrides)
-- Example template (commented — do NOT execute without gate doc amendment):
--   UPDATE ai_gateway.tenants SET rps_limit = 50, rpm_limit = 1500
--     WHERE slug = 'converseai';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Non-destructive reversal: mark Phase 4 seed rows expired (valid_to=now())
-- instead of DELETE, so historical billing_events can still join to prices
-- by (model, provider, unit, valid_from) for audit reports.
UPDATE ai_gateway.prices
SET valid_to = now()
WHERE notes LIKE 'Phase 4 seed%' AND valid_to IS NULL;

UPDATE ai_gateway.fx_rates
SET valid_to = now()
WHERE currency_pair = 'USD/BRL' AND valid_to IS NULL;
-- +goose StatementEnd
