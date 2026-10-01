---
phase: quick-260930-uru
plan: 01
status: complete
subsystem: gateway / ops-vast-3060
tags: [gateway, hot-reload, upstreams, vast, pod-3060, breaker]
clickup: 86akrbegb
requires: []
provides:
  - "ai_gateway.upstreams.url_override (migration 0038) + trigger que dispara NOTIFY para url_override/tier_priority"
  - "Loader.TargetURL / Loader.OnURLChange / UpstreamConfig.URLSource"
  - "proxy.NewDynamic{Embeddings,Audio,TTSTarget,Rerank}Proxy"
  - "breaker.Set.Reset / shed.LatencyRing.Reset"
  - "gatewayctl upstreams update --url / --clear-url; list mostra URL_OVERRIDE"
  - "unified3060.py flip_upstreams (sem PUT no Portainer) + subcomando flip-stack-legacy"
affects: [ai-gateway-prod (stack 38), /opt/vast-3060 no ops-claude]
tech-stack:
  added: []
  patterns: ["alvo de proxy resolvido por request a partir do snapshot do loader", "backstop periódico de Refresh + mutex de Refresh"]
key-files:
  created:
    - gateway/db/migrations/0038_upstreams_url_override.sql
    - gateway/internal/upstreams/url.go
    - gateway/internal/upstreams/loader_override_test.go
    - gateway/internal/proxy/dynamic_target.go
    - gateway/internal/proxy/dynamic_target_test.go
    - gateway/internal/breaker/breaker_reset_test.go
    - gateway/internal/shed/latency_reset_test.go
    - gateway/internal/primary/sweep_log_test.go
    - gateway/internal/emerg/sweep_log_test.go
    - gateway/cmd/gatewayctl/upstreams_flags_test.go
    - gateway/internal/integration_test/migration_0038_test.go
  modified:
    - gateway/db/queries/upstreams.sql
    - gateway/internal/db/gen/{models,querier,upstreams.sql}.go
    - gateway/internal/db/migrate_test.go
    - gateway/internal/upstreams/{loader,types,exports_helpers,loader_export_test}.go
    - gateway/cmd/gatewayctl/{upstreams,main,upstreams_test}.go
    - gateway/internal/proxy/{embeddings,audio,tts,rerank}.go
    - gateway/internal/breaker/breaker.go
    - gateway/internal/shed/latency.go
    - gateway/cmd/gateway/main.go
    - gateway/internal/primary/sweep.go
    - gateway/internal/emerg/sweep.go
    - gateway/internal/integration_test/{migration_0026,migration_0029}_test.go
    - ops/vast-3060/unified3060.py
    - ops/vast-3060/test_unified3060.py
decisions:
  - "Hook OnURLChange não toca o map shedLatency: reseta o breaker (lock próprio) e enfileira o nome; o anel é zerado em onUpstreamsReload sob reloadMu"
  - "Loader.Refresh serializado por refreshMu: impede que um Refresh do backstop com leitura anterior ao UPDATE sobrescreva o snapshot do NOTIFY"
  - "flip_upstreams sempre seta as 4 rows (sem idempotência por parse do list); UPDATE com mesmo valor não dispara NOTIFY (IS DISTINCT FROM)"
  - "Breaker.Reset não publica evento Redis (só local + gauge = 0)"
  - "systemd/vast-unified-start.service Description NÃO alterada (ainda diz 'flip stack 38') para não divergir da unit deployada"
metrics:
  completed: 2026-10-01
  tasks: 3
  commits: 3
---

# Quick 260930-uru: hot-reload da URL dos upstreams do pod 3060 sem restart do gateway

A URL dos upstreams local-stt, kokoro-tts, rerank-gpu e embed-gpu agora fica em `ai_gateway.upstreams.url_override`. O valor é recarregado por LISTEN/NOTIFY, com um refresh de segurança a cada 60s, e os 4 proxies resolvem o alvo a cada request. Com isso, o `unified3060.py start` troca o pod com 4 chamadas `gatewayctl upstreams update --url` e não faz mais o PUT no stack 38, que recriava a task do gateway.

## Commits

| Task | Commit | Descrição |
|------|--------|-----------|
| 1 | `5f501b5` | migration 0038 + sqlc + loader (override > env, fallback, TargetURL, OnURLChange) + gatewayctl --url/--clear-url |
| 2 | `239b0a5` | proxies dinâmicos, registro incondicional em main.go, Reset de breaker/latência, backstop 60s, log do label sweep |
| 3 | `cbfe056` | unified3060 flip via url_override (ssh + /gatewayctl), flip-stack-legacy manual |

## Resultados de teste (executor, 2026-10-01)

- `cd gateway && gofmt -l .` → vazio.
- `go build ./... && go vet ./... && go vet -tags integration ./...` → ok.
- `go test ./...` → 28 pacotes ok, 0 FAIL.
- `go test -race` em internal/{proxy,breaker,shed,upstreams,primary,emerg,emerg/vast}, cmd/gatewayctl e cmd/gateway → todos ok.
- `sqlc generate` (v1.30.0, mesma versão do CI) rodado 2x → nenhum diff no tree.
- `cd ops/vast-3060 && python3 -m unittest -v test_unified3060` → 20 testes ok (os ResourceWarning são de código anterior, build_onstart).
- Grep do plano (`flip_stack(env, ip, ports)` só em def/legacy) → ok.
- **NÃO rodado pelo executor:** testes `-tags integration` com Docker (`internal/integration_test`, `cmd/gatewayctl`), que ficam com o orquestrador. Testes novos/alterados que dependem disso:
  - `migration_0038_test.go`: hot-reload via NOTIFY e Down/Up da 0038.
  - `cmd/gatewayctl/upstreams_test.go`: `TestRunUpstreams_Update_URLOverride_SetListClear`.
  - `migration_0026_test.go` / `migration_0029_test.go`: contagem de `Down(N)` relativa ao HEAD subiu +1 por causa da 0038 (10→11, 12→13, 9→10). Mesmo padrão que as quicks anteriores usaram em cada migration nova.

## O que mudou (resumo técnico)

- **Precedência da URL:** um override válido ganha da env. Override inválido gera um WARN `status=invalid_url_override` e o loader usa a env. Uma row que só tem override (env vazia) agora carrega. Sem override e sem env, a row continua sendo pulada.
- **Proxies dinâmicos:** sem URL efetiva, o Director aponta para `ifix-upstream-unresolved.invalid` e o `unresolvedTargetRoundTripper` devolve `errDialFailedFallthrough` sem fazer I/O de rede. O ErrorHandler suprime o 502 e o dispatcher cascateia para o tier-1. O director de cada URL fica em cache pelo ponteiro `*url.URL`, que é estável até o próximo Refresh.
- **Construtores antigos** (`NewEmbeddingsProxy` etc.): assinatura e mensagens de erro iguais, agora delegam para a versão dinâmica com alvo fixo.
- **Reload:** `onUpstreamsReload` é usado pelo LISTEN e pelo backstop, os dois sob `reloadMu`. `Loader.Refresh` é serializado por `refreshMu`. Refresh sem mudança loga em Debug. O INFO "upstreams refreshed" só aparece quando algo muda.
- **Sweep:** o primary e o emerg emitem, a cada execução, um INFO `"<kind> orphan sweep done"` com os campos listed, matched_label, orphans, destroyed e failed.

## Desvios do plano

1. **[Rule 1 - Bug] Corrida entre o Refresh do backstop e o do NOTIFY.** Com dois chamadores de `Refresh`, um tick que leu o banco antes do UPDATE podia trocar o snapshot depois do Refresh disparado pelo NOTIFY. A URL velha voltaria e ficaria até o próximo tick (60s). Correção: `refreshMu` dentro de `Loader.Refresh`. A ordem de travas (backstop: reloadMu→refreshMu; LISTEN: refreshMu liberado antes de reloadMu) não forma ciclo. Commit `239b0a5`.
2. **[Rule 1 - Bug] Leitura concorrente do map no hook.** O plano sugeria o hook ler `shedLatency[name]`. No caminho do LISTEN o hook roda fora do `reloadMu`, então isso podia ler o map enquanto o backstop escreve, e o Go derruba o processo com "concurrent map read and map write". Agora o hook só reseta o breaker e enfileira o nome; o anel é zerado em `onUpstreamsReload`, sob `reloadMu`. Commit `239b0a5`.
3. **[Rule 3 - Blocking] Contagem de `Down(N)` nos testes de integração.** Os testes de migration usam `Down(N)` contado a partir do HEAD, e a 0038 empurra todos em +1. Ajustei 0026 e 0029. Commit `5f501b5`.
4. **Teste puro do gatewayctl em arquivo próprio** (`upstreams_flags_test.go`). O `upstreams_test.go` tem build tag `integration` e um TestMain com containers, então um teste sem DB lá não rodaria no `go test ./...`. O teste com DB de `--url`/`--clear-url` foi adicionado no `upstreams_test.go`.
5. **`unified3060.py`:** a linha que chama `flip_stack` dentro de `cmd_flip_stack_legacy` ganhou o comentário `# legacy` para passar no grep de verificação do plano.

## Deploy — fora do executor (ordem exata)

Pré-condição: imagem do gateway buildada a partir deste branch, depois do merge em develop.

1. **Build** da imagem nova do gateway. O `gatewayctl` vai junto na mesma imagem.
2. **Migration ANTES do binário novo** (`AI_GATEWAY_MIGRATE_ON_BOOT=false`; o binário novo faz SELECT de `url_override`). No worker-vm:
   `docker run --rm --env-file <env do stack 38> <imagem-nova> /gatewayctl migrate up`
   O binário antigo continua funcionando depois da 0038 (ver "Riscos" abaixo).
3. **Stack 38 com a imagem nova** (PUT). É o último restart planejado da task.
4. **Seed das 4 rows** com a URL atual da env, o que não muda o tráfego:
   `docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl upstreams update --name <local-stt|kokoro-tts|rerank-gpu|embed-gpu> --url <valor atual de UPSTREAM_*>`
   Conferir com `/gatewayctl upstreams list` (coluna URL_OVERRIDE).
5. **Deploy do `ops/vast-3060/unified3060.py`** em `/opt/vast-3060/`. Opcional: atualizar o texto `Description=` da unit `vast-unified-start.service`, que ainda diz "flip stack 38" (é só texto, não muda comportamento).
6. **Observar o próximo start das 07h:**
   - `docker service ps ai-gateway-prod_gateway`: nenhuma task nova.
   - No log do gateway: `upstream effective url changed` (4x) e `breaker reset (upstream url changed)`.
   - No journal do unified3060: `upstreams flipados via url_override` e depois `edge ok`.
   - No log do gateway: `primary orphan sweep done` / `emerg orphan sweep done` a cada sweep.

## Rollback

1. Limpar os overrides para voltar às envs: `/gatewayctl upstreams update --name <X> --clear-url` nas 4 rows.
2. Se o pod mudou e a env está velha: `unified3060.py flip-stack-legacy`. Faz o PUT antigo no stack 38 e recria a task. Precisa de `PORTAINER_API_KEY`.
3. Imagem anterior no stack 38.
4. `gatewayctl migrate down` (0038 down) **só depois** do binário antigo estar rodando. O binário novo faz SELECT da coluna e quebraria sem ela.
5. Restaurar o `unified3060.py` anterior em `/opt/vast-3060/` (o novo depende de `--url` existir no gatewayctl).

## Riscos em aberto — FATO vs HIPÓTESE

**FATOS** (fonte entre parênteses):
- Root do ops-claude consegue `ssh -i /home/pedro/.ssh/id_ed25519 -o BatchMode=yes root@10.10.10.50`, e a host key está no known_hosts do root (verificado pelo orquestrador em prod, 2026-10-01 01:16Z). Por isso `gateway_ssh_argv` usa `BatchMode=yes` sem `StrictHostKeyChecking=no`.
- `/gatewayctl upstreams list` via `docker exec` funciona em prod, e o DSN é `doadmin`, que pode fazer UPDATE (orquestrador, mesma verificação).
- O container prod não tem `sh`/`env` (orquestrador). O comando remoto chama `/gatewayctl` direto no `docker exec`; o `$(docker ps ...)` é expandido pelo shell do host worker-vm (teste `GatewayctlCmdTest.test_argv_exact`).
- O gateway prod roda com 1/1 réplica (orquestrador). Então o `Breaker.Reset` local cobre o único processo.
- Em prod, `kokoro-tts` está com TIER 0 (orquestrador). O código novo não usa tier em nada: proxies, TargetURL e hook são por nome.
- Nenhuma query de `db/queries/upstreams.sql` usa `SELECT *` (grep no arquivo). As 3 SELECTs listam colunas, então o ADD COLUMN NULL não afeta o binário antigo.
- O trigger 0038 só adiciona condições ao WHEN do 0009 (`tier_priority`, `url_override`). Função e trigger de INSERT/DELETE ficaram iguais (arquivo da migration).

**HIPÓTESES** (e o que resolveria):
- HIPÓTESE: o LISTEN aplica o override em menos de 2s em prod. O `sleep(5)` antes do `validate_edge` tem folga, e o `validate_edge` ainda tenta 10x20s. Resolve: comparar o timestamp de `upstream effective url changed` com o do `gatewayctl update` no primeiro start das 07h.
- HIPÓTESE: se houver mais de uma réplica no futuro, uma réplica com `remoteOpen` para o nome mantém o curto-circuito até expirar o Cooldown, porque o Reset não publica evento Redis. O espelho `gw:breaker:state` no Redis também pode continuar "open" até a próxima transição. Resolve: `docker service ls` (hoje 1/1, então não se aplica) e `gatewayctl breaker list` logo após um flip.
- HIPÓTESE: a migration 0038 Up/Down passa no Postgres dos testes de integração. Não rodei com Docker. Resolve: `go test -tags integration ./internal/integration_test/... -run 'Migration00(26|29|38)'` pelo orquestrador.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície fora do `<threat_model>` do plano. As mitigações T-uru-01..04 estão implementadas: `ValidateUpstreamURL` no loader e no gatewayctl, `shlex.quote` + `validate_url` no script, fallthrough sem I/O e backstop de 60s.

## Self-Check: PASSED

- Arquivos criados existem (migration 0038, url.go, dynamic_target.go e testes): verificado via `git status`/build.
- Commits `5f501b5`, `239b0a5` e `cbfe056` presentes em `git log`.
