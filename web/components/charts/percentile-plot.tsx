"use client";
import { formatMs } from "@/lib/insights";
import { useWidth } from "./use-width";

export type PercentileRow = {
  key: string;
  label: string;
  color: string;
  p50: number | null;
  p95: number | null;
  p99: number | null;
};

const LOG_TICKS = [1, 10, 100, 1000, 10_000, 60_000, 600_000];

/**
 * Latency percentiles on a shared log axis: a dot at p50, a ring at p95 and a
 * tick at p99, joined by a hairline so the spread reads at a glance. Values
 * are printed on every row (no hover needed); the log scale lets millisecond
 * replays and multi-second upstream calls share one axis honestly.
 */
export function PercentilePlot({
  rows,
  label,
}: {
  rows: PercentileRow[];
  label: string;
}) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const vals = rows
    .flatMap((r) => [r.p50, r.p95, r.p99])
    .filter((v): v is number => v != null && v > 0);
  const lo = vals.length
    ? 10 ** Math.floor(Math.log10(Math.max(1, Math.min(...vals))))
    : 1;
  const hiRaw = vals.length ? Math.max(...vals) : 1000;
  const hi =
    LOG_TICKS.find((t) => t >= hiRaw && t > lo) ??
    10 ** Math.ceil(Math.log10(hiRaw));
  const L = 6,
    R = 10,
    axisH = 22;
  // Narrow: values move under each line so they never collide with its label.
  const narrow = width > 0 && width < 440;
  const rowH = narrow ? 70 : 50;
  const plotW = Math.max(0, width - L - R);
  const x = (v: number) =>
    L +
    ((Math.log10(Math.max(v, lo)) - Math.log10(lo)) /
      (Math.log10(hi) - Math.log10(lo) || 1)) *
      plotW;
  const ticks = LOG_TICKS.filter((t) => t >= lo && t <= hi);
  const height = rows.length * rowH + axisH;
  const summary = rows
    .map(
      (r) =>
        `${r.label}: p50 ${formatMs(r.p50)}, p95 ${formatMs(r.p95)}, p99 ${formatMs(r.p99)}`,
    )
    .join("; ");
  return (
    <div
      ref={ref}
      role="img"
      aria-label={`${label}. ${summary}`}
      style={{ height }}
    >
      {width > 0 && (
        <svg
          width={width}
          height={height}
          aria-hidden="true"
          className="block overflow-visible"
        >
          {ticks.map((t) => (
            <g key={t}>
              <line
                x1={x(t) + 0.5}
                x2={x(t) + 0.5}
                y1={4}
                y2={height - axisH}
                className="stroke-border"
              />
              <text
                x={x(t)}
                y={height - 6}
                textAnchor={t === ticks[0] ? "start" : "middle"}
                className="fill-muted-foreground text-[10.5px] tabular-nums"
              >
                {formatMs(t).replace(" ", "")}
              </text>
            </g>
          ))}
          {rows.map((r, i) => {
            const cy = i * rowH + 34;
            const has = r.p50 != null;
            return (
              <g key={r.key}>
                <text
                  x={L}
                  y={cy - 16}
                  className="fill-foreground text-[11.5px] font-medium"
                >
                  {r.label}
                </text>
                <text
                  x={narrow ? L : width - R}
                  y={narrow ? cy + 20 : cy - 16}
                  textAnchor={narrow ? "start" : "end"}
                  className="fill-muted-foreground text-[11px] tabular-nums"
                >
                  {has
                    ? `p50 ${formatMs(r.p50)} · p95 ${formatMs(r.p95)}`
                    : "No samples"}
                </text>
                {has && (
                  <>
                    <line
                      x1={x(r.p50!)}
                      x2={x(r.p99 ?? r.p95 ?? r.p50!)}
                      y1={cy}
                      y2={cy}
                      stroke={r.color}
                      strokeOpacity={0.45}
                      strokeWidth={2}
                      strokeLinecap="round"
                    />
                    {r.p99 != null && (
                      <line
                        x1={x(r.p99)}
                        x2={x(r.p99)}
                        y1={cy - 5}
                        y2={cy + 5}
                        stroke={r.color}
                        strokeWidth={2}
                        strokeLinecap="round"
                      />
                    )}
                    {r.p95 != null && (
                      <circle
                        cx={x(r.p95)}
                        cy={cy}
                        r={4.5}
                        className="fill-card"
                        stroke={r.color}
                        strokeWidth={2}
                      />
                    )}
                    <circle
                      cx={x(r.p50!)}
                      cy={cy}
                      r={5.5}
                      fill={r.color}
                      className="stroke-card"
                      strokeWidth={2}
                    />
                  </>
                )}
              </g>
            );
          })}
        </svg>
      )}
    </div>
  );
}
