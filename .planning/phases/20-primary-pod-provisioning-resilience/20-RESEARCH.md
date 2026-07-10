# Phase 20 — RESEARCH: what already exists (map, don't rebuild)

**Gathered:** 2026-07-10
**Method:** every claim below read from the file at the cited line. No guessing.

## ⚠️ Scope correction that changes the plan

**The primary reconciler is `gateway/internal/primary/`, NOT `gateway/internal/emerg/`.** The task prompt and `20-CONTEXT.md:50` both say `emerg/lifecycle.go` — that is the EMERGENCY pod (a separate FSM). The primary-pod coldstart loop, fail_streak, and picker all live in `gateway/internal/primary/reconciler.go` (88 KB) + `gateway/internal/primary/lifecycle.go`. Everything the planner touches for FF-01/FF-02/BL-01/AL-01 is in `primary/`, not `emerg/`. The two packages are deliberately import-cycle-separated (`primary/lifecycle.go:21-31` redeclares `VastAPI` to avoid importing emerg).

**Biggest scope-shrinker:** `UpdatePodConfigFieldAllowlist` **already exists** (`gateway/internal/db/gen/pod_config.sql.go:362`) AND the `allowlist` PATCH case already exists (`config_write.go:155-161`) AND the dashboard already renders an allowlist control (`pod-config-controls.tsx:100`). AL-01 does **not** need a new query/PATCH/UI — it only needs the reconciler to CALL the existing `UpdatePodConfigFieldAllowlist` on success. Same for BL-01: `UpdatePodConfigFieldBlocklist` (`pod_config.sql.go:371`) + PATCH `blocklist` case (`config_write.go:148`) already exist and are consumed by the picker; the gap is purely auto-CALLING them.

---

## BL-01 — auto-blocklist on repeated failure (reuse fail_streak)

**What exists:**
- `CountConsecutiveFailedPrimaryProvisions` query — source `gateway/db/queries/primary_lifecycles.sql:78-103`, generated `gateway/internal/db/gen/primary_lifecycles.sql.go:45-78`. Definition of a "failed" lifecycle (sql:80-84): `first_health_pass_at IS NULL AND ended_at IS NOT NULL`. A success = `first_health_pass_at IS NOT NULL`. The open row (`ended_at IS NULL`) is excluded; the count is the newest contiguous run of failures and STOPS at the first success.
- The picker consumes it: `reconciler.go:1292-1304` computes `failStreak`, sets `mode = "allowlist_preferred"` iff `failStreak >= 2`. The allowlist-first pass fires only when `failStreak >= 2 && len(hot.VastMachineAllowlist) > 0` (`reconciler.go:1339`). Blocklist is applied as a Vast search-filter exclusion via `vast.DefaultSearchFilters(..., hot.VastMachineBlocklist...)` (`reconciler.go:1310-1316`).
- The write queries `UpdatePodConfigFieldBlocklist(ctx, []int64)` (`pod_config.sql.go:371-384`) and `UpdatePodConfigFieldAllowlist(ctx, []int64)` (`pod_config.sql.go:362-369`) both exist.
- **Confirmed nothing writes them today:** `grep UpdatePodConfigFieldBlocklist|Allowlist` outside `config_write.go`/`db/gen`/`_test` → ZERO hits. The lists are populated ONLY by the manual PATCH (`config_write.go:148,155`). This matches SEED-009 §"no auto-blocklist in code".

**The gap:** auto-POPULATE the lists on lifecycle outcome. Not a counter (the counter is done), not a schema change.

**Where to hook (same outcome events for both BL-01 + AL-01):** `provisionLifecycle` (`reconciler.go:1271`) ends at `return r.waitForReadyOrDestroy(...)` (`reconciler.go:1438`). At that line **`offer` is still in scope** → `offer.MachineID` is available. This is the ONE site where machine_id + success/failure are both known:
```go
// reconciler.go:1438 (current)
return r.waitForReadyOrDestroy(ctx, lifecycleID, instance.ID, offer.DphTotal, log)
```
Wrap it: `err := r.waitForReadyOrDestroy(...)`; `err != nil` → auto-blocklist `offer.MachineID` (+ remove from allowlist); `err == nil` → auto-allowlist `offer.MachineID` (+ remove from blocklist). Post-pick failures (`create_error`, terminal, port_bind, health_timeout, status_msg_error) all route back through this return. Pre-pick failures (`search_failed`, `no_offers_below_cap`, `build_create_request_failed` at `reconciler.go:1343,1365,1384,1405,1410`) have NO machine → nothing to blocklist, correctly skipped.
- **BL-01 threshold nuance:** decision #4 says "ban when fail_streak≥2". `failStreak` was read at the TOP of THIS attempt (before it counted). So after this attempt fails, the effective consecutive count is `failStreak+1`. To ban on the 2nd consecutive failure, gate the blocklist-append on `failStreak >= 1`. **Planner must pick the exact predicate** (this is the only real logic decision in BL-01).
- The blocklist/allowlist are `[]int64` in `pod_config`; read the live list via `r.liveCfg()` / the captured `hot` (`reconciler.go:1282`), append+dedup+write. Allowlist cap ~20 FIFO + dedup per decision #5.
- **`machine_id` is also persisted** in the `offer_accepted` event JSONB (`reconciler.go:1419`) — a fallback source if a later refactor needs it outside `offer` scope, but the line-1438 hook makes that unnecessary.

**quick-260702-nse artifacts:** `.planning/quick/260702-nse-primary-pod-provision-cheapest-market-na/` (`260702-nse-PLAN.md` + `260702-nse-SUMMARY.md`) — this is where the `fail_streak<2 market / >=2 allowlist_preferred` policy shipped.

---

## FF-01 / FF-02 — coldstart poll loop fast-fail (`created`-budget + progress-stall)

**The loop:** `waitForReadyOrDestroy` (`reconciler.go:1453-1680`). Current exit conditions:
- `ctx.Done()` → destroy + `cancelled_in_flight` (1520-1523).
- `deadline.C` = `time.NewTimer(hot.ColdStartBudgetS * time.Second)` → destroy + `health_timeout` (1460, 1524-1527). **This is the only ceiling on regime 1/2/3 today** — default `PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS=2400` (40 min; `config.go:243,510`). (Note: `pod_config.coldstart_budget_s` overrides via the `hot` snapshot; `config.go` is the disaster fallback.)
- `ErrInstanceNotFound` ×3 strikes → `instance_terminal_state_confirmed` (1531-1544).
- `inst.StatusMsg` contains "error" → `vast_status_msg_error:<trunc>` early abort (1556-1571). *(Reviews #11 — a Vast-surfaced error msg fails fast already.)*
- `inst.IsTerminal()` ×3 strikes → `instance_terminal_state` (1572-1583). `IsTerminal` = `actual_status ∈ {exited, unknown, offline}` (`vast/types.go:172-183`).
- **Regime 1 (stuck at `created`):** `actual_status != "running"` → bare `continue` (1587-1593), resetting `firstRunningAt`. **NO sub-budget.** A pod stuck at `created`/`scheduling` burns the full `ColdStartBudgetS`. ← **FF-01 gap confirmed.**
- **Port-bind budget (already exists, Phase 6.6.Y):** once `actual_status == "running"`, `firstRunningAt` is anchored (1594-1597). Two catches fire `public_port_bind_timeout` once `elapsed >= hot.PortBindBudgetS` (default 120s; `config.go:241,508`): (a) empty pod URLs (1599-1622), (b) `r.deps.Reachable != nil && !Reachable(urls.LLM)` = TCP dial timeout/no-route (1634-1653). **CRITICAL:** the reachability gate keys on CONNECTION-level failure only — connection-*refused* returns `Reachable==true` (host up, service warming) so a legit slow download keeps polling (`reconciler.go:174-193` Deps.Reachable doc). This covers SEED regime "host TCP-unreachable" (companion problem 1), NOT regime 1 (`created`, never reaches running) and NOT regime 3 (host reachable, download stalls in-container).
- Success: all 4 of `HealthCheck(LLM/STT/TTS/DCGM)` pass → `markReady` (1658-1677).

**Where FF-01 hooks (`created`-budget):** a second anchor + budget mirroring `firstRunningAt`/`portBindBudget`. Anchor at first poll where instance exists but `actual_status != "running"` (the `continue` at 1587-1593); if contiguous non-running time exceeds `created_budget_s` → destroy + close with a new reason (e.g. `created_state_timeout`). Mirror the `>= budget` `int(elapsed.Seconds())` + per-poll Warn pattern at 1605-1620.

**Where FF-02 hooks (progress-stall):** the port-bind reachability path does NOT see in-container download progress (host reachable, `:9100` health-bridge not up yet — see onstart order below). FF-02 needs the onstart-log heartbeat (FF-03) polled inside this loop with a `progress_stall_budget_s`: if the last heartbeat line advanced > budget ago while still non-ready → destroy + close (`progress_stall_timeout`). This is net-new signal ingestion, not just a timer.

**Does 6.6.Y reachability already cover regime 1/2?** Partially: it covers the SEED "running + published ports + TCP-unreachable" host (companion problem 1, machine `97859`). It does NOT cover regime 1 (`24953` stuck at `created` — never anchors `firstRunningAt`) nor regime 3 (download stall — Reachable returns true). So FF-01 + FF-02 are genuinely uncovered.

**Budgets come from the captured snapshot** `hot := r.currentProvisionCfg()` (`reconciler.go:1459`) — stable for the in-flight attempt (T-17-09). New `created_budget_s`/`progress_stall_budget_s` must be added to `podconfig.PodConfig` so they ride the same snapshot.

---

## FF-03 — Vast logs API + SSH-tail fallback

**Vast client to mirror:** `gateway/internal/emerg/vast/client.go`. Pattern for a new `RequestLogs`/`FetchLogs`:
- Construct URL `fmt.Sprintf("%s/...", c.baseURL)` (baseURL `https://console.vast.ai/api/v0`, `client.go:73`).
- Auth via `c.setAuthHeader(req)` — the ONE place the key touches a request (`client.go:331-333`), `Authorization: Bearer <key>`. Never log the key (enforced by `TestClientNeverLogsAPIKey`).
- `http.NewRequestWithContext` + `c.httpClient.Do` (30s timeout, `client.go:79,98`).
- Metrics: `obs.GatewayVastAPIRequestsTotal.WithLabelValues("<op>", "<status>")` (e.g. `client.go:257-264`).
- Errors via `c.parseErrorBody(resp)` (`client.go:343-385`).
- Vast logs API per decision #1: `PUT /api/v0/instances/request_logs/{id}/` → response `result_url` → `GET result_url` (async, ~seconds). **`result_url` shape is an OPEN QUESTION — validate on first real coldstart (CONTEXT §Claude's Discretion).**
- Methods take/return simple DTOs; add to the `primary.VastAPI` interface (`primary/lifecycle.go:26-31`) + `emerg.VastAPI` if shared, so the reconciler can call it and tests can fake it.

**SSH-tail fallback path:** there is **NO Go SSH client** in the reconciler. `grep ssh` in `gateway/internal/` returns only: the `SshHost`/`SshPort` struct fields (`vast/types.go:142-143`) and the emerg onstart bash (`emerg/lifecycle.go:682-697`). SSH tail lives ONLY in bash: `pod/scripts/vast-ai.sh ssh-exec` (`vast-ai.sh:147-159`, `ssh -o StrictHostKeyChecking=accept-new ...`). So FF-03(b) SSH-tail in Go is **net-new** — either shell out to `ssh`/`vast-ai.sh` or add an `x/crypto/ssh` client. `inst.SshHost`/`inst.SshPort` are available from `GetInstance`. **Decision for planner:** the SSH host publishes early (independent of onstart), reading the same `/var/log/onstart.log`. Given no Go SSH exists and decision #1 makes Vast logs API primary, the lazy path is: Vast logs API in Go, SSH-tail as a shell-out fallback (or defer SSH entirely to UAT if the logs API proves reliable — CONTEXT §Claude's Discretion allows falling 100% onto SSH OR onto logs API).

---

## FF-02 / OBS-01 — onstart log source + download heartbeat

- `pod/onstart.sh:20-22` writes `/var/log/onstart.log` via `exec > >(tee -a /var/log/onstart.log) 2>&1`. ✓ confirmed — the log is captured to file AND stdout (Vast console).
- **onstart order (matches CONTEXT):** preflight (`onstart.sh:31`) → resolve + run `download-weights.sh` (blocking, before line 100) → `docker compose up -d` (`onstart.sh:105`) → wait for health-bridge `:9100` readiness (`onstart.sh:107-126`). So during the weights download the `:9100` health-bridge does NOT exist yet → the only external progress signal is `actual_status` + the onstart log. ✓ regime-3 blindness confirmed.
- `pod/scripts/download-weights.sh:55` uses `mc cp --quiet` inside `download_and_verify()`. Per-file it logs exactly TWO lines: `log "fetching ${key} -> ${dest}"` (line 54, at start) and `log "ok ${dest} (sha256=...)"` (line 67, at end). `log()` = `printf '[%s] [download-weights] %s\n' "$(date -Iseconds)" ...` (line 32). The 3 downloads run in PARALLEL (`download-weights.sh:78-83`). **With `--quiet`, a 16 GB Qwen emits ZERO output between "fetching" and "ok"** → a stall mid-file is invisible (FF-02 blind). ← OBS-01 fix: drop `--quiet` (mc then prints a periodic progress line = the mid-file heartbeat FF-02 keys on) OR emit periodic progress. **A heartbeat line looks like** an ISO-8601-timestamped `[download-weights]` line; FF-02's stall detector watches for the newest such timestamp to keep advancing.

---

## CFG-01 / UI-01 — 2 new budget fields end-to-end (exact molde)

Latest goose migration = `gateway/db/migrations/0032_replace_piper_with_kokoro_tts.sql` → **new migration is `0033`**. `pod_config` table defined in `0031_create_pod_config.sql`.

To add `created_budget_s` + `progress_stall_budget_s` (each: 1 hot INTEGER col + min + max), mirror `coldstart_budget_s`/`port_bind_budget_s` at EVERY layer:

1. **Migration** `0033_*.sql`: `ALTER TABLE ai_gateway.pod_config ADD COLUMN created_budget_s INTEGER NOT NULL DEFAULT <d>, ADD COLUMN created_budget_s_min ..., ADD COLUMN created_budget_s_max ...` (+ same 3 for progress_stall). **Also extend the UPDATE NOTIFY trigger WHEN clause** (`0031...sql` lists every column in `pod_config_update_notify` `WHEN (...)` — the new 6 cols must be added or edits won't fire a reload). Set defaults so a `DEFAULT` covers existing rows (0031 columns carry NO SQL default + a Go seed; simplest for an ALTER is a real DEFAULT).
2. **sqlc regen** `gateway/internal/db/gen/pod_config.sql.go`: `GetPodConfig` SELECT + `Scan` (lines 15-64) gain the 6 cols; `SeedPodConfig` INSERT + params struct (67-175) gain 6; add `UpdatePodConfigFieldCreatedBudgetS` + `...ProgressStallBudgetS` (mirror `UpdatePodConfigFieldColdstartBudgetS` at 404-411) + 4 bound updaters (mirror `UpdatePodConfigBoundColdstartBudgetSMin/Max` 218-234). Also `gen/models.go` `AiGatewayPodConfig` struct + `SeedPodConfig` query source `gateway/db/queries/pod_config.sql`.
3. **`podconfig` typed view** `gateway/internal/podconfig/types.go`: add `CreatedBudgetS int` + `ProgressStallBudgetS int` to `PodConfig` (struct at :28, mapping in `rowToPodConfig` :162-181), and `...Min/...Max` to `PodConfigBounds` (:50, `rowToBounds` :185-208).
4. **PATCH handler** `gateway/internal/admin/config_write.go`: add to the `podConfigWriteQueries` interface (:53-91), add 2 `case` in `writeConfig` (mirror :184-187 `coldstart_budget_s`) + 4 `case` in `writeBound` (mirror :254-261). Uses `h.writeIntConfig`/`writeIntBoundMin/Max` already generic.
5. **GET read** `gateway/internal/admin/config_read.go:50-51,69-72,144-145,160-161` — add the 6 fields to the response struct + mapping (mirror `ColdstartBudgetS`/`...Min`/`...Max`).
6. **Dashboard** `dashboard/src/components/pod-config-controls.tsx` — add 2 entries to `FIELD_GROUPS` "Orçamentos e timeouts" group (`:142-183`), copy the `coldstart_budget_s` descriptor verbatim (`:146-154`: `kind:"int"`, `configKey`, `minKey`, `maxKey`, `nextProvision:true`). Add the 6 keys to the `PodConfig`/`PodConfigBounds` TS types in `dashboard/src/lib/gateway.ts`. (Phase 17 slider molde — no new component.)
7. **Seed defaults** wherever `SeedPodConfig` is called at boot (env→DB seed, Plan 17-03) — add the 2 budget defaults + 4 bound defaults. `created_budget_s` ~120s, `progress_stall_budget_s` ~120s (decision #6), with min/max bounds in the style of the 0031 comments (e.g. 30/600).

**`UpdatePodConfigFieldAllowlist` (AL-01 mirror) already exists** — `pod_config.sql.go:362`, PATCH case `config_write.go:155`, dashboard control `pod-config-controls.tsx:100`. No new query/PATCH/UI needed for AL-01.

---

## Phase 12 death-detection overlap (coldstart vs steady-state = distinct paths)

CONFIRMED distinct:
- **Coldstart (pre-Ready):** `waitForReadyOrDestroy` (`reconciler.go:1453`) with FUNCTION-LOCAL `terminalStrikes`/`notFoundStrikes` (1470,1484) — the loop is ONE call.
- **Steady-state (post-Ready, Phase 12 / SEED-011/012, RES-11):** `evaluateReady` (`reconciler.go:444`) calls `pollDeathOnReadyTick` (`reconciler.go:531`) once per 1 Hz tick; strikes persist on the STRUCT (`r.terminalStrikes`/`r.notFoundStrikes`, `reconciler.go:327-329`, `deathStrikeMu`-guarded) because each tick is a separate call. Comment `reconciler.go:525-528` explicitly notes the counters differ from the in-loop provisioning poll precisely because of pre-ready vs post-ready.

They share the 3-strike CONCEPT (`terminalConfirmStrikes = 3`, `reconciler.go:108`) and `IsTerminal()`/`ErrInstanceNotFound` classification, but NOT code/state. Phase 20 (coldstart, pre-Ready) touches `waitForReadyOrDestroy` only — no Phase-12 overlap. `markReady` resets the struct strikes on the Provisioning→Ready transition (`reconciler.go:529-530`).

---

## Testing patterns (unit-testable without live Vast)

`gateway/internal/primary/reconciler_test.go` (125 KB) fakes everything the fast-fail logic needs:
- `fakeVast` (`reconciler_test.go:82-135`) — scriptable `searchOffersFn`/`createInstanceFn`/`getInstanceFn`/`destroyInstanceFn` closures. Return a `vast.Instance{ActualStatus:"created"}` per poll to drive FF-01; return running-but-unreachable for port-bind.
- `fakeLoader`/`fakeDCGMScraper`/`fakeInflight`/`fakeDBTX` (`:137-344`). `r.SetQueriesForTest(gen.New(dbtx))` (`:494`) injects the sqlc handle over a fake DBTX — no real Postgres.
- `Deps.Reachable` and `Deps.HealthCheck` are injectable closures (`lifecycle.go:171-193`) — script them to isolate the reachability vs health gates. `Deps.DeviceReport` likewise.
- Test constructors: `SetVastClient`, `SetQueriesForTest`, `SetLastProvisionFailureAtForTest` (`:542`); `export_test.go` exposes internals.

**Clock constraint (real):** `waitForReadyOrDestroy` uses `time.NewTimer(...)` + `time.Now()` for `firstRunningAt` (`reconciler.go:1460,1596`) — there is **NO injectable clock**. The existing tests exercise timing by (a) overriding the package var `primaryInstancePollIntervalForTest` (`reconciler.go:117`, `var` not `const`) to speed the poll, and (b) setting the budget to 0 so `elapsed >= budget` fires on the FIRST running poll (the `>=`-not-`>` comment at `reconciler.go:1614-1615` "finding #7" exists for exactly this deterministic test). **FF-01/FF-02 timing tests must follow the same idiom** (budget=0 or tiny + fast poll interval + scripted fakeVast state sequence). No fake clock needed if the planner keeps the `>=`-on-first-poll trick; adding an injectable clock would be net-new plumbing (probably not worth it — ponytail: reuse the budget=0 idiom).

---

## Do NOT rebuild (already shipped)

- `CountConsecutiveFailedPrimaryProvisions` + `failStreak<2 market / >=2 allowlist_preferred` picker policy (`primary_lifecycles.sql:78`, `reconciler.go:1292-1402`). Reuse — do not add an in-memory counter (SEED's original sketch, explicitly DISCARDED by decision #2).
- `UpdatePodConfigFieldBlocklist` **and** `UpdatePodConfigFieldAllowlist` queries (`pod_config.sql.go:362,371`) — both exist. Only CALL them.
- `blocklist` + `allowlist` PATCH cases (`config_write.go:148,155`) + dashboard controls (`pod-config-controls.tsx:93,100`). AL-01 needs NO new PATCH/UI/query.
- Port-bind fast-fail + RFC1918 reject (Phase 6.6.Y): `public_port_bind_timeout` via `Deps.Reachable` (`reconciler.go:1594-1653`), `rejectPrivateIPOffers` (`reconciler.go:1705`). Covers the TCP-unreachable regime — don't re-add.
- `status_msg` error early-abort + 3-strike terminal/not-found confirms (`reconciler.go:1531-1583`). Regimes with a Vast-surfaced error already fail fast.
- Phase-12 Ready-tick death poll (`evaluateReady`/`pollDeathOnReadyTick`) — steady-state, distinct path, don't touch.
- `coldstart_budget_s`/`port_bind_budget_s` full CFG/UI stack (Phase 17) — the exact molde to copy, don't recreate.
- Vast client auth/error/metrics pattern (`vast/client.go`) — extend with one method, don't rewrite.
- `pod/onstart.sh` tee to `/var/log/onstart.log` — already there.

## Open questions for the planner

1. **BL-01 exact ban predicate:** `failStreak` is read pre-attempt; to ban on the 2nd consecutive failure gate on `failStreak >= 1` at the line-1438 hook. Confirm the off-by-one against decision #4 ("ban when fail_streak≥2").
2. **Vast logs API `result_url` shape** — validate on first real coldstart UAT (CONTEXT §Claude's Discretion); the async `PUT → result_url → GET` contract is asserted by CONTEXT decision #1 but the body shape is unverified in-repo.
3. **FF-03(b) SSH-tail:** no Go SSH exists (only `pod/scripts/vast-ai.sh ssh-exec`). Decide: shell-out to `ssh`/`vast-ai.sh`, add `x/crypto/ssh`, or defer SSH entirely and lean on the Vast logs API (CONTEXT allows either extreme). Lazy default: logs API in Go, SSH as a deferred fallback.
4. **`created_budget_s` close-reason string** (e.g. `created_state_timeout`) — new `shutdown_reason` token; confirm it doesn't need alerter-fingerprint wiring like the terminal reasons.
5. **Migration 0033 defaults for existing rows:** 0031 columns use NO SQL default + a Go seed; an `ALTER ADD COLUMN NOT NULL` needs either a `DEFAULT` (simplest) or a backfill. Confirm the seed path (`SeedPodConfig` is `ON CONFLICT DO NOTHING` — it won't populate the new cols on an already-seeded row, so the migration DEFAULT is load-bearing for the live prod row).
6. **FF-02 stall detector placement:** ingesting the onstart-log heartbeat means an async fetch (Vast logs API, ~seconds) inside a 5s-ish poll loop — decide whether to fetch every Nth poll or on a separate cadence to avoid stretching the loop.
