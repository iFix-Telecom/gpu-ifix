---
phase: 18
slug: tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1
status: derived
nyquist_compliant: true
wave_0_complete: false
created: 2026-07-05
---

# Phase 18 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Preenchido pelo planner na derivação (mapa por-task abaixo é stub até os PLANs existirem).

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework (gateway Go)** | `go test` (stdlib + testify, padrão do repo em `gateway/internal/**`) |
| **Framework (dashboard)** | conferir na derivação — `dashboard/` (Next.js); Phase 17 usou testes de server-action/proxy se presentes, senão validação por comportamento |
| **Config file** | `gateway/go.mod` (Go); `dashboard/package.json` (TS) |
| **Quick run command** | `cd gateway && go test ./internal/admin/... ` |
| **Full suite command** | `cd gateway && go test ./...` |
| **Estimated runtime** | ~30–90s (Go); dashboard TBD |

---

## Sampling Rate

- **After every task commit:** `cd gateway && go test ./internal/admin/...` (handlers novos)
- **After every plan wave:** `cd gateway && go test ./...`
- **Before `/gsd:verify-work`:** suite Go verde + smoke live dos endpoints admin via X-Admin-Key
- **Max feedback latency:** ~90 segundos

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 18-01-01 | 01 | 1 | TEN-UI-01, TEN-UI-02 | T-18-05 (slug dup/injection) | GET lista todos os tenants; POST cria (slug+name); slug dup → 409; slug vazio → 400 sem query | unit | `go test ./internal/admin/ -run TestTenant -count=1` | ✅ criado na task | ⬜ pending |
| 18-01-02 | 01 | 1 | TEN-UI-03, TEN-UI-04, TEN-UI-05 | T-18-04 (raw/hash leak) | create-key retorna raw 1× no JSON, NUNCA key_hash/key_lookup_hash; data_class inválida → 400; revoke idempotente; list sem hash | unit | `go test ./internal/admin/ -run TestKey -count=1` | ✅ criado na task | ⬜ pending |
| 18-01-03 | 01 | 1 | TEN-UI-01..05 | T-18-02 (endpoint sem auth) | 5 rotas montadas DENTRO do adminRouter (X-Admin-Key); build+test verdes; gofmt limpo | build+unit | `cd gateway && go build ./... && go test ./internal/admin/... -count=1` | ✅ | ⬜ pending |
| 18-02-01 | 02 | 2 | TEN-UI-07, TEN-UI-10 (env) | T-18-03, T-18-10 | gateway-admin POST+PATCH server-only; leak-guard {route.ts,gateway-admin.ts}; stack 40 → gateway consolidado | source+behavior | `bunx vitest run src/lib/gateway.test.ts -t leak` | ✅ existente | ⬜ pending |
| 18-02-02 | 02 | 2 | TEN-UI-06 | T-18-03 (admin key leak) | fetchTenants/fetchTenantKeys via proxy GET-only relativo; wrappers de leitura NÃO leem a key | unit | `bunx vitest run src/lib/gateway.test.ts -count=1` | ✅ | ⬜ pending |
| 18-03-01 | 03 | 3 | TEN-UI-08, TEN-UI-09 | T-18-01, T-18-08 | createTenant/createTenantKey/revokeKey: requireOwner 1º; operator → 0 gateway + 0 audit; 1 audit/ação | unit/behavior | `bunx vitest run src/lib/admin-actions.test.ts -t TEN-UI` | ❌ W0 (task cria testes) | ⬜ pending |
| 18-03-02 | 03 | 3 | TEN-UI-08, TEN-UI-09 | T-18-04 (raw em audit) | wrappers use-server thin; metadata de key.create sem raw; requireOwner não exportado | unit | `bunx vitest run src/lib/admin-actions.test.ts -count=1` | ❌ W0 | ⬜ pending |
| 18-04-01 | 04 | 4 | TEN-UI-10 | T-18-01 (operator vê controles) | RSC /tenants/gerenciar (getViewerRole+fetchTenantsServer); nav nova; /tenants métricas intacta | typecheck | `bunx tsc --noEmit` | ✅ | ⬜ pending |
| 18-04-02 | 04 | 4 | TEN-UI-10, TEN-UI-11 | T-18-04, T-18-09 | island: raw 1× (não persiste), revoke confirm-impacto sem type-to-confirm, data-class no fluxo de key, operator read-only | typecheck+human | `bunx tsc --noEmit` + checkpoint | ✅ | ⬜ pending |
| 18-04-03 | 04 | 4 | TEN-UI-10, TEN-UI-11 | T-18-01, T-18-04 | E2E owner (criar/gerar-raw/revogar/data-class) + operator read-only + audit sem raw | human-verify | manual UAT (dashboard live) | — | ⬜ pending |

---

## Wave 0 Requirements

- [ ] `gateway/internal/admin/tenants_test.go` — cobre TEN-UI-01..05 (create tenant, list, create key sem hash, revoke idempotente)
- [ ] Fixtures: DB de teste com tenant seed (reusar helper `freshSchema`/testdb existente do repo)
- [ ] Dashboard: confirmar se há harness de teste de server-action; se não, marcar owner-gating + leak-guard como manual-only com UAT

*Frameworks já existem (go test); sem instalação nova esperada.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Fluxo UI completo (criar tenant → gerar key → copiar raw 1× → revogar) | TEN-UI-10 | UI E2E owner-gated no dashboard live (worker-vm stack 40) | Login owner → página tenants-admin → criar → gerar key → confirmar raw mostrado 1× → revogar → confirmar sumiço |
| Operator vê read-only (sem botões de mutação) | TEN-UI-10 | Depende de sessão better-auth com role operator | Login operator → página → nenhum controle de escrita presente |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 90s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** derived 2026-07-05 (planner). Todas as tasks têm `<verify><automated>` (go test / vitest / tsc) exceto o checkpoint human-verify final (18-04-03, E2E owner/operator + audit — só observável no dashboard live, mesmo gate manual da Phase 17). Sem 3 tasks consecutivas sem automated verify. Os testes Go (18-01) e admin-actions (18-03) são criados dentro das próprias tasks (fake-queries / mocks hoisted, sem DB), padrão config_write_test.go / admin-actions.test.ts.
