"use client";

/**
 * Economia trend chart — a Recharts `ComposedChart` over the daily
 * `/admin/economy` series:
 *   vast_brl     → grouped BAR, --chart-llm  (custo Vast real/dia)
 *   phantom_brl  → grouped BAR, --chart-embed (custo externo evitado/dia)
 *   creditos_haiku_brl → grouped BAR, --chart-credit (Haiku passo ①, créditos)
 *   caixa_externo_brl  → grouped BAR, --chart-ext    (externo pago em caixa)
 *   economia_brl → LINE overlay, --primary   (economia líquida/dia =
 *                  phantom + créditos Haiku − Vast)
 *
 * The daily money amounts are what the day IS — discrete daily amounts, best
 * compared side by side as bars. The net saving is the derived headline, so it
 * rides over them as a line: here the X axis IS time, so a line is legitimate
 * (unlike latency-by-route, where the axis is categorical).
 *
 * Every series is BRL on the same scale → ONE shared Y axis.
 */

import {
  Bar,
  CartesianGrid,
  ComposedChart,
  Line,
  XAxis,
  YAxis,
} from "recharts";

import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart";
import type { EconomyDayRow } from "@/lib/gateway";

/**
 * Categorical palette: the bars are DIFFERENT KINDS of money (own GPU, saving,
 * credits, cash), not tiers of one status — so each takes its own category
 * hue, not the green/amber ramp. The net saving keeps the
 * brand accent because it is the number the screen exists to show.
 */
const chartConfig = {
  vast_brl: { label: "Vast R$/dia", color: "var(--chart-llm)" },
  phantom_brl: { label: "Economizado pelo pod R$/dia", color: "var(--chart-embed)" },
  creditos_haiku_brl: { label: "Haiku (créditos) R$/dia", color: "var(--chart-credit)" },
  caixa_externo_brl: { label: "Externo (caixa) R$/dia", color: "var(--chart-ext)" },
  economia_brl: { label: "Economia R$/dia", color: "var(--primary)" },
} satisfies ChartConfig;

export interface EconomyTrendChartProps {
  /** Daily series from `/admin/economy` (EconomyResponse.series). */
  rows: EconomyDayRow[];
}

export function EconomyTrendChart({ rows }: EconomyTrendChartProps) {
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-[260px] w-full">
      <ComposedChart
        data={rows}
        margin={{ top: 8, right: 16, bottom: 8, left: 8 }}
      >
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="date"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          className="font-mono text-[11px]"
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={56}
          className="font-mono text-[11px] tabular-nums"
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        <ChartLegend content={<ChartLegendContent />} />
        <Bar
          dataKey="vast_brl"
          fill="var(--color-vast_brl)"
          radius={[4, 4, 0, 0]}
        />
        <Bar
          dataKey="phantom_brl"
          fill="var(--color-phantom_brl)"
          radius={[4, 4, 0, 0]}
        />
        <Bar
          dataKey="creditos_haiku_brl"
          fill="var(--color-creditos_haiku_brl)"
          radius={[4, 4, 0, 0]}
        />
        <Bar
          dataKey="caixa_externo_brl"
          fill="var(--color-caixa_externo_brl)"
          radius={[4, 4, 0, 0]}
        />
        <Line
          dataKey="economia_brl"
          type="monotone"
          stroke="var(--color-economia_brl)"
          strokeWidth={2}
          dot={false}
        />
      </ComposedChart>
    </ChartContainer>
  );
}
