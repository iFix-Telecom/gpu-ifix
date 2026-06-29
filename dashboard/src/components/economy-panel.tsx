/**
 * Economia — the OBS-09 KPI panel.
 *
 * Renders the KPI metrics side by side (CONTEXT §Números do painel) over the
 * server-computed `/admin/economy` summary:
 *   1. Líquido R$        — economia_liquida_brl (phantom − Vast; positive = saved)
 *   2. ROI multiplier    — roi_multiplier (phantom avoided per R$1 of GPU)
 *   3. Custo OpenRouter   — custo_openrouter_brl (real external spend, pod DOWN)
 *   4. Custo Vast (GPU)   — vast_brl (real Vast spend: closed primary_lifecycles
 *                           total_cost_brl + live accrual for the open lifecycle)
 *   5. % servido local    — pct_servido_local (fraction served by the GPU)
 *   6. Horas pod UP       — horas_pod_up (pod-up hours in the period)
 *
 * ROI and % local are nullable server-side (denominator zero → JSON null) and
 * render "—" rather than Inf/NaN. Mirrors operacao-cost-panel.tsx's KPI grid.
 */

import { KpiCard } from "@/components/kpi-card";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { formatBrl } from "@/lib/format";
import type { EconomyResponse } from "@/lib/gateway";

export interface EconomyPanelProps {
  summary: EconomyResponse["summary"];
}

/** ROI multiplier → "4,02×" or "—" when null (vast_brl == 0). */
function formatRoi(roi: number | null): string {
  if (roi === null) return "—";
  return `${roi.toLocaleString("pt-BR", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}×`;
}

/** Local-served fraction (0–1) → "87,0 %" or "—" when null (no requests). */
function formatPctLocal(pct: number | null): string {
  if (pct === null) return "—";
  return `${(pct * 100).toLocaleString("pt-BR", {
    minimumFractionDigits: 1,
    maximumFractionDigits: 1,
  })} %`;
}

/** Pod-up hours → "56,5 h". */
function formatHoras(horas: number): string {
  return `${horas.toLocaleString("pt-BR", {
    minimumFractionDigits: 1,
    maximumFractionDigits: 1,
  })} h`;
}

export function EconomyPanel({ summary }: EconomyPanelProps) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[20px] font-semibold">Economia</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          <KpiCard
            caption="Líquido R$"
            value={formatBrl(summary.economia_liquida_brl)}
            hint="Phantom − Vast no período"
          />
          <KpiCard
            caption="ROI multiplier"
            value={formatRoi(summary.roi_multiplier)}
            hint="Phantom por R$1 de GPU"
          />
          <KpiCard
            caption="Custo OpenRouter"
            value={formatBrl(summary.custo_openrouter_brl)}
            hint="Fallback (pod down)"
          />
          <KpiCard
            caption="Custo Vast (GPU)"
            value={formatBrl(summary.vast_brl)}
            hint="Gasto real Vast.ai"
          />
          <KpiCard
            caption="% servido local"
            value={formatPctLocal(summary.pct_servido_local)}
            hint="Requests na GPU"
          />
          <KpiCard
            caption="Horas pod UP"
            value={formatHoras(summary.horas_pod_up)}
            hint="No período"
          />
        </div>
      </CardContent>
    </Card>
  );
}
