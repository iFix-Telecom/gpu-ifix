---
phase: 05-load-shedding-saturation-aware-routing
plan: 07
subsystem: ops
tags: [go, cli, gatewayctl, shed, jsonb, redis, postgres, sqlc]

# Dependency graph
requires:
  - phase: 05-load-shedding-saturation-aware-routing
    provides: "redisx.{WriteShedForce,DeleteShedForce,AllShedStateKeys,ReadShedState,GetShedForce}, sqlc UpdateTenantShedLimits, JSONB shed_* fields on upstreams.circuit_config"
provides:
  - "gatewayctl shed-state subcomando (leitura diagnóstica do mirror Redis)"
  - "gatewayctl shed-force {on|off|clear} subcomando (override operacional com TTL ≤ 1h)"
  - "gatewayctl thresholds set subcomando (JSONB merge em upstreams.circuit_config)"
  - "gatewayctl tenant set-shed-limits subcomando (partial UPDATE de caps por tenant + priority_tier)"
affects: [05-08, 06-*, runbook-incidents, ops-tooling]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Subcomando CLI plain-Go com flag.NewFlagSet(ContinueOnError) + exit codes 0/1/2 padronizados"
    - "JSONB merge in-Go (read row -> unmarshal map -> overlay -> marshal -> UPDATE) preservando campos não tocados — herdado de Phase 3 runUpstreamsUpdate"
    - "Partial UPDATE via sqlc.narg + pgtype.{Int4,Text}{Valid: true} para campos opcionais — herdado de Phase 4 runTenantSetQuota"
    - "Range validation pre-DB (operator typo guard): caps em [1, 1000] para inflight per-tenant, [1, 1_000_000] MiB para VRAM, mínimo 5s para arm/recover"
    - "loadAndRedis helper (companion de loadAndPool): config.Load + redisx.NewClient com defer Close"

key-files:
  created:
    - gateway/cmd/gatewayctl/shed.go
    - gateway/cmd/gatewayctl/shed_test.go
    - gateway/cmd/gatewayctl/thresholds.go
    - gateway/cmd/gatewayctl/thresholds_test.go
    - gateway/cmd/gatewayctl/tenants_shed.go
    - gateway/cmd/gatewayctl/tenants_shed_test.go
  modified:
    - gateway/cmd/gatewayctl/main.go
    - gateway/cmd/gatewayctl/tenant.go

key-decisions:
  - "Integration tests round-trip (Redis + Postgres testcontainers) ficam para Plan 05-08 (integration_test/); este plano cobre apenas unit tests de flag validation + range checks pre-DB. Razão: gatewayctl/upstreams_test.go já é //go:build integration e usa setupContainers em TestMain; replicar essa infra inteira para outro arquivo no mesmo package geraria duplicação. O verbo integration de 05-08 vai consumir tanto os subcomandos novos quanto o harness existente."
  - "shed-force TTL ceiling (1h) é enforced server-side no redisx.WriteShedForce (já presente desde Plan 05-05) — o CLI passa a duration sem clamp, deixa o erro propagar. Isso garante que qualquer caller (CLI, futuro admin API HTTP) obedece a mesma policy T-05-09."
  - "VRAM cap de 1_000_000 MiB (= 1 PiB) no thresholds set é typo guard apenas (T-05-13). Valores baixos (ex: 100 MiB) são aceitos intencionalmente — operador pode querer trigger agressivo em ambiente de teste. Phase 10 pode adicionar --confirm para extremos."
  - "JSONB merge usa read-modify-write em Go ao invés de jsonb_set em SQL. Razão: o pattern já está consolidado em runUpstreamsUpdate (Phase 3), evita SQL exotic functions, e mantém validação no Go layer onde os erros são mais descritivos."

patterns-established:
  - "Subcomandos do gatewayctl que tocam Redis devem usar loadAndRedis (helper criado em shed.go) — companion de loadAndPool. Manter o defer Close()."
  - "Tests de validação de flag em package main (sem build tag) — round-trip semantics em //go:build integration. Pattern já existente é confirmado: tenant_test.go faz isso para parseWindowHours."

requirements-completed: [LSH-04, LSH-05]

# Metrics
duration: 5min
completed: 2026-05-11
---

# Phase 5 Plan 07: gatewayctl Operational Surface for Shed Subsystem Summary

**Quatro subcomandos novos no gatewayctl unificado para diagnóstico (shed-state), override emergencial (shed-force), tune de thresholds runtime (thresholds set) e ajuste de caps fairness por tenant (tenant set-shed-limits) — todos consumindo a API de Wave 2 sem alterar a chain de produção.**

## Performance

- **Duration:** ~5 min (3 ciclos TDD: 6 commits RED+GREEN)
- **Started:** 2026-05-11T19:29:08-03:00
- **Completed:** 2026-05-11T19:33:31-03:00
- **Tasks:** 3
- **Files modified:** 8 (6 novos + 2 editados)

## Accomplishments

- **`gatewayctl shed-state`** — read-only diagnostic via SCAN `gw:shed:*` + overlay `gw:shed:force:*`. Output em table (default tabwriter) ou JSON. Filtro `--upstream` opcional para zoom em um único upstream. Colunas: UPSTREAM, STATE, SINCE_UNIX, REASON, INFLIGHT, P95_MS, VRAM_MIB, FORCE, TTL_S (9 colunas, supera o requisito de 6).
- **`gatewayctl shed-force {on|off|clear}`** — override operacional com TTL bounded ≤ 1h (enforced server-side por `redisx.WriteShedForce`). TTL default 300s; aceita qualquer `time.ParseDuration` válido. `clear` faz DEL imediato sem TTL.
- **`gatewayctl thresholds set`** — JSONB merge em `upstreams.circuit_config` preservando campos pré-existentes (`failures`, `cooldown_s`). 5 flags mapeiam para chaves shed_*: `--inflight` → `shed_inflight_max`, `--p95-ms` → `shed_p95_ms`, `--vram-mib` → `shed_vram_used_mib` (MiB, não bytes — RESEARCH Pitfall 1), `--arm-s` → `shed_arm_seconds`, `--recover-s` → `shed_recover_seconds`. NOTIFY upstreams_changed dispara hot-reload em <2s (SC-3 budget).
- **`gatewayctl tenant set-shed-limits`** — partial UPDATE em `ai_gateway.tenants` via sqlc `UpdateTenantShedLimits`. 4 flags: `--llm`, `--stt`, `--embed` (todos em [1, 1000]), `--tier` (S|A|B). Sentinel -1 / string vazia preservam valor existente. NOTIFY tenants_changed (trigger estendido pela migration 0016) dispara hot-reload.

## Task Commits

Cada task foi commitada atomicamente em ciclo TDD (RED → GREEN):

1. **Task 7.1: shed-state + shed-force**
   - RED: `792f102` — test(05-07): add failing tests for gatewayctl shed-state/shed-force flag validation
   - GREEN: `d0bbdb1` — feat(05-07): implement gatewayctl shed-state and shed-force subcommands

2. **Task 7.2: thresholds set (JSONB merge)**
   - RED: `b7a6aa3` — test(05-07): add failing tests for gatewayctl thresholds set flag validation
   - GREEN: `577beaf` — feat(05-07): implement gatewayctl thresholds set with JSONB merge

3. **Task 7.3: tenant set-shed-limits + dispatcher wiring**
   - RED: `4c117d3` — test(05-07): add failing tests for gatewayctl tenant set-shed-limits
   - GREEN: `76ec4fa` — feat(05-07): implement tenant set-shed-limits + wire shed/thresholds dispatchers

## Files Created/Modified

### Criados (6)
- `gateway/cmd/gatewayctl/shed.go` — `runShedState` (SCAN+HGETALL+overlay) e `runShedForce` (Write/Delete), helper `loadAndRedis`. 236 linhas.
- `gateway/cmd/gatewayctl/shed_test.go` — 5 unit tests cobrindo invalid action, missing args, missing --upstream, TTL parse error, unknown format. 75 linhas.
- `gateway/cmd/gatewayctl/thresholds.go` — `runThresholds` dispatcher + `runThresholdsSet` com JSONB merge in-Go. 225 linhas.
- `gateway/cmd/gatewayctl/thresholds_test.go` — 9 unit tests cobrindo missing --upstream, no-flags-is-error, todos os range checks. 98 linhas.
- `gateway/cmd/gatewayctl/tenants_shed.go` — `runTenantSetShedLimits` com partial UPDATE via sqlc. 134 linhas.
- `gateway/cmd/gatewayctl/tenants_shed_test.go` — 8 unit tests cobrindo missing --slug, no-flags, invalid tier, ranges per flag, e teste positivo que confirma que tier válido passa validação. 107 linhas.

### Editados (2)
- `gateway/cmd/gatewayctl/main.go` — usage help atualizado com 3 novos comandos, switch dispatcher adicionado 3 cases (`shed-state`, `shed-force`, `thresholds`). +9 linhas.
- `gateway/cmd/gatewayctl/tenant.go` — usage do tenant atualizada, switch dispatcher adicionado case `set-shed-limits`. +5 linhas.

**Total:** 890 linhas adicionadas, 2 linhas removidas.

## Decisions Made

- **Pattern reuse over invention:** seguiu integralmente o pattern map de 05-PATTERNS.md (runUpstreamsUpdate para JSONB merge, runTenantSetQuota para partial UPDATE). Zero código novo "do zero" — todas as decisões estruturais herdadas de Phase 3 / 4.
- **Sem nova dependência:** não foi necessário adicionar `miniredis` (sugestão do plano) — a infra de testes integration do gatewayctl/upstreams_test.go já provisiona Redis via testcontainers; integration tests round-trip ficam para Plan 05-08.
- **Defesa em camadas no TTL:** o CLI deixa o `time.ParseDuration` validar formato, o servidor `redisx.WriteShedForce` valida a janela `(0, 1h]`. Resultado: erro de TTL malformado retorna exit 2 (usage), erro de TTL > 1h retorna exit 1 (server policy). Operador sabe a diferença pela mensagem.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Plano usava `mustRedisClient` / `mustPool` que não existem no codebase**
- **Found during:** Task 7.1 (planejamento da impl de shed.go)
- **Issue:** A spec do plano referenciava `mustRedisClient(log)` e `mustPool(log)` como helpers pré-existentes ("Pattern: mustRedisClient() + mustPool() já existem"). Verificado via grep: nenhum dos dois existe no diretório `cmd/gatewayctl/`. O único helper compartilhado é `loadAndPool` (com naming convention `loadAnd*`, não `must*`).
- **Fix:** Criado `loadAndRedis(ctx, log) (*redis.Client, error)` em `shed.go` seguindo exatamente o pattern de `loadAndPool` (em main.go:83). Os subcomandos retornam exit 1 se falhar a conexão. Uso direto de `q := gen.New(pool)` para chamadas DB ao invés de `mustPool`.
- **Files modified:** `gateway/cmd/gatewayctl/shed.go` (loadAndRedis helper)
- **Verification:** `go build ./...` e `go vet ./gateway/...` limpos; smoke test confirmou que erros de DSN/Redis surfaceiam com exit code 1 (não panic).
- **Committed in:** `d0bbdb1`

**2. [Rule 2 - Missing Critical] Tests do plano sugeriam `miniredis` dep — não adicionada**
- **Found during:** Task 7.1 (planejamento dos tests)
- **Issue:** A spec sugeria usar `github.com/alicebob/miniredis/v2` para tests do shed-force round-trip. Verificado via `grep miniredis go.mod`: a dep não existe e adicionar uma dep de teste cross-package por causa de um único arquivo é maior overhead do que o ganho. O pattern existente do gatewayctl é placeholder unit tests + `//go:build integration` para round-trip.
- **Fix:** Manteve tests unitários focados em flag validation + range checks (que não precisam de Redis/DB). Round-trip integration deferido para Plan 05-08 (que já tem o harness testcontainers em integration_test/).
- **Files modified:** N/A (decisão de scope)
- **Verification:** Todos os 22 unit tests adicionados passam com `-race -count=1` em 6.4s. Sem mudança em go.mod / go.sum.
- **Committed in:** N/A (não é um fix, é uma decisão de scope documentada via deferred-items)

**3. [Rule 2 - Missing Critical] Validações extras de range não previstas explicitamente no plano**
- **Found during:** Task 7.2, 7.3 (implementação dos range checks)
- **Issue:** O plano listou validações como "arm_s >= 5, recover_s >= 5, vram_mib ∈ [1, 1_000_000]" mas não cobriu `--inflight=0` nem `--p95-ms=0` (zero == "always trip"; operator typo guard essencial).
- **Fix:** Adicionado check `< 1` para `--inflight` e `--p95-ms` em thresholds.go com mensagem explicativa ("zero would always trip the FSM"). Mesma defesa aplicada a `--llm/--stt/--embed=0` em tenants_shed.go (com mensagem "must be in [1, 1000]").
- **Files modified:** `gateway/cmd/gatewayctl/thresholds.go`, `gateway/cmd/gatewayctl/tenants_shed.go`
- **Verification:** Tests `TestRunThresholdsSet_RejectsInflightZero`, `TestRunThresholdsSet_RejectsP95Zero`, `TestRunTenantSetShedLimits_RejectsLLMZero`, `TestRunTenantSetShedLimits_RejectsSTTOutOfRange`, `TestRunTenantSetShedLimits_RejectsEmbedOutOfRange` cobrem esses paths.
- **Committed in:** `577beaf`, `76ec4fa`

---

**Total deviations:** 3 auto-fixed (1 blocking, 1 missing critical scope decision, 1 missing critical defensive coverage)
**Impact on plan:** Todas as deviations endurecem o contrato sem expandir scope. Zero quebra do envelope (apenas os 7 arquivos listados foram criados/editados).

## Issues Encountered

- **Base mismatch ao spawn:** o worktree foi criado em HEAD `d26f1ac` (branch Phase 2), enquanto a base esperada era `21e62606` (Phase 5 Wave 3). Resetado com `git reset --hard 21e62606` per protocolo do prompt. Após reset, todos os commits saem na cadeia correta. Tempo perdido: ~30s.
- **`go` não no PATH default:** ao primeiro `go build`, comando não encontrado — Go está em `~/.local/go/bin/`. Resolvido exportando PATH no primeiro bash call de cada operação build/test. Não impactou commits.

## User Setup Required

Nenhum — todos os subcomandos são consumidos via `docker exec ifix-ai-gateway /gatewayctl ...` per CLAUDE.md (Plan 02-08 já entregou o binário no container). Sem mudança em env vars: `AI_GATEWAY_PG_DSN` + `AI_GATEWAY_REDIS_ADDR` (já existentes) cobrem todos os novos subcomandos.

## Runbook Operacional

Exemplos copy-paste friendly para o runbook de incidentes:

### Diagnóstico
```bash
# Estado atual do FSM em todos os upstreams (formato table, default):
docker exec ifix-ai-gateway /gatewayctl shed-state

# Mesmo, em JSON para parsing:
docker exec ifix-ai-gateway /gatewayctl shed-state --format json

# Foco em um único upstream:
docker exec ifix-ai-gateway /gatewayctl shed-state --upstream local-llm
```

### Override emergencial — desligar shedding temporariamente
```bash
# Forçar FSM=off no local-llm por 10 minutos (TTL max = 1h):
docker exec ifix-ai-gateway /gatewayctl shed-force off --upstream local-llm --ttl 10m

# Limpar override (retomar evaluation normal):
docker exec ifix-ai-gateway /gatewayctl shed-force clear --upstream local-llm
```

### Override emergencial — forçar shed ON (drenar tráfego)
```bash
# Forçar FSM=on no local-llm por 5 minutos:
docker exec ifix-ai-gateway /gatewayctl shed-force on --upstream local-llm --ttl 5m
```

### Tune de thresholds runtime (hot-reload via NOTIFY <2s)
```bash
# Ajustar todos os 5 thresholds do local-llm:
docker exec ifix-ai-gateway /gatewayctl thresholds set \
  --upstream local-llm \
  --inflight 8 --p95-ms 2000 --vram-mib 21504 \
  --arm-s 30 --recover-s 60

# Ajustar apenas P95 (preserva os outros campos do JSONB):
docker exec ifix-ai-gateway /gatewayctl thresholds set --upstream local-llm --p95-ms 2500
```

### Ajuste de caps por tenant
```bash
# Subir cap LLM do tenant converseai para 8 + promover a priority tier S:
docker exec ifix-ai-gateway /gatewayctl tenant set-shed-limits \
  --slug converseai --llm 8 --tier S

# Reduzir cap STT temporariamente (só STT, deixa LLM/embed/tier intocados):
docker exec ifix-ai-gateway /gatewayctl tenant set-shed-limits --slug converseai --stt 1
```

## Threat Flags

Nenhum threat flag novo — os subcomandos consomem APIs já cobertas pelo threat model de 05-PLAN (T-05-09 mitigado server-side, T-05-13 mitigado por range validation, T-05-14 aceito conforme dispostivos do plano).

## Next Phase Readiness

- **Plan 05-08 (integration_test):** consome todos os 4 subcomandos via `exec.Command` em testes round-trip Redis+Postgres. O harness testcontainers já existente em `gateway/cmd/gatewayctl/upstreams_test.go` é o template direto.
- **Phase 6+:** o operator surface está completo. Futuras evoluções de auth/RBAC no gatewayctl (T-05-14 — aceito por enquanto) ficam para Phase 10 conforme decidido.

## Self-Check: PASSED

Verificação dos artefatos declarados:
- `gateway/cmd/gatewayctl/shed.go` — FOUND
- `gateway/cmd/gatewayctl/shed_test.go` — FOUND
- `gateway/cmd/gatewayctl/thresholds.go` — FOUND
- `gateway/cmd/gatewayctl/thresholds_test.go` — FOUND
- `gateway/cmd/gatewayctl/tenants_shed.go` — FOUND
- `gateway/cmd/gatewayctl/tenants_shed_test.go` — FOUND
- `gateway/cmd/gatewayctl/main.go` — modified (8 grep hits em main.go + tenant.go)
- `gateway/cmd/gatewayctl/tenant.go` — modified

Verificação dos commits:
- `792f102` (RED 7.1) — FOUND
- `d0bbdb1` (GREEN 7.1) — FOUND
- `b7a6aa3` (RED 7.2) — FOUND
- `577beaf` (GREEN 7.2) — FOUND
- `4c117d3` (RED 7.3) — FOUND
- `76ec4fa` (GREEN 7.3) — FOUND

Verificação de gates (build/vet/test):
- `go build ./...` — clean
- `go vet ./gateway/...` — clean
- `go test -race -count=1 ./gateway/cmd/gatewayctl/...` — PASS (6.4s)

## TDD Gate Compliance

Todos os 3 tasks seguiram o ciclo TDD RED → GREEN:

| Task | RED commit | GREEN commit | Status |
|------|------------|--------------|--------|
| 7.1 shed-state/shed-force | `792f102` test | `d0bbdb1` feat | OK |
| 7.2 thresholds set | `b7a6aa3` test | `577beaf` feat | OK |
| 7.3 tenant set-shed-limits + wiring | `4c117d3` test | `76ec4fa` feat | OK |

REFACTOR phase não executado em nenhum task — código GREEN já estava limpo (sem code smells detectados, sem duplicação cross-arquivo). Refactor sem reason é overhead.

---
*Phase: 05-load-shedding-saturation-aware-routing*
*Plan: 07 (Wave 4 — operator CLI surface)*
*Completed: 2026-05-11*
