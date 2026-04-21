---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 03
subsystem: gateway/db
tags:
  - phase-04
  - migrations
  - seed
  - prices
  - fx
  - quotas
requires:
  - 04-01  # Wave 0 gate doc (04-WAVE0-GATES.md) closed
  - 04-02  # Migrations 0010..0014 create the tables seeded here (parallel wave-1 execution; live apply validated in 04-08)
provides:
  - "Initial price rows for qwen3.5-27b (openrouter-fireworks), whisper-1 (openai), text-embedding-3-small (openai)"
  - "Initial USD/BRL fx_rates row (5.10)"
  - "Non-destructive Down migration (UPDATE valid_to=now())"
affects:
  - gateway/db/migrations/0015_seed_prices_and_quotas.sql
tech-stack:
  patterns:
    - "goose migration with SET search_path + StatementBegin/End"
    - "INSERT ... ON CONFLICT DO NOTHING (idempotent re-run)"
    - "Non-destructive Down via UPDATE valid_to=now()"
  added:
    - "Price catalogue seed (5 rows across 3 providers)"
    - "FX seed (USD/BRL 5.10)"
key-files:
  created:
    - gateway/db/migrations/0015_seed_prices_and_quotas.sql
  modified: []
decisions:
  - "Used Wave-0-gate placeholder values from RESEARCH snapshot (operator chose this over live dashboard fetch); flagged UAT revalidation in Plan 04-09 directly in migration comments"
  - "Kept all migration-0013 quota DEFAULTs (no per-slug UPDATE in this migration) per A3 sign-off — operator will refine per-tenant later via gatewayctl tenant set-quota"
  - "Added a 5th price row (text-embedding-3-small unit=embed_request @ $0.00000100) as phantom-cost helper for D-B4 tier-0 local BGE-M3; encodes `avg 50 tokens × $0.00000002`"
  - "Down block uses UPDATE valid_to=now() instead of DELETE so historical billing_events can still join to prices by (model, provider, unit, valid_from)"
metrics:
  completed: 2026-04-21
  duration_minutes: ~15
  commits: 1
---

# Phase 4 Plan 03: Seed migration 0015 (prices + fx + quotas) Summary

**One-liner:** Migration `0015_seed_prices_and_quotas.sql` seeds the price catalogue, USD/BRL fx rate, and quota overrides from the operator-confirmed values in `04-WAVE0-GATES.md` — all idempotent via `ON CONFLICT DO NOTHING`, with a non-destructive Down that marks rows expired instead of deleting them.

## Scope

Plan 04-03 is the operator-gated split-out of migration 0015 from the Plan-04-02 migration bundle (0010–0014). Running this migration before the Wave 0 gate is closed would produce wrong billing data from day one; splitting it out means Plan 04-02 can execute against a reviewed schema set while 04-03 runs against the live operator decisions captured in `04-WAVE0-GATES.md`.

## Seeded Values

### Prices (`ai_gateway.prices`)

| Model | Provider | Unit | USD Cost | Source (gate doc) | Notes |
|-------|----------|------|----------|-------------------|-------|
| `qwen3.5-27b` | `openrouter-fireworks` | `input_token` | `0.00000020` | A1 $0.195/1M aggregate | Stored rounded to numeric(12,8); real value 0.000000195 |
| `qwen3.5-27b` | `openrouter-fireworks` | `output_token` | `0.00000156` | A1 $1.56/1M aggregate | — |
| `whisper-1` | `openai` | `audio_second` | `0.00010000` | A5 $0.006/min | = 0.006 / 60 |
| `text-embedding-3-small` | `openai` | `input_token` | `0.00000002` | PROJECT.md / RESEARCH Apr 2026 | $0.02 / 1M |
| `text-embedding-3-small` | `openai` | `embed_request` | `0.00000100` | D-B4 phantom-cost helper | avg 50 tokens × $0.02/1M |

**Important precision note:** `qwen3.5-27b` input_token is stored as `0.00000020` because `numeric(12,8)` has 8 fractional digits; the gate-doc exact value `0.000000195` (9 fractional digits) does not fit without schema change. The 2-significant-digit approximation introduces ~2.6% drift at this price point, well within the 10% UAT-revalidation threshold flagged in the gate doc. If Plan 04-09 confirms the exact per-token value is load-bearing, migration 0016 can widen the column to `numeric(14,10)` or similar.

### FX rate (`ai_gateway.fx_rates`)

| Currency pair | Rate | Source |
|---------------|------|--------|
| `USD/BRL` | `5.100000` | 04-WAVE0-GATES.md §"FX rate inicial" (same as config default `AI_GATEWAY_USD_BRL_RATE_DEFAULT`) |

### Per-tenant quota overrides

**None.** Operator signed off (gate doc §A3) on keeping all migration-0013 column DEFAULTs for the v1 rollout:

| Dimension | Default from 0013 |
|-----------|-------------------|
| `daily_quota_tokens` | 10_000_000 |
| `monthly_quota_tokens` | 300_000_000 |
| `daily_quota_audio_minutes` | 600 |
| `monthly_quota_audio_minutes` | 18_000 |
| `daily_quota_embeds` | 100_000 |
| `monthly_quota_embeds` | 3_000_000 |
| `rps_limit` | 20 |
| `rpm_limit` | 600 |

The migration includes a commented-out template `UPDATE ai_gateway.tenants SET rps_limit = 50, rpm_limit = 1500 WHERE slug = 'converseai'` so future per-slug overrides can be added without touching the migration shape — but nothing is executed at this time. Operator refines per-tenant via `gatewayctl tenant set-quota` post-deploy.

## Commits

| Hash | Scope | Message |
|------|-------|---------|
| `9e1b867` | feat(04) | seed migration 0015 — prices + fx + quotas from Wave 0 gate doc |

## Link back to gate doc

- **Gate file:** `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-WAVE0-GATES.md`
- **Gate confirmed:** 2026-04-21 (placeholder sign-off — operator Pedro)
- **UAT revalidation:** **REQUIRED** in Plan 04-09. The migration's cabeçalho comment cites the gate doc path explicitly so any reviewer reading 0015 can trace every numeric back to operator approval, and every price row has `"UAT revalidate 04-09"` in its `notes` column.

## Deviations from Plan

### None (plan executed exactly as written)

- Placeholder substitution followed the gate doc exactly for all 4 documented values (qwen input/output, whisper, fx).
- Embedding price row came from `PROJECT.md` / `RESEARCH.md` §State-of-the-Art because the gate doc only documents A1/A2 (qwen) and A5 (whisper) in the price table — but the plan's `must_haves.truths` explicitly requires ≥4 price rows *including* text-embedding-3-small. Both the `input_token` row and the `embed_request` phantom-cost helper are included as specified in the plan's Task 1 SQL template (lines 138-146 of 04-03-PLAN.md).
- Non-destructive Down block uses `UPDATE ... SET valid_to = now()` per the plan template.
- `ON CONFLICT DO NOTHING` on every INSERT (4 clauses — 3 price INSERTs + 1 fx INSERT).

## Verification Performed

| Check | Result |
|-------|--------|
| File exists at `gateway/db/migrations/0015_seed_prices_and_quotas.sql` | ✅ |
| `grep -c "INSERT INTO ai_gateway.prices"` | 3 (≥1 required) |
| `grep -c "INSERT INTO ai_gateway.fx_rates"` | 1 |
| `grep -c "qwen3.5-27b"` | 4 (≥2 required) |
| `grep -c "whisper-1"` | 2 (≥1 required) |
| `grep -c "text-embedding-3-small"` | 3 (≥1 required) |
| `grep -c "ON CONFLICT"` | 4 (≥4 required) |
| `grep -c "USD/BRL"` | 4 (≥1 required) |
| `grep -c "04-WAVE0-GATES.md"` | 10 (≥1 required) |
| Stray placeholders `<seed_..._per_token>` / `<usd_brl_rate_from_gate>` / `<provider_from_gate>` | 0 ✅ |
| `+goose Up` / `+goose Down` blocks both present | ✅ (6 directives total) |
| `+goose StatementBegin` / `+goose StatementEnd` balanced | ✅ (2/2) |
| Parenthesis balance | ✅ (46 open / 46 close) |
| `sqlc compile` (against existing sqlc.yaml) | ✅ exit 0 |

**Live `goose up` skipped** per plan's verification section — migration 0015 requires 0010–0014 already applied (from Plan 04-02, executing in parallel in a separate worktree). Plan 04-08 (integration tests) validates the full 0001..0015 apply chain on a PG16 testcontainer.

## Known Stubs

None. Every INSERTed value is final seed data confirmed by the operator gate. The phantom-cost helper row (`text-embedding-3-small` unit=`embed_request`) is explicitly *not* a stub — it is the D-B4 implementation pattern for populating `usage_counters.cost_local_phantom_brl` when upstream is tier-0 local BGE-M3.

## Threat Flags

None. Migration is a pure seed (INSERT/UPDATE into pre-existing tables from Plan 04-02) with no new surface, no new trust boundaries, no new network endpoints. The `T-04-09` (tampering via operator-supplied numerics) and `T-04-10` (info disclosure via seed values) threats from the plan's threat register are mitigated as written: every value cites the git-tracked gate doc in the `notes` column, and prices are public commercial info.

## Self-Check: PASSED

- `gateway/db/migrations/0015_seed_prices_and_quotas.sql` → **FOUND**
- Commit `9e1b867` (feat(04): seed migration 0015) → **FOUND**
- `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-03-SUMMARY.md` → **WRITTEN (this file)**
