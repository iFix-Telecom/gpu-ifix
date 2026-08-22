"use client";

/**
 * "Top tenants por volume" — horizontal bars, one per tenant (mockup `.hbars`).
 *
 * Plain CSS, not Recharts: a ranked bar list is a grid of divs whose widths are
 * a ratio. A chart runtime here would buy nothing and cost a re-render on every
 * poll. The bar length is `requests_count / max`, so the top row is always
 * full-width and every other row reads as a fraction of the busiest tenant.
 *
 * The fill color encodes MODALITY (chat / audio / embed), derived from the
 * tenant's own usage counters by `topTenantsByVolume` — the hues come from the
 * colorblind-safe categorical palette, never from the status ramp (a tenant's
 * modality is not a health state).
 */

import { formatCount } from "@/lib/format";
import type { TenantModality, TenantVolumeRow } from "@/lib/consumo";

/** Modality → categorical palette utility. */
const MODALITY_FILL: Record<TenantModality, string> = {
  llm: "bg-chart-llm",
  stt: "bg-chart-stt",
  embed: "bg-chart-embed",
};

const LEGEND: Array<{ modality: TenantModality; label: string }> = [
  { modality: "llm", label: "chat (LLM)" },
  { modality: "stt", label: "áudio (STT)" },
  { modality: "embed", label: "embed" },
];

export interface TenantVolumeBarsProps {
  /** Ranked rows from `topTenantsByVolume`, highest first. */
  rows: TenantVolumeRow[];
}

export function TenantVolumeBars({ rows }: TenantVolumeBarsProps) {
  if (rows.length === 0) {
    return (
      <p className="py-8 text-center text-[14px] text-muted-foreground">
        Sem dados no período.
      </p>
    );
  }

  // Guard the denominator: an all-zero period must not divide by zero.
  const max = Math.max(...rows.map((r) => r.requests_count), 0);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap gap-4">
        {LEGEND.map((item) => (
          <span
            key={item.modality}
            className="flex items-center gap-1.5 text-[12px] text-muted-foreground"
          >
            <span
              aria-hidden="true"
              className={`size-2.5 rounded-[3px] ${MODALITY_FILL[item.modality]}`}
            />
            {item.label}
          </span>
        ))}
      </div>

      <div className="flex flex-col gap-2.5">
        {rows.map((row) => {
          const pct = max > 0 ? (row.requests_count / max) * 100 : 0;
          return (
            <div
              key={row.tenant_id}
              className="grid grid-cols-[110px_1fr_64px] items-center gap-3 sm:grid-cols-[150px_1fr_64px]"
            >
              <span
                className="truncate font-mono text-[12.5px] text-muted-foreground"
                title={row.label}
              >
                {row.label}
              </span>
              <div className="h-5 overflow-hidden rounded-[5px] bg-secondary">
                <div
                  className={`h-full rounded-[5px] ${MODALITY_FILL[row.modality]}`}
                  style={{ width: `${pct}%` }}
                />
              </div>
              <span className="text-right font-mono text-[12.5px] tabular-nums">
                {formatCount(row.requests_count)}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
