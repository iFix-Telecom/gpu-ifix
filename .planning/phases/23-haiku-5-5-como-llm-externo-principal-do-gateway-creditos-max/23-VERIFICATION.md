---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
verified: 2026-10-16T00:45:00Z
status: human_needed
overall: passed_partial
score: 8/9 requisitos verificados; 1 (HAIKU-07) com item dependente de tempo pendente
overrides_applied: 0
gaps: []
time_gated:
  - item: "Recontagem de 24 h pós-rollout (23-ROLLOUT-EVIDENCE.md, seção 'Checkpoint — Task 3')"
    liberado_a_partir_de: "2026-10-16 02:51 BRT (05:51 UTC)"
    requisito: HAIKU-07
  - item: "Confirmação do Pedro do rollout após 24 h (23-16 Task 3)"
    liberado_a_partir_de: "após a recontagem acima"
    requisito: HAIKU-07
human_verification:
  - test: "Rodar a query de recontagem de 24 h de 23-ROLLOUT-EVIDENCE.md a partir de 2026-10-16 02:51 BRT"
    expected: "(1) 0 erros 400 de prefill/sampling; (2) nenhum 5xx novo de chat; (4) openrouter-* sem price_table; (5) finish_reason=length sem concentração anômala por tenant"
    why_human: "Janela de 24 h ainda não fechou no momento da verificação (2026-10-15 21:45 BRT)"
  - test: "Pedro confirma o rollout após 24 h (painel /economia x openrouter.ai/activity; chat-ifix e Maestro sem erro novo)"
    expected: "Resposta explícita do Pedro registrada em 23-ROLLOUT-EVIDENCE.md"
    why_human: "Checkpoint humano do plano 23-16 Task 3"
  - test: "Investigar o 503 de TTS do tenant claude-tts às 23:43:11 UTC (request_id 01a141f2-c400-7439-8d1a-17fc7d44f8bc) nos logs do gateway"
    expected: "Saber se o openrouter-tts devolveu 503 e a cascata para local-tts não ocorreu, ou se o 503 foi gerado no próprio gateway"
    why_human: "O verificador só tinha permissão de SQL somente leitura; o motivo não está no audit_log"
  - test: "Definir o papel da conta adm@ifixtelecom.com.br (US$ 5)"
    expected: "Decisão do Pedro: 3ª chave do pool do passo ① ou fora"
    why_human: "Decisão de negócio, fora dos planos"
---

# Phase 23: Haiku 5.5 como LLM externo principal do gateway (créditos Max) — Relatório de Verificação

**Objetivo da fase:** todo tráfego LLM fora do pod segue a cadeia ① Haiku 5.5 direto na Anthropic (créditos das contas Max, rodízio de chaves) → ② Haiku 5.5 no OpenRouter → ③ DeepSeek v4.1 Flash no OpenRouter, com effort high; depois desligar a 3090. Mais HAIKU-08 (TTS externo) e HAIKU-09 (painel Economia fidedigno).
**Verificado em:** 2026-10-16 00:45 UTC (2026-10-15 21:45 BRT)
**Status:** human_needed (equivale a **passed_partial**: nenhum gap bloqueante; um item dependente de tempo)
**Re-verificação:** não, verificação inicial

Método: leitura de código, `gofmt`/`go vet`/`go test ./...` local, vitest e pytest locais, e SQL somente leitura no `bd_ai_gateway` de produção (consultas feitas às 00:43 UTC de 2026-10-16). Nenhuma escrita, deploy ou chamada ao Portainer. Os testes de integração (`-tags integration`) **não** foram reexecutados pelo verificador.

## FATOS (com fonte)

### Build e testes locais

| Verificação | Resultado | Fonte |
|---|---|---|
| `gofmt -l .` e `go vet ./...` em `gateway/` | saída vazia | execução local, HEAD `298de8b` |
| `go test ./... -count=1` em `gateway/` | 28 pacotes `ok`, 0 FAIL (inclui `internal/proxy`, `internal/admin`, `cmd/gatewayctl`) | execução local |
| `npx vitest run src/components/economy-panel.test.tsx src/lib/gateway.test.ts` | 2 arquivos, 21 testes passaram | execução local em `dashboard/` |
| `python3 -m pytest -q test_unified3060.py` | 134 passaram | execução local em `ops/vast-3060/` |
| Testes de integração da cadeia | existem: `TestHaikuChain_A..G2`, `TestHaikuChain_TTSStatusCascade` (subtestes 429/503 cascateiam), `TestSensitiveNeverExternalHaiku` | `gateway/internal/integration_test/haiku_chain_test.go:380-549,791-801`, `haiku_sensitive_test.go:48` |

### Estado de produção (SQL somente leitura, 2026-10-16 00:43 UTC)

| Item | Valor medido |
|---|---|
| Versão goose | 43 aplicada (0041 e 0042 em 2026-10-15 05:49:59 UTC; 0043 às 19:51:15 UTC) |
| Upstreams LLM | `local-llm` tier 0 **disabled**; `anthropic-haiku` tier 1 prio 0 enabled; `openrouter-chat` tier 1 prio 10 enabled; `openrouter-deepseek` tier 1 prio 20 enabled |
| Upstreams TTS | `kokoro-tts` tier 0 enabled; `openrouter-tts` tier 1 prio 0 enabled; `local-tts` tier 1 prio 10 enabled |
| Aliases da cadeia | `qwen`, `deepseek-flash`, `deepseek-v4-flash`, `gpt-4o`, `gpt-5.4-mini`, `gpt-5.4-mini-2026-03-17`, `ia-kanban`, `anthropic/claude-haiku-5.5`, `claude-haiku-5-5`: os 9 têm as 3 linhas `anthropic-haiku`→`claude-haiku-5-5`, `openrouter-chat`→`anthropic/claude-haiku-5.5` (prefs `order:[anthropic]`), `openrouter-deepseek`→`deepseek/deepseek-v4.1-flash:exacto` |
| `maestro-summary` | 1 linha só: `openrouter-chat`→`deepseek/deepseek-v4.1-flash:nitro`, prefs `order:[BaseTen]` (intocado, igual ao snapshot citado em 23-ROLLOUT-EVIDENCE §2.2) |
| Preços vigentes (`valid_to is null`) | `claude-haiku-5-5`/anthropic: input 1e-7, output 5e-7, cache_read 1e-8, cache_write 1,25e-7, cache_write_1h 2e-7; `claude-haiku-5-5:long`: 5× (input 5e-7, output 2,5e-6, cache_write 6,25e-7); `tts-1`/openrouter input_token 4e-5 |
| `pod_config.schedule_disabled` | `t` (updated_at 2026-10-14 11:15:42 UTC) |
| Chat desde a ativação do ① (15:29 UTC) | `anthropic-haiku` 200 × 4854; `openrouter-chat` 200 × 6; **0** respostas ≥ 400 |
| 400 de prefill/temperature/top_p desde o hotfix (03:53:58 UTC) | **0** (baseline antes do hotfix: 37 em 24 h, 23-02-HOTFIX-EVIDENCE §7) |
| Billing desde 05:50:37 UTC | `anthropic-haiku` 4897 `price_table` (cache_read 22.572.466, cache_write 10.491.879, R$ 13,17); `openrouter-chat` 3472 `provider_reported` (R$ 9,73); `openrouter-deepseek` 1 `provider_reported` (canário do 23-16); `openrouter-tts` 33 `price_table` (R$ 1,93) |
| RES-08 (chaves `api_keys.data_class='sensitive'`: cobrancas, converseai-contacts-embed, telefonia) | tráfego sensível desde 03:53 UTC: `local-embed` 200 × 12, `blocked_sensitive` 503 × 1 (chat), `tts` 503 × 1, `embed` 503 × 1; **0** linhas de billing de tráfego sensível em upstream externo |
| TTS desde 19:20 UTC | `openrouter-tts` 200 × 33, 400 × 2 (claude-wpp, 19:52 e 19:59 UTC, antes do fix `b25fc3f`), 503 × 1 (claude-tts, 23:43:11 UTC); após 20:08 UTC: 200 × 30, 503 × 1; `local-tts` 0 chamadas |
| `tenants.llm_effort` | nenhum tenant com override (todos no default) |
| `aux_pod_lifecycles` | 0 linhas (3060 desligada desde 2026-10-14) |
| Requests do ① com prompt > 100k tokens | 0 (maior prompt total observado ≈ 54k; coluna `tokens_in` já inclui os tokens de cache) |

### Código (pontos de verificação)

| Comportamento | Fonte |
|---|---|
| Tradutor OpenAI→Messages por allowlist (sampling não passa); tools e `tool_choice` traduzidos | `gateway/internal/proxy/anthropic_translate_req.go:18,190-202,638` |
| Pool de chaves: estado `exhausted`, detecção de "credit balance is too low", re-teste 6 h, revogada 24 h | `gateway/internal/proxy/anthropic_keypool.go:47,50,68,213` |
| Effort: `DefaultLLMEffort = "high"`, cliente > tenant > default, folga `OpenRouterThinkingHeadroom = 4096` no ② (fix `114d7ab`) | `gateway/internal/proxy/llm_effort.go:32,49,94,101-131` |
| Override de effort por tenant via CLI | `gateway/cmd/gatewayctl/tenant_llm_effort.go:1-5` |
| Status que cascateiam: 402, 429, 500, 502, 503, 504 | `gateway/internal/proxy/chat_retryable_status.go:30-42` |
| RES-08: candidato externo pulado para tenant sensível | `gateway/internal/proxy/dispatcher.go:613-665` |
| Degrau de 100k: `claude-haiku-5-5:long` quando prompt > 100000 | `gateway/internal/proxy/interceptor_usage.go:387-430`; teste `TestUsageAnthropicHaikuLongTier` (`interceptor_usage_haiku_test.go:168`) |
| `/admin/economy` com `pct_servido_local_tokens`, `custo_creditos_haiku_brl`, `custo_externo_caixa_brl`, `vast_3060_brl` | `gateway/internal/admin/economy.go:76,83`; `dashboard/src/components/economy-panel.tsx:85-100` |
| 3060 grava ciclo de vida no gateway (start/stop) | `ops/vast-3060/unified3060.py:180,233,979,1032` |
| Marcadores TBD/FIXME/XXX nos 80 arquivos de código alterados desde `f06681f` | 0 ocorrências |

## Verdades observáveis (por requisito)

| Req | Verdade | Status | Evidência |
|---|---|---|---|
| HAIKU-01 | Adaptador OpenAI→Messages remove sampling, preserva tools/`tool_choice` forçado; prefill normalizado em ① e ② | ✓ VERIFICADO | código acima; prefill_400 = 0 desde 03:53 UTC (SQL); ① 4854 × 200 sem nenhum ≥ 400; gates g1–g5 e smokes (c)–(f) em 23-MAXKEYS-EVIDENCE |
| HAIKU-02 | Pool de 2–3 chaves em ordem fixa, esgotada até renovação, re-teste periódico, chaves protegidas | ✓ VERIFICADO (com risco aceito) | código do keypool; 2 chaves em prod com dias 19 e 13 (23-MAXKEYS-EVIDENCE "Dias de renovação"); testes `TestHaikuChain_C_CreditTooLowAllKeysCascades...`. Risco aceito pelo Pedro: chaves coladas no chat sem rotação ("não vou rotacionar", 23-MAXKEYS-EVIDENCE) |
| HAIKU-03 | Cadeia ①→②→③ nos aliases do padrão DeepSeek, cascata entre passos, `maestro-summary` intocado | ✓ VERIFICADO | 9 aliases × 3 linhas e prioridades 0/10/20 (SQL); `maestro-summary` intocado (SQL); ③ vivo pelo canário (23-ROLLOUT-EVIDENCE §2.6: 1 billing `openrouter-deepseek`, confirmado no SQL); cascata ①→② e ②→③ por status coberta em testes de integração (não reexecutados aqui) |
| HAIKU-04 | Preço Haiku com degrau 5× > 100k e cache read/write, refletido em billing e dashboard | ✓ VERIFICADO | preços exatos após 0043 (SQL); ① com `price_table` e cache gravado (SQL); dashboard com card de créditos (código + vitest). O degrau 100k só tem prova por teste unitário: nenhum request real passou de 100k |
| HAIKU-05 | Effort high por padrão, cliente respeitado, override por tenant, prompt caching (chat-ifix) | ✓ VERIFICADO | código `llm_effort.go`; CLI `set-llm-effort`; cache_read 22,5 M tokens no ① (SQL) e 1585/1589 no smoke de cache (23-MAXKEYS-EVIDENCE) |
| HAIKU-06 | Tenant sensível nunca vai para upstream externo | ✓ VERIFICADO | tráfego sensível só em `local-embed` ou bloqueado com 503; 0 billing externo de tráfego sensível (SQL); `dispatcher.go:665` |
| HAIKU-07 | Rollout em prod com validação e 3090 desligada formalmente | ⏳ PARCIAL — dependente de tempo | rollout e ① ativos (SQL: goose 43, upstreams, 4854 × 200); 3090 off (`schedule_disabled=t`, `local-llm` disabled); Pedro "approved" no 23-18 Task 3 (23-MAXKEYS-EVIDENCE, ~20:40 BRT). **Falta:** recontagem de 24 h e confirmação do 23-16 Task 3 |
| HAIKU-08 | TTS externo com voz PT-BR escolhida por teste cego, billing por caractere, consumidores claude-tts/claude-wpp/chat-ifix | ✓ VERIFICADO (com 1 alerta) | teste cego, escolha "voz a" = `elevenlabs/eleven-v3`/`eric` (23-TTS-EVIDENCE); `openrouter-tts` 33 × 200 com billing `price_table` a US$ 4e-5/char (SQL); claude-wpp 200 após o fix `b25fc3f`. **Alerta:** 1 resposta 503 do `openrouter-tts` sem cascata para `local-tts` (ver HIPÓTESES) |
| HAIKU-09 | Painel Economia: custo OpenRouter real com cache, ① como "coberto por créditos", 3060 somada ao Vast, economia a preço Haiku, % local por tokens | ✓ VERIFICADO | campos no `/admin/economy` e no painel (código, vitest 21/21); OpenRouter com `provider_reported` (SQL); conferência Console × painel feita pelo Pedro (23-MAXKEYS-EVIDENCE, Checkpoint Task 3). O caminho da 3060 está cabeado e testado (pytest 134), mas sem dado real: `aux_pod_lifecycles` = 0 porque a 3060 está desligada |

**Pontuação:** 8/9 verificados; HAIKU-07 parcial por item dependente de tempo.

## Itens dependentes de tempo (não são falha)

| # | Item | Liberado a partir de | Comando |
|---|---|---|---|
| 1 | Recontagem de 24 h pós-rollout | 2026-10-16 02:51 BRT (05:51 UTC) | query de 5 blocos em 23-ROLLOUT-EVIDENCE.md, seção "Checkpoint — Task 3" |
| 2 | Confirmação do Pedro do rollout após 24 h (23-16 Task 3) | após o item 1 | UI `/economia` × `openrouter.ai/activity`; chat-ifix e Maestro sem erro novo |

Observação: a recontagem parcial feita pelo verificador já cobre cerca de 21 h desde o hotfix (03:53 UTC) com 0 erros 400 de prefill e 0 respostas ≥ 400 no chat desde a ativação do ①.

## Gaps

Nenhum gap bloqueante.

## Alertas (WARNING, não bloqueiam)

1. **TTS 503 sem cascata (FATO):** `audit_log` 2026-10-15 23:43:11 UTC, tenant claude-tts, `/v1/audio/speech`, `upstream=openrouter-tts`, status 503, latência 1465 ms, `error_code` e `reason` vazios; `local-tts` teve 0 chamadas desde 19:20 UTC. O teste `TestHaikuChain_TTSStatusCascade/503_cascateia` espera cascata para o Kokoro. Este 503 não está registrado em 23-TTS-EVIDENCE.
2. **Effort high sem medição de raciocínio (FATO, 23-MAXKEYS-EVIDENCE):** a resposta OpenAI do ① não traz `reasoning_tokens`; o gate g1 com effort high gerou 0 blocos thinking. Não há prova de que high muda o comportamento do ①.
3. **Tool loop com thinking no turno 1 (NÃO SEI, 23-MAXKEYS-EVIDENCE):** `ANTHROPIC_TOOL_LOOP_MODE=omit` foi validado só com turno 1 sem bloco thinking. Desde 15:29 UTC houve 0 respostas ≥ 400 no chat (SQL), o que não mostra o 400 até agora.
4. **Fallback TTS ao vivo não provado:** o alias canário de TTS é impossível (CHECK da migration 0005); a prova é só por teste de integração (23-TTS-EVIDENCE, "Fallback").
5. **Pendência fora dos planos:** papel da conta adm@ifixtelecom.com.br (US$ 5) não definido.
6. **Higiene de documentação:** `23-VALIDATION.md` segue `status: draft` com a tabela inteira em ⬜ e sign-off pendente; `REQUIREMENTS.md` ainda marca HAIKU-01..09 como `Pending`.

## Achados fora do escopo da fase (informativo)

- `rerank-cpu` respondeu 503 × 22 ao tenant converseai entre 16:30 e 23:04 UTC (SQL). Rerank não faz parte da Phase 23.
- `local-embed` 502 em lote entre 03:00 e 05:52 BRT, registrado como pré-existente em 23-ROLLOUT-EVIDENCE (Task 3).

## HIPÓTESES

- HIPÓTESE: as 6 requests `openrouter-chat` após 15:29 UTC (tenant chat-ifix, modelo cobrado `deepseek/deepseek-v4.1-flash`, entre 17:13 e 19:37 UTC) são do alias `maestro-summary`, que só tem linha `openrouter-chat`, e não cascata do ①. O `audit_log` não grava o modelo pedido. Resolve com o log do gateway desses request_ids ou com o corpo da requisição.
- HIPÓTESE: o 503 de TTS de 23:43 UTC foi gerado no gateway (breaker ou director) e não pelo OpenRouter, o que explicaria a falta de cascata. Resolve com o log do gateway do request_id `01a141f2-c400-7439-8d1a-17fc7d44f8bc`.
- HIPÓTESE: as duas chaves Max continuam `state=ok`. A última leitura registrada é de 19:20 UTC (23-MAXKEYS-EVIDENCE); o verificador não consultou `/admin/anthropic/keys` (fora da permissão). Resolve com `GET /admin/anthropic/keys`. O fato de o ① ter servido 100% do chat até 00:43 UTC é compatível com isso.
- HIPÓTESE: a imagem em produção é `develop-b25fc3f` (23-ROLLOUT-EVIDENCE, seção da migration 0043). O verificador não inspecionou o serviço; o efeito do fix (claude-wpp com 200 após 20:08 UTC) aparece no SQL.

## Verificação humana necessária

1. **Recontagem de 24 h** — rodar a query de 23-ROLLOUT-EVIDENCE.md a partir de 2026-10-16 02:51 BRT. Esperado: 0 erros 400 de prefill/sampling, sem 5xx novo de chat, openrouter-* sem `price_table`.
2. **Confirmação do Pedro (23-16 Task 3)** — painel `/economia` × `openrouter.ai/activity`; chat-ifix e Maestro sem erro novo.
3. **503 de TTS às 23:43 UTC** — conferir no log do gateway por que não houve cascata para `local-tts`.
4. **Conta adm@ifixtelecom.com.br** — decidir se entra como 3ª chave do pool.

---

_Verificado: 2026-10-16T00:45:00Z_
_Verificador: Claude (gsd-verifier)_

## Adendo do orquestrador (2026-10-15 ~22:20 BRT)

**TTS 503 de 23:43:11 UTC — causa confirmada (FATO).** Log do container `develop-b25fc3f` para `01a141f2-c400-7439-8d1a-17fc7d44f8bc`: `dispatching upstream=openrouter-tts` → após 1,78 s `upstream error ... "err":"context canceled"` e `status 503`. O cliente (claude-tts) encerrou a conexão antes da resposta; o gateway registra cliente cancelado como 503 e não tenta fallback (correto: não há quem receba). Não é falha da cascata TTS.
HIPÓTESE: o cancelamento veio de uma interrupção da fala no próprio claude-tts-speak (fala nova substituindo a anterior). Resolve: log do claude-tts-speak, que roda fora deste host.

**rerank-cpu 503 para o converseai (FATO, fora do escopo da fase).** Mesmo padrão: `context canceled` em exatos ~4,0 s (22 casos, p50 4003 ms) — timeout do cliente converseai. Os 8 sucessos do `rerank-cpu` tiveram p50 de 25,3 s (máx. 28,2 s). Desde que a 3060 foi desligada (14/10) o rerank roda só em CPU e quase nunca cabe no limite de 4 s do converseai. O container `ai-gateway-rerank` estava em 83 % de CPU no momento da consulta. Decisão necessária fora da Phase 23 (provedor externo de rerank, modelo menor em CPU, ou converseai sem rerank).
