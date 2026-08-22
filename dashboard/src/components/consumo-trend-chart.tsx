"use client";

/**
 * Consumption trend chart — a Recharts `LineChart` (via the shadcn `chart`
 * block) with two series merged by date across all tenants:
 *   tokens   → --chart-llm (tokens/dia)
 *   cost_brl → --chart-ext (custo R$/dia)
 *
 * Tokens are large counts and cost is a sparse/small BRL value, so a shared
 * Y axis would flatten cost to zero. Each line gets its own axis via
 * `yAxisId`: tokens on the left, cost on the right.
 *
 * GAPS ARE LOAD-BEARING: the rows carry `null` for days with no billing row
 * (see `fillDateGaps`), and `connectNulls={false}` keeps the line BROKEN over
 * them. Bridging the gap would draw a smooth interpolation across days we have
 * no record for — exactly the artifact that made the ago/2026 billing-partition
 * outage look like normal traffic.
 */

import {
  CartesianGrid,
  Line,
  LineChart,
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
import type { DailyGapRow } from "@/lib/consumo";

/** tokens/dia + custo R$/dia → categorical palette (kind of thing, not status). */
const chartConfig = {
  tokens: { label: "Tokens/dia", color: "var(--chart-llm)" },
  cost_brl: { label: "Custo R$/dia", color: "var(--chart-ext)" },
} satisfies ChartConfig;

export interface ConsumoTrendChartProps {
  /**
   * Per-day rows over the FULL requested range — `fillDateGaps(aggregateDaily(…))`.
   * A `null` value means "no billing row for this day", not zero.
   */
  rows: DailyGapRow[];
}

export function ConsumoTrendChart({ rows }: ConsumoTrendChartProps) {
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-[260px] w-full">
      <LineChart data={rows} margin={{ top: 8, right: 16, bottom: 8, left: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="date"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          className="font-mono text-[11px]"
        />
        <YAxis
          yAxisId="tokens"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={56}
          className="font-mono text-[11px] tabular-nums"
        />
        <YAxis
          yAxisId="cost"
          orientation="right"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={56}
          className="font-mono text-[11px] tabular-nums"
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        <ChartLegend content={<ChartLegendContent />} />
        <Line
          yAxisId="tokens"
          dataKey="tokens"
          type="monotone"
          stroke="var(--color-tokens)"
          strokeWidth={2}
          dot={false}
          connectNulls={false}
        />
        <Line
          yAxisId="cost"
          dataKey="cost_brl"
          type="monotone"
          stroke="var(--color-cost_brl)"
          strokeWidth={2}
          dot={false}
          connectNulls={false}
        />
      </LineChart>
    </ChartContainer>
  );
}
