---
task_id: 260528-h12
slug: breaker-snapshot-honor-force-override
mode: quick
type: execute
created: 2026-05-28
seed: .planning/seeds/SEED-005-health-endpoint-snapshot-ignores-force-override.md
debug_ref: .planning/debug/audit-blocked-sensitive-override-not-propagated.md
files_modified:
  - gateway/internal/breaker/breaker.go
  - gateway/internal/breaker/breaker_test.go
  - gateway/internal/upstreams/health.go
  - gateway/internal/upstreams/health_test.go
autonomous: true
non_goals:
  - Do NOT modify Snapshot() — keep both methods, document contract distinction.
  - Do NOT touch scripts/integration-smoke/smoke-sensitive-failover.py (SEED-006).
  - Do NOT touch dispatcher or audit middleware.
  - Do NOT add /admin/health/effective-state or any new HTTP surface.
  - Do NOT modify Redis keys, gatewayctl, or CheckForceOverride semantics.
must_haves:
  truths:
    - "/v1/health/upstreams reports state='forced-open' for any upstream with an active operator force-override."
    - "When force-override is active, /v1/health/upstreams overall status is degraded (or failed), never ok — matching the routing-layer reality."
    - "Snapshot() (the legacy method) is unchanged in behavior; any caller that wanted raw FSM still gets raw FSM."
  artifacts:
    - path: gateway/internal/breaker/breaker.go
      provides: "EffectiveStateSnapshot() map[string]string emitting 'forced-open' when CheckForceOverride is true."
    - path: gateway/internal/upstreams/health.go
      provides: "buildHealthResponse consumes EffectiveStateSnapshot."
    - path: gateway/internal/breaker/breaker_test.go
      provides: "TestEffectiveStateSnapshot — closed/open/forced-open coverage."
    - path: gateway/internal/upstreams/health_test.go
      provides: "Force-override sub-test asserting state='forced-open' + non-ok status."
  key_links:
    - from: "gateway/internal/upstreams/health.go:buildHealthResponse"
      to: "gateway/internal/breaker/breaker.go:EffectiveStateSnapshot"
      via: "bs.EffectiveStateSnapshot() call (replaces bs.Snapshot())"
      pattern: "bs\\.EffectiveStateSnapshot\\(\\)"
    - from: "gateway/internal/breaker/breaker.go:EffectiveStateSnapshot"
      to: "gateway/internal/breaker/breaker.go:CheckForceOverride"
      via: "per-name force-override check inside the snapshot loop"
      pattern: "CheckForceOverride"
---

<objective>
Promote SEED-005 to executable code. `/v1/health/upstreams` currently calls `Set.Snapshot()` which reads only the raw `gobreaker.CircuitBreaker.State()` per upstream and ignores the Redis-backed operator force-override. After `gatewayctl breaker force-open -upstream=local-llm -ttl=5m`, the dispatcher correctly emits `blocked_sensitive` 503s via `EffectiveState`, but the health endpoint reports `state="closed"` or `"half-open"` — misleading dashboards + breaking `smoke-sensitive-failover.py` pre-condition gates that poll for `OPEN_LIKE_STATES = {open, forced-open, FORCED_OPEN}`.

Add a new `EffectiveStateSnapshot()` method that mirrors `EffectiveState`'s contract (force-override honored), and switch the health endpoint to use it. Emit `"forced-open"` as the wire state when a force-override is active — matches what `smoke-sensitive-failover.py:150` already accepts.

Purpose: routing-layer reality and observation-layer reality must agree. Operator-driven breaker state must be visible to dashboards and smoke gates.
Output: 2 production-code changes + 2 test-code additions, all in a single PR / 3-commit sequence ready for develop merge + Portainer deploy.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
</execution_context>

<context>
@.planning/seeds/SEED-005-health-endpoint-snapshot-ignores-force-override.md
@.planning/debug/audit-blocked-sensitive-override-not-propagated.md
@gateway/internal/breaker/breaker.go
@gateway/internal/breaker/breaker_test.go
@gateway/internal/breaker/force_override.go
@gateway/internal/breaker/force_override_test.go
@gateway/internal/upstreams/health.go
@gateway/internal/upstreams/health_test.go

<interfaces>
<!-- Key types/contracts the executor needs. Extracted from the codebase. -->
<!-- Executor uses these directly — no codebase exploration needed. -->

From `gateway/internal/breaker/breaker.go`:
- `Set` struct holds `mu sync.RWMutex`, `cbs map[string]*gobreaker.CircuitBreaker[*http.Response]`, `forceCache *forceCache`.
- `(s *Set) CheckForceOverride(name string) bool` — pure cache read, returns true when `e.set && e.state == "open"`. Safe on hot path.
- `(s *Set) RefreshForceOverride(ctx context.Context, name string)` — forces a Redis GET + cache update for one name.
- `(s *Set) maybeRefreshForceOverride(name string)` — debounced refresh helper (1-second freshness window via `forceCacheFreshness`), uses `context.Background()` internally.
- `(s *Set) EffectiveState(name string) gobreaker.State` — already exists at lines 231–244; returns `StateOpen` when `CheckForceOverride(name)` is true, else delegates to `cb.State()`. Calls `maybeRefreshForceOverride(name)` once at the top. Unknown upstream → `StateClosed`.
- `(s *Set) Snapshot() map[string]string` — existing legacy method at lines 248–256; reads `cb.State().String()` per name under `s.mu.RLock()`. **STAYS UNCHANGED.**

From `gateway/internal/upstreams/health.go`:
- `buildHealthResponse(loader *Loader, bs *breaker.Set) ([]byte, int, error)` at line 119. Currently calls `snap = bs.Snapshot()` at line 134. Status derivation uses literal string `"closed"` at line 166 — anything that is NOT `"closed"` is treated as unhealthy (open / half-open / unknown all skip the `tier0Closed` + `roleHasClosed` increments). The status switch at lines 175–183 does NOT enumerate the unhealthy state strings explicitly; it only checks `st == "closed"`. So `"forced-open"` will automatically be treated as unhealthy without additional changes to the comparison logic.

From `gateway/internal/breaker/breaker_test.go` (test fixtures already in scope):
- `newTestSet(t, names, opts) (*Set, *miniredis.Miniredis)` at line 27.
- `fastOpts()` at line 40 — `ConsecutiveFailures: 3, Cooldown: 100ms`.
- `discardLogger()` at line 21.
- `TestSnapshotReturnsAllStates` at line 177 — pattern to mirror.

From `gateway/internal/breaker/force_override.go` + `force_override_test.go`:
- `ForceOverrideValue{State string, TTLSec int, SetBy string, SetAt time.Time}` — JSON shape stored at `gw:breaker:force:{name}`.
- `ForceOverrideKey(name string) string` — helper returning `"gw:breaker:force:" + name`.
- Pattern to install a force-override in tests (force_override_test.go:156–161):
  ```go
  val := ForceOverrideValue{State: "open", TTLSec: 300, SetBy: "operator", SetAt: time.Now().UTC()}
  buf, _ := json.Marshal(val)
  _ = rdb.Set(ctx, ForceOverrideKey("local-llm"), string(buf), 300*time.Second).Err()
  s.RefreshForceOverride(ctx, "local-llm")
  ```

From `gateway/internal/upstreams/health_test.go` (test fixtures already in scope):
- `sixUpstreams()` at line 22.
- `newMinRedis(t)` at line 37.
- `tripBreaker(t, bs, name)` at line 57 — drives a breaker to `StateOpen` via 3×503.
- `loaderNames(l)` at line 76.
- Status assertion patterns at lines 96–113.

From `scripts/integration-smoke/smoke-sensitive-failover.py:150`:
- `OPEN_LIKE_STATES = frozenset({"open", "forced-open", "FORCED_OPEN"})` — smoke already accepts the new string we are about to emit. NO smoke change needed.
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Add EffectiveStateSnapshot() to breaker.go + unit tests</name>
  <files>
    gateway/internal/breaker/breaker.go,
    gateway/internal/breaker/breaker_test.go
  </files>
  <behavior>
    - Sub-test (a) natural-closed: fresh breaker for upstream "a", no failures, no force-override. `s.EffectiveStateSnapshot()["a"] == "closed"`.
    - Sub-test (b) natural-open: drive breaker "b" to StateOpen via 3×503 (mirror TestSnapshotReturnsAllStates at breaker_test.go:177). No force-override. `s.EffectiveStateSnapshot()["b"] == "open"`.
    - Sub-test (c) force-override: install `ForceOverrideValue{State:"open", ...}` at `ForceOverrideKey("c")` in miniredis, call `s.RefreshForceOverride(ctx, "c")`, then assert `s.EffectiveStateSnapshot()["c"] == "forced-open"` regardless of natural state (which is `closed` since no failures were driven).
    - Sub-test (d, optional but cheap): force-override + naturally-open same upstream — `EffectiveStateSnapshot[name] == "forced-open"` wins. Install override on "d" AND drive it to StateOpen first; force-override emits "forced-open" not "open".
  </behavior>
  <action>
    In `gateway/internal/breaker/breaker.go`, add a new exported method `EffectiveStateSnapshot() map[string]string` directly below the existing `Snapshot()` method (around line 256). Method body:

    1. Acquire `s.mu.RLock()` and defer Unlock.
    2. Pre-allocate `out := make(map[string]string, len(s.cbs))`.
    3. Loop over the names in `s.cbs` (the names are the source of truth for "known upstreams"). For each `n`:
       - Call `s.CheckForceOverride(n)` (pure cache read — safe under RLock since forceCache has its own mutex). If true, set `out[n] = "forced-open"` and continue.
       - Otherwise set `out[n] = s.cbs[n].State().String()` (same shape as Snapshot).
    4. Return `out`.

    Refresh strategy: do NOT loop calling `RefreshForceOverride` per name (that would issue one Redis GET per upstream per health request and blow the 2s cache budget). Instead, call `s.maybeRefreshForceOverride(n)` for each name BEFORE acquiring `s.mu.RLock()` — `maybeRefreshForceOverride` is debounced by `forceCacheFreshness`, so in steady state this is a map-read per name with one Redis GET amortized across the freshness window. Concretely:

    ```
    // Pseudocode, do NOT inline:
    // 1. Snapshot the name set under a brief RLock so we don't hold the lock across Redis calls.
    // 2. For each name, call s.maybeRefreshForceOverride(name).
    // 3. Re-acquire RLock for the actual snapshot loop.
    ```

    Document the contract distinction in a godoc above the new method: "EffectiveStateSnapshot returns name→state-string with operator force-override honored (Phase 06.9 Plan 04). Values are 'closed', 'half-open', 'open', or 'forced-open'. Use this for any caller that decides routing or reports operational state (dashboard, /v1/health/upstreams). The legacy Snapshot() returns raw FSM state only; use it ONLY when you explicitly want to ignore force-override (e.g. debugging the natural FSM)."

    Cite SEED-005 by name in the godoc as the origin of the contract distinction.

    In `gateway/internal/breaker/breaker_test.go`, add a new test `TestEffectiveStateSnapshot` after `TestSnapshotReturnsAllStates` (line 188). Use `t.Run` for the 4 sub-tests above. Mirror the existing fixtures: `newTestSet`, `fastOpts`, the `for i := 0; i < 3; i++ { _, _ = s.Execute(...) }` pattern for tripping breakers, and the `ForceOverrideValue` + `rdb.Set` + `RefreshForceOverride` pattern from `force_override_test.go:156–169` for installing the override. You will need to add imports for `encoding/json` if not present and confirm the test file is in the `breaker` package (so `ForceOverrideValue`/`ForceOverrideKey` are accessible unqualified).
  </action>
  <verify>
    <automated>cd gateway && go test ./internal/breaker/... -run TestEffectiveStateSnapshot -v</automated>
  </verify>
  <done>
    `go test ./internal/breaker/...` green with 4 new sub-tests passing. `go build ./...` green. `Snapshot()` is byte-identical to its pre-change state (no edits to lines 246–256). New godoc on `EffectiveStateSnapshot` references SEED-005.
  </done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Switch buildHealthResponse to EffectiveStateSnapshot + health_test coverage</name>
  <files>
    gateway/internal/upstreams/health.go,
    gateway/internal/upstreams/health_test.go
  </files>
  <behavior>
    - New sub-test `TestHealthHandler_ForceOverrideEmitsForcedOpen`: install a force-override on `local-llm` (via the `ForceOverrideValue` + `rdb.Set` + `RefreshForceOverride` pattern), GET `/v1/health/upstreams`, assert:
      1. `body.upstreams.local-llm.state == "forced-open"`
      2. `body.status` is NOT `"ok"` — must be `"degraded"` because the same role's tier-1 (`openrouter-chat`) is closed (allRolesHaveAnyClosed=true, allTier0Closed=false → degraded).
      3. HTTP code is 200 (degraded keeps 200 per existing contract at health.go:174–183).
    - Existing tests (`TestHealthHandler_AllClosed_OK`, `TestHealthHandler_Tier0OpenButTier1Closed_Degraded`, `TestHealthHandler_NoClosedForRole_Failed`, `TestHealthHandler_Cache2s`) MUST continue to pass — the natural-FSM paths must still produce `"closed"`/`"open"`/`"half-open"` as before.
  </behavior>
  <action>
    In `gateway/internal/upstreams/health.go`:

    1. At line 134, replace `snap = bs.Snapshot()` with `snap = bs.EffectiveStateSnapshot()`. No other line in `buildHealthResponse` changes — the status derivation at lines 166–183 only checks `st == "closed"` (anything else falls through to degraded/failed buckets), so `"forced-open"` is automatically classified as unhealthy without further edits.
    2. Update the `upstreamStatus.State` field godoc at line 41–42 to add `"forced-open"` to the enumerated values: change `// "closed" | "half-open" | "open" | "unknown"` to `// "closed" | "half-open" | "open" | "forced-open" | "unknown"`.
    3. Update the package-doc at lines 1–13 to mention that the snapshot honors operator force-override (one sentence: "State is derived from breaker.Set.EffectiveStateSnapshot, which honors operator force-overrides installed via gatewayctl breaker force-open (SEED-005)."). Keep the existing Phase 3 history intact.

    In `gateway/internal/upstreams/health_test.go`:

    1. Add the new sub-test `TestHealthHandler_ForceOverrideEmitsForcedOpen` after `TestHealthHandler_NoClosedForRole_Failed` (line 174). Test outline (mirror existing fixtures):

       ```
       // Pseudocode outline — do NOT inline as final code:
       loader := upstreams.NewLoaderForTest(sixUpstreams()...)
       rdb := newMinRedis(t)
       bs := breaker.NewSet(rdb, discardLogger(), breaker.DefaultOptions(), loaderNames(loader))

       // Install force-override on local-llm.
       val := breaker.ForceOverrideValue{State: "open", TTLSec: 300, SetBy: "test", SetAt: time.Now().UTC()}
       buf, _ := json.Marshal(val)
       _ = rdb.Set(context.Background(), breaker.ForceOverrideKey("local-llm"), string(buf), 300*time.Second).Err()
       bs.RefreshForceOverride(context.Background(), "local-llm")

       h := upstreams.NewHealthHandler(loader, bs, discardLogger())
       rec := httptest.NewRecorder()
       req := httptest.NewRequest(http.MethodGet, "/v1/health/upstreams", nil)
       h.ServeHTTP(rec, req)

       // Assertions:
       // - rec.Code == 200 (degraded → 200)
       // - body.upstreams.local-llm.state == "forced-open"
       // - body.status == "degraded"
       ```

    2. Add the necessary imports to `health_test.go`: `context`, `encoding/json` (likely already imported — confirm). `breaker` is already imported at line 16.

    3. Do NOT modify the existing 4 tests. They must remain byte-identical to confirm the natural-FSM path is unchanged.
  </action>
  <verify>
    <automated>cd gateway && go test ./internal/upstreams/... -run TestHealthHandler -v</automated>
  </verify>
  <done>
    All 5 health-handler tests pass (4 existing + 1 new). `go build ./...` green. `health.go:134` calls `bs.EffectiveStateSnapshot()`. `upstreamStatus.State` godoc enumerates the new `"forced-open"` value. SEED-005 is cited in the package-doc comment.
  </done>
</task>

<task type="auto">
  <name>Task 3: Full build + repo-wide test + git push</name>
  <files>(no files modified — verification + commit/push)</files>
  <action>
    1. From the repo root, run `cd gateway && go build ./...` — must be green.
    2. From the repo root, run `cd gateway && go test ./internal/breaker/... ./internal/upstreams/...` — must be green (this is the full surface affected; do NOT run the whole gateway test suite here unless explicitly requested, to keep the quick-mode loop tight). If `go vet` flags any new warnings on the changed files, fix them before commit.
    3. Stage the 4 modified files only (do NOT `git add -A`):
       - `gateway/internal/breaker/breaker.go`
       - `gateway/internal/breaker/breaker_test.go`
       - `gateway/internal/upstreams/health.go`
       - `gateway/internal/upstreams/health_test.go`
    4. Commit with a message that ties to SEED-005 and the audit debug session, using the project's commit-format. Suggested message:

       ```
       fix(breaker,health): /v1/health/upstreams snapshot honors force-override

       Add Set.EffectiveStateSnapshot() that emits "forced-open" when an
       operator force-override is active, and route buildHealthResponse
       through it. Snapshot() stays in place for callers that explicitly
       want raw FSM state.

       Promotes SEED-005. Unblocks smoke-sensitive-failover.py
       pre-condition gate (OPEN_LIKE_STATES already accepts "forced-open").

       Refs: SEED-005,
             .planning/debug/audit-blocked-sensitive-override-not-propagated.md

       Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
       ```

    5. Push to the current branch (`gsd/phase-06.9-close`). Do NOT switch branches, do NOT force-push, do NOT merge to develop here — that is the deploy operator's call.
    6. After push, run `git log -1 --stat` and report the commit SHA + diffstat back to the operator. Manual sanity step (operator runs, not the executor): after develop merge + Portainer redeploy, `ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open -upstream=local-llm -ttl=1m'` + `curl -sS https://ai-gateway.converse-ai.app/v1/health/upstreams | jq .upstreams."local-llm".state` should report `"forced-open"` and the top-level `.status` should be `"degraded"`, not `"ok"`.
  </action>
  <verify>
    <automated>cd gateway && go build ./... && go test ./internal/breaker/... ./internal/upstreams/...</automated>
  </verify>
  <done>
    Build + tests green. Single commit pushed to `gsd/phase-06.9-close` with the 4 file changes and a message referencing SEED-005. Operator has the commit SHA and the manual-sanity recipe.
  </done>
</task>

</tasks>

<verification>
1. `cd gateway && go build ./...` — green.
2. `cd gateway && go test ./internal/breaker/... ./internal/upstreams/...` — green; 4 new breaker sub-tests + 1 new health sub-test added; all pre-existing tests in those packages still pass byte-identically.
3. `grep -n "bs.Snapshot()" gateway/internal/upstreams/health.go` returns zero matches; `grep -n "bs.EffectiveStateSnapshot()" gateway/internal/upstreams/health.go` returns exactly one match at the former line 134.
4. `grep -n "func (s \*Set) Snapshot()" gateway/internal/breaker/breaker.go` still returns one match (legacy method preserved).
5. `grep -n "func (s \*Set) EffectiveStateSnapshot()" gateway/internal/breaker/breaker.go` returns one match (new method added).
6. Manual post-deploy sanity (operator-driven, NOT part of the executor's verify):
   - `ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open -upstream=local-llm -ttl=1m'`
   - `curl -sS https://ai-gateway.converse-ai.app/v1/health/upstreams | jq '.upstreams."local-llm".state, .status'`
   - Expect: `"forced-open"` and `"degraded"` (or `"failed"` if all roles have only tier-0 forced).
   - Cleanup: `ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close -upstream=local-llm'`.
</verification>

<success_criteria>
- `Set.EffectiveStateSnapshot()` exists and emits `"forced-open"` when `CheckForceOverride(name) == true`, else `cb.State().String()`.
- `Set.Snapshot()` is byte-identical to pre-change.
- `buildHealthResponse` calls `bs.EffectiveStateSnapshot()`. No other call site of `Snapshot()` changed.
- 4 new breaker sub-tests + 1 new health sub-test pass; all pre-existing tests in both packages pass.
- `go build ./...` and the scoped `go test` are green.
- Single commit pushed to `gsd/phase-06.9-close` referencing SEED-005 and the debug session.
- Smoke script `scripts/integration-smoke/smoke-sensitive-failover.py` is NOT touched (SEED-006).
- Dispatcher, audit middleware, gatewayctl, Redis keys, and the `/admin` surface are NOT touched.
</success_criteria>

<output>
After Task 3 completes, write a brief SUMMARY back to the operator with:
- Commit SHA + 1-line diffstat.
- The exact `curl` + `gatewayctl` recipe in the verification section as the post-deploy smoke-test.
- A note that SEED-005 can be marked `status: shipped` once the develop merge + Portainer redeploy lands and the manual sanity check passes.
- Do NOT create a `.planning/quick/260528-h12-.../SUMMARY.md` file unless the operator asks — quick-mode summaries are returned inline by the executor.
</output>
