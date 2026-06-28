---
phase: 13-dashboard-user-management-gest-o-de-operadores-owner-only-se
verified: 2026-06-28T14:00:00Z
status: human_needed
score: 9/10
overrides_applied: 0
human_verification:
  - test: "Invite operator via browser: logar como owner em ai-dashboard.converse-ai.app, ir a /settings/operadores, clicar '+ Provisionar operador', preencher nome e e-mail @ifixtelecom.com.br válido, clicar 'Enviar convite' e confirmar que o e-mail de definição de senha chega à caixa do destinatário"
    expected: "E-mail entregue via Brevo SMTP com link /reset-password/[token]; operador consegue clicar no link, definir senha e fazer login"
    why_human: "Requer BREVO_SMTP_USER / BREVO_SMTP_PASS setados no container ifix-ai-dashboard na n8n-ia-vm (confirmado AUSENTES — UM-09 runtime inoperante). Verificação de entrega de e-mail não é automatizável por grep."
  - test: "Reset de senha de operador via browser: owner envia reset de senha a operador existente; operador recebe e-mail com link /reset-password/[token] funcional"
    expected: "E-mail entregue; link consome o token Better Auth (single-use); operador define nova senha e loga normalmente"
    why_human: "Mesmo bloqueio: BREVO_SMTP_USER / BREVO_SMTP_PASS ausentes no container. Fluxo UM-06 + UM-09 end-to-end requer SMTP ativo."
  - test: "Owner-gate visual: logar como operador não-owner e verificar que '+ Provisionar operador' e o menu '···' NÃO aparecem na listagem /settings/operadores"
    expected: "Nenhum botão de provision ou ações de linha visível para operador"
    why_human: "Comportamento de UI que depende de sessão real com role=operator na prod; grep não verifica renderização condicional em tempo de execução."
  - test: "Remover operador: owner acessa /settings/operadores, abre '···' de um operador teste, clica 'Remover operador', confirma o alert-dialog e verifica que o operador sumiu da lista"
    expected: "Operador removido; sessões revogadas; toast de sucesso exibido; linha desaparece após revalidatePath"
    why_human: "Exige browser com sessão owner real em prod; testa também a revalidação do RSC e o alert-dialog default-focus."
  - test: "Reset 2FA: owner reseta o 2FA de um operador, operador consegue se re-enrolar no próximo login sem violação CR-01"
    expected: "Linha two_factor apagada; twoFactorEnabled=false; sessões do operador encerradas; operador ao logar é redirecionado para /2fa/enroll e consegue completar nova inscrição"
    why_human: "Exige dois navegadores (owner + operador), estado real de banco prod e fluxo interativo de TOTP."
---

# Phase 13: dashboard-user-management Verification Report

**Phase Goal:** Operadores do dashboard ai-gateway conseguem trocar a própria senha (self-service) e o owner consegue gerenciar operadores pelo browser, sem `seed-admins.sh` nem SQL manual.
**Verified:** 2026-06-28T14:00:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | UM-01: Operador logado pode trocar a própria senha em /settings sem gate de owner e sem audit | VERIFIED | `dashboard/src/app/settings/page.tsx` chama `authClient.changePassword`; sem `requireOwner`/`writeAuditLog`; `settings/page.test.tsx` 2/2 GREEN |
| 2 | UM-02: admin plugin registrado com `adminRoles:["owner"]` e `defaultRole:"operator"` | VERIFIED | `dashboard/src/lib/auth.ts:57-89` registra `admin({ adminRoles:["owner"], defaultRole:"operator" })` com `createAccessControl`; `auth.test.ts` UM-02 GREEN |
| 3 | UM-03: seed idempotente elege 1º operador como owner e normaliza demais a operator | VERIFIED | `scripts/dashboard/seed-owner.ts` com UPDATE SET idempotente; `dashboard/src/lib/seed-owner.ts` exporta `seedOwner()`; `seed-owner.test.ts` 2/2 GREEN |
| 4 | UM-04: Server Action owner-gated: convidar/criar operador (random pwd + requestPasswordReset) | VERIFIED | `admin-actions.ts:195-233` — `requireOwner` + `inviteOperator` com disposable password + `sendMail`; `admin-actions.test.ts` UM-04 GREEN |
| 5 | UM-05: Server Action owner-gated: remover operador + revogar todas as sessões | VERIFIED | `admin-actions.ts:244-263` — `revokeSessions` ENTÃO `deleteUser`; `admin-actions.test.ts` UM-05 GREEN |
| 6 | UM-06: Server Action owner-gated: resetar senha de operador via link + revogar sessões | VERIFIED | `admin-actions.ts:273-295` — `deliverPasswordLink` + `revokeSessions`; `admin-actions.test.ts` UM-06 GREEN (coberto pelo UM-04 compound test) |
| 7 | UM-07: Server Action owner-gated: resetar 2FA (clear two_factor + twoFactorEnabled=false + revogar) — CR-01 intacto | VERIFIED | `admin-actions.ts:307-345` — delete `schema.twoFactor` + update `twoFactorEnabled=false`; NUNCA chama `/two-factor/enable`; `auth.test.ts` (h) UM-07 GREEN |
| 8 | UM-08: Tabela `admin_audit_log` existe; toda ação admin grava 1 row; self-service change-password NÃO logado | VERIFIED | `schema-custom.ts` declara `admin_audit_log`; `writeAuditLog` chamado em cada op admin (`admin-actions.ts:226/256/288/338`); `changePassword` explicitamente sem `writeAuditLog` (D-09); `admin-actions.test.ts` D-09 assertion GREEN |
| 9 | UM-09: Brevo SMTP wired em `sendResetPassword`; reachability do container confirmada | PARTIAL | Código: `email.ts` — `nodemailer.createTransport(smtp-relay.brevo.com:587)` OK; `auth.ts:117-124` — `sendResetPassword` chama `mailer.sendMail` OK. **RUNTIME: BREVO_SMTP_USER / BREVO_SMTP_PASS AUSENTES** no container `ifix-ai-dashboard` na n8n-ia-vm (confirmado via `docker inspect`). Entrega de e-mail inoperante até as creds serem configuradas. |
| 10 | UM-10: `operadores/page.tsx` lê role real; owner-gate esconde controles para não-owners | VERIFIED | `page.tsx:69` — `COALESCE(role,'operator')`; nenhum `i === 0`; `getViewerRole()` + `isOwner`; `ProvisionOperatorButton`/`OperatorRowActions` só renderizados quando `isOwner`; `page.test.tsx` 2/2 GREEN |

**Score:** 9/10 truths verificadas (UM-09 PARTIAL — código implementado, runtime inoperante)

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `dashboard/src/lib/auth.ts` | admin plugin + sendResetPassword | VERIFIED | admin({ adminRoles:["owner"] }) + sendResetPassword via mailer; CR-01 + D-13 hooks preservados |
| `dashboard/src/lib/email.ts` | nodemailer Brevo SMTP transport | VERIFIED | createTransport smtp-relay.brevo.com:587; exports mailer + sendMail + MAIL_FROM |
| `dashboard/src/lib/schema.ts` | colunas role/banned/banReason/banExpires na user; impersonatedBy na session | VERIFIED | grep confirma role/banned/ban_reason/ban_expires em user; impersonated_by em session |
| `dashboard/src/lib/schema-custom.ts` | admin_audit_log pgTable D-08 columns + index | VERIFIED | adminAuditLog com id/actorId/actorEmail/targetId/targetEmail/action/metadata/createdAt + actor_idx |
| `dashboard/src/lib/db.ts` | spread authSchema + customSchema | VERIFIED | L22-30: import authSchema/customSchema; const schema = { ...authSchema, ...customSchema } |
| `dashboard/drizzle.config.ts` | schema glob schema*.ts | VERIFIED | L54: schema: "./src/lib/schema*.ts" |
| `dashboard/src/lib/admin-actions.ts` | requireOwner + 4 server actions + writeAuditLog | VERIFIED | exports: requireOwner, inviteOperator, removeOperator, resetOperatorPassword, resetOperator2FA, writeAuditLog, changePassword |
| `dashboard/src/lib/viewer.ts` | getViewerRole() seam para RSC | VERIFIED | getViewerRole() wraps auth.api.getSession; fail-closed (null = não-owner) |
| `dashboard/src/app/settings/page.tsx` | self-service change-password Surface A | VERIFIED | authClient.changePassword; inline error em res.error; sem requireOwner/writeAuditLog |
| `dashboard/src/app/settings/operadores/page.tsx` | COALESCE(role,'operator'); sem i===0; sem seed-admins.sh | VERIFIED | grep confirmado; isOwner from getViewerRole() gates controles |
| `dashboard/src/app/settings/operadores/operator-controls.tsx` | island "use client" com provision dialog + row actions | VERIFIED | ProvisionOperatorButton + OperatorRowActions; wired a inviteOperator/removeOperator/resetOperatorPassword/resetOperator2FA |
| `dashboard/src/app/reset-password/[token]/page.tsx` | landing para link invite/reset | VERIFIED | authClient.resetPassword({ newPassword, token }); sucesso → router.push("/login"); token nunca renderizado |
| `scripts/dashboard/seed-owner.ts` | seed idempotente fail-fast sem DSN | VERIFIED | UPDATE idempotente; process.exit(1) se DASHBOARD_DATABASE_URL ausente; sem impressão de secrets |
| `dashboard/src/lib/seed-owner.ts` | função pura seedOwner para testes | VERIFIED | exporta seedOwner<T extends SeedUserLike>; testado em seed-owner.test.ts 2/2 GREEN |
| `dashboard/src/components/ui/dialog.tsx` | shadcn dialog primitive | VERIFIED | existe em components/ui/ |
| `dashboard/src/components/ui/dropdown-menu.tsx` | shadcn dropdown-menu primitive | VERIFIED | existe em components/ui/ |
| `dashboard/src/components/ui/alert-dialog.tsx` | shadcn alert-dialog primitive | VERIFIED | existe em components/ui/ |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `auth.ts` | `email.ts` | `import { mailer } from "./email"` | VERIFIED | L62: import mailer; L117-124: sendResetPassword chama mailer.sendMail |
| `admin-actions.ts` | `auth.ts` (auth.api.*) | `import { auth as realAuth }` | VERIFIED | L43; requireOwner usa auth.api.getSession com headers() |
| `admin-actions.ts` | `email.ts` (sendMail) | `import { sendMail } from "@/lib/email"` | VERIFIED | L45; deliverPasswordLink chama sendMail |
| `admin-actions.ts` | `db.ts` (schema.twoFactor/user) | `import { getDb, schema }` | VERIFIED | L44; resetOperator2FA usa schema.twoFactor + schema.user |
| `admin-actions.ts` | `schema-custom.ts` (adminAuditLog) | via db.ts schema spread | VERIFIED | schema.adminAuditLog usado em writeAuditLog L182 |
| `db.ts` | `schema-custom.ts` | `import * as customSchema` + spread | VERIFIED | L23,30: spread { ...authSchema, ...customSchema } |
| `drizzle.config.ts` | `schema*.ts` | glob `./src/lib/schema*.ts` | VERIFIED | L54: migra authSchema + customSchema incluindo admin_audit_log |
| `operadores/page.tsx` | `@/lib/viewer` | `import { getViewerRole }` | VERIFIED | L29; viewerRole → isOwner gate |
| `operadores/page.tsx` | `operator-controls.tsx` | `import { ProvisionOperatorButton, OperatorRowActions }` | VERIFIED | L30; renderizados condicionalmente por isOwner |
| `operator-controls.tsx` | `@/lib/admin-actions` | `import { inviteOperator, removeOperator, ... }` | VERIFIED | L57-61; todos os 4 actions importados e chamados |
| `settings/page.tsx` | `auth-client.ts` | `import { authClient }` + `authClient.changePassword` | VERIFIED | L41, L76-80 |
| `reset-password/[token]/page.tsx` | `auth-client.ts` | `authClient.resetPassword({ newPassword, token })` | VERIFIED | L76 |

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|--------------------|--------|
| `operadores/page.tsx` | `operators[]` | SQL direto: `SELECT id, name, email, COALESCE(role,'operator') ...` | Sim — query real ao `user` table | FLOWING |
| `operadores/page.tsx` | `isOwner` | `getViewerRole()` → `auth.api.getSession({ headers })` | Sim — sessão real Better Auth | FLOWING |
| `settings/page.tsx` | `error` / sucesso | `authClient.changePassword` → API Better Auth → DB | Sim — Better Auth valida currentPassword no DB | FLOWING |
| `admin-actions.ts` — writeAuditLog | audit row | `getDb().insert(schema.adminAuditLog)` | Sim — drizzle insert real quando DSN configurado | FLOWING |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| tsc --noEmit limpo | `cd dashboard && bunx tsc --noEmit` | exit 0 (sem erros) | PASS |
| Suite vitest completa | `cd dashboard && bun run test` | 51/51 passed, 12 arquivos, exit 0 | PASS |
| seed-owner.test.ts 2/2 GREEN (UM-03) | `bun run test src/lib/seed-owner.test.ts` | 2 passed | PASS |
| admin-actions.test.ts 5/5 GREEN (UM-04..UM-08) | `bun run test src/lib/admin-actions.test.ts` | 5 passed | PASS |
| operadores/page.test.tsx 2/2 GREEN (UM-10) | `bun run test src/app/settings/operadores/page.test.tsx` | 2 passed | PASS |
| settings/page.test.tsx 2/2 GREEN (UM-01) | `bun run test src/app/settings/page.test.tsx` | 2 passed | PASS |
| auth.test.ts 8/8 GREEN (UM-02, UM-07, CR-01) | `bun run test src/lib/auth.test.ts` | 8 passed | PASS |
| BREVO_SMTP_USER env no container prod | `docker inspect ifix-ai-dashboard` | `NO BREVO VARS` — variáveis ausentes | FAIL (ver UM-09) |

### Probe Execution

Sem probes `scripts/*/tests/probe-*.sh` declarados nesta fase. Skipped.

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| UM-01 | 13-04 | Self-service change-password /settings | SATISFIED | settings/page.tsx + page.test.tsx 2/2 GREEN |
| UM-02 | 13-02 | Admin plugin adminRoles:["owner"] + schema cols | SATISFIED | auth.ts L57-89; auth.test.ts 8/8 GREEN |
| UM-03 | 13-02 | Seed idempotente owner | SATISFIED | seed-owner.ts (script) + seed-owner.ts (lib) + test 2/2 GREEN; prod: owners=1, null_roles=0 (13-02-SUMMARY evidência) |
| UM-04 | 13-03 | Server Action owner-gated: invite | SATISFIED | admin-actions.ts inviteOperator; test GREEN |
| UM-05 | 13-03 | Server Action owner-gated: remover + revogar sessões | SATISFIED | admin-actions.ts removeOperator; test GREEN |
| UM-06 | 13-03 | Server Action owner-gated: resetar senha | SATISFIED | admin-actions.ts resetOperatorPassword; test GREEN |
| UM-07 | 13-03 | Server Action owner-gated: resetar 2FA CR-01-safe | SATISFIED | admin-actions.ts resetOperator2FA; NUNCA chama /two-factor/enable; auth.test.ts (h) GREEN |
| UM-08 | 13-03 | admin_audit_log: ops admin logadas; self-service não | SATISFIED | writeAuditLog em cada op; changePassword sem writeAuditLog; test D-09 assertion GREEN |
| UM-09 | 13-02/13-03 | Brevo SMTP wired em sendResetPassword; reachability confirmada | PARTIAL | Código completamente implementado (email.ts + auth.ts); creds BREVO_SMTP_USER/PASS ausentes no container prod → entrega inoperante |
| UM-10 | 13-05 | operadores/page.tsx role real + owner-gate | SATISFIED | COALESCE(role) no SQL; isOwner gate; page.test.tsx 2/2 GREEN |

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `admin-actions.ts` | 356-368 | `changePassword` body vazio (void args.*) | INFO — stub intencional | D-09 documenta: a função existe para garantir zero chamadas a writeAuditLog; a implementação real é o authClient.changePassword no browser (settings/page.tsx). Não é stub acidental; o contrato load-bearing (sem audit) é testado. |

Nenhum marcador `TBD`, `FIXME`, ou `XXX` encontrado nos arquivos modificados nesta fase.

### Human Verification Required

#### 1. Entrega de e-mail via Brevo SMTP (UM-09 runtime)

**Test:** Setar `BREVO_SMTP_USER` e `BREVO_SMTP_PASS` (conta Brevo 797fad001) no env do container `ifix-ai-dashboard` na n8n-ia-vm, reiniciar o container, logar como owner em `https://ai-dashboard.converse-ai.app`, convidar um novo operador @ifixtelecom.com.br e verificar se o e-mail de definição de senha é entregue.
**Expected:** E-mail chega ao destinatário com link `https://ai-dashboard.converse-ai.app/reset-password/[token]`; operador clica, define senha, faz login e é redirecionado a /2fa/enroll.
**Why human:** `BREVO_SMTP_USER` / `BREVO_SMTP_PASS` confirmados AUSENTES via `docker inspect ifix-ai-dashboard` (`NO BREVO VARS`). Entrega de e-mail transacional não é verificável por grep. Requer credenciais vivas.

#### 2. Reset de senha de operador via link (UM-06 + UM-09 end-to-end)

**Test:** Como owner, selecionar "Resetar senha" no menu `···` de um operador existente, confirmar o alert-dialog e verificar se o e-mail de redefinição chega.
**Expected:** E-mail entregue via Brevo; operador define nova senha; sessões antigas revogadas.
**Why human:** Mesmo bloqueio de creds SMTP. Requer BREVO habilitado no container.

#### 3. Owner-gate cosmética na UI (UM-10 runtime)

**Test:** Logar como operador (role=operator, não owner) em `https://ai-dashboard.converse-ai.app/settings/operadores` e confirmar que `+ Provisionar operador` e o menu `···` NÃO são renderizados.
**Expected:** Sem controles de gestão visíveis para não-owner; servidor re-valida de qualquer forma (D-03).
**Why human:** A renderização condicional é testada em teste unitário (page.test.tsx), mas a verificação com sessão real de prod (role=operator) em prod requer browser.

#### 4. Reset 2FA + re-enroll CR-01-safe (UM-07 runtime)

**Test:** Como owner, resetar o 2FA de um operador teste; como operador (sessão revogada), logar novamente e completar /2fa/enroll com um novo TOTP sem que o sistema bloqueie o re-enrollment.
**Expected:** Fluxo completo: reset 2FA pelo owner → sessões encerradas → login novamente → /2fa/enroll disponível → TOTP novo registrado.
**Why human:** Requer dois browsers simultâneos, estado real de DB prod e interação com TOTP que não pode ser automatizada por grep.

#### 5. Remover operador e verificar lista (UM-05 runtime)

**Test:** Como owner, remover um operador teste via `···` > Remover operador > confirmar alert-dialog; verificar que a linha desaparece da listagem (revalidatePath efetuado) e que o operador não consegue mais logar.
**Expected:** Operador removido; toast "Operador {email} removido."; lista atualizada server-side; login do operador falha.
**Why human:** Exige browser em prod com sessão owner real e criação de um operador teste prévio.

## Gaps Summary

Nenhum gap bloqueador. O único item com status PARTIAL é UM-09 (runtime de entrega de e-mail), que decorre de creds operacionais ausentes (`BREVO_SMTP_USER` / `BREVO_SMTP_PASS`) no container prod — não de código ausente ou incorreto. O código está completamente implementado e unit-testado com mailer mockado.

**Ação necessária pelo operador:** Adicionar `BREVO_SMTP_USER` e `BREVO_SMTP_PASS` (conta Brevo 797fad001, `smtp-relay.brevo.com:587`) ao env do stack `ifix-ai-dashboard` na n8n-ia-vm via Portainer ou docker compose, então reiniciar o container e executar o teste manual de convite (item 1 acima).

---

_Verificado: 2026-06-28T14:00:00Z_
_Verificador: Claude (gsd-verifier)_
