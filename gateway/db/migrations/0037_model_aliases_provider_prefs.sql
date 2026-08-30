-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick 260830-o2j — OpenRouter provider routing per TENANT and per MODEL.
--
-- Until now the openrouter-chat director injected ONE global
-- `provider.order` (env UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER, default
-- ["novita"]) into every chat request. Two OPTIONAL jsonb columns now carry
-- an OpenRouter `provider` object verbatim (provider-routing schema:
--   only / order / ignore / allow_fallbacks / require_parameters /
--   data_collection / zdr / quantizations / sort / max_price /
--   preferred_min_throughput / preferred_max_latency):
--
--   tenants.provider_prefs        → applies to every OpenRouter chat call of
--                                   that tenant (HIGHEST precedence)
--   model_aliases.provider_prefs  → applies to the (alias, openrouter-chat)
--                                   row when the tenant has none
--   neither                       → legacy global env behaviour
--
-- Validation lives in gateway/internal/models/provider_prefs.go and is
-- enforced by gatewayctl AND the admin API before any write.
ALTER TABLE ai_gateway.model_aliases
    ADD COLUMN IF NOT EXISTS provider_prefs JSONB;

COMMENT ON COLUMN ai_gateway.model_aliases.provider_prefs IS
    'quick 260830-o2j: optional OpenRouter `provider` routing object injected verbatim by the openrouter-chat director for this (alias, upstream_name) when the tenant has no provider_prefs. NULL = fall through. Only meaningful for upstream_name=openrouter-chat.';

ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS provider_prefs JSONB;

COMMENT ON COLUMN ai_gateway.tenants.provider_prefs IS
    'quick 260830-o2j: optional OpenRouter `provider` routing object applied to EVERY openrouter-chat call of this tenant (precedence over model_aliases.provider_prefs). NULL = fall through.';

-- The tenants loader hot-reloads on NOTIFY tenants_changed, whose UPDATE
-- trigger is column-filtered (0013 → 0016). WHEN clauses are not alterable,
-- so DROP + CREATE with provider_prefs appended. Keep in sync with 0016.
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
    OR NEW.provider_prefs           IS DISTINCT FROM OLD.provider_prefs
))
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Restore the 0016 trigger shape (without provider_prefs) before dropping
-- the column, otherwise the WHEN clause would reference a missing column.
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

ALTER TABLE ai_gateway.tenants DROP COLUMN IF EXISTS provider_prefs;
ALTER TABLE ai_gateway.model_aliases DROP COLUMN IF EXISTS provider_prefs;
-- +goose StatementEnd
