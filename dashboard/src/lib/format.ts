/**
 * Shared metric formatting + aggregation helpers for the dashboard.
 *
 * All metric numbers render with the pt-BR locale and feed cells that carry
 * `tabular-nums` (UI-SPEC §Typography).
 */

import type { StatusTier } from "@/lib/fsm";
import type { TenantMetricRow } from "@/lib/gateway";

/** Format a latency value in milliseconds — e.g. `480 ms`. */
export function formatMs(ms: number): string {
  return `${Math.round(ms).toLocaleString("pt-BR")} ms`;
}

/** Format an error rate (0–1 fraction) as a percentage — e.g. `2,4 %`. */
export function formatErrorRate(rate: number): string {
  return `${(rate * 100).toLocaleString("pt-BR", {
    minimumFractionDigits: 1,
    maximumFractionDigits: 1,
  })} %`;
}

/** Format a BRL cost value — e.g. `R$ 84,20`. */
export function formatBrl(value: number): string {
  return value.toLocaleString("pt-BR", {
    style: "currency",
    currency: "BRL",
  });
}

/** Format an integer count — e.g. `1.234`. */
export function formatCount(value: number): string {
  return Math.round(value).toLocaleString("pt-BR");
}

/**
 * Humanize an uptime in seconds to a compact pt-BR form:
 *   < 60s     → "Ns"
 *   < 3600s   → "Nm"
 *   < 86400s  → "Nh Mm"
 *   else      → "Nd Mh"
 * Negative / NaN inputs render as "—".
 */
export function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "—";
  const s = Math.floor(seconds);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) {
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    return `${h}h ${m}m`;
  }
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  return `${d}d ${h}h`;
}

/**
 * YYYY-MM-DD from the LOCAL date components — the `/admin/usage` and
 * `/admin/economy` from/to query format.
 *
 * WR-08: round-tripping through `toISOString()` shifts the calendar day by the
 * operator's UTC offset (BRT is UTC-3, so local midnight is the PREVIOUS day in
 * UTC), producing wrong cost numbers on the boundary days. The gateway
 * interprets from/to in America/Sao_Paulo, so the string must be exactly the
 * calendar day the operator sees.
 */
export function isoDate(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** The current month so far: day 1 → today (local calendar). */
export function currentMonthRange(): { from: string; to: string } {
  const now = new Date();
  return {
    from: isoDate(new Date(now.getFullYear(), now.getMonth(), 1)),
    to: isoDate(now),
  };
}

/**
 * Error-rate → status tier (UI-SPEC §Semantic status palette thresholds):
 *   < 1%   → healthy
 *   1–5%   → warning
 *   > 5%   → critical
 */
export function errorRateTier(rate: number): StatusTier {
  if (rate > 0.05) return "critical";
  if (rate >= 0.01) return "warning";
  return "healthy";
}

/**
 * Latency P95 → status tier — a coarse SLO heuristic for the KPI color:
 *   ≤ 800ms  → healthy
 *   ≤ 2000ms → warning
 *   > 2000ms → critical
 */
export function latencyTier(p95Ms: number): StatusTier {
  if (p95Ms > 2000) return "critical";
  if (p95Ms > 800) return "warning";
  return "healthy";
}

/** The worst (highest) P95 across all tenant/route rows. */
export function aggregateP95(tenants: TenantMetricRow[]): number {
  if (tenants.length === 0) return 0;
  return Math.max(...tenants.map((t) => t.p95));
}

/** Request-weighted mean error rate across all tenant/route rows. */
export function aggregateErrorRate(tenants: TenantMetricRow[]): number {
  const totalRequests = tenants.reduce((sum, t) => sum + t.requests, 0);
  if (totalRequests === 0) return 0;
  const weightedErrors = tenants.reduce(
    (sum, t) => sum + t.error_rate * t.requests,
    0,
  );
  return weightedErrors / totalRequests;
}

/** Total request count across all tenant/route rows. */
export function aggregateRequests(tenants: TenantMetricRow[]): number {
  return tenants.reduce((sum, t) => sum + t.requests, 0);
}
