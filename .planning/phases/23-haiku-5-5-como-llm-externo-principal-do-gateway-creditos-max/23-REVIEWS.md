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
