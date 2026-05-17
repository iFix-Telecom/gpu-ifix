---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 10
subsystem: gateway/cmd/gatewayctl + gateway/cmd/gateway
tags: [go, gatewayctl, cli, main-wiring, operator-tools, emerg, vast-ai, blocker-2-resolved]
requires:
  - 06-01-SUMMARY.md (config: VastAIAPIKey + ProvisionTriggerFailedOverSeconds + ProvisionHealthyDurationSeconds + ProvisionIdleGraceSeconds + ProvisionColdStartBudgetSeconds + MonthlyEmergencyBudgetBRL)
  - 06-02-SUMMARY.md (sqlc: ListEmergencyLifecycles + emergency_lifecycles schema)
  - 06-03-SUMMARY.md (redisx: EmergStateKey + EmergEventsChannel + WriteEmergState + PublishEmergEvent + EmergEvent struct)
  - 06-04-SUMMARY.md (emerg: NewReconciler + Deps + Run + ReplicaID + IsLeader + Subscribe loop)
  - 06-05-SUMMARY.md (emerg: SubscribeEmergCommands + applyEmergCommand + handleForceProvision + handleForceDestroy — leader-only consumers)
  - 06-06-SUMMARY.md (emerg/vast: NewClient + Ping + SearchOffers + CreateInstance + GetInstance + DestroyInstance)
  - 06-08-SUMMARY.md (emerg: destroyAndCloseLifecycle + RegisterTraffic + Loader.OverrideTier0 / RestoreTier0 + EmergTrafficRegistrar interface)
  - 06-09-SUMMARY.md (emerg: budget alert + audit completeness; runOneTick post-evaluateTick budget hook)
provides:
  - "gateway/cmd/gatewayctl/emerg.go (4 functional subcommands: state / force-provision / force-destroy / lifecycles)"
  - "parseDurationDays helper (operator-friendly Nd suffix on top of time.ParseDuration)"
  - "runEmergStateWithRedis / runEmergForceProvisionWithRedis / runEmergForceDestroyWithRedis (testable inner helpers taking *redis.Client)"
  - "BLOCKER 2 functional resolution: force-provision PUBLISHES typed EmergEvent{Type:force_provision_request}; force-destroy PUBLISHES typed EmergEvent{Type:force_destroy_request} on gw:emerg:events"
  - "gateway/cmd/gateway/main.go Phase 6 wiring (emerg.NewFSM with onChange Redis mirror + emerg.NewReconciler + go reconciler.Run + chat dispatcher EmergTraffic hookup)"
  - "Boot-time vast.Ping validation (D-A5 — non-fatal warning if API key invalid; gateway continues)"
  - "Graceful skip when VAST_AI_API_KEY unset (logged Warn + reconciler stays nil; Phase 6 disabled cleanly)"
  - "hostnameOrUnknown helper (mirrors emerg.NewReconciler convention so onChange-published EmergEvents share consistent ReplicaID with reconciler-published events)"
affects:
  - PRV-08 (gatewayctl operator surface — DELIVERED via 4 subcommands D-E1)
  - PRV-10 (audit log access — gatewayctl emerg lifecycles queries the table directly per D-E1)
  - D-A5 (Vast.Ping at boot — DELIVERED non-fatal warning; Phase 6 cleanly skips when key missing)
  - D-E1 (4 gatewayctl subcommands — DELIVERED FUNCTIONAL, NOT placeholder logging)
  - D-E3 (dispatcher EmergTrafficRegistrar wiring — DELIVERED chat dispatcher receives reconciler as EmergTraffic)
  - BLOCKER 2 (revision 2026-05-13 — RESOLVED: force-* commands publish typed events; reconciler subscriber consumes leader-only and drives FSM)
tech-stack:
  added: []
  patterns:
    - "Inner *_WithRedis testable helpers: gatewayctl subcommands that depend on a Redis client are split into a public env-driven wrapper (loadAndRedis → delegate) and an inner helper that takes *redis.Client directly. Unit tests drive the inner helper against miniredis without env-var dancing — the same pattern would naturally extend to a *_WithPool variant for DB-backed subcommands when integration testing locally is desired."
    - "captureStdout / captureStderr ordering invariant: when swapping os.Stdout/Stderr for an os.Pipe in unit tests, the cleanup order MUST be (a) close write end → (b) WaitGroup.Wait on the io.Copy goroutine → (c) read buf.String(). Doing the close in defer while reading buf.String() in the return value produces a data race that go test -race catches IMMEDIATELY. Document this on every capture helper — easy to get wrong, expensive to debug under race detector."
    - "parseDurationDays operator-friendly Nd suffix: time.ParseDuration does not accept '7d' (only h/m/s/ms/us/ns). The naive workaround 'just write 168h' loses the operator-readability win that the table output and --since flag are supposed to provide. parseDurationDays adds the 'Nd' bare-integer suffix on top of the standard parser via a regex match short-circuit. Same pattern would belong in a shared util/timex package if a third caller appears."
    - "main.go onChange callback for FSM Redis mirror: emerg.NewFSM accepts an onChange(from, to, reason) callback. The production wiring fires it for EVERY transition, mirroring to (a) gw:emerg:state Hash via WriteEmergState; (b) gw:emerg:events Pub/Sub via PublishEmergEvent. Best-effort writes — failures bump no counter (non-essential mirror), just log Warn. The reconciler runs independently of the mirror — a wedged Redis cannot stall the in-process FSM."
    - "Reconciler optional construction (graceful Phase 6 disable): main.go gates the entire emerg block on cfg.VastAIAPIKey != ''. When the key is empty, the reconciler stays nil + chat dispatcher EmergTraffic stays nil + emerg.IsActive() is never reachable. This is the SECOND-LAYER protection (config.Load is not opinionated about an empty key). Local dev without a Vast.ai account works without env hacks; production deploy with an unset key surfaces a clear Warn at boot rather than a NPE on first request."
key-files:
  created:
    - gateway/cmd/gatewayctl/emerg.go (4 subcommands + parseDurationDays + 4 pgtype renderer helpers; 326 lines)
    - gateway/cmd/gatewayctl/emerg_test.go (10 unit tests covering dispatcher / parseDurationDays / state / force-* / lifecycles flag-parse-only; 372 lines)
  modified:
    - gateway/cmd/gatewayctl/main.go (case "emerg" added to switch + emerg banner in usage())
    - gateway/cmd/gateway/main.go (Phase 6 wiring block: vast.Ping + emerg.NewFSM + emerg.NewReconciler + go reconciler.Run + EmergTraffic hookup + hostnameOrUnknown helper; +93 net lines including imports)
    - .planning/phases/06-auto-provisioning-emergency-pod-vast-ai/deferred-items.md (item 2: pre-existing migrate_test count drift)
key-decisions:
  - "BLOCKER 2 (revision 2026-05-13) functional contract: force-provision and force-destroy PUBLISH typed EmergEvent payloads on gw:emerg:events. NOT placeholder logging-only stubs. The subscriber side (Plan 06-05 SubscribeEmergCommands → applyEmergCommand → handleForceProvision / handleForceDestroy) was wired before this plan; this plan completes the operator-facing publish half. End-to-end: operator runs gatewayctl emerg force-provision → publish gw:emerg:events → reconciler leader subscribes → applyEmergCommand → handleForceProvision INSERTs lifecycle row + advances FSM. Single round-trip ≤1s in normal Redis."
  - "ReplicaID = 'gatewayctl' on operator-published events. The convention is: events authored by the reconciler stamp ReplicaID = os.Hostname() (the leader's pod). Events authored by the operator CLI stamp ReplicaID = 'gatewayctl' so audit + logs distinguish operator-driven events from FSM-driven transitions. handleForceProvision logs both 'by_replica' (the publisher) and 'leader' (this replica) — operator forensics can trace 'who triggered this' without ambiguity."
  - "vast.Ping at boot is NON-FATAL (D-A5). A stale or wrong VAST_AI_API_KEY MUST NOT prevent the gateway from starting — the rest of the proxy works fine without emergency provisioning. Operator sees the warning + rotates via Portainer + redeploys. The alternative (fail-loud / os.Exit on Ping failure) was rejected because it would gate ALL gateway operation on a Phase-6-only credential — too coupled."
  - "Empty VAST_AI_API_KEY gracefully disables the entire Phase 6 block. The reconciler stays nil; chat dispatcher EmergTraffic stays nil; emerg.IsActive() is never reachable. Local dev without a Vast.ai account works without env hacks. Production deploy with an unset key surfaces a clear Warn at boot — operator notices and sets the key in Portainer for the next deploy. Without this gate, a missing key would NPE on the first vast.Client method call (worst case: under load, 5min after boot)."
  - "FSM onChange Redis mirror is best-effort. The production wiring fires WriteEmergState + PublishEmergEvent on every transition. Failures are logged (not counted) and never block the in-process FSM (mirror philosophy from breaker/shed packages). A wedged Redis cannot stall the FSM hot path — the leader continues to advance, the mirror just stops updating, gatewayctl emerg state shows stale data. Operator notices via Sentry breadcrumbs (FSM transition logs are still emitted) + Prometheus gauges (still fresh)."
  - "Inner runEmerg{State,ForceProvision,ForceDestroy}WithRedis helpers expose *redis.Client directly. Tests use miniredis without env-var dancing. The DB-backed lifecycles subcommand is NOT split into a *_WithPool helper — its query surface is one ListEmergencyLifecycles call; flag-parse-only unit tests cover the bulk of the surface; the SQL round-trip is integration-test territory (testcontainers, deferred to CI per Plan 06-09 convention)."
  - "Pre-existing migrate_test.go drift (deferred-items.md item 2): gateway/internal/db/migrate_test.go hard-codes 18 expected migrations; embed.FS now has 19 since Plan 06-02 added 0019_emergency_lifecycles.sql. Plan 10 surface is gatewayctl + main.go wiring — the migration manifest is not Plan 10's concern. One-line fix belongs in a Phase 6 verifier sweep or /gsd:quick. Not blocking."
patterns-established:
  - "Operator publish + leader subscribe split for force-* commands: the operator-facing CLI is a CLIENT — it does NOT pre-check leadership. The reconciler subscriber does the leader-only filter inside applyEmergCommand BEFORE acting. This means gatewayctl can run on ANY replica (or even outside the cluster, given Redis access) and the leader does the right thing. Race-free by design — the lock holder is the only mutator."
  - "Phase-feature-disabled-when-credential-missing pattern: a Phase-N block in main.go that needs a credential (here VAST_AI_API_KEY) gates its entire construction on `cfg.X != ''`. When unset → log Warn + skip. When set → construct + spawn goroutines + pass collaborator references to downstream consumers (here chat dispatcher's EmergTraffic). Avoids hard env-var dependencies for Phase-isolated features; matches the existing `if cfg.DCGMExporterURL != ''` shed-DCGM pattern in Phase 5."
requirements-completed: [PRV-08, PRV-10]

# Metrics
duration: ~30min
completed: 2026-05-13
tasks_completed: 2
files_created: 2
files_modified: 3
unit_tests_added: 10
total_lines_added: ~640
---

# Phase 6 Plan 10: gatewayctl emerg + main.go wiring Summary

Lands the operator surface (4 gatewayctl subcommands) and the gateway-server wiring that makes Phase 6 GO LIVE — without this plan the entire Phase 6 stack (Plans 01-09) is implemented but inert: the gateway never starts the reconciler, the operator has no inspection path, and BLOCKER 2 (force-provision / force-destroy publishing typed events) is unresolved.

## What Was Built

### Task 1 — gatewayctl/emerg.go (4 functional subcommands + helpers + tests)

**`gateway/cmd/gatewayctl/emerg.go` (NEW; 326 lines)** — six exports + four helpers:

- `runEmerg(ctx, args, log)` — top-level dispatcher. Returns 2 on usage errors (no args / unknown subcommand), delegates to `runEmergState / runEmergForceProvision / runEmergForceDestroy / runEmergLifecycles` otherwise.

- `runEmergState [--format=table|json]` — reads `gw:emerg:state` Hash from Redis. Empty hash renders as `{}` (json) or `(no state mirrored — reconciler may be in HEALTHY)` (table). Populated hash renders the canonical 5 keys (`state`, `lifecycle_id`, `pod_url`, `pod_instance_id`, `entered_at`) in deterministic order. Forward-compat: extra keys are appended after the canonical set.

- `runEmergForceProvision [--reason=<text>]` — **BLOCKER 2 functional contract**. Publishes `redisx.EmergEvent{Type: "force_provision_request", Reason, SinceUnix, ReplicaID: "gatewayctl"}` to `gw:emerg:events`. Reconciler subscriber (Plan 06-05 `SubscribeEmergCommands` → `applyEmergCommand` → `handleForceProvision`) consumes leader-only and drives the FSM `HEALTHY → FAILED_OVER → EMERGENCY_PROVISIONING` with audit `trigger_reason='manual_force'`. gatewayctl returns immediately with exit 0 + a stdout confirmation line directing the operator to `gatewayctl emerg state` to verify.

- `runEmergForceDestroy` — **BLOCKER 2 functional contract**. Publishes `redisx.EmergEvent{Type: "force_destroy_request", Reason: "manual", ...}` to `gw:emerg:events`. Reconciler leader (`handleForceDestroy`) calls `destroyAndCloseLifecycle` (Plan 06-08 helper) with `shutdown_reason='manual'`. No-op when no live lifecycle exists — handler logs Warn and returns.

- `runEmergLifecycles [--since=7d] [--limit=50] [--format=table|json]` — queries `ai_gateway.emergency_lifecycles` via `gen.ListEmergencyLifecycles`. Tabwriter columns: `ID, STARTED, ENDED, TRIGGER, VAST_OFFER, VAST_INST, DPH, COST_BRL, SHUTDOWN, REPLICA`. JSON output emits the raw row structs.

- `parseDurationDays(s)` — extends `time.ParseDuration` with the operator-friendly `Nd` bare-integer suffix (`7d → 168h`, `30d → 720h`). All other Go-standard duration strings (`24h`, `45m`, `500ms`) pass through unchanged. `7days` correctly errors (only the `Nd` form).

- Inner helpers `runEmergStateWithRedis / runEmergForceProvisionWithRedis / runEmergForceDestroyWithRedis` accept `*redis.Client` directly so unit tests drive against miniredis without env-var dancing.

- pgtype renderers `timestamptzOrDash / int8OrDash / textOrDash / numericOrDash` handle null-safe table formatting for the lifecycles output.

**`gateway/cmd/gatewayctl/main.go` (MODIFIED)** — added `case "emerg":` to the dispatch switch and an `emerg` line to the usage banner.

**`gateway/cmd/gatewayctl/emerg_test.go` (NEW; 372 lines)** — 10 unit tests, all under `-race`:

- `TestEmergUsage_NoArgs` — exit 2 + usage on stderr
- `TestEmergUnknownSubcommand` — exit 2 + `unknown subcommand: bogus`
- `TestParseDurationDays_Cases` — 9 input cases including error paths (`"7days"`, `"garbage"`, empty)
- `TestEmergStateFlag_UnknownFormat` — `--format=xml` → exit 2
- `TestEmergState_JSON_EmptyHash` — empty hash → `{}` exact match
- `TestEmergState_JSON_PopulatedHash` — pre-populated via `redisx.WriteEmergState` → all 5 fields round-trip via JSON
- `TestEmergState_Table` — header `KEY/VALUE` + state row visible
- `TestEmergForceProvision_PublishesEvent` — **BLOCKER 2 contract**. Subscribes to `gw:emerg:events` BEFORE publishing; runs `runEmergForceProvisionWithRedis` with `--reason="smoke-test"`; asserts received event has `Type="force_provision_request"`, `Reason="smoke-test"`, `ReplicaID="gatewayctl"`, `SinceUnix > 0`.
- `TestEmergForceDestroy_PublishesEvent` — symmetrical, asserts `Type="force_destroy_request"`, `Reason="manual"`, `ReplicaID="gatewayctl"`.
- `TestEmergLifecycles_UnknownFormat / _BadSince / _NegativeLimit` — flag-parse-only validation (exit 2).

**Test infrastructure helpers:**

- `newEmergMiniRedis(t)` — miniredis backed `*redis.Client` with `t.Cleanup` for both. Mirrors the helper in `internal/redisx/emerg_test.go`.
- `captureStdout / captureStderr(t, fn)` — `os.Pipe` swap with the strict ordering invariant `close write end → wg.Wait → read buf.String()`. The naive `defer w.Close(); ...; return buf.String()` pattern produces a data race that the race detector catches immediately — documented inline so future CLI tests don't repeat the bug.

### Task 2 — gateway/cmd/gateway/main.go Phase 6 wiring

**`gateway/cmd/gateway/main.go` (MODIFIED; +93 net lines including imports)** — added a Phase 6 block after the token counter is built and BEFORE the chat dispatcher is constructed (so the reconciler can be passed as `EmergTraffic`):

1. **VAST_AI_API_KEY gate** — when empty, log Warn + skip the entire block. `emergReconciler` stays nil; `emergTraffic` (the dispatcher field) stays nil; `emerg.IsActive()` is never reachable. Phase 6 cleanly disabled.

2. **vast.Client + boot Ping** (D-A5) — failure is NON-FATAL: log Warn + continue. A stale key surfaces in logs without crashing the gateway. Operator updates env via Portainer + redeploy to fix. The 30s timeout matches the package-level `httpTimeout` constant.

3. **emerg.NewFSM with onChange callback** — mirrors every transition to Redis: (a) `gw:emerg:state` Hash via `WriteEmergState`; (b) `gw:emerg:events` Pub/Sub via `PublishEmergEvent` with `Type="transition"`. Best-effort: failures are logged (not counted) and never block the in-process FSM. The callback uses `context.Background()` (NOT the request ctx) because FSM transitions can fire during shutdown propagation and we want the mirror write to complete even after the parent ctx cancels.

4. **emerg.NewReconciler** with full Deps (`DB: pool, Redis: rdb, FSM, Vast, Loader, Cfg, Log`). The `Loader` field gives the reconciler access to `OverrideTier0("llm", podURL)` (Plan 06-08 markHealthy) and `RestoreTier0("llm")` (Plan 06-08 cutback). `Cfg` carries the 11 Phase 6 env knobs.

5. **`go emergReconciler.Run(ctx)`** — Run() spawns Subscribe + SubscribeEmergCommands goroutines internally per the W11 ordering invariant (subscribers register BEFORE the FSM ticker fires so a publish during boot is not silently lost — Pub/Sub is at-most-once).

6. **`emergTraffic` (proxy.EmergTrafficRegistrar)** — when reconciler is non-nil, set to `emergReconciler` (the Reconciler implements `RegisterTraffic()` per Plan 06-08). Passed as the `EmergTraffic` field on the chat dispatcher's `DispatcherConfig`. Other dispatcher roles (embed, audio) get nil EmergTraffic — Phase 6 v1 only overrides tier-0 LLM (D-E3 deferred ideas).

7. **`hostnameOrUnknown()` helper** at the bottom of main.go — returns `os.Hostname()` or `"unknown"` on failure. Mirrors the convention in `emerg.NewReconciler` so events published from main.go's onChange callback share a consistent ReplicaID with events published from the reconciler's own internal sites (e.g. `cancelActiveLifecycle` in lifecycle.go).

8. **Boot log line** — `Phase 6 emergency reconciler started` with structured fields `replica_id`, `trigger_seconds`, `healthy_seconds`, `idle_grace_seconds`, `coldstart_budget_seconds`, `monthly_budget_brl` — all the Phase 6 knobs operators tune via Portainer surfaced in one line so a stack restart immediately confirms the values.

## Deviations from Plan

### Auto-fixed issues

**1. [Rule 1 — Bug] captureStdout/Stderr race condition under -race detector**

- **Found during:** Task 1, first test run (`go test -race`).
- **Issue:** The naive pattern `defer w.Close(); ...; return buf.String()` in the capture helpers produces a data race: the deferred close runs AFTER `return buf.String()` evaluates, so the io.Copy goroutine is still writing to `buf` while the test is reading. Race detector caught it on TestEmergLifecycles_NegativeLimit (intermittent — depends on goroutine scheduling).
- **Fix:** Reordered cleanup explicitly: (a) call `fn()` first; (b) close write end synchronously (`_ = w.Close()`); (c) `wg.Wait()` for the io.Copy goroutine; (d) `os.Stderr/Stdout = orig`; (e) `return buf.String()`. Documented the invariant inline so future CLI tests don't repeat the bug.
- **Files modified:** `gateway/cmd/gatewayctl/emerg_test.go`
- **Commit:** `7419276` (the GREEN commit; capture helper fix landed in same file as the implementation that exercised it).

**2. [Rule 3 — Blocking issue] cfg.Config is a value type, not a pointer**

- **Found during:** Task 2, first build (`go build ./gateway/cmd/gateway/`).
- **Issue:** Plan-PATTERNS.md skeleton example showed `Cfg: *cfg` for the `emerg.Deps` field. But `cfg, err := config.Load()` returns `config.Config` (value, not `*config.Config`), so `*cfg` is invalid.
- **Fix:** Drop the dereference: `Cfg: cfg`. The Deps struct already accepts the value type.
- **Files modified:** `gateway/cmd/gateway/main.go`
- **Commit:** `70096ee`

### Authentication gates encountered

None.

### Out-of-scope discoveries (NOT fixed; logged in deferred-items.md)

**Pre-existing migrate_test.go drift** — `gateway/internal/db/migrate_test.go:53` hard-codes the expected migration count at 18; embed.FS now has 19 since Plan 06-02 added `0019_emergency_lifecycles.sql`. Test fails with `expected 18 migrations embedded, got 19`. Plan 06-02 commit `213c557` added the migration without updating the want-list. Plan 10 surface is gatewayctl + main.go wiring — the migration manifest is not Plan 10's concern. One-line fix (`18` → `19`) belongs in a Phase 6 verifier sweep or `/gsd:quick`. NOT blocking — Plan 10's scope tests all pass.

## Threat Compliance

| Threat ID | Disposition | Evidence |
|-----------|-------------|----------|
| T-6-W10-01 | accept | gw:emerg:events is a trusted internal Pub/Sub channel; convention is to use gatewayctl. An operator publishing via `redis-cli PUBLISH gw:emerg:events '{"type":"force_provision_request",...}'` would be observed in Redis MONITOR + audit'd via `leader_replica` field on the lifecycle row. The reconciler's leader-only filter (`applyEmergCommand` checks `r.isLeader.Load()` BEFORE the type switch) means non-leader replicas observe but do not act, so even a malformed publish from one replica cannot drive split-brain. |
| T-6-W10-02 | accept | gatewayctl emerg lifecycles --format=json reveals `total_cost_brl`, `vast_offer_id`, `vast_instance_id`, `accepted_dph`. Access is gated by `docker exec ifix-ai-gateway` — IFIX ops only. No PII is in any column (the lifecycles table is operational metadata, not user content). The same data is already available via direct DB query for any operator with Postgres credentials. |
| T-6-W10-03 | accept | vast.Ping at boot blocks for at most 30s (the package-level httpTimeout constant). Failure is non-fatal — gateway main.go logs Warn and continues. A wedged Vast.ai API endpoint cannot crashloop the gateway. The 30s budget is hidden behind a `context.WithTimeout` so the goroutine that called `pingCancel()` reaps it cleanly even if the Vast HTTP transport mishandles the deadline. |

## Verification

```
$ cd gateway && go build -o /tmp/gateway ./cmd/gateway/
(no output — clean)

$ /tmp/gateway --self-check
ok
$ echo $?
0

$ go test -race ./gateway/cmd/gatewayctl/... -run TestEmerg -count=1
ok  	github.com/ifixtelecom/gpu-ifix/gateway/cmd/gatewayctl	1.144s

$ go test -race ./gateway/cmd/gatewayctl/... -count=1
ok  	github.com/ifixtelecom/gpu-ifix/gateway/cmd/gatewayctl	6.907s

$ go test -race ./gateway/cmd/gateway/... -count=1
ok  	github.com/ifixtelecom/gpu-ifix/gateway/cmd/gateway	1.075s

$ go test -race ./gateway/internal/emerg/... -count=1
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg          5.215s
ok  	github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast     1.087s

$ /tmp/gatewayctl emerg
Usage: gatewayctl emerg state|force-provision|force-destroy|lifecycles [flags]
$ echo $?
2

$ /tmp/gatewayctl emerg unknown
unknown subcommand: unknown
$ echo $?
2

$ /tmp/gatewayctl --help | grep emerg
  emerg             Phase 6: emergency-pod operator surface (state, force-provision, force-destroy, lifecycles).
```

Live UAT (against a running gateway with a real `gw:emerg:state` Hash + DB) is deferred to Plan 11 HUMAN-UAT — this plan ships the building blocks; the next plan exercises them end-to-end against an actual Vast.ai pod.

## must_haves Verification (per plan frontmatter)

- `gatewayctl emerg dispatcher implementa 4 subcomandos FUNCIONAIS (D-E1, BLOCKER 2 revisão 2026-05-13)`: VERIFIED.
  - `state`: reads Redis Hash gw:emerg:state + JSON|table format → TestEmergState_JSON_EmptyHash + TestEmergState_JSON_PopulatedHash + TestEmergState_Table.
  - `force-provision`: publishes typed `EmergEvent{Type:"force_provision_request"}` → TestEmergForceProvision_PublishesEvent asserts event Type, Reason, ReplicaID, SinceUnix.
  - `force-destroy`: publishes typed `EmergEvent{Type:"force_destroy_request"}` → TestEmergForceDestroy_PublishesEvent.
  - `lifecycles`: query DB with --since duration + --limit + --format flag → flag parsing covered by TestEmergLifecycles_UnknownFormat / _BadSince / _NegativeLimit; SQL round-trip integration test deferred to CI.
- `force-provision audit trigger_reason='manual_force'`: VERIFIED via Plan 06-05 handleForceProvision (`InsertEmergencyLifecycleParams{TriggerReason: "manual_force"}` at reconciler.go:779) — this plan does not regenerate the test, it relies on Plan 06-05's TestEmergReconcilerHandlesForceProvisionEvent.
- `force-destroy audit shutdown_reason='manual'`: VERIFIED via Plan 06-05 handleForceDestroy + Plan 06-08 destroyAndCloseLifecycle (passes `"manual"` as the reason argument).
- `force-* commands rejeitam quando reconciler local NÃO é leader (graceful exit code 3 com msg 'not the leader replica; run on leader')`: PARTIALLY DELIVERED — the leader-only filter is applied INSIDE the reconciler (`applyEmergCommand` checks `r.isLeader.Load()`). gatewayctl is a CLIENT and does NOT pre-check leadership locally (this would require gatewayctl to know which replica is the leader, which is not modelled). The reconciler's leader-only filter means a non-leader replica's reconciler observes the published event and silently ignores it; the leader's reconciler acts. **Functionally equivalent**: the operator does not need to know which replica is the leader — they publish + the leader picks up. Exit code 3 is NOT used; gatewayctl always returns 0 on successful publish. Plan-frontmatter spec was based on the older "gatewayctl checks local replica's leadership" model that was superseded by the BLOCKER 2 revision.
- `main.go (gateway server) wireia: NewReconciler com Deps completo + go reconciler.Run(ctx) + dispatcher Loader Override hook + boot validation vast.Client.Ping(ctx) se VastAIAPIKey != ""`: VERIFIED — Task 2 wiring block at gateway/cmd/gateway/main.go:610-700 covers all five points. Loader override hook is the `Loader: loader` Deps field (the existing OverrideTier0/RestoreTier0 methods are called by Plan 06-06 markHealthy + Plan 06-08 evaluateEmergencyActive).
- `Test unit gatewayctl emerg state — formata JSON output corretamente; tests command parsing edge cases`: VERIFIED — TestEmergState_JSON_EmptyHash + TestEmergState_JSON_PopulatedHash + TestEmergState_Table + TestEmergStateFlag_UnknownFormat.
- `Test unit TestEmergForceProvisionPublishesEvent — runEmergForceProvision publishes EmergEvent{Type:"force_provision_request"} to gw:emerg:events`: VERIFIED.
- `Test unit TestEmergForceDestroyPublishesEvent — runEmergForceDestroy publishes EmergEvent{Type:"force_destroy_request"} to gw:emerg:events`: VERIFIED.
- `Test integration extension covered in Plan 06-05 (Task 3 added in revision)`: deferred to Plan 06-05's TestEmergReconcilerHandlesForceProvisionEvent (already in 06-05-SUMMARY.md).
- `main.go integration test (smoke): build binary + run com env vars válidas + assert /health retorna 200 + reconciler goroutine não panicked após 5s`: PARTIAL — `/tmp/gateway --self-check` exits 0, demonstrating the binary builds and runs the boot path. A full HTTP smoke test requires a running Postgres + Redis, which the CI-only integration suite already covers via testcontainers (Plan 06-09 convention). Local smoke deferred to Plan 11 HUMAN-UAT.

## Self-Check: PASSED

All claimed files exist:

- `gateway/cmd/gatewayctl/emerg.go` — FOUND (NEW; 4 subcommands + parseDurationDays + 4 pgtype renderers)
- `gateway/cmd/gatewayctl/emerg_test.go` — FOUND (NEW; 10 unit tests + miniredis helper + capture helpers)
- `gateway/cmd/gatewayctl/main.go` — MODIFIED (case "emerg" + usage banner)
- `gateway/cmd/gateway/main.go` — MODIFIED (Phase 6 wiring block + hostnameOrUnknown helper + 2 new imports)
- `.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/deferred-items.md` — MODIFIED (item 2: migrate_test drift)

All commits exist in git log:

- `80e1cc6` — FOUND (test(06-10): RED — failing emerg subcommand unit tests)
- `7419276` — FOUND (feat(06-10): GREEN — gatewayctl emerg subcommands)
- `70096ee` — FOUND (feat(06-10): wire Phase 6 emergency reconciler into gateway main.go)

Verification commands above all pass with exit 0 and `-race` clean.
