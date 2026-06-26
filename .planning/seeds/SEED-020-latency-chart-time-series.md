---
title: Latency chart como série temporal (eixo = tempo, não rota)
trigger_condition: quando alguém precisar de tendência histórica de latência (debug de regressão, capacity planning, ou após Phase 13 estabelecer o padrão de série temporal no dashboard)
planted_date: 2026-06-26
source: /gsd:explore (dashboard melhorias)
---

# SEED-020: Latency chart vira série temporal

## Problema
`dashboard/src/components/latency-chart.tsx` ("Visão geral") plota P50/P95/P99 com
**eixo X = `route` (categórico)**, não tempo. Cada rota é um tick. Resultado: zero
tendência histórica — só o snapshot atual de percentis por rota da janela rolante de 5min.
Usuário não consegue ver "latência piorou nas últimas 2h".

## Causa raiz
`/admin/metrics` (`gateway/internal/admin/metrics.go`) computa percentis de uma janela
rolante de 5min do `audit_log` via `percentile_cont` — retorna um snapshot, não buckets
temporais.

## O que seria preciso
- Gateway: endpoint que retorna percentis **bucketed por tempo** (ex: P95 por hora nas
  últimas 24h), agregando `audit_log` por `date_trunc`.
- UI: trocar eixo X do `latency-chart` para tempo; opcionalmente seletor de janela.

## Por que seed e não fase
Não é o número que importa hoje (economia é). A Phase 13 já estabelece o padrão de
série temporal real (gráfico de economia) — depois disso, replicar pra latência é barato.
Reaproveitar o mesmo padrão de bucketing temporal.

## Relacionado
- Phase 13 (OBS-09) — série temporal de economia (estabelece o padrão)
- [[dashboard-economia-definicao-e-gaps]]
