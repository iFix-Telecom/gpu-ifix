---
title: Sign-off UAT live dos 6 apps cliente → gateway (Phase 8/9)
date: 2026-06-27
priority: medium
source: v1-MILESTONE-AUDIT.md (Warning)
requirements: [INT-01, INT-02, INT-03, INT-04, INT-05, INT-06]
---

# Warning — UAT cliente sem sign-off

## Problema
Gateway-side completo (6 tenants provisionados, keys mintadas, smoke scripts + runbooks).
Mas `08-HUMAN-UAT.md` + `09-HUMAN-UAT.md` estão `final_status: pending` — sem confirmação
do operador de que os 6 apps roteiam pelo gateway em prod.

## Apps a confirmar
converseai-v4 (apps/api + agents), chat-ifix, telefonia, cobranças, campanhas, voice-api.

## Fechar
- [ ] Por app: confirmar `OPENAI_BASE_URL`/key apontando pro gateway em prod.
- [ ] Rodar smoke script por tenant (exit 0) + anexar report ao HUMAN-UAT.
- [ ] Flipar `final_status` dos 2 HUMAN-UAT → passed.
- [ ] Traceability INT-01..06: "Code-complete; live UAT pending" → Complete.

Nota: Phase 10 cutover exercitou 5/6 (chat-ifix 3/4 por variância STT cloud); só falta o sign-off formal.
