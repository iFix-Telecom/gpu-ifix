-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 23 — escala de prices.unit_cost_usd. NUMERIC(12,8) (0012) arredonda
-- preço por token abaixo de 1e-8: o cache_write do Haiku 5.5 (1.25e-7) era
-- gravado como 1.2e-7 e o do :long (6.25e-7) como 6.3e-7 (23-ROLLOUT-EVIDENCE).
-- NUMERIC(20,12) guarda até 1e-12 US$/unidade e mantém 8 dígitos inteiros.
-- Ampliar precisão e escala juntos não perde valor nenhum das linhas atuais.
-- Nenhuma view depende da coluna. O trigger prices_update_notify (0012)
-- cita a coluna no WHEN, e o Postgres recusa ALTER TYPE de coluna usada em
-- definição de trigger: drop, altera e recria idêntico, na mesma transação.
DROP TRIGGER IF EXISTS prices_update_notify ON ai_gateway.prices;

ALTER TABLE ai_gateway.prices
    ALTER COLUMN unit_cost_usd TYPE NUMERIC(20,12);

CREATE TRIGGER prices_update_notify
AFTER UPDATE ON ai_gateway.prices
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.unit_cost_usd IS DISTINCT FROM OLD.unit_cost_usd
    OR NEW.valid_to IS DISTINCT FROM OLD.valid_to
))
EXECUTE FUNCTION ai_gateway.notify_prices_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Volta a NUMERIC(12,8): valores com mais de 8 casas são arredondados.
DROP TRIGGER IF EXISTS prices_update_notify ON ai_gateway.prices;

ALTER TABLE ai_gateway.prices
    ALTER COLUMN unit_cost_usd TYPE NUMERIC(12,8);

CREATE TRIGGER prices_update_notify
AFTER UPDATE ON ai_gateway.prices
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.unit_cost_usd IS DISTINCT FROM OLD.unit_cost_usd
    OR NEW.valid_to IS DISTINCT FROM OLD.valid_to
))
EXECUTE FUNCTION ai_gateway.notify_prices_changed();
-- +goose StatementEnd
