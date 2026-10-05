import * as React from "react";
import { cn } from "@/lib/utils";

/**
 * Outcome and chart tones map straight onto the color tokens so a badge, a
 * chart legend swatch and an inline label for the same thing share one hue.
 */
export type BadgeTone =
  | "neutral"
  | "hit"
  | "miss"
  | "recorded"
  | "interrupted"
  | "error"
  | "warn"
  | "primary";
const tones: Record<BadgeTone, string> = {
  neutral: "border-border bg-muted text-foreground",
  hit: "border-hit/30 bg-hit/10 text-hit",
  miss: "border-miss/30 bg-miss/10 text-miss",
  recorded: "border-recorded/30 bg-recorded/10 text-recorded",
  interrupted: "border-interrupted/30 bg-interrupted/10 text-interrupted",
  error: "border-error/30 bg-error/10 text-error",
  warn: "border-warn/30 bg-warn/10 text-warn",
  primary: "border-primary/30 bg-primary/10 text-primary",
};
export function Badge({
  className,
  tone = "neutral",
  ...p
}: React.HTMLAttributes<HTMLSpanElement> & { tone?: BadgeTone }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-1 whitespace-nowrap rounded border px-1.5 text-[11px] font-medium leading-none",
        tones[tone],
        className,
      )}
      {...p}
    />
  );
}
