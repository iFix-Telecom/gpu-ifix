# Phase 17: Dashboard pod-config control — Research

**Researched:** 2026-06-29
**Domain:** Go gateway config refactor (env-at-boot → DB-backed) + Next.js admin dashboard control surface
**Confidence:** HIGH (all claims from direct codebase reads + live prod env capture)

## Summary

Phase 17 moves the primary-pod config surface from `config.go` env-at-boot (read once, immutable `Config` struct) into a DB-backed `pod_config` table the gateway reconciler re-reads, with hybrid apply: **hot** configs (blocklist/allowlist/caps/schedule/budgets/timeouts) re-read live, **structural** configs (GPU shape, images, weights) restart-gated via a self-restart admin endpoint. The dashboard gets an owner-only edit surface + a live-status poll of `primary_lifecycles`.

The **single best discovery**: the gateway already has the exact hot-reload pattern this phase needs — the `upstreams.Loader` keeps a DB-backed config snapshot fresh via Postgres `LISTEN/NOTIFY` (migration `0009_upstreams_notify_trigger.sql`) + atomic snapshot swap on `Refresh()` [VERIFIED: gateway/internal/upstreams/loader.go:39,103-196]. `pod_config` should mirror this verbatim: a `pod_config_changed` NOTIFY trigger + a `podconfig.Loader` with `atomic.Pointer[Snapshot]`, and the reconciler reads hot fields from `loader.Load()` instead of `r.cfg`.

The **central refactor seam**: the `Reconciler` holds `cfg config.Config` by value (an immutable boot copy) [VERIFIED: gateway/internal/primary/lifecycle.go:241] and `rule ScheduleRule` parsed once at construction [VERIFIED: lifecycle.go:242,334; schedule.go:103 ParseScheduleEnv]. Every hot field is read off `r.cfg.*` / `r.rule.*` on the per-tick or per-provision path. Hot-reload = swap those specific reads for `r.podCfg.Load().*`. Critically, a provisioning attempt runs in its own goroutine spawned per `spawnProvisioning` [VERIFIED: reconciler.go:1149] and reads the offer-selection config (blocklist/allowlist/caps) at provision-START [VERIFIED: reconciler.go:1194-1258] — so re-reading from the live snapshot at provision-start is both correct and safe: an in-flight lifecycle keeps its captured values, the NEXT provision picks up the operator's edit. This is exactly the blocklist-append use case that motivated the phase.

**Primary recommendation:** Build a `pod_config` table (single-row or key-value) + `podconfig.Loader` mirroring `upstreams.Loader` (LISTEN/NOTIFY + atomic snapshot). Seed it from the current env on first boot (env stays as the default/fallback). Refactor the ~16 hot reads in `reconciler.go`/`budget.go`/`schedule.go` to read the snapshot; leave the ~13 structural reads on `cfg` and apply them via a new admin `POST /admin/gateway/restart` (`os.Exit(0)` + docker `restart: unless-stopped`). Add `GET /admin/primary/lifecycle` for the live-status poll. All edits go through the Phase 13 owner-gated dashboard server action + `admin_audit_log`; the dashboard only ever calls the gateway admin API (Phase 15 proxy).

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Config persistence (`pod_config`) | Database (Postgres `ai_gateway` schema) | — | Same DB the reconciler + dashboard already share; LISTEN/NOTIFY hot-reload precedent lives here |
| Hot-reload read path | API/Backend (gateway reconciler Go) | — | Reconciler owns provisioning; only it can safely re-read config mid-lifecycle |
| Self-restart for structural apply | API/Backend (gateway `os.Exit(0)`) | CDN/Infra (docker `restart: unless-stopped`) | Web app must never hold the docker socket; restart policy is the orchestration |
| Config edit form + validation | Frontend Server (Next.js server action) | Browser (form UI) | Owner-gate must be server-side (Phase 13 `requireOwner`); bounds validated server-side before save |
| Admin-key injection / proxy | Frontend Server (Next.js route handler) | — | `GATEWAY_ADMIN_KEY` lives only in the server route (Phase 15) |
| Live lifecycle status | API/Backend (gateway reads `primary_lifecycles`) | Frontend Server (poll proxy) | FSM + lifecycle rows are gateway-owned state |
| Audit trail | Database (`admin_audit_log`, dashboard-side) | Frontend Server (writeAuditLog) | Phase 13 pattern; edit originates dashboard-side |

## Standard Stack

This is an internal refactor — **no new external packages**. All work uses libraries already in the tree.

### Core (already present)
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `jackc/pgx/v5` + `pgxpool` | in tree | Postgres access, `LISTEN`/`WaitForNotification` | Already the gateway DB driver; loader hot-reload uses it [VERIFIED: gateway/go.mod + loader.go] |
| `sqlc` (gen) | in tree | typed queries in `internal/db/gen` | `pod_config` queries generated same as `primary_lifecycles.sql.go` [VERIFIED: gateway/internal/db/gen] |
| `goose` migrations | in tree | `db/migrations/00NN_*.sql` | Head is `0030`; next is `0031` [VERIFIED: ls db/migrations] |
| `go-chi/chi` | in tree | admin sub-router | `/admin/*` routes mounted on chi router [VERIFIED: cmd/gateway/main.go:1488] |
| Drizzle ORM | in tree | dashboard `admin_audit_log` + custom schema | Phase 13 `schema-custom.ts` [VERIFIED: dashboard/src/lib/schema-custom.ts:25] |
| Next.js server actions + better-auth | in tree | owner-gate + form | Phase 13 `requireOwner` [VERIFIED: dashboard/src/lib/admin-actions.ts:117] |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| LISTEN/NOTIFY hot-reload | Per-tick DB `SELECT` (poll) | Simpler, but adds a DB round-trip to the 1Hz hot path; NOTIFY mirrors the proven `upstreams` pattern and is event-driven. **Recommend NOTIFY**, with a per-tick read of the in-memory snapshot (zero DB cost on the tick). |
| Single-row `pod_config` table | Key-value (`pod_config(key, value, type)`) rows | KV is schema-flexible + audits per-field cleanly, but loses type safety + per-column constraints. **Recommend single typed row** (one row, typed columns, mirrors how `cfg` is a struct) + a Postgres CHECK-constraint bounds layer; KV acceptable if planner prefers per-field audit granularity. |
| Self-restart `os.Exit(0)` | docker socket / orchestrator API from dashboard | Socket-from-web-app rejected by locked arch. `os.Exit(0)` + `restart: unless-stopped` is the locked decision [CITED: notes/dashboard-pod-config-control-architecture.md §3]. |

**Installation:** none (no new dependencies).

## Package Legitimacy Audit

**Not applicable** — Phase 17 installs no external packages. It is an internal Go refactor + Next.js feature using libraries already vendored in `gateway/go.mod` and `dashboard/package.json`. No `npm install` / `go get` of new modules is anticipated. If the planner discovers a need for a new package (e.g. a form library), run the Package Legitimacy Gate at that point.

## Config Surface Table (CENTERPIECE)

Every primary-pod-relevant setting. `cfg.go` line = where loaded; "read at" = consumption site. **read-once-at-boot** is the *current* behavior (all are, because `r.cfg`/`r.rule` are immutable boot snapshots); the **Class** column is the *target* Phase-17 behavior.

| # | Setting (Config field) | Env var | Type | Prod value | Loaded (config.go) | Read at (consumption) | Class |
|---|------------------------|---------|------|-----------|--------------------|-----------------------|-------|
| 1 | `PrimaryVastMachineBlocklist` | `PRIMARY_VAST_MACHINE_BLOCKLIST` | []int64 | `55942,45778,52359,41367,48112,53128,49188,36773,44184,45688,94979,129536,104433,56883` (14) | :491 | reconciler.go:1198 (provision) | **HOT** |
| 2 | `PrimaryVastMachineAllowlist` | `PRIMARY_VAST_MACHINE_ALLOWLIST` | []int64 | `7970,12863` | :492 | reconciler.go:1219 (provision) | **HOT** |
| 3 | `PrimaryVastPriceCapPrimary` | `PRIMARY_VAST_PRICE_CAP_PRIMARY` | float64 | `0.30` | :501 | reconciler.go:1194,1203 (provision) | **HOT** |
| 4 | `PrimaryVastPriceCapFallback` | `PRIMARY_VAST_PRICE_CAP_FALLBACK` | float64 | `0.60` | :502 | reconciler.go:1194,1203 (provision) | **HOT** |
| 5 | `PrimaryHostID` | `PRIMARY_HOST_ID` | int64 | `0` | :453 | reconciler.go:1195 (provision) | **HOT** (low-value; rarely changed) |
| 6 | `PrimaryVastRejectPrivateIP` | `PRIMARY_VAST_REJECT_PRIVATE_IP` | bool | *(unset → true)* | :507 | reconciler.go:1579 (provision) | **HOT** |
| 7 | `PrimaryProvisionColdStartBudgetSeconds` | `PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS` | int | `3600` | :508 | reconciler.go:1333 (provision) | **HOT** (next provision) |
| 8 | `PrimaryPublicPortBindBudgetSeconds` | `PRIMARY_PUBLIC_PORT_BIND_BUDGET_SECONDS` | int | *(unset → 120)* | :506 | reconciler.go:1389,1486 (provision) | **HOT** (next provision) |
| 9 | `PrimaryProvisionFailureCooldownSeconds` | `PRIMARY_PROVISION_FAILURE_COOLDOWN_SECONDS` | int | *(unset → 300)* | :509 | reconciler.go:416 (per-tick, evaluateAsleep) | **HOT** |
| 10 | `MonthlyPrimaryBudgetBRL` | `MONTHLY_PRIMARY_BUDGET_BRL` | float64 | `2400` | :510 | budget.go:CheckBudget (per budget-tick) | **HOT** |
| 11 | `PrimaryPodScheduleUpHour` | `PRIMARY_POD_SCHEDULE_UP_HOUR` | int | `9` | :512 | schedule.go via r.rule → reconciler.go:393,484 (per-tick) | **HOT** |
| 12 | `PrimaryPodScheduleDownHour` | `PRIMARY_POD_SCHEDULE_DOWN_HOUR` | int | `17` | :513 | r.rule (per-tick) | **HOT** |
| 13 | `PrimaryPodScheduleDays` | `PRIMARY_POD_SCHEDULE_DAYS` | []string | `mon,tue,wed,thu,fri` | :514 | r.rule (per-tick) | **HOT** |
| 14 | `PrimaryPodScheduleGraceRampDownSeconds` | `PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS` | int | *(unset → 300)* | :515 | reconciler.go:750 (draining) | **HOT** |
| 15 | `PrimaryPodScheduleProvisionLeadSeconds` | `PRIMARY_POD_SCHEDULE_PROVISION_LEAD_SECONDS` | int | *(unset → 1800)* | :519 | r.rule.ShouldBeProvisioned (per-tick) | **HOT** |
| 16 | `PrimaryPodScheduleDisabled` | `PRIMARY_POD_SCHEDULE_DISABLED` | bool | `false` | :517 | reconciler.go:376,481 (per-tick) | **HOT** (dangerous — see bounds) |
| 17 | `PrimaryPodScheduleTimezone` | `PRIMARY_POD_SCHEDULE_TIMEZONE` | string | `America/Sao_Paulo` | :511 | schedule.go:104 LoadLocation (boot parse) | **STRUCTURAL** (tz reparse rare + fail-fast risk) |
| 18 | `PrimaryVastGPUNamePrimary` | `PRIMARY_VAST_GPU_NAME_PRIMARY` | string | `RTX 3090` | :499 | reconciler.go:1196 (provision) | **STRUCTURAL** (shape) |
| 19 | `PrimaryVastGPUNameFallback` | `PRIMARY_VAST_GPU_NAME_FALLBACK` | string | `RTX 3090` | :500 | reconciler.go:1196 (provision) | **STRUCTURAL** (shape) |
| 20 | `PrimaryVastNumGPUsPrimary` | `PRIMARY_VAST_NUM_GPUS_PRIMARY` | int | `1` | :503 | reconciler.go:1197 (provision) | **STRUCTURAL** (shape) |
| 21 | `PrimaryVastNumGPUsFallback` | `PRIMARY_VAST_NUM_GPUS_FALLBACK` | int | `1` ⚠️ (code default 2) | :504 | reconciler.go:1197 (provision) | **STRUCTURAL** (shape) |
| 22 | `PrimaryTemplateImage` | `PRIMARY_TEMPLATE_IMAGE` | string | `ghcr.io/ifixtelecom/converseai-primary-pod:main` | :470 | buildCreateRequest (provision) | **STRUCTURAL** (image) |
| 23 | `PrimaryInfinityImage` | `PRIMARY_INFINITY_IMAGE` | string | *(unset → pinned default)* | :471 | buildCreateRequest | **STRUCTURAL** (image; likely vestigial w/ custom image) |
| 24 | `PrimaryDCGMImage` | `PRIMARY_DCGM_IMAGE` | string | *(unset → pinned)* | :472 | buildCreateRequest | **STRUCTURAL** (image; vestigial) |
| 25 | `PrimarySpeachesImage` | `PRIMARY_SPEACHES_IMAGE` | string | *(unset → pinned)* | :485 | buildCreateRequest | **STRUCTURAL** (image; vestigial) |
| 26 | `PrimaryQwenWeightsKey` | `PRIMARY_QWEN_WEIGHTS_KEY` | string | `qwen3-30b-a3b-2507-Q4_K_M/v1.0.0/model.gguf` | :473 | buildCreateRequest (Env to pod) | **STRUCTURAL** (weights+image lockstep) |
| 27 | `PrimaryQwenWeightsSHA256` | `PRIMARY_QWEN_WEIGHTS_SHA256` | string | `6c997b8a…74d0` | :474 | buildCreateRequest | **STRUCTURAL** |
| 28 | `PrimaryWhisperWeightsKey` | `PRIMARY_WHISPER_WEIGHTS_KEY` | string | `whisper-large-v3/v1.0.0/model.tar.gz` | :482 | buildCreateRequest | **STRUCTURAL** |
| 29 | `PrimaryWhisperWeightsSHA256` | `PRIMARY_WHISPER_WEIGHTS_SHA256` | string | `d8e8f6e4…34d0` | :483 (FAIL-FAST, no default) | buildCreateRequest | **STRUCTURAL** |
| 30 | `PrimaryBGEM3WeightsKey` | `PRIMARY_BGEM3_WEIGHTS_KEY` | string | `bge-m3/v1.0.0/model.tar.gz` | :486 | buildCreateRequest | **STRUCTURAL** |
| 31 | `PrimaryBGEM3WeightsSHA256` | `PRIMARY_BGEM3_WEIGHTS_SHA256` | string | `67fca5ab…a7fb` | :488 (FAIL-FAST) | buildCreateRequest | **STRUCTURAL** |
| 32 | `PrimaryChatterboxWeightsKey` | `PRIMARY_CHATTERBOX_WEIGHTS_KEY` | string | `chatterbox-mtl-v2/v1.0.0/cache.tar.gz` | :489 | buildCreateRequest | **STRUCTURAL** |
| 33 | `PrimaryChatterboxWeightsSHA256` | `PRIMARY_CHATTERBOX_WEIGHTS_SHA256` | string | `c47cd41e…d0c4` | :490 (FAIL-FAST) | buildCreateRequest | **STRUCTURAL** |
| 34 | `PrimaryQwenJinjaKey` / `…SHA256` | `PRIMARY_QWEN_JINJA_KEY` / `_SHA256` | string | *(unset → "")* | :476-477 | buildCreateRequest | **STRUCTURAL** |
| 35 | `PrimaryLlamaArgs` | `PRIMARY_LLAMA_ARGS` | []string | *(unset → const)* | :478 | buildCreateRequest | **STRUCTURAL** |

**Infra/secrets (OUT OF SCOPE — do NOT put in `pod_config`, never dashboard-editable):** `AI_GATEWAY_PG_DSN`, `AI_GATEWAY_REDIS_ADDR`/`_PASSWORD`, `VAST_AI_API_KEY`, `MINIO_*`, `DCGM_EXPORTER_URL`. These are deploy-time secrets/infra. The dashboard manages **pod policy knobs**, not credentials.

**Count: 35 settings catalogued → 16 HOT, 19 STRUCTURAL** (counting #34 Jinja key+sha and #27/#29/#31/#33 as their grouped fields; if every sha/key counted separately the structural count rises but the *classes* are unchanged).

## Hot vs Structural Classification — Rationale & Hazards

### HOT (reconciler re-reads from `pod_config` snapshot; no restart)

**Why these are safe to live-reload:** They are read either (a) per-tick on the schedule-evaluation path (`evaluateAsleep`/`evaluateReady`/`evaluateDraining`, budget-check) or (b) at the START of a provisioning goroutine (offer selection). Neither path mutates an already-running pod — they govern *whether/how the next lifecycle is created or torn down*. An in-flight provisioning goroutine captured its values at spawn and is unaffected by a mid-flight edit; the change lands on the next tick/provision.

| Field(s) | Why hot | Mid-lifecycle hazard |
|----------|---------|----------------------|
| blocklist / allowlist (#1,#2) | Read at provision-start (reconciler.go:1198,1219); the motivating use case (append a bad host, next provision avoids it) | **None.** In-flight provision keeps its host; only next offer-search sees the edit. This is the desired behavior. |
| price caps (#3,#4) | Read at provision-start | Lowering a cap mid-Ready does nothing to the running pod (correct). Next provision uses new cap. **Dangerous direction:** cap-DOWN can make "no offer below cap" → pod never re-provisions (confirm required). |
| host_id, reject-private-ip (#5,#6) | Read at provision-start | None; filter preference. |
| coldstart / port-bind / failure-cooldown budgets (#7,#8,#9) | #9 per-tick; #7,#8 per-provision | Editing #7/#8 while provisioning does NOT shorten the *current* deadline (timer already armed at reconciler.go:1333) — applies next provision. #9 applies next tick. Safe. |
| monthly budget (#10) | Per budget-check tick | Pure alert threshold; zero provisioning impact. Fully safe. |
| schedule UpHour/DownHour/Days/Lead/Grace (#11-15) | Re-parse `ScheduleRule` from snapshot each tick | **Hazard:** narrowing the window (DownHour passes "now") triggers `evaluateReady`→drain on the next tick — drains a Ready pod immediately. This is *correct* (operator changed the schedule) but should be a **confirmed** action. Widening is safe. |
| `Disabled` (#16) | Per-tick at reconciler.go:376,481 | **Hazard:** flipping `Disabled=true` does NOT drain a running pod (evaluateReady returns early under Disabled per UAT-14 fix at reconciler.go:431-434), and stops new schedule-driven provisioning. Flipping `false→true` mid-peak strands the pod up until DownHour/force-down. Flipping `true→false` re-arms the scheduler. Confirm required. |

**Implementation note for schedule:** `r.rule` is a parsed struct (`ScheduleRule` with a `*time.Location`). Hot-reload = call `ParseScheduleEnv`-equivalent against the snapshot on change (or each tick) and atomically swap `r.rule`. Keep the **fail-fast on bad timezone** at the *edit* boundary (reject the save) so a bad tz can never reach the reconciler — timezone itself stays structural (#17).

### STRUCTURAL (boot-only; require `POST /admin/gateway/restart`)

**Why these need a restart even though some are technically read at provision-start:** GPU shape (#18-21), images (#22-25), weights keys+SHAs (#26-34), and llama args (#35) define the *pod's identity and its lockstep contract with the MinIO weight store and the built image*. Three reasons to keep them restart-gated:

1. **Lockstep integrity.** A weights key change only works if that object exists in MinIO with that SHA, AND the image can load it. Editing the key live invites a silent provision failure (SHA mismatch → `buildCreateRequest` fail-fast, or pod boots wrong model). A deliberate restart forces the operator to treat shape/image/weights as one coordinated change.
2. **Cost/safety blast radius.** Wrong `NUM_GPUS` OOMs the pod; wrong GPU name finds zero offers; wrong image bricks cold-start. The locked arch explicitly classes these structural [CITED: architecture notes §2].
3. **No live benefit.** Unlike blocklist (operator needs it NOW to stop bleeding), shape/image changes pair with a redeploy anyway.

**Timezone (#17)** is structural because `time.LoadLocation` is the one schedule field with a boot fail-fast (schedule.go:104) — keep that guarantee by validating tz at save-time and applying on restart.

## Validation Bounds Per Field

| Field | Min | Max | Enum/Format | Dangerous? (confirm) |
|-------|-----|-----|-------------|----------------------|
| blocklist / allowlist | — | — | CSV of positive int64 Vast machine_ids; dedupe; reject non-numeric | No (append safe). Allowlist that excludes all known-good hosts → starvation: warn. |
| price cap primary/fallback | `0.10` | `1.50` | float, 2dp; fallback ≥ primary recommended | **cap-DOWN = confirm** (can strand provisioning) |
| host_id | 0 | — | int64 ≥ 0 (0 = unknown) | No |
| reject_private_ip | — | — | bool | No |
| coldstart budget s | `300` | `5400` | int (5min–90min) | Lowering below ~1800 risks killing legit slow cold-starts: warn |
| port-bind budget s | `30` | `600` | int | No |
| failure cooldown s | `60` | `1800` | int (must exceed one 2+4+8≈14s attempt cycle, per config.go:165) | No |
| monthly budget BRL | `0` | `100000` | float | No (alert only) |
| schedule UpHour | `0` | `23` | int | Window-narrowing = confirm |
| schedule DownHour | `0` | `23` | int (≠ UpHour; overnight wrap allowed) | Window-narrowing = confirm |
| schedule Days | — | — | subset of `{mon,tue,wed,thu,fri,sat,sun}`; non-empty recommended | Empty days = pod never provisions: confirm |
| grace ramp-down s | `0` | `1800` | int | No |
| provision lead s | `0` | `7200` | int (≤ a few × coldstart budget) | No |
| **schedule Disabled** | — | — | bool | **confirm both directions** (strands or re-arms prod availability) |
| timezone | — | — | valid IANA tz (LoadLocation must succeed) — validate at save | structural → restart confirm |
| GPU name primary/fallback | — | — | enum: `RTX 3090` / `RTX 4090` / `RTX 5090` (Vast GPU model strings) | structural → confirm |
| NUM_GPUS primary/fallback | `1` | `2` | int ∈ {1,2} (per arch notes; >2 OOM risk) | **shape change = confirm + restart** |
| template/infinity/dcgm/speaches image | — | — | non-empty OCI ref; SHA-pin recommended | structural → confirm |
| weights key | — | — | non-empty MinIO object path | structural → confirm (lockstep) |
| weights SHA256 | — | — | 64-hex lowercase | structural → confirm |
| llama args | — | — | CSV; reject `--chat-template-file` (B1 embedded LOCKED, config.go:202) | structural → confirm |
| **gateway restart** | — | — | action, no field | **always confirm** |

## Implementation Surfaces (Go gateway)

### config.go fail-fast pattern (the thing being refactored)
- `config.Load()` reads all env at boot into an immutable `Config` [VERIFIED: config.go:356-682]. Fail-fast: required-var loop (config.go:573-581 → `ErrMissingEnv`), URL-suffix check (:597-617), **legacy primary-shape hard-fail** (:666-679 → `ErrLegacyPrimaryEnv`, uses `os.LookupEnv` presence-check). `main.go` `os.Exit(2)` on error.
- **Migration implication:** the `pod_config` row must be **seeded from env on first boot** (see Migration Concern). Env stays as the typed default; `pod_config` overrides it when a row exists. Do NOT remove the env reads — they remain the fallback when the DB read fails (provisioning must never break on a DB hiccup).

### Reconciler tick loop — where to inject the DB re-read
- `Reconciler{ cfg config.Config; rule ScheduleRule }` — immutable boot snapshots [VERIFIED: lifecycle.go:239-242]. `NewReconciler`/`NewReconcilerFull` copy `deps.Cfg` into `r.cfg` (lifecycle.go:333).
- **Cleanest injection:** add `r.podCfg atomic.Pointer[podconfig.Snapshot]` to the struct + a `podconfig.Loader` (mirror `upstreams.Loader`). Replace the specific HOT reads:
  - offer selection: `r.cfg.PrimaryVast{MachineBlocklist,MachineAllowlist,PriceCap*,GPUName*,NumGPUs*}` + `PrimaryHostID` + `PrimaryVastRejectPrivateIP` at reconciler.go:1194-1258,1579 → read from `snap := r.podCfg.Load()` once at the top of `provisionLifecycle`.
  - budgets/timeouts: reconciler.go:416 (cooldown), :1333 (coldstart), :1389 (port-bind) → snapshot.
  - schedule: re-parse `r.rule` from the snapshot on NOTIFY (or per-tick) — the per-tick reads at :376,393,481,484,750 stay as `r.rule.*`, only `r.rule` itself is swapped.
  - budget.go `CheckBudget` (`b.cfg.MonthlyPrimaryBudgetBRL`) → snapshot.
- **Safe-fallback rule:** `podCfg.Load()` returns the last-good snapshot; the loader's initial `Refresh` seeds it from `pod_config` (which was seeded from env). If a NOTIFY-triggered `Refresh` fails, KEEP the existing snapshot (log + metric, like `obs.UpstreamsReloadTotal` at loader.go:111,196). **Provisioning must never block on a config-read failure.**

### Hot-reload mechanism — mirror upstreams.Loader verbatim
- `upstreams.Loader.Refresh` loads enabled rows + atomic snapshot swap [VERIFIED: loader.go:39,103-196]; main.go spawns a `LISTEN upstreams_changed` goroutine that calls `Refresh` on each notification [VERIFIED: main.go:346 + loader.go:39 comment].
- Migration `0009_upstreams_notify_trigger.sql` is the trigger template: a `notify_pod_config_changed()` plpgsql function + `AFTER INSERT/UPDATE/DELETE` trigger calling `pg_notify('pod_config_changed', …)` [VERIFIED: db/migrations/0009].
- **Recommend:** `pod_config` NOTIFY trigger (fire on any config-column change) + a `podconfig.Loader` with `Refresh` + a `ListenAndReload` goroutine in main.go. Reconciler reads `r.podCfg.Load()`; on each NOTIFY the loader swaps the snapshot and re-parses the schedule rule.

### Self-restart admin endpoint
- No restart/primary endpoint exists today [VERIFIED: grep — only `os.Exit(0)` at main.go:131, the gatewayctl path]. Build `POST /admin/gateway/restart`:
  - Gate: mount inside the existing `adminRouter` (chi) so `admin.Middleware` (X-Admin-Key) applies [VERIFIED: main.go:1488-1524].
  - Behavior: write audit/log, flush Sentry (`sentry.Flush`), respond 202, then `os.Exit(0)` in a deferred goroutine after a short grace so the HTTP response flushes. Docker `restart: unless-stopped` (CONFIRMED on prod `ifix-ai-gateway` per arch notes §3) restarts → re-reads `pod_config` (now DB-seeded).
  - **Multi-replica hazard:** `os.Exit(0)` only restarts the replica that received the request. Structural config is read by ALL replicas at boot. If prod runs >1 gateway replica, a single restart leaves other replicas on stale structural config until they restart. See Open Risks.

### Live-status endpoint
- Build `GET /admin/primary/lifecycle` on `adminRouter`. Data sources already exist:
  - FSM state: `primaryReconciler` is in scope in main.go (passed to `OperationsHandler` at main.go:110); expose `rec.FSM.State()` + `rec.ActivePodURLs()` [VERIFIED: lifecycle.go:344].
  - lifecycle rows: sqlc queries `GetOpenPrimaryLifecycle`, `ListPrimaryLifecycles` already generated [VERIFIED: db/gen/primary_lifecycles.sql.go:58,140]. The `primary_lifecycles` table cols (started_at, first_health_pass_at, drain_started_at, ended_at, trigger_reason, vast_offer_id, vast_instance_id, accepted_dph, total_cost_brl, shutdown_reason, events jsonb, leader_replica) are the event trail.
  - **Reuse `admin.OperationsHandler` as the template** — it already reads `rec` + `emergFSM` + `cfg` + `ListPrimaryLifecycles` and renders FSM/schedule/lifecycle sections [VERIFIED: operations.go:117-216]. `/admin/primary/lifecycle` can be a focused subset (current FSM + the open/recent lifecycle event trail) or the planner may extend `/admin/operations`.

## Dashboard Reuse Surfaces (Next.js)

| Surface | File | What it gives Phase 17 |
|---------|------|------------------------|
| Admin proxy (X-Admin-Key injection) | `dashboard/src/app/api/gateway/[...path]/route.ts` | Forwards `/api/gateway/*` → gateway `/admin/*` with the key. **GET-only today** (line 15,49) — Phase 17 must extend it to forward POST/PATCH for config writes + restart, OR (cleaner) do config writes through a **server action** that calls the gateway admin API directly (owner-gated), keeping the proxy read-only. [VERIFIED: route.ts] |
| owner-gate | `dashboard/src/lib/admin-actions.ts:117` `requireOwner(actor, auth)` | Server-side `role === "owner"` enforcement; throws localized error for operators. Wrap every config-write action. [VERIFIED] |
| audit | `admin-actions.ts:156` `writeAuditLog({actor, action, metadata, …})` → `admin_audit_log` table | One row per config change (who/when/what). Phase 13 pattern; self-service NOT audited (config edits ARE). [VERIFIED] |
| audit schema | `dashboard/src/lib/schema-custom.ts:25` `adminAuditLog` (id, actorId, actorEmail, targetId/Email, action, metadata jsonb, createdAt) | Reuse as-is; `action="pod_config.update"`, `metadata={field, old, new}`. [VERIFIED] |
| gateway client wrappers | `dashboard/src/lib/gateway.ts` (+ `gateway.test.ts`) | Typed fetch helpers to the proxy; add a `podConfig`/`lifecycle` fetcher. [VERIFIED: ls] |
| FSM rendering | `dashboard/src/lib/fsm.ts` | Existing FSM-state helpers for the live-status panel. [VERIFIED: ls] |
| operator read-only | `admin-actions.ts` role model (`"owner" | "operator"`) | Operator sees config + live status, cannot edit (UI hide + server `requireOwner`). [VERIFIED] |

## Migration Concern (env→DB while config.go is fail-fast-at-boot)

**Problem:** `config.go` fail-fasts at boot on missing required env (config.go:573) and on legacy primary vars (config.go:666). Moving config to DB must not break boot, and must not create a chicken-and-egg (gateway needs config to boot, config lives in a DB the gateway connects to after `config.Load`).

**Safe migration (recommended):**
1. **Keep env as the typed default/fallback.** `config.Load()` stays exactly as-is — env still loads into `cfg`. This preserves the fail-fast guarantees and means a DB-less boot still works.
2. **Seed `pod_config` from env on first boot.** Migration `0031_create_pod_config.sql` creates the table EMPTY (or with a sentinel). At boot, after `pool` connects, `podconfig.Loader.Refresh` checks: if no `pod_config` row exists, INSERT one populated from the current `cfg.Primary*` values (an idempotent seed), then load it. Subsequent boots read the existing row. (Alternative: a one-shot seed in `main.go` between pool-connect and reconciler-start.)
3. **Precedence: `pod_config` row > env default.** Once seeded, the reconciler reads the snapshot; env only matters if the DB read fails (fallback to `cfg`).
4. **Structural fields:** keep reading from `cfg` at provision (they only change on restart, which re-runs `config.Load` AND re-seeds nothing — so structural edits must write BOTH the `pod_config` row AND… see risk). Simplest: on restart the gateway re-reads `pod_config` for structural too (treat structural as "DB-backed but only consumed at boot"). This means the dashboard writes structural changes to `pod_config`, the row persists, and the restart applies them — env is just the genesis seed.
5. **Do NOT delete env vars from the Portainer stack** in this phase — they remain the disaster-recovery default if `pod_config` is ever wiped. (A later phase could remove them once `pod_config` is proven.)

**Edge case:** `PRIMARY_WHISPER/BGEM3/CHATTERBOX_WEIGHTS_SHA256` have NO env default (fail-fast empty passthrough, config.go:483,488,490). The seed must copy the prod values (captured above) into `pod_config` so a later env removal doesn't reintroduce the fail-fast.

## Runtime State Inventory

Phase 17 is primarily a code/schema change, but it touches DB + live service config:

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | NEW `ai_gateway.pod_config` table (migration 0031). `primary_lifecycles` (read-only consumption). `admin_audit_log` (dashboard DB — new `pod_config.*` action rows). | Migration up on prod gateway DB; seed row from current env. |
| Live service config | Prod gateway env has ~20 `PRIMARY_*` vars in the **Portainer stack UI** (not git). Once `pod_config` is authoritative, these become the *seed/fallback* — keep them but they no longer drive runtime after seed. | Document the dual-source; do NOT delete env this phase. |
| OS-registered state | None — gateway runs as a docker container with `restart: unless-stopped` (the restart mechanism). | None. Verified via arch notes §3 (prod restart policy confirmed). |
| Secrets/env vars | `VAST_AI_API_KEY`, `MINIO_*`, DSNs stay env-only (NOT in `pod_config`). | None — explicitly out of scope. |
| Build artifacts | Gateway binary rebuild + redeploy to n8n-ia-vm (PROD gateway). Dashboard rebuild (ai-dashboard PROD on n8n-ia-vm). | Standard build+deploy (see MEMORY gateway-prod-build-deploy + ai-dashboard-prod-topology). |

**Prod drift flagged:** prod env has `PRIMARY_POD_SERVE_STT=false` (a var whose reader was DELETED in code per config.go:520 / SEED-019) and `PRIMARY_VAST_NUM_GPUS_FALLBACK=1` (code default is 2). The seed must capture the *actual prod values*, and the planner should decide whether to drop the dead `PRIMARY_POD_SERVE_STT` row during this phase.

## Common Pitfalls

### Pitfall 1: Breaking provisioning on a DB config-read failure
**What goes wrong:** Reconciler reads `pod_config` live; a transient DB error returns empty/zero config → provisions with cap=0 (never finds an offer) or empty blocklist (lands on a known-bad host).
**Avoid:** Atomic snapshot with last-good retention (mirror `upstreams.Loader` — never swap in a failed read; loader.go:111). Seed guarantees a valid initial snapshot. Reconciler reads in-memory snapshot, never a synchronous DB call on the hot path.

### Pitfall 2: Mid-lifecycle structural edit silently mis-provisions
**What goes wrong:** Operator edits a weights key while a pod is Ready; assumes it took effect; next provision (or a confused restart) loads a model that doesn't match the image.
**Avoid:** Structural fields are restart-gated AND confirm-gated. The dashboard must show a "restart required to apply" badge for structural fields and block "apply" without the explicit restart action.

### Pitfall 3: Schedule narrowing drains a healthy prod pod immediately
**What goes wrong:** Owner sets DownHour=14 at 15:00; next tick `evaluateReady`→drain kills the serving pod mid-peak.
**Avoid:** Confirm dialog on any schedule edit that closes the current window; surface "this will drain the running pod now" when `IsInPeak` would flip false.

### Pitfall 4: cap-down strands provisioning
**What goes wrong:** Cap lowered below market → `no_offers_below_cap` every attempt → pod never comes up; looks like an outage.
**Avoid:** Confirm on cap-down; the live-status panel (`/admin/primary/lifecycle` showing repeated `no_offers_below_cap` shutdown_reason) makes it diagnosable — exactly the SEED-020 / 2026-06-29 scenario this phase targets.

### Pitfall 5: Multi-replica config skew on restart
**What goes wrong:** `os.Exit(0)` restarts one replica; others keep stale structural config. Only the leader provisions, but a leadership handoff to a stale replica re-provisions with old shape.
**Avoid:** See Open Risks — decide single-replica prod (current) vs. a coordinated restart (publish a `restart` event all replicas honor).

## State of the Art

| Old Approach | Current (Phase 17) Approach | Impact |
|--------------|------------------------------|--------|
| `ssh n8n-ia-vm → sed .env → docker compose recreate` for every blocklist/cap/schedule change | Owner edits in dashboard → `pod_config` row → reconciler hot-reload (seconds, no restart) | Eliminates the manual SSH+sed loop (the 2026-06-29 pain); adds audit + bounds + operator visibility |
| Config drift invisible (stale `PRIMARY_NUM_GPUS` mis-provisioned shape — Phase 6.6.Y) | Single source of truth in `pod_config` + audit trail | Drift is auditable; legacy-var hard-fail already prevents the worst case |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Prod runs a SINGLE gateway replica (so `os.Exit(0)` restart applies cleanly) | Self-restart / Pitfall 5 | If >1 replica, structural restart leaves skew — needs coordinated restart design. **Confirm replica count with operator.** |
| A2 | `PrimaryInfinity/DCGM/Speaches Image` are vestigial under the custom `converseai-primary-pod:main` image (baked in, not separate containers) | Config table #23-25 | If still live separate images, they're structural-and-active, not vestigial. Low risk (still structural either way). |
| A3 | Validation bounds (cap 0.10–1.50, NUM_GPUS 1–2, UpHour 0–23, etc.) match operator intent | Bounds table | Bounds came from arch notes + code comments, not an operator sign-off. discuss-phase should confirm exact ranges. |
| A4 | The dashboard config-write path should be a server action (not the read-only proxy extended to POST) | Dashboard reuse | Either works; planner picks. Server action keeps the proxy read-only + reuses `requireOwner`/`writeAuditLog` cleanly. |
| A5 | `admin_audit_log` (dashboard DB) is the right audit home, not the gateway's partitioned `audit_log` | Open Risks | If config changes must appear in gateway-side audit (e.g. for the `/admin/audit` panel), need a gateway audit write too. |

## Open Questions / Risks for plan-phase

1. **Audit location (A5):** The edit happens dashboard-side (owner server action) → natural home is the dashboard `admin_audit_log` (Phase 13 pattern, `writeAuditLog`). BUT the gateway has its own partitioned `audit_log` + `/admin/audit` panel. **Decision needed:** dashboard-only audit (simplest, recommended) vs. dual-write so config changes show in the gateway audit panel too. Recommend dashboard `admin_audit_log` only, with `action="pod_config.update"` + `metadata={field, old, new}`.

2. **Multi-replica restart coordination (A1):** `os.Exit(0)` restarts only the receiving replica. If prod is single-replica (likely — confirm), trivial. If multi-replica, structural apply needs either (a) a Redis `restart` event all replicas honor (mirror the existing `gw:primary:events` pub/sub the reconciler already uses, reconciler.go:158) or (b) accept that structural changes apply on each replica's natural restart. **Confirm replica count.**

3. **In-flight lifecycle when a hot config changes:** Confirmed safe — provisioning captures config at goroutine spawn (reconciler.go:1149) and the timer/offer-set are fixed for that attempt. A hot edit lands on the NEXT provision/tick. **No special handling needed**, but the dashboard should communicate "applies to next provisioning" for blocklist/cap edits made while a pod is provisioning.

4. **Single-row vs key-value `pod_config` schema:** Single typed row mirrors the `Config` struct + enables per-column CHECK bounds; KV enables per-field audit + flexible additions. **Recommend single-row** + dashboard-side per-field audit diffing. Planner decides.

5. **Proxy must support writes:** `route.ts` is GET-only (line 15). Either extend it for POST/PATCH (restart + config write) or route writes through a server action. **Recommend server action for writes** (owner-gate + audit live there), keep proxy read-only for the live-status poll. (A4)

6. **SEED-020 overlap:** Once blocklist/allowlist/caps are DB-editable, the allowlist *policy* (preference vs. hard-filter) becomes a config knob — but the broaden-vs-hard-filter LOGIC is a separate code change in reconciler.go:1206-1259, OUT OF SCOPE for Phase 17 (it's a provisioning-logic change, not a config-control change). Flag for a future phase; do not bundle.

7. **`PRIMARY_POD_SERVE_STT` dead env (prod):** Lingers in prod env with no reader. The seed must NOT carry it into `pod_config`. Optionally clean it from the Portainer stack during this phase's deploy.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Postgres `ai_gateway` schema (prod gateway DB) | `pod_config` table + LISTEN/NOTIFY | ✓ | DigitalOcean managed PG (CLAUDE.md) | — |
| goose migrations | `0031_create_pod_config.sql` | ✓ (in tree, head 0030) | — | — |
| Prod gateway container w/ `restart: unless-stopped` | self-restart apply | ✓ | CONFIRMED (arch notes §3) | — |
| ai-dashboard PROD (n8n-ia-vm) | dashboard UI | ✓ | MEMORY ai-dashboard-prod-topology | — |
| X-Admin-Key admin API | proxy + new endpoints | ✓ | `ifix_admin_…613f` (CLAUDE.md) | — |

**No blocking missing dependencies.**

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework (gateway) | Go `testing` + `testify`-style; integration via `internal/integration_test` (testcontainers PG/Redis) |
| Framework (dashboard) | Bun test (`*.test.ts`: `admin-actions.test.ts`, `gateway.test.ts`) |
| Quick run (gateway) | `go test ./internal/primary/... ./internal/config/... ./internal/admin/...` |
| Full suite (gateway) | `go test ./... -tags integration` (per MEMORY gateway-integration-tests-not-in-executor-check — run `-tags integration` + `gofmt -l` before push) |
| Quick run (dashboard) | `bun test src/lib/admin-actions.test.ts` |

### Phase Requirements → Test Map (anticipated POD-CFG-* family)
| Behavior | Test Type | Automated Command | File Exists? |
|----------|-----------|-------------------|-------------|
| Hot field re-read from snapshot (blocklist applies next provision) | unit | `go test ./internal/primary/ -run Reconcile` | ❌ Wave 0 (new podconfig snapshot test) |
| `pod_config` Loader refresh + atomic swap + last-good on error | unit | `go test ./internal/podconfig/` (new pkg) | ❌ Wave 0 |
| Bounds rejection (cap/UpHour/NUM_GPUS out of range) | unit | dashboard action test + gateway CHECK | ❌ Wave 0 |
| owner-gate on config write | unit | `bun test src/lib/admin-actions.test.ts` | ⚠️ extend existing |
| audit row on config change | unit | `bun test` (writeAuditLog) | ⚠️ extend existing |
| `/admin/gateway/restart` admin-key gated + 202 | integration | `go test ./internal/admin/ -run Restart` | ❌ Wave 0 |
| `/admin/primary/lifecycle` returns FSM + event trail | integration | `go test ./internal/admin/ -run Lifecycle` | ❌ Wave 0 |
| migration 0031 up/down + seed-from-env idempotent | integration | `go test ./internal/integration_test -run PodConfig -tags integration` | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** package-scoped `go test ./internal/{primary,config,admin,podconfig}/...` + `bun test <touched>.test.ts`
- **Per wave merge:** `go test ./... -tags integration` + `gofmt -l`
- **Phase gate:** full suite green + live UAT (owner edits blocklist in dashboard → prod reconciler avoids host next provision; restart applies a structural change)

### Wave 0 Gaps
- [ ] `gateway/internal/podconfig/loader_test.go` — snapshot refresh + last-good-on-error
- [ ] `gateway/internal/primary/reconciler_test.go` — extend: offer-selection reads snapshot not cfg
- [ ] `gateway/internal/admin/restart_test.go` + `lifecycle_test.go` — admin-key gate + payload
- [ ] `gateway/db/migrations/0031_create_pod_config.sql` + NOTIFY trigger + sqlc regen
- [ ] dashboard: extend `admin-actions.test.ts` for `updatePodConfig` owner-gate + bounds + audit

## Security Domain

### Applicable ASVS Categories
| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | better-auth session (dashboard) + X-Admin-Key (gateway admin API) — both already enforced |
| V4 Access Control | yes | **owner-only edits**, operator read-only — server-side `requireOwner` (admin-actions.ts:117), NOT UI-hiding alone |
| V5 Input Validation | yes | per-field bounds (table above) validated server-side before save + Postgres CHECK constraints on `pod_config` |
| V6 Cryptography | n/a | no new secrets; weights SHAs are public-grade integrity, not secret |
| V7 Logging | yes | `admin_audit_log` row per change (who/when/what) — Phase 13 `writeAuditLog` |

### Known Threat Patterns
| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Operator escalates to edit config | Elevation of Privilege | Server-side `requireOwner` (not client role check); confirm dialogs are UX not security |
| Admin key leaks to browser | Information Disclosure | Phase 15 proxy keeps `GATEWAY_ADMIN_KEY` server-only (route.ts greps clean); writes via server action, not client fetch |
| Malicious config bricks prod (cap=0, empty days, bad image) | Denial of Service | Bounds + CHECK constraints + confirm on dangerous fields; live-status panel makes mis-config diagnosable |
| Unauthenticated restart endpoint | Tampering/DoS | Mount inside `adminRouter` so `admin.Middleware` (X-Admin-Key) gates it (main.go:1489) |
| Config-read failure starves provisioning | DoS | Last-good atomic snapshot; env fallback; never synchronous DB read on hot path |

## Sources

### Primary (HIGH confidence — direct codebase reads)
- `gateway/internal/config/config.go:1-794` — full config surface, fail-fast pattern, loaders
- `gateway/internal/primary/reconciler.go:132-230,375-418,1115-1262,1333-1579` — tick loop, evaluate*, offer selection, provision goroutine
- `gateway/internal/primary/lifecycle.go:141-337` — Deps + Reconciler struct, cfg/rule snapshot
- `gateway/internal/primary/schedule.go:95-160` — ParseScheduleEnv, rule consumption
- `gateway/internal/primary/budget.go:60-120` — MonthlyPrimaryBudgetBRL read
- `gateway/internal/upstreams/loader.go:39,103-196` — hot-reload pattern to mirror
- `gateway/db/migrations/0009_upstreams_notify_trigger.sql` — NOTIFY trigger template
- `gateway/internal/db/gen/primary_lifecycles.sql.go:35-299` — available lifecycle queries
- `gateway/internal/admin/middleware.go:156-197` — X-Admin-Key gate
- `gateway/internal/admin/operations.go:117-216` — closest existing handler (reads rec+cfg+lifecycles)
- `gateway/cmd/gateway/main.go:131,346,1488-1524` — os.Exit, LISTEN goroutine, admin router mount
- `dashboard/src/app/api/gateway/[...path]/route.ts` — admin proxy (GET-only)
- `dashboard/src/lib/admin-actions.ts:117,156` — requireOwner + writeAuditLog
- `dashboard/src/lib/schema-custom.ts:25` — admin_audit_log schema
- **Live prod env capture** — `ssh n8n-ia-vm docker inspect ifix-ai-gateway` (all PRIMARY_* values, 2026-06-29)
- `notes/dashboard-pod-config-control-architecture.md` — locked decisions
- `SEED-020`, `research/questions.md`, ROADMAP Phase 17 block

## Metadata

**Confidence breakdown:**
- Config surface + classification: HIGH — every field traced to load-site + consumption-site + live prod value
- Hot-reload mechanism: HIGH — exact precedent exists (upstreams.Loader + NOTIFY 0009)
- Implementation seams: HIGH — Reconciler struct + offer-selection + admin router all read directly
- Bounds: MEDIUM — derived from code comments + arch notes, not operator-signed (A3)
- Multi-replica restart: MEDIUM — depends on prod replica count (A1, confirm)

**Research date:** 2026-06-29
**Valid until:** ~2026-07-29 (stable internal codebase; re-verify prod env values if Phase 16+ deploys change PRIMARY_* between now and planning)

---

## Orchestrator addendum (2026-06-29) — A1 resolved

**Risk A1 (multi-replica restart skew) is MOOT in prod today.** Verified: PROD `/opt/ai-gateway-prod/docker-compose.yml` runs the gateway as a **single replica** (1 `ifix-ai-gateway` container, no `deploy:`/`replicas:`/`scale` block — `deploy:` is Swarm-only and ignored by `docker compose up` per the compose file's own Pitfall 3 note). `os.Exit(0)` restarts the only replica → structural-config self-restart is safe. The leader-election seen in logs ("acquired primary leadership") is HA/failover semantics, not concurrent replicas. **Caveat for the plan:** if prod ever scales to >1 gateway replica, the self-restart endpoint must fan out (or restarts must be coordinated) — document this as a known constraint, not a today-blocker.
