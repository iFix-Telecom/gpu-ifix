---
phase: quick-260702-nse
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/db/queries/primary_lifecycles.sql
  - gateway/internal/db/gen/primary_lifecycles.sql.go
  - gateway/internal/db/gen/querier.go
  - gateway/internal/primary/reconciler.go
  - gateway/internal/primary/reconciler_test.go
autonomous: true
requirements: [QUICK-260702-nse]

must_haves:
  truths:
    - "On provision attempts 1 and 2 (failStreak < 2) the reconciler searches the full qualified open market only — the machine_id {in: allowlist} preference pass never runs even when PRIMARY_VAST_MACHINE_ALLOWLIST is non-empty."
    - "On provision attempt 3+ (failStreak >= 2) the reconciler runs the existing allowlist-first-then-broaden behavior verbatim."
    - "failStreak counts the most-recent contiguous run of failed lifecycles (first_health_pass_at IS NULL AND ended_at IS NOT NULL), stopping at the first success, and never counts the currently-open (ended_at IS NULL) row."
    - "blocklist / reliability / cuda / driver / inet / num_gpus / price-cap / reject-private-ip filters stay applied in BOTH branches; only the allowlist preference pass is gated."
    - "The offer-pick log events carry the computed fail_streak and the chosen mode (market_cheapest | allowlist_preferred)."
  artifacts:
    - path: "gateway/db/queries/primary_lifecycles.sql"
      provides: "CountConsecutiveFailedPrimaryProvisions :one query"
      contains: "CountConsecutiveFailedPrimaryProvisions"
    - path: "gateway/internal/db/gen/primary_lifecycles.sql.go"
      provides: "sqlc-generated CountConsecutiveFailedPrimaryProvisions binding"
      contains: "func (q *Queries) CountConsecutiveFailedPrimaryProvisions"
    - path: "gateway/internal/primary/reconciler.go"
      provides: "failStreak gate around the allowlist preference pass in provisionLifecycle"
      contains: "failStreak"
  key_links:
    - from: "gateway/internal/primary/reconciler.go"
      to: "gateway/internal/db/gen (CountConsecutiveFailedPrimaryProvisions)"
      via: "q.CountConsecutiveFailedPrimaryProvisions(ctx) at the top of provisionLifecycle"
      pattern: "CountConsecutiveFailedPrimaryProvisions"
    - from: "provisionLifecycle allowlist pass"
      to: "failStreak >= 2 gate"
      via: "if guard wrapping the len(hot.VastMachineAllowlist) > 0 block"
      pattern: "failStreak >= 2"
---

<objective>
Change the primary pod provisioning policy so the reconciler self-vets the cheapest open-market offer on the first two provision attempts and only falls back to the PRIMARY_VAST_MACHINE_ALLOWLIST preference from the third attempt onward.

Today the allowlist-first pass pins host 7970 at $0.219/h on EVERY attempt, even when the open market has qualifying 1×RTX 3090 offers at ~$0.136/h (38% cheaper). By gating the allowlist pass behind a `failStreak >= 2` condition, attempts 1-2 go straight to the cheapest qualified market offer; only after two consecutive failures (which prove the cheap market is flaky right now) does the reconciler prefer the known-good allowlisted hosts.

Purpose: cut PROD primary pod cost ~38% when the cheap market is healthy, without giving up the known-good fallback when it is not.
Output: a new sqlc query, its regenerated binding, a gated allowlist block in provisionLifecycle with mode/fail_streak logging, and tests proving the three failStreak branches.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md

Repo layout note: the gateway Go module lives under `gateway/`. All `go`/`sqlc`/`gofmt` commands run from `gateway/`.

sqlc binary: `~/go/bin/sqlc` (v1.30.0). Regen command (run from `gateway/`):
`~/go/bin/sqlc generate` — reads `gateway/sqlc.yaml`, writes to `gateway/internal/db/gen/`. Never hand-edit files under `internal/db/gen/` — regenerate.

Primary integration tests carry `//go:build integration`. The unit tests this plan touches (`internal/primary/reconciler_test.go`) have NO build tag and run under `go test ./...`.

<interfaces>
From gateway/internal/emerg/vast/types.go — filter construction (do NOT change these signatures):
```go
// Full qualified market filter pair [primary, fallback]; blocklist applied as machine_id {notin}.
func DefaultSearchFilters(primaryCap, fallbackCap float64, primaryHostID int64,
    primaryGPU, fallbackGPU string, primaryNumGPUs, fallbackNumGPUs int, blocklist ...int64) []SearchFilter
// Replaces machine_id clause with {in: allowlist}; returns f unchanged when allowlist empty.
func WithMachineAllowlist(f SearchFilter, allowlist []int64) SearchFilter
```

From gateway/internal/primary/reconciler.go — query handle + test injection:
```go
func (r *Reconciler) queries() *gen.Queries   // nil when no DB wired
func (r *Reconciler) SetQueriesForTest(q *gen.Queries)
```

From gateway/internal/db/gen (existing :one count shape to mirror):
```go
func (q *Queries) CountSensitivePeakInvariant(ctx context.Context) (int64, error)
```

Test harness (gateway/internal/primary/reconciler_test.go):
- `fakeVast.searchOffersFn func(ctx, vast.SearchFilter) ([]vast.Offer, error)` — capture the filter shape reaching SearchOffers.
- `fakeDBTX.queryRowFn func(ctx, sql string, args ...any) pgx.Row` — script QueryRow per-query by matching on the raw `sql` string; `r.SetQueriesForTest(gen.New(dbtx))` wires it.
- The count query returns a single `int64`; script a row whose `Scan(dest ...any)` sets `*dest[0].(*int64)`.
- Existing tests `TestReconcilerVastFallback` (line ~931) and `TestProvisionLifecycle_OfferSelectionReadsSnapshotNotCfg` (line ~1019) drive `r.provisionLifecycle(ctx, id, log)` directly with `cfg.PrimaryVastMachineAllowlist = nil` — use them as the harness template.

The pod_config allowlist read inside provisionLifecycle is `hot.VastMachineAllowlist` (from `hot := r.liveCfg()`), NOT `cfg.PrimaryVastMachineAllowlist`. Tests must set the allowlist on BOTH cfg and the pod_config snapshot loader (see `NewStaticLoaderForTest` usage in TestProvisionLifecycle_OfferSelectionReadsSnapshotNotCfg) so `hot.VastMachineAllowlist` is non-empty.
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Add CountConsecutiveFailedPrimaryProvisions query, regen sqlc, write failing gate tests (RED)</name>
  <files>gateway/db/queries/primary_lifecycles.sql, gateway/internal/db/gen/primary_lifecycles.sql.go, gateway/internal/db/gen/querier.go, gateway/internal/primary/reconciler_test.go</files>
  <behavior>
    Query semantics (CountConsecutiveFailedPrimaryProvisions → int64):
    - No lifecycle rows at all → 0.
    - Only the currently-open row exists (ended_at IS NULL) → 0 (open row excluded).
    - Newest ended row is a success (first_health_pass_at IS NOT NULL) → 0.
    - Newest two ended rows are failures then a success → 2.
    - Failures are contiguous from newest; the count stops at the first success.

    Reconciler gate tests (drive provisionLifecycle directly, allowlist NON-empty in cfg + snapshot):
    - Test A (failStreak 0): SearchOffers receives a filter with NO `machine_id {in: ...}` clause on the first call (market-only), even though allowlist is non-empty. A `machine_id {notin: ...}` blocklist clause MAY be present; assert absence of the `in` key specifically.
    - Test B (failStreak 1): same as A — market-only, no `in` clause.
    - Test C (failStreak 2): the FIRST SearchOffers call receives a filter carrying `machine_id {in: allowlist}` (allowlist-first pass ran).
    Script the count via `fakeDBTX.queryRowFn` matching the count SQL (e.g. `strings.Contains(sql, "fail_streak")`) and returning an int64 row.
  </behavior>
  <action>
Add a new `-- name: CountConsecutiveFailedPrimaryProvisions :one` query to gateway/db/queries/primary_lifecycles.sql. It returns the length of the most-recent contiguous run of FAILED primary lifecycles. A failed lifecycle = `first_health_pass_at IS NULL AND ended_at IS NOT NULL`; a success = `first_health_pass_at IS NOT NULL`. Exclude the currently-open row by restricting to `ended_at IS NOT NULL`. Implement with a window: number ended rows `ROW_NUMBER() OVER (ORDER BY id DESC) AS rn`, find `MIN(rn)` among rows where `first_health_pass_at IS NOT NULL` (the newest success), and `COUNT(*)::bigint AS fail_streak` of failure rows whose `rn` is less than that success rn (use `COALESCE(min_success_rn, <large sentinel>)` so "no success yet" counts every ended failure). Column alias `fail_streak`, cast `::bigint`. Match the schema in gateway/db/migrations/0023_primary_lifecycles.sql (table `ai_gateway.primary_lifecycles`, BIGSERIAL `id`).

Regenerate: from `gateway/`, run `~/go/bin/sqlc generate`. This must add `func (q *Queries) CountConsecutiveFailedPrimaryProvisions(ctx context.Context) (int64, error)` to internal/db/gen/primary_lifecycles.sql.go and the method to the Querier interface. Do NOT hand-edit gen files.

Write the failing tests in gateway/internal/primary/reconciler_test.go following the existing `TestReconcilerVastFallback` / `TestProvisionLifecycle_OfferSelectionReadsSnapshotNotCfg` pattern: build a reconciler, set `cfg.PrimaryVastMachineAllowlist` AND the pod_config snapshot allowlist (via the static loader) to a NON-empty slice, wire a `fakeDBTX` whose `queryRowFn` returns the desired failStreak int64 for the count SQL, capture the first SearchOffers filter via `searchOffersFn`, and assert on presence/absence of the `machine_id["in"]` key. Cover failStreak 0, 1, and 2 (name them e.g. TestReconcilerAllowlistGate_*). Add a fake count row type (mirror `insertReturningRow`) that scans an int64 into `dest[0]`. These tests MUST FAIL now because the gate does not exist yet (attempt-1 currently runs the allowlist pass).
  </action>
  <verify>
    <automated>cd gateway && ~/go/bin/sqlc generate && grep -q "func (q \*Queries) CountConsecutiveFailedPrimaryProvisions" internal/db/gen/primary_lifecycles.sql.go && go build ./internal/db/gen/... && (go test ./internal/primary/ -run 'TestReconcilerAllowlistGate' 2>&1 | grep -q -E 'FAIL|no tests to run|undefined' && echo RED-OK)</automated>
  </verify>
  <done>New query present in .sql; sqlc regen adds the binding to gen (verified by grep); the new gate tests exist and FAIL (RED) because provisionLifecycle does not yet gate the allowlist pass; `go build ./internal/db/gen/...` compiles.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Gate the allowlist pass behind failStreak >= 2 in provisionLifecycle (GREEN) + mode/fail_streak logging</name>
  <files>gateway/internal/primary/reconciler.go</files>
  <behavior>
    - failStreak computed once at the top of provisionLifecycle: `q := r.queries()`; if `q == nil` → failStreak 0; else call `CountConsecutiveFailedPrimaryProvisions(ctx)`; on error, log a warning and default failStreak to 0 (do NOT abort provisioning — a count failure must not block the pod coming up).
    - `mode := "market_cheapest"` when failStreak < 2, `"allowlist_preferred"` when >= 2.
    - Allowlist preference pass (`if len(hot.VastMachineAllowlist) > 0 { ... }`) runs ONLY when `failStreak >= 2`. When failStreak < 2 the block is skipped entirely and each shape goes straight to the full qualified `SearchOffers(ctx, f)` broaden path (blocklist-only, dph_total asc, cheapest = pickable[0]).
    - All other filters unchanged in both branches.
    - `fail_streak` and `mode` appear on the "primary offers found for shape (allowlist pass)", "primary offers found for shape", and "primary offer picked" log events.
  </behavior>
  <action>
In gateway/internal/primary/reconciler.go `provisionLifecycle` (around lines 1271-1360), immediately after `hot := r.liveCfg(); r.provisionCfg.Store(&hot)` compute failStreak: read `q := r.queries()`; if q is nil set `failStreak := int64(0)`, else `failStreak, ferr := q.CountConsecutiveFailedPrimaryProvisions(ctx)` and on `ferr != nil` `log.Warn("primary failstreak count failed; defaulting to market_cheapest", "err", ferr)` + set failStreak to 0. Derive `mode := "market_cheapest"; if failStreak >= 2 { mode = "allowlist_preferred" }`.

Wrap the existing allowlist preference block — the `if len(hot.VastMachineAllowlist) > 0 { ... }` at ~lines 1315-1335 — so it only executes when `failStreak >= 2`. Change the condition to `if failStreak >= 2 && len(hot.VastMachineAllowlist) > 0 {`. Leave the broaden `SearchOffers(ctx, f)` path (the unrestricted, blocklist-only search) exactly as-is so it runs for every shape in both modes. Do NOT touch DefaultSearchFilters or WithMachineAllowlist.

Add `"fail_streak", failStreak, "mode", mode` to the three offer-pick log events: the allowlist-pass "primary offers found for shape (allowlist pass)" Info, the broaden "primary offers found for shape" Info, and the "primary offer picked" Info. Keep existing fields.

Note: this adds ONE QueryRow call per provision. Existing tests that drive provisionLifecycle with an unscripted `fakeDBTX` hit the errRow path → failStreak defaults to 0 → market mode; those tests already set allowlist=nil so behavior is unchanged. Beware: do not shadow the `q := r.queries()` handle already declared later in the function (~line 1387) — reuse a single handle or scope the new one so the file still compiles.
  </action>
  <verify>
    <automated>cd gateway && go test ./internal/primary/ -run 'TestReconcilerAllowlistGate|TestReconcilerVastFallback|TestProvisionLifecycle' 2>&1 | tail -6</automated>
  </verify>
  <done>The three new gate tests PASS (GREEN); pre-existing provisionLifecycle tests (TestReconcilerVastFallback, TestProvisionLifecycle_OfferSelectionReadsSnapshotNotCfg) still pass; allowlist pass runs only at failStreak>=2; log events carry fail_streak + mode.</done>
</task>

<task type="auto">
  <name>Task 3: Full test + format + sqlc drift check</name>
  <files>gateway/internal/primary/reconciler.go, gateway/internal/primary/reconciler_test.go, gateway/db/queries/primary_lifecycles.sql</files>
  <action>
Run the full gateway unit suite and the primary integration suite (integration tag) to confirm no regressions. Run `gofmt -l` on every touched Go file and fix any listed. Confirm `sqlc generate` produces no drift beyond the intended new query (re-run and check `git status`/`git diff` shows only the new binding). If the primary DB-backed integration tests can exercise the streak query against the fixture DB, add/confirm coverage; if not feasible against the fixture, the unit-level scripted tests from Task 1 are the coverage of record.
  </action>
  <verify>
    <automated>cd gateway && test -z "$(gofmt -l internal/primary/reconciler.go internal/primary/reconciler_test.go)" && go test ./... 2>&1 | tail -15 && go test -tags integration ./internal/integration_test/ -run Primary 2>&1 | tail -8 && ~/go/bin/sqlc generate && git status --porcelain gateway/internal/db/gen</automated>
  </verify>
  <done>`go test ./...` green; primary integration tests green; `gofmt -l` prints nothing for touched files; re-running `sqlc generate` yields no unexpected drift (only the new CountConsecutiveFailedPrimaryProvisions binding).</done>
</task>

</tasks>

<verification>
- failStreak query returns the contiguous newest-failure count, excludes the open row, and returns 0 when the newest ended row is a success.
- Attempts 1-2 (failStreak 0/1) never emit a `machine_id {in}` clause; attempt 3+ (failStreak 2) emits it on the first SearchOffers call.
- Blocklist and all other qualification filters remain present in both modes.
- Offer-pick log events carry `fail_streak` and `mode`.
- `go test ./...` + primary integration tests green; `gofmt -l` clean; no sqlc drift beyond the new query.
</verification>

<success_criteria>
- New `CountConsecutiveFailedPrimaryProvisions :one` query + regenerated sqlc binding committed (gen not hand-edited).
- provisionLifecycle gates the allowlist preference pass on `failStreak >= 2`; market-cheapest path runs at failStreak < 2.
- Three gate tests (failStreak 0/1/2) pass; pre-existing provisionLifecycle tests still pass.
- Full unit + primary integration suites green; formatting clean; sqlc regen clean.
</success_criteria>

<output>
Create `.planning/quick/260702-nse-primary-pod-provision-cheapest-market-na/260702-nse-SUMMARY.md` when done.
</output>
