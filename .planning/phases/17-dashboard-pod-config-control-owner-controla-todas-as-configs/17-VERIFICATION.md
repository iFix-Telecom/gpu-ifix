---
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
verified: 2026-06-30T20:05:00Z
status: passed
human_verification_outcome: "approved 2026-07-01 — 3/3 live UAT items passed on dashboard-dev (hot-reload e2e, live panel, owner-vs-operator RBAC). 5 integration bugs found + fixed during UAT (see 17-HUMAN-UAT.md): RSC cookie, server-action relative URL, /operacao budget source, sidebar logout/nav, /settings route-group."
score: 15/15 must-haves verified
overrides_applied: 0
human_verification:
  - test: "Owner edita blocklist no dashboard (append known-bad host id) → próximo provision do reconciler prod pula o host SEM restart do gateway"
    expected: "Em segundos, o pod_config_changed NOTIFY recarrega o loader; a próxima provision/tick seleciona ofertas usando a nova blocklist; o painel ao vivo mostra shutdown_reason / lifecycle trail. Nenhum SSH+sed, nenhum restart."
    why_human: "End-to-end hot-reload exige gateway prod ao vivo + ciclo de provisioning Vast real. Cobertura unitária/integração prova loader+reconciler+NOTIFY isoladamente, mas o ciclo completo (UI → DB → NOTIFY → reconciler → seleção de oferta Vast) só é observável num ambiente rodando (VALIDATION.md §Gate manual UAT)."
  - test: "Painel ao vivo /operacao/config renderiza o FSM + event trail e atualiza a cada 10s; estados skeleton/erro/StaleIndicator aparecem corretamente"
    expected: "Painel mostra fsm_state, leader, trail (started_at → first_health_pass → drain → ended) com tiers de fsm.ts; StaleIndicator marca dados parados; poll de 10s reflete transições"
    why_human: "Renderização visual + comportamento de polling em tempo real não verificáveis por grep/teste estático"
  - test: "Owner vê afordâncias de edição (lápis) por campo; operator vê os MESMOS valores read-only (sem controles de edição) nas 4 superfícies"
    expected: "Login como owner → pencils + bounds editáveis; login como operator → idênticos valores, zero controles de edição; tentativa de edição via action ainda barrada server-side"
    why_human: "Diferenciação visual de papéis e fluxo de UI exigem sessão autenticada real (owner e operator) no dashboard"
---

# Phase 17: Dashboard Pod Config Control Verification Report

**Phase Goal:** Owner controla TODAS as configs hot do pod primário pelo dashboard — config movida de env-at-boot → tabela DB `pod_config`, gateway lê do DB; 16 hot fields hot-reloaded pelo reconciler via NOTIFY (sem restart); 19 estruturais read-only; dashboard fala só com a admin API do gateway; painel ao vivo de lifecycle; owner-only edita, operator read-only, bounds validation, confirm em mudanças perigosas, audit de toda mudança.
**Verified:** 2026-06-30T20:05:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Config movida env→DB: migration 0031 cria `ai_gateway.pod_config` single-row (16 hot + bounds) | ✓ VERIFIED | `0031_create_pod_config.sql`: boolean PK `CHECK (id = TRUE)`, 16 colunas hot + 10 pares min/max NOT NULL; build sqlc/gen exit 0 |
| 2 | `pod_config_changed` NOTIFY trigger dispara só em mudança real (IS DISTINCT FROM) | ✓ VERIFIED | função `notify_pod_config_changed()` + 2 triggers split (insert/delete sempre; update gated em 35 colunas `IS DISTINCT FROM OLD`, `pg_trigger_depth()=0`) |
| 3 | Seed env→DB idempotente no 1º boot; env permanece fallback | ✓ VERIFIED | main.go:929 `SeedPodConfig(podConfigSeedParams(cfg))`; query `ON CONFLICT (id) DO NOTHING`; integration test assert 2º seed no-op |
| 4 | `podconfig.Loader` hot-reload (atomic snapshot + last-good-on-error) | ✓ VERIFIED | loader.go: `atomic.Pointer[snapshot]`, Refresh em erro `PodConfigReloadTotal("error").Inc()` + `return` SEM swap (mantém last-good); Load() lock-free |
| 5 | LISTEN/NOTIFY recarrega via pgxlisten, sobrevive a erro transitório | ✓ VERIFIED | listen.go: `listener.Handle("pod_config_changed", ...)` chama `loader.Refresh`, retorna nil p/ manter listener vivo; 5s backoff |
| 6 | Reconciler lê 16 hot fields do `podCfg.Load()` (não r.cfg); estruturais ficam em r.cfg | ✓ VERIFIED | reconciler.go:1217 `r.podCfg.Load()` p/ offer selection; schedule re-parse de snapshot (1247); budget.go lê MonthlyBudgetBRL do snapshot; estruturais (GPU/imagens/llama) permanecem r.cfg |
| 7 | `PATCH /admin/primary/config` (X-Admin-Key) → UpdatePodConfigField/Bound; SEM restart/estrutural | ✓ VERIFIED | config_write.go: allowlist estático field→typed-query (zero SQL dinâmico), 16 config + 20 bound cases; montado em adminRouter PATCH; grep restart/self-restart = vazio |
| 8 | `GET /admin/primary/lifecycle` (FSM + event trail da lifecycle aberta) | ✓ VERIFIED | lifecycle.go `GetOpenPrimaryLifecycle`; montado adminRouter GET `/primary/lifecycle`; testes admin passam |
| 9 | `GET /admin/primary/config` → 16 hot + 9/10 bounds typed JSON (lê pod_config, não boot env) | ✓ VERIFIED | config_read.go: `GetPodConfig` → `{config, bounds}` typed; montado adminRouter GET `/primary/config` |
| 10 | Server action owner-gated (`requireOwner` server-side) + validação vs bound corrente | ✓ VERIFIED | admin-actions-core.ts: `requireOwner()` session-only; `updatePodConfigCore` refetch live config → range-validate vs bound → patch; CR-01 fix confirmado |
| 11 | Audit dashboard-side não-forjável (action+metadata {field,old,new}); sem dual-write gateway | ✓ VERIFIED | `writeAuditLog` só no core não-RPC; `db` removido da superfície pública (CR-02 fix); old vem do refetch; testes invariante passam |
| 12 | Confirm simples 1-clique com string de impacto específica em ações perigosas | ✓ VERIFIED | pod-config-controls.tsx `dangerFor`: cap-down ("Reduzir o teto…"), schedule-narrow ("Estreitar a janela…drena o pod AGORA"), Disabled, dias vazios, allowlist restritiva; AlertDialog 1-clique, sem type-to-confirm |
| 13 | Display read-only dos 19 campos estruturais | ✓ VERIFIED | page.tsx `STRUCTURAL_FIELDS` = 19 entradas (timezone/GPUs/imagens/pesos+SHA/llama args), em-dash placeholders, "Alterado via redeploy/env — não editável aqui" (D-01 by-design) |
| 14 | Painel ao vivo poll 10s de /admin/primary/lifecycle, reusa fsm.ts + StaleIndicator | ✓ VERIFIED | pod-config-live-panel.tsx: `queryFn: fetchPrimaryLifecycle`, `refetchInterval: 10000`, StaleIndicator importado |
| 15 | Bounds owner-editáveis seedados dos defaults da RESEARCH | ✓ VERIFIED | podConfigSeedParams: cap 0.10-1.50, coldstart 300-5400, port-bind 30-600, cooldown 60-1800, budget 0-100000, hours 0-23, grace 0-1800, lead 0-7200 — match exato RESEARCH |

**Score:** 15/15 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `gateway/db/migrations/0031_create_pod_config.sql` | single-row table + bounds + NOTIFY trigger | ✓ VERIFIED | 145 linhas; CHECK(id=TRUE); 2 triggers; IS DISTINCT FROM em 35 colunas |
| `gateway/internal/podconfig/{loader,listen,types}.go` | snapshot loader + LISTEN reload | ✓ VERIFIED | atomic.Pointer; last-good-on-error; pgxlisten pod_config_changed |
| `gateway/internal/primary/reconciler.go` | snapshot-backed offer selection | ✓ VERIFIED | `r.podCfg.Load()` no provision start; estruturais em r.cfg |
| `gateway/internal/admin/{config_read,config_write,lifecycle}.go` | 2 read + 1 write handler | ✓ VERIFIED | allowlist estático; bounds + cross-field validation; X-Admin-Key gated |
| `gateway/cmd/gateway/main.go` | loader wiring + ListenAndReload + boot seed + mounts | ✓ VERIFIED | NewLoader, SeedPodConfig, ListenAndReload, 3 mounts em adminRouter |
| `dashboard/src/lib/admin-actions.ts` | thin use-server wrappers session-only | ✓ VERIFIED | wrappers chamam `requireOwner()` sem args; sem actor/auth/db na assinatura pública |
| `dashboard/src/lib/admin-actions-core.ts` | non-RPC server-only core | ✓ VERIFIED | `import "server-only"`, SEM "use server"; requireOwner/writeAuditLog só aqui |
| `dashboard/src/lib/{gateway,gateway-admin,gateway-server}.ts` | fetch wrappers + key helper | ✓ VERIFIED | fetchPodConfig/fetchPrimaryLifecycle via proxy GET; gatewayAdminPatch server-only c/ X-Admin-Key |
| `dashboard/src/components/pod-config-{controls,live-panel}.tsx` | editor + bounds + confirms + live panel | ✓ VERIFIED | isOwner gate cosmético; dangerFor; updatePodConfig; refetchInterval 10s |
| `dashboard/src/app/(dashboard)/operacao/config/page.tsx` | RSC 4 superfícies | ✓ VERIFIED | fetchPodConfigServer; isOwner; 19 estruturais; PodConfigControls + PodConfigLivePanel |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| admin-actions.ts wrappers | requireOwner session | `requireOwner()` sem args | ✓ WIRED | Identidade só da sessão; actor forjado ignorado (CR-01 fix) |
| updatePodConfigCore | gateway PATCH | `gatewayAdminPatch("primary/config")` server-only | ✓ WIRED | NÃO passa pelo proxy GET-only; X-Admin-Key server-side |
| reconciler | podconfig snapshot | `r.podCfg.Load()` | ✓ WIRED | hot path lock-free |
| main.go | ListenAndReload | goroutine | ✓ WIRED | LISTEN pod_config_changed |
| config_write/read | pod_config | UpdatePodConfigField/Bound / GetPodConfig sqlc | ✓ WIRED | typed queries, sem SQL dinâmico |
| live-panel | fetchPrimaryLifecycle | React Query refetchInterval 10000 | ✓ WIRED | |

### CR-01 / CR-02 Authz Fix Verification (foco crítico do pedido)

| Invariante | Status | Evidência |
|-----------|--------|-----------|
| CR-01: owner-gate não-burlável por actor forjado | ✓ VERIFIED | Wrappers `"use server"` chamam `requireOwner()` SEM argumento → força path da sessão (`headers()` + `getSession`). Assinatura pública é `{field, value}` apenas — sem seam `actor`. Teste `(CR-01) updatePodConfig action exposes NO actor seam`: `updatePodConfig({actor:{role:"owner"},...})` → rejeitado UNAUTHENTICATED, gateway nunca chamado. PASS. |
| CR-02 (suppress): `db` removido da superfície pública | ✓ VERIFIED | Wrapper não aceita `db`; core recebe `args.db` undefined em prod → `isMemBag(undefined)=false` → `dbConfigured()=true` → insert real drizzle. Cliente não consegue injetar bag. |
| CR-02 (forge): writeAuditLog não-RPC | ✓ VERIFIED | `writeAuditLog` e `requireOwner` vivem em `admin-actions-core.ts` (sem "use server") — não exportados de admin-actions.ts. Teste `(CR-02) requireOwner and writeAuditLog NOT exported from use server module`: `pub.requireOwner`/`pub.writeAuditLog` undefined. PASS. |
| Leak guard / server-only | ✓ VERIFIED | core e gateway-admin têm `import "server-only"`; X-Admin-Key confinado a route.ts + gateway-admin.ts |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Gateway compila | `go build ./...` | exit 0 | ✓ PASS |
| Gateway unit tests (podconfig/admin/primary) | `go test ./internal/{podconfig,admin,primary}/...` | ok (todos) | ✓ PASS |
| Dashboard tests (admin-actions + gateway) | `npx vitest run admin-actions.test.ts gateway.test.ts` | 26 passed | ✓ PASS |
| CR-01/CR-02 invariant tests | incluídos acima | 3 testes hardening PASS | ✓ PASS |
| gofmt limpo nos arquivos da fase | `gofmt -l internal/podconfig internal/admin/*.go` | vazio | ✓ PASS |
| Sem endpoint de restart | `grep -rn gateway/restart\|selfRestart` | vazio | ✓ PASS |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| POD-CFG-01 | 17-01 | Migration 0031 single-row + NOTIFY | ✓ SATISFIED | Truth 1, 2 |
| POD-CFG-02 | 17-01/03 | Seed env→DB idempotente, env fallback | ✓ SATISFIED | Truth 3 |
| POD-CFG-03 | 17-02/03 | podconfig.Loader hot-reload | ✓ SATISFIED | Truth 4, 5 |
| POD-CFG-04 | 17-03 | Reconciler lê snapshot, estruturais r.cfg | ✓ SATISFIED | Truth 6 |
| POD-CFG-05 | 17-01 | Storage bounds owner-editável seedado | ✓ SATISFIED | Truth 15 |
| POD-CFG-06 | 17-04 | PATCH /admin/primary/config | ✓ SATISFIED | Truth 7 |
| POD-CFG-07 | 17-04/05 | GET /admin/primary/lifecycle | ✓ SATISFIED | Truth 8 |
| POD-CFG-08 | 17-06 | Editor 16 hot owner-edit/operator RO | ✓ SATISFIED | Truth 12 + isOwner gate (visual → human) |
| POD-CFG-09 | 17-06 | Editor bounds owner-edit/operator RO | ✓ SATISFIED | BoundsCard isOwner |
| POD-CFG-10 | 17-05 | Server action owner-gated + validação vs bound | ✓ SATISFIED | Truth 10, CR-01 fix |
| POD-CFG-11 | 17-05 | Audit não-forjável {field,old,new}, sem dual-write | ✓ SATISFIED | Truth 11, CR-02 fix |
| POD-CFG-12 | 17-06 | Confirm 1-clique impacto específico | ✓ SATISFIED | Truth 12 |
| POD-CFG-13 | 17-06 | Display read-only 19 estruturais | ✓ SATISFIED | Truth 13 |
| POD-CFG-14 | 17-06 | Painel ao vivo poll 10s | ✓ SATISFIED | Truth 14 (visual → human) |
| POD-CFG-15 | 17-04/05/06 | GET /admin/primary/config + fetchPodConfig | ✓ SATISFIED | Truth 9 |

Todos os 15 IDs declarados nos 6 plans e presentes em REQUIREMENTS.md. Nenhum órfão.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `dashboard/src/app/api/gateway/[...path]/route.ts` | 80 | Proxy catch-all GET de `/admin/*` p/ qualquer autenticado, com X-Admin-Key anexado, sem allowlist de path nem owner-check (WR-01 do review, NÃO endereçado) | ⚠️ Warning | Operator pode LER todo endpoint admin observability via proxy. Blast radius read-only (PATCH não exportado no proxy). Predates a fase; standing concern documentado no review. |
| `dashboard/src/components/pod-config-controls.tsx` | ~691 | Confirm "Salvar sem nenhum dia?" p/ schedule_days vazio que o gateway SEMPRE rejeita (400) (WR-02 do review, NÃO endereçado) | ⚠️ Warning | UX enganosa: confirmar sempre produz erro genérico. Não bloqueia o goal (owner seta dias válidos normalmente). |

Nenhum debt-marker (TBD/FIXME/XXX) nos arquivos da fase. Nenhum stub: todos os artefatos têm implementação substantiva e wired.

### Human Verification Required

Veja frontmatter `human_verification`. Resumo:
1. **Hot-reload E2E em prod** — owner edita blocklist → reconciler prod pula host na próxima provision SEM restart. Requer gateway prod + ciclo Vast ao vivo (VALIDATION.md §Gate manual UAT).
2. **Painel ao vivo visual** — FSM + trail + poll 10s + StaleIndicator/skeleton/erro.
3. **Gate visual owner vs operator** — pencils p/ owner, idêntico read-only p/ operator nas 4 superfícies.

### Gaps Summary

Nenhum gap bloqueante. Os 15 must-haves estão implementados e verificados no nível de código: migration + loader + reconciler refactor + 3 endpoints + server actions + UI, com builds verdes (gateway exit 0), 26 testes de dashboard e a suíte Go passando, e gofmt limpo. O fix de authz CR-01/CR-02 (commits c6978d2/01445d6/e075592) HOLDS: o owner-gate deriva identidade só da sessão (actor forjado é ignorado — provado por teste de invariante) e o audit não é forjável/suprimível por cliente (writeAuditLog/requireOwner não-RPC no core server-only; `db` fora da superfície pública). Duas warnings pré-existentes/menores do review (WR-01 proxy passthrough, WR-02 confirm de dias-vazios morto) permanecem não-endereçadas — são WARNINGS, não blockers, e a resolução do review focou só nos CR blockers.

Status **human_needed** porque o comportamento load-bearing da fase — hot-reload end-to-end em produção sem restart — e a renderização visual (painel ao vivo, gate owner/operator) só são confirmáveis num ambiente rodando, conforme o gate de UAT manual já declarado em VALIDATION.md.

---

_Verified: 2026-06-30T20:05:00Z_
_Verifier: Claude (gsd-verifier)_
