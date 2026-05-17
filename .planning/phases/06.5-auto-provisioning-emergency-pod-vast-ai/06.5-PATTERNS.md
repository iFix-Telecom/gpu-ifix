# Phase 6: Auto-provisioning Emergency Pod (Vast.ai) - Pattern Map

**Mapped:** 2026-05-13
**Files analyzed:** 24 (Wave 0 list per 06-VALIDATION.md)
**Analogs found:** 22 / 24 (2 são novos sem analog direto: `lifecycle.go`, `recovery.go` — uso de pattern composto Standard Go)

---

## File Classification

| Novo / Modificado | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `gateway/internal/emerg/fsm.go` | FSM (state machine) | event-driven | `gateway/internal/shed/fsm.go` | exact (4→9 estados) |
| `gateway/internal/emerg/fsm_test.go` | unit test | — | `gateway/internal/shed/fsm_test.go` | exact |
| `gateway/internal/emerg/reconciler.go` | reconciler/orchestrator | tick (1Hz) + event-driven | `gateway/internal/shed/tick.go` | role-match (1Hz tick) |
| `gateway/internal/emerg/reconciler_test.go` | unit test | — | `gateway/internal/shed/tick_test.go` | exact |
| `gateway/internal/emerg/lifecycle.go` | lifecycle goroutine | request-response (HTTP REST) + state mutation | (composto, novel) | new — combina cancel-pattern + Vast client + DB writes |
| `gateway/internal/emerg/lifecycle_test.go` | unit test | — | `gateway/internal/shed/reconcile_test.go` (timer pattern) | partial |
| `gateway/internal/emerg/recovery.go` | recovery handler | batch query + per-row branching | (novel) | new — DB scan + Vast.GetInstance + per-row close |
| `gateway/internal/emerg/recovery_test.go` | unit test | — | (novel) | new |
| `gateway/internal/emerg/budget.go` | helper (cost calc + alert) | batch (60s tick) + Sentry emit | `gateway/internal/billing/cost.go` | partial (cost calc helper) |
| `gateway/internal/emerg/budget_test.go` | unit test | — | (novel) | new |
| `gateway/internal/emerg/subscribe.go` | Pub/Sub subscriber | event-driven (consume) | `gateway/internal/breaker/subscribe.go` | exact (replicate 56 linhas) |
| `gateway/internal/emerg/subscribe_test.go` | unit test | — | `gateway/internal/breaker/subscribe_test.go` | exact |
| `gateway/internal/emerg/mirror.go` | namespace constants | — | `gateway/internal/breaker/mirror.go` | exact |
| `gateway/internal/emerg/errors.go` | sentinel errors | — | `gateway/internal/breaker/errors.go` | exact |
| `gateway/internal/emerg/vast/client.go` | external HTTP REST client | request-response | (novel — Vast.ai REST puro Go) | new — `pod/scripts/vast-ai.sh` (Bash) é referência de shape |
| `gateway/internal/emerg/vast/client_test.go` | unit test (httptest.Server) | — | `gateway/internal/breaker/breaker_test.go` (httptest pattern) | partial |
| `gateway/internal/emerg/vast/types.go` | DTOs (Offer, Instance, etc.) | — | `gateway/internal/upstreams/types.go` | partial (struct + Zod-equivalent shape) |
| `gateway/internal/redisx/emerg.go` | Redis Hash + Pub/Sub helpers + redsync wrapper | event-driven + key-value | `gateway/internal/redisx/breaker.go` (71 linhas integral) + `gateway/internal/redisx/shed.go` (245 linhas) | exact (Hash + Pub/Sub) + new (redsync wrapper) |
| `gateway/internal/redisx/emerg_test.go` | unit test | — | `gateway/internal/redisx/breaker_test.go` | exact |
| `gateway/db/migrations/0019_emergency_lifecycles.sql` | DDL migration | — | `gateway/db/migrations/0010_create_billing_events.sql` (DDL com índices + COMMENT) | role-match |
| `gateway/db/queries/emergency_lifecycles.sql` | sqlc queries | CRUD | `gateway/db/queries/billing.sql` (CTE + ON CONFLICT) e `db/queries/upstreams.sql` (org pattern) | exact |
| `gateway/sqlc.yaml` | sqlc config (modificação) | — | (já existe — append único) | exact |
| `gateway/cmd/gatewayctl/emerg.go` | CLI subcommand dispatcher | request-response (CLI args → DB/Redis) | `gateway/cmd/gatewayctl/upstreams.go` (4 subcomandos + tabwriter + JSON flag) e `gateway/cmd/gatewayctl/shed.go` (Redis-only) | exact |
| `gateway/cmd/gateway/main.go` | wiring (modificação) | boot orchestration | (já existe — adiciona NewReconciler + go reconciler.Run + dispatcher hookup) | partial |
| `gateway/internal/proxy/dispatcher.go` | router (modificação) | request-response | (já existe — adiciona `emerg.IsActive()` check) | partial |
| `gateway/internal/upstreams/loader.go` | loader (modificação) | snapshot + atomic.Pointer override | (já existe — adiciona `OverrideTier0`/`RestoreTier0`) | partial |
| `gateway/internal/obs/metrics.go` | Prometheus collectors (modificação) | — | (já existe — adiciona 7 collectors) | partial |
| `gateway/internal/integration_test/emerg_provision_happy_test.go` | integration test | testcontainers + httptest mock | `gateway/internal/integration_test/breaker_state_machine_test.go` | exact (testcontainers + mock + Eventually) |
| `gateway/internal/integration_test/emerg_cancel_pre_create_test.go` | integration test | — | mesmo padrão | exact |
| `gateway/internal/integration_test/emerg_cancel_post_create_test.go` | integration test | — | mesmo padrão | exact |
| `gateway/internal/integration_test/emerg_leader_test.go` | integration test (concurrency) | — | (novel — 2 reconcilers + 1 Redis) | new |
| `gateway/internal/integration_test/emerg_leader_recovery_zombie_test.go` | integration test | — | mesmo padrão + pre-seed DB | exact |
| `gateway/internal/integration_test/emerg_trigger_test.go` | integration test | — | mesmo padrão + Pub/Sub publish | exact |
| `gateway/internal/integration_test/emerg_singleton_test.go` | integration test (DB constraint) | — | `gateway/internal/integration_test/migrate_test.go` | partial |
| `gateway/internal/integration_test/emerg_budget_alert_test.go` | integration test (Sentry hub) | — | (novel — usa sentry-go test transport) | partial |

---

## Pattern Assignments

### 1. `gateway/internal/emerg/fsm.go` (FSM, event-driven, 9-state)

**Analog:** `gateway/internal/shed/fsm.go` (lines 26-258)
**Match quality:** exact — replicar struct + transition CAS + onChange callback; expandir `State` enum de 4 para 9 valores.

**Imports pattern** (`shed/fsm.go` lines 26-34):
```go
package emerg

import (
    "log/slog"
    "sync/atomic"
    "time"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)
```

**State enum pattern** (`shed/fsm.go` lines 38-49 — expand 4→9):
```go
type State int32

const (
    StateHealthy State = iota
    StateDegraded
    StateFailedOver
    StateEmergencyProvisioning
    StateEmergencyActive
    StateRecovering
    StateCooldown
    StateOffHours
    StateMaintenance
)

func (s State) String() string {
    switch s {
    case StateHealthy: return "healthy"
    case StateDegraded: return "degraded"
    // ... 9 cases total
    }
    return "unknown"
}
```

**FSM struct + atomic.Int32 pattern** (`shed/fsm.go` lines 110-138):
```go
type FSM struct {
    state     atomic.Int32
    enteredAt atomic.Int64 // unix-seconds
    cfg       atomic.Pointer[Config]
    onChange  func(from, to State, reason string)
    log       *slog.Logger
}

func (f *FSM) State() State { return State(f.state.Load()) }
func (f *FSM) EnteredAt() time.Time { return time.Unix(f.enteredAt.Load(), 0) }
```

**Transition CAS pattern** (`shed/fsm.go` lines 238-257) — copiar idêntico, trocando obs gauge:
```go
func (f *FSM) transition(from, to State, now time.Time, reason string) {
    if from == to { return }
    if !f.state.CompareAndSwap(int32(from), int32(to)) { return }
    f.enteredAt.Store(now.Unix())
    obs.GatewayEmergencyState.WithLabelValues(to.String()).Set(1)
    // reset other state gauges to 0 (only one active state at a time)
    f.log.Info("emerg FSM transition",
        "from", from.String(), "to", to.String(),
        "reason", reason, "at", now.Format(time.RFC3339),
    )
    if f.onChange != nil { f.onChange(from, to, reason) }
}
```

---

### 2. `gateway/internal/emerg/reconciler.go` (reconciler, tick 1Hz + leader CAS)

**Analog primário:** `gateway/internal/shed/tick.go` (lines 94-118 ticker pattern)
**Analog secundário:** RESEARCH Pattern 2 lines 360-401 (redsync Lock/Extend dentro do tick)
**Match quality:** role-match — 1Hz ticker é idêntico; lógica leader+evaluate é nova.

**Ticker imports + Run skeleton** (`shed/tick.go` lines 94-118):
```go
package emerg

import (
    "context"
    "log/slog"
    "sync/atomic"
    "time"

    "github.com/go-redsync/redsync/v4"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/redis/go-redis/v9"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

func (r *Reconciler) Run(ctx context.Context) {
    if r.fsm == nil { return }
    log := r.log.With("subsystem", "reconciler")
    interval := r.tickInterval
    if interval <= 0 { interval = 1 * time.Second }

    t := time.NewTicker(interval)
    defer t.Stop()

    mutex := r.redsync.NewMutex("gw:emerg:lock",
        redsync.WithExpiry(30*time.Second),
        redsync.WithTries(1),
        redsync.WithRetryDelay(0),
    )

    log.Info("emerg reconciler started", "interval", interval)
    for {
        select {
        case <-ctx.Done():
            // Pitfall 8: separate ctx for unlock — orig ctx already cancelled
            releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
            defer releaseCancel()
            if r.isLeader.Load() {
                _, _ = mutex.UnlockContext(releaseCtx)
            }
            log.Info("emerg reconciler stopping")
            return
        case now := <-t.C:
            r.runOneTick(ctx, mutex, now, log)
        }
    }
}
```

**Leader CAS within tick** (RESEARCH Pattern 2 + Pitfall 4 — check both `ok` AND `err`):
```go
func (r *Reconciler) runOneTick(ctx context.Context, mutex *redsync.Mutex, now time.Time, log *slog.Logger) {
    if !r.isLeader.Load() {
        if err := mutex.LockContext(ctx); err != nil {
            return // someone else holds it; non-leader path
        }
        r.isLeader.Store(true)
        log.Info("acquired leadership")
        if err := r.recoverOrphanLifecycles(ctx); err != nil {
            log.Error("orphan recovery failed", "err", err)
        }
    } else {
        // Renew every 10s = 1/3 TTL (CONTEXT D-B2). Use elapsed-based check instead of separate goroutine.
        if now.Unix()-r.lastExtendUnix.Load() >= 10 {
            ok, err := mutex.ExtendContext(ctx)
            if err != nil || !ok {
                // Pitfall 4: any non-(true, nil) = lost leadership
                log.Warn("lost leadership", "err", err, "ok", ok)
                r.isLeader.Store(false)
                return
            }
            r.lastExtendUnix.Store(now.Unix())
        }
    }
    // Leader: evaluate FSM transitions
    r.evaluateTick(ctx, now)
}
```

**Per-state evaluation pattern** (segue switch de `shed/fsm.go` Evaluate lines 173-218 — separar por método para 9 estados, conforme RESEARCH Open Question 10).

---

### 3. `gateway/internal/emerg/subscribe.go` (Pub/Sub consumer, event-driven)

**Analog:** `gateway/internal/breaker/subscribe.go` integral (1-56)
**Match quality:** exact — replicar 1:1, trocando channel name e payload type.

**Reconnect-with-backoff loop** (`breaker/subscribe.go` lines 19-56):
```go
package emerg

import (
    "context"
    "encoding/json"
    "sync/atomic"
    "time"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// Subscribe consumes gw:breaker:events to detect local-llm OPEN sustained.
// (Phase 6 reads breaker channel — does NOT publish to gw:emerg:events here;
// publishing happens in fsm.go onChange.)
func (r *Reconciler) Subscribe(ctx context.Context) {
    log := r.log.With("subsystem", "subscribe")
    for {
        if err := ctx.Err(); err != nil { return }
        ps := redisx.SubscribeBreakerEvents(ctx, r.rdb)
        ch := ps.Channel()
        drained := false
        for !drained {
            select {
            case <-ctx.Done():
                _ = ps.Close()
                return
            case msg, ok := <-ch:
                if !ok { drained = true; break }
                var ev redisx.BreakerEvent
                if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
                    log.Warn("malformed breaker event", "payload", msg.Payload, "err", err)
                    continue
                }
                r.tracker.ApplyEvent(ev)
                log.Debug("applied breaker event", "upstream", ev.Upstream, "state", ev.State)
            }
        }
        _ = ps.Close()
        log.Warn("pubsub channel closed; reconnecting")
        select {
        case <-ctx.Done(): return
        case <-time.After(1 * time.Second):
        }
    }
}
```

**Tracker pattern** (RESEARCH lines 906-930 — `localLlmTracker` mantém in-memory `openSince` timer para `SustainedFailedOverSeconds()`):
```go
type localLlmTracker struct {
    state     atomic.Value // string: "closed" | "half-open" | "open"
    openSince atomic.Int64
}

func (t *localLlmTracker) ApplyEvent(ev redisx.BreakerEvent) {
    if ev.Upstream != "local-llm" { return }
    t.state.Store(ev.State)
    if ev.State == "open" {
        if t.openSince.Load() == 0 { t.openSince.Store(time.Now().Unix()) }
    } else {
        t.openSince.Store(0)
    }
}

func (t *localLlmTracker) SustainedFailedOverSeconds() int64 {
    s, _ := t.state.Load().(string)
    if s != "open" { return 0 }
    since := t.openSince.Load()
    if since == 0 { return 0 }
    return time.Now().Unix() - since
}
```

---

### 4. `gateway/internal/redisx/emerg.go` (Redis Hash + Pub/Sub + redsync pool wrapper)

**Analog:** `gateway/internal/redisx/breaker.go` integral (1-71) + `gateway/internal/redisx/shed.go` (HGETALL pattern lines 116-130)
**Match quality:** exact (Hash + Pub/Sub helpers) + new (redsync pool factory)

**Imports + namespace constants** (segue `redisx/shed.go` lines 22-57):
```go
package redisx

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    "github.com/go-redsync/redsync/v4"
    redsyncredis "github.com/go-redsync/redsync/v4/redis/goredis/v9"
    "github.com/redis/go-redis/v9"
)

const (
    EmergEventsChannel  = "gw:emerg:events"
    emergStateKeyPrefix = "gw:emerg:"
    emergLockKey        = "gw:emerg:lock"
    redisOpTimeout      = 2 * time.Second // matches breaker.go and shed.go
)

func EmergStateKey() string { return emergStateKeyPrefix + "state" }
```

**Event payload struct** (segue `redisx/shed.go` ShedEvent lines 65-80):
```go
type EmergEvent struct {
    Type        string `json:"type"`         // "transition" | "cancel_in_flight" | "lifecycle_close"
    State       string `json:"state"`        // current FSM state string
    LifecycleID int64  `json:"lifecycle_id,omitempty"`
    Reason      string `json:"reason,omitempty"`
    SinceUnix   int64  `json:"since_unix"`
    ReplicaID   string `json:"replica_id"`   // os.Hostname()
    Payload     map[string]any `json:"payload,omitempty"`
}
```

**Write/Publish/Subscribe trio** (replicar `redisx/breaker.go` lines 41-65 — 2-second op timeout, fail-fast on nil rdb):
```go
func WriteEmergState(ctx context.Context, rdb *redis.Client, state, lifecycleID, podURL, podInstanceID string, enteredUnix int64) error {
    if rdb == nil { return fmt.Errorf("redisx: nil client") }
    ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
    defer cancel()
    return rdb.HSet(ctx, EmergStateKey(), map[string]any{
        "state":            state,
        "lifecycle_id":     lifecycleID,
        "pod_url":          podURL,
        "pod_instance_id":  podInstanceID,
        "entered_at":       enteredUnix,
    }).Err()
}

func PublishEmergEvent(ctx context.Context, rdb *redis.Client, ev EmergEvent) error {
    if rdb == nil { return fmt.Errorf("redisx: nil client") }
    ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
    defer cancel()
    payload, err := json.Marshal(ev)
    if err != nil { return err }
    return rdb.Publish(ctx, EmergEventsChannel, payload).Err()
}

func SubscribeEmergEvents(ctx context.Context, rdb *redis.Client) *redis.PubSub {
    return rdb.Subscribe(ctx, EmergEventsChannel)
}

// NewEmergRedsync wraps go-redsync v4 — único helper novo deste arquivo.
// Caller cria a Mutex via rs.NewMutex(emergLockKey, ...) com TTL/Tries/RetryDelay opts.
func NewEmergRedsync(rdb *redis.Client) *redsync.Redsync {
    pool := redsyncredis.NewPool(rdb)
    return redsync.New(pool)
}
```

---

### 5. `gateway/internal/emerg/lifecycle.go` (provision + cancel + destroy goroutine)

**Analog:** sem analog direto — composto de Standard Go (context.WithCancel) + RESEARCH Pattern 4 lines 446-528.
**Match quality:** new — única peça **realmente novel** do package.

**Goroutine spawn pattern** (RESEARCH Pattern 4 lines 456-466):
```go
package emerg

import (
    "context"
    "errors"
    "fmt"
    "net/http"
    "sync/atomic"
    "time"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

type Lifecycle struct {
    ID     int64
    Cancel context.CancelFunc
}

func (r *Reconciler) startProvisioning(parentCtx context.Context, lifecycleID int64) {
    ctx, cancel := context.WithCancel(parentCtx)
    r.activeLifecycle.Store(&Lifecycle{ID: lifecycleID, Cancel: cancel})

    go func() {
        defer cancel()
        if err := r.provisionLifecycle(ctx, lifecycleID); err != nil {
            r.log.Error("lifecycle failed", "id", lifecycleID, "err", err)
        }
    }()
}
```

**Triple-layer checkpoint pattern** (RESEARCH lines 468-493 + Pitfall 5 epsilon comparison + Pitfall 9 actual_status terminal):
```go
func (r *Reconciler) provisionLifecycle(ctx context.Context, id int64) error {
    if ctx.Err() != nil { return ctx.Err() }

    offers, err := r.vast.SearchOffers(ctx, r.searchFilter())
    if err != nil { return err }

    // Pitfall 5 — epsilon when comparing dph_total to cap
    var pickable []vast.Offer
    for _, o := range offers {
        if o.DphTotal > r.cfg.PriceCapDPH+0.0001 { continue }
        pickable = append(pickable, o)
    }
    if len(pickable) == 0 { return ErrNoOffersBelowCap }

    for attempt := 0; attempt < 3; attempt++ {
        if ctx.Err() != nil { return ctx.Err() }
        offer := pickable[0]
        instance, err := r.vast.CreateInstance(ctx, offer.ID, r.createRequest(offer, id))
        if err == nil {
            return r.waitForReadyOrDestroy(ctx, id, instance.ID)
        }
        if !errors.Is(err, vast.ErrOfferGone) { return err }
        // Bid race — re-search with exp backoff 2s/4s/8s
        select {
        case <-ctx.Done(): return ctx.Err()
        case <-time.After(time.Duration(1<<attempt) * 2 * time.Second):
        }
        offers, err = r.vast.SearchOffers(ctx, r.searchFilter())
        if err != nil { return err }
        pickable = filterBelowCap(offers, r.cfg.PriceCapDPH)
        if len(pickable) == 0 { return ErrNoOffersBelowCap }
    }
    r.captureSentry(id, "offer_race_lost", map[string]any{"attempts": 3})
    return ErrOfferRaceLost
}
```

**waitForReadyOrDestroy** (RESEARCH lines 495-528 + Pitfall 9 — `actual_status ∈ {exited, unknown, offline}` triggers destroy):
```go
func (r *Reconciler) waitForReadyOrDestroy(ctx context.Context, lifecycleID, instanceID int64) error {
    poll := time.NewTicker(5 * time.Second)
    defer poll.Stop()
    deadline := time.NewTimer(time.Duration(r.cfg.ColdStartBudgetSeconds) * time.Second)
    defer deadline.Stop()

    for {
        select {
        case <-ctx.Done():
            // Cancel post-create: MUST destroy (Pitfall 8 — separate ctx)
            destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
            _ = r.vast.DestroyInstance(destroyCtx, instanceID)
            destroyCancel()
            r.closeLifecycle(lifecycleID, "cancelled_in_flight")
            return ctx.Err()
        case <-deadline.C:
            destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
            _ = r.vast.DestroyInstance(destroyCtx, instanceID)
            destroyCancel()
            r.closeLifecycle(lifecycleID, "health_timeout")
            return ErrHealthTimeout
        case <-poll.C:
            inst, err := r.vast.GetInstance(ctx, instanceID)
            if err != nil { continue }
            // Pitfall 9 — terminal states (do not retry)
            if inst.ActualStatus == "exited" || inst.ActualStatus == "unknown" || inst.ActualStatus == "offline" {
                destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
                _ = r.vast.DestroyInstance(destroyCtx, instanceID)
                destroyCancel()
                r.closeLifecycle(lifecycleID, "instance_terminal_state")
                return ErrInstanceTerminal
            }
            if inst.ActualStatus != "running" || inst.PublicIPAddr == "" { continue }
            // Pitfall 1 — actual_status:running ≠ pod ready; verify /health
            healthURL := r.podHealthURL(inst)
            if r.checkHealth(ctx, healthURL) {
                r.markHealthy(ctx, lifecycleID, healthURL, inst)
                return nil
            }
        }
    }
}
```

---

### 6. `gateway/internal/emerg/recovery.go` (leader recovery, batch query + per-row branching)

**Analog:** sem analog direto — derived from CONTEXT D-D5 + RESEARCH Pitfall 7.
**Match quality:** new — DB scan + Vast.GetInstance + close per-row.

**Pattern** (CONTEXT lines 124-136 + RESEARCH Pitfall 7 — `vast_instance_id IS NULL` = pre-create orphan):
```go
package emerg

import (
    "context"
    "errors"

    gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

func (r *Reconciler) recoverOrphanLifecycles(ctx context.Context) error {
    rows, err := r.q.ListLiveEmergencyLifecycles(ctx)
    if err != nil { return err }
    for _, row := range rows {
        switch {
        case !row.VastInstanceID.Valid:
            // Pitfall 7 — INSERT happened before create_instance; close orphan
            r.closeLifecycle(row.ID, "leader_recovery_pre_create")
        default:
            inst, err := r.vast.GetInstance(ctx, row.VastInstanceID.Int64)
            if errors.Is(err, vast.ErrInstanceNotFound) {
                r.closeLifecycle(row.ID, "leader_recovery_lost")
                continue
            }
            if err != nil {
                r.log.Warn("leader recovery: GetInstance failed; skipping", "id", row.ID, "err", err)
                continue
            }
            if !inst.IsActive() { // exited|unknown|offline
                destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
                _ = r.vast.DestroyInstance(destroyCtx, row.VastInstanceID.Int64)
                destroyCancel()
                r.closeLifecycle(row.ID, "leader_recovery_zombie")
                continue
            }
            // Active + healthy: resume FSM from JSONB events
            r.resumeFSMFromEvents(row)
        }
    }
    return nil
}
```

---

### 7. `gateway/internal/emerg/budget.go` (monthly cost aggregate + Sentry alert + dedupe)

**Analog primário:** `gateway/internal/billing/cost.go` (cost calc helper — partial)
**Analog secundário:** RESEARCH Pitfall 11 (atomic.Int64 day-bucket dedupe)
**Match quality:** partial.

**Dedupe pattern** (RESEARCH Pitfall 11 lines 1411-1417 — CORRECT version):
```go
package emerg

import (
    "context"
    "strconv"
    "sync/atomic"
    "time"

    "github.com/getsentry/sentry-go"

    gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

type budgetAlertDedupe struct {
    lastEmittedDay atomic.Int64 // unix epoch / 86400
}

func (b *budgetAlertDedupe) shouldEmit() bool {
    today := time.Now().Unix() / 86400
    last := b.lastEmittedDay.Load()
    if last == today { return false }
    return b.lastEmittedDay.CompareAndSwap(last, today)
}
```

**60s aggregate query** (CONTEXT D-D2 lines 111-118 — query ai_gateway.emergency_lifecycles):
```go
func (r *Reconciler) checkBudget(ctx context.Context) {
    cost, err := r.q.GetMonthlyCostBRL(ctx)
    if err != nil {
        r.log.Warn("monthly cost query failed", "err", err)
        return
    }
    obs.GatewayEmergencyMonthCostBRL.Set(cost)
    if cost <= r.cfg.MonthlyBudgetBRL { return }
    if !r.budgetDedupe.shouldEmit() { return }

    hub := sentry.CurrentHub().Clone()
    hub.Scope().SetTag("subsystem", "emerg")
    hub.Scope().SetTag("alert", "budget_exceeded")
    hub.Scope().SetExtra("month_cost_brl", cost)
    hub.Scope().SetExtra("budget_brl", r.cfg.MonthlyBudgetBRL)
    hub.CaptureMessage("monthly emergency budget exceeded")
}
```

---

### 8. `gateway/internal/emerg/vast/client.go` (HTTP REST client, request-response)

**Analog primário:** sem analog direto Go (não há `internal/openrouter/`); `pod/scripts/vast-ai.sh` Phase 1 documenta endpoint shape (Bash, NÃO copiar literal)
**Analog secundário:** `gateway/internal/proxy/director.go` (HTTP forward pattern — 60% match)
**Match quality:** new — primeira API client externo Go puro do gateway.

**Imports + Client struct** (Standard Go HTTP client pattern, RESEARCH Code Examples lines 1495-1543):
```go
package vast

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "strconv"
    "strings"
    "time"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

const (
    DefaultBaseURL = "https://vast.ai/api/v0"
    httpTimeout    = 30 * time.Second // CONTEXT D-A1 — package-level, NOT env-tunable
)

type Client struct {
    baseURL    string
    apiKey     string
    httpClient *http.Client
}

func NewClient(apiKey string) *Client {
    return &Client{
        baseURL: DefaultBaseURL,
        apiKey:  apiKey,
        httpClient: &http.Client{Timeout: httpTimeout},
    }
}

func NewClientWithBaseURL(apiKey, baseURL string) *Client {
    c := NewClient(apiKey)
    c.baseURL = baseURL
    return c
}
```

**SearchOffers pattern** (RESEARCH lines 1498-1528 — request-response with metric counter):
```go
func (c *Client) SearchOffers(ctx context.Context, filter SearchFilter) ([]Offer, error) {
    q, err := json.Marshal(filter)
    if err != nil { return nil, err }
    u := fmt.Sprintf("%s/bundles/?q=%s", c.baseURL, url.QueryEscape(string(q)))
    req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
    if err != nil { return nil, err }
    req.Header.Set("Authorization", "Bearer "+c.apiKey)

    obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", "started").Inc()
    resp, err := c.httpClient.Do(req)
    if err != nil {
        obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", "transport_error").Inc()
        return nil, err
    }
    defer resp.Body.Close()
    obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", strconv.Itoa(resp.StatusCode)).Inc()
    if resp.StatusCode != 200 { return nil, c.parseErrorBody(resp) }

    var body struct { Offers []Offer `json:"offers"` }
    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil { return nil, err }
    return body.Offers, nil
}
```

**Defensive error parsing** (RESEARCH lines 745-779 — Vast retorna error envelopes inconsistentes):
```go
func (c *Client) parseErrorBody(resp *http.Response) error {
    body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
    var env vastErrorEnvelope
    _ = json.Unmarshal(body, &env)
    msg := env.Msg
    if msg == "" { msg = env.Message }
    if msg == "" { msg = string(body) }
    switch resp.StatusCode {
    case 401, 403:
        return &VastError{Status: resp.StatusCode, Code: "unauthorized", Msg: msg}
    case 404, 410:
        if env.Error == "no_such_ask" || strings.Contains(msg, "no longer available") {
            return ErrOfferGone
        }
        if env.Error == "no_such_instance" { return ErrInstanceNotFound }
        return &VastError{Status: resp.StatusCode, Code: env.Error, Msg: msg}
    case 429: return ErrRateLimited
    case 500, 502, 503, 504:
        return &VastError{Status: resp.StatusCode, Code: "server_error", Msg: msg}
    }
    return &VastError{Status: resp.StatusCode, Code: env.Error, Msg: msg}
}

// Auth header MUST never be logged (Validation T-6-01). Tests assert this via
// httptest.Server inspection of req.Header — never log resp body para search
// (offers podem ter machine_id sensível para concorrentes).
```

---

### 9. `gateway/internal/emerg/vast/types.go` (DTOs)

**Analog:** `gateway/internal/upstreams/types.go` (struct + JSON tag pattern — partial)
**Match quality:** partial — apenas struct shape pattern, sem inferência de Drizzle.

**Key DTOs** (RESEARCH lines 583-602 + 670-690 — Offer + Instance estruturas):
```go
package vast

type Offer struct {
    ID           int64   `json:"id"`           // ask_id used in PUT /asks/{id}/
    GpuName      string  `json:"gpu_name"`
    NumGpus      int     `json:"num_gpus"`
    DphTotal     float64 `json:"dph_total"`
    Reliability  float64 `json:"reliability"`
    InetDown     int     `json:"inet_down"`
    CudaMaxGood  float64 `json:"cuda_max_good"`
    MachineID    int64   `json:"machine_id"`
    HostID       int64   `json:"host_id"`
    Geolocation  string  `json:"geolocation"`
    Rentable     bool    `json:"rentable"`
    Verified     bool    `json:"verified"`
}

type Instance struct {
    ID             int64   `json:"id"`
    ActualStatus   string  `json:"actual_status"`   // running|exited|unknown|offline|loading|scheduling
    IntendedStatus string  `json:"intended_status"`
    SshHost        string  `json:"ssh_host"`
    SshPort        int     `json:"ssh_port"`
    PublicIPAddr   string  `json:"public_ipaddr"`
    MachineID      int64   `json:"machine_id"`
    HostID         int64   `json:"host_id"`
    DphTotal       float64 `json:"dph_total"`
    ImageUUID      string  `json:"image_uuid"`
    Label          string  `json:"label"`
}

func (i Instance) IsActive() bool {
    return i.ActualStatus == "running" || i.ActualStatus == "loading" || i.ActualStatus == "scheduling"
}

type CreateRequest struct {
    ClientID    string            `json:"client_id"`     // "me"
    Image       string            `json:"image"`
    Env         map[string]string `json:"env"`
    Onstart     string            `json:"onstart"`
    Runtype     string            `json:"runtype"`       // "ssh"
    Disk        int               `json:"disk"`
    Label       string            `json:"label"`
    TargetState string            `json:"target_state"`  // "running"
}

type CreateResponse struct {
    Success     bool  `json:"success"`
    NewContract int64 `json:"new_contract"`
}

type vastErrorEnvelope struct {
    Success bool   `json:"success,omitempty"`
    Error   string `json:"error,omitempty"`
    Msg     string `json:"msg,omitempty"`
    Message string `json:"message,omitempty"`
    AskID   int64  `json:"ask_id,omitempty"`
}
```

---

### 10. `gateway/internal/emerg/errors.go` (sentinel errors)

**Analog:** `gateway/internal/breaker/errors.go` (1-23) — package godoc + `var (...)` block
**Match quality:** exact

**Pattern** (`breaker/errors.go` lines 1-23):
```go
// Package emerg implements the Phase 6 emergency-pod reconciler (FSM,
// leader-elected, Vast.ai-backed). See gateway/internal/emerg/fsm.go.
package emerg

import "errors"

var (
    ErrOfferRaceLost      = errors.New("emerg: bid race lost after 3 attempts")
    ErrHealthTimeout      = errors.New("emerg: pod /health did not pass within budget")
    ErrInstanceTerminal   = errors.New("emerg: vast instance entered terminal state")
    ErrNoOffersBelowCap   = errors.New("emerg: no offers below VAST_PRICE_CAP_DPH")
    ErrLeaderLost         = errors.New("emerg: leadership lost mid-tick")
    ErrLifecycleSingleton = errors.New("emerg: live lifecycle already exists (D-B5 violation)")
)
```

**Vast-specific sentinels** em `gateway/internal/emerg/vast/errors.go`:
```go
package vast

import "errors"

var (
    ErrOfferGone        = errors.New("vast: offer no longer available (404/410 no_such_ask)")
    ErrInstanceNotFound = errors.New("vast: instance not found (404 no_such_instance)")
    ErrRateLimited      = errors.New("vast: HTTP 429 rate limited")
    ErrUnauthorized     = errors.New("vast: HTTP 401/403 — VAST_AI_API_KEY invalid")
)

type VastError struct {
    Status int
    Code   string
    Msg    string
}

func (e *VastError) Error() string {
    return "vast: HTTP " + http.StatusText(e.Status) + ": " + e.Msg
}
```

---

### 11. `gateway/internal/emerg/mirror.go` (namespace constants — 1-line file)

**Analog:** `gateway/internal/breaker/mirror.go` integral (1-13)
**Match quality:** exact.

```go
package emerg

const Namespace = "gw:emerg:"
```

---

### 12. `gateway/db/migrations/0019_emergency_lifecycles.sql` (DDL migration)

**Analog:** `gateway/db/migrations/0010_create_billing_events.sql` (DDL com CHECK + indexes + COMMENT)
**Match quality:** role-match — DDL only, sem trigger NOTIFY (config emergency vem de env, não tabela hot-reloadable).

**DDL skeleton** (composto de migration 0010 + RESEARCH lines 1565-1619):
```sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 6 — emergency pod lifecycle audit table.
-- Source: CONTEXT.md D-B4 + Pitfall 15 fix (added first_health_pass_at column).
CREATE TABLE IF NOT EXISTS ai_gateway.emergency_lifecycles (
    id                      BIGSERIAL PRIMARY KEY,
    started_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    first_health_pass_at    TIMESTAMPTZ,
    ended_at                TIMESTAMPTZ,
    trigger_reason          TEXT NOT NULL,
        -- 'failed_over_sustained' | 'manual_force'
    vast_offer_id           BIGINT,
    vast_instance_id        BIGINT,
    accepted_dph            NUMERIC(6,4),
    total_cost_brl          NUMERIC(10,4),
    shutdown_reason         TEXT,
    events                  JSONB NOT NULL DEFAULT '[]'::JSONB,
    leader_replica          TEXT
);

-- D-B5 — partial unique index: at most 1 row with ended_at IS NULL.
CREATE UNIQUE INDEX IF NOT EXISTS emergency_live_singleton
    ON ai_gateway.emergency_lifecycles ((TRUE)) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_started_at
    ON ai_gateway.emergency_lifecycles (started_at DESC);

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_live
    ON ai_gateway.emergency_lifecycles (ended_at) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_month_cost
    ON ai_gateway.emergency_lifecycles (started_at)
    WHERE ended_at IS NOT NULL;

COMMENT ON TABLE ai_gateway.emergency_lifecycles IS
    'Audit trail for Vast.ai emergency pod lifecycles (Phase 6 PRV-10).';
COMMENT ON COLUMN ai_gateway.emergency_lifecycles.first_health_pass_at IS
    'Timestamp when pod /health first returned healthy. Used as start of cost calc (D-D4).';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.emergency_lifecycles CASCADE;
-- +goose StatementEnd
```

---

### 13. `gateway/db/queries/emergency_lifecycles.sql` (sqlc queries)

**Analog:** `gateway/db/queries/upstreams.sql` (sqlc query org pattern — `:exec`, `:one`, `:many`, `sqlc.narg`)
**Match quality:** exact.

**Query naming pattern + JSONB append** (segue `db/queries/billing.sql` CTE + ON CONFLICT pattern + RESEARCH lines 939-987):
```sql
-- name: InsertEmergencyLifecycle :one
INSERT INTO ai_gateway.emergency_lifecycles (started_at, trigger_reason, leader_replica)
VALUES (NOW(), $1, $2)
RETURNING id;

-- name: UpdateEmergencyLifecycleVastIDs :exec
UPDATE ai_gateway.emergency_lifecycles
SET vast_offer_id = $2,
    vast_instance_id = $3,
    accepted_dph = $4,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: MarkEmergencyLifecycleHealthy :exec
UPDATE ai_gateway.emergency_lifecycles
SET first_health_pass_at = NOW(),
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: CloseEmergencyLifecycle :exec
UPDATE ai_gateway.emergency_lifecycles
SET ended_at = NOW(),
    shutdown_reason = $2,
    total_cost_brl = $3,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: ListLiveEmergencyLifecycles :many
-- Used by leader recovery (D-D5). Partial unique index guarantees ≤1 row.
SELECT id, vast_offer_id, vast_instance_id, started_at, events
FROM ai_gateway.emergency_lifecycles
WHERE ended_at IS NULL;

-- name: GetMonthlyCostBRL :one
SELECT COALESCE(SUM(total_cost_brl), 0)::numeric AS month_cost
FROM ai_gateway.emergency_lifecycles
WHERE started_at >= date_trunc('month', NOW())
  AND ended_at IS NOT NULL;

-- name: ListEmergencyLifecycles :many
-- gatewayctl emerg lifecycles --since N --limit M.
SELECT id, started_at, ended_at, trigger_reason, vast_offer_id, vast_instance_id,
       accepted_dph, total_cost_brl, shutdown_reason, leader_replica
FROM ai_gateway.emergency_lifecycles
WHERE started_at >= $1
ORDER BY started_at DESC
LIMIT $2;
```

**`sqlc.yaml` modification:** append `db/queries/emergency_lifecycles.sql` to the `queries:` array (já segue layout linha 11-21 do arquivo atual).

---

### 14. `gateway/cmd/gatewayctl/emerg.go` (CLI dispatcher, 4 subcomandos)

**Analog primário:** `gateway/cmd/gatewayctl/upstreams.go` (4 subcomandos + tabwriter + JSON flag — 1-227)
**Analog secundário:** `gateway/cmd/gatewayctl/shed.go` (Redis-only state read + force pattern)
**Match quality:** exact.

**Imports + dispatcher skeleton** (`upstreams.go` lines 1-46):
```go
package main

import (
    "context"
    "encoding/json"
    "errors"
    "flag"
    "fmt"
    "log/slog"
    "os"
    "text/tabwriter"
    "time"

    "github.com/jackc/pgx/v5"
    "github.com/redis/go-redis/v9"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
    gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func runEmerg(ctx context.Context, args []string, log *slog.Logger) int {
    if len(args) == 0 {
        fmt.Fprintln(os.Stderr, "Usage: gatewayctl emerg state|force-provision|force-destroy|lifecycles [flags]")
        return 2
    }
    switch args[0] {
    case "state":            return runEmergState(ctx, args[1:], log)
    case "force-provision":  return runEmergForceProvision(ctx, args[1:], log)
    case "force-destroy":    return runEmergForceDestroy(ctx, args[1:], log)
    case "lifecycles":       return runEmergLifecycles(ctx, args[1:], log)
    default:
        fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
        return 2
    }
}
```

**`emerg state` JSON+table pattern** (segue `shed.go` runShedState lines 63-166 — read Redis Hash, format flag for JSON|table):
```go
func runEmergState(ctx context.Context, args []string, log *slog.Logger) int {
    fs := flag.NewFlagSet("emerg state", flag.ContinueOnError)
    format := fs.String("format", "table", "output format: table | json")
    if err := fs.Parse(args); err != nil { return 2 }
    rdb, err := loadAndRedis(ctx, log)
    if err != nil { fmt.Fprintf(os.Stderr, "error: %v\n", err); return 1 }
    defer func() { _ = rdb.Close() }()

    m, err := rdb.HGetAll(ctx, "gw:emerg:state").Result()
    if err != nil { fmt.Fprintf(os.Stderr, "read emerg state: %v\n", err); return 1 }
    // ... format JSON or tabwriter table per shed.go pattern
    return 0
}
```

**`emerg lifecycles` DB query + tabwriter** (segue `upstreams.go` runUpstreamsList lines 51-95 — sqlc query + format table):
```go
func runEmergLifecycles(ctx context.Context, args []string, log *slog.Logger) int {
    fs := flag.NewFlagSet("emerg lifecycles", flag.ContinueOnError)
    sinceStr := fs.String("since", "7d", "duration window (e.g. 24h, 7d)")
    limit := fs.Int("limit", 50, "max rows")
    format := fs.String("format", "table", "output format: table | json")
    if err := fs.Parse(args); err != nil { return 2 }
    // ... loadAndPool + gen.New(pool) + q.ListEmergencyLifecycles + tabwriter print
    return 0
}
```

---

### 15. Integration tests (`gateway/internal/integration_test/emerg_*.go`)

**Analog primário:** `gateway/internal/integration_test/breaker_state_machine_test.go` (1-128 — testcontainers + httptest.Server mock + Eventually + Snapshot)
**Analog secundário:** `gateway/internal/integration_test/setup_test.go` (testcontainers Postgres 16 + Redis 7 — sharedPG/sharedRedis pattern)
**Match quality:** exact.

**Integration test skeleton** (`breaker_state_machine_test.go` lines 1-50):
```go
//go:build integration

package integration

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"
    "time"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

func TestIntegration_EmergProvisionHappyPath(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    pool, rdb := freshSchema(t, ctx)

    mockVast := newMockVastServer(t)
    defer mockVast.Close()
    mockPodHealth := newMockPodHealthServer(t)
    defer mockPodHealth.Close()

    reconciler := emerg.NewReconciler(emerg.Deps{
        DB:           pool,
        Redis:        rdb,
        VastClient:   vast.NewClientWithBaseURL("dummy", mockVast.URL),
        TickInterval: 100 * time.Millisecond, // accelerate per Pitfall 13
        ColdBudget:   5 * time.Second,
    })
    go reconciler.Run(ctx)

    publishBreakerEvent(t, rdb, "local-llm", "open")
    require.Eventually(t, func() bool {
        return reconciler.State() == emerg.StateEmergencyActive
    }, 5*time.Second, 100*time.Millisecond)

    var instID int64
    err := pool.QueryRow(ctx,
        "SELECT vast_instance_id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL").Scan(&instID)
    require.NoError(t, err)
    require.Equal(t, int64(12345), instID)
}
```

**Mock Vast.ai server** (RESEARCH lines 1198-1241 — `httptest.Server` + atomic counters + atomic.Pointer[Response]):
```go
type mockVastServer struct {
    *httptest.Server
    searchHits     atomic.Int64
    createHits     atomic.Int64
    destroyHits    atomic.Int64
    statusHits     atomic.Int64
    searchResponse atomic.Pointer[[]vast.Offer]
    createStatus   atomic.Int32
}

func newMockVastServer(t *testing.T) *mockVastServer {
    m := &mockVastServer{}
    m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch {
        case strings.HasPrefix(r.URL.Path, "/bundles/"):
            m.searchHits.Add(1)
            offers := m.searchResponse.Load()
            if offers != nil { _ = json.NewEncoder(w).Encode(map[string]any{"offers": *offers}) }
        case strings.HasPrefix(r.URL.Path, "/asks/") && r.Method == "PUT":
            m.createHits.Add(1)
            status := int(m.createStatus.Load())
            if status == 0 { status = 200 }
            w.WriteHeader(status)
            if status == 200 {
                _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "new_contract": 12345})
            } else if status == 404 {
                _ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "no_such_ask", "msg": "not available"})
            }
        case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == "DELETE":
            m.destroyHits.Add(1)
            _ = json.NewEncoder(w).Encode(map[string]any{"success": true})
        case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == "GET":
            m.statusHits.Add(1)
            // ... return current state
        case r.URL.Path == "/users/current":
            _ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "email": "test@ifix"})
        }
    }))
    return m
}
```

**Singleton DB index test** (`integration_test/migrate_test.go` pattern — direct SQL):
```go
func TestIntegration_EmergSingletonDBIndex(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    pool, _ := freshSchema(t, ctx)

    // Insert 1st live lifecycle
    _, err := pool.Exec(ctx, `INSERT INTO ai_gateway.emergency_lifecycles (trigger_reason) VALUES ('manual_force')`)
    require.NoError(t, err)
    // 2nd live lifecycle MUST violate emergency_live_singleton index
    _, err = pool.Exec(ctx, `INSERT INTO ai_gateway.emergency_lifecycles (trigger_reason) VALUES ('manual_force')`)
    require.Error(t, err)
    require.Contains(t, err.Error(), "emergency_live_singleton")
}
```

---

## Shared Patterns

### Padrão A — In-process autoritativo + Redis mirror + Pub/Sub

**Source:** `gateway/internal/breaker/{breaker,subscribe,mirror}.go` (Phase 3 D-D1) + `gateway/internal/shed/{fsm,subscribe}.go` (Phase 5 D-C3)
**Apply to:** `internal/emerg/fsm.go`, `internal/emerg/subscribe.go`, `internal/redisx/emerg.go`

**Invariant:**
- FSM in-process é autoritativo; Redis nunca é source of truth
- Hot-path `State()` reads são `atomic.Load` (lockless)
- Redis ops timeout = **2 seconds** (`redisOpTimeout` em `redisx/breaker.go` linha 50, replicado em `redisx/shed.go` linha 50)
- Mirror failures bumpam `obs.Gateway*MirrorFailures` counter; FSM continua operando

**Apply pattern** (do `redisx/breaker.go` lines 41-65):
```go
ctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
defer cancel()
return rdb.HSet(ctx, key, fields).Err() // 2s budget; failure logged, not propagated
```

---

### Padrão B — Sentry breadcrumbs + CaptureMessage para terminal errors

**Source:** `gateway/internal/obs/sentry.go` (Phase 2 D-A4 init) + Phase 3 D-G2 instituiu pattern para breakers
**Apply to:** `internal/emerg/fsm.go` (transition breadcrumbs), `internal/emerg/lifecycle.go` (terminal error CaptureMessage), `internal/emerg/budget.go` (budget warning), `internal/emerg/recovery.go` (zombie capture)

**Per-transition breadcrumb (zero cost — attached to next CaptureMessage):**
```go
sentry.AddBreadcrumb(&sentry.Breadcrumb{
    Category:  "emerg",
    Message:   fmt.Sprintf("state %s→%s", from.String(), to.String()),
    Level:     sentry.LevelInfo,
    Timestamp: time.Now(),
    Data: map[string]interface{}{
        "lifecycle_id": lifecycleID,
        "reason":       reason,
    },
})
```

**Terminal error CaptureMessage (forensic):**
```go
hub := sentry.CurrentHub().Clone()
hub.Scope().SetTag("subsystem", "emerg")
hub.Scope().SetTag("lifecycle_id", strconv.FormatInt(lifecycleID, 10))
hub.Scope().SetTag("shutdown_reason", "offer_race_lost")
hub.Scope().SetExtra("attempts", 3)
hub.CaptureMessage("emergency lifecycle aborted: offer_race_lost")
```

Apenas estados terminais (`offer_race_lost`, `health_timeout`, `leader_recovery_zombie`, `budget_exceeded`, `instance_terminal_state`) emitem CaptureMessage. Transições normais usam **somente** breadcrumb.

---

### Padrão C — Prometheus collector registration via `promauto`

**Source:** `gateway/internal/obs/metrics.go` lines 17-50 (Phase 2) + lines 56-100 (Phase 3 breaker) + lines 270-340 (Phase 5 shed)
**Apply to:** `internal/obs/metrics.go` (modificar para adicionar 7 collectors emergency_*)

**Pattern para gauge + counter + histogram** (da metrics.go existente):
```go
// 1 gauge — current state (label "state", set 1 para current, 0 para outros)
var GatewayEmergencyState = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_emergency_state",
    Help: "Current emergency FSM state. 1 for active, 0 for others.",
}, []string{"state"})

// 2 counter — lifecycles by reason
var GatewayEmergencyLifecyclesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_emergency_lifecycles_total",
    Help: "Count of emergency lifecycles by trigger and shutdown reason.",
}, []string{"trigger_reason", "shutdown_reason"})

// 3 gauge — active pod presence
var GatewayEmergencyActivePod = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_emergency_active_pod",
    Help: "1 when emergency pod is live serving traffic; 0 otherwise.",
}, []string{"pod_url"})

// 4 histogram — provision duration
var GatewayEmergencyProvisionDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
    Name:    "gateway_emergency_provision_duration_seconds",
    Help:    "Time from search to /health pass for emergency pod provisions.",
    Buckets: []float64{30, 60, 120, 300, 600, 900},
})

// 5 gauge — current pod cost
var GatewayEmergencyCostDPH = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_emergency_cost_dph",
    Help: "Current emergency pod USD per hour cost.",
}, []string{"lifecycle_id"})

// 6 gauge — month-to-date cost
var GatewayEmergencyMonthCostBRL = promauto.NewGauge(prometheus.GaugeOpts{
    Name: "gateway_emergency_month_cost_brl",
    Help: "Running monthly aggregate cost in BRL of closed lifecycles.",
})

// 7 counter — Vast API requests by op + status
var GatewayVastAPIRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_vast_api_requests_total",
    Help: "Vast.ai REST requests by operation and HTTP status (or 'transport_error', 'started').",
}, []string{"op", "status"})
```

---

### Padrão D — Atomic.Pointer-based dispatcher override

**Source:** `gateway/internal/upstreams/loader.go` lines 21-141 (snapshot atomic.Pointer pattern Phase 3 + 5)
**Apply to:** `gateway/internal/upstreams/loader.go` (modificar para adicionar `OverrideTier0`/`RestoreTier0`)

**Add fields to existing Loader struct + 2 methods** (RESEARCH lines 1033-1071):
```go
type Loader struct {
    pool          *pgxpool.Pool
    q             loaderQueries
    snap          atomic.Pointer[snapshot]
    log           *slog.Logger
    tier0Override map[string]*atomic.Pointer[string] // role → URL override (LLM only in v1)
}

// Initialize tier0Override map in NewLoader (LLM-only per CONTEXT D-E3 v1)
func NewLoader(...) (*Loader, error) {
    l := &Loader{
        // ...
        tier0Override: map[string]*atomic.Pointer[string]{
            "llm": new(atomic.Pointer[string]),
        },
    }
    // ...
}

func (l *Loader) OverrideTier0(role, url string) {
    if p, ok := l.tier0Override[role]; ok {
        p.Store(&url)
    }
}

func (l *Loader) RestoreTier0(role string) {
    if p, ok := l.tier0Override[role]; ok {
        p.Store(nil)
    }
}

// MODIFY Resolve to check override first for tier=0
func (l *Loader) Resolve(role string, tier int) (UpstreamConfig, bool) {
    s := l.snap.Load()
    if s == nil { return UpstreamConfig{}, false }
    if tier == 0 {
        if p, ok := l.tier0Override[role]; ok {
            if overrideURL := p.Load(); overrideURL != nil && *overrideURL != "" {
                u, ok := s.byRoleTier[RoleTier{Role: role, Tier: 0}]
                if !ok { return UpstreamConfig{}, false }
                u.URL = *overrideURL
                u.Name = "emergency_pod_" + role
                return u, true
            }
        }
    }
    u, ok := s.byRoleTier[RoleTier{Role: role, Tier: tier}]
    return u, ok
}
```

---

### Padrão E — testcontainers harness + httptest mock + race detector

**Source:** `gateway/internal/integration_test/setup_test.go` (sharedPG + sharedRedis via `sync.Once`) + `gateway/internal/integration_test/breaker_state_machine_test.go` (mock + Eventually pattern)
**Apply to:** todos os 8 arquivos `gateway/internal/integration_test/emerg_*_test.go`

**Build tag** (mandatory na primeira linha):
```go
//go:build integration
```

**`-race` flag mandatory** (Phase 5 D-C5; Phase 6 high concurrency com 4+ goroutines):
```bash
cd gateway && go test -race -tags=integration ./internal/integration_test/emerg_*_test.go
```

**Acceleration env-tunable** (Pitfall 13 — pass per-test):
- `PROVISION_TRIGGER_FAILED_OVER_SECONDS=1` (vs prod 120)
- `PROVISION_HEALTHY_DURATION_SECONDS=1` (vs prod 300)
- `PROVISION_IDLE_GRACE_SECONDS=1` (vs prod 300)
- `PROVISION_COLDSTART_BUDGET_SECONDS=5` (vs prod 600)
- `TickInterval: 100 * time.Millisecond` em `emerg.Deps` (vs prod 1s)

---

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `gateway/internal/emerg/lifecycle.go` | provision/cancel/destroy goroutine | request-response (Vast HTTP) + state mutation + cancel | Padrão composto: Standard Go `context.WithCancel` + RESEARCH Pattern 4 + 3-checkpoint logic. Não há analog Go in-house — `internal/proxy/director.go` é HTTP forward (sem cancel-with-destroy semantics). Construir do zero seguindo RESEARCH lines 446-528. |
| `gateway/internal/emerg/recovery.go` | leader recovery handler | DB scan + Vast.GetInstance + per-row close | Single-purpose recovery função; ninguém no codebase faz "DB SELECT WHERE X + per-row external API call + DB UPDATE close". Construir seguindo CONTEXT D-D5 spec lines 124-136 + RESEARCH Pitfall 7. |
| `gateway/internal/integration_test/emerg_leader_test.go` | concurrency test (2 reconcilers + 1 Redis) | concurrency | Phase 3-5 breaker/shed tests usam **single replica** mock; Phase 6 é o primeiro lock-distributed test. Construir spawnando 2x `go reconciler.Run(ctx)` compartilhando `rdb`, asseverando que apenas 1 escreve `emergency_lifecycles`. |

---

## Metadata

**Analog search scope:**
- `gateway/internal/breaker/` — Phase 3 D-D1 pattern (atomic + Pub/Sub + Hash mirror)
- `gateway/internal/shed/` — Phase 5 D-C1 pattern (FSM + 1Hz tick + atomic.Pointer config)
- `gateway/internal/redisx/` — Redis helpers (breaker.go, shed.go, client.go)
- `gateway/internal/proxy/` — HTTP forward + dispatcher patterns (director.go, dispatcher.go)
- `gateway/internal/upstreams/` — atomic.Pointer snapshot loader (loader.go)
- `gateway/internal/obs/` — Prometheus + Sentry init (metrics.go)
- `gateway/internal/integration_test/` — testcontainers + httptest + Eventually (setup_test.go, breaker_state_machine_test.go)
- `gateway/cmd/gatewayctl/` — CLI subcommand pattern (upstreams.go, shed.go)
- `gateway/db/migrations/` — DDL migration pattern (0010_create_billing_events.sql, 0017_evolve_upstreams_shed_thresholds.sql)
- `gateway/db/queries/` — sqlc query org (upstreams.sql, billing.sql)

**Files scanned:** 16 source files + 4 SQL files + 1 sqlc.yaml + 6 RESEARCH/CONTEXT/VALIDATION sections = **27 reads**

**Pattern extraction date:** 2026-05-13

**Key invariants for planner:**
1. **Migration number is `0019`** (NOT `0017` as CONTEXT D-B4 says — Phase 5 already consumed 0017 + 0018; verified via `ls db/migrations/`).
2. **Add `first_health_pass_at TIMESTAMPTZ` column** to migration 0019 schema (Pitfall 15 — D-D4 cost calc requires it; CONTEXT schema omitted it).
3. **Vast.ai port mapping discovery** is unresolved (Pitfall 2 + Open Question 1+5) — planner deve resolver no plan-phase via spike de 1 instance create + GET response inspection. Fallback: SSH-proxy via `golang.org/x/crypto/ssh`.
4. **redsync `Extend()` returns `(bool, error)`** — sempre verificar **ambos** `ok` e `err`; treat any non-`(true, nil)` as lost leadership (Pitfall 4).
5. **Pitfall 8 — separate ctx for unlock:** `releaseCtx, _ := context.WithTimeout(context.Background(), 2*time.Second); _, _ = mutex.UnlockContext(releaseCtx)` — NÃO usar ctx parent já cancelado.
6. **Pitfall 5 — epsilon comparison:** `if offer.DphTotal > cap+0.0001 { skip }` — float64 IEEE 754 drift.
7. **Pitfall 11 — budget alert dedupe:** day-bucket `atomic.Int64` com CAS; **NÃO** usar a versão buggy do CONTEXT (RESEARCH lines 1411-1417 has the correct version).
8. **Pitfall 13 — test acceleration:** todos os timer-based env vars devem ser env-tunable (CONTEXT já garante para 4 deles); test override via `os.Setenv` antes de `config.Load()`.
9. **Pub/Sub channel naming:** Phase 6 **subscreve** ao Phase 3 channel `gw:breaker:events` (linha 26 `redisx/breaker.go`) para detectar `local-llm` state changes; Phase 6 **publica** ao novo channel `gw:emerg:events` para visibility cross-replica do FSM 9-estado.
10. **Sentry init é noop se SENTRY_DSN vazio** (Phase 2 D-A4) — testes não precisam de mock de Sentry, mas TestEmergBudgetAlert deve usar `sentry.CurrentHub().Client().Transport.(*sentry.HTTPTransport)` para introspect sent events.

---

## PATTERN MAPPING COMPLETE

**Phase:** 6 - Auto-provisioning Emergency Pod (Vast.ai)
**Files classified:** 24 (Wave 0) + 4 modificações (`main.go`, `dispatcher.go`, `loader.go`, `obs/metrics.go`, `sqlc.yaml`)
**Analogs found:** 22 / 24 exact-or-partial-match; 2 novel (lifecycle.go, recovery.go) + 1 novel test (emerg_leader_test.go)

### Coverage
- Files with exact analog: 16 (FSM, subscribe, mirror, errors, redisx, queries, gatewayctl, integration tests, all DDL/sqlc patterns)
- Files with role-match analog: 4 (reconciler, lifecycle_test, vast/types, vast/client)
- Files with no analog (novel): 3 (lifecycle.go, recovery.go, emerg_leader_test.go) + budget.go partial

### Key Patterns Identified
- **Padrão A** — In-process autoritativo + Redis mirror (HSet + Publish, 2s timeout, fail-soft) + Pub/Sub subscribe with 1s reconnect backoff. Replica direto Phase 3+5.
- **Padrão B** — Sentry breadcrumbs por transição (zero cost) + CaptureMessage com Hub.Clone()+SetTag para terminal errors apenas. Phase 6 estados terminais: 5 (`offer_race_lost`, `health_timeout`, `leader_recovery_zombie`, `budget_exceeded`, `instance_terminal_state`).
- **Padrão C** — `promauto.NewGaugeVec`/`NewCounterVec`/`NewHistogram` em `var ...` global em `obs/metrics.go`; 7 novos collectors `gateway_emergency_*` + `gateway_vast_api_requests_total`.
- **Padrão D** — `atomic.Pointer[string]` per role para dispatcher override race-free; `OverrideTier0`/`RestoreTier0` no Loader; LLM-only em v1.
- **Padrão E** — testcontainers Postgres 16 + Redis 7 (sharedPG via sync.Once) + httptest.Server mock Vast (atomic counters + atomic.Pointer responses) + `-race` mandatório + env-tunable timer acceleration.

### File Created
`/home/pedro/projetos/pedro/gpu-ifix/.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-PATTERNS.md`

### Ready for Planning
Pattern mapping complete. Planner can reference `06-PATTERNS.md` Section 1-15 directly when authoring per-plan action sections, with concrete excerpt quotes (file path + line numbers) for every analog assignment.
