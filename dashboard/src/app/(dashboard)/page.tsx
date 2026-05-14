"use client";

/**
 * Overview — the operator's at-a-glance screen (OBS-03).
 *
 * Layout (UI-SPEC §Visual Hierarchy + §Spacing scale):
 *   - page title row + the "Atualizado há {n}s" stale indicator
 *   - KPI row (P95 / error rate / requests) — md/16px gap
 *   - FSM panel + latency chart — xl/32px section gap
 *
 * Polls `fetchMetrics` via `useQuery` (the provider's 5–10s refetchInterval
 * drives the cadence). Renders `skeleton` blocks on the initial fetch and the
 * "Sem dados no período" empty state when there are no tenants.
 */

import { useQuery } from "@tanstack/react-query";

import { FsmPanel } from "@/components/fsm-panel";
import { KpiCard } from "@/components/kpi-card";
import { LatencyChart } from "@/components/latency-chart";
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
  errorRateTier,
  formatCount,
  formatErrorRate,
  formatMs,
  latencyTier,
} from "@/lib/format";
import { fetchMetrics } from "@/lib/gateway";

export default function OverviewPage() {
  const { data, isLoading, isError, dataUpdatedAt } = useQuery({
    queryKey: ["metrics"],
    queryFn: () => fetchMetrics(),
  });

  return (
    <div className="flex flex-col gap-8">
      {/* Title row — Display heading + stale indicator. */}
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-[28px] font-semibold leading-[1.2]">Visão geral</h1>
        <StaleIndicator updatedAt={dataUpdatedAt} />
      </div>

      {isLoading ? (
        <OverviewSkeleton />
      ) : isError ? (
        <Card>
          <CardContent className="py-8 text-center text-[14px] text-muted-foreground">
            Não foi possível carregar as métricas do gateway. Verifique se o
            gateway está no ar e se a admin-key está válida, depois recarregue a
            página.
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
          {/* KPI row — md/16px gap. */}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
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
              caption="Requests"
              value={formatCount(aggregateRequests(data.tenants))}
              hint={`${data.inflight} em voo`}
            />
          </div>

          {/* FSM panel + latency chart — xl/32px section gap from the KPI row. */}
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <FsmPanel fsmState={data.fsm_state} />
            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle className="text-[20px] font-semibold">
                  Latência por rota
                </CardTitle>
              </CardHeader>
              <CardContent>
                {data.by_route.length === 0 ? (
                  <p className="py-8 text-center text-[14px] text-muted-foreground">
                    Sem dados no período
                  </p>
                ) : (
                  <LatencyChart rows={data.by_route} />
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
    <div className="flex flex-col gap-8">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-40 w-full lg:col-span-2" />
      </div>
    </div>
  );
}
