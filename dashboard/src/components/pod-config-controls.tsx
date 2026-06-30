"use client";

/**
 * Surfaces A + B — pod-config hot-field editor + bounds editor (Phase 17).
 *
 * Client island fed by the RSC page (`/operacao/config`): `isOwner` + the live
 * `config` (16 hot fields) + `bounds` (numeric min/max pairs). It is the
 * editor's ONLY data source — the values render from the server-fetched live DB
 * row, never stale boot env.
 *
 * This Task-1 base renders the 16 hot fields READ-ONLY in three grouped cards
 * (the UI-SPEC "Config view — read-only default" state, and the operator view).
 * Task 2 layers the owner per-field inline edit + the five dangerous-action
 * confirms on top; Task 3 adds the bounds table (Surface B).
 */

import type { PodConfigBounds, PodConfigSection } from "@/lib/gateway";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

// ──────────────────────────────────────────────────────────────────────────
// Field metadata — the single source for both the read-only render and the
// Task-2 owner editor.
// ──────────────────────────────────────────────────────────────────────────

export type FieldKind = "csv" | "int" | "float" | "switch" | "days";

export interface HotField {
  /** Gateway PATCH `field` name (admin-actions CONFIG_FIELDS key). */
  field: string;
  label: string;
  kind: FieldKind;
  configKey: keyof PodConfigSection;
  /** Muted unit suffix shown after a numeric value ($/R$). */
  unit?: string;
  minKey?: keyof PodConfigBounds;
  maxKey?: keyof PodConfigBounds;
  /** Hour field — 0-23 + up≠down cross-field rule. */
  hour?: boolean;
  /** Toast variant: true → "Vale a partir do próximo provisionamento." */
  nextProvision: boolean;
}

export interface FieldGroup {
  title: string;
  description: string;
  fields: HotField[];
}

export const FIELD_GROUPS: FieldGroup[] = [
  {
    title: "Seleção de oferta",
    description: "Filtros que o reconciler aplica ao buscar a próxima oferta Vast.",
    fields: [
      {
        field: "blocklist",
        label: "Blocklist de máquinas",
        kind: "csv",
        configKey: "vast_machine_blocklist",
        nextProvision: true,
      },
      {
        field: "allowlist",
        label: "Allowlist de máquinas",
        kind: "csv",
        configKey: "vast_machine_allowlist",
        nextProvision: true,
      },
      {
        field: "cap_primary",
        label: "Teto de preço (primário)",
        kind: "float",
        configKey: "cap_primary",
        unit: "$",
        minKey: "cap_primary_min",
        maxKey: "cap_primary_max",
        nextProvision: true,
      },
      {
        field: "cap_fallback",
        label: "Teto de preço (fallback)",
        kind: "float",
        configKey: "cap_fallback",
        unit: "$",
        minKey: "cap_fallback_min",
        maxKey: "cap_fallback_max",
        nextProvision: true,
      },
      {
        field: "host_id",
        label: "Host ID",
        kind: "int",
        configKey: "host_id",
        nextProvision: true,
      },
      {
        field: "reject_private_ip",
        label: "Rejeitar IP privado",
        kind: "switch",
        configKey: "reject_private_ip",
        nextProvision: true,
      },
    ],
  },
  {
    title: "Orçamentos e timeouts",
    description: "Limites de custo e prazos de provisionamento.",
    fields: [
      {
        field: "coldstart_budget_s",
        label: "Orçamento de cold-start (s)",
        kind: "int",
        configKey: "coldstart_budget_s",
        minKey: "coldstart_budget_s_min",
        maxKey: "coldstart_budget_s_max",
        nextProvision: true,
      },
      {
        field: "port_bind_budget_s",
        label: "Orçamento de bind de porta (s)",
        kind: "int",
        configKey: "port_bind_budget_s",
        minKey: "port_bind_budget_s_min",
        maxKey: "port_bind_budget_s_max",
        nextProvision: true,
      },
      {
        field: "failure_cooldown_s",
        label: "Cooldown de falha (s)",
        kind: "int",
        configKey: "failure_cooldown_s",
        minKey: "failure_cooldown_s_min",
        maxKey: "failure_cooldown_s_max",
        nextProvision: false,
      },
      {
        field: "monthly_budget_brl",
        label: "Orçamento mensal (R$)",
        kind: "float",
        configKey: "monthly_budget_brl",
        unit: "R$",
        minKey: "monthly_budget_brl_min",
        maxKey: "monthly_budget_brl_max",
        nextProvision: false,
      },
    ],
  },
  {
    title: "Agenda",
    description: "Janela de funcionamento do pod e kill-switch.",
    fields: [
      {
        field: "schedule_up_hour",
        label: "Hora de subir",
        kind: "int",
        configKey: "schedule_up_hour",
        minKey: "schedule_up_hour_min",
        maxKey: "schedule_up_hour_max",
        hour: true,
        nextProvision: false,
      },
      {
        field: "schedule_down_hour",
        label: "Hora de descer",
        kind: "int",
        configKey: "schedule_down_hour",
        minKey: "schedule_down_hour_min",
        maxKey: "schedule_down_hour_max",
        hour: true,
        nextProvision: false,
      },
      {
        field: "schedule_days",
        label: "Dias",
        kind: "days",
        configKey: "schedule_days",
        nextProvision: false,
      },
      {
        field: "grace_ramp_down_s",
        label: "Grace de ramp-down (s)",
        kind: "int",
        configKey: "grace_ramp_down_s",
        minKey: "grace_ramp_down_s_min",
        maxKey: "grace_ramp_down_s_max",
        nextProvision: false,
      },
      {
        field: "provision_lead_s",
        label: "Lead de provisionamento (s)",
        kind: "int",
        configKey: "provision_lead_s",
        minKey: "provision_lead_s_min",
        maxKey: "provision_lead_s_max",
        nextProvision: false,
      },
      {
        field: "schedule_disabled",
        label: "Agenda desativada",
        kind: "switch",
        configKey: "schedule_disabled",
        nextProvision: false,
      },
    ],
  },
];

export const DAY_ORDER = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
export const DAY_LABELS: Record<string, string> = {
  mon: "seg",
  tue: "ter",
  wed: "qua",
  thu: "qui",
  fri: "sex",
  sat: "sáb",
  sun: "dom",
};

/** Render a hot field's current value as pt-BR text (read-only display). */
export function formatHotValue(f: HotField, config: PodConfigSection): string {
  const v = config[f.configKey];
  switch (f.kind) {
    case "csv": {
      const arr = (v as number[]) ?? [];
      return arr.length > 0 ? arr.join(", ") : "—";
    }
    case "switch":
      return v ? "sim" : "não";
    case "days": {
      const days = (v as string[]) ?? [];
      return days.length > 0
        ? days.map((d) => DAY_LABELS[d] ?? d).join(", ")
        : "—";
    }
    default: {
      const num = String(v);
      return f.unit ? `${num} ${f.unit}` : num;
    }
  }
}

// ──────────────────────────────────────────────────────────────────────────

export interface PodConfigControlsProps {
  isOwner: boolean;
  config: PodConfigSection;
  bounds: PodConfigBounds;
}

/** Read-only Field idiom (matches operacao-fsm-panel). */
function ReadOnlyField({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[12px] font-semibold text-muted-foreground">
        {label}
      </span>
      <span className="text-[14px] tabular-nums">{value}</span>
    </div>
  );
}

export function PodConfigControls({ config }: PodConfigControlsProps) {
  return (
    <div className="flex flex-col gap-8">
      {FIELD_GROUPS.map((group) => (
        <Card key={group.title}>
          <CardHeader>
            <CardTitle className="text-[20px] font-semibold">
              {group.title}
            </CardTitle>
            <CardDescription>{group.description}</CardDescription>
          </CardHeader>
          <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-3">
            {group.fields.map((f) => (
              <ReadOnlyField
                key={f.field}
                label={f.label}
                value={formatHotValue(f, config)}
              />
            ))}
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
