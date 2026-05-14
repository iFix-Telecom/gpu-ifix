/**
 * Per-tenant metrics table — a shadcn `table` of `fetchMetrics().tenants`.
 *
 * Columns: tenant, route, P50/P95/P99 (tabular-nums), error rate
 * (status-colored badge per the UI-SPEC threshold), requests.
 *
 * UI-SPEC §Layout Constraints — data-table rows are pinned to a 36px fixed
 * height. §Copywriting — the "Sem dados no período" empty state.
 */

import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { errorRateTier, formatCount, formatErrorRate } from "@/lib/format";
import type { StatusTier } from "@/lib/fsm";
import { type TenantMetricRow, tenantLabel } from "@/lib/gateway";
import { cn } from "@/lib/utils";

/** error-rate tier → badge classes (UI-SPEC status palette). */
function errorBadgeClass(tier: StatusTier): string {
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

export interface TenantTableProps {
  rows: TenantMetricRow[];
}

export function TenantTable({ rows }: TenantTableProps) {
  if (rows.length === 0) {
    return (
      <p className="py-8 text-center text-[14px] text-muted-foreground">
        Sem dados no período
      </p>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead className="text-[12px] font-semibold">Tenant</TableHead>
          <TableHead className="text-[12px] font-semibold">Rota</TableHead>
          <TableHead className="text-right text-[12px] font-semibold">
            P50
          </TableHead>
          <TableHead className="text-right text-[12px] font-semibold">
            P95
          </TableHead>
          <TableHead className="text-right text-[12px] font-semibold">
            P99
          </TableHead>
          <TableHead className="text-right text-[12px] font-semibold">
            Taxa de erro
          </TableHead>
          <TableHead className="text-right text-[12px] font-semibold">
            Requests
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row) => {
          const tier = errorRateTier(row.error_rate);
          return (
            <TableRow
              key={`${row.tenant_id}:${row.route}`}
              // 36px fixed row height (UI-SPEC §Layout Constraints).
              className="h-9"
            >
              {/* WR-10: render the human label (name → slug → UUID), not
                  the raw UUID, so an operator triaging an incident sees a
                  recognizable tenant. The UUID stays the row key above. */}
              <TableCell className="text-[14px]">
                {tenantLabel(row)}
              </TableCell>
              <TableCell className="text-[14px] text-muted-foreground">
                {row.route}
              </TableCell>
              <TableCell className="text-right text-[14px] tabular-nums">
                {formatCount(row.p50)}
              </TableCell>
              <TableCell className="text-right text-[14px] tabular-nums">
                {formatCount(row.p95)}
              </TableCell>
              <TableCell className="text-right text-[14px] tabular-nums">
                {formatCount(row.p99)}
              </TableCell>
              <TableCell className="text-right">
                <Badge
                  className={cn(
                    "text-[12px] font-semibold tabular-nums",
                    errorBadgeClass(tier),
                  )}
                >
                  {formatErrorRate(row.error_rate)}
                </Badge>
              </TableCell>
              <TableCell className="text-right text-[14px] tabular-nums">
                {formatCount(row.requests)}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
