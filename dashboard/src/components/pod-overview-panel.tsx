"use client";

/**
 * Visão geral — the "Por pod" section (mockup `.pods`).
 *
 * Puts the FSM-managed primary (3090/LLM) and EVERY secondary Vast instance on
 * the account side by side. Before this panel the 3060 was invisible on the
 * overview even though it serves all STT traffic.
 *
 * HONESTY BOUNDARY — the two pod kinds carry DIFFERENT fields on purpose:
 *   - The primary card shows route P95/requests, but labels them
 *     "rota /chat (todos upstreams)". `/admin/metrics` aggregates by ROUTE, not
 *     by upstream, and that route is also served by the external fallback while
 *     the pod sleeps. Printing it as "the pod's latency" would be a lie.
 *   - Secondary cards show NO route, P95 or request count at all. The gateway
 *     has no pod↔route binding for externally managed instances
 *     (ops/vast-3060/vast3060.py owns the 3060) — that data simply does not
 *     exist, so nothing is rendered for it.
 */

import {
  primaryStateClass,
  primaryStateLabel,
} from "@/components/operacao-fsm-panel";
import { Badge } from "@/components/ui/badge";
import { formatBrl, formatCount, formatMs, formatUptime } from "@/lib/format";
import type {
  MetricsResponse,
  OperationsResponse,
  OperationsSecondaryPod,
  TenantMetricRow,
} from "@/lib/gateway";
import { cn } from "@/lib/utils";

/** The chat route as emitted by audit/middleware.go `routeTemplate`. */
const CHAT_ROUTE = "/v1/chat/completions";
/** The upstream whose breaker reflects the primary pod. */
const PRIMARY_UPSTREAM = "local-llm";

/**
 * Vast `actual_status` → badge classes. Mirrors
 * operacao-secondary-pods-panel.tsx — secondary pods speak Vast's raw status
 * vocabulary, not the primary FSM state names.
 */
function secondaryStatusClass(status: string): string {
  switch (status) {
    case "running":
      return "bg-primary/15 text-primary";
    case "loading":
    case "scheduling":
      return "bg-status-warning/15 text-status-warning";
    case "exited":
    case "offline":
      return "bg-destructive/15 text-destructive";
    default: // unknown | ""
      return "bg-muted text-muted-foreground";
  }
}

/** One label/value pair inside a pod card (mockup `.metric`). */
function Metric({
  label,
  children,
  className,
}: {
  label: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="text-[10.5px] font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <span
        className={cn(
          "truncate font-mono text-[15px] font-semibold tabular-nums",
          className,
        )}
      >
        {children}
      </span>
    </div>
  );
}

/** Card shell with the 3px categorical accent bar on the left edge. */
function PodCard({
  accent,
  title,
  badge,
  role,
  children,
}: {
  accent: string;
  title: string;
  badge: React.ReactNode;
  role: string;
  children: React.ReactNode;
}) {
  return (
    <div className="relative overflow-hidden rounded-xl border border-border bg-card px-5 py-4">
      <span
        aria-hidden="true"
        className="absolute inset-y-0 left-0 w-[3px]"
        style={{ background: accent }}
      />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-2 font-display text-[16px] font-semibold">
          <span
            aria-hidden="true"
            className="size-[9px] rounded-[2px]"
            style={{ background: accent }}
          />
          {title}
        </span>
        {badge}
      </div>
      <p className="mb-3.5 mt-0.5 text-[12.5px] text-muted-foreground">
        {role}
      </p>
      <div className="grid grid-cols-2 gap-x-2 gap-y-3 sm:grid-cols-3">
        {children}
      </div>
    </div>
  );
}

/**
 * Worst P95 + total requests for one route across every tenant row.
 * `/admin/metrics` ships per-(tenant,route) rows only — there is no by-route
 * aggregate server-side, so it is derived here. Max (not mean) for P95: an
 * at-a-glance SLO view must show the worst case, not hide it in an average.
 */
function routeStats(
  tenants: TenantMetricRow[],
  route: string,
): { p95: number; requests: number; hasRows: boolean } {
  const rows = tenants.filter((t) => t.route === route);
  if (rows.length === 0) return { p95: 0, requests: 0, hasRows: false };
  return {
    p95: Math.max(...rows.map((t) => t.p95)),
    requests: rows.reduce((sum, t) => sum + t.requests, 0),
    hasRows: true,
  };
}

export interface PodOverviewPanelProps {
  operations: OperationsResponse;
  metrics: MetricsResponse;
}

export function PodOverviewPanel({
  operations,
  metrics,
}: PodOverviewPanelProps) {
  const { fsm, vast_cost, breakers, secondary_pods } = operations;
  const chat = routeStats(metrics.tenants ?? [], CHAT_ROUTE);
  const breaker = breakers.find((b) => b.upstream === PRIMARY_UPSTREAM);

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <PodCard
        accent="var(--chart-llm)"
        title="Pod primário — LLM"
        role="GPU sob a FSM do gateway · agenda + failover automáticos"
        badge={
          <Badge
            data-state={fsm.primary_state}
            className={cn(
              "text-[11px] font-semibold",
              primaryStateClass(fsm.primary_state),
            )}
          >
            {primaryStateLabel(fsm.primary_state)}
          </Badge>
        }
      >
        <Metric label="Instância">
          {fsm.active_instance_id ? `#${fsm.active_instance_id}` : "—"}
        </Metric>
        <Metric label="Custo hoje">{formatBrl(vast_cost.today_brl)}</Metric>
        <Metric label="Custo mês">
          {formatBrl(vast_cost.month_brl)}{" "}
          <small className="font-medium text-muted-foreground">
            {vast_cost.budget_pct_used}% budget
          </small>
        </Metric>
        <Metric label="Breaker" className="text-[13px]">
          {breaker?.state ?? "—"}
        </Metric>
        {/*
          The label names the ROUTE, not the pod: while the primary sleeps this
          same route is served by the external fallback, so these two numbers
          are NOT "the 3090's latency".
        */}
        <Metric label="P95 · rota /chat (todos upstreams)">
          {chat.hasRows ? formatMs(chat.p95) : "—"}
        </Metric>
        <Metric label="Requests · rota /chat (todos upstreams)">
          {chat.hasRows ? formatCount(chat.requests) : "—"}
        </Metric>
      </PodCard>

      {secondary_pods.length === 0 ? (
        <div className="flex items-center rounded-xl border border-border bg-card px-5 py-4 text-[14px] text-muted-foreground">
          Nenhum outro pod ativo.
        </div>
      ) : (
        secondary_pods.map((pod) => (
          <SecondaryPodCard key={pod.id} pod={pod} />
        ))
      )}
    </div>
  );
}

function SecondaryPodCard({ pod }: { pod: OperationsSecondaryPod }) {
  return (
    <PodCard
      accent="var(--chart-stt)"
      title="Pod secundário"
      role="Instância Vast fora da FSM — gerenciada por script externo"
      badge={
        <Badge
          data-state={pod.status}
          className={cn(
            "text-[11px] font-semibold",
            secondaryStatusClass(pod.status),
          )}
        >
          {pod.status || "—"}
        </Badge>
      }
    >
      <Metric label="GPU">
        {pod.gpu_name || "—"} ×{pod.num_gpus}
      </Metric>
      <Metric label="Rótulo" className="text-[13px]">
        {pod.label || "—"}
      </Metric>
      <Metric label="Custo">{formatBrl(pod.dph_brl)}/h</Metric>
      <Metric label="No ar há">{formatUptime(pod.uptime_seconds)}</Metric>
      <Metric label="Instância">#{pod.id}</Metric>
    </PodCard>
  );
}
