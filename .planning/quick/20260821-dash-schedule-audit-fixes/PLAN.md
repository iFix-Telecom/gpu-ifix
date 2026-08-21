---
phase: quick-20260821-dash-schedule-audit-fixes
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/primary/reconciler.go
  - gateway/internal/primary/reconciler_test.go
  - gateway/internal/admin/operations.go
  - gateway/internal/admin/operations_test.go
  - gateway/internal/audit/fsm_adapter.go
  - gateway/internal/audit/fsm_adapter_test.go
  - gateway/internal/audit/writer.go
  - gateway/internal/emerg/fsm.go
  - gateway/internal/emerg/fsm_test.go
  - gateway/cmd/gateway/main.go
autonomous: true
requirements: [DASH-AUDIT-20260821]

must_haves:
  truths:
    - "GET /admin/operations schedule section reflects the LIVE pod_config schedule (8→19 enabled in prod), not the static env schedule (9→17 disabled), whenever the reconciler is wired"
    - "With rec nil (Vast off / tests), the schedule section still falls back to ParseScheduleEnv with the existing parse-error → minimal-disabled behavior"
    - "Every primary FSM transition (asleep/provisioning/ready/draining/destroying) writes an audit_log row with event_kind=primary_state_change so /incidents shows pod lifecycle history"
    - "Every emerg FSM transition writes an audit_log row with event_kind=emerg_state_change AND data_class='normal' so the row survives the NOT NULL enum on CopyFrom"
    - "Audit inserts are best-effort/non-blocking (async Enqueue) and never stall or fail an FSM transition"
  artifacts:
    - path: "gateway/internal/primary/reconciler.go"
      provides: "exported LiveRule() delegating to unexported liveRule()"
      contains: "func (r *Reconciler) LiveRule() ScheduleRule"
    - path: "gateway/internal/admin/operations.go"
      provides: "scheduleSection preferring h.rec.LiveRule() over ParseScheduleEnv"
      contains: "h.rec.LiveRule()"
    - path: "gateway/internal/audit/fsm_adapter.go"
      provides: "adapter satisfying primary's untyped stateChangeWriter, emitting event_kind=primary_state_change with DataClass normal"
      contains: "primary_state_change"
    - path: "gateway/internal/emerg/fsm.go"
      provides: "emerg transition audit event with DataClass normal + kind emerg_state_change"
      contains: "emerg_state_change"
    - path: "gateway/cmd/gateway/main.go"
      provides: "primary.NewFSM wired with the audit adapter instead of nil writer"
      contains: "PrimaryFSMAuditAdapter"
  key_links:
    - from: "gateway/internal/admin/operations.go"
      to: "gateway/internal/primary/reconciler.go"
      via: "OperationsHandler.rec.LiveRule() (nil-guarded)"
      pattern: "rec\\.LiveRule"
    - from: "gateway/cmd/gateway/main.go"
      to: "gateway/internal/audit/fsm_adapter.go"
      via: "primary.NewFSM(writer, onChange) first arg"
      pattern: "NewFSM\\(.*Adapter"
    - from: "gateway/internal/emerg/fsm.go"
      to: "gateway/internal/audit/writer.go"
      via: "WriteStateChange with DataClass populated"
      pattern: "DataClass:\\s*\"normal\""
---

<objective>
Fix two operator-facing dashboard bugs found in the live audit of ai-dashboard.converse-ai.app (2026-08-21):

**Fix A** — `/admin/operations` schedule section reads the STATIC env schedule (`ParseScheduleEnv(h.cfg)` → 9→17, DISABLED in prod) while the reconciler actually decides via the unexported `liveRule()` (pod_config snapshot → 8→19, enabled). The Operação page contradicts the Config-do-pod page and the pod's real behavior. Fix: export `LiveRule()` and make the handler use it when `h.rec != nil`.

**Fix B** — Incident History (`/incidents` → `GET /admin/audit`, `WHERE event_kind IS NOT NULL`) is permanently empty except for 27 old `breaker_force_*` rows. Root causes verified in code:
1. **Primary FSM writer is never wired**: `cmd/gateway/main.go:989` passes `primary.NewFSM(nil, onChange)` — the FSM's built-in `stateChangeWriter` hook (`internal/primary/fsm.go:213-229`, fires on every `Transition`/`SetState` via `commitTransitionSideEffects`) is dead code in prod. That is THE single choke point for all primary transitions — hook there, not in the reconciler.
2. **Emerg FSM hook IS wired** (`main.go:879` `emergFSM.SetAuditWriter(auditWriter)`) but the event it builds (`internal/emerg/fsm.go:330-345`) sets NO `DataClass`. `audit_log.data_class` is `ai_gateway.data_class NOT NULL` (migration 0003, enum from 0002) and the flusher passes `e.DataClass` raw into CopyFrom (`internal/audit/writer.go:257`) — an empty value fails the enum cast and kills the WHOLE batch (writer.go Flush is one CopyFrom per batch). Latent row-loss bug; fix regardless of whether emerg ever transitioned in prod.

Purpose: the two dashboard pages already render correctly — only the data source is wrong. No frontend changes (verified: `dashboard/src/lib/gateway.ts` treats `event_kind` as an opaque nullable string).

Output: two conventional commits on `develop` (commit only — do NOT push; deploy is a separate orchestrator step).
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@gateway/internal/admin/operations.go
@gateway/internal/primary/reconciler.go
@gateway/internal/primary/fsm.go
@gateway/internal/emerg/fsm.go
@gateway/internal/audit/writer.go
@gateway/cmd/gateway/main.go

<interfaces>
<!-- Verified 2026-08-21 while planning. Executor should use these directly. -->

From gateway/internal/primary/fsm.go:58-60 — the hook the adapter must satisfy (structural, unexported name but public shape):
```go
type stateChangeWriter interface {
	WriteStateChange(kind string, ev any) error
}
```

From gateway/internal/primary/fsm.go:213-229 — what the FSM passes to the writer on EVERY successful transition (both `Transition` and `SetState` funnel through `commitTransitionSideEffects`):
```go
_ = f.writer.WriteStateChange("fsm_transition", map[string]any{
	"from":   from.String(),  // e.g. "asleep"
	"to":     to.String(),    // e.g. "provisioning"
	"at":     now,            // time.Time
	"reason": reason,         // trigger / shutdown_reason string
})
```

From gateway/internal/audit/writer.go:167 — the sink (async, non-blocking Enqueue; mints request_id when zero; defaults TS when zero):
```go
func (w *Writer) WriteStateChange(kind string, ev Event) // Event fields: TS, Route, Method, Upstream, DataClass, Reason, ...
```

From gateway/internal/emerg/fsm.go:330-345 — the existing emerg event (pattern to mirror, MISSING DataClass today):
```go
f.auditWriter.WriteStateChange("fsm_transition", audit.Event{
	TS:       now,
	Route:    "emerg_fsm_transition",
	Method:   from.String() + "->" + to.String(),
	Upstream: to.String(),
	Reason:   reason,
})
```

From gateway/internal/primary/reconciler.go:1263 — the private method to export:
```go
func (r *Reconciler) liveRule() ScheduleRule
```

From gateway/internal/admin/operations.go:119-127 — handler already holds `rec *primary.Reconciler // nil-safe: Vast off`; `fsmSection()` at :281 shows the nil-guard pattern.

Test fixtures already available:
- `podconfig.NewStaticLoaderForTest(cfg PodConfig, rule ScheduleRule, bounds PodConfigBounds, log)` (loader.go:144) — used in primary/budget_test.go:96.
- `primary.NewReconcilerFull(primary.Deps{...})` (reconciler.go:2302) sets `rule: deps.Rule, podCfg: deps.PodCfg` (:2316-2317) — cheap construction, exported, usable from package admin tests.
- `buildReconciler(t, Deps{...})` helper in primary/reconciler_test.go:447 for same-package tests.
- Snapshot schedule fields: `podconfig.PodConfig{ScheduleUpHour, ScheduleDownHour, ScheduleDays: []string{"mon",...}, ScheduleEnabled?, ...}` — see schedule_test.go:62 and `ParseScheduleFromSnapshot` for the exact 6 HOT fields.
- audit writer tests use a fake flusher (writer_test.go) — reuse for the adapter test.
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Fix A — /admin/operations schedule section reads the live reconciler rule</name>
  <files>gateway/internal/primary/reconciler.go, gateway/internal/primary/reconciler_test.go, gateway/internal/admin/operations.go, gateway/internal/admin/operations_test.go</files>
  <action>
1. `gateway/internal/primary/reconciler.go`: add exported method directly below `liveRule()` (:1263-1273):
   `func (r *Reconciler) LiveRule() ScheduleRule { return r.liveRule() }`
   Doc comment: live evaluable schedule rule for read-only observers (admin /admin/operations); same fallback semantics as liveRule (snapshot re-parse → boot rule). ScheduleRule is a value type — safe to return by value.
2. `gateway/internal/admin/operations.go` `scheduleSection()` (:296-335): when `h.rec != nil`, use `rule := h.rec.LiveRule()` and SKIP the ParseScheduleEnv call entirely (LiveRule never errors — it falls back internally). When `h.rec == nil` (Vast off / tests), keep the existing `primary.ParseScheduleEnv(h.cfg)` path INCLUDING the parse-error → minimal-disabled-section behavior. The rest of the function (days rendering, NextTransition, timezone fallback) is rule-driven and unchanged.
3. Tests:
   - `gateway/internal/primary/reconciler_test.go`: add `TestLiveRule_Exported` — (a) `buildReconciler` with `Deps{Rule: bootRule, PodCfg: nil}` → `LiveRule()` returns the boot rule; (b) `Deps{PodCfg: podconfig.NewStaticLoaderForTest(podconfig.PodConfig{ScheduleUpHour: 8, ScheduleDownHour: 19, ScheduleDays: [...], ...}, ...)}` with a boot rule saying 9→17 → `LiveRule()` reflects 8→19. NOTE: verify whether `NewStaticLoaderForTest` produces a non-nil `Load()` snapshot — `liveRule` only re-parses when `podCfg.Load() != nil`; if the static loader leaves the snapshot nil, use whatever seam budget_test.go relies on, or test case (b) via `ParseScheduleFromSnapshot` equivalence and keep (a) as the LiveRule unit test.
   - `gateway/internal/admin/operations_test.go`: existing tests pass nil rec (:183, :285, :306) — they now exercise the fallback path and MUST stay green unchanged. Add `TestOperationsHandler_ScheduleFromLiveRule`: build a real `primary.NewReconcilerFull(primary.Deps{Rule: <9→17 disabled>, PodCfg: NewStaticLoaderForTest(<8→19 enabled snapshot>), Log: discardLog(), ...})`, inject via `newOperationsHandlerWithQueries(fake, bset, rec, nil, nil, cfg, discardLog())`, assert the schedule section shows up_hour 8 / down_hour 19 / disabled false — NOT the env 9→17. If NewReconcilerFull demands deps that make this brittle (e.g. it dials Redis/DB at construction — verify by reading :2302-2330 before writing the test), fall back to: nil-rec fallback test in admin + the LiveRule unit test in primary (both packages together still cover the wire).
4. Commit (do NOT push): `fix(admin): operations schedule section reads live reconciler rule, not static env (dashboard ops audit 2026-08-21)`
  </action>
  <verify>
    <automated>cd gateway && gofmt -l ./internal/admin ./internal/primary | (! grep .) && go test ./internal/admin/... ./internal/primary/...</automated>
  </verify>
  <done>LiveRule() exported; scheduleSection uses it when rec non-nil with env fallback when nil; new tests prove snapshot schedule wins over env; existing nil-rec tests untouched and green; commit created.</done>
</task>

<task type="auto">
  <name>Task 2: Fix B — primary + emerg FSM transitions write audit_log state-change rows</name>
  <files>gateway/internal/audit/fsm_adapter.go, gateway/internal/audit/fsm_adapter_test.go, gateway/internal/audit/writer.go, gateway/internal/emerg/fsm.go, gateway/internal/emerg/fsm_test.go, gateway/cmd/gateway/main.go</files>
  <action>
**Primary FSM (currently writer=nil → zero rows ever):**
1. New file `gateway/internal/audit/fsm_adapter.go`: exported `PrimaryFSMAuditAdapter` struct holding `W *Writer` and `Log *slog.Logger`, with method `WriteStateChange(kind string, ev any) error` satisfying primary's untyped `stateChangeWriter` interface (see <interfaces>). Behavior:
   - Type-assert `ev.(map[string]any)` and extract `from`, `to`, `reason` (strings) and `at` (time.Time) — the exact payload primary/fsm.go:217-222 emits. On assertion failure: `Log.Warn` and `return nil` (best-effort — NEVER propagate an error into the FSM path; the FSM discards it anyway but keep the contract explicit).
   - Build `Event{TS: at, Route: "primary_fsm_transition", Method: from + "->" + to, Upstream: to, DataClass: "normal", Reason: fmt.Sprintf("%s→%s (%s)", from, to, reason)}` and call `a.W.WriteStateChange("primary_state_change", ev)` (async Enqueue — non-blocking; WriteStateChange mints request_id + defaults TS). `DataClass: "normal"` is MANDATORY — `audit_log.data_class` is a NOT NULL enum (migration 0003) and the flusher CopyFrom passes it raw (writer.go:257); an empty value poisons the entire batch.
   - Doc comment: explain the map-payload contract with primary/fsm.go and why kind param ("fsm_transition") is remapped to "primary_state_change" (distinguish primary vs emerg in the /incidents feed).
2. `gateway/internal/audit/fsm_adapter_test.go`: using the existing fake-flusher pattern from writer_test.go, assert (a) a well-formed map produces one enqueued Event with EventKind "primary_state_change", DataClass "normal", Route "primary_fsm_transition", Method "asleep->provisioning", Reason containing "asleep→provisioning ("; (b) a malformed payload (non-map) returns nil and enqueues nothing.
3. `gateway/cmd/gateway/main.go` (:989): replace `primary.NewFSM(nil, func(...))` with the adapter: `primary.NewFSM(&audit.PrimaryFSMAuditAdapter{W: auditWriter, Log: log}, func(...))`. `auditWriter` is constructed at :269, well before this point, and is never nil on this path (same guarantee `emergFSM.SetAuditWriter(auditWriter)` at :879 relies on). Package `audit` is already imported in main.go. Leave the onChange Redis-mirror closure untouched.

**Emerg FSM (hook wired at main.go:879 but event drops on the data_class enum):**
4. `gateway/internal/emerg/fsm.go` (:330-345): in the transition audit event add `DataClass: "normal"` (with a short comment referencing the NOT NULL enum / batch-poison hazard, mirroring the wording in cmd/gatewayctl/breaker.go's writeBreakerAudit) and change the kind from `"fsm_transition"` to `"emerg_state_change"`; set `Reason: fmt.Sprintf("%s→%s (%s)", from, to, reason)` for parity with the primary rows (Method keeps `from->to`). Update the `SetAuditWriter` doc comment (:179-187) that still says `event_kind = "fsm_transition"`. Renaming the kind is safe: prod has ZERO rows with kind fsm_transition (only breaker_force_* exist) and the dashboard treats event_kind as an opaque string (dashboard/src/lib/gateway.ts — no enum).
5. `gateway/internal/emerg/fsm_test.go` (:353-354): update the kind assertion to `"emerg_state_change"`; extend the same test (or add one) to assert the captured event has `DataClass == "normal"` and Reason contains "→".
6. `gateway/internal/audit/writer.go` (~:155-157): update the WriteStateChange doc comment's list of kinds to include `primary_state_change` and `emerg_state_change` (doc only — there is no runtime validation of kinds).
7. NO migrations (event_kind TEXT since 0020, reason since 0022, all columns exist). NO changes to `ListAuditStateChanges` (already `WHERE event_kind IS NOT NULL`). NO dashboard changes.
8. Commit (do NOT push): `fix(audit): record primary/emerg FSM transitions as audit_log state changes (dashboard ops audit 2026-08-21)`
  </action>
  <verify>
    <automated>cd gateway && gofmt -l ./internal/audit ./internal/emerg ./cmd/gateway | (! grep .) && go test ./internal/audit/... ./internal/emerg/... && go build ./...</automated>
  </verify>
  <done>Primary FSM writer wired via adapter (event_kind primary_state_change, data_class normal); emerg event carries data_class normal + kind emerg_state_change; all inserts ride the existing async audit.Writer (non-blocking, best-effort); adapter + emerg tests green; commit created.</done>
</task>

<task type="auto">
  <name>Task 3: Full validation sweep</name>
  <files></files>
  <action>
Final gate across both fixes (nothing new to write — fix anything red before finishing):
- `cd gateway && gofmt -l .` → empty output.
- `go build ./...` → clean.
- `go test ./internal/admin/... ./internal/primary/... ./internal/emerg/... ./internal/audit/...` → green. (Integration tests with `-tags integration` run in CI only — docker is unavailable on this host; do NOT attempt them locally.)
- `git log --oneline -2` shows the two conventional commits from Tasks 1-2; `git status` shows a clean tree (PLAN/SUMMARY planning files excepted). Confirm NOTHING was pushed (`git status -sb` shows ahead-of-origin — expected; deploy is the orchestrator's separate step).
  </action>
  <verify>
    <automated>cd gateway && gofmt -l . | (! grep .) && go build ./... && go test ./internal/admin/... ./internal/primary/... ./internal/emerg/... ./internal/audit/...</automated>
  </verify>
  <done>gofmt clean, build clean, all four target package trees green, exactly two commits present and unpushed.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| dashboard → /admin/* | admin-key-authed operator surface; schedule section is read-only |
| FSM transition path → audit_log | internal state written to DB; must never block or fail the FSM |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-Q0821-01 | DoS | primary/emerg FSM transition path | mitigate | audit write rides audit.Writer's async non-blocking Enqueue (buffer-full → drop + WARN); adapter returns nil on any failure — transition never stalls |
| T-Q0821-02 | Tampering | audit_log batch integrity | mitigate | DataClass:"normal" set explicitly on both synthetic events — empty enum value would poison the whole CopyFrom batch (verified writer.go:257 + migration 0003) |
| T-Q0821-03 | Info disclosure | audit reason strings | accept | reason carries FSM trigger strings only (schedule/drain causes), no tenant data, no PII; rows are event_kind-tagged operator telemetry |
| T-Q0821-SC | Tampering | package installs | accept | no new dependencies introduced (stdlib + existing internal packages only) |
</threat_model>

<verification>
- `/admin/operations` (post-deploy, orchestrator step): schedule block shows up 8 / down 19 / disabled false matching pod_config, and `should_be_provisioned` true inside the 8-19 window.
- `/incidents` (post-deploy): next pod cycle produces `primary_state_change` rows (asleep→provisioning→ready→draining→destroying→asleep) with readable reasons.
- Locally: all automated gates in Tasks 1-3.
</verification>

<success_criteria>
- Operações page and Config-do-pod page can no longer contradict each other: both derive from the pod_config snapshot when the reconciler is live.
- Incident History fills from the next FSM transition onward; breaker_force_* rows keep working (no changes to that path or to ListAuditStateChanges).
- No migrations, no frontend changes, no pushes; two conventional commits referencing "dashboard ops audit 2026-08-21".
</success_criteria>

<output>
Create `.planning/quick/20260821-dash-schedule-audit-fixes/SUMMARY.md` when done.
</output>
