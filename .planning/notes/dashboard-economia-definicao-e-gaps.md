---
title: Dashboard — definição de economia + decisões e gaps (exploração)
date: 2026-06-26
context: /gsd:explore "melhorias no dashboard"
---

# Dashboard: o que importa de verdade = economia real

## Sinal central da exploração
Das 3 queixas (dados faltantes, gráfico quebrado, sem histórico), o que mais trava o
usuário: **não ter parâmetro real se a GPU própria está economizando de fato vs OpenRouter.**
Esse é o propósito do pod primary existir — e o painel que mediria isso está deferred.

## Fórmula de economia (decidida)
```
economia = soma(custo phantom)  −  custo real Vast GPU
         = quanto pagaria          quanto paguei
           no OpenRouter            alugando GPU
```
- **Phantom** = `cost_local_phantom_brl` em `ai_gateway.billing_events`. Só é gravado
  quando a request foi servida LOCAL (GPU). Quando roteia externo, custo é real-externo,
  não phantom. → somar phantom já = "valor entregue pela GPU", alinhado naturalmente com
  janelas de pod-up.
- **Custo real Vast** = `ai_gateway.primary_lifecycles`: `total_cost_brl` (lifecycles
  fechados) + accrual live (`accepted_dph × horas-desde-started`) p/ o lifecycle aberto.
  Lógica já existe em `gateway/internal/admin/operations.go:220-256`.

## Os 3 números que o usuário quer (lado a lado)
1. **Líquido R$** — phantom − Vast no período (positivo = economizei X)
2. **Recorte janela pod-up** — só horas com pod UP (seg-sex 9-17h BRT)
3. **Multiplicador ROI** — phantom evitado por R$1 de GPU (ex: 3.2x = saudável)
+ gráfico de **economia ao longo do tempo** (série temporal real, eixo X = tempo)

## Blocker técnico (o motivo de estar deferred)
Painel economia hoje nil de propósito: `operations.go:18-21,108,257`
("PhantomMonthBRL intentionally left nil"), `lib/gateway.ts:198,248`,
`operacao-cost-panel.tsx:6`. Falta uma query de **soma phantom gateway-wide** (todos
tenants, sem filtro de tenant). `/admin/usage` é per-tenant; `/consumo` contorna com
fan-out + `Promise.allSettled` — mas o painel ops precisa da soma server-side.

## Decisão sobre confiança no preço
Preço phantom = **confiável, NÃO validar antes**. Histórico: custo já apareceu R$0 por
mismatch de chave (`model.gguf` vs seed); corrigido por daily timer ops-claude que popula
preços OpenRouter + forex. Assumir correto e construir.

## Gaps conhecidos NÃO-prioritários (fora da Phase 15)
- **audio_seconds / embeds_count = 0** — metering não grava. KPIs de áudio/embed no
  `/consumo` vêm zerados. (`HANDOFF-tier3-gpu-metrics.md:27`)
- **Latency chart não é série temporal** — eixo X = rota (categórico), não tempo.
  Só snapshot per-route, zero tendência. → seed separado.
- **Tier 3 GPU/RAM/CPU** — painel de hardware nunca começou. Ver
  `HANDOFF-tier3-gpu-metrics.md` (plano: estender `/admin/operations` com seção `gpu{}`
  lida do DCGMScraper :9400).
- **Custo total do `/consumo`** usa `cost_local_phantom_brl` (phantom), não custo externo real.

## Stack do dashboard (referência)
Next.js 15 / React 19, port 3001, Recharts 2.15.4, TanStack Query, better-auth+2FA.
Proxy read-only `dashboard/src/app/api/gateway/[...path]/route.ts` → gateway `/admin/*`
com `X-Admin-Key`. Dashboard nunca toca Postgres direto.
