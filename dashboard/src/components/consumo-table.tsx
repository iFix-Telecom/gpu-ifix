"use client";

/**
 * Per-tenant consumption table — a shadcn `table` of the aggregated
 * `perTenantRows(responses)` output.
 *
 * Columns: Tenant | Custo R$ | Tokens entrada | Tokens saída | Áudio (s) |
 * Embeds. Rows arrive already sorted by cost desc (the page passes them
 * straight through). 0 renders as 0 — no placeholder substitution.
 *
 * Numeric cells carry `tabular-nums` so digits don't jitter on refetch
 * (UI-SPEC §Typography).
 */

import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { TenantUsageRow } from "@/lib/consumo";
import { formatBrl, formatCount } from "@/lib/format";

export interface ConsumoTableProps {
  /** Per-tenant rows — already sorted by cost desc by the page. */
  rows: TenantUsageRow[];
}

export function ConsumoTable({ rows }: ConsumoTableProps) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Tenant</TableHead>
          <TableHead className="text-right">Custo R$</TableHead>
          <TableHead className="text-right">Tokens entrada</TableHead>
          <TableHead className="text-right">Tokens saída</TableHead>
          <TableHead className="text-right">Áudio (s)</TableHead>
          <TableHead className="text-right">Embeds</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.length === 0 ? (
          <TableRow>
            <TableCell
              colSpan={6}
              className="py-8 text-center text-[14px] text-muted-foreground"
            >
              Sem dados no período.
            </TableCell>
          </TableRow>
        ) : (
          rows.map((row) => (
            <TableRow key={row.tenant_id}>
              <TableCell>{row.label}</TableCell>
              <TableCell className="text-right tabular-nums">
                {formatBrl(row.cost_local_phantom_brl)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.tokens_in)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.tokens_out)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.audio_seconds)}
              </TableCell>
              <TableCell className="text-right tabular-nums">
                {formatCount(row.embeds_count)}
              </TableCell>
            </TableRow>
          ))
        )}
      </TableBody>
    </Table>
  );
}
