"use client";
import type { ReactNode } from "react";
import { type LucideIcon, RefreshCw, X, XCircle } from "lucide-react";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** Maps a history outcome (or lookup outcome) onto its color token. */
export function outcomeTone(value: string): BadgeTone {
  if (/^hit|success/i.test(value)) return "hit";
  if (/miss/i.test(value)) return "miss";
  if (/recorded/i.test(value)) return "recorded";
  if (/interrupt/i.test(value)) return "interrupted";
  if (!value) return "neutral";
  return "error";
}
export function Outcome({
  value,
  className,
}: {
  value: string;
  className?: string;
}) {
  return (
    <Badge tone={outcomeTone(value)} className={className}>
      {value || "unknown"}
    </Badge>
  );
}

/**
 * Heading row for a view: the title at left, filters or actions at right.
 * Views title themselves with this so every page opens the same way.
 */
export function ViewHeader({
  title,
  description,
  actions,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between",
        className,
      )}
    >
      <div className="min-w-0">
        <h2 className="text-xl font-semibold tracking-tight">{title}</h2>
        {description && (
          <p className="mt-0.5 max-w-prose text-[13px] text-muted-foreground">
            {description}
          </p>
        )}
      </div>
      {actions && (
        <div className="flex flex-wrap items-center gap-2">{actions}</div>
      )}
    </div>
  );
}

export function Empty({
  icon: Icon,
  title,
  body,
  action,
  className,
}: {
  icon: LucideIcon;
  title: string;
  body: string;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex min-h-56 flex-col items-center justify-center px-5 py-8 text-center",
        className,
      )}
    >
      <div className="mb-3 rounded-lg border border-dashed p-3">
        <Icon className="size-5 text-muted-foreground" aria-hidden />
      </div>
      <p className="font-medium">{title}</p>
      <p className="mt-1 max-w-sm text-[13px] text-muted-foreground">{body}</p>
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function ErrorBanner({
  message,
  onDismiss,
  className,
}: {
  message: string;
  onDismiss?: () => void;
  className?: string;
}) {
  if (!message) return null;
  return (
    <div
      role="alert"
      className={cn(
        "flex items-start gap-2.5 rounded-md border border-error/40 bg-error/10 px-3 py-2.5 text-[13px] text-foreground",
        className,
      )}
    >
      <XCircle className="mt-0.5 size-4 shrink-0 text-error" aria-hidden />
      <span className="min-w-0 flex-1 break-words">{message}</span>
      {onDismiss && (
        <Button
          variant="ghost"
          size="xs"
          className="-my-1 -mr-1 size-6 p-0 text-muted-foreground"
          onClick={onDismiss}
          aria-label="Dismiss error"
        >
          <X className="size-3.5" />
        </Button>
      )}
    </div>
  );
}

export function Spinner({
  label,
  className,
}: {
  label: string;
  className?: string;
}) {
  return (
    <div
      role="status"
      className={cn("flex h-32 items-center justify-center", className)}
    >
      <RefreshCw
        className="size-5 animate-spin text-muted-foreground"
        aria-hidden
      />
      <span className="sr-only">{label}</span>
    </div>
  );
}

/** Reads a request or JSON payload in the dark code panel. */
export function CodeBlock({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <pre
      className={cn(
        "max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-md bg-code p-3 font-mono text-[11.5px] leading-5 text-code-foreground",
        className,
      )}
    >
      {children}
    </pre>
  );
}
