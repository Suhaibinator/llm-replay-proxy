"use client";
import * as S from "@radix-ui/react-select";
import { Check, ChevronDown, ChevronUp } from "lucide-react";
import { cn } from "@/lib/utils";
export const Select = S.Root;
export const SelectValue = S.Value;
export const SelectGroup = S.Group;
export function SelectTrigger({
  className,
  children,
  ...p
}: React.ComponentProps<typeof S.Trigger>) {
  return (
    <S.Trigger
      className={cn(
        "inline-flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-card px-3 text-sm whitespace-nowrap outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50 data-[placeholder]:text-muted-foreground [&>span]:truncate",
        className,
      )}
      {...p}
    >
      {children}
      <S.Icon asChild>
        <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
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
          "relative z-50 max-h-[min(var(--radix-select-content-available-height),20rem)] min-w-[var(--radix-select-trigger-width)] overflow-hidden rounded-md border bg-card text-card-foreground shadow-lg",
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
export function SelectLabel({
  className,
  ...p
}: React.ComponentProps<typeof S.Label>) {
  return (
    <S.Label
      className={cn("px-2 py-1.5 text-xs text-muted-foreground", className)}
      {...p}
    />
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
        "relative flex w-full cursor-default items-center rounded py-1.5 pl-2 pr-8 text-sm outline-none select-none data-[disabled]:pointer-events-none data-[disabled]:opacity-50 data-[highlighted]:bg-muted",
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
export function SelectSeparator({
  className,
  ...p
}: React.ComponentProps<typeof S.Separator>) {
  return (
    <S.Separator className={cn("my-1 h-px bg-border", className)} {...p} />
  );
}
