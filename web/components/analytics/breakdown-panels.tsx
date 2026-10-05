"use client";
import { ChevronRight, Disc3, MessagesSquare } from "lucide-react";
import type { Insights, OutcomeCounts, ThreadSummary } from "@/lib/api";
import { DataTable } from "@/components/charts/data-table";
import { Swatch } from "@/components/charts/legend";
import { SplitBar } from "@/components/charts/split-bar";
import {
  formatAgo,
  formatCost,
  formatCount,
  formatExact,
  formatPercent,
  OUTCOMES,
  routeRows,
  threadTitle,
  tokenTotal,
} from "@/lib/insights";
import { Panel, PanelEmpty } from "./panel";

const outcomeParts = (c: Record<(typeof OUTCOMES)[number]["key"], number>) =>
  OUTCOMES.map((o) => ({
    key: o.key,
    label: o.label,
    color: o.color,
    value: c[o.key],
  }));
const outcomeLabel = (
  name: string,
  c: Record<(typeof OUTCOMES)[number]["key"], number>,
) =>
  `${name}: ${OUTCOMES.map((o) => `${o.label} ${formatExact(c[o.key])}`).join(", ")}`;

/** History outcome value behind each count, for links into Traffic. */
const OUTCOME_FILTER: Record<(typeof OUTCOMES)[number]["key"], string> = {
  hits: "hit",
  recorded: "recorded",
  misses: "miss",
  interrupted: "interrupted",
  errors: "error",
};

/**
 * Where requests ended up (outcome shares, each a link into Traffic) and
 * which endpoints they hit (per-route outcome bars).
 */
export function OutcomesPanel({
  totals,
  routes,
  onOpenOutcome,
  className,
}: {
  totals: OutcomeCounts;
  routes: Insights["routes"];
  onOpenOutcome: (outcome: string) => void;
  className?: string;
}) {
  const rows = routeRows(routes);
  const max = Math.max(1, ...rows.map((r) => r.requests));
  const total = totals.requests;
  return (
    <Panel
      className={className}
      title="Outcomes & routes"
      description="Where requests ended up; select an outcome to open it in Traffic"
      table={
        <div className="space-y-3">
          <DataTable
            caption="Requests by outcome"
            rows={OUTCOMES}
            rowKey={(o) => o.key}
            columns={[
              { key: "o", header: "Outcome", cell: (o) => o.label },
              {
                key: "n",
                header: "Requests",
                numeric: true,
                cell: (o) => formatExact(totals[o.key]),
              },
              {
                key: "p",
                header: "Share",
                numeric: true,
                cell: (o) =>
                  formatPercent(total ? totals[o.key] / total : null),
              },
            ]}
          />
          <DataTable
            caption="Requests per route by outcome"
            rows={rows}
            rowKey={(r) => r.route}
            columns={[
              { key: "route", header: "Route", cell: (r) => r.route },
              {
                key: "req",
                header: "Requests",
                numeric: true,
                cell: (r) => formatExact(r.requests),
              },
              ...OUTCOMES.map((o) => ({
                key: o.key,
                header: o.label,
                numeric: true,
                cell: (r: (typeof rows)[number]) => formatExact(r[o.key]),
              })),
              {
                key: "rate",
                header: "Hit rate",
                numeric: true,
                cell: (r) => formatPercent(r.hitRate),
              },
            ]}
          />
        </div>
      }
    >
      <ul className="-mx-2 space-y-0.5">
        {OUTCOMES.map((o) => {
          const share = total ? totals[o.key] / total : 0;
          return (
            <li key={o.key}>
              <button
                type="button"
                onClick={() => onOpenOutcome(OUTCOME_FILTER[o.key])}
                title={`${o.hint}. Open in Traffic`}
                className="group w-full rounded-md px-2 py-1.5 text-left text-xs outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
              >
                <span className="flex items-center gap-2">
                  <Swatch color={o.color} />
                  <span className="font-medium text-foreground">{o.label}</span>
                  <span className="hidden truncate text-muted-foreground sm:inline xl:hidden 2xl:inline">
                    {o.hint}
                  </span>
                  <span className="ml-auto tabular-nums font-semibold">
                    {formatExact(totals[o.key])}
                  </span>
                  <span className="w-9 text-right tabular-nums text-muted-foreground">
                    {formatPercent(total ? share : null)}
                  </span>
                </span>
                <span
                  aria-hidden="true"
                  className="mt-1 ml-[18px] block h-1 overflow-hidden rounded-full bg-muted"
                >
                  <span
                    className="block h-full rounded-full"
                    style={{ width: `${share * 100}%`, background: o.color }}
                  />
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      <h4 className="mt-4 mb-2 border-t pt-3 text-xs font-medium text-muted-foreground">
        Routes
      </h4>
      {!rows.length ? (
        <p className="text-xs text-muted-foreground">
          No routes in this range.
        </p>
      ) : (
        <ul className="space-y-3">
          {rows.map((r) => (
            <li key={r.route} className="min-w-0">
              <div className="mb-1.5 flex items-baseline gap-2 text-xs">
                <code className="min-w-0 truncate font-medium text-foreground">
                  {r.route}
                </code>
                <span className="ml-auto shrink-0 tabular-nums text-muted-foreground">
                  <span className="font-semibold text-foreground">
                    {formatCount(r.requests)}
                  </span>{" "}
                  · {formatPercent(r.hitRate)} hit
                </span>
              </div>
              <SplitBar
                parts={outcomeParts(r)}
                fill={r.requests / max}
                label={outcomeLabel(r.route, r)}
              />
            </li>
          ))}
        </ul>
      )}
    </Panel>
  );
}

const rowButton =
  "group flex w-full min-w-0 items-start gap-3 rounded-lg px-2 py-2 text-left outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring";

export function TopRecordingsPanel({
  recordings,
  onInspect,
  className,
}: {
  recordings: Insights["top_recordings"];
  onInspect: (id: number) => void;
  className?: string;
}) {
  return (
    <Panel
      className={className}
      title="Most replayed recordings"
      description="By hits in range; open one to inspect"
      bodyClassName="px-2 sm:px-3"
    >
      {!recordings.length ? (
        <div className="px-2">
          <PanelEmpty
            icon={<Disc3 className="size-5" />}
            title="Nothing replayed yet"
          >
            Recordings show up here once a request is served from the cache.
          </PanelEmpty>
        </div>
      ) : (
        <ol className="space-y-0.5">
          {recordings.map((r, i) => (
            <li key={r.recording_id}>
              <button
                type="button"
                className={rowButton}
                onClick={() => onInspect(r.recording_id)}
              >
                <span className="mt-0.5 w-4 shrink-0 text-right text-xs tabular-nums text-muted-foreground">
                  {i + 1}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="line-clamp-2 text-xs text-foreground [overflow-wrap:anywhere]">
                    {r.preview?.trim() || `Recording #${r.recording_id}`}
                  </span>
                  <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                    {r.model || "unknown model"} ·{" "}
                    {formatCount(tokenTotal(r.replayed_tokens))} tokens replayed
                    {r.saved_cost != null && (
                      <> · {formatCost(r.saved_cost)} saved</>
                    )}
                  </span>
                </span>
                <span className="shrink-0 text-right">
                  <span className="block text-sm font-semibold tabular-nums">
                    {formatCount(r.hits)}
                  </span>
                  <span className="block text-[11px] text-muted-foreground">
                    hits
                  </span>
                </span>
                <ChevronRight
                  className="mt-1 size-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100"
                  aria-hidden="true"
                />
              </button>
            </li>
          ))}
        </ol>
      )}
    </Panel>
  );
}

export function TopThreadsPanel({
  threads,
  onOpen,
  className,
}: {
  threads: ThreadSummary[];
  onOpen: (thread: string) => void;
  className?: string;
}) {
  return (
    <Panel
      className={className}
      title="Busiest conversations"
      description="Threads with the most requests in range"
      bodyClassName="px-2 sm:px-3"
    >
      {!threads.length ? (
        <div className="px-2">
          <PanelEmpty
            icon={<MessagesSquare className="size-5" />}
            title="No conversation threads"
          >
            Threads group the turns of one agent conversation by its shared
            opening.
          </PanelEmpty>
        </div>
      ) : (
        <ol className="space-y-0.5">
          {threads.map((t) => (
            <li key={t.thread}>
              <button
                type="button"
                className={rowButton}
                onClick={() => onOpen(t.thread)}
              >
                <span className="min-w-0 flex-1">
                  <span className="line-clamp-2 text-xs text-foreground [overflow-wrap:anywhere]">
                    {threadTitle(t)}
                  </span>
                  <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                    {t.model || "unknown model"} · {formatCount(t.max_items)}{" "}
                    items · {formatAgo(t.last_at)}
                  </span>
                  <SplitBar
                    className="mt-1.5"
                    height={4}
                    parts={outcomeParts(t)}
                    label={outcomeLabel(threadTitle(t), t)}
                  />
                </span>
                <span className="shrink-0 text-right">
                  <span className="block text-sm font-semibold tabular-nums">
                    {formatCount(t.requests)}
                  </span>
                  <span className="block text-[11px] text-muted-foreground">
                    requests
                  </span>
                </span>
                <ChevronRight
                  className="mt-1 size-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100"
                  aria-hidden="true"
                />
              </button>
            </li>
          ))}
        </ol>
      )}
    </Panel>
  );
}
