# SEED-008 — `prices` table missing entries for OpenRouter fallback provider; billing cost=0

**Planted:** 2026-05-28
**Discovered during:** Phase 11 11-06 corpus exerciser run (2026-05-28T19:30Z)
**Status:** seed — not yet promoted to phase
**Related:** [[SEED-004-openrouter-model-rewrite-per-upstream]] (model-rewrite gap — still open); [[SEED-007-tokenize-service-down-rest-07-not-enforced]] siblings from same exerciser run; `gateway/internal/proxy/interceptor_usage.go` (USAGE_INTERCEPTOR module)

## Problem

Every chat completion routed through the OpenRouter tier-1 fallback emits

```
{"level":"WARN","msg":"price missing — cost will be 0","module":"GATEWAY","module":"USAGE_INTERCEPTOR","model":"deepseek/deepseek-v4-flash-20260423","provider":"openrouter-fireworks","unit":"input_token"}
{"level":"WARN","msg":"price missing — cost will be 0","module":"GATEWAY","module":"USAGE_INTERCEPTOR","model":"deepseek/deepseek-v4-flash-20260423","provider":"openrouter-fireworks","unit":"output_token"}
```

`ai_gateway.prices` has no row matching `(model="deepseek/deepseek-v4-flash-20260423", provider="openrouter-fireworks", unit="input_token"|"output_token")`, so `UsageInterceptor.attribute` writes `cost_brl=0` into every audit_log envelope on that path. Tenants are not charged for tokens consumed; billing reports for the affected window are mute on real spend.

## Root cause (compound — interacts with SEED-004)

1. Per SEED-004 (still open), `openrouter_director.go` does **not** rewrite `body.model` from the gateway-side alias (`qwen`) to a canonical OpenRouter slug (`qwen/qwen3.5-27b`). OpenRouter then routes `qwen` to whatever its own default catch-all is — which today is `deepseek/deepseek-v4-flash-20260423` via Fireworks **OR** Novita, depending on availability that minute. The actual model returned by OpenRouter varies by request.
2. `prices` table seeds (`gateway/db/seeds/prices.sql` or migration equivalents) carry rows for the *expected* model+provider combos that operators registered. None of the entries match `deepseek/deepseek-v4-flash-*` × `openrouter-fireworks` because that combo was never the configured fallback shape — the operator expected `qwen/qwen3.5-27b` × the explicit Novita pin from `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=novita`.

## Empirical evidence (2026-05-28T19:30Z, exerciser corpus run)

```
$ ssh n8n-ia-vm 'docker logs ifix-ai-gateway --since 20m 2>&1 | grep -c "price missing"'
800+

$ ssh n8n-ia-vm "docker run --rm postgres:16-alpine psql '...bd_ai_gateway_prod...' \
  -c \"SELECT COUNT(*), AVG(cost_brl) FROM ai_gateway.audit_log \
     WHERE ts > NOW() - INTERVAL '15 minutes' AND route='/v1/chat/completions';\""
 count | avg
-------+-----
   400 | 0
```

400 chat completions, average cost 0 BRL. OpenRouter spend WAS incurred (the upstream replies show `usage.cost: 0.00000252-0.00006552 USD`), but the gateway's per-tenant attribution is zero.

## Impact

1. **Billing accuracy = 0**: tenant invoices generated from `audit_log.cost_brl` underreport reality by 100% on the chat completion path. PRD-04 LGPD signoff includes a billing-transparency clause that this violates.
2. **Quota enforcement degraded**: `quota.QuotaMiddleware` enforces monthly BRL budgets via `tenants.monthly_budget_brl`; if cost_brl is always 0, no tenant ever hits the cap → free uncontrolled OpenRouter spend.
3. **SLO dashboard wrong tier-1 attribution**: D-04 SLO panels show "tier-1 cheaper than tier-0" because tier-1 reports $0 — invisible cost shift.

## Scope of Fix

### Option A — Seed prices for the actual fallback model (immediate)

1. Identify the canonical billing-side model name for OpenRouter Fireworks DeepSeek-V4-flash. Look up OpenRouter pricing page; record input + output token rates in USD.
2. Convert to BRL using the same `fx_pairs` table the interceptor reads (USD → BRL daily rate).
3. INSERT rows into `ai_gateway.prices` for `(deepseek/deepseek-v4-flash-20260423, openrouter-fireworks, input_token)` and `(..., output_token)`. Repeat for `openrouter-novita` if requests sometimes route there too.
4. Verify a chat request emits non-zero `cost_brl` in `bd_ai_gateway_prod.ai_gateway.audit_log`.

Bandaid; does NOT fix the underlying SEED-004 model-rewrite gap. If OpenRouter changes its fallback model next month, the same problem recurs.

### Option B — Promote SEED-004 and pin the model end-to-end (correct)

1. Land SEED-004 (`UPSTREAM_LLM_OPENROUTER_MODEL` actually wired into `openrouter_director.go`'s body rewrite).
2. Pin `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=novita` (already set per CLAUDE.md). Together, this guarantees the model+provider tuple is deterministic.
3. Seed `prices` for that ONE deterministic tuple only.
4. Add a Prometheus alert `gateway_price_missing_total > 0` so any future drift fires within 1 minute.

### Recommendation

**Option B.** Option A is operationally fragile — OpenRouter's catch-all routing isn't a contract, and chasing it with `prices` rows is whack-a-mole. SEED-004 has been documented since 2026-05-24 and this is the second downstream effect it has produced (the first was the SEED-007 sibling cost-surprise vector). Promote SEED-004 + ship Option B + close both seeds together.

## Test Plan (when promoted)

- Pre-deploy: assert no `price missing` warnings in dev gateway logs over a 5-min sustained chat workload.
- Post-deploy: assert `bd_ai_gateway_prod.ai_gateway.audit_log` rows for `/v1/chat/completions` have `cost_brl > 0` matching the OpenRouter-reported `usage.cost` × FX rate (±2%).
- Regression: kill the `prices` row for the canonical tuple, assert the Prometheus alert fires within 60s.

## Files

- `gateway/internal/proxy/interceptor_usage.go` (USAGE_INTERCEPTOR module — emit point of the warn)
- `gateway/internal/proxy/openrouter_director.go` (SEED-004 fix target — body.model rewrite)
- `gateway/db/seeds/prices.sql` or migration that seeds prices (verify which file is the source of truth)
- `gateway/internal/obs/` (new `PriceMissingTotal` counter + alert if promoting Option B)
- `~/.claude/CLAUDE.md` notes on `UPSTREAM_LLM_OPENROUTER_*` env vars (sanity-check that env is set on prod stack `.env`)
