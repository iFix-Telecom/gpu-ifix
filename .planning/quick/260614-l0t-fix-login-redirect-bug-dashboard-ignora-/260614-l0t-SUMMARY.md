---
quick_id: 260614-l0t
description: "Fix login redirect bug — dashboard ignora data.twoFactorRedirect, manda pra /2fa/challenge"
date: 2026-06-14
status: complete
commits:
  - 248a894 fix(quick-260614-l0t): branch login redirect on twoFactorRedirect
  - 7777543 test(quick-260614-l0t): pin three login redirect outcomes
  - e054125 chore: merge quick task worktree (260614-l0t login redirect fix)
files:
  - dashboard/src/app/login/page.tsx
  - dashboard/src/app/login/page.test.tsx
---

# Quick Task 260614-l0t — Login redirect fix (2FA)

## Problema

Operadores com 2FA ativo não conseguiam logar em `https://ai-dashboard.converse-ai.app`.
Sintoma: submeter e-mail+senha corretos → tela volta com erro / `?session_expired=1`,
nunca chega na tela de código TOTP.

## Root cause (reproduzido live via Playwright)

- Backend `POST /api/auth/sign-in/email` está **correto**: retorna HTTP 200
  `{"twoFactorRedirect":true}` + cookie temporário `two_factor` (sem session real,
  porque `emailAndPassword.autoSignIn:false`).
- `dashboard/src/app/login/page.tsx` `handleSubmit` só lia `{ error }` do
  `signIn.email(...)`, ignorava o sinal `twoFactorRedirect`. No caminho sem-erro
  fazia `router.push("/")`.
- Sem session real, o middleware não acha cookie de sessão → ricocheteia
  `/` → `/login?session_expired=1`. A rota `/2fa/challenge` nunca era alcançada.

## Fix

`handleSubmit` agora captura o sinal `twoFactorRedirect` via callback `onSuccess(context)`
do better-auth e roteia pra `/2fa/challenge` antes do `push("/")`.

**Desvio do plano (Rule 1):** o literal `data.twoFactorRedirect` do plano NÃO typecheck
em better-auth 1.4.22 (`TS2339` — campo ausente no tipo do `data` aguardado). O sinal só
é exposto pela união narrowed `context.data.twoFactorRedirect` dentro do callback
`onSuccess` (confirmado contra docs oficiais better-auth via Context7). Comportamento
runtime idêntico; `tsc` limpo.

- Caminho de erro e happy-path não-2FA: inalterados.
- Sem edição em `auth-client.ts` ou `middleware.ts`.

## Verificação

- `bunx tsc --noEmit` — limpo
- `bun run test -- src/app/login/page.test.tsx` — 3/3 (2FA→/2fa/challenge, happy→/, erro→sem navegação)
- Suite completa: 8 arquivos / 34 testes pass, zero regressão

## Deploy

Push `develop` → GitHub Actions (runner self-hosted VM .30) → webhook Portainer →
recria container dashboard. Deploy fora do escopo deste plano (orquestrador/Actions).

## Pendências pós-deploy (não bloqueiam o fix)

- Validar login end-to-end no browser após deploy (e-mail + senha + TOTP).
- Trocar senha temp `IfixDash#2026` + regenerar backup codes (expostos no chat).
