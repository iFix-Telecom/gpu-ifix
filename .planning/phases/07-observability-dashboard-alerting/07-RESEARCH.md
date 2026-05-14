# Phase 7: Observability — Dashboard & Alerting - Research

**Researched:** 2026-05-14
**Domain:** Go gateway observability (Prometheus + Sentry + audit) + alerting goroutine (Chatwoot/ClickUp/Brevo) + Next.js 15 ops dashboard (Better Auth + shadcn + Recharts)
**Confidence:** HIGH (existing codebase verified directly; external API patterns confirmed against in-repo reference implementations + official docs)

## Summary

Phase 7 is mostly a **wiring + extension phase, not a greenfield phase**. The gateway already has `gateway/internal/obs/` (Prometheus collectors via promauto, `promhttp.Handler()`, Sentry init with a `BeforeSend` redaction hook), `gateway/internal/audit/` (async batched writer to a partitioned `audit_log` table), an `/admin` chi sub-router gated by `X-Admin-Key` bcrypt middleware (`gateway/internal/admin/`), and three Redis Pub/Sub event streams already published by Phases 3/5/6 (`gw:breaker:events`, `gw:shed:events`, `gw:emerg:events`). The work is: (1) add a `/admin/metrics` JSON handler next to the existing `/admin/usage` handler; (2) audit and bound Prometheus label cardinality (the collectors already declare bounded labels — this is mostly verification + adding latency histograms); (3) add an alerting goroutine that subscribes to the three Pub/Sub channels and fans out to Chatwoot/ClickUp/Brevo with Redis-TTL dedup; (4) extend the audit writer with explicit event-type rows for FSM transitions / tenant activate-deactivate / pod lifecycle / threshold changes; (5) verify+extend the Sentry `BeforeSend` to also scrub request/response bodies (it currently only scrubs headers); (6) build a new greenfield `dashboard/` Next.js 15 app.

The three external integrations all have **in-repo reference implementations** in sibling projects: `cobrancas-api/src/lib/clickup/` is the canonical resilient-client pattern (AdaptiveRateLimiter reading `X-RateLimit-*` headers + in-memory CircuitBreaker + `withRetry`); `campanhas-chatifix/packages/backend/src/dispatch/message.sender.ts` shows the exact Chatwoot Application API call shape (`POST /api/v1/accounts/{account_id}/conversations` with `api_access_token` header and an embedded `message` object); `converseai-v4/packages/email/src/send.ts` shows the Brevo SMTP pattern (plain `nodemailer.createTransport` against `SMTP_HOST`/`SMTP_PORT`/`SMTP_USER`/`SMTP_PASS`). The catch: those references are **TypeScript**, and the alerting logic runs **in the Go gateway** — so the patterns must be re-implemented in Go, not imported. Go already has `cenkalti/backoff/v5` and `sony/gobreaker/v2` in `go.mod` (used by Phase 3), which are the Go equivalents of the cobrancas-api retry/circuit-breaker utilities.

**Primary recommendation:** Treat Phase 7 as five gateway-side extension tasks (each touching one existing package) plus one greenfield `dashboard/` app. Re-implement the Chatwoot/ClickUp/Brevo clients in Go as small `internal/alert/` sub-packages, reusing the already-vendored `cenkalti/backoff/v5` + `sony/gobreaker/v2` for resilience (do NOT add new HTTP-client libraries). For the dashboard, copy the converseai-v4 `radix-nova` shadcn preset and Better Auth structure verbatim, but strip every plugin converseai-v4 uses except `emailAndPassword` — this is a 4-admin internal tool.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| `/admin/metrics` JSON aggregation (OBS-01) | API / Backend (Go gateway) | — | Metrics live in the gateway process; only the gateway can compute P50/P95/P99 + inflight + FSM state. New handler on the existing `/admin` sub-router. |
| `/metrics` Prometheus exposition + cardinality budget (OBS-02) | API / Backend (Go gateway) | — | `promhttp.Handler()` already wired at `main.go:842`; cardinality is a property of how collectors declare labels. |
| Alerting goroutine: Pub/Sub → severity tier → channel fan-out (OBS-04, OBS-05, OBS-06) | API / Backend (Go gateway) | Database (Redis for dedup TTL) | CONTEXT.md locks "alerting runs IN the gateway as a goroutine". Mirrors the existing `breakerSet.Subscribe(ctx)` / `emergReconciler.Run(ctx)` goroutine pattern in `main.go`. |
| Alert delivery clients (Chatwoot, ClickUp, Brevo) | API / Backend (Go gateway) | External services | Outbound HTTP from the gateway process. Re-implements the cobrancas-api resilience pattern in Go. |
| Audit log event-type extension (OBS-07) | API / Backend (Go gateway) | Database (Postgres `audit_log`) | Extends `gateway/internal/audit/writer.go`; reuses the existing partitioned table + async writer. |
| Sentry redaction of headers + bodies (OBS-08) | API / Backend (Go gateway) | External (Sentry) | `BeforeSend` hook in `gateway/internal/obs/sentry.go`. |
| Dashboard rendering (charts, tables, banner) (OBS-03) | Frontend Server (Next.js 15 SSR/RSC) | Browser (React Query polling, Recharts client components) | New `dashboard/` app. Reads gateway `/admin/*` JSON. |
| Dashboard auth (4 admins, email/password) | Frontend Server (Next.js 15 + Better Auth) | Database (Better Auth's own tables) | CONTEXT.md locks a standalone Better Auth instance — its own DB schema, not the gateway's `admin_keys`. |
| Dashboard → gateway data fetch | Frontend Server (Next.js route handlers / server components) | — | The `X-Admin-Key` must never reach the browser — proxy gateway calls through Next.js server-side code, never client fetch. |

## Standard Stack

### Core — Gateway side (Go) — ALL already in `go.mod`, nothing new to add

| Library | Version (in go.mod) | Purpose | Why Standard |
|---------|--------------------|---------|--------------|
| `github.com/prometheus/client_golang` | v1.20.5 `[VERIFIED: go.mod]` | Prometheus collectors + `promhttp.Handler()` | Already the gateway's metrics layer (`obs/metrics.go`). v1.23.2 is latest `[VERIFIED: proxy.golang.org]` but **do not bump in this phase** — no Phase 7 feature needs it; bumping is unrelated churn. |
| `github.com/getsentry/sentry-go` | v0.29.1 `[VERIFIED: go.mod]` | Sentry client + `BeforeSend` hook | Already wired in `obs/sentry.go`. |
| `github.com/cenkalti/backoff/v5` | v5.0.3 `[VERIFIED: go.mod]` | Exponential backoff for retry | Already used by Phase 3 (RES-02). This is the Go equivalent of cobrancas-api's `withRetry`. |
| `github.com/sony/gobreaker/v2` | v2.4.0 `[VERIFIED: go.mod]` | Circuit breaker | Already used by Phase 3 (RES-01). The Go equivalent of cobrancas-api's `CircuitBreaker` class. Wrap each external alert client (Chatwoot, ClickUp, Brevo) in its own `gobreaker.CircuitBreaker`. |
| `github.com/redis/go-redis/v9` | v9.18.0 `[VERIFIED: go.mod]` | Pub/Sub subscribe + dedup `SET ... EX` | Already used everywhere. The alerting goroutine subscribes via the existing `redisx.Subscribe*` helpers; dedup is a `SET key val NX EX 300`. |
| Go stdlib `net/http` + `net/smtp` | go 1.24.9 `[VERIFIED: go.mod]` | Outbound HTTP (Chatwoot/ClickUp) + SMTP (Brevo) | No HTTP-client or SMTP library needed. `net/smtp` covers Brevo's plain SMTP-AUTH submission. `httputil.ReverseProxy` is already the gateway's HTTP idiom; plain `http.Client` with a timeout is sufficient for alert POSTs. |

### Core — Dashboard side (Next.js 15 / TS) — greenfield `dashboard/` app

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `next` | `^15.2.0` `[VERIFIED: converseai-v4/apps/web/package.json]` | App Router framework | CONTEXT.md locks "Next.js 15 (App Router)". Match converseai-v4's pinned `^15.2.0` exactly — do NOT use latest (16.2.6) `[VERIFIED: npm]`; the UI-SPEC and converseai-v4 parity both assume 15. |
| `react` / `react-dom` | `^19.0.0` `[VERIFIED: converseai-v4]` | React 19 | Next.js 15 ships with React 19. Recharts 2.15.x needs a `react-is` override under React 19 (see Pitfalls). |
| `better-auth` | `~1.4.18` `[VERIFIED: converseai-v4/packages/auth/package.json]` | Standalone email/password auth | CONTEXT.md locks "Better Auth, configured following the converseai-v4 pattern". Match converseai-v4's pinned `~1.4.18` — NOT latest 1.6.11 `[VERIFIED: npm]`. |
| `recharts` | `2.15.4` `[VERIFIED: converseai-v4]` | Latency percentile / cost time-series charts | UI-SPEC locks "Recharts 2.15.x per converseai-v4 standard". Exact pin `2.15.4`. Latest is 3.8.1 `[VERIFIED: npm]` — do NOT use; shadcn `radix-nova` chart block targets Recharts 2.x. |
| `shadcn` (CLI) | `^3.8.4` `[VERIFIED: converseai-v4]` | Component scaffolding | `npx shadcn init` with the `radix-nova` preset (UI-SPEC §Design System). |
| `radix-ui` | `^1.4.3` `[VERIFIED: converseai-v4]` | Primitive components under shadcn | Per converseai-v4 `components.json`. |
| `lucide-react` | `^0.564.0` `[VERIFIED: converseai-v4]` | Icons | UI-SPEC locks lucide. |
| `@tanstack/react-query` | `^5.65.0` `[VERIFIED: converseai-v4]` | 5–10s polling of `/admin/*` (CONTEXT.md locks polling, not WebSocket) | converseai-v4 standard for server state. |
| `sonner` | `^2.0.7` `[VERIFIED: converseai-v4]` | Toasts | UI-SPEC component inventory. |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| Drizzle ORM or Better Auth's built-in adapter | — | Better Auth DB persistence | converseai-v4 uses `drizzleAdapter`. For a 4-admin tool, Better Auth's bundled adapter against a small Postgres schema (or even SQLite, since the dashboard is single-replica) is sufficient — **decide in discuss-phase**. The dashboard's auth DB is SEPARATE from the gateway's `ai_gateway` schema. |
| `prometheus/promlint` or `promtool` | (CLI, not a dep) | Verify metric naming + sanity-check exposition | Optional — `promtool check metrics` against a curl of `/metrics` is the cardinality-audit tool (see Pitfalls). |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `net/smtp` for Brevo | Brevo transactional **HTTP API** (`api.brevo.com/v3/smtp/email`) | The HTTP API needs an API key + JSON; `net/smtp` needs only SMTP creds. CONTEXT.md says "Brevo SMTP" explicitly → use `net/smtp`. The HTTP API would be a deviation from the locked decision. |
| Re-implementing Chatwoot client in Go | Calling campanhas-chatifix as a proxy | campanhas-chatifix is a separate service with its own concerns; coupling the gateway's alerting to it adds a failure hop. CONTEXT.md says "Chatwoot API" directly → gateway calls Chatwoot directly. |
| Better Auth standalone instance | Shared session with converseai-v4 | Explicitly rejected in CONTEXT.md ("Its own instance — not a shared session"). |
| React Query polling | WebSocket / SSE live push | CONTEXT.md: "polling is the simplest sufficient mechanism" — SC-1 only requires "live", not "real-time push". |
| Bumping `client_golang` to v1.23.2 | Stay on v1.20.5 | No Phase 7 feature needs the bump; it would be unrelated churn that risks the `go test -race` gate. |

**Installation (dashboard app only — gateway adds zero Go deps):**
```bash
# inside the new dashboard/ directory
npx create-next-app@15 dashboard --typescript --app --no-tailwind  # then add tailwind per shadcn
npx shadcn@3 init   # choose radix-nova preset, baseColor neutral, cssVariables true
npm install better-auth@~1.4.18 recharts@2.15.4 @tanstack/react-query@^5.65.0 \
            lucide-react@^0.564.0 sonner@^2.0.7
npx shadcn@3 add card table badge alert button tabs select popover calendar \
                  sidebar skeleton sonner separator scroll-area chart
```

**Version verification performed this session:**
- `better-auth`: latest `1.6.11` `[VERIFIED: npm view]` — but use `~1.4.18` to match converseai-v4.
- `recharts`: latest `3.8.1` `[VERIFIED: npm view]` — but use `2.15.4` to match converseai-v4 + shadcn radix-nova.
- `next`: latest `16.2.6` `[VERIFIED: npm view]` — but use `^15.2.0` to match converseai-v4.
- `prometheus/client_golang`: latest `v1.23.2` (2025-09-05) `[VERIFIED: proxy.golang.org]` — gateway stays on `v1.20.5`.

## Architecture Patterns

### System Architecture Diagram

```
                          ┌─────────────────────────────────────────────────┐
                          │              ifix-ai-gateway (Go)               │
                          │                                                 │
  request traffic ──────► │  chi router ──► obs.RequestsMiddleware           │
                          │                  │  (records RequestsTotal +     │
                          │                  │   NEW latency histogram)      │
                          │                  ▼                              │
                          │              proxy / dispatcher                 │
                          │                                                 │
                          │  ┌───────────────────────────────────────────┐  │
                          │  │ Phases 3/5/6 publish to Redis Pub/Sub:     │  │
                          │  │   gw:breaker:events  gw:shed:events        │  │
                          │  │   gw:emerg:events                          │  │
                          │  └───────────────┬───────────────────────────┘  │
                          │                  │                              │
                          │   NEW: alert goroutine (go alerter.Run(ctx))     │
                          │     subscribes all 3 channels ──► classify       │
                          │     severity (critical/warning/info) ──►         │
                          │     dedup check (Redis SET NX EX 300) ──►        │
                          │     fan-out by channel matrix:                   │
                          │       critical → Chatwoot + ClickUp + Brevo      │
                          │       warning  → ClickUp + Brevo                 │
                          │       info     → (no external; dashboard banner) │
                          │                                                 │
                          │   /metrics  ◄── promhttp.Handler() (existing)    │
                          │   /admin/metrics (NEW) ◄─┐                       │
                          │   /admin/usage (existing)│ X-Admin-Key bcrypt mw │
                          │   /admin/audit  (NEW)  ◄─┘                       │
                          │                                                 │
                          │   audit.Writer (existing async batch) ◄── NEW    │
                          │     event-type rows: fsm_transition,             │
                          │     tenant_activate, pod_lifecycle, threshold    │
                          │                                                 │
                          │   obs.Init → Sentry BeforeSend (extend: scrub    │
                          │     bodies, not just headers)                    │
                          └──────┬───────────────────┬────────────┬──────────┘
                                 │                   │            │
              ┌──────────────────┘         ┌─────────┘     ┌──────┘
              ▼                            ▼               ▼
      Chatwoot API              ClickUp API v2        Brevo SMTP
   POST /api/v1/accounts/      POST /list/{id}/task   smtp-relay.brevo.com:587
   {acct}/conversations        Authorization: <token> SMTP-AUTH
   header: api_access_token    X-RateLimit-* headers
              ▲
              │ Postgres audit_log (partitioned, ai_gateway schema)
              │       ▲
   ┌──────────┴───────┴──────────────────────────────────┐
   │          dashboard/ (Next.js 15 App Router)         │
   │                                                     │
   │  Browser ──► Next.js server (RSC + route handlers)  │
   │                │                                    │
   │                ├─ Better Auth (/api/auth/[...all])  │
   │                │   email/password, own DB schema    │
   │                │   middleware: redirect unauthed    │
   │                │                                    │
   │                └─ server-side fetch to gateway      │
   │                    /admin/metrics, /admin/usage,    │
   │                    /admin/audit  (X-Admin-Key stays │
   │                    server-side, NEVER in browser)   │
   │                                                     │
   │  Client components: React Query polls Next.js       │
   │   route handlers every 5–10s; Recharts renders      │
   │   P50/P95/P99 + cost; shadcn table/badge/alert      │
   └─────────────────────────────────────────────────────┘
```

### Recommended Project Structure

**Gateway additions (extend existing packages — minimal new dirs):**
```
gateway/internal/
├── obs/
│   ├── metrics.go        # EXTEND: add latency HistogramVec (route,upstream,tenant)
│   ├── sentry.go         # EXTEND: BeforeSend also scrubs event.Request.Data + Response
│   └── middleware.go     # EXTEND: record the new latency histogram
├── audit/
│   └── writer.go         # EXTEND: add EventKind field + WriteStateChange() helper
├── admin/
│   ├── metrics.go        # NEW: GET /admin/metrics JSON handler
│   └── audit.go          # NEW: GET /admin/audit JSON handler (paginated audit_log read)
└── alert/                # NEW package — the alerting goroutine + clients
    ├── alerter.go        # Run(ctx): subscribes 3 Pub/Sub channels, classify, dedup, fan-out
    ├── severity.go       # event → severity tier mapping + channel matrix
    ├── dedup.go          # Redis SET NX EX 300 fingerprint dedup
    ├── chatwoot.go       # Go Chatwoot Application API client (gobreaker-wrapped)
    ├── clickup.go        # Go ClickUp client (rate-limiter + gobreaker + backoff)
    └── brevo.go          # net/smtp sender
```
```
gateway/db/
├── migrations/
│   └── 0020_audit_log_event_kind.sql   # NEW: ALTER audit_log ADD COLUMN event_kind TEXT
└── queries/
    └── audit.sql                       # EXTEND: add SELECT for /admin/audit pagination
```

**Dashboard (greenfield):**
```
dashboard/
├── components.json              # radix-nova preset, copied from converseai-v4
├── src/
│   ├── app/
│   │   ├── api/auth/[...all]/route.ts   # toNextJsHandler(auth)
│   │   ├── api/gateway/[...path]/route.ts # server-side proxy → gateway /admin/* (holds X-Admin-Key)
│   │   ├── login/page.tsx
│   │   ├── (dashboard)/
│   │   │   ├── layout.tsx       # sidebar + critical-event banner
│   │   │   ├── page.tsx         # Overview: KPI row + FSM panel + charts
│   │   │   ├── tenants/page.tsx
│   │   │   └── incidents/page.tsx  # audit-log / incident history
│   │   └── globals.css          # radix-nova .dark tokens + --status-warning
│   ├── lib/
│   │   ├── auth.ts              # betterAuth({ emailAndPassword: { enabled: true } })
│   │   ├── auth-client.ts       # createAuthClient
│   │   └── gateway.ts           # typed fetch wrappers for /admin/* (server-only)
│   ├── middleware.ts            # getSessionCookie → redirect unauthed to /login
│   └── components/
│       ├── ui/                  # shadcn blocks
│       ├── kpi-card.tsx
│       ├── latency-chart.tsx    # Recharts LineChart, 3 percentile series
│       ├── fsm-panel.tsx
│       └── critical-banner.tsx
```

### Pattern 1: Alerting goroutine — mirror the existing `Subscribe` loop

**What:** A long-lived goroutine spawned in `main.go` next to `go breakerSet.Subscribe(ctx)` and `go emergReconciler.Run(ctx)`. It subscribes to all three Pub/Sub channels, classifies each event into a severity tier, checks a Redis dedup key, and fans out.
**When to use:** This is the OBS-04/05/06 core.
**Example (pattern from `gateway/internal/breaker/subscribe.go`, verified in-repo):**
```go
// Source: gateway/internal/breaker/subscribe.go (existing canonical pattern)
func (a *Alerter) Run(ctx context.Context) {
	log := a.log.With("subsystem", "alerter")
	for {
		if err := ctx.Err(); err != nil { return }
		ps := redisx.SubscribeBreakerEvents(ctx, a.rdb) // + shed + emerg channels
		ch := ps.Channel()
		for {
			select {
			case <-ctx.Done():
				_ = ps.Close(); return
			case msg, ok := <-ch:
				if !ok { goto reconnect }
				a.handle(ctx, msg) // classify → dedup → fan-out
			}
		}
	reconnect:
		_ = ps.Close()
		log.Warn("pubsub closed; reconnecting")
		select {
		case <-ctx.Done(): return
		case <-time.After(1 * time.Second):
		}
	}
}
```
**Note:** `redis.PubSub` can subscribe to multiple channels on one connection: `rdb.Subscribe(ctx, "gw:breaker:events", "gw:shed:events", "gw:emerg:events")`. `msg.Channel` tells you which fired. `[CITED: github.com/redis/go-redis/v9]`

### Pattern 2: Alert dedup — Redis `SET NX EX`

**What:** Each alert has a fingerprint (e.g. `crit:local-llm:gpu-down`). Before sending, `SET fingerprint 1 NX EX 300`; if the key already existed (NX fails), the alert is a duplicate within the 5-min window — skip the external send (still raise the dashboard banner / log).
**Example:**
```go
// Source: go-redis v9 SetNX semantics [CITED: github.com/redis/go-redis/v9]
ok, err := rdb.SetNX(ctx, "gw:alert:dedup:"+fingerprint, "1", 5*time.Minute).Result()
if err != nil {
	// fail-OPEN for alerting: a Redis hiccup must not silence a critical page.
	// Send the alert anyway, log the dedup failure.
}
if !ok {
	// duplicate within window — skip external channels, still log
	return
}
```
**Decision needed:** dedup fail-open vs fail-closed. Recommend **fail-open** for `critical` (better a duplicate page than a missed one) and **fail-closed** for `warning`/`info` (alert fatigue is the larger risk). Flag for discuss-phase.

### Pattern 3: Go ClickUp client — re-implement the cobrancas-api resilience stack

**What:** The cobrancas-api SDK (`AdaptiveRateLimiter` + `CircuitBreaker` + `withRetry`) re-expressed in Go using already-vendored libs.
**Mapping:**
| cobrancas-api (TS) | Go equivalent (already in go.mod) |
|--------------------|-----------------------------------|
| `CircuitBreaker` class | `sony/gobreaker/v2` — one breaker per external service |
| `withRetry` exponential backoff | `cenkalti/backoff/v5` |
| `AdaptiveRateLimiter` reading `X-RateLimit-*` | small custom struct: read `X-RateLimit-Remaining`/`X-RateLimit-Reset` after each response, sleep before next call if remaining ≤ 0 |
| Retryable classification (5xx/429 retry, 4xx throw) | same logic in the `backoff.Operation` — return `backoff.Permanent(err)` for 4xx-except-429 |
**ClickUp API facts `[VERIFIED: cobrancas-api/src/lib/clickup/client.ts + developer.clickup.com]`:**
- Create task: `POST https://api.clickup.com/api/v2/list/{list_id}/task`, body `{ name, description?, status?, priority?, tags?, ... }`.
- Auth: static personal token in the `Authorization` header (NOT `Bearer` — raw token). ClickUp tokens are not refreshable → 401 is non-retryable.
- Rate limit: 100 req/min/token on Free/Unlimited/Business plans `[CITED: developer.clickup.com/docs/rate-limits]`. Returns `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset` (Unix seconds) on every response.

### Pattern 4: Go Chatwoot client — Application API, mirror campanhas-chatifix

**What:** Send a WhatsApp message by creating a conversation with an embedded initial message via the Chatwoot **Application API** (the agent-side API, NOT the public client API).
**Chatwoot API facts `[VERIFIED: campanhas-chatifix/packages/backend/src/dispatch/message.sender.ts]`:**
- Endpoint: `POST {CHATWOOT_URL}/api/v1/accounts/{account_id}/conversations`
- Auth header: `api_access_token: <token>` (plus `Content-Type: application/json`)
- Body shape (free-form text):
  ```json
  {
    "inbox_id": <inbox_id>,
    "contact_id": <contact_id>,
    "status": "open",
    "message": { "content": "ALERTA: GPU primária indisponível há 45s. Failover ativo." }
  }
  ```
- Response: `{ "id": <conversation_id>, ... }`
- To post a follow-up message into an existing conversation: `POST /api/v1/accounts/{account_id}/conversations/{conversation_id}/messages` with `{ "content": "...", "message_type": "outgoing" }`.
- The Ifix Chatwoot host is `https://crm.ifixtelecom.com.br` (env `CHATWOOT_API_URL` / `CHATWOOT_API_TOKEN` in campanhas-chatifix). `[VERIFIED: campanhas-chatifix/packages/backend/src/config.ts]`
- **Open question for discuss-phase:** which `account_id` / `inbox_id` / `contact_id` does the on-call operator map to? campanhas-chatifix resolves these per-campaign; Phase 7 needs a fixed "on-call operator" contact + inbox. This MUST be config (env vars), and the values must be obtained from the Ifix Chatwoot admin.
- **Note:** The public docs at developers.chatwoot.com describe `/public/api/v1/inboxes/{identifier}/...` — that is the *client widget* API, NOT the one to use. The Application API (`/api/v1/accounts/...`) is the correct path, confirmed by the working campanhas-chatifix implementation. `[VERIFIED: in-repo reference]`

### Pattern 5: Brevo SMTP from Go — `net/smtp`

**What:** Transactional email via Brevo's SMTP relay.
**Facts `[VERIFIED: converseai-v4/packages/email/src/send.ts shows the SMTP env pattern]`:**
- converseai-v4 uses `nodemailer` against `SMTP_HOST`/`SMTP_PORT`(587)/`SMTP_USER`/`SMTP_PASS`, `secure: false` (STARTTLS on 587).
- Brevo SMTP relay host is `smtp-relay.brevo.com:587` `[ASSUMED — confirm exact host + creds with Ifix; converseai-v4 reads them from env so the host is deploy-config, not code]`.
- Go: `net/smtp.SendMail(addr, smtp.PlainAuth("", user, pass, host), from, to, msg)` — STARTTLS is handled by `SendMail` when the server advertises it on 587.
**Recommendation:** wrap the send in a `gobreaker` breaker + a short `backoff` retry, same as the other two clients. Email is the least latency-sensitive channel, so a 2-3 retry budget is fine.

### Pattern 6: `/admin/metrics` JSON handler — clone `/admin/usage`

**What:** A new handler struct in `gateway/internal/admin/metrics.go` that mirrors `UsageHandler` (`gateway/internal/admin/usage.go`): a struct with injected queries/deps, a `ServeHTTP`, registered under the existing `/admin` sub-router in `buildRouter` (`main.go:947`).
**Data sources for the JSON:** P50/P95/P99 per route+upstream comes from the latency histogram (new) — but a Prometheus `HistogramVec` is not directly queryable for quantiles inside the process. **This is the key design decision** (see "Per-tenant percentiles" below). The FSM state comes from `emerg.FSM.State()` (lockless atomic read, already exposed). Inflight comes from the `GatewayInflight` gauge. Error rate is derivable from `RequestsTotal`.

### Anti-Patterns to Avoid

- **Putting the gateway's `X-Admin-Key` in browser-reachable code.** The dashboard must proxy every `/admin/*` call through a Next.js server route handler or server component. A client-side `fetch` with the admin key leaks it to anyone with devtools. UI-SPEC already says the dashboard is read-only; keep the key server-side.
- **Adding `request_id` (or any UUID) as a Prometheus label.** Instant cardinality explosion. The existing collectors already avoid this; the new latency histogram must too.
- **Using a Prometheus `Summary` for per-tenant latency.** Summaries compute quantiles in-process but are NOT aggregatable across replicas and are expensive. Use a `Histogram` with explicit buckets (the gateway already does this for `gateway_probe_duration_ms`).
- **Re-implementing retry/backoff/circuit-breaker by hand in the alert clients.** `cenkalti/backoff/v5` and `sony/gobreaker/v2` are already vendored and already used by Phase 3 — use them.
- **Blocking the Pub/Sub consumer loop on a slow external API.** If Chatwoot is down, a synchronous send in the `handle()` path stalls the consumer and backs up the channel. Fan-out sends should be dispatched to a small worker pool or bounded goroutine per channel, mirroring the `MakePublishTransition` bounded-worker pattern Phase 5 already uses.
- **Bumping Next.js/React/Recharts/better-auth to latest.** The UI-SPEC and converseai-v4 parity assume specific majors. Match converseai-v4's pins exactly.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Exponential backoff for alert retries | Custom `time.Sleep` loop | `cenkalti/backoff/v5` (already in go.mod) | Jitter, max-elapsed-time, `Permanent` error handling — all edge cases the Phase 3 code already relies on. |
| Circuit breaker for Chatwoot/ClickUp/Brevo | Custom failure-count struct | `sony/gobreaker/v2` (already in go.mod) | Half-open probing, state callbacks — Phase 3 already uses it for upstreams. |
| Quantile/percentile computation | Custom ring buffer + sort | Prometheus `Histogram` buckets; for the `/admin/metrics` JSON, either (a) read the histogram and approximate quantiles from buckets, or (b) reuse the Phase 5 latency ring buffer (`gateway_p95_request_ms` is already derived from it) | The gateway ALREADY has a per-upstream P95 ring buffer from Phase 5 (`GatewayP95RequestMs`, `SHED_LATENCY_RING_SIZE`). Extend that, don't build a parallel one. |
| Prometheus exposition format | Custom `/metrics` text writer | `promhttp.Handler()` (already wired `main.go:842`) | — |
| Async batched DB writes for audit | New writer | `gateway/internal/audit/Writer` (existing) | Already does 500-row/1s batching with backpressure + drop counter. |
| Auth for the dashboard | Custom session/cookie code | `better-auth` `emailAndPassword` | CONTEXT.md locks it. |
| Email templating | String concat | `@react-email/components` (converseai-v4 uses it) OR plain text — for a 4-admin alert email, plain text is fine | Alert emails are operational, not marketing — plain text body is acceptable and simpler. |
| Charts | Custom SVG | Recharts (`LineChart`/`AreaChart`) via shadcn `chart` block | UI-SPEC locks it. |

**Key insight:** Almost every "hard" part of Phase 7 already exists in the gateway codebase or a sibling repo. The risk in this phase is *not* missing capability — it's accidentally building a second copy of something that already exists (a second latency tracker, a second retry helper, a second circuit breaker). Every Phase 7 task should start by grepping for the existing thing.

## Runtime State Inventory

> Phase 7 is mostly additive (new handlers, new goroutine, one additive migration). It is NOT a rename/refactor. This section is included because it touches deploy config and a DB migration.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | `audit_log` partitioned table (Postgres `ai_gateway` schema) — Phase 7 adds an `event_kind` column via migration 0020. Existing rows get `NULL`/default. Better Auth needs its OWN tables (user/session/account) in a separate schema or DB. | Additive migration for `event_kind` (nullable, no backfill needed). New Better Auth schema — run `npx @better-auth/cli migrate` against the dashboard's DB. |
| Live service config | Chatwoot needs a designated on-call `account_id`/`inbox_id`/`contact_id`; ClickUp needs a target `list_id`; both need API tokens. These live in the Ifix Chatwoot/ClickUp UIs, NOT in git. | Obtain values from Ifix admins; add as Portainer stack env vars. **Blocking for execution — see Open Questions.** |
| OS-registered state | None — the gateway runs as a Docker Swarm service; the dashboard will be a new service in the same Portainer stack. No OS-level registration. | None — verified: deploy is Portainer stack + GitHub webhook (CLAUDE.md Dev Environment). |
| Secrets/env vars | NEW gateway env vars: `CHATWOOT_API_URL`, `CHATWOOT_API_TOKEN`, `CHATWOOT_ONCALL_INBOX_ID`, `CHATWOOT_ONCALL_CONTACT_ID`, `CLICKUP_API_TOKEN`, `CLICKUP_ALERT_LIST_ID`, `BREVO_SMTP_HOST`, `BREVO_SMTP_PORT`, `BREVO_SMTP_USER`, `BREVO_SMTP_PASS`, `ALERT_EMAIL_TO`, `ALERT_EMAIL_FROM`. NEW dashboard env vars: `BETTER_AUTH_SECRET`, `BETTER_AUTH_URL`, `GATEWAY_ADMIN_KEY`, `GATEWAY_BASE_URL`, dashboard DB DSN. | Add all to the Portainer stack UI BEFORE deploy. Follow the gateway's existing config pattern: optional vars empty = feature disabled with a WARN (e.g. empty `CHATWOOT_API_TOKEN` → Chatwoot channel disabled, not fail-boot). |
| Build artifacts | New `dashboard/` app needs its own Dockerfile + a new GitHub Actions build job + a new Portainer service entry + a new GHA self-hosted runner is NOT needed (the existing converseai-v4 runners on the dev VM don't apply — this repo is `IfixTelecom/gpu-ifix`, which already has a Vast.ai runner per CLAUDE.md; a Next.js build runs fine on `ubuntu-latest`). | New `.github/workflows/build-dashboard.yml`; new service block in `gateway/docker-compose.yml` (or a sibling compose file); new `dashboard/Dockerfile`. |

## Common Pitfalls

### Pitfall 1: Per-tenant latency percentiles vs cardinality budget
**What goes wrong:** Naively adding `tenant` + `route` + `upstream` labels to a latency `HistogramVec` with, say, 12 buckets = `6 tenants × 4 routes × 6 upstreams × (12 buckets + _count + _sum)` ≈ 2000 series for ONE metric. Add a few more label combos and the 10k budget is gone.
**Why it happens:** Histograms multiply: every label-value combination gets its own full set of bucket series.
**How to avoid:**
- Do NOT cross `tenant × route × upstream` on the same histogram. Pick the minimum label set per metric. For OBS-01's "P50/P95/P99 per route and per upstream" + per-tenant on the dashboard: consider TWO narrower histograms — one labelled `{route}` (4 values) and one labelled `{upstream}` (6 values) — and expose per-tenant latency via the **existing Phase 5 ring buffer** (`GatewayP95RequestMs{upstream}`) or via an `/admin/metrics` JSON computed from `audit_log` (which already has `tenant_id` + `latency_ms` per row), NOT via a Prometheus label.
- Keep bucket count low: 8–10 buckets covering the realistic latency range (the gateway already uses 7 buckets for `gateway_probe_duration_ms`).
- The `/admin/metrics` JSON (dashboard's data source) can compute true per-tenant P50/P95/P99 from `audit_log` SQL (`percentile_cont` over `latency_ms` grouped by `tenant_id`) — Postgres does percentiles natively, no cardinality cost.
**Warning signs:** `count(group({__name__=~"gateway_.*"})) by (__name__)` climbing; `promtool check metrics` output growing.

### Pitfall 2: Sentry `BeforeSend` only scrubs headers, not bodies
**What goes wrong:** The current `obs/sentry.go` `BeforeSend` redacts `event.Request.Headers` and clears `Cookies`, but does NOT touch `event.Request.Data` (request body) or any response body captured into the event. OBS-08 explicitly requires payload-body redaction.
**Why it happens:** Phase 2 only needed header redaction; bodies weren't captured then.
**How to avoid:** Extend `BeforeSend` to also handle `event.Request.Data`. For a panic that captured a chat request, `Data` may contain the full prompt JSON. Safest: for `data_class=sensitive` tenants, drop `event.Request.Data` entirely; for `normal`, either drop it or scrub known sensitive JSON keys. Also scrub `event.Extra` and breadcrumb data if any handler stuffs payloads there. The codebase already has `httpx.IsSensitiveKey` and a `sensitiveKeys` map — reuse it.
**Warning signs:** A test that constructs a `sentry.Event` with a populated `Request.Data` and asserts the secret string is absent post-`BeforeSend`.

### Pitfall 3: Recharts 2.15 + React 19 peer-dependency break
**What goes wrong:** `npm install recharts@2.15.4` under React 19 fails or renders blank charts because `react-is` resolves to a React-18 version.
**Why it happens:** Recharts 2.x peer-depends on an older `react-is`.
**How to avoid:** Add an `overrides` (npm) / `resolutions` (bun) block pinning `react-is` to the React 19 line. shadcn documents this exact workaround. `[CITED: ui.shadcn.com/docs/react-19]` converseai-v4 already ships `recharts@2.15.4` under `react@^19` — copy whatever override block converseai-v4's root `package.json` uses.
**Warning signs:** Blank `<LineChart>`, console error about `react-is`, or `npm install` peer-dep error.

### Pitfall 4: Pub/Sub is at-most-once — boot-window events are lost
**What goes wrong:** If the alerter goroutine is spawned AFTER the breaker/shed/emerg subsystems start publishing, any transition that fires during the boot gap is silently lost — including a critical one.
**Why it happens:** Redis Pub/Sub has no replay; a message published with no subscriber is gone.
**How to avoid:** Spawn `go alerter.Run(ctx)` EARLY in `main.go` — before the subsystems that publish, mirroring the W11 ordering invariant the emerg reconciler already documents ("consumers spawned BEFORE the reconciler ticker"). Additionally, on alerter boot, do one reconciliation read of the current FSM state (`gw:emerg:state` Hash, `gw:breaker:{name}` Hashes) so a gateway restart during an active incident still surfaces the banner.
**Warning signs:** Alerts that "should have fired" during a deploy window don't.

### Pitfall 5: Alerter goroutine stalls on a slow/dead external API
**What goes wrong:** Chatwoot is down; the `handle()` function does a synchronous POST with a 30s timeout; the Pub/Sub consumer loop is blocked for 30s per event; the channel buffer backs up; events are dropped.
**Why it happens:** Coupling the consume loop to the send loop.
**How to avoid:** The consume loop should classify + dedup + enqueue to a bounded per-channel worker (or a buffered channel with a small worker pool). Each external client has its own `gobreaker` so a dead Chatwoot opens its breaker and fails fast instead of timing out. Phase 5's `MakePublishTransition` bounded-worker pattern is the in-repo precedent.
**Warning signs:** A `gateway_alert_dropped_total` counter (add one) going non-zero; consumer lag.

### Pitfall 6: ClickUp 401 retried forever
**What goes wrong:** ClickUp tokens are static. A bad token returns 401. A naive retry loop retries the 401 indefinitely (or up to max-elapsed), wasting time and never succeeding.
**Why it happens:** Treating all errors as retryable.
**How to avoid:** Classify like cobrancas-api's `withRetry`: 4xx (except 429) → `backoff.Permanent(err)` (stop immediately); 429 → retry honoring `X-RateLimit-Reset`; 5xx + network → retry with backoff. `[VERIFIED: cobrancas-api/src/lib/clickup/utils/retry.ts]`
**Warning signs:** Repeated 401s in logs; alert tasks never created.

### Pitfall 7: Better Auth DB colliding with the gateway's `ai_gateway` schema
**What goes wrong:** Pointing Better Auth's `drizzleAdapter` at the gateway's Postgres + `ai_gateway` schema, polluting it with `user`/`session`/`account` tables, or worse, a name collision.
**Why it happens:** "It's the same Postgres cluster, reuse it."
**How to avoid:** Better Auth gets its OWN schema (e.g. `dashboard_auth`) or its OWN small database. CONTEXT.md already says the instance is standalone. For a 4-admin tool, even SQLite-on-a-volume is defensible since the dashboard is single-replica — decide in discuss-phase, but keep it isolated from `ai_gateway`.
**Warning signs:** Migration order conflicts; the gateway's `sqlc` codegen picking up Better Auth tables.

### Pitfall 8: `audit_log` is partitioned by month — new event-type rows still need a partition
**What goes wrong:** Writing FSM-transition audit rows works fine now, but the partition only exists for the current + next 2 months (seeded by migration 0003). The partition-roll command (`gatewayctl`, plan 02-09) was deferred. If audit rows are written past the seeded window with no partition, the insert fails.
**Why it happens:** Partitioned tables reject rows with no matching partition.
**How to avoid:** This is a pre-existing gateway concern, not new to Phase 7 — but Phase 7 increases audit write volume (state-change rows in addition to per-request rows). Confirm the partition-roll story before Phase 7 ships, or note it as a known limitation. The deferred 02-09 cold-storage export is explicitly out of Phase 7 scope per CONTEXT.md `<deferred>`, but partition *creation* (vs *dropping*) is a different concern — flag for the planner.
**Warning signs:** `no partition of relation "audit_log" found for row` errors after a few months.

## Code Examples

### Latency histogram with bounded labels (gateway side)
```go
// Source: pattern from existing gateway/internal/obs/metrics.go ProbeDurationMs
// Two narrow histograms instead of one wide one — keeps cardinality bounded.
var RequestDurationByRoute = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "gateway_request_duration_ms_by_route",
	Help:    "Request duration in ms, labelled by route template only.",
	Buckets: []float64{25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
}, []string{"route"}) // 4 route values × 9 buckets ≈ 44 series

var RequestDurationByUpstream = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "gateway_request_duration_ms_by_upstream",
	Help:    "Request duration in ms, labelled by upstream only.",
	Buckets: []float64{25, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
}, []string{"upstream"}) // 6 upstream values × 9 buckets ≈ 60 series
// Per-tenant P50/P95/P99 is computed in /admin/metrics from audit_log SQL,
// NOT from a Prometheus label — see Pitfall 1.
```

### Per-tenant percentiles from audit_log (no cardinality cost)
```sql
-- Source: Postgres percentile_cont — for the /admin/metrics JSON handler.
-- audit_log already has tenant_id + latency_ms per row.
SELECT
  tenant_id,
  route,
  percentile_cont(0.50) WITHIN GROUP (ORDER BY latency_ms) AS p50,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms) AS p95,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY latency_ms) AS p99,
  count(*)                                                  AS requests,
  count(*) FILTER (WHERE status_code >= 500)::float / count(*) AS error_rate
FROM ai_gateway.audit_log
WHERE ts >= NOW() - INTERVAL '5 minutes'
GROUP BY tenant_id, route;
```

### Sentry BeforeSend extended to scrub bodies
```go
// Source: extends existing gateway/internal/obs/sentry.go BeforeSend
BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event.Request != nil {
		for k := range event.Request.Headers {
			if httpx.IsSensitiveKey(k) {
				event.Request.Headers[k] = "***REDACTED***"
			}
		}
		event.Request.Cookies = ""
		// NEW (OBS-08): request body may contain prompts / api keys.
		if event.Request.Data != "" {
			event.Request.Data = "***REDACTED***" // safest: drop entirely
		}
	}
	// NEW: scrub Extra / Contexts if any handler stuffed payloads there.
	delete(event.Extra, "request_body")
	delete(event.Extra, "response_body")
	return event
},
```

### Better Auth standalone instance (dashboard side) — minimal
```ts
// Source: Context7 /better-auth/better-auth + converseai-v4 pattern, stripped to 1 plugin
// dashboard/src/lib/auth.ts
import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { db, schema } from "./db"; // dashboard's OWN db, NOT ai_gateway

export const auth = betterAuth({
  baseURL: process.env.BETTER_AUTH_URL,
  database: drizzleAdapter(db, { provider: "pg", schema }),
  emailAndPassword: { enabled: true }, // 4 admins, no email verification needed for internal tool
  session: { expiresIn: 60 * 60 * 24 * 7 },
  advanced: { database: { generateId: "uuid" } },
});
export type Auth = typeof auth;
```
```ts
// dashboard/src/app/api/auth/[...all]/route.ts
import { toNextJsHandler } from "better-auth/next-js";
import { auth } from "@/lib/auth";
export const { POST, GET } = toNextJsHandler(auth);
```
```ts
// dashboard/src/middleware.ts — redirect unauthed
import { getSessionCookie } from "better-auth/cookies";
import { NextRequest, NextResponse } from "next/server";
export function middleware(req: NextRequest) {
  const session = getSessionCookie(req);
  if (!session && !req.nextUrl.pathname.startsWith("/login"))
    return NextResponse.redirect(new URL("/login", req.url));
  return NextResponse.next();
}
export const config = { matcher: ["/((?!login|api/auth|_next|favicon).*)"] };
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `toNextJsHandler` from `better-auth/next-js` | Still current in 1.4.x | — | The converseai-v4 pattern is current; no change needed. |
| Recharts 2.x | Recharts 3.x released | 2025 | 3.x exists but shadcn `radix-nova` chart block + converseai-v4 both target 2.15.x — stay on 2.15.4. |
| `prometheus/client_golang` v1.20 | v1.23.2 | 2025-09 | Newer version exists; nothing in Phase 7 requires it. Stay on v1.20.5. |
| Next.js 15 | Next.js 16 released | 2026 | 16 exists; converseai-v4 + UI-SPEC assume 15. Stay on `^15.2.0`. |

**Deprecated/outdated:**
- Chatwoot `/public/api/v1/inboxes/...` (the widget/client API) — NOT deprecated, but it is the WRONG API for agent-side message sending. Use the Application API `/api/v1/accounts/...`.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Brevo SMTP relay host is `smtp-relay.brevo.com:587` | Pattern 5 | LOW — converseai-v4 reads SMTP host from env, so the host is deploy-config not code; just need the right value at deploy time. Confirm with Ifix infra. |
| A2 | The dashboard can use a separate small DB (Postgres schema or SQLite) for Better Auth, isolated from `ai_gateway` | Standard Stack / Pitfall 7 | LOW — CONTEXT.md already says "standalone instance"; only the storage substrate is undecided. Discuss-phase decision. |
| A3 | `audit_log` partition creation will not fail within the Phase 7 + integration-test horizon (current + 2 months seeded by migration 0003) | Pitfall 8 | MEDIUM — if Phase 7 runs/tests past the seeded window, audit inserts fail. Partition-roll automation was deferred (02-09). Planner should confirm. |
| A4 | The existing Phase 5 latency ring buffer (`GatewayP95RequestMs`, `SHED_LATENCY_RING_SIZE`) can be extended/read for per-upstream percentiles instead of building a new tracker | Don't Hand-Roll / Pitfall 1 | LOW — worst case, the `/admin/metrics` JSON computes percentiles purely from `audit_log` SQL, which is cardinality-free anyway. |
| A5 | A Next.js dashboard build runs fine on GitHub `ubuntu-latest` (no self-hosted runner needed for the build, unlike the gateway which builds on ubuntu-latest too) | Runtime State Inventory | LOW — the gateway already builds on `ubuntu-latest` per `build-gateway.yml`; a Node build is even lighter. |
| A6 | The Ifix Chatwoot has (or can have) a designated "on-call operator" contact + inbox that the alerter posts into | Pattern 4 / Open Questions | HIGH — if no such contact/inbox exists, the WhatsApp-alert path has nowhere to send. This is the single biggest external-dependency unknown. |
| A7 | A single ClickUp `list_id` is an acceptable target for all alert tasks (critical + warning) | Pattern 3 / Open Questions | LOW — worst case, two lists (critical / warning). Discuss-phase decision. |

## Open Questions (RESOLVED)

1. **Chatwoot on-call routing target**
   - What we know: campanhas-chatifix calls `POST /api/v1/accounts/{account_id}/conversations` with `inbox_id` + `contact_id`; the Ifix Chatwoot host is `crm.ifixtelecom.com.br`.
   - What's unclear: which `account_id`, `inbox_id`, and `contact_id` represent "the on-call operator's WhatsApp". campanhas-chatifix resolves these per-campaign — Phase 7 needs a fixed, pre-created target.
   - Recommendation: discuss-phase must get these three IDs (or a contact phone number to create-or-lookup) from the Ifix Chatwoot admin. Treat as env config; empty token = Chatwoot channel disabled with a WARN (gateway's established optional-feature pattern).
   - **RESOLVED:** Treated as optional env vars (`CHATWOOT_ONCALL_ACCOUNT_ID`, `CHATWOOT_INBOX_ID`, `CHATWOOT_CONTACT_ID`, plus `CHATWOOT_API_TOKEN`); empty = Chatwoot channel disabled with a WARN log per the gateway optional-feature pattern. The specific IDs are an operator prerequisite captured in 07-HUMAN-UAT.md (plan 07-09), obtained from the Ifix Chatwoot admin before deploy.

2. **ClickUp target list + token**
   - What we know: Create task = `POST /list/{list_id}/task`, auth = raw token in `Authorization`.
   - What's unclear: which ClickUp list receives alert tasks; whether critical and warning go to the same list; which token (a dedicated integration token vs a personal token).
   - Recommendation: get a dedicated ClickUp list + a service token from Ifix. Default to one list; revisit if noise.
   - **RESOLVED:** `CLICKUP_ALERT_LIST_ID` and `CLICKUP_API_TOKEN` are optional env vars; empty = ClickUp channel disabled with a WARN. Single target list for v1 (revisit if noise). Token + list ID come from the Ifix team — an operator prerequisite in 07-HUMAN-UAT.md.

3. **Brevo SMTP credentials + the `ALERT_EMAIL_TO` distribution**
   - What we know: converseai-v4 uses `SMTP_HOST`/`SMTP_PORT`/`SMTP_USER`/`SMTP_PASS`; Brevo is the standard Ifix relay.
   - What's unclear: exact relay host, the sending identity, and which email address(es) the ~4 operators monitor.
   - Recommendation: reuse Ifix's existing Brevo SMTP creds; `ALERT_EMAIL_TO` is an env-config comma list.
   - **RESOLVED:** Reuse the standard Ifix Brevo SMTP credentials; `ALERT_EMAIL_TO` is a comma-separated env var. Empty = email channel disabled with a WARN. Operator prerequisite in 07-HUMAN-UAT.md.

4. **Better Auth storage substrate**
   - What we know: standalone instance, converseai-v4 uses `drizzleAdapter` over Postgres.
   - What's unclear: separate Postgres schema in the DO cluster vs separate DB vs SQLite-on-volume (the dashboard is single-replica).
   - Recommendation: separate Postgres schema (`dashboard_auth`) in the existing DO cluster — consistent with the gateway's "shared cluster, dedicated schema" model. Discuss-phase to confirm.
   - **RESOLVED via 07-CONTEXT.md:** Standalone Better Auth instance on a separate Postgres schema (`dashboard_auth`) in the shared DO cluster, isolated from the `ai_gateway` schema (plan 07-07 Task 2).

5. **Dedup fail-open vs fail-closed per severity tier**
   - What we know: dedup = Redis `SET NX EX 300`.
   - What's unclear: behaviour when Redis is unreachable during a dedup check.
   - Recommendation: fail-OPEN for `critical` (never silence a page), fail-CLOSED for `warning`/`info` (alert fatigue). Confirm in discuss-phase.
   - **RESOLVED via 07-CONTEXT.md:** Fail-OPEN for `critical` (never suppress a critical alert on a Redis error), fail-CLOSED for `warning`/`info`. Implemented in plan 07-05 `dedup.go`.

6. **`/admin/metrics` time window**
   - What we know: SC-1 wants "live" P50/P95/P99/error-rate/cost.
   - What's unclear: the rolling window for the percentiles (last 5 min? last 1h? since boot?).
   - Recommendation: a short rolling window (5–15 min) computed from `audit_log` SQL, with the window configurable. The UI-SPEC's date-range filter is for the cost/usage view, separate from the live KPI window.
   - **RESOLVED:** 5-minute rolling window computed from `audit_log` via Postgres `percentile_cont`, configurable via a `?window` query param. Implemented in plan 07-03.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | Gateway build/test | ✓ (CI uses 1.23; go.mod says 1.24.9) | 1.23 (CI) / 1.24.9 (go.mod) | — |
| Node.js + npm | Dashboard build | ✓ on `ubuntu-latest` GHA runner | Node 20+ | — |
| Postgres (DO shared cluster, `ai_gateway` schema) | audit_log read/write | ✓ | 16 + pgvector | — |
| Redis (in-cluster `infra-redis-1`) | Pub/Sub subscribe + dedup TTL | ✓ | 7 | — |
| Sentry | OBS-08 | ✓ (opt-in via `SENTRY_DSN`) | — | Empty DSN = Sentry disabled (existing behaviour) |
| Chatwoot API (`crm.ifixtelecom.com.br`) | OBS-04/05 critical WhatsApp | ✗ token/IDs not yet provisioned | — | Empty `CHATWOOT_API_TOKEN` = channel disabled with WARN; alert still logged + dashboard banner |
| ClickUp API | OBS-04 critical+warning task | ✗ token/list not yet provisioned | — | Empty `CLICKUP_API_TOKEN` = channel disabled with WARN |
| Brevo SMTP | OBS-04/05 email | ✗ creds not yet confirmed for this repo | — | Empty `BREVO_SMTP_*` = email channel disabled with WARN |
| Portainer stack + GitHub webhook | Deploy of gateway + new dashboard service | ✓ | — | — |
| `promtool` (cardinality audit) | OBS-02 verification | ✗ (not installed; optional) | — | `count by (__name__)` PromQL against a curl of `/metrics`, or a Go test that counts registered collectors |

**Missing dependencies with no fallback:** None block *building* Phase 7 — the gateway's established pattern is "empty optional env var = feature disabled with WARN, not fail-boot". But the **three alert channels cannot be runtime-verified** (SC-2: "operator gets a WhatsApp message + email within 60s") without the Chatwoot/ClickUp/Brevo credentials. This makes SC-2 a HUMAN-UAT item, consistent with how Phases 1–6 handled live-deploy verification.

**Missing dependencies with fallback:** All three alert channels degrade gracefully to "log + dashboard banner only" when their credentials are absent — the dashboard, metrics, audit, and Sentry work fully without them.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework (gateway) | Go stdlib `testing` + `-race`; integration tests gated by `//go:build integration` using `testcontainers-go` (Postgres) + `miniredis` |
| Framework (dashboard) | None yet — greenfield. Recommend Vitest + React Testing Library for component logic; Playwright is already a converseai-v4 devDep pattern but heavy for a 4-admin tool |
| Config file (gateway) | none — `go test ./gateway/...`; CI in `.github/workflows/build-gateway.yml` |
| Quick run command | `go test ./gateway/internal/obs/... ./gateway/internal/audit/... ./gateway/internal/admin/... ./gateway/internal/alert/... -race -count=1` |
| Full suite command | `go test ./gateway/... ./pkg/openai/... ./pod/... -count=1 -race -timeout=5m` then `go test -tags=integration ./gateway/internal/integration_test/... -count=1 -timeout=10m` |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| OBS-01 | `/admin/metrics` returns P50/P95/P99/error-rate/inflight/FSM JSON, gated by X-Admin-Key | unit + integration | `go test ./gateway/internal/admin/... -run TestMetrics -race` | ❌ Wave 0 |
| OBS-02 | `/metrics` exposes bounded labels; total series ≤ 10k; no `request_id`-style labels | unit | `go test ./gateway/internal/obs/... -run TestCardinality -race` (count registered collectors + label combos) | ❌ Wave 0 |
| OBS-03 | Dashboard renders per-tenant latency/error/cost/FSM/incident-history | component (Vitest) + manual | `cd dashboard && npm test` (component logic); visual is manual | ❌ Wave 0 |
| OBS-04 | Severity classification + channel matrix (critical→3 channels, warning→2, info→0) | unit | `go test ./gateway/internal/alert/... -run TestSeverity -race` | ❌ Wave 0 |
| OBS-05 | Chatwoot/ClickUp/Brevo clients build correct requests; resilience (breaker/retry/rate-limit) | unit (httptest mock servers) | `go test ./gateway/internal/alert/... -run TestClient -race` | ❌ Wave 0 |
| OBS-06 | Dedup: 2nd identical alert within 5 min is suppressed (Redis SET NX EX) | integration (miniredis) | `go test ./gateway/internal/alert/... -run TestDedup -race` | ❌ Wave 0 |
| OBS-07 | Audit writer emits a row for FSM transition / tenant activate-deactivate / pod lifecycle / threshold change | integration (testcontainers Postgres) | `go test -tags=integration ./gateway/internal/integration_test/... -run TestAuditStateChange` | ❌ Wave 0 |
| OBS-08 | `BeforeSend` removes `authorization`, `x-api-key`, AND request/response bodies from a constructed event | unit | `go test ./gateway/internal/obs/... -run TestBeforeSendScrubsBodies -race` | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** the package-scoped quick run for the package touched (e.g. `go test ./gateway/internal/alert/... -race`)
- **Per wave merge:** full gateway unit suite `go test ./gateway/... -race -count=1`
- **Phase gate:** full unit suite + `-tags=integration` suite green before `/gsd-verify-work`; dashboard `npm run build` + `npm test` green

### Wave 0 Gaps
- [ ] `gateway/internal/admin/metrics_test.go` — covers OBS-01
- [ ] `gateway/internal/admin/audit_test.go` — covers OBS-07 read path
- [ ] `gateway/internal/obs/cardinality_test.go` — covers OBS-02 (assert collector/label budget)
- [ ] `gateway/internal/obs/sentry_test.go` — covers OBS-08 (BeforeSend body scrub) — file may not exist yet
- [ ] `gateway/internal/alert/severity_test.go`, `alert/dedup_test.go`, `alert/chatwoot_test.go`, `alert/clickup_test.go`, `alert/brevo_test.go` — cover OBS-04/05/06
- [ ] `gateway/internal/integration_test/audit_state_change_test.go` — covers OBS-07 write path (testcontainers)
- [ ] `gateway/internal/integration_test/alerter_pubsub_test.go` — covers the Pub/Sub→classify→dedup→fan-out flow end-to-end (miniredis + httptest mock external APIs)
- [ ] Dashboard: `dashboard/vitest.config.ts` + component tests for `latency-chart`, `fsm-panel`, `critical-banner` logic; framework install `npm i -D vitest @testing-library/react`
- [ ] SC-2 (WhatsApp+email within 60s) is a HUMAN-UAT item — cannot be automated without live Chatwoot/ClickUp/Brevo credentials

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Dashboard: Better Auth `emailAndPassword` (bcrypt/scrypt internally). Gateway `/admin/*`: existing `X-Admin-Key` bcrypt middleware. |
| V3 Session Management | yes | Better Auth session cookies (httpOnly, sameSite); 7-day expiry per converseai-v4 pattern. `middleware.ts` enforces auth on all non-login routes. |
| V4 Access Control | yes | All `/admin/*` gateway routes already behind `admin.Middleware`. Dashboard is single-role (4 admins, all equal) — no RBAC needed (Out-of-Scope per REQUIREMENTS.md). |
| V5 Input Validation | yes | `/admin/metrics` + `/admin/audit` query params validated like `/admin/usage` already does (`httpx.WriteOpenAIError` on bad input). Dashboard: validate date-range inputs. |
| V6 Cryptography | yes | No hand-rolled crypto. Better Auth handles password hashing. SMTP over STARTTLS (port 587). Chatwoot/ClickUp over HTTPS. |
| V7 Error Handling & Logging | yes | OBS-08 IS this category — Sentry must not leak secrets/PII; `pino`/`slog` already use `httpx.Redactor`. Audit log is the append-only trail (OBS-07). |
| V9 Communications | yes | All external calls (Chatwoot, ClickUp, Brevo, Sentry) over TLS. Gateway↔dashboard: `X-Admin-Key` only ever travels server-side. |
| V13 API & Web Service | yes | `/admin/metrics` + `/admin/audit` are new API endpoints — same auth + error envelope as `/admin/usage`. |

### Known Threat Patterns for {Go gateway + Next.js dashboard + 3 external integrations}

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Admin key leaked to browser via client-side fetch | Information Disclosure | Dashboard proxies all `/admin/*` calls through Next.js server route handlers; key in server-only env, never `NEXT_PUBLIC_*`. |
| Prompt/PII leaking to Sentry on a panic | Information Disclosure | OBS-08: `BeforeSend` scrubs headers AND bodies AND `Extra`; drop `Request.Data` for sensitive tenants. |
| Alert payloads leaking PII to Chatwoot/ClickUp/email | Information Disclosure | Alert messages carry only non-sensitive IDs/state (tenant slug, upstream name, FSM state, duration) — never request bodies. Same convention `EmergEvent.Payload` already enforces ("non-sensitive IDs only — enforced via code review"). |
| SMTP credentials / API tokens in logs or git | Information Disclosure | All secrets are Portainer stack env vars; never committed (CLAUDE.md rule). `slog` redactor already scrubs sensitive keys. |
| Alert spam / fatigue (DoS on the operators) | Denial of Service | OBS-06 dedup (Redis 5-min TTL) + the gobreaker on each client + a `gateway_alert_dropped_total` counter. |
| Forged Pub/Sub events (operator typo via `redis-cli`) | Tampering | Malformed JSON is logged at WARN and dropped, never crashes the loop — the exact pattern `emerg/subscribe.go` already documents (Threat T-6-W5-02). |
| Better Auth open registration | Spoofing / Elevation | `emailAndPassword.enabled` allows sign-up by default — for a 4-admin tool, DISABLE self-signup or gate it; seed the 4 admin accounts via the Better Auth CLI / a seed script. Flag for discuss-phase. |
| Unauthenticated `/metrics` exposure | Information Disclosure | `/metrics` is intentionally unauthenticated (CONTEXT.md / `main.go:842`) and Prometheus-standard; ensure it carries NO secrets/PII (it carries only counters/gauges/histograms with bounded labels — verify in the OBS-02 cardinality test). |

## Sources

### Primary (HIGH confidence)
- In-repo codebase (verified by direct Read this session): `gateway/internal/obs/{metrics,sentry,middleware}.go`, `gateway/internal/audit/{writer,middleware}.go`, `gateway/internal/admin/{usage,middleware}.go`, `gateway/internal/redisx/{breaker,emerg,shed}.go`, `gateway/internal/breaker/subscribe.go`, `gateway/internal/emerg/{fsm,subscribe,reconciler}.go`, `gateway/cmd/gateway/main.go`, `gateway/internal/config/config.go`, `gateway/db/migrations/0003_create_audit_log_partitioned.sql`, `go.mod`, `.github/workflows/build-gateway.yml`
- In-repo reference implementations (sibling projects): `cobrancas-api/src/lib/clickup/` (client.ts, utils/{rate-limiter,circuit-breaker,retry}.ts) — ClickUp resilient-client pattern; `campanhas-chatifix/packages/backend/src/dispatch/message.sender.ts` + `config.ts` — Chatwoot Application API call shape; `converseai-v4/packages/auth/{index,client}.ts` + `package.json` — Better Auth setup; `converseai-v4/packages/email/src/send.ts` — Brevo SMTP pattern; `converseai-v4/apps/web/{package.json,components.json}` — Next.js 15 / shadcn radix-nova versions
- Context7 `/better-auth/better-auth` — Next.js App Router setup (`toNextJsHandler`, route handler, `getSessionCookie` middleware, `emailAndPassword`)
- developer.clickup.com/docs/rate-limits — ClickUp rate limits (100 req/min/token) + `X-RateLimit-*` headers
- developers.chatwoot.com/llms.txt + api-reference — Chatwoot API structure (note: public vs application API distinction)

### Secondary (MEDIUM confidence)
- ui.shadcn.com/docs/react-19 — Recharts 2.x + React 19 `react-is` override (cross-verified: converseai-v4 ships exactly `recharts@2.15.4` under `react@^19`)
- docs.sentry.io/platforms/go/configuration/filtering — `BeforeSend` for Go; `event.Request.Data` is the request body
- prometheus.io + community sources — histogram cardinality multiplication, `prometheus_tsdb_head_series`, bounded-label best practice (cross-verified against the gateway's own existing collector design comments)

### Tertiary (LOW confidence)
- Brevo SMTP exact relay host (`smtp-relay.brevo.com:587`) — A1 in Assumptions Log; converseai-v4 reads host from env so it's deploy-config, confirm with Ifix infra

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — every gateway-side lib is already in `go.mod`; every dashboard-side lib version is pinned in converseai-v4's `package.json`, verified this session.
- Architecture: HIGH — the alerting goroutine, Pub/Sub subscribe loop, `/admin` handler pattern, and audit writer are all directly modelled on existing, verified in-repo code.
- External integrations: HIGH for call shapes (verified against working in-repo reference implementations), MEDIUM for the specific routing targets/credentials (Open Questions 1–3 — these are provisioning unknowns, not technical unknowns).
- Pitfalls: HIGH — Pitfalls 1, 2, 4, 5, 8 are derived from reading the actual gateway code; Pitfall 3 cross-verified against converseai-v4 + shadcn docs.

**Research date:** 2026-05-14
**Valid until:** 2026-06-13 (30 days — stack is stable; the only volatility is the external-credential provisioning, which is a process unknown not a tech-drift one)
