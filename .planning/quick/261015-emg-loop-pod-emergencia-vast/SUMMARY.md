# Quick 261015-emg: loop do pod de emergência na Vast com o primário desligado

**Card:** [Gateway: loop do pod de emergência Vast consumiu saldo com primário desligado](https://app.clickup.com/t/86akug0x1)

## Incidente (fatos)

Em 14/10, por volta das 09:00 BRT, o 3090 primário foi desligado de propósito e o upstream `local-llm` foi desabilitado. Com isso, o breaker `local-llm` ficou OPEN e o reconciler de emergência (Phase 6) disparou.

Entre 14/10 09:00 e 15/10 00:54 BRT, o reconciler criou e destruiu **967** lifecycles em `ai_gateway.emergency_lifecycles`. Nenhum chegou a `health_ok`.

| Dia (UTC) | Instâncias criadas | Custo na fatura Vast |
|---|---|---|
| 14/10 | 354 | US$ 3,44 |
| 15/10 | 212 | US$ 2,17 |

O loop parou às 00:54:07 BRT com o deploy da Phase 23. O container novo nasce com o tracker em `closed` e não recebeu nenhum OPEN depois disso.

## Causa (código)

O loop vinha do `spawnProvisionGoroutine` (`internal/emerg/lifecycle.go`). Só a falha `offer_race_lost` levava a FSM para Cooldown. As falhas `health_timeout`, `instance_terminal_state` e `no_offers_below_cap` voltavam direto para Healthy. Como o breaker continuava OPEN, `evaluateHealthy` rearmava o gatilho no tick seguinte.

Fatores que agravaram:
- O teto `MONTHLY_EMERGENCY_BUDGET_BRL` só gera alerta no Sentry e não bloqueia nada.
- Não havia como desligar só a emergência. A única saída era remover `VAST_AI_API_KEY`, o que derruba também o primário.

## Correção

- Toda falha que não seja cancelamento agora passa pelo Cooldown de falha (`PROVISION_FAILURE_COOLDOWN_SECONDS`, padrão 60 s).
- `EMERGENCY_MAX_CONSECUTIVE_FAILURES` (padrão 3): depois de N provisionamentos seguidos sem `health_pass`, o gatilho automático trava. Um evento no Sentry registra a trava. O gatilho volta a armar quando o `local-llm` é observado CLOSED de novo, ou com um `force_provision`. O valor 0 desliga a trava.
- `EMERGENCY_PROVISIONING_ENABLED=false` é um kill-switch do gatilho automático. O `force_provision` manual continua funcionando e o primário não é afetado.
- O log de boot passa a mostrar `auto_trigger_enabled` e `max_consecutive_failures`.

## Testes

- `go test ./internal/emerg/ ./internal/config/` passou, incluindo o novo `failure_streak_test.go`.
- `go test -tags integration -run Emerg ./internal/integration_test/` passou (126 s).
