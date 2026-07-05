# Phase 18 — RESEARCH: Tenant management UI no dashboard (owner-gated)

> Consumido pelo gsd-planner. Prescritivo, não exploratório. Cada claim factual
> traz proveniência [VERIFIED: grep/file] · [CITED: docs] · [ASSUMED] + confiança.
> Investigação = codebase (Go gateway + gatewayctl + dashboard Next.js). Sem web/Context7:
> nenhum pacote externo novo entra nesta fase.

---

## Summary

**O achado que dimensiona a fase inteira:** o gateway HTTP admin API (`/admin/*`, gated
por `X-Admin-Key`) **NÃO expõe nenhum CRUD de tenant/key hoje**. Toda a gestão de
tenant/key vive apenas no `gatewayctl` (CLI), que fala **direto no Postgres** via
`gen.New(pool)` — não passa pela admin API. Logo, o padrão herdado da Phase 17
(dashboard → proxy server-only `/api/gateway` → admin API do gateway) **exige construir
handlers Go novos** no gateway (cenário **(b)** da pergunta pivô). As queries sqlc já
existem (`CreateTenant`, `ListTenants`, `InsertAPIKey`, `RevokeAPIKey`,
`ListActiveKeys*WithMeta`, `GetTenantConfig`, `UpdateTenantMode`, `UpdateTenantQuota`),
então o trabalho Go é **handler-thin sobre queries prontas** — não é schema/migration novo
(exceto talvez uma listagem enriquecida). O trabalho dashboard é 1:1 com Phase 17: um
proxy GET-only já existente + um write-helper server-only + server actions owner-gated
auditadas + a página. [VERIFIED: grep main.go, cmd/gatewayctl/*.go, db/queries/*.sql]
**Confiança: HIGH.**

**Landmine de correção resolvido:** `data_class` mora em DUAS tabelas. A **autoritativa
para roteamento/RES-08 (sensitive → nunca tier-1 externo)** é `api_keys.data_class` — é
por-KEY, setada no momento de criar a key. `tenants.data_class` existe só como coluna
defensiva do CHECK `chk_sensitive_no_peak` (Phase 4). Portanto o controle "definir
data-class" da UI pertence ao **fluxo de gerar API key**, NÃO ao tenant.
[VERIFIED: migrations 0002/0013, key.go:85-91, STATE.md] **Confiança: HIGH.**

**Correção de doc obsoleta:** CLAUDE.md diz "não há `key list` ainda — Phase 11 candidate".
**Está desatualizado** — `gatewayctl key list [--tenant]` existe e é completo desde a
Phase 11 (key.go:224-305, queries `ListActiveKeysAllWithMeta`/`ListActiveKeysByTenantWithMeta`).
[VERIFIED: key.go, auth.sql] **Confiança: HIGH.**

---

## Architectural Responsibility Map

| Camada | Papel na Phase 18 | Arquivo(s) âncora | Novo/Reuso |
|--------|-------------------|-------------------|------------|
| **Gateway admin HTTP handlers** (Go) | Expor tenant/key CRUD sob `/admin/*` gated por `X-Admin-Key`. Handlers finos sobre queries sqlc existentes. | `gateway/internal/admin/{config_read,config_write}.go` (templates), **novos `tenants_read.go`/`tenants_write.go`/`keys.go`** | **NOVO** (é o grosso do trabalho Go) |
| **sqlc queries** | CRUD de dados. **Já existem todas.** | `db/queries/admin.sql`, `db/queries/auth.sql`, `db/queries/tenants_admin.sql` | REUSO (talvez 1 query nova de listagem enriquecida) |
| **Key generation** | Gerar raw key + hash argon2 + lookup + prefix. | `internal/auth/argon2.go:46 GenerateAPIKey` | REUSO verbatim |
| **Admin middleware** | Gate `X-Admin-Key` (bcrypt). | `internal/admin/middleware.go:159 Middleware`, `main.go:1569` | REUSO |
| **Router mount** | Montar as rotas novas dentro do `adminRouter`. | `cmd/gateway/main.go:1569-1620` | EDITAR |
| **Dashboard GET proxy** | `/api/gateway/*` → `${GATEWAY_BASE_URL}/admin/*` com X-Admin-Key server-side. GET-only. | `src/app/api/gateway/[...path]/route.ts` | REUSO (já pronto, genérico) |
| **Dashboard write helper** | `X-Admin-Key` server-only para mutações. Hoje só PATCH — **generalizar p/ POST/DELETE**. | `src/lib/gateway-admin.ts` | EDITAR (pequeno) |
| **Server actions owner-gated** | `requireOwner` → validar → mutar via helper → 1 row de audit. | `src/lib/admin-actions-core.ts` + `admin-actions.ts` | NOVO (espelha `updatePodConfigCore`) |
| **Read wrappers + types** | `fetchTenants()`/`fetchTenantKeys()` GET via proxy + tipos TS espelhando o JSON Go. | `src/lib/gateway.ts` | EDITAR |
| **UI página** | Listar/criar tenant, gerar/revogar key, data-class. Owner edita / operator read-only. | **novo componente** + `src/app/(dashboard)/tenants/` (rota já existe, é a de MÉTRICAS) | NOVO |
| **Audit** | 1 row por ação admin em `admin_audit_log`. | `writeAuditLog` (admin-actions-core.ts:176) | REUSO |
| **Nav** | Link na sidebar (se rota nova). | `src/components/app-sidebar.tsx` | EDITAR |

**GOTCHA de rota:** `src/app/(dashboard)/tenants/page.tsx` **JÁ EXISTE** — é a tabela de
**métricas/custo por tenant** (Phase 7 observability, `"use client"`, polls `fetchMetrics`).
NÃO é uma página de gestão. Decisão de layout (planner): estender essa página com uma
seção owner-gated de gestão, OU criar rota irmã (ex. `/tenants/gerenciar`). Recomendação
ASSUMED: seção/sub-rota separada para não misturar o polling de métricas (client) com o
form de gestão (server actions). [VERIFIED: tenants/page.tsx head] **Confiança: HIGH (fato),
MEDIUM (recomendação de layout).**

---

## Architecture Patterns

### PADRÃO-TEMPLATE (Phase 17, espelhar 1:1)

O fluxo canônico owner-gated write, provado na Phase 17:

```
[UI client] → server action ("use server", admin-actions.ts)
   → *Core (admin-actions-core.ts, server-only, NÃO "use server")
       1. requireOwner()            ← gate server-side SEMPRE 1º (D-03/CR-01/CR-02)
       2. (refetch estado vivo p/ validar + audit-old)   ← quando aplicável
       3. validação server-side ANTES de qualquer chamada gateway (defense-in-depth)
       4. gatewayAdminPatch/Post/... (server-only, injeta X-Admin-Key)   ← única saída de mutação
       5. writeAuditLog() exatamente 1 row {action, actor, target, metadata}
       6. safeRevalidate(path)
```
[VERIFIED: admin-actions-core.ts:133-650] **Confiança: HIGH.**

Reads seguem o outro caminho (GET-only, sem key no browser):
```
[UI] → fetchX() (gateway.ts) → GET /api/gateway/<path> (route.ts, injeta X-Admin-Key) → GET /admin/<path>
```
[VERIFIED: route.ts:80-86, gateway.ts:439] **Confiança: HIGH.**

### Handler Go (espelhar `config_read.go`/`config_write.go`)

Padrão do write handler (config_write.go):
- Struct com **interface de queries isolada** (`podConfigWriteQueries`) → `*gen.Queries`
  satisfaz em prod, fake em teste conta chamadas.
- Dual constructor: `New...Handler(*gen.Queries,...)` (prod) + `new...WithQueries(fake,...)` (teste).
- Body decode → `switch` sobre **allowlist estática de campos** (sem SQL dinâmico) →
  validação → query tipada → envelope terminal.
- Envelope de erro: `httpx.WriteOpenAIError(w, status, type, code, msg)` +
  `obs.GatewayAdminRequests.WithLabelValues(route, "4xx"/"5xx"/"2xx").Inc()`.
[VERIFIED: config_write.go:53-141, 414-442] **Confiança: HIGH.**

### Mount (main.go)

Rotas novas entram no MESMO bloco `adminRouter` (já sob `admin.Middleware` X-Admin-Key):
```go
adminRouter.Method(http.MethodGet,  "/tenants",        px.adminTenantsListHandler)
adminRouter.Method(http.MethodPost, "/tenants",        px.adminTenantCreateHandler)
adminRouter.Method(http.MethodGet,  "/tenants/{slug}/keys", px.adminKeysListHandler)
adminRouter.Method(http.MethodPost, "/tenants/{slug}/keys", px.adminKeyCreateHandler)
adminRouter.Method(http.MethodPost, "/keys/{id}/revoke",     px.adminKeyRevokeHandler)
```
(nomes ilustrativos — o planner decide o REST shape). [VERIFIED: main.go:1569-1620,
padrão `adminRouter.Method(...)`] **Confiança: HIGH.**

---

## Don't Hand-Roll (reuse existente)

| NÃO reescrever | USE | Arquivo:linha |
|----------------|-----|---------------|
| Geração de key (raw+hash+lookup+prefix) | `auth.GenerateAPIKey()` | argon2.go:46 [VERIFIED] |
| Insert de key com data_class | `q.InsertAPIKey(...)` (RETURNING id/prefix/...) | admin.sql:11, key.go:85 [VERIFIED] |
| Criar tenant (slug+name, 23505=dup) | `q.CreateTenant` + trata `pgErr.Code=="23505"` | admin.sql:1, tenant.go:76-85 [VERIFIED] |
| Listar tenants (todos, inclusive sem tráfego) | `q.ListTenants` (ORDER BY created_at DESC) | admin.sql:8 [VERIFIED] |
| Listar keys sem vazar hash | `q.ListActiveKeysAllWithMeta`/`...ByTenantWithMeta` (projeção sem hash) | auth.sql:41/60, key.go:171-209 [VERIFIED] |
| Revogar key (idempotente) | `q.RevokeAPIKey` (UPDATE status='revoked' WHERE active) + checa já-revogada | admin.sql:19, key.go:111-164 [VERIFIED] |
| set data_class do tenant (sensitive+peak) | validação já em `UpdateTenantMode` path + CHECK DB | tenant.go:153, migration 0013:51 [VERIFIED] |
| set quota/mode | `q.UpdateTenantQuota`/`UpdateTenantMode` (partial via narg) | tenants_admin.sql:29/41 [VERIFIED] |
| Gate X-Admin-Key | `admin.Middleware` (já monta todo `/admin/*`) | middleware.go:159 [VERIFIED] |
| Owner gate + audit + revalidate (dashboard) | `requireOwner`/`writeAuditLog`/`safeRevalidate` | admin-actions-core.ts:133/176/662 [VERIFIED] |
| Proxy GET com X-Admin-Key | `route.ts` genérico (já cobre qualquer path GET) | route.ts:27-78 [VERIFIED] |
| Erro OpenAI envelope | `httpx.WriteOpenAIError` + `obs.GatewayAdminRequests` | config_write.go:417 [VERIFIED] |

---

## Common Pitfalls

1. **[CRÍTICO] Confundir data_class de tenant vs key.** RES-08 (sensitive → 503, nunca
   tier-1 externo) decide off **`api_keys.data_class`**. Setar só `tenants.data_class`
   NÃO muda roteamento. O controle "data-class" da UI = campo do **create-key**, não do
   tenant. [VERIFIED: STATE.md 19-04 "sensitivity keys off api_keys.data_class NOT
   tenants.data_class", migration 0013:26] **Confiança: HIGH.**

2. **[CRÍTICO] Raw key aparece UMA vez.** `GenerateAPIKey` retorna o raw; o DB guarda só
   hash. O handler POST create-key deve retornar o raw **no corpo JSON dessa única
   resposta** e a UI exibe-e-esquece (copiar agora). NUNCA logar o raw (key.go:97-98
   avisa: slog redactor não cobre). NUNCA persistir/re-exibir. [VERIFIED: key.go:79-107]
   **Confiança: HIGH.**

3. **CreateTenant não aceita data_class/quota/mode.** É só `(slug, name)`; data_class
   default 'normal'. Setar mode/quota/data-class de tenant = chamada SEPARADA
   (`UpdateTenantMode`/`UpdateTenantQuota`). Se a UI quiser "criar tenant com quota", o
   handler faz create + update em sequência (ou o planner mantém quotas/mode como
   "opcionais" pós-criação, como diz o escopo). [VERIFIED: admin.sql:1, tenant.go:56-89]
   **Confiança: HIGH.**

4. **gatewayctl NÃO é reusável do Next.js.** É um binário Go que abre pool Postgres
   próprio. Shell-out via `docker exec` do dashboard = anti-padrão (o dashboard fala HTTP
   com a admin API, nunca docker socket — invariante Phase 17). Por isso os handlers Go
   novos. [VERIFIED: key.go:61 loadAndPool, 17-CONTEXT invariante] **Confiança: HIGH.**

5. **`gateway-admin.ts` hoje só faz PATCH.** Generalizar para aceitar método
   (`gatewayAdminMutate(method, path, body)`) ou adicionar `gatewayAdminPost`. Manter o
   `import "server-only"` na linha 1 e o teste leak-guard que exige `GATEWAY_ADMIN_KEY`
   em EXATAMENTE `{route.ts, gateway-admin.ts}`. [VERIFIED: gateway-admin.ts:27-70,
   17-05-SUMMARY leak-guard] **Confiança: HIGH.**

6. **A rota `/tenants` já é a de métricas.** Não sobrescrever a página de observability.
   [VERIFIED: tenants/page.tsx] **Confiança: HIGH.**

7. **Grep-clean de doc comments.** Os acceptance gates da Phase 17 eram greps literais que
   tropeçaram em tokens dentro de comentários (ex. "restart", "os.Exit"). Se o planner
   definir gates literais, redigir comentários para não conter os tokens proibidos.
   [VERIFIED: 17-04-SUMMARY deviation #1] **Confiança: MEDIUM (depende dos gates do plano).**

8. **`ListActiveKeys*` filtra `status='active'`.** Uma UI de gestão pode querer mostrar
   keys revogadas (histórico). Se sim, precisa query nova (`ListAllKeysByTenant` sem filtro
   de status) — decisão do planner. Hoje só há a projeção active-only sem hash.
   [VERIFIED: auth.sql:41-79] **Confiança: HIGH.**

---

## Code Examples (excertos reais deste repo)

### 1. gatewayctl key create — a lógica exata a portar para handler HTTP (data_class por-key + raw once)
`gateway/cmd/gatewayctl/key.go:79-108`
```go
raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
// ...
inserted, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
    TenantID:      tenant.ID,
    KeyHash:       hash,
    KeyLookupHash: lookupHash,
    KeyPrefix:     prefix,
    DataClass:     *dataClass, // "normal" | "sensitive" — pgx encodes string → ENUM
})
// IMPORTANT: print the raw key ... ONCE. Operator must copy it now.
fmt.Printf("key=%s\nid=%s\nprefix=%s\ntenant=%s\ndata_class=%s\n",
    raw, inserted.ID.String(), prefix, *tenantSlug, *dataClass)
// NO raw key in log record
```

### 2. Query surface pronta (nada de migration nova para o CRUD básico)
`gateway/db/queries/admin.sql`
```sql
-- name: CreateTenant :one
INSERT INTO ai_gateway.tenants (slug, name) VALUES ($1, $2) RETURNING id, slug, name, created_at, updated_at;
-- name: ListTenants :many
SELECT id, slug, name, created_at, updated_at FROM ai_gateway.tenants ORDER BY created_at DESC;
-- name: InsertAPIKey :one
INSERT INTO ai_gateway.api_keys (tenant_id, key_hash, key_lookup_hash, key_prefix, data_class)
VALUES ($1,$2,$3,$4,$5) RETURNING id, tenant_id, key_hash, key_lookup_hash, key_prefix, status, data_class, created_at, revoked_at, last_used_at;
-- name: RevokeAPIKey :exec
UPDATE ai_gateway.api_keys SET status='revoked', revoked_at=NOW() WHERE id=$1 AND status='active';
```
[VERIFIED] — **atenção: `InsertAPIKey ... RETURNING key_hash` inclui o hash; o handler NÃO
deve serializar `key_hash` na resposta JSON.** Só {id, prefix, data_class, raw}.

### 3. Write handler Go template (allowlist estática + envelope + métrica)
`gateway/internal/admin/config_write.go:53-141, 414-442` — copiar a forma:
interface de queries isolada, dual constructor, `switch` sobre campos allowlisted,
`httpx.WriteOpenAIError` + `obs.GatewayAdminRequests.WithLabelValues(route, class).Inc()`.

### 4. Owner-gated server action com audit (dashboard) — espelhar
`dashboard/src/lib/admin-actions-core.ts:523-592` (`updatePodConfigCore`):
```
requireOwner FIRST → (refetch p/ old) → validar → gatewayAdminPatch → writeAuditLog{field,old,new} → revalidate
```
Para Phase 18: `createTenantCore` / `createTenantKeyCore` / `revokeKeyCore` seguem o mesmo
esqueleto, trocando PATCH por POST e a validação por (slug regex, data_class ∈
{normal,sensitive}). O audit metadata **nunca** inclui o raw key (privacy invariant,
admin-actions-core.ts:176 doc).

### 5. Write helper server-only (generalizar de PATCH → método)
`dashboard/src/lib/gateway-admin.ts:27-70` — mudar `method: "PATCH"` para parâmetro; manter
`import "server-only"` (linha 1) e o header `X-Admin-Key` lido de `process.env`.

### 6. Proxy GET genérico (já cobre GET /admin/tenants sem mudança)
`dashboard/src/app/api/gateway/[...path]/route.ts:27-86` — encaminha qualquer path GET,
injeta X-Admin-Key, `cache: "no-store"`. `fetchTenants()` novo em gateway.ts só chama
`/api/gateway/tenants`. [VERIFIED]

---

## Validation Architecture (Nyquist)

Amostrar validação em cada fronteira; sem gaps entre camadas.

| Fronteira | O que validar | Onde | Evidência-tipo |
|-----------|---------------|------|----------------|
| **Trust boundary (gateway HTTP)** | X-Admin-Key presente/válido | `admin.Middleware` (já) | 401 sem/errada key [VERIFIED middleware.go] |
| **Input (create tenant)** | slug url-safe não-vazio, name não-vazio, dup=23505→409/1 | handler Go (espelhar tenant.go:63-85) | teste: slug vazio→400, dup→409 |
| **Input (create key)** | data_class ∈ {normal,sensitive} | handler Go (key.go:56-59) | teste: data_class inválida→400 |
| **Input (revoke)** | id UUID válido; idempotência já-revogada | handler Go (key.go:122,153) | teste: UUID inválido→400, revoke 2×→ok |
| **Owner gate (dashboard)** | role==owner ANTES de qualquer gateway call | `requireOwner` 1º em cada *Core | teste: operator→throw, ZERO gateway calls, ZERO audit |
| **Segredo não vaza** | resposta create-key não traz `key_hash`; raw só 1×; nunca em log/audit | handler Go + writeAuditLog metadata | teste: JSON sem hash; audit metadata sem raw |
| **Admin-key não vaza p/ browser** | `GATEWAY_ADMIN_KEY` só em {route.ts, gateway-admin.ts} | leak-guard test (já existe) | teste filesystem-scan [VERIFIED 17-05] |
| **Audit durável** | exatamente 1 row por mutação {action, actor, target, metadata} | writeAuditLog | teste: 1 row, action correta |

**Nyquist gaps a cobrir no plano:** teste do handler Go com fake-queries contando chamadas
(padrão config_write_test.go), + teste da server action com `requireOwner` injetado
(padrão admin-actions.test.ts). Nenhuma camada sem teste unitário. Live-UAT (criar tenant
real + key + revoke contra o gateway consolidado no worker-vm) = checkpoint autonomous:false
(padrão herdado). [VERIFIED: config_write_test.go, admin-actions.test.ts existem]
**Confiança: HIGH.**

---

## Security Domain (V4 Access Control + V5 Input Validation)

| Ameaça | Mitigação | Proveniência |
|--------|-----------|--------------|
| **T-18-01 Operator cria/revoga tenant/key** | `requireOwner` server-side 1º em TODA action; UI-hide é cosmético (mesmo D-03 da Phase 13/17) | admin-actions-core.ts:133 [VERIFIED] |
| **T-18-02 Endpoint admin sem auth** | Rotas montadas dentro de `adminRouter`/`admin.Middleware` (X-Admin-Key bcrypt); sem key→401 | main.go:1570, middleware.go:159 [VERIFIED] |
| **T-18-03 Admin key vaza p/ browser** | Key só server-side em `{route.ts, gateway-admin.ts}` (`import "server-only"`); leak-guard test | gateway-admin.ts:1, 17-05-SUMMARY [VERIFIED] |
| **T-18-04 Raw API key exposta/logada** | Raw retornado 1× no JSON, exibido-e-esquecido; DB só hash; NUNCA log/audit metadata | key.go:97-107 [VERIFIED] |
| **T-18-05 SQL injection via campo** | Queries sqlc parametrizadas; sem SQL string-built; data_class é ENUM (pgx valida) | admin.sql, key.go:56 [VERIFIED] |
| **T-18-06 LGPD: sensitive vira peak** | `UpdateTenantMode` rejeita sensitive+peak antes do SQL + CHECK DB `chk_sensitive_no_peak` + fail-fast boot | tenant.go:153, migration 0013:51 [VERIFIED] |
| **T-18-07 data_class errada não protege** | Controle data-class na key (autoritativa RES-08), validado ∈{normal,sensitive} | migration 0002/0013, STATE 19-04 [VERIFIED] |
| **T-18-08 Mutação sem trilha** | 1 `admin_audit_log` row por ação (`tenant.create`/`key.create`/`key.revoke`), metadata sem segredo | writeAuditLog [VERIFIED] |
| **T-18-09 Confirm perigoso (revoke key ativa)** | Confirm de um clique com string de impacto (padrão POD-CFG-12: sem type-to-confirm) | 17 D-04 [CITED ROADMAP] |

**V5 Input Validation:** validação server-side ANTES de qualquer gateway call (defense-in-depth
com a validação do próprio handler Go) — padrão exato do `updatePodConfigCore`. slug:
url-safe regex; data_class: enum whitelist; name: non-empty. **Confiança: HIGH.**

---

## Assumptions Log

- **[ASSUMED]** REST shape: `GET/POST /admin/tenants`, `GET/POST /admin/tenants/{slug}/keys`,
  `POST /admin/keys/{id}/revoke`. O planner ajusta; as queries suportam qualquer shape.
  Confiança MEDIUM (é convenção, não fato).
- **[ASSUMED]** Layout: seção/sub-rota de gestão separada da `/tenants` de métricas (não
  misturar client-polling com server-action form). MEDIUM.
- **[ASSUMED]** Escopo "quotas/mode opcionais" = pós-criação via `UpdateTenantQuota`/`Mode`,
  não no create atômico. Alinha com o CreateTenant só-slug+name. MEDIUM.
- **[ASSUMED]** Listar keys = só active (query existente). Se histórico (revogadas) for
  requisito, +1 query nova. MEDIUM.
- **[ASSUMED]** Sem migration nova necessária para o CRUD básico (todas queries existem).
  Só entra migration se o planner quiser uma listagem enriquecida (ex. count de keys por
  tenant num JOIN). HIGH de que o básico não precisa; MEDIUM sobre a listagem enriquecida.

---

## Open Questions (para discuss/plan)

1. **Página nova vs estender `/tenants` (métricas)?** Recomendação: sub-rota/seção separada.
2. **REST shape exato** dos endpoints admin (nested keys sob tenant vs flat).
3. **Listar keys revogadas** (histórico) ou só ativas? Muda se precisa query nova.
4. **Criar tenant já com data-class/quota/mode** (create+update em sequência no handler)
   ou fluxo em 2 passos (cria tenant → depois gera key com data-class → depois seta quota)?
   O escopo diz "quotas/mode opcionais" → sugere 2 passos.
5. **set-mode/set-quota entram nesta fase** ou só create/list/key-gen/revoke/data-class?
   O escopo lista "quotas/mode opcionais" — o planner decide MVP vs completo.
6. **Confirm em revoke** — string de impacto específica (padrão POD-CFG-12).
7. **Onde roda em prod:** dashboard = worker-vm stack 40 (Portainer), `AI_GATEWAY_PG_DSN`→
   `bd_ai_gateway` (dados de tenant/key vivem aqui), login store = `bd_ai_dashboard_prod`.
   O gateway admin API alvo = worker-vm (consolidado Phase 19). Confirmar `GATEWAY_BASE_URL`/
   `GATEWAY_ADMIN_KEY` do stack 40 apontam pro gateway consolidado. [VERIFIED: STATE 19-03,
   CLAUDE.md topologia] **Confiança: HIGH sobre onde os dados vivem.**

---

## `<phase_requirements>`

ROADMAP lista "Requirements: TBD". Proposta de família nova **TEN-UI-xx** (derivar em
plan-phase, como POD-CFG-xx foi na Phase 17). Não reaproveitar TEN-01..09 (já Complete,
são backend). Sugestão:

```
TEN-UI-01  Gateway GET /admin/tenants (lista todos os tenants, inclusive sem tráfego) via X-Admin-Key
TEN-UI-02  Gateway POST /admin/tenants (create slug+name; 409 em slug dup) via X-Admin-Key
TEN-UI-03  Gateway GET /admin/tenants/{slug}/keys (lista keys sem key_hash/raw) via X-Admin-Key
TEN-UI-04  Gateway POST /admin/tenants/{slug}/keys (gera key; data_class∈{normal,sensitive}; retorna raw 1×, sem hash) via X-Admin-Key
TEN-UI-05  Gateway POST /admin/keys/{id}/revoke (idempotente) via X-Admin-Key
TEN-UI-06  Dashboard read wrappers fetchTenants/fetchTenantKeys via proxy GET-only (sem admin key no browser)
TEN-UI-07  Dashboard write helper server-only generalizado (POST/PATCH) — leak-guard: key só em {route.ts, gateway-admin.ts}
TEN-UI-08  Server actions owner-gated createTenant/createTenantKey/revokeKey (requireOwner 1º, validação server-side)
TEN-UI-09  Audit: 1 admin_audit_log row por ação (tenant.create/key.create/key.revoke), metadata sem segredo/raw
TEN-UI-10  UI: listar tenants + criar tenant + gerar key (mostra raw 1×) + revogar key + seletor data-class; owner-edita/operator read-only
TEN-UI-11  Confirm perigoso (revoke key ativa) com string de impacto — sem type-to-confirm
TEN-UI-12  (OPCIONAL) set-mode/set-quota do tenant via gateway PATCH + action owner-gated auditada
```
Confiança: MEDIUM (IDs/escopo são proposta; o planner ratifica na derivação).

---

## Sources

- `gateway/cmd/gatewayctl/tenant.go`, `key.go` [VERIFIED] — lógica CLI a portar
- `gateway/db/queries/{admin,auth,tenants_admin}.sql` [VERIFIED] — queries prontas
- `gateway/internal/admin/{config_write,config_read,errors,middleware}.go` [VERIFIED] — templates de handler
- `gateway/internal/auth/argon2.go:46` [VERIFIED] — GenerateAPIKey
- `gateway/cmd/gateway/main.go:1304-1620` [VERIFIED] — mounts admin, prova ausência de rotas tenant/key
- `gateway/db/migrations/{0002,0013}` [VERIFIED] — data_class em api_keys (autoritativa) + tenants (defensiva)
- `dashboard/src/lib/{gateway-admin,admin-actions-core,gateway}.ts` [VERIFIED] — padrão dashboard
- `dashboard/src/app/api/gateway/[...path]/route.ts` [VERIFIED] — proxy GET
- `dashboard/src/app/(dashboard)/tenants/page.tsx` [VERIFIED] — rota já ocupada (métricas)
- `.planning/phases/17-*/17-04-SUMMARY.md`, `17-05-SUMMARY.md` [CITED] — THE template
- `.planning/STATE.md` (19-03/19-04) [CITED] — topologia worker-vm, data_class off api_keys
- `.planning/REQUIREMENTS.md`, `ROADMAP.md` [CITED] — Phase 13/17/18 scope
- `~/.claude/CLAUDE.md` [CITED] — topologia ai-gateway, gatewayctl invocation, correção "key list existe"

---

## Metadata

- **Phase:** 18 — Tenant management UI no dashboard (owner-gated)
- **Método:** codebase investigation (grep/read Go + Next.js); zero web/Context7 (sem pacote novo)
- **Pergunta pivô resolvida:** cenário **(b)** — handlers admin HTTP de tenant/key **precisam
  ser construídos** no gateway (não existem); queries sqlc já prontas → trabalho Go é fino.
- **Landmine resolvido:** data_class autoritativa = `api_keys` (por-key), não `tenants`.
- **Confiança global:** HIGH nos fatos de código; MEDIUM nas decisões de shape/layout (planner).
- **Depende de:** Phase 13 (owner-gate + admin_audit_log + server-action pattern), Phase 17
  (proxy + gateway-admin.ts + handler templates), Phase 19 (gateway consolidado worker-vm = alvo prod).
```
