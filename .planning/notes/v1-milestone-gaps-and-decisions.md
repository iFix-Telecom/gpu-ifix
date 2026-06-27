---
title: v1 milestone — gaps abertos + decisões pendentes (pós-audit)
date: 2026-06-27
context: v1-MILESTONE-AUDIT.md (status gaps_found)
---

# v1 — estado real pós-audit (2026-06-27)

Audit completo: `.planning/v1-MILESTONE-AUDIT.md`. v1 ~95% real; 5 pilares em prod.
REQUIREMENTS.md reconciliado: 65/82 done (eram 25 — 40 checkboxes stale corrigidos).

## 17 requirements ainda abertos

### 🔴 Blocker 1 — UM-01..10 (Phase 13 nunca executada) — DECISÃO PENDENTE
Gestão de operadores dashboard + change-password self-service não existem. Phase 13 tem
5 PLANs + CONTEXT/RESEARCH/UI-SPEC/VALIDATION mas 0 SUMMARY. `/settings/operadores` é
roster read-only (Phase 11); admin plugin better-auth não instalado.
**Decisão necessária:** executar Phase 13 (`/gsd:execute-phase 13`) OU adiar UM-* pro v2.
É o único feature gap de verdade do v1.

### 🔴 Blocker 2 — TEN-04 metering STT/embed → todo [[stt-embed-metering-fix]]
Quota áudio/embed não-enforçada + /consumo 0s. Fix pequeno no gateway.

### 🟡 Warning — INT-01..06 UAT cliente → todo [[phase8-9-client-uat-signoff]]
6 apps roteando pelo gateway sem sign-off formal. Code/infra prontos.

## Tech debt (não-bloqueante)
- Phase 06.5 (PRV-01..10) sem VERIFICATION.md — código presente+verificado, falta artefato. Backfill.
- Phase 06 sem VERIFICATION.md (superseded por 06.5).
- PRD-07: DNS/TLS live em `ai-gateway.converse-ai.app` (era `gateway.ifix.com.br`) — corrigir texto do req.
- OBS-02: cardinality Prometheus ≤10k static-verified, contagem live não confirmada.
- Phase 03 `scaffold_imports.go` (dep-pin) pode estar não-limpo — verificar.

## Caminho pra fechar v1 honesto
1. ✅ Reconciliar 40 checkboxes stale (FEITO 2026-06-27).
2. Decidir UM-01..10: executar Phase 13 vs adiar v2.
3. Fix metering (todo) → fecha TEN-04 + OBS-09 /consumo.
4. Sign-off UAT clientes (todo) → fecha INT-01..06.
5. Backfill 06.5 VERIFICATION + corrigir PRD-07.

Depois: `/gsd:complete-milestone` (versão NÃO v1.0 — já taggeada no Phase 10; usar v1.1 ou re-tag).
