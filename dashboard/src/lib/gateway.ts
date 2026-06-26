/**
 * Typed fetch wrappers for the gateway admin API.
 *
 * Every wrapper calls the SERVER-SIDE proxy at `/api/gateway/*` — NEVER the
 * gateway directly. The proxy (src/app/api/gateway/[...path]/route.ts) is the
 * only place the gateway admin key is read; these wrappers run in the browser
 * and must stay key-free (threat T-07-24).
 *
 * The response shapes mirror the gateway's 07-03 JSON handlers
 * (`/admin/metrics`, `/admin/audit`) and the existing `/admin/usage`
 * (gateway/internal/admin/usage.go UsageResponse). 07-08 builds the UI
 * against these types.
 */

/** Base path of the server-side proxy. All wrappers go through here. */
const GATEWAY_PROXY_BASE = "/api/gateway";

// --- /admin/metrics (OBS-01) ----------------------------------------------
//
// These interfaces mirror the Go handler `admin.MetricsResponse` /
// `admin.TenantLatencyRow` / `admin.InflightRow` in
// gateway/internal/admin/metrics.go FIELD-FOR-FIELD. The Go handler is the
// source of truth — it is tested and merged. Do not add fields the handler
// does not emit.

/** Per-tenant + per-route latency percentiles and error rate. */
export interface TenantMetricRow {
  tenant_id: string;
  /**
   * Human-readable tenant slug/name from the gateway's LEFT JOIN on
   * ai_gateway.tenants (WR-10). Both are `null` when the audit row's
   * tenant no longer exists in the tenants table — use `tenantLabel`
   * to render, which falls back name → slug → raw UUID.
   */
  tenant_slug: string | null;
  tenant_name: string | null;
  route: string;
  p50: number;
  p95: number;
  p99: number;
  requests: number;
  error_rate: number;
}

/**
 * Operator-facing label for a tenant row (WR-10). Prefers the human name,
 * then the slug, then the raw UUID — an operator triaging an incident
 * should see `ConverseAI`, not `8f1c0d2e-4a5b-…`. The UUID fallback keeps
 * the row identifiable even for a since-deleted tenant.
 */
export function tenantLabel(
  row: Pick<TenantMetricRow, "tenant_id" | "tenant_slug" | "tenant_name">,
): string {
  return row.tenant_name ?? row.tenant_slug ?? row.tenant_id;
}

/** One upstream's current in-flight request count. */
export interface InflightRow {
  upstream: string;
  inflight: number;
}

/**
 * `/admin/metrics` JSON — per-tenant percentiles, per-upstream inflight
 * counts, and the current failover FSM state. Mirrors
 * `admin.MetricsResponse` in gateway/internal/admin/metrics.go.
 */
export interface MetricsResponse {
  window: string;
  fsm_state: string;
  tenants: TenantMetricRow[];
  inflight: InflightRow[];
}

/** Latency keyed by a single dimension (e.g. route). */
export interface LatencyRow {
  key: string;
  p50: number;
  p95: number;
  p99: number;
}

/**
 * Derive per-route latency rows from the per-(tenant,route) percentile
 * rows the gateway emits. The gateway's `/admin/metrics` does NOT ship a
 * `by_route` aggregate, so the dashboard collapses the tenant rows here:
 * for each route, take the worst (max) percentile across tenants — the
 * latency chart is an at-a-glance SLO view, so the worst case is the
 * honest signal.
 */
export function latencyByRoute(tenants: TenantMetricRow[]): LatencyRow[] {
  const byRoute = new Map<string, LatencyRow>();
  for (const t of tenants) {
    const existing = byRoute.get(t.route);
    if (existing) {
      existing.p50 = Math.max(existing.p50, t.p50);
      existing.p95 = Math.max(existing.p95, t.p95);
      existing.p99 = Math.max(existing.p99, t.p99);
    } else {
      byRoute.set(t.route, {
        key: t.route,
        p50: t.p50,
        p95: t.p95,
        p99: t.p99,
      });
    }
  }
  return Array.from(byRoute.values());
}

/** Total in-flight requests across every upstream. */
export function totalInflight(rows: InflightRow[]): number {
  return rows.reduce((sum, r) => sum + r.inflight, 0);
}

// --- /admin/audit (OBS-07) ------------------------------------------------
//
// These interfaces mirror the Go handler `admin.AuditResponse` /
// `admin.AuditRow` in gateway/internal/admin/audit.go FIELD-FOR-FIELD.
// Nullable Postgres columns (upstream, error_code, event_kind, reason) are
// rendered as JSON null by the handler — typed `string | null` here.

/** One audit_log state-change row — mirrors `admin.AuditRow`. */
export interface AuditRow {
  ts: string;
  request_id: string;
  tenant_id: string;
  route: string;
  method: string;
  upstream: string | null;
  status_code: number;
  latency_ms: number;
  error_code: string | null;
  event_kind: string | null;
  /** Human-readable cause of the state change (e.g. FSM transition reason). */
  reason: string | null;
}

/**
 * `/admin/audit` JSON — paginated state-change history, newest-first.
 * Mirrors `admin.AuditResponse` — `items`, `limit`, `offset` (no `total`:
 * the gateway handler does not run a COUNT).
 */
export interface AuditResponse {
  items: AuditRow[];
  limit: number;
  offset: number;
}

// --- /admin/usage (Phase 4, existing) -------------------------------------

/** `/admin/usage` JSON — mirrors gateway/internal/admin/usage.go UsageResponse. */
export interface UsageResponse {
  tenant: {
    id: string;
    slug: string;
    name: string;
    data_class: string;
    mode: string;
  };
  range: {
    from: string;
    to: string;
    granularity: string;
    timezone: string;
  };
  summary: {
    tokens_in: number;
    tokens_out: number;
    audio_seconds: number;
    embeds_count: number;
    cost_local_brl: number;
    cost_local_phantom_brl: number;
    cost_external_brl: number;
    cost_total_brl: number;
    requests_count: number;
  };
  rows: Array<{
    date: string;
    tokens_in: number;
    tokens_out: number;
    audio_seconds: number;
    embeds_count: number;
    cost_local_brl: number;
    cost_local_phantom_brl: number;
    cost_external_brl: number;
    cost_total_brl: number;
    requests_count: number;
  }>;
}

// --- /admin/operations (quick-260625-v04, Tier-2 "Operação") --------------
//
// These interfaces mirror the Go handler `admin.OperationsResponse` and its
// sections in gateway/internal/admin/operations.go FIELD-FOR-FIELD. The Go
// handler is the source of truth. Nullable Postgres columns are rendered as
// JSON null by the handler → typed as `T | null` here. `phantom_month_brl`
// is omitted by the handler this version (economy deferred) → optional.

/** Primary + emergency FSM state. Mirrors `admin.FSMSection`. */
export interface OperationsFSM {
  primary_state: string; // asleep|provisioning|ready|draining|destroying|unknown
  emerg_state: string; // unknown when Vast/Phase-6 off
  active_lifecycle_id: number;
  active_instance_id: number;
  is_leader: boolean;
}

/** Resolved schedule window + next transition. Mirrors `admin.ScheduleSection`. */
export interface OperationsSchedule {
  timezone: string;
  up_hour: number;
  down_hour: number;
  days: string[]; // ordered ["mon","tue",...]
  provision_lead_seconds: number;
  grace_ramp_down_seconds: number;
  disabled: boolean;
  should_be_provisioned_now: boolean;
  next_transition_at: string; // RFC3339; "" when none
  next_transition_kind: string; // up|down|""
}

/** One primary lifecycle row. Mirrors `admin.LifecycleRow`. */
export interface OperationsLifecycle {
  id: number;
  started_at: string;
  drain_started_at: string | null;
  ended_at: string | null; // null = still running
  trigger_reason: string;
  vast_instance_id: number | null;
  accepted_dph: number | null;
  cost_brl: number | null; // null while open
  shutdown_reason: string | null;
}

/** One upstream's effective breaker state. Mirrors `admin.BreakerRow`. */
export interface OperationsBreaker {
  upstream: string;
  state: string; // closed|half-open|open|forced-open
}

/** Vast spend + budget. Mirrors `admin.VastCostSection`. */
export interface OperationsVastCost {
  today_brl: number;
  month_brl: number;
  budget_brl: number;
  budget_pct_used: number;
  phantom_month_brl?: number; // omitted this version (economy deferred)
}

/**
 * `/admin/operations` JSON — the Tier-2 "Operação" panel's single fetch.
 * Mirrors `admin.OperationsResponse` in gateway/internal/admin/operations.go.
 */
export interface OperationsResponse {
  fsm: OperationsFSM;
  schedule: OperationsSchedule;
  lifecycles: OperationsLifecycle[];
  breakers: OperationsBreaker[];
  vast_cost: OperationsVastCost;
}

/**
 * Error envelope surfaced by the proxy or the gateway.
 *
 * `message` carries the SPECIFIC server-side cause when one is available
 * (WR-06) — the proxy emits `configuration_error` (500) /
 * `upstream_unreachable` (502) envelopes, and the gateway admin handlers
 * emit OpenAI-style `{error:{type,code,message}}` envelopes. `type` is
 * the machine-readable discriminator (e.g. "upstream_unreachable",
 * "invalid_request_error") so a page can tell a down gateway from a bad
 * key from an unconfigured proxy.
 */
export class GatewayError extends Error {
  readonly status: number;
  readonly type: string | null;
  constructor(status: number, message: string, type: string | null = null) {
    super(message);
    this.name = "GatewayError";
    this.status = status;
    this.type = type;
  }
}

/** The error-envelope shape both the proxy and the gateway emit. */
interface ErrorEnvelope {
  error?: { message?: string; type?: string };
}

/**
 * Internal fetch helper — always hits the `/api/gateway/*` proxy, never the
 * gateway directly. Throws GatewayError on non-2xx; WR-06: it parses the
 * JSON error envelope and surfaces the SPECIFIC `message`/`type` from the
 * proxy or gateway instead of a hardcoded generic string, so the operator
 * sees the actual diagnostic (bad key vs down gateway vs unconfigured
 * proxy) — the whole point of an incident-triage tool.
 */
async function proxyGet<T>(path: string, query?: Record<string, string>): Promise<T> {
  const qs = query ? `?${new URLSearchParams(query).toString()}` : "";
  const res = await fetch(`${GATEWAY_PROXY_BASE}/${path}${qs}`, {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
  });

  if (!res.ok) {
    // Try to surface the structured envelope; fall back to the generic
    // string only when the body is missing or not the expected shape.
    let message = "Não foi possível carregar as métricas do gateway.";
    let type: string | null = null;
    try {
      const body = (await res.json()) as ErrorEnvelope;
      if (body.error?.message) message = body.error.message;
      if (body.error?.type) type = body.error.type;
    } catch {
      // Non-JSON or empty body — keep the generic fallback message.
    }
    throw new GatewayError(res.status, message, type);
  }

  return (await res.json()) as T;
}

/** GET /admin/metrics — live per-tenant percentiles + FSM state. */
export function fetchMetrics(window?: string): Promise<MetricsResponse> {
  return proxyGet<MetricsResponse>(
    "metrics",
    window ? { window } : undefined,
  );
}

/** GET /admin/audit — paginated state-change history, newest-first. */
export function fetchAudit(limit = 50, offset = 0): Promise<AuditResponse> {
  return proxyGet<AuditResponse>("audit", {
    limit: String(limit),
    offset: String(offset),
  });
}

/** GET /admin/usage — per-tenant cost/usage breakdown for a date range. */
export function fetchUsage(
  tenant: string,
  from: string,
  to: string,
): Promise<UsageResponse> {
  return proxyGet<UsageResponse>("usage", { tenant, from, to });
}

/** GET /admin/operations — Tier-2 "Operação" aggregate (FSM/schedule/cost). */
export function fetchOperations(): Promise<OperationsResponse> {
  return proxyGet<OperationsResponse>("operations");
}
