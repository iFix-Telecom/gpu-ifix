# Phase 13: dashboard-user-management - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-06-14
**Phase:** 13-dashboard-user-management
**Areas discussed:** Mecanismo de role, Fluxo criar operador, Reset-2FA controlado, Audit log

---

## Mecanismo de role

| Option | Description | Selected |
|--------|-------------|----------|
| admin plugin | better-auth admin plugin — createUser/removeUser/setUserPassword/revokeUserSessions/setRole prontos; não expor ban/impersonate | ✓ |
| Coluna role manual | coluna `role` via migração + server-actions custom chamando auth.api/drizzle | |
| Você decide | researcher/planner avalia | |

**User's choice:** admin plugin

| Option (Owner) | Description | Selected |
|--------|-------------|----------|
| role no banco | role='owner' persistido; 1º operador via seed one-shot | ✓ |
| created_at ASC | owner = menor created_at (frágil; anti-pattern) | |

**User's choice:** role no banco

| Option (Enforce) | Description | Selected |
|--------|-------------|----------|
| Server Actions | server action revalida sessão+role=owner; sem rota HTTP extra | ✓ |
| Route Handlers | /api/admin/* — superfície HTTP a mais sem ganho | |
| Você decide | planner escolhe | |

**User's choice:** Server Actions
**Notes:** middleware mantido como camada adicional; enforcement server-side obrigatório (não só UI).

---

## Fluxo criar operador

| Option | Description | Selected |
|--------|-------------|----------|
| Senha temp direto | owner cria com senha temp via admin createUser | |
| Convite por e-mail/link | operador recebe link Brevo, define própria senha via token | ✓ |
| Você decide | planner escolhe | |

**User's choice:** Convite por e-mail/link

| Option (Entrega) | Description | Selected |
|--------|-------------|----------|
| Tela UMA vez | senha mostrada uma vez ao owner | |
| E-mail via Brevo | enviado por e-mail ao operador via Brevo SMTP | ✓ |
| Você decide | planner decide | |

**User's choice:** E-mail via Brevo
**Notes:** SMTP via nodemailer (não API Brevo — Authorised IPs bloqueia API; SMTP ok). Reuse sugerido do fluxo better-auth requestPasswordReset/resetPassword.

---

## Reset-2FA controlado

| Option | Description | Selected |
|--------|-------------|----------|
| Clear + re-enroll no login | apaga two_factor + enabled=false + revoga sessões → /2fa/enroll; CR-01 intacto | ✓ |
| Endpoint admin dedicado | rotaciona secret server-side; risco de contornar CR-01 | |
| Você decide | planner desenha | |

**User's choice:** Clear + re-enroll no login

| Option (Reset senha) | Description | Selected |
|--------|-------------|----------|
| Mesma rota do convite | e-mail Brevo set-password link + revoga sessões | ✓ |
| Senha temp via setUserPassword | gera temp via admin plugin + revoga sessões | |
| Você decide | planner alinha | |

**User's choice:** Mesma rota do convite

---

## Audit log

| Option | Description | Selected |
|--------|-------------|----------|
| Nova tabela no DB dashboard | admin_audit_log em bd_ai_dashboard_prod; isolado do ai_gateway | ✓ |
| Reuse audit_log do gateway | cruza isolamento Pitfall 7 | |
| Só log estruturado | NDJSON; não consultável, some no rotate | |

**User's choice:** Nova tabela no DB dashboard

| Option (Escopo) | Description | Selected |
|--------|-------------|----------|
| Toda ação admin | 4 ops + revogações; não self-service | ✓ |
| Admin + self-service | inclui troca self-service | |
| Você decide | planner define | |

**User's choice:** Toda ação admin

---

## Claude's Discretion

- Layout/UX da página self-service `/settings` e modal/menu "···" (seguir UI-SPEC v2 §Settings + padrão existente).
- Rate-limit nas server actions admin (alinhar ao rateLimit de auth.ts).
- Detalhe do esquema de migração admin-plugin (generate + drizzle-kit push, CLI-canônico).

## Deferred Ideas

- UI de leitura do audit log (aba "Auditoria") — tabela criada agora, visualização é fase futura.
- RBAC granular além de owner/operator.
- Rotação/regeneração de backup codes via UI.
