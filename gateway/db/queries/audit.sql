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
SELECT ts, request_id, tenant_id, route, method, upstream, status_code,
       latency_ms, error_code, event_kind, reason
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
