-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 20 — CFG-01: two new coldstart fast-fail budgets on pod_config.
-- created_budget_s      (regime-1 fast-fail, FF-01): how long a Vast instance
--                       may sit in actual_status=created before being destroyed.
-- progress_stall_budget_s (regime-3 fast-fail, FF-02): how long the onstart
--                       weight download may show no byte progress before kill.
-- Both are HOT owner-editable INTEGER fields with min/max bound columns,
-- mirroring the EXISTING coldstart_budget_s / port_bind_budget_s stack.
--
-- SQL DEFAULT is LOAD-BEARING: SeedPodConfig is ON CONFLICT DO NOTHING, so it
-- will NOT populate these columns on the already-seeded prod row. The DEFAULT
-- backfills existing rows; the seed param covers fresh installs (Q5).
ALTER TABLE ai_gateway.pod_config
    ADD COLUMN created_budget_s              INTEGER NOT NULL DEFAULT 120,
    ADD COLUMN created_budget_s_min          INTEGER NOT NULL DEFAULT 30,
    ADD COLUMN created_budget_s_max          INTEGER NOT NULL DEFAULT 600,
    ADD COLUMN progress_stall_budget_s       INTEGER NOT NULL DEFAULT 120,
    ADD COLUMN progress_stall_budget_s_min   INTEGER NOT NULL DEFAULT 30,
    ADD COLUMN progress_stall_budget_s_max   INTEGER NOT NULL DEFAULT 600;

-- Recreate the UPDATE NOTIFY trigger to include the 6 new columns in its
-- WHEN predicate so an edit to any of them fires pod_config_changed and the
-- loader hot-reloads. Repo idiom is DROP TRIGGER IF EXISTS + CREATE TRIGGER
-- (0009/0031) — NOT CREATE OR REPLACE TRIGGER. The insert/delete trigger and
-- the notify_pod_config_changed() function are unchanged.
DROP TRIGGER IF EXISTS pod_config_update_notify ON ai_gateway.pod_config;

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
        OR NEW.created_budget_s IS DISTINCT FROM OLD.created_budget_s
        OR NEW.created_budget_s_min IS DISTINCT FROM OLD.created_budget_s_min
        OR NEW.created_budget_s_max IS DISTINCT FROM OLD.created_budget_s_max
        OR NEW.progress_stall_budget_s IS DISTINCT FROM OLD.progress_stall_budget_s
        OR NEW.progress_stall_budget_s_min IS DISTINCT FROM OLD.progress_stall_budget_s_min
        OR NEW.progress_stall_budget_s_max IS DISTINCT FROM OLD.progress_stall_budget_s_max
    )
)
EXECUTE FUNCTION ai_gateway.notify_pod_config_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Drop the trigger FIRST: its WHEN-clause references the 6 columns below, so
-- dropping the columns while the trigger is live fails with 2BP01 (dependency).
DROP TRIGGER IF EXISTS pod_config_update_notify ON ai_gateway.pod_config;

ALTER TABLE ai_gateway.pod_config
    DROP COLUMN IF EXISTS created_budget_s,
    DROP COLUMN IF EXISTS created_budget_s_min,
    DROP COLUMN IF EXISTS created_budget_s_max,
    DROP COLUMN IF EXISTS progress_stall_budget_s,
    DROP COLUMN IF EXISTS progress_stall_budget_s_min,
    DROP COLUMN IF EXISTS progress_stall_budget_s_max;

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
