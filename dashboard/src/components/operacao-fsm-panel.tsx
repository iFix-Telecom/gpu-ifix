/**
 * Operação — primary FSM state + schedule panel.
 *
 * Renders the primary pod's FSM state, the emergency FSM state, leadership,
 * the active lifecycle/instance ids, and the schedule window (timezone,
 * up→down hours, days, should-be-provisioned-now, next transition).
 *
 * UI-SPEC §Semantic status palette — FSM state tier:
 *   ready                       → --primary (green)
 *   provisioning|draining       → --status-warning (amber)
 *   destroying|unknown          → --destructive / --muted
 *   asleep                      → --muted-foreground (neutral, off by design)
 */

import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import type { OperationsFSM, OperationsSchedule } from "@/lib/gateway";
import { cn } from "@/lib/utils";

/** Primary FSM state → badge classes. */
function primaryStateClass(state: string): string {
  switch (state) {
    case "ready":
      return "bg-primary/15 text-primary";
    case "provisioning":
    case "draining":
      return "bg-status-warning/15 text-status-warning";
    case "destroying":
      return "bg-destructive/15 text-destructive";
    default: // asleep | unknown
      return "bg-muted text-muted-foreground";
  }
}

/** Primary FSM state → pt-BR label. */
function primaryStateLabel(state: string): string {
  switch (state) {
    case "asleep":
      return "dormindo";
    case "provisioning":
      return "provisionando";
    case "ready":
      return "pronto";
    case "draining":
      return "drenando";
    case "destroying":
      return "destruindo";
    default:
      return "desconhecido";
  }
}

/** Next-transition kind → pt-BR verb. */
function transitionLabel(kind: string): string {
  switch (kind) {
    case "up":
      return "subir";
    case "down":
      return "descer";
    default:
      return "—";
  }
}

/** Format an RFC3339 timestamp in pt-BR, or "—" when empty/invalid. */
function formatDateTime(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("pt-BR");
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

export interface OperacaoFsmPanelProps {
  fsm: OperationsFSM;
  schedule: OperationsSchedule;
}

export function OperacaoFsmPanel({ fsm, schedule }: OperacaoFsmPanelProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">
          Estado do pod primário
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center gap-3">
          <Badge
            data-state={fsm.primary_state}
            className={cn(
              "text-[12px] font-semibold",
              primaryStateClass(fsm.primary_state),
            )}
          >
            {primaryStateLabel(fsm.primary_state)}
          </Badge>
          <span className="text-[12px] font-semibold text-muted-foreground">
            Emergência: {fsm.emerg_state}
          </span>
          <Badge
            className={cn(
              "text-[12px] font-semibold",
              fsm.is_leader
                ? "bg-primary/15 text-primary"
                : "bg-muted text-muted-foreground",
            )}
          >
            {fsm.is_leader ? "líder" : "não-líder"}
          </Badge>
        </div>

        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <Field label="Lifecycle ativo">
            {fsm.active_lifecycle_id || "—"}
          </Field>
          <Field label="Instância Vast">
            {fsm.active_instance_id || "—"}
          </Field>
          <Field label="Fuso horário">{schedule.timezone || "—"}</Field>
          <Field label="Janela">
            {String(schedule.up_hour).padStart(2, "0")}h →{" "}
            {String(schedule.down_hour).padStart(2, "0")}h
          </Field>
          <Field label="Dias">
            {schedule.days.length > 0 ? schedule.days.join(", ") : "—"}
          </Field>
          <Field label="Deve estar no ar agora?">
            <Badge
              className={cn(
                "text-[12px] font-semibold",
                schedule.should_be_provisioned_now
                  ? "bg-primary/15 text-primary"
                  : "bg-muted text-muted-foreground",
              )}
            >
              {schedule.should_be_provisioned_now ? "sim" : "não"}
            </Badge>
          </Field>
          <Field label="Próxima transição">
            {formatDateTime(schedule.next_transition_at)}
          </Field>
          <Field label="Tipo da transição">
            {transitionLabel(schedule.next_transition_kind)}
          </Field>
        </div>

        {schedule.disabled ? (
          <p className="text-[12px] text-muted-foreground">
            Agenda desativada (kill-switch ligado).
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}
