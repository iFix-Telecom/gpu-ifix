---
status: partial
phase: 07-observability-dashboard-alerting
source: [07-VALIDATION.md Manual-Only Verifications, 07-09-PLAN.md Task 3, 07-CONTEXT.md, 07-RESEARCH.md Open Questions 1-4]
started: 2026-05-14
updated: 2026-05-14
operator: ___________
date_executed: ___________
final_status: pending  # passed | passed_partial | human_needed
---

# Phase 7 — Human UAT (LIVE Observability & Alerting)

This document drives the operator-only UAT for Phase 7 (Observability —
Dashboard & Alerting). It is the live-credential and live-delivery
verification that **cannot** run autonomously — mirroring the deferred-UAT
pattern every prior phase used (`03-08` / `04-09` / `06-11`).

The autonomous plans `07-01..07-08` build the whole subsystem and the
test suites are green. The gateway's alert channels **degrade gracefully**
to "log + dashboard banner only" when credentials are absent (the
optional-feature pattern) — so the autonomous build is **NOT blocked** by
missing credentials. This plan documents and gates the success criteria
that need real Chatwoot/ClickUp/Brevo credentials, a real on-call routing
target, a deployed gateway + dashboard, and Sentry: **SC-2** (WhatsApp +
email + ClickUp + banner within 60s), **SC-3** (live dedup), **SC-5**
(Prometheus cardinality under 10k), **SC-6** (Sentry redaction), plus
**SC-1** (the live dashboard).

The companion runbook is
[`gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md`](../../../gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md)
— read its **Deploy** section before running any scenario below.

---

## Prerequisites

Verify every row is satisfied **before** starting S1. These resolve
`07-RESEARCH.md` Open Questions 1-4 (the Chatwoot/ClickUp/Brevo/Better-Auth
provisioning unknowns) and set the 12 alert env vars + `SENTRY_DSN` on the
deployed gateway.

### Open Questions 1-4 — resolve first

- [ ] **OQ-1 / A6 (Chatwoot on-call routing — HIGHEST RISK).** Obtain from
      the Ifix Chatwoot admin a designated **on-call operator** mapping:
      `CHATWOOT_ONCALL_ACCOUNT_ID`, `CHATWOOT_ONCALL_INBOX_ID`,
      `CHATWOOT_ONCALL_CONTACT_ID`, plus `CHATWOOT_API_URL` +
      `CHATWOOT_API_TOKEN`. **A6 is the single biggest external-dependency
      unknown** — if no on-call contact/inbox exists, the WhatsApp-alert
      path has nowhere to send. If it cannot be provisioned, S1's WhatsApp
      leg is `passed_partial` (the email + ClickUp + banner legs can still
      pass).
- [ ] **OQ-2 (ClickUp target).** Obtain from the Ifix team a
      `CLICKUP_API_TOKEN` (a service token, not a personal one) and a
      target `CLICKUP_ALERT_LIST_ID` — a single list for both critical and
      warning tasks in v1.
- [ ] **OQ-3 (Brevo SMTP).** Use the standard Ifix Brevo SMTP credentials:
      `BREVO_SMTP_HOST`, `BREVO_SMTP_PORT` (default `587`),
      `BREVO_SMTP_USER`, `BREVO_SMTP_PASS`, `ALERT_EMAIL_FROM`, and a
      comma-separated `ALERT_EMAIL_TO` (the on-call distribution).
- [ ] **OQ-4 (Better Auth storage).** Provision the dashboard's **own**
      isolated Postgres schema/DB (`DASHBOARD_DATABASE_URL`) — **never**
      the gateway's `ai_gateway` schema (07-RESEARCH Pitfall 7). Run
      `npx @better-auth/cli migrate` against it.

### Deploy state

- [ ] **Gateway alert env vars set in the Portainer stack** (gateway
      service): all 12 alert vars above + `SENTRY_DSN`. Each is optional —
      an unset var disables its channel with a `WARN`, never fails boot.
- [ ] **Dashboard service deployed** in the same Portainer stack with
      `BETTER_AUTH_SECRET`, `BETTER_AUTH_URL`, `GATEWAY_ADMIN_KEY` (must
      match the gateway's admin key), `GATEWAY_BASE_URL`
      (e.g. `http://gateway:8080`), `DASHBOARD_DATABASE_URL`, `PORT=3001`.
- [ ] **Better Auth operator accounts created** — the ~4 Ifix admin
      email/password accounts that will sign in to the dashboard.
- [ ] **Boot logs confirm the alerter + channel status:**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep -iE "alert channel|alerter"'`
      — each **configured** channel is silent; each **unconfigured**
      channel logs exactly one `... alert channel disabled — <VAR> unset`
      WARN. (This is how you confirm the graceful-degradation rule and
      which scenarios will be `passed_partial`.)
- [ ] **Migration 0020 applied:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "\d ai_gateway.audit_log"'`
      shows the `event_kind` column.
- [ ] **`promtool` available** on the box (or a Prometheus instance
      scraping the gateway) for S4.
- [ ] **Sentry project ready** — `ifix-ai-gateway` (or `-dev`), DSN in
      `SENTRY_DSN`, dashboard accessible.

> **Container name note:** the compose file does not set `container_name`,
> so the Portainer stack-prefixed name is `ai-gateway-dev_gateway` (or
> `ai-gateway-dev-gateway-1` depending on Compose version). Substitute the
> actual name from `ssh vps-ifix-vm 'docker ps | grep gateway'` and the
> dashboard name from `... | grep dashboard` if the commands below 404.

---

## Current Test

[awaiting human testing — operator runs S1-S6 sequentially below]

---

## Scenarios

### S1 — Critical event → WhatsApp + email + dashboard banner within 60s (SC-2, OBS-04/OBS-05)

**Pre-conditions:** gateway + dashboard deployed; Chatwoot + Brevo channels
enabled (boot logs show NO disabled-channel WARN for them); dashboard FSM
panel at `HEALTHY`.

**Setup:** open the dashboard in a browser, sign in, stay on the Overview.

**Action:**

```bash
ssh vps-ifix-vm

# Capture T0
T0=$(date -u +"%Y-%m-%dT%H:%M:%SZ"); echo "T0=$T0"

# Induce a critical event — force the local-llm breaker OPEN (or stop the
# local LLM upstream so the breaker trips on its own). force-provision is
# the cleanest deterministic trigger: it drives the FSM to FAILED_OVER /
# EMERGENCY_PROVISIONING which severityFor() classifies critical.
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "phase7_uat_s1"
```

**Expected:**

- Within **60s** of `T0`: the **on-call operator's WhatsApp** receives a
  Chatwoot message describing the critical event.
- Within **60s** of `T0`: an **email** arrives at `ALERT_EMAIL_TO` via
  Brevo describing the same event.
- The **dashboard sticky critical banner** turns red (the FSM panel shows
  `FAILED_OVER` / `EMERGENCY_*`) — the banner appears even if a channel is
  disabled, because it reads FSM state, not alert events.

**Pass/Fail:**

- PASS: WhatsApp + email + banner all present within 60s.
- `passed_partial`: a credential leg is genuinely unavailable (e.g. A6
  on-call contact not provisioned) — record which leg and the blocker;
  the other legs can still PASS.
- FAIL: a leg whose channel IS configured did not deliver within 60s, or
  the banner never appeared — a real defect.

**Cleanup:** S2 reads the same event; tear down after S2 with
`docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy`.

result: [pending]

---

### S2 — Same critical event opened a ClickUp task (SC-2 cont., OBS-04/OBS-05)

**Pre-conditions:** S1 executed; ClickUp channel enabled (no disabled-channel
WARN for it at boot).

**Setup:** open the target ClickUp list (`CLICKUP_ALERT_LIST_ID`) in a
browser.

**Action:** none — S1's critical event already fanned out to ClickUp.
Refresh the ClickUp list.

**Expected:**

- A **new task** appears in the target ClickUp list for S1's critical
  event, with a title/description identifying the incident (the same
  fingerprint S1 alerted on).

**Pass/Fail:**

- PASS: exactly one new task in the target list for the S1 event.
- `passed_partial`: ClickUp channel not configured (no token / list ID) —
  record the blocker.
- FAIL: ClickUp channel IS configured but no task appeared, OR a retry
  storm of duplicate tasks (would indicate the Pitfall 6 4xx-permanent
  classification regressed) — a real defect.

**Cleanup:** archive/delete the test task in ClickUp; then
`docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy` to
return the FSM to `HEALTHY`.

result: [pending]

---

### S3 — Warning event repeated within 5 min → exactly one notification per channel (SC-3, OBS-06)

**Pre-conditions:** FSM at `HEALTHY`; ClickUp + Brevo channels enabled
(warning tier = ClickUp + Brevo, no WhatsApp).

**Setup:** open the target ClickUp list + the `ALERT_EMAIL_TO` inbox.

**Action:**

```bash
ssh vps-ifix-vm

# Trigger the SAME warning-tier event repeatedly inside the 5-min dedup
# window. A fallback-upstream breaker flap is warning-class; the simplest
# deterministic repeat is to publish the same breaker event 5x on the
# breaker channel (substitute a real fallback upstream name, e.g. openrouter).
for i in 1 2 3 4 5; do
  docker exec infra-redis-1 redis-cli -n 5 PUBLISH gw:breaker:events \
    '{"upstream":"openrouter","state":"open","timestamp":'"$(date +%s)"'}'
  echo "published warning event $i"
  sleep 20
done
```

**Expected:**

- **Exactly one** ClickUp task for the repeated warning fingerprint
  (not five).
- **Exactly one** email for the repeated warning fingerprint (not five).
- The `gw:alert:dedup:` Redis key for that fingerprint exists for ~5 min:
  `docker exec infra-redis-1 redis-cli -n 5 KEYS "gw:alert:dedup:*"`.

**Pass/Fail:**

- PASS: one notification per channel for the five repeats; the dedup key
  is present.
- `passed_partial`: ClickUp and/or Brevo not configured — record the
  blocker; verify dedup via the Redis key alone.
- FAIL: more than one notification per channel inside the window — the
  `SET NX EX 300` dedup gate regressed — a real defect.

**Cleanup:** archive/delete the test ClickUp task; let the dedup key
expire (~5 min).

result: [pending]

---

### S4 — Prometheus /metrics under 10k active series, standard-tooling-consumable (SC-5, OBS-02)

**Pre-conditions:** gateway deployed and has served some traffic.

**Setup:** none.

**Action:**

```bash
ssh vps-ifix-vm

# 1. Raw exposition + promtool sanity check (naming, types, dup series)
docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics \
  | promtool check metrics

# 2. Total active series count — the headline number vs the 10k budget
docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics \
  | grep -vE '^#' | grep -c .

# 3. Series count per metric name — the per-metric breakdown
docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics \
  | grep -vE '^#' | sed -E 's/\{.*//' | sort | uniq -c | sort -rn | head -20
```

**Expected:**

- `promtool check metrics` **exits 0** (the exposition is consumable by
  standard Prometheus tooling).
- Step 2 returns a number **well under 10000**.
- Step 3 shows no single `gateway_*` metric with hundreds+ of series (the
  two latency histograms are deliberately narrow — `..._by_route` ~4
  values, `..._by_upstream` ~6 values; per-tenant percentiles are
  Postgres-computed in `/admin/metrics`, not Prometheus labels).

**Pass/Fail:**

- PASS: `promtool` exit 0 AND total series < 10000.
- FAIL: `promtool` non-zero, OR total series ≥ 10000, OR step 3 shows an
  unbounded/crossed-label metric — a real defect (see the runbook's
  "/metrics series climbing" playbook).

**Cleanup:** none (read-only).

result: [pending]

---

### S5 — Sentry redacts authorization / x-api-key / payload bodies (SC-6, OBS-08)

**Pre-conditions:** `SENTRY_DSN` set; Sentry project accessible.

**Setup:** open the Sentry project (`ifix-ai-gateway` or `-dev`).

**Action:**

```bash
ssh vps-ifix-vm

# Trigger a captured event. A circuit-breaker trip on a real request path
# is captured by Sentry; the S1 force-provision path also produces
# subsystem:emerg events. To exercise body redaction specifically, drive
# a request that panics or trips a breaker WHILE carrying an Authorization
# header + a JSON body (a normal /v1/chat/completions call during an
# induced upstream failure).
```

In the Sentry UI, open the most recent captured event and inspect:

1. **Request → Headers** — the `authorization` and `x-api-key` header
   values must show `***REDACTED***`, not the real token.
2. **Request → Cookies** — must be empty/cleared.
3. **Request → Data (body)** — any request body must be dropped or its
   sensitive JSON keys scrubbed; no prompt content / token in the clear.
4. **Additional Data / breadcrumbs** — no secret reflected into `Extra`
   or breadcrumb payloads.

**Expected:**

- Every sensitive field shows `***REDACTED***` (or is dropped/cleared).
- No `authorization` / `x-api-key` value, no cookie, no request/response
  body content is visible in the clear.

**Pass/Fail:**

- PASS: all four inspection points are clean.
- FAIL: any secret value or body content visible in the clear — this is a
  **security incident** (T-07-33 / OBS-08); follow the runbook's "Sentry
  leaking a secret" playbook (rotate the credential, extend `BeforeSend`,
  delete the leaking event) — a real defect.

**Cleanup:** none (read-only); delete the test event from Sentry if it
carried any sensitive data.

result: [pending]

---

### S6 — Live dashboard: per-tenant latency + cost + FSM state polling (SC-1, OBS-03)

**Pre-conditions:** dashboard deployed; Better Auth operator accounts
created; gateway serving traffic.

**Setup:** open the dashboard URL in a browser (devtools Network tab open).

**Action:**

1. Visit the dashboard URL **unauthenticated** — confirm it redirects to
   `/login`.
2. Sign in with a Better Auth operator account — confirm it lands on the
   Overview.
3. On **Overview**: confirm the KPI row (P95 / error rate / requests), the
   FSM panel (correct pt-BR label + status color), the 3-series latency
   chart, and the "Atualizado há {n}s" indicator cycling every ~5-10s.
4. On **Tenants**: confirm the per-tenant metrics table renders; pick a
   date range + "Aplicar período" and confirm the cost columns update via
   `fetchUsage`.
5. On **Incidents**: confirm the audit-log table lists state-change rows
   newest-first (including the `fsm_transition` rows from S1); walk the
   limit/offset pager.
6. In **devtools Network**: confirm every gateway call goes to
   `/api/gateway/*` and **no request carries an `X-Admin-Key` header from
   the browser**.

**Expected:**

- The auth gate redirects unauthenticated traffic to `/login`.
- All three views render live data and poll every ~5-10s.
- The per-tenant table shows latency + cost; the FSM panel shows the live
  state.
- No `X-Admin-Key` ever appears in a browser request (it is injected
  server-side only by the `/api/gateway/*` proxy).

**Pass/Fail:**

- PASS: auth gate works, all views render + poll, no `X-Admin-Key` in the
  browser.
- `passed_partial`: dashboard not yet deployed / Better Auth accounts not
  created — record the blocker.
- FAIL: a view fails to render live data, the poll does not refresh, OR an
  `X-Admin-Key` header is visible in a browser request (T-07-29 breach) —
  a real defect.

**Cleanup:** none.

result: [pending]

---

## Sign-off Table

One row per scenario. Mark **Result** `pass` / `passed_partial` / `fail`.

| Scenario | Maps to       | Result  | Date | Operator | Notes |
| -------- | ------------- | ------- | ---- | -------- | ----- |
| S1       | SC-2          | pending |      |          |       |
| S2       | SC-2 (cont.)  | pending |      |          |       |
| S3       | SC-3          | pending |      |          |       |
| S4       | SC-5          | pending |      |          |       |
| S5       | SC-6          | pending |      |          |       |
| S6       | SC-1          | pending |      |          |       |

**Overall phase status:** `pending`
<!-- set to one of:
     passed         — all live scenarios pass
     passed_partial — some scenarios are credential-blocked (build NOT blocked)
     human_needed   — a real defect was found (not a missing credential)
-->

- **Operator:** ___________
- **Date:** ___________
- **Sentry events linked (S5):** ___________

### `passed_partial` is a documented, non-blocking path

Per the gateway optional-feature pattern, a scenario whose credentials are
genuinely unavailable is marked **`passed_partial`** with the blocker
noted — it does **not** fail the phase. The autonomous build
(`07-01..07-08`) is already green; the alert channels degrade gracefully
to "log + dashboard banner only" when credentials are absent. Live
delivery is the deferred-UAT pattern every prior phase used (`03-08` /
`04-09` / `06-11`). Only a **real defect** (a configured channel that does
not deliver, a dedup regression, a cardinality blowout, a Sentry leak, an
`X-Admin-Key` in the browser) sets the overall status to `human_needed`.

---

## Gaps

(populate during/after execution — list any scenario that did not pass,
why, and what follow-up `/gsd-plan-phase --gaps` pass tracks the
remediation. Distinguish a missing credential — `passed_partial`, no
gap — from a real defect — `human_needed`, file a gap.)
