-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 999.2 — force_machine_id: an OPS/UAT escape-hatch to PIN the primary
-- provisioner to a specific Vast machine_id, bypassing market_cheapest and the
-- allowlist-preferred pass. 0 = disabled (normal market pick). Primary use: the
-- regime-1 (created_state_timeout / FF-01) live UAT, which needs to force a
-- known-flaky host that hangs in actual_status=created — the market picker never
-- lets an operator select a specific host (host_id is an EXCLUSION filter, not a
-- pin). Secondary use: ops pinning a known-good host during an incident.
-- HOT owner-editable BIGINT; DEFAULT backfills the already-seeded prod row.
ALTER TABLE ai_gateway.pod_config
    ADD COLUMN force_machine_id BIGINT NOT NULL DEFAULT 0;

-- Recreate the UPDATE NOTIFY trigger to include force_machine_id in its WHEN
-- predicate so a PATCH fires pod_config_changed and the loader hot-reloads
-- (otherwise the picker would not see the pin until the next unrelated edit or
-- a restart). DROP + CREATE idiom (repo standard, not CREATE OR REPLACE).
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
        OR NEW.force_machine_id IS DISTINCT FROM OLD.force_machine_id
    )
)
EXECUTE FUNCTION ai_gateway.notify_pod_config_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Drop the trigger FIRST: its WHEN-clause references force_machine_id.
DROP TRIGGER IF EXISTS pod_config_update_notify ON ai_gateway.pod_config;

ALTER TABLE ai_gateway.pod_config
    DROP COLUMN IF EXISTS force_machine_id;

-- Restore the 0033 trigger (without force_machine_id in the WHEN predicate).
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
