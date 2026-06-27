# Phase 15: dashboard-economia-e-historico — Context

**Gathered:** 2026-06-26
**Status:** Ready for planning
**Source:** /gsd:explore (2026-06-26) + inline design resolution of RESEARCH open questions

<domain>
## Phase Boundary

Construir um painel de **Economia** no ifix-ai-dashboard que prova se rodar a GPU própria
(Vast) economiza de verdade vs OpenRouter, e tornar o histórico de incidentes consultável.

- **OBS-09** — painel economia (phantom vs Vast real) com série temporal real.
- **OBS-10** — `/incidents` (audit log) ganha filtro de data + busca + total count.

FORA de escopo (capturado em note/seed/HANDOFF): metering audio_seconds/embeds=0,
latency chart série temporal (SEED-020), Tier 3 GPU/RAM/CPU (HANDOFF-tier3).
</domain>

<decisions>
## Implementation Decisions (LOCKED)

### Fórmula economia
- `economia_líquida = SUM(cost_local_phantom_brl) − custo_real_Vast` por período.
- Phantom só é gravado quando request servida LOCAL (GPU). Quando pod down → fallback
  OpenRouter, custo real-externo, sem phantom.
- Preço phantom = **confiável, NÃO planejar task de validação** (daily timer OpenRouter+forex já popula).

### Endpoint (research recomendado, aceito)
- NOVO `GET /admin/economy?from=&to=` espelhando shape `{range, summary, rows}` do `usage.go`.
- Computa os números + série diária **server-side**. NÃO inflar `/admin/operations`
  (poll 5-10s) com array de série temporal. NÃO fazer client-fan-out por tenant
  (anti-pattern do /consumo que reintroduz partial-failure).
- Blocker destravado: nova query sqlc `SumPhantomAllTenants...` **sem `WHERE tenant_id`**
  (o motivo de `phantom_month_brl` estar nil hoje em operations.go:18-21,257).

### Forex
- Usar constante `cfg.USDToBRLRate` pro accrual Vast ao vivo (consistência com painel
  operations atual). NÃO usar tabela fx_rates.

### Vast cost bucketing
- Reusar accrual exato de `operations.go:214-245`. Atribuir custo de cada lifecycle à
  data BRT do `started_at` (pod roda janelas 9-17h BRT same-day → 1 lifecycle = 1 dia).
  Total do período é exato independente do bucketing.

### UI placement
- **Nova rota `/economia`** (date-range + cadência próprias). Adicionar ao sidebar.
  NÃO embutir em /operacao.

### Números do painel (lado a lado)
Decisão: dropar "recorte janela pod-up" (matematicamente == líquido, pois phantom=0 E
Vast=0 quando pod down). Em vez disso, exibir TODAS as métricas úteis:
1. **Líquido R$** — phantom − Vast no período (positivo = economizei)
2. **ROI multiplier** — phantom evitado por R$1 de GPU (guard divisão-por-zero quando Vast=0)
3. **Custo OpenRouter pago (fallback)** — gasto real externo quando pod estava DOWN
   ("custo de não ter pod 24/7")
4. **% servido local** — fração de requests/tokens servidos pela GPU vs fallback externo
5. **Horas pod UP** — total de horas que o pod rodou no período (contexto)
+ **Gráfico série temporal real** (eixo X = tempo/dia; mirror dual-axis de consumo-trend-chart.tsx)

### OBS-10 — histórico de incidentes
- Estender `ListAuditStateChanges` (+from/to/search params, ILIKE parametrizado) + nova
  `CountAuditStateChanges`. Copiar bloco `Popover`+`Calendar` de `consumo/page.tsx` pro
  `/incidents` page. Retornar `total` pro pager.
</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Research + exploração
- `.planning/phases/15-dashboard-economia-e-historico-from-gsd-explore-2026-06-26/15-RESEARCH.md` — contrato sqlc/handler/wiring, padrão chart, file:line de tudo
- `.planning/notes/dashboard-economia-definicao-e-gaps.md` — fórmula + decisões + ponteiros

### Gateway (Go) — código a clonar
- `gateway/internal/admin/usage.go` — shape `{range, summary, rows}` + query per-tenant billing_events (espelhar para /admin/economy)
- `gateway/internal/admin/operations.go:214-256` — accrual Vast (total_cost_brl + accepted_dph×horas)
- `gateway/internal/admin/audit.go` — handler audit (estender p/ OBS-10)
- `gateway/internal/db/` — camada sqlc gen (onde adicionar SumPhantom + CountAudit)

### Dashboard (Next.js)
- `dashboard/src/lib/gateway.ts` — wrappers tipados (add fetchEconomy)
- `dashboard/src/lib/consumo.ts` — agregação (referência, NÃO reusar o fan-out)
- `dashboard/src/components/consumo-trend-chart.tsx` — padrão dual-axis Recharts a espelhar
- `dashboard/src/components/operacao-cost-panel.tsx` — padrão KPI cards
- `dashboard/src/app/(dashboard)/consumo/page.tsx` — padrão Popover+Calendar date-range (copiar p/ /economia e /incidents)
- `dashboard/src/components/app-sidebar.tsx` — add nav /economia
- `dashboard/src/app/api/gateway/[...path]/route.ts` — proxy (já cobre /admin/* GET)
</canonical_refs>

<specifics>
## Specific Ideas

- Guard divisão-por-zero no ROI (Vast cost=0 → exibir "—" ou ∞ tratado).
- Timezone: pod-up windows America/Sao_Paulo vs billing_events UTC — alinhar bucketing
  (idiom de tz já existe no código, seguir).
- Stack: dev-only, sem spend Vast/GPU (lê dados existentes em billing_events/primary_lifecycles).
</specifics>

<deferred>
## Deferred Ideas

- Latency chart como série temporal → SEED-020 (fase futura, reusa padrão de série temporal desta fase)
- Tier 3 GPU/RAM/CPU → HANDOFF-tier3-gpu-metrics.md
- Metering audio_seconds/embeds=0 → fora de escopo
</deferred>

---

*Phase: 15-dashboard-economia-e-historico*
*Context gathered: 2026-06-26 via /gsd:explore + inline resolution*
