---
title: Filtro de data + busca + total count no /incidents (audit history)
date: 2026-06-26
priority: medium
source: /gsd:explore (dashboard melhorias)
requirement: OBS-10
---

# /incidents: histórico não-navegável

## Problema
Página `/incidents` (audit log de mudanças de estado) hoje só tem pager limit/offset.
Sem filtro de data, sem busca, sem total count → "não consigo consultar histórico".

## Onde
- UI: `dashboard/src/app/(dashboard)/incidents/page.tsx`
- Client: `dashboard/src/lib/gateway.ts:144-148` (`fetchAudit` — sem `total`)
- Handler: gateway `internal/admin/audit.go`
- Tabela: `ai_gateway.audit_log` (`gateway/internal/db/gen/audit.sql.go:45,120`)

## Escopo
- [ ] Handler `/admin/audit` aceita `from`/`to` date range
- [ ] Handler retorna `total` (rodar COUNT) p/ pager mostrar total de páginas
- [ ] Busca por texto (event type / tenant / state)
- [ ] UI: date range picker + search box (reusar padrão do `/consumo` tenant filter)

## Nota
Faz parte da Phase 15 (OBS-10) — pode virar plano dentro dela em vez de todo solto.
Mantido aqui pra não perder o detalhe de implementação.
