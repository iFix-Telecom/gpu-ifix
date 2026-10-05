# HANDOFF 2026-10-01 ~19:20 BRT — custo dos pods Vast (retomar em nova sessão)

ClickUp ativo: [pod primário 3090: reduzir custo](https://app.clickup.com/t/86akrnpwc) (86akrnpwc).
Relacionados em "em testes": 86akregky (custo 3060), 86akreh6u (STT→gemini).

## ATUALIZAÇÃO 2026-10-05 13:46 BRT — PRIMÁRIO DE VOLTA A ON-DEMAND (decisão Pedro)
- Bid no primário 3090: 3 tentativas, 0 aproveitadas (535 health_timeout, 537 host sem disco, 539 preemptado em 8 min).
  Ready atrasou 08:10 (sex) e 08:45 (seg) vs janela 07:30; economia só ~10–15% (US$0,12–0,14 vs 0,15–0,16/h).
- `UPDATE pod_config SET offer_mode='ondemand'` aplicado a quente (NOTIFY + refresh ok, sem restart). Demais campos mantidos
  (bid_margin 1.15, max_preemptions 2, min_reliability 0.95). Código bid permanece no gateway (reversível).
- 3060 SEGUE em bid (US$0,045/h); gargalo dele = timeouts de boot (xtts/infinity/boot), não preempção.
- Pendências: audit flush falha com byte inválido UTF-8 (0x98) → registro descartado; 4× panic `abort Handler` 00:10 05/10.

## ATUALIZAÇÃO 2026-10-02 06:05 BRT — DEPLOY FEITO (passos 2–4)
- Integração local verde (integration_test 383s, gatewayctl 41s); CI run 36940337213 verde (webhook dev = falha esperada).
- Push develop `1a99386`. Migration **0039 aplicada** em prod (goose v39) via one-off com a imagem nova.
- Stack 38 → `develop-1a99386@sha256:1fd0e30c40d6…` (84 envs; backup em scratchpad da sessão). 0 ERROR no boot.
- pod_config: `bid | 1.15 | 2 | 0.950` ✔. Overrides 3060 preservados. Smoke chat (`ok`, stop) + embeddings (1024) ✔.
- Primário estava `asleep` no deploy (sem lifecycle derrubado). **Pendente passo 5:** 1º primário bid sobe ~09:00 —
  conferir coluna MODE no `gatewayctl primary lifecycles`, DPH vs `dph_total` da instância; 3060 bid às 07:00
  (`journalctl -u vast-unified-start -u vast-unified-watchdog`).

## Estado

### Status do código (atualizado 2026-10-01 ~19:50)
- **Quick 261001-qdd CONCLUÍDA e MERGEADA em develop** (commits c4fd3e5..02e5736 + merge; SUMMARY em
  `.planning/quick/261001-qdd-primario-3090-custo-real-bid-fallback/261001-qdd-SUMMARY.md`).
  `go test ./...` OK pós-merge. **NÃO pushado, NÃO deployado.** Falta integração Docker + push + CI + deploy.
- Escopo entregue: migration **0039** (pod_config `offer_mode='bid'`, `bid_margin` 1.15,
  `max_preemptions_per_day` 2, `min_reliability` 0.95 só primário; primary_lifecycles `is_bid`,`bid_price`);
  ranking custo real (GPU + storage + download amortizado `inet_down_cost×20G/8h`, envs
  `PRIMARY_WEIGHTS_DOWNLOAD_GB`/`PRIMARY_EXPECTED_HOURS_PER_START`); bid + fallback on-demand;
  preempção → destroy + close `preempted` + reprovisão; métrica `gateway_primary_preemptions_total`;
  coluna MODE no `gatewayctl primary lifecycles`.
- Conferência pós-migration: `SELECT offer_mode,bid_margin,max_preemptions_per_day,min_reliability FROM ai_gateway.pod_config;`
  → `bid | 1.15 | 2 | 0.950` (`gatewayctl primary config show` NÃO existe).
- Rollback a quente: `UPDATE ai_gateway.pod_config SET offer_mode='ondemand', updated_at=NOW() WHERE id=TRUE;`
  (idem `min_reliability=0.99`). Completo: imagem anterior no stack 38 primeiro, depois `migrate down`.

### Próximos passos (APROVADOS pelo Pedro: "ok para os próximos passos")
1. ~~merge~~ FEITO (develop local, 4+ commits à frente do origin).
2. Verificar: `cd gateway && gofmt -l . && go build ./... && go vet ./... && go vet -tags integration ./... && go test ./...`
   + integração com Docker via sudo (receita na memória `gateway-prod-build-deploy`):
   `sudo -n env CI_ALLOW_TIGHT_SHED_TIMING=1 PATH=... go test -tags=integration ./gateway/internal/integration_test/... ./gateway/cmd/gatewayctl/...`
   (rodar 1 pacote por vez — host com pouca RAM; `TestSensitiveSaturated503` pode falhar por pressão de memória → re-rodar isolado).
3. Docs GSD (SUMMARY + STATE), scan de segredos no diff, push develop, aguardar CI `build-gateway`.
4. Deploy prod (ordem):
   a. pre-pull `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-<sha>` no worker-vm;
   b. **migration 0039** one-off com a imagem NOVA (env-file = TODAS as envs do container vivo,
      umask 077, apagar depois): `docker run --rm --env-file f --entrypoint /gatewayctl <img> migrate status|up`;
   c. PUT stack 38 (Portainer endpoint 6) trocando só `image:` (84 envs iguais; backup do stack antes);
   d. verificar versão, 0 ERROR, `primary state`, overrides 3060 preservados, smoke chat/embeddings;
   e. confirmar `offer_mode='bid'` e `min_reliability=0.95` no pod_config.
   Rollback: `offer_mode='ondemand'`; `docker service update --rollback ai-gateway-prod_gateway`.
   ⚠️ Restart do gateway durante provisionamento do primário reinicia o lifecycle (aceitável).
5. Validar no 1º dia: primário nasce bid? custo real logado vs `dph_total` da instância
   (HIPÓTESE: min_bid sem storage); status real de uma preempção.

### Já em produção hoje
- Gateway `develop-66050fd`: hot-reload URL (url_override), auth singleflight+L1, revoke imediato,
  shed dwell, STT OOM sem breaker, sweep de órfãos com log.
- `/opt/vast-3060` (commit 3d4b0b2): bid default + fallback, watchdog 3 min (07–19h), disco 40G,
  whisper int8_float16, ranking com download amortizado. **1º pod 3060 interruptível: 2026-10-02 07:00**
  — conferir `journalctl -u vast-unified-start -u vast-unified-watchdog`.
- Preços groq-whisper + phantom cadastrados.

### Achados registrados
- L4: cabe ctx de prod mas ~90 tok/s (3090 ~190) e US$/1M tokens pior → não usar no primário.
- Taxa de download por host (`inet_down_cost`) pesa até ~US$0,50/subida.
- Primário barato (US$0,10) morreu sozinho às 13:08Z (host terminal) — mercado, não bug.
