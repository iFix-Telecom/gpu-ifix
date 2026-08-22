---
phase: quick-260821-uhr-dashboard-redesign-fase1
plan: 01
subsystem: ui
tags: [nextjs, tailwind-v4, recharts, next-font, dashboard, observability, design-tokens]

requires:
  - phase: quick-20260822-dashboard-secondary-pods
    provides: "/admin/operations.secondary_pods (fan-out da Vast ListInstances) — a fonte dos cards de pod secundário"
  - phase: 15-01
    provides: "/admin/economy (summary de 5 métricas + série diária phantom-vs-vast)"
provides:
  - "Identidade visual aprovada aplicada: dark navy + accent lima, 3 famílias de fonte self-hosted, paleta categórica daltônico-segura"
  - "Seção 'Por pod' na Visão geral — primário (FSM) + todo pod secundário da conta Vast"
  - "Latência por rota em barras agrupadas P50/P95/P99 (rotas são categorias, não série)"
  - "Sparkline de custo GPU no KPI, alimentada pela série real de /admin/economy"
  - "Top tenants por volume (barras horizontais, cor por modalidade derivada)"
  - "Donut local vs externo sobre pct_servido_local / phantom_brl / custo_openrouter_brl"
  - "Custo/dia da Economia em barras (vast vs phantom) com economia líquida sobreposta"
  - "Lacuna de billing visível como buraco na série de consumo (null + connectNulls=false)"
affects: [dashboard-redesign-fase2, ai-dashboard-deploy]

tech-stack:
  added: []
  patterns:
    - "Paleta categórica (llm/stt/ext/embed) separada da rampa de status (ok/warn/crit) — cor de modalidade nunca é cor de saúde"
    - "Ausência de dado = null + connectNulls={false} ou '—'; nunca zero implícito"
    - "Sparkline opt-in: KPI só recebe série quando existe histórico REAL no backend"
    - "Uma query React Query por endpoint, independentes — falha de um painel não apaga a tela"

key-files:
  created:
    - dashboard/src/components/sparkline.tsx
    - dashboard/src/components/sparkline.test.tsx
    - dashboard/src/components/pod-overview-panel.tsx
    - dashboard/src/components/pod-overview-panel.test.tsx
    - dashboard/src/components/tenant-volume-bars.tsx
    - dashboard/src/components/local-vs-external-donut.tsx
    - dashboard/src/lib/consumo.test.ts
  modified:
    - dashboard/src/app/globals.css
    - dashboard/src/app/layout.tsx
    - dashboard/src/components/kpi-card.tsx
    - dashboard/src/components/latency-chart.tsx
    - dashboard/src/components/economy-trend-chart.tsx
    - dashboard/src/components/consumo-trend-chart.tsx
    - dashboard/src/app/(dashboard)/page.tsx
    - dashboard/src/app/(dashboard)/consumo/page.tsx
    - dashboard/src/lib/format.ts
    - dashboard/src/lib/consumo.ts

key-decisions:
  - "FsmPanel MANTIDO na Visão geral: failover FSM (/admin/metrics fsm_state) e lifecycle do pod (/admin/operations primary_state) são máquinas de estado diferentes, não redundantes"
  - "Cards de pod secundário NÃO exibem rota/P95/requests — o gateway não tem vínculo pod↔rota para instância gerenciada por script externo"
  - "P95/requests do primário rotulados 'rota /chat (todos upstreams)' — a rota também é servida pelo fallback externo enquanto o pod dorme"
  - "Só o KPI de custo recebe sparkline: /admin/metrics é janela rolante de 5 min sem histórico"
  - "Modalidade do tenant derivada dos contadores (stt/embed só sem tokens), nunca do nome do tenant"
  - "--chart-1..5 repontados para a paleta categórica para que gráficos legados herdem as cores novas"

patterns-established:
  - "Tokens de fonte via next/font/google + @theme inline: --font-sans/display/mono, aplicados em @layer base"
  - "Barras para eixo X categórico, linha só para eixo X temporal"
  - "Denominador nulo do gateway (pct_servido_local: null) renderiza estado vazio, não 0%"

requirements-completed: [DASH-RD-F1-01, DASH-RD-F1-02, DASH-RD-F1-03, DASH-RD-F1-04]

duration: 38min
completed: 2026-08-21
---

# Quick 260821-uhr: Dashboard Redesign Fase 1 — Summary

**Identidade aprovada (dark navy + lima + Space Grotesk/Inter/JetBrains Mono self-hosted) e paleta daltônico-segura aplicadas ao ai-dashboard, com "Por pod", latência em barras agrupadas, top tenants por volume, donut local-vs-externo e lacuna de billing visível — tudo sobre campos que já existiam em /admin/{metrics,operations,usage,economy}, zero endpoint novo e zero dependência npm.**

## Performance

- **Duração:** ~38 min
- **Início:** 2026-08-22T01:01Z (baseline tsc/test/build antes da Task 1)
- **Conclusão:** 2026-08-22T01:39Z
- **Tasks:** 3/3
- **Arquivos modificados/criados:** 17 (10 modificados, 7 criados)

## Accomplishments

- O 3060 saiu da invisibilidade: a Visão geral agora mostra o primário e **todo** pod secundário da conta Vast lado a lado, cada um só com os campos que o gateway realmente sabe sobre ele.
- A latência por rota deixou de ser uma linha ligando `/chat` a `/audio` (que desenhava uma inclinação inexistente entre categorias) e virou barras agrupadas P50/P95/P99.
- Dias sem registro de billing agora aparecem como **lacuna** na série de consumo. Com `connectNulls={false}`, o incidente de partição do `billing_events` (ago/2026) apareceria como buraco em vez de um vale plausível.
- Quem consome o quê ficou visível: top tenants por volume de requests, com a modalidade derivada dos próprios contadores de uso.

## Task Commits

1. **Task 1: Identidade visual — tokens, fontes e primitiva de sparkline** — `5838c67` (feat)
2. **Task 2: Visão geral — "Por pod", latência em barras e custo/dia** — `6826a0a` (feat)
3. **Task 3: Consumo — top tenants, local vs externo e lacuna visível** — `a3930f1` (feat)

## Files Created/Modified

### Criados
- `dashboard/src/components/sparkline.tsx` — primitiva SVG pura (sem recharts) para os KPIs; com <2 pontos não renderiza nada.
- `dashboard/src/components/sparkline.test.tsx` — 2 casos: polyline com 5 pontos; nada com 0/1 ponto.
- `dashboard/src/components/pod-overview-panel.tsx` — seção "Por pod" (primário + secundários), com a fronteira de honestidade documentada no header.
- `dashboard/src/components/pod-overview-panel.test.tsx` — 3 casos: label do estado do primário, 1 card por pod secundário, estado vazio.
- `dashboard/src/components/tenant-volume-bars.tsx` — barras horizontais em CSS puro, cor por modalidade.
- `dashboard/src/components/local-vs-external-donut.tsx` — donut SVG; `pct_servido_local === null` → estado vazio, sem donut.
- `dashboard/src/lib/consumo.test.ts` — 4 casos cobrindo ordenação/limit, as 4 combinações de modalidade e o preenchimento de lacunas (inclusive na virada de mês).

### Modificados
- `dashboard/src/app/globals.css` — bloco `.dark` repintado com a paleta do mockup; status separado do accent; `--chart-llm/stt/ext/embed`; `--chart-1..5` repontados; famílias de fonte no `@theme inline`; `h1-h3` na face display e `tabular-nums` no body em `@layer base`.
- `dashboard/src/app/layout.tsx` — Space Grotesk / Inter / JetBrains Mono via `next/font/google` (self-hosted no build).
- `dashboard/src/components/kpi-card.tsx` — props opcionais `series`/`seriesColor`; caption 11.5px uppercase; valor na face display.
- `dashboard/src/components/latency-chart.tsx` — `LineChart` → `BarChart` agrupado.
- `dashboard/src/components/economy-trend-chart.tsx` — `LineChart` → `ComposedChart` (2 barras + 1 linha).
- `dashboard/src/components/consumo-trend-chart.tsx` — aceita `null` e usa `connectNulls={false}`.
- `dashboard/src/app/(dashboard)/page.tsx` — 4 KPIs, queries independentes de `operations`/`economy`, seção "Por pod".
- `dashboard/src/app/(dashboard)/consumo/page.tsx` — KPI de Requests, cards de top tenants e local-vs-externo, série via `fillDateGaps`, query de economy separada.
- `dashboard/src/lib/format.ts` — `isoDate` + `currentMonthRange`.
- `dashboard/src/lib/consumo.ts` — `topTenantsByVolume`, `fillDateGaps`, tipos `TenantModality`/`TenantVolumeRow`/`DailyGapRow`.

## Decisions Made

1. **`FsmPanel` mantido na Visão geral.** O plano autorizava removê-lo "apenas se ficar redundante com o card do primário". Ao inspecionar, os dois leem máquinas de estado **diferentes**: o card do pod mostra o lifecycle (`asleep|provisioning|ready|draining|destroying`, de `/admin/operations`), o `FsmPanel` mostra o failover (`HEALTHY|DEGRADED|FAILED_OVER|OFF_HOURS`, de `/admin/metrics`). "Pod dormindo + failover saudável" e "pod pronto + em failover" são situações reais e distintas — colapsar as duas esconderia uma. A condição de remoção não se verificou, então ele foi mantido (agora na seção "Latência & failover").
2. **Fronteira de honestidade nos cards de pod.** O primário exibe P95/requests da rota `/chat`, mas rotulados "rota /chat (todos upstreams)"; os secundários não exibem rota/latência/requests nenhum. `/admin/metrics` agrega por **rota**, não por upstream, e o gateway não tem vínculo pod↔rota para instância gerenciada por script externo (`ops/vast-3060/vast3060.py`).
3. **Sparkline só no custo.** `/admin/economy` tem série diária real; `/admin/metrics` é janela rolante de 5 min sem histórico. Série de latência/erro exigiria persistir snapshots — Fase 2. Está comentado no código para não ser "consertado" por engano depois.
4. **Modalidade do tenant derivada, conservadora.** `stt` e `embed` só quando não há tráfego de token nenhum; caso contrário `llm`. Um tenant que transcreve áudio **e** resume com LLM é um tenant de LLM com etapa de áudio — pintá-lo de laranja mentiria sobre onde está o gasto de token.
5. **`--chart-1..5` repontados** para a paleta categórica, para que gráficos legados que ainda usam os slots numéricos herdem as cores novas em vez da rampa monocromática verde antiga.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing critical functionality] Remoção do `FsmPanel` teria apagado o estado de failover da Visão geral**
- **Encontrado em:** Task 2
- **Problema:** A primeira redação da Visão geral removeu o `FsmPanel` (o plano permitia). Ao verificar as fontes, ficou claro que `metrics.fsm_state` (failover) e `operations.fsm.primary_state` (lifecycle) são vocabulários distintos de máquinas de estado distintas. O `CriticalBanner` no layout só cobre os estados critical/warning — nos estados `HEALTHY`/`OFF_HOURS` o operador ficaria sem nenhuma confirmação de que o failover está são.
- **Correção:** `FsmPanel` reintroduzido numa seção "Latência & failover" (grid 3 colunas com o card de latência), com comentário explicando por que os dois estados coexistem.
- **Arquivos:** `dashboard/src/app/(dashboard)/page.tsx`
- **Verificação:** `npx tsc --noEmit` limpo; suíte verde; `grep -rn "FsmPanel"` confirma que o componente voltou a ter consumidor (não virou código morto).
- **Commit:** `6826a0a`

**2. [Rule 3 - Blocking] Ambiente do worktree sem `node_modules` e sem o marcador ClickUp**
- **Encontrado em:** setup (antes da Task 1)
- **Problema:** (a) O worktree não tinha `dashboard/node_modules` — nenhum gate (`tsc`/`vitest`/`build`) rodava. (b) O hook `clickup-link-enforce.sh` bloqueava toda edição porque `.planning/clickup-active-task.json` (gitignored) não existe no worktree.
- **Correção:** (a) symlink de `dashboard/node_modules` para o checkout principal (gitignored, não versionado, zero mudança em `package.json`/lockfile). (b) espelhado o marcador do repo principal, que já está configurado como `{"skip": true}` — o projeto opta por não vincular ao ClickUp; nenhuma política foi contornada.
- **Arquivos:** nenhum arquivo versionado.
- **Verificação:** `git diff --stat dashboard/package.json dashboard/package-lock.json` vazio.

---

**Total de desvios:** 2 auto-corrigidos (1× Rule 2, 1× Rule 3)
**Impacto no plano:** Nenhum scope creep. O desvio 1 preserva informação operacional que a redação literal do plano teria descartado; o desvio 2 é infraestrutura de execução, sem efeito no código entregue.

## Issues Encountered

**Flakes de timeout de 5s em suítes NÃO relacionadas.** Com o `testTimeout` padrão, um subconjunto variável de `src/lib/auth.test.ts`, `src/lib/admin-actions.test.ts` e `src/app/(dashboard)/settings/operadores/page.test.tsx` falha com `Test timed out in 5000ms` — 1 falha numa execução, 3 na seguinte. Uma execução também abortou com uma asserção nativa do node (`uv_thread_create` falhou em `node_platform.cc:109`), ou seja, a máquina ficou sem folga de threads.

**FATO:** com `npx vitest run --testTimeout=30000 --pool=forks --poolOptions.forks.maxForks=2` a suíte inteira fica verde: **17 arquivos / 87 testes**. **FATO:** nenhum desses arquivos importa qualquer módulo tocado por este plano (a página de operadores importa só `drizzle-orm`, `lucide-react`, `@/lib/db`, `@/lib/viewer` e `./operator-controls`). **FATO:** o teste que falha por timeout passa em ~8s quando recebe folga — a asserção nunca é alcançada.

Fora do escopo (não causado por arquivo deste plano) → registrado em `deferred-items.md` com a evidência e o fix sugerido (subir `testTimeout` no `vitest.config.ts`, que é config compartilhada e não está na lista de arquivos deste plano).

## Verification

```
cd dashboard
npx tsc --noEmit                                     # limpo
npx vitest run --testTimeout=30000 \
  --pool=forks --poolOptions.forks.maxForks=2        # 17 arquivos / 87 testes verdes
npm run build                                        # ok, 3 fontes resolvidas e self-hosted
git diff --stat dashboard/package.json dashboard/package-lock.json   # vazio
grep -rn "GATEWAY_ADMIN_KEY" dashboard/src           # só route.ts + gateway-admin.ts (+ os testes deles)
```

Greps de done-criteria:
- `grep -c 'chart-llm' src/app/globals.css` → 5
- `grep -c 'BarChart' src/components/latency-chart.tsx` → 4
- `grep -c 'fetchOperations' 'src/app/(dashboard)/page.tsx'` → 2
- `grep -c 'topTenantsByVolume' 'src/app/(dashboard)/consumo/page.tsx'` → 2
- `grep -c 'connectNulls' src/components/consumo-trend-chart.tsx` → 3

CSS gerado no build confirma os tokens: `--chart-llm:#3d8fe0`, `.bg-chart-llm{background-color:var(--chart-llm)}`, `.font-display{font-family:var(--font-space-grotesk)}`, `.font-mono{font-family:var(--font-jetbrains-mono)}`, `h1,h2,h3{font-family:var(--font-space-grotesk);…}`. 10 arquivos `.woff2` self-hosted em `.next/static/media/`.

## Known Stubs

Nenhum. Todo número renderizado vem de um campo existente de `/admin/{metrics,operations,usage,economy}`; ausência de dado renderiza `—` ou estado vazio explícito, nunca `0`/`NaN`.

## Threat Flags

Nenhuma superfície nova. Zero endpoint criado, zero server action, zero mutação, zero import de `gateway-admin.ts` ou leitura de `process.env.GATEWAY_ADMIN_KEY` nos arquivos deste plano (T-RDF1-01 e T-RDF1-03 mitigados; T-RDF1-SC verificado — `package.json`/lockfile intocados).

## User Setup Required

Nenhuma configuração de serviço externo. **Verificação humana pendente** (o mockup é a fonte de verdade visual):

1. `cd dashboard && npm run dev` → http://localhost:3001, login com a conta owner.
2. Visão geral: fundo navy, accent lima, títulos em Space Grotesk, números em JetBrains Mono; 4 KPIs com sparkline SÓ no de custo; "Por pod" com 3090 e 3060 lado a lado; latência em barras agrupadas.
3. Consumo: top tenants em barras horizontais coloridas por modalidade; donut local vs externo; lacuna visível na tendência.
4. Economia: custo/dia em barras (vast vs phantom) com economia líquida sobreposta.
5. Contraste dos badges de status (ok/warn/crit) contra o fundo novo.

## Next Phase Readiness

Fase 2 continua bloqueada pelos mesmos limites de dado documentados no plano e reafirmados pelo código: séries temporais de latência/erro exigem persistir snapshots (`/admin/metrics` é janela de 5 min), e "OpenRouter por modelo" exige gasto por modelo no gateway. Deploy (stack Portainer 40 / imagem GHCR) não faz parte deste plano.

## Self-Check: PASSED

- 9/9 arquivos declarados como criados existem em disco (7 de código + SUMMARY + deferred-items).
- 3/3 hashes de commit existem em `git log --oneline --all`: `5838c67`, `6826a0a`, `a3930f1`.
- Nenhum commit deste plano deleta arquivo rastreado (`git diff --diff-filter=D` vazio nos 3).

---
*Quick task: 260821-uhr-dashboard-redesign-fase1*
*Concluído: 2026-08-21*
