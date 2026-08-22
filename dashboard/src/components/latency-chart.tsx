"use client";

/**
 * Latency percentile chart — a Recharts `BarChart` (via the shadcn `chart`
 * block) with three GROUPED bars per route: P50 / P95 / P99.
 *
 * WHY BARS AND NOT A LINE (approved-mockup rule #4):
 * the X axis here is a set of ROUTES (/chat, /embeddings, /audio) — unordered
 * CATEGORIES, not a continuum. A line connecting them draws a slope between
 * "chat" and "audio", which asserts a rate of change that does not exist;
 * operators read that slope as a trend and misjudge which route is degrading.
 * Grouped bars compare categories without implying any continuity. Latency
 * over TIME would legitimately be a line — but `/admin/metrics` is a 5-minute
 * rolling window with no history, so that chart cannot be drawn honestly yet.
 *
 * UI-SPEC §Semantic status palette — the P50/P95/P99 series keep the
 * green / amber / red status ramp so the worst percentile reads as the worst:
 *   P50 → --primary          (green)
 *   P95 → --status-warning   (amber)
 *   P99 → --destructive      (red)
 */

import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";

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
  /** Per-route latency rows — derived via `latencyByRoute(tenants)`. */
  rows: LatencyRow[];
}

export function LatencyChart({ rows }: LatencyChartProps) {
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-[260px] w-full">
      <BarChart data={rows} margin={{ top: 8, right: 16, bottom: 8, left: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey="key"
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          className="font-mono text-[11px]"
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          width={48}
          className="font-mono text-[11px] tabular-nums"
          unit=" ms"
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        <ChartLegend content={<ChartLegendContent />} />
        <Bar dataKey="p50" fill="var(--color-p50)" radius={[4, 4, 0, 0]} />
        <Bar dataKey="p95" fill="var(--color-p95)" radius={[4, 4, 0, 0]} />
        <Bar dataKey="p99" fill="var(--color-p99)" radius={[4, 4, 0, 0]} />
      </BarChart>
    </ChartContainer>
  );
}
