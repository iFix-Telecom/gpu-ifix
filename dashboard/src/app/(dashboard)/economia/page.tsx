"use client";

/**
 * Economia — does running the own GPU (Vast) save money vs the OpenRouter
 * fallback? (OBS-09)
 *
 * Unlike /consumo (which fans out one `/admin/usage` per tenant and tolerates
 * partial failures), this page makes a SINGLE server-side call: the gateway's
 * `/admin/economy` computes the gateway-wide 5-metric summary + daily
 * phantom-vs-Vast series itself. No per-tenant fan-out, no settled-promise
 * merge, no partial-undercount note — the server returns one authoritative
 * answer.
 *
 * Loading → `skeleton`; failure → the pt-BR error state with a "Tentar
 * novamente" button (UI-SPEC §Copywriting). The period filter defaults to the
 * current month (day 1 → today).
 */

import { useQuery } from "@tanstack/react-query";
import { CalendarIcon } from "lucide-react";
import { useState } from "react";
import type { DateRange } from "react-day-picker";

import { EconomyPanel } from "@/components/economy-panel";
import { EconomyTrendChart } from "@/components/economy-trend-chart";
import { StaleIndicator } from "@/components/stale-indicator";
import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Skeleton } from "@/components/ui/skeleton";
import { fetchEconomy, GatewayError } from "@/lib/gateway";

/** WR-06: the specific proxy/gateway cause, or the generic fallback. */
function errorMessage(error: unknown): string {
  return error instanceof GatewayError
    ? error.message
    : "Não foi possível carregar as métricas do gateway.";
}

/**
 * YYYY-MM-DD — the `/admin/economy` from/to query format.
 *
 * WR-08: format the LOCAL date components directly. `react-day-picker`
 * returns `Date` objects at local midnight; round-tripping through the UTC ISO
 * serializer shifts the operator's selected calendar day by their UTC offset,
 * producing wrong numbers for the boundary days. The gateway interprets
 * from/to in America/Sao_Paulo, so the date string must be exactly the
 * calendar day the operator picked.
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

export default function EconomiaPage() {
  // Default range = current month so the page loads data on first render
  // without requiring an explicit "Aplicar período".
  const defaultMonth = currentMonthRange();
  const [range, setRange] = useState<DateRange | undefined>({
    from: new Date(new Date().getFullYear(), new Date().getMonth(), 1),
    to: new Date(),
  });
  const [applied, setApplied] = useState<{ from: string; to: string }>(
    defaultMonth,
  );

  const query = useQuery({
    queryKey: ["economia", applied],
    queryFn: () => fetchEconomy(applied.from, applied.to),
  });

  function applyPeriod() {
    if (!range?.from || !range?.to) return;
    setApplied({ from: isoDate(range.from), to: isoDate(range.to) });
  }

  const canApply = range?.from !== undefined && range?.to !== undefined;

  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-[28px] font-semibold leading-[1.2]">Economia</h1>
        <StaleIndicator updatedAt={query.dataUpdatedAt} />
      </div>

      {/* Period filter — gateway-wide, no tenant select. */}
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
      </div>

      {query.isLoading ? (
        <div className="flex flex-col gap-8">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-64 w-full" />
        </div>
      ) : query.isError ? (
        <div className="flex flex-col items-center gap-4 py-8 text-center">
          <p className="text-[14px] text-muted-foreground">
            {errorMessage(query.error)} Verifique se o gateway está no ar e se a
            admin-key está válida, depois use Tentar novamente.
          </p>
          <Button size="sm" variant="outline" onClick={() => query.refetch()}>
            Tentar novamente
          </Button>
        </div>
      ) : query.data ? (
        <>
          {/* 5-metric KPI panel. */}
          <EconomyPanel summary={query.data.summary} />

          {/* Daily phantom-vs-Vast trend. */}
          <Card>
            <CardHeader>
              <CardTitle className="text-[20px] font-semibold">
                Tendência (R$/dia)
              </CardTitle>
            </CardHeader>
            <CardContent>
              {query.data.series.length === 0 ? (
                <p className="py-8 text-center text-[14px] text-muted-foreground">
                  Sem dados no período.
                </p>
              ) : (
                <EconomyTrendChart rows={query.data.series} />
              )}
            </CardContent>
          </Card>
        </>
      ) : null}
    </div>
  );
}
