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
