/**
 * Operação — recent primary lifecycle timeline.
 *
 * A shadcn `table` of the month's primary lifecycles (newest-first as the
 * gateway returns them). Columns: início | trigger | instância Vast | custo |
 * estado. An OPEN lifecycle (ended_at null) shows "em curso" for cost and
 * "rodando" for state; a closed one shows the BRL cost + shutdown reason.
 *
 * Numeric cells carry `tabular-nums` so digits don't jitter on refetch.
 */

import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatBrl } from "@/lib/format";
import type { OperationsLifecycle } from "@/lib/gateway";

/** Format an RFC3339 timestamp in pt-BR, or "—" when empty/invalid. */
function formatDateTime(iso: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("pt-BR");
}

export interface OperacaoLifecycleTimelineProps {
  lifecycles: OperationsLifecycle[];
}

export function OperacaoLifecycleTimeline({
  lifecycles,
}: OperacaoLifecycleTimelineProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">
          Lifecycles recentes
        </CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Início</TableHead>
              <TableHead>Trigger</TableHead>
              <TableHead>Instância Vast</TableHead>
              <TableHead className="text-right">Custo</TableHead>
              <TableHead>Estado</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {lifecycles.length === 0 ? (
              <TableRow>
                <TableCell
                  colSpan={5}
                  className="py-8 text-center text-[14px] text-muted-foreground"
                >
                  Sem lifecycles no período.
                </TableCell>
              </TableRow>
            ) : (
              lifecycles.map((lc) => {
                const open = lc.ended_at === null;
                return (
                  <TableRow key={lc.id}>
                    <TableCell className="tabular-nums">
                      {formatDateTime(lc.started_at)}
                    </TableCell>
                    <TableCell>{lc.trigger_reason}</TableCell>
                    <TableCell className="tabular-nums">
                      {lc.vast_instance_id ?? "—"}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {lc.cost_brl !== null ? formatBrl(lc.cost_brl) : "em curso"}
                    </TableCell>
                    <TableCell>
                      {open
                        ? "rodando"
                        : `encerrada${lc.shutdown_reason ? ` (${lc.shutdown_reason})` : ""}`}
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}
