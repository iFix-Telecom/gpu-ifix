-- name: InsertAuditLogContent :exec
-- Only called for data_class = 'normal' rows. Sensitive tenants leave
-- this table untouched (CONTEXT.md D-B2). Batch metadata inserts for
-- audit_log use pgx.CopyFrom directly (see gateway/internal/audit).
INSERT INTO ai_gateway.audit_log_content (request_id, ts, prompt, response)
VALUES ($1, $2, $3, $4)
ON CONFLICT (request_id, ts) DO NOTHING;

-- name: ListAuditStateChanges :many
-- Phase 7 — paginated read for the observability dashboard's state-change
-- feed (consumed by the admin handler in plan 07-03). Returns only rows
-- tagged with a non-NULL event_kind (FSM/state-change audit rows added by
-- migration 0020); ordinary request rows are excluded. ts DESC + LIMIT/OFFSET
-- keeps the page compact; the idx_audit_log_ts index (ts, tenant_id, route),
-- added by migration 0021, serves the ts-leading sort.
-- `reason` (migration 0022) carries the human-readable transition cause for
-- state-change rows — distinct from error_code (request error codes).
-- Phase 15 (OBS-10): the dashboard's /incidents history needs to filter by a
-- BRT date range and a free-text search. $1/$2 bound the [from, to) window
-- (exclusive end set by the handler); $3 is the single parameterized ILIKE
-- pattern — "%" matches everything (the sentinel for "no search"), otherwise
-- "%term%". The search arg is bound once and reused across route/reason/
-- error_code/event_kind so a hostile value can never be string-concatenated
-- into SQL (threat T-15-05). The metadata-only column list is PRESERVED —
-- audit_log_content (prompts/responses) is NEVER selected (threat T-07-09).
SELECT ts, request_id, tenant_id, route, method, upstream, status_code,
       latency_ms, error_code, event_kind, reason
FROM ai_gateway.audit_log
WHERE event_kind IS NOT NULL
  AND ts >= $1 AND ts < $2
  AND ($3 = '%' OR route ILIKE $3 OR reason ILIKE $3 OR error_code ILIKE $3 OR event_kind ILIKE $3)
ORDER BY ts DESC
LIMIT $4 OFFSET $5;

-- name: CountAuditStateChanges :one
-- Phase 15 (OBS-10): honest total for the /incidents pager — same WHERE
-- predicate as ListAuditStateChanges ($1/$2 range, $3 search) minus the
-- LIMIT/OFFSET so the dashboard derives canNext as (offset+limit < total)
-- instead of a heuristic. COUNT is bounded by the date range (threat T-15-08).
SELECT COUNT(*)::bigint AS total
FROM ai_gateway.audit_log
WHERE event_kind IS NOT NULL
  AND ts >= $1 AND ts < $2
  AND ($3 = '%' OR route ILIKE $3 OR reason ILIKE $3 OR error_code ILIKE $3 OR event_kind ILIKE $3);

-- name: TenantLatencyPercentiles :many
-- Phase 7 — per-tenant/route latency percentiles for the observability
-- dashboard's /admin/metrics JSON (consumed by the admin handler in plan
-- 07-03). Postgres computes percentile_cont natively over latency_ms, so
-- the dashboard gets true P50/P95/P99 with zero Prometheus-cardinality
-- cost (Pitfall 1 — no tenant label on a histogram). $1 is the window-start
-- timestamp; the handler passes NOW() - window (default 5 minutes). The
-- `ts >= $1` range scan + the `GROUP BY al.tenant_id, al.route` are served by
-- idx_audit_log_ts (ts, tenant_id, route), added by migration 0021 — the
-- (tenant_id, ts) index cannot serve a predicate with no tenant_id
-- equality. Bounded by the window + the ~6-tenant cardinality
-- (threat T-07-10, accept).
-- WR-10: LEFT JOIN ai_gateway.tenants so the row carries the human-readable
-- slug + name. The dashboard renders the name (falling back to slug, then
-- the raw UUID) instead of showing an operator a bare UUID during incident
-- triage. LEFT JOIN — not INNER — so an audit row whose tenant was deleted
-- still surfaces (slug/name come back NULL, the dashboard falls back to the
-- UUID). The join keys on tenants.id PK, a single index probe per group.
SELECT al.tenant_id,
       t.slug AS tenant_slug,
       t.name AS tenant_name,
       al.route,
       percentile_cont(0.50) WITHIN GROUP (ORDER BY al.latency_ms) AS p50,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY al.latency_ms) AS p95,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY al.latency_ms) AS p99,
       count(*) AS requests,
       (count(*) FILTER (WHERE al.status_code >= 500)::float / NULLIF(count(*), 0))::float8 AS error_rate
FROM ai_gateway.audit_log al
LEFT JOIN ai_gateway.tenants t ON t.id = al.tenant_id
WHERE al.ts >= $1
GROUP BY al.tenant_id, t.slug, t.name, al.route;
