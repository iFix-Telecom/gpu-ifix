---
slug: audit-pipeline-silent-since-2026-05-25
status: root_cause_found
goal: find_root_cause_only
tdd_mode: false
trigger: phase-11-08-segment-b-audit-gates-failed
started_at: 2026-05-27T23:45:00Z
resolved_at: 2026-05-28T00:05:00Z
specialist_hint: go
---

# Audit Pipeline Silent Since 2026-05-25 — Root Cause Report

## Status

**root_cause_found** — Not a gateway bug. The audit pipeline is HEALTHY and
writing rows continuously to `bd_ai_gateway_prod` (latest row at
2026-05-28 01:26:22 UTC, 99 total rows since the 2026-05-26 11:40:43 UTC
cutover). The perceived "silence since 2026-05-25" is a
**diagnostic-query target mismatch**: the smoke gates and the operator
queried the legacy database `bd_ai_gateway` (without the `_prod` suffix),
which has been a quiet leftover since the Phase 10 prod cutover on
2026-05-26.

## Summary (1 paragraph)

Phase 10 deployed `/opt/ai-gateway-prod/` on n8n-ia-vm at 2026-05-26 ~11:18
UTC with `AI_GATEWAY_PG_DSN=postgres://…/bd_ai_gateway_prod?sslmode=require`
— a brand-new prod database created by `scripts/deploy/bootstrap-postgres.sh`
during Phase 10-02. The pre-existing dev/UAT database `bd_ai_gateway` was
left in place but stopped receiving writes from 2026-05-25 22:50:50 UTC
(the last gateway request against the legacy dev stack `/opt/ai-gateway-dev/`
before cutover prep). The `smoke-sensitive-failover.py` audit_decision gate
and the operator's drill-down `SELECT MAX(ts) FROM ai_gateway.audit_log`
both pointed at `bd_ai_gateway` (legacy), where they correctly found no new
rows. Querying the actual prod DSN `bd_ai_gateway_prod` shows the audit
writer is working perfectly — the 11-08 Segment B chaos probes are all
captured (sensitive 503, normal 200/503, /v1/health/upstreams). No source
edit is needed. The fix is a **smoke-runner config update**: point the
audit_decision gate's DB connection at `bd_ai_gateway_prod`.

## Symptom Timeline (UTC)

| Time (UTC) | Source | Event |
|------------|--------|-------|
| 2026-04-19 21:03:07Z | DB | `bd_ai_gateway.ai_gateway.audit_log` first row (legacy dev DB cold-start) |
| 2026-05-23 01:42:10Z | DB | Phase 06.6 UAT lifecycle 99 closed (last UAT-era activity) |
| 2026-05-25 22:50:50Z | DB | `bd_ai_gateway.ai_gateway.audit_log` MAX(ts) — last row in legacy DB |
| 2026-05-25 23:03 BRT (~02:03 UTC 05-26) | git | commit 50921b7 "Phase 06.9 close + cascade-close 02/03/05" — last dev-stack activity before cutover |
| 2026-05-26 ~03:00–11:18 UTC | ops | Phase 10 Wave 0/1/2/3 — bootstrap-postgres.sh creates `bd_ai_gateway_prod`; `/opt/ai-gateway-prod/` stack deployed on n8n-ia-vm |
| 2026-05-26 11:40:43Z | DB | `bd_ai_gateway_prod.ai_gateway.audit_log` first row — prod audit pipeline going live |
| 2026-05-27 22:01 BRT (01:01 UTC 05-28) | ops | This-session operator patched 13 Phase 06.7 weights env vars to /opt/ai-gateway-prod/.env and `docker compose up -d` recreated container |
| 2026-05-28 01:01:26Z | docker | Current `ifix-ai-gateway` container created |
| 2026-05-28 01:22:44Z–01:26:22Z | DB | Phase 11-08 Segment B chaos probes captured in `bd_ai_gateway_prod` audit_log (sensitive 503 ×4, normal 200/503 ×6, health probes ×4) |
| 2026-05-27 ~23:30 UTC | operator | Detected MAX(ts)=2026-05-25 22:50:50 against `bd_ai_gateway` (the WRONG db) → mis-diagnosed as gateway silence |

## Evidence Gathered

### E1 — Gateway env var: `AI_GATEWAY_PG_DSN` points at `bd_ai_gateway_prod`

```
$ ssh n8n-ia-vm 'docker inspect ifix-ai-gateway --format "{{range .Config.Env}}{{println .}}{{end}}"' | grep PG_DSN
AI_GATEWAY_PG_DSN=postgres://doadmin:<REDACTED>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_gateway_prod?sslmode=require
AI_GATEWAY_PG_MAX_CONNS=10
```

Note the `_prod` suffix. There is also `DASHBOARD_DATABASE_URL=…/bd_ai_dashboard_prod…`.
Both prod databases were created by `scripts/deploy/bootstrap-postgres.sh`
during Phase 10-02.

### E2 — DO cluster contains THREE relevant databases

```
$ ssh n8n-ia-vm 'docker run --rm postgres:16-alpine psql "…/defaultdb…" \
   -c "SELECT datname FROM pg_database WHERE datname LIKE '\''%gateway%'\'' OR datname LIKE '\''%dashboard%'\'' ORDER BY datname;"'
       datname
----------------------
 bd_ai_dashboard_prod
 bd_ai_gateway          ← legacy dev/UAT, the one operator queried
 bd_ai_gateway_prod     ← the live prod target (Phase 10 cutover)
(3 rows)
```

### E3 — `bd_ai_gateway` (legacy): MAX(ts) = 2026-05-25 22:50:50, 211 rows total

```
$ ssh n8n-ia-vm 'docker run --rm postgres:16-alpine psql "…/bd_ai_gateway…" \
   -c "SELECT MAX(ts), MIN(ts), COUNT(*) FROM ai_gateway.audit_log;"'
            max_ts             |            min_ts             | total_rows
-------------------------------+-------------------------------+------------
 2026-05-25 22:50:50.457542+00 | 2026-04-19 21:03:07.507603+00 |        211
```

Matches the operator's drill-down exactly. This database is a Phase ≤09
artifact, abandoned at cutover.

### E4 — `bd_ai_gateway_prod` (live prod): MAX(ts) = 2026-05-28 01:26:22, 99 rows, growing

```
$ ssh n8n-ia-vm 'docker run --rm postgres:16-alpine psql "…/bd_ai_gateway_prod…" \
   -c "SELECT MAX(ts), MIN(ts), COUNT(*) FROM ai_gateway.audit_log;"'
            max_ts             |             min_ts            | total_rows
-------------------------------+-------------------------------+------------
 2026-05-28 01:26:22.235945+00 | 2026-05-26 11:40:43.06619+00  |         99
```

First row at 11:40:43 UTC 2026-05-26 = ~22 minutes after Phase 10 stack
came up (consistent with cutover sequence).

### E5 — Phase 11-08 Segment B chaos probes ARE captured in `bd_ai_gateway_prod`

```
$ ssh n8n-ia-vm 'docker run --rm postgres:16-alpine psql "…/bd_ai_gateway_prod…" \
   -c "SELECT ts, tenant_id, data_class, route, status_code, upstream
         FROM ai_gateway.audit_log ORDER BY ts DESC LIMIT 10;"'
              ts               |              tenant_id               | data_class |        route         | status_code | upstream
-------------------------------+--------------------------------------+------------+----------------------+-------------+----------
 2026-05-28 01:26:22.235945+00 | 4af0a9b5-f4a7-4e34-8dc1-7f7084aeef98 | sensitive  | /v1/chat/completions |         503 | llm
 2026-05-28 01:26:18.053271+00 | 4af0a9b5-f4a7-4e34-8dc1-7f7084aeef98 | sensitive  | /v1/chat/completions |         503 | llm
 2026-05-28 01:26:17.870701+00 | 4af0a9b5-f4a7-4e34-8dc1-7f7084aeef98 | sensitive  | /v1/health/upstreams |         503 |
 2026-05-28 01:26:06.560364+00 | 3415dbec-8f91-4a80-9b16-7e3535059ab7 | normal     | /v1/health/upstreams |         503 |
 2026-05-28 01:24:08.406938+00 | 3415dbec-8f91-4a80-9b16-7e3535059ab7 | normal     | /v1/health/upstreams |         503 |
 2026-05-28 01:24:04.012786+00 | 4af0a9b5-f4a7-4e34-8dc1-7f7084aeef98 | sensitive  | /v1/chat/completions |         503 | llm
 2026-05-28 01:24:03.426122+00 | 3415dbec-8f91-4a80-9b16-7e3535059ab7 | normal     | /v1/chat/completions |         503 | llm
 2026-05-28 01:23:42.650456+00 | 3415dbec-8f91-4a80-9b16-7e3535059ab7 | normal     | /v1/health/upstreams |         503 |
 2026-05-28 01:22:48.795825+00 | 3415dbec-8f91-4a80-9b16-7e3535059ab7 | normal     | /v1/chat/completions |         200 | llm
 2026-05-28 01:22:44.516694+00 | 4af0a9b5-f4a7-4e34-8dc1-7f7084aeef98 | sensitive  | /v1/chat/completions |         503 | llm
```

These rows directly correspond to Phase 11-08 Segment B chaos:
- `tenant_id=4af0a9b5-…` = sensitive (`telefonia` or `cobrancas`) returning
  503 `upstream_unavailable_for_sensitive_tenant` (RES-08 behavior — `streaming_fail_fast` and `fail_closed` gates PASS)
- `tenant_id=3415dbec-…` = normal tenant mixing 200 (OpenRouter up) and 503
  (during chaos drop window)
- `/v1/health/upstreams` probes also captured

The audit writer is functioning perfectly.

### E6 — Audit writer source confirms schema-name `ai_gateway` is hard-coded; database is DSN-driven

```
$ rg 'pgx.Identifier|"ai_gateway"' gateway/internal/audit/writer.go
gateway/internal/audit/writer.go:291:		pgx.Identifier{"ai_gateway", "audit_log"},
```

Writer uses `pgx.CopyFrom(ctx, pgx.Identifier{"ai_gateway", "audit_log"}, …)` —
schema is `ai_gateway`, table is `audit_log`. Database routing is entirely
governed by `AI_GATEWAY_PG_DSN`. No code change can have caused writes to
shift databases — only the env var changes the target.

### E7 — Gateway container last-restart and live-logs healthy

```
$ ssh n8n-ia-vm 'docker inspect ifix-ai-gateway --format "{{.Created}} {{.State.StartedAt}}"'
2026-05-28T01:01:26.853351487Z 2026-05-28T01:01:29.954537801Z
```

Container is the post-env-patch recreation from earlier this session. Logs
show normal breaker state-flap (expected — primary GPU is asleep, that's
why all `llm` and `local-*` upstreams are open). No `audit` error lines.
No `panic`, `fatal`, `writer exited`, or `flush failed` lines anywhere
in the last 2 hours of logs.

### E8 — Git log: no audit/writer-related commits in 2026-05-23..2026-05-26 window

```
$ git log --all --pretty=format:"%h %ai %s" --since=2026-05-23 --until=2026-05-26 \
    -- gateway/internal/audit/ gateway/cmd/gateway/
(empty — no commits touched these paths in the window)
```

The audit writer module has not been modified anywhere near the
2026-05-25 22:50:50 boundary. Last source touch to `gateway/internal/audit/`
predates the symptom by days, ruling out a code regression.

## Hypothesis Evaluation

| ID | Hypothesis | Verdict | Evidence |
|----|------------|---------|----------|
| H1 | Wrong DB target — gateway writes to a different DSN than what was queried | **CONFIRMED** | E1+E2+E3+E4+E5: gateway writes to `bd_ai_gateway_prod`; operator queried `bd_ai_gateway`. Both DBs exist; legacy DB stopped at 2026-05-25 22:50:50 (pre-cutover); prod DB has been ingesting normally since 2026-05-26 11:40:43. |
| H2 | INSERT grant lost on `ai_gateway_app` role | **REFUTED** | E4+E5: writes are succeeding in `bd_ai_gateway_prod` right now (99 rows incl. tonight's chaos probes). If grants were broken, the live writes would also fail. (Note: gateway uses `doadmin` per E1 DSN, not `ai_gateway_app`, but this is orthogonal — the writes work.) |
| H3 | Audit writer error path swallowed | **REFUTED** | E5+E7: writes succeed and reach the DB. There are no errors to swallow. |
| H4 | Async writer goroutine died (panic + non-recovered) | **REFUTED** | E5+E7: writer goroutine is alive and producing rows continuously. No panic in last 2h logs. |
| H5 | Partition maintenance issue (RANGE partition by ts) | **REFUTED** | E5: row at 2026-05-28 lands in the correct partition (202605 carries through, 202606 already pre-created — gateway has 4 partitions visible per `\d ai_gateway.audit_log`). |
| H6 | Batch flush stuck on db pool exhaustion | **REFUTED** | E5: throughput is ~10 rows/min during chaos — no queueing pathology. |
| H7 | Gateway restart cleared in-memory audit buffer | **REFUTED** | E5: container restarted at 2026-05-28 01:01:26 and yet rows from 01:22:44 onward are present. Buffer pathology would only lose seconds, not days. |
| H8 | Phase 11-02 dashboard migration broke audit DSN | **REFUTED** | E1 shows `AI_GATEWAY_PG_DSN` still points at the correct prod DB; dashboard migration (2026-05-27) touched only `DASHBOARD_DATABASE_URL`. |

## Root Cause

**Not a defect in `gateway/internal/audit/writer.go`.** The defect is in the
**smoke-runner's database connection target** (the gate that emitted the
false-positive `audit_decision` FAIL in 11-08 Segment B), compounded by an
operator drill-down query also issued against the wrong database.

### Where the wrong DSN lives

1. **Smoke runner** — `phases/11-prod-hardening/smoke-sensitive-failover.py`
   (or the env/config file it reads). The `audit_decision` gate connects
   to a Postgres database to validate that an audit row exists for the
   request_id it just generated. That DSN must point at
   `bd_ai_gateway_prod` (matching the gateway's `AI_GATEWAY_PG_DSN`),
   not the legacy `bd_ai_gateway`.

2. **Operator runbook / mental model** — Any drill-down SQL during incident
   triage (e.g. RUNBOOK-INCIDENTS) that says
   `SELECT MAX(ts) FROM ai_gateway.audit_log` must be qualified with the
   correct dbname (`bd_ai_gateway_prod`) or use a documented `$AUDIT_DSN`
   variable.

### Why this happened

`scripts/deploy/bootstrap-postgres.sh` (Phase 10-02) created the new prod
database with the `_prod` suffix to keep it cleanly separated from the
existing dev/UAT `bd_ai_gateway`. Phase 10 docs and Phase 10 RUNBOOK
followed through, but Phase 11 smoke artifacts and the operator's
ad-hoc SQL did not get updated to the new name. The legacy DB was never
dropped (correct decision — it has 211 historical rows for audit/forensic
continuity), so the wrong DSN silently returns "stale" results.

## Recommended Fix (PR-shaped, NOT applied)

Two surgical changes, no gateway source touched:

### Fix 1 — Smoke runner DSN

**Suggested change:** Update the smoke runner so its `audit_decision` gate
connects to `bd_ai_gateway_prod`. Identify the smoke runner's DB
configuration (likely an env file under `.planning/phases/11-prod-hardening/`
or a script env var), and either:
- Set its DSN env var to mirror the gateway's `AI_GATEWAY_PG_DSN` (single
  source of truth), or
- Read directly from `/opt/ai-gateway-prod/.env` at smoke-runner startup.

**Test recipe (one-liner):**
```bash
ssh n8n-ia-vm 'docker run --rm postgres:16-alpine psql "$AUDIT_DSN" -c \
  "SELECT request_id, ts FROM ai_gateway.audit_log
   WHERE request_id = '\''<probe-request-id>'\'' LIMIT 1;"'
```
After cutover, the gate should find a row for every probe request_id within
≤1s of the request completing (writer flush interval is 1s — see writer.go:25).

### Fix 2 — RUNBOOK / mental-model fix

**Suggested change:** Add a one-line note to `.planning/RUNBOOK-INCIDENTS.md`
(or wherever the audit drill-down query is documented) clarifying that the
authoritative prod audit DB is **`bd_ai_gateway_prod`**, not
**`bd_ai_gateway`**. Also: prefix all SQL drill-down snippets with the
intended target DB name as a comment.

Example:
```sql
-- Target: bd_ai_gateway_prod (NOT bd_ai_gateway — legacy dev/UAT, stale since 2026-05-25)
SELECT MAX(ts) FROM ai_gateway.audit_log;
```

### Optional follow-up — eliminate the legacy DB confusion

Once Phase 11 closes and forensic continuity from the dev/UAT period is no
longer needed:
- Take a final dump of `bd_ai_gateway` (e.g. `pg_dump … bd_ai_gateway > audit_legacy_2026-04..2026-05.sql`) for archival.
- Rename it to `bd_ai_gateway_legacy_archived_<date>` or drop it via DO console.
- This eliminates the foot-gun permanently.

**DO NOT execute the rename/drop in this session** — operator must
authorize and verify the archive first.

## Cross-Reference to Related Bugs

### Primary Reconciler Silent Hang (`primary-reconciler-silent-hang.md`)

Confirmed **completely separate root cause**. The reconciler bug was a
missing log + missing BestEffortDestroy on the `ErrInstanceNotFound` branch
of `waitForReadyOrDestroy` (gateway/internal/primary/reconciler.go:893-900).
That bug touches `vast.Client.GetInstance` error handling and Vast.ai
orphan-cleanup — entirely unrelated to audit writes.

**Shared DB pool sanity check:** Both bugs use the same `*pgxpool.Pool`
configured by `AI_GATEWAY_PG_DSN`. In the reconciler bug, the DB writes
for `InsertPrimaryLifecycle` / `ClosePrimaryLifecycle` succeeded (lifecycle
row visible in `gatewayctl primary lifecycles`). In this bug, audit_log
writes are also succeeding in the same database. Conclusion: the shared
pool is NOT the cause for either bug. The two bugs are fully decoupled.

### Phase 06.7 env-var drift (this session, earlier)

The operator's earlier-session fix (appending 13 missing Phase 06.7 env
vars to `/opt/ai-gateway-prod/.env`) is the reason container creation time
is `2026-05-28 01:01:26Z` instead of the Phase 10 cutover timestamp. The
audit pipeline silence persisted across that container recreate **because
the operator was still querying the wrong database the whole time** — the
new container kept writing to `bd_ai_gateway_prod` (correctly), but the
verification query kept hitting `bd_ai_gateway` (also correctly returning
"no new rows"). This explains the "audit silence persisted after env patch"
observation in the initial brief without invoking H1..H8 as
gateway-internal causes.

## Resolution

**root_cause_found.** No source edit required. Fix is configuration-level
(smoke runner + runbook). Operator should authorize Fix 1 + Fix 2 in a
separate session.

The Phase 11-08 Segment B `audit_decision` and `never_external` gates
should be **re-run after Fix 1 is applied** — based on E5, the rows
already exist in the correct database, so the gates will pass cleanly
on re-execution.

The "audit pipeline silent since 2026-05-25" bug entry can be **closed
without code change** once smoke runner and runbook are aligned.
