---
phase: quick-260930-vkt
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/auth/apikey.go
  - gateway/internal/auth/l1cache.go
  - gateway/internal/auth/l1cache_test.go
  - gateway/internal/auth/apikey_test.go
  - gateway/internal/shed/set.go
  - gateway/internal/shed/set_test.go
  - gateway/cmd/gateway/main.go
  - gateway/internal/integration_test/helpers_shed_test.go
  - gateway/internal/integration_test/shed_edge_cases_test.go
  - gateway/internal/integration_test/shed_sc2_hysteresis_test.go
  - gateway/cmd/gatewayctl/model_alias_integration_test.go
  - gateway/cmd/gatewayctl/primary_lifecycles_integration_test.go
  - gateway/cmd/gatewayctl/upstreams_test.go
  - .github/workflows/build-gateway.yml
autonomous: true
requirements: [CLICKUP-86akrbgbt]
clickup: 86akrbgbt

must_haves:
  truths:
    - "N requisições concorrentes com a MESMA key em cache miss executam 1 lookup no DB e 1 argon2id (não N)"
    - "Com Redis fora (erro em GET), requisições repetidas da mesma key válida NÃO rodam argon2 por requisição — servidas do L1 in-process"
    - "Janela de revogação continua ≤ 60s (D-A2): L1 só é populado pelo caminho autoritativo (DB+argon2), TTL L1 30s ≤ TTL Redis 60s"
    - "Erros (DB, argon2, key inválida/revogada) nunca entram no L1 como sucesso"
    - "shed_arm_seconds / shed_recover_seconds do circuit_config chegam ao FSM no boot e em todo reload (LISTEN + backstop 60s); 0 → 30/60"
    - "go test -tags=integration ./gateway/cmd/gatewayctl/... roda no job de integração do CI"
  artifacts:
    - path: "gateway/internal/auth/l1cache.go"
      provides: "cache L1 in-process TTL+limite de tamanho, só entradas positivas"
    - path: "gateway/internal/auth/apikey.go"
      provides: "Verify com L1 → Redis → neg → singleflight(DB+argon2+cachePut+l1Put)"
      contains: "singleflight"
    - path: "gateway/internal/shed/set.go"
      provides: "Set.ApplyConfigs(map[string]Config) aplicando arm/recover (0 → default) em FSMs existentes"
    - path: ".github/workflows/build-gateway.yml"
      contains: "./gateway/cmd/gatewayctl/..."
  key_links:
    - from: "gateway/cmd/gateway/main.go"
      to: "shedSet.ApplyConfigs"
      via: "chamado logo após shedSet.Rebuild no boot E dentro de onUpstreamsReload"
      pattern: "ApplyConfigs"
    - from: "gateway/internal/auth/apikey.go"
      to: "golang.org/x/sync/singleflight"
      via: "Verifier.sf.Do(hex(lookupHash), ...)"
      pattern: "sf\\.Do"
---

<objective>
Corrigir 2 bugs reais do gateway e destravar os testes de integração quebrados.

A) Stampede argon2id no auth (PROD, medido no worker-vm: 1 verify 303ms, 10 conc 1.3s, 50 conc 27s). Causa: cache miss → argon2id 64MiB/3iter sem coalescing; erro de Redis cai no caminho caro por requisição.
B) shed_arm_seconds / shed_recover_seconds nunca chegam ao FSM (FSM.UpdateConfig sem caller fora de testes desde Phase 5 ac8cb8e) — FSM fica fixo em 30s/60s.
C/D) Testes de integração podres (internal/integration_test + cmd/gatewayctl) e job de CI que não roda os de gatewayctl.

Output: auth com singleflight + L1; wiring de config do shed; testes corrigidos; CI ampliado. SEM push, SEM deploy. Executor NÃO roda Docker/testcontainers — integração é do orquestrador.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@./CLAUDE.md
@gateway/internal/auth/apikey.go
@gateway/internal/auth/cache.go
@gateway/internal/auth/apikey_test.go
@gateway/internal/shed/set.go
@gateway/internal/shed/fsm.go
@gateway/cmd/gateway/main.go

Scratch do debugger com patches já validados (diff contra c03e329):
/tmp/claude-1000/-home-pedro-projetos-pedro-gpu-ifix/4402981a-72bf-491d-b313-511740ed26a5/scratchpad/repo/
- gateway/cmd/gateway/main.go → `applyShedCfg` (linhas ~466-484 + chamada no reload) — reaproveitar a lógica, mas mover para o pacote shed (testável).
- gateway/internal/integration_test/helpers_shed_test.go → `UPDATE ai_gateway.tenants SET rps_limit=1000, rpm_limit=60000` no seed + warm-up serial por tenant.
- Logs: scratchpad/fy_sc2.log, fy_TestSC2_HysteresisNoFlapping.log, fx_TestSensitiveSaturated503.log, fx_TestTier1UnavailableShedded503.log, ctl.log.

<interfaces>
FATOS extraídos do código (develop 7a495fc):

auth (apikey.go):
- `type Verifier struct { pool *pgxpool.Pool; q authQueries; redis *redis.Client; log *slog.Logger; touchBuf *TouchBuffer }`
- Construtores: `NewVerifier(pool, rdb, log, touchBuf)` (main.go:273) e `NewVerifierWithQueries(q, rdb, log, touchBuf)` (testes).
- `Verify(ctx, rawKey) (AuthContext, error)`: malformed → `cacheGet` (erro só WARN e segue) → `negCacheCheck` → `q.GetActiveKeyByLookupHash(ctx, LookupHash(rawKey))` (ErrNoRows → negCachePut + ErrInvalidAPIKey; outro erro → `fmt.Errorf("auth: db lookup: %w")`) → `VerifyHash(rawKey, row.KeyHash)` → `cachePut` → `touchBuf.Enqueue(row.ID)` → `hitToAuth(entry)`.
- `cacheEntry{TenantID, APIKeyID, DataClass, Status, KeyPrefix}`; `hitToAuth` mapeia Status "active"→ok, "revoked"→ErrRevokedAPIKey, outro→ErrInvalidAPIKey.
- cache.go: `cacheTTL = 60s`, `negCacheTTL = 5s`, chaves `gw:apikey:<sha256hex>` / `gw:apikey:neg:<sha256hex>`.
- REVOGAÇÃO HOJE: `gatewayctl key revoke` (cmd/gatewayctl/key.go:157) só faz `q.RevokeAPIKey` no DB — NÃO invalida Redis, sem NOTIFY/pubsub. Propagação = expiração do TTL Redis 60s (D-A2). Não existe invalidação ativa para replicar no L1.
- Testes: `fakeQueries` com `lookupCalls` atômico, `forceErr`, `rows map[hex]row`; `newTestVerifierFull(t) (mr, q, v, tb)` com miniredis.
- go.mod raiz: `go 1.24.9`; `golang.org/x/sync v0.19.0` já é dependência direta (nenhum uso de singleflight ainda). Sem lib de LRU no go.mod — NÃO adicionar dependência nova.

shed:
- `Set.Rebuild(names []string)` só cria FSM para nomes novos (com `s.defaultCfg`) e remove ausentes; FSMs existentes mantêm estado (D-C5).
- `Set.Get(name) (*FSM, bool)`; `Set.defaultCfg Config` vindo de `Options{DefaultArmSeconds:30, DefaultRecoverSeconds:60}` (main.go:435-437).
- `(*FSM).UpdateConfig(cfg Config)` → força `cfg.Upstream = f.upstream` e `f.cfg.Store(&cfg)` (atômico).
- `shed.Config{Upstream string; ArmSeconds int64; RecoverSeconds int64; ...}` (fsm.go:74) — conferir campos restantes antes de montar o struct (não zerar campos além de arm/recover que o FSM leia).
- upstreams: `loader.Names() []string`, `loader.Get(name) (UpstreamConfig, bool)`, `UpstreamConfig.CircuitConfig.ShedArmSeconds int` / `.ShedRecoverSeconds int` (types.go:79,114).
- main.go: boot `shedSet.Rebuild(upstreamNames)` (~465); reload `onUpstreamsReload` (~535) chama `breakerSet.Rebuild` + `shedSet.Rebuild(loader.Names())`, e é o ÚNICO ponto usado tanto pelo LISTEN quanto pelo backstop periódico de 60s (260930-uru), sempre sob `reloadMu`.

FSM (fsm.go ~195-215): Off→Armed (saturated) ; Armed→Off (!saturated) | Armed→On (elapsed≥Arm) ; On→Recovering (!saturated, SEM espera) ; Recovering→On (saturated, pula Armed) | Recovering→Off (elapsed≥Recover).
Middleware (middleware.go:193): só shedda em StateOn; Recovering/Armed/Off passam tier-0. `local_inflight_max_llm=0` → `defaultCapForRole` (4) (middleware.go:200-203).

gatewayctl tests (-tags integration):
- model_alias_integration_test.go:154 lê `row["Alias"]`/`row["UpstreamName"]`; output de `get` virou snake_case em 49b8be9 (model_alias.go:299-309) → `alias`, `upstream_name`.
- `freshSchema` (upstreams_test.go:~108) trunca api_keys, audit_log, audit_log_content, usage_counters, tenants — NÃO reseta model_aliases.
- primary_lifecycles_integration_test.go: `seedPrimaryLifecycle(t, ctx, pool, reason, offsetMinutes, instanceID, ended bool)`; seeds com ended=false em FetchesFromDB (id1,id2 abertos) e RespectsLimitFlag (loop, todos abertos) violam índice único `primary_live_singleton` (migration 0023:41, só 1 linha aberta).
- CI: job `integration-test` em .github/workflows/build-gateway.yml:84-109 roda só `./gateway/internal/integration_test/...`.
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Auth — singleflight + L1 in-process no Verifier</name>
  <files>gateway/internal/auth/l1cache.go, gateway/internal/auth/l1cache_test.go, gateway/internal/auth/apikey.go, gateway/internal/auth/apikey_test.go</files>
  <behavior>
    - TestVerify_SingleflightCoalescesConcurrentMiss: fake bloqueia GetActiveKeyByLookupHash num canal até 50 goroutines estarem em Verify com a mesma key válida; libera → todas retornam sucesso com o mesmo TenantID; lookupCalls == 1 e contador de argon2 == 1.
    - TestVerify_SingleflightDistinctKeysNotCoalesced: 2 keys diferentes concorrentes → 2 lookups.
    - TestVerify_L1ServesWhenRedisDown: 1º Verify popula; depois `mr.Close()` (Redis erro em GET); 20 Verify seguintes → sucesso, lookupCalls continua 1, argon2 continua 1.
    - TestVerify_L1ExpiresAndRevocationPropagates: após 1º Verify, trocar row no fake para ausente (revogada → ErrNoRows, igual a query real que filtra status='active'), flush miniredis, avançar relógio injetado do L1 além de l1TTL → próximo Verify retorna ErrInvalidAPIKey. Antes de expirar o L1, o Verify ainda passa (documenta a janela).
    - TestVerify_ErrorsNotCachedInL1: forceErr de DB → erro; remover forceErr → próximo Verify faz novo lookup (lookupCalls 2) e sucede; key revogada/inválida nunca entra no L1 (l1.len()==0).
    - TestVerify_LeaderCancelDoesNotFailWaiters: líder com ctx cancelado logo após entrar no sf; waiter com ctx vivo recebe sucesso (não context.Canceled).
    - l1cache_test: put/get, TTL expirado retorna miss, limite de tamanho respeitado (inserir max+N → len ≤ max), concorrência sob -race.
    - Todos os testes existentes de apikey_test.go, cache_test.go e load_test.go continuam passando.
  </behavior>
  <action>
1. Criar `gateway/internal/auth/l1cache.go`: tipo não exportado `l1Cache` com `sync.Mutex`, `map[string]l1Item{entry cacheEntry; exp time.Time}`, `ttl`, `max int`, `now func() time.Time` (injetável para testes). Métodos `get(key) (cacheEntry, bool)` (expirado → apaga e miss), `put(key, entry)`, `len() int`. Ao atingir `max` no put: primeiro varre e remove expirados; se ainda cheio, remove uma entrada arbitrária (iteração de map) — sem dependência nova (decisão: não há LRU no go.mod; limite de tamanho é o que importa, a política exata de despejo não). Constantes: `l1TTL = 30 * time.Second` e `l1MaxEntries = 10000`, com comentário em pt-BR explicando que l1TTL ≤ cacheTTL (60s) e que o L1 só é escrito pelo caminho autoritativo, então a janela de revogação de D-A2 continua ≤ 60s. Chave do L1 = `cacheKeyFor(rawKey)` (sha256, nunca a key crua em memória como chave).
2. Em `apikey.go`: adicionar ao `Verifier` os campos `sf singleflight.Group` (golang.org/x/sync/singleflight, já no go.mod), `l1 *l1Cache` e `verifyHash func(raw, hash string) (bool, error)` (default `VerifyHash`; testes substituem para contar chamadas argon2). Inicializar l1 e verifyHash nos DOIS construtores.
3. Nova ordem em `Verify`: malformed → **L1 get** (hit → `hitToAuth`, e ainda `touchBuf.Enqueue` NÃO é necessário: touch já é debounced; manter comportamento atual de enfileirar só no caminho DB) → Redis positive (como hoje; hit NÃO popula L1 — decisão para não estender a janela de revogação além de 60s) → negative cache (como hoje) → `v.sf.Do(hex(LookupHash(rawKey)), fn)`.
4. `fn` do singleflight encapsula: DB lookup + `v.verifyHash` + `cachePut` + `l1.put` + `touchBuf.Enqueue`, retornando `cacheEntry` ou erro. Rodar com `ctx` desacoplado do chamador: `context.WithoutCancel(ctx)` + `context.WithTimeout(…, 5s)` (Go 1.24 ok), para que cancelamento do líder não falhe os waiters. ErrNoRows / mismatch → negCachePut + ErrInvalidAPIKey (como hoje); erro de DB → wrap como hoje; NENHUM erro é escrito em L1 nem em cache positivo. L1 só recebe entry com Status "active" (revogada cai no hitToAuth mas não vai para L1).
5. Fora do sf: `hitToAuth(entry)` no resultado compartilhado. Logs/erros mantêm as mensagens atuais (`auth: db lookup: %w`, `argon2 verify error`).
6. Atualizar o comentário de topo do `Verify`/`Verifier` com a nova hierarquia (L1 → Redis → neg → singleflight). Comentários em pt-BR consistentes com o estilo das quick tasks recentes.
7. Escrever os testes de `<behavior>` em apikey_test.go (adicionar canal de bloqueio opcional ao `fakeQueries`, ex. campo `gate chan struct{}` + `entered` contador) e l1cache_test.go.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && gofmt -l gateway/internal/auth && go vet ./gateway/internal/auth/... && go test -race -count=1 ./gateway/internal/auth/...</automated>
  </verify>
  <done>gofmt vazio; todos os testes de auth (novos + existentes) passam com -race; singleflight comprovado por lookupCalls==1 e argon2==1 com 50 goroutines; L1 serve com Redis fechado; revogação propaga após l1TTL.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Shed — aplicar shed_arm/recover_seconds do circuit_config no boot e no reload</name>
  <files>gateway/internal/shed/set.go, gateway/internal/shed/set_test.go, gateway/cmd/gateway/main.go</files>
  <behavior>
    - TestSet_ApplyConfigs_UpdatesExistingFSM: Rebuild(["a"]); ApplyConfigs({"a": {ArmSeconds:5, RecoverSeconds:7}}) → FSM "a" usa arm 5 / recover 7 (verificar via Evaluate com relógio: Armed→On só após ≥5s; ou via getter de config se existir).
    - TestSet_ApplyConfigs_ZeroFallsBackToDefault: arm/recover 0 → defaultCfg (30/60 vindo de Options).
    - TestSet_ApplyConfigs_PreservesState: FSM em StateOn continua On após ApplyConfigs (D-C5).
    - TestSet_ApplyConfigs_UnknownNameIgnored: nome sem FSM → no-op, sem panic.
  </behavior>
  <action>
1. Em `set.go`, adicionar `func (s *Set) ApplyConfigs(cfgs map[string]Config)`: sob `s.mu.RLock`, para cada nome com FSM existente, partir de `s.defaultCfg`, sobrescrever ArmSeconds/RecoverSeconds quando > 0, e chamar `fsm.UpdateConfig(cfg)` (UpdateConfig já fixa Upstream). Antes de escrever, conferir em fsm.go se `Config` tem outros campos lidos pelo FSM — preservá-los a partir de defaultCfg/config atual, nunca zerar. Doc comment: corrige o gap de Phase 5 (ac8cb8e) — circuit_config parseado mas nunca aplicado.
2. Em `main.go`, criar uma closure `applyShedConfigs := func()` (lógica do scratch `applyShedCfg`): monta `map[string]shed.Config` a partir de `loader.Names()` + `loader.Get(n).CircuitConfig.ShedArmSeconds/ShedRecoverSeconds` (int → int64) e chama `shedSet.ApplyConfigs`. Chamar logo após `shedSet.Rebuild(upstreamNames)` no boot (~linha 465) e dentro de `onUpstreamsReload` logo após `shedSet.Rebuild(loader.Names())` — esse é o ponto único usado pelo LISTEN e pelo backstop periódico 60s da 260930-uru (já sob reloadMu). Não criar um segundo ponto de reload.
3. Comentário pt-BR no main.go: prod hoje não tem rows com shed_arm/recover custom (diagnóstico do debugger) → comportamento de prod inalterado (30/60).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && gofmt -l gateway/internal/shed gateway/cmd/gateway && go build ./gateway/... && go vet ./gateway/internal/shed/... ./gateway/cmd/gateway/... && go test -race -count=1 ./gateway/internal/shed/... ./gateway/internal/upstreams/... ./gateway/cmd/gateway/...</automated>
  </verify>
  <done>ApplyConfigs existe e é coberto por teste; main.go chama-o no boot e em onUpstreamsReload (grep `ApplyConfigs` retorna ≥3 ocorrências não-comentário: definição + 2 chamadas via closure); -race verde.</done>
</task>

<task type="auto">
  <name>Task 3: Corrigir testes de integração (shed + gatewayctl) e ampliar CI</name>
  <files>gateway/internal/integration_test/helpers_shed_test.go, gateway/internal/integration_test/shed_edge_cases_test.go, gateway/internal/integration_test/shed_sc2_hysteresis_test.go, gateway/cmd/gatewayctl/model_alias_integration_test.go, gateway/cmd/gatewayctl/primary_lifecycles_integration_test.go, gateway/cmd/gatewayctl/upstreams_test.go, .github/workflows/build-gateway.yml</files>
  <action>
O executor NÃO roda Docker/testcontainers; garante compilação (`go vet -tags=integration`, `-tags=integration,integration_slow`) e deixa a execução real para o orquestrador. Onde algo "não validado" for escrito, marcar no SUMMARY como HIPÓTESE pendente de execução.

1. helpers_shed_test.go (`newShedStack`): portar do scratch (a) `UPDATE ai_gateway.tenants SET rps_limit = 1000, rpm_limit = 60000` após o seed (SC1 batia rate limit 20 rps; default da migration 0013:21) e (b) warm-up serial de 1 requisição autenticada por tenant depois do /health, com Tier0 latency 0 (validado no scratch). Com o fix da Task 1 o warm-up deixa de ser estritamente necessário, mas mantém para isolar os testes de shed do custo do argon2; comentar isso.
2. shed_edge_cases_test.go `TestSensitiveSaturated503`: o teste atual seta `local_inflight_max_llm = 0`, que significa "default 4" (middleware.go:200-203) → passa 200. Reescrever: forçar FSM ON via override Redis `gw:shed:force:local-llm=on` (ver como `TestShedForceOverride` no mesmo arquivo seta o override e reutilizar o helper/padrão), `local_inflight_max_llm = 1` para telefonia, Tier0Mock com latência alta (ex. 3s), disparar 1 request de telefonia em goroutine, esperar ela entrar em voo (poll curto do inflight ou sleep ≥ 300ms), então mandar a 2ª → asserção estrita 503 + Retry-After 5 + código sensível + audit row. Não validado — marcar no SUMMARY.
3. `TestTier1UnavailableShedded503`: fortalecer se viável — usar o mesmo force-ON + cap baixo para garantir que o request realmente é sheddado, tier1 mock 503 até o breaker abrir, e transformar a asserção soft em `t.Errorf` para status != 503 / Retry-After != "30" / código ausente. Se, ao ler o caminho D-D1 no middleware/dispatcher, não houver forma determinística, manter soft e registrar o porquê no SUMMARY (não silenciar).
4. shed_sc2_hysteresis_test.go: com arm/recover agora honrados (1s/2s) e fases de 10s, o esperado é ~4 transições por ciclo de 20s ≈ 24 em 120s; observado no scratch 111. Antes de mudar a asserção, LER fy_sc2.log / fy_TestSC2_HysteresisNoFlapping.log no scratchpad e classificar as transições por reason. HIPÓTESE do debugger: On→Recovering não tem espera (fsm.go:200) e Recovering não shedda (middleware.go:193) → em saturação o tráfego volta ao tier-0, satura de novo, Recovering→On (fsm.go:205) → bounce On↔Recovering. Se os logs confirmarem `signal_dropped` / `signal_returned_during_recover` dominando dentro das fases altas, isso é FLAPPING REAL do FSM: registrar como achado no SUMMARY (seção "Achados") com contagem por reason, e NÃO afrouxar a asserção para esconder — trocar a asserção para um limite por ciclo coerente com a máquina correta (ex. `transitions <= 6 * ciclos` contando apenas Off↔Armed↔On↔Recovering↔Off completos) e deixar o teste FALHANDO de forma explícita se o flapping persistir, com mensagem apontando o achado. Se os logs mostrarem que o excesso é artefato do teste (ex. publish duplicado), ajustar a contagem e documentar. Não alterar fsm.go/middleware.go nesta task (correção do FSM, se necessária, é decisão do Pedro).
5. gatewayctl `TestModelAliasGet_ReturnsSpecificRow`: usar `row["alias"]` e `row["upstream_name"]` (snake_case de 49b8be9). Em `freshSchema` (upstreams_test.go), resetar model_aliases para o estado do seed: localizar a migration que semeia model_aliases e, após o TRUNCATE dos demais, executar `DELETE FROM ai_gateway.model_aliases` + reinserir exatamente as linhas do seed (copiar o INSERT da migration) — `TestModelAliasList_Returns6Rows` deve continuar vendo 6.
6. primary_lifecycles_integration_test.go: o índice `primary_live_singleton` (0023:41) só admite 1 linha aberta. Em `TestRunPrimaryLifecyclesIntegration_FetchesFromDB` e `TestRunPrimaryLifecycles_RespectsLimitFlag`, semear com `ended=true` todas exceto a mais recente (menor offsetMinutes). Conferir se alguma asserção depende de linha aberta e ajustar coerentemente.
7. build-gateway.yml, job `integration-test`, step "Integration tests": acrescentar `go test -tags=integration ./gateway/cmd/gatewayctl/... -count=1 -v -timeout=10m` (mesmo step ou step novo logo depois). Manter o restante do job intacto.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && gofmt -l gateway && go vet -tags=integration ./gateway/... && go vet -tags=integration,integration_slow ./gateway/internal/integration_test/... && go test -count=1 ./gateway/... ./pkg/openai/... && grep -v '^\s*#' .github/workflows/build-gateway.yml | grep -c 'tags=integration ./gateway/cmd/gatewayctl'</automated>
  </verify>
  <done>Tudo compila com as tags de integração; go test ./... (unit) verde; CI contém o comando dos testes de gatewayctl; SUMMARY lista quais testes de integração foram alterados sem execução real (para o orquestrador rodar com Docker) e o veredito do SC2 (flapping real ou artefato) com contagem por reason.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| client → gateway auth | API key não confiável chega no header; Verify decide tenant/data_class |
| gateway → Redis | cache compartilhado; pode falhar/ficar indisponível |
| DB (circuit_config) → shed FSM | config operacional controlada por admin via gatewayctl |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-vkt-01 | Elevation | L1 cache (apikey.go) | mitigate | L1 só populado no caminho DB+argon2 com Status active; TTL 30s ≤ Redis 60s → janela de revogação ≤ 60s (D-A2 inalterado); teste de propagação de revogação |
| T-vkt-02 | Spoofing | singleflight key | mitigate | chave = sha256 completo do raw key (hex(LookupHash)); resultado compartilhado só entre requests da mesma key |
| T-vkt-03 | Denial of Service | argon2 stampede / Redis blip | mitigate | singleflight coalesce + L1 evita argon2 por request; L1 com limite 10k entradas (memória limitada) |
| T-vkt-04 | Information Disclosure | L1 memória | accept | guarda só tenant/key ids/prefixo, chave do mapa é sha256; key crua nunca armazenada |
| T-vkt-05 | Tampering | erros cacheados | mitigate | nenhum erro/negativo entra no L1; testes TestVerify_ErrorsNotCachedInL1 |
| T-vkt-06 | Denial of Service | shed arm/recover custom | accept | só admin escreve circuit_config; 0 → default 30/60; prod sem rows custom |
</threat_model>

<verification>
- `gofmt -l gateway` vazio
- `go build ./gateway/...`, `go vet ./gateway/...`, `go vet -tags=integration ./gateway/...`, `go vet -tags=integration,integration_slow ./gateway/internal/integration_test/...`
- `go test -count=1 ./gateway/... ./pkg/openai/...`
- `go test -race -count=1 ./gateway/internal/auth/... ./gateway/internal/shed/... ./gateway/internal/upstreams/... ./gateway/cmd/gateway/...`
- Orquestrador (com sudo/Docker): `CI_ALLOW_TIGHT_SHED_TIMING=1 go test -tags=integration ./gateway/internal/integration_test/... -count=1`, `-tags=integration,integration_slow -run TestSC2`, `go test -tags=integration ./gateway/cmd/gatewayctl/... -count=1`
</verification>

<success_criteria>
- Stampede argon2 eliminado (1 argon2 por key em miss concorrente; 0 em blip de Redis após 1º verify)
- shed_arm/recover_seconds efetivamente aplicados no boot e em todo reload
- Testes gatewayctl corrigidos e no CI; testes de shed corrigidos ou com achado de flapping documentado (sem asserção afrouxada para esconder)
- Sem push, sem deploy
</success_criteria>

<output>
Criar `.planning/quick/260930-vkt-auth-singleflight-argon2-shed-arm-recove/260930-vkt-SUMMARY.md` com seções FATOS (comandos/saídas) e HIPÓTESES separadas, achado do SC2 com contagem por reason, e lista de testes de integração alterados pendentes de execução Docker pelo orquestrador.
</output>
