-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 7 (CR-02) — add a dedicated leading-ts index for the
-- TenantLatencyPercentiles query in the observability dashboard's
-- /admin/metrics handler.
--
-- The existing idx_audit_log_tenant_ts is (tenant_id, ts DESC): it cannot
-- serve a predicate with NO tenant_id equality and only a `ts >= $1`
-- range — Postgres would fall back to a per-partition sequential scan of
-- audit_log (the highest-volume table in the system). The dashboard polls
-- /admin/metrics every ~7s, so every poll would full-scan the current
-- partition.
--
-- (ts, tenant_id, route) leads with ts so the `ts >= $1` range is an
-- index range scan, and also carries tenant_id + route so the
-- `GROUP BY tenant_id, route` can be served from the index. Additive +
-- idempotent: CREATE INDEX IF NOT EXISTS is safe to re-run under the
-- AI_GATEWAY_MIGRATE_ON_BOOT flag. The ALTER targets the partitioned
-- parent only; PostgreSQL propagates the index to every existing and
-- future partition.
CREATE INDEX IF NOT EXISTS idx_audit_log_ts
    ON ai_gateway.audit_log (ts, tenant_id, route);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

DROP INDEX IF EXISTS ai_gateway.idx_audit_log_ts;
-- +goose StatementEnd
