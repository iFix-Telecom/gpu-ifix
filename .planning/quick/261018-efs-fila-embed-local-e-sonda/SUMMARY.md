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

## Deploy (2026-10-18)

- PR #23 → `develop` `2fee2ca`; CI verde (unit, integração, build). Stack 38 com `develop-2fee2ca@sha256:0bd7bcbf…` às 13:35 BRT. Cópias: `~/gw-embed-17u65kmhb1h/stack38*-pre-efs-20261018-133458.json` (600).
- Smoke: embed 200 (0,5–1,1 s), chat 200. Dois 404 e um 504 nas trocas de container não aparecem no `audit_log` (proxy de entrada durante a troca).

## Teste de carga e ajuste da concorrência

- 8 chamadas pesadas simultâneas (3 textos longos cada) pelo gateway, limite 4: sonda pulada a cada 10 s, breaker do local-embed **não abriu**, fila segurou 4 chamadas por ~29,7 s. Mas todas as 8 deram 502 em 30 s (as 4 em atendimento passaram de 30 s; as 4 da fila também, depois da espera).
- Medição direta no infinity (rede `ai-gateway`, mesmo corpo):
  - n=1: 565 s — ficou atrás das 8 chamadas que o gateway já tinha abandonado.
  - n=2: 2 × 220 s (221 s no total).
  - n=4: 2 × 276 s e 2 × 618 s (618 s no total).
  - Container a 378–437 % de CPU por ~15 min, voltou a ~1 % ao final.
- **FATO:** o infinity continua calculando chamadas que o cliente abandonou; um 502 por timeout não libera CPU.
- **FATO:** de 2 para 4 simultâneas a vazão não melhorou (≈110 s vs ≈155 s por chamada) e a latência de cada uma subiu.
- **FATO (madrugada de 18/10, `audit_log`):** com 2–3 chamadas reais simultâneas, todas 200 e máximo de 24,5 s.
- Decisão: `EMBED_LOCAL_MAX_CONCURRENT=2` no stack 38 (compose + Env) às 14:03 BRT. Cópia: `stack38*-pre-conc-20261018-140315.json`. Smoke: 5/5 embed 200 (1,1–2,4 s).

## Pendente

- Conferir a madrugada de 19/10: 503 `local_queue_full`, 499, breaker do local-embed, `brain_kb_ingest_failed`/`help_center_*` no Maestro.
- HIPÓTESE: com o timeout de 30 s, chamadas reais que passam do limite ainda gastam CPU no infinity depois do 502 (trabalho perdido). Subir o `EmbedResponseHeaderTimeout` (hoje fixo em 30 s no código) reduziria o desperdício, desde que o Maestro espere mais. Resolve com o log do Maestro de 19/10.
