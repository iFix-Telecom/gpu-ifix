/**
 * Operação — Vast cost + budget panel.
 *
 * KPI row: custo hoje / custo mês / budget, plus a month/budget progress bar
 * (budget_pct_used). The bar turns amber ≥ 75% and red ≥ 90% (UI-SPEC
 * §Semantic status palette). Economy (phantom vs OpenRouter) is DEFERRED —
 * not rendered this version.
 */

import { KpiCard } from "@/components/kpi-card";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { formatBrl } from "@/lib/format";
import type { OperationsVastCost } from "@/lib/gateway";
import { cn } from "@/lib/utils";

/** Budget percent → bar fill color. */
function barClass(pct: number): string {
  if (pct >= 90) return "bg-destructive";
  if (pct >= 75) return "bg-status-warning";
  return "bg-primary";
}

export interface OperacaoCostPanelProps {
  vastCost: OperationsVastCost;
}

export function OperacaoCostPanel({ vastCost }: OperacaoCostPanelProps) {
  const pct = vastCost.budget_pct_used;
  const clamped = Math.min(100, Math.max(0, pct));

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">
          Custo Vast
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <KpiCard caption="Custo hoje" value={formatBrl(vastCost.today_brl)} />
          <KpiCard caption="Custo mês" value={formatBrl(vastCost.month_brl)} />
          <KpiCard caption="Budget" value={formatBrl(vastCost.budget_brl)} />
        </div>

        <div className="flex flex-col gap-1.5">
          <div className="flex items-center justify-between text-[12px] font-semibold text-muted-foreground">
            <span>Uso do budget</span>
            <span className="tabular-nums">
              {pct.toLocaleString("pt-BR", {
                minimumFractionDigits: 1,
                maximumFractionDigits: 1,
              })}{" "}
              %
            </span>
          </div>
          <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
            <div
              className={cn("h-full rounded-full transition-all", barClass(pct))}
              style={{ width: `${clamped}%` }}
            />
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
