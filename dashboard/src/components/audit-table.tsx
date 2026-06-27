"use client";

/**
 * Audit-log / incident-history table — a shadcn `table` (inside a
 * `scroll-area`) of `fetchAudit().items`, newest-first, with a
 * limit/offset pager.
 *
 * Columns follow the ACTUAL `AuditRow` shape the Go `/admin/audit` handler
 * emits (gateway/internal/admin/audit.go) — ts, event_kind, tenant_id,
 * route, method, status_code, latency_ms, error_code, reason. There is no
 * `id`, no `actor`, no `detail` JSON blob (CR-01); the human-readable
 * cause of a state change rides the dedicated `reason` column (CR-03).
 *
 * The pager is driven by the server-reported `total` (15-02 added a real
 * COUNT over the same range/search predicate): `canNext = offset + limit <
 * total`. When `total` is absent it falls back to the legacy full-page
 * heuristic (`rows.length >= limit`).
 *
 * UI-SPEC §Layout Constraints — 36px fixed rows. §Copywriting — the
 * "Nenhum evento registrado no período." empty state.
 */

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { AuditRow } from "@/lib/gateway";

export interface AuditTableProps {
  rows: AuditRow[];
  /** Current page size + offset. */
  limit: number;
  offset: number;
  /**
   * Total matching rows (real COUNT — 15-02). When provided, the pager
   * derives honest bounds (`offset + limit < total`); when absent, it falls
   * back to the legacy full-page heuristic.
   */
  total?: number;
  /** Pager callbacks — the page owns the limit/offset query state. */
  onPrev: () => void;
  onNext: () => void;
}

/**
 * The cause cell: the `reason` column (state-change rows — e.g. the FSM
 * transition reason) falling back to `error_code` (request rows), then a
 * dash. The two are now distinct gateway columns (CR-03).
 */
function formatCause(row: AuditRow): string {
  return row.reason ?? row.error_code ?? "—";
}

export function AuditTable({
  rows,
  limit,
  offset,
  total,
  onPrev,
  onNext,
}: AuditTableProps) {
  if (rows.length === 0) {
    return (
      <p className="py-8 text-center text-[14px] text-muted-foreground">
        Nenhum evento registrado no período.
      </p>
    );
  }

  const from = offset + 1;
  const to = offset + rows.length;
  const canPrev = offset > 0;
  // Server `total` (15-02 COUNT) gives honest bounds; fall back to the
  // full-page heuristic when total is unavailable.
  const canNext = total !== undefined ? offset + limit < total : rows.length >= limit;

  return (
    <div className="flex flex-col gap-4">
      <ScrollArea className="h-[480px] w-full">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="text-[12px] font-semibold">
                Horário
              </TableHead>
              <TableHead className="text-[12px] font-semibold">
                Evento
              </TableHead>
              <TableHead className="text-[12px] font-semibold">
                Tenant
              </TableHead>
              <TableHead className="text-[12px] font-semibold">Rota</TableHead>
              <TableHead className="text-[12px] font-semibold">
                Motivo
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {/* `/admin/audit` returns items newest-first — rendered as-is.
                Keyed on request_id (unique, non-nil per CR-03). */}
            {rows.map((row) => (
              <TableRow key={row.request_id} className="h-9">
                <TableCell className="text-[14px] tabular-nums">
                  {new Date(row.ts).toLocaleString("pt-BR")}
                </TableCell>
                <TableCell>
                  <Badge
                    variant="outline"
                    className="text-[12px] font-semibold"
                  >
                    {row.event_kind ?? "—"}
                  </Badge>
                </TableCell>
                <TableCell className="text-[14px]">{row.tenant_id}</TableCell>
                <TableCell className="text-[14px] text-muted-foreground">
                  {row.route}
                </TableCell>
                <TableCell className="text-[14px] text-muted-foreground">
                  {formatCause(row)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </ScrollArea>

      {/* limit/offset pager — current range, plus the real total when known. */}
      <div className="flex items-center justify-between gap-4">
        <span className="text-[12px] font-semibold text-muted-foreground tabular-nums">
          {from}–{to}
          {total !== undefined ? ` de ${total}` : ""}
        </span>
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={onPrev}
            disabled={!canPrev}
          >
            Anteriores
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={onNext}
            disabled={!canNext}
          >
            Próximos
          </Button>
        </div>
      </div>
    </div>
  );
}
