---
status: partial
phase: 15-dashboard-economia-e-historico
source: [15-VERIFICATION.md]
started: 2026-06-26
updated: 2026-06-26
---

## Current Test

[awaiting human testing]

## Tests

### 1. Renderização visual /economia
expected: 5 KPI cards mostram valores reais (ou "—" quando null: ROI/% local com Vast=0), e o gráfico de tendência BRL com 3 linhas renderiza corretamente no browser com dados live do gateway. Rota /economia acessível pelo sidebar.
result: [pending]

### 2. Filtros interativos /incidents
expected: Calendar picker abre, search box filtra linhas, pager mostra `{from}–{to} de {total}`, offset reseta ao mudar filtro.
result: [pending]

## Summary

total: 2
passed: 0
issues: 0
pending: 2
skipped: 0
blocked: 0

## Gaps
