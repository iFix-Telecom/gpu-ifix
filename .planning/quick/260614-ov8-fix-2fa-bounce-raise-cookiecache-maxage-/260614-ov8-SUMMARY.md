---
quick_id: 260614-ov8
description: "Fix 2FA bounce: raise cookieCache maxAge 60→1800 (auth.ts)"
date: 2026-06-14
status: complete
commits:
  - f1b9acd fix(quick-260614-ov8): raise cookieCache maxAge 60→1800 to stop 2FA bounce loop
files:
  - dashboard/src/lib/auth.ts
  - dashboard/src/lib/auth.test.ts
  - dashboard/src/middleware.ts
  - gateway/docs/RUNBOOK-2FA-RECOVERY.md
---

# Quick Task 260614-ov8 — Fix 2FA bounce (cookieCache maxAge)

## Problema

Após login + 2FA, acessar URL direta (ou refresh/link) depois de ~60s ociosos
ricocheteava de volta pra `/2fa/challenge`. Loop. ("url direta cai na 2fa")

## Root cause (diagnosticado live, sem editar — diagnose-first)

- Sessão no banco tem `two_factor_verified=t` (hook `session.create.before` em auth.ts funciona). NÃO era o bug.
- O middleware (Edge) lê `twoFactorVerified` do **cookieCache** (`getCookieCache`), não do banco.
- better-auth reescreve o cookieCache só em `/api/auth/get-session` (session.mjs:230). Mas o dashboard
  **nunca chama get-session**: `useSession` existe só em `first-login`, e o polling do overview vai pra
  `/api/gateway` (não `/api/auth`).
- cookieCache `maxAge=60s` → expira e nunca é reaquecido → após 60s `getCookieCache` retorna null →
  fallback pessimista do middleware (`twoFactorVerified:false`) → redirect `/2fa/challenge`.

## Fix (Option A, aprovado pelo usuário)

`dashboard/src/lib/auth.ts`: `session.cookieCache.maxAge` **60 → 1800** (igual ao `session.expiresIn`
idle de 30min). O cache passa a viver toda a sessão; o middleware sempre tem os claims.

`twoFactorVerified` é monotônico (false→true, nunca reverte na sessão) → 30min de staleness nesse campo
é inócuo. Único tradeoff real: propagação de revogação de sessão/2FA ao Edge sobe de até 60s para até
30min — aceitável p/ painel interno de 4 admins (header do auth.ts já documenta aceitar staleness do cache).

Mudanças de comentário/doc acompanhando: WR-01 + CR-01 no auth.ts, comentário do middleware.ts, e
`RUNBOOK-2FA-RECOVERY.md` (wait 60s → até 30min). Sem mudança de lógica no middleware. Sem useSession/polling
(Option B rejeitado).

## Verificação

- `bunx tsc --noEmit` — limpo
- `bun run test` — 34/34 (8 arquivos)
- `grep "maxAge: 60" src/lib/` vazio; `maxAge: 1800` em auth.ts:93

## Deploy

Build na n8n-ia-vm → `:latest-dev` → `compose up -d --pull never`. Verificado live.
