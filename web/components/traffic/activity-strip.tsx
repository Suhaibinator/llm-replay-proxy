"use client";
import { useMemo } from "react";
import type { History } from "@/lib/api";
import { activity, bucketLabel, TONES } from "@/lib/traffic";
import { cn } from "@/lib/utils";
import { toneBg, toneLabel } from "./tone";

const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
});
const day = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

/** Requests per time bucket across the visible rows, stacked by outcome. */
export function ActivityStrip({ rows, now }: { rows: History[]; now: number }) {
  const a = useMemo(() => activity(rows, now), [rows, now]);
  const fmt = a.end - a.start > 20 * 3600_000 ? day : clock;
  const total = rows.length;
  const present = TONES.filter((t) => a.buckets.some((b) => b.counts[t]));
  const summary = `${total.toLocaleString()} requests ${bucketLabel(a.minutes)} from ${fmt.format(a.start)} to ${fmt.format(a.end)}, peak ${a.peak}.`;
  return (
    <figure className="rounded-xl border bg-card px-4 pb-2.5 pt-3">
      <figcaption className="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="font-medium text-foreground">Activity</span>
        <span>requests {bucketLabel(a.minutes)}</span>
        <span className="tabular-nums">peak {a.peak.toLocaleString()}</span>
        <span className="ml-auto flex flex-wrap gap-x-3 gap-y-1">
          {present.map((t) => (
            <span key={t} className="inline-flex items-center gap-1.5">
              <span className={cn("size-2 rounded-[2px]", toneBg[t])} />
              {toneLabel[t]}
            </span>
          ))}
        </span>
      </figcaption>
      <div
        role="img"
        aria-label={summary}
        className="flex h-12 items-end gap-px"
      >
        {a.buckets.map((b) => (
          <div
            key={b.start}
            title={`${fmt.format(b.start)} · ${b.total.toLocaleString()} request${b.total === 1 ? "" : "s"}${TONES.filter(
              (t) => b.counts[t],
            )
              .map((t) => `\n${toneLabel[t]}: ${b.counts[t]}`)
              .join("")}`}
            className="group relative flex h-full min-w-0 flex-1 flex-col-reverse"
          >
            {b.total === 0 ? (
              <span className="h-px w-full bg-border" />
            ) : (
              TONES.filter((t) => b.counts[t]).map((t) => (
                <span
                  key={t}
                  className={cn(
                    "w-full first:rounded-b-[1px] last:rounded-t-[2px] group-hover:opacity-80",
                    toneBg[t],
                  )}
                  style={{
                    height: `${(b.counts[t] / Math.max(1, a.peak)) * 100}%`,
                  }}
                />
              ))
            )}
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-[11px] tabular-nums text-muted-foreground">
        <span>{fmt.format(a.start)}</span>
        <span>now</span>
      </div>
    </figure>
  );
}
