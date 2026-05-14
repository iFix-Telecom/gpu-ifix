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

/** Per-tenant + per-route latency percentiles and error rate. */
export interface TenantMetricRow {
  tenant_id: string;
  route: string;
  p50: number;
  p95: number;
  p99: number;
  requests: number;
  error_rate: number;
}

/** Latency keyed by a single dimension (route or upstream). */
export interface LatencyRow {
  key: string;
  p50: number;
  p95: number;
  p99: number;
}

/**
 * `/admin/metrics` JSON — per-tenant percentiles, per-route + per-upstream
 * latency, inflight count, and the current failover FSM state.
 */
export interface MetricsResponse {
  window: string;
  generated_at: string;
  tenants: TenantMetricRow[];
  by_route: LatencyRow[];
  by_upstream: LatencyRow[];
  inflight: number;
  fsm_state: string;
}

// --- /admin/audit (OBS-07) ------------------------------------------------

/** One audit_log state-change row. */
export interface AuditRow {
  id: string;
  ts: string;
  event_kind: string;
  tenant_id: string | null;
  actor: string | null;
  detail: Record<string, unknown> | null;
}

/** `/admin/audit` JSON — paginated state-change history, newest-first. */
export interface AuditResponse {
  rows: AuditRow[];
  limit: number;
  offset: number;
  total: number;
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

/** Error envelope surfaced by the proxy or the gateway. */
export class GatewayError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "GatewayError";
    this.status = status;
  }
}

/**
 * Internal fetch helper — always hits the `/api/gateway/*` proxy, never the
 * gateway directly. Throws GatewayError on non-2xx so callers (and the
 * UI-SPEC error state) can surface "Não foi possível carregar as métricas…".
 */
async function proxyGet<T>(path: string, query?: Record<string, string>): Promise<T> {
  const qs = query ? `?${new URLSearchParams(query).toString()}` : "";
  const res = await fetch(`${GATEWAY_PROXY_BASE}/${path}${qs}`, {
    method: "GET",
    headers: { Accept: "application/json" },
    cache: "no-store",
  });

  if (!res.ok) {
    throw new GatewayError(
      res.status,
      "Não foi possível carregar as métricas do gateway.",
    );
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
