"use client";
import * as S from "@radix-ui/react-select";
import { Check, ChevronDown, ChevronUp } from "lucide-react";
import { cn } from "@/lib/utils";
export const Select = S.Root;
export const SelectValue = S.Value;
export function SelectTrigger({
  className,
  children,
  ...p
}: React.ComponentProps<typeof S.Trigger>) {
  return (
    <S.Trigger
      className={cn(
        "inline-flex h-9 w-full items-center justify-between gap-2 rounded-lg border border-input bg-background px-3 text-sm whitespace-nowrap outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 data-[placeholder]:text-muted-foreground [&>span]:truncate",
        className,
      )}
      {...p}
    >
      {children}
      <S.Icon asChild>
        <ChevronDown className="size-4 shrink-0 opacity-60" />
      </S.Icon>
    </S.Trigger>
  );
}
export function SelectContent({
  className,
  children,
  position = "popper",
  ...p
}: React.ComponentProps<typeof S.Content>) {
  return (
    <S.Portal>
      <S.Content
        position={position}
        sideOffset={4}
        className={cn(
          "relative z-50 max-h-[min(var(--radix-select-content-available-height),20rem)] min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-lg border bg-card text-card-foreground shadow-lg",
          className,
        )}
        {...p}
      >
        <S.ScrollUpButton className="flex h-6 items-center justify-center text-muted-foreground">
          <ChevronUp className="size-4" />
        </S.ScrollUpButton>
        <S.Viewport className="p-1">{children}</S.Viewport>
        <S.ScrollDownButton className="flex h-6 items-center justify-center text-muted-foreground">
          <ChevronDown className="size-4" />
        </S.ScrollDownButton>
      </S.Content>
    </S.Portal>
  );
}
export function SelectItem({
  className,
  children,
  ...p
}: React.ComponentProps<typeof S.Item>) {
  return (
    <S.Item
      className={cn(
        "relative flex w-full cursor-default items-center rounded-md py-1.5 pl-2 pr-8 text-sm outline-none select-none data-[disabled]:pointer-events-none data-[disabled]:opacity-50 data-[highlighted]:bg-muted",
        className,
      )}
      {...p}
    >
      <S.ItemText>{children}</S.ItemText>
      <S.ItemIndicator className="absolute right-2 inline-flex items-center">
        <Check className="size-4" />
      </S.ItemIndicator>
    </S.Item>
  );
}
