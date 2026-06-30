# Phase 17: Dashboard pod-config control — Context

**Gathered:** 2026-06-30
**Status:** Ready for planning

<domain>
## Phase Boundary

Superfície **owner-only** no ai-dashboard pra **ler + editar a config HOT do pod
primário** (DB-backed `pod_config`, hot-reload via Postgres LISTEN/NOTIFY que o
reconciler relê) + um **painel de status de provisioning ao vivo**. Mata o loop
`ssh n8n-ia-vm → sed .env → docker compose recreate` que era a dor de toda
mudança de blocklist/cap/schedule (incidente 2026-06-29).

**O que esta fase NÃO faz** (cortado na discussão — ver D-01/D-02):
- NÃO edita campos estruturais (shape/image/weights/llama-args) — esses ficam
  env-only, exibidos read-only.
- NÃO implementa o endpoint `POST /admin/gateway/restart` (self-restart) — fora
  de escopo, porque sem edição estrutural não há o que aplicar via restart.
- NÃO toca docker socket, NÃO edita `.env`, NÃO muda apps clientes.

Single-replica prod confirmado (RESEARCH A1 resolvido) — não há skew de réplica
a tratar.
</domain>

<decisions>
## Implementation Decisions

### Escopo dos campos editáveis (Área 1)
- **D-01:** **Apenas os 16 campos HOT** são editáveis no dashboard. Os 19
  ESTRUTURAIS (GPU shape #18-21, images #22-25, weights key+SHA #26-34, llama
  args #35, timezone #17) ficam **env-only, exibidos READ-ONLY** no painel
  (transparência/diagnóstico). Mudança estrutural continua via redeploy/env
  normal (que recria o container). Razão: editar SHA de weights num form web é
  raro + perigoso (lockstep MinIO/imagem); o ganho da fase é o hot-reload da dor
  diária. **Mata RESEARCH Pitfall 2 (mid-lifecycle structural edit) e Pitfall 5
  (multi-replica restart skew) inteiros.**
- **D-02:** `pod_config` guarda **só os 16 hot fields**, seedados do env no
  primeiro boot (env permanece como default/fallback — NÃO remover do Portainer
  stack). Estruturais **NÃO entram** em `pod_config`; o display read-only lê do
  `cfg`/`/admin/operations` via a API existente. **Sem self-restart endpoint**
  nesta fase. Loader hot-reload = espelhar `upstreams.Loader` verbatim
  (LISTEN/NOTIFY + `atomic.Pointer[Snapshot]` + last-good-on-error).

### Bounds de validação (Área bounds / A3)
- **D-03:** Os **bounds (min/max) de cada hot field são eles próprios
  configuráveis no dashboard** — NÃO CHECK constraints hardcoded. O owner
  redefine o envelope de segurança. **Seed** dos bounds = os defaults da
  RESEARCH (cap $0.10–$1.50, coldstart 300–5400s, port-bind 30–600s,
  failure-cooldown 60–1800s, budget R$0–100k, UpHour/DownHour 0–23, grace
  0–1800s, lead 0–7200s, days subset `{mon..sun}`), depois editáveis.
  **Implicação de escopo:** ~dobra a superfície de config (cada hot field + seu
  par min/max). Planner decide o storage (ex: colunas `*_min`/`*_max` no
  `pod_config`, ou tabela `pod_config_bounds` seedada dos defaults). Operator
  continua **read-only** sobre os bounds também (só owner edita o envelope).
- **D-03a:** Validação em 2 camadas mantida: o valor do campo é validado contra
  o bound *corrente* (server-side, antes de salvar). Timezone (#17) permanece
  estrutural — não há tz editável, então sem o risco de fail-fast `LoadLocation`
  chegar no reconciler.

### Força do confirm em ações perigosas (Área 2)
- **D-04:** **Dialog simples (um clique) + warning específico de impacto.** SEM
  type-to-confirm. O texto do modal carrega o peso: descreve o efeito concreto
  ("isso vai drenar o pod rodando agora" para estreitar-schedule-com-pod-up;
  "cap abaixo do mercado pode impedir o provisioning" para cap-down). Aplica-se
  a: cap-down, estreitar schedule (DownHour passa "now"), `Disabled=true`, days
  vazio, allowlist que excluiria todos os hosts conhecidos. Confirm é UX
  anti-fat-finger, NÃO camada de segurança (a segurança é `requireOwner`
  server-side).

### Painel de status ao vivo (Área 3)
- **D-05:** Painel = **FSM state corrente + a lifecycle ABERTA com event trail**
  (`offer_accepted → health checks → ready`, ou `shutdown_reason` na falha).
  Poll de `GET /admin/primary/lifecycle`. Nível mínimo que diagnostica o flap de
  bad-host pela UI (caso motivador) sem virar tela de histórico. NÃO incluir a
  tabela de N-últimas-lifecycles com custo/tendência (fica pra fase futura se
  precisar). Reusar `admin.OperationsHandler` como template do novo endpoint.

### Onde audita (Área 4)
- **D-06:** **`admin_audit_log` dashboard-side APENAS** (tabela Phase 13).
  `action="pod_config.update"`, `metadata={field, old, new}`. SEM dual-write no
  `audit_log` particionado do gateway — preserva o isolamento Phase 13
  (dashboard NUNCA escreve no schema `ai_gateway`). A edição nasce dashboard-side
  (server action owner-gated) → lar natural é o `admin_audit_log`. Auditar toda
  edição de config + toda edição de bound.

### Authz / write path (carregado da arch + Phase 13)
- **D-07:** Edições via **Next.js Server Action owner-gated** (`requireOwner`
  server-side, Phase 13) que chama a admin API do gateway diretamente. O
  admin-proxy `route.ts` (Phase 15, GET-only) **permanece read-only** — usado só
  pelo poll do painel ao vivo. Writes NÃO passam pelo proxy. Operator = read-only
  em tudo (config, bounds, painel).

### Claude's Discretion
- Storage exato dos bounds (colunas `*_min`/`*_max` vs tabela `pod_config_bounds`)
  — planner decide na RESEARCH-driven plan.
- Schema `pod_config` single-row vs key-value — RESEARCH recomenda single-row
  typed; planner decide (com bounds editáveis, KV pode ficar mais natural).
- Limpeza do env morto `PRIMARY_POD_SERVE_STT` no Portainer stack durante deploy
  — opcional, planner decide.
- Layout/UX exato dos forms e do painel — seguir o padrão de UI já estabelecido
  no dashboard (Phase 13/15).
</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Arquitetura travada + research da fase
- `.planning/notes/dashboard-pod-config-control-architecture.md` — decisões LOCKED §1-4 (env→DB, híbrido hot/estrutural, self-restart, live-status, guardrails). NOTA: D-01/D-02 desta fase RESTRINGEM o escopo locked (estrutural read-only, sem self-restart agora).
- `.planning/phases/17-dashboard-pod-config-control-owner-controla-todas-as-configs/17-RESEARCH.md` — **Config Surface Table (centerpiece)**: 35 settings, 16 HOT / 19 STRUCTURAL, load-site + consumption-site + valor prod de cada. Bounds table, pitfalls, migration concern (seed env→DB), A1-resolvido addendum.

### Fases anteriores (reuse anchors)
- `.planning/phases/13-dashboard-user-management-gest-o-de-operadores-owner-only-se/13-CONTEXT.md` — owner/operator authz (D-01..D-09), `admin_audit_log`, server-action + `requireOwner`, isolamento dashboard↛ai_gateway.
- `.planning/phases/15-dashboard-economia-e-historico-from-gsd-explore-2026-06-26/` — admin-proxy route + `lib/gateway.ts` wrappers (Phase 15).

### Gateway — código a refatorar / espelhar
- `gateway/internal/upstreams/loader.go:39,103-196` — **padrão de hot-reload a espelhar verbatim** (LISTEN/NOTIFY + atomic snapshot + last-good-on-error).
- `gateway/db/migrations/0009_upstreams_notify_trigger.sql` — template do NOTIFY trigger (`notify_pod_config_changed` + AFTER INSERT/UPDATE/DELETE). Head atual = 0030, próxima = 0031.
- `gateway/internal/config/config.go:356-682` — env-at-boot fail-fast (o seam refatorado; env permanece como seed/fallback).
- `gateway/internal/primary/reconciler.go:1194-1258,1579,416,1333,1389` — offer-selection + budgets/timeouts (reads HOT a trocar pelo snapshot).
- `gateway/internal/primary/lifecycle.go:239-242,333` — struct `Reconciler{cfg, rule}` (onde injetar `r.podCfg atomic.Pointer`).
- `gateway/internal/primary/schedule.go:95-160` — `ParseScheduleEnv` (re-parse do `rule` no NOTIFY).
- `gateway/internal/primary/budget.go:60-120` — `MonthlyPrimaryBudgetBRL`.
- `gateway/internal/admin/operations.go:117-216` — **template do novo endpoint** `/admin/primary/lifecycle` (já lê rec+cfg+lifecycles).
- `gateway/internal/db/gen/primary_lifecycles.sql.go` — queries `GetOpenPrimaryLifecycle`/`ListPrimaryLifecycles` p/ o painel.

### Dashboard — superfícies de reuse
- `dashboard/src/lib/admin-actions.ts:117,156` — `requireOwner` + `writeAuditLog`.
- `dashboard/src/lib/schema-custom.ts:25` — schema `adminAuditLog`.
- `dashboard/src/app/api/gateway/[...path]/route.ts` — admin-proxy (GET-only; fica read-only p/ o poll).
- `dashboard/src/lib/gateway.ts`, `dashboard/src/lib/fsm.ts` — fetch helpers + FSM rendering.
</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `upstreams.Loader` (LISTEN/NOTIFY + atomic snapshot): copiar como `podconfig.Loader`.
- Migration `0009` NOTIFY trigger: template direto pro `pod_config_changed`.
- `admin.OperationsHandler`: já agrega FSM + cfg + lifecycles → base do `/admin/primary/lifecycle`.
- `requireOwner` + `writeAuditLog` + `adminAuditLog` schema (Phase 13): authz + audit prontos.
- Phase 15 `lib/gateway.ts` + admin-proxy: poll do painel ao vivo.

### Established Patterns
- Env-at-boot fail-fast (`config.Load`) permanece intacto = seed/fallback; `pod_config` (16 hot) sobrepõe quando a row existe.
- Provisioning captura config no spawn do goroutine (reconciler.go:1149) → edit hot vale na PRÓXIMA provision/tick, in-flight não afetada (seguro, é o comportamento desejado do blocklist-append).
- Dashboard NUNCA escreve no schema `ai_gateway` (D-06 mantém).

### Integration Points
- Novo `podconfig.Loader` + `ListenAndReload` goroutine no `main.go` (espelha o `LISTEN upstreams_changed`).
- Reconciler troca ~13 reads HOT de `r.cfg`/`r.rule` por `r.podCfg.Load()`.
- Novo endpoint `GET /admin/primary/lifecycle` no `adminRouter` (chi, X-Admin-Key gated).
- Migration `0031_create_pod_config.sql` + seed env→DB idempotente + sqlc regen.
</code_context>

<specifics>
## Specific Ideas

- Caso motivador concreto: o flap de bad-host 2026-06-29 (blocklist-append via
  SSH+sed). O painel ao vivo (D-05) tem que tornar ESSE cenário diagnosticável
  pela UI (ver `shutdown_reason` = `no_offers_below_cap` / bad-host retries).
- Bounds configuráveis (D-03) é decisão deliberada do owner: o envelope de
  segurança não é fixo no código — owner ajusta cap-min/cap-max e demais ranges
  conforme o mercado Vast muda (ex: teto $1.50 hoje cobre 5090, mas pode mudar).
</specifics>

<deferred>
## Deferred Ideas

- **Self-restart endpoint + edição de campos estruturais** (shape/image/weights):
  cortados de Phase 17 (D-01/D-02). Quando houver necessidade real de mudar shape
  pela UI, retomar com `POST /admin/gateway/restart` + confirm+restart-gate (arch
  note §3). Fica como fase futura.
- **Histórico de lifecycles com custo/tendência** no painel (tabela das N
  últimas, accepted_dph, total_cost_brl): D-05 ficou no mínimo diagnóstico.
  Futuro.
- **Dual-write no gateway `/admin/audit`** (visão unificada de auditoria):
  rejeitado por ora (D-06) pra preservar isolamento. Reconsiderar se um painel de
  audit unificado virar requisito.
- **SEED-020 allowlist policy** (preference vs hard-filter): é mudança de LÓGICA
  de provisioning no reconciler (reconciler.go:1206-1259), NÃO de config-control.
  Fora de escopo (RESEARCH Open Q6). Fase própria.

### Reviewed Todos (not folded)
None — nenhum todo casou com o escopo desta fase.
</deferred>

---

*Phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs*
*Context gathered: 2026-06-30*
