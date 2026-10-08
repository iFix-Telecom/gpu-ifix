-- quick-261007-t9f — 02-fix.sql (WRITES TO PROD — orchestrator only, AFTER deploy + prices)
--
-- Historical correction of "Economia" (cost_local_phantom_brl):
--   A. non-self-hosted rows (openrouter-chat, gemini-stt, groq-whisper,
--      openai-whisper, unknown, ...) with phantom <> 0  -> phantom = 0
--   B. self-hosted embed/rerank rows (local-embed, embed-gpu, rerank-gpu) with
--      phantom = 0 and tokens_in > 0 -> phantom = round(tokens_in * 1e-8 * fx, 6)
--      (1e-8 USD/token = reference price of bge-m3 / bge-reranker-v2-m3 seeded
--      in runbook step 2; fx = current USD/BRL row)
--
-- Self-hosted predicate = exact mirror of Go isSelfHostedUpstream
-- (gateway/internal/proxy/interceptor_usage.go). starts_with, not LIKE.
--
-- DRY-RUN: replace the final COMMIT with ROLLBACK (the \copy snapshots are
-- still written, which is harmless).
--
-- IDEMPOTENT: after one successful run both predicates (A and B) select zero
-- rows, so a second run changes nothing.
--
-- DELTA-BASED usage_counters: the cache table is adjusted by the per
-- (tenant, BRT date) delta of the corrected rows instead of being rebuilt, so
-- it does not depend on billing_events retention covering the whole history of
-- usage_counters (billing_events starts 2026-07-04, usage_counters 2026-07-01).
-- The delta reproduces the insert CTE rounding (each event is cast to
-- numeric(10,4) before being added — queries/billing.sql), so
-- delta = sum(round(new,4) - round(old,4)). GREATEST(0, ...) guards drift.
--
-- KNOWN LIMITATION: usage_counters.date is the BRT date of the FLUSH
-- (now() at insert), brt_date here is the BRT date of the event ts. They
-- differ only for requests in flight across BRT midnight.
-- HIPÓTESE: negligible. Check: compare usage_counters vs billing_events per day
-- after the fix (query at the bottom).
--
-- RUN with a host volume so the rollback CSVs survive the --rm container.
-- Use a FRESH directory per run: a 2nd (idempotent, no-op) run in the same
-- directory would overwrite the rollback CSVs with empty files.
--   ssh worker-vm 'D=/root/billing-fix-261007-$(date +%Y%m%dT%H%M%S); mkdir -p $D && echo $D && \
--     C=$(docker ps -q -f name=ai-gateway-prod_gateway|head -1); \
--     DSN=$(docker inspect $C --format "{{range .Config.Env}}{{println .}}{{end}}" | grep ^AI_GATEWAY_PG_DSN= | cut -d= -f2-); \
--     docker run --rm -i --network host -v $D:/work -w /work -e DSN="$DSN" postgres:16-alpine \
--       sh -c "psql \"\$DSN\" -v ON_ERROR_STOP=1 -f -"' < 02-fix.sql
--   then: scp 'worker-vm:<D printed above>/*.csv' ~/billing-fix-261007/
--
-- ROLLBACK (from the CSVs):
--   billing_events:  SET cost_local_phantom_brl = old_phantom  WHERE (request_id, ts) match
--   usage_counters:  SET cost_local_phantom_brl = cost_local_phantom_brl (CSV value) WHERE (tenant_id, date) match
\pset pager off
\set ON_ERROR_STOP on

BEGIN;

CREATE TEMP TABLE fx ON COMMIT DROP AS
SELECT rate FROM ai_gateway.fx_rates
WHERE currency_pair = 'USD/BRL' AND valid_to IS NULL
ORDER BY valid_from DESC LIMIT 1;

SELECT rate AS fx_usd_brl_used FROM fx;

CREATE TEMP TABLE fix ON COMMIT DROP AS
SELECT b.request_id,
       b.ts,
       b.tenant_id,
       b.upstream,
       (b.ts AT TIME ZONE 'America/Sao_Paulo')::date AS brt_date,
       b.cost_local_phantom_brl AS old_phantom,
       CASE
         WHEN NOT (starts_with(b.upstream,'local-') OR starts_with(b.upstream,'emergency_pod_')
                   OR b.upstream IN ('rerank-gpu','rerank-cpu','embed-gpu','kokoro-tts','voice-api-piper'))
           THEN 0::numeric(10,6)
         ELSE round(b.tokens_in * 0.00000001 * fx.rate, 6)::numeric(10,6)
       END AS new_phantom
FROM ai_gateway.billing_events b CROSS JOIN fx
WHERE (NOT (starts_with(b.upstream,'local-') OR starts_with(b.upstream,'emergency_pod_')
            OR b.upstream IN ('rerank-gpu','rerank-cpu','embed-gpu','kokoro-tts','voice-api-piper'))
       AND b.cost_local_phantom_brl <> 0)
   OR (b.upstream IN ('local-embed','embed-gpu','rerank-gpu')
       AND b.cost_local_phantom_brl = 0 AND b.tokens_in > 0);

SELECT upstream, count(*) AS rows, sum(old_phantom) AS old_phantom_brl, sum(new_phantom) AS new_phantom_brl
FROM fix GROUP BY upstream ORDER BY upstream;

-- snapshot 1: billing_events rows about to change
\copy (SELECT request_id, ts, upstream, old_phantom, new_phantom FROM fix ORDER BY ts) TO 'billing_events_phantom_rollback_20261007.csv' CSV HEADER

CREATE TEMP TABLE uc_delta ON COMMIT DROP AS
SELECT tenant_id, brt_date,
       sum(round(new_phantom, 4) - round(old_phantom, 4))::numeric(12,4) AS delta
FROM fix
GROUP BY tenant_id, brt_date;

-- snapshot 2: usage_counters rows about to change
\copy (SELECT u.tenant_id, u.date, u.cost_local_phantom_brl FROM ai_gateway.usage_counters u JOIN uc_delta d ON d.tenant_id = u.tenant_id AND d.brt_date = u.date ORDER BY u.date, u.tenant_id) TO 'usage_counters_phantom_rollback_20261007.csv' CSV HEADER

UPDATE ai_gateway.billing_events b
SET cost_local_phantom_brl = f.new_phantom
FROM fix f
WHERE b.request_id = f.request_id AND b.ts = f.ts;

UPDATE ai_gateway.usage_counters u
SET cost_local_phantom_brl = GREATEST(0, u.cost_local_phantom_brl + d.delta)
FROM uc_delta d
WHERE u.tenant_id = d.tenant_id AND u.date = d.brt_date;

-- checks (must hold before COMMIT)
-- 1. no non-self-hosted row keeps phantom (expect 0)
SELECT count(*) AS non_selfhosted_with_phantom
FROM ai_gateway.billing_events
WHERE NOT (starts_with(upstream,'local-') OR starts_with(upstream,'emergency_pod_')
           OR upstream IN ('rerank-gpu','rerank-cpu','embed-gpu','kokoro-tts','voice-api-piper'))
  AND cost_local_phantom_brl <> 0;

-- 2. no self-hosted embed/rerank row with tokens left at phantom 0 (expect 0)
SELECT count(*) AS selfhosted_embed_rerank_unpriced
FROM ai_gateway.billing_events
WHERE upstream IN ('local-embed','embed-gpu','rerank-gpu')
  AND cost_local_phantom_brl = 0 AND tokens_in > 0;

-- 3. phantom per upstream after the fix
SELECT upstream, count(*) AS rows, round(sum(cost_local_phantom_brl), 4) AS phantom_brl
FROM ai_gateway.billing_events
GROUP BY upstream HAVING sum(cost_local_phantom_brl) <> 0
ORDER BY phantom_brl DESC;

-- 4. totals after the fix (billing_events vs usage_counters)
SELECT (SELECT round(sum(cost_local_phantom_brl), 4) FROM ai_gateway.billing_events) AS billing_events_phantom_brl,
       (SELECT round(sum(cost_local_phantom_brl), 4) FROM ai_gateway.usage_counters) AS usage_counters_phantom_brl;

-- 5. per-day drift usage_counters vs billing_events (last 30 BRT days; see KNOWN LIMITATION)
SELECT d.date,
       round(sum(u.cost_local_phantom_brl), 4) AS uc_phantom,
       (SELECT round(sum(b.cost_local_phantom_brl::numeric(10,4)), 4)
          FROM ai_gateway.billing_events b
         WHERE (b.ts AT TIME ZONE 'America/Sao_Paulo')::date = d.date) AS be_phantom_rounded
FROM (SELECT DISTINCT date FROM ai_gateway.usage_counters WHERE date >= current_date - 30) d
JOIN ai_gateway.usage_counters u ON u.date = d.date
GROUP BY d.date ORDER BY d.date;

COMMIT;
