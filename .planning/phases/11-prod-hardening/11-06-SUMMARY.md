---
phase: 11-prod-hardening
plan: 11-06
subsystem: testing
tags: [load-test, slo, ai-gateway, vast, prd-01, uat, latency, percentiles]

requires:
  - phase: 11-01
    provides: load-replay.py + audit-log-export.py + report schema + STT fixture OGG
provides:
  - PRD-01 live load-test evidence (30-min sustained replay against prod gateway)
  - load-report-2026-06-12.json (schema-valid, per-route P50/P95/P99, D-04 gates)
  - SLO baseline characterization for 1x RTX 5090 single-GPU dev shape at c=50
  - SEED-012 candidate (prober loader.All vs Resolve) + SEED-011 reinforcement
affects: [11-07, 11-08, 11-09, phase-12, slo-revision, runbook-ingestion]

tech-stack:
  added: []
  patterns:
    - "Liveness gating on FSM=ready + Vast actual_status=running + direct pod /health 200 (NOT /v1/health/upstreams, which is poisoned by the prober bug)"
    - "Speedup-compressed fixture replay: 2h window x speedup 4 -> ~14min wall, full row replay"
    - "External panic gate flipped post-run via docker-logs grep + Sentry (zero_5xx_panic)"

key-files:
  created:
    - .planning/phases/11-prod-hardening/load-report-2026-06-12.json
  modified:
    - .planning/phases/11-prod-hardening/11-06-EVIDENCE.md

key-decisions:
  - "Reported honest SLO-breach numbers (chat/embed P95 + error-rate FAIL) with no retry-shopping for green"
  - "Per-upstream attribution documented as structurally unavailable (no X-Upstream header + schema additionalProperties:false) rather than forcing a Rule 4 schema/engine change"
  - "Gated on pod /health + Vast running + FSM ready, NOT /v1/health/upstreams (prober-bug 503)"
  - "Used --max-concurrency 50 per PLAN (authoritative) + --speedup 4 per task guidance"

patterns-established:
  - "Pattern: observed-system bugs surfaced as seeds, never auto-fixed during a UAT load run"
  - "Pattern: report stays schema-valid as the engine emits it; tier-attribution gaps live in EVIDENCE.md, not in a mutated report"

requirements-completed: [PRD-01]

duration: 14min (run) / ~35min (task 4+5 execution)
completed: 2026-06-12
---

# Phase 11 Plan 11-06: PRD-01 Live Load-Test Summary

**30-min sustained replay (1207 req, 0 panics) against the prod ai-gateway on a healthy 1xRTX 5090 primary; SLO gates evaluated honestly — chat/embed P95 + error-rate breached under single-GPU saturation at c=50, STT P95 + zero-panic PASS, gates.all_passed=false.**

## Performance

- **Duration:** ~14 min run wall (11:46:44Z → 12:00:42Z); ~35 min total task 4+5 execution
- **Started:** 2026-06-12T11:46:44Z (run launch)
- **Completed:** 2026-06-12T12:02Z (commits)
- **Tasks:** 2 (Task 4 — 30-min run; Task 5 — evidence + report + summary)
- **Files modified:** 2 committed (load-report-2026-06-12.json created; 11-06-EVIDENCE.md extended)

## Accomplishments

- Executed the 30-min sustained load replay against `https://ai-gateway.converse-ai.app` with the real 1207-row prod fixture (`--duration 1800 --max-concurrency 50 --speedup 4`). All 1207 rows replayed; 33 transient `upstream_5xx`, 0 panics, 0 sensitive-tenant 503s.
- Primary tier-0 (Vast 40697682 / machine 34554 / 1×RTX 5090) confirmed **Ready throughout** (FSM=ready + Vast running + pod /health 200 at T+0/T+6/T+12/post).
- Captured per-route P50/P95/P99 in a schema-valid `load-report-2026-06-12.json`; flipped `zero_5xx_panic=true` after a 0-count log grep + Sentry check; recomputed `all_passed=false`.
- Extended `11-06-EVIDENCE.md` with the full run evidence, force-up saga (SEED-011), dry-run GO, per-route table, gates verdict, SLO-breach analysis, spend accounting, and the prober-bug known issue (SEED-012 candidate).
- Secret-hygiene grep + forbidden-key audits PASS on both committed files.

## Task Commits

1. **Task 4 + Task 5: 30-min run + EVIDENCE + report** - `650aad7` (docs)

_Task 4 (run) and Task 5 (evidence authoring + commit) ship in one atomic docs commit per the plan's task 11-06-05 commit step (the report is the run's only artifact; the run itself produces no source changes)._

## Files Created/Modified

- `.planning/phases/11-prod-hardening/load-report-2026-06-12.json` - schema-valid D-04 gates report; per-route P50/P95/P99; gates.all_passed=false; zero_5xx_panic verified.
- `.planning/phases/11-prod-hardening/11-06-EVIDENCE.md` - extended from the becce70 preflight evidence with the 30-min run, force-up saga, gates verdict, SLO analysis, spend, and known-issue notes.

## Gates Verdict (D-04 SLO v1.0)

| Gate | Threshold | Measured | Verdict |
|------|-----------|----------|---------|
| p95_chat_ms_le_5000 | ≤ 5000 ms | 21680 ms | FAIL |
| p95_embed_ms_le_1000 | ≤ 1000 ms | 2662 ms | FAIL |
| p95_stt_ms_le_10000 | ≤ 10000 ms | 2800 ms | PASS |
| error_rate_lt_1pct | < 1% | 2.73% | FAIL |
| zero_5xx_panic | 0 | 0 | PASS |
| **all_passed** | AND | — | **FALSE** |

Per-route n: chat 704, embed 401, STT 101, health/upstreams 1 (total 1207).

## Decisions Made

- Reported SLO-breach numbers verbatim (no retry-shopping). The 1×RTX 5090 single-GPU dev shape saturated under speedup-4 / concurrency-50 replay; chat queueing dominated latency. Recorded as a baseline saturation characterization → Phase 11-gap / Phase 12 input.
- Did NOT add per-upstream attribution (would be a Rule 4 schema + engine change). The gateway emits no `X-Upstream` header and the schema forbids the field; tier-0 attribution inferred from `server: llama.cpp` + primary-Ready-throughout (no tier-1 failover).
- Gated on direct pod health, not `/v1/health/upstreams` (poisoned by the prober bug).

## Deviations from Plan

### Auto-fixed / documented

**1. [Rule 3 - Blocking] `python` binary absent; used `python3`**
- **Found during:** Task 4 launch.
- **Issue:** host has `python3` (3.11.2), no `python` symlink.
- **Fix:** invoked all scripts via `python3`. No code change.
- **Committed in:** n/a (invocation only).

**2. [Plan-supported flag] `--speedup 4`**
- The 2-hour fixture window would not replay within the 30-min budget at 1×; `--speedup 4` (reviews LOW #5 flag) compressed pacing so all 1207 rows replayed in ~14 min wall. SLO percentiles are still measured at the compressed real-request pacing. Concurrency stayed at the PLAN-specified 50.

---

**Total deviations:** 2 (1 blocking-resolved, 1 plan-supported flag). **Impact:** none on correctness; both documented in EVIDENCE.md.

## Issues Encountered

- **Force-up saga (SEED-011):** prior force-up attempts hit CDI faults on machines 45688/94979 (blocklisted) and a billing-stop killed instance on machine 28974 (FSM stayed `ready` against a dead pod for 25+ min — HIGH bug, seed filed). Resolved with a +$19.94 credit top-up and a healthy 1×RTX 5090 (instance 40697682). All pre-run; the 30-min run itself ran clean against the healthy primary.
- **Prober bug (SEED-012 candidate):** `loader.All()` vs `Resolve()` divergence → `local-*` breakers flap open forever + `/v1/health/upstreams` returns 503 despite a healthy pod. Real dispatcher traffic unaffected (1207 requests landed). NOT fixed (observed-system; surfaced as a seed).

## Known Stubs

None.

## Threat Flags

None — the load report carries only the pre-vetted metric columns (no bodies, headers, keys, or DSNs); both committed files passed the secret-grep + forbidden-key audits.

## Next Phase Readiness

- PRD-01 live load-test evidence is committed and runbook-ingestible (11-09 canonical example).
- **Blocker for a clean SLO pass:** `gates.all_passed=false`. The orchestrator/planner should treat the chat/embed P95 + error-rate breaches as a Phase 11-gap (revisit concurrency, GPU shape, or SLO pacing assumptions) and file the SEED-012 prober-bug seed. SEED-011 (FSM ready-with-stopped-instance) is already filed.
- `closes reviews`: HIGH #5 (secret hygiene — audited), MEDIUM #1 (11-01 preflight — PASS), LOW #3 (DRY-RUN gate — GO), LOW #4 (fixture row-count + route-mix — PASS). **MEDIUM #2 (per-upstream distribution) PARTIAL** — documented as structurally unavailable in this gateway build (no X-Upstream header + schema additionalProperties:false); deferred to Phase 12 (upstream-attribution header + schema extension).

## Self-Check: PASSED

- FOUND: `load-report-2026-06-12.json` (schema-valid, gates.all_passed=false)
- FOUND: `11-06-EVIDENCE.md` (extended)
- FOUND: `11-06-SUMMARY.md`
- FOUND: commit `650aad7` (report + evidence)
- Secret-grep + forbidden-key audits PASS on all three files.

---
*Phase: 11-prod-hardening*
*Completed: 2026-06-12*
