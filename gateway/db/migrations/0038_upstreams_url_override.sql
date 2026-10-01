-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Quick 260930-uru: URL efetiva do upstream sobrescrevível em runtime.
-- O flip diário do pod 3060 (ops/vast-3060/unified3060.py) passava pelas
-- envs do stack Portainer 38, o que recriava a task do gateway. Com esta
-- coluna o loader prefere url_override (quando válido) a os.Getenv(url_env)
-- e o NOTIFY abaixo recarrega o snapshot sem restart.
-- ADD COLUMN NULL é compatível com o binário antigo: todos os SELECTs de
-- db/queries/upstreams.sql listam colunas explícitas (nenhum SELECT *).
ALTER TABLE ai_gateway.upstreams ADD COLUMN IF NOT EXISTS url_override TEXT NULL;

COMMENT ON COLUMN ai_gateway.upstreams.url_override IS 'URL efetiva sobrescrita em runtime (hot-reload, sem restart). Quando NULL usa os.Getenv(url_env). Setada por gatewayctl upstreams update --url; limpa com --clear-url.';

-- Recria o trigger de UPDATE do 0009 acrescentando url_override (hot-reload)
-- e tier_priority (lacuna antiga: mudança de prioridade não recarregava).
-- Função notify_upstreams_changed() e trigger INSERT/DELETE inalterados.
DROP TRIGGER IF EXISTS upstreams_update_notify ON ai_gateway.upstreams;

CREATE TRIGGER upstreams_update_notify
AFTER UPDATE ON ai_gateway.upstreams
FOR EACH ROW
WHEN (
    pg_trigger_depth() = 0 AND (
        NEW.name IS DISTINCT FROM OLD.name
        OR NEW.role IS DISTINCT FROM OLD.role
        OR NEW.tier IS DISTINCT FROM OLD.tier
        OR NEW.tier_priority IS DISTINCT FROM OLD.tier_priority
        OR NEW.url_env IS DISTINCT FROM OLD.url_env
        OR NEW.url_override IS DISTINCT FROM OLD.url_override
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
SET search_path = ai_gateway, public;

-- Restaura o trigger exatamente como no 0009.
DROP TRIGGER IF EXISTS upstreams_update_notify ON ai_gateway.upstreams;

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

ALTER TABLE ai_gateway.upstreams DROP COLUMN IF EXISTS url_override;
-- +goose StatementEnd
