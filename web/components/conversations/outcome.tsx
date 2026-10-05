"use client";
// Outcome colors come only from the --color-{hit,miss,recorded,interrupted,error}
// theme tokens. Color is never the only signal: pills carry a label, strips a
// text equivalent, and non-success marks a hatch.
import type { CSSProperties } from "react";
import type { OutcomeCounts } from "@/lib/api";
import {
  OUTCOME_LABEL,
  describeCounts,
  outcomeKind,
  stripSegments,
  type OutcomeKind,
} from "@/lib/threads";
import { cn } from "@/lib/utils";

// Literal class names so Tailwind generates them.
export const OUTCOME_BG: Record<OutcomeKind, string> = {
  hit: "bg-hit",
  miss: "bg-miss",
  recorded: "bg-recorded",
  interrupted: "bg-interrupted",
  error: "bg-error",
};
export const OUTCOME_SOFT_BG: Record<OutcomeKind, string> = {
  hit: "bg-hit/12",
  miss: "bg-miss/12",
  recorded: "bg-recorded/12",
  interrupted: "bg-interrupted/12",
  error: "bg-error/12",
};
export const OUTCOME_BORDER: Record<OutcomeKind, string> = {
  hit: "border-hit/45",
  miss: "border-miss/45",
  recorded: "border-recorded/45",
  interrupted: "border-interrupted/45",
  error: "border-error/45",
};
export const OUTCOME_ACCENT: Record<OutcomeKind, string> = {
  hit: "border-l-hit",
  miss: "border-l-miss",
  recorded: "border-l-recorded",
  interrupted: "border-l-interrupted",
  error: "border-l-error",
};

/** The token as a CSS value; `@theme inline` may not emit --color-*. */
export const outcomeColor = (kind: OutcomeKind) =>
  `var(--color-${kind}, var(--${kind}))`;

/** Successful outcomes are solid; the rest are hatched as a second cue. */
export const isTrouble = (k: OutcomeKind) => k !== "hit" && k !== "recorded";

export function outcomeFill(kind: OutcomeKind, faded = false): CSSProperties {
  const color = outcomeColor(kind);
  const mix = (p: number) => `color-mix(in oklab, ${color} ${p}%, transparent)`;
  if (faded)
    return {
      background: isTrouble(kind)
        ? `repeating-linear-gradient(135deg, ${mix(55)} 0 3px, ${mix(25)} 3px 5px)`
        : mix(42),
    };
  if (!isTrouble(kind)) return { background: color };
  return {
    background: `repeating-linear-gradient(135deg, ${color} 0 3px, color-mix(in oklab, ${color} 55%, transparent) 3px 5px)`,
  };
}

/** A left edge in the outcome color; the parent needs `relative overflow-hidden`. */
export function AccentBar({ kind }: { kind: OutcomeKind }) {
  return (
    <span
      aria-hidden="true"
      className={cn("absolute inset-y-0 left-0 w-1", OUTCOME_BG[kind])}
    />
  );
}

export function OutcomeDot({
  kind,
  className,
}: {
  kind: OutcomeKind;
  className?: string;
}) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-block size-2 shrink-0 rounded-full",
        OUTCOME_BG[kind],
        className,
      )}
    />
  );
}

/** A labelled outcome: colored dot plus words, in text ink. */
export function OutcomePill({
  outcome,
  className,
  compact,
}: {
  outcome: string;
  className?: string;
  /** Below the sm breakpoint show only the dot (the label stays for screen readers). */
  compact?: boolean;
}) {
  const kind = outcomeKind(outcome);
  const label =
    outcome === "incomplete" ? "Incomplete" : OUTCOME_LABEL[kind] || outcome;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 whitespace-nowrap rounded-full border px-2 py-0.5 text-xs font-medium text-foreground",
        OUTCOME_SOFT_BG[kind],
        OUTCOME_BORDER[kind],
        compact && "max-sm:border-0 max-sm:bg-transparent max-sm:px-0",
        className,
      )}
    >
      <OutcomeDot kind={kind} className={cn(compact && "max-sm:size-2.5")} />
      <span className={cn(compact && "max-sm:sr-only")}>{label}</span>
    </span>
  );
}

/** Proportional outcome bar for a thread, with its text equivalent. */
export function OutcomeStrip({
  counts,
  className,
}: {
  counts: OutcomeCounts;
  className?: string;
}) {
  const segments = stripSegments(counts);
  return (
    <div
      role="img"
      aria-label={describeCounts(counts)}
      className={cn("flex h-2 w-full gap-[2px] overflow-hidden", className)}
    >
      {segments.map((s) => (
        <span
          key={s.kind}
          className="h-full min-w-[3px] first:rounded-l-full last:rounded-r-full"
          style={{ flexGrow: s.count, ...outcomeFill(s.kind) }}
        />
      ))}
      {!segments.length && (
        <span className="h-full flex-1 rounded-full bg-muted" />
      )}
    </div>
  );
}

export function OutcomeLegend({
  counts,
  className,
}: {
  counts: Record<OutcomeKind, number>;
  className?: string;
}) {
  const kinds = (Object.keys(counts) as OutcomeKind[]).filter(
    (k) => counts[k] > 0,
  );
  return (
    <ul
      aria-label="Outcomes"
      className={cn(
        "flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground",
        className,
      )}
    >
      {kinds.map((k) => (
        <li key={k} className="inline-flex items-center gap-1.5">
          <span
            aria-hidden="true"
            className="inline-block h-2.5 w-3.5 rounded-[3px]"
            style={outcomeFill(k)}
          />
          <span className="text-foreground">{OUTCOME_LABEL[k]}</span>
          <span className="tabular-nums">{counts[k]}</span>
        </li>
      ))}
    </ul>
  );
}
