/**
 * Economia — the OBS-09 KPI panel.
 *
 * Renders the KPI metrics side by side (CONTEXT §Números do painel) over the
 * server-computed `/admin/economy` summary:
 *   1. Líquido R$                   — economia_liquida_brl (phantom − Vast)
 *   2. ROI multiplier               — roi_multiplier (phantom per R$1 of GPU)
 *   3. Haiku (coberto por créditos) — custo_creditos_haiku_brl (step ① cost,
 *                                     paid by Max credits → caixa real R$ 0)
 *   4. Custo externo (caixa)        — custo_externo_caixa_brl (real external
 *                                     cash spend, WITHOUT the Haiku step ①)
 *   5. Custo Vast (GPU)             — vast_brl (3090 + 3060); hint shows the
 *                                     3060 share (vast_3060_brl)
 *   6. % servido local              — pct_servido_local_tokens (by TOKENS);
 *                                     hint keeps pct_servido_local (requests)
 *   7. Horas pod UP                 — horas_pod_up (primary pod-up hours)
 *
 * 23-13 (HAIKU-09): `custo_openrouter_brl` (external total incl. Haiku) is no
 * longer a card — it mixed credit-covered spend with real cash.
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
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-7">
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
            caption="Haiku (coberto por créditos)"
            value={formatBrl(summary.custo_creditos_haiku_brl)}
            hint={`Caixa real: ${formatBrl(0)}`}
          />
          <KpiCard
            caption="Custo externo (caixa)"
            value={formatBrl(summary.custo_externo_caixa_brl)}
            hint="Gasto real fora dos créditos"
          />
          <KpiCard
            caption="Custo Vast (GPU)"
            value={formatBrl(summary.vast_brl)}
            hint={`3060: ${formatBrl(summary.vast_3060_brl)}`}
          />
          <KpiCard
            caption="% servido local"
            value={formatPctLocal(summary.pct_servido_local_tokens)}
            hint={`Por tokens · requests: ${formatPctLocal(summary.pct_servido_local)}`}
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
