import { cn } from "@/lib/utils";

export type SplitPart = {
  key: string;
  label: string;
  color: string;
  value: number;
};

/**
 * A horizontal part-to-whole bar: segments separated by a 2px surface gap,
 * rounded only at the two outer data ends. `label` is its accessible text.
 */
export function SplitBar({
  parts,
  label,
  className,
  height = 8,
  /** Draw the bar at this share of the track (for bars compared across rows). */
  fill = 1,
}: {
  parts: SplitPart[];
  label: string;
  className?: string;
  height?: number;
  fill?: number;
}) {
  const total = parts.reduce((a, p) => a + Math.max(0, p.value), 0);
  const visible = parts.filter((p) => p.value > 0);
  return (
    <div
      role="img"
      aria-label={label}
      title={label}
      className={cn(
        "flex w-full overflow-hidden rounded-[4px] bg-muted",
        className,
      )}
      style={{ height }}
    >
      {total > 0 && (
        <div
          className="flex h-full gap-[2px]"
          style={{ width: `${Math.max(0, Math.min(1, fill)) * 100}%` }}
        >
          {visible.map((p, i) => (
            <span
              key={p.key}
              className={cn(
                "block h-full min-w-[2px]",
                i === 0 && "rounded-l-[4px]",
                i === visible.length - 1 && "rounded-r-[4px]",
              )}
              style={{ flexGrow: p.value, flexBasis: 0, background: p.color }}
            />
          ))}
        </div>
      )}
    </div>
  );
}
