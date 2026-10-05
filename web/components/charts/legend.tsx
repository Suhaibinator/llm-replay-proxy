import { cn } from "@/lib/utils";

export type LegendItem = {
  key: string;
  label: string;
  color: string;
  /** Optional total shown after the label. */
  value?: string;
  hint?: string;
  /** Line key for line series; rect (the default) for bars and areas. */
  shape?: "rect" | "line" | "ring";
};

/** Legend keys mirror the mark: rect for bars/areas, line for lines. Text stays in ink. */
export function Legend({
  items,
  className,
}: {
  items: LegendItem[];
  className?: string;
}) {
  return (
    <ul className={cn("flex flex-wrap gap-x-4 gap-y-1.5 text-xs", className)}>
      {items.map((i) => (
        <li key={i.key} className="flex items-center gap-1.5" title={i.hint}>
          <Swatch color={i.color} shape={i.shape} />
          <span className="text-muted-foreground">{i.label}</span>
          {i.value != null && (
            <span className="font-semibold tabular-nums text-foreground">
              {i.value}
            </span>
          )}
        </li>
      ))}
    </ul>
  );
}

export function Swatch({
  color,
  shape = "rect",
  className,
}: {
  color: string;
  shape?: LegendItem["shape"];
  className?: string;
}) {
  if (shape === "line")
    return (
      <span
        aria-hidden="true"
        className={cn("h-0.5 w-3 shrink-0 rounded-full", className)}
        style={{ background: color }}
      />
    );
  if (shape === "ring")
    return (
      <span
        aria-hidden="true"
        className={cn(
          "size-2.5 shrink-0 rounded-full border-2 bg-card",
          className,
        )}
        style={{ borderColor: color }}
      />
    );
  return (
    <span
      aria-hidden="true"
      className={cn("size-2.5 shrink-0 rounded-[3px]", className)}
      style={{ background: color }}
    />
  );
}
