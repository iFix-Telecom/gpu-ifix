---
phase: 11
plan: 11-06
slug: load-test-live-uat
status: complete-gates-evaluated
date: 2026-06-12
operator: pedro (orchestrator-driven, parallel executor agent)
spend_usd: 0.62
gates_all_passed: false
---

# Phase 11 PRD-01 Load-Test Evidence

> Re-attempt 3 — 2026-06-11/12. Prior attempts (2026-05-27, 2026-05-28) DEFERRED at
> the fixture-availability gate (`bd_ai_gateway_prod` had ~57 replayable rows vs
> the ≥1000 gate). The primary reconciler silent-hang blocker was RESOLVED via
> commit `01e7558` (quick 260527-wgs). **This attempt: the data-volume blocker
> is also resolved — a real (non-synthesized) 1207-row fixture was exported from
> a genuine prod peak window, the DRY-RUN passed GO, and the 30-min sustained run
> COMPLETED.** History from the two prior attempts is preserved in the
> "Prior Attempts" appendix at the bottom of this file.

## Metadata

- Date: 2026-06-12 (preflight + fixture 2026-06-11; force-up + dry-run + 30-min run 2026-06-12)
- Operator host: ops-claude (162.55.92.154 — whitelisted in DO Trusted Sources)
- Gateway target URL: https://ai-gateway.converse-ai.app
- Prod gateway host: n8n-ia-vm, stack `/opt/ai-gateway-prod`
- Audit DB (correct target): `bd_ai_gateway_prod.ai_gateway.audit_log` via env
  `AI_GATEWAY_PG_DSN` (NOT `DASHBOARD_DATABASE_URL` → that points to
  `bd_ai_dashboard_prod`, which has no `audit_log` table — corrected 2026-06-11)
- Fixture path: `/tmp/load-fixture.jsonl` (1207 rows, LOCAL/gitignored — never committed)
- Load generator: `scripts/integration-smoke/load-replay.py` @ git_sha `e9d04afbcea0`
- Run params: `--duration 1800 --max-concurrency 50 --speedup 4`
- Run window (UTC): **2026-06-12T11:46:44Z → 2026-06-12T12:00:42Z** (~14 min wall;
  the 1207-row, 2-hour fixture window replayed fully under speedup 4 before the
  1800 s duration cap — every fixture row was sent exactly once)
- Committed report: `.planning/phases/11-prod-hardening/load-report-2026-06-12.json`

## Task 11-06-01 — 11-01 Deliverables Preflight (PASS, all 5 sub-checks exit 0)

| Sub-check | Result | Evidence |
|-----------|--------|----------|
| 1. audit-log-export.py importable + JOIN present | PASS | `--help` exits 0, 8 args matched; `LEFT JOIN ai_gateway.audit_log_content c` at line 319 |
| 2. load-replay.py importable + 7 args + env keys + multipart | PASS | `--help` exits 0, all 7 args present; `IFIX_KEY_` 3 matches; `files = {...}` multipart at line 398 |
| 3. report schema Draft 2020-12 valid + panic gate `[boolean,null]` + gates_external_inputs | PASS | `jsonschema.Draft202012Validator.check_schema` OK; assertions pass |
| 4. STT fixture OGG signature | PASS | `Ogg data, Opus audio, version 0.1, mono, 16000 Hz` |
| 5. fixture dir marker + gitignore | PASS | `.planning/load-test-fixtures/.gitignore` present; `.gitignore` carries `load-test-fixtures/*.jsonl` |

Runtime deps on host confirmed importable: `jsonschema`, `httpx 0.28.1`, `psycopg`,
`structlog`, `jq 1.6`. `python` binary absent — used `python3` (Python 3.11.2).

Zero Vast spend, zero HTTPS calls to prod gateway in this task.

## Task 11-06-02 — Fixture Export + Row-Count + Route-Mix Gate (PASS — REAL prod fixture)

**Decisive change vs prior attempts:** prod `audit_log` has accumulated real
tenant traffic. The export is genuine prod data — NOT the x32 synthesis the
2026-05-27 attempt was forced into.

Prod audit_log volume survey (last 30 days, `AI_GATEWAY_PG_DSN` →
`bd_ai_gateway_prod.ai_gateway.audit_log`):

- Total rows: 1587
- Route distribution: `/v1/chat/completions` 774, `/v1/embeddings` 425,
  `/v1/audio/transcriptions` 304, `/v1/health/upstreams` 63, `gatewayctl_breaker` 21
- Busiest contiguous window: 2026-05-28 22:00–24:00 UTC (1207 rows)

Chosen export window: **2026-05-28T22:00:00Z → 2026-05-29T00:00:00Z** (2 h).
`audit-log-export.py` emitted **1207 rows, 0 errors, 0 skipped** (re-exported
2026-06-12 to confirm freshness; identical gate results).

Gate results (`/tmp/load-fixture.jsonl`):

| Gate | Threshold | Actual | Verdict |
|------|-----------|--------|---------|
| Row count | ≥ 1000 | 1207 | PASS |
| Route: chat | present | 704 | PASS |
| Route: embed | present | 401 | PASS |
| Route: STT | present | 101 | PASS |
| Route: health/upstreams | (incidental) | 1 | n/a |
| Chat streaming shape (`_sanitized_body.stream==true`) | ≥ 1 | 100 | PASS |
| Chat tool-call shape (`tools`/`tool_choice`) | ≥ 1 | 203 | PASS |
| Sensitive rows | 0 | 0 | PASS (Pitfall 1) |
| `_replay_api_key` field rows | 0 | 0 | PASS |
| Raw key material (`ifix_sk_`/`sk-or-`/`Bearer `) in fixture bytes | 0 | 0 | PASS |

Tenants in fixture (NAME only — no key values): `converseai` (data_class=normal).
Required env var: `IFIX_KEY_CONVERSEAI`. Step E preflight Python check exits 0
(env var exported in operator shell; sourced from local-only `~/.claude/CLAUDE.md`,
never written to any committed file).

**Single-tenant caveat:** the peak window is 100% `converseai` (normal) traffic.
Per D-01 + Open Question #1 the baseline excludes sensitive tenants by design, so a
single-normal-tenant fixture satisfies the PRD-01 baseline contract. It does mean
the replay exercises one tenant's auth path repeatedly rather than a multi-tenant
mix; documented as a known characteristic, not a gate failure.

## Task 11-06-03 — DRY-RUN — GO (operator-acknowledged)

2-minute DRY-RUN executed against prod gateway 2026-06-12T11:41:23Z → 11:43:39Z:

- `total_requests` = **558**, `error_count` = **1** (0.18% — well under the <5% dry-run gate)
- routes = `{chat: 304, embed: 202, stt: 51, health/upstreams: 1}`
- jsonschema-valid against `load-replay-report-schema.json`
- `/v1/chat/completions` present ✓

**GO signal (operator pedro):** `DRY-RUN GO — n=558; err=1 (0.18%); routes={chat:304,embed:202,stt:51};`
`schema_valid=true; primary Ready; spend cap $5 acknowledged 2026-06-11T23:29Z (re-confirmed 2026-06-12).`

## Force-Up Saga (2026-06-12 — operator history; see SEED-011)

The primary tier-0 pod required a multi-strike force-up before the run. Summary
(full bug write-up in `.planning/seeds/SEED-011-fsm-ready-with-stopped-instance.md`):

1. Early force-up attempts landed on machines **45688 / 94979** → CDI faults
   (GPU not visible inside the container) → both **blocklisted**.
2. A force-up to machine **28974** (Vietnam, instance 40642262) reached FSM
   `ready` at `00:03:49Z`, but Vast **billing-stopped** the instance ~`00:04Z`
   (account balance hit the threshold: balance −$0.056, credit 0;
   `actual_status=exited`, `intended_status=stopped`). The instance still EXISTED
   (no 404), so the steady-state Ready reconciler did **not** tear it down — FSM
   stayed `ready` for 25+ minutes while LLM/STT traffic got `connection refused`
   502s. This is **SEED-011** (HIGH): Ready-state reconcile only reacts to 404,
   not to `actual_status in {exited, stopped}`.
3. Operator topped up credit (+$19.94) and forced up the final, healthy primary:
   **Vast instance 40697682, machine 34554** (British Columbia), **1×RTX 5090**,
   **$0.4806/h**, `actual_status=running`. Pod IP `207.102.87.207`, ports
   llm:53606 / stt:53860 / tts:54172 / dcgm:53691 — all `/health` 200.
4. FSM reached `ready` and stayed STABLE (90 s recheck passed). This is the primary
   used for the DRY-RUN and the 30-min run.

## Task 11-06-04 — 30-min Sustained Run (COMPLETE — gates evaluated)

### Liveness gating (per the prober-bug caveat — see Known Issues below)

The run was gated on **FSM=ready + Vast `actual_status=running` + direct pod
`/health` 200** — NOT on `/v1/health/upstreams` returning 200 (that endpoint
returns 503 status=failed because of the prober bug; it does not reflect real
dispatcher routing). Primary health was confirmed at T+0, T+6 min, T+12 min, and
post-run (T+14 min) — **Ready throughout**:

| Checkpoint (UTC) | FSM state | Vast actual/intended | pod llm /health |
|------------------|-----------|----------------------|-----------------|
| T+0  (11:46:44Z) | ready | running / running | 200 |
| T+6  (11:52:05Z) | ready | running / running | (run alive) |
| T+12 (11:58:17Z) | ready | running / running | (run alive) |
| post (12:01:42Z) | ready | running / running | 200 |

### Run summary

- start_ts: **2026-06-12T11:46:44Z** · end_ts: **2026-06-12T12:00:42Z**
- total_requests: **1207** · error_count: **33** (all `upstream_5xx`)
- error rate: **33 / 1207 = 2.73%** (non-503; the run produced no 503s)
- schema_valid: **True**

### Per-route metrics (P50/P95/P99)

| Route | n | p50_ms | p95_ms | p99_ms | upstream attribution |
|-------|---|--------|--------|--------|----------------------|
| /v1/chat/completions | 704 | 8877 | **21680** | 22084 | tier-0 pod (llama.cpp) — see note |
| /v1/embeddings | 401 | 1720 | **2662** | 3064 | tier-0 local-embed container |
| /v1/audio/transcriptions | 101 | 1601 | **2800** | 2825 | tier-0 pod (speaches/STT) |
| /v1/health/upstreams | 1 | 523 | 523 | 523 | gateway-local (incidental fixture row) |

**Per-upstream distribution NOT available in the committed report — observed-system
limitation (see Known Issues #2).** The gateway emits **no `X-Upstream` response
header** in this build (verified by a live header dump: a 200 chat response carried
`server: llama.cpp`, `x-request-id`, `x-ratelimit-*` — but no upstream-attribution
header). The report schema also forbids a `summary.routes.<route>.upstreams` object
(`additionalProperties: false`, per-route record is exactly `n/p50_ms/p95_ms/p99_ms`).
Adding per-upstream attribution would require a schema + engine change (Rule 4
architectural — deliberately NOT done in this plan). The `server: llama.cpp` header
and the dispatcher's `Resolve()` override (emergency_pod_* synthetic upstreams)
confirm real traffic landed on the tier-0 pod; tier-1 (OpenRouter/OpenAI) was **not
exercised** — primary was healthy throughout, so no failover occurred. This is the
plan's anticipated Phase 12 candidate: "forced tier mix during baseline" +
upstream-attribution header.

### Gates verdict (D-04 SLO v1.0 — 5 measured + 1 external panic)

| Gate | Threshold | Measured | Verdict |
|------|-----------|----------|---------|
| p95_chat_ms_le_5000 | ≤ 5000 ms | 21680 ms | **FAIL** |
| p95_embed_ms_le_1000 | ≤ 1000 ms | 2662 ms | **FAIL** |
| p95_stt_ms_le_10000 | ≤ 10000 ms | 2800 ms | **PASS** |
| error_rate_lt_1pct | < 1% non-503 | 2.73% | **FAIL** |
| zero_5xx_panic | 0 panics | 0 (log grep + Sentry) | **PASS** |
| **all_passed** | AND of 5 | — | **FALSE** |

- **zero_5xx_panic = true:** `docker logs ifix-ai-gateway --since 2026-06-12T11:46:44Z
  --until 2026-06-12T12:00:42Z | grep -cE "^panic:|goroutine [0-9]+ \[running\]"`
  returned **0**. Sentry query (`project=ifix-ai-gateway-prod, level=fatal`, run
  window) — no fatal events. `gates_external_inputs.verified_at = 2026-06-12T12:01:42Z`.
- **all_passed = false:** three SLO gates breached under the 1×RTX 5090 single-GPU
  **DEV** shape at concurrency 50. Per the load-test discipline ("report whatever the
  numbers are, do not retry-shop for green numbers") these are recorded as-is.

### SLO-breach analysis (no retry-shopping)

The 1×RTX 5090 single-GPU shape **saturated** under concurrency 50:
- **chat** p50 8.9 s / p95 21.7 s — the single GPU serialized LLM decode across 50
  in-flight requests; queueing dominated latency. The PRD-01 SLO (p95 ≤ 5 s) assumes
  the prod 1×5090 shape under *organic* load, not a 4×-compressed replay at c=50.
- **embed** p95 2.66 s vs 1 s SLO — embed shares the same host; under chat saturation
  the embed container contended for CPU/PCIe.
- **error_rate** 2.73% — 33 transient `upstream_5xx` under saturation; **0 panics,
  0 connection-refused, 0 sensitive-tenant 503s**. The 5xx correlate with the
  prober-bug breaker flapping (local-* breakers open/half-open continuously) which
  intermittently short-circuited a small fraction of dispatch attempts.

This is a **baseline saturation characterization**, not a clean SLO pass. The honest
conclusion: at speedup 4 + concurrency 50 the single-GPU dev shape cannot hold the
v1.0 chat/embed P95 SLOs. The numbers are the deliverable; remediation (lower
concurrency, prod 2×3090/multi-GPU shape, or revised SLO pacing assumptions) is a
Phase 11-gap / Phase 12 input, captured below.

## Vast Spend Accounting

- Instance: **40697682**, machine **34554**, **1×RTX 5090**, rate **$0.4806/h**.
- 30-min run incremental: ~14 min wall × $0.4806/h ≈ **$0.11**.
- Session total (this UAT, including prior force-up attempts + dry-run + run):
  **≈ $0.62** (prior ~$0.50 from yesterday's CDI-failed force-ups + today's ~1 h
  pod time). Credit top-up +$19.94 left ample headroom.
- **Within the $5 absolute Phase 11 cap.** Operator-acknowledged spend cap honored;
  no abort triggered.

## Deviations / Pitfalls Hit

1. **[Rule 3 — blocking, resolved] `python` binary absent on host** — used `python3`
   (3.11.2) for all script invocations. No code change.
2. **[Plan deviation — concurrency] plan task 11-06-04 Step 1 specifies
   `--max-concurrency 50`; executed at 50.** (The orchestrator prompt's task_4
   guidance flagged a possible default of 20 — the PLAN is authoritative and says 50,
   so 50 was used. Documented for traceability.) Added `--speedup 4` per task_4
   guidance so the 2-hour fixture window replays inside the 30-min budget; this is an
   explicit plan-supported flag (`reviews LOW #5`), not an unplanned change.
3. **[Observed-system, NOT fixed] prober-bug breaker flapping** — `local-llm`/
   `local-stt`/`local-tts`/`voice-api-piper` breakers flapped open↔half-open the
   entire run (see Known Issues #1). Real dispatcher traffic was unaffected (1207
   requests landed on the pod, llama.cpp responded). NO code edited — this is a
   pre-existing gateway bug, surfaced as a seed for the orchestrator.
4. **[Observed-system, NOT fixed] no `X-Upstream` header** — per-upstream attribution
   unavailable (Known Issues #2). Documented, not fixed (would be Rule 4 architectural).

## Known Issues (observed-system behavior — documented, NOT fixed in this plan)

### Issue #1 — prober uses `loader.All()`, ignoring `tier0Override` (SEED-012 candidate)

The health prober calls `loader.All()` (gateway/internal/upstreams/loader.go:358)
instead of `Resolve()` (:222), so probes hit the **static, dead** `UPSTREAM_LLM_URL`
(10.10.10.20:8000) rather than the live emergency_pod_* synthetic upstream. Result:
- `local-llm`/`local-stt`/`local-tts` breakers **flap open forever** in prod even
  with a healthy pod (observed flapping throughout this run's log window).
- `/v1/health/upstreams` returns HTTP **503 status=failed** even though real traffic
  is served correctly.
- The **dispatcher** uses `Resolve()` with the tier-0 override, so **real traffic
  flows to the pod correctly** — verified live (chat 200 with `server: llama.cpp`;
  1207 requests landed). The breaker flapping cost a small fraction of dispatch
  attempts (correlated with the 33 transient `upstream_5xx`).

**Consequence honored in this run:** gating used FSM=ready + Vast running + direct
pod `/health` 200, NOT `/v1/health/upstreams`. A seed (SEED-012 candidate) will be
filed by the orchestrator.

### Issue #2 — no upstream-attribution response header

The gateway emits no `X-Upstream` (or equivalent) response header in this build, so
`load-replay.py`'s `r.headers.get("X-Upstream")` is always `None` and the report
cannot populate per-route per-upstream distribution. Combined with the schema's
`additionalProperties: false`, per-upstream attribution (reviews MEDIUM #2) is
**structurally unavailable** in this build. Tier attribution in this run is inferred
from `server: llama.cpp` (tier-0 pod) + primary-Ready-throughout (no tier-1 failover).
Phase 12 candidate: add an upstream-attribution header + extend the report schema.

### Issue #3 — SEED-011: FSM stays `ready` when the Vast instance is stopped/exited

Surfaced during the force-up saga (see above + the SEED-011 file). Ready-state
reconcile only reacts to 404, not to `actual_status in {exited, stopped}`. Billing-stop
left the FSM advertising tier-0 Ready against a dead pod for 25+ minutes. HIGH severity;
seed already filed (`.planning/seeds/SEED-011-fsm-ready-with-stopped-instance.md`).

## Cross-ref

- Sibling report: `load-report-2026-06-12.json` (committed, schema-valid).
- Seeds: `SEED-011` (FSM ready-with-stopped-instance), SEED-012 candidate (prober
  loader.All vs Resolve).

## Sign-off

Operator: pedro (parallel executor agent, orchestrator-driven). Date: 2026-06-12.
Verdict: **30-min sustained run COMPLETE; gates.all_passed = FALSE** (chat/embed P95
+ error-rate breached under 1×RTX 5090 single-GPU dev shape at c=50; zero panics).
Recorded as a baseline saturation characterization → Phase 11-gap / Phase 12 input.
No retry-shopping. Primary Ready throughout. Spend ≈ $0.62, within $5 cap.

## Secret Hygiene Attestation (reviews HIGH #5 / Consensus Top Action #3)

ATTESTATION: This evidence file and the sibling load-report-2026-06-12.json contain
ZERO raw API keys, ZERO Authorization header values, ZERO request bodies, ZERO response
bodies, ZERO DSN strings, ZERO PII. All tenant references use env-var labels
(IFIX_KEY_CONVERSEAI) or slug labels (converseai). Sentry references use issue URL only.
Log grep results redact to error class / breaker-state lines (no bodies, no keys).
The audit DSN and the Vast API key were bound to shell variables and never printed.
Verified by: pedro (executor agent) at 2026-06-12T12:02Z.

---

## Prior Attempts (appendix — preserved from earlier sessions)

### Attempt 1 — 2026-05-27 (BLOCKED on primary reconciler tech debt)

11-06 ABORTED before DRY-RUN. Two blockers: (1) dev audit_log volume insufficient
(201 rows over 5 weeks; only 32 usable → x32 synthesis to 1024 rows, artificial
baseline); (2) primary reconciler hung silently between offer-pick and
lifecycle-create DB write (~$0.04 orphan spend before manual destroy). Blocker (2)
RESOLVED 2026-05-27 via commit `01e7558` (3-strike confirm + BestEffortDestroy).
Also discovered + fixed n8n-ia-vm Phase 06.7 config drift (13 missing env vars,
appended from dev `.env`; backup `/opt/ai-gateway-prod/.env.bak-11-06-uat`).

### Attempt 2 — 2026-05-28T20:55Z (DEFERRED on data volume)

Stage-1 deliverable preflight 5/5 PASS. Fixture-availability GATE FAILED:
`bd_ai_gateway_prod` had ~57 replayable rows over 7 days vs the ≥1000 + 5-route-class
gate (prod cutover was only ~2 days prior; tenant integration in early ramp).
Decision: defer 11-06 + 11-07 live UATs to a future session; re-export once prod
accumulates ≥1000 rows in a window. **No Vast spend incurred.**

### Attempt 3 — 2026-06-11/12 (THIS session — COMPLETE)

Data-volume blocker RESOLVED — real 1207-row fixture exported from the
2026-05-28 22:00–24:00 UTC prod peak window. Preflight 5/5 PASS, fixture gates PASS,
DRY-RUN GO. After the force-up saga (SEED-011), the 30-min sustained run completed
against a healthy 1×RTX 5090 primary. Gates evaluated: **all_passed=false** (3 SLO
breaches under single-GPU saturation; 0 panics). See tasks 11-06-01…04 above.
