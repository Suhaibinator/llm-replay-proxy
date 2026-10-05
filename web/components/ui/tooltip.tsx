"use client";
import * as T from "@radix-ui/react-tooltip";
import { cn } from "@/lib/utils";
export const TooltipProvider = T.Provider;
export const Tooltip = T.Root;
export const TooltipTrigger = T.Trigger;
export function TooltipContent({
  className,
  sideOffset = 4,
  ...p
}: React.ComponentProps<typeof T.Content>) {
  return (
    <T.Portal>
      <T.Content
        sideOffset={sideOffset}
        className={cn(
          "z-50 max-w-xs rounded-md border border-border bg-card px-2.5 py-1.5 text-xs text-card-foreground shadow-lg",
          className,
        )}
        {...p}
      />
    </T.Portal>
  );
}
