"use client";

/**
 * Surface C — live provisioning-status panel (Phase 17, D-05).
 *
 * Polls `GET /admin/primary/lifecycle` every 10s via React Query (same idiom as
 * `operacao/page.tsx`) and renders the primary FSM state badge + leadership +
 * emergency state, then the OPEN lifecycle's event trail (offer accepted →
 * first health pass → ready, or a `--destructive` terminal row when a
 * `shutdown_reason` is present). The event steps are derived from the typed
 * timestamp fields on the open lifecycle (started_at / first_health_pass_at /
 * drain_started_at / ended_at + shutdown_reason) — never from the opaque
 * `events` jsonb, so the trail stays type-safe.
 *
 * Read-only for BOTH roles (D-05/D-07) — no edit affordance, no pod-relaunch
 * control of any kind.
 * Loading → skeleton; empty → pt-BR "nenhum provisionamento"; error → pt-BR
 * error + "Tentar novamente" (UI-SPEC §Copywriting).
 */

import { useQuery } from "@tanstack/react-query";

import {
  primaryStateClass,
  primaryStateLabel,
} from "@/components/operacao-fsm-panel";
import { StaleIndicator } from "@/components/stale-indicator";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  fetchPrimaryLifecycle,
  GatewayError,
  type PrimaryLifecycleOpen,
} from "@/lib/gateway";
import { fsmMeta, tierTextClass, type StatusTier } from "@/lib/fsm";
import { cn } from "@/lib/utils";

/** The specific proxy/gateway cause, or the generic fallback. */
function errorMessage(error: unknown): string {
  return error instanceof GatewayError
    ? error.message
    : "Não foi possível carregar o estado de provisionamento. Verifique se o gateway está no ar e use Tentar novamente.";
}

/** Format an RFC3339 timestamp as a pt-BR time, or "—" when empty/invalid. */
function formatTime(iso: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("pt-BR");
}

interface TrailStep {
  label: string;
  at: string | null;
  tier: StatusTier;
}

/**
 * Derive the ordered event trail from the open lifecycle's typed timestamp
 * fields. A `shutdown_reason` (failure) renders as a terminal `--destructive`
 * row.
 */
function buildTrail(
  open: PrimaryLifecycleOpen,
  fsmState: string,
): TrailStep[] {
  const steps: TrailStep[] = [];

  steps.push({
    label:
      open.accepted_dph != null
        ? `Oferta aceita · $${open.accepted_dph.toFixed(3)}/h`
        : "Oferta aceita · provisionando",
    at: open.started_at,
    tier: "warning",
  });

  if (open.first_health_pass_at) {
    steps.push({
      label: "Primeiro health check OK",
      at: open.first_health_pass_at,
      tier: "healthy",
    });
  }

  if (fsmState === "ready" && open.first_health_pass_at) {
    steps.push({
      label: "Pod pronto",
      at: open.first_health_pass_at,
      tier: "healthy",
    });
  }

  if (open.drain_started_at) {
    steps.push({
      label: "Drenagem iniciada",
      at: open.drain_started_at,
      tier: "warning",
    });
  }

  if (open.shutdown_reason || open.ended_at) {
    steps.push({
      label: open.shutdown_reason
        ? `Encerrado: ${open.shutdown_reason}`
        : "Encerrado",
      at: open.ended_at,
      tier: "critical",
    });
  }

  return steps;
}

export function PodConfigLivePanel() {
  const query = useQuery({
    queryKey: ["primary-lifecycle"],
    queryFn: fetchPrimaryLifecycle,
    // Live panel — matches the operacao poll cadence (D-05).
    refetchInterval: 10000,
  });

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4 space-y-0">
        <CardTitle className="text-[20px] font-semibold">
          Provisionamento ao vivo
        </CardTitle>
        <StaleIndicator updatedAt={query.dataUpdatedAt} />
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {query.isLoading ? (
          <div className="flex flex-col gap-3">
            <div className="h-6 w-40 animate-pulse rounded bg-muted" />
            <div className="h-24 w-full animate-pulse rounded bg-muted" />
          </div>
        ) : query.isError ? (
          <div className="flex flex-col items-center gap-4 py-6 text-center">
            <p className="text-[14px] text-muted-foreground">
              {errorMessage(query.error)}
            </p>
            <Button size="sm" variant="outline" onClick={() => query.refetch()}>
              Tentar novamente
            </Button>
          </div>
        ) : query.data ? (
          <>
            <div className="flex flex-wrap items-center gap-3">
              <Badge
                data-state={query.data.fsm_state}
                className={cn(
                  "text-[12px] font-semibold",
                  primaryStateClass(query.data.fsm_state),
                )}
              >
                {primaryStateLabel(query.data.fsm_state)}
              </Badge>
              <Badge
                className={cn(
                  "text-[12px] font-semibold",
                  query.data.leader
                    ? "bg-primary/15 text-primary"
                    : "bg-muted text-muted-foreground",
                )}
              >
                {query.data.leader ? "líder" : "não-líder"}
              </Badge>
              <span className="text-[12px] font-semibold text-muted-foreground">
                Emergência: {fsmMeta(query.data.emergency_state).label}
              </span>
            </div>

            {query.data.open_lifecycle ? (
              <ol className="flex flex-col gap-2">
                {buildTrail(
                  query.data.open_lifecycle,
                  query.data.fsm_state,
                ).map((step, i) => (
                  <li
                    key={`${step.label}-${i}`}
                    className="flex items-center gap-2"
                  >
                    <span
                      aria-hidden
                      className={cn(
                        "size-2 shrink-0 rounded-full bg-current",
                        tierTextClass(step.tier),
                      )}
                    />
                    <span className="text-[14px]">{step.label}</span>
                    <span className="ml-auto text-[12px] tabular-nums text-muted-foreground">
                      {formatTime(step.at)}
                    </span>
                  </li>
                ))}
              </ol>
            ) : (
              <p className="text-[14px] text-muted-foreground">
                Nenhum provisionamento em curso. O pod está dormindo ou
                aguardando a janela.
              </p>
            )}
          </>
        ) : null}
      </CardContent>
    </Card>
  );
}
