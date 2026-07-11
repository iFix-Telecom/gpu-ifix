---
phase: 20
plan: "06"
subsystem: primary-provisioning
tags: [uat, coldstart, fast-fail, ff-02, ff-03, vast-logs-api, live]
requires: [20-04, 20-05, 20-07]
provides: [ff03-onstartlog-fix, live-uat-evidence]
affects:
  - gateway/internal/emerg/vast/client.go
  - gateway/internal/emerg/vast/client_test.go
  - gateway/internal/admin/operations_test.go
tech-stack:
  added: []
  patterns: [result-url-materialization-retry]
key-files:
  created: []
  modified:
    - gateway/internal/emerg/vast/client.go
    - gateway/internal/emerg/vast/client_test.go
    - gateway/internal/admin/operations_test.go
key-decisions:
  - "20-07 e o fix FF-03 (retry) foram deployados em prod DURANTE esta UAT — o gateway prod rodava develop-2d38dbc (sem 20-07); agora roda develop-69b6ae9."
  - "FF-02/regime-3 estava inerte em prod: a Vast logs API materializa o result_url ~2s após o PUT, o GET imediato pegava 403 → FetchError perpétuo. Fix: retry no GET (onstartFetchAttempts=4 × onstartFetchBackoff=1s)."
  - "Regime 1 (created stall) não é induzível via config: o picker usa market_cheapest e só honra allowlist com failStreak>=2. Regime 3 byte-frozen exige tarpit S3 (key-404 causa crash-loop, não stall). Ambos viram follow-up."
requirements-completed: [FF-03, AL-01, OBS-11]
duration: "~2h30 (UAT ao vivo + 3 deploys)"
completed: 2026-07-11
---

# Phase 20 Plan 06: HUMAN-UAT coldstart fast-fail em Vast real — Summary

UAT ao vivo em Vast pago (4 lifecycles, machine 141325 California). O achado central: **o CÓDIGO do 20-04/20-07 não estava em produção** — o gateway prod rodava `develop-2d38dbc`. A UAT virou um ciclo diagnóstico→fix→deploy→re-validação e destravou um bug crítico (FF-02 inerte).

- **Lifecycles:** 139 (ready happy-path), 140 (`public_port_bind_timeout` @3min — bug pré-20-07), 141 (regime-3 tentativa, crash-loop), 142 (ready, pós-fix, no-regression).
- **Gasto total:** ~R$0.25 (~$0.05).

## Provado ao vivo

- **FF-03 root cause + fix (commit `69b6ae9`):** `OnstartLog` fazia PUT→GET imediato; a Vast logs API materializa o `result_url` (S3) ~2s depois → 403 → FetchError → FF-02 nunca armava. Fix = retry no GET. **Prova:** métricas `gateway_vast_api_requests_total{op="fetch_logs",status="200"}` = 0 (antes) → 21 (pós-fix, um coldstart).
- **20-07 deployado + não-regressão:** lifecycle 140 (prod sem 20-07) morreu por `public_port_bind_timeout` @~3min = bug reproduzido; após deploy (`75bc822`/`69b6ae9`) o coldstart de 21min não é mais falso-morto.
- **AL-01:** machine 141325 auto-allowlist após markReady; fora do blocklist; sem overlap. (cap-20 FIFO não estressado.)
- **BL-01 exclusão (global-failure safety):** `public_port_bind_timeout` (140,141) NÃO adiciona ao blocklist — machine-attributable-only confirmado.
- **Q2 resolvido:** o JSON da Vast logs API usa a key `result_url` (decode do gateway já correto); host `s3.amazonaws.com/public.vast.ai`, https ✓.
- **allowlist_preferred:** confirmado ativar só com `failStreak>=2` (reconciler.go:1360).
- **Happy path + inferência local:** pod tier-0 serve `model.gguf` → "Brasília".

## Deviations from Plan

**[Rule 1/2 — bug + missing prereq]** O plano assumia o código já em prod. Não estava. A UAT expandiu para: (1) push+build+deploy do 20-07; (2) fix do flake `operations_test` (boundary meia-noite BRT) que bloqueava o CI (`efbbc6d`,`75bc822`); (3) descoberta e fix do FF-03 materialization race (`69b6ae9`), com deploy e re-validação.

**Total deviations:** 3 (todas necessárias p/ a UAT ser possível). **Impacto:** positivo — resolveu um bug crítico que deixaria FF-02/regime-3 inerte em prod indefinidamente.

## Issues Encountered / Follow-ups

Regimes 1 e 3 **não foram exercitados ao vivo** (limitação de indução, não falha de código):
1. **Regime 1 (created stall):** não induzível via config — picker `market_cheapest`, allowlist só com `failStreak>=2`. Precisa host flaky orgânico ou mecanismo de pin.
2. **Regime 3 (download stall byte-frozen):** key-404 causa crash-loop (`exited↔running`), não stall. Precisa tarpit S3 (bytes congelados, container vivo).
3. **SSRF host-allowlist no `FetchLogs` GET** (20-03 finding #8): host real agora observado (`s3.amazonaws.com/public.vast.ai`) — pronto p/ implementar.
4. Gate `download_in_flight` do 20-07: agora TEM telemetria (FF-02 arma), mas o path port-bind específico não foi observado (portas bindaram rápido nos happy-paths) — coberto por unit tests.

## Next Phase Readiness

Phase 20 completa (7/7 plans). Código de resiliência de coldstart deployado e o bug crítico de telemetria (FF-03) resolvido e provado em prod. Follow-ups 1-3 são novos itens de backlog.
