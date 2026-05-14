---
phase: 07-observability-dashboard-alerting
plan: 06
subsystem: gateway-composition-root
tags: [go, main-go, wiring, alerting, admin-api, audit, fsm, observability]

# Dependency graph
requires:
  - phase: 07-observability-dashboard-alerting
    plan: 02
    provides: "audit.Writer.WriteStateChange(kind, ev) helper + Event.EventKind field"
  - phase: 07-observability-dashboard-alerting
    plan: 03
    provides: "admin.NewMetricsHandler / admin.NewAuditHandler — the /admin/metrics + /admin/audit handlers"
  - phase: 07-observability-dashboard-alerting
    plan: 05
    provides: "alert.NewAlerter / Alerter.Run / Alerter.ReconcileBoot — the alerting goroutine core"
  - phase: 07-observability-dashboard-alerting
    plan: 01
    provides: "config.Config 13 optional alert fields (Chatwoot/ClickUp/Brevo)"
  - phase: 06-auto-provisioning-emergency-pod-vast-ai
    provides: "emerg.FSM 7-state machine + commitTransitionSideEffects onChange path"
provides:
  - "main.go: buildAlertChannels — Chatwoot/ClickUp/Brevo Channels constructed from config, each optional (missing env var = WARN + skip, never fail-boot)"
  - "main.go: alert.Alerter built + ReconcileBoot run + go alerter.Run(ctx) spawned EARLY (before breakerSet.Subscribe / emergReconciler.Run)"
  - "main.go: /admin/metrics + /admin/audit mounted under the admin-key-gated /admin sub-router"
  - "emerg.FSM.SetAuditWriter — threads audit.Writer into the FSM so every transition emits a WriteStateChange(fsm_transition) audit_log row (OBS-07)"
affects: []

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Optional-channel construction from config: a switch on config non-emptiness builds the client OR logs a single disabled-channel WARN naming the missing env var — the SentryDSN precedent, an unset alert var never fails boot"
    - "EARLY goroutine spawn for at-most-once Pub/Sub: go alerter.Run(ctx) is placed textually before the breaker/shed/emerg publishers so a boot-window transition is not silently lost (07-RESEARCH Pitfall 4)"
    - "Interface-typed FSM dependency + setter: emerg.FSM gains a stateChangeWriter interface field set via SetAuditWriter — keeps NewFSM's signature stable for the ~12 existing test call sites while letting fsm_test.go inject a recording fake"
    - "Function-scope hoist for cross-block reuse: emergFSM is declared at main() scope (was block-local) so the /admin/metrics handler can read FSM.State() even when Phase 6 is disabled (nil FSM → handler reports 'unknown')"

key-files:
  created: []
  modified:
    - gateway/cmd/gateway/main.go
    - gateway/cmd/gateway/main_test.go
    - gateway/internal/emerg/fsm.go
    - gateway/internal/emerg/fsm_test.go

key-decisions:
  - "Per-channel 'required fields' for the enable/skip gate: Chatwoot needs API token + API URL + on-call account ID; ClickUp needs API token + alert list ID; Brevo needs SMTP host + user + pass + from + at-least-one to-address. A partially-configured channel is SKIPPED with a WARN naming the FIRST missing var — never half-built."
  - "emerg.FSM gets the audit.Writer via a SetAuditWriter setter, NOT a NewFSM constructor parameter — ~12 existing NewFSM(log, onChange) test call sites stay untouched; a stateChangeWriter interface (not the concrete *audit.Writer) lets fsm_test.go inject a recording fake with no live Postgres."
  - "The FSM audit row maps the transition onto the existing audit.Event fields: Route='emerg_fsm_transition', Method='<from>-><to>', Upstream='<to-state>', ErrorCode='<reason>'. The Event struct has no dedicated from/to-state fields; these existing nullable columns are what the /admin/audit feed already surfaces."
  - "emergFSM hoisted to function scope (was scoped inside the `else` branch of `if cfg.VastAIAPIKey == \"\"`) so admin.NewMetricsHandler can take it. When Phase 6 is disabled emergFSM stays nil and admin.fsmStateString already handles nil → 'unknown'."

requirements-completed: [OBS-01, OBS-04, OBS-05, OBS-07]

# Metrics
duration: ~30min
completed: 2026-05-14
---

# Phase 7 Plan 06: Composition-Root Wiring (main.go + FSM Audit) Summary

**The composition root — `main.go` now constructs the three alert clients from config (each optional: a missing env var logs a WARN and skips, never fails boot), builds the `Alerter`, runs `ReconcileBoot`, and spawns `go alerter.Run(ctx)` textually BEFORE every event-publishing subsystem (Pitfall 4); mounts `/admin/metrics` + `/admin/audit` under the admin-key-gated `/admin` sub-router; and threads the shared `audit.Writer` into the emergency FSM so every transition writes a `fsm_transition` `audit_log` row.**

## Performance

- **Duration:** ~30 min
- **Tasks:** 3 (all `type="auto"`)
- **Files modified:** 4 (0 created, 4 modified)

## Accomplishments

- **Alert clients + alerter goroutine (Task 1, OBS-04/05/06).** A new `buildAlertChannels(cfg, log)` helper builds a `[]alert.Channel`: for each of Chatwoot / ClickUp / Brevo it checks the required config fields and either constructs the concrete client (`alert.NewChatwootClient` / `NewClickUpClient` / `NewBrevoClient`) or logs a single disabled-channel WARN naming the first missing env var. `main.go` then builds `alerter := alert.NewAlerter(rdb, channels, log)`, calls `alerter.ReconcileBoot(ctx)`, and spawns `go alerter.Run(ctx)` — placed immediately after the Redis client is constructed, textually **before** `go breakerSet.Subscribe(ctx)` (line 300) and `go emergReconciler.Run(ctx)` (line ~709). With all 12 alert vars unset the slice is empty, every channel logs a WARN, and the alerter still runs (classify + dedup + log; external fan-out is just empty) — the gateway boots normally.
- **Admin observability routes (Task 2, OBS-01/OBS-07).** The `proxies` struct gains `adminMetricsHandler` + `adminAuditHandler http.Handler` fields. `main.go` constructs `admin.NewMetricsHandler(gen.New(pool), emergFSM, log)` and `admin.NewAuditHandler(gen.New(pool), log)` next to the existing `adminUsageHandler`. `emergFSM` was hoisted from block scope to function scope so the metrics handler can take it (nil when Phase 6 is disabled — `admin.fsmStateString` already maps nil → `"unknown"`). Both routes are registered inside the existing `if px.adminVerifier != nil` block, gated by the same `admin.Middleware` (X-Admin-Key bcrypt) as `/usage`. The unauthenticated Prometheus `r.Handle("/metrics", obs.Handler())` (line ~886) is untouched and stays distinct from the admin-key-gated `/admin/metrics`.
- **FSM transition audit rows (Task 3, OBS-07).** `emerg.FSM` gains a `stateChangeWriter` interface field + a `SetAuditWriter` setter. In `commitTransitionSideEffects` — the same post-CAS path that emits the Sentry breadcrumb — the FSM now calls `f.auditWriter.WriteStateChange("fsm_transition", audit.Event{...})` with `Route="emerg_fsm_transition"`, `Method="<from>-><to>"`, `Upstream="<to-state>"`, `ErrorCode="<reason>"`. The write goes through `audit.Writer`'s existing async batch writer (non-blocking `Enqueue` — never stalls the FSM CAS). A nil `auditWriter` (the default; tests + early-boot wiring) skips the call. `main.go` threads the shared `auditWriter` in via `emergFSM.SetAuditWriter(auditWriter)`.

## Task Commits

Each task was committed atomically:

1. **Task 1: construct alert clients + spawn alerter goroutine early** — `dd626f8` (feat)
2. **Task 2: mount /admin/metrics + /admin/audit under admin sub-router** — `ed824e8` (feat)
3. **Task 3: FSM transitions emit fsm_transition audit rows** — `f8bb020` (feat)

## Files Created/Modified

- `gateway/cmd/gateway/main.go` — `alert` import; `buildAlertChannels` helper; alerter construction + `ReconcileBoot` + `go alerter.Run(ctx)` spawned early; `proxies.adminMetricsHandler` + `adminAuditHandler` fields; both admin handlers constructed + assigned + route-registered; `emergFSM` hoisted to function scope; `emergFSM.SetAuditWriter(auditWriter)` call
- `gateway/cmd/gateway/main_test.go` — `alert` + `config` imports; `TestBuildAlertChannels_AllUnsetBootsClean` (all alert vars unset → 0 channels, `NewAlerter` constructs without panic) + `TestBuildAlertChannels_PartialConfigEnablesSubset` (only fully-configured channels enabled)
- `gateway/internal/emerg/fsm.go` — `audit` import; `stateChangeWriter` interface; `FSM.auditWriter` field + `SetAuditWriter` setter; `WriteStateChange("fsm_transition", ...)` call in `commitTransitionSideEffects` with a nil guard
- `gateway/internal/emerg/fsm_test.go` — `audit` import; `fakeStateChangeWriter` recording fake; `TestFSMTransitionEmitsAuditRow` (one row per committed transition, none on a CAS-failed transition, correct field mapping) + `TestFSMNilAuditWriterDoesNotPanic`

## Decisions Made

- **Per-channel "required fields" gate.** Chatwoot is enabled only when `CHATWOOT_API_TOKEN` + `CHATWOOT_API_URL` + `CHATWOOT_ONCALL_ACCOUNT_ID` are all set; ClickUp needs `CLICKUP_API_TOKEN` + `CLICKUP_ALERT_LIST_ID`; Brevo needs `BREVO_SMTP_HOST` + `BREVO_SMTP_USER` + `BREVO_SMTP_PASS` + `ALERT_EMAIL_FROM` + a non-empty `ALERT_EMAIL_TO`. A partial config is **skipped** (with a WARN naming the first missing var), never half-built — a client missing its destination address would silently no-op.
- **Setter, not constructor parameter, for the FSM audit dependency.** `emerg.NewFSM(log, onChange)` has ~12 call sites across `fsm_test.go`, `reconciler_test.go`, `budget_test.go`, `subscribe_test.go`. Adding a third parameter would churn all of them. A `SetAuditWriter` setter keeps the constructor stable; typing the field as a `stateChangeWriter` interface (rather than the concrete `*audit.Writer`) lets `fsm_test.go` inject a recording fake with no live Postgres pool. `emerg` importing `audit` introduces no cycle — `audit` imports only `db/gen` + `obs`, neither of which imports `emerg` (verified).
- **FSM audit row field mapping.** `audit.Event` has no dedicated from-state / to-state fields, so the transition is mapped onto the existing nullable columns the `/admin/audit` feed already surfaces: `Route="emerg_fsm_transition"` (the route label), `Method="<from>-><to>"` (the transition arrow), `Upstream="<to-state>"` (the resulting state), `ErrorCode="<reason>"` (the human-readable cause — not an error, but the column `/admin/audit` exposes for free-text). `EventKind` is set to `"fsm_transition"` by `WriteStateChange` itself.
- **`emergFSM` hoisted to function scope.** It was block-local inside the `else` branch of `if cfg.VastAIAPIKey == ""`. `admin.NewMetricsHandler` needs it. Hoisting to a `var emergFSM *emerg.FSM` at `main()` scope means when Phase 6 is disabled `emergFSM` stays nil — and `admin.fsmStateString` (07-03) already maps a nil FSM to `"unknown"`, so `/admin/metrics` renders cleanly with Phase 6 off.

## Deviations from Plan

None — plan executed exactly as written. All three tasks, the four-file modification list, every acceptance criterion, the verification gate, and the threat model were satisfied as specified. No auto-fixes (no Rule 1/2/3 triggers), no blocking issues, no architectural decisions (no Rule 4), no authentication gates.

## Issues Encountered

- **Worktree path-safety bug (#3099) hit during Task 1.** The first three `Edit` calls used absolute paths under the **main repo** (`/home/pedro/projetos/pedro/gpu-ifix/gateway/...`) instead of the worktree (`.../.claude/worktrees/agent-.../gateway/...`), so the edits landed in the main repo's working tree. Recovered by copying the two modified files into the worktree and reverting the main repo's working-tree changes (`git checkout -- <files>` — only the two tracked files, no blanket reset). All subsequent edits used worktree-absolute paths. No commits were made to the wrong repo; the recovery was a clean file transfer before any commit.
- **`git` was accidentally run from the main repo on the first commit attempt** (a `cd` into the main repo path in a compound command), which the per-commit HEAD-assertion correctly caught — it refused to commit because the main repo's HEAD is on the protected `develop` branch. Re-run from the worktree (HEAD on `worktree-agent-*`) succeeded. The guard worked exactly as designed (#2924).
- **Build artifact cleanup.** `go build ./cmd/gateway/` writes a `gateway/gateway` ELF binary into the worktree. Removed it before finishing — it is transient build output, not a plan deliverable, and is not committed.

## Threat Surface

No new threat surface beyond the plan's `<threat_model>`. All four registered threats are mitigated as designed:

- **T-07-20 (Spoofing — /admin/metrics, /admin/audit mounting):** both routes are registered ONLY inside the `if px.adminVerifier != nil` block, after `adminRouter.Use(admin.Middleware(px.adminVerifier, log))` — the same Phase 4 X-Admin-Key bcrypt gate as `/admin/usage`. Verified by reading: `grep -c 'adminRouter.Method'` returns 3 (usage + metrics + audit), all inside the verifier block. The unauthenticated Prometheus `r.Handle("/metrics", obs.Handler())` is untouched — `git diff` shows no edit to that line.
- **T-07-21 (Repudiation — FSM transition audit):** every committed FSM transition now writes an append-only `audit_log` row with `event_kind = "fsm_transition"` via `audit.Writer`'s existing async batch writer. `TestFSMTransitionEmitsAuditRow` asserts exactly one row per committed transition and zero rows on a CAS-failed transition. The `audit_log` table has no UPDATE path — the trail is immutable.
- **T-07-22 (DoS — missed boot-window alerts):** `go alerter.Run(ctx)` is spawned textually before `go breakerSet.Subscribe(ctx)` and `go emergReconciler.Run(ctx)` — confirmed by `grep -n 'go .*[Aa]lerter\|go breakerSet.Subscribe\|go emergReconciler.Run'`: alerter spawn line 207 < breaker 300 < emerg ~709. `ReconcileBoot(ctx)` runs at startup to replay an active emergency incident found in the Redis state mirror.
- **T-07-23 (Information Disclosure — alert credentials at boot):** every disabled-channel WARN logs only the channel name + the missing env var NAME (e.g. `"chatwoot alert channel disabled — CHATWOOT_API_TOKEN unset"`) — never a token value. `buildAlertChannels` reads the credentials from `config.Config` plain-string fields and passes them straight into the client constructors; no credential is ever logged.

## Known Stubs

None. `buildAlertChannels` constructs real `alert.Channel` clients wired to live config. The alerter is constructed, `ReconcileBoot`-ed, and spawned. `/admin/metrics` + `/admin/audit` are mounted on the real admin sub-router with the real handlers from 07-03. The FSM audit call goes through the real `audit.Writer`. After this plan the gateway boots with full Phase 7 gateway-side observability + alerting.

## Next Phase Readiness

- **Phase 7 gateway-side wiring is complete.** `main.go` is the single composition root touched by this plan — alerter spawn, admin routes, FSM audit are all live. The remaining Phase 7 plans (dashboard frontend, UAT) consume `/admin/metrics` + `/admin/audit` over HTTP and observe the `fsm_transition` audit rows — no further `main.go` wiring is needed for them.
- **Verification green.** `cd gateway && go build ./...` exits 0; `go vet ./internal/emerg/ ./cmd/gateway/` clean; the full gateway test suite `go test ./... -count=1 -race` is green and race-clean — all 27 packages `ok`, zero FAIL, zero DATA RACE (the `internal/auth` package's argon2id suite took ~434s under `-race`, the known Phase 6 tech-debt item, but passed).
- **No blockers.**

## Self-Check: PASSED

- All 4 modified files exist on disk in the worktree.
- All 3 task commits reachable in git history: `dd626f8`, `ed824e8`, `f8bb020`.
- `go build ./...` exits 0; full `-race` suite green (27/27 packages `ok`).
- Acceptance criteria verified: alerter spawn line (207) < both publisher spawn lines (300, ~709); `ReconcileBoot` present; `log.Warn` count rose 15→25 (≥3 disabled-channel WARNs); `adminRouter.Method` count = 3; `r.Handle("/metrics", obs.Handler())` unchanged; `grep 'WriteStateChange("fsm_transition"'` found in `fsm.go`.

---
*Phase: 07-observability-dashboard-alerting*
*Completed: 2026-05-14*
