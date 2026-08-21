"use client";

/**
 * Operação — "Outros pods" panel.
 *
 * Lists every Vast instance on the account EXCEPT the active primary (which
 * the FSM panel already shows). Read-only: no start/stop/destroy affordances.
 * The 3060 STT/TTS pod (managed externally by ops/vast-3060/vast3060.py) shows
 * here today; any future pod added to the Vast account appears automatically
 * with no code change. Empty → the "Nenhum outro pod ativo." state.
 *
 * Mirrors the operacao-fsm-panel styling (Field helper, Badge classes, grid).
 */

import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { formatBrl, formatUptime } from "@/lib/format";
import type { OperationsSecondaryPod } from "@/lib/gateway";
import { cn } from "@/lib/utils";

/**
 * Vast actual_status → badge classes (UI-SPEC §Semantic status palette).
 * Local to this panel — the secondary pods use Vast's raw status vocabulary,
 * not the primary FSM state names.
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

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[12px] font-semibold text-muted-foreground">
        {label}
      </span>
      <span className="text-[14px] tabular-nums">{children}</span>
    </div>
  );
}

export interface OperacaoSecondaryPodsPanelProps {
  pods: OperationsSecondaryPod[];
}

export function OperacaoSecondaryPodsPanel({
  pods,
}: OperacaoSecondaryPodsPanelProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">Outros pods</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {pods.length === 0 ? (
          <p className="text-[14px] text-muted-foreground">
            Nenhum outro pod ativo.
          </p>
        ) : (
          pods.map((pod) => (
            <div
              key={pod.id}
              className="flex flex-col gap-3 rounded-lg border border-border p-4"
            >
              <div className="flex flex-wrap items-center gap-3">
                <Badge
                  data-state={pod.status}
                  className={cn(
                    "text-[12px] font-semibold",
                    secondaryStatusClass(pod.status),
                  )}
                >
                  {pod.status || "—"}
                </Badge>
                <span className="text-[14px] font-semibold">
                  {pod.gpu_name || "—"} ×{pod.num_gpus}
                </span>
                <span className="text-[12px] text-muted-foreground tabular-nums">
                  #{pod.id}
                </span>
              </div>

              <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
                <Field label="Rótulo">{pod.label || "—"}</Field>
                <Field label="Custo">{formatBrl(pod.dph_brl)}/h</Field>
                <Field label="No ar há">
                  {formatUptime(pod.uptime_seconds)}
                </Field>
              </div>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}
