---
phase: quick-261007-t9f
plan: 01
subsystem: gateway/billing
tags: [billing, economia, phantom, tts, embed, rerank, audit]
clickup: 86akttrf8
requires: []
provides:
  - "phantom (Economia) só para upstream self-hosted; external só para não-self-hosted"
  - "rota de billing/audit/obs `tts` separada de `stt`"
  - "metering de TTS (tokens_in = caracteres do input, model tts-1)"
  - "embed_request opcional quando a requisição já tem tokens"
  - "audit não perde mais linhas de TTS com sucesso (resposta binária fora do JSONB)"
  - "ops/01-preview.sql + ops/02-fix.sql (correção histórica)"
affects: [ai-gateway prod (stack 38), /admin/economy, ai-dashboard Economia]
tech-stack:
  added: []
  patterns: ["interface billingEnqueuer como seam de teste do flusher", "billing.NewStaticPricesLoader"]
key-files:
  created:
    - gateway/internal/proxy/tts_request_chars_middleware.go
    - gateway/internal/proxy/tts_request_chars_middleware_test.go
    - gateway/internal/proxy/interceptor_usage_cost_test.go
    - gateway/internal/proxy/interceptor_usage_tts_test.go
    - gateway/internal/audit/upstream_for_route_test.go
    - gateway/internal/obs/upstream_for_route_internal_test.go
    - .planning/quick/261007-t9f-billing-economia-local-tts-embed-rerank/ops/01-preview.sql
    - .planning/quick/261007-t9f-billing-economia-local-tts-embed-rerank/ops/02-fix.sql
  modified:
    - gateway/internal/proxy/interceptor_usage.go
    - gateway/internal/proxy/interceptor_usage_test.go
    - gateway/internal/auditctx/override.go
    - gateway/internal/audit/middleware.go
    - gateway/internal/obs/middleware.go
    - gateway/internal/billing/prices_loader.go
    - gateway/internal/billing/flusher.go
    - gateway/cmd/gateway/main.go
decisions:
  - "Economia (cost_local_phantom_brl) só para tráfego servido por infra própria; os dois custos são mutuamente exclusivos por upstream"
  - "Lista self-hosted: local-*, emergency_pod_*, rerank-gpu, rerank-cpu, embed-gpu, kokoro-tts, voice-api-piper"
  - "TTS: tokens_in = nº de caracteres (runes) do input, unit input_token, model de billing fixo tts-1 (sem migration)"
  - "embed_request só é precificado quando tokens_in == 0 ou existe linha de preço"
  - "Audit: rotas /v1/audio/* só guardam response em audit_log_content se for JSON válido"
metrics:
  completed: 2026-10-08
  tasks: 3
  commits: 7
---

# Quick 261007-t9f: Billing Economia só local + metering TTS + embed/rerank Summary

A Economia (`cost_local_phantom_brl`) agora conta só o que a infra própria serviu, porque o custo externo e a Economia passaram a ser exclusivos por upstream. Também mudou:

- O TTS passou a gerar billing (caracteres do `input`, model `tts-1`).
- O `embed_request` deixou de gerar alerta de preço faltante para o bge-m3.
- Achado e corrigido durante o diagnóstico: o audit perdia **todas** as linhas de TTS com sucesso, porque o áudio binário quebrava o flush do JSONB.

## Commits

| # | Hash | Tipo | Descrição |
|---|------|------|-----------|
| 1 | 351f511 | test | testes falhando: self-hosted ampliado, exclusividade de custo, rota tts |
| 2 | 8c07bd9 | feat | phantom só self-hosted + rota tts (billing/audit/obs) + embed_request opcional |
| 3 | 97b9b94 | test | testes falhando: middleware de caracteres TTS + branch TTS do Intercept |
| 4 | d9b2475 | feat | metering TTS + usageInterceptor nos 3 proxies TTS + middleware montado |
| 5 | 85b1eae | test | teste falhando: áudio binário no audit_log_content |
| 6 | fcfb652 | fix | audit só guarda response JSON em rotas de áudio |
| 7 | bc814ba | chore | ops/01-preview.sql + ops/02-fix.sql |

## Verificação

- `gofmt -l gateway/`: nenhuma saída. `go vet ./gateway/...`: sem problemas. `go build ./gateway/...`: compila.
- `go test ./gateway/... -count=1`: todos os pacotes passaram.
- `go test -tags integration ./gateway/...` (sudo + Docker): todos os pacotes passaram, exceto `cmd/gatewayctl`. Nessa rodada completa, 6 testes `TestRunUpstreams_*` deram `db: ping: context deadline exceeded` (60s). O pacote não foi tocado. Rodando o pacote sozinho, os testes passaram em 46.6s: falha de contenção de containers na suíte paralela, não regressão.
- Integração final de `internal/integration_test`, `internal/audit` e `internal/proxy` depois do último commit de código: passou (313s).
- O `02-fix.sql` foi validado num Postgres 16 local descartável, com fixture (fx 5.0, 4 linhas):
  - a correção ficou correta: usage_counters 0.5123 → 0.6000, igual a billing_events;
  - as checagens pré-COMMIT deram 0 e 0;
  - a 2ª execução não alterou nada (SELECT 0 / UPDATE 0), ou seja, é idempotente.

## Desvios do plano

### Corrigidos automaticamente

1. **[Rule 2 — consistência] `obs/middleware.go` upstreamForRoute também mapeia `/v1/audio/speech` → `tts`.** O código comenta que essa função é "kept in sync with audit.upstreamForRoute". Sem a mudança, o label de métricas divergiria do audit_log. Commit 8c07bd9.
2. **[Rule 3 — seam de teste] `UsageInterceptor.flusher` virou a interface `billingEnqueuer`, e foi criado `billing.NewStaticPricesLoader`.** O `*billing.Flusher` e o `PricesLoader` exigem pgxpool, e o pacote proxy não conseguia montar um FinalizeRequest com captura de evento sem DB. O construtor só atribui um ponteiro não-nil, para evitar o typed-nil na interface. Commits 351f511 e 8c07bd9.
3. **[Rule 1 — bug, achado no diagnóstico da Task 3] O audit perdia as linhas de TTS com sucesso.** Detalhes em FATOS F1–F3. Correção de 1 condição em `audit/middleware.go`, coberta por teste (commits 85b1eae e fcfb652). A Task 3 permitia o fix quando fosse trivial e comprovado por log e teste.
4. **[Rule 1] Os CSVs de rollback do 02-fix seriam sobrescritos numa 2ª execução.** O psql grava os CSVs no container `--rm`, e uma reexecução no mesmo diretório gera CSVs vazios. O runbook agora monta um volume do host num diretório **novo por execução** (com timestamp).
5. **[Precisão] O delta de usage_counters reproduz o arredondamento do CTE de insert.** O insert faz `::numeric(10,4)` por evento, então o delta é `sum(round(new,4) - round(old,4))`. Motivo: o preview mostrou usage_counters com R$ 104,61 contra R$ 600,04 em billing_events, porque micro-valores por evento arredondam para 0 em numeric(10,4). Subtrair o delta sem arredondar zeraria dias inteiros via GREATEST.

### Testes existentes ajustados

- Nenhum teste existente afirmava phantom > 0 para upstream externo.
- Ampliados (sem mudar o que já era afirmado): a tabela de `TestIsSelfHostedUpstream`, com os novos self-hosted e os externos `openai-whisper`, `groq-whisper`, `unknown` e `""`, e a tabela de `TestRouteToBillingRouteRerank`, com o caso `/v1/audio/speech → tts`.

## Efeitos colaterais conhecidos

- **Quota:** em `usage_counters.tokens_in`, os caracteres de TTS passam a somar na quota diária e mensal de tokens do tenant. É o mesmo comportamento do rerank. Limites medidos: claude-tts e claude-wpp com 10.000.000 por dia e 300.000.000 por mês; chat-ifix com 100.000.000 por dia. Sem risco prático.
- O adapter `voice-api-piper` continua sem metering, porque é um http.Handler próprio, sem ReverseProxy.

## Diagnóstico TTS (Task 3, somente leitura)

### FATOS (com fonte)

- **F1 — Desde 2026-07-10 não existe nenhuma linha de sucesso de TTS no audit_log.** A query de `audit_log` com `route LIKE '%speech%'` nos últimos 90 dias deu status 400=6, 404=15, 502=11, 503=220 e nenhum 2xx. `billing_events` nunca teve rota ou upstream de TTS (0 linhas).
- **F2 — Houve TTS com sucesso, mas a linha de audit foi perdida no flush.**
  - Log do gateway prod (container 1f7458f5f34f):
    - `2026-10-07T21:05:24-03:00 request 01a118d2-ae74-77d7-b68d-b0cc25dda3b0 POST /v1/audio/speech status 200 bytes 2321430 latency_ms 101344`
    - 1s depois: `audit flush failed ... invalid byte sequence for encoding "UTF8": 0xff (SQLSTATE 22021) batch_size 1`.
  - No container anterior (7b347fd3d961) o padrão se repete duas vezes:
    - às 15:26:36 BRT, um 200 local-tts de 36140 bytes, seguido de flush failed `0x00` com batch_size 2 (a outra linha do batch também se perdeu);
    - às 18:33:01 BRT, um 200 kokoro-tts de 104524 bytes, seguido de flush failed `0x98`.
  - Causa no código: `audit/middleware.go` captura o corpo da resposta de tenants normais e o grava em `audit_log_content.response`, que é **JSONB**. O áudio binário faz o batch inteiro dar rollback (`writer.go` dbFlusher.Flush é uma transação única).
- **F3 — Correção aplicada:** nas rotas `/v1/audio/*`, a response só é guardada se `json.Valid`. Os erros JSON continuam sendo capturados (teste `TestMiddleware_AudioJSONErrorStillCaptured`).
- **F4 — As 101 linhas com `upstream=stt` em /v1/audio/speech nunca foram despachadas.** O rótulo `stt` vinha do default de `upstreamForRoute`. Corpos de erro em `audit_log_content`, todos 503 `upstream_unavailable` (latência p50 de 188–195 ms):
  - 41× "No primary upstream configured for role.": o `Loader.Resolve("tts",0)` retornou !ok, o que significa nenhuma linha tier-0 tts habilitada no snapshot naquele instante. Inclui o claude-tts de 2026-10-07 18:25Z.
  - 39× "Primary upstream unavailable and no fallback configured for role.": `ResolveAllTier1("tts")` vazio (inclui uat10-test em 2026-09-01).
  - 21× "All inference upstreams are unavailable.": havia candidatos tier-1, mas nenhum estava CLOSED.
  - A HIPÓTESE do contexto ("upstream stt = Piper na CPU") está **refutada**: o rótulo era o default de rota, não um upstream.
- **F5 — Topologia TTS medida em prod em 2026-10-08 (`SELECT ... FROM ai_gateway.upstreams WHERE role='tts'`):**
  - `kokoro-tts` é **tier 0** (enabled), com url_override `http://218.112.61.47:52204`;
  - `local-tts` é **tier 1** (enabled), sem override;
  - env do gateway: `UPSTREAM_TTS_URL=http://kokoro-tts:8880`, um container Kokoro em CPU no próprio worker-vm (`ai-gateway-tts_kokoro-tts.1`, Up 4 weeks);
  - `UPSTREAM_TTS_KOKORO_URL=http://91.150.160.38:17040`;
  - `UPSTREAM_TTS_PIPER_URL=http://10.10.10.30:5100` **está definida**, ao contrário do que o planner registrou como "unset". Não existe linha `voice-api-piper` na tabela upstreams, então o adapter fica registrado no mapa de proxies mas nunca é resolvido.
- **F6 — local-tts 503 (claude-wpp, 24×, latência de 19.905 a 120.004 ms): os casos de ~120 s são o timeout de escrita da rota.**
  - O log mostra `dispatching role tts upstream local-tts` seguido de `status 503 bytes 0 latency_ms ~119.9xx`. Ocorreu em 7 requisições no dia 07/10, incluindo a das 20:45:53 BRT (23:45Z): `01a118c2-5c8a-...`, com latência de 119.977 ms.
  - `GATEWAY_WRITE_TIMEOUT_AUDIO_S` tem default 120 (`config.go:466`), e o `http.TimeoutHandler` responde 503 com corpo vazio.
  - Ou seja, a síntese no Kokoro em CPU passou de 120 s. Exemplo: um 200 do mesmo local-tts levou 101 s para 2,3 MB.
- **F7 — Breaker do kokoro-tts:** às 21:25–21:29 BRT ele oscila open↔half-open a cada 40 s (log do container atual). Às 15:19–15:24 BRT, a mesma oscilação.
- **F8 — Os 3 casos de local-tts 400 (chat-ifix, 2026-09-08)** têm corpo `Voice 'marcos' not found. Available voices: af_alloy, ...`: voz inexistente no Kokoro.
- **F9 — kokoro-tts 404** (claude-tts, 2026-09-06): `Model 'tts' is not installed locally`. O cliente mandou `model: "tts"`, que o Kokoro rejeitou.
- **F10 — kokoro-tts e local-tts 502** (claude-wpp, 11×): corpo `upstream_unreachable`, com latências de 60.207 a 60.313 ms.
- **F11 — `emergency_pod_llm` era tratado como externo.** Log: `WARN price missing — cost will be 0 model model.gguf provider emergency_pod_llm unit input_token/output_token` (15:25:05 BRT). Isso confirma o bug corrigido na Task 1. A Economia dele já vinha sendo gravada pela referência `model.gguf/openrouter-fireworks` (input 3e-8, output 1.28e-6, linhas existentes).

### HIPÓTESES (cada uma com a evidência que resolveria)

- **H1:** os 502 de ~60 s (F10) são o `ResponseHeaderTimeout` de 60 s do transport TTS (`NewDynamicOverrideProxy` / `NewDynamicTTSTargetProxy`), e não falha de rede. Resolveria: ler o transport de `NewDynamicTTSTargetProxy` em `dynamic_target.go` e achar no log o erro `timeout awaiting response headers` com esses request_ids.
- **H2:** a oscilação do kokoro-tts às 21:25 BRT (F7) é o pod 3060 destruído na janela 07–20 h BRT com o `url_override` ainda gravado (218.112.61.47:52204). Resolveria: `vastai show instances` ou o state.json do pod no horário, mais o histórico de escrita do `url_override` (log do reconciler, "url_override cleared").
- **H3:** os 41 "No primary upstream configured" (F4) aconteceram em janelas em que a linha tier-0 tts estava desabilitada ou em troca de tier (flip kokoro/local). Resolveria: histórico de `gatewayctl upstreams update` (linhas `event_kind` de state-change no audit_log) cruzado com os timestamps.
- **H4:** a requisição de 2026-10-07 15:25:05 BRT (503, 124 bytes, 583 ms, claude-tts) é a linha 18:25Z "No primary upstream configured for role." (mesmo minuto, corpo JSON de 124 bytes). O local-tts estava CLOSED desde 15:21 (log), o que é coerente com faltar o tier-0 e não o tier-1. Resolveria: comparar o request_id do audit_log com o do log do gateway.
- **H5 (premissa do script):** a divergência entre usage_counters.date (data do flush) e brt_date (data do ts) é desprezível. Resolveria: a checagem 5 do 02-fix (comparação por dia, depois da correção).

### NÃO SEI

- Por que o tier-0 tts some do snapshot do loader nos casos de F4 (o código mostra só que `byRoleTier[tts,0]` estava ausente). Sem histórico de mudanças da tabela upstreams naquele horário, o dado é insuficiente.

## Preview em prod (`ops/01-preview.sql`, executado em 2026-10-08 ~00:31Z, somente leitura)

- FX vigente USD/BRL = **4.980405** (valid_from 2026-10-07 11:30Z).
- As linhas de preço `openrouter-fireworks` para bge-m3, bge-reranker-v2-m3 e tts-1 **não existem** (0 rows). O passo 2 do runbook é obrigatório antes do 02-fix.
- Retenção de billing_events: de 2026-07-04 17:49Z a 2026-10-08 00:30Z, com 382.941 eventos.
- **(i) Não-self-hosted com phantom (a zerar):**

| upstream | linhas | phantom R$ | external R$ |
|----------|-------:|-----------:|------------:|
| openrouter-chat | 148.343 | 519,156032 | 519,156032 |
| gemini-stt | 34.634 | 11,080881 | 11,080881 |
| groq-whisper | 540 | 1,913934 | 6,146599 |
| openai-whisper | 180 | 0,029852 | 0,311652 |

- **(ii) Embed e rerank self-hosted sem phantom (a precificar a 1e-8 USD/token):**

| upstream | model | linhas | tokens_in | novo phantom R$ |
|----------|-------|-------:|----------:|----------------:|
| local-embed | bge-m3 | 21.160 | 37.523.922 | 1,869103 |
| embed-gpu | bge-m3 | 9.450 | 17.311.797 | 0,862275 |
| rerank-gpu | bge-reranker-v2-m3 | 323 | 3.519.049 | 0,175272 |

- Self-hosted com cost_external ≠ 0: **0 linhas**.
- **Economia total (toda a retenção): antes R$ 600,0384, depois R$ 70,7644.**
- Últimos 30 dias, por dia BRT: antes, de R$ 0,98 a R$ 43,33 por dia útil; depois, de R$ 0,02 a R$ 3,71 por dia. Exemplo: 2026-10-07 passa de 28,4510 para 2,8132.
- Phantom em usage_counters hoje: R$ 104,6113 (2026-07-01..2026-10-07). É menor que em billing_events por causa do arredondamento numeric(10,4) por evento (desvio 5).

## Runbook de ops (para o ORQUESTRADOR, depois do merge em develop)

O executor **não** executou nada disto em prod: não houve deploy, `prices set` nem 02-fix.

1. **Deploy na stack 38** (ai-gateway-prod, endpoint 6):
   - push em develop; o CI builda `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-<sha>`;
   - pré-pull: `ssh worker-vm docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:develop-<sha>`;
   - PUT no Portainer na stack 38 trocando **só** a tag da imagem do gateway (preservar o env);
   - validar `/healthz` e o log de boot sem erro.
   - A imagem atual era `develop-a363ff6`.
2. **Preços** (sem preço, a Economia de embed, rerank e TTS fica em 0 com WARN):
   ```bash
   G='docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl'
   ssh worker-vm "$G prices set --model bge-m3 --provider openrouter-fireworks --unit input_token --usd 0.00000001 --notes 'OpenRouter baai/bge-m3 prompt price 2026-10-07 (savings reference)'"
   ssh worker-vm "$G prices set --model bge-reranker-v2-m3 --provider openrouter-fireworks --unit input_token --usd 0.00000001 --notes 'DeepInfra/Novita list 0.01/1M 2026-10-07 - HIPOTESE nao verificada na pagina oficial'"
   ssh worker-vm "$G prices set --model tts-1 --provider openrouter-fireworks --unit input_token --usd 0.000015 --notes 'OpenAI tts-1 15/1M chars; route tts tokens_in = input characters'"
   ```
   - NÃO cadastrar `embed_request` para bge-m3 (D-P3 o tornou opcional, e o gatewayctl rejeita usd 0).
   - Validar: `SELECT model, unit, unit_cost_usd FROM ai_gateway.prices WHERE valid_to IS NULL AND provider='openrouter-fireworks' AND model IN ('bge-m3','bge-reranker-v2-m3','tts-1');`. Devem vir 3 linhas, todas `input_token`.
3. **Snapshot e correção:**
   - Rodar de novo `ops/01-preview.sql` para ter os números atuais (comando no cabeçalho do arquivo).
   - Rodar o `ops/02-fix.sql` com **volume do host em diretório novo**. O comando exato está no cabeçalho do arquivo:
     ```bash
     ssh worker-vm 'D=/root/billing-fix-261007-$(date +%Y%m%dT%H%M%S); mkdir -p $D && echo $D && \
       C=$(docker ps -q -f name=ai-gateway-prod_gateway|head -1); \
       DSN=$(docker inspect $C --format "{{range .Config.Env}}{{println .}}{{end}}" | grep ^AI_GATEWAY_PG_DSN= | cut -d= -f2-); \
       docker run --rm -i --network host -v $D:/work -w /work -e DSN="$DSN" postgres:16-alpine \
         sh -c "psql \"\$DSN\" -v ON_ERROR_STOP=1 -f -"' \
       < .planning/quick/261007-t9f-billing-economia-local-tts-embed-rerank/ops/02-fix.sql
     scp 'worker-vm:<D impresso acima>/*.csv' ~/billing-fix-261007/
     ```
   - Antes do COMMIT, confirmar que as checagens 1 e 2 dão **0** e que o total de billing_events fica perto de R$ 70,76 (mais o tráfego novo). Para dry-run, trocar o `COMMIT;` final por `ROLLBACK;`.
   - Rollback a partir dos CSVs: `billing_events.cost_local_phantom_brl = old_phantom` por (request_id, ts); `usage_counters.cost_local_phantom_brl` = valor do CSV por (tenant_id, date).
   - Ordem: o deploy vem **antes** do 02-fix. Se o gateway antigo continuar gravando phantom em openrouter-chat depois do fix, basta reexecutar o 02-fix, que é idempotente e só pega as linhas novas.
4. **Smoke:**
   - 1 `POST /v1/audio/speech` com o tenant claude-tts no horário do pod. Conferir com `SELECT route, upstream, model, tokens_in, cost_local_phantom_brl, cost_external_brl FROM ai_gateway.billing_events WHERE route='tts' ORDER BY ts DESC LIMIT 5`. Esperado: route=tts, model=tts-1, tokens_in = nº de caracteres, phantom > 0, external = 0.
   - **Novo:** essa mesma requisição deve aparecer no `audit_log` com status 200 e upstream `kokoro-tts` ou `local-tts`, e o log não deve ter `audit flush failed`.
   - 1 chat via openrouter: phantom = 0 e external > 0.
   - 1 embed local: phantom > 0 e nenhum WARN `price missing ... embed_request` no log.
   - `/admin/economy` com totais coerentes com o "depois" do preview.

## Threat Flags

Nenhuma superfície nova além do `<threat_model>`:
- T-t9f-01 está mitigado com leitura limitada a 64 KiB e repasse via MultiReader.
- T-t9f-04 está respeitado: nenhum conteúdo de `input` foi logado nem lido. As queries de diagnóstico leram só `audit_log_content.response` (erros JSON), nunca `prompt`.

## Known Stubs

Nenhum.

## Notas para o orquestrador

- STATE.md, ROADMAP.md e PLAN.md não foram modificados nem commitados, conforme as constraints. Este SUMMARY também não foi commitado.
- Foi criado `.planning/clickup-active-task.json` (`{"skip": true}`, gitignored) no worktree, espelhando o marcador do repo principal, para satisfazer o hook clickup-link. O card do plano é 86akttrf8.

## Self-Check: PASSED

- Arquivos criados: os 8 arquivos de `key-files.created` existem (verificado com `ls` antes de escrever).
- Commits 351f511, 8c07bd9, 97b9b94, d9b2475, 85b1eae, fcfb652 e bc814ba presentes em `git log`.

## Ops executado pelo orquestrador (2026-10-07 22:00–22:05 BRT) — FATOS
- Preços (gatewayctl prices set, provider openrouter-fireworks, input_token): bge-m3 1e-8, bge-reranker-v2-m3 1e-8, tts-1 1.5e-5.
- Deploy stack 38: develop-a363ff6 → `develop-1040569@sha256:e91ef6cc…` (CI unit+integration verdes; pré-pull; PUT Env 84). Healthy, 0 ERROR.
- 02-fix: ensaio com ROLLBACK, depois COMMIT. billing_events UPDATE 214.640, usage_counters UPDATE 486. Checagem 1 = 0; checagem 2 = 65 linhas com ≤10 tokens cujo phantom arredonda a 0 em numeric(10,6) (não é erro). Economia total R$ 600,04 → **R$ 70,76** (billing_events); usage_counters R$ 46,70.
- CSVs rollback: worker-vm `/root/billing-fix-261007-20261007T220225/` + cópia ops-claude `~/billing-fix-261007/`.
- Smoke: TTS 200 (local-tts, 18 chars → tokens_in 18, phantom R$0,001344, ext 0) e linha 200 no audit_log (bug do flush resolvido); chat openrouter phantom 0 / ext >0; embed local phantom >0. 0 "audit flush failed", 0 "price missing", 0 ERROR.
