-- quick-261007-t9f — 01-preview.sql (READ-ONLY)
--
-- Preview of the historical "Economia" (cost_local_phantom_brl) correction.
-- Decision (Pedro, 2026-10-07): phantom = savings = only traffic served by our
-- own infra. Paid external traffic must have phantom = 0; self-hosted
-- embed/rerank (priced only after `gatewayctl prices set`) get phantom
-- tokens_in * 1e-8 USD * fx.
--
-- Self-hosted predicate = exact mirror of Go isSelfHostedUpstream
-- (gateway/internal/proxy/interceptor_usage.go). starts_with, not LIKE: `_` is
-- a LIKE wildcard.
--
-- Run (from ops-claude, DSN never printed):
--   ssh worker-vm 'C=$(docker ps -q -f name=ai-gateway-prod_gateway|head -1); \
--     DSN=$(docker inspect $C --format "{{range .Config.Env}}{{println .}}{{end}}" | grep ^AI_GATEWAY_PG_DSN= | cut -d= -f2-); \
--     docker run --rm -i --network host -e DSN="$DSN" postgres:16-alpine sh -c "psql \"\$DSN\" -v ON_ERROR_STOP=1 -f -"' < 01-preview.sql
\pset pager off
\set ON_ERROR_STOP on

-- (iv) column types (numeric(10,6) on billing_events, numeric(10,4) on usage_counters)
\d ai_gateway.billing_events
\d ai_gateway.usage_counters

-- current FX (USD/BRL)
SELECT rate AS fx_usd_brl, valid_from
FROM ai_gateway.fx_rates
WHERE currency_pair = 'USD/BRL' AND valid_to IS NULL
ORDER BY valid_from DESC LIMIT 1;

-- reference prices relevant to the fix (expected after runbook step 2)
SELECT model, provider, unit, unit_cost_usd, valid_from
FROM ai_gateway.prices
WHERE valid_to IS NULL AND provider = 'openrouter-fireworks'
  AND model IN ('bge-m3', 'bge-reranker-v2-m3', 'tts-1')
ORDER BY model, unit;

-- retention window actually present in billing_events
SELECT min(ts) AS oldest_event, max(ts) AS newest_event, count(*) AS events
FROM ai_gateway.billing_events;

-- (i) NON-self-hosted upstreams carrying phantom (to be zeroed)
SELECT upstream, count(*) AS rows, sum(cost_local_phantom_brl) AS phantom_brl,
       sum(cost_external_brl) AS external_brl, min(ts) AS first_ts, max(ts) AS last_ts
FROM ai_gateway.billing_events
WHERE NOT (starts_with(upstream,'local-') OR starts_with(upstream,'emergency_pod_')
           OR upstream IN ('rerank-gpu','rerank-cpu','embed-gpu','kokoro-tts','voice-api-piper'))
  AND cost_local_phantom_brl <> 0
GROUP BY upstream ORDER BY phantom_brl DESC;

-- (ii) self-hosted embed/rerank with phantom = 0 and tokens (to be priced at 1e-8 USD/token)
WITH fx AS (
  SELECT rate FROM ai_gateway.fx_rates
  WHERE currency_pair = 'USD/BRL' AND valid_to IS NULL
  ORDER BY valid_from DESC LIMIT 1
)
SELECT b.upstream, b.model, count(*) AS rows, sum(b.tokens_in) AS tokens_in,
       round(sum(round(b.tokens_in * 0.00000001 * fx.rate, 6)), 6) AS new_phantom_brl,
       min(b.ts) AS first_ts, max(b.ts) AS last_ts
FROM ai_gateway.billing_events b CROSS JOIN fx
WHERE b.upstream IN ('local-embed','embed-gpu','rerank-gpu')
  AND b.cost_local_phantom_brl = 0 AND b.tokens_in > 0
GROUP BY b.upstream, b.model ORDER BY tokens_in DESC;

-- informational: self-hosted rows that carry cost_external (should be 0 by the Go gate)
SELECT upstream, count(*) AS rows, sum(cost_external_brl) AS external_brl
FROM ai_gateway.billing_events
WHERE (starts_with(upstream,'local-') OR starts_with(upstream,'emergency_pod_')
       OR upstream IN ('rerank-gpu','rerank-cpu','embed-gpu','kokoro-tts','voice-api-piper'))
  AND cost_external_brl <> 0
GROUP BY upstream;

-- (iii) Economia per BRT day, last 30 days: before vs after (simulated)
WITH fx AS (
  SELECT rate FROM ai_gateway.fx_rates
  WHERE currency_pair = 'USD/BRL' AND valid_to IS NULL
  ORDER BY valid_from DESC LIMIT 1
), ev AS (
  SELECT (b.ts AT TIME ZONE 'America/Sao_Paulo')::date AS brt_date,
         b.cost_local_phantom_brl AS old_phantom,
         CASE
           WHEN NOT (starts_with(b.upstream,'local-') OR starts_with(b.upstream,'emergency_pod_')
                     OR b.upstream IN ('rerank-gpu','rerank-cpu','embed-gpu','kokoro-tts','voice-api-piper'))
             THEN 0::numeric
           WHEN b.upstream IN ('local-embed','embed-gpu','rerank-gpu')
                AND b.cost_local_phantom_brl = 0 AND b.tokens_in > 0
             THEN round(b.tokens_in * 0.00000001 * fx.rate, 6)
           ELSE b.cost_local_phantom_brl
         END AS new_phantom
  FROM ai_gateway.billing_events b CROSS JOIN fx
  WHERE b.ts >= now() - interval '30 days'
)
SELECT brt_date, round(sum(old_phantom), 4) AS economia_antes_brl,
       round(sum(new_phantom), 4) AS economia_depois_brl
FROM ev GROUP BY brt_date ORDER BY brt_date;

-- totals (whole retention) before vs after
WITH fx AS (
  SELECT rate FROM ai_gateway.fx_rates
  WHERE currency_pair = 'USD/BRL' AND valid_to IS NULL
  ORDER BY valid_from DESC LIMIT 1
)
SELECT round(sum(b.cost_local_phantom_brl), 4) AS economia_antes_brl,
       round(sum(CASE
         WHEN NOT (starts_with(b.upstream,'local-') OR starts_with(b.upstream,'emergency_pod_')
                   OR b.upstream IN ('rerank-gpu','rerank-cpu','embed-gpu','kokoro-tts','voice-api-piper'))
           THEN 0::numeric
         WHEN b.upstream IN ('local-embed','embed-gpu','rerank-gpu')
              AND b.cost_local_phantom_brl = 0 AND b.tokens_in > 0
           THEN round(b.tokens_in * 0.00000001 * fx.rate, 6)
         ELSE b.cost_local_phantom_brl END), 4) AS economia_depois_brl
FROM ai_gateway.billing_events b CROSS JOIN fx;

-- usage_counters phantom total (cache table the fix also adjusts)
SELECT round(sum(cost_local_phantom_brl), 4) AS usage_counters_phantom_brl, min(date), max(date)
FROM ai_gateway.usage_counters;
