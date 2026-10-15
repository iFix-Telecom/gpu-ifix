-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 23 (23-03) — Haiku 5.5 como LLM externo principal do gateway.
--
-- Cadeia LLM tier-1 (ordenada por tier_priority):
--     tier=1 tier_priority=0  anthropic-haiku      (NOVO — API Anthropic direta, créditos)
--     tier=1 tier_priority=10 openrouter-chat      (deslocado 0→10 — Haiku via OpenRouter)
--     tier=1 tier_priority=20 openrouter-deepseek  (NOVO — DeepSeek via OpenRouter, último recurso)
-- TTS tier-1: openrouter-tts entra na FRENTE (prio 0) e todo o tier 1 de tts
-- existente é deslocado +10 SEM assumir prioridade fixa (prod diverge das
-- migrations 0024/0032 — local-tts reapontado via admin API).
--
-- anthropic-haiku e openrouter-deepseek nascem enabled=false: o loader só
-- carrega linhas enabled, então a imagem antiga (sem proxy para eles) não os
-- enxerga na janela migrate → troca de imagem. O 23-16 habilita via
-- `gatewayctl upstreams enable --name=<nome>` depois do healthcheck.
-- openrouter-tts nasce enabled=true: o env UPSTREAM_TTS_OPENROUTER_URL só é
-- setado no 23-17 e, até lá, o loader pula a linha (url_env vazio).
--
-- Guardas (T-23-66): nenhuma divergência de prod é descartada em silêncio.
--   (a) posição LLM ocupada por outro upstream → RAISE ANTES do 1º UPDATE;
--   (c) prioridade tts tier 1 ≥ 990 → RAISE (deslocamento em dois passos);
--   (c) conferência final da DEFINIÇÃO dos 4 upstreams → RAISE se divergente
--       (o ON CONFLICT (name) DO NOTHING nunca preserva homônimo errado).
--
-- O banco guarda só o NOME dos envs de credencial (T-23-09), nunca a chave.

-- (a) Guarda de posição LLM — roda ANTES de qualquer UPDATE para que (llm,1,10)
-- ocupada falhe com a mensagem própria, não com o erro cru de UNIQUE.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT name, tier_priority
          FROM ai_gateway.upstreams
         WHERE role = 'llm' AND tier = 1
           AND (
                (tier_priority = 0  AND name NOT IN ('openrouter-chat', 'anthropic-haiku'))
             OR (tier_priority = 10 AND name <> 'openrouter-chat')
             OR (tier_priority = 20 AND name <> 'openrouter-deepseek')
           )
    LOOP
        RAISE EXCEPTION 'phase23 0041: posicao (llm,1,%) ocupada por %', r.tier_priority, r.name;
    END LOOP;
END
$$;

UPDATE ai_gateway.upstreams SET tier_priority = 10 WHERE name='openrouter-chat' AND role='llm' AND tier=1;

-- (b) Novos upstreams LLM, desabilitados até o 23-16.
INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env, enabled) VALUES
    ('anthropic-haiku',     'llm', 1, 0,  'UPSTREAM_LLM_ANTHROPIC_URL',  'UPSTREAM_LLM_ANTHROPIC_API_KEYS',false),
    ('openrouter-deepseek', 'llm', 1, 20, 'UPSTREAM_LLM_OPENROUTER_URL', 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER',false)
ON CONFLICT (name) DO NOTHING;

-- (c) TTS: desloca o tier 1 inteiro +10 e insere openrouter-tts na frente.
-- Só desloca quando openrouter-tts ainda não existe (re-Up após Down não
-- desloca duas vezes). O deslocamento é em dois passos (+1000, -990) porque a
-- UNIQUE(role,tier,tier_priority) não é deferrable e +10 direto colidiria
-- no meio do UPDATE (ex.: 0→10 com uma linha ainda em 10).
DO $$
DECLARE
    maxprio INT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM ai_gateway.upstreams WHERE name = 'openrouter-tts') THEN
        SELECT MAX(tier_priority) INTO maxprio
          FROM ai_gateway.upstreams
         WHERE role = 'tts' AND tier = 1;
        IF maxprio IS NOT NULL AND maxprio >= 990 THEN
            RAISE EXCEPTION 'phase23 0041: prioridade tts tier 1 >= 990 (%)', maxprio;
        END IF;

        UPDATE ai_gateway.upstreams SET tier_priority = tier_priority + 1000
         WHERE role = 'tts' AND tier = 1;
        UPDATE ai_gateway.upstreams SET tier_priority = tier_priority - 990
         WHERE role = 'tts' AND tier = 1;

        INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env) VALUES
            ('openrouter-tts', 'tts', 1, 0, 'UPSTREAM_TTS_OPENROUTER_URL', 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER')
        ON CONFLICT (name) DO NOTHING;
    END IF;
END
$$;

-- Conferência final de DEFINIÇÃO (não só de posição). `enabled` fica de fora:
-- é estado operacional, não definição (o valor inicial é afirmado nos testes).
DO $$
DECLARE
    want RECORD;
    got  RECORD;
BEGIN
    FOR want IN
        SELECT * FROM (VALUES
            ('anthropic-haiku',     'llm', 1, 0,  'UPSTREAM_LLM_ANTHROPIC_URL',  'UPSTREAM_LLM_ANTHROPIC_API_KEYS'),
            ('openrouter-chat',     'llm', 1, 10, 'UPSTREAM_LLM_OPENROUTER_URL', 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER'),
            ('openrouter-deepseek', 'llm', 1, 20, 'UPSTREAM_LLM_OPENROUTER_URL', 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER'),
            ('openrouter-tts',      'tts', 1, 0,  'UPSTREAM_TTS_OPENROUTER_URL', 'UPSTREAM_LLM_OPENROUTER_AUTH_BEARER')
        ) AS w(name, role, tier, tier_priority, url_env, auth_bearer_env)
    LOOP
        SELECT u.role, u.tier, u.tier_priority, u.url_env, u.auth_bearer_env
          INTO got
          FROM ai_gateway.upstreams u
         WHERE u.name = want.name;
        IF NOT FOUND
           OR got.role            IS DISTINCT FROM want.role
           OR got.tier            IS DISTINCT FROM want.tier
           OR got.tier_priority   IS DISTINCT FROM want.tier_priority
           OR got.url_env         IS DISTINCT FROM want.url_env
           OR got.auth_bearer_env IS DISTINCT FROM want.auth_bearer_env
        THEN
            RAISE EXCEPTION 'phase23 0041: upstream % ausente ou com definicao divergente (%)',
                want.name,
                CASE WHEN NOT FOUND THEN 'ausente'
                     ELSE format('role=%s tier=%s prio=%s url_env=%s auth=%s',
                                 got.role, got.tier, got.tier_priority, got.url_env, got.auth_bearer_env)
                END;
        END IF;
    END LOOP;
END
$$;

-- (d) Override de effort por tenant (HAIKU-05). NULL = sem override.
ALTER TABLE ai_gateway.tenants
    ADD COLUMN IF NOT EXISTS llm_effort TEXT NULL
    CHECK (llm_effort IN ('off', 'low', 'medium', 'high', 'xhigh', 'max'));

COMMENT ON COLUMN ai_gateway.tenants.llm_effort IS
    'Phase 23: override de effort/thinking do LLM por tenant. Precedencia: valor explicito do cliente > tenants.llm_effort > padrao high. NULL = sem override.';

-- Hot-reload: o trigger de UPDATE é filtrado por coluna (0013 → 0016 → 0037).
-- WHEN não é alterável → DROP + CREATE com llm_effort acrescentado.
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
    OR OLD.llm_effort IS DISTINCT FROM NEW.llm_effort
))
EXECUTE FUNCTION ai_gateway.notify_tenants_changed();

-- (e) Units de cache em prices (HAIKU-04). A CHECK de 0012 é inline (nome
-- gerado); localiza pelo catálogo em vez de assumir o nome.
DO $$
DECLARE
    c RECORD;
BEGIN
    FOR c IN
        SELECT conname
          FROM pg_constraint
         WHERE conrelid = 'ai_gateway.prices'::regclass
           AND contype = 'c'
           AND pg_get_constraintdef(oid) ILIKE '%unit%'
    LOOP
        EXECUTE format('ALTER TABLE ai_gateway.prices DROP CONSTRAINT %I', c.conname);
    END LOOP;
END
$$;

ALTER TABLE ai_gateway.prices
    ADD CONSTRAINT prices_unit_check CHECK (unit IN (
        'input_token', 'output_token', 'audio_second', 'embed_request',
        'cache_read_token', 'cache_write_token', 'cache_write_1h_token'
    ));

-- (f) Colunas de cache + origem do custo no billing (pai particionado:
-- ADD COLUMN propaga para as partições criadas via PARTITION OF).
ALTER TABLE ai_gateway.billing_events
    ADD COLUMN IF NOT EXISTS tokens_cache_read  INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tokens_cache_write INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cost_source        TEXT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Ordem reversa exata do Up.

-- (f)
ALTER TABLE ai_gateway.billing_events
    DROP COLUMN IF EXISTS cost_source,
    DROP COLUMN IF EXISTS tokens_cache_write,
    DROP COLUMN IF EXISTS tokens_cache_read;

-- (e) remove preços com units novas antes de restaurar a CHECK original de 0012.
DELETE FROM ai_gateway.prices
 WHERE unit IN ('cache_read_token', 'cache_write_token', 'cache_write_1h_token');

ALTER TABLE ai_gateway.prices DROP CONSTRAINT IF EXISTS prices_unit_check;
ALTER TABLE ai_gateway.prices
    ADD CONSTRAINT prices_unit_check CHECK (unit IN ('input_token', 'output_token', 'audio_second', 'embed_request'));

-- (d) restaura o trigger no formato 0037 ANTES do DROP COLUMN (o WHEN
-- referenciaria uma coluna inexistente).
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

ALTER TABLE ai_gateway.tenants DROP COLUMN IF EXISTS llm_effort;

-- Aliases que apontem para os upstreams desta migration (criados por planos
-- posteriores) saem antes das linhas de upstream.
DELETE FROM ai_gateway.model_aliases
 WHERE upstream_name IN ('anthropic-haiku', 'openrouter-deepseek', 'openrouter-tts');

-- (c) remove openrouter-tts e desfaz o deslocamento −10 em dois passos — só
-- quando a linha existia (espelha a condição do Up).
DO $$
DECLARE
    removed INT;
BEGIN
    DELETE FROM ai_gateway.upstreams WHERE name = 'openrouter-tts';
    GET DIAGNOSTICS removed = ROW_COUNT;
    IF removed > 0 THEN
        UPDATE ai_gateway.upstreams SET tier_priority = tier_priority + 1000
         WHERE role = 'tts' AND tier = 1;
        UPDATE ai_gateway.upstreams SET tier_priority = tier_priority - 1010
         WHERE role = 'tts' AND tier = 1;
    END IF;
END
$$;

-- (b) + (a)
DELETE FROM ai_gateway.upstreams WHERE name IN ('anthropic-haiku', 'openrouter-deepseek');

UPDATE ai_gateway.upstreams SET tier_priority = 0
 WHERE name = 'openrouter-chat' AND role = 'llm' AND tier = 1 AND tier_priority = 10;
-- +goose StatementEnd
