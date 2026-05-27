# Incident Response Runbook

**Phase 11 (`ifix-ai-gateway`) production incident playbook.** Read this when:

- A new incident is suspected (operator instinct / alert / client report).
- A historical incident is being post-mortem'd (cross-ref [`POSTMORTEM-TEMPLATE.md`](./POSTMORTEM-TEMPLATE.md)).
- Operator handoff between shifts mentions one of the 4 incident classes below.
- An Ifix admin reports inability to log in to the dashboard due to lost 2FA
  device + lost backup codes (see [Class 4](#class-4-rate-limit--quota-lockout-tenant)
  sub-detection and [Operator Recovery Procedures](#operator-recovery-procedures)).
- Signals do not cleanly match classes 1–4 → start at
  [Triagem de Incidente Desconhecido](#triagem-de-incidente-desconhecido).

**Last updated: 2026-05-27.**

Related runbooks (8 siblings — every incident class cross-refs at least one):

- [`RUNBOOK-DEPLOY.md`](./RUNBOOK-DEPLOY.md) — release flow, per-env keys, GHA retrigger workflow.
- [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) — Phase 3 circuit-breaker + tier-0 ↔ tier-1 fallback.
- [`RUNBOOK-PRIMARY-POD.md`](./RUNBOOK-PRIMARY-POD.md) — Phase 6.6 Vast primary lifecycle (schedule-driven).
- [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md) — Phase 6 reactive emergency pod (breaker-driven).
- [`RUNBOOK-OBSERVABILITY-ALERTING.md`](./RUNBOOK-OBSERVABILITY-ALERTING.md) — Sentry + Prometheus + audit drift.
- [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md) — Phase 4 per-tenant rate-limit + quota + billing.
- [`RUNBOOK-CLIENT-INTEGRATION.md`](./RUNBOOK-CLIENT-INTEGRATION.md) — normal-tenant integration troubleshooting.
- [`RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`](./RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md) — sensitive-tenant LGPD invariants.
- [`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md) — admin 2FA device-loss + lost-backup-codes recovery procedure (separation-of-duty + audit-logged SQL reset).

> **Provenance note.** This runbook was synthesized from the plan-defined
> 4 incident classes (D-11) and Google SRE blameless template (D-10) on
> 2026-05-27, *before* the Wave 2 live chaos UAT plans (11-06 load test,
> 11-07 primary kill, 11-08 OpenRouter DROP) executed. Each class
> "Detection" section names the canonical Wave 2 evidence file *if
> available* and falls back to Phase 10 EVIDENCE files for the baseline
> signal shape. Update each "Canonical example" line once the Wave 2
> UAT closes with `passed` or `passed_partial`.

---

## Mental Model (30 seconds)

SLO v1.0 thresholds (D-04) — any sustained breach is an incident:

| Metric                       | Threshold | Source |
|------------------------------|-----------|--------|
| P95 chat completion          | ≤ 5 s     | D-04   |
| P95 embed                    | ≤ 1 s     | D-04   |
| P95 STT (Whisper)            | ≤ 10 s    | D-04   |
| Error rate (non-503)         | < 1 %     | D-04   |
| 5xx panic event count        | 0         | D-04   |

The "canonical SLO measured PASS" evidence point is the Phase 11 PRD-01
load-test baseline (see [SLO v1.0 anchor](#slo-v10-anchor) at end of
runbook).

The 4 incident classes (D-11) — names verbatim, do not paraphrase
elsewhere in this doc (grep-test guarded):

1. **[Primary pod down](#class-1-primary-pod-down)** — Vast yank / supervisord crash / GPU OOM.
2. **[OpenRouter / OpenAI degraded](#class-2-openrouter--openai-degraded)** — tier-1 fallback path unhealthy.
3. **[Audit/billing pipeline broken](#class-3-auditbilling-pipeline-broken)** — audit_log gap / billing partial.
4. **[Rate-limit / quota lockout tenant](#class-4-rate-limit--quota-lockout-tenant)** — single-tenant 429 storm OR admin 2FA lockout sub-class.

Triage protocol (every incident):

1. Capture timestamp + first failing `request_id` from operator note.
2. Match signals to one of the 4 classes (next section). If no clean
   match → jump to [Triagem de Incidente Desconhecido](#triagem-de-incidente-desconhecido).
3. Follow the class's **Diagnose → Mitigate → Verify** flow.
4. Open a SEV-{1|2|3} postmortem ticket (see [Postmortem cross-ref](#postmortem-cross-ref)).

---

## Class 1: Primary pod down

**Affected SLO:** P95 chat (5 s); error rate (degraded tier-0 ↔ tier-1 switchover).

### Detection signals
- Prometheus: `gateway_primary_state{state="asleep"}` (or `state="draining"` stuck past grace).
- Sentry breadcrumb: `subsystem:primary`, `shutdown_reason:health_timeout` or `shutdown_reason:provision_failure`.
- `GET /v1/health/upstreams` returns `local-llm.state="open"` (and `local-stt`, `local-embed` for the 3-role tier-0).
- Vast.ai console: instance status `exited` / `gone` / `host_unavailable`.
- Audit log lookup: `event_kind=primary_lifecycle_close` in `ai_gateway.audit_log` within the last 30 minutes.

Canonical example: `.planning/phases/11-prod-hardening/11-07-EVIDENCE.md`
(chaos primary-kill UAT — Vast API DELETE → natural breaker open →
invisible failover) **if available**; otherwise refer to Phase 10
EVIDENCE files (`.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md`
and `10-01-CAPACITY-OBSERVED.md`) for baseline FSM transition
observations and the breaker-open shape against `local-llm`.

### Diagnose

```bash
# 1) FSM state — prefer JSON output (gateway/cmd/gatewayctl/primary.go:127)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl primary state --json'
# Fallback if --json flag absent: human-readable form
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl primary state'

# 2) Upstream health snapshot (gateway/internal/upstreams/health_test.go:87)
curl -s https://ai-gateway.converse-ai.app/v1/health/upstreams | jq

# 3) Reconciler logs (last 5 min)
ssh n8n-ia-vm 'docker logs ifix-ai-gateway --since 5m --tail 200 | grep -E "primary|reconcil"'

# 4) Vast.ai instance state (extract instance ID from `primary state` above)
INSTANCE_ID=...   # from step 1
curl -sH "Authorization: Bearer $VAST_AI_API_KEY" \
  "https://console.vast.ai/api/v0/instances/$INSTANCE_ID/" | jq '.actual_status, .cur_state'
```

### Mitigate — observe FIRST, intervene only if stuck

**Rule:** wait **90 seconds** for the controller to reconcile autonomously
*before* manual intervention. The FSM auto-advances Ready → Draining →
Asleep on probe failure; manual `force-up` while the controller is
mid-reconcile can fight the controller (Codex review concern, plan 11-07
LOW #4).

After 90 s of observation, if FSM is still in `Draining` (not `Asleep`):

```bash
# (a) Force the controller through Draining → Asleep
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl primary force-down --reason class1_cleanup'
# (b) Provision a fresh instance (only after force-down succeeded)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl primary force-up --reason class1_replace'
```

If Vast inventory is empty at the configured price cap, revise per
[`RUNBOOK-PRIMARY-POD.md`](./RUNBOOK-PRIMARY-POD.md) §"Capacity
exhaustion":

- `PRIMARY_VAST_PRICE_CAP_DPH` (default $0.60/h for 2×RTX 3090 per Phase 06.8 spec).
- `PRIMARY_VAST_MACHINE_ALLOWLIST` (allow-list 43803,55158 per Phase 06.8 LOCK).

If primary remains unrecoverable for >15 min and emergency pod has not
auto-spun, escalate to [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md)
§"Manual force-spin" path.

### Verify

```bash
curl -s https://ai-gateway.converse-ai.app/v1/health/upstreams | jq '.upstreams[] | select(.name=="local-llm")'
# Expect: state="closed"
# Plus on a real request:
curl -sD- -H "Authorization: Bearer $TENANT_KEY" \
  https://ai-gateway.converse-ai.app/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen-chat","messages":[{"role":"user","content":"ping"}]}' \
  | grep -i 'X-Upstream:'
# Expect: X-Upstream: local-llm
```

### Cross-ref

- [`RUNBOOK-PRIMARY-POD.md`](./RUNBOOK-PRIMARY-POD.md) — Vast lifecycle, schedule, capacity exhaustion.
- [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) — tier-0 ↔ tier-1 fallback policy.
- [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md) — breaker-driven emergency replacement.

---

## Class 2: OpenRouter / OpenAI degraded

**Affected SLO:** P95 chat / embed / STT (fallback path);
sensitive-tenant 503 rate (expected per RES-08).

### Detection signals
- Prometheus: any of `openrouter-chat`, `openai-whisper`, `openai-embed` breaker `state=open`.
- Per-route 503 spike on `/v1/chat/completions`, `/v1/audio/transcriptions`, `/v1/embeddings`.
- Sensitive tenants (`telefonia`, `cobrancas`) receiving HTTP 503 with `code: "upstream_unavailable_for_sensitive_tenant"` — **this is the expected RES-08 behavior when primary is also down**; do not "fix" by routing them to OpenRouter.
- OpenRouter status page (https://status.openrouter.ai/) reports incident.
- Sentry event tag `upstream:openrouter-chat` or `upstream:openai-*` with elevated rate.

Canonical example: `.planning/phases/11-prod-hardening/11-08-EVIDENCE.md`
(chaos OpenRouter DROP UAT — `iptables -I OUTPUT … --comment phase11-chaos-openrouter`
scoped to the gateway container netns per plan 11-08 [reviews HIGH #8])
**if available**; otherwise refer to Phase 10 EVIDENCE files
(`.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md`) for any
naturally-observed breaker-open episode against `openrouter-chat`, OR
queue a re-run via the planned re-test path in
`.planning/phases/11-prod-hardening/11-VALIDATION.md`.

### Diagnose

```bash
# 1) Upstream health snapshot
curl -s https://ai-gateway.converse-ai.app/v1/health/upstreams | jq '.upstreams[] | select(.name | startswith("open"))'

# 2) OpenRouter side — is it the provider or us?
curl -sD- https://openrouter.ai/api/v1/auth/key \
  -H "Authorization: Bearer $UPSTREAM_LLM_OPENROUTER_AUTH_BEARER" | head -20

# 3) If suspected egress block scoped to the gateway container netns
#    (NOT host-wide iptables — see plan 11-08 reviews HIGH #8 blast-radius rule):
ssh n8n-ia-vm 'sudo nsenter -t $(docker inspect -f "{{.State.Pid}}" ifix-ai-gateway) -n iptables -L OUTPUT --line-numbers'

# 4) Per-route P95 + error rate (last 5 min)
curl -sH "X-Admin-Key: $AI_GATEWAY_ADMIN_KEY" \
  "https://ai-gateway.converse-ai.app/admin/metrics?window=5m" | jq '.routes'
```

### Mitigate

Default action: **wait for provider recovery**. The breaker auto-cools
after 30 s (HALF_OPEN) and re-closes on first success. No manual action
is needed for ~80 % of tier-1 incidents.

If the provider has confirmed recovery (status page green) but the
breaker is still OPEN past 5 min:

```bash
# Operator escape hatch — release the breaker (gateway/cmd/gatewayctl/breaker.go:187)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream=openrouter-chat'
```

If the failure is on our side (egress block on gateway container
netns) and chaos cleanup is needed, follow the cleanup pattern in
plan 11-08's `scripts/chaos/openrouter-iptables-drop.sh cleanup`
which removes only rules tagged `--comment phase11-chaos-openrouter`.

**Sensitive-tenant invariant.** Do NOT force-close the breaker AND
proceed to route sensitive tenants to OpenRouter. RES-08 forbids that
flow regardless of breaker state; the only sensitive-tenant remediation
is bringing primary `local-llm` back (see [Class 1](#class-1-primary-pod-down)).

### Verify

```bash
# Breaker state transitions
curl -s https://ai-gateway.converse-ai.app/v1/health/upstreams | jq '.upstreams[] | select(.name=="openrouter-chat") | .state'
# Expect: "half_open" → "closed" within 1 min after first successful synthetic probe.

# Per-route P95 back to baseline
curl -sH "X-Admin-Key: $AI_GATEWAY_ADMIN_KEY" \
  "https://ai-gateway.converse-ai.app/admin/metrics?window=5m" | jq '.routes."/v1/chat/completions".p95_ms'
# Expect: ≤ 5000 (D-04 SLO).
```

### Cross-ref

- [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) — tier-1 routing decision table + tool-call non-failover rule.
- [`RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`](./RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md) — sensitive-tenant 503 expected response shape.

---

## Class 3: Audit/billing pipeline broken

**Affected SLO:** none directly (gateway returns 200 to clients), but
LGPD/compliance posture and billing integrity break silently.

### Detection signals
- `SELECT count(*) FROM ai_gateway.audit_log WHERE ts > NOW() - INTERVAL '5 minutes'` returns 0 while `/admin/metrics` shows non-zero request count.
- `billing_events` rows with `source='partial'` or `null` body where `data_class='normal'` should produce a content row.
- Gateway container logs contain UTF-8 / gzip magic-byte errors (`invalid UTF-8 …`, byte `0x8b`).
- Sentry tag `pipeline:audit` or `pipeline:billing` with elevated rate.

Canonical example: Phase 10 commit `5bd79d1` retrospective (`fix(proxy):
strip client Accept-Encoding so Transport auto-decompresses upstream
response` — the audit/billing 0x8b gzip bug closed during Phase 10
deploy) **if available** as a recorded incident, with cross-reference
to [`RUNBOOK-OBSERVABILITY-ALERTING.md`](./RUNBOOK-OBSERVABILITY-ALERTING.md)
§"Audit drift" detection; otherwise the operator probe is the SQL
counter query below.

### Diagnose

```bash
# 1) Audit row count over last 5 minutes
psql "$AI_GATEWAY_DSN" -tAc \
  "SELECT count(*) FROM ai_gateway.audit_log WHERE ts > NOW() - INTERVAL '5 minutes';"

# 2) Cross-check with admin metrics request count
curl -sH "X-Admin-Key: $AI_GATEWAY_ADMIN_KEY" \
  "https://ai-gateway.converse-ai.app/admin/metrics?window=5m" | jq '.total_requests'
# Drift of >5% between SQL count and metrics count = pipeline broken.

# 3) Check audit_log_content rows for normal tenants (sensitive MUST NOT emit content)
psql "$AI_GATEWAY_DSN" -tAc "
  SELECT a.data_class, count(c.audit_log_id) AS content_rows, count(a.id) AS audit_rows
  FROM ai_gateway.audit_log a
  LEFT JOIN ai_gateway.audit_log_content c ON c.audit_log_id = a.id
  WHERE a.ts > NOW() - INTERVAL '5 minutes'
  GROUP BY a.data_class;"

# 4) Gateway container logs for gzip / UTF-8 errors
ssh n8n-ia-vm 'docker logs ifix-ai-gateway --since 5m 2>&1 | grep -E "0x8b|invalid UTF|gzip"'
```

### Mitigate

If `gateway/internal/proxy/director.go:80` has regressed (the
`r.Header.Del("Accept-Encoding")` line introduced by commit
**5bd79d1** is missing), redeploy a build that carries the fix:

```bash
# Confirm the fix is in the deployed binary
ssh n8n-ia-vm 'docker exec ifix-ai-gateway grep -c "Accept-Encoding" /usr/local/bin/gateway' || true
# Confirm the source line is still present in HEAD
git -C /home/pedro/projetos/pedro/gpu-ifix show 5bd79d1 --stat
git -C /home/pedro/projetos/pedro/gpu-ifix grep -n "Accept-Encoding" gateway/internal/proxy/director.go
# If line is gone in HEAD → revert the regression PR + redeploy per RUNBOOK-DEPLOY.md.
```

If billing rows are lagging (queue back-pressure, not a code
regression), drain the BullMQ billing queue then re-emit partial events
per [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md)
§"Backlog drain".

### Verify

```bash
# Audit row count should match the request counter within ±1% over a 1-minute window
psql "$AI_GATEWAY_DSN" -tAc \
  "SELECT count(*) FROM ai_gateway.audit_log WHERE ts > NOW() - INTERVAL '1 minute';"
curl -sH "X-Admin-Key: $AI_GATEWAY_ADMIN_KEY" \
  "https://ai-gateway.converse-ai.app/admin/metrics?window=1m" | jq '.total_requests'

# No more UTF-8 / 0x8b errors in container logs
ssh n8n-ia-vm 'docker logs ifix-ai-gateway --since 5m 2>&1 | grep -cE "0x8b|invalid UTF|gzip"'
# Expect: 0
```

### Reference commit

**5bd79d1** — `fix(proxy): strip client Accept-Encoding so Transport
auto-decompresses upstream response`. Phase 10 hotfix. Affected file:
`gateway/internal/proxy/director.go:80`
(line `r.Header.Del("Accept-Encoding")`). Removing this header
unconditionally lets Go's `http.Transport` negotiate gzip on the
upstream leg and auto-decompress the response body before the audit
tee reads it.

### Cross-ref

- [`RUNBOOK-OBSERVABILITY-ALERTING.md`](./RUNBOOK-OBSERVABILITY-ALERTING.md) — audit drift detection rule.
- [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md) — billing pipeline + partial-event re-emission.

---

## Class 4: Rate-limit / quota lockout tenant

**Affected SLO:** per-tenant error rate (429s exceed 1 % of that
tenant's window); no global SLO impact.

### Detection signals
- Prometheus: `gateway_rate_limit_rejected_total{tenant=X}` spike vs
  baseline (defined in
  [`gateway/internal/obs/metrics.go:150`](../internal/obs/metrics.go)).
- Client reports HTTP 429 with `Retry-After` header for one specific
  tenant slug.
- `/admin/metrics` shows per-tenant rate-rejection > 0 for any window.
- **Sub-class — admin 2FA lockout:** an Ifix admin (NOT an API tenant)
  reports inability to log in to the dashboard via TOTP challenge
  because they lost the authenticator device AND lost the 10 backup
  codes. This is a credential/lockout-shaped incident — same triage
  family as a tenant lockout — and routes to the
  [Operator Recovery Procedures](#operator-recovery-procedures) section
  below, specifically the
  [`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md) procedure.

Canonical example: `.planning/phases/11-prod-hardening/11-06-EVIDENCE.md`
(PRD-01 load-test baseline — per-tenant P95 + 429 rate reference under
30-min sustained replay traffic) **if available**; otherwise refer to
Phase 10 `.planning/phases/10-prod-deploy-ai-gateway/` smoke-*.py run
timings as the closest baseline of per-tenant request distribution.

### Diagnose

```bash
# 1) Inspect the quota state for the affected tenant
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant get-quota --tenant=<slug>'

# 2) Confirm the tenant slug + key prefix + status
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl key list --tenant=<slug>'

# 3) Is the client abusive, or is the quota misconfigured?
psql "$AI_GATEWAY_DSN" -tAc "
  SELECT date_trunc('minute', ts) AS m, count(*) FROM ai_gateway.audit_log
  WHERE tenant_id = (SELECT id FROM ai_gateway.tenant WHERE slug='<slug>')
    AND ts > NOW() - INTERVAL '30 minutes'
  GROUP BY 1 ORDER BY 1;"
```

For the **admin 2FA lockout sub-class**, the diagnosis is operator-side
only — the admin cannot pass the `/2fa/challenge` page. Confirm with:

```bash
psql "$DASHBOARD_DATABASE_URL" -tAc \
  "SELECT email, two_factor_enabled FROM dashboard_auth.\"user\" WHERE email = '{LOCKED_OUT_EMAIL}';"
```

If `two_factor_enabled = true` and the admin confirms (over a secondary
voice channel — see [Operator Recovery Procedures](#operator-recovery-procedures))
that the device + backup codes are lost, proceed to
[`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md).

### Mitigate

Tenant quota path (operator action):

```bash
# Adjust the quota window for the tenant (raise short-term ceiling)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant set-quota --tenant=<slug> --tokens=N'

# OR — escape hatch — temporarily lift the breaker pressure if applicable
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream=<upstream>'
```

Coordinate with the tenant operator (WhatsApp / email) — quota
adjustments should be paired with a follow-up conversation about
expected traffic shape.

**Admin 2FA lockout sub-class:** follow the full
[`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md) procedure
(separation-of-duty + audit-row-before-SQL-UPDATE).

### Verify

```bash
# 429 rate decays back to baseline
curl -sH "X-Admin-Key: $AI_GATEWAY_ADMIN_KEY" \
  "https://ai-gateway.converse-ai.app/admin/metrics?window=5m" | jq '.routes["/v1/chat/completions"].rate_limit_rejected'

# Per-tenant error rate back under SLO threshold (< 1 %)
psql "$AI_GATEWAY_DSN" -tAc "
  SELECT count(*) FILTER (WHERE status_code >= 500)::float / NULLIF(count(*),0)
  FROM ai_gateway.audit_log
  WHERE tenant_id = (SELECT id FROM ai_gateway.tenant WHERE slug='<slug>')
    AND ts > NOW() - INTERVAL '5 minutes';"
```

For the admin 2FA lockout sub-class, verification = the locked-out
admin successfully re-enrolls via the `/2fa/enroll` page (Plan 11-02
flow) and stores the new backup codes in their password manager.

### Cross-ref

- [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md) — per-tenant quota tuning + billing reconciliation.
- [`RUNBOOK-CLIENT-INTEGRATION.md`](./RUNBOOK-CLIENT-INTEGRATION.md) — normal-tenant rate-limit contract + Retry-After convention.
- [`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md) — admin 2FA reset procedure (sub-class).

---

## Operator Recovery Procedures

These flows do not map 1:1 to a single incident-class detection signal
but are part of the on-call toolkit invoked from inside one of the 4
classes above.

### Admin 2FA recovery (device-loss + lost-backup-codes)

A locked-out admin has lost BOTH the TOTP authenticator (phone wipe,
app deletion, …) AND the 10 backup codes (no password-manager copy).
Self-service recovery is forbidden (would defeat the 2FA control).
A **secondary admin** executes the reset; the locked-out admin
re-enrolls afterwards.

Two paths:

- **Preferred (future):** if a future Phase ships
  `gatewayctl admin reset-2fa --email <addr>`, prefer the subcommand —
  it wraps the audit log row + SQL update atomically.
- **Current default:** direct Postgres update inside a transaction,
  with the `audit_log` row written **before** the SQL UPDATE so a
  failed UPDATE still leaves a repudiation breadcrumb.

The full procedure (SQL snippets, separation-of-duty checklist,
secondary-channel voice confirmation) lives in
[`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md).

Paired flows:

- If the locked-out admin also forgot their password (or operator
  suspects credential compromise alongside device loss), pair the 2FA
  reset with a password reset via
  [`scripts/dashboard/seed-admins.sh`](../../scripts/dashboard/seed-admins.sh)
  (Plan 11-05 — per-env admin provisioning script; emits a `chmod 600`
  password file, never stdout).
- After reset, the locked-out admin re-enrolls via the
  `/2fa/enroll` dashboard page (Plan 11-02 enroll flow).

---

## Triagem de Incidente Desconhecido

Entry-point when signals do not cleanly match classes 1–4. NOT a 5th
class. Open a SEV-2 postmortem ticket from
[`POSTMORTEM-TEMPLATE.md`](./POSTMORTEM-TEMPLATE.md) immediately, then
follow the triage protocol below.

Every incident triaged here MUST resolve to one of the 4 existing
classes after diagnosis OR drive a documented expansion of the class
taxonomy via a postmortem-tracked D-XX update in the next planning
cycle. Unknown incidents are the canonical case where the blameless
template earns its keep.

### Triage protocol (5 minutes)

1. **Gather signals.** Open in 4 tabs:
   - `https://ai-gateway.converse-ai.app/admin/metrics?window=5m` (admin key required).
   - `https://ai-gateway.converse-ai.app/v1/health/upstreams`.
   - Sentry feed for project `ifix-ai-gateway-prod` (last 30 min).
   - `ssh n8n-ia-vm 'docker logs ifix-ai-gateway --tail=200'`.
   Note timestamps and the **first failing `request_id`**; both go
   into the SEV-2 ticket Timeline section.

2. **Cross-check class detection signals in order.** Walk classes
   1 → 2 → 3 → 4 against the gathered signals. If any class's
   detection-signals row matches, **fall through to that class's
   Diagnose section** — do not stay in triage.

3. **No match → escalation path:**
   - (a) Page the on-call platform owner via the WhatsApp escalation
     chain (PagerDuty equivalent for the 4-admin team). Include the
     timeline + breadcrumbs gathered in step 1.
   - (b) **Open a SEV-2 postmortem ticket**: copy
     [`POSTMORTEM-TEMPLATE.md`](./POSTMORTEM-TEMPLATE.md) to
     `.planning/postmortems/postmortem-{YYYY-MM-DD}-{slug}.md`,
     commit the empty 9-section skeleton, and reference the commit in
     the on-call note. The template is the canonical SRE blameless
     9-section form; do NOT improvise.
   - (c) If user-facing impact (any tenant sees 5xx errors), notify
     affected tenants via WhatsApp + the status page within 15 minutes
     using the [`RUNBOOK-CLIENT-INTEGRATION.md`](./RUNBOOK-CLIENT-INTEGRATION.md)
     §"Incident communication" template.
   - (d) Propose a new incident class only via a v2 RUNBOOK update
     driven by a new D-XX decision in the next planning cycle — DO
     NOT create a 5th class in v1 here. The 4-class taxonomy is a
     deliberate scope ceiling (D-11) to prevent unbounded classes.

---

## Postmortem cross-ref

Every incident that exceeds 5 minutes of operator response time OR
breaches any SLO threshold (Mental Model table above) MUST produce a
postmortem.

Source-of-truth template: [`POSTMORTEM-TEMPLATE.md`](./POSTMORTEM-TEMPLATE.md)
(Google SRE blameless 9-section — D-10). Copy-and-fill per incident:

```bash
DATE=$(date -u +%Y-%m-%d)
SLUG=primary-pod-down       # short kebab-case incident slug
cp gateway/docs/POSTMORTEM-TEMPLATE.md \
   .planning/postmortems/postmortem-${DATE}-${SLUG}.md
$EDITOR .planning/postmortems/postmortem-${DATE}-${SLUG}.md
git add .planning/postmortems/postmortem-${DATE}-${SLUG}.md && \
  git commit -m "postmortem(${SLUG}): ${DATE} incident — draft"
```

The template skeleton is authoritative; never edit
`POSTMORTEM-TEMPLATE.md` in place to "fix" a single postmortem (treat
it as an immutable template — fixes go through a regular plan that
updates the template + back-references every existing instance).

---

## Sibling runbooks

| Runbook | Scope |
|---------|-------|
| [`RUNBOOK-DEPLOY.md`](./RUNBOOK-DEPLOY.md) | Release flow, per-env keys (D-19), GHA retrigger workflow (D-18.4). |
| [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) | Circuit-breaker policy, tier-0 ↔ tier-1 routing decision table, tool-call non-failover rule. |
| [`RUNBOOK-PRIMARY-POD.md`](./RUNBOOK-PRIMARY-POD.md) | Phase 6.6 Vast primary FSM, schedule, capacity exhaustion. |
| [`RUNBOOK-EMERGENCY-POD.md`](./RUNBOOK-EMERGENCY-POD.md) | Phase 6 breaker-driven emergency pod. |
| [`RUNBOOK-OBSERVABILITY-ALERTING.md`](./RUNBOOK-OBSERVABILITY-ALERTING.md) | Sentry + Prometheus + audit-drift detection. |
| [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md) | Per-tenant rate-limit + quota + billing reconciliation. |
| [`RUNBOOK-CLIENT-INTEGRATION.md`](./RUNBOOK-CLIENT-INTEGRATION.md) | Normal-tenant integration troubleshooting + tenant-comms template. |
| [`RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`](./RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md) | Sensitive-tenant LGPD invariants (RES-08 503 behavior). |
| [`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md) | Admin 2FA device-loss + lost-backup-codes recovery (separation-of-duty + audit-logged SQL reset). |

---

## SLO v1.0 anchor

Restating the D-04 thresholds for incident-detection reference:

| Metric                       | Threshold | Rationale |
|------------------------------|-----------|-----------|
| P95 chat completion          | ≤ 5 s     | Hand-tuned for Qwen 3.5 27B tier-0 + OpenRouter tier-1 mix. |
| P95 embed                    | ≤ 1 s     | BGE-M3 local Infinity + OpenAI 3-small fallback (`dimensions=1024`). |
| P95 STT                      | ≤ 10 s    | Whisper-large-v3 local + OpenAI whisper-1 fallback; 30-s audio clip target. |
| Error rate (non-503)         | < 1 %     | 503s are explicit sensitive-tenant `upstream_unavailable_for_sensitive_tenant` — counted separately. |
| 5xx panic count              | 0         | Recoverer catches and converts to OpenAI-shaped 500; any panic event is a postmortem trigger. |

Canonical "SLO measured PASS" evidence point: the Phase 11 PRD-01
load-test baseline at
`.planning/phases/11-prod-hardening/11-06-EVIDENCE.md` once Wave 2
closes. Until then, the baseline reference is the Phase 10 close-out
in `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md`
(status: `passed`).
