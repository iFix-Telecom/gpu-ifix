/**
 * KPI card — one metric on a shadcn `card`.
 *
 * UI-SPEC §Typography:
 *   - caption: Label 12/600
 *   - value:   Display 28/600, ALWAYS `tabular-nums` (digits must not jitter
 *              on the 5–10s React Query refetch)
 * UI-SPEC §Color §Semantic status palette — an optional `status` tier colors
 * the value (e.g. error rate >5% → critical/red).
 */

import type { StatusTier } from "@/lib/fsm";
import { tierTextClass } from "@/lib/fsm";
import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";

export interface KpiCardProps {
  /** 12/600 Label caption — e.g. "P95 latência". */
  caption: string;
  /** The formatted metric value — e.g. "480 ms". */
  value: string;
  /** Optional status tier; when set, colors the Display value. */
  status?: StatusTier;
  /** Optional 12/600 sub-label below the value (e.g. context / window). */
  hint?: string;
}

export function KpiCard({ caption, value, status, hint }: KpiCardProps) {
  return (
    <Card size="sm">
      <CardContent className="flex flex-col gap-1">
        <span className="text-[12px] font-semibold text-muted-foreground">
          {caption}
        </span>
        <span
          className={cn(
            // Display 28/600 + tabular-nums (UI-SPEC typography table).
            "text-[28px] font-semibold leading-[1.2] tabular-nums",
            status ? tierTextClass(status) : "text-foreground",
          )}
        >
          {value}
        </span>
        {hint ? (
          <span className="text-[12px] font-semibold text-muted-foreground">
            {hint}
          </span>
        ) : null}
      </CardContent>
    </Card>
  );
}
