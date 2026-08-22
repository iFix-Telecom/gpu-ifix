"use client";

/**
 * Consumo — aggregated cost/usage across ALL tenants for a date range.
 *
 * The gateway's `/admin/usage` takes a single `tenant` and has no "all" mode,
 * so this page aggregates client-side: list tenants via `fetchMetrics`, fan
 * out one `fetchUsage` per tenant with `Promise.allSettled`, then sum/merge
 * via the `@/lib/consumo` helpers. A single failing tenant must NOT blank the
 * page — partial failures are tolerated and surfaced as an honest note.
 *
 * Loading → `skeleton`; total failure → the pt-BR error state with a
 * "Tentar novamente" button (UI-SPEC §Copywriting). The period filter
 * defaults to the current month (day 1 → today).
 */

import { useQuery } from "@tanstack/react-query";
import { CalendarIcon } from "lucide-react";
import { useState } from "react";
import type { DateRange } from "react-day-picker";

import { ConsumoTable } from "@/components/consumo-table";
import { ConsumoTrendChart } from "@/components/consumo-trend-chart";
import { KpiCard } from "@/components/kpi-card";
import { LocalVsExternalDonut } from "@/components/local-vs-external-donut";
import { StaleIndicator } from "@/components/stale-indicator";
import { TenantVolumeBars } from "@/components/tenant-volume-bars";
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
import {
  aggregateDaily,
  aggregateSummary,
  fillDateGaps,
  perTenantRows,
  topTenantsByVolume,
} from "@/lib/consumo";
import { formatBrl, formatCount } from "@/lib/format";
import {
  fetchEconomy,
  fetchMetrics,
  fetchUsage,
  GatewayError,
} from "@/lib/gateway";

/** WR-06: the specific proxy/gateway cause, or the generic fallback. */
function errorMessage(error: unknown): string {
  return error instanceof GatewayError
    ? error.message
    : "Não foi possível carregar as métricas do gateway.";
}

/**
 * YYYY-MM-DD — the `/admin/usage` from/to query format.
 *
 * WR-08: format the LOCAL date components directly. `react-day-picker`
 * returns `Date` objects at local midnight; round-tripping through
 * `toISOString()` shifts the operator's selected calendar day by their UTC
 * offset, producing wrong cost numbers for the boundary days. The gateway
 * interprets from/to in America/Sao_Paulo, so the date string must be exactly
 * the calendar day the operator picked.
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

export default function ConsumoPage() {
  // Default range = current month so the page loads data on first render
  // without requiring an explicit "Aplicar período".
  const defaultMonth = currentMonthRange();
  const [range, setRange] = useState<DateRange | undefined>({
    from: new Date(
      new Date().getFullYear(),
      new Date().getMonth(),
      1,
    ),
    to: new Date(),
  });
  const [applied, setApplied] = useState<{ from: string; to: string }>(
    defaultMonth,
  );

  const query = useQuery({
    queryKey: ["consumo", applied],
    queryFn: async () => {
      // 1. List tenants. If this throws, the whole query rejects → error state.
      const metrics = await fetchMetrics();
      // 2. De-duplicate tenant ids (a tenant appears once per route in metrics).
      const ids = Array.from(
        new Map(
          (metrics.tenants ?? []).map((t) => [t.tenant_id, t]),
        ).values(),
      ).map((t) => t.tenant_id);
      // 3. Fan out one usage call per tenant — partial failures tolerated.
      const settled = await Promise.allSettled(
        ids.map((id) => fetchUsage(id, applied.from, applied.to)),
      );
      // 4. Keep only the fulfilled responses.
      const responses = settled
        .filter((r) => r.status === "fulfilled")
        .map((r) => (r as PromiseFulfilledResult<Awaited<ReturnType<typeof fetchUsage>>>).value);
      // 5. Count failures for the honest partial-failure note.
      const failures = settled.length - responses.length;
      return {
        summary: aggregateSummary(responses),
        // Expanded over the FULL range so days with no billing row render as
        // a gap in the line instead of a fabricated zero.
        daily: fillDateGaps(
          aggregateDaily(responses),
          applied.from,
          applied.to,
        ),
        tenants: perTenantRows(responses),
        topTenants: topTenantsByVolume(responses),
        failures,
        total: settled.length,
      };
    },
  });

  /*
   * SEPARATE query on purpose: `/admin/economy` is a single gateway-wide
   * aggregate with a different failure mode from the per-tenant `/admin/usage`
   * fan-out. Folding it into the query above would mean one economy 500 blanks
   * the entire screen; kept apart, only the donut degrades.
   */
  const economyQuery = useQuery({
    queryKey: ["economia-consumo", applied],
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
        <h1 className="text-[28px] font-semibold leading-[1.2]">Consumo</h1>
        <StaleIndicator updatedAt={query.dataUpdatedAt} />
      </div>

      {/* Period filter — all tenants, no tenant select. */}
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
          <Skeleton className="h-48 w-full" />
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
          {query.data.failures > 0 ? (
            <p className="text-[12px] text-muted-foreground">
              {query.data.failures} de {query.data.total} tenants não
              retornaram dados no período.
            </p>
          ) : null}

          {/* Aggregated KPI row. */}
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 xl:grid-cols-6">
            <KpiCard
              caption="Requests"
              value={formatCount(query.data.summary.requests_count)}
            />
            <KpiCard
              caption="Custo total"
              value={formatBrl(query.data.summary.cost_local_phantom_brl)}
            />
            <KpiCard
              caption="Tokens entrada"
              value={formatCount(query.data.summary.tokens_in)}
            />
            <KpiCard
              caption="Tokens saída"
              value={formatCount(query.data.summary.tokens_out)}
            />
            <KpiCard
              caption="Áudio (s)"
              value={formatCount(query.data.summary.audio_seconds)}
            />
            <KpiCard
              caption="Embeds"
              value={formatCount(query.data.summary.embeds_count)}
            />
          </div>

          {/* Volume ranking + local-vs-external split. */}
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="text-[16px] font-semibold">
                  Top tenants por volume
                </CardTitle>
                <p className="text-[12px] text-muted-foreground">
                  Requests do período · a cor indica a modalidade, derivada dos
                  próprios contadores do tenant.
                </p>
              </CardHeader>
              <CardContent>
                <TenantVolumeBars rows={query.data.topTenants} />
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="text-[16px] font-semibold">
                  Local vs externo
                </CardTitle>
                <p className="text-[12px] text-muted-foreground">
                  Quanto do tráfego a GPU própria serviu — e o que isso evitou
                  de gasto externo.
                </p>
              </CardHeader>
              <CardContent>
                {economyQuery.data ? (
                  <LocalVsExternalDonut summary={economyQuery.data.summary} />
                ) : economyQuery.isLoading ? (
                  <Skeleton className="h-32 w-full" />
                ) : (
                  <p className="py-8 text-center text-[14px] text-muted-foreground">
                    Não foi possível carregar a economia do período.
                  </p>
                )}
              </CardContent>
            </Card>
          </div>

          {/* Trend chart. */}
          <Card>
            <CardHeader>
              <CardTitle className="text-[16px] font-semibold">
                Tendência
              </CardTitle>
            </CardHeader>
            <CardContent>
              {/* Every day in the range is present after fillDateGaps, so the
                  emptiness test is "no day carries data", not "no rows". */}
              {query.data.daily.every((d) => d.tokens === null) ? (
                <p className="py-8 text-center text-[14px] text-muted-foreground">
                  Sem dados no período.
                </p>
              ) : (
                <>
                  <ConsumoTrendChart rows={query.data.daily} />
                  <p className="mt-3 text-[12px] text-muted-foreground">
                    Dias sem nenhum registro de billing aparecem como lacuna na
                    linha — não como zero. Uma lacuna dentro de um período com
                    tráfego indica incidente de ingestão, não queda de uso.
                  </p>
                </>
              )}
            </CardContent>
          </Card>

          {/* Per-tenant breakdown. */}
          <Card>
            <CardHeader>
              <CardTitle className="text-[20px] font-semibold">
                Consumo por tenant
              </CardTitle>
            </CardHeader>
            <CardContent>
              <ConsumoTable rows={query.data.tenants} />
            </CardContent>
          </Card>
        </>
      ) : null}
    </div>
  );
}
