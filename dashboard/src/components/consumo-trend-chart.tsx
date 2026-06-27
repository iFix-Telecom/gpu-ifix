"use client";

/**
 * Consumption trend chart — a Recharts `LineChart` (via the shadcn `chart`
 * block) with two series merged by date across all tenants:
 *   tokens   → --primary (tokens/dia)
 *   cost_brl → --status-warning (custo R$/dia)
 *
 * Tokens are large counts and cost is a sparse/small BRL value, so a shared
 * Y axis would flatten cost to zero. Each line gets its own axis via
 * `yAxisId`: tokens on the left, cost on the right.
 *
 * Axis labels are Label 12/600 per the UI-SPEC typography table.
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
import type { DailyAggRow } from "@/lib/consumo";

/** tokens/dia + custo R$/dia → status palette tokens. */
const chartConfig = {
  tokens: { label: "Tokens/dia", color: "var(--primary)" },
  cost_brl: { label: "Custo R$/dia", color: "var(--status-warning)" },
} satisfies ChartConfig;

export interface ConsumoTrendChartProps {
  /** Merged per-day rows — derived via `aggregateDaily(responses)`. */
  rows: DailyAggRow[];
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
          className="text-[12px] font-semibold"
        />
        <YAxis
          yAxisId="tokens"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={56}
          className="text-[12px] font-semibold tabular-nums"
        />
        <YAxis
          yAxisId="cost"
          orientation="right"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={56}
          className="text-[12px] font-semibold tabular-nums"
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
        />
        <Line
          yAxisId="cost"
          dataKey="cost_brl"
          type="monotone"
          stroke="var(--color-cost_brl)"
          strokeWidth={2}
          dot={false}
        />
      </LineChart>
    </ChartContainer>
  );
}
