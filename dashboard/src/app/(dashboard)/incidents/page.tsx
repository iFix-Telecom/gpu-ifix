"use client";

/**
 * Incident History — the audit-log view (OBS-03 / OBS-10 "incident-history").
 *
 * Polls `fetchAudit` via `useQuery`; the `/admin/audit` handler returns rows
 * newest-first, rendered as-is by `AuditTable`. OBS-10 adds a date-range
 * picker and a free-text search box above the table (BRT range + parameterized
 * ILIKE live in the Go handler — 15-02), and a pager driven by the real
 * `total` COUNT (offset + limit < total) rather than a row-count heuristic.
 *
 * Loading → `skeleton`; fetch failure → the pt-BR error state with a
 * "Tentar novamente" button (UI-SPEC §Copywriting). The period filter
 * defaults to the current month (day 1 → today).
 */

import { useQuery } from "@tanstack/react-query";
import { CalendarIcon } from "lucide-react";
import { useState } from "react";
import type { DateRange } from "react-day-picker";

import { AuditTable } from "@/components/audit-table";
import { StaleIndicator } from "@/components/stale-indicator";
import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Skeleton } from "@/components/ui/skeleton";
import { fetchAudit, GatewayError } from "@/lib/gateway";

const PAGE_SIZE = 50;

/**
 * YYYY-MM-DD — the `/admin/audit` from/to query format.
 *
 * WR-08: format the LOCAL date components directly. `react-day-picker`
 * returns `Date` objects at local midnight; round-tripping through
 * `toISOString()` shifts the operator's selected calendar day by their UTC
 * offset. The gateway interprets from/to in America/Sao_Paulo, so the date
 * string must be exactly the calendar day the operator picked.
 */
function isoDate(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** The current month so far: day 1 → today (local). */
function currentMonthRange(): { from: string; to: string } {
  const now = new Date();
  return {
    from: isoDate(new Date(now.getFullYear(), now.getMonth(), 1)),
    to: isoDate(now),
  };
}

export default function IncidentsPage() {
  const [offset, setOffset] = useState(0);
  // Default range = current month so the page loads data on first render
  // without requiring an explicit "Aplicar período" (Pitfall 6).
  const defaultMonth = currentMonthRange();
  const [range, setRange] = useState<DateRange | undefined>({
    from: new Date(new Date().getFullYear(), new Date().getMonth(), 1),
    to: new Date(),
  });
  const [applied, setApplied] = useState<{ from: string; to: string }>(
    defaultMonth,
  );
  const [search, setSearch] = useState("");

  const { data, isLoading, isError, error, refetch, dataUpdatedAt } = useQuery({
    queryKey: ["audit", offset, applied, search],
    queryFn: () =>
      fetchAudit(PAGE_SIZE, offset, applied.from, applied.to, search),
  });

  function applyPeriod() {
    if (!range?.from || !range?.to) return;
    setOffset(0);
    setApplied({ from: isoDate(range.from), to: isoDate(range.to) });
  }

  const canApply = range?.from !== undefined && range?.to !== undefined;

  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-[28px] font-semibold leading-[1.2]">
          Histórico de incidentes
        </h1>
        <StaleIndicator updatedAt={dataUpdatedAt} />
      </div>

      {/* Period + search filters. */}
      <div className="flex flex-wrap items-center gap-2">
        <Popover>
          <PopoverTrigger asChild>
            <Button size="sm" variant="outline">
              <CalendarIcon />
              {range?.from && range?.to
                ? `${isoDate(range.from)} → ${isoDate(range.to)}`
                : "Selecione o período"}
            </Button>
          </PopoverTrigger>
          <PopoverContent className="w-auto p-0" align="start">
            <Calendar
              mode="range"
              selected={range}
              onSelect={setRange}
              numberOfMonths={2}
            />
          </PopoverContent>
        </Popover>

        <Button size="sm" onClick={applyPeriod} disabled={!canApply}>
          Aplicar período
        </Button>

        <Input
          className="h-9 w-[260px]"
          placeholder="Buscar por rota, motivo ou código…"
          value={search}
          onChange={(e) => {
            setOffset(0);
            setSearch(e.target.value);
          }}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-[20px] font-semibold">
            Eventos de mudança de estado
          </CardTitle>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <Skeleton className="h-[480px] w-full" />
          ) : isError ? (
            <div className="flex flex-col items-center gap-4 py-8 text-center">
              <p className="text-[14px] text-muted-foreground">
                {/* WR-06: show the specific proxy/gateway cause. */}
                {error instanceof GatewayError
                  ? error.message
                  : "Não foi possível carregar as métricas do gateway."}{" "}
                Verifique se o gateway está no ar e se a admin-key está válida,
                depois use Tentar novamente.
              </p>
              <Button size="sm" variant="outline" onClick={() => refetch()}>
                Tentar novamente
              </Button>
            </div>
          ) : (
            <AuditTable
              rows={data?.items ?? []}
              limit={data?.limit ?? PAGE_SIZE}
              offset={data?.offset ?? offset}
              total={data?.total}
              onPrev={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
              onNext={() => setOffset((o) => o + PAGE_SIZE)}
            />
          )}
        </CardContent>
      </Card>
    </div>
  );
}
