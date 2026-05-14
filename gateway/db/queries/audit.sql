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
-- keeps the page compact; the idx_audit_log_tenant_ts index serves the sort.
SELECT ts, request_id, tenant_id, route, method, upstream, status_code,
       latency_ms, error_code, event_kind
FROM ai_gateway.audit_log
WHERE event_kind IS NOT NULL
ORDER BY ts DESC
LIMIT $1 OFFSET $2;

-- name: TenantLatencyPercentiles :many
-- Phase 7 — per-tenant/route latency percentiles for the observability
-- dashboard's /admin/metrics JSON (consumed by the admin handler in plan
-- 07-03). Postgres computes percentile_cont natively over latency_ms, so
-- the dashboard gets true P50/P95/P99 with zero Prometheus-cardinality
-- cost (Pitfall 1 — no tenant label on a histogram). $1 is the window-start
-- timestamp; the handler passes NOW() - window (default 5 minutes). The
-- ts >= $1 scan is served by idx_audit_log_tenant_ts and bounded by the
-- window + the ~6-tenant cardinality (threat T-07-10, accept).
SELECT tenant_id,
       route,
       percentile_cont(0.50) WITHIN GROUP (ORDER BY latency_ms) AS p50,
       percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) AS p95,
       percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms) AS p99,
       count(*) AS requests,
       (count(*) FILTER (WHERE status_code >= 500)::float / NULLIF(count(*), 0))::float8 AS error_rate
FROM ai_gateway.audit_log
WHERE ts >= $1
GROUP BY tenant_id, route;
