---
phase: quick-261001-qdd
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/db/migrations/0039_primary_bid_mode.sql
  - gateway/db/queries/pod_config.sql
  - gateway/db/queries/primary_lifecycles.sql
  - gateway/internal/db/gen/ (sqlc regenerated: models.go, pod_config.sql.go, primary_lifecycles.sql.go, querier.go)
  - gateway/internal/podconfig/types.go
  - gateway/internal/admin/config_read.go
  - gateway/internal/admin/config_write.go
  - gateway/internal/config/config.go
  - gateway/internal/emerg/vast/types.go
  - gateway/internal/primary/pricing.go
  - gateway/internal/primary/pricing_test.go
  - gateway/internal/primary/lifecycle.go
  - gateway/internal/primary/reconciler.go
  - gateway/internal/primary/reconciler_test.go
  - gateway/internal/obs/metrics.go
  - gateway/cmd/gatewayctl/primary.go
autonomous: true
requirements: [QUICK-261001-qdd]
clickup: 86akrnpwc

must_haves:
  truths:
    - "Primary offer ranking uses REAL cost = hourly (dph_base on-demand, or bid price) + storage_cost*45/730, and the per-shape cap applies to that real cost"
    - "Default mode is bid: the reconciler searches both on-demand and bid offers, creates the cheapest by real cost with `price` in PUT /asks/{id}/ when bid wins; ties go to on-demand"
    - "Falls back to on-demand automatically when no bid offer is eligible OR when today's (America/Sao_Paulo) preempted-lifecycle count >= max_preemptions_per_day (default 2)"
    - "A bid instance that Vast stops (intended_status=stopped or actual_status exited/stopped) is destroyed, its lifecycle closed with shutdown_reason exactly `preempted`, billing suppression NOT armed, machine NOT blocklisted, and the schedule loop re-provisions"
    - "primary_lifecycles persists is_bid + bid_price; `gatewayctl primary lifecycles` shows a MODE column; metric gateway_primary_preemptions_total increments on each preemption"
    - "Emerg provisioning and the `ifix-primary-lifecycle-<id>` label/sweep are unchanged; all existing primary/emerg/admin/podconfig/gatewayctl tests stay green"
  artifacts:
    - path: "gateway/internal/primary/pricing.go"
      provides: "Pure RealCost / BidPriceFor / RankCandidates / ChooseMode"
    - path: "gateway/db/migrations/0039_primary_bid_mode.sql"
      provides: "pod_config offer_mode/bid_margin/max_preemptions_per_day + primary_lifecycles is_bid/bid_price + trigger recreate"
      contains: "offer_mode"
  key_links:
    - from: "reconciler.provisionLifecycle"
      to: "RankCandidates"
      via: "on-demand + bid SearchOffers results per shape"
      pattern: "RankCandidates\\("
    - from: "reconciler.provisionLifecycle"
      to: "vast.CreateRequest.Price"
      via: "pointer set only when candidate IsBid"
      pattern: "Price\\s*="
    - from: "handleConfirmedDeath / waitForReadyOrDestroy"
      to: "closeLifecycle(..., \"preempted\")"
      via: "pendingCloseReason consumed by evaluateDestroying"
      pattern: "\"preempted\""
---

<objective>
Cut the PRIMARY pod (1x RTX 3090, Qwen3-30B-A3B) cost: (1) rank Vast offers by REAL cost with the cap applied to real cost; (2) default to interruptible (bid) instances with automatic on-demand fallback (no eligible bid, or >= N preemptions today); (3) detect bid preemption, destroy + close `preempted` + re-provision; (4) persist mode/bid on the lifecycle, expose in gatewayctl, add a preemption metric.

Purpose: Pedro decision 2026-10-01 (ClickUp 86akrnpwc). Bid on a 3090 is materially cheaper than on-demand; the current picker orders by Vast's dph_total and never bids.
Output: migration 0039, pure pricing module + tests, reconciler wiring, metric, gatewayctl column. NO deploy, NO prod access, NO real Vast calls (orchestrator owns deploy: migration one-off before the stack 38 PUT).
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@gateway/internal/primary/reconciler.go
@gateway/internal/primary/lifecycle.go
@gateway/internal/emerg/vast/types.go
@gateway/db/migrations/0034_pod_config_force_machine_id.sql
@gateway/db/queries/primary_lifecycles.sql
@ops/vast-3060/unified3060.py

<investigation_findings>
Why the Argentina on-demand 3090 (~$0.142 total) lost to Serbia ($0.1633) on 2026-10-01 — FROM CODE (planner read):

FATO (code):
- Ordering is NOT the cause by itself: provisionLifecycle takes `pickable[0]` of the server result ordered `dph_total asc` (vast/types.go DefaultSearchFilter `order: [[dph_total, asc]]`, `limit: 20`), filtered only by FilterBelowCap (dph_total <= cap+1e-4) and RejectPrivateIPOffers. An offer that PASSED the server filter with a lower dph_total would have been picked. So the cheaper offer was EXCLUDED by a filter (or absent at provision time).
- Server filter the reconciler sends (vast/types.go:357-368) differs from Pedro's manual test:
  * `reliability gte 0.99` (field `reliability`) — test used `reliability2 >= 0.97`. Any offer with reliability in [0.97,0.99) is dropped.
  * `cuda_max_good gte 12.8` and `driver_vers gte 570000000` — not in the test.
  * `host_id neq pod_config.host_id` (if >0) and `machine_id notin pod_config.vast_machine_blocklist` (auto-grown by recordProvisionOutcome on machine-attributable failures).
  * client-side drop of RFC1918 public_ipaddr when reject_private_ip=true.
  * NO `type` key sent (test: on-demand) and NO storage size sent, so the dph_total Vast returns/orders by is computed at Vast's default storage, not our 45 GB; we never compute dph_base + storage_cost*45/730 ourselves.
- `inet_down gte 200` is looser than the test's 300 — not a cause. Mode was `market` (failStreak<2) so the allowlist pass did not run.

HIPÓTESE (needs prod/market data; most likely first): Argentina offer had `reliability` < 0.99 (or cuda_max_good < 12.8 / driver < 570, or its machine_id is in the blocklist). Resolves with (orchestrator, not executor):
  1. pod_config:  ssh worker-vm "docker exec \$(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl primary config show" — or SQL on bd_ai_gateway: `SELECT cap_primary, host_id, vast_machine_blocklist, reject_private_ip, force_machine_id FROM ai_gateway.pod_config;`
  2. market: `curl -s -G https://console.vast.ai/api/v0/bundles/ -H "Authorization: Bearer $VAST_API_KEY" --data-urlencode 'q={"gpu_name":{"eq":"RTX 3090"},"num_gpus":{"eq":1},"rentable":{"eq":true},"type":"on-demand","geolocation":{"in":["AR"]},"limit":50}' | jq '.offers[]|{id,machine_id,host_id,geolocation,reliability,reliability2,cuda_max_good,driver_vers,inet_down,dph_total,dph_base,storage_cost,min_bid,public_ipaddr}'` and compare each field against the filter above + the blocklist. (Spot inventory: the specific offer may be gone; any AR 3090 row shows which clause bites.)
- This plan does NOT relax reliability/cuda/driver thresholds (policy change, not decided by Pedro) — it fixes the ranking (real cost) and adds bid. If step 2 shows reliability is the blocker, relaxing to reliability2>=0.97 is a separate decision for Pedro.
</investigation_findings>

<interfaces>
Existing (extracted; do not re-explore):

gateway/internal/emerg/vast/types.go
- type Offer struct { ID, GpuName, NumGpus, DphTotal `dph_total`, Reliability, InetDown, CudaMaxGood, MachineID, HostID, Geolocation, Rentable, PublicIPAddr }
- type Instance struct { ID, ActualStatus, IntendedStatus, ..., DphTotal, Label, StatusMsg, ... }; IsTerminal() = actual_status in {exited, unknown, offline}
- type CreateRequest struct { ClientID, Image, Env, Onstart, Runtype, Entrypoint, Args, Disk int, Label, TargetState }
- type SearchFilter map[string]any; DefaultSearchFilter(maxDPH, hostID, gpuName, numGPUs, blocklist...) (SHARED with emerg — do not change its output); WithMachineAllowlist(f, ids)

gateway/internal/primary/reconciler.go
- provisionLifecycle(ctx, lifecycleID, log) lines ~1315-1515: hot := r.liveCfg(); failStreak; filters := vast.DefaultSearchFilters(...); force_machine_id branch; per-shape allowlist pass (failStreak>=2) + broaden pass; offer := pickable[0]; buildCreateRequest(offer, lifecycleID); CreateInstance; UpdatePrimaryLifecycleVastIDs{AcceptedDph: offer.DphTotal}; waitForReadyOrDestroy(ctx, lifecycleID, instance.ID, offer.DphTotal, log); recordProvisionOutcome(...)
- bootHotCfg(cfg) maps config.Config → podconfig.PodConfig (fallback when podCfg nil — what unit tests use)
- pollDeathOnReadyTick → *deathClassification{dead, cause}; classifyDeath(inst): intended=stopped → "billing_stopped"; handleConfirmedDeath arms billingSuppressedAt only when cause=="billing_stopped"; startDrain → Draining → evaluateDestroying (line ~831) BestEffortDestroy + closeLifecycle(ctx, id, "destroyed", 0)
- waitForReadyOrDestroy ~line 1833: `billingStopped || inst.IsTerminal()` 3-strike → BestEffortDestroy + closeLifecycle("instance_terminal_state")
- machine-attributable reasons list ~line 2460-2480 includes "instance_terminal_state" (drives auto-blocklist in recordProvisionOutcome)
- recoverOpenLifecycle (line ~2139) restores activeInstanceID from GetOpenPrimaryLifecycle
- r.rule.Timezone *time.Location (America/Sao_Paulo)

gateway/internal/primary/lifecycle.go:520-521 — `Disk: 45`, `Label: fmt.Sprintf("ifix-primary-lifecycle-%d", lifecycleID)`

pod_config field pattern (commit d589b42, force_machine_id): migration ADD COLUMN ... DEFAULT + DROP/CREATE TRIGGER pod_config_update_notify with the new column in WHEN; db/queries/pod_config.sql `-- name: UpdatePodConfigField<X> :exec`; podconfig/types.go PodConfig field + rowToPodConfig; admin/config_write.go podConfigWriteQueries interface + `case "<json_key>":` with validation; admin/config_read.go ConfigSection field.

obs metric pattern: obs.PrimaryDeathDetectedTotal (promauto CounterVec, name gateway_primary_death_detected_total).

sqlc: ~/go/bin/sqlc v1.30.0 (matches gen header). Run from gateway/: `~/go/bin/sqlc generate`.

Reference algorithm (Python, already in prod for the 3060 pod): ops/vast-3060/unified3060.py real_cost (L276), bid_price_for (L295), choose_mode (L313), rank_candidates (L355), offer_query type "on-demand"|"bid" (L410), is_terminal incl. actual_status "stopped" (L978). BID_MARGIN_DEFAULT=1.15, MAX_PREEMPT_DEFAULT=2, HOURS_PER_MONTH=730.
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Data layer — migration 0039, sqlc queries, pod_config/config fields, Vast types</name>
  <files>gateway/db/migrations/0039_primary_bid_mode.sql, gateway/db/queries/pod_config.sql, gateway/db/queries/primary_lifecycles.sql, gateway/internal/db/gen/*, gateway/internal/podconfig/types.go, gateway/internal/admin/config_read.go, gateway/internal/admin/config_write.go, gateway/internal/config/config.go, gateway/internal/emerg/vast/types.go</files>
  <action>
Migration 0039 (goose Up/Down, `SET search_path = ai_gateway, public;`, ADDITIVE only, follow 0034 exactly):
- pod_config: `offer_mode TEXT NOT NULL DEFAULT 'bid' CHECK (offer_mode IN ('bid','ondemand'))`, `bid_margin NUMERIC(4,2) NOT NULL DEFAULT 1.15 CHECK (bid_margin >= 1.00 AND bid_margin <= 5.00)`, `max_preemptions_per_day INTEGER NOT NULL DEFAULT 2 CHECK (max_preemptions_per_day >= 0 AND max_preemptions_per_day <= 20)`. DEFAULTs backfill the existing prod row (Pedro decision: default bid).
- DROP + CREATE TRIGGER pod_config_update_notify copying the full 0034 WHEN predicate verbatim and appending the 3 new columns (hot-reload). Down: drop trigger, drop the 3 columns, restore the 0034 trigger body verbatim.
- primary_lifecycles: `is_bid BOOLEAN NULL`, `bid_price NUMERIC(6,4) NULL` (NULL = legacy row). Update the shutdown_reason comment to list 'preempted'. Down drops both.
Queries:
- pod_config.sql: UpdatePodConfigFieldOfferMode(text), UpdatePodConfigFieldBidMargin(numeric), UpdatePodConfigFieldMaxPreemptionsPerDay(int4). Seed insert untouched (DB DEFAULT covers fresh DB).
- primary_lifecycles.sql: new `-- name: SetPrimaryLifecycleOfferMode :exec` (UPDATE is_bid=$2, bid_price=$3 WHERE id=$1) — separate query so UpdatePrimaryLifecycleVastIDs signature does not change. New `-- name: CountPrimaryPreemptionsSince :one` → `SELECT COUNT(*)::bigint FROM ai_gateway.primary_lifecycles WHERE shutdown_reason = 'preempted' AND ended_at >= $1`. Add `is_bid, bid_price` to the SELECT lists of GetOpenPrimaryLifecycle, ListPrimaryLifecycles and ListPrimaryLifecyclesInRange (additive struct fields; fix any compile fallout in admin/operations.go, admin/economy.go and their tests).
- Run `cd gateway && ~/go/bin/sqlc generate`; the diff must touch only gen files for these queries/models.
podconfig/types.go: add OfferMode string, BidMargin float64, MaxPreemptionsPerDay int + rowToPodConfig mapping.
admin/config_write.go: add the 3 Update methods to podConfigWriteQueries + cases "offer_mode" (must be "bid"|"ondemand"), "bid_margin" (1.00..5.00), "max_preemptions_per_day" (0..20) using the existing decode helpers + h.validationErr. config_read.go: expose the 3 fields in ConfigSection (json offer_mode, bid_margin, max_preemptions_per_day). Update fake queriers in admin tests to satisfy the interface.
config/config.go: add PrimaryVastOfferMode (env PRIMARY_VAST_OFFER_MODE, default "bid"), PrimaryVastBidMargin (PRIMARY_VAST_BID_MARGIN, default 1.15), PrimaryVastMaxPreemptionsPerDay (PRIMARY_VAST_MAX_PREEMPTIONS_PER_DAY, default 2) following the existing floatOr/intOr pattern — boot fallback consumed by bootHotCfg in Task 2 (and lets unit tests pick the mode).
vast/types.go: Offer gains `DphBase float64 json:"dph_base"`, `StorageCost float64 json:"storage_cost"` (US$/GB/month), `MinBid float64 json:"min_bid"`. CreateRequest gains `Price *float64 json:"price,omitempty"` (pointer + omitempty so emerg's wire JSON is byte-identical; FATO quick 261001-cwi: vast-cli sends `price` in PUT /asks/{id}/ for interruptible). Do NOT touch DefaultSearchFilter/DefaultSearchFilters (shared with emerg).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && go build ./... && go test ./internal/podconfig/... ./internal/admin/... ./internal/config/... ./internal/emerg/... ./cmd/gatewayctl/... 2>&1 | tail -20 && grep -c "offer_mode" db/migrations/0039_primary_bid_mode.sql</automated>
  </verify>
  <done>Build green; podconfig/admin/config/emerg/gatewayctl tests green; migration has Up+Down with trigger recreate including the 3 new columns; CreateRequest JSON without Price set has no "price" key (add a vast types_test assertion).</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Real-cost ranking + bid selection + on-demand fallback in provisionLifecycle</name>
  <files>gateway/internal/primary/pricing.go, gateway/internal/primary/pricing_test.go, gateway/internal/primary/lifecycle.go, gateway/internal/primary/reconciler.go, gateway/internal/primary/reconciler_test.go</files>
  <behavior>
    - RealCost(o, isBid=false, 0, 45): DphBase=0.1466, StorageCost=0.27 → hourly 0.1466, storageH=0.27*45/730, total=sum; DphBase==0 → falls back to DphTotal with storageH 0 (keeps legacy fixtures working)
    - BidPriceFor(o{MinBid:0.10, StorageCost:s}, 1.15, cap, storageH): returns round4(0.115); when 0.115+storageH > cap, bid lowered to round4(cap-storageH) (minus 0.0001 if rounding still exceeds); returns ok=false if result < MinBid or MinBid<=0
    - RankCandidates(od, bid, mode="bid", cap, margin, 45): cheapest total wins; equal total → on-demand; mode="ondemand" ignores bid list; candidates with total > cap+1e-4 excluded; empty → ok=false
    - ChooseMode("bid", preemptToday=2, max=2) == "ondemand"; ("bid",1,2)=="bid"; ("ondemand",0,2)=="ondemand"; invalid string → "bid"; max<=0 disables the preemption fallback (cfg mode always honored) — document in a comment
    - Reconciler (fake Vast branching on filter["type"]): bid cheaper → CreateInstance req.Price != nil and == bid, SetPrimaryLifecycleOfferMode(is_bid=true); on-demand cheaper (Argentina-like fixture with lower real cost but higher-sorted dph_total) → picked by real cost, req.Price nil; no bid eligible → on-demand; preemptions today >= max → no bid search issued at all
  </behavior>
  <action>
Create gateway/internal/primary/pricing.go (pure, no I/O) porting ops/vast-3060/unified3060.py real_cost/bid_price_for/choose_mode/rank_candidates (single cap step — the Go reconciler already has its own shape-fallback loop, so no cap_steps): const primaryDiskGB = 45, hoursPerMonth = 730; type Candidate { Offer vast.Offer; IsBid bool; Bid float64; Hourly, StorageH, Total float64 }; funcs RealCost, BidPriceFor, RankCandidates, ChooseMode with the behavior above. Replace the `Disk: 45` literal in lifecycle.go buildCreateRequest with primaryDiskGB (label line untouched).
In reconciler.go: bootHotCfg maps the 3 new config fields. provisionLifecycle:
- After failStreak, compute preemptToday via q.CountPrimaryPreemptionsSince(start of today in r.rule.Timezone; if Timezone nil use time.UTC) — on error/nil DB treat as 0 and log Warn (must never block provisioning). mode := ChooseMode(hot.OfferMode, preemptToday, hot.MaxPreemptionsPerDay). Log mode + preempt_today on the existing offer-found/picked lines (rename the existing log key "mode" for market/allowlist to "pick_mode" only if no test asserts on it; otherwise use a new key "offer_mode").
- Add primary-only helper primaryFilter(f vast.SearchFilter, kind string) returning a COPY with "type": kind ("on-demand" | "bid") and "limit": 64; for kind "bid" delete "dph_total" (bid price is filtered client-side via min_bid; on-demand keeps the server dph_total<=cap pre-filter + order). Do NOT mutate the shared filter maps.
- Per shape (both allowlist pass and broaden pass): on-demand search (primaryFilter(f,"on-demand")); if mode=="bid" also bid search (primaryFilter(f,"bid")) — a bid search ERROR is logged and treated as empty (never aborts; mirrors Python pick_offer). Apply rejectPrivateIPOffers to both lists, then RankCandidates(od, bd, mode, shapeCaps[i], hot.BidMargin, primaryDiskGB) instead of FilterBelowCap+pickable[0]. Keep the force_machine_id branch on-demand only, unchanged semantics (pin bypasses cap; document in a comment).
- If mode=="bid" and the winner is on-demand, log Info "primary bid: no eligible/cheaper bid offer, using on-demand".
- Create: req := buildCreateRequest(...); if cand.IsBid { p := cand.Bid; req.Price = &p }. Accepted DPH stored/passed to waitForReadyOrDestroy = cand.Total when IsBid (bid + storage), offer.DphTotal otherwise (unchanged cost semantics for on-demand). Add is_bid, bid_price, real_cost, storage_h, dph_base, storage_cost, min_bid to the offer-picked log and the offer_accepted event JSON. After UpdatePrimaryLifecycleVastIDs succeeds call q.SetPrimaryLifecycleOfferMode(id, is_bid, bid_price NULL when on-demand) — failure logged Warn, not fatal. Store r.activeIsBid (new atomic.Bool) = cand.IsBid; recoverOpenLifecycle restores it from the open row's is_bid (NULL → false).
- Existing tests: fixtures without DphBase/MinBid must keep picking the same offer (RealCost falls back to DphTotal; MinBid 0 → bid ineligible). If a test breaks ONLY because the bid mode issues an extra SearchOffers call (call-count / filter-sequence assertions), set PrimaryVastOfferMode="ondemand" in that test's config — never weaken the assertion itself. Add new tests per <behavior> in pricing_test.go and reconciler_test.go (fake Vast branches on filter["type"]; fake queries for CountPrimaryPreemptionsSince/SetPrimaryLifecycleOfferMode follow the file's existing fake-DB pattern — if the reconciler only uses gen.Queries over a pool, test via an injected func hook on the reconciler like other DB-dependent tests do).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && go test ./internal/primary/... -count=1 2>&1 | tail -15</automated>
  </verify>
  <done>pricing_test covers all behavior bullets; reconciler tests prove bid pick sends price, on-demand real-cost pick beats lower-sorted dph_total, fallback on no-bid and on preempt limit; full ./internal/primary suite green.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 3: Preemption detection → destroy + close `preempted` + reprovision; metric; gatewayctl MODE column; full gates</name>
  <files>gateway/internal/primary/reconciler.go, gateway/internal/primary/reconciler_test.go, gateway/internal/obs/metrics.go, gateway/cmd/gatewayctl/primary.go</files>
  <behavior>
    - Ready tick, activeIsBid=true, GetInstance returns intended_status=stopped (or actual_status "stopped"/"exited") 3x → death cause "preempted"; BestEffortDestroy called (via drain→destroy path); lifecycle closed with shutdown_reason exactly "preempted"; billingSuppressionActive()==false afterwards; FSM returns to Asleep and the next in-window tick provisions again
    - Same signal with StatusMsg containing credit/account/saldo → still "billing_stopped" (suppression armed) even for bid
    - activeIsBid=false (on-demand) → classifyDeath behavior unchanged (intended=stopped → billing_stopped)
    - Provisioning phase, bid lifecycle, terminal/stopped 3-strike → BestEffortDestroy + close "preempted" (not "instance_terminal_state"); recordProvisionOutcome does NOT blocklist the machine
    - gateway_primary_preemptions_total increments once per preemption (both paths)
  </behavior>
  <action>
- obs/metrics.go: add PrimaryPreemptionsTotal = promauto CounterVec name "gateway_primary_preemptions_total", label "phase" ∈ {ready, provisioning}; extend PrimaryDeathDetectedTotal help text to list "preempted".
- Death poll: in pollDeathOnReadyTick, when r.activeIsBid.Load() is true, a terminal observation = inst.IsTerminal() OR intended_status=="stopped" OR actual_status=="stopped" (Python is_terminal shape; vast-cli: outbid → stopped). Keep the 3-strike confirm. classifyDeath gains the bid flag: credit/account/saldo StatusMsg → "billing_stopped" (checked FIRST, zero-credit must still suppress); else bid → "preempted"; else existing logic untouched.
- handleConfirmedDeath: for cause "preempted" set r.pendingCloseReason (new atomic.Pointer[string]) = "preempted" BEFORE startDrain, inc PrimaryPreemptionsTotal{phase="ready"}; no billing marker (already conditional). evaluateDestroying: close reason = pendingCloseReason if set (then clear) else "destroyed" — keeps all existing "destroyed" assertions. Clear pendingCloseReason also in markReady and on the abort paths that reset activeLifecycleID, so it cannot leak into a later lifecycle.
- waitForReadyOrDestroy terminal/stopped 3-strike block: if r.activeIsBid.Load() and StatusMsg has no credit/account/saldo marker → BestEffortDestroy, closeLifecycle("preempted"), inc PrimaryPreemptionsTotal{phase="provisioning"}, return reason "preempted". Do NOT add "preempted" to the machine-attributable list (a lost bid is market, not host fault). Ensure errReason/classification helpers (~line 2365-2380) map "preempted" through unchanged.
- Re-provision: verify (test) that after a preempted close in-window, evaluateAsleep provisions again on a later tick (respecting the existing failure cooldown only for the provisioning-phase path, since that one sets lastProvisionFailureAt via the goroutine error return). Preempted lifecycles count toward CountPrimaryPreemptionsSince → Task 2's ChooseMode flips to on-demand after N.
- Label `ifix-primary-lifecycle-<id>` and sweep.go untouched.
- gatewayctl/primary.go runPrimaryLifecyclesWithPool: add a MODE column ("bid@0.1234" when is_bid true, "ondemand" when false, "-" when NULL) in table output and is_bid/bid_price keys in json output; update gatewayctl tests that assert header text.
- Final gates from gateway/: `gofmt -l .` must print nothing; `go vet ./...` and `go vet -tags integration ./...` clean (integration compile only — Docker-backed run is the orchestrator's); full `go test ./...`.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && test -z "$(gofmt -l .)" && go vet ./... && go vet -tags integration ./... && go test ./... -count=1 2>&1 | grep -v "^ok\|no test files" | tail -20</automated>
  </verify>
  <done>All behavior bullets have passing tests; gofmt/vet (incl. integration tag) clean; full gateway test suite green; no change in emerg or sweep.go.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| dashboard/admin → PATCH pod_config | owner-editable offer_mode/bid_margin/max_preemptions_per_day reach the provisioner |
| Vast API → reconciler | offer fields (dph_base, storage_cost, min_bid) and instance status drive spend + destroy decisions |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-qdd-01 | Tampering | config_write offer_mode/bid_margin/max_preemptions | mitigate | server-side enum/range validation in config_write + DB CHECK constraints in 0039 (defense in depth) |
| T-qdd-02 | Denial of Service (cost) | RankCandidates / BidPriceFor | mitigate | cap enforced on real cost (bid+storage) with epsilon; bid never below min_bid nor above cap−storage; missing fields fall back to dph_total (never to 0 cost) |
| T-qdd-03 | Denial of Service (availability) | preemption loop | mitigate | auto on-demand fallback after max_preemptions_per_day preempted lifecycles today; bid search errors degrade to on-demand, never abort |
| T-qdd-04 | Repudiation | lifecycle audit | mitigate | is_bid/bid_price columns + offer_accepted event payload + shutdown_reason 'preempted' + metric |
| T-qdd-05 | Tampering | billing-stop vs preempt misclassification | mitigate | credit/account/saldo StatusMsg checked first → billing_stopped suppression still arms for bid pods; zero-credit cannot loop provisions |
| T-qdd-06 | Elevation | emerg/sweep regressions | accept | emerg + sweep untouched (Price is omitempty pointer; label unchanged); covered by existing suites |
</threat_model>

<verification>
- `cd gateway && go test ./... -count=1` green; `go vet -tags integration ./...` clean; `gofmt -l .` empty.
- grep: `grep -n '"preempted"' gateway/internal/primary/reconciler.go` shows close paths; `grep -n 'json:"price,omitempty"' gateway/internal/emerg/vast/types.go`.
- No file under gateway/internal/emerg/ other than vast/types.go (+ its test) changed: `git diff --stat -- gateway/internal/emerg | grep -v vast/types`.
</verification>

<success_criteria>
- Offer choice = min real cost (hourly + storage_cost*45/730) under cap, bid or on-demand, ties → on-demand.
- Default bid with automatic on-demand fallback (no eligible bid / >= N preemptions today, N hot-editable).
- Bid preemption → destroy + close 'preempted' + metric + re-provision; no blocklist, no billing suppression.
- Lifecycle row records is_bid/bid_price; gatewayctl shows MODE.
- Deploy handoff for orchestrator documented in SUMMARY: run 0039 (goose) on bd_ai_gateway BEFORE the stack 38 image PUT; confirm `pod_config.offer_mode='bid'`.
</success_criteria>

<output>
Create `.planning/quick/261001-qdd-primario-3090-custo-real-bid-fallback/261001-qdd-SUMMARY.md` when done (include the FATO/HIPÓTESE investigation block and the two confirmation commands).
</output>
