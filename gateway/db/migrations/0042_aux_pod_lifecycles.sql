-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 23 (23-03, HAIKU-09) — custo de pods Vast auxiliares que vivem FORA
-- do reconciler do primary (hoje: o pod unificado RTX 3060 STT+TTS+rerank+
-- embed, ciclo diário up→destroy pelo unified3060.py). Molde: 0023
-- primary_lifecycles, mas enxuto — sem FSM/events, só janela + custo.
CREATE TABLE IF NOT EXISTS ai_gateway.aux_pod_lifecycles (
    id                BIGSERIAL PRIMARY KEY,
    pod               TEXT NOT NULL,
        -- identificador lógico do pod (ex.: 'unified-3060')
    vast_instance_id  BIGINT,
    started_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at          TIMESTAMPTZ,
    dph_usd           NUMERIC(8,4),
    total_cost_usd    NUMERIC(10,4),
    total_cost_brl    NUMERIC(10,4),
    notes             TEXT,
    CONSTRAINT aux_pod_lifecycles_pod_instance_key UNIQUE (pod, vast_instance_id)
);

CREATE INDEX IF NOT EXISTS idx_aux_pod_lifecycles_started_at
    ON ai_gateway.aux_pod_lifecycles (started_at DESC);

CREATE INDEX IF NOT EXISTS idx_aux_pod_lifecycles_open
    ON ai_gateway.aux_pod_lifecycles (pod) WHERE ended_at IS NULL;

COMMENT ON TABLE ai_gateway.aux_pod_lifecycles IS
    'Phase 23: custo de pods Vast auxiliares fora do reconciler — 3060 unificado; gravado via POST /admin/aux-pod-lifecycles pelo unified3060.py; historico so a partir da Phase 23.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.aux_pod_lifecycles;
-- +goose StatementEnd
