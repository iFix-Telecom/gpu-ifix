/**
 * Failover FSM-state panel — a shadcn `card` showing the gateway's current
 * `fsm_state` as a status-colored `badge` with the pt-BR label.
 *
 * UI-SPEC §Copywriting (FSM labels) + §Semantic status palette — the
 * state→label→tier mapping lives in `@/lib/fsm` (single source of truth,
 * shared with the critical banner). Tier → badge color:
 *   healthy  → --primary (green)
 *   warning  → --status-warning (amber)
 *   critical → --destructive (red)
 *   neutral  → --muted-foreground
 */

import type { StatusTier } from "@/lib/fsm";
import { fsmMeta } from "@/lib/fsm";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

/** Tier → badge classes (the status palette applied to the FSM pill). */
function tierBadgeClass(tier: StatusTier): string {
  switch (tier) {
    case "healthy":
      return "bg-primary/15 text-primary";
    case "warning":
      return "bg-status-warning/15 text-status-warning";
    case "critical":
      return "bg-destructive/15 text-destructive";
    case "neutral":
      return "bg-muted text-muted-foreground";
  }
}

export interface FsmPanelProps {
  /** The current `fsm_state` string from `fetchMetrics`. */
  fsmState: string | undefined;
}

export function FsmPanel({ fsmState }: FsmPanelProps) {
  const meta = fsmMeta(fsmState);

  return (
    <Card>
      <CardHeader>
        {/* Heading 20/600. */}
        <CardTitle className="text-[20px] font-semibold">
          Estado de failover
        </CardTitle>
      </CardHeader>
      <CardContent className="flex items-center gap-2">
        <Badge
          data-tier={meta.tier}
          className={cn("text-[12px] font-semibold", tierBadgeClass(meta.tier))}
        >
          {meta.label}
        </Badge>
        <span className="text-[12px] font-semibold text-muted-foreground tabular-nums">
          {fsmState ?? "—"}
        </span>
      </CardContent>
    </Card>
  );
}
