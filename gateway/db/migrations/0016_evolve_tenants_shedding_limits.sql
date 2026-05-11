-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 5 — per-tenant fairness hard caps (D-B1 / D-B2).
--
-- Four hard-cap columns + priority_tier metadata are added to ai_gateway.tenants.
-- Defaults are conservative (4/2/8/'A') so a freshly migrated row immediately
-- has a usable cap without operator action.
--
-- IMPORTANT: per 05-WAVE0-GATES.md Gate B (resolved 2026-05-11), this migration
-- does NOT seed per-tenant caps. The operator is the source of truth for
-- production caps via `gatewayctl tenant set-shed-limits` (Plan 05-07). Column
-- defaults are inert until that command runs, so the system boots safely
-- regardless of which slug variants ('voice-api' vs 'voice_api', etc.) exist
-- in the live tenants table.
ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS local_inflight_max_llm   INT  NOT NULL DEFAULT 4,
    ADD COLUMN IF NOT EXISTS local_inflight_max_stt   INT  NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS local_inflight_max_embed INT  NOT NULL DEFAULT 8,
    ADD COLUMN IF NOT EXISTS priority_tier            TEXT NOT NULL DEFAULT 'A'
        CHECK (priority_tier IN ('S','A','B'));

-- Expand the existing tenants_update_notify trigger so changes to the 4 new
-- columns also publish to NOTIFY tenants_changed. tenants.Loader.Refresh
-- already subscribes to that channel via pgxlisten (see internal/tenants/
-- listen.go in Phase 4); the only thing missing is the WHEN-clause coverage.
--
-- WHEN clauses are not alterable in Postgres, so we DROP + CREATE. The full
-- WHEN list must remain in sync with 0013_evolve_tenants_schedule_quota.sql.
DROP TRIGGER IF EXISTS tenants_update_notify ON ai_gateway.tenants;

CREATE TRIGGER tenants_update_notify
AFTER UPDATE ON ai_gateway.tenants
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.mode                        IS DISTINCT FROM OLD.mode
    OR NEW.peak_window_start        IS DISTINCT FROM OLD.peak_window_start
    OR NEW.peak_window_end          IS DISTINCT FROM OLD.peak_window_end
    OR NEW.schedule_timezone        IS DISTINCT FROM OLD.schedule_timezone
    OR NEW.daily_quota_tokens       IS DISTINCT FROM OLD.daily_quota_tokens
    OR NEW.monthly_quota_tokens     IS DISTINCT FROM OLD.monthly_quota_tokens
    OR NEW.daily_quota_audio_minutes IS DISTINCT FROM OLD.daily_quota_audio_minutes
    OR NEW.monthly_quota_audio_minutes IS DISTINCT FROM OLD.monthly_quota_audio_minutes
    OR NEW.daily_quota_embeds       IS DISTINCT FROM OLD.daily_quota_embeds
    OR NEW.monthly_quota_embeds     IS DISTINCT FROM OLD.monthly_quota_embeds
    OR NEW.rps_limit                IS DISTINCT FROM OLD.rps_limit
    OR NEW.rpm_limit                IS DISTINCT FROM OLD.rpm_limit
    OR NEW.data_class               IS DISTINCT FROM OLD.data_class
    OR NEW.local_inflight_max_llm   IS DISTINCT FROM OLD.local_inflight_max_llm
    OR NEW.local_inflight_max_stt   IS DISTINCT FROM OLD.local_inflight_max_stt
    OR NEW.local_inflight_max_embed IS DISTINCT FROM OLD.local_inflight_max_embed
    OR NEW.priority_tier            IS DISTINCT FROM OLD.priority_tier
))
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

DROP TRIGGER IF EXISTS tenants_update_notify ON ai_gateway.tenants;

ALTER TABLE ai_gateway.tenants
    DROP COLUMN IF EXISTS local_inflight_max_llm,
    DROP COLUMN IF EXISTS local_inflight_max_stt,
    DROP COLUMN IF EXISTS local_inflight_max_embed,
    DROP COLUMN IF EXISTS priority_tier;

-- Recreate the original Phase 4 trigger (without Phase 5 cols) so downgrading
-- leaves the NOTIFY pipeline in a coherent state for Phase 4-only operation.
CREATE TRIGGER tenants_update_notify
AFTER UPDATE ON ai_gateway.tenants
FOR EACH ROW
WHEN (pg_trigger_depth() = 0 AND (
    NEW.mode                        IS DISTINCT FROM OLD.mode
    OR NEW.peak_window_start        IS DISTINCT FROM OLD.peak_window_start
    OR NEW.peak_window_end          IS DISTINCT FROM OLD.peak_window_end
    OR NEW.schedule_timezone        IS DISTINCT FROM OLD.schedule_timezone
    OR NEW.daily_quota_tokens       IS DISTINCT FROM OLD.daily_quota_tokens
    OR NEW.monthly_quota_tokens     IS DISTINCT FROM OLD.monthly_quota_tokens
    OR NEW.daily_quota_audio_minutes IS DISTINCT FROM OLD.daily_quota_audio_minutes
    OR NEW.monthly_quota_audio_minutes IS DISTINCT FROM OLD.monthly_quota_audio_minutes
    OR NEW.daily_quota_embeds       IS DISTINCT FROM OLD.daily_quota_embeds
    OR NEW.monthly_quota_embeds     IS DISTINCT FROM OLD.monthly_quota_embeds
    OR NEW.rps_limit                IS DISTINCT FROM OLD.rps_limit
    OR NEW.rpm_limit                IS DISTINCT FROM OLD.rpm_limit
    OR NEW.data_class               IS DISTINCT FROM OLD.data_class
))
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();
-- +goose StatementEnd
