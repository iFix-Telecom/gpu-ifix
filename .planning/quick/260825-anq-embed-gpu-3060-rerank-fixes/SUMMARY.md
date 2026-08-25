---
phase: quick-260825-anq
plan: 01
subsystem: gateway
tags: [embed, rerank, billing, prober, migration, tier0]
requires:
  - "migration 0035 (rerank role) no HEAD"
  - "NewRerankProxy variádico (interceptors ...ProxyResponseInterceptor)"
provides:
  - "migration 0036: embed-gpu tier-0 (UPSTREAM_EMBED_GPU_URL), local-embed tier-1 prio 10, openai-embed tier-2 fora da cascata"
  - "roster tier0Roles com rerank (rerank-gpu passa a ser probado)"
  - "billing de /v1/rerank: route='rerank', tokens_in do usage.prompt_tokens do Infinity, cost_external_brl=0"
  - "isSelfHostedUpstream gate (local-*/rerank-gpu/rerank-cpu/embed-gpu) — sem WARN price-missing por request"
affects:
  - "deploy: orquestrador precisa seguir a ordem pod → env+imagem → migrate (loader skipa row sem url_env → 503 embed)"
tech-stack:
  added: []
  patterns: ["cascade tier via UNIQUE(role,tier,tier_priority)", "ritual Down(N) dos testes de migration", "gate self-hosted no costExternal"]
key-files:
  created:
    - gateway/db/migrations/0036_embed_gpu_tier0.sql
  modified:
    - gateway/internal/integration_test/migration_0026_test.go
    - gateway/internal/integration_test/migration_0029_test.go
    - gateway/internal/config/config.go
    - gateway/cmd/gateway/main.go
    - gateway/internal/upstreams/loader.go
    - gateway/internal/upstreams/loader_test.go
    - gateway/internal/upstreams/health_test.go
    - gateway/internal/proxy/interceptor_usage.go
    - gateway/internal/proxy/interceptor_usage_test.go
    - gateway/internal/billing/flusher.go
    - gateway/internal/db/migrate_test.go
decisions:
  - "openai-embed demovido p/ tier 2 (fora da cascata) em vez de deletado — dims 1536≠1024 = corrupção silenciosa; row/env/proxy retidos p/ re-enable manual"
  - "applyAudioEmbedUsage SEM branch rerank — tokens vêm do parse top-level de usage no Close (godoc registra)"
  - "costPhantom inalterado (D-B4); só o costExternal ganhou o gate isSelfHostedUpstream"
metrics:
  duration: "~29min"
  completed: "2026-08-25T11:22Z"
---

# Quick 260825-anq: Embed na 3060 + fixes prober/billing rerank — Summary

**One-liner:** migration 0036 promove embed-gpu (pod Vast 3060, Infinity bge-m3 1024) a tier-0 do role embed com local-embed CPU como fallback e openai-embed (1536 dims) fora da cascata; de carona, rerank entra no roster do prober e /v1/rerank passa a gerar billing_event (route=rerank, tokens do Infinity, custo externo 0).

## Commits

| Task | Commit | Mensagem |
|------|--------|----------|
| 1 | `8018566` | feat(gateway): migration 0036 — embed-gpu tier-0 na 3060, local-embed vira fallback, openai-embed fora da cascata |
| 2 | `2513532` | feat(gateway): wiring embed-gpu — UPSTREAM_EMBED_GPU_URL + proxy tier-0 no role embed |
| 3 | `5fd8938` | fix(gateway): adiciona rerank ao roster tier0Roles — rerank-gpu nunca era probado (LAST_PROBE '-') |
| 4 | `d4ede77` | fix(gateway): billing de rerank — usageInterceptor no proxy, route 'rerank', custo externo 0 p/ self-hosted |
| 5 | `fe8128a` | test(quick-260825-anq): adiciona 0036 ao pin de migrations embedadas (TestEmbedFS_HasAllMigrations) |

Commitado direto no `develop`; **NÃO pushado** (push é do orquestrador).

## O que foi feito

- **0036 (Task 1):** Up = openai-embed 1→2, local-embed 0→1 prio 10, INSERT embed-gpu (embed,0,0,`UPSTREAM_EMBED_GPU_URL`) ON CONFLICT DO NOTHING; Down simétrico em ordem reversa com WARNING de operador (espelho 0035). Ritual Down(N): 0026 `Down(8)→Down(9)` e `Down(10)→Down(11)`, 0029 `Down(7)→Down(8)` (0028 ancorado, não bumpado), comentários narrativos + t.Fatal atualizados.
- **Wiring (Task 2):** `UpstreamEmbedGPUURL` opcional no config; `embedRoleProxies["embed-gpu"]` via `NewEmbeddingsProxy` passthrough com `usageInterceptor` (guard env != ""); `upstream_embed_gpu` no log de boot.
- **Prober (Task 3):** `tier0Roles = {llm,stt,tts,embed,rerank}`. Testes TDD (RED confirmado antes do fix): `ResolveTier0Roles` inclui rerank-gpu; health rerank tier-0 open + cpu closed = "degraded" 200; ambos open = "failed" 503.
- **Billing (Task 4):** `usageInterceptor` nos 2 call sites de `NewRerankProxy`; case `/v1/rerank → "rerank"` em `routeToBillingRoute` (corrige stamp do dispatcher E fallback); `isSelfHostedUpstream()` substitui o prefixo `local-` no gate de `costExternal` (mata WARN "price missing" + `gateway_prices_missing.Inc()` por request); comentário Route do flusher ganha "rerank". Testes TDD (RED confirmado): route table, gate table, Close com body real do Infinity (`prompt_tokens:29` → TokensIn=29/TokensOut=0, slot deletado BL-02), no-op de `applyAudioEmbedUsage` p/ rerank.

## Gates (Task 5) — resultado

| Gate | Resultado |
|------|-----------|
| 1. `gofmt -l .` (cwd gateway/) | **VAZIO** — OK |
| 2. `go test ./... -count=1` | **VERDE** (28 pacotes ok) |
| 3. `sudo env ... go test -tags integration ./internal/integration_test/ -run Migration -count=1` | **VERDE** (0019/0026x2/0028x4/0029x6 PASS, 0036 aplicada no full march) |

Sweep estendido (`-tags integration ./...`): 9 falhas, **TODAS pré-existentes** — provado rodando os mesmos testes num worktree no commit base `2c7c676` (falham identicamente sem as mudanças deste plano):
- gatewayctl: `TestModelAliasGet_ReturnsSpecificRow` (asserta chaves `Alias`/`UpstreamName` mas o CLI emite `alias`/`upstream_name` — bug do próprio teste), `TestRunPrimaryLifecyclesIntegration_FetchesFromDB`, `TestRunPrimaryLifecycles_RespectsLimitFlag`
- integration_test (shed/DCGM/SC): `TestSensitiveSaturated503`, `TestTier1UnavailableShedded503`, `TestDCGMFailOpen`, `TestSC1_*`, `TestSC3_*`, `TestSC4_*` — bate com a memória `gateway-integration-tests-not-in-executor-check` (precisam de DeviceReport=cuda + fixture :9100)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] TestEmbedFS_HasAllMigrations pinava 35 migrations**
- **Found during:** Task 5 (gate `go test ./...`)
- **Issue:** `gateway/internal/db/migrate_test.go` pina a lista completa do embed.FS; 0036 não estava no ritual do plano
- **Fix:** adicionada `0036_embed_gpu_tier0.sql` à lista `want`
- **Files modified:** `gateway/internal/db/migrate_test.go`
- **Commit:** `fe8128a`

### Process deviations

- **1 commit por task (instrução do orquestrador) em vez de commits RED/GREEN separados** nas tasks tdd=true — o ciclo TDD foi respeitado na execução (testes escritos primeiro, RED verificado nas Tasks 3 e 4 antes do fix), mas squashado num commit atômico por task. Na Task 1 o RED não foi rodado isolado (2 runs Docker completos só p/ ver o vermelho); Up/Down/idempotência validados no run verde.

## Deferred Issues

- `TestChatProxy_SSEStreamingFlushesPerChunk` flaky sob carga paralela (wall-clock; passa 30x isolado) — pré-existente, intocado; detalhes em `deferred-items.md`.
- Testes de integration pré-existentes vermelhos listados acima (gatewayctl x3 + shed/DCGM/SC x6) — fora do escopo, sem relação com o diff.

## Known Stubs

Nenhum — sem placeholders/stubs introduzidos.

## Threat Flags

Nenhum surface novo fora do threat model do plano: `UPSTREAM_EMBED_GPU_URL` é env opcional server-side (mesmo padrão das envs rerank); nenhum endpoint/auth path novo.

## Deploy (orquestrador — hazard de ordem)

O loader **skipa** rows sem url_env setada: rodar `migrate up` (0036) antes da env `UPSTREAM_EMBED_GPU_URL` chegar no stack 38 deixa o role embed sem tier-0 → **503 em /v1/embeddings**. Sequência obrigatória: pod (Infinity multi-model `--served-model-name bge-m3` exato) → env+imagem juntos no Portainer → `gatewayctl migrate up`. Detalhes/validação E2E/rollback: seção `<deploy_steps>` do PLAN.md.

## Self-Check: PASSED

- `gateway/db/migrations/0036_embed_gpu_tier0.sql` existe
- 5 commits presentes no develop (8018566, 2513532, 5fd8938, d4ede77, fe8128a)
- Diff `2c7c676..HEAD` restrito ao escopo do plano (+ migrate_test.go do deviation)
- `rerank.go`, `dispatcher.go`, `probe.go`, `deploy/embed/docker-compose.yml` intocados
- Greps de sanidade: roster (`loader.go:289`), case `/v1/rerank` (`interceptor_usage.go:429`), usageInterceptor nos 2 call sites rerank (`main.go:832,840`)
