import * as React from "react";
import { cn } from "@/lib/utils";
export function Badge({
  className,
  ...p
}: React.HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border border-border px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide",
        className,
      )}
      {...p}
    />
  );
}
