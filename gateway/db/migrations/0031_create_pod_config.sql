-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 17 — DB-backed pod config (POD-CFG-01/02/05).
-- Single-row table holding ONLY the 16 HOT config fields (RESEARCH Config
-- Surface Table rows 1-16, CONTEXT D-01/D-02) plus owner-editable min/max
-- bound columns for the 10 numeric fields (CONTEXT D-03). Structural fields
-- (#17-35) do NOT enter this table — they stay env-only + restart-gated.
--
-- The table is created EMPTY. The env->DB seed runs in Go at boot (Plan 17-03)
-- via SeedPodConfig (ON CONFLICT DO NOTHING) so operator edits survive reboots
-- (RESEARCH Migration Concern). NO seed INSERT here.
--
-- Single-row guard: boolean PK defaulting TRUE with CHECK (id = TRUE) so at
-- most one row can ever exist (canonical singleton idiom).
CREATE TABLE IF NOT EXISTS ai_gateway.pod_config (
    id                          BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id = TRUE),

    -- 16 HOT config fields (native types per Config Surface Table) -------------
    vast_machine_blocklist      BIGINT[]    NOT NULL,  -- #1 PrimaryVastMachineBlocklist
    vast_machine_allowlist      BIGINT[]    NOT NULL,  -- #2 PrimaryVastMachineAllowlist
    cap_primary                 NUMERIC     NOT NULL,  -- #3 PrimaryVastPriceCapPrimary
    cap_fallback                NUMERIC     NOT NULL,  -- #4 PrimaryVastPriceCapFallback
    host_id                     BIGINT      NOT NULL,  -- #5 PrimaryHostID (0 = unknown)
    reject_private_ip           BOOLEAN     NOT NULL,  -- #6 PrimaryVastRejectPrivateIP
    coldstart_budget_s          INTEGER     NOT NULL,  -- #7 PrimaryProvisionColdStartBudgetSeconds
    port_bind_budget_s          INTEGER     NOT NULL,  -- #8 PrimaryPublicPortBindBudgetSeconds
    failure_cooldown_s          INTEGER     NOT NULL,  -- #9 PrimaryProvisionFailureCooldownSeconds
    monthly_budget_brl          NUMERIC     NOT NULL,  -- #10 MonthlyPrimaryBudgetBRL
    schedule_up_hour            INTEGER     NOT NULL,  -- #11 PrimaryPodScheduleUpHour
    schedule_down_hour          INTEGER     NOT NULL,  -- #12 PrimaryPodScheduleDownHour
    schedule_days               TEXT[]      NOT NULL,  -- #13 PrimaryPodScheduleDays
    grace_ramp_down_s           INTEGER     NOT NULL,  -- #14 PrimaryPodScheduleGraceRampDownSeconds
    provision_lead_s            INTEGER     NOT NULL,  -- #15 PrimaryPodScheduleProvisionLeadSeconds
    schedule_disabled           BOOLEAN     NOT NULL,  -- #16 PrimaryPodScheduleDisabled

    -- Owner-editable min/max bounds for the 10 numeric fields (D-03) ----------
    -- Defaults come from SeedPodConfig args (RESEARCH Validation Bounds table);
    -- the columns themselves carry NO SQL DEFAULT.
    cap_primary_min             NUMERIC     NOT NULL,  -- 0.10
    cap_primary_max             NUMERIC     NOT NULL,  -- 1.50
    cap_fallback_min            NUMERIC     NOT NULL,  -- 0.10
    cap_fallback_max            NUMERIC     NOT NULL,  -- 1.50
    coldstart_budget_s_min      INTEGER     NOT NULL,  -- 300
    coldstart_budget_s_max      INTEGER     NOT NULL,  -- 5400
    port_bind_budget_s_min      INTEGER     NOT NULL,  -- 30
    port_bind_budget_s_max      INTEGER     NOT NULL,  -- 600
    failure_cooldown_s_min      INTEGER     NOT NULL,  -- 60
    failure_cooldown_s_max      INTEGER     NOT NULL,  -- 1800
    monthly_budget_brl_min      NUMERIC     NOT NULL,  -- 0
    monthly_budget_brl_max      NUMERIC     NOT NULL,  -- 100000
    schedule_up_hour_min        INTEGER     NOT NULL,  -- 0
    schedule_up_hour_max        INTEGER     NOT NULL,  -- 23
    schedule_down_hour_min      INTEGER     NOT NULL,  -- 0
    schedule_down_hour_max      INTEGER     NOT NULL,  -- 23
    grace_ramp_down_s_min       INTEGER     NOT NULL,  -- 0
    grace_ramp_down_s_max       INTEGER     NOT NULL,  -- 1800
    provision_lead_s_min        INTEGER     NOT NULL,  -- 0
    provision_lead_s_max        INTEGER     NOT NULL,  -- 7200

    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE ai_gateway.pod_config IS
    'Single-row DB-backed primary-pod HOT config (Phase 17). 16 hot fields + 10 numeric bound pairs. Seeded from env at boot (Plan 17-03); owner-edited via PATCH /admin/primary/config (Plan 17-04). pod_config_changed NOTIFY drives the in-memory loader reload.';

-- NOTIFY trigger — mirror 0009_upstreams_notify_trigger.sql. The Postgres
-- restriction that an INSERT trigger's WHEN cannot reference OLD and a DELETE
-- trigger's WHEN cannot reference NEW forces the split into two triggers; both
-- call the same notify_pod_config_changed() function.
CREATE OR REPLACE FUNCTION ai_gateway.notify_pod_config_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('pod_config_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS pod_config_insert_delete_notify ON ai_gateway.pod_config;
DROP TRIGGER IF EXISTS pod_config_update_notify ON ai_gateway.pod_config;

-- INSERT/DELETE: always fire (config-table writes are operator actions).
CREATE TRIGGER pod_config_insert_delete_notify
AFTER INSERT OR DELETE ON ai_gateway.pod_config
FOR EACH ROW
WHEN (pg_trigger_depth() = 0)
EXECUTE FUNCTION ai_gateway.notify_pod_config_changed();

-- UPDATE: fire only when a real config-or-bound column changes. An idempotent
-- re-save of identical values (or a bare updated_at bump) leaves every listed
-- column IS NOT DISTINCT FROM OLD and skips the reload entirely (T-17-02).
CREATE TRIGGER pod_config_update_notify
AFTER UPDATE ON ai_gateway.pod_config
FOR EACH ROW
WHEN (
    pg_trigger_depth() = 0 AND (
        NEW.vast_machine_blocklist IS DISTINCT FROM OLD.vast_machine_blocklist
        OR NEW.vast_machine_allowlist IS DISTINCT FROM OLD.vast_machine_allowlist
        OR NEW.cap_primary IS DISTINCT FROM OLD.cap_primary
        OR NEW.cap_fallback IS DISTINCT FROM OLD.cap_fallback
        OR NEW.host_id IS DISTINCT FROM OLD.host_id
        OR NEW.reject_private_ip IS DISTINCT FROM OLD.reject_private_ip
        OR NEW.coldstart_budget_s IS DISTINCT FROM OLD.coldstart_budget_s
        OR NEW.port_bind_budget_s IS DISTINCT FROM OLD.port_bind_budget_s
        OR NEW.failure_cooldown_s IS DISTINCT FROM OLD.failure_cooldown_s
        OR NEW.monthly_budget_brl IS DISTINCT FROM OLD.monthly_budget_brl
        OR NEW.schedule_up_hour IS DISTINCT FROM OLD.schedule_up_hour
        OR NEW.schedule_down_hour IS DISTINCT FROM OLD.schedule_down_hour
        OR NEW.schedule_days IS DISTINCT FROM OLD.schedule_days
        OR NEW.grace_ramp_down_s IS DISTINCT FROM OLD.grace_ramp_down_s
        OR NEW.provision_lead_s IS DISTINCT FROM OLD.provision_lead_s
        OR NEW.schedule_disabled IS DISTINCT FROM OLD.schedule_disabled
        OR NEW.cap_primary_min IS DISTINCT FROM OLD.cap_primary_min
        OR NEW.cap_primary_max IS DISTINCT FROM OLD.cap_primary_max
        OR NEW.cap_fallback_min IS DISTINCT FROM OLD.cap_fallback_min
        OR NEW.cap_fallback_max IS DISTINCT FROM OLD.cap_fallback_max
        OR NEW.coldstart_budget_s_min IS DISTINCT FROM OLD.coldstart_budget_s_min
        OR NEW.coldstart_budget_s_max IS DISTINCT FROM OLD.coldstart_budget_s_max
        OR NEW.port_bind_budget_s_min IS DISTINCT FROM OLD.port_bind_budget_s_min
        OR NEW.port_bind_budget_s_max IS DISTINCT FROM OLD.port_bind_budget_s_max
        OR NEW.failure_cooldown_s_min IS DISTINCT FROM OLD.failure_cooldown_s_min
        OR NEW.failure_cooldown_s_max IS DISTINCT FROM OLD.failure_cooldown_s_max
        OR NEW.monthly_budget_brl_min IS DISTINCT FROM OLD.monthly_budget_brl_min
        OR NEW.monthly_budget_brl_max IS DISTINCT FROM OLD.monthly_budget_brl_max
        OR NEW.schedule_up_hour_min IS DISTINCT FROM OLD.schedule_up_hour_min
        OR NEW.schedule_up_hour_max IS DISTINCT FROM OLD.schedule_up_hour_max
        OR NEW.schedule_down_hour_min IS DISTINCT FROM OLD.schedule_down_hour_min
        OR NEW.schedule_down_hour_max IS DISTINCT FROM OLD.schedule_down_hour_max
        OR NEW.grace_ramp_down_s_min IS DISTINCT FROM OLD.grace_ramp_down_s_min
        OR NEW.grace_ramp_down_s_max IS DISTINCT FROM OLD.grace_ramp_down_s_max
        OR NEW.provision_lead_s_min IS DISTINCT FROM OLD.provision_lead_s_min
        OR NEW.provision_lead_s_max IS DISTINCT FROM OLD.provision_lead_s_max
    )
)
EXECUTE FUNCTION ai_gateway.notify_pod_config_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS pod_config_update_notify ON ai_gateway.pod_config;
DROP TRIGGER IF EXISTS pod_config_insert_delete_notify ON ai_gateway.pod_config;
DROP FUNCTION IF EXISTS ai_gateway.notify_pod_config_changed();
DROP TABLE IF EXISTS ai_gateway.pod_config CASCADE;
-- +goose StatementEnd
