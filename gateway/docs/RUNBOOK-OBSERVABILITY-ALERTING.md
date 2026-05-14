# Observability & Alerting Runbook — Phase 7 (Dashboard, Alerting, Metrics, Sentry)

**Owner:** IFIX Platform Engineering
**Last updated:** 2026-05-14
**Stack:** `ai-gateway-dev` / `ai-gateway-prod` (Portainer) + the `dashboard` service in the same stack
**Phase reference:** `.planning/phases/07-observability-dashboard-alerting/07-CONTEXT.md`

This runbook covers the Phase 7 observability + alerting subsystem:
`gateway/internal/alert/` (the alerting goroutine + Chatwoot/ClickUp/Brevo
channels), `gateway/internal/admin/` (`/admin/metrics` + `/admin/audit`
JSON), `gateway/internal/obs/` (`/metrics` Prometheus + the Sentry
`BeforeSend` redactor), `gateway/internal/audit/` (the `audit_log` writer
extended with `fsm_transition` rows), and the `dashboard/` Next.js 15
operator dashboard. Read this when:

- The operator dashboard shows stale data, a wrong FSM state, or won't load.
- A critical/warning event happened but no WhatsApp message, email, or
  ClickUp task arrived.
- `/metrics` active-series count is climbing toward the 10k budget.
- `gateway_alert_dropped_total` is non-zero.
- A Sentry event looks like it might be leaking a secret or a request body.
- Post-incident review of the alerting + audit trail.

Sibling runbooks:

- [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md) — Phase 6 Vast.ai
  auto-provisioning. The Phase 6 FSM transitions are exactly what the
  Phase 7 alerter classifies and what the dashboard FSM panel renders.
- [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) — Phase 3 circuit-breaker
  + tier-0 ↔ tier-1 fallback. The `gw:breaker:events` Pub/Sub stream the
  alerter subscribes to originates here.
- [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md) — Phase 4
  per-tenant rate-limit + quota + billing. The `/admin` sub-router +
  admin-key middleware the dashboard proxies through was added in Phase 4.

The companion live-verification sheet is
[`.planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md`](../../.planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md)
— read its **Prerequisites** section before the first deploy with alert
credentials.

---

## Architecture Overview (60 seconds)

```
   Redis Pub/Sub                gateway server
   gw:breaker:events  ─┐   ┌──────────────────────────────────────────┐
   gw:shed:events     ─┼──▶│ alert.Alerter goroutine (spawned EARLY)   │
   gw:emerg:events    ─┘   │  consume → severityFor() → dedup (SET NX  │
                           │  EX 300) → bounded per-channel workers    │
                           │     ├─ Chatwoot client (gobreaker)        │──▶ Chatwoot API (WhatsApp)
                           │     ├─ ClickUp  client (gobreaker)        │──▶ ClickUp API (task)
                           │     └─ Brevo    client (gobreaker)        │──▶ Brevo SMTP (email)
                           │  full worker queue → AlertDroppedTotal++  │
                           │                                          │
                           │ /metrics          (Prometheus, no auth)  │──▶ promtool / Prometheus
                           │ /admin/metrics    (JSON, X-Admin-Key)     │─┐
                           │ /admin/audit      (JSON, X-Admin-Key)     │─┤
                           │ /admin/usage      (JSON, X-Admin-Key)     │─┤
                           │                                          │ │
                           │ obs.BeforeSend redactor ─────────────────│──▶ Sentry (redacted events)
                           │ audit.Writer  ──▶ ai_gateway.audit_log    │ │
                           └──────────────────────────────────────────┘ │
                                                                         │
   ┌──────────────────────────────────────────────────────┐             │
   │ dashboard/ (Next.js 15, port 3001)                   │             │
   │  Better Auth /login gate (middleware.ts)             │             │
   │  /api/gateway/* server proxy ── adds X-Admin-Key ─────│─────────────┘
   │  React Query 7s poll → KPI row / FSM panel /          │
   │  latency chart / tenant table / audit table /         │
   │  sticky critical banner                               │
   └──────────────────────────────────────────────────────┘
```

### Severity → channel matrix (07-CONTEXT.md)

The alerter's `severityFor()` (`gateway/internal/alert/severity.go`) is a
pure transform — no I/O — that classifies one raw Pub/Sub event into a
tier + a channel-agnostic `Message`:

| Tier         | Channels                              | Example trigger                                                  |
| ------------ | ------------------------------------- | ---------------------------------------------------------------- |
| **critical** | Chatwoot (WhatsApp) + ClickUp + Brevo | `local-llm` breaker → OPEN; FSM → `FAILED_OVER` / `EMERGENCY_*`   |
| **warning**  | ClickUp + Brevo (no WhatsApp)         | a fallback upstream (openrouter/openai) breaker trip; shed ARMED  |
| **info**     | none — dashboard banner / log only    | recovery transitions, informational FSM moves                    |

`local-llm` is `primaryLLMUpstream` — a breaker/shed event for it is
critical-class; the same event for a fallback upstream is warning-class
(the fallback chain doing its job is not a page).

### Graceful-degradation rule (the optional-feature pattern)

Every alert channel is an **optional feature**, mirroring the
`SENTRY_DSN` precedent. `buildAlertChannels` (`gateway/cmd/gateway/main.go`,
plan 07-06) checks each channel's required env vars:

- **Chatwoot** enabled only when `CHATWOOT_API_TOKEN` + `CHATWOOT_API_URL`
  + `CHATWOOT_ONCALL_ACCOUNT_ID` are all set.
- **ClickUp** enabled only when `CLICKUP_API_TOKEN` + `CLICKUP_ALERT_LIST_ID`
  are both set.
- **Brevo** enabled only when `BREVO_SMTP_HOST` + `BREVO_SMTP_USER` +
  `BREVO_SMTP_PASS` + `ALERT_EMAIL_FROM` + a non-empty `ALERT_EMAIL_TO`
  are all set.

A missing or partial config logs a single `WARN` naming the **first
missing env var** (e.g. `chatwoot alert channel disabled — CHATWOOT_API_TOKEN unset`)
and the channel is **skipped — never half-built, never fail-boot**. With
all 12 alert vars unset, the alerter still runs (classify + dedup + log);
the external fan-out slice is just empty. The system degrades to
**"log + dashboard banner only"** — the dashboard critical banner is
driven by the FSM state in `/admin/metrics`, not by the alert channels,
so it surfaces a critical incident even with zero channels configured.

### Dedup (`gw:alert:dedup:` — OBS-06)

Every classified event carries a **stable fingerprint** of the form
`<source>:<key>:<state>` (e.g. `breaker:local-llm:open`) — deterministic
for the same logical incident, and **timestamp-free** so a flapping
breaker re-tripping produces the same fingerprint. Before fan-out, the
alerter does a Redis `SET NX EX 300` on `gw:alert:dedup:<fingerprint>`
(`gateway/internal/alert/dedup.go`, `dedupTTL = 5 * time.Minute`):

- **First** occurrence in the 5-minute window → key is set → fan out.
- **Repeat** fingerprint inside the window → key already exists → suppressed.

So a warning event repeating ten times in five minutes produces
**exactly one** notification per channel. Fail-open for critical (a Redis
error on a critical fingerprint still sends — better a duplicate page
than a missed one); fail-closed for warning.

### EARLY goroutine spawn (Pitfall 4 — at-most-once Pub/Sub)

Redis Pub/Sub has no replay: a message published with no subscriber is
gone. `go alerter.Run(ctx)` is therefore spawned **textually before**
every event-publishing subsystem in `main.go` (before
`go breakerSet.Subscribe(ctx)` and `go emergReconciler.Run(ctx)`), and
`alerter.ReconcileBoot(ctx)` runs at startup to read the current FSM /
breaker state out of the Redis mirror so a gateway restart **during** an
active incident still surfaces the banner.

---

## The Dashboard

### What it is

`dashboard/` is a Next.js 15 (App Router) operator dashboard, a separate
service in the same Portainer stack, listening on port **3001**. It is
the OBS-03 deliverable. It **never** talks to the gateway directly and
**never** holds the admin key in the browser — every gateway call goes
through its own `/api/gateway/*` server-side proxy which injects
`X-Admin-Key` server-side only.

### Login

Auth is a **standalone Better Auth** instance (email/password, ~4 Ifix
admins), configured following the converseai-v4 pattern. Better Auth has
its **own** isolated Postgres schema/DB (`DASHBOARD_DATABASE_URL`),
**never** the gateway's `ai_gateway` schema (Pitfall 7). `middleware.ts`
is the session gate: an unauthenticated request to any `(dashboard)`
route redirects to `/login`. SSO is deferred to Phase 10 (PRD-06).

### What each view shows

- **Overview (`/`)** — a KPI row (P95 / error rate / requests), the **FSM
  panel**, and a 3-series Recharts latency chart (P50 green / P95 amber /
  P99 red). The "Atualizado há {n}s" stale indicator next to the title
  confirms the poll cadence.
- **Tenants (`/tenants`)** — a per-tenant metrics table (36px rows,
  status-colored error-rate badge) + a date-range cost filter that drives
  `fetchUsage` for the per-tenant cost summary.
- **Incidents (`/incidents`)** — the `audit_log` / incident-history
  table, newest-first, with a limit/offset pager. This is where
  `fsm_transition`, tenant activate/deactivate, and threshold-change rows
  surface.

### How to read the FSM panel + the critical banner

- The **FSM panel** renders the Phase 6 emergency FSM state (the 7-state
  machine from `RUNBOOK-EMERGENCY-POD.md`) as a status-colored badge with
  a pt-BR label. `HEALTHY` = green; `DEGRADED` / `RECOVERING` / `COOLDOWN`
  = amber; `FAILED_OVER` / `EMERGENCY_PROVISIONING` / `EMERGENCY_ACTIVE`
  = red. The state→label→tier map is centralized in
  `dashboard/src/lib/fsm.ts`.
- The **sticky critical banner** is the dashboard's primary focal point:
  a 44px red (`--destructive`) banner on a critical FSM state, amber
  (`--status-warning`) on a degraded state, hidden on `HEALTHY`. The
  "Reconhecer incidente" button silences it **locally** for 5 minutes
  (mirroring the alert dedup TTL) — it is **read-only local state**, zero
  gateway write. The banner is fed by the FSM state in `/admin/metrics`,
  so it shows even when every alert channel is disabled.

### Polling model

One React Query provider sets `refetchInterval: 7000ms` (inside the
5–10s UI-SPEC band) + `refetchOnWindowFocus`. Every `useQuery` inherits
it, and the banner + the Overview/Tenants pages share the `['metrics']`
queryKey so the 7s poll dedupes to a single network request.

---

## `/metrics` — Prometheus + the Cardinality Audit (OBS-02)

### The endpoint

`r.Handle("/metrics", obs.Handler())` is mounted **unauthenticated** in
`main.go` (the standard Prometheus convention — scrape target, no admin
key). It is distinct from the admin-key-gated `/admin/metrics` JSON.

### The ≤10k-active-series budget

Phase 7's hard budget is **≤10k active series** total, consumable by
standard Prometheus tooling. The risk (Pitfall 1) is histograms:
histograms multiply — every label-value combination gets a full set of
bucket series. The mitigation already in the code:

- The two Phase 7 latency histograms are **deliberately narrow**:
  `gateway_request_duration_ms_by_route` (labelled by `route` only,
  ~4 values) and `gateway_request_duration_ms_by_upstream` (labelled by
  `upstream` only, ~6 values) — **never** crossed on one histogram.
- Per-tenant P50/P95/P99 is computed in **Postgres** (`percentile_cont`
  over `audit_log.latency_ms` grouped by `tenant_id`) and served via the
  `/admin/metrics` JSON — **not** via a Prometheus label. Postgres
  percentiles cost zero cardinality.
- No unbounded label anywhere (`request_id` is never a label).

### The cardinality audit procedure

Run this on the deployed gateway whenever `/metrics` series count is in
question, or as the periodic OBS-02 check:

```bash
# 1. Raw exposition + promtool sanity check (naming, types, dup series)
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics' \
  | promtool check metrics

# 2. Total active series count — the headline number against the 10k budget
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics' \
  | grep -vE '^#' | grep -c .

# 3. Series count per metric name — finds the offender if the total is climbing
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics' \
  | grep -vE '^#' | sed -E 's/\{.*//' | sort | uniq -c | sort -rn | head -20
```

If you have a Prometheus instance scraping the gateway, the equivalent
PromQL is `count by (__name__)({__name__=~"gateway_.*"})` and
`count(group({__name__=~"gateway_.*"})) by (__name__)`.

**Pass:** step 2 returns well under 10000; `promtool check metrics`
exits 0 (consumable by standard tooling).
**Fail / investigate:** step 2 ≥ 10000, or step 3 shows one
`gateway_*` metric with hundreds+ of series — that metric has an
unbounded or crossed label set; see "Known Failure Modes → /metrics
series climbing".

---

## `/admin/metrics` + `/admin/audit` — the Admin-Key-Gated JSON

Both routes are mounted **only** inside the `if px.adminVerifier != nil`
block in `main.go`, behind the same Phase 4 `admin.Middleware`
(`X-Admin-Key`, bcrypt-verified) as `/admin/usage`. The dashboard's
server proxy is the normal caller; to curl them directly for diagnosis:

```bash
# /admin/metrics — P50/P95/P99 per route + upstream, error rate, inflight,
# saturation, current FSM state (the dashboard's data source)
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s \
  -H "X-Admin-Key: $GATEWAY_ADMIN_KEY" \
  http://localhost:8080/admin/metrics' | jq

# /admin/audit — paginated audit_log feed, newest-first; limit is capped
# server-side, metadata-only columns (no prompt/response bodies)
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s \
  -H "X-Admin-Key: $GATEWAY_ADMIN_KEY" \
  "http://localhost:8080/admin/audit?limit=50&offset=0"' | jq

# Filter the audit feed to FSM transitions only
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s \
  -H "X-Admin-Key: $GATEWAY_ADMIN_KEY" \
  "http://localhost:8080/admin/audit?limit=50"' \
  | jq '[.[] | select(.event_kind == "fsm_transition")]'
```

A `401`/`403` means the `X-Admin-Key` is wrong or unset — check
`GATEWAY_ADMIN_KEY` in the Portainer stack env. A `200` with `[]` from
`/admin/audit` is normal on a fresh gateway with no state changes yet.

---

## Sentry — What Gets Captured, What Gets Redacted (OBS-08)

### What gets captured

Sentry (`gateway/internal/obs/sentry.go`, enabled when `SENTRY_DSN` is
set — empty = Sentry disabled, no fail-boot) captures: gateway panics,
circuit-breaker trips, and emergency-provisioning failures. Phase 6
terminal-state events carry `subsystem:emerg` breadcrumbs (see
`RUNBOOK-EMERGENCY-POD.md` Sentry Forensics).

### What gets redacted (the OBS-08 guarantee)

The `BeforeSend` hook scrubs, **before the event leaves the process**:

- `event.Request.Headers` — `authorization` and `x-api-key` are replaced
  with `***REDACTED***` (Phase 2 behaviour).
- `event.Request.Cookies` — cleared.
- `event.Request.Data` — the request **body**. Phase 7 extended
  `BeforeSend` here (Pitfall 2): a panic that captured a chat request
  would otherwise carry the full prompt JSON. For `data_class=sensitive`
  tenants the body is dropped entirely; otherwise known sensitive JSON
  keys are scrubbed via the existing `httpx.IsSensitiveKey` /
  `sensitiveKeys` map. Response bodies and `event.Extra` are scrubbed the
  same way.

The S5 scenario in `07-HUMAN-UAT.md` actively verifies this live: trigger
a captured event, confirm `authorization` + `x-api-key` headers and any
request/response body show `***REDACTED***` in the Sentry UI. The unit
guard is the `BeforeSend` test that constructs a `sentry.Event` with a
populated `Request.Data` and asserts the secret string is absent
post-redaction.

---

## Known Failure Modes

Drawn from `07-RESEARCH.md` Common Pitfalls. Each is a "this is expected,
here is the signal, here is the action" entry.

### Boot-window lost events (Pitfall 4)

**What:** Redis Pub/Sub is at-most-once. A breaker/FSM transition that
fires in the gap **before** `go alerter.Run(ctx)` is subscribed is
silently lost — including a critical one.
**Why it should not happen:** `go alerter.Run(ctx)` is spawned textually
before every publisher in `main.go`, and `ReconcileBoot` replays the
current Redis-mirrored state at startup.
**Signal:** an alert that "should have fired" during a deploy window
never arrived; the dashboard banner is correct (it reads state, not
events) but no WhatsApp/email landed.
**Action:** confirm the spawn ordering is intact —
`grep -n 'go .*[Aa]lerter\|go breakerSet.Subscribe\|go emergReconciler.Run' gateway/cmd/gateway/main.go`
— the alerter spawn line number must be **lower** than both publisher
lines. If a deploy raced, the incident is still visible on the dashboard;
re-trigger the alert manually or wait for the next transition. If the
ordering regressed, that is a code bug — file it against `main.go`.

### Alerter stall on a dead external API (Pitfall 5 → `gateway_alert_dropped_total`)

**What:** Chatwoot (or ClickUp/Brevo) is down; a synchronous send blocks
the consumer; the per-channel worker queue backs up; events are dropped.
**Why it is bounded:** the consume loop only classifies + dedups +
enqueues to a **bounded per-channel worker**; each external client has
its own `gobreaker` so a dead Chatwoot fails fast instead of timing out.
A full worker queue increments `obs.AlertDroppedTotal` instead of
blocking the loop.
**Signal:** `gateway_alert_dropped_total` is non-zero (Prometheus, or
`/metrics` grep). A non-zero value means the gateway is mid-incident AND
producing alerts faster than a channel can drain — usually because that
channel's external API is slow/down.
**Action:**
```bash
# Is the counter moving?
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics | grep gateway_alert_dropped_total'
# Which channel's breaker is open? (alerter logs)
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 15m 2>&1 | grep -iE "alert|chatwoot|clickup|brevo|breaker"'
```
Check the upstream provider's status (Chatwoot / ClickUp / Brevo). The
drop is a fail-safe, not data loss of record — the FSM state and the
`audit_log` row still exist; only the *notification* was shed. Once the
external API recovers, the breaker closes and fan-out resumes.

### ClickUp 401 (Pitfall 6)

**What:** ClickUp tokens are static; a bad/expired token returns `401`. A
naive retry loop would retry the 401 forever.
**Why it is bounded:** the ClickUp client classifies like cobrancas-api's
`withRetry` — 4xx-except-429 is `backoff.Permanent(err)` (stop
immediately, no retry storm); 429 honours `X-RateLimit-Reset`; 5xx +
network retry with backoff.
**Signal:** repeated `401` in the alerter logs; alert tasks never appear
in the target ClickUp list, but no retry storm.
**Action:** the `CLICKUP_API_TOKEN` is bad or revoked. Get a fresh token
from the Ifix ClickUp admin, update it in the Portainer stack env,
redeploy. Confirm `CLICKUP_ALERT_LIST_ID` still points at a list the
token can write to.

### `audit_log` partition-window limitation (Pitfall 8)

**What:** `audit_log` is partitioned by month; partitions are seeded for
the current + next 2 months (migration 0003). The partition-roll
automation (`gatewayctl`, plan 02-09) was **deferred**. Phase 7
**increases** audit write volume (`fsm_transition` rows on top of
per-request rows). If audit rows are written past the seeded window with
no matching partition, the **insert fails**.
**Signal:** gateway logs show
`ERROR: no partition of relation "audit_log" found for row`; the
`/admin/audit` feed and the dashboard Incidents page stop gaining new
rows.
**Action:** this is a **known limitation**, not a Phase 7 regression.
Create the missing partition manually:
```sql
-- substitute the month that has no partition (e.g. 2026-08)
CREATE TABLE IF NOT EXISTS ai_gateway.audit_log_2026_08
  PARTITION OF ai_gateway.audit_log
  FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
```
Then file/track the partition-roll automation (deferred 02-09) — the
deferred 02-09 *cold-storage export* is out of Phase 7 scope, but
partition *creation* is a separate, real operational need.

---

## Incident Playbook — Detection → Diagnosis → Action

### Alerts not arriving

**Detection:** a critical/warning event clearly happened (the dashboard
FSM panel shows `FAILED_OVER`, or `RUNBOOK-FAILOVER` shows a breaker
OPEN), but no WhatsApp message, no email, and no ClickUp task.

**Diagnosis:**

```bash
# 1. Are the channels even enabled? Look for the disabled-channel WARNs at boot.
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 24h 2>&1 | grep -i "alert channel disabled"'

# 2. Is the alerter dropping events? (channel down / queue full)
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics | grep gateway_alert_dropped_total'

# 3. Did dedup suppress it? (a repeat fingerprint inside the 5-min window)
ssh vps-ifix-vm 'docker exec infra-redis-1 redis-cli -n 5 KEYS "gw:alert:dedup:*"'

# 4. Alerter activity in the incident window
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 30m 2>&1 | grep -iE "alert|chatwoot|clickup|brevo"'
```

**Action:**

- **Step 1 shows a disabled-channel WARN** → that channel's env vars are
  missing/partial in the Portainer stack. This is the graceful-degradation
  rule working as designed — the system is in "log + dashboard banner
  only" mode for that channel. To enable it, set the channel's required
  env vars (see "Graceful-degradation rule" above) and redeploy. The
  build was never blocked; this is the documented `passed_partial` path.
- **Step 2 non-zero** → see "Known Failure Modes → Alerter stall".
- **Step 3 shows a matching `gw:alert:dedup:` key** → the alert WAS sent
  once; this is dedup working (OBS-06). A repeated warning within 5 min
  is **supposed** to produce exactly one notification. Not a defect.
- **Steps 1–3 clean, step 4 shows the alerter classified the event but a
  send failed** → check the specific channel's `gobreaker` state and the
  external provider's status (Chatwoot/ClickUp/Brevo). For ClickUp 401
  specifically see "Known Failure Modes → ClickUp 401".
- **Step 4 shows no alerter activity at all for an event that fired** →
  suspect the boot-window race (Pitfall 4); confirm the spawn ordering.

### Stale dashboard

**Detection:** the dashboard "Atualizado há {n}s" indicator climbs past
~15s without resetting, or the KPI numbers / FSM panel are visibly old.

**Diagnosis:**

```bash
# 1. Is the dashboard service up?
ssh vps-ifix-vm 'docker ps --filter name=dashboard --format "{{.Names}} {{.Status}}"'

# 2. Can the dashboard's server proxy reach the gateway? (from inside the dashboard container)
ssh vps-ifix-vm 'docker exec <dashboard-container> curl -s -o /dev/null -w "%{http_code}\n" \
  -H "X-Admin-Key: $GATEWAY_ADMIN_KEY" http://gateway:8080/admin/metrics'

# 3. Is the gateway's /admin/metrics actually serving?
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s -o /dev/null -w "%{http_code}\n" \
  -H "X-Admin-Key: $GATEWAY_ADMIN_KEY" http://localhost:8080/admin/metrics'

# 4. Dashboard logs
ssh vps-ifix-vm 'docker logs <dashboard-container> --since 15m 2>&1 | tail -50'
```

**Action:**

- **Step 1 dashboard down** → restart the dashboard service via Portainer;
  check the boot logs for a Better Auth DB connection error (Pitfall 7 —
  it must point at its OWN `DASHBOARD_DATABASE_URL`, not `ai_gateway`).
- **Step 2 returns 401/403** → `GATEWAY_ADMIN_KEY` mismatch between the
  dashboard service env and the gateway service env in the Portainer
  stack — align them and redeploy.
- **Step 2 returns a connection error but step 3 is 200** → the
  dashboard's `GATEWAY_BASE_URL` is wrong (should point at the gateway
  service name on the shared Docker network, e.g. `http://gateway:8080`).
- **Step 3 non-200** → the problem is the gateway, not the dashboard —
  the dashboard is correctly showing the last good data. Diagnose the
  gateway (`RUNBOOK-EMERGENCY-POD` / `RUNBOOK-FAILOVER`).
- **All steps green but the indicator still climbs** → a client-side
  React Query issue; a hard browser refresh re-establishes the 7s poll.

### /metrics series climbing

**Detection:** the cardinality audit (above) step 2 returns a number
trending toward 10000, or `promtool check metrics` output is growing.

**Diagnosis:** run step 3 of the cardinality audit — series count per
metric name. One `gateway_*` metric with hundreds+ of series is the
offender.

**Action:**

- If the offender is a **histogram** with a crossed label set
  (`tenant × route × upstream` on one histogram) → that violates
  Pitfall 1. The fix is code: split it into narrow single-label
  histograms (the pattern the two Phase 7 histograms already follow), or
  move the per-tenant breakdown to the `/admin/metrics` Postgres-computed
  JSON. File against `gateway/internal/obs/metrics.go`.
- If the offender has an **unbounded label** (something per-request like
  a request ID, a path with IDs in it, a free-text error string) → that
  label must be removed or bucketed. Code fix, file against
  `metrics.go` / the middleware that records it.
- This is **not** a runtime mitigation you apply on the box — it is a
  build-time cardinality discipline. The runbook's job is detection; the
  remediation is a code change reviewed against the 10k budget.

### Sentry leaking a secret

**Detection:** a Sentry event in the `ifix-ai-gateway` (or `-dev`)
project shows an `authorization` / `x-api-key` header value, a cookie,
or a request/response body containing a token or a prompt — anything
that is **not** `***REDACTED***`.

**Diagnosis:**

```bash
# Confirm SENTRY_DSN is set (Sentry enabled) and find the leaking event class
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 1h 2>&1 | grep -i sentry'
# Re-run the BeforeSend unit guard locally
cd gateway && go test ./internal/obs/ -count=1 -run TestSentry
```

**Action:**

- **This is a security incident (T-07-33 / OBS-08).** Treat the leaked
  value as compromised: rotate the leaked credential immediately (the
  relevant API token / admin key — see CLAUDE.md token store + Portainer
  stack), then redeploy.
- The leak means `BeforeSend` (`gateway/internal/obs/sentry.go`) did not
  cover the field that carried it. Identify the field
  (`Request.Headers` / `Request.Cookies` / `Request.Data` / a response
  body / `event.Extra` / a breadcrumb) and extend `BeforeSend` to scrub
  it — reuse `httpx.IsSensitiveKey` / the `sensitiveKeys` map. Add a unit
  test that constructs the event shape and asserts the secret string is
  absent post-redaction. File and fast-track the fix.
- In Sentry itself, delete the leaking event(s) from the project so the
  secret is not retained in Sentry's storage.
- **Never** commit any secret value to git or to this runbook — the
  runbook and `07-HUMAN-UAT.md` reference env var **names** only
  (T-07-33).

---

## Deploy

### Pre-deploy checklist

1. CI build green: <https://github.com/IfixTelecom/gpu-ifix/actions> —
   the `build-gateway` workflow AND the `build-dashboard` workflow on
   `develop` (dev) or `main` (prod).
2. Migration `gateway/db/migrations/0020_audit_log_event_kind.sql` is
   committed and present in the gateway image (additive nullable
   `event_kind` column — runs via `AI_GATEWAY_MIGRATE_ON_BOOT=true`).
3. The dashboard's Better Auth schema is migrated against its **own**
   `DASHBOARD_DATABASE_URL` (`npx @better-auth/cli migrate`), isolated
   from `ai_gateway`.

### Deploy via Portainer

1. Open Portainer: <https://portainer3.ifixtelecom.com.br>.
2. Stacks → `ai-gateway-dev` (or `ai-gateway-prod`) → Editor.
3. Add/update the **12 alert env vars + `SENTRY_DSN`** on the **gateway**
   service. Every one is **optional** — an unset var disables its channel
   with a `WARN`, never fails boot:

   | Env var (gateway service)    | Channel  | Required-together group                                          |
   | ---------------------------- | -------- | ---------------------------------------------------------------- |
   | `CHATWOOT_API_URL`           | Chatwoot | Chatwoot needs URL + token + on-call account ID all set          |
   | `CHATWOOT_API_TOKEN`         | Chatwoot |                                                                  |
   | `CHATWOOT_ONCALL_ACCOUNT_ID` | Chatwoot |                                                                  |
   | `CHATWOOT_ONCALL_INBOX_ID`   | Chatwoot | (routing detail; obtained from the Ifix Chatwoot admin)          |
   | `CHATWOOT_ONCALL_CONTACT_ID` | Chatwoot | (routing detail; the on-call operator contact)                   |
   | `CLICKUP_API_TOKEN`          | ClickUp  | ClickUp needs token + alert list ID both set                     |
   | `CLICKUP_ALERT_LIST_ID`      | ClickUp  |                                                                  |
   | `BREVO_SMTP_HOST`            | Brevo    | Brevo needs host + user + pass + from + at-least-one to-address  |
   | `BREVO_SMTP_PORT`            | Brevo    | (default `587` if unset)                                         |
   | `BREVO_SMTP_USER`            | Brevo    |                                                                  |
   | `BREVO_SMTP_PASS`            | Brevo    |                                                                  |
   | `ALERT_EMAIL_FROM`           | Brevo    |                                                                  |
   | `ALERT_EMAIL_TO`             | Brevo    | comma-separated; empty = email channel disabled                  |
   | `SENTRY_DSN`                 | Sentry   | empty = Sentry disabled (Phase 2 var)                            |

   The actual values come from the Ifix Chatwoot/ClickUp admins + the
   standard Ifix Brevo SMTP creds — they live in the Portainer stack UI,
   **never** in git (CLAUDE.md rule). See `07-HUMAN-UAT.md` Prerequisites
   for how the operator obtains them.

4. Add the **dashboard service env vars** on the `dashboard` service:
   `BETTER_AUTH_SECRET`, `BETTER_AUTH_URL`, `GATEWAY_ADMIN_KEY` (must
   match the gateway's admin key), `GATEWAY_BASE_URL` (the gateway
   service name on the shared network, e.g. `http://gateway:8080`),
   `DASHBOARD_DATABASE_URL` (the dashboard's OWN isolated DB), `PORT=3001`.
5. Hit **Update the stack** → webhook → Portainer pulls the new gateway +
   dashboard images.
6. Watch container creation:
   `ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev'`.

### Post-deploy checklist

- [ ] **Gateway container running:**
      `ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev_gateway --format "{{.Status}}"'`
      shows `Up N seconds (healthy)`.
- [ ] **Dashboard container running:**
      `ssh vps-ifix-vm 'docker ps --filter name=dashboard --format "{{.Status}}"'`.
- [ ] **Alerter spawned + channel status logged at boot:**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep -iE "alert channel|alerter"'`
      — each configured channel is silent; each unconfigured channel logs
      one `... alert channel disabled — <VAR> unset` WARN. This is
      expected and is how you confirm the graceful-degradation rule.
- [ ] **Migration 0020 applied:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "\d ai_gateway.audit_log"'`
      shows the `event_kind` column.
- [ ] **`/metrics` exposed + under budget:** run the cardinality audit
      above — `promtool check metrics` exits 0, total series well under 10k.
- [ ] **`/admin/metrics` + `/admin/audit` gated:** an unauthenticated
      `curl http://localhost:8080/admin/metrics` returns `401`/`403`; with
      `-H "X-Admin-Key: $GATEWAY_ADMIN_KEY"` it returns `200` + JSON.
- [ ] **Dashboard auth gate works:** visiting the dashboard URL
      unauthenticated redirects to `/login`.
- [ ] **Sentry quiet at idle:** with `SENTRY_DSN` set, no events appear
      until a real panic / breaker trip / provisioning failure.

### Auto-prereq if alerting is disabled by design

To deploy with **no alert channels** (the all-degraded mode): leave all
12 alert env vars unset. Boot logs show three disabled-channel WARNs; the
alerter still runs (classify + dedup + log); the dashboard critical
banner still works (it reads FSM state, not alert events). This is a
fully supported configuration — the autonomous build (07-01..07-08) is
green with zero alert credentials.

---

## Rollback

To **disable alerting** without rolling back the gateway image:

1. In the Portainer stack, clear the 12 alert env vars on the gateway
   service (leaving `SENTRY_DSN` is fine — Sentry is orthogonal).
2. Hit "Update the stack" → webhook redeploys.
3. Verify the boot logs show the three disabled-channel WARNs and the
   alerter still running. The system is now in "log + dashboard banner
   only" mode.

To **fully revert** to a pre-Phase-7 gateway image:

1. In Portainer, set the gateway stack image tag to the pre-Phase-7
   `develop-<sha>` build.
2. Hit "Update the stack" → webhook redeploys with the old image.
3. Migration `0020_audit_log_event_kind.sql` is **additive and nullable**
   — it does not need to be reverted; the unused column is harmless. If
   you must revert the schema:
   `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway goose -dir db/migrations postgres "$AI_GATEWAY_PG_DSN" down'`.
4. To remove the dashboard, delete the `dashboard` service block from the
   stack and redeploy — it is independent of the gateway.

---

## References

- Phase 7 Context (PRD): [`.planning/phases/07-observability-dashboard-alerting/07-CONTEXT.md`](../../.planning/phases/07-observability-dashboard-alerting/07-CONTEXT.md)
- Phase 7 Research (Pitfalls, env inventory): [`.planning/phases/07-observability-dashboard-alerting/07-RESEARCH.md`](../../.planning/phases/07-observability-dashboard-alerting/07-RESEARCH.md)
- Phase 7 Validation matrix: [`.planning/phases/07-observability-dashboard-alerting/07-VALIDATION.md`](../../.planning/phases/07-observability-dashboard-alerting/07-VALIDATION.md)
- HUMAN-UAT scenarios: [`.planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md`](../../.planning/phases/07-observability-dashboard-alerting/07-HUMAN-UAT.md)
- UI spec: [`.planning/phases/07-observability-dashboard-alerting/07-UI-SPEC.md`](../../.planning/phases/07-observability-dashboard-alerting/07-UI-SPEC.md)
- Composition-root wiring summary: [`.planning/phases/07-observability-dashboard-alerting/07-06-SUMMARY.md`](../../.planning/phases/07-observability-dashboard-alerting/07-06-SUMMARY.md)
- Dashboard UI summary: [`.planning/phases/07-observability-dashboard-alerting/07-08-SUMMARY.md`](../../.planning/phases/07-observability-dashboard-alerting/07-08-SUMMARY.md)
- Sibling runbooks: [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md), [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md), [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md)
- Alerting code: `gateway/internal/alert/` (`alerter.go`, `severity.go`, `dedup.go`, `chatwoot.go`, `clickup.go`, `brevo.go`)
- Admin JSON code: `gateway/internal/admin/` (`metrics.go`, `audit.go`)
- Metrics + Sentry code: `gateway/internal/obs/` (`metrics.go`, `sentry.go`, `middleware.go`)
- Dashboard: `dashboard/` (Next.js 15, port 3001)
