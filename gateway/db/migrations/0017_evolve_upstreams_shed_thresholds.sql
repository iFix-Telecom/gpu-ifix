-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 5 — seed saturation thresholds on the three tier-0 upstreams.
--
-- Five JSONB fields are merged (via `||`) into the existing circuit_config so
-- the Phase 3 fields (failures, cooldown_s) are preserved untouched. Tier-1
-- upstreams (openrouter-chat, openai-whisper, openai-text-embedding-3-small)
-- are intentionally left alone — the shed FSM never runs against tier-1
-- (CONTEXT.md D-C4).
--
-- CRITICAL UNIT NOTE: shed_vram_used_mib is in MiB, NOT bytes. DCGM exporter
-- reports DCGM_FI_DEV_FB_USED in MiB natively (RESEARCH.md Pitfall 1), so
-- 21 GB threshold = 21 × 1024 = 21504 MiB. Do NOT write 22548578304 (bytes);
-- the parser in internal/upstreams/types.go expects MiB.
--
-- The UPDATEs below fire the upstreams_update_notify trigger from 0009 because
-- circuit_config IS DISTINCT FROM check matches, which publishes NOTIFY
-- upstreams_changed → upstreams.Loader.Refresh picks the new thresholds up
-- within <2s (SC-3 budget).

UPDATE ai_gateway.upstreams
SET circuit_config = COALESCE(circuit_config, '{}'::jsonb) || jsonb_build_object(
        'shed_inflight_max',    8,
        'shed_p95_ms',          2000,
        'shed_vram_used_mib',   21504,
        'shed_arm_seconds',     30,
        'shed_recover_seconds', 60
    )
WHERE name = 'local-llm';

UPDATE ai_gateway.upstreams
SET circuit_config = COALESCE(circuit_config, '{}'::jsonb) || jsonb_build_object(
        'shed_inflight_max',    4,
        'shed_p95_ms',          3000,   -- Whisper is slower by design
        'shed_vram_used_mib',   21504,
        'shed_arm_seconds',     30,
        'shed_recover_seconds', 60
    )
WHERE name = 'local-stt';

UPDATE ai_gateway.upstreams
SET circuit_config = COALESCE(circuit_config, '{}'::jsonb) || jsonb_build_object(
        'shed_inflight_max',    16,
        'shed_p95_ms',          500,
        'shed_vram_used_mib',   21504,
        'shed_arm_seconds',     30,
        'shed_recover_seconds', 60
    )
WHERE name = 'local-embed';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Strip the 5 shed_* keys from circuit_config. Other fields (failures,
-- cooldown_s) survive intact because `-` only removes the named keys.
UPDATE ai_gateway.upstreams
SET circuit_config = circuit_config
                   - 'shed_inflight_max'
                   - 'shed_p95_ms'
                   - 'shed_vram_used_mib'
                   - 'shed_arm_seconds'
                   - 'shed_recover_seconds'
WHERE name IN ('local-llm', 'local-stt', 'local-embed');
-- +goose StatementEnd
