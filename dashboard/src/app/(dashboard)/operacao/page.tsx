"use client";

/**
 * Operação — primary-pod operational health in one view.
 *
 * A single `fetchOperations` call (GET /admin/operations via the proxy)
 * drives four panels: FSM state + schedule, the lifecycle timeline, the
 * per-upstream breaker badges, and the Vast cost/budget. The panel is live —
 * React Query refetches every 10s (the gateway data sources poll ~5–10s, so
 * a tighter interval would not surface fresher numbers).
 *
 * Loading → skeletons; error → the pt-BR error state with a "Tentar
 * novamente" button (UI-SPEC §Copywriting).
 */

import { useQuery } from "@tanstack/react-query";

import { OperacaoBreakerBadges } from "@/components/operacao-breaker-badges";
import { OperacaoCostPanel } from "@/components/operacao-cost-panel";
import { OperacaoFsmPanel } from "@/components/operacao-fsm-panel";
import { OperacaoLifecycleTimeline } from "@/components/operacao-lifecycle-timeline";
import { StaleIndicator } from "@/components/stale-indicator";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { fetchOperations, GatewayError } from "@/lib/gateway";

/** WR-06: the specific proxy/gateway cause, or the generic fallback. */
function errorMessage(error: unknown): string {
  return error instanceof GatewayError
    ? error.message
    : "Não foi possível carregar o estado operacional do gateway.";
}

export default function OperacaoPage() {
  const query = useQuery({
    queryKey: ["operations"],
    queryFn: fetchOperations,
    // Live panel — the gateway data sources poll ~5–10s.
    refetchInterval: 10000,
  });

  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center justify-between gap-4">
        <h1 className="text-[28px] font-semibold leading-[1.2]">Operação</h1>
        <StaleIndicator updatedAt={query.dataUpdatedAt} />
      </div>

      {query.isLoading ? (
        <div className="flex flex-col gap-8">
          <Skeleton className="h-48 w-full" />
          <Skeleton className="h-32 w-full" />
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
          <OperacaoFsmPanel
            fsm={query.data.fsm}
            schedule={query.data.schedule}
          />
          <OperacaoCostPanel vastCost={query.data.vast_cost} />
          <OperacaoBreakerBadges breakers={query.data.breakers} />
          <OperacaoLifecycleTimeline lifecycles={query.data.lifecycles} />
        </>
      ) : null}
    </div>
  );
}
