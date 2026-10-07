-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick-261007-sjo — audit_log.cost_brl never carried a value: the writer
-- always COPYed NULL ("Phase 4 populates" was never done) and no query reads
-- it. Per-request cost lives in billing_events. Decision Pedro 2026-10-07.
-- Deploy order: gateway image that no longer COPYs the column FIRST, then
-- this migration (an old image would fail COPY against the dropped column).
ALTER TABLE ai_gateway.audit_log DROP COLUMN IF EXISTS cost_brl;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;
ALTER TABLE ai_gateway.audit_log ADD COLUMN IF NOT EXISTS cost_brl NUMERIC(10, 4);
-- +goose StatementEnd
