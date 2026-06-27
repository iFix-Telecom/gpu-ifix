-- name: InsertBillingEvent :exec
-- Atomic insert into billing_events + UPSERT delta into usage_counters in a
-- single statement (no application-level txn needed). The CTE prevents the
-- replay double-count described in RESEARCH §Pitfall 7: when ON CONFLICT
-- (request_id, ts) DO NOTHING fires, the CTE returns zero rows, so the
-- usage_counters UPSERT also no-ops.
--
-- IMPORTANT: timezone idiom is `(now() AT TIME ZONE 'America/Sao_Paulo')::date`
-- (the alternative "CURRENT_DATE" + tz form documented in RESEARCH §Anti-Patterns
-- is invalid SQL; do NOT use it).
WITH inserted AS (
    INSERT INTO ai_gateway.billing_events
        (request_id, ts, tenant_id, api_key_id, route, upstream, model,
         tokens_in, tokens_out, audio_seconds, embeds_count,
         cost_local_brl, cost_local_phantom_brl, cost_external_brl,
         currency, source)
    VALUES
        ($1, $2, $3, $4, $5, $6, $7,
         $8, $9, $10, $11,
         0, $12, $13,
         'BRL', $14)
    ON CONFLICT (request_id, ts) DO NOTHING
    RETURNING tenant_id, tokens_in, tokens_out, audio_seconds, embeds_count,
              cost_local_phantom_brl, cost_external_brl
)
INSERT INTO ai_gateway.usage_counters
    (tenant_id, date, tokens_in, tokens_out, audio_seconds, embeds_count,
     cost_local_phantom_brl, cost_external_brl, requests_count)
SELECT tenant_id,
       (now() AT TIME ZONE 'America/Sao_Paulo')::date,
       tokens_in, tokens_out, audio_seconds::bigint, embeds_count,
       cost_local_phantom_brl::numeric(10,4), cost_external_brl::numeric(10,4), 1
FROM inserted
ON CONFLICT (tenant_id, date) DO UPDATE SET
    tokens_in              = ai_gateway.usage_counters.tokens_in + EXCLUDED.tokens_in,
    tokens_out             = ai_gateway.usage_counters.tokens_out + EXCLUDED.tokens_out,
    audio_seconds          = ai_gateway.usage_counters.audio_seconds + EXCLUDED.audio_seconds,
    embeds_count           = ai_gateway.usage_counters.embeds_count + EXCLUDED.embeds_count,
    cost_local_phantom_brl = ai_gateway.usage_counters.cost_local_phantom_brl + EXCLUDED.cost_local_phantom_brl,
    cost_external_brl      = ai_gateway.usage_counters.cost_external_brl + EXCLUDED.cost_external_brl,
    requests_count         = ai_gateway.usage_counters.requests_count + 1;

-- name: SumBillingEventsByDate :many
-- Authoritative aggregation for GET /admin/usage (D-D2 -- query billing_events,
-- NOT usage_counters cache). granularity='day' -- frontend can re-aggregate.
SELECT
    (ts AT TIME ZONE 'America/Sao_Paulo')::date AS date,
    COALESCE(SUM(tokens_in), 0)::bigint                AS tokens_in,
    COALESCE(SUM(tokens_out), 0)::bigint               AS tokens_out,
    COALESCE(SUM(audio_seconds), 0)::real              AS audio_seconds,
    COALESCE(SUM(embeds_count), 0)::bigint             AS embeds_count,
    COALESCE(SUM(cost_local_brl), 0)::numeric(20,6)    AS cost_local_brl,
    COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS cost_local_phantom_brl,
    COALESCE(SUM(cost_external_brl), 0)::numeric(20,6) AS cost_external_brl,
    COUNT(*)::bigint                                    AS requests_count
FROM ai_gateway.billing_events
WHERE tenant_id = $1
  AND ts >= $2
  AND ts <  $3
GROUP BY (ts AT TIME ZONE 'America/Sao_Paulo')::date
ORDER BY date;

-- name: SumBillingEventsRange :one
-- Aggregate over the entire range -- for the `summary` field.
SELECT
    COALESCE(SUM(tokens_in), 0)::bigint                AS tokens_in,
    COALESCE(SUM(tokens_out), 0)::bigint               AS tokens_out,
    COALESCE(SUM(audio_seconds), 0)::real              AS audio_seconds,
    COALESCE(SUM(embeds_count), 0)::bigint             AS embeds_count,
    COALESCE(SUM(cost_local_brl), 0)::numeric(20,6)    AS cost_local_brl,
    COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS cost_local_phantom_brl,
    COALESCE(SUM(cost_external_brl), 0)::numeric(20,6) AS cost_external_brl,
    COUNT(*)::bigint                                    AS requests_count
FROM ai_gateway.billing_events
WHERE tenant_id = $1
  AND ts >= $2
  AND ts <  $3;

-- name: SumPhantomAllTenantsByDate :many
-- GATEWAY-WIDE phantom series for GET /admin/economy (OBS-09). The deliberate
-- omission of `WHERE tenant_id = $1` is the OBS-09 blocker fix: operations.go
-- could not populate phantom_month_brl because no no-tenant-filter sum existed.
-- Drives the daily chart series (economia_liquida per day = phantom - vast).
-- Keep the `(ts AT TIME ZONE 'America/Sao_Paulo')::date` idiom verbatim --
-- CURRENT_DATE + tz is invalid SQL (RESEARCH Anti-Patterns).
SELECT
    (ts AT TIME ZONE 'America/Sao_Paulo')::date AS date,
    COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS phantom_brl
FROM ai_gateway.billing_events
WHERE ts >= $1
  AND ts <  $2
GROUP BY (ts AT TIME ZONE 'America/Sao_Paulo')::date
ORDER BY date;

-- name: SumBillingAllTenantsRange :one
-- GATEWAY-WIDE summary aggregate for GET /admin/economy (OBS-09). No tenant
-- filter -- sums phantom + real external spend across all tenants. The
-- INVARIANT (CONTEXT): cost_local_phantom_brl is written ONLY when a request
-- was served local/GPU, so "served local" iff cost_local_phantom_brl > 0.
SELECT
    COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS phantom_brl,
    COALESCE(SUM(cost_external_brl), 0)::numeric(20,6)      AS external_brl,
    COUNT(*) FILTER (WHERE cost_local_phantom_brl > 0)::bigint AS local_requests,
    COUNT(*)::bigint                                        AS total_requests
FROM ai_gateway.billing_events
WHERE ts >= $1
  AND ts <  $2;
