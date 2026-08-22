/**
 * KPI card — one metric on a shadcn `card`.
 *
 * Mockup `.kpi`:
 *   - caption: 11.5px uppercase, letter-spaced, muted
 *   - value:   display face, 29/700, ALWAYS `tabular-nums` (digits must not
 *              jitter on the 5–10s React Query refetch)
 *   - spark:   OPTIONAL 26px trend line under the hint
 *
 * UI-SPEC §Color §Semantic status palette — an optional `status` tier colors
 * the value (e.g. error rate >5% → critical/red).
 *
 * `series` is opt-in on purpose: a KPI only gets a sparkline when a REAL
 * historical series exists for it. Callers must never synthesize one.
 */

import { Sparkline } from "@/components/sparkline";
import type { StatusTier } from "@/lib/fsm";
import { tierTextClass } from "@/lib/fsm";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export interface KpiCardProps {
  /** Uppercase caption — e.g. "P95 latência". */
  caption: string;
  /** The formatted metric value — e.g. "480 ms". */
  value: string;
  /** Optional status tier; when set, colors the Display value. */
  status?: StatusTier;
  /** Optional sub-label below the value (e.g. context / window). */
  hint?: string;
  /**
   * Optional REAL historical series, oldest → newest. Rendered as a sparkline
   * only when it has 2+ points; never pass a placeholder or a padded array.
   */
  series?: number[];
  /** Sparkline stroke color — defaults to the brand accent. */
  seriesColor?: string;
}

export function KpiCard({
  caption,
  value,
  status,
  hint,
  series,
  seriesColor,
}: KpiCardProps) {
  return (
    <Card size="sm">
      <CardContent className="flex flex-col gap-1">
        <span className="text-[11.5px] font-semibold uppercase tracking-wide text-muted-foreground">
          {caption}
        </span>
        <span
          className={cn(
            "font-display text-[28px] font-bold leading-[1.2] tracking-[-0.02em] tabular-nums",
            status ? tierTextClass(status) : "text-foreground",
          )}
        >
          {value}
        </span>
        {hint ? (
          <span className="text-[12px] font-medium text-muted-foreground">
            {hint}
          </span>
        ) : null}
        {series ? <Sparkline points={series} color={seriesColor} /> : null}
      </CardContent>
    </Card>
  );
}
