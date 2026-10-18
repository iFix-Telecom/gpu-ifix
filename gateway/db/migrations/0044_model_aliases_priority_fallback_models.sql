-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick 261018 — ordem por alias + fallback nativo de modelo do OpenRouter.
--
-- priority (INT, opcional): ordem dos candidatos de UM alias pinado. Quando
-- ALGUMA linha do alias tem priority não-nula, o dispatcher (dispatchPinned)
-- tenta os upstreams desse alias em priority ASC (nulos por último, empate
-- pela ordem global (tier, tier_priority) de ai_gateway.upstreams). Todas
-- nulas = comportamento idêntico ao anterior (ordem global). As regras de
-- tier-0/emergency e RES-08 (tenant sensitive nunca vai a tier>=1) não mudam.
--
-- fallback_models (TEXT[], opcional): só para openrouter-chat /
-- openrouter-deepseek. Quando presente, o director injeta
-- "models": [target, ...fallback_models] no corpo (parâmetro nativo de
-- fallback de modelo do OpenRouter), mantendo "model" = target. Validação
-- (escopo de upstream, formato dos slugs) em gatewayctl e na API admin.
--
-- Colunas novas são NULL-able e sem default: as linhas existentes ficam
-- intactas e nenhum alias de outro cliente muda de rota.
ALTER TABLE ai_gateway.model_aliases
    ADD COLUMN IF NOT EXISTS priority INT;

ALTER TABLE ai_gateway.model_aliases
    ADD COLUMN IF NOT EXISTS fallback_models TEXT[];

COMMENT ON COLUMN ai_gateway.model_aliases.priority IS
    'quick 261018: ordem do upstream dentro do alias pinado (ASC, nulos por último, empate pela ordem global tier/tier_priority). Todas nulas no alias = ordem global.';

COMMENT ON COLUMN ai_gateway.model_aliases.fallback_models IS
    'quick 261018: lista de fallback de modelo do OpenRouter; o director injeta "models": [target, ...fallback_models]. Só vale para upstream_name openrouter-chat / openrouter-deepseek. NULL = sem fallback.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;

ALTER TABLE ai_gateway.model_aliases DROP COLUMN IF EXISTS fallback_models;
ALTER TABLE ai_gateway.model_aliases DROP COLUMN IF EXISTS priority;
-- +goose StatementEnd
