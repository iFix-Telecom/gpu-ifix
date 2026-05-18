-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 6.6 — primary pod lifecycle audit table.
-- Source: 06.6-CONTEXT.md D-08 + 06.6-RESEARCH.md §Runtime State Inventory.
-- Migration number = 0023 (computed at execution time per reviews consensus action #10:
--   LAST_NUM=$(ls gateway/db/migrations/ | sort -V | tail -1 | grep -oE '^[0-9]+'); NEXT_NUM=$(printf "%04d" $((10#$LAST_NUM + 1)))
-- Latest migration in tree at exec time: 0022_audit_log_reason.sql).
--
-- Parallel of ai_gateway.emergency_lifecycles (0019_emergency_lifecycles.sql),
-- with one extra column: drain_started_at TIMESTAMPTZ — records when the schedule
-- reconciler entered the drain ramp-down window before destroying the pod
-- (Phase 6.6 D-08 PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS gate).
CREATE TABLE IF NOT EXISTS ai_gateway.primary_lifecycles (
    id                      BIGSERIAL PRIMARY KEY,
    started_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    first_health_pass_at    TIMESTAMPTZ,
    drain_started_at        TIMESTAMPTZ,
        -- NEW vs emerg (Phase 6.6): drain ramp-down audit. Set when reconciler
        -- enters drain window; ended_at is set after grace expires + destroy succeeds.
    ended_at                TIMESTAMPTZ,
    trigger_reason          TEXT NOT NULL,
        -- 'schedule_window_entered' | 'manual_force_up'
    vast_offer_id           BIGINT,
    vast_instance_id        BIGINT,
    accepted_dph            NUMERIC(6,4),
    total_cost_brl          NUMERIC(10,4),
    shutdown_reason         TEXT,
        -- 'schedule_window_exited' | 'drain_grace_elapsed' | 'cancelled_in_flight'
        -- | 'health_timeout' | 'offer_race_lost' | 'manual_force_down' | 'budget_exceeded'
        -- | 'leader_recovery_zombie' | 'instance_terminal_state' | 'vast_5xx'
    events                  JSONB NOT NULL DEFAULT '[]'::JSONB,
        -- [{ts, from_state, to_state, reason, payload}]
    leader_replica          TEXT
        -- os.Hostname() of the gateway replica that was leader at lifecycle start
);

-- Partial unique index (parity with emerg D-B5): at most 1 row with ended_at IS NULL.
-- Defense-in-depth alongside leader-election + reconciler check.
CREATE UNIQUE INDEX IF NOT EXISTS primary_live_singleton
    ON ai_gateway.primary_lifecycles ((TRUE)) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_primary_lifecycles_started_at
    ON ai_gateway.primary_lifecycles (started_at DESC);

CREATE INDEX IF NOT EXISTS idx_primary_lifecycles_live
    ON ai_gateway.primary_lifecycles (ended_at) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_primary_lifecycles_month_cost
    ON ai_gateway.primary_lifecycles (started_at)
    WHERE ended_at IS NOT NULL;

COMMENT ON TABLE ai_gateway.primary_lifecycles IS
    'Audit trail for Vast.ai primary pod lifecycles (Phase 6.6). One row per provision attempt; events JSONB captures full timeline.';
COMMENT ON COLUMN ai_gateway.primary_lifecycles.first_health_pass_at IS
    'Timestamp when pod /health first returned healthy. Used as start of cost calc: hours_active = ended_at - first_health_pass_at.';
COMMENT ON COLUMN ai_gateway.primary_lifecycles.drain_started_at IS
    'Timestamp when reconciler entered drain ramp-down window (Phase 6.6 D-08). NEW vs emerg.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.primary_lifecycles CASCADE;
-- +goose StatementEnd
