---
phase: quick-261001-qdd
plan: 01
subsystem: gateway/primary (Vast provisioning)
tags: [vast, bid, interruptible, real-cost, preemption, pod_config, reliability]
clickup: 86akrnpwc
requires: []
provides:
  - "migration 0039: pod_config offer_mode/bid_margin/max_preemptions_per_day/min_reliability + primary_lifecycles is_bid/bid_price (trigger recreated, hot reload)"
  - "primary/pricing.go: RealCost / BidPriceFor / RankCandidates / ChooseMode (pure)"
  - "provisionLifecycle: on-demand + bid search, real-cost rank, Price only for bid, on-demand fallback"
  - "bid preemption -> destroy + close 'preempted' + re-provision; gateway_primary_preemptions_total{phase}"
  - "gatewayctl primary lifecycles MODE column; admin GET/PATCH /admin/primary/config with the 4 new fields"
affects: [ai-gateway-prod (stack 38), bd_ai_gateway]
tech-stack:
  added: []
  patterns: ["cap sobre GPU+storage, ranking sobre custo real total (com download amortizado)", "close reason pendente consumido pelo evaluateDestroying"]
key-files:
  created:
    - gateway/db/migrations/0039_primary_bid_mode.sql
    - gateway/internal/primary/pricing.go
    - gateway/internal/primary/pricing_test.go
    - gateway/internal/primary/reconciler_bid_test.go
    - gateway/internal/primary/reconciler_preempt_test.go
    - gateway/internal/admin/config_write_offer_test.go
    - gateway/internal/integration_test/migration_0039_test.go
    - gateway/cmd/gatewayctl/primary_mode_test.go
  modified:
    - gateway/db/queries/{pod_config,primary_lifecycles}.sql
    - gateway/internal/db/gen/{models,pod_config.sql,primary_lifecycles.sql,querier}.go
    - gateway/internal/podconfig/types.go
    - gateway/internal/admin/{config_read,config_write,config_write_test}.go
    - gateway/internal/config/config.go
    - gateway/internal/emerg/vast/{types,types_test}.go
    - gateway/internal/primary/{lifecycle,reconciler,reconciler_test}.go
    - gateway/internal/obs/metrics.go
    - gateway/cmd/gatewayctl/primary.go
    - gateway/internal/db/migrate_test.go
    - gateway/internal/integration_test/{migration_0026,migration_0029,migration_0038}_test.go
decisions:
  - "Cap aplicado a GPU+storage (semântica antiga do cap); ranking pelo custo real total incluindo download amortizado (inet_down_cost*20GB/8h)"
  - "max_preemptions_per_day <= 0 DESLIGA o fallback por preempção (diverge do Python 3060, onde 0 = sempre on-demand); para forçar on-demand usar offer_mode='ondemand'"
  - "accepted_dph: on-demand mantém dph_total (sem mudança); bid grava bid + storage/h. Download amortizado é só termo de ranking"
  - "force_machine_id continua on-demand e sem cap client-side; passa a usar o piso de reliability do primário"
  - "min_reliability 0.95 só no primário (cópia do filtro); emerg segue 0.99. Filtro client-side mantém oferta sem o campo (Reliability 0)"
  - "WEIGHTS_DOWNLOAD_GB / EXPECTED_HOURS_PER_START = env de boot (PRIMARY_WEIGHTS_DOWNLOAD_GB=20, PRIMARY_EXPECTED_HOURS_PER_START=8), não coluna hot"
metrics:
  completed: 2026-10-01
  tasks: 3 (+ 2 adições do coordenador: download amortizado, min_reliability)
  commits: 6
  files_changed: 30
---

# Quick 261001-qdd: primário 3090 com custo real, bid e fallback on-demand

O provisionador do pod primário agora ordena as ofertas da Vast pelo custo real e, por padrão, aluga instância interrompível (bid) quando ela sai mais barata. O custo real soma a GPU (dph_base ou o lance), o storage de 45 GB e o download dos pesos amortizado por partida. Se nenhum bid serve, ou se já houve 2 preempções no dia (BRT), ele aluga on-demand. Uma instância bid derrubada pela Vast é destruída, o lifecycle fecha como `preempted` e o loop de agenda provisiona outra. O piso de reliability do primário caiu de 0.99 para 0.95.

## Commits (branch `worktree-agent-ae3bfecd27e30132b`, base `0a9c9fa`)

| # | Commit | Descrição |
|---|--------|-----------|
| 1 | `c4fd3e5` | data layer: migration 0039, queries, sqlc, podconfig/admin/config, vast types (dph_base, storage_cost, min_bid, inet_down_cost, Price) |
| 2 | `650dd25` | test (RED): pricing_test.go |
| 3 | `435f766` | feat: pricing.go + provisionLifecycle (custo real, bid, fallback, auditoria) |
| 4 | `864e216` | test (RED): preempção + coluna MODE |
| 5 | `1c7ccf6` | feat: detecção de preempção, close `preempted`, métrica, MODE no gatewayctl |
| 6 | `02e5736` | feat: `min_reliability` 0.95 só no primário (pedido do Pedro durante a execução) |

## Testes (executor, 2026-10-01)

- `cd gateway && gofmt -l .` → vazio.
- `go build ./... && go vet ./... && go vet -tags integration ./...` → ok.
- `go test ./... -count=1` → todos os pacotes ok, 0 FAIL.
- `go test -race -count=1` em `internal/primary`, `internal/emerg`, `internal/emerg/vast`, `internal/podconfig` e `cmd/gatewayctl` → ok.
- `sqlc generate` com a v1.30.0, a mesma do header. O diff fica só nos gen das queries e models novos.
- **O executor NÃO rodou** os testes `-tags integration` com Docker. Eles ficam com o orquestrador:
  - `migration_0039_test.go` (novo): defaults bid/1.15/2/0.95, Down(1) remove as 6 colunas e mantém 1 trigger, e o re-Up.
  - Bumps de `Down(N)` relativos ao HEAD por causa da 0039: 0026 vai de 11→12 e de 13→14, 0029 de 10→11, e 0038 DownUp de 1→2.
  - `cmd/gatewayctl/primary_lifecycles_integration_test.go` passa a ver a coluna MODE na tabela. FATO: nenhum teste compara o texto do header.

## Mudanças de comportamento em prod (depois do deploy)

1. **Modo bid por padrão.** O DEFAULT da coluna faz backfill de `offer_mode='bid'` na row de prod. Para cada shape, roda uma busca on-demand e uma busca bid. Os dois filtros são cópias com `type` e `limit: 64`. A busca bid não tem `dph_total` no filtro do servidor.
2. **Ranking pelo custo real.**
   - `CapCost = hourly + storage_cost*45/730`. O cap por shape se aplica a esse valor.
   - `Total = CapCost + inet_down_cost*20/8`. Ganha o menor Total. Em empate ganha o on-demand.
   - Uma oferta sem `dph_base` cai para `dph_total`. Nos fixtures antigos a ordem não muda.
3. **Lance.** O lance é `min_bid × 1.15`, arredondado a 4 casas. Se passar do cap, é reduzido até caber. Se ficar abaixo de `min_bid`, a oferta é descartada. O lance vai em `price` no PUT `/asks/{id}/`. Sem bid, o JSON não tem a chave, e o request do emerg sai byte a byte igual.
4. **Fallback para on-demand** em três casos:
   - nenhum bid elegível;
   - erro na busca bid (registrado em log, não aborta);
   - `max_preemptions_per_day` (2) lifecycles `preempted` desde a meia-noite em America/Sao_Paulo. Nesse caso nem roda a busca bid.
5. **Preempção.**
   - **Lifecycle bid em Ready.** Conta como observação terminal `intended_status=stopped`, `actual_status=stopped` ou `IsTerminal`, com 3 strikes. A causa sai `preempted`, salvo se o status_msg tiver credit/account/saldo, aí sai `billing_stopped`. Depois: drain → destroy → close `shutdown_reason='preempted'`, sem marcador de billing, e o próximo tick dentro da janela reprovisiona. Métricas incrementadas: `gateway_primary_death_detected_total{cause="preempted"}` e `gateway_primary_preemptions_total{phase="ready"}`.
   - **Lifecycle bid durante o cold start.** Os mesmos 3 strikes levam a destroy + close `preempted`. Não gera report da máquina e não entra no blocklist, porque `preempted` não é motivo atribuível à máquina. Incrementa `{phase="provisioning"}`. Esse caminho conta como falha de provision, então vale o cooldown normal de `failure_cooldown_s`.
   - **On-demand:** a classificação continua exatamente como antes.
6. **`min_reliability` = 0.95 só no primário.** Vale no filtro do servidor, num espelho client-side e no pin `force_machine_id`. O emerg e o `DefaultSearchFilter` compartilhado continuam com 0.99. Os filtros de cuda, driver e inet_down não mudaram.
7. **Auditoria.**
   - `primary_lifecycles.is_bid` e `bid_price` passam a ser gravados. Rows antigas ficam NULL.
   - O evento `offer_accepted` traz a decomposição: offer_mode, is_bid, bid_price, real_cost, cap_cost, gpu_h, storage_h, download_h, dph_base, storage_cost, inet_down_cost, min_bid e preempt_today. O log `primary offer picked` traz os mesmos campos.
   - `accepted_dph` de um lifecycle bid = lance + storage/h.
8. **`gatewayctl primary lifecycles`** ganhou a coluna MODE: `bid@0.1234`, `ondemand` ou `-` para row legada. A saída json traz `is_bid` e `bid_price`.
9. **Admin `GET/PATCH /admin/primary/config`** expõe e valida os campos novos:
   - `offer_mode`: só `bid` ou `ondemand`;
   - `bid_margin`: 1.00 a 5.00;
   - `max_preemptions_per_day`: 0 a 20;
   - `min_reliability`: 0.5 a 1.0.

   O banco tem CHECK equivalente.

## Deploy — orquestrador (ordem exata)

Pré-condição: merge deste branch em develop e imagem do gateway buildada a partir dele. O `gatewayctl` vai na mesma imagem.

1. **Migration 0039 ANTES do binário novo.** O binário novo lê `offer_mode/bid_margin/max_preemptions_per_day/min_reliability` via `SELECT` explícito em `GetPodConfig`, e `is_bid/bid_price` em `GetOpenPrimaryLifecycle` e `List*`. Sem as colunas, ele quebra. No worker-vm:
   `docker run --rm --env-file <env do stack 38> <imagem-nova> /gatewayctl migrate up`
   FATO, pelo código: a 0039 é só aditiva, com DEFAULTs e colunas NULL. O binário antigo não seleciona essas colunas. O sqlc gera listas explícitas, e o `SELECT *` do sqlc vira uma lista fixa no gen antigo. Por isso o binário antigo segue funcionando depois da 0039.
2. **Conferir a row** em `bd_ai_gateway`:
   `SELECT offer_mode, bid_margin, max_preemptions_per_day, min_reliability, cap_primary FROM ai_gateway.pod_config;` → esperado `bid | 1.15 | 2 | 0.950 | 0.2`.
   Obs.: o plano citava `gatewayctl primary config show`, mas esse subcomando NÃO existe (FATO: `cmd/gatewayctl/primary.go` só tem state/force-up/force-down/schedule/lifecycles). Usar SQL ou `GET /admin/primary/config`.
3. **PUT do stack 38** com a imagem nova. Não precisa de env nova: os defaults das envs de boot só valem quando o loader de pod_config não está ligado.
4. **Observar o próximo provision** (start agendado ou force-up):
   - log `primary offers found for shape` com `ondemand_count`/`bid_count`/`offer_mode`/`min_reliability`;
   - log `primary offer picked` com `is_bid`, `bid_price`, `real_cost`, `gpu_h`, `storage_h`, `download_h`;
   - `gatewayctl primary lifecycles --since 24h` com a coluna MODE.
   - Se aparecer `preempted`: `gateway_primary_preemptions_total` subindo e um lifecycle novo logo em seguida.

## Rollback

1. **Só desligar o bid (hot, sem restart):**
   `UPDATE ai_gateway.pod_config SET offer_mode='ondemand', updated_at=NOW() WHERE id=TRUE;`
   O trigger `pod_config_update_notify` dispara o NOTIFY e o loader recarrega. Alternativa: `PATCH /admin/primary/config {"kind":"config","field":"offer_mode","value":"ondemand"}` pelo dashboard. HIPÓTESE: esse PATCH usa a mesma auth das outras edições do dashboard; não conferi o middleware.
2. **Voltar o piso de reliability:** `UPDATE ai_gateway.pod_config SET min_reliability=0.99, updated_at=NOW() WHERE id=TRUE;` (hot).
3. **Rollback completo:** imagem anterior no stack 38. Depois disso, e só depois, rodar `gatewayctl migrate down` para desfazer a 0039. O binário novo não sobe sem as colunas.

## Investigação (por que a oferta da Argentina perdeu)

**FATOS (código + dado do orquestrador 2026-10-01 ~18:30 BRT):**
- O picker antigo ordenava pelo `dph_total` da Vast, que não reflete os 45 GB de disco. Agora ordena pelo custo real (este plano).
- O filtro do servidor exigia `reliability >= 0.99`, `cuda_max_good >= 12.8` e `driver_vers >= 570000000`. O orquestrador mediu que a maioria das ofertas 3090 baratas tinha reliability entre 0.688 e 0.98 ou cuda < 12.8, e que a oferta da Argentina já tinha saído do mercado.
- Na pod_config de prod: cap_primary 0.2, host_id 0, blocklist de 8 máquinas, reject_private_ip true, force_machine_id 0.
- Pedro decidiu, durante a execução, baixar o piso de reliability do primário para 0.95 (commit `02e5736`). cuda e driver ficam como estão.
- No benchmark L4, o download por partida custou US$0.0267/GB × 19.3 GB ≈ US$0.51, e entre hosts 3090 o mesmo download varia de US$0.025 a US$0.48. Por isso o download entrou no ranking.

**HIPÓTESES:**
- HIPÓTESE: a oferta da Argentina caiu por reliability < 0.99 (o mais provável pela amostra do mercado) e não pelo blocklist. Só dá para resolver com a linha dela no `/bundles`, e a oferta já sumiu.
- HIPÓTESE: o `dph_total` que a Vast devolve embute o storage default da busca, não os 45 GB. Isso só afeta o fallback, porque o ranking novo usa `dph_base` + storage quando há `dph_base`. Para resolver: uma linha real de `/bundles` com `dph_base`, `storage_cost` e `dph_total` lado a lado.
- HIPÓTESE: o filtro bid com `type: "bid"` e a ordenação herdada (`dph_total asc`, limit 64) traz os bids mais baratos. Não verifiquei se a Vast ordena bid por `min_bid`, e o ranking final é client-side. Para resolver: uma busca bid real comparando a ordem com `min_bid`.
- HIPÓTESE: zerar o crédito de uma instância bid sem o marcador credit/account/saldo no status_msg seria classificado como `preempted`. O estrago fica limitado a `max_preemptions_per_day` (2). Depois disso o on-demand falha no create e entra o cooldown normal. Para resolver: o status_msg real de uma parada por saldo com bid.
- HIPÓTESE: o sinal de outbid da Vast é `intended_status=stopped` ou `actual_status=stopped`. Vem da referência vast-cli e do 3060 (`is_terminal`), não de um primário bid real. Para resolver: o primeiro `preempted` em prod, com os campos logados no strike.

## Desvios do plano

### Adições pedidas pelo coordenador

**1. Download amortizado no custo real (fora do plano original)**
- `Offer.InetDownCost` (`inet_down_cost`) e `CostParams{DiskGB, WeightsDownloadGB, ExpectedHoursPerStart}`.
- O cap fica sobre GPU+storage e o ranking sobre o Total. A escolha está documentada em `pricing.go`.
- Knobs são env de boot, não hot: `PRIMARY_WEIGHTS_DOWNLOAD_GB` (20) e `PRIMARY_EXPECTED_HOURS_PER_START` (8).
- Testes: `TestRealCost_DownloadAmortization` e `TestRankCandidates_DownloadChangesWinner`.
- Commits: `c4fd3e5` (campo/env) e `435f766` (modelo).
- **Pendência para o orquestrador:** o ranking Python do pod 3060 (`ops/vast-3060/unified3060.py`, `real_cost`) NÃO tem esse termo de download.

**2. `min_reliability` 0.95 só no primário (decisão do Pedro, 2026-10-01 ~19:20 BRT)** — coluna em pod_config dentro da própria 0039 (ainda não deployada), com fallback 0.95, valor validado no admin e espelho client-side. Testes: `TestFilterMinReliability` e `TestProvision_MinReliability_RejectsBelowFloor` (0.96 passa, 0.94 é rejeitada), além das asserções em `TestPrimaryFilter_CopiesAndSpecialises` (emerg segue 0.99). Commit `02e5736`.

### Corrigidos automaticamente

**3. [Regra 1] O pin `force_machine_id` usaria o piso 0.99 do filtro compartilhado.** Com o piso do primário em 0.95, um host pinado com reliability entre 0.95 e 0.99 sumiria em silêncio. O pin passou a usar `primaryFilter(..., "on-demand", minRel)`. Commit `02e5736`.

**4. [Regra 3] O teste `TestReconcilerVastFallback` fixa a SEQUÊNCIA de chamadas SearchOffers (1 por shape).** O modo bid soma uma busca por shape, então o teste passou a rodar com `PrimaryVastOfferMode="ondemand"`, como o plano prevê. A asserção continua igual. A sequência em modo bid é coberta por `TestProvision_BidCheaper_SendsPrice`.

**5. Teste novo** `TestRecoverOpenLifecycle_RestoresIsBid`: depois de um restart, o `is_bid` é restaurado (NULL → false).

## Conformidade com o TDD

As implementações de `pricing.go` e da preempção foram rascunhadas antes de rodar os testes. Os commits `test(...)` (650dd25, 864e216) vêm antes dos `feat(...)` (435f766, 1c7ccf6), e nesses commits de teste o código não compila sem a implementação. O RED vale como falha de compilação, não como asserção falhando.

## Known Stubs

Nenhum.

## Self-Check: PASSED

- Arquivos: `gateway/db/migrations/0039_primary_bid_mode.sql`, `gateway/internal/primary/pricing.go`, `pricing_test.go`, `reconciler_bid_test.go`, `reconciler_preempt_test.go`, `gateway/internal/integration_test/migration_0039_test.go` e `gateway/cmd/gatewayctl/primary_mode_test.go` existem.
- Commits `c4fd3e5`, `650dd25`, `435f766`, `864e216`, `1c7ccf6` e `02e5736` estão presentes no branch `worktree-agent-ae3bfecd27e30132b`.
