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

/**
 * What a tenant actually spends its requests on. DERIVED from the usage
 * counters — the gateway has no "modality" column, and guessing from the
 * tenant's NAME would be fiction.
 */
export type TenantModality = "llm" | "stt" | "embed";

/** One horizontal volume bar — a tenant's request count for the period. */
export interface TenantVolumeRow {
  tenant_id: string;
  label: string;
  /** `summary.requests_count` — straight from the gateway. */
  requests_count: number;
  modality: TenantModality;
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

/**
 * Classify a tenant's traffic from its usage counters alone.
 *
 * The rules are deliberately conservative: a tenant only counts as "stt" or
 * "embed" when it has NO token traffic at all. A tenant that transcribes audio
 * AND then summarizes it with an LLM is an LLM tenant with an audio step, and
 * painting it orange would misreport where the token spend lives.
 */
function classifyModality(
  s: UsageResponse["summary"],
): TenantModality {
  const tokens = s.tokens_in + s.tokens_out;
  if (s.audio_seconds > 0 && tokens === 0) return "stt";
  if (s.embeds_count > 0 && tokens === 0 && s.audio_seconds === 0)
    return "embed";
  return "llm";
}

/**
 * The busiest tenants by REQUEST COUNT for the period, highest first.
 *
 * Volume, not cost: `perTenantRows` already ranks by spend, and the two
 * answers differ — a tenant can dominate request volume on the free local pod
 * while costing nothing. Both questions are legitimate; this one answers "who
 * is hammering the gateway".
 *
 * @param responses - the fulfilled `fetchUsage` results, one per tenant.
 * @param limit - how many rows to keep (default 10).
 */
export function topTenantsByVolume(
  responses: UsageResponse[],
  limit = 10,
): TenantVolumeRow[] {
  return responses
    .map((r) => ({
      tenant_id: r.tenant.id,
      // Same fallback chain as perTenantRows: a since-renamed tenant stays
      // identifiable instead of collapsing to a blank label.
      label: r.tenant.name || r.tenant.slug || r.tenant.id,
      requests_count: r.summary.requests_count,
      modality: classifyModality(r.summary),
    }))
    .sort((a, b) => b.requests_count - a.requests_count)
    .slice(0, limit);
}

/** A trend point that can be MISSING — `null` means "no billing row", not zero. */
export interface DailyGapRow {
  date: string;
  tokens: number | null;
  cost_brl: number | null;
}

/** One calendar day in ms — the step used to walk the range in UTC. */
const DAY_MS = 86_400_000;

/**
 * Expand a sparse daily series over the FULL requested range, emitting `null`
 * for days with no billing row.
 *
 * This is the whole point: a missing day and a zero day are different facts. A
 * zero says "the gateway ran and served nothing"; a gap says "we have no
 * record for this day" — which in this system has meant a real ingestion
 * incident (the ago/2026 `billing_events` partition gap dropped 21 days of
 * billing while traffic was normal). Filling gaps with 0 would have drawn a
 * plausible valley and hidden the outage.
 *
 * The walk is done on `Date.UTC` epoch arithmetic so a DST shift or a negative
 * local offset can never skip or duplicate a day.
 *
 * @param rows - the merged per-day rows from `aggregateDaily`.
 * @param from - inclusive start, YYYY-MM-DD.
 * @param to - inclusive end, YYYY-MM-DD.
 */
export function fillDateGaps(
  rows: DailyAggRow[],
  from: string,
  to: string,
): DailyGapRow[] {
  const start = Date.parse(`${from}T00:00:00Z`);
  const end = Date.parse(`${to}T00:00:00Z`);
  // Unparseable or inverted range — return the data untouched rather than
  // inventing a window the caller did not ask for.
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) {
    return rows.map((r) => ({
      date: r.date,
      tokens: r.tokens,
      cost_brl: r.cost_brl,
    }));
  }

  const byDate = new Map(rows.map((r) => [r.date, r]));
  const out: DailyGapRow[] = [];
  for (let t = start; t <= end; t += DAY_MS) {
    const date = new Date(t).toISOString().slice(0, 10);
    const hit = byDate.get(date);
    out.push({
      date,
      tokens: hit ? hit.tokens : null,
      cost_brl: hit ? hit.cost_brl : null,
    });
  }
  return out;
}
