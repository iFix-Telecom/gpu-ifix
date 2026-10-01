---
status: complete
quick_id: 261001-9fh
---
# 261001-9fh — shed: fim do flapping On↔Recovering

Commits: 52209f6 (Recovering continua sheddando, middleware isShedding), 9c45390 (dwell hysteresis na FSM).

- 1ª tentativa (só Recovering sheddando) NÃO resolveu: SC2 87/81 bounces. Causa real (FATO): shed segura inflight do
  tier-0 no limiar (teste: shed_inflight_max=4 = cap default por tenant) → sinal alterna request-a-request.
- Dwell: On→Recovering após min(3s,recover) limpo contínuo; Recovering→On após min(3s,arm) saturado sustentado;
  Recovering→Off após recover_seconds limpo ininterrupto (blip reinicia).
- SC2 (integration_slow, CI_ALLOW_TIGHT_SHED_TIMING=1): 192 → **24** transições (4/ciclo, 0 bounce). Integração
  completa 125 PASS / 0 FAIL. Unit shed -race ok.
- Prod: local-llm shed_inflight_max=1000 → shed praticamente inativo hoje; mudança não altera tráfego atual.
