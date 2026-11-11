-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- quick 17u65kmjaet — teto de entrada por upstream (circuit_config.max_tokens_in).
--
-- O Haiku 5.5 cobra 5× a ENTRADA acima de 100.000 tokens (FATO, doc viva
-- platform.claude.com/docs/en/about-claude/pricing: $0,10→$0,50/MTok acima de
-- 100k). Acima desse teto compensa PULAR o anthropic-haiku e seguir pro próximo
-- tier-1 da cascata (openrouter-chat → openrouter-deepseek). O dispatcher
-- (cascadeTier1) lê este campo de forma GENÉRICA: qualquer upstream com
-- max_tokens_in > 0 é pulado quando a entrada estimada excede o teto, sem contar
-- falha de breaker. Nada hardcoded "haiku" no caminho de decisão.
--
-- Data-only (circuit_config JSONB já existe): aditivo, sem ALTER. O UPDATE de
-- circuit_config dispara o trigger upstreams_update_notify (0038) →
-- upstreams_changed → o loader recarrega o snapshot sem restart. Semeia SÓ a
-- linha anthropic-haiku; todos os demais upstreams ficam sem teto (0/ausente =
-- ilimitado, comportamento inalterado).
UPDATE ai_gateway.upstreams
   SET circuit_config = jsonb_set(
           COALESCE(circuit_config, '{}'::jsonb),
           '{max_tokens_in}',
           '100000'::jsonb,
           true)
 WHERE name = 'anthropic-haiku';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway, public;
UPDATE ai_gateway.upstreams
   SET circuit_config = circuit_config - 'max_tokens_in'
 WHERE name = 'anthropic-haiku';
-- +goose StatementEnd
