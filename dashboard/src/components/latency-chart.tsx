"use client";

/**
 * Latency percentile chart — a Recharts `LineChart` (via the shadcn `chart`
 * block) with three series: P50 / P95 / P99.
 *
 * UI-SPEC §Semantic status palette — "Chart series for P50/P95/P99 use
 * green / amber / red respectively so the three percentile lines are
 * distinguishable at a glance":
 *   P50 → --primary (green)
 *   P95 → --status-warning (amber)
 *   P99 → --destructive (red)
 * Axis labels are Label 12/600.
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
import type { LatencyRow } from "@/lib/gateway";

/** P50/P95/P99 → the 3-tier status palette tokens. */
const chartConfig = {
  p50: { label: "P50", color: "var(--primary)" },
  p95: { label: "P95", color: "var(--status-warning)" },
  p99: { label: "P99", color: "var(--destructive)" },
} satisfies ChartConfig;

export interface LatencyChartProps {
  /** Per-route latency rows from `fetchMetrics().by_route`. */
  rows: LatencyRow[];
}

export function LatencyChart({ rows }: LatencyChartProps) {
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-[260px] w-full">
      <LineChart data={rows} margin={{ top: 8, right: 16, bottom: 8, left: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="key"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          // Label 12/600 axis labels.
          className="text-[12px] font-semibold"
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={48}
          className="text-[12px] font-semibold tabular-nums"
          unit=" ms"
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        <ChartLegend content={<ChartLegendContent />} />
        <Line
          dataKey="p50"
          type="monotone"
          stroke="var(--color-p50)"
          strokeWidth={2}
          dot={false}
        />
        <Line
          dataKey="p95"
          type="monotone"
          stroke="var(--color-p95)"
          strokeWidth={2}
          dot={false}
        />
        <Line
          dataKey="p99"
          type="monotone"
          stroke="var(--color-p99)"
          strokeWidth={2}
          dot={false}
        />
      </LineChart>
    </ChartContainer>
  );
}
