"use client";

/**
 * Surfaces A + B — pod-config hot-field editor + bounds editor (Phase 17).
 *
 * Client island fed by the RSC page (`/operacao/config`): `isOwner` + the live
 * `config` (16 hot fields) + `bounds` (numeric min/max pairs). It is the
 * editor's ONLY data source — values render from the server-fetched live DB
 * row, never stale boot env.
 *
 * Surface A (this file): the 16 hot fields in three grouped cards. OWNER gets a
 * per-field inline edit (pencil → typed control + Salvar/Cancelar; one save =
 * one server action = one audit row = one toast). The five DANGEROUS actions
 * (cap-down, schedule-narrow-while-up, Disabled=true, empty days, restrictive
 * allowlist) route through a one-click `alert-dialog` carrying the SPECIFIC
 * pt-BR impact string (D-04) — NO type-to-confirm. OPERATOR sees identical
 * values with NO edit affordances. The server action (Plan 17-05) re-checks
 * requireOwner — the UI gate is cosmetic.
 *
 * Surface B (bounds table) is appended in Task 3.
 */

import { Pencil } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { updatePodConfig, updatePodConfigBound } from "@/lib/admin-actions";
import type { PodConfigBounds, PodConfigSection } from "@/lib/gateway";

// ──────────────────────────────────────────────────────────────────────────
// Field metadata — the single source for the read-only render + the editor.
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

const GENERIC_ERROR =
  "Não foi possível salvar agora. Tente novamente em alguns segundos.";

/** Map a server-action throw to UI-SPEC inline copy. */
function mapSaveError(err: unknown): string {
  const msg = (err as Error)?.message ?? "";
  if (/owner/i.test(msg)) return "Edição restrita ao owner do dashboard.";
  return GENERIC_ERROR;
}

/** 14×14 inline pending spinner (verbatim from operator-controls). */
function Spinner() {
  return (
    <span
      aria-hidden
      className="inline-block size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
    />
  );
}

interface Danger {
  title: string;
  body: string;
  confirmLabel: string;
}

// ──────────────────────────────────────────────────────────────────────────

export interface PodConfigControlsProps {
  isOwner: boolean;
  config: PodConfigSection;
  bounds: PodConfigBounds;
}

export function PodConfigControls({
  isOwner,
  config: initialConfig,
  bounds,
}: PodConfigControlsProps) {
  const router = useRouter();
  const [config, setConfig] = useState<PodConfigSection>(initialConfig);

  /** Commit a saved value into the local snapshot + toast + reconcile. */
  function onSaved(f: HotField, value: unknown) {
    setConfig((prev) => ({ ...prev, [f.configKey]: value }));
    toast.success(
      f.nextProvision
        ? `${f.label} atualizado. Vale a partir do próximo provisionamento.`
        : `${f.label} atualizado.`,
    );
    router.refresh();
  }

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
              <HotFieldRow
                key={f.field}
                field={f}
                config={config}
                bounds={bounds}
                isOwner={isOwner}
                onSaved={onSaved}
              />
            ))}
          </CardContent>
        </Card>
      ))}

      {/* Surface B — owner-editable validation bounds. */}
      <BoundsCard isOwner={isOwner} bounds={bounds} />
    </div>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// Surface B — bounds editor (Campo | Mín | Máx) for the bounded numeric fields.
// ──────────────────────────────────────────────────────────────────────────

/** The bounded numeric hot fields (those carrying a min/max pair), in order. */
const BOUNDED: HotField[] = FIELD_GROUPS.flatMap((g) => g.fields).filter(
  (f): f is HotField & { minKey: keyof PodConfigBounds; maxKey: keyof PodConfigBounds } =>
    Boolean(f.minKey && f.maxKey),
);

function BoundsCard({
  isOwner,
  bounds: initialBounds,
}: {
  isOwner: boolean;
  bounds: PodConfigBounds;
}) {
  const router = useRouter();
  const [bounds, setBounds] = useState<PodConfigBounds>(initialBounds);

  function onBoundSaved(
    field: keyof PodConfigBounds,
    value: number,
    label: string,
  ) {
    setBounds((prev) => ({ ...prev, [field]: value }));
    toast.success(`Limite de ${label} atualizado.`);
    router.refresh();
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">
          Limites de validação
        </CardTitle>
        <CardDescription>
          Envelope de segurança de cada campo. O valor salvo é validado contra
          estes limites.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Campo</TableHead>
              <TableHead>Mín</TableHead>
              <TableHead>Máx</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {BOUNDED.map((f) => (
              <TableRow key={f.field}>
                <TableCell className="text-[14px]">{f.label}</TableCell>
                <BoundCell
                  isOwner={isOwner}
                  label={f.label}
                  side="min"
                  field={f.minKey as keyof PodConfigBounds}
                  counterpartField={f.maxKey as keyof PodConfigBounds}
                  bounds={bounds}
                  onSaved={onBoundSaved}
                />
                <BoundCell
                  isOwner={isOwner}
                  label={f.label}
                  side="max"
                  field={f.maxKey as keyof PodConfigBounds}
                  counterpartField={f.minKey as keyof PodConfigBounds}
                  bounds={bounds}
                  onSaved={onBoundSaved}
                />
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

/** One Mín/Máx cell — read-only value + (owner) inline number edit. */
function BoundCell({
  isOwner,
  label,
  side,
  field,
  counterpartField,
  bounds,
  onSaved,
}: {
  isOwner: boolean;
  label: string;
  side: "min" | "max";
  field: keyof PodConfigBounds;
  counterpartField: keyof PodConfigBounds;
  bounds: PodConfigBounds;
  onSaved: (field: keyof PodConfigBounds, value: number, label: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const value = bounds[field] as number;
  const counterpart = bounds[counterpartField] as number;

  function begin() {
    setDraft(String(value));
    setError(null);
    setEditing(true);
  }

  async function save() {
    const num = Number(draft);
    if (draft.trim() === "" || Number.isNaN(num)) {
      setError("Informe um número válido.");
      return;
    }
    // Cross-field rule (server-authoritative): min < max.
    if (
      (side === "min" && num >= counterpart) ||
      (side === "max" && num <= counterpart)
    ) {
      setError("O mínimo precisa ser menor que o máximo.");
      return;
    }
    setPending(true);
    setError(null);
    try {
      await updatePodConfigBound({ field, value: num });
      onSaved(field, num, label);
      setEditing(false);
    } catch (err) {
      setError(mapSaveError(err));
    } finally {
      setPending(false);
    }
  }

  if (!isOwner || !editing) {
    return (
      <TableCell className="tabular-nums">
        <div className="flex items-center gap-2">
          <span>{value}</span>
          {isOwner ? (
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground"
              aria-label={`Editar limite de ${label}`}
              onClick={begin}
            >
              <Pencil className="size-3.5" />
            </button>
          ) : null}
        </div>
      </TableCell>
    );
  }

  return (
    <TableCell className="tabular-nums">
      <div className="flex flex-col gap-2">
        <Input
          type="number"
          value={draft}
          disabled={pending}
          onChange={(e) => setDraft(e.target.value)}
          className="h-8 w-24 tabular-nums"
        />
        <div className="flex items-center gap-2">
          <Button type="button" size="sm" disabled={pending} onClick={save}>
            {pending ? (
              <span className="inline-flex items-center gap-2">
                <Spinner />
                Salvando…
              </span>
            ) : (
              "Salvar"
            )}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={pending}
            onClick={() => setEditing(false)}
          >
            Cancelar
          </Button>
        </div>
        {error ? (
          <span className="text-[12px] text-destructive">{error}</span>
        ) : null}
      </div>
    </TableCell>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// One hot field: read-only value + (owner) inline edit + dangerous confirm.
// ──────────────────────────────────────────────────────────────────────────

function HotFieldRow({
  field: f,
  config,
  bounds,
  isOwner,
  onSaved,
}: {
  field: HotField;
  config: PodConfigSection;
  bounds: PodConfigBounds;
  isOwner: boolean;
  onSaved: (f: HotField, value: unknown) => void;
}) {
  const [mode, setMode] = useState<"view" | "edit">("view");
  const [draft, setDraft] = useState("");
  const [draftDays, setDraftDays] = useState<string[]>([]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<{ danger: Danger; value: unknown } | null>(
    null,
  );

  const current = config[f.configKey];

  function beginEdit() {
    setError(null);
    if (f.kind === "csv") {
      setDraft(((current as number[]) ?? []).join(", "));
    } else if (f.kind === "days") {
      setDraftDays([...((current as string[]) ?? [])]);
    } else {
      setDraft(String(current));
    }
    setMode("edit");
  }

  function cancel() {
    setMode("view");
    setError(null);
  }

  /** Parse + validate the draft → typed value, or set an inline error. */
  function parseDraft(): { value: unknown } | { error: string } {
    if (f.kind === "csv") {
      const parts = draft
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      if (!parts.every((p) => /^\d+$/.test(p))) {
        return { error: "Use apenas IDs numéricos separados por vírgula." };
      }
      return { value: parts.map((p) => Number(p)) };
    }
    if (f.kind === "days") {
      return { value: DAY_ORDER.filter((d) => draftDays.includes(d)) };
    }
    // int / float
    const num = Number(draft);
    if (draft.trim() === "" || Number.isNaN(num)) {
      return { error: GENERIC_ERROR };
    }
    if (f.hour) {
      if (num < 0 || num > 23) {
        return { error: "A hora precisa estar entre 0 e 23." };
      }
      const other =
        f.field === "schedule_up_hour"
          ? config.schedule_down_hour
          : config.schedule_up_hour;
      if (num === other) {
        return { error: "Hora de descer não pode ser igual à de subir." };
      }
    }
    if (f.minKey && f.maxKey) {
      // Bounds are validated authoritatively server-side; this inline check
      // gives an immediate hint using the bounds the editor already knows.
      const lo = bounds[f.minKey] as number;
      const hi = bounds[f.maxKey] as number;
      if (num < lo || num > hi) {
        return {
          error:
            f.unit === "$" || f.unit === "R$"
              ? `O teto precisa estar entre ${lo} e ${hi}.`
              : `Valor fora do limite permitido (${lo}–${hi}).`,
        };
      }
    }
    return { value: f.kind === "int" ? Math.trunc(num) : num };
  }

  /** Decide whether a save is dangerous (D-04) and which confirm to show. */
  function dangerFor(value: unknown): Danger | null {
    if (
      (f.field === "cap_primary" || f.field === "cap_fallback") &&
      typeof value === "number" &&
      value < (current as number)
    ) {
      return {
        title: "Reduzir o teto de preço?",
        body: `Um teto abaixo do mercado pode impedir o provisionamento: se nenhuma oferta couber abaixo de ${value}, o pod não sobe até você aumentar o teto de volta.`,
        confirmLabel: "Reduzir teto",
      };
    }
    if (
      (f.field === "schedule_down_hour" &&
        typeof value === "number" &&
        value < config.schedule_down_hour) ||
      (f.field === "schedule_up_hour" &&
        typeof value === "number" &&
        value > config.schedule_up_hour)
    ) {
      return {
        title: "Estreitar a janela agora?",
        body: "Isto vai drenar o pod que está rodando AGORA. O atendimento pelo pod primário cai imediatamente até a próxima janela.",
        confirmLabel: "Estreitar agenda",
      };
    }
    if (f.field === "schedule_disabled" && value === true) {
      return {
        title: "Desativar a agenda?",
        body: "O kill-switch interrompe o provisionamento automático. O pod não sobe nos próximos horários até você reativar.",
        confirmLabel: "Desativar agenda",
      };
    }
    if (
      f.field === "schedule_days" &&
      Array.isArray(value) &&
      value.length === 0
    ) {
      return {
        title: "Salvar sem nenhum dia?",
        body: "Sem dias selecionados, o pod nunca será provisionado pela agenda.",
        confirmLabel: "Salvar mesmo assim",
      };
    }
    if (
      f.field === "allowlist" &&
      Array.isArray(value) &&
      value.length > 0 &&
      !(value as number[]).includes(config.host_id)
    ) {
      return {
        title: "Salvar esta allowlist?",
        body: "Esta allowlist exclui todos os hosts conhecidos. O provisionamento pode ficar sem ofertas elegíveis.",
        confirmLabel: "Salvar allowlist",
      };
    }
    return null;
  }

  /** Server write → on success update local snapshot + toast; else inline error. */
  async function commit(value: unknown) {
    setPending(true);
    setError(null);
    try {
      await updatePodConfig({ field: f.field, value });
      onSaved(f, value);
      setMode("view");
      setConfirm(null);
    } catch (err) {
      setError(mapSaveError(err));
      setConfirm(null);
    } finally {
      setPending(false);
    }
  }

  /** Salvar from the edit row (text/number/days/csv). */
  function attemptSave(value: unknown) {
    const danger = dangerFor(value);
    if (danger) {
      setConfirm({ danger, value });
      return;
    }
    void commit(value);
  }

  function onEditSave() {
    const parsed = parseDraft();
    if ("error" in parsed) {
      setError(parsed.error);
      return;
    }
    attemptSave(parsed.value);
  }

  // ── Switch fields: the switch is the control (no pencil). Owner toggles
  //    directly; Disabled=true routes through the dangerous confirm. ──────
  if (f.kind === "switch") {
    const checked = current as boolean;
    return (
      <div className="flex flex-col gap-0.5">
        <span className="text-[12px] font-semibold text-muted-foreground">
          {f.label}
        </span>
        {isOwner ? (
          <div className="flex items-center gap-2">
            <Switch
              checked={checked}
              disabled={pending}
              aria-label={`Editar ${f.label}`}
              onCheckedChange={(next) => attemptSave(next)}
            />
            <span className="text-[12px] text-muted-foreground">
              {checked ? "sim" : "não"}
            </span>
            {pending ? <Spinner /> : null}
          </div>
        ) : (
          <span className="text-[14px] tabular-nums">
            {checked ? "sim" : "não"}
          </span>
        )}
        {error ? (
          <span className="text-[12px] text-destructive">{error}</span>
        ) : null}
        <ConfirmDialog
          confirm={confirm}
          pending={pending}
          onCancel={() => setConfirm(null)}
          onConfirm={() => confirm && commit(confirm.value)}
        />
      </div>
    );
  }

  // ── Read-only (operator, or owner view mode) ──────────────────────────
  if (!isOwner || mode === "view") {
    return (
      <div className="flex flex-col gap-0.5">
        <span className="text-[12px] font-semibold text-muted-foreground">
          {f.label}
        </span>
        <div className="flex items-center gap-2">
          <span className="text-[14px] tabular-nums">
            {formatHotValue(f, config)}
          </span>
          {isOwner ? (
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground"
              aria-label={`Editar ${f.label}`}
              onClick={beginEdit}
            >
              <Pencil className="size-3.5" />
            </button>
          ) : null}
        </div>
      </div>
    );
  }

  // ── Owner edit mode (csv / int / float / days) ─────────────────────────
  return (
    <div className="flex flex-col gap-2">
      <span className="text-[12px] font-semibold text-muted-foreground">
        {f.label}
      </span>
      {f.kind === "days" ? (
        <div className="flex flex-wrap gap-1">
          {DAY_ORDER.map((d) => {
            const on = draftDays.includes(d);
            return (
              <Button
                key={d}
                type="button"
                size="sm"
                variant={on ? "default" : "outline"}
                disabled={pending}
                onClick={() =>
                  setDraftDays((prev) =>
                    prev.includes(d)
                      ? prev.filter((x) => x !== d)
                      : [...prev, d],
                  )
                }
              >
                {DAY_LABELS[d]}
              </Button>
            );
          })}
        </div>
      ) : (
        <Input
          type={f.kind === "csv" ? "text" : "number"}
          step={
            f.kind === "float"
              ? f.field === "monthly_budget_brl"
                ? 1
                : 0.01
              : 1
          }
          inputMode={f.kind === "csv" ? "text" : "numeric"}
          placeholder={
            f.kind === "csv"
              ? "IDs separados por vírgula — ex: 55942, 45778"
              : f.hour
                ? "0–23"
                : undefined
          }
          value={draft}
          disabled={pending}
          onChange={(e) => setDraft(e.target.value)}
          className="tabular-nums"
        />
      )}
      <div className="flex items-center gap-2">
        <Button type="button" size="sm" disabled={pending} onClick={onEditSave}>
          {pending ? (
            <span className="inline-flex items-center gap-2">
              <Spinner />
              Salvando…
            </span>
          ) : (
            "Salvar"
          )}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          disabled={pending}
          onClick={cancel}
        >
          Cancelar
        </Button>
      </div>
      {error ? (
        <span className="text-[12px] text-destructive">{error}</span>
      ) : null}
      <ConfirmDialog
        confirm={confirm}
        pending={pending}
        onCancel={() => setConfirm(null)}
        onConfirm={() => confirm && commit(confirm.value)}
      />
    </div>
  );
}

/**
 * One-click dangerous-action confirm (D-04) — specific impact string, named
 * destructive confirm, default-focus Cancelar, pending spinner. NO
 * type-to-confirm. Confirm is UX; the server action is the authoritative gate.
 */
function ConfirmDialog({
  confirm,
  pending,
  onCancel,
  onConfirm,
}: {
  confirm: { danger: Danger; value: unknown } | null;
  pending: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog
      open={confirm !== null}
      onOpenChange={(o) => {
        if (!o && !pending) onCancel();
      }}
    >
      <AlertDialogContent style={{ maxWidth: 384, padding: 24 }}>
        <AlertDialogHeader>
          <AlertDialogTitle>{confirm?.danger.title}</AlertDialogTitle>
          <AlertDialogDescription>
            {confirm?.danger.body}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter className="gap-2">
          <AlertDialogCancel disabled={pending}>Cancelar</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={pending}
            onClick={(e) => {
              e.preventDefault();
              onConfirm();
            }}
          >
            {pending ? (
              <span className="inline-flex items-center gap-2">
                <Spinner />
                {confirm?.danger.confirmLabel}
              </span>
            ) : (
              confirm?.danger.confirmLabel
            )}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
