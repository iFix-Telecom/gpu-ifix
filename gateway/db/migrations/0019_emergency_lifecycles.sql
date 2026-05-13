-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 6 — emergency pod lifecycle audit table.
-- Source: CONTEXT.md D-B4 + Pitfall 15 fix (added first_health_pass_at column).
-- Migration number = 0019 per RESEARCH Pitfall 10 (Phase 5 consumed 0017+0018).
CREATE TABLE IF NOT EXISTS ai_gateway.emergency_lifecycles (
    id                      BIGSERIAL PRIMARY KEY,
    started_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    first_health_pass_at    TIMESTAMPTZ,
    ended_at                TIMESTAMPTZ,
    trigger_reason          TEXT NOT NULL,
        -- 'failed_over_sustained' | 'manual_force'
    vast_offer_id           BIGINT,
    vast_instance_id        BIGINT,
    accepted_dph            NUMERIC(6,4),
    total_cost_brl          NUMERIC(10,4),
    shutdown_reason         TEXT,
        -- 'cutback_idle' | 'cancelled_in_flight' | 'health_timeout'
        -- | 'offer_race_lost' | 'manual' | 'budget_exceeded'
        -- | 'leader_recovery_zombie' | 'leader_recovery_lost' | 'leader_recovery_pre_create'
        -- | 'instance_terminal_state' | 'vast_5xx'
    events                  JSONB NOT NULL DEFAULT '[]'::JSONB,
        -- [{ts, from_state, to_state, reason, payload}]
    leader_replica          TEXT
        -- os.Hostname() of the gateway replica that was leader at lifecycle start
);

-- D-B5 — partial unique index: at most 1 row with ended_at IS NULL.
-- Defense-in-depth alongside leader-election + reconciler check (D-C5).
CREATE UNIQUE INDEX IF NOT EXISTS emergency_live_singleton
    ON ai_gateway.emergency_lifecycles ((TRUE)) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_started_at
    ON ai_gateway.emergency_lifecycles (started_at DESC);

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_live
    ON ai_gateway.emergency_lifecycles (ended_at) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_month_cost
    ON ai_gateway.emergency_lifecycles (started_at)
    WHERE ended_at IS NOT NULL;

COMMENT ON TABLE ai_gateway.emergency_lifecycles IS
    'Audit trail for Vast.ai emergency pod lifecycles (Phase 6 PRV-10). One row per provision attempt; events JSONB captures full timeline.';
COMMENT ON COLUMN ai_gateway.emergency_lifecycles.first_health_pass_at IS
    'Timestamp when pod /health first returned healthy. Used as start of cost calc (D-D4): hours_active = ended_at - first_health_pass_at.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.emergency_lifecycles CASCADE;
-- +goose StatementEnd
