# Phase 12: gateway-resilience-remediation - Research

**Researched:** 2026-06-12
**Domain:** Go gateway resilience — FSM death detection, prober/dispatcher tier-0 parity, reverse-proxy dial-failure fallthrough
**Confidence:** HIGH (all three fixes are localized edits to code read in-session; the canonical bugs are proven live and the reusable assets exist in-tree)

## Summary

Phase 12 is a **remediation phase against three live-proven resilience gaps**, not a greenfield feature. Every fix lands inside the existing `gateway/` Go module (go 1.24.9, `sony/gobreaker/v2 v2.4.0`, `redis/go-redis/v9`, `jackc/pgx/v5`). The work is overwhelmingly **wiring existing primitives into a path that's currently missing them** — the FSM machinery, the 3-strike confirm pattern, the `Resolve(role,0)` override path, the tier-1 cascade loop, the `alert` fan-out, and the breaker force-override key all already exist and were read this session. The risk is not "can we build it" — it's "do we wire it into the exact tick/handler without breaking the Phase 3 no-retry-on-tool-calls and RES-08 sensitive-block invariants."

RES-11 (FSM blind to death): `evaluateReady` (reconciler.go:403) currently only re-asserts the tier-0 override and checks the schedule window — it never polls Vast for the tracked instance. The classification logic to port already exists in `waitForReadyOrDestroy` (3-strike `IsTerminal()` for `exited/stopped/offline` + 3-strike `ErrInstanceNotFound`) and in `recoverOpenLifecycle` ("instance not running; closing as orphan"). `Instance.IsTerminal()` (emerg/vast/types.go:182) already returns true for `exited/unknown/offline` — the billing-stop shape. **The 11-07 evidence adds a critical wrinkle the SEED missed: the FSM's tracked `pod_instance_id` was EMPTY after a force-up, so even a 404 path would have had no instance to poll.** RES-11 must therefore (a) re-source the tracked instance id when it's empty (the D-05 display/state-hash bug is in-scope and load-bearing, not cosmetic), and (b) add the death poll to the Ready tick.

RES-12 (prober blind to life): `probe.go:160` and `health.go:128` both call `loader.All()` (raw DB snapshot, ignores `tier0Override`); the dispatcher uses `loader.Resolve(role,0)` (honors override). The fix is to resolve tier-0 per role through `Resolve(role,0)` in both the probe tick and the health handler so prober/dispatcher/health all agree on what tier-0 IS. RES-13 (no dial-failure fallthrough): `dispatchTo` calls `httputil.ReverseProxy.ServeHTTP`; on a dial failure the proxy's `ErrorHandler` (errors.go:35) writes a 502 `upstream_unreachable` directly to `w` — committing the response with no chance to fall through. The fix is a **dispatcher-level dial-failure interception** (custom `RoundTripper` or a pre-dial probe) that, on connection-class error with breaker CLOSED, re-dispatches into the existing tier-1 cascade loop instead of letting `ErrorHandler` write the 502.

**Primary recommendation:** Split into ~4 implementation waves (RES-12 first — smallest, unblocks observability for the chaos gate; then RES-11 + D-05; then RES-13; then the dev→prod chaos UAT gate + CAP-01 doc). Reuse `IsTerminal()`, the 3-strike counter pattern from commit 01e7558, `Resolve(role,0)`, the `ResolveAllTier1` cascade loop, and `breaker.IsSuccessful`'s error-class taxonomy verbatim — do not invent new classification.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Detect primary pod death on steady-state | API/Backend (`primary` reconciler goroutine) | — | The reconciler owns the FSM lifecycle; death detection is a reconcile-tick concern, not a request-path concern |
| Post-death FSM transition + breaker force-open | API/Backend (`primary` FSM ↔ `breaker`) | — | FSM is the source of truth for tier-0 liveness; breaker state must mirror it deterministically (D-04) |
| Critical alert on death (billing-stop vs host-yank) | API/Backend (`primary` → Redis pub → `alert` Alerter) | — | Alert fan-out is already an async Redis-channel consumer; FSM only publishes an event |
| Tier-0 resolution parity | API/Backend (`upstreams` loader/prober/health) | — | All three surfaces must consult the same `Resolve(role,0)` override path |
| Dial-failure fallthrough to tier-1 | API/Backend (`proxy` dispatcher ↔ transport) | — | Request-path concern; the dispatcher owns the fallback chain decision |
| Chaos validation (kill primary) | Ops/External (ops-claude → Vast API DELETE → prod gateway) | API/Backend (audit_log = system of record) | Destructive live test; evidence read from `audit_log`, not breaker state (which the prober bug made untrustworthy) |
| Saturation/capacity decision | Doc-only (CAP-01) | — | D-19: analysis document this phase; implementation deferred |

## User Constraints (from CONTEXT.md)

### Locked Decisions

**RES-11 — FSM post-death policy**
- **D-01:** After confirmed death: Ready → Draining → Asleep + BestEffortDestroy; the **existing schedule loop** decides re-provisioning (re-provisions naturally if inside the peak window). Zero new retry logic. Billing-stop must NOT enter a provision-fail loop (no credit → would fail again).
- **D-02:** **3-strike for BOTH signals** — same counter/pattern as the 404 (commit 01e7558) applied to `actual_status in {exited, stopped}` (or `intended_status=stopped`). Vast reports transient `exited` in some scenarios; ~15s extra latency eliminates false-positives.
- **D-03:** **Critical via existing `alert` package** (severity critical → fan-out Chatwoot + ClickUp + Brevo). Billing-stop gets a distinct title ("Vast account sem crédito — primary billing-stopped"); normal death (host-yank/404) also alerts, different cause. Zero new alerting infra.
- **D-04:** On confirmed detection, FSM **opens `local-llm`/`local-stt`/`local-tts` breakers deterministically** — eliminates the window of requests hitting a dead address while observation accumulates. Breaker state = FSM truth. Combined with RES-13, dispatcher falls to tier-1 instantly.
- **D-05:** The secondary SEED-011 display bug (`gatewayctl primary state` shows empty `pod_url`/`lifecycle_id` while proxy still routes — state hash vs routing table out of sync) **is in-scope**, joined to the reconciler work. Operators depend on this command in incident runbooks.

**RES-13 — Tier-1 fallthrough on dial failure**
- **D-06:** **Connection-class ONLY** — connection refused, no route, DNS fail, dial timeout: errors BEFORE any byte reaches the upstream. Retry 100% safe (request never processed). Response timeout and 5xx keep current breaker-observation behavior (Phase 3 tool-call no-retry preserved).
- **D-07:** **Streams (SSE) also fall through when the dial fails** — dial fail is pre-byte, tier-1 retry is invisible to the client. NEVER retry after the stream began (headers/chunks sent) — which is never connection-class anyway.
- **D-08:** If the chosen tier-1 also fails dial, **cascade the entire chain** in the same `tier_priority` ASC loop (Phase 11.2 D-B5′): record candidate breaker failure → try next CLOSED. 502 only when the chain is exhausted.
- **D-09:** Tier-0 dial failure **records as a failure on the `local-*` breaker** in addition to triggering fallthrough — breaker opens naturally after N dials; subsequent requests skip the dead dial. Fallthrough = bridge; breaker = stable state.
- **D-10 (RES-08 carried forward, LOCKED):** Sensitive tenants (telefonia, cobrancas) NEVER fall to external tier-1. Tier-0 dial failure for sensitive = HTTP 503 `upstream_unavailable_for_sensitive_tenant`, as today.

**RES-12 — Prober/dispatcher parity**
- **D-11:** Probe tick resolves tier-0 via **`Resolve(role, 0)`** — same path as the dispatcher, honoring `tier0Override`. Prober and dispatcher agree on what tier-0 IS.
- **D-12:** With override active, the prober does NOT probe the static tier-0 rows that were replaced (dead prod URLs like `10.10.10.20:8000`) — their breakers don't flap. When the override drops, they get probed again.
- **D-13:** When the pod goes Ready (override activated), **force-close/reset the `local-*` breakers** that were OPEN from probing the dead URL — inherited state is stale by definition (pod just passed provisioning probes). Symmetric to the force-open on death (D-04).
- **D-14:** `/v1/health/upstreams` lists the **effective tier-0 + an override flag** ("override active, source: primary pod"); replaced static rows appear as standby/overridden without affecting the aggregate status. Health = truth of real traffic.
- **D-15:** With tier-0 healthy, tier-1 (OpenRouter/OpenAI) probes STOP — original D-A2 intent; the SEED-012 flap is what broke it. First signal of dead tier-1 comes from the RES-13 fallthrough (D-09 records on the breaker).

**Chaos + CAP-01**
- **D-16:** **Dev first, then prod as final gate.** Dev-stack validates all 3 fixes with a cheap kill; only then re-run the 11-07 recipe in prod (1×5090) expecting zero-502. Total spend ~$0.80-1.50.
- **D-17 (user-specified):** For the chaos UAT pod, **price is the 1st selection factor** — the kill is destructive, no premium shape needed; the cheapest qualified offer wins (allowlist/blocklist still apply, but cost decides).
- **D-18:** **Zero connection-class 502** during T+0..end of chaos — no `upstream_unreachable`. 503 `sensitive_block` still expected (RES-08). Degraded latency during failover OK (no p95 gate this UAT).
- **D-19 (CAP-01):** **Doc-only this phase** — analyze 11-06 data, write the decision (concurrency cap / queue / shape) in a document. Implementation becomes a future phase if it requires code.

### Claude's Discretion
- Exact shape of the death-classification code in the Ready tick (direct reuse of the recover-path helper vs extracting a shared helper).
- Connection-class detection in Go (`net.OpError`/`syscall.ECONNREFUSED` matching, etc.) and where to intercept in the proxy.
- Body buffering for retry (multipart STT) — limits and implementation.
- Shape of the `/v1/health/upstreams` payload (override-flag field names).
- New Prometheus metrics for the fallthrough/death-detection paths.
- Plan order and wave split.

### Deferred Ideas (OUT OF SCOPE)
- **CAP-01 implementation** (concurrency cap / queue / shape upgrade) — Phase 12 ships only the decision doc (D-19); code becomes a future phase.
- **Periodic tier-1 probe baseline** (detect an expired OpenRouter key before an incident) — D-15 kept the D-A2 gating; revisit if a dead-tier-1-without-detection incident occurs.
- **Immediate re-provision post-host-yank** (without waiting for the schedule tick) — D-01 chose schedule-driven; revisit if the schedule loop's MTTR proves too slow in a real incident.

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| RES-11 | FSM death detection on the Ready tick (port the 3-strike `IsTerminal` + `ErrInstanceNotFound` classification from `waitForReadyOrDestroy`/`recoverOpenLifecycle` into `evaluateReady`); advance Ready→Draining→Asleep + BestEffortDestroy; distinct critical alert for billing-stop | `Instance.IsTerminal()` (emerg/vast/types.go:182) already classifies `exited/unknown/offline`; 3-strike counter pattern in reconciler.go:944-1057; recover-path classification at reconciler.go:1247-1259; **11-07 evidence: empty tracked instance id (D-05) must be fixed first or the poll has nothing to query** |
| RES-12 | Prober + health handler resolve tier-0 via `Resolve(role,0)` (parity with dispatcher), honoring `tier0Override`; force-close stale `local-*` breakers on markReady (D-13) | `loader.Resolve(role,0)` (loader.go:222) already honors override; swap `loader.All()` calls at probe.go:160 and health.go:128; `OverrideTier0`/`Tier0OverrideURL` getters exist |
| RES-13 | Dispatcher falls through to tier-1 on connection-class dial failure (breaker CLOSED), cascading the `tier_priority` ASC chain; record dial failure on the `local-*` breaker; sensitive tenants still 503 (RES-08); zero-502 budget under chaos | `dispatchTo` (dispatcher.go:338) → `ReverseProxy.ServeHTTP` → `ErrorHandler` 502 (errors.go:35); `ResolveAllTier1` cascade loop exists (dispatcher.go:264-279); `breaker.IsSuccessful` (breaker.go:360) is the canonical error-class taxonomy |
| CAP-01 | Saturation baseline decision doc (chat p95 21.7s @ concurrency 50 on 1×5090 → queue-depth/concurrency-cap/shape decision) — doc-only acceptance | 11-06-EVIDENCE.md has the load-test saturation numbers; D-19 = document-only, no code |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

- **Go gateway runtime/style:** `gateway/` is a single Go module (go 1.24.9). Tests are Go `*_test.go` co-located per package; the codebase uses table-driven tests + `httptest`. Run via `go test ./...` from the repo root (module is `github.com/ifixtelecom/gpu-ifix`, go.mod at repo root).
- **GSD workflow enforcement:** all file-changing work goes through a GSD command (this is `/gsd:plan-phase` → `/gsd:execute-phase`). No direct edits outside the workflow.
- **No speculative language in evidence/docs:** validate every claim with file/log/code evidence (this is already the Phase 11 evidence discipline).
- **Debugger agents must NOT make speculative edits** — dev pushes go to prod-dev on commit. Diagnose first, validate with the user, then edit. (Relevant to the chaos UAT recovery steps.)
- **Sensitive tenant invariant (RES-08):** telefonia/cobrancas NEVER route to external tier-1; this is LGPD-load-bearing and LOCKED (D-10).
- **Secret hygiene in evidence:** no raw keys, DSNs, or tokens in committed `.md` (the 11-07 evidence file has an explicit attestation pattern to mirror).
- **Prod gateway location:** n8n-ia-vm, stack `/opt/ai-gateway-prod` (operator-managed `docker compose`, NOT Portainer). Dev gateway: `ai-gateway-dev` is a Portainer GitOps stack on vps-ifix-vm. Chaos kill is driven from ops-claude via the Vast API.

## Standard Stack

This phase adds **zero new third-party packages**. Every primitive needed already exists in `gateway/go.mod`.

### Core (already present, verified in go.mod)
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/sony/gobreaker/v2` | v2.4.0 | Circuit breaker per upstream; `EffectiveState`/`Execute` already wrap it | In-tree since Phase 3; D-04/D-09/D-13 drive its force-override + observation paths `[VERIFIED: gateway/go.mod]` |
| `github.com/redis/go-redis/v9` | v9.18.0 | Breaker force-override keys, alert pub/sub channels, primary event publish | The FSM→alert and force-open mechanisms are Redis-backed already `[VERIFIED: gateway/go.mod]` |
| `github.com/jackc/pgx/v5` | v5.7.1 | `primary_lifecycles` table reads/writes (sqlc-generated queries) | Lifecycle close/recover paths use it `[VERIFIED: gateway/go.mod]` |
| `github.com/prometheus/client_golang` | (in go.mod) | New fallthrough/death-detection metrics via `promauto` | Established pattern in `obs/metrics.go` `[VERIFIED: codebase grep]` |
| `net/http/httputil` (stdlib) | go 1.24.9 | `ReverseProxy` per upstream; RES-13 intercepts its Transport/ErrorHandler | All proxies are `*httputil.ReverseProxy` `[VERIFIED: codebase grep]` |
| `net`, `syscall`, `errors` (stdlib) | go 1.24.9 | Connection-class error classification for D-06 | `errors.As(err, *net.OpError)` + `errors.Is(err, syscall.ECONNREFUSED)` `[CITED: golang/go#23827, #16036]` |

### Supporting (no new installs)
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/cenkalti/backoff/v5` | v5.0.3 | `DoWithBackoff` already in `retry.go` (unwired) | Only if a wave needs bounded retry on the re-dispatch; D-06 retry is a single fall-through, not exponential — likely NOT needed |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Custom `RoundTripper` wrapping each Transport (RES-13) | Catch error in `ErrorHandler` and re-dispatch | `ErrorHandler` fires AFTER `ServeHTTP` may have written response headers — too late to fall through cleanly. The RoundTripper sees the dial error BEFORE any write. **Recommend RoundTripper.** `[CITED: golang/go#14329]` |
| Extract a shared `classifyDeath()` helper (RES-11) | Duplicate the 3-strike logic inline in `evaluateReady` | Extraction is cleaner and testable in isolation; the recover path + wait path + ready tick would all call it. **Recommend extraction** (Claude's discretion D allows it). |

**Installation:** None. `go build ./...` against the existing module.

**Version verification:**
```
$ grep -E "gobreaker|go-redis|pgx|backoff" gateway-repo/go.mod  →  verified above
go 1.24.9  (go.mod line 3)  [VERIFIED: gateway/go.mod]
```

## Package Legitimacy Audit

> Not applicable — this phase installs **zero** external packages. All dependencies are already pinned in `go.mod` (verified in-session) or are Go stdlib (`net`, `net/http/httputil`, `syscall`, `errors`). slopcheck/registry verification is moot because nothing is added.

**Packages removed due to slopcheck [SLOP] verdict:** none (no installs)
**Packages flagged as suspicious [SUS]:** none (no installs)

## Architecture Patterns

### System Architecture Diagram

```
                          ┌──────────────────────────────────────────────┐
  tenant request          │            ai-gateway (Go, prod n8n-ia-vm)    │
  ──────────────────────► │                                              │
  /v1/chat/completions    │  ┌────────────┐   3.Resolve(role,0)          │
  /v1/audio/transcriptions│  │ dispatcher │──────────────┐               │
                          │  │ (proxy/)   │              ▼               │
                          │  └─────┬──────┘     ┌──────────────────┐     │
                          │        │            │ upstreams.Loader │     │
                          │  4.breaker          │  tier0Override   │◄──┐ │
                          │   EffectiveState    │  Resolve / All   │   │ │
                          │        │            └──────────────────┘   │ │
                          │   ┌────▼────────────────────┐              │ │
                          │   │ CLOSED → dispatchTo t0  │              │ │
                          │   │  ReverseProxy.ServeHTTP │              │ │
                          │   │   │ dial OK → 200        │              │ │
                          │   │   │ dial FAIL (conn-     │              │ │  D-04/D-13
   RES-13 ───────────────┼───┼───┤  refused, no route)  │              │ │  force-open /
   intercept dial fail    │   │   │   ╔══ NEW: classify  │              │ │  force-close
   BEFORE ErrorHandler    │   │   │   ║   connection-    │              │ │  breakers
   writes 502             │   │   │   ║   class? ──► YES ─┼─► ResolveAllTier1 cascade
                          │   │   │   ║              NO ──┼─► ErrorHandler 502 (today)
                          │   │   │   ╚═══════════════════│              │ │
                          │   │   │  sensitive → 503 RES-08(unchanged)   │ │
                          │   └────────────────────────── ┘              │ │
                          │                                              │ │
                          │  ┌──────────────────────────────────────┐   │ │
   RES-11 ────────────────┼─►│ primary.Reconciler (goroutine)       │───┘ │
   evaluateReady tick     │  │  evaluateReady:                      │     │
   polls Vast for tracked │  │   ╔═ NEW: GetInstance(trackedID)     │     │
   instance; 3-strike     │  │   ║   IsTerminal()? / NotFound?      │     │
   IsTerminal/NotFound    │  │   ║   3-strike → startDrain          │     │
                          │  │   ║   + force-open local-* (D-04)    │     │
                          │  │   ║   + publish primary_event(death) ┼──┐  │
   D-05 ──────────────────┼─►│   ║   (fix empty trackedID first)    │  │  │
   state-hash ↔ routing   │  │   ╚════════════════════════════════ │  │  │
                          │  └──────────────────────────────────────┘  │  │
                          │                                             ▼  │
   RES-12 ────────────────┼─► probe.go doTick: Resolve(role,0) per role │  │
   prober resolves via    │      (was loader.All) → no flap on dead URL │  │
   Resolve(role,0)        │   health.go buildHealthResponse: same swap  │  │
                          └──────────────────────────────────┬──────────┘  │
                                                             ▼              │
                          Redis pub/sub: PrimaryEvents ──► alert.Alerter ───┘
                                                          (severity critical →
                                                           Chatwoot+ClickUp+Brevo)
                                                          D-03 billing-stop vs host-yank
```

### Recommended Project Structure (files touched — no new packages)
```
gateway/internal/primary/
├── reconciler.go      # evaluateReady (RES-11 death poll), shared classifyDeath helper, D-05 trackedID fix
├── fsm.go             # (read-only — transitions already atomic; no new states)
gateway/internal/upstreams/
├── probe.go           # doTick: loader.All() → per-role Resolve(role,0) (RES-12 / D-11,D-12)
├── health.go          # buildHealthResponse: same swap + override flag in payload (D-14)
├── loader.go          # (likely add a small ResolveTier0Roles() helper; Resolve already exists)
gateway/internal/proxy/
├── dispatcher.go      # dispatchTo → dial-failure interception + tier-1 cascade re-dispatch (RES-13)
├── transport.go (NEW) # fallthroughRoundTripper: classify connection-class dial errors (D-06)
├── errors.go          # ErrUpstreamUnreachable sentinel reused; classification helper
gateway/internal/breaker/
├── force_override.go  # ADD force-close/reset write (currently open-only — see Pitfall 4) for D-04/D-13
gateway/internal/obs/
├── metrics.go         # NEW counters: fallthrough_total, primary_death_detected_total
docs/
├── CAP-01-saturation-decision.md (NEW)  # D-19 doc-only deliverable
scripts/chaos/
├── vast-delete.sh     # (exists — re-run recipe; possibly extend for dev kill)
```

### Pattern 1: Port the 3-strike death classification into the Ready tick (RES-11)
**What:** `evaluateReady` currently never polls Vast. Add a poll of the tracked instance id that reuses the exact 3-strike confirm already in `waitForReadyOrDestroy`.
**When to use:** Every Ready-state reconcile tick (the schedule loop already calls `evaluateReady`).
**Example:**
```go
// Source: gateway/internal/primary/reconciler.go:1004-1057 (existing pattern to port)
inst, err := r.deps.Vast.GetInstance(ctx, instanceID)
if err != nil {
    if errors.Is(err, vast.ErrInstanceNotFound) {
        notFoundStrikes++
        if notFoundStrikes >= terminalConfirmStrikes { /* death-confirmed path */ }
        return
    }
    notFoundStrikes = 0
    return
}
if inst.IsTerminal() { // exited|unknown|offline — the billing-stop shape (types.go:182)
    terminalStrikes++
    if terminalStrikes >= terminalConfirmStrikes { /* death-confirmed → startDrain + force-open + alert */ }
    return
}
terminalStrikes = 0
```
**Death-confirmed path (D-01/D-04/D-03):** call `startDrain(ctx, "primary_instance_dead", log)` (already advances Ready→Draining and RestoreTier0s the slots), then force-open `local-llm`/`local-stt`/`local-tts` breakers, then publish a `PrimaryEvent` with a distinct reason (`billing_stopped` vs `host_death`) so the Alerter's `severityForPrimary`/`severityForEmerg` fans out a critical alert. `BestEffortDestroy` is reached via the existing Draining→Destroying→`evaluateDestroying` path (reconciler.go:476).

### Pattern 2: Resolve tier-0 through the override path in prober + health (RES-12)
**What:** Replace `loader.All()` enumeration with per-role `Resolve(role,0)` so the synthetic `emergency_pod_<role>` (live pod URL) is probed, not the dead static row.
**When to use:** `probe.go` `doTick` and `health.go` `buildHealthResponse`.
**Example:**
```go
// Source: gateway/internal/upstreams/loader.go:222 (existing override-honoring path)
for _, role := range []string{"llm", "stt", "tts", "embed"} {
    u, ok := p.loader.Resolve(role, 0) // honors tier0Override → emergency_pod_<role> when active
    if !ok { continue }
    // probe u (the EFFECTIVE tier-0), not the raw static row
}
```
For D-12 (don't probe the replaced static rows): when `Resolve(role,0)` returns `IsEmergency=true`, skip the underlying static row in the same tick (it's overridden). For D-14, `buildHealthResponse` adds an `override_active bool` + `override_source string` per role and reports the static row as `standby`/`overridden` without dragging the aggregate to `failed`.

### Pattern 3: Dial-failure fallthrough via a custom RoundTripper (RES-13)
**What:** Wrap each upstream proxy's `Transport` in a `RoundTripper` that classifies the error. On a connection-class error (pre-byte), it returns a typed sentinel the dispatcher recognizes — the dispatcher then re-dispatches into the `ResolveAllTier1` cascade. On any post-byte error, it behaves exactly as today (ErrorHandler 502, breaker observation).
**When to use:** Tier-0 dispatch for normal tenants only (sensitive → 503 unchanged per D-10).
**Example:**
```go
// Source: golang/go#14329, #16036 — ErrorHandler fires too late; intercept at RoundTrip
type fallthroughRoundTripper struct{ base http.RoundTripper }
func (f fallthroughRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
    resp, err := f.base.RoundTrip(r)
    if err != nil && isConnectionClass(err) {
        return nil, errDialFailedFallthrough // typed sentinel; dispatcher catches and re-dispatches
    }
    return resp, err
}
// D-06 classification (reuse breaker.IsSuccessful taxonomy, breaker.go:360):
func isConnectionClass(err error) bool {
    var opErr *net.OpError
    if errors.As(err, &opErr) {
        // dial-phase op ("dial") with ECONNREFUSED / no route / DNS = pre-byte
        if opErr.Op == "dial" { return true }
    }
    return errors.Is(err, syscall.ECONNREFUSED) || isDNSError(err)
}
```
**Body re-readability (Claude's discretion C):** the dispatcher must set `r.GetBody` (or buffer the body) before the first dispatch so the tier-1 re-dispatch can resend it. For multipart STT (`/v1/audio/transcriptions`) the body can be large — buffer with a cap (recommend a config-bounded limit, e.g. 32 MB, matching the STT upload ceiling) and fail to 503 if over-cap rather than buffering unbounded. Non-streaming chat/embed bodies are small (token-capped at 16k/8k) so buffering is cheap.

### Anti-Patterns to Avoid
- **Re-dispatching after any bytes were written to the client** — D-07: once SSE chunks or response headers are sent, NEVER fall through (and a post-first-byte error is never connection-class anyway). The RoundTripper only signals fallthrough for pre-byte dial errors.
- **Adding a new FSM state for "dead"** — D-01 reuses the existing Ready→Draining→Asleep transitions. No new states.
- **Inventing new error classification** — reuse `breaker.IsSuccessful`'s taxonomy and `Instance.IsTerminal()`. Don't string-match Vast status in a new place.
- **Letting the death poll run when trackedID is empty** — D-05: fix the empty `pod_instance_id` first (re-source from Vast list or repair the state-hash↔routing sync), otherwise the poll silently no-ops exactly as it did in 11-07.
- **Force-opening sensitive routing into tier-1** — D-04 force-opens the breaker (so dispatch leaves tier-0) but RES-08/D-10 still routes sensitive to 503, not external tier-1.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Vast death classification | New status-string matcher | `Instance.IsTerminal()` (emerg/vast/types.go:182) | Already classifies `exited/unknown/offline`; the billing-stop shape is covered |
| Transient-flap tolerance | New debounce | The 3-strike counter from commit 01e7558 (reconciler.go:944) | Proven against false-positive terminal closes (UAT lifecycle 4/2) |
| Tier-0 override resolution | New override lookup | `loader.Resolve(role,0)` (loader.go:222) | The dispatcher already uses it; parity is the whole point of RES-12 |
| Tier-1 cascade | New fallback loop | `ResolveAllTier1` + the `tier_priority` ASC loop (dispatcher.go:264) | D-08 explicitly reuses this loop |
| Error-class taxonomy | New net.Error matcher | `breaker.IsSuccessful` (breaker.go:360) as the reference | Already distinguishes 4xx/5xx/timeout/conn — extend, don't fork |
| Critical alert fan-out | New alerting | `alert.Alerter` + Redis `PrimaryEvents`/`EmergEvents` channels | D-03: FSM only publishes an event; the Alerter does Chatwoot+ClickUp+Brevo + dedup |
| Breaker deterministic open/close | New breaker state writes | `gw:breaker:force:{name}` force-override key (force_override.go:71) | D-04/D-13 invoke the existing mechanism — BUT see Pitfall 4 (force-CLOSE not yet implemented) |
| Best-effort destroy | New Vast destroy loop | `vastutil.BestEffortDestroy` (used throughout reconciler.go) | Idempotent, orphan-safe |

**Key insight:** Phase 12 is ~80% wiring existing primitives into a path that's missing them. The only genuinely new code is (1) the dial-failure RoundTripper + dispatcher re-dispatch glue (RES-13), (2) a programmatic force-CLOSE write (the force-override is open-only today — Pitfall 4), and (3) the D-05 state-hash/trackedID repair. Everything else is a function call into code that already works.

## Runtime State Inventory

> This is a remediation phase on a running prod gateway, not a rename. The "runtime state" that matters is the **prod gateway's live in-memory + Redis state** that the fixes interact with. Verified in-session against the code + 11-06/11-07 evidence.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | `primary_lifecycles` Postgres table (`bd_ai_gateway_prod`): the open lifecycle row whose `vast_instance_id` the death poll reads. 11-07 showed the FSM in-memory `activeInstanceID` can be 0/empty while a row + proxy route still exist. | Code edit: RES-11 must read `activeInstanceID` AND reconcile against the open DB row (recover-path already does `GetOpenPrimaryLifecycle`); D-05 fixes the in-memory↔display divergence. No data migration. |
| Live service config | Redis `gw:breaker:force:{name}` force-override keys (TTL'd, written by gatewayctl today). FSM-driven force-open (D-04) and force-close (D-13) write these programmatically. Redis `tier0Override` is **in-memory in the Loader** (not Redis) — per-replica, rebuilt on boot. | Code edit: add a programmatic force-CLOSE write (Pitfall 4). The in-memory override needs no migration (rebuilt by markReady/recover). |
| OS-registered state | None — gateway is a docker container; no Task Scheduler / systemd units embed the changed behavior. The prod stack `/opt/ai-gateway-prod` is operator-managed `docker compose` (redeploy = pull new image + `up`). | None — verified: no OS-level state references these code paths. |
| Secrets/env vars | `VAST_AI_API_KEY` (chaos kill), `AI_GATEWAY_PG_DSN` (audit_log evidence), tenant keys (`ifix_sk_*`). None are renamed; the chaos UAT reads them at runtime. Prod static rows `UPSTREAM_LLM_URL=http://10.10.10.20:8000` / STT `:8001` are the dead addresses RES-12 stops probing. | None — verified: no secret/env rename. The dead static URLs stay as standby rows (D-12/D-14), not deleted. |
| Build artifacts | The gateway binary is rebuilt from source per deploy (GHCR image). No stale egg-info/compiled-name artifacts. Dev = `ai-gateway-dev` Portainer GitOps (push `develop` → build → webhook); prod = manual image pull on n8n-ia-vm. | None for build artifacts. Deploy path: dev validates via Portainer redeploy; prod via operator `docker compose` pull+up before the prod chaos gate (D-16). |

**The canonical question (post-edit runtime state):** After the code is merged, the prod gateway still holds (a) the in-memory `tier0Override` map and breaker states — rebuilt correctly on next pod cycle; (b) any active `gw:breaker:force:*` Redis keys — TTL'd; (c) the open `primary_lifecycles` row — reconciled by recover-path on boot. **No manual state surgery is required for the code fixes**; the only live action is the operator deploying the new image to prod before the D-18 chaos gate.

## Common Pitfalls

### Pitfall 1: The empty tracked instance id defeats the death poll (the 11-07 killer)
**What goes wrong:** RES-11 adds a Vast poll to `evaluateReady`, but the FSM's `activeInstanceID` is 0 (the D-05 display/state-hash bug after a force-up), so `GetInstance(0)` has nothing to poll and the death is never detected — exactly the 11-07 reproduction.
**Why it happens:** The proxy routing table (the live pod URL in `tier0Override`/`activePodURLs`) and the FSM state-hash (`activeInstanceID`/`activeLifecycleID`) can diverge after a force-up recovery path. The 11-07 evidence is explicit: "the FSM's tracked `pod_instance_id` was already EMPTY... so the steady-state loop had no instance to poll."
**How to avoid:** Fix D-05 FIRST (it's in-scope, not cosmetic). When the death poll finds `activeInstanceID == 0` but `activePodURLs` is non-nil, reconcile by reading the open `primary_lifecycles` row's `vast_instance_id` (the recover-path already does this) — or repair the force-up path so it always populates `activeInstanceID`. **Plan RES-11 and D-05 in the same wave; D-05 is a prerequisite, not a nice-to-have.**
**Warning signs:** `gatewayctl primary state` shows `state=ready` with empty `pod_url`/`lifecycle_id`; the death poll logs nothing on a real kill.

### Pitfall 2: Falling through after the response is committed (SSE)
**What goes wrong:** A naive RES-13 catches the dial error in `ErrorHandler` and tries to re-dispatch, but `ServeHTTP` may already have written headers (especially for streaming), producing a corrupted/double-write response.
**Why it happens:** `httputil.ReverseProxy.ErrorHandler` fires after `ServeHTTP` has begun; for SSE the `FlushInterval: -1` path flushes per chunk.
**How to avoid:** Intercept at the `RoundTrip` level (before any write), not in `ErrorHandler`. A connection-class dial error is by definition pre-byte (D-07), so the response is guaranteed uncommitted at that point. Return a typed sentinel; let the dispatcher choose tier-1.
**Warning signs:** Tests show partial bodies or "superfluous WriteHeader" warnings in logs.

### Pitfall 3: Probe tick + dispatcher disagree because only ONE was fixed
**What goes wrong:** RES-12 swaps `probe.go` to `Resolve(role,0)` but leaves `health.go`'s `buildHealthResponse` on `loader.All()` — the prober stops flapping but `/v1/health/upstreams` still reports `failed` (the chaos gate evidence surface stays broken).
**Why it happens:** Two independent call sites of `loader.All()` (probe.go:160 AND health.go:128) both need the same fix.
**How to avoid:** Fix BOTH in the RES-12 wave. The D-18 chaos gate's evidence partly relies on `/v1/health/upstreams` telling the truth.
**Warning signs:** Prober clean but health endpoint still 503 with a healthy pod.

### Pitfall 4: Breaker force-override is OPEN-only — D-13 force-CLOSE is not yet implemented
**What goes wrong:** D-04 (force-open on death) maps cleanly to the existing `gw:breaker:force:{name}` write, but D-13 (force-CLOSE/reset on markReady) has NO existing write path — `ForceOverrideValue.State` is "currently always 'open' (Plan 04 ships open-only; the field is string-typed for forward-compat with future force-close/half-open semantics)" (force_override.go:84).
**Why it happens:** The force-override mechanism was built open-only in Phase 06.9 Plan 04.
**How to avoid:** D-13 requires either (a) implementing the force-close state write (extend `ForceOverrideValue.State` to "closed" + teach `EffectiveState`/`CheckForceOverride` to honor it), or (b) a direct breaker reset via deleting the force key + resetting the gobreaker counts. **Flag this as new code, not a call into an existing helper.** Confirm the cleanest approach during planning.
**Warning signs:** A plan task that says "force-close the breaker" with no corresponding write API.

### Pitfall 5: Billing-stop re-provision loop burns money
**What goes wrong:** After a billing-stop death, D-01 lets the schedule loop re-provision — but with no Vast credit, the re-provision fails, and a naive loop retries forever, each attempt a failed Vast bid.
**Why it happens:** Billing-stop and host-yank both produce `IsTerminal()`, but only billing-stop means "provisioning will keep failing."
**How to avoid:** D-01 is explicit: "Billing-stop não entra em loop de provision-fail." The death classifier must distinguish billing-stop (`intended_status=stopped` + balance signal / `account lacks credit`) from host-yank (404/host death), emit the distinct billing-stop alert (D-03), and NOT trigger re-provision for the credit case. The existing `vast_status_msg_error` early-abort + cooldown gate (reconciler.go:1032) is the pattern to lean on.
**Warning signs:** Repeated `provisionLifecycle` attempts in logs after a billing-stop; cumulative Vast spend climbing with no Ready pod.

### Pitfall 6: Chaos gate verdict polluted by saturation 5xx
**What goes wrong:** The prod chaos re-run drives load at concurrency 50 (11-06's saturation point on 1×5090, chat p95 21.7s) and gets organic 5xx/timeout noise that muddies the "zero connection-class 502" gate.
**Why it happens:** 1×5090 saturates at ~50 concurrency; 11-07 deliberately dropped to `--max-concurrency 20` to keep the chaos signal clean.
**How to avoid:** Run the chaos load at moderate concurrency (~20, per 11-07) — D-18 gates specifically on connection-class `upstream_unreachable`, not p95. Read evidence from `audit_log.error_code`, not aggregate latency.
**Warning signs:** Evidence table mixes `gateway_timeout`/`upstream_5xx` with `upstream_unreachable` and can't cleanly attribute the 502 budget.

## Code Examples

### Detect connection-class dial error (D-06)
```go
// Source: golang/go#23827 (dialing errors), breaker.go:360 taxonomy
import ("errors"; "net"; "syscall")

func isConnectionClass(err error) bool {
    if err == nil { return false }
    var opErr *net.OpError
    if errors.As(err, &opErr) && opErr.Op == "dial" {
        return true // dial-phase failure = pre-byte, safe to re-dispatch
    }
    if errors.Is(err, syscall.ECONNREFUSED) { return true }
    var dnsErr *net.DNSError
    if errors.As(err, &dnsErr) { return true }
    var netErr net.Error
    if errors.As(err, &netErr) && netErr.Timeout() {
        // dial timeout (pre-byte) — distinguish from ResponseHeaderTimeout
        // by checking it surfaced from RoundTrip before any response.
        return true
    }
    return false
}
```

### Publish a distinct primary-death event for the Alerter (D-03)
```go
// Source: reconciler.go:540 publishPrimaryEvent pattern + alert/severity.go:36
r.publishPrimaryEvent(ctx, redisx.PrimaryEvent{
    Type:        "primary_death_confirmed",
    State:       "draining",
    LifecycleID: r.activeLifecycleID.Load(),
    Reason:      reason, // "billing_stopped" (distinct title) | "host_death"
    SinceUnix:   time.Now().Unix(),
    ReplicaID:   r.deps.ReplicaID,
}, log)
// severityForPrimary/severityForEmerg maps local-llm down → SeverityCritical →
// channelsFor(critical) = [chatwoot, clickup, brevo]  (severity.go:200)
```

### New Prometheus metrics (Claude's discretion E)
```go
// Source: obs/metrics.go promauto pattern (lines 17-95)
var DialFallthroughTotal = promauto.NewCounterVec(
    prometheus.CounterOpts{Name: "gateway_dial_fallthrough_total",
        Help: "Tier-0 dial failures that fell through to tier-1, by role and outcome."},
    []string{"role", "outcome"}) // outcome: tier1_served | chain_exhausted | sensitive_blocked

var PrimaryDeathDetectedTotal = promauto.NewCounterVec(
    prometheus.CounterOpts{Name: "gateway_primary_death_detected_total",
        Help: "Confirmed primary-pod deaths detected on the Ready tick, by cause."},
    []string{"cause"}) // cause: billing_stopped | host_death | not_found
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Death detection only at startup (recover path) | Death detection on steady-state Ready tick | Phase 12 (this) | Autonomous failover; no operator force-down needed |
| Prober/health on `loader.All()` (raw rows) | `Resolve(role,0)` (override-honoring) | Phase 12 (this) | Breakers + health endpoint tell the truth |
| Fallback only when breaker OPEN | Fallback also on connection-class dial failure (breaker CLOSED) | Phase 12 (this) | Closes the 100×502 window (T+0..first-byte) |
| Breaker force-override open-only | Add programmatic force-close/reset (D-13) | Phase 12 (this) | Symmetric FSM-driven breaker control on pod life/death |

**Deprecated/outdated:**
- Relying on breaker state or `/v1/health/upstreams` as chaos evidence — until RES-12 ships, the 11-07 deviation #2 stands: read `audit_log.upstream`+`error_code` as the authoritative dispatcher-behavior signal.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `intended_status=stopped` / a "balance"/"credit" signal is reliably distinguishable from host-yank in the Vast `GetInstance` response (needed for D-01/D-03 billing-stop vs host-death split) | Pitfall 5, Pattern 1 | If Vast doesn't surface a credit/billing signal cleanly, the billing-stop alert can't be distinguished from host-death at detection time — may need a balance API call or `status_msg` parse. Confirm the exact Vast field during planning by inspecting a live billing-stopped instance. |
| A2 | Multipart STT body cap of ~32 MB is acceptable for buffering-to-retry (D-06/D-07 streaming + body re-read) | Pattern 3 | If real STT uploads exceed the cap, those requests can't fall through and would 503 instead of failing over. Confirm the actual STT upload ceiling against the audio proxy / tenant usage. |
| A3 | `net.OpError.Op == "dial"` reliably identifies pre-byte failures across the Go 1.24 transport (vs response-header-timeout surfacing differently) | Code Examples (isConnectionClass) | A misclassification could either (a) fall through on a non-safe error or (b) miss a dial failure. Mitigated by gating on `Op=="dial"` + ECONNREFUSED; verify with a unit test that dials a closed port. |
| A4 | The Alerter's `severityForPrimary`/primary-event path already fans a `local-llm`-down event to critical (the exact channel D-03 needs) without new severity code | Pattern: publish death event | If primary events don't currently map to a critical severity, D-03 needs a small `severity.go` addition (still no new infra). Verify `severityForEmerg`/primary mapping covers a primary-death reason. |
| A5 | Re-running `scripts/chaos/vast-delete.sh` against the new prod image reproduces the same kill cleanly (recipe unchanged) | Validation Architecture | If the recipe needs adaptation for the fixed gateway (e.g., trackedID now populated so `resolve_instance_id` works), minor script edit. Low risk — script already has a Vast-list fallback. |

**These five assumptions are the items discuss-phase / planning should confirm before locking the corresponding tasks.** A1 and A4 are the highest-leverage (they shape the death classifier and the alert wiring).

## Open Questions (RESOLVED)

1. **D-13 force-close mechanism** — RESOLVED: extend force-override key with State="closed" honored by EffectiveState (Plan 12-01 Task 2)
   - What we know: The force-override Redis key is open-only today (force_override.go:84); the field is string-typed for forward-compat.
   - What's unclear: Whether to extend `State` to "closed" (and teach `EffectiveState` to honor it) vs delete-key + reset gobreaker counts directly on markReady.
   - Recommendation: Plan a small spike/decision task in the RES-12 wave (D-13 belongs with markReady). Prefer extending the existing key semantics for symmetry with D-04's force-open.

2. **Billing-stop signal source (A1)** — RESOLVED: dual-signal (IntendedStatus==stopped primary + ActualStatus==exited && StatusMsg credit/account fallback), confirmed against live JSON before merge (Plan 12-02 Task 2)
   - What we know: `Instance.IsTerminal()` covers the `exited` shape; 11-06 saw `actual_status=exited`, `intended_status=stopped`, balance -$0.056.
   - What's unclear: Whether `GetInstance` returns a balance/credit field, or whether billing-stop must be inferred from `intended_status=stopped` + a separate account-balance call.
   - Recommendation: During planning, inspect one live billing-stopped instance's full JSON to pin the exact field for the distinct alert.

3. **D-05 trackedID repair: source of truth** — RESOLVED: both — repair write path AND fall back to open primary_lifecycles row at poll time (Plan 12-02 Task 1)
   - What we know: in-memory `activeInstanceID` can be 0 while the proxy route + open DB row exist.
   - What's unclear: Whether to repair the force-up path (always set `activeInstanceID`) or reconcile from the DB row at poll time, or both.
   - Recommendation: Do both — repair the write path AND have the death poll fall back to the open `primary_lifecycles` row (defense in depth; the recover path already reads that row).

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | Build + unit/integration tests | ✓ (assumed on dev host) | go 1.24.9 (go.mod) | — |
| Vast.ai API | Chaos UAT kill (dev + prod) | ✓ | n/a (REST) | — (chaos gate is hard-required by D-16/D-18) |
| Vast.ai credit | Cheapest-qualified pod for dev + prod chaos (~$0.80-1.50 total) | ✗ verify first | n/a | **Check credit BEFORE provisioning** (per MEMORY: resilience recovery starts with credit check) |
| Prod gateway (n8n-ia-vm `/opt/ai-gateway-prod`) | Final chaos gate (D-16 prod re-run) | ✓ | operator-managed docker compose | — |
| Dev gateway (`ai-gateway-dev`, vps-ifix-vm) | Dev-first validation (D-16) | ✓ | Portainer GitOps stack | — |
| Postgres `bd_ai_gateway_prod.audit_log` | Chaos evidence (system of record) | ✓ | DigitalOcean managed | — |
| `scripts/chaos/vast-delete.sh` | Re-run the 11-07 recipe | ✓ (in repo, 0755, bash -n clean) | — | — |

**Missing dependencies with no fallback:**
- **Vast.ai credit** must be confirmed before either chaos run (the 11-06 billing-stop happened *because* credit hit zero). This is an operator pre-flight, not a code blocker — but the prod gate (D-18) cannot pass without a live pod.

**Missing dependencies with fallback:**
- None — all code-level dependencies are in-tree.

## Validation Architecture

> Nyquist validation is ENABLED (`workflow.nyquist_validation: true`). Each fix gets unit/integration coverage in the dev stack, then the prod chaos re-run is the phase gate (D-16/D-18).

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `net/http/httptest` (table-driven; established across `primary/`, `proxy/`, `upstreams/`) |
| Config file | none — `go test` convention; module `github.com/ifixtelecom/gpu-ifix`, go.mod at repo root |
| Quick run command | `go test ./gateway/internal/primary/... ./gateway/internal/upstreams/... ./gateway/internal/proxy/...` |
| Full suite command | `go test ./...` |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| RES-11 | Ready tick polls Vast; 3-strike `IsTerminal()` → startDrain + force-open + alert event | unit | `go test ./gateway/internal/primary/ -run TestEvaluateReady_DeathDetection` | ❌ Wave 0 (extend reconciler_test.go) |
| RES-11 | 3-strike confirm resets on a non-terminal observation (no false positive) | unit | `go test ./gateway/internal/primary/ -run TestEvaluateReady_TransientExitedDoesNotDrain` | ❌ Wave 0 |
| RES-11/D-05 | Death poll falls back to open `primary_lifecycles` row when `activeInstanceID==0` | unit | `go test ./gateway/internal/primary/ -run TestEvaluateReady_EmptyTrackedIDReconciles` | ❌ Wave 0 |
| RES-11/D-01 | Billing-stop does NOT trigger re-provision loop; host-death does (within window) | unit | `go test ./gateway/internal/primary/ -run TestDeath_BillingStopNoReprovision` | ❌ Wave 0 |
| RES-12 | `probe.go` doTick resolves tier-0 via `Resolve(role,0)`; no flap on dead static row when override active | unit | `go test ./gateway/internal/upstreams/ -run TestProbe_HonorsTier0Override` | ❌ Wave 0 (extend probe_test.go) |
| RES-12/D-14 | `buildHealthResponse` reports effective tier-0 + override flag; aggregate not `failed` with healthy pod | unit | `go test ./gateway/internal/upstreams/ -run TestHealth_OverrideEffectiveTier0` | ❌ Wave 0 (extend health_test.go) |
| RES-12/D-13 | markReady force-closes stale `local-*` breakers | unit | `go test ./gateway/internal/primary/ -run TestMarkReady_ResetsStaleBreakers` | ❌ Wave 0 |
| RES-13 | Connection-class dial failure (breaker CLOSED) falls through to tier-1; serves 200 | integration | `go test ./gateway/internal/proxy/ -run TestDispatcher_DialFailureFallsThrough` | ❌ Wave 0 (extend dispatcher_test.go) |
| RES-13/D-08 | Cascade: tier-1 dial failure tries next candidate; 502 only when chain exhausted | integration | `go test ./gateway/internal/proxy/ -run TestDispatcher_CascadeOnDialFailure` | ❌ Wave 0 |
| RES-13/D-10 | Sensitive tenant dial failure → 503 `upstream_unavailable_for_sensitive_tenant`, NEVER tier-1 | integration | `go test ./gateway/internal/proxy/ -run TestDispatcher_SensitiveNeverFallsThrough` | ❌ Wave 0 |
| RES-13/D-06 | Response-timeout / 5xx do NOT fall through (only connection-class) | unit | `go test ./gateway/internal/proxy/ -run TestIsConnectionClass` | ❌ Wave 0 |
| ALL | Dev-stack live kill validates the 3 fixes together (cheap pod) | manual-UAT | `scripts/chaos/vast-delete.sh` on dev (autonomous:false) | ✓ (script exists) |
| D-18 | Prod chaos re-run: zero connection-class 502 in T+0..end | manual-UAT (HARD GATE) | `scripts/chaos/vast-delete.sh` on prod + audit_log query | ✓ (recipe exists, 11-07) |

### Sampling Rate
- **Per task commit:** the package-scoped quick run for the package touched (`go test ./gateway/internal/<pkg>/...`).
- **Per wave merge:** `go test ./...` (full Go suite green).
- **Phase gate:** Full suite green → dev chaos kill PASS (D-16) → prod chaos re-run PASS with zero connection-class 502 (D-18) before `/gsd:verify-work`.

### Wave 0 Gaps
- [ ] `gateway/internal/primary/reconciler_test.go` — add `TestEvaluateReady_DeathDetection`, `_TransientExitedDoesNotDrain`, `_EmptyTrackedIDReconciles`, `TestDeath_BillingStopNoReprovision`, `TestMarkReady_ResetsStaleBreakers` (covers RES-11, D-01, D-05, D-13)
- [ ] `gateway/internal/upstreams/probe_test.go` — add `TestProbe_HonorsTier0Override` (RES-12/D-11/D-12)
- [ ] `gateway/internal/upstreams/health_test.go` — add `TestHealth_OverrideEffectiveTier0` (RES-12/D-14)
- [ ] `gateway/internal/proxy/dispatcher_test.go` — add `TestDispatcher_DialFailureFallsThrough`, `_CascadeOnDialFailure`, `_SensitiveNeverFallsThrough` (RES-13/D-08/D-10); these can use `httptest` servers + a closed-port URL to force a real dial failure (existing test style at dispatcher_test.go:47)
- [ ] `gateway/internal/proxy/transport_test.go` (NEW) — `TestIsConnectionClass` dialing a closed port (RES-13/D-06)
- [ ] Framework install: none — Go `testing` already present.

*The Go test infra is mature (29 test files across the three target packages); all gaps are new test functions in existing files plus one new `transport_test.go`.*

## Security Domain

> `security_enforcement` is not present in config (config.json has no such key) — treat as enabled. This phase is internal-resilience, but two ASVS-relevant invariants apply.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no | No auth change; tenant API-key auth untouched |
| V3 Session Management | no | Stateless gateway |
| V4 Access Control | yes | RES-08/D-10: sensitive tenants (telefonia, cobrancas) NEVER route to external tier-1 — the dial-failure fallthrough MUST preserve this (a fallthrough bug that routes sensitive data to OpenRouter is an LGPD data-residency violation) |
| V5 Input Validation | partial | Token-cap pre-dispatch (16k/8k) already enforced; body buffering for retry must not bypass it |
| V6 Cryptography | no | No crypto change |
| V7 Error Handling / Logging | yes | Death-detection + fallthrough must not log secrets; alert bodies must not leak tenant payloads (the alert system already dedups + the 11-07 attestation pattern applies to evidence) |

### Known Threat Patterns for this stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Sensitive tenant data leaks to external tier-1 via a fallthrough bug | Information Disclosure | D-10/RES-08 invariant: sensitive → 503, never fall through. Dedicated test `TestDispatcher_SensitiveNeverFallsThrough` is a HARD gate. |
| Billing-stop re-provision loop drains Vast credit (financial DoS, self-inflicted) | Denial of Service | D-01: billing-stop does NOT re-provision; distinct alert prompts operator to add credit (Pitfall 5) |
| Orphaned Vast pod after death (runaway spend) | (resource leak) | `vastutil.BestEffortDestroy` on confirmed death (D-01) + the 11-07 cleanup verified count=0 |
| Secret leakage in death/fallthrough logs or alert bodies | Information Disclosure | Reuse the existing structured-error shape (no payload); evidence attestation grep gate from 11-07 |

## Sources

### Primary (HIGH confidence)
- `gateway/internal/primary/reconciler.go` — `evaluateReady` (L403), `waitForReadyOrDestroy` 3-strike (L931-1057), `recoverOpenLifecycle` (L1220-1287), `startDrain`/`markReady`/`closeLifecycle` — read in full
- `gateway/internal/emerg/vast/types.go:138-188` — `Instance` struct + `IsTerminal()`/`IsActive()` (the death classifier)
- `gateway/internal/upstreams/loader.go` — `Resolve(role,0)` (L222), `All()` (L358), `OverrideTier0`/`Tier0OverrideURL`
- `gateway/internal/upstreams/probe.go:156-189` — `doTick` (`loader.All()` at L160) + tier-1 gating
- `gateway/internal/upstreams/health.go` — `buildHealthResponse` (`loader.All()` at L128)
- `gateway/internal/proxy/dispatcher.go` — routing decision tree (L195-280), `dispatchTo` (L338), `ResolveAllTier1` cascade loop
- `gateway/internal/proxy/errors.go` — `ErrorHandler` 502 (the dial-failure sink) + sentinels
- `gateway/internal/proxy/chat.go`/`audio.go` — `ReverseProxy` Transport construction
- `gateway/internal/breaker/breaker.go:354-377` — `IsSuccessful` error taxonomy; `force_override.go` (open-only override)
- `gateway/internal/alert/severity.go`/`alerter.go` — critical fan-out (Chatwoot+ClickUp+Brevo), Redis-channel consumption
- `gateway/go.mod` — go 1.24.9, gobreaker v2.4.0, go-redis v9.18.0, pgx v5.7.1, backoff v5.0.3 (versions verified)
- `.planning/seeds/SEED-011-*.md`, `SEED-012-*.md` — canonical bug definitions
- `.planning/phases/11-prod-hardening/11-07-EVIDENCE.md` — live chaos reproduction (100×502, empty trackedID finding, chaos recipe)
- `.planning/phases/12-.../12-CONTEXT.md` — locked decisions D-01..D-19

### Secondary (MEDIUM confidence)
- golang/go#14329 (custom ErrorHandler), #16036 (ReverseProxy vs transport retries), #23827 (expose dialing errors) — confirm the RoundTripper-not-ErrorHandler approach for RES-13

### Tertiary (LOW confidence)
- WebSearch summary on ReverseProxy dial-vs-response error distinction — corroborates the architectural choice; the concrete classifier is grounded in the in-tree `breaker.IsSuccessful` and Go stdlib `net.OpError`.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — zero new deps; every primitive read in-session and version-verified in go.mod
- Architecture (RES-11/RES-12): HIGH — both are localized swaps into code whose target functions and reusable helpers were read line-by-line
- Architecture (RES-13): MEDIUM-HIGH — the RoundTripper interception pattern is the correct Go idiom (corroborated by golang/go issues) but is the only genuinely-new code; the body-buffering cap (A2) and `net.OpError` classification (A3) need a confirming unit test
- Pitfalls: HIGH — Pitfalls 1 (empty trackedID), 3 (two All() call sites), 4 (force-close not implemented), 5 (billing-stop loop) are all grounded in read code + live evidence

**Research date:** 2026-06-12
**Valid until:** 2026-07-12 (stable — the code is in-tree and the bugs are proven; only A1/A4 Vast-field assumptions could shift, and only if Vast changes its API)
