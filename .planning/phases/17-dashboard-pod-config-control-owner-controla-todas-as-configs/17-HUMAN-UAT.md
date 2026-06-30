---
status: partial
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
source: [17-VERIFICATION.md]
started: 2026-06-30T20:08:00Z
updated: 2026-06-30T20:08:00Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. Hot-reload end-to-end em prod (sem restart)
expected: Owner edita blocklist no dashboard (append known-bad host id) → em segundos o `pod_config_changed` NOTIFY recarrega o loader; a próxima provision/tick do reconciler seleciona ofertas usando a nova blocklist e pula o host; painel ao vivo mostra shutdown_reason / lifecycle trail. Nenhum SSH+sed, nenhum restart do gateway.
result: [pending]

### 2. Painel ao vivo /operacao/config (FSM + poll 10s)
expected: Painel renderiza fsm_state, leader e event trail (started_at → first_health_pass → drain → ended) com tiers de fsm.ts; StaleIndicator marca dados parados; poll de 10s reflete transições de estado em tempo real; estados skeleton/erro aparecem corretamente.
result: [pending]

### 3. Diferenciação owner vs operator nas 4 superfícies
expected: Login como owner → afordâncias de edição (lápis) por campo + bounds editáveis; login como operator → MESMOS valores read-only, zero controles de edição nas 4 superfícies; tentativa de edição via server action como operator continua barrada server-side (CR-01 fix).
result: [pending]

## Summary

total: 3
passed: 0
issues: 0
pending: 3
skipped: 0
blocked: 0

## Gaps
