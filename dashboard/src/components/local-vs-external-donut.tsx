"use client";

/**
 * "Local vs externo" — the share of traffic the own GPU served, as a donut
 * (mockup: two stroked circles with dasharray/dashoffset).
 *
 * HONESTY RULE: `pct_servido_local` is `*float64` server-side and comes back
 * `null` when the denominator (total requests) is zero — the gateway refuses
 * to emit Inf/NaN. When it is null there is NOTHING to slice, so this renders
 * the empty state and NO donut. Coercing null to 0 would draw a full purple
 * ring reading "0% local / 100% external", which is a factual claim about a
 * period in which nothing happened.
 */

import { formatBrl } from "@/lib/format";
import type { EconomyResponse } from "@/lib/gateway";

/** Donut geometry — r=46, stroke 18, on a 120×120 viewBox (mockup). */
const R = 46;
const CIRCUMFERENCE = 2 * Math.PI * R;

export interface LocalVsExternalDonutProps {
  summary: EconomyResponse["summary"];
}

export function LocalVsExternalDonut({ summary }: LocalVsExternalDonutProps) {
  const pct = summary.pct_servido_local;

  if (pct === null) {
    return (
      <p className="py-8 text-center text-[14px] text-muted-foreground">
        Sem dados no período.
      </p>
    );
  }

  // Clamp defensively: a share outside [0,1] would produce a wrapping arc.
  const local = Math.min(Math.max(pct, 0), 1);
  const localLen = CIRCUMFERENCE * local;
  const externalLen = CIRCUMFERENCE - localLen;

  return (
    <div className="flex flex-wrap items-center gap-6 py-2">
      <svg viewBox="0 0 120 120" width={120} height={120} role="img">
        <title>
          {`${(local * 100).toFixed(1)}% das requisições servidas pelo pod local`}
        </title>
        {/* Track. */}
        <circle
          cx="60"
          cy="60"
          r={R}
          fill="none"
          stroke="var(--secondary)"
          strokeWidth={18}
        />
        {/* External slice starts at 12 o'clock… */}
        <circle
          cx="60"
          cy="60"
          r={R}
          fill="none"
          stroke="var(--chart-ext)"
          strokeWidth={18}
          strokeDasharray={`${externalLen} ${CIRCUMFERENCE}`}
          transform="rotate(-90 60 60)"
        />
        {/* …and the local slice picks up where it ends. */}
        <circle
          cx="60"
          cy="60"
          r={R}
          fill="none"
          stroke="var(--chart-llm)"
          strokeWidth={18}
          strokeDasharray={`${localLen} ${CIRCUMFERENCE}`}
          strokeDashoffset={-externalLen}
          transform="rotate(-90 60 60)"
        />
        <text
          x="60"
          y="58"
          textAnchor="middle"
          className="fill-foreground font-display text-[17px] font-bold"
        >
          {`${(local * 100).toLocaleString("pt-BR", {
            minimumFractionDigits: 1,
            maximumFractionDigits: 1,
          })}%`}
        </text>
        <text
          x="60"
          y="72"
          textAnchor="middle"
          className="fill-muted-foreground text-[8.5px]"
        >
          local
        </text>
      </svg>

      <dl className="flex flex-col gap-3 text-[12.5px]">
        <div>
          <dt className="flex items-center gap-2 text-muted-foreground">
            <span
              aria-hidden="true"
              className="size-2.5 rounded-[3px] bg-chart-llm"
            />
            Pod local (grátis)
          </dt>
          <dd className="mt-0.5 font-mono text-[15px] font-semibold tabular-nums">
            {formatBrl(summary.phantom_brl)}{" "}
            <span className="text-[12px] font-medium text-muted-foreground">
              economizados
            </span>
          </dd>
        </div>
        <div>
          <dt className="flex items-center gap-2 text-muted-foreground">
            <span
              aria-hidden="true"
              className="size-2.5 rounded-[3px] bg-chart-ext"
            />
            OpenRouter (externo)
          </dt>
          <dd className="mt-0.5 font-mono text-[15px] font-semibold tabular-nums">
            {formatBrl(summary.custo_openrouter_brl)}{" "}
            <span className="text-[12px] font-medium text-muted-foreground">
              pagos
            </span>
          </dd>
        </div>
      </dl>
    </div>
  );
}
