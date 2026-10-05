/**
 * Decorative trend line for a stat tile (the tile states the value; the
 * charts below carry the per-bucket numbers). Null points break the line.
 */
export function Sparkline({
  values,
  color = "var(--color-muted-foreground)",
  height = 28,
  max,
}: {
  values: (number | null)[];
  color?: string;
  height?: number;
  /** Fixed top of the scale (e.g. 1 for rates); defaults to the data max. */
  max?: number;
}) {
  const n = values.length;
  if (n < 2 || values.every((v) => v == null || v === 0))
    return (
      <div aria-hidden="true" style={{ height }} className="flex items-end">
        <div className="h-px w-full bg-border" />
      </div>
    );
  const top = max ?? Math.max(...values.map((v) => v ?? 0), 1e-9);
  const W = 100,
    pad = 2;
  const x = (i: number) => (i / (n - 1)) * W;
  const y = (v: number) => pad + (1 - v / top) * (height - pad * 2);
  const segments: string[] = [];
  let cur = "";
  values.forEach((v, i) => {
    if (v == null) {
      if (cur) segments.push(cur);
      cur = "";
      return;
    }
    cur += `${cur ? "L" : "M"}${x(i).toFixed(2)},${y(v).toFixed(2)}`;
  });
  if (cur) segments.push(cur);
  const area = segments
    .map((d) => {
      const pts = d
        .slice(1)
        .split("L")
        .map((p) => p.split(",").map(Number));
      return `${d}L${pts[pts.length - 1][0]},${height}L${pts[0][0]},${height}Z`;
    })
    .join("");
  let last = n - 1;
  while (last > 0 && values[last] == null) last--;
  return (
    <svg
      aria-hidden="true"
      viewBox={`0 0 ${W} ${height}`}
      preserveAspectRatio="none"
      className="block w-full overflow-visible"
      style={{ height }}
    >
      <path d={area} fill={color} fillOpacity={0.1} />
      {segments.map((d, i) => (
        <path
          key={i}
          d={d}
          fill="none"
          stroke={color}
          strokeWidth={1.5}
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
      ))}
      {values[last] != null && (
        <line
          x1={x(last)}
          x2={x(last)}
          y1={y(values[last] as number)}
          y2={y(values[last] as number)}
          stroke={color}
          strokeWidth={5}
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
      )}
    </svg>
  );
}
