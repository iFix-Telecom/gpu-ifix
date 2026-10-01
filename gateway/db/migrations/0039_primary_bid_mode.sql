-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick-261001-qdd — primary 3090 real cost + interruptible (bid) instances.
-- offer_mode: 'bid' (default, Pedro 2026-10-01) searches on-demand AND bid
--   offers and rents the cheapest by REAL cost; 'ondemand' never bids.
-- bid_margin: bid price = min_bid * bid_margin (capped at cap - storage/h).
-- max_preemptions_per_day: after N 'preempted' lifecycles today (BRT) the
--   reconciler falls back to on-demand for the rest of the day. 0 disables
--   the fallback (cfg mode always honored).
-- DEFAULTs backfill the already-seeded prod row. ADDITIVE only.
ALTER TABLE ai_gateway.pod_config
    ADD COLUMN offer_mode TEXT NOT NULL DEFAULT 'bid'
        CHECK (offer_mode IN ('bid', 'ondemand')),
    ADD COLUMN bid_margin NUMERIC(4,2) NOT NULL DEFAULT 1.15
        CHECK (bid_margin >= 1.00 AND bid_margin <= 5.00),
    ADD COLUMN max_preemptions_per_day INTEGER NOT NULL DEFAULT 2
        CHECK (max_preemptions_per_day >= 0 AND max_preemptions_per_day <= 20);

-- Lifecycle audit: was this lifecycle a bid (interruptible) rental and at
-- what bid price (US$/h, excl. storage). NULL = legacy row (pre-0039).
ALTER TABLE ai_gateway.primary_lifecycles
    ADD COLUMN is_bid BOOLEAN NULL,
    ADD COLUMN bid_price NUMERIC(6,4) NULL;

COMMENT ON COLUMN ai_gateway.primary_lifecycles.shutdown_reason IS
    'Final reason the lifecycle ended (e.g. destroyed, instance_terminal_state, billing_stopped, preempted = bid instance stopped/outbid by Vast).';

-- Recreate the UPDATE NOTIFY trigger with the 3 new columns in its WHEN
-- predicate so a PATCH hot-reloads (DROP + CREATE idiom, repo standard).
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
        OR NEW.offer_mode IS DISTINCT FROM OLD.offer_mode
        OR NEW.bid_margin IS DISTINCT FROM OLD.bid_margin
        OR NEW.max_preemptions_per_day IS DISTINCT FROM OLD.max_preemptions_per_day
    )
)
EXECUTE FUNCTION ai_gateway.notify_pod_config_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Drop the trigger FIRST: its WHEN-clause references the 0039 columns.
DROP TRIGGER IF EXISTS pod_config_update_notify ON ai_gateway.pod_config;

ALTER TABLE ai_gateway.primary_lifecycles
    DROP COLUMN IF EXISTS bid_price,
    DROP COLUMN IF EXISTS is_bid;

COMMENT ON COLUMN ai_gateway.primary_lifecycles.shutdown_reason IS NULL;

ALTER TABLE ai_gateway.pod_config
    DROP COLUMN IF EXISTS max_preemptions_per_day,
    DROP COLUMN IF EXISTS bid_margin,
    DROP COLUMN IF EXISTS offer_mode;

-- Restore the 0034 trigger (verbatim, with force_machine_id).
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
