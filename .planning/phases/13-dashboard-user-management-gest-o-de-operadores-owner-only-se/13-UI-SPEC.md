---
phase: 13
slug: dashboard-user-management-gest-o-de-operadores-owner-only-se
status: draft
shadcn_initialized: true
preset: radix-nova
created: 2026-06-15
inherits_from: 11-UI-SPEC.md
baseline_prototype: dashboard/src/app/settings/operadores/page.tsx (Phase 11 implemented + live)
---

# Phase 13 — UI Design Contract

> Visual and interaction contract for Phase 13 — **dashboard-user-management** (gestão de operadores owner-only + self-service change-password) on the standalone `dashboard/` Next.js 15 app.
>
> **Visual baseline LOCKED.** Phase 07 shipped the dashboard look-and-feel; Phase 11 (`11-UI-SPEC.md`, `status: approved`) added the auth surface + the **Operadores tab** that this phase makes functional. Phase 13 does NOT redesign — it ACTIVATES the placeholder buttons in `settings/operadores/page.tsx` and ADDS one self-service `/settings` page, both consistent with the existing pattern. All design tokens (spacing, typography, color, components) are inherited verbatim from Phase 11/07 unless explicitly overridden below.
>
> **Source of truth for the existing visual pattern:** the already-implemented + live `dashboard/src/app/settings/operadores/page.tsx` (read 2026-06-15). This file is the de-facto reference; Phase 13 extends it without breaking its layout.

---

## Phase 13 net-new screens / states

Per CONTEXT.md scope (D-01..D-09). Two surfaces, eight states.

**Surface A — Self-service change-password (`/settings`)** — any logged-in operator:
1. **Change-password form (default)** — current-password + new-password + confirm-password fields, single primary CTA. Lives in a new `/settings` page (sibling tab to `operadores`).
2. **Change-password pending** — disabled "Alterando…" button with inline 14×14 spinner.
3. **Change-password success** — sonner toast + fields cleared; inline confirmation copy.
4. **Change-password error (wrong current / weak new / mismatch)** — inline field error, no toast.

**Surface B — Gestão de operadores (owner-only)** — extends `settings/operadores/page.tsx`:
5. **Provisionar-operador modal** — name + email form; opened by the now-functional `+ Provisionar operador` button. Sends Brevo invite link (D-04/D-05).
6. **Row-action menu (`···`)** — dropdown anchored to the per-row `···` button with four owner-only actions: Resetar senha, Resetar 2FA, Remover operador (each gated server-side D-03).
7. **Destructive confirmation dialog** — confirm step for the two destructive actions (Remover operador, Resetar 2FA) and the session-revoking Resetar senha. Shows target email + what will happen (sessions revoked, etc.).
8. **Owner-only visibility gate** — when the viewer's `role !== 'owner'`, the `+ Provisionar operador` button and every `···` menu are hidden (UI layer); server actions re-check (D-03). The `Função` badge reads the REAL `role` column (D-02), replacing the current `i===0` visual derivation.

---

## Design System

| Property | Value |
|----------|-------|
| Tool | shadcn (already initialized — `dashboard/components.json` present, verified 2026-06-15) |
| Preset | `radix-nova` — inherited from Phase 07/11, locked by converseai-v4 standard |
| Component library | radix-ui (shadcn `radix-nova` style, `baseColor: neutral`, `cssVariables: true`, `prefix: ""`) |
| Icon library | lucide-react (`^0.564.0`, already in `dashboard/package.json`) |
| Font | `--font-sans` (Geist Sans, converseai-v4 default) — `next/font` self-hosted |

**Source:** `dashboard/components.json` declares `"style": "radix-nova"`, `"baseColor": "neutral"`, `"iconLibrary": "lucide"`, `"registries": {}`. No new `shadcn init`. Tailwind v4 (`@tailwindcss/postcss ^4`, tokens in `src/app/globals.css`).

### Theme mode

| Property | Value |
|----------|-------|
| Default theme | **Dark only** — `<html class="dark">` (inherited). `globals.css` ships a `:root` light set for preset fidelity but no toggle is shipped. |
| Theme toggle | Out of scope (inherited decision). |

### Net-new shadcn blocks required this phase

Phase 13 introduces interactive primitives the dashboard does not yet have. Inventory of `dashboard/src/components/ui/` (2026-06-15): present = `alert, badge, button, calendar, card, chart, input, popover, scroll-area, select, separator, sheet, sidebar, skeleton, sonner, table, tabs, tooltip`. **Absent: `dialog`, `dropdown-menu`, `alert-dialog`, `label`, `form`.**

| Net-new block | Decision | Justification |
|---------------|----------|---------------|
| `dialog` | **INSTALL** | Provisionar-operador modal (state 5) needs focus-trap + ESC dismiss + backdrop. The auth surface rendered backup codes inline (no modal) — but a name+email form launched from a table action is a true modal context. `sheet` (present) is an alternative but reads as a side-panel; a centered `dialog` matches the "NewKeyModal" pattern referenced in Phase 11 `pages-extra.jsx`. |
| `dropdown-menu` | **INSTALL** | Row-action `···` menu (state 6). No existing primitive provides anchored menu + keyboard nav + ESC. `popover` (present) could host it but lacks roving-focus menu semantics (WCAG menu pattern). |
| `alert-dialog` | **INSTALL** | Destructive confirmation (state 7) for Remover / Resetar 2FA / Resetar senha. `alert-dialog` enforces an explicit confirm/cancel and is the shadcn-canonical destructive-confirm primitive. Do NOT reuse plain `dialog` for destructive confirms (no enforced cancel default-focus). |
| `label` | **INSTALL (or reuse `<label>`)** | Modal + change-password form labels. Existing pattern (`2fa/enroll/page.tsx`) uses plain `<label className="text-xs font-semibold">`. Executor MAY keep plain `<label>` to match precedent and skip the block — **prefer plain `<label>` (no new dep)**. |
| `form` | **DO NOT INSTALL** | Existing forms use plain `<form>` + `useState` (`login`, `2fa/enroll`). Phase 13 matches — server actions handle submission. |

**Slopcheck gate (inherited from Phase 11):** before any `npx shadcn add`, confirm no present primitive covers the UX. Record install/skip decision in the plan execution evidence. All three installs above are from the **official shadcn registry** — no third-party registry.

---

## Spacing Scale

**Inherited verbatim from Phase 07/11.** Phase 13 introduces no new spacing tokens.

| Token | Value | Usage in Phase 13 |
|-------|-------|-------------------|
| xs | 4px | Icon-to-label gap inside dropdown-menu items; tight inline gaps |
| sm | 8px | Dialog button-row gap; form field-to-error gap; menu item vertical padding |
| md | 16px | **Form inner gap** (between change-password fields); dialog content gap; card inner gap (matches existing `gap-6`→ see exception) |
| lg | 24px | **Dialog padding** all sides; page section padding (existing `p-6`) |
| xl | 32px | Reserved — inter-section gaps |
| 2xl | 48px | Reserved |
| 3xl | 64px | Reserved |

**Exceptions (sub-token, prototype-fidelity — inherited from the live `operadores/page.tsx`):**

| Value | Location | Justification |
|-------|----------|---------------|
| `padding: "8px 12px"` | Operadores table cells (existing) | Both multiples of 4 — NOT a true exception; listed for continuity. |
| `padding: "5px 10px"` + `borderRadius: 5` | `+ Provisionar operador` button (existing inline style) | Matches the `btn-sm` 5px radius token established in Phase 11. Carried over unchanged; do not "fix" to 4px multiples or it diverges from the shipped button. |
| `padding: "2px 8px"` | Role + 2FA badges (existing) | Badge density token from Phase 11 `badge-*` variants. Carried over. |
| row `height: 36px` | Table rows (existing) | Layout constraint token (Phase 07 data-table row), not spacing. |
| avatar `h-7 w-7` (28px) | Operator initials avatar (existing) | Component dim (Phase 11), not spacing. |

These are the COMPLETE set of sub-token values touched by Phase 13. New dialogs/menus resolve all other padding/gap to `{4, 8, 16, 24, 32, 48, 64}`.

---

## Typography

**Inherited verbatim from Phase 07/11** (4 sizes × 2 weights, `--font-sans` Geist Sans). The live `operadores/page.tsx` uses these effective roles:

| Role | Size | Weight | Line height | Tabular | Phase 13 usage |
|------|------|--------|-------------|---------|----------------|
| Display / stat value | `text-2xl` (24px) | 600 (`font-semibold`) | 1.2 | yes | Stat-card values (existing); page `h1` "Configurações" |
| Heading | `text-sm`/`text-base` (14–16px) | 600 | 1.2 | — | Dialog titles, section headers ("Operadores", "Alterar senha"), table caption header |
| Body | 14px | 400 | 1.5 | — | Dialog descriptions, helper copy, form input text |
| Label | 12px (`text-xs`) / 11px (`text-[11px]`) | 600 (labels) / 400 (muted meta) | 1.4 | — | Field labels, badge text, table headers (uppercase tracking-wider), subtitle meta |

**Per-element mapping (Phase 13):**

| Element | Role |
|---------|------|
| Page `h1` "Configurações" | Display 24px / 600 / `tracking-tight` (existing) |
| Page subtitle (`ai-dashboard… · N operadores · TOTP obrigatório`) | Label 11–12px / 400 / `text-muted-foreground` (existing) |
| Tab labels (`Geral · Integrações · Chaves admin · Operadores · Segurança`) | Body 14px / 400 inactive, 600 active + 2px `--primary` border-bottom (existing) |
| Dialog title ("Provisionar operador", "Remover operador?", "Resetar 2FA?", "Resetar senha?") | Heading 16px / 600 |
| Dialog description | Body 14px / 400 / `text-muted-foreground` |
| Form field label ("Nome", "E-mail", "Senha atual", "Nova senha", "Confirmar nova senha") | Label 12px / 600 |
| Form input text | 14px / 400 / `var(--foreground)` (shadcn `input`) |
| Dropdown-menu item label | Body 14px / 400; destructive item = 14px / 400 / `text-destructive` |
| Table cells (operator name) | 14px; email sub-line 11px / `text-muted-foreground` |
| Badge text (role, 2FA) | 11px / 600 |
| Relative-time cell ("agora / há 3h / nunca") | 12px / `text-muted-foreground` / `tabular-nums` (existing `relativeTime`) |
| Inline field error | 12px / 400 / `var(--destructive)` |

**Tabular numerals MANDATORY on:** session counts, stat-card values, relative-time cells (already applied via `tabular-nums` in the existing page). No new monospace surfaces — Phase 13 NEVER displays TOTP secrets, hashes, or backup codes in the UI (privacy rule below).

---

## Color

**Inherited verbatim from Phase 07/11** (`radix-nova .dark` OKLCH tokens from `globals.css`). No new tokens this phase.

| Role | OKLCH value | Token | Usage in Phase 13 |
|------|-------------|-------|-------------------|
| Dominant (60%) | `oklch(0.13 0.028 261.692)` | `--background` | Settings page background, dialog backdrop scrim base |
| Secondary (30%) | `oklch(0.21 0.034 264.665)` | `--card` | Stat cards, table card, dialog surface, dropdown-menu surface |
| Accent (10%) | `oklch(0.648 0.2 131.684)` (green) | `--primary` | See reserved-for list below |
| Destructive | `oklch(0.704 0.191 22.216)` (red) | `--destructive` | Remove/reset destructive CTAs + confirm dialogs; "Remover operador" menu item; inline form errors |
| Warning | `oklch(0.769 0.188 70.08)` (amber) | `--status-warning` | `owner` role badge + `aguardando enroll` 2FA badge (existing); stat tone="warning" |
| Border | `oklch(1 0 0 / 10%)` | `--border` | Card/table/dialog/menu borders |
| Muted fg | `oklch(0.707 0.022 261.325)` | `--muted-foreground` | Helper copy, email sub-lines, `···` button default state, relative-time |

**Accent (`--primary`) reserved for (Phase 13 scope) — explicit list:**

- Single primary CTA per surface: `+ Provisionar operador` (table top-right), `Enviar convite` (provision dialog primary), `Alterar senha` (change-password form primary).
- Active settings tab 2px border-bottom (existing).
- Operator avatar initials text + tinted background (existing `color-mix(--primary 18% --card)`).
- `2FA ativo` badge tint + text (existing `badge-healthy`).
- Input focus ring (`--ring`).

**Accent NOT used for:** dropdown-menu item hover (uses `--accent`/`--row-hover` neutral tint), `···` trigger button (muted→foreground on hover), destructive actions (those use `--destructive`), cancel buttons in dialogs (ghost/secondary neutral).

**Destructive (`--destructive`) reserved for:** the confirm-action button inside the Remover/Resetar-2FA/Resetar-senha `alert-dialog`; the "Remover operador" `dropdown-menu` item label; inline change-password validation errors. Reset-2FA and Reset-senha menu items themselves are NEUTRAL in the menu (they are recoverable invite flows) — only their CONFIRM buttons inside the dialog carry destructive styling because they revoke sessions.

**Semantic badge palette (inherited, from live page + Phase 11):**

| Variant | Background | Foreground | Phase 13 use |
|---------|------------|------------|--------------|
| `badge-healthy` | `color-mix(--primary 18% --card)` | `var(--primary)` | `2FA ativo` |
| `badge-warning` | `color-mix(--status-warning 16% --card)` | `var(--status-warning)` | `owner` role, `aguardando enroll` |
| `badge-neutral` | `var(--surface-tint-strong, --card)` | `var(--muted-foreground)` | `operator` role |

---

## Copywriting Contract

**Audience:** ~4 internal Ifix operators (D-13 allowlist `@ifixtelecom.com.br`). Language: **pt-BR**, operational, direct, no marketing tone. Matches existing dashboard copy.

### Primary CTAs

| Surface | CTA copy |
|---------|----------|
| Change-password form (default) | **Alterar senha** |
| Change-password form (pending) | **Alterando…** (disabled, inline spinner) |
| Provision modal (primary) | **Enviar convite** |
| Provision modal (pending) | **Enviando…** (disabled, inline spinner) |
| Row menu `···` (trigger) | (icon-only `···` / `MoreHorizontal`, `aria-label="Ações para {nome}"` — existing) |
| Remover confirm dialog (primary destructive) | **Remover operador** |
| Resetar 2FA confirm dialog (primary destructive) | **Resetar 2FA** |
| Resetar senha confirm dialog (primary destructive) | **Enviar reset de senha** |
| Any dialog cancel (secondary/ghost) | **Cancelar** |

### Section titles + descriptions

| Surface | Title | Description |
|---------|-------|-------------|
| Settings page header | Configurações | `ai-dashboard.converse-ai.app · {N} operadores · TOTP obrigatório` (existing) |
| Change-password section (`/settings`) | Alterar senha | Defina uma nova senha para sua conta. Será necessário informar sua senha atual. |
| Operadores section (existing) | Operadores | (table card header — existing) |
| Provision modal | Provisionar operador | O operador receberá um e-mail com um link para definir a própria senha. Acesso restrito a contas `@ifixtelecom.com.br`. |
| Remover confirm | Remover operador? | Remove `{email}` e encerra todas as sessões dele imediatamente. Esta ação não pode ser desfeita. |
| Resetar 2FA confirm | Resetar 2FA? | Desativa o 2FA de `{email}` e encerra as sessões dele. No próximo login ele será obrigado a configurar um novo autenticador. |
| Resetar senha confirm | Resetar senha? | Envia um e-mail de redefinição para `{email}` e encerra as sessões ativas dele. O operador define a própria senha nova pelo link. |

### Form field labels + placeholders

| Field | Label | Placeholder |
|-------|-------|-------------|
| Provision name | Nome | Nome completo do operador |
| Provision email | E-mail | nome@ifixtelecom.com.br |
| Current password | Senha atual | (none) |
| New password | Nova senha | (none) |
| Confirm new password | Confirmar nova senha | (none) |

### Inline / field error copy

| Trigger | Copy |
|---------|------|
| Change-password: wrong current password | Senha atual incorreta. Verifique e tente novamente. |
| Change-password: new == current | A nova senha precisa ser diferente da atual. |
| Change-password: confirm mismatch | As senhas não coincidem. |
| Change-password: too weak | A senha precisa ter pelo menos 8 caracteres. |
| Provision: email outside allowlist (D-13 server reject) | Apenas e-mails `@ifixtelecom.com.br` são permitidos. |
| Provision: email already registered | Já existe um operador com este e-mail. |
| Any server/network failure | Não foi possível concluir a ação agora. Tente novamente em alguns segundos. |
| Non-owner attempts an admin op (server action rejects — should not be reachable via hidden UI) | Ação restrita ao owner do dashboard. |

### Success states (sonner toast)

| Action | Toast copy |
|--------|-----------|
| Change-password success | Senha alterada com sucesso. |
| Provision invite sent | Convite enviado para `{email}`. |
| Operator removed | Operador `{email}` removido. |
| 2FA reset | 2FA de `{email}` resetado. Sessões encerradas. |
| Password reset sent | E-mail de redefinição enviado para `{email}`. |

### Empty / loading states

| Context | Copy |
|---------|------|
| Operadores table empty (existing fallback) | Nenhum operador cadastrado. |
| Operadores load error (existing) | Erro ao carregar operadores: `{message}` |
| Provision modal submitting | Enviando… (button) |
| Owner viewing as non-owner (no admin controls) | (No empty-state copy — controls are simply absent; the table remains visible read-only) |

### Destructive confirmations (Phase 13 scope)

| Action | Pattern |
|--------|---------|
| Remover operador | `alert-dialog`. Destructive confirm button = "Remover operador" (`--destructive`); default-focus on **Cancelar**. Body states sessions are revoked + irreversibility. |
| Resetar 2FA | `alert-dialog`. Confirm = "Resetar 2FA" (`--destructive`, revokes sessions). Body clarifies CR-01-safe path (re-enroll on next login). Default-focus Cancelar. |
| Resetar senha | `alert-dialog`. Confirm = "Enviar reset de senha" (`--destructive` tone because it revokes sessions). Default-focus Cancelar. |
| Change own password | NO confirm dialog — reversible self-service action; inline form + sonner success is sufficient (D-09: self-service change is NOT an admin action, not audited). |
| Provision operator | `dialog` (NOT alert-dialog) — constructive, not destructive. Form modal with Cancelar + Enviar convite. |

**Audit note (D-08/D-09):** every admin action (provision, remove, reset-password, reset-2FA + the session revocations) writes to `admin_audit_log`. The self-service change-password does NOT. No audit-log VIEWER UI in this phase (deferred) — the table is created now, the read tab is a future phase.

---

## Visual Hierarchy & Layout

### Settings page shell (extends existing `operadores/page.tsx`)

The existing page is a single `<main className="flex min-h-screen flex-col p-6 gap-6">` with: header → tab nav → stat strip → table card. Phase 13:

- **Adds a `Segurança` (or `Conta`) tab** to the existing tab nav for the self-service `/settings` change-password surface, OR ships `/settings` as the change-password page and links the existing `operadores` as a tab within the same Settings shell (executor discretion — keep the 2px `--primary` active-tab indicator either way).
- **Replaces the `i===0` role derivation** in the `Função` column with the real `role` column (D-02): `owner` → `badge-warning`, `operator` → `badge-neutral`.
- **Activates `+ Provisionar operador`** → opens the provision `dialog` (was a dead button).
- **Activates the per-row `···`** → opens the `dropdown-menu` (was literal `···` text — replace with `MoreHorizontal` lucide icon, keep `aria-label`).
- **Replaces the footer note** `provisionados via scripts/dashboard/seed-admins.sh` with copy reflecting UI provisioning (e.g. `operadores gerenciados pelo painel`), since seed-admins.sh is being superseded.
- **Owner gate:** if viewer `role !== 'owner'`, hide `+ Provisionar operador` and all `···` triggers (server actions re-enforce — D-03).

### Provision dialog (state 5)

Centered `dialog`, `max-width: 384px` (matches auth card width), 24px padding, 16px inner gap. Title + description (copy above) → Nome field → E-mail field → footer row (Cancelar ghost left, Enviar convite primary right, 8px gap). Pending = primary disabled + inline 14×14 spinner.

### Row-action menu (state 6)

`dropdown-menu` anchored bottom-right to the `···` (`MoreHorizontal`) trigger. Items, in order:
1. Resetar senha (neutral)
2. Resetar 2FA (neutral)
3. `separator`
4. Remover operador (`text-destructive`)

Each item opens its respective `alert-dialog`. Menu surface = `--card`, item hover = neutral `--accent`/`--row-hover` tint, 14px body, icon-left optional (`KeyRound`, `ShieldOff`, `Trash2`).

### Destructive confirm dialog (state 7)

`alert-dialog`, centered, `max-width: 384px`, 24px padding. Title (question form) → description (target email + consequence) → footer (Cancelar default-focus + destructive confirm). Confirm pending = disabled + spinner.

### Change-password form (Surface A)

Lives in the Settings shell on its own tab/section. A single `card` (`max-width` ~480px) with 24px padding, 16px field gap: section title "Alterar senha" + description → Senha atual → Nova senha → Confirmar nova senha → primary "Alterar senha". Inline errors per field (12px `--destructive`). Success clears fields + sonner toast.

### Component dimension constraints (inherited)

| Component | Fixed dimension | Source |
|-----------|-----------------|--------|
| Table row height | 36px | Phase 07/11 (existing page `height: 36`) |
| Operator avatar initials | 28×28 (`h-7 w-7`), `rounded-full`, 11px / 600 | existing page |
| `+ Provisionar` button | `padding 5px 10px`, `borderRadius 5` | existing page (btn-sm token) |
| Inline button spinner | 14×14, 2px border, top-transparent, `0.8s linear infinite` | Phase 11 |
| Dialog / modal width | `max-width: 384px` (auth-card parity) | Phase 11 |
| Change-password card | `max-width: ~480px` | new (single-column form) |
| Focus ring | 3px `--ring` glow | shadcn radix-nova |

---

## Component Inventory

| Block | Status | Used for in Phase 13 |
|-------|--------|----------------------|
| `card` | present | Stat strip, table card (existing); change-password card |
| `input` | present | Provision name/email; change-password 3 fields |
| `button` | present | All CTAs (`default`, `secondary`, `ghost`, `destructive` variants all exist) |
| `table` | present | Operadores roster (existing) |
| `badge` | present | Role + 2FA badges (existing) |
| `skeleton` | present | (optional) loading rows |
| `sonner` | present | All success/error toasts |
| `separator` | present | Dropdown-menu divider before destructive item |
| `tooltip` | present | (optional) `···` / icon affordances |
| **`dialog`** | **INSTALL** | Provision-operator modal (state 5) |
| **`dropdown-menu`** | **INSTALL** | Row-action `···` menu (state 6) |
| **`alert-dialog`** | **INSTALL** | Destructive confirms (state 7) |
| `label` | INSTALL or reuse `<label>` | Form labels — **prefer plain `<label>`** (matches `2fa/enroll`) |
| `form` | DO NOT INSTALL | Plain `<form>` + `useState` + server actions |

**Icon usage (lucide-react, already installed):**

| Icon | Usage |
|------|-------|
| `MoreHorizontal` | Row-action `···` trigger (replaces literal `···` text) |
| `UserPlus` | `+ Provisionar operador` button leading icon (optional) |
| `KeyRound` | Resetar senha menu item / change-password section |
| `ShieldOff` | Resetar 2FA menu item |
| `Trash2` | Remover operador menu item (destructive) |
| `RefreshCw`, `Bell` | Header ghost icons (existing) |

---

## Privacy / Redaction Rules (inherited, MANDATORY)

From the existing page header comment + Phase 11 §Privacy. Phase 13 introduces admin actions but MUST NOT widen exposure:

- E-mails OK (authenticated operator viewing peers).
- Last-login = relative copy only ("agora / há 3h / há 2d / nunca").
- **NEVER displayed anywhere in the UI:** TOTP secrets, backup codes, password hashes, temporary passwords (the provision/reset flow sends a link — the UI NEVER shows the temp password), IP addresses, session cookie values, raw user UUIDs.
- Reset-password and provision flows deliver a **link via Brevo SMTP** (D-04/D-05/D-07) — the dashboard UI shows only "convite enviado" / "e-mail de redefinição enviado", never a credential.

---

## Registry Safety

| Registry | Blocks Used | Safety Gate |
|----------|-------------|-------------|
| shadcn official | card, input, button, table, badge, skeleton, sonner, separator, tooltip, **dialog**, **dropdown-menu**, **alert-dialog**, label (optional) | not required for official shadcn registry |

**No third-party registries declared.** `dashboard/components.json` ships `"registries": {}` (verified 2026-06-15). Phase 13 adds NO third-party registry. Registry vetting gate: **not applicable**.

**Slopcheck:** the three net-new installs (`dialog`, `dropdown-menu`, `alert-dialog`) are justified above against the present-primitive inventory. Record the actual `npx shadcn add` decision + slopcheck result in the plan execution evidence.

---

## Inheritance Notes

Explicitly inherits `.planning/phases/11-prod-hardening/11-UI-SPEC.md` (which inherits Phase 07). Phase 13 changes to existing surfaces:

| Existing surface | Phase 13 change |
|------------------|-----------------|
| `dashboard/src/app/settings/operadores/page.tsx` | **Extended** — `Função` column reads real `role` (D-02); `+ Provisionar operador` opens dialog; `···` opens dropdown-menu; footer note updated; owner-gate hides controls for non-owners. Layout (header/tabs/stat strip/table) preserved. |
| Settings tab nav | **Extended** — add `Segurança`/`Conta` tab for self-service `/settings` change-password (or host change-password as a section reachable from the shell). 2px `--primary` active indicator preserved. |
| `dashboard/src/app/settings/` route group | **NEW** — `/settings` self-service change-password page (Surface A). |
| `dashboard/src/lib/auth.ts` | (non-visual) admin plugin wired (D-01) — no UI contract beyond enabling the ops. |
| `dashboard/src/components/ui/` | **NEW** — `dialog.tsx`, `dropdown-menu.tsx`, `alert-dialog.tsx` added via `npx shadcn add`. |

---

## Anchors for plan-phase (do not break)

- Surfaces: `/settings` (self-service change-password), `settings/operadores` (admin, existing).
- Server actions enforce `role==='owner'` server-side (D-03) — UI hiding is cosmetic only.
- Role source: real `role` column (D-02), NOT `i===0`.
- Brevo SMTP via nodemailer for invite/reset links (D-04/D-05/D-07) — UI never shows credentials.
- Reset-2FA = clear + re-enroll on next login, CR-01 intact (D-06).
- Destructive confirms via `alert-dialog`; provision via `dialog`; row menu via `dropdown-menu`.
- Audit: every admin op → `admin_audit_log`; self-service change-password NOT audited (D-09).
- No audit-log viewer UI this phase (deferred).
- pt-BR copy, internal-operator tone, dark-only, radix-nova tokens.

---

## Out-of-Scope Visual Surfaces

| Surface | Why excluded |
|---------|--------------|
| Audit-log viewer tab ("Auditoria") | Deferred (CONTEXT.md §Deferred) — table created, read UI is a future phase. |
| RBAC management UI beyond owner/operator | Out of scope (CONTEXT.md). |
| Tenant / API-key admin in the dashboard | Out of scope. |
| Backup-code regeneration UI | Deferred. |
| `scripts/dashboard/seed-admins.sh` output | Non-visual CLI — superseded by this UI. |

---

## Checker Sign-Off

- [ ] Dimension 1 Copywriting: PASS
- [ ] Dimension 2 Visuals: PASS
- [ ] Dimension 3 Color: PASS
- [ ] Dimension 4 Typography: PASS
- [ ] Dimension 5 Spacing: PASS
- [ ] Dimension 6 Registry Safety: PASS

**Approval:** pending
