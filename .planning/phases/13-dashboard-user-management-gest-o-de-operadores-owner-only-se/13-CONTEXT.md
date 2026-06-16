# Phase 13: dashboard-user-management — Context

**Gathered:** 2026-06-14
**Status:** Ready for planning

<domain>
## Phase Boundary

Operadores do dashboard `ai-gateway` (https://ai-dashboard.converse-ai.app)
conseguem (1) trocar a própria senha em self-service e (2) o **owner** gerencia
operadores pelo browser — sem `seed-admins.sh` nem SQL manual.

**Em escopo:**
1. **Self-service change-password** — operador logado troca a própria senha
   (`authClient.changePassword`, exige senha atual). Página em `/settings`.
2. **Gestão de operadores (owner-only)** — tornar funcionais os botões
   placeholder de `settings/operadores/page.tsx` ("+ Provisionar operador" + menu "···"):
   - Criar/convidar operador `@ifixtelecom.com.br`
   - Remover operador (+ revogar todas as sessões dele)
   - Resetar senha de operador
   - Resetar 2FA de operador

**Fora de escopo** (novas capacidades = outras fases): RBAC além de owner/operator,
admin de tenants/API-keys do gateway, UI de visualização do audit log (a tabela é
criada agora; a aba de leitura é fase futura), integração de sessão com converseai-v4.
</domain>

<decisions>
## Implementation Decisions

### Mecanismo de role
- **D-01:** Instalar o **better-auth `admin` plugin**. Fornece coluna `role` +
  endpoints prontos `createUser` / `removeUser` / `setUserPassword` /
  `revokeUserSessions` / `setRole` — cobrem as 4 ops. Codebase HOJE omite esse
  plugin de propósito (`auth.ts` header + `seed-admins.sh`); Phase 13 reverte
  essa escolha porque agora precisa exatamente dessas ops. **NÃO expor**
  `banUser` / `impersonateUser` na UI (vetores de escalação).
- **D-02:** Owner determinado por **`role='owner'` explícito persistido no banco**
  (não por `created_at` ASC / `i===0`). O 1º operador existente recebe `owner`
  via seed/migração one-shot; demais = `operator`. Determinístico e auditável.
  A UI de `operadores/page.tsx` deve passar a ler o role real do banco (hoje é
  derivado de `i===0` só visualmente).
- **D-03:** Owner-gating das 4 ops via **Next.js Server Actions** — cada op
  revalida sessão + `role==='owner'` no servidor antes de chamar `auth.api`.
  Enforcement server-side é obrigatório (NÃO só na UI). Middleware permanece como
  camada adicional. Sem route handlers `/api/admin/*` (Phase 13 é só browser).

### Fluxo criar / convidar operador
- **D-04:** **Convite por e-mail com link**, entregue via **Brevo SMTP**
  (nodemailer — NÃO a API Brevo, que tem Authorised IPs ligado; SMTP não é
  afetado pela trava). Owner informa nome + e-mail; operador recebe link, define
  a própria senha via token, depois enrolla 2FA no primeiro login. Allowlist
  `@ifixtelecom.com.br` (D-13 existente em `auth.ts` databaseHooks) continua
  validando server-side.
- **D-05:** Reuse sugerido do fluxo nativo better-auth `requestPasswordReset` /
  `resetPassword` com hook `sendResetPassword`: admin `createUser` com senha
  random descartável + disparar e-mail de set-password via token (evita inventar
  tabela/endpoint de convite próprios). Researcher valida viabilidade no
  better-auth instalado.

### Reset-2FA controlado (respeitando CR-01)
- **D-06:** Reset-2FA = **clear + re-enroll no login**. Server action owner-only:
  apaga a row `two_factor` do alvo + `two_factor_enabled=false` + revoga as
  sessões dele. No próximo login o middleware roteia o operador para
  `/2fa/enroll` (estado legítimo não-enrolado). **CR-01 (`auth.ts:127-147`)
  permanece intacto** — `/two-factor/enable` só é permitido quando
  `enabled=false`, então o reset não reabre o vetor de rotação-de-credencial.
  Ação audit-logged.

### Reset de senha de operador
- **D-07:** **Mesma rota do convite** — owner dispara reset → operador recebe
  e-mail Brevo com link de set-password (reusa D-04/D-05) + revoga sessões do
  alvo. Operador define a própria senha nova.

### Audit log de ações admin
- **D-08:** Nova tabela **`admin_audit_log` no próprio `bd_ai_dashboard_prod`**
  (schema `public`) — colunas mínimas: `actor_id`, `actor_email`, `target_id`,
  `target_email`, `action`, `created_at`, `metadata`. Mesma conexão drizzle do
  dashboard; isolada do `ai_gateway` (07-RESEARCH Pitfall 7 — dashboard NUNCA
  toca ai_gateway). Tabela CLI-canônica-aware (ver regra schema.ts).
- **D-09:** Escopo auditado = **toda ação admin**: criar/convidar, remover,
  reset-senha, reset-2FA + revogações de sessão associadas. **NÃO** logar a
  troca de senha self-service do próprio operador (não é ação admin sobre terceiro).

### Claude's Discretion
- Layout/UX exato da página self-service `/settings` e da modal/menu "···" de
  operadores — seguir UI-SPEC v2 §Settings já referenciada em `operadores/page.tsx`
  e o padrão visual existente da página.
- Rate-limit nas server actions admin (se necessário) — planner decide alinhado
  ao `rateLimit` já configurado em `auth.ts`.
- Detalhe do esquema de migração admin-plugin sobre a base existente (generate +
  drizzle-kit push) — seguir workflow CLI-canônico documentado em `schema.ts`.
</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Roadmap / requisitos
- `.planning/ROADMAP.md` §"Phase 13: dashboard-user-management" — goal, scope,
  decisões locked, security surface (rodar `/gsd:secure-phase` depois).

### Código dashboard (fonte de verdade do estado atual)
- `dashboard/src/lib/auth.ts` — instância better-auth standalone; CR-01 hook
  anti-rotação 2FA (L127-147); twoFactor plugin; databaseHooks allowlist D-13;
  cookieCache/session config. **Admin plugin será adicionado aqui.**
- `dashboard/src/lib/auth-client.ts` — client better-auth (twoFactorClient);
  `adminClient()` será adicionado p/ as ops admin no browser.
- `dashboard/src/lib/schema.ts` — schema Drizzle CLI-canônico (regenerado via
  `bunx @better-auth/cli generate` + `drizzle-kit push`; NÃO editar à mão).
  `admin_audit_log` + colunas do admin plugin entram via esse workflow.
- `dashboard/src/app/settings/operadores/page.tsx` — página alvo; botões
  placeholder a ativar; regra de privacidade (nunca expor TOTP/backup/hash/IP);
  hoje deriva role de `i===0` (trocar por role real, D-02).
- `dashboard/src/middleware.ts` — gate de sessão + 2FA em 2 estágios; contrato
  de cookie-claim; pessimismo CR-01. Relevante p/ reset-2FA (D-06) e enforcement.
- `dashboard/src/lib/allowlist.ts` — `isAllowedEmail` (@ifixtelecom.com.br).
- `scripts/dashboard/seed-admins.sh` — provisionamento HTTP-only atual a ser
  substituído pela UI; documenta por que admin plugin foi evitado antes.

### Recovery / ops
- Memória `ai-dashboard-access-and-2fa-recovery.md` — URL/DSN do dashboard,
  reset de senha (scrypt N=16384 r=16 p=1 dkLen=64, `salt:keyhex`), reset 2FA
  via banco. DB prod = `bd_ai_dashboard_prod` schema `public`; DSN
  `sslmode=no-verify` + `search_path=public`.
- `gateway/docs/RUNBOOK-2FA-RECOVERY.md` — procedimento de recovery 2FA citado
  pelo CR-01 (o novo reset-2FA por UI deve ser consistente com ele).
- `gateway/docs/RUNBOOK-INCIDENTS.md` classe 4 — caveat de propagação de
  revogação de sessão (até 30min por cookieCache maxAge=1800).

### Infra (e-mail)
- Brevo SMTP `smtp-relay.brevo.com:587` (CLAUDE.md global §Brevo) — usar via
  nodemailer/SMTP. **API Brevo tem Authorised IPs ligado → não usar API**;
  SMTP não é afetado. Confirmar que o container do dashboard (n8n-ia-vm)
  alcança o relay SMTP.
</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `twoFactor` plugin + `two_factor` table: reset-2FA (D-06) opera sobre essa
  tabela + coluna `two_factor_enabled` do user.
- Fluxo `/2fa/enroll` já existe (`app/2fa/enroll/page.tsx`) — destino do
  re-enroll pós-reset.
- Fluxo nativo better-auth de password-reset (`sendResetPassword`) — base p/
  D-04/D-05/D-07 (convite + reset senha por link).
- Query de roster + session stats já implementada em `operadores/page.tsx`
  (`loadOperators`) — estende com role real.

### Established Patterns
- DB isolado do ai_gateway (07-RESEARCH Pitfall 7) — `admin_audit_log` fica no
  DB do dashboard.
- Schema CLI-canônico: NÃO editar `schema.ts` à mão; mudar config em `auth.ts`
  → `generate` → `drizzle-kit push`.
- Server components Next 15 App Router (operadores page é server component);
  ops admin = Server Actions.
- Privacidade na UI: nunca expor TOTP secret, backup codes, password hash, IP,
  cookie, UUID cru.

### Integration Points
- `auth.ts` plugins array (+admin) e hooks (CR-01 deve permanecer).
- `auth-client.ts` (+adminClient) p/ chamadas do browser.
- `operadores/page.tsx` botões "+ Provisionar" e "···" → server actions.
- Nova página/seção `/settings` p/ self-service change-password.
</code_context>

<specifics>
## Specific Ideas

- "1º operador = owner" é a regra de bootstrap, mas persistida como role
  explícito, não inferida por ordem.
- Entregar self-service change-password JUNTO com a gestão de operadores
  (decisão locked no roadmap, discuss 2026-06-14).
- Reset-2FA deve gerar um caminho controlado + audit que NÃO reabra o vetor que
  o CR-01 fecha — usuário foi explícito sobre preservar o CR-01.
</specifics>

<deferred>
## Deferred Ideas

- **UI de leitura do audit log** (aba "Auditoria" na página de settings) — a
  tabela `admin_audit_log` é criada agora; a visualização é fase futura.
- **RBAC granular** além de owner/operator — fora de escopo.
- **Rotação/regeneração de backup codes via UI** — relacionado a 2FA mas não
  pedido nesta fase.

None além disso — discussão ficou dentro do escopo da fase.
</deferred>

---

*Phase: 13-dashboard-user-management*
*Context gathered: 2026-06-14*
