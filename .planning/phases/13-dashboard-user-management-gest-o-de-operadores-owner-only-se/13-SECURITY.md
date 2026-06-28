---
phase: 13-dashboard-user-management-gest-o-de-operadores-owner-only-se
audited: 2026-06-28
auditor: gsd-security-auditor
register_authored_at_plan_time: true
asvs_level: default
block_on: high
threats_total: 15
threats_closed: 15
threats_open: 0
result: SECURED
---

# Phase 13 — Security Audit: Dashboard User Management (operadores, owner-only)

Verificação das mitigações declaradas no threat-model da Phase 13 contra o código
implementado em `dashboard/`. Modo: VERIFY (register authored at plan time) — cada
ameaça foi confirmada por leitura/grep no arquivo citado no plano de mitigação. NÃO
houve varredura por novas ameaças. Implementação é READ-ONLY; nenhum arquivo de
implementação foi modificado.

**Resultado: 15/15 ameaças CLOSED. Nenhum BLOCKER. Nenhuma unregistered flag.**

> Nota operacional (não é gap de segurança): `BREVO_SMTP_USER` / `BREVO_SMTP_PASS`
> ainda ausentes no container `ifix-ai-dashboard` na n8n-ia-vm (13-VERIFICATION UM-09
> PARTIAL). Isso é um item de runtime/entrega de e-mail — o código de transporte e as
> mitigações de segurança (creds só em env, nunca IP-locked HTTP API) estão presentes e
> verificados. Não rebaixa nenhum threat.

## Threat Register — Verification

| Threat ID | STRIDE | Disposition | Status | Evidence (file:line) |
|-----------|--------|-------------|--------|----------------------|
| T-13-SC | Tampering (supply-chain) | mitigate | CLOSED | `dashboard/package.json:24` nodemailer `^9.0.1` + `:42` `@types/nodemailer`; **NENHUM** script `install`/`preinstall`/`postinstall` em package.json; checkpoint blocking-human aprovado (13-01-SUMMARY §Package Legitimacy: v9.0.1, autor Andris Reinman, OIDC trusted publisher, zero deps de produção, zero install scripts) |
| T-13-REG | Tampering (registry) | accept | CLOSED | `dashboard/components.json:24` `"registries": {}` (somente registry oficial shadcn); ver Accepted Risks |
| T-13-mig | Tampering (prod migration) | mitigate | CLOSED | 13-02-SUMMARY §Task 3: diff additive-only (read-only introspection vs schema), aplicado `drizzle-kit push --force` exit 0 — apenas `CREATE TABLE admin_audit_log` + `CREATE INDEX` + `ALTER TABLE … ADD`, sem DROP/ALTER TYPE; SQL-asserted em prod |
| T-13-lockout | DoS (role NULL / no owner) | mitigate | CLOSED | `scripts/dashboard/seed-owner.ts:50-59` UPDATE idempotente (elege earliest, normaliza NULL→operator) + `:85-91` assert exatamente 1 owner & 0 NULL roles ou `exit(1)`; `dashboard/src/lib/seed-owner.ts:29-47` regra pura; prod: owners=1 null_roles=0 (13-02-SUMMARY) |
| T-13-authz | EoP (hidden-but-callable action) | mitigate | CLOSED | `dashboard/src/lib/admin-actions.ts:117-140` `requireOwner()` re-valida `role==='owner'` server-side; chamado em TODA ação: `inviteOperator:202`, `removeOperator:251`, `resetOperatorPassword:280`, `resetOperator2FA:315`; UI hide é cosmético (`operadores/page.tsx:284,399` gated por `isOwner`); session lida via `auth.api.getSession({headers})` `:126` |
| T-13-crossdb | Tampering (write to ai_gateway) | mitigate | CLOSED | `dashboard/src/lib/db.ts:39` DSN isolado `DASHBOARD_DATABASE_URL` (sem connection string `ai_gateway` em lugar nenhum — grep confirma só negações em doc-comments); `schema-custom.ts:25` `admin_audit_log` no DB próprio (`public`); `gateway.ts` é fetch-wrapper sobre proxy `/api/gateway/*`, sem DB/DSN próprio (grep: no Pool/drizzle/DATABASE_URL) |
| T-13-smtp | Info Disclosure (Brevo creds/origin) | mitigate | CLOSED | `dashboard/src/lib/email.ts:24-32` creds só de `process.env.BREVO_SMTP_USER/PASS` (container env, não IP-locked HTTP API — comentário `:10-14` documenta o porquê do SMTP); origin público via `BETTER_AUTH_URL` (`auth.ts:103`); 13-02-SUMMARY confirma `BETTER_AUTH_URL` = `https://ai-dashboard.converse-ai.app` em prod |
| T-13-2fa | Tampering (CR-01 credential-rotation) | mitigate | CLOSED | CR-01 hook preservado `dashboard/src/lib/auth.ts:195-214` (rejeita `/two-factor/enable` com `TWO_FACTOR_ALREADY_ENABLED` quando já habilitado); reset-2FA em `admin-actions.ts:317-334` limpa `schema.twoFactor` + `twoFactorEnabled=false` direto na tabela, **NUNCA** chama `/two-factor/enable` (grep confirma ausência de chamada em admin-actions.ts) |
| T-13-invite | Spoofing/Input (invite email) | mitigate | CLOSED | `dashboard/src/lib/allowlist.ts:27-34` `isAllowedEmail` server-side (default `ifixtelecom.com.br`); aplicado em `auth.ts:242` (databaseHooks.user.create.before, gate autoritativo) e re-checado em `admin-actions.ts:207`; senha random NUNCA retornada à UI (`admin-actions.ts:217` throwaway = 2×UUID, só usada na criação, link enviado por e-mail) |
| T-13-session | Session mgmt (remove/reset ops) | mitigate (03) / accept (05) | CLOSED | `admin-actions.ts:424-434` `revokeSessions` via adapter; chamado em removeOperator (`:253` antes do delete), resetOperatorPassword (`:285`), resetOperator2FA (`:336`); staleness do cookieCache ≤7d documentado em `auth.ts:148-160` — ver Accepted Risks (T-13-session-05) |
| T-13-audit | Repudiation (admin logging) | mitigate | CLOSED | `admin-actions.ts:156-183` `writeAuditLog` 1 row por op admin (`:226/256/288/338`); self-service `changePassword:356-368` explicitamente SEM `writeAuditLog` (D-09); metadata sem secrets (só action + actor/target identity, `:163-172`); `schema-custom.ts:25-40` tabela `admin_audit_log` com index |
| T-13-disclosure | Info Disclosure (temp-pwd/TOTP/token render) | mitigate | CLOSED | Entrega LINK não senha (`admin-actions.ts:395-417` deliverPasswordLink + `auth.ts:117-124` sendResetPassword); token lido de route-param mas NUNCA renderizado em JSX (`reset-password/[token]/page.tsx:48-49,76` — só passado a `resetPassword`); UI operadores só renderiza e-mail + tempo relativo (`operadores/page.tsx:339-343`, `operator-controls.tsx:22-23`) — nunca TOTP/backup/hash/temp-pwd/IP/raw UUID |
| T-13-selfpw | Spoofing (change-pw sem current) | mitigate | CLOSED | `dashboard/src/app/settings/page.tsx:76-80` `authClient.changePassword({currentPassword,…})` exige senha atual, verificada server-side por Better Auth; senha atual errada → `res.error` → erro inline (`:83-92`), nunca toast |
| T-13-token | Tampering (reset token replay/origin) | mitigate | CLOSED | `reset-password/[token]/page.tsx:76` `authClient.resetPassword({newPassword,token})` — token consumido pela verification table single-use do Better Auth; link origin = `BETTER_AUTH_URL` público (`auth.ts:110-113`, 13-02-SUMMARY confirma origin prod) |
| T-13-escalation | EoP (banUser/impersonate exposure) | mitigate | CLOSED | Grep gate: NENHUMA chamada a `.banUser`/`.impersonateUser`/`admin.ban`/`admin.impersonate`/`.setRole`/`.setUserPassword`/`authClient.admin` em `src/` (excl. testes). Matches residuais são só: coluna `impersonatedBy` (`schema.ts:69`, adicionada passivamente pelo admin plugin), statement `"impersonate"` no roles-map do owner (`auth.ts:81`, AC config — não endpoint wired) e doc-comments. UI só expõe invite/remove/reset-pw/reset-2FA (D-01) |

## Accepted Risks Log

### T-13-REG — shadcn add (registry trust)
**Disposition:** accept.
A instalação de primitivas shadcn (dialog/dropdown-menu/alert-dialog) confia no
registry oficial shadcn. `dashboard/components.json:24` mantém `"registries": {}`
(nenhum registry de terceiros configurado). Componentes copiados como source ao repo
e revisados. Risco residual de comprometimento do registry oficial aceito para um
painel interno de ~4 operadores.

### T-13-session-05 — cookieCache staleness (≤7 dias)
**Disposition:** accept (parte 05 da mitigação T-13-session).
`auth.ts:133-161` define `cookieCache.maxAge = 7 dias = session.expiresIn`. Após
`removeOperator`/`resetOperatorPassword`/`resetOperator2FA` as sessões são revogadas
no servidor (`DELETE FROM session`), mas decisões do Edge middleware baseadas no
cookie cache podem ficar stale por até 7 dias para o claim monotônico
`twoFactorVerified`. Para revogação IMEDIATA, o runbook exige deletar a row em
`public.session` no banco (invalida server-side independente do cache). Trade-off
aceito para painel interno de 4 admins (documentado verbatim em `auth.ts:148-160` +
RUNBOOK-INCIDENTS.md class 4 / RUNBOOK-2FA-RECOVERY.md). As ações admin JÁ deletam a
row de sessão via adapter (`revokeSessions`), então a revogação é server-side imediata
nessas operações; o staleness residual aplica-se apenas a sessões cujo cookie ainda
não reaqueceu — não a uma sessão deletada.

## Unregistered Flags

Nenhuma. 13-05-SUMMARY §Threat Flags declara explicitamente "None" (sem novos
endpoints de rede, paths de auth, acesso a arquivo, ou mudanças de schema além das já
mapeadas no threat-model). Os SUMMARYs 01-04 não introduzem nova superfície de ataque
fora do register.

## Audit Trail

- **Modo:** VERIFY (register_authored_at_plan_time=true). Cada disposition `mitigate`
  verificada por grep/leitura no arquivo citado; `accept` registrada acima.
- **Arquivos de implementação lidos (READ-ONLY, não modificados):** `auth.ts`,
  `admin-actions.ts`, `email.ts`, `db.ts`, `schema-custom.ts`, `drizzle.config.ts`,
  `viewer.ts`, `allowlist.ts`, `seed-owner.ts` (lib + script),
  `settings/page.tsx`, `settings/operadores/page.tsx`, `operator-controls.tsx`,
  `reset-password/[token]/page.tsx`, `package.json`, `components.json`, `gateway.ts`.
- **Grep gates executados:**
  - T-13-escalation: `banUser|impersonate|setUserPassword|setRole|authClient.admin` →
    nenhuma chamada de endpoint em src (só schema/AC/doc residuais).
  - T-13-crossdb: `ai_gateway|*_DATABASE_URL` → DSN isolado, sem cross-db.
  - T-13-SC: nenhum `install/preinstall/postinstall` em package.json.
  - T-13-2fa: nenhuma chamada `/two-factor/enable` em admin-actions.ts.
  - T-13-disclosure: token/totp/backup só em controlled inputs e self-enroll próprio,
    nunca renderizados no fluxo de gestão de operadores.
- **Block_on:** high. Nenhum threat aberto de severidade high → não bloqueia.

_Auditado: 2026-06-28 · gsd-security-auditor_
