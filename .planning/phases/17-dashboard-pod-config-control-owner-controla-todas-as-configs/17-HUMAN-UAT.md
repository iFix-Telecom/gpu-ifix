---
status: resolved
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
source: [17-VERIFICATION.md]
started: 2026-06-30T20:08:00Z
updated: 2026-06-30T20:08:00Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. Hot-reload end-to-end (sem restart)
expected: Owner edita blocklist no dashboard (append known-bad host id) → em segundos o `pod_config_changed` NOTIFY recarrega o loader; a próxima provision/tick do reconciler seleciona ofertas usando a nova blocklist e pula o host; painel ao vivo mostra shutdown_reason / lifecycle trail. Nenhum SSH+sed, nenhum restart do gateway.
result: PASS (parcial — mecanismo hot-reload provado no gateway-dev 2026-07-01 via admin API). PATCH /admin/primary/config blocklist append 88888 → HTTP 200 → gateway logou `pod_config refreshed` imediatamente (NOTIFY→loader snapshot swap, SEM restart) → GET refletiu o novo valor. Bounds validation (cap_primary=99→400 "value out of bound"), cross-field (up_hour==down_hour→400), auth (sem X-Admin-Key→401) todos provados. Migration 0031 aplicou no boot; seed env→DB OK. PENDENTE: observar o reconciler PULAR o host num ciclo de provision real (força-up — schedule disabled no dev); o "reconciler lê snapshot" está provado por unit+integration.

### 2. Painel ao vivo /operacao/config (FSM + poll 10s)
expected: Painel renderiza fsm_state, leader e event trail (started_at → first_health_pass → drain → ended) com tiers de fsm.ts; StaleIndicator marca dados parados; poll de 10s reflete transições de estado em tempo real; estados skeleton/erro aparecem corretamente.
result: PASS (2026-07-01, dashboard-dev). Painel ao vivo renderiza FSM (dormindo/líder/emergency healthy) + schedule + atualiza a cada 10s.

### 3. Diferenciação owner vs operator nas 4 superfícies
expected: Login como owner → afordâncias de edição (lápis) por campo + bounds editáveis; login como operator → MESMOS valores read-only, zero controles de edição nas 4 superfícies; tentativa de edição via server action como operator continua barrada server-side (CR-01 fix).
result: PASS (2026-07-01). Owner edita (lápis + save persiste no pod_config, ex monthly_budget_brl 2400→1000 propagou p/ enforcement + display /operacao); operator só vê valores, sem lápis. Confirmado pelo usuário.

## Summary

total: 3
passed: 3
issues: 0
pending: 0
skipped: 0
blocked: 0

## Fixes de integração achados no UAT (todos em develop, testados + deployados dev)
1. 3afad16 — config não carregava: fetchPodConfigServer (RSC) não repassava cookie → middleware bounce /login HTML.
2. 4161423 — save do owner falhava: refetch-de-validação usava fetchPodConfig (URL relativa) → ERR_INVALID_URL server-side; trocado p/ fetchPodConfigServer.
3. 1660f19 — /operacao budget stale: GET /admin/operations lia h.cfg (env boot) em vez do snapshot pod_config; wired o loader no OperationsHandler.
4. 6834255 — sidebar sem logout/nav de conta: adicionado SidebarFooter (identidade + Sair + Configurações + Operadores owner-only).
5. a729369 — /settings sem sidebar: movido p/ dentro do route group (dashboard) (URL idêntica).

notas:
- Deploy dev 2026-07-01: gateway-dev (vps-ifix-vm, stack 34) rodando imagem Phase 17 (6df75b33, version=dev), migration 0031 aplicada, podconfig LISTEN ativo. GHCR latest-dev = digest d5e85f5a (Phase 17). Rollback: tag pre-p17-3dcc083 (imagem antiga) na VM.
- Admin key temp UAT: id 00aff493-fbd7-4258-af25-77fc35fa4d31 (label uat-17-hotreload) — REVOGAR após UAT.
- Dashboard-dev PROVISIONADO 2026-07-01 pra Tests 2+3:
  - URL: https://ai-dashboard-dev.ifixtelecom.com.br (aponta pro gateway-dev)
  - Owner: owner-uat@ifixtelecom.com.br / UatOwner@2026 (role owner)
  - Operator: operator-uat@ifixtelecom.com.br / UatOperator@2026 (role operator)
  - GOTCHA: 1º login exige enroll de 2FA (TOTP) — escanear QR em /2fa/enroll antes de chegar em /operacao/config (gate por design, igual PROD).
  - Infra (teardown pós-UAT): container ifix-ai-dashboard-dev (vps-ifix-vm), DB bd_ai_dashboard_dev (DO), CF A record ai-dashboard-dev (id 510d59a4475afbe9f9d5652c21f8e3c4), imagem ifix-ai-dashboard:dev-p17.
  - Admin keys temp a revogar pós-UAT: 00aff493 (uat-17-hotreload), 03a7027a (dashboard-dev-p17).

## Gaps
