-- name: InsertAuditLogContent :exec
-- Only called for data_class = 'normal' rows. Sensitive tenants leave
-- this table untouched (CONTEXT.md D-B2). Batch metadata inserts for
-- audit_log use pgx.CopyFrom directly (see gateway/internal/audit).
INSERT INTO ai_gateway.audit_log_content (request_id, ts, prompt, response)
VALUES ($1, $2, $3, $4)
ON CONFLICT (request_id, ts) DO NOTHING;
