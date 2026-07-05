---
phase: 18
slug: tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1
status: draft
nyquist_compliant: false
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

> Stub — o planner preenche 1 linha por task ao derivar os PLANs (IDs TEN-UI-xx).

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 18-01-01 | 01 | 1 | TEN-UI-02 | T-18-01 (slug dup / injection) | POST /admin/tenants cria tenant; 409 em slug duplicado; sem key_hash no response | unit | `go test ./internal/admin/ -run TestCreateTenant` | ❌ W0 | ⬜ pending |
| 18-01-02 | 01 | 1 | TEN-UI-04 | T-18-02 (raw key leak) | POST create-key retorna raw 1× no JSON; DB só hash; response NUNCA serializa `key_hash` | unit | `go test ./internal/admin/ -run TestCreateKey` | ❌ W0 | ⬜ pending |
| 18-01-03 | 01 | 1 | TEN-UI-05 | — | POST /admin/keys/{id}/revoke idempotente (2ª chamada = 200/no-op) | unit | `go test ./internal/admin/ -run TestRevokeKey` | ❌ W0 | ⬜ pending |
| 18-02-01 | 02 | 2 | TEN-UI-08 | T-18-03 (owner bypass) | server action nega non-owner ANTES de qualquer mutação (requireOwner 1º) | integration/behavior | dashboard test ou UAT | ❌ W0 | ⬜ pending |
| 18-02-02 | 02 | 2 | TEN-UI-07 | T-18-04 (admin key leak) | X-Admin-Key só referenciada em `route.ts` + `gateway-admin.ts`; grep no bundle client = 0 | source assertion | `grep -rL` guard | ❌ W0 | ⬜ pending |

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
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
