---
quick_id: 261007-gyq
clickup: 86aktfct6
type: quick
---

# 261007-gyq — price-sync auto-descobre modelos sem preço + /modelos na ordem de roteamento

## Contexto (fatos levantados em 2026-10-07)
- Custo é match EXATO `{model, provider, unit}` em `ai_gateway.prices` (`gateway/internal/billing/cost.go`). Sem linha → custo 0 + WARN `price missing` + métrica `gateway_prices_missing`.
- O job diário `scripts/price-sync/gateway-price-sync.sh` (systemd user timer em ops-claude, instalado em `/home/pedro/bin/gateway-price-sync.sh`) só sincroniza um `MODEL_MAP` fixo. O padrão do OpenRouter mudou para `deepseek/deepseek-v4.1-flash` em 29/09 e nunca foi adicionado → 35k chamadas com custo 0 (já corrigido à mão e com backfill hoje).
- **O script do repo está DESATUALIZADO** em relação ao instalado: o instalado chama `ssh worker-vm "docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl"`; o do repo ainda chama `ssh n8n-ia-vm docker exec ifix-ai-gateway` (VM desligada). Partir do instalado.
- `/modelos` (dashboard) e `gatewayctl model-alias list` mostram as linhas de alias na ordem de `gateway/db/queries/model_aliases.sql` (`ORDER BY alias, upstream_name` = alfabética). O roteamento real segue `ai_gateway.upstreams` por `tier, tier_priority` (ex.: whisper: local-stt 0/0 → gemini-stt 1/10 → groq-whisper 1/15 → openai-whisper 2/20).

## Task 1 — price-sync: auto-descoberta de modelos OpenRouter sem preço
Arquivos: `scripts/price-sync/gateway-price-sync.sh` (+ teste/dry-run).
1. Trazer para o repo o wrapper `gatewayctl()` do instalado (`/home/pedro/bin/gateway-price-sync.sh`, worker-vm + container dinâmico) e o comentário WHERE atualizado.
2. Além do `MODEL_MAP` fixo (mantido, ele cobre `model.gguf` e chaves com data), descobrir automaticamente os modelos de chat roteados pelo OpenRouter usados nos últimos 7 dias:
   - Fonte: banco do gateway, `SELECT DISTINCT model FROM ai_gateway.billing_events WHERE upstream='openrouter-chat' AND ts > now()-interval '7 days'`. Acesso: no worker-vm, `docker run --rm -e D="$DSN" postgres:16-alpine psql "$D" -Atc ...` com o DSN lido do env do container do gateway (`AI_GATEWAY_PG_DSN` via `docker inspect`) — sem gravar DSN em disco nem em log.
   - Para cada modelo descoberto que exista com `.id` idêntico em `GET https://openrouter.ai/api/v1/models`, gravar `input_token`/`output_token` com provider `openrouter-fireworks` (mesmo `gatewayctl prices set` já usado). Modelo ausente no OpenRouter → WARN no log + contador `models_unmatched`, não falha.
   - Não duplicar chaves já cobertas pelo `MODEL_MAP`.
   - Linha final de log inclui `models_discovered=N models_unmatched=M`.
3. Manter fail-safe (API fora não sobrescreve), `DRY_RUN=1` e idempotência.
4. Validar com `DRY_RUN=1 bash scripts/price-sync/gateway-price-sync.sh` (deve listar `deepseek/deepseek-v4.1-flash` como descoberto) e `bash -n` + `shellcheck` se disponível.
5. NÃO instalar em `/home/pedro/bin` (o orquestrador instala depois do merge).

## Task 2 — ordem de roteamento nas listas de alias
Arquivos: `gateway/db/queries/model_aliases.sql`, código sqlc gerado (`gateway/internal/db/gen/`), dashboard `/modelos`.
1. `ListModelAliases`: `LEFT JOIN ai_gateway.upstreams u ON u.name = m.upstream_name` e `ORDER BY m.alias, u.tier NULLS LAST, u.tier_priority NULLS LAST, m.upstream_name`. Colunas retornadas inalteradas (sem mudar o JSON da API). Regenerar com sqlc (mesma versão/configuração do repo; o CI verifica codegen).
2. Dashboard `modelos-controls.tsx`: adicionar coluna "Tier" (de `upstreams` já carregados na página, por nome) na tabela de aliases e ordenar no cliente pela mesma regra (alias, tier, tier_priority, nome) — defensivo caso a API antiga responda. Texto curto explicando que a ordem = ordem de tentativa.
3. Testes: teste existente de model_aliases (gateway) ajustado/novo assertando a ordem por tier; dashboard: lint/typecheck/teste do componente se houver padrão.

## Gates
- `cd gateway && gofmt -l . && go build ./... && go vet ./... && go test ./...`
- sqlc codegen limpo (mesmo comando do CI `build-gateway`).
- `cd dashboard && bun run lint && bun run typecheck` (ou os scripts equivalentes do package.json) + testes existentes.
- Commits atômicos por task, mensagens em inglês terminando com `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Sem push/deploy.
