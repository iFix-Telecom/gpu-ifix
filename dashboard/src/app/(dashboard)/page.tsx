"use client";

/**
 * Overview — the operator's at-a-glance screen (OBS-03).
 *
 * Layout (approved redesign mockup):
 *   - page title row + the "Atualizado há {n}s" stale indicator
 *   - 4 KPIs (requests / P95 / erro / custo GPU hoje)
 *   - "Por pod" — the primary + every secondary Vast instance
 *   - latency by route, as GROUPED BARS (routes are categories, not a series)
 *
 * Three INDEPENDENT queries feed the screen (`metrics`, `operations`,
 * `economy`). They are deliberately not merged into one queryFn: a failure in
 * `/admin/operations` must not blank the latency chart, and a failure in
 * `/admin/economy` must not blank the KPI row. Partial data beats no data on
 * an incident-triage screen.
 */

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { FsmPanel } from "@/components/fsm-panel";
import { KpiCard } from "@/components/kpi-card";
import { LatencyChart } from "@/components/latency-chart";
import { PodOverviewPanel } from "@/components/pod-overview-panel";
import { StaleIndicator } from "@/components/stale-indicator";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  aggregateErrorRate,
  aggregateP95,
  aggregateRequests,
  currentMonthRange,
  errorRateTier,
  formatBrl,
  formatCount,
  formatErrorRate,
  formatMs,
  latencyTier,
} from "@/lib/format";
import {
  fetchEconomy,
  fetchMetrics,
  fetchOperations,
  GatewayError,
  latencyByRoute,
  totalInflight,
} from "@/lib/gateway";

/** How many trailing days of R$/dia feed the cost KPI's sparkline. */
const COST_SPARK_DAYS = 14;

/** Section divider in the mockup's eyebrow style. */
function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="mt-2 flex items-center gap-2.5 text-[11px] font-semibold uppercase tracking-[0.1em] text-muted-foreground">
      {children}
      <span aria-hidden="true" className="h-px flex-1 bg-border" />
    </div>
  );
}

export default function OverviewPage() {
  const { data, isLoading, isError, error, dataUpdatedAt } = useQuery({
    queryKey: ["metrics"],
    queryFn: () => fetchMetrics(),
  });

  // Independent of the metrics query — see the file header.
  const operationsQuery = useQuery({
    queryKey: ["operations"],
    queryFn: () => fetchOperations(),
  });

  // Frozen on mount: recomputing the range every render would produce a new
  // queryKey object each pass and refetch forever.
  const [range] = useState(() => currentMonthRange());
  const economyQuery = useQuery({
    queryKey: ["economia-overview", range],
    queryFn: () => fetchEconomy(range.from, range.to),
  });

  // The gateway emits per-(tenant,route) rows + an InflightRow[] array —
  // it does NOT ship a `by_route` aggregate or a scalar inflight count.
  // Derive both client-side from the real response shape.
  const byRoute = data ? latencyByRoute(data.tenants) : [];
  const inflight = data ? totalInflight(data.inflight) : 0;

  const vastCost = operationsQuery.data?.vast_cost;
  /*
   * Only the COST KPI gets a sparkline, because only the cost has a real
   * history: `/admin/economy` returns a per-day series. `/admin/metrics` is a
   * 5-minute ROLLING WINDOW with no history at all, so a latency or error-rate
   * sparkline would have to be invented. Persisting periodic metric snapshots
   * (and charting them) is Fase 2 — until then those three KPIs stay bare.
   */
  const costSeries = economyQuery.data?.series
    .slice(-COST_SPARK_DAYS)
    .map((d) => d.vast_brl);

  return (
    <div className="flex flex-col gap-6">
      {/* Title row — Display heading + stale indicator. */}
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-[28px] font-bold leading-[1.2]">Visão geral</h1>
        <StaleIndicator updatedAt={dataUpdatedAt} />
      </div>

      {isLoading ? (
        <OverviewSkeleton />
      ) : isError ? (
        <Card>
          <CardContent className="py-8 text-center text-[14px] text-muted-foreground">
            {/* WR-06: surface the specific proxy/gateway cause when one is
                available, not a hardcoded generic string. */}
            {error instanceof GatewayError
              ? error.message
              : "Não foi possível carregar as métricas do gateway."}{" "}
            Verifique se o gateway está no ar e se a admin-key está válida,
            depois recarregue a página.
          </CardContent>
        </Card>
      ) : !data || data.tenants.length === 0 ? (
        <Card>
          <CardContent className="py-8 text-center">
            <p className="text-[20px] font-semibold">Sem dados no período</p>
            <p className="mt-1 text-[14px] text-muted-foreground">
              Nenhuma requisição registrada. Confirme que os tenants estão
              roteando pelo gateway.
            </p>
          </CardContent>
        </Card>
      ) : (
        <>
          {/* KPI row. */}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <KpiCard
              caption="Requests"
              value={formatCount(aggregateRequests(data.tenants))}
              hint={`janela ${data.window} · ${inflight} em voo`}
            />
            <KpiCard
              caption="P95 latência"
              value={formatMs(aggregateP95(data.tenants))}
              status={latencyTier(aggregateP95(data.tenants))}
              hint={`janela ${data.window}`}
            />
            <KpiCard
              caption="Taxa de erro"
              value={formatErrorRate(aggregateErrorRate(data.tenants))}
              status={errorRateTier(aggregateErrorRate(data.tenants))}
              hint="média ponderada por requests"
            />
            <KpiCard
              caption="Custo GPU hoje"
              value={vastCost ? formatBrl(vastCost.today_brl) : "—"}
              hint={
                vastCost
                  ? `mês ${formatBrl(vastCost.month_brl)} · budget ${vastCost.budget_pct_used}%`
                  : "custo indisponível"
              }
              series={costSeries}
              seriesColor="var(--chart-llm)"
            />
          </div>

          {/* "Por pod" — primary + every secondary Vast instance. */}
          <SectionLabel>Por pod</SectionLabel>
          {operationsQuery.isLoading ? (
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
              <Skeleton className="h-44 w-full" />
              <Skeleton className="h-44 w-full" />
            </div>
          ) : operationsQuery.data ? (
            <PodOverviewPanel
              operations={operationsQuery.data}
              metrics={data}
            />
          ) : (
            // Degraded, not fatal: the rest of the screen stays useful.
            <p className="text-[13px] text-muted-foreground">
              Não foi possível carregar o estado dos pods.
            </p>
          )}

          {/*
            Failover state + latency by route.

            `FsmPanel` is KEPT alongside the new pod cards because the two read
            different state machines: the pod card shows the primary's
            LIFECYCLE (asleep/provisioning/ready/…) from /admin/operations,
            while this shows the gateway's FAILOVER state
            (HEALTHY/DEGRADED/FAILED_OVER/OFF_HOURS) from /admin/metrics.
            "Pod dormindo + failover saudável" and "pod pronto + em failover"
            are both real, distinct situations — collapsing them would hide one.
          */}
          <SectionLabel>Latência &amp; failover</SectionLabel>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <FsmPanel fsmState={data.fsm_state} />
            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle className="text-[16px] font-semibold">
                  Latência por rota
                </CardTitle>
                <p className="text-[12px] text-muted-foreground">
                  Barras agrupadas P50/P95/P99 — rotas são categorias, não uma
                  série temporal.
                </p>
              </CardHeader>
              <CardContent>
                {byRoute.length === 0 ? (
                  <p className="py-8 text-center text-[14px] text-muted-foreground">
                    Sem dados no período
                  </p>
                ) : (
                  <LatencyChart rows={byRoute} />
                )}
              </CardContent>
            </Card>
          </div>
        </>
      )}
    </div>
  );
}

/** Initial-fetch skeleton — mirrors the KPI row + panels layout. */
function OverviewSkeleton() {
  return (
    <div className="flex flex-col gap-6">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-28 w-full" />
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Skeleton className="h-44 w-full" />
        <Skeleton className="h-44 w-full" />
      </div>
      <Skeleton className="h-64 w-full" />
    </div>
  );
}
