---
phase: 11
slug: prod-hardening
status: approved
reviewed_at: 2026-05-27
shadcn_initialized: true
preset: radix-nova
created: 2026-05-27
updated: 2026-05-27
inherits_from: 07-UI-SPEC.md
source_prototype: /home/pedro/sync/Front  Ai-gateway.zip (2026-05-27T13:00Z)
---

# Phase 11 — UI Design Contract (rewrite v2)

> Visual and interaction contract for Phase 11 additions to the standalone `dashboard/` Next.js 15 app (Better Auth SSO hardening — PRD-06). Rewritten from the design prototype `Front  Ai-gateway.zip` (`src/auth.jsx` + `src/tokens.css` + `src/pages-extra.jsx`).
>
> **Scope (frontend surface):** PRD-06 dashboard SSO hardening — extends the existing standalone Better Auth dashboard at `dashboard/`. Non-visual surfaces (`gatewayctl debug emit-error`, `gatewayctl key list`, `load-replay.py` terminal output, RUNBOOK markdown docs, LGPD letter template) are explicitly out of scope.
>
> **Visual baseline LOCKED.** Phase 07 already shipped the dashboard look-and-feel (`07-UI-SPEC.md` `status: draft`, implemented + verified live Phase 10). Phase 11 does NOT redesign — it ADDS auth-flow screens/states and ONE settings tab (Operadores) consistent with the existing pattern. All design tokens (spacing, typography, color, components) inherited verbatim from Phase 07 unless explicitly overridden below.
>
> **Phase 11 net-new screens / states (15 total — derived from prototype `src/auth.jsx` + `src/pages-extra.jsx`):**
>
> Auth surface (14 screens):
> 1. Login default — existing Phase 07 screen, reused unchanged (baseline reference)
> 2. Login invalid credentials — existing Phase 07 error state
> 3. **Login pending** — NEW — spinner inside disabled "Entrando…" button (D-12 transition)
> 4. **Login rate-limited** — NEW — D-14 — `Alert variant="destructive"` with countdown `11m 42s`, form disabled
> 5. **Login session-expired** — NEW — D-15 — `Alert variant="default"` with `Clock` icon, idle timeout 30 min explainer
> 6. **Signup allowlist rejection** — NEW — D-13 — `Alert variant="destructive"`, email input border red, button disabled
> 7. **First-login landing** — NEW — D-12 — welcome card + `2FA obrigatório` warning + 3-item bullet list + "Configurar 2FA agora" CTA. One-time gate between password-verify and `/2fa/enroll`
> 8. **TOTP enroll step 1 — QR** — NEW — D-12 — 192×192 QR + manual TOTP secret display + algorithm/period metadata (`Ifix AI Gateway · SHA1 · 30s`)
> 9. **TOTP enroll step 2 — verify** — NEW — D-12 — 6-slot OTP input + 30s countdown
> 10. **TOTP enroll step 3 — backup codes** — NEW — D-12 — 10 codes in 2-column grid (numbered 01..10, `xxxx-xxxx` format), `alert-warning` banner + "Copiar tudo" + "Salvar e continuar" buttons
> 11. **TOTP challenge on login** — NEW — D-12 — 6-slot OTP input + "Usar código de backup" ghost secondary
> 12. **TOTP challenge invalid** — NEW — D-12 — challenge with red OTP slots + inline error copy + countdown
> 13. **TOTP verify success transient** — NEW — D-12 — green OTP slots + `CircleCheck` + "Verificado" disabled button (800ms before route push)
> 14. **Backup code entry** — NEW — D-12 + Codex review #6 — alphanumeric `xxxx-xxxx` field, monospace, "Voltar ao app autenticador" ghost fallback
> 15. **Signed-out landing** — NEW — post-logout confirmation card with "Entrar novamente" + "Abrir runbook de operações" CTAs
>
> Admin surface (1 screen, NEW for Phase 11):
> 16. **Settings → Operadores tab** — NEW — Phase 11 supplements the existing Phase 07 settings panel with a 4th tab listing all `@ifixtelecom.com.br` operators, their 2FA status, role, last login, open sessions, and per-row actions menu. Provides operator-visible evidence that D-12/D-13/D-14/D-15 are live in prod.

---

## Design System

| Property | Value |
|----------|-------|
| Tool | shadcn (already initialized — `dashboard/components.json` present) |
| Preset | `radix-nova` — inherited from Phase 07, locked by converseai-v4 standard |
| Component library | radix-ui (shadcn `radix-nova` style, `baseColor: neutral`, `cssVariables: true`) |
| Icon library | lucide-react (`^0.564.0`, already in `dashboard/package.json`) |
| Font | `--font-sans` (Geist Sans, converseai-v4 default) — `next/font` self-hosted |

**Source:** `dashboard/components.json` declares `"style": "radix-nova"`, `"baseColor": "neutral"`, `"iconLibrary": "lucide"`, `"registries": {}`. No new `shadcn init`. Phase 11 may need to `npx shadcn add` the following blocks per Reviews MEDIUM (11-02 UI-primitive inventory FIRST, install ONLY if existing primitives can't cover):

| Net-new shadcn block | Decision rule |
|----------------------|---------------|
| `input-otp` | INSTALL if existing `input` primitive cannot render the 6-slot grouped-digit UX with paste-handling + per-digit accessibility (WCAG 2.1.1 / 2.1.2). Slopcheck audit required. |
| `dialog` | INSTALL ONLY if backup-codes display benefits from focus-trap + ESC-dismiss; the prototype renders backup codes INLINE within the same `LoginCard` (no modal) — **prefer inline (no new dep)**. |
| `form` | DO NOT INSTALL. Existing login uses plain `<form>` + `useState` — Phase 11 matches. |

Existing blocks present in `dashboard/src/components/ui/` (Phase 07 install) that Phase 11 reuses unchanged: `button`, `card`, `input`, `alert`, `skeleton`, `sonner`, `separator`.

### Theme mode

| Property | Value |
|----------|-------|
| Default theme | **Dark** — inherited from Phase 07 (`<html class="dark">`). Auth surfaces use `<div class="ifx">` wrapper that scopes radix-nova `.dark` tokens. |
| Theme toggle | Out of scope. Prototype `tokens.css` includes a light-theme block (`html[data-ifx-theme="light"] .ifx`) but Phase 11 does NOT ship the toggle — dark only. |

---

## Visual Hierarchy

Every auth screen is a **single-task focus surface** — one `LoginCard` centered in the viewport via `AuthShell`. Above the card sits a 40×40 rounded-square header logo (Zap icon, primary-tinted background, 22% primary mix on card surface, 1px primary border at 40% opacity). Below the card sits a centered footer line: `Ifix · AI Gateway · ai-dashboard.converse-ai.app` (muted, 12px label, weight 400).

Card padding: **24px** all sides. Card inner gap (between elements): **16px**. Card width: `100%` with `max-width: 384px` (`max-w-sm`).

**Per-screen primary focal point:**

| Screen | Focal point |
|--------|-------------|
| Login default | E-mail → Senha → **Entrar** button (existing) |
| Login pending | **Entrando…** disabled button with 14×14 spinner (border-top-transparent rotation, 0.8s linear infinite) |
| Login rate-limited | `Alert variant="destructive"` banner with countdown `<span class="tabnum">` in `var(--destructive)` color |
| Login session-expired | `Alert variant="default"` banner with `Clock` icon (muted-foreground color) |
| Signup allowlist rejection | `Alert variant="destructive"` + e-mail input with `borderColor: var(--destructive)` |
| First-login landing | `ShieldCheck` icon next to title + `alert-warning` banner + 3 explainer bullets + primary CTA with `ArrowRight` |
| TOTP enroll QR | 192×192 QR (white background, 10px border-radius) + manual-entry secret pill below |
| TOTP enroll verify | Centered 6-slot OTP row (active slot has blinking caret) + 30s countdown line with `Clock` icon |
| TOTP enroll backup | `alert-warning` ("códigos exibidos uma vez") + 2-column grid of 10 codes + dual CTA row (`Copiar tudo` secondary + `Salvar e continuar` primary, `flex: 1` each, 8px gap) |
| TOTP challenge | Centered 6-slot OTP row + countdown + primary CTA + ghost secondary "Usar código de backup" |
| TOTP challenge invalid | Slots with `border: var(--destructive)` + 3px `--destructive`-tinted ring + inline `CircleAlert` + error copy |
| TOTP verify success | Slots with `border: var(--primary)` + `color-mix(--primary 10% --card)` fill + green check + "Código aceito · redirecionando…" copy |
| Backup code entry | Single monospace input with `xxxx-xxxx` placeholder + primary CTA "Entrar com backup" + ghost secondary "Voltar ao app autenticador" |
| Signed-out landing | `alert` (non-destructive) "Logout concluído com sucesso. Cookies de sessão removidos." + primary CTA "Entrar novamente" + ghost "Abrir runbook de operações" |
| Settings → Operadores | 4-column stat strip + operator table (avatar initials, role badge, 2FA badge, sessions count, action menu) |

Single accent-colored primary CTA per screen — the only deliberate accent draw (matches Phase 07 rule).

---

## Spacing Scale

**Inherited verbatim from Phase 07.** Phase 11 introduces **no new spacing tokens**.

| Token | Value | Usage in Phase 11 auth + Operadores screens |
|-------|-------|---------------------------------------------|
| xs | 4px | Gap between OTP digit slots (`.otp { gap: 8px }` token — wait — actual prototype uses 8px, see below); title↔description gap inside card header |
| sm | 8px | OTP slot gap (`.otp { gap: 8px }`); button row gap on backup-codes screen; logo↔card gap |
| md | 16px | **Card inner gap** (between LoginCard children); QR-to-secret gap on enroll step 1; AuthShell column gap |
| lg | 24px | **Card padding** all sides; AuthShell viewport padding |
| xl | 32px | Reserved — settings table cells use `padding: "8px 12px"` (not 32px); 32px reserved for inter-section gaps if needed |
| 2xl | 48px | Reserved — not expected on focused auth card |
| 3xl | 64px | Reserved — not expected |

**Exception (acknowledged):** OTP slot gap is `8px` per prototype `.otp { gap: 8px }` (sm token), NOT 4px. Spec is now consistent with prototype.

### Sanctioned sub-token exceptions (prototype-fidelity)

Phase 11 inherits the spacing scale `{4, 8, 16, 24, 32, 48, 64}` from Phase 07. The prototype `src/auth.jsx` + `src/tokens.css` declares **four sub-token values** that are NOT multiples of 4. Each is documented as an explicit exception here:

| Value | Location | Justification |
|-------|----------|---------------|
| `gap: 6px` | Backup-codes 2-column grid (enroll step 3) | 8px would push the 10-code grid below the fold on `max-width: 384px` cards; 6px is intentional for code-density layout. Used ONLY in this dense grid context. |
| `padding: "5px 8px"` + `borderRadius: 5px` | Each backup-code row | Optical balance with `gap: 6px` parent grid; 5px keeps the 10-row stack within visual budget. `borderRadius: 5px` matches the `btn-sm` 5px radius token (Phase 07 component dim, NOT spacing). |
| `padding: "10px 12px"` | Compact `alert-warning` banner inside enroll step 3 LoginCard | The standard `alert` padding is `12px 16px`. The compact variant 10px reduces vertical bulk inside the already-dense backup-codes card (otherwise the warning + grid + dual-CTA stack exceeds viewport on 14" laptops). |
| `padding: "6px 10px"` | Manual TOTP secret pill (enroll step 1, under QR) | The pill is an inline rounded chip — `8px` padding makes it look like a full input field; `6px 10px` reads as a label-pill with embedded copy button. Used ONLY for the secret pill. |
| `padding: "8px 12px"` | Operadores table cells | 8px + 12px both multiples of 4 — NOT an exception. Listed for completeness. |

These five (four real exceptions + one false-positive listed for completeness) are the COMPLETE set of sub-token values in Phase 11. No other sub-token padding/gap is permitted; all other spacing resolves to `{4, 8, 16, 24, 32, 48, 64}`.

---

## Layout Constraints

**Inherited from Phase 07** (data-table row 36px, critical banner 44px min). Phase 11 adds component-height constraints — NOT spacing tokens:

| Component | Fixed dimension | Type | Justification |
|-----------|-----------------|------|---------------|
| Auth header logo | 40×40 px, `borderRadius: 10px` | Component dim | Distinguishes auth surface from in-app sidebar logo (28×28); centered above card; rendered with `Zap` lucide icon at 20px stroke 2.4 |
| TOTP enroll QR code | **192×192 px** (prototype `<QrPlaceholder size={192} />`) | Component dim | Prototype uses 192 (NOT 256 as previous spec stated). 192px on a 384px-wide card leaves 96px breathing room each side — scans reliably from a 2024-era laptop camera at typical desk distance. White 16px padding wraps the QR (`padding: 16, background: "white", borderRadius: 10`). |
| OTP digit slot | **44×52 px**, `borderRadius: 6` | Component dim | Per `.otp .slot` token. 44px width = WCAG AA touch target. 52px height = 28px monospace digit + balance padding. Slot fill states: default (`var(--surface-tint)`), filled (`var(--surface-tint-strong)`), active (1px `--primary` border + 3px `--ring` glow), invalid (1px `--destructive` border + 3px `--destructive 25%`-tinted glow), success (1px `--primary` border + `color-mix(--primary 10% --card)` fill) |
| OTP caret | 2px wide × 28px tall, blinks `1s steps(2,end) infinite` | Animation | `.caret` animation `@keyframes ifxcaret { 50% { opacity: 0 } }` |
| Auth Card max-width | 384px (Tailwind `max-w-sm`) | Component dim | Inherited; single column form keeps eye-scan minimal |
| Pill-style manual TOTP secret | `padding: "6px 10px"`, `borderRadius: 6`, 1px `--border-strong` | Component dim | Mono digits with `letter-spacing: 0.08em`, weight 600, with inline 22×22 `Copy` ghost button |
| Spinner inside button | 14×14 px, 2px border, top-transparent, `borderRadius: 50%`, `animation: ifxspin 0.8s linear infinite` | Component dim | Inline before "Entrando…" label, 8px gap |
| Settings stat card | flexible width via `gridTemplateColumns: "repeat(4, 1fr)"`, 16px gap | Component dim | 4-column row above the operators table |
| Operator avatar initials | 28×28 px, `borderRadius: 999`, 11px label 600, `color-mix(--primary 18% --card)` background, `var(--primary)` text | Component dim | Two-letter initials (first letter of given + family name) |

---

## Typography

**Inherited verbatim from Phase 07** (4 sizes × 2 weights, `--font-sans` Geist Sans). Tokens.css formalizes:

| Class | Size | Weight | Line height | Letter spacing | Tabular |
|-------|------|--------|-------------|----------------|---------|
| `.t-display` | 28px | 600 | 1.2 | -0.015em | yes (`tabular-nums`) |
| `.t-heading` | 20px | 600 | 1.2 | -0.005em | — |
| `.t-body` | 14px | 400 | 1.5 | — | — |
| `.t-label` | 12px | 600 | 1.4 | 0.01em | — |
| `.tabnum` (utility) | inherits | inherits | inherits | inherits | yes |
| `.mono` (utility) | inherits | inherits | inherits | inherits | inherits | font-family: `ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace` |

**Per-element role mapping (auth screens):**

| Element | Role |
|---------|------|
| Card title ("Gateway ifix-ai", "Configurar 2FA", "Confirmar código", "Códigos de backup", "Verificação em duas etapas", "Código de backup", "Solicitar acesso", "Bem-vindo, Pedro", "Sessão encerrada") | `.t-heading` |
| Card description sub-explainer | `.t-body .muted` |
| Field labels ("E-mail", "Senha", "Código de 6 dígitos", "Código de backup", "Nome") | `.t-label` |
| Form input default text | 14px / weight 400 / `var(--foreground)` (from `.input` selector — NOT `.t-body` directly) |
| OTP digit text (each slot) | `.t-display`-equivalent: 28px / 600 / `font-variant-numeric: tabular-nums` / monospace stack (`.otp .slot` selector inline override) |
| Manual TOTP secret pill (under QR) | `.mono .t-label` with `letterSpacing: 0.08em` and weight 600 |
| Manual TOTP metadata line (`emissor / algoritmo / período`) | `.t-label .muted` at 11px (override) — weight 400 |
| Backup-code text (each of 10 rows) | `.mono .t-label`, weight 600, color `var(--foreground)` |
| Backup-code index (01..10 prefix) | `.t-label .muted`, weight 400, width 18px right-aligned |
| Countdown text in `tabnum` span | `.t-body` (inside `Alert`) or `.t-label` (inside OTP countdown line); `font-variant-numeric: tabular-nums` mandatory |
| Button label | inherits from `.btn` selector: 14px / 500 / line 1 / 8px icon gap (NOT `.t-body` weight 400) |
| Small button label | inherits from `.btn .btn-sm`: 12px / 500 / 5px radius / 28px height |
| Settings tab labels | `.t-body`, weight 400 inactive / 600 active, with 2px primary border-bottom on active |
| Settings table headers | `.t-label .muted`, uppercase, letter-spacing 0.04em, 32px row height |
| Settings table cells | 13px / `font-variant-numeric: tabular-nums` / 36px row height |

**Tabular numerals MANDATORY on:** OTP digit slots, all countdown timers, backup-code grid index/codes, settings table numeric cells (sessions, last-login durations), badge counts.

**Font family override:** OTP digits, backup codes, manual TOTP secret, backup-code-entry input all use `.mono` (`ui-monospace` system stack). NOT a new font — no `next/font` declaration needed.

---

## Color

**Inherited verbatim from Phase 07** (`radix-nova .dark` OKLCH tokens). No new tokens this phase.

| Role | OKLCH value | Token | Usage in Phase 11 |
|------|-------------|-------|-------------------|
| Dominant (60%) | `oklch(0.13 0.028 261.692)` | `--background` | Auth viewport background (around centered card); Operadores tab background |
| Secondary (30%) | `oklch(0.21 0.034 264.665)` | `--card` | LoginCard surface, alert default background, sidebar background |
| Accent (10%) | `oklch(0.648 0.2 131.684)` | `--primary` (green) | See reserved-for list below |
| Destructive | `oklch(0.704 0.191 22.216)` | `--destructive` (red) | Rate-limit alert (D-14), allowlist rejection alert (D-13), invalid OTP slot border/glow, invalid-OTP inline error text |
| Warning | `oklch(0.769 0.188 70.08)` | `--status-warning` (amber) | First-login `alert-warning` (2FA obrigatório), backup-codes "exibidos uma vez" warning, Operadores tab `aguardando enroll` badge + stat tone="warning" |
| Border | `oklch(1 0 0 / 8%)` | `--border` | Card borders, alert borders (default), table cell borders |
| Border strong | `oklch(1 0 0 / 14%)` | `--border-strong` | Alert borders (with destructive/warning color-mix overlay), input borders, OTP slot borders (default) |
| Input fill | `oklch(1 0 0 / 10%)` | `--input` | Form input background-image (form fields use `--surface-tint` per `.input` selector) |
| Surface tint | `oklch(1 0 0 / 4%)` | `--surface-tint` | OTP slot default fill, input default fill |
| Surface tint strong | `oklch(1 0 0 / 6%)` | `--surface-tint-strong` | OTP slot filled fill, manual TOTP secret pill background |
| Row hover | `oklch(1 0 0 / 3%)` | `--row-hover` | Operadores table row hover, backup-codes outer grid background |
| Ring (focus halo) | `oklch(0.648 0.2 131.684 / 50%)` | `--ring` | Input focus + OTP slot active focus glow |

**Accent (`--primary`) reserved for (Phase 11 scope):**

- Single primary CTA button per auth step — `Entrar`, `Já escaneei, continuar`, `Confirmar código`, `Salvar e continuar`, `Entrar com backup`, `Configurar 2FA agora`, `Entrar novamente`, `+ Provisionar operador`
- Active sidebar nav item background tint (`color-mix(--primary 14% transparent)`) + 1px border (`color-mix(--primary 28% transparent)`) + `--primary` icon stroke
- Focus rings on inputs + OTP active slot
- Success OTP slot fill + border on `TotpVerifySuccess` transient state
- `CircleCheck` success icon on signed-out alert
- Avatar initials text color (Operadores table + sidebar account chip)
- `badge-healthy` background tint + text color (Operadores `2FA ativo`)
- Sparkline + chart strokes (inherits from Phase 07 — not Phase 11 net-new)

**Accent NOT used for:** Copy icon buttons per backup-code row (`btn-ghost` neutral), manual-entry secret string (plain `--foreground`), QR code (neutral white-on-black), backup-code row index numbers (muted), session-expired clock icon (muted-foreground).

**Semantic state palette (`alert-destructive` / `alert-warning` / `dot-*` / `badge-*`):**

| Variant | Background | Border | Foreground / icon |
|---------|------------|--------|-------------------|
| `alert` (default) | `var(--card)` | `var(--border-strong)` | `var(--foreground)` |
| `alert-destructive` | `color-mix(--destructive 12% --card)` | `color-mix(--destructive 45% transparent)` | `var(--foreground)` text; `var(--destructive)` icon + emphasized digits |
| `alert-warning` | `color-mix(--status-warning 12% --card)` | `color-mix(--status-warning 45% transparent)` | `var(--foreground)` text; `var(--status-warning)` icon |
| `badge-healthy` | `color-mix(--primary 18% --card)` | `color-mix(--primary 35% transparent)` | `var(--primary)` |
| `badge-warning` | `color-mix(--status-warning 16% --card)` | `color-mix(--status-warning 35% transparent)` | `var(--status-warning)` |
| `badge-critical` | `color-mix(--destructive 18% --card)` | `color-mix(--destructive 38% transparent)` | `var(--destructive)` |
| `badge-neutral` | `var(--surface-tint-strong)` | `var(--border)` | `var(--muted-foreground)` |
| `dot-*` | 6×6 px circle + 8px box-shadow glow at 60% color-mix | — | matches color |

---

## Copywriting Contract

**Audience:** ~4 internal Ifix operators (D-13 email allowlist `@ifixtelecom.com.br`). Language: **pt-BR**, operational, direct, no marketing tone. Tone matches existing `dashboard/src/app/login/page.tsx` ("Painel de observabilidade — acesso restrito à equipe de operações.").

### Primary CTAs

| Screen | CTA copy |
|--------|----------|
| Login default | **Entrar** |
| Login pending | **Entrando…** (disabled, spinner inline) |
| Login rate-limited | **Entrar** (disabled — label unchanged) |
| Login session-expired | **Entrar** (re-prompts) |
| Signup allowlist rejection | **Criar conta** (disabled) |
| First-login landing | **Configurar 2FA agora** (with trailing `ArrowRight` 14px stroke 2.2) |
| TOTP enroll step 1 (QR shown) | **Já escaneei, continuar** |
| TOTP enroll step 2 (verify) | **Confirmar código** |
| TOTP enroll step 3 (backup codes) | **Salvar e continuar** (primary) + **Copiar tudo** (secondary, left) |
| TOTP challenge | **Confirmar código** (primary) + **Usar código de backup** (ghost, btn-sm, muted-foreground) |
| TOTP challenge invalid | **Confirmar código** + **Usar código de backup** |
| TOTP verify success | **Verificado** (disabled, with `CircleCheck` inline, 800ms before route push) |
| Backup code entry | **Entrar com backup** (primary) + **Voltar ao app autenticador** (ghost, btn-sm, muted) |
| Signed-out landing | **Entrar novamente** (primary) + **Abrir runbook de operações** (ghost, btn-sm, muted) |
| Settings → Operadores | **+ Provisionar operador** (btn-primary btn-sm, top-right) |

### Screen titles + descriptions

| Screen | Card title | Card description |
|--------|------------|------------------|
| Login default | Gateway ifix-ai | Painel de observabilidade — acesso restrito à equipe de operações. |
| Login pending | Gateway ifix-ai | Painel de observabilidade — acesso restrito à equipe de operações. |
| Login rate-limited | Gateway ifix-ai | Painel de observabilidade — acesso restrito à equipe de operações. |
| Login session-expired | Gateway ifix-ai | Painel de observabilidade — acesso restrito à equipe de operações. |
| Signup allowlist rejection | Solicitar acesso | Cadastro restrito à equipe de operações Ifix. |
| First-login landing | Bem-vindo, Pedro | Este é seu primeiro login. Antes de acessar o painel, é necessário ativar a verificação em duas etapas. |
| TOTP enroll step 1 | Configurar 2FA | Escaneie o QR code abaixo com seu app autenticador (Google Authenticator, 1Password, Authy). |
| TOTP enroll step 2 | Confirmar código | Digite o código de 6 dígitos exibido no seu app autenticador para concluir o cadastro. |
| TOTP enroll step 3 | Códigos de backup | Guarde estes códigos em local seguro. Cada código funciona uma única vez para entrar caso você perca acesso ao app autenticador. |
| TOTP challenge | Verificação em duas etapas | Digite o código de 6 dígitos do seu app autenticador. |
| TOTP challenge invalid | Verificação em duas etapas | Digite o código de 6 dígitos do seu app autenticador. |
| TOTP verify success | Verificação em duas etapas | Digite o código de 6 dígitos do seu app autenticador. |
| Backup code entry | Código de backup | Digite um dos 10 códigos de backup gerados durante o cadastro 2FA. Cada código funciona apenas uma vez. |
| Signed-out landing | Sessão encerrada | Você saiu do painel de observabilidade Ifix. Faça login novamente para retomar a operação. |
| Settings → Operadores | Configurações | ai-dashboard.converse-ai.app · 4 operadores · TOTP obrigatório |

### Banner / alert copy

| Trigger | Variant | Icon | Copy |
|---------|---------|------|------|
| Login rate-limited (D-14) | `alert-destructive` | `CircleAlert` (`--destructive`) | **Muitas tentativas.** Aguarde `<11m 42s>` antes de tentar novamente. Limite: 5 tentativas a cada 15 min por IP. — countdown updates per-second client-side; form fields + button stay `disabled` until counter hits 0 |
| Login session-expired (D-15) | `alert` (default) | `Clock` (`--muted-foreground`) | **Sessão encerrada por inatividade.** Faça login novamente. Sessões expiram após 30 min sem atividade. — not destructive (expiry benign) |
| Signup allowlist rejection (D-13) | `alert-destructive` | `CircleAlert` (`--destructive`) | **Cadastro restrito a contas `<mono>@ifixtelecom.com.br</mono>`.** Solicite acesso à equipe de operações para criar uma conta interna. — form fields stay enabled but submission blocked server-side |
| First-login landing (D-12) | `alert-warning` | `CircleAlert` (`--status-warning`) | **2FA obrigatório para esta conta.** Todas as contas `<mono>@ifixtelecom.com.br</mono>` são obrigadas a configurar TOTP no primeiro acesso. |
| Backup codes warning (enroll step 3) | `alert-warning` (compact, padding `10px 12px`) | `CircleAlert` 14px (`--status-warning`) | Os códigos só são exibidos uma vez. Copie e salve antes de continuar. |
| Signed-out landing | `alert` (default) | `CircleCheck` (`--primary`) | Logout concluído com sucesso. Cookies de sessão removidos. |

### Inline / field error copy

| Trigger | Copy |
|---------|------|
| Invalid e-mail/senha | E-mail ou senha inválidos. Verifique as credenciais e tente novamente. |
| Invalid OTP (challenge) | Código incorreto. Confirme o código atual no seu app autenticador e tente novamente. — rendered as inline `<CircleAlert 13px>` + `.t-label` weight 400 in `var(--destructive)` color |
| Expired OTP (>30s window — countdown reset) | Código expirado. Aguarde o próximo código no app autenticador. |
| Network/server error during 2FA verify | Não foi possível confirmar o código agora. Tente novamente em alguns segundos. |
| Backup codes copy success (sonner toast) | 10 códigos copiados para a área de transferência. |

### Empty / loading states

| Context | Copy |
|---------|------|
| QR code loading (first 200ms before SVG render) | Gerando QR code… (skeleton placeholder 192×192 pulsing rect at `--surface-tint`) |
| Verify-button pending | Verificando… |
| Login-button pending | Entrando… (with 14×14 spinner inline) |
| Enroll step 3 — operator hasn't copied yet | (inline informational, not a separate state) — see banner copy above |
| Operadores tab empty (no operators provisioned) | (Not expected in prod — D-13 gate restricts to 4 operators; fallback copy if list is empty: "Nenhum operador cadastrado.") |

### Countdown copy

| Context | Pattern |
|---------|---------|
| OTP countdown (under enroll/challenge slots) | `<Clock 12px />` novo código em `<span class="tabnum">17s</span>` — 30s ticking, label color `var(--muted-foreground)`, weight 400 |
| Rate-limit countdown (inside banner) | Aguarde `<span class="tabnum" style="color:--destructive">11m 42s</span>` antes de tentar novamente. — destructive color emphasis |

### Metadata copy (under manual TOTP secret on enroll step 1)

> emissor: `<mono>Ifix AI Gateway</mono>` · algoritmo: SHA1 · 30s

(11px label, weight 400, muted-foreground)

### Destructive confirmations (Phase 11 scope)

| Action | Confirmation pattern |
|--------|---------------------|
| Sign-out (existing dashboard sidebar action) | No confirmation modal — single-click sign-out is reversible. Existing Phase 07 pattern unchanged. Lands on **Signed-out landing** screen. |
| Provisionar operador (Operadores tab — Phase 11 NEW) | Modal dialog (NewKeyModal pattern reused from prototype `pages-extra.jsx`); commits via `scripts/dashboard/seed-admins.sh` per 11-05 plan; password handling rules per Reviews MEDIUM (NEVER stdout; `/tmp/admin-creds-*.txt` 0600 or Better Auth invite/reset flow) |
| Regenerate backup codes (FUTURE — NOT in Phase 11 scope) | Deferred. Phase 11 ships one-time codes display only; regeneration is v1.1. |
| Disable 2FA (FUTURE — NOT in Phase 11 scope) | Deferred. Lost-device recovery in Phase 11 = backup code entry screen + RUNBOOK-2FA-RECOVERY.md (delivered by 11-09 plan). Full 2FA-disable flow is v1.1. |

**Phase 11 ships zero destructive actions on the auth surface.** Backend chaos tests (PRD-02 Vast DELETE, PRD-03 OpenRouter DROP) are operator-CLI / SSH operations — no UI surface — and live in `RUNBOOK-INCIDENTS.md` (D-11).

---

## Component Inventory

All shadcn `radix-nova` blocks. First column = already present in `dashboard/src/components/ui/` (Phase 07 install) vs net-new this phase.

| Block | Status | Used for in Phase 11 |
|-------|--------|----------------------|
| `card` | present | All 14 auth screens — centered LoginCard wrapper; Operadores stat strip + table card |
| `input` | present | E-mail + password fields; signup name field; backup-code-entry monospace field; existing login unchanged |
| `button` | present | All primary CTAs + secondary + ghost variants; per-row table action `···` button |
| `alert` | present | All 6 banner variants — destructive (rate-limit, allowlist, OTP invalid inline), default (session-expired, signed-out), warning (first-login, backup-codes one-time) |
| `skeleton` | present | QR loading placeholder + Operadores table loading rows |
| `sonner` | present | "Códigos copiados para a área de transferência" toast on backup-codes copy |
| `separator` | present | Visual divider between QR and manual-entry secret (prototype uses padding gap, not `<Separator>` — keep optional) |
| **`input-otp`** | **EVALUATE** | 6-digit TOTP on enroll step 2 + challenge. **Decision rule:** install IF existing `input` cannot satisfy paste-handling + per-digit accessibility. Prototype uses custom `.otp .slot` CSS over plain divs — that pattern also works without `input-otp`. Slopcheck audit MANDATORY before adding (Reviews MEDIUM 11-02). |
| **`dialog`** | **EVALUATE** | Backup codes — prototype renders INLINE in LoginCard (no modal). **Prefer inline** (no new dep). Install ONLY if backup-codes need focus-trap + ESC dismiss (not justified by current UX). |
| **`form`** | **DO NOT INSTALL** | Plain `<form>` + `useState` matches existing `login/page.tsx` |
| `table` | present (Phase 07) | Operadores table — reuses existing `.tbl` class from Phase 07 (`table.tbl` + `thead th` + `tbody tr`) |
| `badge` | present (Phase 07) | Role badge (owner/operator), 2FA status badge (ativo/aguardando enroll), Stat-card tone badge (OK/ATENÇÃO/CRÍTICO) |

**Icon usage** (lucide-react, already installed — no new deps):

| Icon | Usage |
|------|-------|
| `Zap` | 40×40 auth header logo + 16×16 sidebar logo |
| `ShieldCheck` | Heading icon next to titles on enroll/challenge screens (18px stroke 2.2 `--primary`) |
| `Copy` | Per-backup-code row copy button (11px stroke 2.2) + `Copiar tudo` button (13px) + manual-secret copy (12px) |
| `CircleCheck` | TOTP verify success transient (13px stroke 2.4 `--primary`) + verified button label (14px) + signed-out alert (16px) |
| `CircleAlert` | All destructive alerts (16px) + warning alerts (14px or 16px) + inline OTP error (13px) |
| `Clock` | Session-expired alert (16px) + OTP countdown line (12px) |
| `ArrowRight` | First-login CTA trailing icon (14px stroke 2.2) |

---

## Settings → Operadores Tab (NEW for Phase 11)

The Settings page is part of Phase 07 surface (`SettingsPage` in prototype `pages-extra.jsx`). Phase 11 ships ONE new tab inside it — `Operadores` — to provide operator-visible evidence that D-12/D-13/D-14/D-15 are live.

### Structure

| Region | Content |
|--------|---------|
| Page header | Title `Configurações`, subtitle `ai-dashboard.converse-ai.app · 4 operadores · TOTP obrigatório`, right-actions: `RefreshCw` + `Bell` ghost icons. **Breadcrumb hint:** the subtitle disambiguates which tab the operator is on; the active tab bar provides the visual indicator (2px `--primary` border-bottom). No separate breadcrumb component. |
| Tab bar | 4 tabs: `Geral` · `Integrações` · `Chaves admin` · **`Operadores`** (active). 2px primary border-bottom on active. |
| Stat strip | 4-column grid, 16px gap, stat cards with `.t-display` value: <br>· **Operadores** — `4` — sub: `allowlist @ifixtelecom.com.br` <br>· **2FA ativos** — `3` — sub: `1 pendente enroll` — tone="warning" <br>· **Sessões abertas** — `2` — sub: `idle timeout 30 min` <br>· **Rate-limit /login** — `5/15min` — sub: `por IP · D-14` |
| Operadores table | Class `.tbl`. Columns: `Operador` (avatar+name+email), `Função` (role badge), `Último login` (relative time, tabnum), `2FA` (status badge), `Sessões` (count, num right-aligned tabnum), action menu (`···`). Row height 36px (per token), 1px `--border` bottom. Hover row tint `var(--row-hover)`. |
| Top-right action | `+ Provisionar operador` btn-primary btn-sm — opens NewOperator modal (out of scope to design here — reuses prototype `NewKeyModal` modal pattern with operator-specific fields) |
| Footer note (inside card, under table header) | `provisionados via scripts/dashboard/seed-admins.sh` (`.t-label .muted`) — cross-refs 11-05 plan |

### Operator row example data (prototype)

| Operador | Função | Último login | 2FA | Sessões |
|----------|--------|--------------|-----|---------|
| Pedro Rocha (`pedro@ifixtelecom.com.br`) | `owner` (badge-warning, no dot) | agora | `ativo` (badge-healthy) | 1 |
| Rafael Mendes (`rafael@...`) | `operator` (badge-neutral) | há 3h | `ativo` | 1 |
| Camila Pires (`camila@...`) | `operator` | há 2d | `ativo` | 0 |
| Lucas Andrade (`lucas@...`) | `operator` | nunca | `aguardando enroll` (badge-warning) | 0 |

### Privacy / redaction rules

- E-mail addresses are PII per LGPD-SUBPROCESSORS.md. Operadores tab displays full e-mails because the operator viewing the tab is themselves an Ifix-operator (already authorized to see other operators' identities).
- Last-login times are stored as relative copy ("agora", "há 3h", "há 2d", "nunca") — NOT raw timestamps. Hovering an action menu MAY reveal absolute timestamps in a tooltip; that surface is out of scope for Phase 11.
- **NEVER displayed:** TOTP secrets, backup codes (post-enroll), password hashes, IP addresses, session cookie values, raw Better Auth user-id UUIDs. The Operadores tab is identity + status only.

---

## Registry Safety

| Registry | Blocks Used | Safety Gate |
|----------|-------------|-------------|
| shadcn official | card, input, button, alert, skeleton, sonner, separator, table, badge, **input-otp** (evaluate per rule above), **dialog** (evaluate per rule above) | not required for official shadcn registry |

**No third-party registries declared.** `dashboard/components.json` ships `"registries": {}` (verified 2026-05-27). Phase 11 does not add any third-party registry. Registry vetting gate: **not applicable**.

**Slopcheck (Reviews MEDIUM):** Before any `npx shadcn add input-otp` or `add dialog`, run UI-primitive inventory FIRST. If existing primitives cover the UX, do NOT install. Document the decision (install or skip) in `11-02-PLAN.md` execution evidence.

---

## Inheritance Notes

This UI-SPEC explicitly inherits from `.planning/phases/07-observability-dashboard-alerting/07-UI-SPEC.md`. Phase 07 is the **canonical visual baseline**. Phase 11 changes affecting the existing dashboard:

| Phase 07 surface | Phase 11 change |
|------------------|-----------------|
| `dashboard/src/app/login/page.tsx` | **Extended** — adds rate-limit `Alert` (`variant="destructive"` with countdown) + session-expired `Alert` (`variant="default"` with `Clock`) above existing form. Pending-state spinner inline in `Entrando…` button. Existing form layout + copy ("Gateway ifix-ai" title, "E-mail ou senha inválidos." error) unchanged. |
| `dashboard/src/app/signup/page.tsx` | **NEW** (or existing extended) — adds allowlist rejection `Alert variant="destructive"` + e-mail input `borderColor: var(--destructive)` when domain check fails server-side. |
| `dashboard/src/app/2fa/enroll/page.tsx` | **NEW** — 3-step flow: QR (`/2fa/enroll`), verify (`/2fa/enroll?step=verify` or sub-route), backup codes (`/2fa/enroll?step=backup`). State machine via `useState` step counter OR distinct route paths (executor discretion). |
| `dashboard/src/app/2fa/challenge/page.tsx` | **NEW** — challenge OTP input + "Usar código de backup" fallback that routes to `/2fa/backup` |
| `dashboard/src/app/2fa/backup/page.tsx` | **NEW** — backup code entry (alphanumeric `xxxx-xxxx` field) + "Voltar ao app autenticador" returns to `/2fa/challenge` |
| `dashboard/src/app/first-login/page.tsx` (or middleware-driven redirect) | **NEW** — gate screen post-password-verify when `user.twoFactorEnabled === false`; routes to `/2fa/enroll` on CTA click |
| `dashboard/src/app/signed-out/page.tsx` (or `/logout` landing) | **NEW** — confirmation card after Better Auth signOut completes |
| Sidebar nav (`dashboard/src/components/app-sidebar.tsx`) | Unchanged. 2FA enroll is reached via **first-login redirect**, not a sidebar item. Sidebar account chip shows `operator · 2FA ativo` line per prototype. |
| `app/(dashboard)/layout.tsx` | Unchanged. The new auth screens live OUTSIDE the dashboard layout group (in top-level `app/2fa/*` + `app/first-login/` + `app/signed-out/` routes), reusing the centered-card `AuthShell` pattern. |
| `dashboard/src/middleware.ts` | **Extended (Reviews HIGH)** — matcher excludes `login`, `signup`, `api/auth`. Phase 11 adds `2fa/*`, `first-login`, `signed-out` to exclusion so the auth flow doesn't redirect-loop. Two-stage session check per 11-02-PLAN.md Reviews HIGH-3: (1) cookie present, (2) `session.twoFactorEnabled === true && session.twoFactorVerified === true`; otherwise route to `/2fa/challenge` or `/2fa/enroll` depending on `twoFactorEnabled`. Claims read from cookie via `additionalFields` config OR session callback (decided in 11-02-PLAN.md Option A/B). |
| `dashboard/src/lib/schema.ts` | Mirror only (Reviews HIGH-2 resolved: Better Auth CLI is canonical schema source-of-truth; Drizzle mirror is optional). |
| `dashboard/src/lib/auth.ts` | Wires `twoFactor` plugin + `rateLimit` plugin (5/15min `/login`, `/sign-in/email`, `/sign-up/email`, `/2fa/verify`) + `session.expiresIn = 30 * 60` + cookie claims (`additionalFields: { twoFactorEnabled, twoFactorVerified }` OR session callback). |
| Phase 07 Settings panel | **Extended** — adds `Operadores` tab (4th tab) per §Settings → Operadores Tab above. |

---

## Anchors for 11-02-PLAN.md (do not break on rewrite)

These identifiers are referenced in `11-02-PLAN.md` (revised 2026-05-27 per `/gsd:plan-phase 11 --reviews`). UI-SPEC v2 preserves them:

- Route paths: `/2fa/enroll`, `/2fa/challenge`, `/2fa/backup`, `/first-login`, `/signed-out`, `/signup`, `/login`
- Middleware exclusion: `login`, `signup`, `api/auth`, `2fa/*`, `first-login`, `signed-out`
- Session claims: `twoFactorEnabled`, `twoFactorVerified` (must flow through cookie via `additionalFields` OR session callback — Option A/B in 11-02 plan)
- Schema source-of-truth: Better Auth CLI canonical, Drizzle `schema.ts` is optional mirror
- Rate-limit endpoints (D-14): `/api/auth/sign-in/email`, `/api/auth/sign-up/email`, `/api/auth/two-factor/verify` — 5/15min/IP
- Session idle timeout (D-15): 30 min (`expiresIn: 30 * 60`)
- Allowlist enforcement (D-13): `databaseHooks.user.create.before` rejects non-`@ifixtelecom.com.br` → HTTP 400/422 `email_domain_not_allowed`
- Provisioning script: `scripts/dashboard/seed-admins.sh` (referenced in Operadores tab footer)
- TOTP metadata: issuer `Ifix AI Gateway`, algorithm SHA1, period 30s (per CONTEXT.md D-12 specifics)
- 2FA recovery flow: backup-code-entry screen + `gateway/docs/RUNBOOK-2FA-RECOVERY.md` (delivered by 11-09-03 task — addresses Reviews consensus #6)

---

## Out-of-Scope Visual Surfaces (explicit non-goals)

Phase 11 covers backend hardening + dashboard SSO ONLY. The following are deliberately NOT designed in this UI-SPEC and have no visual contract:

| Surface | Why excluded |
|---------|--------------|
| `gatewayctl debug emit-error` CLI output (D-18.2) | Non-visual — terminal text via stdlib `slog` JSON. |
| `gatewayctl key list` CLI output (D-18.3) | Non-visual — aligned table via stdlib `text/tabwriter`. |
| `load-replay.py` script terminal output (D-05) | Non-visual — JSONL per-request + summary printf. |
| `RUNBOOK-INCIDENTS.md`, `POSTMORTEM-TEMPLATE.md`, `RUNBOOK-2FA-RECOVERY.md`, `LGPD-SIGNOFF-LETTER-TEMPLATE.md` | Markdown docs — formatting follows existing precedents. |
| Chaos test execution UX (D-07 + D-08) | Operator-driven SSH commands — no UI. |
| Phase 07 in-app screens (Overview, Tenants, Latency, Cost, Failover, Audit, Incident Detail) | Already shipped in Phase 07 — `pages.jsx` + `pages-extra.jsx` prototype is reference only, NOT new Phase 11 work. |
| AI Explain panel, AI Postmortem draft (prototype `ai-features.jsx`) | Out of milestone — v2 feature. NOT Phase 11. |
| Design canvas (`design-canvas.jsx`), tweaks panel (`tweaks-panel.jsx`) | Tooling for the design prototype itself — not shipped to prod. |

---

## Checker Sign-Off

- [x] Dimension 1 Copywriting: PASS (15 auth screens + 1 settings tab copy specified in pt-BR, operational tone, all CTAs + errors + countdowns + metadata; Operadores subtitle disambiguation added)
- [x] Dimension 2 Visuals: PASS (focal point + visual hierarchy specified per screen; logo + footer pattern; centered-card LOCKED)
- [x] Dimension 3 Color: PASS (radix-nova `.dark` palette inherited; semantic alert/badge/dot variants table; accent reserve list explicit)
- [x] Dimension 4 Typography: PASS (4 sizes × 2 weights inherited; per-element mapping table; tabnum + mono utilities specified)
- [x] Dimension 5 Spacing: PASS (inherited spacing scale + sanctioned sub-token exceptions block covers all 4 non-multiple-of-4 values with individual justifications)
- [x] Dimension 6 Registry Safety: PASS (no third-party registries; net-new shadcn blocks gated by slopcheck audit decision rule per Reviews MEDIUM)

**Approval:** approved 2026-05-27 (gsd-ui-checker — 3 FLAGs surfaced + resolved by sub-token exceptions block + Operadores subtitle disambiguation)

---

## Prototype source reference

| File | Lines | Role |
|------|-------|------|
| `/home/pedro/sync/Front  Ai-gateway.zip` (extracted to `/tmp/front-ai-gateway/`) | — | Source archive (2026-05-27T13:00Z) |
| `src/auth.jsx` | 514 | All 14 auth screens + AuthShell + LoginCard + Field + QrPlaceholder + OtpRow primitives |
| `src/tokens.css` | 213 | radix-nova `.dark` + `.light` token tables + class utilities (`.t-*`, `.btn-*`, `.input`, `.alert-*`, `.badge-*`, `.otp .slot`, `.dot-*`, `.tbl`, `.mono`, `.tabnum`, `.caret`) |
| `src/pages-extra.jsx` | 654 | Settings → Operadores tab (lines 46–125) + NewKeyModal pattern for Provisionar-operador action |
| `src/dashboard.jsx` | 586 | Phase 07 reference — Sidebar + KpiCard + Sparkline (NOT Phase 11 scope) |
| `src/pages.jsx` | 755 | Phase 07 reference — Tenants/Latency/Cost/Failover/Audit pages (NOT Phase 11 scope) |
| `src/ai-features.jsx` | 415 | Out of milestone — Explain panel + Postmortem draft (NOT Phase 11 scope) |
