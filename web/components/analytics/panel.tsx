"use client";
import { useId, useState, type ReactNode } from "react";
import { ChartColumn, Table2 } from "lucide-react";
import { cn } from "@/lib/utils";

/**
 * A titled analytics section. When `table` is given, a Chart/Table switch
 * swaps the visual for its data table (the accessible twin of every chart).
 */
export function Panel({
  title,
  description,
  actions,
  legend,
  table,
  children,
  className,
  bodyClassName,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  legend?: ReactNode;
  table?: ReactNode;
  children: ReactNode;
  className?: string;
  bodyClassName?: string;
}) {
  const id = useId();
  const [showTable, setShowTable] = useState(false);
  return (
    <section
      aria-labelledby={id}
      className={cn(
        "min-w-0 rounded-xl border bg-card text-card-foreground shadow-sm",
        className,
      )}
    >
      <header className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 px-4 pt-4 sm:px-5">
        <div className="min-w-0">
          <h3 id={id} className="text-sm font-semibold tracking-tight">
            {title}
          </h3>
          {description && (
            <p className="mt-0.5 text-xs text-muted-foreground">
              {description}
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {actions}
          {table && (
            <div
              role="group"
              aria-label={`${title} view`}
              className="inline-flex rounded-md border p-0.5"
            >
              {(
                [
                  [false, "Chart", ChartColumn],
                  [true, "Table", Table2],
                ] as const
              ).map(([value, text, Icon]) => (
                <button
                  key={text}
                  type="button"
                  aria-pressed={showTable === value}
                  onClick={() => setShowTable(value)}
                  className={cn(
                    "inline-flex h-6 items-center gap-1 rounded-[5px] px-2 text-[11px] font-medium text-muted-foreground outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring",
                    showTable === value
                      ? "bg-secondary text-secondary-foreground"
                      : "hover:text-foreground",
                  )}
                >
                  <Icon className="size-3" aria-hidden="true" />
                  {text}
                </button>
              ))}
            </div>
          )}
        </div>
      </header>
      {legend && !showTable && (
        <div className="px-4 pt-3 sm:px-5">{legend}</div>
      )}
      <div className={cn("px-4 pt-3 pb-4 sm:px-5 sm:pb-5", bodyClassName)}>
        {showTable && table ? table : children}
      </div>
    </section>
  );
}

/** Small segmented control used inside panels and the filter bar. */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
  size = "sm",
}: {
  label: string;
  value: T;
  options: readonly { id: T; label: string; title?: string }[];
  onChange: (v: T) => void;
  size?: "sm" | "md";
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="inline-flex rounded-lg border bg-card p-0.5"
    >
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          title={o.title}
          aria-pressed={value === o.id}
          onClick={() => onChange(o.id)}
          className={cn(
            "rounded-md font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring",
            size === "md" ? "h-8 px-3 text-sm" : "h-6 px-2 text-[11px]",
            value === o.id
              ? "bg-primary text-primary-foreground shadow-sm"
              : "text-muted-foreground hover:bg-muted hover:text-foreground",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/** In-panel empty state that says what to do next. */
export function PanelEmpty({
  icon,
  title,
  children,
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex min-h-40 flex-col items-center justify-center rounded-lg border border-dashed px-4 py-6 text-center">
      {icon && <div className="mb-2 text-muted-foreground">{icon}</div>}
      <p className="text-sm font-medium">{title}</p>
      {children && (
        <div className="mt-1 max-w-sm text-xs text-muted-foreground">
          {children}
        </div>
      )}
    </div>
  );
}
