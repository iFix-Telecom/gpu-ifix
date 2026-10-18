# Quick 261018-efs: fila curta no local-embed + sonda não derruba upstream ocupado

**Card:** [ai-gateway: fila curta para o embedding local absorver a rajada noturna](https://app.clickup.com/t/17u65kmhcj5) (subtarefa de 17u65kmhb1h)

## Problema (fatos, madrugada de 2026-10-18)

- Desde 17/10 o `openai-embed` está desligado. Com o breaker do `local-embed` aberto, `bge-m3` responde 503 `pinned_upstreams_unavailable` na hora.
- `audit_log`: 227 × 503 instantâneos em `/v1/embeddings` (chat-ifix/Maestro), em ondas às 01:16, 02:33, 02:42–02:43 e 03:05 BRT.
- Maestro (worker1): `brain_kb_ingest_failed` nas 16 contas às 01:15 e 58 × `help_center_article_failed`; nenhum `help_center_synced`.
- 01:14–01:15: até 22 chamadas simultâneas no infinity de 3 CPUs; 30 passaram de 30 s.
- 02:33, 02:43 e 03:05: concorrência máxima de 1–3 chamadas, todas reais com 200 em 12–20 s. No log, às 02:33:28 o breaker abriu entre duas respostas reais 200.
- Código: timeout de chamada real **não** conta no breaker (`errors.go`, `preByteTimeout=false`). Quem registra falha é a sonda (`upstreams/probe.go`), que faz um "ping" de embedding a cada 10 s com orçamento de 15 s (`PROBE_BUDGET_SECONDS=15` em prod). Com o infinity ocupado, o ping fica atrás de lotes de 15–20 s; 3 falhas seguidas abrem o breaker por 30 s.

## Decisão

Pedro, 2026-10-18: "siga com 1 e 2" — (1) a sonda não derruba upstream ocupado; (2) fila curta no local-embed.

## Mudança

- `internal/proxy/local_gate.go` (`LocalGate`): envolve o proxy do `local-embed`.
  - Até `EMBED_LOCAL_MAX_CONCURRENT` (padrão 4) chamadas simultâneas; as demais esperam até `EMBED_LOCAL_QUEUE_WAIT_S` (padrão 60 s) por vaga, antes do RoundTrip (não consome o `ResponseHeaderTimeout` de 30 s).
  - Espera esgotada → 503 `local_queue_full` + `Retry-After`, sem fallthrough e sem falha no breaker. Cliente desiste na fila → 499.
  - Registra chamadas em andamento e o último 2xx real (`Busy`).
- `upstreams.ProbeConfig.SkipIfBusy`: a sonda do `local-embed` é pulada quando há chamada real em andamento **e** um 2xx real nos últimos `EMBED_LOCAL_BUSY_WINDOW_S` (padrão 60 s). Upstream travado para de gerar 2xx e em 60 s a sonda volta a valer.
- `GATEWAY_WRITE_TIMEOUT_EMBED_S` padrão 30 → 90 (fila de até 60 s + 30 s de resposta).
- Testes: `local_gate_test.go` (limite/fila, fila cheia 503 sem fallthrough, 499 na fila, `Busy`, não-2xx não renova), `probe_test.go` `TestProbe_SkipIfBusy`, default do config.

## Hipóteses em aberto

- HIPÓTESE: o Maestro espera mais de 30 s por um embedding. Se o limite dele for menor, a fila vira 499 (sem abrir breaker). Resolve: log do Maestro na madrugada de 19/10.
- HIPÓTESE: 4 simultâneas é bom para 3 CPUs. Ajustável por env sem build.

## Rollback

- Só a fila: `EMBED_LOCAL_MAX_CONCURRENT=0`. Só a sonda: `EMBED_LOCAL_BUSY_WINDOW_S=0`. Tudo: imagem anterior `develop-e7a1fe6` no stack 38.
