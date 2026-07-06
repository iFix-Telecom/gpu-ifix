---
phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1
type: verification
status: passed
verified: 2026-07-06
verifier: gsd-phase-verifier
requirements_in_scope: [TEN-UI-01, TEN-UI-02, TEN-UI-03, TEN-UI-04, TEN-UI-05, TEN-UI-06, TEN-UI-07, TEN-UI-08, TEN-UI-09, TEN-UI-10, TEN-UI-11]
requirements_deferred: [TEN-UI-12]
evidence:
  gateway_build: "go build ./... exit 0"
  gateway_tests: "go test ./internal/admin/... → ok 0.604s"
  dashboard_tests: "bunx vitest run → 78/78 passed (14 files)"
  dashboard_typecheck: "bunx tsc --noEmit → clean (exit 0)"
  leak_guard: "GATEWAY_ADMIN_KEY em exatamente {gateway-admin.ts, api/gateway/[...path]/route.ts}"
  e2e: "owner/operator flow human-approved em prod 2026-07-06 (ai-dashboard.converse-ai.app)"
---

# Phase 18 — Verification

Goal (ROADMAP §Phase 18): dar ao owner uma UI de gestão de tenants no dashboard —
listar tenants, criar tenant (slug+name), gerar/revogar API key, definir data-class —
consumindo a admin API do gateway (X-Admin-Key) via proxy server-only, eliminando o
"criar tenant hoje é só CLI". Operator read-only.

**Veredito: PASSED.** Todos os 6 must-haves confirmados contra o código (não os summaries).
Build + testes Go verdes; 78/78 testes do dashboard verdes; tsc limpo; leak-guard intacto.
TEN-UI-01..11 implementados e testados; TEN-UI-12 corretamente DEFERIDO. Uma inconsistência
menor de documentação (checkboxes TEN-UI-10/11 não marcados no REQUIREMENTS.md) — não afeta
o goal, detalhada em Gaps.

O checkpoint E2E owner/operator (18-04 Task 3) foi **aprovado por humano em produção em
2026-07-06** — os itens de human_verification estão satisfeitos; nenhum item fica pendente
de verificação humana.

---

## Must-have 1 — Gateway: 5 rotas admin sob adminRouter (X-Admin-Key) — PASS

- `gateway/cmd/gateway/main.go:1629-1636` monta as 5 rotas DENTRO do bloco
  `if px.adminVerifier != nil {` (linha 1586) → todas sob `admin.Middleware` (X-Admin-Key;
  sem key → 401 pelo middleware existente).
- Uma guarda por campo de handler (não um guard único): rotas de tenant sob
  `if px.adminTenantHandler != nil` (1629), rotas de key sob `if px.adminKeysHandler != nil` (1633).
- As 5 rotas presentes: `GET /tenants`, `POST /tenants`, `GET /tenants/{slug}/keys`,
  `POST /tenants/{slug}/keys`, `POST /keys/{id}/revoke`.
- Handlers construídos em main.go:1353-1354 (`admin.NewTenantAdminHandler` /
  `admin.NewKeysAdminHandler`) e atribuídos ao struct (1380-1381).
- **Evidência de execução:** `go build ./...` exit 0; `go test ./internal/admin/... -count=1`
  → `ok 0.604s` (inclui os 9 testes fake-queries novos + suite admin pré-existente).

## Must-have 2 — create-key nunca serializa hash; raw aparece 1× — PASS

- `keys_admin_http.go:63-71`: `createKeyResponse` struct tem campos `{id, key_prefix, tenant,
  data_class, key}` — **nenhum** campo de hash. O raw sai só em `Key: raw` (linha 215), uma vez.
- `keys_admin_http.go:199-207`: log de sucesso carrega apenas id/tenant/slug/data_class/prefix —
  o raw **nunca** é logado.
- `key_hash`/`key_lookup_hash` aparecem no arquivo apenas como campos de INPUT
  (`InsertAPIKeyParams`, 188-189) e num doc-comment — nunca numa struct de resposta.
- Teste `TestKeyCreate_ReturnsRawOnceNoHash` (keys_admin_http_test.go) asserta que o corpo
  contém `"key"` e `!Contains("key_hash") && !Contains("key_lookup_hash")`.
- `keyListItem` (53-61) também é hash-free (projeção operator-safe).

## Must-have 3 — Dashboard reads via proxy; helper server-only; leak-guard — PASS

- `gateway.ts`: `fetchTenants` = `proxyGet<TenantRow[]>("tenants")`, `fetchTenantKeys(slug)` =
  `proxyGet(...encodeURIComponent(slug)/keys)` — GET-only, via `/api/gateway`, sem admin key.
- `gateway-admin.ts`: `import "server-only"` na linha 1; `gatewayAdminMutate` (comum POST/PATCH)
  lê `GATEWAY_ADMIN_KEY` só de `process.env`; `gatewayAdminPost<T>` devolve o JSON verbatim,
  `gatewayAdminPatch` mantém `Promise<void>`, `gatewayAdminGet<T>` (add 18-04) para RSC direto.
- **Leak-guard:** `grep -rln GATEWAY_ADMIN_KEY src` (excl. testes) → EXATAMENTE
  `gateway-admin.ts` + `api/gateway/[...path]/route.ts`. Confirmado.
- **Evidência:** `bunx vitest run` → 78/78 (inclui gateway.test.ts 16 + gateway-admin.test.ts 3
  + gateway-server.test.ts 3); `bunx tsc --noEmit` limpo.

## Must-have 4 — Server actions owner-gated + audit — PASS

`admin-actions-core.ts` (`import "server-only"`, sem `"use server"`):
- `createTenantCore` (690), `createTenantKeyCore` (732), `revokeKeyCore` (773): `requireOwner`
  é a **PRIMEIRA await** em cada (699/741/781), antes de qualquer `post`.
- Validação server-side ANTES do gateway: slug `TENANT_SLUG_RE` + name não-vazio (703-710);
  `data_class ∈ {normal,sensitive}` via `KEY_DATA_CLASSES` (743); keyId não-vazio (784).
- Exatamente 1 `writeAuditLog` por ação: `tenant.create` {slug,name}, `key.create`
  {tenant,data_class,key_prefix}, `key.revoke` {key_id}.
- **Raw nunca no audit:** key.create metadata carrega `created.key_prefix`, nunca `created.key`
  (757-762); o raw é `return created` ao caller (766).
- Wrappers `admin-actions.ts` (153-176): `createTenant`/`createTenantKey`/`revokeKey` chamam
  `requireOwner()` sem args (identidade só da sessão, CR-01); `requireOwner`/`writeAuditLog`
  NÃO reexportados (CR-02).
- **Testes (admin-actions.test.ts, describe TEN-UI-08/09):** operator → `gatewayAdminPostMock`
  não chamado + `db.adminAuditLog.length === 0` (676-677); owner → 1 POST + 1 audit row;
  slug/data_class inválidos → 0 gateway (728, 776+); createTenantKey retorna RAW ao caller mas
  `Object.keys(metadata).not.toContain("key")` + `JSON.stringify(metadata).not.toContain(RAW)`
  (772-773).

## Must-have 5 — UI /tenants/gerenciar owner-aware + confirms — PASS

- `page.tsx`: RSC (`export const dynamic="force-dynamic"`), sem `"use client"`; lê
  `getViewerRole()` → `isOwner` (30-31) e `fetchTenantsServer()` (36); passa `isOwner` +
  `initialTenants` ao island; try/catch → card de erro pt-BR.
- `tenant-controls.tsx` (`"use client"`): importa createTenant/createTenantKey/revokeKey de
  `@/lib/admin-actions`.
- **data-class só no fluxo de gerar key:** o `<Select normal|sensitive>` vive em `GenerateKeyDialog`
  (578-593); o create-tenant dialog só tem slug+name. Na lista, `data_class` aparece apenas como
  badge read-only (328-331), não seletor.
- **revoke = alert-dialog com string de impacto, sem type-to-confirm:** `RevokeKeyDialog` usa
  `AlertDialog` com descrição de impacto explícita ("Qualquer app usando essa key passa a receber
  401 imediatamente…"), confirm 1-clique `variant="destructive"`, Cancelar em foco.
  `grep 'digite|type-to-confirm|confirmação por texto'` → vazio.
- **operator read-only:** todos os controles de mutação gated em `isOwner` (botões 156/235/244,
  coluna de ações 309/340, dialogs). Sem isOwner → tabela + keys read-only, zero botões.
- **sidebar:** `app-sidebar.tsx:45` item `/tenants` (métricas) intacto; `:46` novo
  `/tenants/gerenciar` "Tenants (gestão)". Rota nova não sobrescreve métricas.

## Must-have 6 — RSC lê gateway direto (gatewayAdminGet), sem self-fetch hairpin — PASS

- `gateway-server.ts:20` importa `gatewayAdminGet`; `fetchPodConfigServer` (28) e
  `fetchTenantsServer` (36) delegam a `gatewayAdminGet<T>(...)` — leitura DIRETA do gateway com
  X-Admin-Key, sem reconstruir a URL pública `/api/gateway/*`.
- Corrige o bug de UAT (`89a5972`): o hairpin RSC→URL-pública→middleware→307 /login fazia
  `res.json()` estourar em `<!DOCTYPE`. A mesma latência afetava fetchPodConfigServer (config
  page) — também corrigido.
- `gatewayAdminGet` mora em `gateway-admin.ts` (o arquivo abençoado) → GATEWAY_ADMIN_KEY continua
  em {route.ts, gateway-admin.ts}; gateway-server.ts não referencia a key (só importa a função).

---

## Rastreabilidade de requisitos

| Req | Descrição | Plan | Status | Evidência |
|-----|-----------|------|--------|-----------|
| TEN-UI-01 | GET /admin/tenants (X-Admin-Key) | 18-01 | ✅ | tenants_admin_http.go List + main.go:1630 |
| TEN-UI-02 | POST /admin/tenants (409 dup) | 18-01 | ✅ | tenants_admin_http.go Create (23505→409) + test |
| TEN-UI-03 | GET /tenants/{slug}/keys (sem hash) | 18-01 | ✅ | keys_admin_http.go List, keyListItem hash-free + test |
| TEN-UI-04 | POST /tenants/{slug}/keys (raw 1×, data_class) | 18-01 | ✅ | keys_admin_http.go Create, createKeyResponse + TestKeyCreate_ReturnsRawOnceNoHash |
| TEN-UI-05 | POST /keys/{id}/revoke (idempotente) | 18-01 | ✅ | keys_admin_http.go Revoke (WHERE status='active') + test |
| TEN-UI-06 | reads fetchTenants/fetchTenantKeys via proxy | 18-02 | ✅ | gateway.ts proxyGet + gateway.test.ts |
| TEN-UI-07 | write helper server-only + leak-guard | 18-02 | ✅ | gateway-admin.ts + leak-guard grep |
| TEN-UI-08 | actions owner-gated + validação server-side | 18-03 | ✅ | admin-actions-core.ts *Core + describe TEN-UI-08/09 |
| TEN-UI-09 | 1 audit row/ação, metadata sem raw | 18-03 | ✅ | writeAuditLog + tests (metadata sem key/RAW) |
| TEN-UI-10 | UI /tenants/gerenciar owner/operator | 18-04 | ✅ | page.tsx + tenant-controls.tsx (E2E aprovado) |
| TEN-UI-11 | confirm perigoso sem type-to-confirm | 18-04 | ✅ | RevokeKeyDialog alert-dialog impacto |
| TEN-UI-12 | set-mode/set-quota | — | ⏸️ DEFERIDO | REQUIREMENTS.md:158 marcado DEFERIDO (fora do MVP, vertical slice própria) |

Todo ID 01..11 atribuído a um plan (01-05→18-01, 06-07→18-02, 08-09→18-03, 10-11→18-04),
consistente com as frontmatter `requirements:` dos PLANs. TEN-UI-12 está explicitamente marcado
DEFERIDO no REQUIREMENTS.md (não silenciosamente descartado).

---

## Gaps

**G1 (menor, doc-hygiene, não bloqueante):** REQUIREMENTS.md linhas 156-157 mantêm TEN-UI-10 e
TEN-UI-11 como `[ ]` (não concluído), apesar de o trabalho estar completo, testado e
**aprovado E2E em prod 2026-07-06**. O ROADMAP.md:214 (item 18-04) já está `[x]`. Discrepância
puramente de sinalização — recomenda-se marcar `[x]` os dois checkboxes para evitar falso
"pendente". Não afeta o goal nem qualquer must-have.

Nenhum outro gap. Nenhuma dependência nova (`go.mod` e `package.json` inalterados nos 4 plans).
Nenhum item requer verificação humana adicional (E2E já aprovado).
