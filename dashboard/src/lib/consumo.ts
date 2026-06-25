/**
 * Client-side aggregation helpers for the `/consumo` page.
 *
 * The gateway's `/admin/usage` requires a single `tenant` and has no "all"
 * mode, so the Consumo page fans out one `fetchUsage` per tenant and feeds
 * the resulting `UsageResponse[]` through these pure functions to produce the
 * aggregated KPI summary, the merged daily trend, and the per-tenant table.
 *
 * Every numeric field comes straight from the gateway's
 * `UsageResponse.summary` / `UsageResponse.rows` — no invented fields.
 *
 * @module lib/consumo
 */

import type { UsageResponse } from "@/lib/gateway";

/** Aggregated totals across every tenant's `summary`. */
export interface ConsumoSummary {
  cost_local_phantom_brl: number;
  cost_total_brl: number;
  tokens_in: number;
  tokens_out: number;
  audio_seconds: number;
  embeds_count: number;
  requests_count: number;
}

/** One merged trend point — total tokens and phantom cost for a single day. */
export interface DailyAggRow {
  date: string;
  /** `tokens_in + tokens_out` summed across all tenants for this date. */
  tokens: number;
  /** `cost_local_phantom_brl` summed across all tenants for this date. */
  cost_brl: number;
}

/** One per-tenant table row, taken from that tenant's `summary`. */
export interface TenantUsageRow {
  tenant_id: string;
  label: string;
  cost_local_phantom_brl: number;
  tokens_in: number;
  tokens_out: number;
  audio_seconds: number;
  embeds_count: number;
}

/**
 * Sum each numeric `summary` field across every response. An empty array
 * yields an all-zero summary (the page renders 0 honestly, no placeholders).
 *
 * @param responses - the fulfilled `fetchUsage` results, one per tenant.
 * @returns the aggregated totals across all tenants.
 */
export function aggregateSummary(responses: UsageResponse[]): ConsumoSummary {
  const total: ConsumoSummary = {
    cost_local_phantom_brl: 0,
    cost_total_brl: 0,
    tokens_in: 0,
    tokens_out: 0,
    audio_seconds: 0,
    embeds_count: 0,
    requests_count: 0,
  };
  for (const r of responses) {
    const s = r.summary;
    total.cost_local_phantom_brl += s.cost_local_phantom_brl;
    total.cost_total_brl += s.cost_total_brl;
    total.tokens_in += s.tokens_in;
    total.tokens_out += s.tokens_out;
    total.audio_seconds += s.audio_seconds;
    total.embeds_count += s.embeds_count;
    total.requests_count += s.requests_count;
  }
  return total;
}

/**
 * Merge every response's `rows` by `date`: sum `tokens_in + tokens_out` into
 * `tokens` and `cost_local_phantom_brl` into `cost_brl`. Sorted ascending by
 * `date` (string compare on YYYY-MM-DD is chronological).
 *
 * @param responses - the fulfilled `fetchUsage` results, one per tenant.
 * @returns the merged per-day trend rows, oldest first.
 */
export function aggregateDaily(responses: UsageResponse[]): DailyAggRow[] {
  const byDate = new Map<string, DailyAggRow>();
  for (const r of responses) {
    for (const row of r.rows) {
      const existing = byDate.get(row.date);
      const tokens = row.tokens_in + row.tokens_out;
      if (existing) {
        existing.tokens += tokens;
        existing.cost_brl += row.cost_local_phantom_brl;
      } else {
        byDate.set(row.date, {
          date: row.date,
          tokens,
          cost_brl: row.cost_local_phantom_brl,
        });
      }
    }
  }
  return Array.from(byDate.values()).sort((a, b) =>
    a.date < b.date ? -1 : a.date > b.date ? 1 : 0,
  );
}

/**
 * One row per tenant, taken from that tenant's `summary`, sorted by
 * `cost_local_phantom_brl` descending (the biggest spender first). The label
 * falls back name → slug → raw id so a since-renamed tenant stays identifiable.
 *
 * @param responses - the fulfilled `fetchUsage` results, one per tenant.
 * @returns the per-tenant rows, highest cost first.
 */
export function perTenantRows(responses: UsageResponse[]): TenantUsageRow[] {
  return responses
    .map((r) => ({
      tenant_id: r.tenant.id,
      label: r.tenant.name || r.tenant.slug || r.tenant.id,
      cost_local_phantom_brl: r.summary.cost_local_phantom_brl,
      tokens_in: r.summary.tokens_in,
      tokens_out: r.summary.tokens_out,
      audio_seconds: r.summary.audio_seconds,
      embeds_count: r.summary.embeds_count,
    }))
    .sort((a, b) => b.cost_local_phantom_brl - a.cost_local_phantom_brl);
}
