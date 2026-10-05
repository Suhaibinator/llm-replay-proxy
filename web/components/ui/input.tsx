import * as React from "react";
import { cn } from "@/lib/utils";
const field =
  "w-full rounded-md border border-input bg-card text-sm text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40 disabled:opacity-50 aria-[invalid=true]:border-destructive";
export function Input({
  className,
  ...p
}: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input className={cn(field, "h-9 px-3", className)} {...p} />;
}
export function Textarea({
  className,
  ...p
}: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cn(field, "min-h-28 px-3 py-2 leading-relaxed", className)}
      {...p}
    />
  );
}
/** A label above its control; `hint` is the short explanation under it. */
export function Field({
  label,
  hint,
  className,
  children,
  htmlFor,
}: {
  label: React.ReactNode;
  hint?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
  htmlFor?: string;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <label htmlFor={htmlFor} className="block text-[13px] font-medium">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}
