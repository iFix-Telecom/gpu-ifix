-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 7 (CR-03) — add a dedicated nullable `reason` column to audit_log
-- so state-change rows (event_kind IS NOT NULL) can carry a
-- human-readable transition cause WITHOUT overloading error_code.
--
-- Before this migration the emergency FSM stuffed its transition reason
-- into error_code (audit.Event.ErrorCode). error_code is also populated
-- by genuine 5xx request rows, so the column had two incompatible
-- meanings and an operator could not distinguish "a request failed with
-- this error" from "the FSM transitioned because of this reason".
--
-- Additive + idempotent: ADD COLUMN IF NOT EXISTS leaves every existing
-- row untouched (reason = NULL) and is safe to re-run under the
-- AI_GATEWAY_MIGRATE_ON_BOOT flag. The ALTER touches the partitioned
-- parent only; PostgreSQL propagates the column to every existing and
-- future partition.
ALTER TABLE ai_gateway.audit_log
    ADD COLUMN IF NOT EXISTS reason TEXT;

COMMENT ON COLUMN ai_gateway.audit_log.reason IS
    'Phase 7: human-readable cause of a state-change audit row (e.g. the emergency FSM transition reason). NULL for ordinary request rows. Distinct from error_code, which carries request error codes.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

ALTER TABLE ai_gateway.audit_log
    DROP COLUMN IF EXISTS reason;
-- +goose StatementEnd
