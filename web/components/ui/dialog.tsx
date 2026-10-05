"use client";
import * as D from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";
export const Dialog = D.Root;
export const DialogTrigger = D.Trigger;
export const DialogClose = D.Close;
export function DialogContent({
  className,
  children,
  ...p
}: React.ComponentProps<typeof D.Content>) {
  return (
    <D.Portal>
      <D.Overlay className="fixed inset-0 z-50 bg-black/55 backdrop-blur-[2px]" />
      <D.Content
        className={cn(
          "fixed left-1/2 top-1/2 z-50 max-h-[92dvh] w-[calc(100%-1.5rem)] max-w-3xl -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-xl border bg-card p-5 text-card-foreground shadow-2xl sm:p-6",
          className,
        )}
        {...p}
      >
        {children}
        <D.Close
          className="absolute right-3 top-3 rounded-md p-1.5 text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
          aria-label="Close"
        >
          <X className="size-4" />
        </D.Close>
      </D.Content>
    </D.Portal>
  );
}
export function DialogHeader({
  className,
  ...p
}: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("mb-4 space-y-1 pr-8", className)} {...p} />;
}
export function DialogTitle({
  className,
  ...p
}: React.ComponentProps<typeof D.Title>) {
  return (
    <D.Title
      className={cn("text-base font-semibold tracking-tight", className)}
      {...p}
    />
  );
}
export function DialogDescription({
  className,
  ...p
}: React.ComponentProps<typeof D.Description>) {
  return (
    <D.Description
      className={cn("text-[13px] text-muted-foreground", className)}
      {...p}
    />
  );
}
export function DialogFooter({
  className,
  ...p
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end",
        className,
      )}
      {...p}
    />
  );
}
