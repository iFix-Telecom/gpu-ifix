---
phase: 23
reviewers: [gemini, codex]
reviewed_at: 2026-10-15T01:47:35Z
plans_reviewed: [23-01-PLAN.md, 23-02-PLAN.md, 23-03-PLAN.md, 23-04-PLAN.md, 23-05-PLAN.md, 23-06-PLAN.md, 23-07-PLAN.md, 23-08-PLAN.md, 23-09-PLAN.md, 23-10-PLAN.md, 23-11-PLAN.md, 23-12-PLAN.md, 23-13-PLAN.md, 23-14-PLAN.md, 23-15-PLAN.md, 23-16-PLAN.md, 23-17-PLAN.md, 23-18-PLAN.md, 23-19-PLAN.md, 23-20-PLAN.md]
---

# Cross-AI Plan Review — Phase 23

Claude pulado (mesma runtime). Codex rodou com `gpt-6-luna` (padrão `gpt-6.1-sol` rejeitado na conta ChatGPT).

## Gemini Review

This is a comprehensive review of the 20 implementation plans for **Phase 23: Haiku 5.5 as the primary external LLM**.

### 1. Summary
The plan is exceptionally well-structured, prioritizing a high-value hotfix (Plan 01/02) to resolve existing production errors while systematically building a robust, multi-step failover chain (Anthropic Direct → OpenRouter Haiku → OpenRouter DeepSeek). It correctly identifies the need for custom translation layers (OpenAI ↔ Messages) to support advanced features like **Prompt Caching** and **Thinking Effort**, which are unavailable via Anthropic’s OpenAI-compatible endpoint. The integration of real-cost reporting (`usage.cost`) and the inclusion of the RTX 3060 pod in the "Economia" panel significantly improve the platform's financial observability.

### 2. Strengths
*   **Tactical Prioritization:** Plan 02's hotfix deployment allows immediate resolution of the `400 Prefill` errors currently impacting `chat-ifix` without waiting for the full phase completion.
*   **Architectural Integrity:** Decisions to avoid the Anthropic SDK for the transport layer and use `httputil.ReverseProxy` ensure that existing resilience features (circuit breakers, audit logging, billing) remain active without reimplementation.
*   **Financial Accuracy:** Moving from estimated token pricing to OpenRouter's reported `usage.cost` eliminates the ~2x overestimation observed in production.
*   **Rigorous Testing:** Extensive use of "Golden Files" for translators (Plan 06/08) and comprehensive integration tests for the cascade logic (Plan 14) provide high confidence in the wire-format translation and failover behavior.
*   **User-Centric Design:** The "Blind Voice Test" (Plan 17) for TTS ensures quality standards are met by the human operator before technical activation.

### 3. Concerns
*   **Effort "Off" Support (HIGH - Plan 08):** The Anthropic Messages API doesn't have a direct "off" effort. Plan 08 Task 1 correctly flags this as a research item. If `thinking:{"type":"disabled"}` is not supported for Haiku 5.5, the fallback to "low" effort might increase costs and latency for tenants that explicitly requested no reasoning.
*   **Header Timeout in Non-Streaming (MEDIUM - Plan 04/12/14):** Increasing the header timeout to 120s is necessary for high-effort reasoning, but it may delay failover for non-streaming clients if the primary provider is slow but not "dead". The plans address this via `ChatRetryableStatusInterceptor`, but the UX impact of a 2-minute wait before cascading should be monitored.
*   **3060 Cost Historical Gap (LOW - Plan 11/13):** Cost recording for the 3060 pod is "from now on." While acceptable, it creates a permanent discontinuity in the "Economia" panel for historical data.
*   **Key Rotation / Cache Locality (LOW - Plan 05):** The "Order Fixed" selection for keys is the correct choice for cache hit rates, but if the first key is consistently hit with 429s, the cascade to the second key will result in "cold" cache hits (higher cost/latency). The 6h re-test interval is a reasonable compromise.

### 4. Suggestions
*   **Validate Thinking Blocks (Plan 06/08):** Confirm that omitting thinking blocks in tool-loops doesn't cause Anthropic to reject the conversation history. While Hypothesis H10 assumes it's safe, a Wave 0 live test (Plan 18 Task 2) is crucial.
*   **Status Code 402 Handling:** Ensure the `KeyPoolTransport` handles `402 Payment Required` specifically by marking the key as exhausted until renewal, as some providers use this instead of 400/429 for balance issues. (Plan 05 already includes 402 in the classifer).
*   **SSE Error Framing:** In `NewAnthropicSSETranslator` (Plan 06), ensure that if the upstream disconnects without a `message_stop` or `[DONE]` sentinel, a synthetic error chunk is emitted to the client to prevent "hanging" connections.

### 5. Risk Assessment: LOW-MEDIUM
The overall risk is **Low-Medium**. 

*   **Justification:** The technical complexity is high (custom SSE translators, multi-key pool logic), but the mitigation strategy is strong:
    1.  **Isolation:** The new upstreams are added alongside existing ones, allowing a quick revert to `openrouter-chat` if the new chain fails.
    2.  **Validation:** The use of dry-runs against production database clones (Plan 15) and a 24-hour validation soak (Plan 16) significantly reduces the risk of migration or configuration failures.
    3.  **Fail-Open:** The step-by-step rollout (Wave 8 main chain, Wave 9 TTS/Max Keys) ensures that failures in one area don't block the entire platform.

**Approval Recommendation:** Proceed with implementation starting from Wave 1.

---

## Codex Review

# Phase 23 plan review

## Summary

The plan set is unusually detailed: it maps requirements to implementation, tests, operational evidence, and approval gates. The dependency order mostly supports the intended rollout: ship the prefill fix early, build the routing and billing pieces, integrate them, then prepare and approve production changes. The main risks are in interfaces between plans and production validation: some plans disagree about implementation order, some fallback paths are not proven to use the new cascade, lifecycle reporting can record inaccurate costs, and production smoke tests can affect unrelated traffic. Resolve those points before execution.

## Strengths

- Plans define concrete artifacts, acceptance criteria, and tests, including fake Anthropic services where production keys are unavailable.
- The sequence keeps the direct Anthropic path dormant until credentials and terms are confirmed; OpenRouter remains available in the meantime.
- Sensitive-tenant behavior, secret handling, migration rollback, image digests, and production snapshots receive explicit attention.
- The plans distinguish provider-reported OpenRouter cost from list-price Anthropic cost and preserve the decision to show Max credits separately from cash spend.
- The rollout has distinct preparation, approval, execution, and post-deploy validation steps.

## Concerns

| Plan | Severity | Concern |
|---|---|---|
| **23-01** | **MEDIUM** | The plan contradicts itself about PASS order: Task 2 says normalize *before* PASS 4 so cache placement sees the normalized conversation; the objective and key link say the normalization follows PASS 4. Pick one order and align the tests and documentation. |
| **23-02** | **LOW** | The “without touching prod” task still reads live production state and creates a snapshot. That is read-only and consistent with the intent, but the task title should distinguish production reads from production changes. The required post-deploy observation of at least two hours also needs a clear stop/resume point. |
| **23-03** | **HIGH** | The migration guard does not protect the `openrouter-chat` move to priority 10; an existing `(llm,1,10)` row can make the initial UPDATE fail before the guard runs. The two-step TTS priority shift can also collide with arbitrary existing priorities. `ON CONFLICT (name) DO NOTHING` may silently preserve a same-named upstream with wrong settings. Add precondition checks and verify the final row definitions, not only slot occupancy. |
| **23-04** | **HIGH** | The new cascade covers pinned chat models, but the plan explicitly leaves unpinned models on the existing override path. The research identified schedule/shed overrides as a possible RES-08 bypass; ensure **all** override dispatch paths enforce sensitivity and the intended fallback policy. Also verify that the generic dispatcher/status interceptor design is suitable for the TTS path before reusing it there. |
| **23-05** | **MEDIUM** | The Redis and in-memory fallback state must remain consistent when Redis recovers; otherwise keys can be retried or remain suppressed unexpectedly. Specify reconciliation behavior and test recovery after a Redis outage. Clarify whether revoked keys should ever become eligible again after the 24-hour probe interval, and how that differs from exhaustion until renewal. |
| **23-06** | **MEDIUM** | The SSE plan relies on a custom parser and translator with several edge cases, but fixtures emphasize only selected event sequences. Explicitly cover CRLF framing, multiple `data:` lines, EOF before `message_stop`, and upstream read errors. Confirm that final usage is emitted once if Anthropic sends cumulative usage in multiple events. |
| **23-07** | **MEDIUM** | The Director plan has no explicit fallback/status test and the later wiring reuses a chat-named status interceptor. Verify that TTS dispatch supplies the context and error handling that interceptor requires, and that a retryable response reaches the TTS fallback candidate before any response bytes are written. |
| **23-08** | **HIGH** | Tool-loop compatibility remains uncertain: responses discard `thinking` blocks, while the research says Anthropic may require thinking blocks to be returned unmodified with tool results. The plan defers live validation to 23-18, after integration and rollout preparation. Make this a release gate for enabling step ①, with a tested fallback if the provider rejects the next tool-loop request. Also define behavior for conversations containing a `system` message with no adjacent user message. |
| **23-09** | **MEDIUM** | The plan and contract need to preserve the resolved precedence everywhere: explicit client request, then tenant override, then `high`. Ensure the direct Anthropic translator receives the same resolved result as OpenRouter, including explicit `off`; otherwise the two Haiku steps can behave differently during fallback. |
| **23-10** | **MEDIUM** | The `usage.cost < 100 USD` guard is an arbitrary upper bound that could reject legitimate usage and silently switch to an estimate. Document the operational basis or make the threshold configurable. Ensure provider-reported cost is captured from both JSON and the final SSE usage frame, and that missing cost is observable in metrics. |
| **23-11** | **MEDIUM** | Stop requests are not idempotent: if the database commits but the HTTP response is lost, retrying returns 404. Since the 3060 publisher is best-effort and network failures are expected, make duplicate stop events safe or provide a way to distinguish already-closed lifecycles. |
| **23-12** | **MEDIUM** | The builder test checks status fallthrough and translation composition, but should also prove request-body restoration on translation errors and verify interceptor ordering against the production composition. Confirm that the translation error path cannot pass an untranslated body to Anthropic as if it were valid. |
| **23-13** | **HIGH** | Lifecycle start is recorded only after the pod is up. That excludes provisioning time from cost accrual unless the script supplies the actual Vast start timestamp or later reconciles it. The plan also needs to create/populate `23-TTS-EVIDENCE.md` before the voice-choice checkpoint, which currently reads that file but Task 1 does not list or create it. |
| **23-14** | **HIGH** | It wires `ChatRetryableStatusInterceptor` into TTS even though 23-04 defines it around chat dispatch state and only implements chat cascade behavior. Prove that this works with the TTS dispatcher and fallback chain, or add a TTS-specific interceptor/path. Also cover the ②→③ final-error behavior in the actual production proxy composition, not just direct fake proxies. |
| **23-15** | **HIGH** | The production dry run may not faithfully reproduce the source database if schema-only plus selected data omits dependencies or migration history details. Define required extensions, roles, and schemas, then verify the restored database is sufficient before treating the dry run as a gate. The plan’s “9 aliases × 3 upstreams” should also say how existing rows and aliases are handled idempotently. |
| **23-16** | **HIGH** | The forced-open breaker smoke test is global to `openrouter-chat`, so it can divert real production traffic and affect cost or availability for up to 120 seconds. Use an isolated canary path or a controlled test mechanism that cannot change routing for unrelated tenants. Define the rollback order for stacks, migrations, aliases, and price rows. |
| **23-17** | **MEDIUM** | The production fallback test also force-opens a shared breaker, which can affect real TTS requests. Use a canary alias/tenant or otherwise isolate it. Additionally, the plan should specify the result if the chosen provider’s audio format or voice mapping differs from the expected MP3/voice contract. |
| **23-18** | **MEDIUM** | The live activation plan has a useful fallback to step ②, but its rollback says to remove the two envs only when a translation smoke fails. Extend rollback criteria to include tool-loop, forced-tool, JSON-schema, cache, and billing failures; those are the highest-risk compatibility points for step ①. |
| **23-19** | **LOW** | The plan updates generated tenant query models and loader fields, but acceptance criteria focus mostly on billing and the loader method. Include a direct test that `llm_effort` survives the sqlc query, refresh, and tenant-ID lookup, including `NULL`. |
| **23-20** | **LOW** | The CLI, admin API, and dashboard each maintain their own allowed-upstream set. That is understandable across packages, but drift is possible. Add parity tests or a shared generated contract so all three surfaces accept and reject the same upstreams and provider preference fields. |

## Suggestions

- **23-01:** Resolve the PASS 4/PASS 5 ordering contradiction before implementation.
- **23-03:** Check all destination priorities before changing any row; after insertion, assert each named upstream’s exact role, tier, priority, URL env, and auth env.
- **23-04 and 23-14:** Trace each chat, schedule/shed, and TTS route end-to-end through dispatcher context, interceptor, ErrorHandler, and breaker accounting. Add integration tests for every path that can send a request externally.
- **23-08 and 23-18:** Make a successful real tool loop a prerequisite for activating step ①, and define whether a failed loop disables that step automatically or requires operator action.
- **23-11 and 23-13:** Record the actual Vast instance start time and make both start and stop updates safe to retry.
- **23-15:** Validate the dry-run’s database fidelity and include an idempotent configuration procedure for aliases and prices.
- **23-16 and 23-17:** Replace shared breaker manipulation with isolated production canaries.
- **Cross-plan:** Add an interface checklist for shared contracts—upstream names, timeout configuration, interceptor ordering, error sentinels, usage fields, and admin route names—before 23-14 integration.

## Risk Assessment

**Overall risk: HIGH.** The plans cover a broad, production-facing change across routing, protocol translation, billing, migrations, dashboard, and external TTS. The approval and snapshot gates help, but unresolved migration collision cases, override-path sensitivity, tool-loop compatibility, and globally scoped production breaker tests could cause incorrect routing, data exposure, or customer impact. Address those issues before rollout; the remaining risks are manageable with the existing test and staged deployment approach.

---

## Consensus Summary

### Agreed Strengths
- Hotfix de prefill (23-01/02) entregue antes do resto.
- Passo ① dormente até chaves/termos; ② e ③ seguem atendendo.
- Billing com `usage.cost` real e custo do ① separado do caixa.
- Gates de aprovação, snapshot, dry-run e soak de 24 h no rollout.

### Agreed Concerns
- **Tool-loop / thinking blocks no ① (23-08, 23-18)** — os dois apontam. Gemini: validar no Wave 0 ao vivo. Codex (HIGH): tornar tool loop real pré-requisito para ativar ①, com rollback automático ou manual definido.
- **SSE: EOF sem `message_stop` (23-06)** — os dois pedem chunk de erro sintético/teste de desconexão; Codex soma CRLF, `data:` múltiplo e usage cumulativo.

### Divergent Views
- **Risco geral:** Gemini LOW-MEDIUM, "pode executar"; Codex HIGH, "resolver antes do rollout".
- **Timeout de 120 s (Gemini MEDIUM)** não aparece no Codex.
- Só o Codex levantou: colisão de prioridade na 0041 (23-03), override schedule/shed fora da cascata e RES-08 (23-04), `ChatRetryableStatusInterceptor` reutilizado no TTS (23-07/14), breaker forçado global em prod (23-16/17), stop de lifecycle não idempotente (23-11), início do lifecycle sem tempo de provisionamento (23-13), fidelidade do dump no dry-run (23-15).

### Checagem do orquestrador (grep nos planos, 2026-10-14)
- **FATO** 23-01: contradição real — `key_links` diz "chamada após PASS 4" (l.30) e a action diz "logo ANTES do PASS 4" (l.112).
- **FATO** 23-16 l.92 e 23-17 l.117: smoke força breaker de `openrouter-chat` / `openrouter-tts` por 120 s — afeta tráfego real de todos os tenants nesse intervalo.
- **FATO** 23-14 l.121: TTS compõe `ChatRetryableStatusInterceptor()`; se ele funciona fora do dispatch de chat é HIPÓTESE não verificada (resolve: ler o interceptor do 23-04 / teste de TTS cascateando).
- **Atribuição errada do Codex:** `23-TTS-EVIDENCE.md` só aparece no 23-17, não no 23-13.
- Demais pontos do Codex (0041, override RES-08, idempotência, dry-run) = HIPÓTESE até checar código/planos.

## Resolução (replan)

Replan de 2026-10-14 (gsd-planner, modo reviews). Planos editados no lugar, sem plano novo; waves e `depends_on` inalterados. As decisões travadas do CONTEXT ficaram como estavam: cadeia ①②③, effort high, RES-08, maestro-summary e "coberto por créditos". Cada ponto HIPÓTESE foi conferido no código antes de mexer no plano.

| # | Ponto (origem) | Plano(s) | Ação | Evidência |
|---|----------------|----------|------|-----------|
| 1 | Contradição PASS 4 vs PASS 5 (FATO) | 23-01, 23-09, VALIDATION | **Alterado** — ordem escolhida: PASS 5 **depois** do PASS 4. Ajustei must_haves, key_links, interfaces e action, e criei um teste novo `TestOpenRouterDirector_PrefillCacheBreakpointOnRealTurn`. O acceptance agora checa a ordem das linhas. O 23-09 (PASS 6) foi realinhado para a ordem final 1→2→3→4→5→6 | Pela construção, o "Prossiga." sintético nunca volta no request seguinte, porque o gateway só o anexa no fim. Por isso o breakpoint precisa ficar no turno real. O behavior do 23-01 (l.109) e o 23-PATTERNS l.171 já diziam "depois"; só a action dizia "antes" |
| 2 | Smoke com force-open global de breaker (FATO) | 23-16, 23-17 | **Alterado** — o ③ e o Kokoro passam a ser provados ao vivo por **alias canário** pinado só no upstream alvo (`p23-canary-step3`, `p23-canary-tts`), que é removido no fim. A lógica de cascata fica provada no 23-14 com a composição real. O force-open global virou item **opcional** no checkpoint, com o impacto descrito e autorização escrita do Pedro | `cmd/gatewayctl/breaker.go` só tem `force-open --upstream X --ttl D`, gravando a chave Redis `gw:breaker:force:{name}` sem escopo de tenant ou alias. O 23-16 usava a flag `--name`, que não existe. `model-alias set/delete` existe (`cmd/gatewayctl/model_alias.go`) |
| 3 | `ChatRetryableStatusInterceptor` no TTS (HIPÓTESE → verificada) | 23-07, 23-14, VALIDATION | **Procede como verificação** (o reuso funciona). Adicionei `TestHaikuChain_TTSStatusCascade`: 429 e 503 cascateiam para kokoro-tts, 400 não cascateia, e o caminho cascadeTier1 também foi coberto. O helper `ExternalChatInterceptors` é compartilhado entre o main e o teste | TTS usa `proxy.NewDispatcher` (main.go l.1391). `dispatchTo` instala `dispatchResult` no ctx em qualquer role (dispatcher.go l.734-760), que é o único gate do interceptor |
| 4 | Tool loop com blocos thinking descartados (consenso) | 23-08, 23-12, 23-14, 23-18 | **Alterado.** No 23-08 entram `IsToolLoopContinuation` e `ToolLoopMode` (omit = padrão H10; disable = thinking desligado só na continuação), mais um golden `_disable`. O 23-12 ganha o modo `skip` (a continuação vai ao ② sem chamar a Anthropic e sem contar no breaker). O 23-14 ganha a env `ANTHROPIC_TOOL_LOOP_MODE`. No 23-18, os **gates g1–g5 rodam direto na API antes do PUT**, usando os golden como corpo, e decidem o modo; se g1/g3/g4/g5 falharem, ① não é ligado | Research l.371: H10 é HIPÓTESE ("Validar no Wave 0") |
| 5 | Bordas do SSE (consenso) | 23-06, VALIDATION | **Alterado** — novos casos: EOF antes de `message_stop` (gera erro sintético `upstream_stream_truncated` + usage + `[DONE]`), erro de leitura (`upstream_read_error`; o tradutor devolve io.EOF), CRLF, várias linhas `data:` e usage cumulativo emitido uma vez com os últimos valores | — |
| 6a | 0041: colisão em (llm,1,10) antes da guarda; homônimo errado preservado pelo ON CONFLICT (HIPÓTESE) | 23-03 | **Procede, alterado.** A guarda de posição foi movida para **antes** do primeiro UPDATE (cobre 0/10/20). Entraram uma guarda de prioridade TTS ≥ 990 e uma conferência final da **definição completa** dos 4 upstreams. Dois testes novos: `OccupiedSlot10Fails` e `SameNameWrongDefinitionFails` | No plano, o UPDATE para 10 vinha antes do bloco DO. Hoje só existe `openrouter-chat` (llm,1,0) (`0008_seed_upstreams.sql`), mas prod diverge das migrations (memória, adendo 2026-09-01) |
| 6b | Override schedule/shed fora da cascata e RES-08 (HIPÓTESE) | 23-04 | **RES-08: não procede.** **Política de fallback: procede, alterado.** Modelo não pinado com override agora cascateia entre os tier-1 (`dispatchOverrideCascade`). O D-D1 só dispara com todos os tier-1 fora (`allTier1Unavailable`) | RES-08: `schedule/middleware.go` l.79-100 + CHECK `chk_sensitive_no_peak`, e `shed/middleware.go` l.214-233 barram o tenant sensível antes de gravar o override. Fallback: `dispatchOverride` atendia um único upstream e não tinha dispatchResult (dispatcher.go l.679-725). Com o loader do 23-04, Resolve(llm,1) passa a ser ①, e o shed D-D1 olhava só o breaker do override |
| 6c | KeyPool: reconciliação Redis↔memória; chave revogada (HIPÓTESE) | 23-05 | **Procede como lacuna de especificação, alterado.** A escrita vai para a memória e depois, best-effort, para o Redis. A flag `redisDirty` dispara a regravação quando o Redis volta, e a leitura usa o estado mais restritivo dos dois. revoked fica sem `until`, com re-teste a cada 24 h. Entraram 3 testes | O plano só dizia "estado em memória espelha o Redis (fallback)" |
| 6d | Teto de 100 USD e custo no frame SSE final (HIPÓTESE) | 23-10 | **Teto: justificado, não configurável.** Pior request ≈ US$ 0,82 (HIPÓTESE: contexto de 1M), então a margem é de ~100×. **SSE: já coberto** (behavior "mesmo via SSE"); acrescentei o caso SSE sem `cost`. A ausência de custo fica observável via `cost_source` (consulta no 23-16 passo 8) | Plano l.83 já tinha o caso SSE |
| 6e | Stop de lifecycle não idempotente (HIPÓTESE) | 23-11 | **Procede, alterado.** Um stop repetido de lifecycle já fechado devolve 200 `already_closed` sem alterar o custo; 404 fica só para "nunca houve start". O start corrige `started_at` com LEAST. Entrou `GetAuxPodLifecycle` | O plano tinha `CloseAuxPodLifecycle ... AND ended_at IS NULL` e respondia 404 quando não havia lifecycle aberto |
| 6f | Start da 3060 sem provisionamento (HIPÓTESE) | 23-13 | **Procede, alterado.** O start passa a ser gravado logo após o create, com o horário de antes do create; usa `start_date` da Vast se existir (HIPÓTESE, a conferir). O stop vai **dentro de `vast_destroy`**, cobrindo os 6 pontos de destroy, inclusive boot falho | O plano dizia "chamar após o pod ficar de pé". Os destroys estão em `unified3060.py` l.191/828/903/1040/1073/1330 |
| 6g | Fidelidade do dump e idempotência (HIPÓTESE) | 23-15, 23-16 | **Procede, alterado.** Dump com `--no-owner --no-privileges` e 3 conferências de fidelidade (extensões, colunas/constraints/triggers, goose 40). Passo 4f lista criar/atualizar/sem mudança por alias e por preço. O rollback ganhou ordem numerada 1–6, referenciada no 23-16 | goose fica em `ai_gateway` (internal/db/migrate.go l.31). Nenhum `CREATE EXTENSION` nas migrations (grep). `UpsertModelAlias` faz ON CONFLICT (alias, upstream_name) e o `set` sem flags preserva as prefs. `prices set` = Expire + Insert na mesma transação |
| 6h | Critérios de rollback do ① (HIPÓTESE) | 23-18 | **Procede, alterado.** Critérios: smokes a–h, cache, billing, painel; verificações em +30 min e +2 h (400 de tradução, 4xx > 1% como HIPÓTESE de limiar, fallthrough > 20%). Há um rollback leve (`skip`) antes do rollback completo | O plano antigo só fazia rollback em erro de tradução |
| 6i | `llm_effort` NULL no sqlc/loader (HIPÓTESE) | 23-19 | **Procede, alterado** — novo teste de integração `TestTenantLLMEffort_RoundTrip` (low, NULL → "", bogus rejeitado pela CHECK) | O plano só tinha testes unitários |
| 6j | Paridade CLI/admin/dashboard (HIPÓTESE) | 23-20 | **Procede (barato), alterado** — fixture único `gateway/internal/admin/testdata/provider_prefs_upstreams.json` e um teste de paridade em cada uma das 3 superfícies | `build-dashboard.yml` faz checkout completo e roda vitest em `dashboard/`, então o caminho `../gateway/...` existe no CI |
| 6k | 23-12: body não traduzível; restauração do body; ordem dos interceptors (Codex) | 23-12, 23-14 | **Alterado.** O `anthropicLocalGuard` responde 400 local sem rede (teste: fake recebe 0 requests). O 23-14 (i) prova que ② recebe o body OpenAI original. A composição de produção é testada pelo helper compartilhado | O plano antigo mandava o body cru à Anthropic |
| 6l | 23-09: mesma decisão de effort em ① e ② (Codex) | 23-12, 23-14 | **Alterado** — `reasoning:{"enabled":false}` no behavior do 23-12 e caso (j) do 23-14 (paridade no fallback) | — |
| 6m | 23-02: título "sem tocar prod" e ponto de retomada das 2 h (Codex LOW) | 23-02 | **Alterado** — título ajustado; `deploy_at`/`recontagem_apos` gravados no evidence, e o SUMMARY "aguardando recontagem" permite retomar só a query | — |
| 7 | Timeout de cabeçalho de 120 s atrasando a cascata (Gemini MEDIUM) | 23-12, 23-14 | **Decisão:** mantém 120 s (necessário em resposta não-streaming com effort high) e **passa a cascatear quando estoura**: `NewChatFallthroughTransport` em ①, ② e openrouter-tts; ③ fica final. Pior caso documentado: 120 s em ① + 120 s em ② | FATO: o openrouter-chat usa `http.Transport` puro (main.go l.1922-1927), e só o STT tinha `preByteTimeout` (transport.go `NewSTTFallthroughTransport`). Hoje, timeout de cabeçalho no chat vira **502 terminal**, sem cascata. O erro de timeout de cabeçalho ocorre antes de qualquer header, portanto é pré-byte também em SSE |
| 8 | `23-TTS-EVIDENCE.md` (atribuição errada do Codex) | 23-17 | **Alterado no 23-17** — a Task 1 agora cria o arquivo (`## Candidatos` com Content-Type e MP3 ok por candidato, `## Método cego`), e o verify confere. O contrato de MP3/voz pedido pelo Codex no 23-17 também foi incorporado: candidato sem MP3 válido é descartado, e o passo 4b reverte o alias se o smoke vier sem `audio/mpeg` | O arquivo só aparece no 23-17 (grep). O 23-13 não o referencia |
| — | Gemini: effort "off" (HIGH) | 23-08, 23-18 | **Já coberto, sem mudança** — fallback "low" só com HIPÓTESE no código, seção no SUMMARY e aviso ao Pedro; medido ao vivo no smoke (h) | 23-08 Task 1 e 23-18 (h) |
| — | Gemini: 402 (sugestão) | 23-05 | **Já coberto** — 402 → exhausted no classificador | 23-05 behavior |
| — | Gemini: cache frio ao trocar de chave (LOW) | — | **Sem mudança** — a ordem fixa é decisão travada do CONTEXT ("Chave esgotada") | CONTEXT |
| — | Gemini: histórico da 3060 (LOW) | — | **Sem mudança** — "só daqui para frente" é escolha de discrição registrada no CONTEXT | CONTEXT |
| — | Codex: checklist cruzado de interfaces | 23-14, 23-20 | **Parcial** — sem documento novo. Os contratos de composição (`ExternalChatInterceptors`) e de upstreams com prefs (fixture) passaram a ter fonte única testada; os demais contratos seguem nos blocos `<interfaces>` de cada plano | — |

Ameaças novas: T-23-73 (timeout de cabeçalho), T-23-74 (force-open global), T-23-75/77 (tool loop no ①), T-23-76 (body não traduzível enviado à Anthropic).
