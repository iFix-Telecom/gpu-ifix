---
phase: 17
slug: dashboard-pod-config-control-owner-controla-todas-as-configs
status: draft
shadcn_initialized: true
preset: radix-nova
created: 2026-06-30
inherits_from: 13-UI-SPEC.md
baseline_prototype: dashboard/src/app/(dashboard)/operacao/page.tsx (Phase 15, live — FSM panel + lifecycle timeline)
---

# Phase 17 — UI Design Contract

> Visual and interaction contract for **Phase 17 — dashboard pod-config control** (owner controls the primary pod's HOT config + bounds + a live provisioning-status panel) on the standalone `dashboard/` Next.js 15 app.
>
> **Visual baseline LOCKED — this phase does NOT introduce a new visual language.** It reuses the design system shipped by Phase 07/11/13 (settings surfaces) and Phase 15 (the `(dashboard)` route group: `operacao`, `economia`, `consumo`). All tokens (spacing, typography, color, components) are inherited verbatim; Phase 17 only adds new owner-gated edit affordances and one live-status panel, consistent with the existing pattern.
>
> **Source of truth for the live pattern this phase extends:** the already-shipped, live `dashboard/src/app/(dashboard)/operacao/page.tsx` + `operacao-fsm-panel.tsx` + `operacao-lifecycle-timeline.tsx` (read 2026-06-30). These render FSM state + a lifecycle table from `/admin/operations` and are the de-facto reference for Phase 17's read-only displays. The owner-gated EDIT pattern is inherited verbatim from `settings/operadores/operator-controls.tsx` (Phase 13: `requireOwner` server action + `dialog`/`alert-dialog` + sonner + inline spinner).

---

## Scope guardrails (from 17-CONTEXT.md — honored exactly)

- **D-01 / D-02:** ONLY the **16 HOT fields** are editable. The **19 STRUCTURAL fields** (GPU shape, images, weights key+SHA, llama args, timezone) are **READ-ONLY DISPLAY**. There is **NO self-restart button/endpoint and NO structural-edit form** in this phase. Do not design either.
- **D-03:** The **validation bounds (min/max) of each hot field are themselves editable** (owner re-defines the safety envelope). The UI therefore has TWO owner-editable surfaces: (a) the **config-value editor** and (b) the **bounds editor**. Operator = read-only on both.
- **D-04:** Dangerous hot actions use a **simple one-click `alert-dialog` confirm** carrying a SPECIFIC impact warning string. **NO type-to-confirm.** Confirm is anti-fat-finger UX, NOT a security layer (security is `requireOwner` server-side).
- **D-05:** Live-status panel = **current FSM state + the OPEN lifecycle event trail** (`offer_accepted → health checks → ready`, or `shutdown_reason` on failure), polling `GET /admin/primary/lifecycle`. NOT a full history table.
- **D-06:** Audit is dashboard-side only (`admin_audit_log`, `action="pod_config.update"` / `"pod_config_bounds.update"`). No audit-VIEWER UI this phase; a small "última alteração por/em" hint per surface is the only audit affordance.
- **D-07:** Owner edits via Next.js **server action** (`requireOwner`). The Phase 15 admin-proxy (`api/gateway/[...path]/route.ts`) stays **read-only** — used only by the live-panel poll. Operator = read-only across config, bounds, and panel.

---

## Phase 17 net-new screens / states

Per CONTEXT.md (D-01..D-07). **Four surfaces, twelve states.** All live on a NEW owner-aware page in the `(dashboard)` route group (placement below).

**Surface A — Config HOT editor (16 fields, grouped)** — owner-edit / operator read-only:
1. **Config view (read-only default)** — three grouped cards (Seleção de oferta · Orçamentos e timeouts · Agenda), each field rendered as label + current value, matching the `operacao-fsm-panel` `Field` idiom.
2. **Field edit mode (owner)** — clicking the per-field pencil swaps the value for the typed input control + `Salvar` / `Cancelar` (inline, per field).
3. **Field saving (pending)** — `Salvar` disabled + inline 14×14 spinner ("Salvando…").
4. **Field save success** — sonner toast + value updates in place; edit mode closes.
5. **Field validation error (out of current bound / bad format)** — inline 12px `--destructive` error under the field; input stays open.
6. **Dangerous-field confirm** — `alert-dialog` with a specific impact string (D-04) before the server action commits. Applies to: cap-down, schedule-narrow-while-pod-up, `Disabled=true`, empty Days, allowlist excluding all known hosts.

**Surface B — Bounds editor (min/max per numeric hot field)** — owner-edit / operator read-only:
7. **Bounds view (read-only default)** — a dense `table`: Campo | Mín | Máx (seeded from RESEARCH defaults).
8. **Bounds edit (owner)** — per-cell inline edit of Mín / Máx (number input + Salvar/Cancelar); same pending/success/error states as Surface A.

**Surface C — Live provisioning-status panel (D-05)** — read-only for BOTH roles:
9. **Panel loading** — `skeleton` (mirrors `operacao` loading).
10. **Panel loaded** — FSM state badge (reuse `fsm.ts` tier palette) + the OPEN lifecycle's vertical **event trail** (offer_accepted → health checks → ready / `shutdown_reason`). Polls every 10s via React Query.
11. **Panel empty** (no open lifecycle) + **panel error** (gateway/proxy down → pt-BR error + "Tentar novamente").

**Surface D — Structural read-only display (D-01)** — read-only for BOTH roles:
12. **Estrutural (somente leitura)** — the 19 structural fields (GPU shape, images, weights key+SHA truncated-mono, llama args, timezone) in a muted card with the note "Alterado via redeploy/env — não editável aqui."

---

## Page placement & navigation

| Decision | Value |
|----------|-------|
| Route | **NEW** `/operacao/config` (sub-route of Operação) — keeps pod-config beside the operational view it governs. (Executor MAY instead ship a top-level `/config-pod`; either is acceptable — keep it inside the `(dashboard)` route group + sidebar.) |
| Sidebar nav | Add one entry after "Operação": label **"Config do pod"**, icon `SlidersHorizontal` (lucide, already installed). **Visible to BOTH roles** — operators reach the read-only view; edit affordances are owner-gated (mirrors the operadores pattern: table visible, controls hidden for non-owner). |
| Owner gate | Page is an RSC that reads the viewer role (`getViewerRole`, Phase 13). Edit affordances live in a client island rendered ONLY when `isOwner`; every server action re-checks `requireOwner` (D-07). |
| Layout shell | `<div className="flex flex-col gap-8">` + `h1` 28/600 (matches `operacao/page.tsx`). Surface C (live panel) at top (diagnostic-first, the motivating use case), then Surface A (config), Surface B (bounds), Surface D (structural) below. |

---

## Design System

| Property | Value |
|----------|-------|
| Tool | shadcn (already initialized — `dashboard/components.json`, verified 2026-06-30) |
| Preset | `radix-nova` — inherited; locked by converseai-v4 standard |
| Component library | radix-ui (shadcn `radix-nova`, `baseColor: neutral`, `cssVariables: true`, `prefix: ""`) |
| Icon library | lucide-react (`^0.564`, already in `dashboard/package.json`) |
| Font | `--font-sans` (Geist Sans) — `next/font` self-hosted |
| Theme mode | **Dark only** — `<html class="dark">` (inherited). No toggle shipped. |

**Source:** `dashboard/components.json` declares `"style": "radix-nova"`, `"baseColor": "neutral"`, `"iconLibrary": "lucide"`, `"registries": {}`. Tailwind v4 (`@tailwindcss/postcss ^4`), tokens in `src/app/globals.css`.

### Net-new shadcn blocks required this phase

Inventory of `dashboard/src/components/ui/` (2026-06-30): present = `alert, alert-dialog, badge, button, calendar, card, chart, dialog, dropdown-menu, input, popover, scroll-area, select, separator, sheet, sidebar, skeleton, sonner, table, tabs, tooltip`. The Phase 13 installs (`dialog`, `dropdown-menu`, `alert-dialog`) are now present. **Absent and relevant: `switch`.**

| Net-new block | Decision | Justification (slopcheck) |
|---------------|----------|---------------------------|
| `switch` | **INSTALL** | Two boolean hot fields — `reject_private_ip` and `Disabled` (kill-switch) — need an editable binary control. No present primitive offers a labeled binary toggle; `select` (present) with "sim/não" reads heavier for a binary and adds a click. `switch` is the shadcn-canonical binary control. Official registry. The `Disabled` switch's `onCheckedChange` opens the dangerous-action `alert-dialog` (D-04) BEFORE committing. |
| `label` | **DO NOT INSTALL** | Reuse plain `<label className="text-[12px] font-semibold">` — matches `operator-controls.tsx` / `2fa/enroll` precedent. |
| `form` | **DO NOT INSTALL** | Plain `<form>` + `useState` + server actions — matches `operator-controls.tsx`. |
| `toggle-group` | **DO NOT INSTALL** | Schedule **Days** (subset of `{seg…dom}`) is a row of 7 small `Button` toggles (active = `--primary` tint, inactive = muted outline) — matches the existing inline-button idiom; no new dep. |

**Slopcheck gate (inherited from Phase 11/13):** before any `npx shadcn add`, confirm no present primitive covers the UX. Only `switch` is justified above; record the actual install decision in plan execution evidence. The install is from the **official shadcn registry** — no third-party registry.

---

## Spacing Scale

**Inherited verbatim from Phase 07/11/13 + the `(dashboard)` route group.** Phase 17 introduces no new spacing tokens; every padding/gap resolves to the 8-point grid.

| Token | Value | Usage in Phase 17 |
|-------|-------|-------------------|
| xs | 4px | `gap-1` — label-to-value gap inside a Field; icon-to-text gap |
| sm | 8px | `gap-2` — edit-row input↔buttons gap; field↔error gap; event-trail step inner padding |
| md | 16px | `gap-4` — field grid gap inside a card; card inner gap; `alert-dialog` content gap |
| lg | 24px | `gap-6` / `p-6` — page padding; card padding; section padding |
| xl | 32px | `gap-8` — **inter-surface gap** (between live panel, config, bounds, structural cards) |
| 2xl | 48px | reserved |
| 3xl | 64px | reserved |

**Inherited exceptions (NOT reusable spacing tokens — visual-continuity only):**

| Value | Where | Note |
|-------|-------|------|
| 12px (`gap-3`) | badge/status row in `operacao-fsm-panel` ("flex-wrap items-center gap-3") | Carried over so the FSM badge row in the live panel matches the shipped `operacao` panel. Do not adopt for new grids (those use `gap-4`). |
| `5px 10px`, radius 5 | inline owner-action button (`+ Provisionar` token) | The Phase 13 `btn-sm` token. If Phase 17 reuses that exact button idiom (e.g. an "Editar" pill), carry it verbatim; do not "fix" to grid. New `Button` instances use shadcn `size` props. |

**Grid rule:** all NEW card/dialog/form padding and gaps MUST resolve to `{4, 8, 16, 24, 32, 48, 64}`.

---

## Typography

**Inherited verbatim from the `(dashboard)` route group** (`operacao`, `economia`, `consumo`, `kpi-card`). Note: this route group uses the **12 / 14 / 20 / 28** scale (page-h1 is 28px, card-title 20px) — distinct from the settings group's 16/24. Phase 17 lives in `(dashboard)`, so it uses **12 / 14 / 20 / 28**.

| Role | Size | Weight | Line height | Tabular | Phase 17 usage |
|------|------|--------|-------------|---------|----------------|
| Display / page h1 | 28px (`text-[28px]`) | 600 | 1.2 | — | Page `h1` "Config do pod" |
| Heading / card title | 20px (`text-[20px]`) | 600 | 1.2 | — | Card titles: "Seleção de oferta", "Orçamentos e timeouts", "Agenda", "Limites de validação", "Provisionamento ao vivo", "Configuração estrutural (somente leitura)" |
| Body | 14px (`text-[14px]`) | 400 | 1.5 | when numeric | Field VALUES, input text, dialog descriptions, event-trail step labels, structural values |
| Label | 12px (`text-[12px]`) | 600 (field labels) / 400 (muted meta) | 1.4 | — | Field labels, table headers, badge text, "vale a partir do próximo provisionamento" hints, "última alteração" hint, inline errors |

**Scale = exactly 4 sizes: 12 (Label) · 14 (Body) · 20 (Heading) · 28 (Display).**

**Tabular numerals MANDATORY on** every numeric config value, bound (min/max) cell, timestamp, and the live-panel timings — values must not jitter on the 10s refetch (apply `tabular-nums`, matching `Field`/`kpi-card`). SHA256 / weights-key strings render in a **monospace** truncated cell (`font-mono text-[12px] text-muted-foreground`, truncate with title attr) — the only mono surfaces; they are STRUCTURAL read-only display, never editable.

---

## Color

**Inherited verbatim from Phase 07/11/13** (`radix-nova .dark` OKLCH tokens in `globals.css`). No new tokens. Reuse the `fsm.ts` status-tier system for the live panel.

| Role | OKLCH (.dark) | Token | Usage in Phase 17 |
|------|---------------|-------|-------------------|
| Dominant (60%) | `oklch(0.13 0.028 261.692)` | `--background` | Page background, dialog backdrop scrim |
| Secondary (30%) | `oklch(0.21 0.034 264.665)` | `--card` | All config/bounds/structural/live-panel cards, `dropdown`/`alert-dialog` surfaces |
| Accent (10%) | `oklch(0.648 0.2 131.684)` (green) | `--primary` | See reserved-for list below |
| Destructive | `oklch(0.704 0.191 22.216)` (red) | `--destructive` | Dangerous-action `alert-dialog` confirm buttons; the `Disabled=true` warning; inline validation errors; critical FSM tier in the live panel |
| Warning | `oklch(0.769 0.188 70.08)` (amber) | `--status-warning` | `provisioning`/`draining` FSM tier; "applies next provision" cautions; cap-down/coldstart soft-warn affordances |
| Border | `oklch(1 0 0 / 10%)` | `--border` | Card/table/dialog borders |
| Muted fg | `oklch(0.707 0.022 261.325)` | `--muted-foreground` | Field labels, hints, structural card body, read-only state, event-trail meta |

**Accent (`--primary`) reserved for (Phase 17 scope) — explicit list:**
- The single `Salvar` primary button inside each per-field / per-cell edit (one primary per active edit at a time).
- The active sidebar nav item ("Config do pod") — existing sidebar accent behavior.
- The `Day` toggle ACTIVE state (selected weekday) — `--primary` tint background + text.
- A `switch` in the ON position (`reject_private_ip=true`) — shadcn switch checked track.
- `healthy`/`ready` FSM tier in the live panel (`text-primary`, via `tierTextClass`).
- Input focus ring (`--ring`).

**Accent NOT used for:** the per-field pencil/"Editar" trigger (muted→foreground on hover), `Cancelar` buttons (ghost/secondary neutral), table row hover (neutral `--accent`), field values (foreground neutral), structural display (muted).

**Destructive (`--destructive`) reserved for:** the CONFIRM button inside a dangerous-action `alert-dialog`; the `Disabled=true`/empty-Days/cap-down warning text inside that dialog; inline validation errors; the `FAILED_OVER`/`EMERGENCY_ACTIVE`/`destroying` FSM tier + a `shutdown_reason` failure row in the event trail. The pencil/edit triggers themselves are NEUTRAL — danger lives in the confirm step, not the affordance.

**Status-tier palette (reuse `fsm.ts` verbatim — DO NOT redefine):** `healthy → --primary`, `warning → --status-warning`, `critical → --destructive`, `neutral → --muted-foreground`. Primary-FSM badge classes reuse `operacao-fsm-panel`'s `primaryStateClass` (`ready`→primary, `provisioning|draining`→warning, `destroying`→destructive, `asleep|unknown`→muted).

---

## Copywriting Contract

**Audience:** ~4 internal Ifix operators; **1 owner** (pedro) edits. Language: **pt-BR**, operational, direct, no marketing tone. Matches existing dashboard copy.

### Primary CTAs & affordances

| Surface | Copy |
|---------|------|
| Per-field edit trigger (owner) | icon-only pencil (`Pencil` lucide), `aria-label="Editar {nome do campo}"` |
| Edit save (default) | **Salvar** |
| Edit save (pending) | **Salvando…** (disabled, inline 14×14 spinner) |
| Edit cancel | **Cancelar** |
| Bounds cell edit trigger | icon-only pencil, `aria-label="Editar limite de {campo}"` |
| Live panel error retry | **Tentar novamente** (matches `operacao`) |
| Dangerous confirm — cap-down | **Reduzir teto** (`--destructive`) |
| Dangerous confirm — estreitar agenda | **Estreitar agenda** (`--destructive`) |
| Dangerous confirm — desativar agenda | **Desativar agenda** (`--destructive`) |
| Dangerous confirm — dias vazios | **Salvar mesmo assim** (`--destructive`) |
| Dangerous confirm — allowlist restritiva | **Salvar allowlist** (`--destructive`) |
| Any dangerous-confirm cancel | **Cancelar** (default-focus) |

### Card titles + descriptions

| Surface | Title | Description / hint |
|---------|-------|--------------------|
| Page header | Config do pod | `Config HOT do pod primário · edição owner-only · vale a partir do próximo provisionamento` |
| Live panel | Provisionamento ao vivo | (Reuse FSM badge + event trail; no sub-copy needed) |
| Config group 1 | Seleção de oferta | Filtros que o reconciler aplica ao buscar a próxima oferta Vast. |
| Config group 2 | Orçamentos e timeouts | Limites de custo e prazos de provisionamento. |
| Config group 3 | Agenda | Janela de funcionamento do pod e kill-switch. |
| Bounds surface | Limites de validação | Envelope de segurança de cada campo. O valor salvo é validado contra estes limites. |
| Structural card | Configuração estrutural (somente leitura) | Forma da GPU, imagens, pesos e args — alterado via redeploy/env, não editável aqui. |

### Field labels (16 HOT + the 19 structural read-only)

**HOT (editable):** Blocklist de máquinas · Allowlist de máquinas · Teto de preço (primário) · Teto de preço (fallback) · Host ID · Rejeitar IP privado · Orçamento de cold-start (s) · Orçamento de bind de porta (s) · Cooldown de falha (s) · Orçamento mensal (R$) · Hora de subir · Hora de descer · Dias · Grace de ramp-down (s) · Lead de provisionamento (s) · Agenda desativada.

**STRUCTURAL (read-only):** Fuso horário · GPU primária · GPU fallback · Nº GPUs (primário) · Nº GPUs (fallback) · Imagem do template · Imagem Infinity · Imagem DCGM · Imagem Speaches · Chave dos pesos Qwen · SHA256 Qwen · Chave Whisper · SHA256 Whisper · Chave BGE-M3 · SHA256 BGE-M3 · Chave Chatterbox · SHA256 Chatterbox · Chave/SHA Jinja · Llama args.

### Input formats / placeholders

| Field type | Control | Placeholder / hint |
|------------|---------|--------------------|
| CSV (blocklist/allowlist) | `Input` text | `IDs separados por vírgula — ex: 55942, 45778` |
| float (caps, budget BRL) | `Input` number, step 0.01 (caps) / 1 (BRL) | unit suffix shown as muted 12px after value (`$`, `R$`) |
| int (host_id, budgets, hours, lead, grace) | `Input` number | hour fields hint `0–23` |
| Days | 7 `Button` toggles `seg ter qua qui sex sáb dom` | — |
| bool (reject_private_ip, Disabled) | `switch` + 12px state label (sim/não) | — |

### Inline / field error copy

| Trigger | Copy |
|---------|------|
| Value outside current bound | `Valor fora do limite permitido ({mín}–{máx}).` |
| CSV non-numeric | `Use apenas IDs numéricos separados por vírgula.` |
| Hour out of 0–23 | `A hora precisa estar entre 0 e 23.` |
| Cap below floor / above ceiling | `O teto precisa estar entre {mín} e {máx}.` |
| Bounds: mín ≥ máx | `O mínimo precisa ser menor que o máximo.` |
| DownHour == UpHour | `Hora de descer não pode ser igual à de subir.` |
| Generic server/network failure | `Não foi possível salvar agora. Tente novamente em alguns segundos.` |
| Operator reaches an edit (should be unreachable via hidden UI) | `Edição restrita ao owner do dashboard.` |

### Success toasts (sonner)

| Action | Toast copy |
|--------|-----------|
| Hot field that applies at next provision (blocklist, allowlist, caps, host_id, reject_private_ip, coldstart, port-bind) | `{Campo} atualizado. Vale a partir do próximo provisionamento.` |
| Hot field that applies next tick (cooldown, budget, schedule fields, Disabled) | `{Campo} atualizado.` |
| Bound min/max | `Limite de {campo} atualizado.` |

### Dangerous confirmations (D-04 — specific impact strings, one-click, NO type-to-confirm)

| Action | Title | Body (specific impact) | Confirm button |
|--------|-------|------------------------|----------------|
| Reduzir teto de preço (cap-down) | Reduzir o teto de preço? | `Um teto abaixo do mercado pode impedir o provisionamento: se nenhuma oferta couber abaixo de {novo valor}, o pod não sobe até você aumentar o teto de volta.` | Reduzir teto |
| Estreitar agenda com pod no ar (DownHour passa "agora") | Estreitar a janela agora? | `Isto vai drenar o pod que está rodando AGORA. O atendimento pelo pod primário cai imediatamente até a próxima janela.` | Estreitar agenda |
| Desativar agenda (`Disabled=true`) | Desativar a agenda? | `O kill-switch interrompe o provisionamento automático. O pod não sobe nos próximos horários até você reativar.` | Desativar agenda |
| Dias vazios | Salvar sem nenhum dia? | `Sem dias selecionados, o pod nunca será provisionado pela agenda.` | Salvar mesmo assim |
| Allowlist exclui todos os hosts conhecidos | Salvar esta allowlist? | `Esta allowlist exclui todos os hosts conhecidos. O provisionamento pode ficar sem ofertas elegíveis.` | Salvar allowlist |

**`alert-dialog` rules (inherited from Phase 13):** default-focus on **Cancelar**; confirm button carries `--destructive`; confirm pending = disabled + spinner. Confirm is UX-only — the authoritative gate is `requireOwner` + server-side bounds validation.

### Empty / loading / read-only states

| Context | Copy |
|---------|------|
| Live panel — no open lifecycle | `Nenhum provisionamento em curso. O pod está dormindo ou aguardando a janela.` |
| Live panel — loading | `skeleton` (no text) |
| Live panel — gateway/proxy error | `Não foi possível carregar o estado de provisionamento. Verifique se o gateway está no ar e use Tentar novamente.` |
| Operator viewing (non-owner) | No empty-state copy — pencils/switches/`Salvar` are simply absent; all values + the live panel remain visible read-only. |
| "última alteração" hint (per surface, D-06) | `Última alteração: {relativo} por {email}` (omit when no audit row yet) |

---

## Visual Hierarchy & Layout

### Live provisioning panel (Surface C — top of page)

A `card`, title "Provisionamento ao vivo" (20/600). Top row: FSM state `Badge` (reuse `primaryStateClass`/`primaryStateLabel`) + leader badge + emergency state (mirror `operacao-fsm-panel`). Below: the OPEN lifecycle's **event trail** as a vertical step list — each step = a small status dot (tier-colored) + 14px label + 12px `tabular-nums` timestamp, rendered from the lifecycle `events` jsonb in order (`offer_accepted → first_health_pass → ready`, or a `--destructive` terminal row with `shutdown_reason`). React Query `refetchInterval: 10000`; `StaleIndicator` in the header (reuse component). Empty/loading/error states per copy table. **Read-only for both roles.**

### Config HOT editor (Surface A — three grouped cards)

Each group is a `card` with a 20/600 title. Inside, a 2-column field grid (`grid grid-cols-2 gap-4 sm:grid-cols-4` for the dense numeric groups; single-column for CSV/Days). Each field uses the `Field` idiom: 12/600 muted label + 14px `tabular-nums` value. **Owner:** a neutral pencil (`Pencil`, 14px) trails the value; click → value swaps to its typed control + `Salvar`(primary) / `Cancelar`(ghost) in an 8px row. **Per-field edit** = one server action = one audit row = one toast (clean per-field diff + targeted dangerous-confirm). Validation runs server-side against the CURRENT bound before commit; out-of-bound → inline error, edit stays open.

> Planner note: per-field inline edit is the prescribed pattern (clean audit + confirm targeting). A per-card "Editar → batch Salvar" mode is an acceptable alternative IF it still emits one `pod_config.update` audit row per changed field and routes each dangerous field through its own confirm.

### Bounds editor (Surface B)

A `card` "Limites de validação" containing a `table`: **Campo | Mín | Máx**. Rows = the numeric hot fields with bounds (caps, coldstart, port-bind, cooldown, budget, UpHour, DownHour, grace, lead). Non-bounded fields (CSV, bool, Days, host_id) are omitted or show `—`. **Owner:** Mín/Máx cells carry an inline pencil → number input + Salvar/Cancelar (same states as Surface A). Cross-field rule enforced server-side: `mín < máx`.

### Structural read-only display (Surface D)

A muted `card` "Configuração estrutural (somente leitura)" — a 2-column `Field` grid of the 19 structural values. Weights keys + SHA256 render `font-mono text-[12px] text-muted-foreground` truncated (full value in `title`). A 12px muted footnote: "Alterado via redeploy/env — não editável aqui." No edit affordances for any role.

### Component dimension constraints (inherited)

| Component | Fixed dimension | Source |
|-----------|-----------------|--------|
| Inline button spinner | 14×14, 2px border, top-transparent, `0.8s linear infinite` | Phase 11/13 (`operator-controls` `Spinner`) |
| `alert-dialog` width | `max-width: 384px`, 24px padding | Phase 13 |
| Field label | 12/600 muted; value 14px tabular | `operacao-fsm-panel` `Field` |
| Table row height | 36px | Phase 07/11 data-table |
| Focus ring | 3px `--ring` glow | radix-nova |
| Day toggle | small `Button` (`size="sm"`), active=`--primary` tint | new (inline-button idiom) |

---

## Component Inventory

| Block | Status | Used for in Phase 17 |
|-------|--------|----------------------|
| `card` | present | All four surfaces' containers |
| `input` | present | Config field edit (text/number); bounds cells |
| `button` | present | Salvar/Cancelar; Day toggles; retry; edit pencils (ghost) |
| `badge` | present | FSM state, leader, schedule "deve estar no ar?" — live panel |
| `table` | present | Bounds editor (Campo/Mín/Máx) |
| `alert-dialog` | present | All five dangerous-action confirms (D-04) |
| `skeleton` | present | Live panel loading |
| `sonner` | present | All save success/error toasts |
| `separator` | present | Card section dividers (optional) |
| `tooltip` | present | (optional) pencil / structural-truncation affordances |
| `scroll-area` | present | (optional) long CSV / event-trail overflow |
| **`switch`** | **INSTALL** | `reject_private_ip` + `Disabled` boolean edit (official registry) |
| `dialog` | present (unused) | not needed — inline edits, not modal forms |
| `dropdown-menu` | present (unused) | not needed this phase |
| `label` | reuse plain `<label>` | field labels (12/600) |
| `form` | DO NOT INSTALL | plain `<form>` + `useState` + server actions |

**Icon usage (lucide-react, installed):**

| Icon | Usage |
|------|-------|
| `SlidersHorizontal` | Sidebar nav "Config do pod" |
| `Pencil` | Per-field / per-bound-cell edit trigger (owner) |
| `Check` / `X` | (optional) Salvar/Cancelar leading icons |
| `ServerCog` | (existing Operação nav) — leave as-is |
| `CircleDot` / `Circle` | Event-trail step dots (tier-colored) |
| `Lock` | (optional) structural card "read-only" affordance |

---

## Privacy / Redaction Rules (inherited, MANDATORY)

Phase 17 displays pod POLICY knobs, never secrets. From RESEARCH "Infra/secrets OUT OF SCOPE":

- **NEVER displayed or editable anywhere:** `VAST_AI_API_KEY`, `MINIO_*`, `AI_GATEWAY_PG_DSN`, `AI_GATEWAY_REDIS_*`, `DCGM_EXPORTER_URL`, the gateway admin key, session/cookie values, raw user UUIDs.
- Weights **SHA256 / keys** are public-grade integrity strings (not secret) — OK to display read-only, truncated mono.
- The "última alteração por" hint shows operator **email** only (already-authenticated owner viewing the audit hint) — never IPs or tokens.
- The admin key stays server-only (Phase 15 proxy / server action) — the client wrappers stay key-free (T-07-24).

---

## Registry Safety

| Registry | Blocks Used | Safety Gate |
|----------|-------------|-------------|
| shadcn official | card, input, button, badge, table, alert-dialog, skeleton, sonner, separator, tooltip, scroll-area, **switch** | not required for official shadcn registry |

**No third-party registries declared.** `dashboard/components.json` ships `"registries": {}` (verified 2026-06-30). Phase 17 adds NO third-party registry. Registry vetting gate: **not applicable**.

**Slopcheck:** the single net-new install (`switch`) is justified above against the present-primitive inventory. Record the actual `npx shadcn add switch` decision + slopcheck result in plan execution evidence.

---

## Inheritance Notes

Explicitly inherits `.planning/phases/13-dashboard-user-management-gest-o-de-operadores-owner-only-se/13-UI-SPEC.md` (owner-gate / server-action / `alert-dialog` / sonner / spinner idioms) and the live `(dashboard)` route-group pattern from Phase 15 (`operacao` FSM panel + lifecycle timeline, `fsm.ts` tiers, `KpiCard`, `StaleIndicator`). Phase 17 changes:

| Existing surface | Phase 17 change |
|------------------|-----------------|
| `src/components/app-sidebar.tsx` | **Extended** — add "Config do pod" nav item (`SlidersHorizontal`) after Operação. |
| `src/app/(dashboard)/operacao/` (or new `/config-pod`) | **NEW** — owner-aware page hosting the four surfaces. |
| `src/lib/fsm.ts` | **Reused as-is** — live-panel FSM labels + tiers. NO new states. |
| `src/lib/admin-actions.ts` | **Extended** (non-visual) — `updatePodConfig` / `updatePodConfigBound` server actions (`requireOwner` + `writeAuditLog`). UI contract: owner-only edit, operator read-only. |
| `src/app/api/gateway/[...path]/route.ts` | **Unchanged** — stays read-only (live-panel poll only); writes go via server action (D-07). |
| `src/components/ui/` | **NEW** — `switch.tsx` via `npx shadcn add switch`. |

---

## Anchors for plan-phase (do not break)

- 16 HOT fields editable; 19 STRUCTURAL fields read-only display; NO restart button, NO structural-edit form (D-01/D-02).
- Bounds (min/max) are a SECOND owner-editable surface; operator read-only on both (D-03).
- Dangerous fields → one-click `alert-dialog` with the SPECIFIC impact string above; NO type-to-confirm (D-04).
- Live panel = FSM state + OPEN lifecycle event trail, polls `GET /admin/primary/lifecycle` every 10s; reuse `fsm.ts` + `StaleIndicator` (D-05).
- Audit dashboard-side only; per-surface "última alteração" hint is the sole audit affordance (no viewer UI) (D-06).
- Writes via owner-gated server action; proxy stays read-only; operator read-only everywhere (D-07).
- Page lives in the `(dashboard)` route group, dark-only, radix-nova tokens, 12/14/20/28 type scale, pt-BR internal-operator tone.
- One net-new install: `switch` (official registry). All other primitives present.

---

## Out-of-Scope Visual Surfaces

| Surface | Why excluded |
|---------|--------------|
| Self-restart button + structural-edit forms (shape/image/weights) | Deferred (D-01/D-02 / CONTEXT §Deferred). |
| Lifecycle HISTORY table with cost/trend (N-últimas) | Deferred (D-05) — live panel is minimal diagnostic only. `operacao`'s existing timeline already covers recent history. |
| Audit-log VIEWER UI for `pod_config.*` rows | Deferred (D-06) — only the "última alteração" hint this phase. |
| Allowlist policy (preference vs hard-filter) toggle | Out of scope (SEED-020, RESEARCH Open Q6) — provisioning-logic change, own phase. |
| Secrets/infra editing (Vast/MinIO/DSN keys) | Explicitly out of scope (RESEARCH) — never dashboard-editable. |

---

## Checker Sign-Off

- [ ] Dimension 1 Copywriting: PASS
- [ ] Dimension 2 Visuals: PASS
- [ ] Dimension 3 Color: PASS
- [ ] Dimension 4 Typography: PASS
- [ ] Dimension 5 Spacing: PASS
- [ ] Dimension 6 Registry Safety: PASS

**Approval:** pending
