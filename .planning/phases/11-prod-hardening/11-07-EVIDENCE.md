---
phase: 11
plan: 11-07
slug: chaos-kill-primary-live-uat
status: live-executed-critical-finding
date: 2026-06-12
operator: pedro (orchestrator-driven, parallel executor agent)
spend_usd: 0.20
prd: PRD-02
chaos_verdict: FAIL_with_critical_finding
seed_reproduced: SEED-011
---

# Phase 11 PRD-02 Chaos: Primary Kill Live UAT

> **Re-run 2026-06-12.** Prior session (2026-05-27, preserved in the Appendix below)
> shipped `scripts/chaos/vast-delete.sh` but DEFERRED the live chaos run because the
> primary could not be brought to `ready` (reconciler silent-hang, since fixed by
> `01e7558`). 11-06 established a healthy primary baseline on 2026-06-12. **This
> session EXECUTED the live mid-load chaos DELETE.** The result is a **HARD-GATE
> FAILURE that is itself the deliverable**: the chaos test reproduced **SEED-011**
> live in prod — the gateway does NOT fail over when its primary Vast instance is
> destroyed; it serves `502 upstream_unreachable` to tenants instead of routing to
> tier-1. This is precisely the class of bug PRD-02 chaos testing exists to surface.

## Metadata

- Date: 2026-06-12
- Operator host: ops-claude (this executor agent; 162.55.92.154)
- Gateway target URL: `https://ai-gateway.converse-ai.app`
- Prod gateway host: n8n-ia-vm, stack `/opt/ai-gateway-prod` (operator-managed docker compose)
- Killed Vast instance: **40697682** (machine 34554, British Columbia, 1×RTX 5090,
  $0.4806/h, pod IP 207.102.87.207) — id only, no token
- Vast API key: referenced as `${VAST_AI_API_KEY}` env var (sourced at runtime, never printed)
- Tenant key: `${IFIX_KEY_CONVERSEAI}` (env-var label; value never echoed/committed)
- Load fixture: `/tmp/load-fixture.jsonl` (1207 rows, LOCAL/gitignored)
- Load generator: `scripts/integration-smoke/load-replay.py` (`--duration 600 --max-concurrency 20 --speedup 4`)
- Authoritative metrics source: `bd_ai_gateway_prod.ai_gateway.audit_log` via `${AI_GATEWAY_PG_DSN}`
  (the load-replay JSON report was not finalized — load was stopped during recovery —
  so per-request status/upstream/error_code were read directly from `audit_log`, which
  is the system of record)
- Vast spend this run: **≈ $0.20** (killed pod final hour fraction + post-chaos re-up pod time);
  within the $5 Phase 11 cap (11-06 spent $0.62; cumulative ≈ $0.82)

## Pre-flight (PASS)

| Check | Result |
|-------|--------|
| Primary FSM `state` | `ready` (entered 2026-06-12T11:18:51Z) |
| Live Vast instance 40697682 | `actual_status=running`, `intended_status=running` |
| Direct pod `/health` (llm:53606) | `{"status":"ok"}` |
| Baseline chat through gateway | HTTP **200**, `server: llama.cpp`, `model.gguf`, fp `b9191-...` → tier-0 pod confirmed |
| Audit traffic (last 3 min, pre-chaos) | chat 133×200 (upstream=`llm`), embed 201×200 (upstream=`embed`) → load flowing |

**SEED-011 display caveat observed at pre-flight:** `gatewayctl primary state` reported
`state=ready` but `pod_instance_id` / `pod_url` / `lifecycle_id` display fields were
**empty** (carried over from the 11-06 force-up). The proxy nonetheless routed real
traffic to the live pod (chat 200 with `server: llama.cpp`). Because the FSM display
held no instance_id, the chaos script's `resolve_instance_id()` could not extract it;
the instance id was sourced authoritatively from the **Vast API instance list** (id
40697682) instead. This empty-display state is the secondary SEED-011 bug and is the
mechanism that defeats the steady-state reconciler (see Critical Finding).

## Chaos timeline (UTC)

| Time | Event | Source |
|------|-------|--------|
| 12:13:50Z (T+0) | `curl -X DELETE` Vast instance 40697682 → **HTTP 200 `{"success":true}`** (`delete_status=killed`) | vast DELETE |
| T+0s | Vast instance status = **gone** (instant; instance list length for 40697682 → 0) | Vast API poll |
| T+0s | First chat **502 `upstream_unreachable`** (time-to-first-failure = 0s) | audit_log |
| T+0..T+16s | 54 chat **200** still served (in-flight reqs to pod IP before host network teardown) | audit_log |
| T+0..T+57s | 50 chat **502** + 50 STT **502** (`upstream=llm`/`stt`; error `upstream_unreachable`) | audit_log |
| T+0..T+90s | FSM **stayed `ready`** every 5s sample — NEVER advanced to Draining (no auto-detection) | OBSERVE-FIRST loop |
| T+90s (12:15:20Z) | OBSERVE-FIRST window complete. Decision: **MANUAL INTERVENTION PERMITTED** (FSM did not auto-recover) | observe loop |
| 12:16:52Z | Operator `force-down --reason 11-07_post_chaos_recover` → FSM `ready → draining` | gatewayctl |
| ~12:17:00Z | FSM `draining → asleep` (clean teardown once force-down event consumed) | gatewayctl |
| 12:17:11Z | Operator `force-up --reason 11-07_post_chaos_recover` → FSM `asleep → provisioning`; new Vast instance booting | gatewayctl |

## OBSERVE-FIRST 90-second window (5s interval — reviews MEDIUM #1)

NO `gatewayctl primary force-up` was invoked before t+90s. The script's fixed window
ran to completion first. Snapshot (FSM + Vast instance + live chat probe):

| t_rel | FSM phase | Vast instance | chat HTTP | dispatcher upstream |
|-------|-----------|---------------|-----------|---------------------|
| t+0s  | ready | gone | 502 (some 200 in-flight) | llm (dead pod) |
| t+5s  | ready | gone | 502 | llm (dead pod) |
| t+12s | ready | gone | 502 | llm (dead pod) |
| t+19s | ready | gone | 502 | llm (dead pod) |
| t+25s | ready | gone | 502 | llm (dead pod) |
| t+32s | ready | gone | 502 | llm (dead pod) |
| t+38s | ready | gone | 502 | llm (dead pod) |
| t+45s | ready | gone | 502 | llm (dead pod) |
| t+52s | ready | gone | 502 | llm (dead pod) |
| t+58s | ready | gone | 502 | llm (dead pod) |
| t+65s | ready | gone | 502 | llm (dead pod) |
| t+71s | ready | gone | 502 | llm (dead pod) |
| t+78s | ready | gone | 502 | llm (dead pod) |
| t+84s | ready | gone | 502 | llm (dead pod) |
| t+90s | ready | gone | 502 | llm (dead pod) |

**t+90s decision:** `auto_recovery=false`; `manual_intervention=true` (issued at
t+90s+Δ = 12:16:52Z, ~182s after DELETE). **Justification:** FSM remained `ready`
against a destroyed instance for the entire window with zero autonomous transition —
the steady-state reconciler did not detect the instance death, so operator
force-down/force-up was required to recover. This satisfies the MEDIUM #1 rule
(intervention only after t+90s with FSM still non-recovered).

## Allowed-error-classes histogram — T+0..T+60s (reviews MEDIUM #2)

Window `2026-06-12 12:13:50Z .. 12:14:50Z`, from `audit_log`:

| Status | Error class | Count | Verdict |
|--------|-------------|-------|---------|
| 200 | (success, in-flight) | 54 | tapered to 0 by T+16s |
| 502 | `upstream_unreachable` (chat, upstream=llm) | 50 | **HARD GATE FAIL** |
| 502 | `upstream_unreachable` (STT, upstream=stt) | 50 | **HARD GATE FAIL** |
| 503 | `upstream_unavailable_transient` | 0 | (none — gateway never entered graceful transient) |
| 503 | `breaker_open` | 0 | (none — breaker never opened to short-circuit) |
| 503 | `upstream_unavailable_for_sensitive_tenant` | 0 | n/a (fixture is `converseai`/normal only) |
| 504 | `gateway_timeout` | 0 | (none) |
| **500** | **panic** | **0** | **HARD GATE PASS** (no panics, no goroutine crashes) |
| **502** | **bad_gateway / upstream_unreachable** | **100** | **HARD GATE FAIL** (zero tolerance) |

- **HTTP 500 panic count = 0** — HARD GATE **PASS**. The gateway did not crash; it
  emitted a clean structured error `{"error":{"code":"upstream_unreachable","type":"api_error"}}`.
- **HTTP 502 bad_gateway count = 100** — HARD GATE **FAIL** (zero tolerance breached).
  This is NOT upstream connection corruption from the gateway side; it is the gateway
  faithfully reporting that its only configured LLM/STT upstream (the destroyed tier-0
  pod) is unreachable, WITHOUT failing over to tier-1.

## Critical Finding — SEED-011 reproduced live (PRD-02 invisible-failover does NOT hold)

**PRD-02 expectation:** kill primary mid-load → breaker `local-llm` opens by natural
observation → FSM advances Ready→Draining→Asleep → tier-1 OpenRouter transparently
takes LLM/STT traffic (a 5–10s blip is acceptable; sustained 5xx is not).

**Observed reality:**
1. DELETE killed the instance instantly (Vast `gone` at T+0).
2. The steady-state Ready reconciler **never detected the death** — FSM stayed `ready`
   for the full 90s window (and would have stayed indefinitely; force-down was required).
3. There was **no tier-1 failover**: every post-cutover chat/STT request dispatched to
   the now-dead `upstream=llm`/`upstream=stt` and returned **502 `upstream_unreachable`**.
   Zero rows show a tier-1 upstream. The dispatcher had no live alternative wired in for
   this primary because the FSM still advertised tier-0 as Ready.
4. Net tenant impact: **100% sustained 502** for LLM + STT from ~T+16s onward (after
   the in-flight 200s drained), persisting until operator force-down/force-up.

**Root cause (confirms SEED-011):** the Ready-state reconcile tick reacts only to a
Vast **404 not-found** for the *tracked* instance id. Here the FSM's tracked
`pod_instance_id` was already EMPTY (the secondary SEED-011 display/state-hash bug from
the 11-06 force-up), so the steady-state loop had no instance to poll for not-found and
therefore never fired the death path. The pod IP was held in the proxy routing table
independently of the (empty) FSM instance id, so traffic kept being routed to the dead
IP. The `force-down` path DID advance the FSM correctly (ready→draining→asleep in <5s),
proving the FSM machinery works — it is the **autonomous detection trigger** that is
missing for the `actual_status in {exited,stopped}` / destroyed-with-empty-tracked-id
case. This is exactly SEED-011's hypothesis, now confirmed under a real DELETE (not just
the billing-stop path originally observed).

**Severity:** HIGH / CRITICAL for PRD-02. Without a reconciler fix, any primary host
yank, billing-stop, or DELETE produces a sustained tenant-facing 502 outage with no
automatic failover. This is the canonical RUNBOOK-INCIDENTS class-1 incident (11-09
ingests this evidence).

## Recovery / Cleanup (reviews MEDIUM #1)

- Vast instance count for killed primary post-DELETE: **0** (verified via Vast API
  instance list; `killed_primary_present=0`, `total=0` at observation time) — **no
  orphan, no runaway spend**.
- Operator intervention AFTER the 90s window: `force-down` (FSM ready→draining→asleep)
  then `force-up` (FSM asleep→provisioning; a fresh Vast instance booted, `vast_instances=1`).
- Post-chaos primary recovery to `ready` was in `provisioning` (new pod loading weights)
  at evidence-write time; the force-up path is the established 11-06 recovery procedure
  and the new instance was confirmed live in the Vast list. Final ready-state confirmation
  is recorded in the SUMMARY (recovery monitor ran post-write).

## SLO note (D-04)

P95 latency during the chaos window is not a meaningful SLO measurement here: post-cutover
the LLM/STT paths returned 502 rather than served responses, so latency percentiles reflect
fast-fail 502s, not served work. The dispositive metric is the **error class** (sustained
502), not latency. The 54 pre-teardown 200s carried normal tier-0 latency.

## Deviations from plan

1. **[Rule 1 / adaptation — orchestrator-directed] Instance id sourced from Vast API,
   not gatewayctl.** The plan's script extracts `pod_instance_id` from `gatewayctl
   primary state`, but the SEED-011 empty-display bug left that field blank. Per the
   orchestrator's live-state guidance, the instance id (40697682) was taken from the
   authoritative Vast instance list. No script edit was needed for the run; the script's
   capability is unchanged (the limitation is in the gateway's display, not the script).
2. **[Rule 1 / adaptation] Failover evidenced via dispatcher (audit `upstream` column),
   not breaker state.** Per the documented prober bug (`/v1/health/upstreams` returns
   null/503 even with a healthy pod), breaker state is unreliable for evidence. The
   `audit_log.upstream` + `status_code` + `error_code` columns are the authoritative
   dispatcher-behavior signal and were used instead. This is honest acknowledgment of
   the prober-bug limitation called out in 11-06-EVIDENCE.md Known Issue #1.
3. **[Plan deviation — concurrency] Load ran at `--max-concurrency 20`** (vs 11-06's 50)
   to keep the pre-chaos baseline clean and avoid the single-GPU saturation 5xx noise
   characterized in 11-06; the chaos signal (502 on dead upstream) is independent of
   concurrency.
4. **[Observed-system, NOT fixed] SEED-011 confirmed.** No gateway code was edited — this
   is a live reproduction of a pre-existing bug, surfaced as the deliverable. Remediation
   is a follow-up (reconciler must treat destroyed/exited/empty-tracked-id as pod-down and
   either re-provision or open breakers + fail over to tier-1).

## Cross-ref

- Chaos script: `scripts/chaos/vast-delete.sh` (this plan; extended this session with
  OBSERVE-FIRST tags + Pattern E summary block).
- Baseline: `11-06-EVIDENCE.md` (PRD-01 load test, 2026-06-12; prober-bug + SEED-011 saga).
- Seed: `.planning/seeds/SEED-011-fsm-ready-with-stopped-instance.md` (now CONFIRMED via
  live DELETE, not just billing-stop).
- Operator-archived off-repo: `/tmp/observe-window.tsv`, `/tmp/chaos-load.log`,
  audit_log query outputs (DSN-bound, not committed).

## Sign-off

Operator: pedro (parallel executor agent, orchestrator-driven). Date: 2026-06-12.
**Verdict: PRD-02 chaos EXECUTED; result = HARD-GATE FAIL (100× HTTP 502 in T+0..T+60s,
zero tier-1 failover) — SEED-011 reproduced live.** Zero HTTP 500 panics (no crash).
Killed instance cleaned up (count=0, no orphan). Primary recovered via documented
post-90s force-down/force-up. Spend ≈ $0.20, cumulative Phase 11 ≈ $0.82, within $5 cap.
No retry-shopping: the failure is the finding.

## Secret Hygiene Attestation (reviews HIGH #4)

ATTESTATION: this evidence file contains NO raw API keys, NO Bearer token values, NO
Authorization header values, NO request bodies, NO response bodies (only the structured
error *shape* `{"code":"upstream_unreachable"}` — no payload), NO DSN strings, NO PII.
Tenant references use env-var labels (`${IFIX_KEY_CONVERSEAI}`). Vast API key referenced
as `${VAST_AI_API_KEY}`. The Vast token and audit DSN were bound to shell variables and
never printed. Pre-commit grep gate
`grep -rE 'ifix_sk_[a-z0-9]{20,}|Bearer [a-f0-9]{60}|postgres://[^@]+@' 11-07-EVIDENCE.md 11-07-PLAN.md`
returns zero matches. Verified by: pedro (executor agent) at 2026-06-12.

---

## Appendix — Prior session (2026-05-27, artifact shipped, live UAT deferred)

Plan 11-07's first session shipped `scripts/chaos/vast-delete.sh` (0755, `set -euo
pipefail`, `bash -n` clean) and DEFERRED the live chaos run because the primary could
not reach `ready` (reconciler silent-hang, later fixed by `01e7558`). The reviews-folded
contract was closed at the artifact level:

| Review finding | Closure |
|----------------|---------|
| `[HIGH #4]` no raw secrets | `VAST_AI_API_KEY` env-var only; argv never carries the token; prefix-only log attribution |
| `[MEDIUM #1]` FIXED 90s observe-then-intervene | `CHAOS_OBSERVE_SECONDS=90`; script does not auto-`force-up`; OBSERVE-FIRST tags |
| `[MEDIUM #2]` allowed-error-class budget | final-log prompt to check 500/502 in T+0..T+60s (hard gate) |
| `[LOW #3]` JSON-preferred id extraction | tries `gatewayctl primary state --json` then awk text fallback |
| `[LOW #5]` DELETE idempotent + timeout + retry | `--connect-timeout 10 --max-time 30`; 200/204/404 success; 5xx 1× retry |

The deferral has now been resolved by this session's live execution (above). The script
was extended this session (commit on the per-agent worktree branch) with literal
`OBSERVE-FIRST` window tags, `OPEN_AT` tracking, explicit `DELETE_STATUS`, and a parseable
Pattern E stdout summary block (`delete_status`/`open_at`/`auto_recovery`/`fsm_at_t90s`/
`breaker_at_t90s`).
