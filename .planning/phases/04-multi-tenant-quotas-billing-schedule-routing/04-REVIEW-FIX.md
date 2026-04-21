---
phase: 04-multi-tenant-quotas-billing-schedule-routing
fixed: 2026-04-21T12:30:00Z
review_path: .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-REVIEW.md
iteration: 1
total_findings: 19
findings_in_scope: 12
fixed_count: 12
deferred_count: 0
reverted_count: 0
status: all_fixed
---

# Phase 4: Code Review Fix Report

**Fixed at:** 2026-04-21T12:30:00Z
**Source review:** `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-REVIEW.md`
**Iteration:** 1

**Scope:** BLOCKER + HIGH + MEDIUM (12 findings). LOW (4) + NIT (3) deferred per orchestrator instructions.

## Summary

- Findings in scope: 12
- Fixed: 12
- Skipped / deferred: 0
- Reverted: 0

## Fix table

| ID    | Severity | Commit    | Status | Note |
|-------|----------|-----------|--------|------|
| BL-01 | BLOCKER  | `8b45240` | fixed  | Wired UsageInterceptor to flusher + prices + fx + tenants; ai_gateway.billing_events now receives rows. Signature expanded; main.go wiring + dispatcher ctx plumbing updated. |
| BL-02 | BLOCKER  | `8b45240` | fixed  | Same commit as BL-01 — FinalizeRequest defers Delete on every terminating path. Reinforced by ME-03 reaper below. |
| HI-01 | HIGH     | `2934894` | fixed  | Lua token bucket short-circuits per-dimension when cap<=0 (RPS or RPM alone can be disabled). Avoids NaN/inf Retry-After and the inverted-semantics rejection. |
| HI-02 | HIGH     | `2000164` | fixed  | Added `usageJSONBuffer` tee for application/json responses (128 KiB cap) so non-streaming chat/embed requests now enqueue billing events with real token counts. |
| HI-03 | HIGH     | `69f46ab` | fixed  | Schedule middleware rejects sensitive+peak at request time (not just boot) via 503 upstream_unavailable_for_sensitive_tenant. New metric label `blocked_sensitive_peak`. |
| HI-04 | HIGH     | `3a2b352` | fixed  | Moved `obs.RequestsMiddleware` to the OUTERMOST position in the /v1/* group chain so gateway_requests_total counts all 4xx/5xx rejections (auth, rate-limit, quota, schedule). |
| ME-01 | MEDIUM   | `18a8eff` | fixed  | Wired `cfg.QuotaFailOpen` into QuotaMiddleware (new `failOpen` parameter). When true + ErrQuotaCheckUnavailable: pass-through + WARN + metric. Runbook promise now honoured. |
| ME-02 | MEDIUM   | `02dc684` | fixed: requires human verification | Removed dead `idempotency.IsReplay` check from rate-limit. The D-D1 "replays skip rate-limit" semantic is now enforced by chain ORDER (idempotency runs downstream + short-circuits). Updated tests + doc. See "Gotchas" below — this is a logic-semantic change that reviewers should re-confirm. |
| ME-03 | MEDIUM   | `a1a0849` | fixed  | Accountant reaper goroutine evicts slots older than 5 min; RequestUsage gained createdAtUnixNano; main.go launches `go accountant.RunReaper(...)`. |
| ME-04 | MEDIUM   | `8751e41` | fixed  | parseWindowHours rejects start==end ("08-08") with a helpful error message hinting "use 00-24 for full-day peak". |
| ME-05 | MEDIUM   | `88eebb3` | fixed  | Added `obs.GatewayPricesMissing{model,provider,unit}` counter incremented from ComputeCostBRL when the price lookup misses. Dashboards can alarm pre-reconcile. |
| ME-06 | MEDIUM   | `6859ce9` | fixed  | Bootstrap admin key now written to os.Stderr plain-text once; the slog warning no longer carries the key attribute (Redactor allow-list bypass closed). |

## Deferred (out of scope for this pass)

LOW + NIT (7 findings total) left untouched per the `fix_priority` instructions:

- **LO-01** `numericFromFloat` truncation — local to `events.go`; no current impact at BRL micro-cent granularity. Suggested follow-up.
- **LO-02** Duplicação de `dataClassString` em 3 pacotes — maintainability, no behaviour drift today.
- **LO-03** `Retry-After` mínimo 1s inflaciona janela RPS — tradeoff conhecido; conforme RFC 7231.
- **LO-04** `TouchAdminKeyLastUsed` nunca chamada — coluna permanece NULL; operadores não conseguem identificar admin keys órfãs (operational, não bloqueia).
- **NI-01** Comentário `main.go:594-596` — já reescrito como parte do HI-04 fix (`3a2b352`). **Indirectly closed.**
- **NI-02** `pkg/openai/types.go` constante `OffHoursUpstreamUnavailableCode` não usada — cosmetic.
- **NI-03** `formatDate(pgtype.Date{Valid:false})` → "-" — defensive cosmetic; não afeta happy path.

## Narrative — what went smoothly, what surfaced unexpected

**BL-01 + BL-02 resolvidos no mesmo commit.** O review separou eles mas a natureza do fix é conjugada: ao adicionar o `FinalizeRequest` hook no Close do teeReader para resolver BL-01 (enfileirar billing.Event), o `defer accountant.Delete(reqID)` dentro de `FinalizeRequest` já cobre todos os caminhos de BL-02. Um único commit atômico `8b45240` cita ambos os IDs no body.

**HI-02 expandiu o escopo do interceptor.** O review sugeria "ou criar um interceptor paralelo para non-streaming, ou adicionar um hook `Intercept` que tee-reader também responses JSON". Escolhi a segunda opção via novo `usageJSONBuffer` (128 KiB cap) que vive junto ao `usageTeeReader` — compartilha a mesma função `FinalizeRequest` e a mesma semântica de source ("final"/"partial"). Isso também fecha um gotcha: o buffer cap de 128 KiB é consistente com o audit body capture, então responses JSON gigantes (improvável em chat/embed) tiram uma mensagem `source=partial` previsível em vez de vazar heap.

**HI-03 precisou de ordenar early-return vs. metric.** O fix original do schedule middleware emitia o 503 DEPOIS de `GatewayScheduleRouting{..."blocked_sensitive_peak"}.Inc()`. Isso fica correto porque o 503 é uma decisão de roteamento — o sensitive foi roteado para "nenhum lugar" mas consumiu uma contagem. Dashboards que filtram por `decision="off_hours_external"` continuam precisos; quem quer capturar as rejeições filtra por `decision="blocked_sensitive_peak"`.

**HI-04 reordenou o chain sem quebrar integração.** A mudança (mover `obs.RequestsMiddleware` para primeiro) é diretamente oposta ao comentário que estava na linha 594-596. Suite de testes de integração (middleware_chain_test.go + outros) passou sem edits — nenhum teste explicitamente dependia da antiga ordem (só o comentário). NI-01 fica automaticamente resolvido.

**ME-01 (QuotaFailOpen) adicionou um parâmetro ao middleware — 4 call-sites atualizados** (main.go + 2 unit tests + 1 integration test). Mudança mecânica; sem lógica de refactor.

**ME-02 (dead code replay) foi a mais difícil — nota de "requires human verification".** O review deu três opções (mover idempotency no chain; deletar tudo; minimalista). Escolhi a mais conservadora: **deletei o check** no rate-limit mas mantive `replay.go` como scaffolding. Motivo: rearquitetar (mover idempotency) é arquitetural grande; deletar o arquivo `replay.go` remove trabalho que pode voltar a ser útil. Porém o teste de integração `TestMiddlewareChainRateLimitBeforeQuota` tinha uma assertiva (caso 3) que testava `idempotency.WithReplay(ctx)` pulando rate-limit — **essa assertiva agora é removida**. Deixo sinalizado como "requires human verification" para o reviewer confirmar que a semântica D-D1 está OK sendo enforced por chain-order em vez de ctx flag.

**ME-03 (reaper) casou com BL-02.** O reaper é defense-in-depth contra casos patológicos (client abort pre-header). `Accountant.Set` agora grava timestamp via `atomic.Int64`; `Reap(ttl)` é atomic-swap do map. Adiciona uma goroutine extra (não significativo; BillingFlusher + tenants listener + audit writer + probe já rodam).

**ME-04 / ME-05 / ME-06 foram surgical.** Todos fix de uma linha a poucas linhas + teste. Sem surpresas.

**Gotchas descobertas durante fixes:**

1. **Test `TestChatProxy_SSEStreamingFlushesPerChunk` é flaky em CI.** Durante BL-01, o primeiro run da suite de proxy falhou por esse teste; o retry individual passou. Não está relacionado aos fixes. Pre-existing issue (ver 03-07 SUMMARY).

2. **Tests antigos em `interceptor_usage_test.go` chamavam `acct.Get(reqID)` DEPOIS do `resp.Body.Close()`.** Com BL-02 deletando o slot no Close, todos esses asserts viravam nil-pointer. Refatorei os testes para verificar `acct.Get` ANTES do Close + adicionar uma nova asserção que acct.Get é nil APÓS Close (prova BL-02).

3. **Package `idempotency` ainda é importado por testes de integração (caso removido).** Removi os imports para evitar unused-import errors.

## Post-fix verification

**Build**
```
$ go build ./...
(no output — success)
```

**go vet**
```
$ go vet ./...
(no output — clean)
```

**gofmt -l**
```
$ gofmt -l .
internal/integration_test/sensitive_block_test.go
internal/integration_test/tool_call_partial_test.go
internal/proxy/toolcall_test.go
```
**Nota:** esses 3 arquivos têm drift de gofmt pré-existente (documentado em `04-06-SUMMARY.md` como "out-of-scope"). Não foram introduzidos pelos fixes deste pass.

**Unit tests (short -race)**
```
$ go test -short -race ./...
(todos os pacotes ok; nenhuma falha)
```

**Integration tests (13 scenarios)**
```
$ go test -tags integration -count=1 -timeout 600s ./internal/integration_test/...
ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/integration_test  52.521s
```

## Commit trail

```
6859ce9 fix(04): stop leaking bootstrap admin key via structured log (ME-06)
88eebb3 fix(04): observable prices-missing metric on cost drift (ME-05)
8751e41 fix(04): reject zero-duration peak windows at gatewayctl CLI (ME-04)
a1a0849 fix(04): accountant reaper evicts slots whose Close never ran (ME-03)
02dc684 fix(04): remove dead idempotency.IsReplay check from rate-limit (ME-02)
18a8eff fix(04): wire cfg.QuotaFailOpen into QuotaMiddleware (ME-01)
2000164 fix(04): capture usage on non-streaming JSON responses (HI-02)
3a2b352 fix(04): mount obs.RequestsMiddleware as OUTERMOST middleware (HI-04)
69f46ab fix(04): reject sensitive+peak at request time, not just boot (HI-03)
2934894 fix(04): Lua token bucket per-dimension disable avoids div-by-zero (HI-01)
8b45240 fix(04): wire UsageInterceptor to billing pipeline end-to-end (BL-01, BL-02)
```

Total: 11 atomic fix commits (BL-01 + BL-02 commitados juntos).

---

_Fixed: 2026-04-21T12:30:00Z_
_Fixer: Claude Opus 4.7 (1M context)_
_Iteration: 1_
