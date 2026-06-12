# Phase 12: gateway-resilience-remediation - Pattern Map

**Mapped:** 2026-06-12
**Files analyzed:** 9 modified + 3 new (12 total)
**Analogs found:** 12 / 12 (all in-tree — this is a remediation phase; every fix wires an existing primitive into a path missing it)

> All paths are absolute under `/home/pedro/projetos/pedro/gpu-ifix/`. Line numbers verified in-session against the current working tree.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `gateway/internal/primary/reconciler.go` (`evaluateReady` RES-11 death poll + D-05 trackedID repair) | reconciler/FSM | event-driven (poll tick) | `waitForReadyOrDestroy` 3-strike loop (same file, L990-1061) | exact (same file, same poll+strike idiom) |
| `gateway/internal/primary/reconciler.go` (death-confirmed path: startDrain + force-open + publish event) | reconciler/FSM | event-driven | `startDrain` (L501) + `publishPrimaryEvent` (L1335) | exact |
| `gateway/internal/primary/fsm.go` | FSM | state-machine | (read-only — no new states; D-01 reuses Ready→Draining→Asleep) | n/a — no edit expected |
| `gateway/internal/upstreams/probe.go` (`doTick` RES-12) | service (prober) | event-driven (probe tick) | `dispatcher` tier-0 resolution `Resolve(role,0)` (dispatcher.go:200) | exact (the parity target itself) |
| `gateway/internal/upstreams/health.go` (`buildHealthResponse` RES-12/D-14) | handler | request-response | same `Resolve(role,0)` swap as probe.go | role-match |
| `gateway/internal/upstreams/loader.go` (optional `ResolveTier0Roles()` helper) | loader/utility | transform | `Resolve` (L222) + `All` (L358) | exact |
| `gateway/internal/proxy/dispatcher.go` (RES-13 dial-failure fallthrough + cascade re-dispatch) | controller (dispatcher) | request-response + streaming | tier-1 cascade loop `ResolveAllTier1` (L264-280) | exact (D-08 reuses this loop) |
| `gateway/internal/proxy/transport.go` **(NEW)** — `fallthroughRoundTripper` + `isConnectionClass` | middleware (transport) | request-response | `breaker.IsSuccessful` taxonomy (breaker.go:360) | role-match (extends taxonomy to net-class) |
| `gateway/internal/proxy/errors.go` (reuse `ErrUpstreamUnreachable` sentinel; add fallthrough sentinel) | utility (errors) | n/a | existing sentinels block (L11-31) | exact |
| `gateway/internal/breaker/force_override.go` (ADD programmatic force-CLOSE write — **new code, Pitfall 4**) | service (breaker override) | CRUD (Redis key) | `gatewayctl breaker.go` force-OPEN writer (cmd/gatewayctl/breaker.go:155-167) | role-match (open-only today; close is new) |
| `gateway/internal/alert/alerter.go` + `severity.go` (subscribe `PrimaryEventsChannel` + `severityForPrimary` — **new wiring, see CRITICAL FINDING**) | service (alerter) | pub-sub | `severityForEmerg` (severity.go:161) + Subscribe block (alerter.go:139-143) | role-match |
| `gateway/internal/obs/metrics.go` (NEW counters: fallthrough, death-detected) | observability | transform | `promauto.NewCounterVec` block (metrics.go:17-23) | exact |
| `docs/CAP-01-saturation-decision.md` **(NEW, doc-only D-19)** | doc | n/a | — | no analog (doc deliverable) |

---

## CRITICAL FINDINGS (planner MUST read — they contradict RESEARCH.md assumptions)

### FINDING 1 — D-03 needs NEW alerter subscription wiring, not just a publish

RESEARCH.md A4 assumes "the Alerter's `severityForPrimary`/primary-event path already fans a `local-llm`-down event to critical … without new severity code." **This is false.** Verified at `gateway/internal/alert/alerter.go:139-143`:

```go
// alerter.go:139-143 — the Subscribe call lists ONLY 3 channels.
ps := a.rdb.Subscribe(ctx,
    redisx.BreakerEventsChannel(),
    redisx.ShedEventsChannel,
    redisx.EmergEventsChannel,
)   // <-- PrimaryEventsChannel ("gw:primary:events") is NOT subscribed
```

And `severityFor` (severity.go:55-66) has **no `case redisx.PrimaryEventsChannel`** — there is `severityForBreaker`, `severityForShed`, `severityForEmerg`, but **no `severityForPrimary`**. So a `PrimaryEvent` published by the reconciler today is consumed by NOBODY in the alert package.

**Implication for the planner:** D-03 ("Critical via existing `alert` package") requires:
1. Add `redisx.PrimaryEventsChannel` to the `a.rdb.Subscribe(...)` list (alerter.go:139).
2. Add `case redisx.PrimaryEventsChannel:` to `severityFor` (severity.go:56).
3. Write a new `severityForPrimary(payload)` mirroring `severityForEmerg` (the analog below), mapping `Reason="billing_stopped"`/`"host_death"` → `SeverityCritical` with distinct titles.

This is **new code with a clean analog**, not a no-op call. `channelsFor(SeverityCritical)` already fans out to `[chatwoot, clickup, brevo]` (severity.go:200-208) — that part is reuse.

**Alternative the planner may prefer:** instead of subscribing to PrimaryEvents in the alerter, the FSM could publish a `BreakerEvent` for `local-llm`→`open` (severityForBreaker already maps `local-llm`+`open` → Critical, severity.go:82-83). But that conflates breaker-driven and FSM-driven causes and loses the billing-stop-vs-host-yank distinct title D-03 requires. **Recommend the `severityForPrimary` path** for the distinct title.

### FINDING 2 — `evaluateReady` today never polls Vast (RES-11 is purely additive)

Verified `evaluateReady` (reconciler.go:403-434): it only (a) re-asserts the tier-0 override slot for llm/stt/tts, and (b) checks `IsInPeak` to schedule-drain. There is **no `GetInstance` call**. The death poll is brand-new logic dropped into this function, reusing the strike pattern from `waitForReadyOrDestroy`.

### FINDING 3 — breaker force-CLOSE write does not exist (Pitfall 4 confirmed)

`gateway/cmd/gatewayctl/breaker.go:155-156` writes `ForceOverrideValue{State: "open", …}` — open is the only state ever written. `ForceOverrideValue.State` is string-typed "for forward-compat with future force-close/half-open" (force_override.go:83-85). D-13 (force-close stale breakers on markReady) is **new code**: either extend `State` to `"closed"` + teach `EffectiveState`/`CheckForceOverride` to honor it, OR delete the force key + reset the gobreaker counts. Plan this as a spike-then-implement task, not a helper call.

---

## Pattern Assignments

### `gateway/internal/primary/reconciler.go` — RES-11 death poll in `evaluateReady`

**Analog:** `waitForReadyOrDestroy` 3-strike loop (same file), and `recoverOpenLifecycle` classification (same file).

**The 3-strike pattern to PORT** (reconciler.go:1004-1061) — this is the canonical confirm idiom (commit 01e7558). Copy the strike-increment / reset-on-healthy structure verbatim into the Ready tick:

```go
// reconciler.go:1004-1061 (waitForReadyOrDestroy) — PORT this into evaluateReady
inst, err := r.deps.Vast.GetInstance(ctx, instanceID)
if err != nil {
    if errors.Is(err, vast.ErrInstanceNotFound) {
        notFoundStrikes++
        if notFoundStrikes >= terminalConfirmStrikes {
            // death-confirmed via ErrInstanceNotFound
        }
        continue // (in the tick: return)
    }
    notFoundStrikes = 0 // reset on transient non-not-found GET error
    continue
}
notFoundStrikes = 0 // reset on healthy GET
if inst.IsTerminal() { // "exited"|"unknown"|"offline" — types.go:182-188 (billing-stop shape)
    terminalStrikes++
    if terminalStrikes >= terminalConfirmStrikes {
        // death-confirmed → startDrain + force-open + alert
    }
    continue
}
terminalStrikes = 0 // reset on any non-terminal observation
```

**Billing-stop vs host-yank classification** (D-01/D-03/Pitfall 5) — reuse the `status_msg` early-abort already in the same loop (reconciler.go:1031-1046). For the death classifier, distinguish:
- billing-stop: `inst.IntendedStatus == "stopped"` (+ `ActualStatus=="exited"`), or a `status_msg`/balance signal → reason `"billing_stopped"`, **DO NOT re-provision** (D-01).
- host-yank: `ErrInstanceNotFound` or `IsTerminal()` without the stopped-intent → reason `"host_death"`, schedule loop re-provisions naturally.
- **Assumption A1 (research) is UNCONFIRMED** — inspect one live billing-stopped instance JSON during planning to pin the exact field. `Instance.IntendedStatus` exists (types.go:141) but its reliability as the billing signal is unverified.

**Death-confirmed path** — chain existing functions:

```go
// startDrain already advances Ready→Draining AND RestoreTier0s the slots (reconciler.go:501)
r.startDrain(ctx, "primary_instance_dead", log)
// D-04: force-open local-* breakers (NEW force-close is D-13; force-OPEN exists via the Redis key)
// D-03: publish the distinct death event (analog below)
```

The Draining→Destroying→`evaluateDestroying` path then calls `vastutil.BestEffortDestroy` (reconciler.go:479) — D-01's BestEffortDestroy is reached for free.

**D-05 trackedID repair (PREREQUISITE — Pitfall 1):** `evaluateDestroying` reads `r.activeInstanceID.Load()` (reconciler.go:477); 11-07 showed this can be `0` after a force-up while the proxy route + open DB row still exist. Reconcile from the open lifecycle row exactly as `recoverOpenLifecycle` does:

```go
// recoverOpenLifecycle (reconciler.go:1226, 1247) — the DB-row read to mirror when activeInstanceID==0
open, err := q.GetOpenPrimaryLifecycle(ctx)
// ... open.VastInstanceID.Int64 is the instance id to repair r.activeInstanceID with
inst, err := r.deps.Vast.GetInstance(ctx, open.VastInstanceID.Int64)
```

Do BOTH (research Open Q3): repair the force-up write path so it always sets `activeInstanceID`, AND have the death poll fall back to the open `primary_lifecycles` row.

---

### `gateway/internal/primary/reconciler.go` — publish distinct death event (D-03)

**Analog:** `publishPrimaryEvent` wrapper (reconciler.go:1335) + `redisx.PrimaryEvent` struct.

The `PrimaryEvent` struct shape (verified `gateway/internal/redisx/primary.go:97`):

```go
type PrimaryEvent struct {
    Type        string `json:"type"`
    State       string `json:"state"`
    LifecycleID int64  `json:"lifecycle_id,omitempty"`
    Reason      string `json:"reason,omitempty"`   // <-- "billing_stopped" | "host_death" carry the distinct title
    SinceUnix   int64  `json:"since_unix"`
    ReplicaID   string `json:"replica_id"`
}
```

Emit on death-confirm (reconciler.go:1335 wrapper handles the nil-Redis + best-effort log):

```go
r.publishPrimaryEvent(ctx, redisx.PrimaryEvent{
    Type:        "primary_death_confirmed",
    State:       "draining",
    LifecycleID: r.activeLifecycleID.Load(),
    Reason:      reason, // "billing_stopped" (distinct title) | "host_death"
    SinceUnix:   time.Now().Unix(),
    ReplicaID:   r.deps.ReplicaID,
}, log)
```

**Then wire the alerter to consume it — see FINDING 1 and the alerter assignment below.**

---

### `gateway/internal/upstreams/probe.go` — RES-12 parity in `doTick`

**Analog:** the dispatcher's own tier-0 resolution (`dispatcher.go:200` `cfg.Loader.Resolve(cfg.Role, 0)`).

**Current bug** (probe.go:160): `doTick` enumerates `p.loader.All()` (raw snapshot, ignores override):

```go
// probe.go:156-188 — CURRENT (the RES-12 bug)
all := p.loader.All()                 // <-- raw rows, ignores tier0Override
for _, u := range all { ... probeOne(tickCtx, u) ... }
```

**The override-honoring path to copy** (loader.go:222-254) — `Resolve(role, 0)` returns the `emergency_pod_<role>` synthetic config (live pod URL, `IsEmergency=true`) when the override is active:

```go
// loader.go:222 — the EXACT path the dispatcher uses; probe must use the same
func (l *Loader) Resolve(role string, tier int) (UpstreamConfig, bool) { ... }
```

**Fix shape** (D-11/D-12) — resolve tier-0 per role through `Resolve(role,0)`; when it returns `IsEmergency==true`, skip the underlying static row this tick (it's overridden):

```go
for _, role := range []string{"llm", "stt", "tts", "embed"} {
    u, ok := p.loader.Resolve(role, 0) // honors override → emergency_pod_<role> when active
    if !ok { continue }
    // probe u (effective tier-0); if u.IsEmergency, the dead static row is overridden — don't probe it
}
```

Tier-1 gating (probe.go:170-182) — the `tier0Closed[role]` map that gates external probes (D-15) stays; just compute it from the resolved tier-0 name, not from the `All()` enumeration.

---

### `gateway/internal/upstreams/health.go` — RES-12/D-14 in `buildHealthResponse`

**Analog:** same `Resolve(role,0)` swap as probe.go (Pitfall 3 — BOTH call sites of `All()` must be fixed).

**Current bug** (health.go:128): `buildHealthResponse` also calls `loader.All()`:

```go
// health.go:128 — second All() call site (Pitfall 3: prober-clean but health still 503 if only one is fixed)
all := loader.All()
```

It already honors force-override on the breaker side via `bs.EffectiveStateSnapshot()` (health.go:140) — mirror that on the tier-0 resolution side. D-14: add `override_active bool` + `override_source string` per role to the `upstreamStatus` payload; report the replaced static row as `standby`/`overridden` without dragging the aggregate to `failed` (the aggregate logic is health.go:108-115).

---

### `gateway/internal/proxy/dispatcher.go` — RES-13 dial-failure fallthrough

**Analog:** the tier-1 cascade loop (dispatcher.go:264-280) — D-08 reuses this EXACT loop for dial-failure cascade.

**The cascade loop to reuse** (dispatcher.go:264-280):

```go
// dispatcher.go:264-280 — normal-tenant tier-1 cascade; D-08 re-enters this on tier-0 dial failure
candidates := cfg.Loader.ResolveAllTier1(cfg.Role)
if len(candidates) == 0 { /* 503 upstream_unavailable */ }
for _, t1 := range candidates {
    if cfg.Breaker.EffectiveState(t1.Name) == gobreaker.StateClosed {
        cfg.dispatchTo(w, r, t1.Name, streaming, log)
        return
    }
}
// 503 only when the chain is exhausted
```

**The routing decision tree** (dispatcher.go:225-280) is where the interception lands. Today, when `t0State == gobreaker.StateClosed` it calls `dispatchTo` and returns (dispatcher.go:226-229) — a dial failure inside that `ServeHTTP` hits `ErrorHandler` and writes the 502 directly. RES-13 must catch the dial failure (via the new RoundTripper sentinel below) and fall into the cascade loop instead.

**Sensitive invariant (D-10/RES-08 — HARD GATE):** the sensitive branch (dispatcher.go:232-253) must be UNCHANGED — a tier-0 dial failure for a sensitive tenant still produces 503 `upstream_unavailable_for_sensitive_tenant` via `writeSensitiveBlock`, NEVER tier-1. Test `TestDispatcher_SensitiveNeverFallsThrough` is the gate.

**Body re-readability (Claude's discretion C):** set `r.GetBody`/buffer before the first dispatch so the tier-1 re-dispatch can resend. Cap multipart STT at a config-bounded limit (research A2 suggests ~32 MB, UNCONFIRMED) → 503 over-cap rather than unbounded buffer.

---

### `gateway/internal/proxy/transport.go` **(NEW)** — `fallthroughRoundTripper` + `isConnectionClass`

**Analog:** `breaker.IsSuccessful` error taxonomy (breaker.go:360-377) — extend its net-class reasoning; do NOT fork a new matcher.

**The taxonomy to extend** (breaker.go:360):

```go
// breaker.go:360-377 — IsSuccessful: the canonical 4xx/5xx/timeout/conn taxonomy.
// "Timeouts, connection-reset-before-first-byte, DNS errors → failure" (the comment confirms
// connection-class is ALREADY the FALSE branch). RES-13 needs the SUBSET that is pre-byte.
func IsSuccessful(err error) bool {
    if err == nil { return true }
    if errors.Is(err, context.Canceled) { return true }
    var he *HTTPError
    if errors.As(err, &he) { return he.Status >= 400 && he.Status < 500 }
    return false // timeouts, conn-reset, DNS → failure
}
```

**The new RoundTripper** (D-06; intercept at RoundTrip, NOT ErrorHandler — Pitfall 2):

```go
// transport.go (NEW) — golang/go#14329/#16036: ErrorHandler fires too late; RoundTrip sees dial error pre-byte
type fallthroughRoundTripper struct{ base http.RoundTripper }

func (f fallthroughRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
    resp, err := f.base.RoundTrip(r)
    if err != nil && isConnectionClass(err) {
        return nil, errDialFailedFallthrough // typed sentinel the dispatcher catches
    }
    return resp, err
}

func isConnectionClass(err error) bool {
    if err == nil { return false }
    var opErr *net.OpError
    if errors.As(err, &opErr) && opErr.Op == "dial" { return true } // dial-phase = pre-byte
    if errors.Is(err, syscall.ECONNREFUSED) { return true }
    var dnsErr *net.DNSError
    if errors.As(err, &dnsErr) { return true }
    return false
}
```

**Assumption A3 (research) UNCONFIRMED** — `net.OpError.Op == "dial"` reliably identifying pre-byte must be proven by `TestIsConnectionClass` dialing a closed port (the `httptest.NewServer(...).Close()` pattern from dispatcher_test.go:47/83 gives a closed-port URL).

---

### `gateway/internal/proxy/errors.go` — reuse sentinel

**Analog:** existing sentinel block (errors.go:11-31). `ErrUpstreamUnreachable` (errors.go:13) is the existing dial-failure sink. Add ONE new sentinel `errDialFailedFallthrough` next to it (errors.go:11-31 style) for the RoundTripper→dispatcher signal. `ErrorHandler` (errors.go:35-48) stays the FINAL 502 sink for non-connection-class errors and exhausted cascades — unchanged.

---

### `gateway/internal/breaker/force_override.go` — programmatic force-CLOSE (D-04/D-13, NEW CODE)

**Analog:** the force-OPEN writer in `gateway/cmd/gatewayctl/breaker.go:155-167` (the only existing writer):

```go
// cmd/gatewayctl/breaker.go:155-167 — force-OPEN write (the ONLY state ever written today)
val := breaker.ForceOverrideValue{ State: "open", TTLSec: ..., SetBy: ..., SetAt: ... }
key := breaker.ForceOverrideKey(*upstream)
rdb.Set(ctx, key, string(buf), ttl).Err()
```

**D-04 force-OPEN from the FSM:** write the same `State:"open"` key programmatically on death-confirm for `local-llm`/`local-stt`/`local-tts`. Reuse `ForceOverrideKey` + `ForceOverrideValue` (force_override.go:76-98). `EffectiveState`/`CheckForceOverride` already honor `State=="open"` (the read path, force_override.go:115 + breaker.go:144-152).

**D-13 force-CLOSE on markReady — NEW (Pitfall 4):** no write path or read-honor exists for close. Two options for the planner to decide (research Open Q1 recommends extending the key for symmetry):
1. Extend `ForceOverrideValue.State` to `"closed"` + teach `CheckForceOverride`/`EffectiveState` (force_override.go:115; breaker.go:144-152, 231-235) to honor it (force-closed short-circuits to `StateClosed`).
2. Delete the force key + reset the gobreaker counts directly on markReady.
Either way: flag as NEW code, with a `TestMarkReady_ResetsStaleBreakers` unit test.

---

### `gateway/internal/alert/alerter.go` + `severity.go` — consume PrimaryEvents (D-03, NEW WIRING — FINDING 1)

**Analog:** `severityForEmerg` (severity.go:161-187) — mirror it for primary-death events.

**Subscribe wiring** (alerter.go:139-143) — add the channel:

```go
ps := a.rdb.Subscribe(ctx,
    redisx.BreakerEventsChannel(),
    redisx.ShedEventsChannel,
    redisx.EmergEventsChannel,
    redisx.PrimaryEventsChannel,   // <-- ADD (constant exists: redisx/primary.go:42 "gw:primary:events")
)
```

**Dispatch wiring** (severity.go:55-66) — add the case:

```go
case redisx.PrimaryEventsChannel:
    return severityForPrimary(payload)
```

**New classifier** (mirror severityForEmerg, severity.go:161):

```go
func severityForPrimary(payload []byte) (Severity, Message, error) {
    var ev redisx.PrimaryEvent
    if err := json.Unmarshal(payload, &ev); err != nil { /* return malformed err */ }
    sev := SeverityInfo
    var title string
    if ev.Type == "primary_death_confirmed" {
        sev = SeverityCritical
        switch ev.Reason {
        case "billing_stopped":
            title = "Vast account sem crédito — primary billing-stopped" // D-03 distinct title (operator-actionable)
        default:
            title = "Primary pod morto (host-yank/404)"
        }
    }
    return sev, Message{ Severity: sev, Title: title, /* Body, Fingerprint: "primary:death:"+ev.Reason */ }, nil
}
```

`channelsFor(SeverityCritical)` → `[chatwoot, clickup, brevo]` (severity.go:200-208) is the existing fan-out — reuse, no change.

---

### `gateway/internal/obs/metrics.go` — new counters (Claude's discretion E)

**Analog:** `promauto.NewCounterVec` block (metrics.go:17-23).

```go
var DialFallthroughTotal = promauto.NewCounterVec(
    prometheus.CounterOpts{ Name: "gateway_dial_fallthrough_total",
        Help: "Tier-0 dial failures that fell through to tier-1, by role and outcome." },
    []string{"role", "outcome"}) // tier1_served | chain_exhausted | sensitive_blocked

var PrimaryDeathDetectedTotal = promauto.NewCounterVec(
    prometheus.CounterOpts{ Name: "gateway_primary_death_detected_total",
        Help: "Confirmed primary-pod deaths detected on the Ready tick, by cause." },
    []string{"cause"}) // billing_stopped | host_death | not_found
```

Keep cardinality bounded (metrics.go:2-3 godoc) — `role`/`outcome`/`cause` are all low-cardinality enums.

---

## Shared Patterns

### 3-strike transient-flap confirm
**Source:** `gateway/internal/primary/reconciler.go:1004-1061` (`waitForReadyOrDestroy`; commit 01e7558)
**Apply to:** RES-11 death poll (both `ErrInstanceNotFound` and `IsTerminal()` signals — D-02)
**Pattern:** increment a per-signal strike counter; `>= terminalConfirmStrikes` confirms; reset to 0 on ANY healthy/non-terminal observation. Each signal has its OWN counter (reconciler.go:1024 resets notFound on transient GET error; L1061 resets terminal on non-terminal obs) so cross-class flaps don't accumulate.

### Vast death classifier
**Source:** `gateway/internal/emerg/vast/types.go:182-188` (`Instance.IsTerminal()`)
**Apply to:** RES-11 (the `exited`/`unknown`/`offline` billing-stop shape). Do NOT string-match Vast status in a new place (anti-pattern).

### Tier-0 override-honoring resolution
**Source:** `gateway/internal/upstreams/loader.go:222-254` (`Resolve(role, 0)`)
**Apply to:** RES-12 prober (probe.go:160) AND health (health.go:128) — BOTH call sites (Pitfall 3). This is the SAME path the dispatcher already uses at dispatcher.go:200.

### Tier-1 cascade loop
**Source:** `gateway/internal/proxy/dispatcher.go:264-280` (`ResolveAllTier1` + `tier_priority` ASC loop)
**Apply to:** RES-13/D-08 dial-failure cascade — re-enter this exact loop, dispatch to first CLOSED candidate, 502 only when exhausted.

### Breaker force-override Redis key
**Source:** `gateway/internal/breaker/force_override.go` (read) + `gateway/cmd/gatewayctl/breaker.go:155-167` (write)
**Apply to:** D-04 force-open (reuse `State:"open"`), D-13 force-close (NEW — Pitfall 4).

### Critical alert fan-out
**Source:** `gateway/internal/alert/severity.go:161-208` (`severityForEmerg` analog + `channelsFor`)
**Apply to:** D-03 — but requires NEW subscription + `severityForPrimary` (FINDING 1), not a no-op publish.

### Prometheus counter
**Source:** `gateway/internal/obs/metrics.go:17-23` (`promauto.NewCounterVec`)
**Apply to:** all new fallthrough/death-detection metrics.

---

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `docs/CAP-01-saturation-decision.md` | doc | n/a | Doc-only D-19 deliverable; analyze 11-06-EVIDENCE.md saturation numbers (chat p95 21.7s @ concurrency 50 on 1×5090). No code analog. |

**Partial-analog files** (the analog covers the *shape* but the feature is genuinely new code, not a call into an existing helper):
- `transport.go` (NEW RoundTripper) — taxonomy analog exists (`breaker.IsSuccessful`) but the RoundTripper interception is new.
- `force_override.go` force-CLOSE — write analog is open-only; close is new.
- `alerter.go`/`severity.go` PrimaryEvents consumption — classifier analog (`severityForEmerg`) exists; the subscription + classifier are new.

---

## Metadata

**Analog search scope:** `gateway/internal/{primary,upstreams,proxy,breaker,alert,obs,emerg/vast,redisx}/`, `gateway/cmd/gatewayctl/`
**Files scanned (read in-session):** reconciler.go (4 ranges), probe.go, loader.go (2 ranges), health.go, dispatcher.go (3 ranges), errors.go, force_override.go, breaker.go (2 ranges: IsSuccessful + Set methods), severity.go, alerter.go (grep), metrics.go, emerg/vast/types.go, redisx/primary.go (grep), cmd/gatewayctl/breaker.go (grep)
**Pattern extraction date:** 2026-06-12
