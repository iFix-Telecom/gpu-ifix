/**
 * Sparkline — a bare SVG trend line for the KPI cards (mockup `.spark`).
 *
 * Deliberately NOT a Recharts chart: this is a 26px-tall ornament rendered
 * four-up in a KPI row, and a full chart runtime per card is wasteful. It has
 * no axes, no tooltip and no legend — the KPI's number is the information; the
 * line only says "and the recent shape looked like this".
 *
 * HONESTY RULE: with fewer than two points there is no trend to draw, so the
 * component renders NOTHING rather than a flat line at zero — a flat line
 * would read as "steady", which is a different claim from "no data".
 */

export interface SparklineProps {
  /** The series, oldest → newest. Fewer than 2 points renders nothing. */
  points: number[];
  /** Stroke color — any CSS color or var(). Defaults to the brand accent. */
  color?: string;
  className?: string;
}

/** viewBox geometry — stretched to the card width via preserveAspectRatio. */
const VIEW_W = 120;
const VIEW_H = 26;
/** Half the end-dot radius, so the marker is never clipped by the viewBox. */
const PAD_Y = 2;

export function Sparkline({
  points,
  color = "var(--primary)",
  className,
}: SparklineProps) {
  if (points.length < 2) return null;

  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min;

  // A perfectly flat series (span === 0) has no vertical information — draw it
  // on the mid-line instead of dividing by zero.
  const y = (v: number) =>
    span === 0
      ? VIEW_H / 2
      : PAD_Y + (VIEW_H - 2 * PAD_Y) * (1 - (v - min) / span);
  const x = (i: number) => (VIEW_W * i) / (points.length - 1);

  const coords = points.map((v, i) => `${x(i).toFixed(2)},${y(v).toFixed(2)}`);
  const lastX = x(points.length - 1);
  const lastY = y(points[points.length - 1]);

  return (
    <svg
      viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
      preserveAspectRatio="none"
      className={className ?? "mt-2 block h-[26px] w-full"}
      // Ornament: the accessible value is the KPI number next to it.
      aria-hidden="true"
      focusable="false"
    >
      <polyline
        points={coords.join(" ")}
        fill="none"
        stroke={color}
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
      <circle cx={lastX} cy={lastY} r={2} fill={color} />
    </svg>
  );
}
