---
quick_id: 261007-sjo
card: 86akttq7f
---
# Quick 261007-sjo: remover `ai_gateway.audit_log.cost_brl`

Decisão Pedro 2026-10-07 (opção 1A). FATOS: writer grava `nil` fixo (writer.go:266, "Phase 4 populates" nunca feito);
0 de 256.552 linhas (30d) e 0 no total com valor; nenhuma query do gateway/dashboard lê; grep em /home/pedro/projetos
sem leitor externo. Custo por requisição vive em `billing_events`. HIPÓTESE residual: leitor externo fora dos repos
(n8n/BI) — `pg_stat_statements` não instalado no bd_ai_gateway, não verificável; mitigação = down migration recria a coluna.

## Task 1 — código
- `gateway/internal/audit/writer.go`: tirar `"cost_brl"` de `auditLogCopyColumns` e o `nil` de `auditLogCopyRow`.
- `gateway/db/migrations/0040_drop_audit_log_cost_brl.sql` (goose): Up `ALTER TABLE ai_gateway.audit_log DROP COLUMN IF EXISTS cost_brl;` Down `ADD COLUMN IF NOT EXISTS cost_brl NUMERIC(10,4)`.
- `gateway/internal/db/gen/models.go`: remover campo `CostBrl` de `AuditLog` (espelha sqlc).
- verify: `gofmt -l gateway`, `go build ./...`, `go vet ./gateway/...`, `go test ./gateway/internal/audit/...`.

## Task 2 — deploy (ordem obrigatória)
1. push develop → CI builda `develop-<sha>`; 2. pré-pull no worker-vm; 3. PUT stack 38 só trocando `image:` (código novo não escreve a coluna, funciona com ela presente);
4. validar health + audit gravando; 5. só então `gatewayctl migrate up` one-off com a imagem nova (DROP). Rollback: `migrate down 1` + `service update --rollback`.
