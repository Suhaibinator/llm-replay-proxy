"use client";
import { useId, useState, type KeyboardEvent, type ReactNode } from "react";
import { labelStride, niceScale } from "@/lib/insights";
import { cn } from "@/lib/utils";
import { TooltipCard, type TooltipRow } from "./tooltip-card";
import { useWidth } from "./use-width";

export type Series = { key: string; label: string; color: string };

type Props<Row> = {
  rows: Row[];
  /** Series stacked (or grouped) above the baseline, bottom first. */
  series: Series[];
  value: (row: Row, key: string) => number;
  /** Optional mirrored stack drawn below the baseline on the same scale. */
  below?: {
    label: string;
    series: Series[];
    value: (row: Row, key: string) => number;
  };
  /** Name of the upper group when `below` is set (e.g. "Upstream"). */
  aboveLabel?: string;
  mode?: "stack" | "group";
  /** X axis label for a row. */
  tick: (row: Row, index: number) => string;
  /** Tooltip heading for a row. */
  title: (row: Row) => string;
  /** Axis tick formatter. */
  format: (n: number) => string;
  /** Tooltip value formatter; defaults to `format`. */
  formatValue?: (n: number) => string;
  /** Extra tooltip lines under the series rows. */
  footer?: (row: Row) => ReactNode;
  /** Accessible name of the chart. */
  label: string;
  /** What one column is, for the keyboard hint ("day", "bucket"). */
  unit?: string;
  height?: number;
  /** Values on the y axis are whole numbers (counts). */
  integer?: boolean;
  /** Wide categorical labels (histograms) want more room per label. */
  tickWidth?: number;
  className?: string;
};

const TOP = 10,
  BOTTOM = 24;

/** A rect whose data end (top, or bottom when `down`) has rounded corners. */
function barPath(
  x: number,
  y: number,
  w: number,
  h: number,
  r: number,
  down = false,
) {
  if (h <= 0 || w <= 0) return "";
  const rr = Math.max(0, Math.min(r, w / 2, h));
  if (!down)
    return `M${x},${y + h}V${y + rr}Q${x},${y} ${x + rr},${y}H${x + w - rr}Q${x + w},${y} ${x + w},${y + rr}V${y + h}Z`;
  return `M${x},${y}V${y + h - rr}Q${x},${y + h} ${x + rr},${y + h}H${x + w - rr}Q${x + w},${y + h} ${x + w},${y + h - rr}V${y}Z`;
}

/**
 * Column chart drawn in SVG at the container's real width: stacked or
 * grouped columns, optionally mirrored below the baseline, with hairline
 * gridlines, clean y ticks, thinned x labels, a hover/focus tooltip and arrow
 * key navigation. One tab stop; the data table lives next to it (see Panel).
 */
export function ColumnChart<Row>({
  rows,
  series,
  value,
  below,
  aboveLabel,
  mode = "stack",
  tick,
  title,
  format,
  formatValue = format,
  footer,
  label,
  unit = "column",
  height = 220,
  integer = true,
  tickWidth = 56,
  className,
}: Props<Row>) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [active, setActive] = useState<number | null>(null);
  const [focused, setFocused] = useState(false);
  const liveId = useId();

  const n = rows.length;
  const sum = (row: Row, ss: Series[], v: (r: Row, k: string) => number) =>
    mode === "stack"
      ? ss.reduce((a, s) => a + Math.max(0, v(row, s.key)), 0)
      : Math.max(0, ...ss.map((s) => v(row, s.key)));
  const maxAbove = Math.max(0, ...rows.map((r) => sum(r, series, value)));
  const maxBelow = below
    ? Math.max(0, ...rows.map((r) => sum(r, below.series, below.value)))
    : 0;

  // One scale for both directions: the step comes from the larger side.
  const scale = niceScale(Math.max(maxAbove, maxBelow), below ? 3 : 4, integer);
  const step = scale.ticks[1] ?? scale.max;
  const topMax = below
    ? Math.max(step, Math.ceil(maxAbove / step - 1e-9) * step)
    : scale.max;
  const botMax = below
    ? Math.max(step, Math.ceil(maxBelow / step - 1e-9) * step)
    : 0;
  const ticks: number[] = [];
  for (let v = -botMax; v <= topMax + step / 2; v += step)
    ticks.push(Number(v.toPrecision(12)));

  // Mirrored charts name their halves in a rotated gutter on the right.
  const RIGHT = below ? 22 : 6;
  const left = Math.max(
    28,
    ...ticks.map((t) => format(Math.abs(t)).length * 6.4 + 10),
  );
  const plotW = Math.max(0, width - left - RIGHT);
  const plotH = height - TOP - BOTTOM;
  const span = topMax + botMax || 1;
  const y = (v: number) => TOP + ((topMax - v) / span) * plotH;
  const baseY = y(0);

  const band = n ? plotW / n : 0;
  const gap = band >= 10 ? 2 : band >= 5 ? 1 : 0;
  const stride = labelStride(n, plotW, tickWidth);
  const groupCount = mode === "group" ? series.length : 1;
  const colW = Math.max(
    1,
    Math.min(
      mode === "group" ? 22 * groupCount + gap : 24,
      band - Math.max(gap, band * 0.18),
    ),
  );
  const barW =
    mode === "group"
      ? Math.max(1, (colW - gap * (groupCount - 1)) / groupCount)
      : colW;
  const radius = barW >= 8 ? 4 : barW >= 4 ? 2 : 0;
  const segGap = barW >= 6 ? 2 : 1;

  const columns = rows.map((row, i) => {
    const x0 = left + band * i + (band - colW) / 2;
    const shapes: { d: string; color: string; key: string }[] = [];
    const stackUp = (
      ss: Series[],
      v: (r: Row, k: string) => number,
      down: boolean,
    ) => {
      if (mode === "group") {
        ss.forEach((s, j) => {
          const val = Math.max(0, v(row, s.key));
          const h = Math.abs(y(val) - baseY);
          const x = x0 + j * (barW + gap);
          const d = down
            ? barPath(x, baseY, barW, h, radius, true)
            : barPath(x, baseY - h, barW, h, radius);
          if (d)
            shapes.push({
              d,
              color: s.color,
              key: `${down ? "b" : "a"}${s.key}`,
            });
        });
        return;
      }
      const vals = ss.map((s) => Math.max(0, v(row, s.key)));
      const topIndex = vals.reduce((last, val, j) => (val > 0 ? j : last), -1);
      let acc = 0;
      ss.forEach((s, j) => {
        const val = vals[j];
        if (val <= 0) return;
        const from = Math.abs(y(acc) - baseY),
          to = Math.abs(y(acc + val) - baseY);
        acc += val;
        // A surface gap separates a segment from the one beneath it.
        const start =
          from > 0 && to - from > segGap + 0.5 ? from + segGap : from;
        const h = Math.max(to - start, 0.75);
        const r = j === topIndex ? radius : 0;
        const d = down
          ? barPath(x0, baseY + start, colW, h, r, true)
          : barPath(x0, baseY - start - h, colW, h, r);
        if (d)
          shapes.push({
            d,
            color: s.color,
            key: `${down ? "b" : "a"}${s.key}`,
          });
      });
    };
    stackUp(series, value, false);
    if (below) stackUp(below.series, below.value, true);
    return { x0, shapes };
  });

  const describe = (row: Row) => {
    const part = (ss: Series[], v: (r: Row, k: string) => number) =>
      ss.map((s) => `${s.label} ${formatValue(v(row, s.key))}`).join(", ");
    return `${title(row)}: ${below ? `${aboveLabel ?? "Above"}: ` : ""}${part(series, value)}${below ? `. ${below.label}: ${part(below.series, below.value)}` : ""}`;
  };

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!n) return;
    const cur = active ?? n - 1;
    const next =
      e.key === "ArrowLeft"
        ? Math.max(0, cur - 1)
        : e.key === "ArrowRight"
          ? Math.min(n - 1, cur + 1)
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? n - 1
              : e.key === "Escape"
                ? null
                : undefined;
    if (next === undefined) return;
    e.preventDefault();
    setActive(next);
  };

  const tooltipRows = (row: Row): TooltipRow[] => {
    const out: TooltipRow[] = [];
    const group = (
      ss: Series[],
      v: (r: Row, k: string) => number,
      heading?: string,
    ) => {
      const items = [...ss].reverse().map((s) => ({
        key: s.key,
        label: s.label,
        color: s.color,
        value: formatValue(v(row, s.key)),
      }));
      if (heading) {
        const total = ss.reduce((a, s) => a + Math.max(0, v(row, s.key)), 0);
        out.push({
          key: `h-${heading}`,
          label: heading,
          value: formatValue(total),
          heading: true,
        });
      }
      out.push(...items);
    };
    if (below) {
      group(series, value, aboveLabel ?? "Above");
      group(below.series, below.value, below.label);
    } else group(series, value);
    return out;
  };

  const activeRow = active != null && active < n ? rows[active] : null;
  const activeX = active != null ? left + band * active + band / 2 : 0;
  const total =
    activeRow && mode === "stack" && !below
      ? sum(activeRow, series, value)
      : null;

  return (
    <div className={cn("relative", className)}>
      <div
        ref={ref}
        role="group"
        aria-roledescription="chart"
        aria-label={`${label}. ${n ? `Use the left and right arrow keys to read each ${unit}.` : "No data."}`}
        aria-describedby={liveId}
        tabIndex={n ? 0 : -1}
        onKeyDown={onKey}
        onFocus={() => {
          setFocused(true);
          setActive((a) => a ?? (n ? n - 1 : null));
        }}
        onBlur={() => {
          setFocused(false);
          setActive(null);
        }}
        onPointerLeave={() => !focused && setActive(null)}
        className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card"
        style={{ height }}
      >
        {width > 0 && (
          <svg
            width={width}
            height={height}
            aria-hidden="true"
            className="block select-none overflow-visible"
          >
            {ticks.map((t) => (
              <g key={t}>
                <line
                  x1={left}
                  x2={width - RIGHT}
                  y1={Math.round(y(t)) + 0.5}
                  y2={Math.round(y(t)) + 0.5}
                  className={
                    t === 0 ? "stroke-muted-foreground/45" : "stroke-border"
                  }
                  strokeWidth={1}
                />
                <text
                  x={left - 8}
                  y={y(t)}
                  dy="0.32em"
                  textAnchor="end"
                  className="fill-muted-foreground text-[10.5px] tabular-nums"
                >
                  {format(Math.abs(t))}
                </text>
              </g>
            ))}
            {below && (
              <>
                {(
                  [
                    [aboveLabel ?? "", (TOP + baseY) / 2],
                    [below.label, (baseY + TOP + plotH) / 2],
                  ] as const
                ).map(([text, cy]) => (
                  <text
                    key={text}
                    transform={`translate(${width - 6} ${cy}) rotate(-90)`}
                    textAnchor="middle"
                    className="fill-muted-foreground text-[10px] font-medium uppercase tracking-wide"
                  >
                    {text}
                  </text>
                ))}
              </>
            )}
            {active != null && active < n && (
              <rect
                x={left + band * active}
                y={TOP}
                width={band}
                height={plotH}
                rx={Math.min(4, band / 4)}
                className="fill-foreground/[0.06]"
              />
            )}
            {columns.map((c, i) => (
              <g
                key={i}
                opacity={active == null || active === i ? 1 : 0.55}
                style={{ transition: "opacity 120ms" }}
              >
                {c.shapes.map((s) => (
                  <path key={s.key} d={s.d} fill={s.color} />
                ))}
              </g>
            ))}
            {rows.map((row, i) =>
              i % stride === (n - 1) % stride ? (
                <text
                  key={i}
                  x={left + band * i + band / 2}
                  y={height - 7}
                  textAnchor={
                    stride > 1 && i === n - 1 && band < 30 ? "end" : "middle"
                  }
                  className="fill-muted-foreground text-[10.5px] tabular-nums"
                >
                  {tick(row, i)}
                </text>
              ) : null,
            )}
            {/* Hit targets: the whole column band, bigger than the mark. */}
            {rows.map((_, i) => (
              <rect
                key={i}
                x={left + band * i}
                y={TOP}
                width={band}
                height={plotH}
                fill="transparent"
                onPointerEnter={() => setActive(i)}
                onPointerMove={() => active !== i && setActive(i)}
              />
            ))}
          </svg>
        )}
      </div>
      {activeRow && (
        <TooltipCard
          x={activeX}
          containerWidth={width}
          top={TOP}
          title={title(activeRow)}
          rows={tooltipRows(activeRow)}
          total={total != null ? formatValue(total) : undefined}
        >
          {footer?.(activeRow)}
        </TooltipCard>
      )}
      <p id={liveId} className="sr-only" aria-live="polite">
        {focused && activeRow ? describe(activeRow) : ""}
      </p>
    </div>
  );
}
