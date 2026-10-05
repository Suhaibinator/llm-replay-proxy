"use client";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export type TooltipRow = {
  key: string;
  label: string;
  value: string;
  color?: string;
  /** A group heading row (bold, no key). */
  heading?: boolean;
};

/**
 * Floating readout for the hovered/focused mark. Sits beside the pointer
 * column, on whichever side has room, so it never overflows the chart. Values
 * lead (strong), labels follow (muted); series are keyed by a short line.
 */
export function TooltipCard({
  x,
  containerWidth,
  top = 0,
  title,
  rows,
  total,
  children,
}: {
  x: number;
  containerWidth: number;
  top?: number;
  title: string;
  rows: TooltipRow[];
  total?: string;
  children?: ReactNode;
}) {
  const flip = x > containerWidth / 2;
  const narrow = containerWidth < 420;
  return (
    <div
      aria-hidden="true"
      className={cn(
        "pointer-events-none absolute z-20 w-max max-w-[min(17rem,calc(100%-1rem))] rounded-lg border bg-card/95 px-3 py-2 text-xs text-card-foreground shadow-lg backdrop-blur-sm",
      )}
      style={
        narrow
          ? { top: top - 4, left: "50%", transform: "translate(-50%, -100%)" }
          : flip
            ? { top, right: containerWidth - x + 14 }
            : { top, left: x + 14 }
      }
    >
      <p className="mb-1.5 font-medium text-foreground">{title}</p>
      <table className="w-full border-collapse">
        <tbody>
          {rows.map((r) => (
            <tr
              key={r.key}
              className={cn(r.heading && "[&:not(:first-child)>td]:pt-1.5")}
            >
              <td className="w-3 pr-2 align-middle">
                {r.color && (
                  <span
                    className="block h-0.5 w-3 rounded-full"
                    style={{ background: r.color }}
                  />
                )}
              </td>
              <td
                className={cn(
                  "pr-3 text-right font-semibold tabular-nums text-foreground",
                  r.heading && "font-bold",
                )}
              >
                {r.value}
              </td>
              <td
                className={cn(
                  "text-muted-foreground",
                  r.heading && "font-medium text-foreground",
                )}
              >
                {r.label}
              </td>
            </tr>
          ))}
          {total && (
            <tr>
              <td />
              <td className="border-t pt-1 pr-3 text-right font-bold tabular-nums text-foreground">
                {total}
              </td>
              <td className="border-t pt-1 text-muted-foreground">Total</td>
            </tr>
          )}
        </tbody>
      </table>
      {children && (
        <div className="mt-1.5 border-t pt-1.5 text-muted-foreground">
          {children}
        </div>
      )}
    </div>
  );
}
