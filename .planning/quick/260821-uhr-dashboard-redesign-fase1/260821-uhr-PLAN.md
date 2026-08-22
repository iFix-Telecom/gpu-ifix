---
phase: quick-260821-uhr-dashboard-redesign-fase1
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - dashboard/src/app/globals.css
  - dashboard/src/app/layout.tsx
  - dashboard/src/components/sparkline.tsx
  - dashboard/src/components/sparkline.test.tsx
  - dashboard/src/components/kpi-card.tsx
  - dashboard/src/lib/format.ts
  - dashboard/src/components/pod-overview-panel.tsx
  - dashboard/src/components/pod-overview-panel.test.tsx
  - dashboard/src/components/latency-chart.tsx
  - dashboard/src/components/economy-trend-chart.tsx
  - dashboard/src/app/(dashboard)/page.tsx
  - dashboard/src/lib/consumo.ts
  - dashboard/src/lib/consumo.test.ts
  - dashboard/src/components/tenant-volume-bars.tsx
  - dashboard/src/components/local-vs-external-donut.tsx
  - dashboard/src/components/consumo-trend-chart.tsx
  - dashboard/src/app/(dashboard)/consumo/page.tsx
autonomous: true
requirements: [DASH-RD-F1-01, DASH-RD-F1-02, DASH-RD-F1-03, DASH-RD-F1-04]

must_haves:
  truths:
    - "O dashboard renderiza com a identidade aprovada: fundo navy escuro, accent verde-lima, Space Grotesk nos títulos, Inter no corpo, JetBrains Mono nos números."
    - "A Visão geral tem uma seção 'Por pod' que mostra o pod primário (3090/LLM) E cada pod secundário da conta Vast (hoje o 3060) lado a lado."
    - "A latência por rota é renderizada em barras agrupadas P50/P95/P99 — nenhuma linha liga categorias de rota."
    - "O KPI de custo GPU na Visão geral mostra uma sparkline com a série real R$/dia de /admin/economy."
    - "A tela Consumo mostra 'Top tenants por volume' em barras horizontais, ordenadas por requests reais do período."
    - "A tela Consumo mostra a distribuição local vs externo em donut, alimentada por pct_servido_local / phantom_brl / custo_openrouter_brl de /admin/economy."
    - "A tela Economia mostra custo por dia em barras (vast vs phantom) com a economia líquida sobreposta."
    - "Dias sem registro de billing dentro do período aparecem como LACUNA visível na série de consumo — nunca como zero implícito."
    - "Nenhuma dependência npm nova é adicionada e nenhum endpoint novo do gateway é criado."
  artifacts:
    - path: "dashboard/src/app/globals.css"
      provides: "paleta dark navy + accent lima + tokens categóricos (llm/stt/ext/embed) + status ok/warn/crit + famílias de fonte no @theme"
      contains: "--color-chart-llm"
    - path: "dashboard/src/app/layout.tsx"
      provides: "Space Grotesk / Inter / JetBrains Mono via next/font/google como CSS vars"
      contains: "next/font/google"
    - path: "dashboard/src/components/sparkline.tsx"
      provides: "primitiva SVG de sparkline usada nos KPIs"
      contains: "polyline"
    - path: "dashboard/src/components/pod-overview-panel.tsx"
      provides: "seção 'Por pod' da Visão geral (primário + secundários)"
      contains: "Pod primário"
    - path: "dashboard/src/components/tenant-volume-bars.tsx"
      provides: "top tenants por volume em barras horizontais"
      contains: "requests_count"
    - path: "dashboard/src/components/local-vs-external-donut.tsx"
      provides: "donut local vs externo sobre o summary de /admin/economy"
      contains: "pct_servido_local"
  key_links:
    - from: "dashboard/src/app/(dashboard)/page.tsx"
      to: "/admin/operations"
      via: "fetchOperations() no useQuery da Visão geral"
      pattern: "fetchOperations"
    - from: "dashboard/src/app/(dashboard)/page.tsx"
      to: "/admin/economy"
      via: "fetchEconomy() para custo/dia + sparkline do KPI de custo"
      pattern: "fetchEconomy"
    - from: "dashboard/src/app/(dashboard)/consumo/page.tsx"
      to: "topTenantsByVolume"
      via: "lib/consumo sobre o fan-out de /admin/usage"
      pattern: "topTenantsByVolume"
    - from: "dashboard/src/components/latency-chart.tsx"
      to: "recharts BarChart"
      via: "barras agrupadas P50/P95/P99"
      pattern: "BarChart"
---

<objective>
Aplicar a Fase 1 do redesign aprovado do ai-dashboard (mockup validado com o Pedro em 2026-08-21): nova identidade visual (paleta dark navy + accent lima + 3 famílias de fonte + paleta categórica daltônico-segura) e os painéis novos que já têm dado real no backend — "Por pod" na Visão geral, top tenants por volume, local-vs-externo em donut, e custo/dia em barras.

Purpose: o dashboard hoje mostra só o pod primário, usa linha ligando categorias de rota (leitura errada) e não expõe quem consome o quê. O mockup aprovado resolve isso com dados que JÁ existem em `/admin/metrics`, `/admin/operations`, `/admin/usage` e `/admin/economy`.
Output: dashboard redesenhado nas 3 telas do mockup (Visão geral, Consumo, Economia), zero endpoint novo no gateway, zero dependência npm nova.

FORA DE ESCOPO (Fase 2, NÃO planejar/implementar aqui): séries temporais de latência e de taxa de erro (o gateway só tem janela de 5 min — exigiria persistir snapshots), "OpenRouter por modelo" (o gateway não tem gasto por modelo), qualquer mudança em Go/gateway, qualquer deploy.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/quick/260821-uhr-dashboard-redesign-fase1/mockup.html
@dashboard/src/app/globals.css
@dashboard/src/app/layout.tsx
@dashboard/src/components/kpi-card.tsx
@dashboard/src/components/latency-chart.tsx
@dashboard/src/components/economy-trend-chart.tsx
@dashboard/src/components/consumo-trend-chart.tsx
@dashboard/src/components/operacao-secondary-pods-panel.tsx
@dashboard/src/lib/consumo.ts
@dashboard/src/app/(dashboard)/page.tsx
@dashboard/src/app/(dashboard)/consumo/page.tsx

<interfaces>
<!-- Contratos que o executor precisa. Extraídos do código atual — NÃO explorar o codebase pra redescobrir. -->

De `dashboard/src/lib/gateway.ts` (mirror field-for-field dos handlers Go — NÃO inventar campos):

MetricsResponse { window, fsm_state, tenants: TenantMetricRow[], inflight: InflightRow[] }
TenantMetricRow { tenant_id, tenant_slug|null, tenant_name|null, route, p50, p95, p99, requests, error_rate }
  → `route` assume EXATAMENTE 3 valores em produção (audit/middleware.go routeTemplate):
    "/v1/chat/completions" | "/v1/embeddings" | "/v1/audio/transcriptions"
InflightRow { upstream, inflight }

OperationsResponse { fsm, schedule, lifecycles, breakers, vast_cost, secondary_pods }
OperationsFSM { primary_state, emerg_state, active_lifecycle_id, active_instance_id, is_leader }
OperationsBreaker { upstream, state }              // state: closed|half-open|open|forced-open
OperationsVastCost { today_brl, month_brl, budget_brl, budget_pct_used, phantom_month_brl? }
OperationsSecondaryPod { id, gpu_name, num_gpus, status, label, dph_brl, uptime_seconds }

EconomyResponse { range, summary, series }
  summary { phantom_brl, vast_brl, economia_liquida_brl, roi_multiplier|null,
            custo_openrouter_brl, pct_servido_local|null, horas_pod_up }
  series: EconomyDayRow[] { date, phantom_brl, vast_brl, economia_brl }

UsageResponse.summary { tokens_in, tokens_out, audio_seconds, embeds_count,
  cost_local_brl, cost_local_phantom_brl, cost_external_brl, cost_total_brl, requests_count }
UsageResponse.rows[]  — mesmos campos + `date`

Fetchers já existentes (todos passam pelo proxy `/api/gateway/*`, sem admin key no cliente):
  fetchMetrics(window?), fetchOperations(), fetchEconomy(from, to), fetchUsage(tenant, from, to)

Helpers já existentes:
  `@/lib/format`: formatMs, formatErrorRate, formatBrl, formatCount, formatUptime,
                  errorRateTier, latencyTier, aggregateP95, aggregateErrorRate, aggregateRequests
  `@/lib/fsm`: tierTextClass(tier), StatusTier = healthy|warning|critical|neutral
  `@/components/operacao-fsm-panel`: primaryStateClass(state), primaryStateLabel(state)  ← EXPORTADOS, reutilizar
  `@/lib/consumo`: aggregateSummary, aggregateDaily, perTenantRows
</interfaces>

<gotchas>
- Contextos de servidor (RSC / server actions) usam `fetchPodConfigServer` com URL absoluta + cookie encaminhado, NUNCA o proxy relativo. Todas as telas deste plano são `"use client"` com `useQuery` → usar os fetchers de `@/lib/gateway` normalmente. NÃO transformar nenhuma dessas páginas em Server Component.
- `next lint`/eslint NÃO está instalado neste app (sem config, sem binário). O gate de lint é `npx tsc --noEmit`.
- O app é dark-only: `<html className="dark">` fixo em layout.tsx. Só o bloco `.dark` do globals.css é renderizado; NÃO gastar esforço no bloco `:root` claro.
- Tailwind v4: tokens declarados em `@theme inline` viram utilitários. `--font-display` → `font-display`, `--color-chart-llm` → `text-chart-llm`/`bg-chart-llm`/`fill-chart-llm`.
- `/admin/metrics` é uma janela rolante de 5 min — NÃO tem histórico. Qualquer sparkline de latência/erro seria dado inventado. Só o KPI de custo ganha sparkline (série real de `/admin/economy`).
- O pod secundário (3060) é gerenciado por script externo (`ops/vast-3060/vast3060.py`); o gateway só sabe o que a API da Vast devolve (`secondary_pods`). NÃO atribuir rota/latência a ele — esse vínculo não existe no dado.
</gotchas>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Identidade visual — tokens, fontes e primitiva de sparkline</name>
  <files>dashboard/src/app/globals.css, dashboard/src/app/layout.tsx, dashboard/src/components/sparkline.tsx, dashboard/src/components/sparkline.test.tsx, dashboard/src/components/kpi-card.tsx</files>
  <action>
Repintar o design system para a identidade aprovada no mockup, sem tocar em nenhum componente de página (todos herdam via token).

globals.css — no bloco `.dark`, remapear os tokens shadcn existentes para a paleta do mockup (hex direto é aceitável; não converter para oklch): `--background` #0b0f16, `--card` e `--popover` #141a24, `--secondary`/`--muted`/`--accent` #1b222e, `--border` #263041, `--input` #263041, `--foreground` #eef2f7, `--muted-foreground` #aab6c6, `--primary` #a3e635, `--primary-foreground` #0b0f16 (tinta escura sobre o lima — contraste), `--ring` #a3e635, `--sidebar` #141a24, `--sidebar-border` #263041, `--sidebar-primary` #a3e635, `--sidebar-primary-foreground` #0b0f16, `--sidebar-accent` #1b222e, `--sidebar-accent-foreground` #eef2f7, `--radius` 0.875rem (14px do mockup). Status separado do accent: `--status-ok` #35c07a, `--status-warning` #e0a92b, `--destructive` #e05555.

Adicionar a paleta categórica validada para daltonismo: `--chart-llm` #3d8fe0, `--chart-stt` #c9791e, `--chart-ext` #9d5fe0, `--chart-embed` #1f9e6d. Repontar `--chart-1..--chart-5` para llm/embed/stt/ext/accent respectivamente, para que gráficos existentes que ainda usam `--chart-N` herdem a paleta nova em vez do verde monocromático atual.

No bloco `@theme inline`, expor: `--color-chart-llm`, `--color-chart-stt`, `--color-chart-ext`, `--color-chart-embed`, `--color-status-ok`, e as famílias `--font-sans: var(--font-inter)`, `--font-display: var(--font-space-grotesk)`, `--font-mono: var(--font-jetbrains-mono)`. Em `@layer base`, fazer `h1, h2, h3` usarem `font-family: var(--font-display)` e `body` manter `font-family: var(--font-sans)` com `font-variant-numeric: tabular-nums`.

layout.tsx — carregar as três fontes com `next/font/google` (self-hosting nativo do Next; NÃO adicionar pacote nem `<link>` para fonts.googleapis.com): `Space_Grotesk` (weights 500/600/700, variable `--font-space-grotesk`), `Inter` (400/500/600, variable `--font-inter`), `JetBrains_Mono` (500/600, variable `--font-jetbrains-mono`), todas com `subsets: ["latin"]` e `display: "swap"`. Aplicar as três `.variable` no `className` do `<html>` mantendo a classe `dark`. Se `npm run build` falhar buscando as fontes (sem egress para fonts.googleapis.com no ambiente de build), PARAR e reportar — não improvisar CDN em runtime sem avisar.

sparkline.tsx — componente puro e minúsculo (sem recharts): props `{ points: number[]; color?: string; className?: string }`. Renderiza `<svg viewBox="0 0 120 26" preserveAspectRatio="none">` com uma `<polyline>` normalizada (min→max mapeado no eixo Y invertido) e um `<circle>` no último ponto. `color` default `"var(--primary)"`. Com menos de 2 pontos, retorna `null` (dado insuficiente não vira gráfico). Adicionar `aria-hidden="true"` — é ornamento do KPI, o número é a informação.

kpi-card.tsx — adicionar props opcionais `series?: number[]` e `seriesColor?: string`; aplicar `font-mono` (além do `tabular-nums` que já existe) ao valor; renderizar `<Sparkline>` abaixo do `hint` quando `series` tiver 2+ pontos. Caption vira `text-[11.5px] uppercase tracking-wide` conforme mockup. Nenhum consumidor atual quebra (props novas são opcionais).

sparkline.test.tsx — cobrir: (a) com 5 pontos renderiza um `polyline`; (b) com 1 ponto ou array vazio não renderiza nada.
  </action>
  <verify>
    <automated>cd dashboard && npx tsc --noEmit && npm test && npm run build</automated>
  </verify>
  <done>tsc limpo, vitest verde (incluindo os 2 testes novos de sparkline), `npm run build` conclui com as 3 fontes resolvidas; `grep -c 'chart-llm' src/app/globals.css` > 0.</done>
</task>

<task type="auto">
  <name>Task 2: Visão geral — seção "Por pod", latência em barras agrupadas e custo/dia</name>
  <files>dashboard/src/lib/format.ts, dashboard/src/components/pod-overview-panel.tsx, dashboard/src/components/pod-overview-panel.test.tsx, dashboard/src/components/latency-chart.tsx, dashboard/src/components/economy-trend-chart.tsx, dashboard/src/app/(dashboard)/page.tsx</files>
  <action>
format.ts — adicionar `isoDate(d: Date): string` (componentes LOCAIS, YYYY-MM-DD — o mesmo helper que consumo/page.tsx e economia/page.tsx já definem localmente; round-trip por `toISOString()` desloca o dia) e `currentMonthRange(): { from: string; to: string }` (dia 1 → hoje). NÃO refatorar as duas páginas existentes para importar daqui — fora de escopo, evita mexer nos testes delas.

pod-overview-panel.tsx (novo, `"use client"`) — props `{ operations: OperationsResponse; metrics: MetricsResponse }`. Grid de 2 colunas (1 no mobile) no formato dos cards `.pod` do mockup (barra de 3px colorida na borda esquerda):
  - Card do primário: barra `--chart-llm`, título "Pod primário — LLM", badge de estado via `primaryStateLabel(operations.fsm.primary_state)` + `primaryStateClass(...)` (importados de `@/components/operacao-fsm-panel`). Campos, em grid de 3 colunas com rótulo 10.5px uppercase e valor `font-mono`: Instância (`#${fsm.active_instance_id}` ou "—" quando 0), Custo hoje (`formatBrl(vast_cost.today_brl)`), Custo mês (`formatBrl(vast_cost.month_brl)` com `budget_pct_used` como sublabel), Breaker (estado do upstream `local-llm` em `operations.breakers`, ou "—" se ausente), P95 e Requests da rota `/v1/chat/completions` derivados de `metrics.tenants` (máximo de p95 entre tenants dessa rota; soma de `requests`).
    O rótulo dos dois últimos campos DEVE dizer "rota /chat (todos upstreams)" — a rota é servida também pelo fallback externo quando o pod está dormindo; atribuir essa latência ao pod seria mentira.
  - Um card por item de `operations.secondary_pods`: barra `--chart-stt`, título "Pod secundário", badge com o `status` cru da Vast (reaproveitar o mapeamento de classes de `operacao-secondary-pods-panel.tsx`: running→primary, loading/scheduling→warning, exited/offline→destructive, resto→muted), campos `gpu_name ×num_gpus`, Rótulo (`label`), Custo (`formatBrl(dph_brl)/h`), No ar há (`formatUptime(uptime_seconds)`), `#id`. NÃO exibir rota, P95 nem requests nesses cards — o gateway não tem esse vínculo para pods externos.
  - `secondary_pods` vazio → um card único com "Nenhum outro pod ativo."

latency-chart.tsx — trocar `LineChart` por `BarChart` com três `<Bar>` agrupadas (p50/p95/p99), `radius={[4,4,0,0]}`, mantendo o `chartConfig` semântico atual (P50 `--primary`, P95 `--status-warning`, P99 `--destructive`) e o eixo X com `dataKey="key"` (a rota). Manter tooltip e legenda do bloco `chart`. Comentar no topo do arquivo POR QUE virou barra: percentis são categorias por rota — linha ligando rotas sugere uma continuidade inexistente (regra #4 do mockup aprovado).

economy-trend-chart.tsx — trocar `LineChart` por `ComposedChart`: `<Bar dataKey="vast_brl" fill="var(--color-vast_brl)">` e `<Bar dataKey="phantom_brl" fill="var(--color-phantom_brl)">` agrupadas por dia, mais `<Line dataKey="economia_brl">` sobreposta (dia é eixo temporal — linha é legítima aqui). Repintar o `chartConfig`: `vast_brl` → `var(--chart-llm)`, `phantom_brl` → `var(--chart-ext)`, `economia_brl` → `var(--primary)`.

(dashboard)/page.tsx — adicionar dois `useQuery` independentes ao lado do de métricas (falha de um NÃO pode apagar a tela): `["operations"] → fetchOperations()` e `["economia-overview", range] → fetchEconomy(range.from, range.to)` com `range = currentMonthRange()` calculado uma vez fora do render (`useState(() => currentMonthRange())`).
  - Subir a linha de KPIs para 4 cards no formato do mockup: Requests (`aggregateRequests`, hint `${inflight} em voo`), P95 latência (`aggregateP95` + `latencyTier`), Taxa de erro (`aggregateErrorRate` + `errorRateTier`), Custo GPU hoje (`vast_cost.today_brl`, hint `mês ${formatBrl(month_brl)} · budget ${budget_pct_used}%`) com `series` = últimos 14 `vast_brl` de `economy.series` e `seriesColor="var(--chart-llm)"`.
  - Os três primeiros KPIs NÃO recebem sparkline. Comentar no código que a janela de 5 min de `/admin/metrics` não tem histórico e que séries de latência/erro são Fase 2 (exigem persistir snapshots) — não inventar série.
  - Inserir a seção "Por pod" (label de seção estilo mockup) entre os KPIs e a tendência, renderizando `<PodOverviewPanel>` quando a query de operations resolver; enquanto carrega, `Skeleton`; se falhar, uma linha discreta "Não foi possível carregar o estado dos pods." (a tela continua útil).
  - Manter o card de latência por rota; remover o `FsmPanel` da Visão geral apenas se ele ficar redundante com o card do primário — SE remover, deletar também o import (tsc pega import órfão).

pod-overview-panel.test.tsx — cobrir: (a) renderiza o rótulo do estado do primário; (b) renderiza um card por pod secundário (2 pods → 2 ids visíveis); (c) `secondary_pods: []` → "Nenhum outro pod ativo."
  </action>
  <verify>
    <automated>cd dashboard && npx tsc --noEmit && npm test && npm run build</automated>
  </verify>
  <done>tsc limpo, vitest verde (incluindo os 3 casos novos do painel de pods), build ok; `grep -c 'BarChart' src/components/latency-chart.tsx` > 0 e `grep -c 'fetchOperations' 'src/app/(dashboard)/page.tsx'` > 0.</done>
</task>

<task type="auto">
  <name>Task 3: Consumo — top tenants, local vs externo e lacuna visível na série</name>
  <files>dashboard/src/lib/consumo.ts, dashboard/src/lib/consumo.test.ts, dashboard/src/components/tenant-volume-bars.tsx, dashboard/src/components/local-vs-external-donut.tsx, dashboard/src/components/consumo-trend-chart.tsx, dashboard/src/app/(dashboard)/consumo/page.tsx</files>
  <action>
lib/consumo.ts — adicionar:
  - `export type TenantModality = "llm" | "stt" | "embed"` e `export interface TenantVolumeRow { tenant_id, label, requests_count, modality }`.
  - `export function topTenantsByVolume(responses: UsageResponse[], limit = 10): TenantVolumeRow[]` — ordena por `summary.requests_count` desc, corta em `limit`, label com fallback name → slug → id (mesma regra de `perTenantRows`). Classificação de modalidade DERIVADA do dado, sem chute: `audio_seconds > 0 && tokens_in + tokens_out === 0` → "stt"; `embeds_count > 0 && tokens_in + tokens_out === 0 && audio_seconds === 0` → "embed"; caso contrário → "llm".
  - `export function fillDateGaps(rows: DailyAggRow[], from: string, to: string): Array<{ date: string; tokens: number | null; cost_brl: number | null }>` — enumera todos os dias YYYY-MM-DD do intervalo (aritmética em UTC sobre `Date.UTC` para não pular dia por fuso) e emite `null` (NÃO zero) nos dias sem linha de billing. É isso que faz a lacuna aparecer como buraco no gráfico em vez de um vale falso.

tenant-volume-bars.tsx (novo, `"use client"`) — barras horizontais em CSS puro (sem recharts): grid `150px 1fr 64px` por linha (label truncado em `font-mono`, trilho `bg-secondary` com preenchimento proporcional a `requests_count / max`, número à direita com `formatCount`). Cor do preenchimento pela modalidade: llm → `bg-chart-llm`, stt → `bg-chart-stt`, embed → `bg-chart-embed`. Legenda no topo com as três cores. Lista vazia → "Sem dados no período."

local-vs-external-donut.tsx (novo, `"use client"`) — props `{ summary: EconomyResponse["summary"] }`. Donut em SVG (dois `<circle>` com `stroke-dasharray`/`stroke-dashoffset`, igual ao mockup): fatia local = `pct_servido_local`, fatia externa = `1 - pct_servido_local`; centro mostra a % local. Legenda lateral: "Pod local (grátis)" com `formatBrl(phantom_brl)` rotulado como economizado, e "OpenRouter (externo)" com `formatBrl(custo_openrouter_brl)`. Quando `pct_servido_local` for `null` (denominador zero no servidor), renderizar "Sem dados no período." e NENHUM donut — nada de 0%/NaN.

consumo-trend-chart.tsx — aceitar valores nulos (`rows: Array<{ date: string; tokens: number | null; cost_brl: number | null }>`) e adicionar `connectNulls={false}` nas duas `<Line>`, para que os dias sem billing fiquem visivelmente vazios. Repintar `chartConfig`: tokens → `var(--chart-llm)`, cost_brl → `var(--chart-ext)`.

(dashboard)/consumo/page.tsx — sem mexer no fan-out existente de `/admin/usage`:
  - Acrescentar `topTenants: topTenantsByVolume(responses)` ao retorno do `queryFn` e passar a série por `fillDateGaps(aggregateDaily(responses), applied.from, applied.to)` antes de entregar ao gráfico.
  - Adicionar um `useQuery` SEPARADO `["economia-consumo", applied] → fetchEconomy(applied.from, applied.to)` — separado de propósito: se `/admin/economy` falhar, só o donut some, a tela continua.
  - Adicionar um KPI "Requests" com `formatCount(summary.requests_count)` na linha de KPIs (o dado já vem no summary e hoje é descartado).
  - Novo card "Top tenants por volume" com `<TenantVolumeBars>`; novo card "Local vs externo" com `<LocalVsExternalDonut>` (ou o estado de indisponível quando a query de economy falhar).
  - Sob o gráfico de tendência, uma linha de hint: dias sem nenhum registro de billing aparecem como lacuna, não como zero — pode indicar incidente de ingestão no período.

consumo.test.ts (novo) — cobrir: (a) `topTenantsByVolume` ordena por requests desc e respeita o `limit`; (b) classifica stt/embed/llm pelas três combinações de campos; (c) `fillDateGaps` insere `null` nos dias ausentes e preserva os presentes, com o comprimento igual ao número de dias do intervalo.
  </action>
  <verify>
    <automated>cd dashboard && npx tsc --noEmit && npm test && npm run build</automated>
  </verify>
  <done>tsc limpo, vitest verde (incluindo os casos novos de consumo.test.ts), build ok; `grep -c 'topTenantsByVolume' 'src/app/(dashboard)/consumo/page.tsx'` > 0 e `grep -c 'connectNulls' src/components/consumo-trend-chart.tsx` > 0.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| browser → dashboard Next.js | sessão better-auth; nenhuma admin key pode existir em bundle de cliente |
| dashboard server → gateway `/admin/*` | proxy server-side é o ÚNICO lugar que lê `GATEWAY_ADMIN_KEY` |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-RDF1-01 | Information Disclosure | componentes/páginas novos | mitigate | Todo fetch usa os wrappers de `@/lib/gateway` (proxy `/api/gateway/*`). NENHUM arquivo deste plano importa `gateway-admin.ts` nem lê `process.env.GATEWAY_ADMIN_KEY`. Gate: `grep -rn "GATEWAY_ADMIN_KEY" dashboard/src` continua restrito a `route.ts` + `gateway-admin.ts`. |
| T-RDF1-02 | Elevation of Privilege | telas de observabilidade | accept | Só leitura; nenhuma server action, nenhuma mutação, nenhum gate owner/operator alterado. |
| T-RDF1-03 | Tampering | superfície de dado exibido | mitigate | Nenhum endpoint novo, nenhum campo inventado: todo número renderizado vem de um campo existente em `/admin/{metrics,operations,usage,economy}`. Denominadores nulos renderizam "—", nunca 0/NaN. |
| T-RDF1-SC | Tampering | dependências npm | mitigate | ZERO pacote novo (`package.json` não é modificado). `next/font/google` já faz parte do `next` instalado. Gate: `git diff --stat dashboard/package.json dashboard/package-lock.json` vazio ao final. |
</threat_model>

<verification>
Automático (roda no fim de cada task, obrigatório na task 3):
```
cd dashboard && npx tsc --noEmit && npm test && npm run build
git diff --stat dashboard/package.json dashboard/package-lock.json   # deve sair vazio
grep -rn "GATEWAY_ADMIN_KEY" dashboard/src                            # só route.ts + gateway-admin.ts
```

Verificação humana (o mockup é a fonte de verdade visual — comparar lado a lado):
1. `cd dashboard && npm run dev` → http://localhost:3001 (login com a conta owner existente).
2. Visão geral: fundo navy, accent lima, títulos em Space Grotesk, números em JetBrains Mono; 4 KPIs com sparkline SÓ no de custo; seção "Por pod" com o 3090 e o 3060 lado a lado; latência em barras agrupadas (nenhuma linha entre rotas).
3. Consumo: "Top tenants por volume" em barras horizontais com cores por modalidade; donut local vs externo com os números de economia; dias sem billing aparecendo como lacuna na tendência.
4. Economia: custo/dia em barras (vast vs phantom) com a economia líquida sobreposta.
5. Conferir contraste/legibilidade dos badges de status (ok/warn/crit) contra o novo fundo.

Deploy NÃO faz parte deste plano (stack Portainer 40 / imagem GHCR seguem a receita existente).
</verification>

<success_criteria>
- As 3 telas do mockup (Visão geral, Consumo, Economia) renderizam com a paleta, as 3 fontes e os painéis aprovados.
- "Por pod" mostra primário + todo pod secundário da conta Vast, sem atribuir rota/latência a pod externo.
- Latência por rota em barras agrupadas P50/P95/P99.
- Top tenants por volume, donut local-vs-externo e custo/dia em barras, todos sobre campos reais dos endpoints existentes.
- Dias sem billing = lacuna visível, não zero.
- `npx tsc --noEmit`, `npm test` e `npm run build` verdes; zero dependência nova; zero mudança em Go.
</success_criteria>

<output>
Create `.planning/quick/260821-uhr-dashboard-redesign-fase1/260821-uhr-SUMMARY.md` when done.
</output>
</content>
</invoke>
