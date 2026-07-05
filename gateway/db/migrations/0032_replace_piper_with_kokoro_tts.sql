-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick 260704-ubt — replace the dead voice-api-piper tier-1 TTS (removed from
-- vps-ifix-vm) with kokoro-tts: a Kokoro-FastAPI OpenAI-compatible
-- /v1/audio/speech server on the worker-vm ai-gateway overlay (DNS
-- kokoro-tts:8880). Unlike Piper (which needed a /tts form adapter), Kokoro
-- speaks OpenAI 1:1, so the gateway wires it via the passthrough NewTTSProxy.
--
-- UNIQUE(role, tier) (0007:23) forbids two rows on ('tts', 1), so this is an
-- in-place UPDATE of the existing piper row (row-neutral, reversible) rather
-- than a delete+insert. url_env repoints to UPSTREAM_TTS_KOKORO_URL. No-op if
-- already migrated (WHERE name = 'voice-api-piper' matches nothing).
UPDATE ai_gateway.upstreams
   SET name = 'kokoro-tts', url_env = 'UPSTREAM_TTS_KOKORO_URL'
 WHERE name = 'voice-api-piper';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Reverse the rename: kokoro-tts -> voice-api-piper, url_env back to
-- UPSTREAM_TTS_PIPER_URL. Row-neutral (same UPDATE, no row count change).
UPDATE ai_gateway.upstreams
   SET name = 'voice-api-piper', url_env = 'UPSTREAM_TTS_PIPER_URL'
 WHERE name = 'kokoro-tts';
-- +goose StatementEnd
