# Observability & Alerting Runbook — Pointer

The canonical Phase 7 observability + alerting runbook lives alongside its
sibling gateway runbooks at:

**[`gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md`](../gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md)**

It covers:

- **The dashboard** — URL, Better Auth login, what each view shows, how
  to read the FSM panel + the sticky critical banner, the 7s polling model.
- **The alerting subsystem** — the severity → channel matrix
  (critical → Chatwoot + ClickUp + Brevo; warning → ClickUp + Brevo;
  info → banner/log only), where the alerter goroutine logs, the
  `gw:alert:dedup:` 5-minute dedup behaviour, and the
  graceful-degradation rule (empty alert env var → channel disabled with
  a WARN → "log + dashboard banner only", never fail-boot).
- **`/metrics`** — the Prometheus endpoint and the **cardinality audit**
  procedure (`curl` the endpoint + `promtool check metrics` + a
  `count by (__name__)` series count) against the ≤10k-active-series budget.
- **`/admin/metrics` + `/admin/audit`** — the admin-key-gated JSON
  endpoints and how to curl them.
- **Sentry** — what gets captured, what gets redacted (`authorization`,
  `x-api-key`, request/response bodies).
- **Known failure modes** — boot-window lost events (Pitfall 4), alerter
  stall on a dead external API (Pitfall 5 → `gateway_alert_dropped_total`),
  ClickUp 401 (Pitfall 6), the `audit_log` partition-window limitation
  (Pitfall 8).
- **Incident playbook** — detection → diagnosis → action for "alerts not
  arriving", "stale dashboard", "/metrics series climbing", and "Sentry
  leaking a secret".

> **Why the canonical file is under `gateway/docs/`:** the three sibling
> runbooks (`RUNBOOK-EMERGENCY-POD.md`, `RUNBOOK-FAILOVER.md`,
> `RUNBOOK-QUOTAS-BILLING.md`) all live in `gateway/docs/`, and the
> runbook's cross-links to them are relative. Keeping all four runbooks
> together preserves those links and the established repo layout. This
> root-level file is a stable pointer so `docs/`-relative references
> (and plan 07-09's `files_modified` path) still resolve.
