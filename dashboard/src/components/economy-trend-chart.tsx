"use client";

/**
 * Economia trend chart — a Recharts `LineChart` (via the shadcn `chart` block)
 * with three series merged by BRT day:
 *   phantom_brl  → --chart-2         (custo phantom evitado/dia)
 *   vast_brl     → --status-warning  (custo Vast real/dia)
 *   economia_brl → --primary         (economia líquida/dia — o número-chave)
 *
 * Unlike consumo-trend-chart.tsx (tokens vs cost, dual axis), every series
 * here is BRL on the same scale → ONE shared Y axis (the per-series axis
 * binding is dropped).
 *
 * Axis labels are Label 12/600 per the UI-SPEC typography table.
 */

import { CartesianGrid, Line, LineChart, XAxis, YAxis } from "recharts";

import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart";
import type { EconomyDayRow } from "@/lib/gateway";

/** Three BRL/dia series → status palette tokens (single shared axis). */
const chartConfig = {
  phantom_brl: { label: "Phantom R$/dia", color: "var(--chart-2)" },
  vast_brl: { label: "Vast R$/dia", color: "var(--status-warning)" },
  economia_brl: { label: "Economia R$/dia", color: "var(--primary)" },
} satisfies ChartConfig;

export interface EconomyTrendChartProps {
  /** Daily series from `/admin/economy` (EconomyResponse.series). */
  rows: EconomyDayRow[];
}

export function EconomyTrendChart({ rows }: EconomyTrendChartProps) {
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-[260px] w-full">
      <LineChart data={rows} margin={{ top: 8, right: 16, bottom: 8, left: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="date"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          className="text-[12px] font-semibold"
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={56}
          className="text-[12px] font-semibold tabular-nums"
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        <ChartLegend content={<ChartLegendContent />} />
        <Line
          dataKey="phantom_brl"
          type="monotone"
          stroke="var(--color-phantom_brl)"
          strokeWidth={2}
          dot={false}
        />
        <Line
          dataKey="vast_brl"
          type="monotone"
          stroke="var(--color-vast_brl)"
          strokeWidth={2}
          dot={false}
        />
        <Line
          dataKey="economia_brl"
          type="monotone"
          stroke="var(--color-economia_brl)"
          strokeWidth={2}
          dot={false}
        />
      </LineChart>
    </ChartContainer>
  );
}
