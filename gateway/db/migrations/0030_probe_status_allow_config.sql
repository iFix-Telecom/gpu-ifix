-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- SEED-013 / quick 260616-gtj: the probe now records last_probe_status='config'
-- for 4xx upstream responses (the breaker classifies 4xx as a config issue, not a
-- health failure — D-A4). The original 0007 CHECK only allowed ('ok','failed',
-- 'timeout'), so every 4xx probe UPDATE failed with SQLSTATE 23514 once the probe
-- fix deployed (e.g. openrouter-chat, gemini-stt which 4xx on the synthetic probe).
-- Widen the allow-list to include 'config'.
ALTER TABLE ai_gateway.upstreams DROP CONSTRAINT IF EXISTS upstreams_last_probe_status_check;
ALTER TABLE ai_gateway.upstreams ADD CONSTRAINT upstreams_last_probe_status_check
    CHECK (last_probe_status IS NULL OR last_probe_status IN ('ok', 'failed', 'timeout', 'config'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Revert any 'config' rows to NULL so the narrower constraint can be re-applied.
UPDATE ai_gateway.upstreams SET last_probe_status = NULL WHERE last_probe_status = 'config';
ALTER TABLE ai_gateway.upstreams DROP CONSTRAINT IF EXISTS upstreams_last_probe_status_check;
ALTER TABLE ai_gateway.upstreams ADD CONSTRAINT upstreams_last_probe_status_check
    CHECK (last_probe_status IS NULL OR last_probe_status IN ('ok', 'failed', 'timeout'));
-- +goose StatementEnd
