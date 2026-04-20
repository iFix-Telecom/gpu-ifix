-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

CREATE OR REPLACE FUNCTION ai_gateway.notify_upstreams_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('upstreams_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS upstreams_change_notify ON ai_gateway.upstreams;
DROP TRIGGER IF EXISTS upstreams_insert_delete_notify ON ai_gateway.upstreams;
DROP TRIGGER IF EXISTS upstreams_update_notify ON ai_gateway.upstreams;

-- Postgres restriction: an INSERT trigger's WHEN clause cannot reference
-- OLD; a DELETE trigger's WHEN clause cannot reference NEW. Splitting
-- into two triggers is the canonical workaround. Both call the same
-- notify_upstreams_changed() function.
--
-- INSERT/DELETE: always fire (config-table writes are operator actions).
CREATE TRIGGER upstreams_insert_delete_notify
AFTER INSERT OR DELETE ON ai_gateway.upstreams
FOR EACH ROW
WHEN (pg_trigger_depth() = 0)
EXECUTE FUNCTION ai_gateway.notify_upstreams_changed();

-- UPDATE: filter out pure probe writebacks (Pitfall 7). The trigger
-- fires only when a config column actually changed. probe writebacks
-- (last_probe_at, last_probe_ms, last_probe_status, last_probe_error,
-- updated_at) leave the listed config columns untouched and therefore
-- skip the notify path entirely.
CREATE TRIGGER upstreams_update_notify
AFTER UPDATE ON ai_gateway.upstreams
FOR EACH ROW
WHEN (
    pg_trigger_depth() = 0 AND (
        NEW.name IS DISTINCT FROM OLD.name
        OR NEW.role IS DISTINCT FROM OLD.role
        OR NEW.tier IS DISTINCT FROM OLD.tier
        OR NEW.url_env IS DISTINCT FROM OLD.url_env
        OR NEW.auth_bearer_env IS DISTINCT FROM OLD.auth_bearer_env
        OR NEW.enabled IS DISTINCT FROM OLD.enabled
        OR NEW.weight IS DISTINCT FROM OLD.weight
        OR NEW.circuit_config IS DISTINCT FROM OLD.circuit_config
    )
)
EXECUTE FUNCTION ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS upstreams_update_notify ON ai_gateway.upstreams;
DROP TRIGGER IF EXISTS upstreams_insert_delete_notify ON ai_gateway.upstreams;
DROP TRIGGER IF EXISTS upstreams_change_notify ON ai_gateway.upstreams;
DROP FUNCTION IF EXISTS ai_gateway.notify_upstreams_changed();
-- +goose StatementEnd
