---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 04
subsystem: gateway/proxy + gateway/upstreams
tags: [cascade, dispatcher, breaker, loader, override, schedule, shed, RES-08]
requires: [23-01]
provides:
  - ChatRetryableStatusInterceptor (para o 23-14 compor nos tier-1 não-finais)
  - contrato SkipBreaker() (para o 23-05)
  - dispatchResult.lastStatus/lastBody/lastContentType
  - dispatchOverrideCascade + pinnedOpts (override de schedule/shed com cascata)
affects: [23-05, 23-12, 23-14]
tech-stack:
  added: []
  patterns: [interceptor de status → sentinel errUpstreamRetryable, devolução do último erro real, breakerSkipper via errors.As]
key-files:
  created:
    - gateway/internal/proxy/chat_retryable_status.go
    - gateway/internal/proxy/chat_retryable_status_test.go
    - gateway/internal/proxy/dispatcher_status_cascade_test.go
  modified:
    - gateway/internal/proxy/dispatcher.go
    - gateway/internal/proxy/errors.go
    - gateway/internal/upstreams/loader.go
    - gateway/internal/upstreams/loader_test.go
decisions:
  - "Status retryable de chat: 402/429/500/502/503/504/524/529; 400 fica de fora (T-23-14)"
  - "Interceptor não olha o streaming flag: status != 2xx chega antes de qualquer byte de SSE"
  - "Na exaustão, o cliente recebe o status e o corpo do último erro real (peek ≤ 8 KiB); sem status registrado, o envelope 503 de antes continua"
  - "byRoleTier passa a guardar o menor tier_priority (putRoleTier) em newSnapshot e Refresh; empate mantém o primeiro escrito"
  - "Na cascata de override, HALF_OPEN conta como utilizável (igual à regra antiga do dispatchOverride); allTier1Unavailable = todos OPEN"
metrics:
  duration: ~55min
  completed: 2026-10-15
  tasks: 3
  files: 8
---

# Phase 23 Plan 04: Cascata por status HTTP + último erro real + override com cascata — Summary

Agora um 402/429/5xx/524/529 de um upstream tier-1 de chat faz o dispatcher passar ao próximo candidato antes de escrever qualquer byte, também com `stream:true`. Quando a cadeia se esgota, o cliente recebe o último erro real do provedor. O breaker não conta falha para erros `SkipBreaker()`. O loader resolve `(role, 1)` pelo menor `tier_priority`. O override de schedule/shed também percorre ①→②→③ (só externos).

## O que foi feito

**Task 1 — interceptor, último erro e SkipBreaker** (`aae6721` RED, `277545a` GREEN)
- `chat_retryable_status.go`: `ChatRetryableStatusInterceptor()`, `chatRetryableStatuses`, `chatUpstreamStatusError` (`Is` → `errUpstreamRetryable`, `Status/Body/ContentType`). Peek limitado a 8 KiB e corpo restaurado com `restoredBody`. Sem `dispatchResult` no ctx, não intercepta.
- `errors.go`: o `ErrorHandler` copia status, corpo e content-type do `chatUpstreamStatusError` para `res.lastStatus/lastBody/lastContentType`.
- `dispatcher.go`: `countsAsBreakerFailure` (over-context e `SkipBreaker()==true` não contam) e `writeLastUpstreamError`. `dispatchPinned` e `cascadeTier1` devolvem o último erro real na exaustão.

**Task 2 — loader e candidato sem proxy** (`415333b` RED, `abc1a01` GREEN)
- `loader.go`: `snapshot.putRoleTier` só grava quando a chave não existe ou quando `u.TierPriority < cur.TierPriority`. É usado tanto pelo `newSnapshot` quanto pelo `Refresh`.
- `cascadeTier1` pula candidato sem proxy e loga WARN `tier-1 candidate has no registered proxy; skipping`. Esse trecho entrou no commit da Task 1, ver Desvios.

**Task 3 — override com cascata** (`f72bdf2` RED, `82976d9` GREEN)
- `dispatchPinned(..., opts pinnedOpts)`: com `skipTier0` o candidato tier-0 fica fora. Na exaustão sem status, o envelope é `off_hours_upstream_unavailable`.
- Override com modelo pinado vai para `dispatchPinned(overridePinnedOpts)`. Override cujo nome é tier-1 da role vai para `dispatchOverrideCascade`. Fora desses casos, segue o `dispatchOverride` single-upstream de antes.
- D-D1 (`all_chat_upstreams_saturated` + `Retry-After: 30`): quando o override é tier-1, a decisão passa a ser `allTier1Unavailable()`. Nos demais casos continua a checagem antiga do breaker do override.
- RES-08: `writeSensitiveBlock` aparece 7 vezes no `dispatcher.go`, mesma contagem do HEAD base. Os gates a montante (schedule e shed) não foram alterados.

## Verificação
- `go test ./internal/proxy/ ./internal/upstreams/ -count=1 -race` passou.
- `gofmt -l .` sem saída, `go build ./...` ok.
- Testes de integração (`-tags=integration`) passaram: `Schedule|Shed|OffHours|Peak|Sensitive|Tier1|Pinned|Dispatch|Cascade|Fallthrough|DCGM|SC1` deu `ok` (64.6s).
- Todos os critérios de aceite conferidos por grep: skipTier0=4, off_hours=8, dispatchOverrideCascade=6, allTier1Unavailable=4, SkipBreaker=5, mensagem de no-proxy=1, `TierPriority <`=5, `400: true`=0.

## Desvios do plano

1. **[Rule 3 - Organização] Testes de dispatcher em arquivo novo.** O plano previa `dispatcher_pinned_test.go`. Os testes de cascata, override e no-proxy foram para `dispatcher_status_cascade_test.go`, com fixture própria de N upstreams e ReverseProxy real. Nenhum teste existente mudou.
2. **[Rule 2 - Correção] `cascadeTier1` também devolve o último erro real.** O plano exigia isso só no `dispatchPinned`. Apliquei o mesmo tratamento na cascata não pinada, para que o truth "último erro real" valha em todos os caminhos de chat. STT não é afetado, porque `sttUpstreamStatusError` não preenche `lastStatus`.
3. **Ordem dos commits.** O skip de candidato sem proxy (Task 2) entrou no commit da Task 1 (`277545a`), porque o hunk estava junto e `git add -p` não está disponível. Por isso o teste `TestCascadeTier1_SkipsCandidateWithoutProxy` passou já no commit RED da Task 2. Os testes do loader falharam no RED como esperado.
4. **Semântica de HALF_OPEN no override.** O plano fala em "tier-1 CLOSED". Em `dispatchOverrideCascade` e `allTier1Unavailable`, mantive HALF_OPEN como utilizável (só OPEN pula), igual à regra histórica do `dispatchOverride` ("dispatch unless OPEN"). Isso preserva o contrato dos testes de integração de off-hours. No caminho pinado em override, vale a regra do `dispatchPinned` (só CLOSED).

## Itens adiados (fora do escopo)
- `TestChatProxy_SSEStreamingFlushesPerChunk` é flaky por timing quando a máquina está carregada. Falhou 1 vez em 5 execuções completas e passou 10/10 isolado. `chat.go` não foi tocado neste plano.
- Na primeira rodada de integração houve falhas de ambiente: FK `api_keys_tenant_id_fkey`, migration 0033 com "already exists" e gateway sem `/health`. **HIPÓTESE:** contenção com executores paralelos usando Docker/Postgres ao mesmo tempo. Evidência que resolveria: rodar a suíte com a máquina sem outros executores. O que se sabe: as mesmas falhas não se repetiram ao rodar de novo, e o código base (2886a7a, extraído via `git archive`) passou no mesmo subconjunto.

## Notas
- O hook `clickup-link-enforce` avisou "repo GSD sem tarefa ClickUp" em cada edição. O vínculo com a tarefa fica com o orquestrador; este executor não rodou o wizard.

## Threat Flags
Nenhuma superfície nova além do `<threat_model>`. O lastBody repassado está limitado a 8 KiB (T-23-11).

## Self-Check: PASSED
- Arquivos criados existem: chat_retryable_status.go, chat_retryable_status_test.go, dispatcher_status_cascade_test.go.
- Commits presentes: aae6721, 277545a, 415333b, abc1a01, f72bdf2, 82976d9.
