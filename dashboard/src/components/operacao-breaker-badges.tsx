/**
 * Operação — per-upstream breaker badges.
 *
 * One status-colored pill per upstream (UI-SPEC §Semantic status palette):
 *   closed       → --primary (green, routing healthy)
 *   half-open    → --status-warning (amber, probing)
 *   open         → --destructive (red, shedding)
 *   forced-open  → --destructive (red, operator override)
 */

import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { OperationsBreaker } from "@/lib/gateway";
import { cn } from "@/lib/utils";

/** Breaker state → badge classes. */
function breakerBadgeClass(state: string): string {
  switch (state) {
    case "closed":
      return "bg-primary/15 text-primary";
    case "half-open":
      return "bg-status-warning/15 text-status-warning";
    case "open":
    case "forced-open":
      return "bg-destructive/15 text-destructive";
    default:
      return "bg-muted text-muted-foreground";
  }
}

/** Breaker state → pt-BR label. */
function breakerLabel(state: string): string {
  switch (state) {
    case "closed":
      return "fechado";
    case "half-open":
      return "meio-aberto";
    case "open":
      return "aberto";
    case "forced-open":
      return "forçado-aberto";
    default:
      return state;
  }
}

export interface OperacaoBreakerBadgesProps {
  breakers: OperationsBreaker[];
}

export function OperacaoBreakerBadges({ breakers }: OperacaoBreakerBadgesProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">
          Disjuntores por upstream
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-2">
        {breakers.length === 0 ? (
          <p className="text-[14px] text-muted-foreground">
            Sem disjuntores configurados.
          </p>
        ) : (
          breakers.map((b) => (
            <Badge
              key={b.upstream}
              data-state={b.state}
              className={cn(
                "text-[12px] font-semibold",
                breakerBadgeClass(b.state),
              )}
            >
              {b.upstream}: {breakerLabel(b.state)}
            </Badge>
          ))
        )}
      </CardContent>
    </Card>
  );
}
