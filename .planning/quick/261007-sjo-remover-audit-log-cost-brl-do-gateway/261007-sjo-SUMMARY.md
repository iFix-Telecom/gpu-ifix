---
quick_id: 261007-sjo
status: complete
card: 86akttq7f
---
# 261007-sjo — drop `ai_gateway.audit_log.cost_brl`

## FATOS
- Coluna nunca teve valor: 0 de 256.552 linhas (30d), 0 no total (bd_ai_gateway, 2026-10-07). Nenhuma query/tela lê; grep em ~/projetos sem leitor externo.
- Código (a363ff6): writer não nomeia mais a coluna no COPY; migration 0040 (DROP COLUMN IF EXISTS / Down recria NUMERIC(10,4)); model sqlc sem CostBrl; testes ajustados (writer, embed count, HEAD bumps Down(n) em 0026/0029/0038/0039). Unit + integração (local Docker e CI) verdes.
- Deploy: stack 38 `develop-9101882` → `develop-a363ff6@sha256:27306b8d…` (pré-pull, PUT Env completo 84 vars). Healthy, 0 ERROR, chat 200, audit gravando.
- Migration 0040 aplicada após o deploy (goose version 40, 503 ms); coluna ausente; audit continua gravando; 0 erros.
- Backups: scratchpad `s38.bak-sjo.json` / `s38-file.bak-sjo.yml`. Rollback: `gatewayctl migrate down 1` (recria coluna) + `docker service update --rollback ai-gateway-prod_gateway` (nessa ordem).

## HIPÓTESES
- HIPÓTESE: nenhum leitor externo fora dos repos (n8n/BI) consultava a coluna — `pg_stat_statements` não está instalado no bd_ai_gateway, não verificável. Se aparecer erro "column cost_brl does not exist" em algum consumidor, `migrate down 1`.
