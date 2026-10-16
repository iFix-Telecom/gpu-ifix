# Quick 261016-rrk: rerank em CPU com modelo menor

**Card:** [ai-gateway: rerank em CPU com modelo menor](https://app.clickup.com/t/17u65kmhb02)

## Problema (fatos)

- Desde que a 3060 foi desligada (14/10), o rerank do gateway roda só no `rerank-cpu` (stack 41, infinity 0.0.77, 4 CPUs, 6 GB).
- O modelo era `BAAI/bge-reranker-v2-m3` (base XLM-R large, ~568M) em float32.
- `audit_log` 15/10: converseai 22 × 503 com p50 4.003 ms (o converseai cancela em 4 s, `rag_rerank_timeout_ms=4000`) e 8 × 200 com p50 25,3 s.
- Medido direto no container, sem fila: 10 documentos de ~800–900 caracteres levaram 14–19 s (pico 52 s).
- O converseai não quebra quando o rerank falha: `_rerank_chunks` cai na ordem vetorial (`chunks[:top_k]`). Perdia a reordenação, não a resposta.

## Decisão

Pedro, 2026-10-15: "rerank, um modelo menor em cpu".

## Comparação

Script `rerank_eval.py` (nesta pasta): 13 perguntas PT-BR de atendimento (telecom e clínica), 10 documentos cada, 1 certo e 9 distratores do mesmo domínio, container temporário com o mesmo limite de 4 CPUs.

| Modelo | top-1 | MRR | p50 | máx | Observação |
|---|---|---|---|---|---|
| BAAI/bge-reranker-v2-m3 (anterior) | 13/13 | 1,000 | 19,0 s | 52,3 s | |
| cross-encoder/mmarco-mMiniLMv2-L12-H384-v1 | 12/13 | 0,949 | 2,3 s | 2,8 s | escolhido |
| unicamp-dl/mMiniLM-L6-v2-mmarco-v2 | — | — | — | — | não sobe no infinity (warmup 1028 tokens > limite 514) |
| bge-reranker-v2-m3 `--engine optimum` | — | — | — | — | repositório sem arquivos ONNX |
| mmarco L12 `--engine optimum` | — | — | — | — | erro no Gather de position embeddings |

- Notas do mmarco na escala 0–1 (0,98 no trecho certo, < 0,001 nos errados). O limiar do converseai (`RERANK_CONFIDENCE_THRESHOLD = 0.05` na nota máxima) continua válido.
- O infinity aceita o `model` antigo (`bge-reranker-v2-m3`) com o modelo novo servido: o converseai não precisa de mudança.

## Deploy (2026-10-16 ~02:10 UTC)

- Snapshot: `~/gw-rerank-17u65kmhb02/stack41{,-file}-pre-*.json` (600).
- Stack 41: `--model-id cross-encoder/mmarco-mMiniLMv2-L12-H384-v1`, `--served-model-name mmarco-mminilmv2-l12-h384`, `start_period` 1200 s → 300 s. Imagem, CPU e memória iguais.
- Container saudável em 49 s.
- Avaliação no container de prod: 12/13, p50 2,09 s, máx 3,69 s (1ª chamada fria).
- Pelo gateway público (`/v1/rerank`, tenant uat10-test, 10 docs): 5/5 em 200, 0,96–1,68 s, trecho certo em 1º (nota 0,995).
- Rollback: PUT do snapshot no stack 41 (volta o bge-v2-m3; cache do modelo antigo segue no volume).
- O tier-0 `rerank-gpu` (pod 3060) continua com o bge-v2-m3 quando o pod voltar.

## Achados durante a validação (2026-10-16)

- **Cliente que desiste gravava 503/502 de upstream.** `http.TimeoutHandler` responde 503 sem corpo quando o contexto é cancelado pelo cliente; a consulta de cota respondia 503 `quota_check_unavailable` no mesmo caso; o ErrorHandler do proxy, 502. Corrigido em `3f75de2` + `7d05c92`: 499 (convenção nginx) via `httpx.RewriteClientClosed` no grupo `/v1` e no enforcer de cota. A taxa de erro (status ≥ 500) deixa de contar esses casos. Teste em prod do `3f75de2`: request cortado em 0,3 s gravado como 499.
- **Sondas para IP de terceiro.** O `url_override` dos tier-0 da 3060 (`rerank-gpu`, `kokoro-tts`, `local-stt`, `embed-gpu`) apontava para `79.116.207.136` (instância destruída em 14/10). Trocado para `http://127.0.0.1:1` com `gatewayctl upstreams update --url`; seguem enabled. Nenhum request real foi para eles em 2 dias (audit_log). Smoke pós-troca: rerank 200 (0,85 s), TTS 200 (1,46 s), embed 200 (0,54 s), STT 200 (1,98 s, transcrição correta).

## Tráfego real pós-troca (consulta 2026-10-16 02:57 BRT)

- `audit_log`, rota rerank, desde 02:10 UTC, sem o tenant de teste: **0 chamadas** do converseai (madrugada). Medição de latência real pendente para o horário comercial.
- Na janela de 24 h anterior à troca: 22 × 503 (cancelamento em 4 s) para 8 × 200 com p50 25,3 s.

## Conferência 2026-10-16 10:13 BRT

- **FATO:** `audit_log` rota rerank desde 02:10 UTC, sem uat10-test: **0 chamadas** (nenhum 200, 499 ou 503). O converseai fez 14 chamadas de chat no período, nenhuma de rerank.
- **FATO:** o container `ai-gateway-rerank` recebeu 2.876 `POST /v1/rerank` em 8 h, todos de `10.0.0.2` (rede de entrada do swarm) e exatamente a cada 10 s — é a sonda do próprio gateway (`internal/upstreams/probe.go:354`, `PROBE_INTERVAL_SECONDS=10`), não tráfego desviado do gateway.
- **FATO:** converseai prod (worker2) e dev (vps-ifix-vm) têm `RAG_RERANK_URL=https://ai-gateway.converse-ai.app` e `RAG_RERANK_MODEL=bge-reranker-v2-m3`. No log do agents de prod não há linha de RAG nas últimas 26 h; no de dev (reiniciado há 3 h) o único evento tem `buscou=False chunks=0`.
- HIPÓTESE: as conversas de hoje não acionaram a busca na base (RAG), então não houve o que reordenar. Resolve: medir de novo depois de um dia com tráfego de RAG.

## Conferência 2026-10-16 18:47 BRT

Consulta somente leitura em `ai_gateway.audit_log`, rota `/v1/rerank`, desde 02:10 UTC, tenant converseai (uat10-test fora).

| Status | Chamadas | p50 | p95 | máx | Janela (UTC) |
|---|---|---|---|---|---|
| 200 | 88 | 1,89 s | 6,47 s | 13,74 s | 13:37–14:02 |
| 499 | 1 | 4,01 s | — | — | 13:40 |

- **FATO:** 0 × 503. Faixas: 48 < 2 s, 30 entre 2 e 4 s, 7 entre 4 e 8 s, 4 ≥ 8 s. A cauda > 4 s está em 13:41 (10 chamadas, máx 13,7 s) e 13:49 (9 chamadas, máx 9,7 s); concorrência máxima de 4 chamadas simultâneas.
- **FATO:** rajada atípica: 88 rerank para 17 chat na mesma janela, todas pela mesma chave do tenant converseai. O agents de prod (worker2, no ar desde 15/10 22:07 UTC) não tem nenhuma linha de rerank/RAG entre 13:35 e 14:05 UTC.
- **FATO:** às 13:51 UTC entrou no develop do converseai o `0aa821948` "fix(rag): rerank com 10 trechos e corte 0.6 para o modelo de CPU" (card 86akuh17c): com 30 trechos a chamada passava do timeout de 4 s e o corte 0,05 descartava acertos. O agents de dev foi recriado às 13:58 UTC. Depois disso: 5 chamadas, 1,35–1,83 s, todas 200 (log do dev: `rerank_ms` 1521 e 1894, 10 trechos).
- **FATO:** o agents de prod ainda roda imagem anterior ao `0aa821948` (iniciado 15/10), ou seja, com 30 trechos e corte 0,05.
- HIPÓTESE: a rajada de 13:37–13:49 é a calibração feita no dev pela sessão do card 86akuh17c, com 30 trechos (daí a cauda > 4 s e o 499 em 4,01 s). Os logs do container anterior se perderam na recriação; resolve com o histórico daquela sessão.
- HIPÓTESE: enquanto o `0aa821948` não for para prod, uma busca RAG real em prod com 30 trechos tende a passar de 4 s e virar 499 (rerank descartado, ordem vetorial mantida). Resolve com o próximo deploy de prod do converseai ou com uma busca RAG em prod.
- Conclusão: o modelo de CPU atende em ~1,5–2 s com 10 trechos; o gateway não precisa de mudança.
