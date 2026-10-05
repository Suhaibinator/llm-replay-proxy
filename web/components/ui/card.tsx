import * as React from "react";
import { cn } from "@/lib/utils";
/**
 * A bordered surface. Cards carry no shadow: in a data-dense console the
 * border alone separates panels, and shadows on everything flatten hierarchy.
 */
export function Card({
  className,
  ...p
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "rounded-lg border border-border bg-card text-card-foreground",
        className,
      )}
      {...p}
    />
  );
}
export function CardHeader({
  className,
  ...p
}: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex flex-col gap-1 p-4", className)} {...p} />;
}
export function CardTitle({
  className,
  ...p
}: React.HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h3
      className={cn("text-[15px] font-semibold tracking-tight", className)}
      {...p}
    />
  );
}
export function CardDescription({
  className,
  ...p
}: React.HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p className={cn("text-[13px] text-muted-foreground", className)} {...p} />
  );
}
export function CardContent({
  className,
  ...p
}: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("p-4 pt-0", className)} {...p} />;
}
export function CardFooter({
  className,
  ...p
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex items-center gap-2 border-t p-4", className)}
      {...p}
    />
  );
}
