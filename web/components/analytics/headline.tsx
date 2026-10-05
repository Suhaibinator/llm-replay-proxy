import type { ReactNode } from "react";
import { Sparkline } from "@/components/charts/sparkline";
import {
  formatCost,
  formatCount,
  formatExact,
  formatMs,
  formatPercent,
  formatRatio,
  type Headline,
} from "@/lib/insights";
import { cn } from "@/lib/utils";

function Stat({
  label,
  value,
  sub,
  trend,
  className,
  hero,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  trend?: ReactNode;
  className?: string;
  hero?: boolean;
}) {
  return (
    <div
      className={cn(
        "flex min-w-0 flex-col gap-1 bg-card p-4 sm:p-5",
        className,
      )}
    >
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className="flex min-w-0 flex-1 flex-col gap-1">
        <span
          className={cn(
            "font-semibold tracking-tight text-foreground",
            hero ? "text-5xl leading-none" : "text-2xl leading-tight",
          )}
        >
          {value}
        </span>
        {sub && <span className="text-xs text-muted-foreground">{sub}</span>}
        {trend && <span className="mt-auto pt-2">{trend}</span>}
      </dd>
    </div>
  );
}

/** Pair of numbers where the second is quieter: "2.4 s  /  8.1 s p95". */
function Pair({ a, b, bLabel }: { a: string; b: string; bLabel: string }) {
  return (
    <span className="flex items-baseline gap-1.5">
      <span>{a}</span>
      <span className="text-sm font-medium text-muted-foreground">
        / {b} <span className="text-xs font-normal">{bLabel}</span>
      </span>
    </span>
  );
}

/**
 * The headline band: one hero figure (cache hit rate: the proxy's reason to
 * exist) and the five numbers that explain it, in a single card.
 */
export function HeadlineStrip({ h }: { h: Headline }) {
  const costKnown = h.savedCost != null || h.upstreamCost != null;
  return (
    <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border shadow-sm sm:grid-cols-3 xl:grid-cols-[1.35fr_repeat(5,minmax(0,1fr))]">
      <Stat
        hero
        className="col-span-2 sm:col-span-1 bg-[color-mix(in_oklab,var(--color-hit)_7%,var(--color-card))]"
        label="Cache hit rate"
        value={formatPercent(h.hitRate)}
        sub={
          h.lookups
            ? `${formatExact(h.hits)} of ${formatExact(h.lookups)} lookups replayed`
            : "No cache lookups in this range"
        }
        trend={
          <Sparkline
            values={h.trend.hitRate}
            max={1}
            color="var(--color-hit)"
          />
        }
      />
      <Stat
        label="Requests"
        value={formatExact(h.requests)}
        sub={`${formatCount(h.threads)} conversation thread${h.threads === 1 ? "" : "s"}`}
        trend={<Sparkline values={h.trend.requests} />}
      />
      <Stat
        label="Tokens replayed"
        value={formatCount(h.replayedTokens)}
        sub={`${formatCount(h.upstreamTokens)} fetched upstream`}
        trend={
          <Sparkline values={h.trend.replayedTokens} color="var(--color-hit)" />
        }
      />
      <Stat
        label="Saved by replay"
        value={costKnown ? formatCost(h.savedCost ?? 0) : "—"}
        sub={
          costKnown
            ? `${formatCost(h.upstreamCost ?? 0)} spent upstream`
            : "Cost appears when the provider reports it (e.g. OpenRouter usage.cost)"
        }
      />
      <Stat
        label="Upstream latency"
        value={
          h.upstreamP50 == null ? (
            "—"
          ) : (
            <Pair
              a={formatMs(h.upstreamP50)}
              b={formatMs(h.upstreamP95)}
              bLabel="p95"
            />
          )
        }
        sub={
          h.upstreamP50 == null
            ? "No upstream calls timed"
            : h.speedup != null
              ? `p50 · replay is ${formatRatio(h.speedup)} faster`
              : "p50 total duration"
        }
      />
      <Stat
        className="max-sm:col-span-2"
        label="Time to first event"
        value={
          h.firstEventP50 == null ? (
            "—"
          ) : (
            <Pair
              a={formatMs(h.firstEventP50)}
              b={formatMs(h.firstEventP95)}
              bLabel="p95"
            />
          )
        }
        sub={
          h.firstEventP50 == null
            ? "Measured on streaming upstream calls"
            : "p50 upstream, streaming calls"
        }
      />
    </dl>
  );
}
