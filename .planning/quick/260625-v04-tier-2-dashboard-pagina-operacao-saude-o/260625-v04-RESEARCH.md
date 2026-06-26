# Quick Task 260625-v04: Tier 2 Dashboard "Operação" (backend) — Research

**Researched:** 2026-06-25
**Domain:** Go gateway `/admin` endpoint + reuse of existing primary/breaker/billing data sources
**Confidence:** HIGH (all claims verified against source at file:line)
**Scope:** BACKEND only — endpoint design + data-source reuse + route registration. UI deferred to planner.

## Summary

Todas as fontes de dados que o painel "Operação" precisa JÁ EXISTEM e são lidas in-process — não há query nova obrigatória, exceto (opcionalmente) uma agregação de custo. O padrão de handler `/admin/*` está cristalizado em `usage.go` + `metrics.go`: struct com interface de queries isolada, construtor duplo (`NewXHandler` prod + `newXHandlerWithQueries` teste), envelope de erro OpenAI, contador Prometheus por branch. A rota se registra em UMA linha no sub-router chi gated por `admin.Middleware` (X-Admin-Key bcrypt).

**Recomendação primária:** UM endpoint agregado `GET /admin/operations` que retorna `{fsm, schedule, lifecycles[], breakers[], vast_cost{today,month,budget}}`. O dashboard faz 1 fetch e renderiza tudo. Reusa: `breaker.Set.EffectiveStateSnapshot()` (in-process, per-upstream, force-override-aware), `gen.ListPrimaryLifecycles` (lifecycles + custo), `primary.ParseScheduleEnv(cfg)` + `ScheduleRule.NextTransition` (schedule), `primary.Reconciler` FSM state (precisa getter exportado novo OU ler mirror Redis `gw:primary:state`), e `cfg.MonthlyPrimaryBudgetBRL`.

---

## 1. Fontes de dados a reusar (cada uma com query/função exata)

### Lifecycles + custo Vast
- **Query:** `gen.ListPrimaryLifecycles(ctx, ListPrimaryLifecyclesParams{StartedAt, Limit})` — `internal/db/gen/primary_lifecycles.sql.go:140`. SQL: `WHERE started_at >= $1 ORDER BY started_at DESC LIMIT $2`.
- **Row:** `ListPrimaryLifecyclesRow` (`primary_lifecycles.sql.go:122`): `ID int64`, `StartedAt time.Time`, `DrainStartedAt/EndedAt pgtype.Timestamptz`, `TriggerReason string`, `VastOfferID/VastInstanceID pgtype.Int8`, `AcceptedDph/TotalCostBrl pgtype.Numeric`, `ShutdownReason/LeaderReplica pgtype.Text`.
- **Como o CLI chama:** `runPrimaryLifecyclesWithPool` → `gen.New(pool)` → `ListPrimaryLifecycles` — `cmd/gatewayctl/primary.go:438-443`.
- **Custo real Vast:** coluna `total_cost_brl` (preenchida só no fechamento — ver Pitfall 1). Fórmula de custo: `accepted_dph × hours_active × USDToBRLRate`, `hours_active = NOW() - first_health_pass_at` — `internal/primary/reconciler.go:1026-1045` (`calculatePrimaryCostBRL`).
- **Agregação hoje/mês:** NÃO existe query SUM. `[VERIFIED: grep — só ListPrimaryLifecycles/GetOpen/Insert/Update/Mark/Close em primary_lifecycles.sql.go]`. Agregar em Go somando `TotalCostBrl` das rows fechadas dentro da janela (start-of-month em America/Sao_Paulo) + accrual ao-vivo da row aberta.

### Breaker states (per-upstream)
- **Fonte recomendada (in-process, dashboard-grade):** `breakerSet.EffectiveStateSnapshot() map[string]string` — `internal/breaker/breaker.go:300`. Valores: `"closed" | "half-open" | "open" | "forced-open"`. Doc da própria função diz textualmente: *"use it for any caller that decides routing or reports operational state (dashboard, /v1/health/upstreams...)"* — `breaker.go:286-291`. Force-override honrado, refresh debounced 1s.
- **`breakerSet` está em escopo no wiring de handlers:** construído em `cmd/gateway/main.go:305`, ainda usado em `:1244` (`upstreams.NewHealthHandler(loader, breakerSet, log)`). Logo basta passá-lo ao novo handler.
- **NÃO usar o caminho do gatewayctl** (`lookupBreakerRowState`, `breaker.go` CLI): ele lê Redis mirror `gw:breaker:{name}` porque o CLI roda FORA do processo (`cmd/gatewayctl/breaker.go:302-312` explica). O handler roda DENTRO → snapshot in-process é mais fresco e sem round-trip.

### FSM state + schedule
- **Emerg FSM state** já está em `/admin/metrics` via `fsmStateString(h.fsm)` → `emerg.FSM.State().String()` — `internal/admin/metrics.go:136,163-168`. Fonte: `*emerg.FSM` injetado em `NewMetricsHandler` (`main.go:1231`).
- **Primary FSM state** (o que o painel "Operação" quer) vive na `primary.Reconciler` via `deps.FSM` (`*primary.FSM`). `primary.FSM.State()` é leitura atômica lockless — `internal/primary/fsm.go:153-155`; `State.String()` → `"asleep"|"provisioning"|"ready"|"draining"|"destroying"` — `fsm.go:95-109`.
  - **GAP:** `primary.Reconciler` NÃO expõe getter público de FSM state hoje (só `IsLeader()`, `ReplicaID()`, `activeLifecycleSnapshot()` privado — `reconciler.go:1719,1726,1789`). `primaryFSM`/`primaryRule` são locais (`:=`) dentro do bloco `if cfg.VastAIAPIKey != ""` (`main.go:897,910`) — NÃO hoisted, logo indisponíveis no wiring de handler (linha 1223+).
  - **2 opções (ver §4 Pitfall A):** (a) adicionar getter exportado `func (r *Reconciler) Snapshot()` retornando fsm-state + active lifecycle/instance IDs + pod URLs e passar `primaryReconciler` (JÁ hoisted, `main.go:893`); ou (b) ler o mirror Redis `gw:primary:state` Hash (`redisx.PrimaryStateKey()` campos `state,lifecycle_id,pod_url,pod_instance_id,entered_at` — `internal/redisx/primary.go:114-128`). Recomendo (a): in-process, sem round-trip, sem nil-handling de mirror vazio.
- **Schedule:** `primary.ParseScheduleEnv(cfg) (ScheduleRule, error)` — `internal/primary/schedule.go:103`. Campos: `Timezone *time.Location`, `UpHour/DownHour int`, `Days map[time.Weekday]bool`, `GraceRampDownS int`, `ProvisionLeadS int`, `Disabled bool` (`schedule.go:63-94`). É pura (env→struct), pode ser chamada DENTRO do handler sem hoist.
  - **Current state + next transition:** `rule.NextTransition(now) (time.Time, kind string)` — `schedule.go:262` (kind `"up"|"down"|""`). `rule.ShouldBeProvisioned(now) bool` — `schedule.go:212`. Padrão de uso já existe em `cmd/gatewayctl/primary.go:359-366`.
- **monthly_budget_brl:** `cfg.MonthlyPrimaryBudgetBRL` (env `MONTHLY_PRIMARY_BUDGET_BRL`, default 800.0) — `internal/config/config.go:244,510`. (NÃO é `MonthlyEmergencyBudgetBRL` — esse é o budget de emergência, default 200.0, `config.go:162`.)

---

## 2. Desenho do endpoint — RECOMENDAÇÃO: 1 agregado `GET /admin/operations`

**Justificativa:** o dashboard renderiza uma página única "Operação" com todos os painéis de uma vez → 1 fetch agregado é mais simples (menos round-trips, menos estados de loading, consistência temporal entre painéis). Vários endpoints pequenos só fariam sentido se painéis tivessem cadências de polling diferentes — não é o caso. Espelha a decisão já tomada em `/admin/metrics` (que agrega tenants+inflight+fsm num response — `metrics.go:44-49`).

**JSON response struct proposto** (espelhando as fontes, tipos já convertidos de pgtype):

```go
// OperationsResponse — GET /admin/operations (painel "Operação").
type OperationsResponse struct {
    FSM       FSMSection        `json:"fsm"`
    Schedule  ScheduleSection   `json:"schedule"`
    Lifecycles []LifecycleRow   `json:"lifecycles"`
    Breakers  []BreakerRow      `json:"breakers"`
    VastCost  VastCostSection   `json:"vast_cost"`
}

type FSMSection struct {
    PrimaryState  string `json:"primary_state"`  // asleep|provisioning|ready|draining|destroying|unknown
    EmergState    string `json:"emerg_state"`    // reuse fsmStateString(emergFSM); "unknown" se Vast off
    ActiveLifecycleID int64 `json:"active_lifecycle_id"` // 0 = nenhum
    ActiveInstanceID  int64 `json:"active_instance_id"`  // 0 = nenhum
    IsLeader      bool   `json:"is_leader"`
}

type ScheduleSection struct {
    Timezone        string `json:"timezone"`         // America/Sao_Paulo
    UpHour          int    `json:"up_hour"`
    DownHour        int    `json:"down_hour"`
    Days            []string `json:"days"`           // ["mon","tue",...]
    ProvisionLeadS  int    `json:"provision_lead_seconds"`
    GraceRampDownS  int    `json:"grace_ramp_down_seconds"`
    Disabled        bool   `json:"disabled"`
    ShouldBeProvisioned bool `json:"should_be_provisioned_now"`
    NextTransitionAt   string `json:"next_transition_at"`   // RFC3339; "" se nenhuma
    NextTransitionKind string `json:"next_transition_kind"` // up|down|""
}

type LifecycleRow struct {
    ID             int64   `json:"id"`
    StartedAt      string  `json:"started_at"`       // RFC3339
    DrainStartedAt *string `json:"drain_started_at"` // null se aberta/sem drain
    EndedAt        *string `json:"ended_at"`         // null = AINDA RODANDO
    TriggerReason  string  `json:"trigger_reason"`
    VastInstanceID *int64  `json:"vast_instance_id"`
    AcceptedDPH    *float64 `json:"accepted_dph"`
    CostBRL        *float64 `json:"cost_brl"`         // null enquanto aberta (ver Pitfall 1)
    ShutdownReason *string `json:"shutdown_reason"`
}

type BreakerRow struct {
    Upstream string `json:"upstream"`
    State    string `json:"state"`   // closed|half-open|open|forced-open
}

type VastCostSection struct {
    TodayBRL     float64 `json:"today_brl"`      // soma closed + accrual da aberta, janela dia BRT
    MonthBRL     float64 `json:"month_brl"`      // idem, janela mês BRT
    BudgetBRL    float64 `json:"budget_brl"`     // cfg.MonthlyPrimaryBudgetBRL
    BudgetPctUsed float64 `json:"budget_pct_used"` // month/budget*100
    PhantomMonthBRL *float64 `json:"phantom_month_brl,omitempty"` // economia vs OpenRouter (ver §5)
}
```

**Conversões (já provadas em `usage.go`):** `pgtype.Numeric.Float64Value()` → `(pgtype.Float8, error)`, lê `.Float64` (`usage.go:183-185,215-217`). `pgtype.Timestamptz`: `if v.Valid { v.Time.Format(time.RFC3339) }` (`usage.go:219-221`). `pgtype.Int8`/`pgtype.Text`: checar `.Valid` (helpers `int8OrDash`/`numericOrDash`/`textOrDash` já existem em gatewayctl mas são CLI-only — o handler converte para `*T` JSON-null).

**Handler skeleton (copiar o padrão usage.go/metrics.go):**

```go
type OperationsHandler struct {
    q       *gen.Queries
    breakers *breaker.Set
    rec     *primary.Reconciler   // nil-safe: Vast off
    emergFSM *emerg.FSM           // nil-safe
    cfg     config.Config
    log     *slog.Logger
}
func NewOperationsHandler(q *gen.Queries, b *breaker.Set, rec *primary.Reconciler,
    emergFSM *emerg.FSM, cfg config.Config, log *slog.Logger) *OperationsHandler { ... }
// ServeHTTP: ParseScheduleEnv(cfg) → NextTransition; ListPrimaryLifecycles(since=monthStart);
// breakers.EffectiveStateSnapshot(); agregar custo em Go; obs counter por branch.
```

---

## 3. Registro de rota (trecho exato a copiar)

**Wiring do handler** (`cmd/gateway/main.go:1223,1231-1232`):
```go
adminUsageHandler := admin.NewUsageHandler(gen.New(pool), log)
adminMetricsHandler := admin.NewMetricsHandler(gen.New(pool), emergFSM, log)
adminAuditHandler := admin.NewAuditHandler(gen.New(pool), log)
// ADICIONAR:
adminOperationsHandler := admin.NewOperationsHandler(gen.New(pool), breakerSet, primaryReconciler, emergFSM, cfg, log)
```
Adicionar campo `adminOperationsHandler http.Handler` ao struct `proxyServer` (junto de `:95,104,105`) e atribuir no literal (`:1248-1251`).

**Mux/registro** — sub-router chi gated por `admin.Middleware` (`cmd/gateway/main.go:1454-1483`). Copiar o mesmo padrão de `/metrics`:
```go
if px.adminVerifier != nil {
    adminRouter := chi.NewRouter()
    adminRouter.Use(admin.Middleware(px.adminVerifier, log))   // X-Admin-Key bcrypt — :1456
    if px.adminUsageHandler != nil {
        adminRouter.Method(http.MethodGet, "/usage", px.adminUsageHandler)   // :1458
    }
    if px.adminMetricsHandler != nil {
        adminRouter.Method(http.MethodGet, "/metrics", px.adminMetricsHandler) // :1465
    }
    // ADICIONAR (mesmo padrão):
    if px.adminOperationsHandler != nil {
        adminRouter.Method(http.MethodGet, "/operations", px.adminOperationsHandler)
    }
    r.Mount("/admin", adminRouter)   // :1483
}
```
**Middleware X-Admin-Key:** `admin.Middleware(px.adminVerifier, log)` (`internal/admin/middleware.go:156`) — aplicado a TODO o sub-router via `.Use()`, então a rota nova herda auth automaticamente. Nenhum código de auth extra.

---

## 4. Pitfalls

**A. Deps do handler — primaryFSM/primaryRule NÃO estão hoisted.** São `:=` locais dentro de `if cfg.VastAIAPIKey != ""` (`main.go:897,910`), invisíveis na linha 1223. `primaryReconciler` JÁ é hoisted (`var primaryReconciler *primary.Reconciler`, `main.go:893`) e `breakerSet`/`emergFSM`/`cfg`/`pool` estão em escopo. **Ação:** passar `primaryReconciler` ao handler + adicionar getter exportado `func (r *Reconciler) Snapshot() (state string, lifecycleID, instanceID int64, isLeader bool)` (lê `r.deps.FSM.State().String()` + `r.activeLifecycleSnapshot()` + `r.IsLeader()`). Handler trata `rec == nil` → `primary_state:"unknown"`. Schedule é recomputável via `ParseScheduleEnv(cfg)` no handler (puro, sem hoist).

**B. Custo da lifecycle ABERTA é NULL.** `total_cost_brl` só é gravado em `ClosePrimaryLifecycle` (`primary_lifecycles.sql.go:15-22`). A row em execução (`ended_at IS NULL`) tem `TotalCostBrl` inválido. Somar cegamente subconta o pod rodando AGORA. **Ação:** para a row aberta, computar accrual ao-vivo = `accepted_dph × (now - first_health_pass_at).Hours() × cfg.USDToBRLRate` (mesma fórmula de `calculatePrimaryCostBRL`, `reconciler.go:1026-1045`; `USDToBRLRate` default 5.0, `config.go:169`).

**C. Janela mês/dia em America/Sao_Paulo.** Replicar o padrão de `usage.go`: `loc, _ := time.LoadLocation("America/Sao_Paulo")` (`usage.go:132`), calcular start-of-day / start-of-month em `loc`, passar `StartedAt` à query. NÃO usar UTC — desloca a fronteira do dia em 3h.

**D. pgtype → JSON null.** `usage.go` usa `_, _ := ...Float64Value()` e ignora erro (cost sempre presente lá). No painel Operação, `accepted_dph`/`cost_brl` PODEM ser null (lifecycle aberta) → usar `*float64` e checar `.Valid` antes de setar, senão emitir `null` (não `0`, que confunde "custo zero" com "ainda não calculado").

**E. `EffectiveStateSnapshot` faz refresh Redis (1 GET/upstream, debounced 1s).** Custo amortizado, OK para endpoint operador (polling ~5-10s). Não chamar em loop apertado.

---

## 5. Economia vs OpenRouter (painel "rodar local economizou X")

- **Phantom cost** (o que tier-1/OpenRouter TERIA custado pelo tráfego servido localmente): coluna `billing_events.cost_local_phantom_brl`, exposta hoje em `/admin/usage` → `Summary.CostLocalPhantomBRL` (`usage.go:63,184,204`). Comentário em `usage.go:206-208` confirma: phantom é coluna reporting-only, NÃO somada ao total real.
- **Custo Vast real:** `primary_lifecycles.total_cost_brl` (+ accrual da aberta).
- **Economia = phantom − vast_real**, na MESMA janela temporal (ambos windowáveis por data America/Sao_Paulo). Cruzável: SIM.
- **Caveat (open item):** as queries `SumBillingEventsRange`/`SumBillingEventsByDate` são TENANT-SCOPED (exigem `TenantID` — `usage.go:88-89,170`). O painel Operação quer phantom AGREGADO de TODOS os tenants. **Não existe** query de phantom gateway-wide sem filtro de tenant. **Opções:** (a) nova sqlc query `SumPhantomAllTenants(from,to)` somando `cost_local_phantom_brl` sem `WHERE tenant_id`; ou (b) loop sobre tenants somando per-tenant (mais lento, N queries). Recomendo (a) se o painel de economia entrar no escopo; senão marcar `phantom_month_brl` como `omitempty` e deixar para fase seguinte.

---

## Architectural Responsibility Map

| Capability | Tier | Rationale |
|------------|------|-----------|
| FSM/schedule/breaker/cost read | API/Backend (gateway `/admin`) | Estado runtime vive no processo gateway (FSM in-proc, breaker Set in-proc) + Postgres (lifecycles); o dashboard NÃO toca DB/Redis direto (padrão `metrics.go:5-8`) |
| Auth | API/Backend (admin.Middleware) | X-Admin-Key bcrypt já gateia todo `/admin/*` |
| Render | Dashboard (Next.js) | fora deste escopo — planner desenha UI |

## Sources

### Primary (HIGH)
- `internal/admin/usage.go`, `internal/admin/metrics.go` — padrão de handler + pgtype→float + tz
- `internal/admin/middleware.go:156` — X-Admin-Key
- `cmd/gateway/main.go:1223-1251,1454-1483` — wiring + registro de rota
- `internal/db/gen/primary_lifecycles.sql.go:107-170` — ListPrimaryLifecycles
- `internal/breaker/breaker.go:286-330` — EffectiveStateSnapshot
- `internal/primary/schedule.go:103-315` — ParseScheduleEnv / NextTransition / ShouldBeProvisioned
- `internal/primary/fsm.go:63-155` — State / String
- `internal/primary/reconciler.go:1026-1045,1719-1797` — calculatePrimaryCostBRL / getters
- `internal/primary/lifecycle.go:141-320` — Deps / Reconciler struct (FSM+Rule fields)
- `internal/config/config.go:169,244,510` — USDToBRLRate / MonthlyPrimaryBudgetBRL
- `internal/redisx/primary.go:114-128` — gw:primary:state mirror (fallback p/ FSM state)

## Metadata
- **Confidence:** Stack reuse HIGH (todas as funções verificadas at file:line). Endpoint design HIGH (espelha metrics.go já existente). Cost aggregation MEDIUM (precisa decisão Go-aggregate vs nova sqlc query).
- **Open items para o planner:** (1) getter `Reconciler.Snapshot()` vs ler Redis mirror; (2) phantom gateway-wide precisa nova query no-tenant-filter se painel economia entrar; (3) cost aggregate em Go vs nova sqlc query.
- **Valid until:** 2026-07-25 (código estável; Phase 14 em execução não toca esses arquivos).
