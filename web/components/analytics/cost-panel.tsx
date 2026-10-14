"use client";
import { useState } from "react";
import type { InsightBucket, Insights } from "@/lib/api";
import { ColumnChart } from "@/components/charts/column-chart";
import { DataTable } from "@/components/charts/data-table";
import { Legend } from "@/components/charts/legend";
import { SplitBar } from "@/components/charts/split-bar";
import {
  COST_PERIOD_LABELS,
  costBreakdown,
  costPeriods,
  costSeries,
  costSummary,
  formatCost,
  formatPercent,
  periodTick,
  periodTitle,
  type CostBreakdownRow,
  type CostPeriod,
  type CostRow,
} from "@/lib/insights";
import { Panel, PanelEmpty, Segmented } from "./panel";

const SPENT = {
  key: "upstream",
  label: "Spent upstream",
  color: "var(--color-recorded)",
  hint: "Provider-reported cost of responses fetched upstream",
};
const SAVED = {
  key: "saved",
  label: "Saved",
  color: "var(--color-hit)",
  hint: "Cost of recorded responses replayed instead of fetched",
};

function Figure({
  label,
  value,
  note,
}: {
  label: string;
  value: string;
  note?: string;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] text-muted-foreground">{label}</dt>
      <dd className="text-base font-semibold tabular-nums tracking-tight">
        {value}
      </dd>
      {note && <dd className="text-[11px] text-muted-foreground">{note}</dd>}
    </div>
  );
}

function Breakdown({
  title,
  rows,
  mono,
}: {
  title: string;
  rows: CostBreakdownRow[];
  mono?: boolean;
}) {
  const max = Math.max(1e-12, ...rows.map((r) => r.total));
  return (
    <div className="min-w-0">
      <h4 className="mb-2 text-xs font-medium text-muted-foreground">
        {title}
      </h4>
      <ul className="space-y-2.5">
        {rows.map((r) => (
          <li key={r.name} className="min-w-0">
            <div className="mb-1 flex items-baseline gap-2 text-xs">
              <span
                className={
                  "min-w-0 truncate font-medium text-foreground" +
                  (mono ? " font-mono" : "")
                }
              >
                {r.name}
              </span>
              <span className="ml-auto shrink-0 tabular-nums text-muted-foreground">
                <span className="font-semibold text-foreground">
                  {formatCost(r.upstream)}
                </span>{" "}
                spent · {formatCost(r.saved)} saved
              </span>
            </div>
            <SplitBar
              height={6}
              fill={r.total / max}
              label={`${r.name}: ${formatCost(r.upstream)} spent, ${formatCost(r.saved)} saved`}
              parts={[
                { ...SPENT, value: r.upstream ?? 0 },
                { ...SAVED, value: r.saved ?? 0 },
              ]}
            />
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * Cost over time as a mirrored column chart: upstream spend rises above the
 * baseline, what replay saved hangs below it, summed per hour, day, week or
 * month (UTC). The aside derives averages and a run rate from the totals;
 * below, spend and savings per provider and route.
 */
export function CostPanel({
  insights,
  series,
  className,
}: {
  insights: Insights;
  series: InsightBucket[];
  className?: string;
}) {
  const periods = costPeriods(insights.bucket);
  const [chosen, setChosen] = useState<CostPeriod>(periods[0]);
  const period = periods.includes(chosen) ? chosen : periods[0];
  const rows = costSeries(insights, series, period);
  const c = costSummary(insights);
  const providers = costBreakdown(insights.providers, (p) => p.provider);
  const routes = costBreakdown(insights.routes, (r) => r.route);
  const unit = COST_PERIOD_LABELS[period].toLowerCase();
  const reported = c.spent != null || c.saved != null;
  const value = (r: CostRow, k: string) =>
    k === "upstream" ? r.upstream : r.saved;

  return (
    <Panel
      className={className}
      title="Cost"
      description="Provider-reported spend upstream (above) vs what replay saved (below), in USD"
      actions={
        reported && (
          <Segmented
            label="Cost period"
            value={period}
            onChange={setChosen}
            options={periods.map((p) => ({
              id: p,
              label: COST_PERIOD_LABELS[p],
              title: `Cost per ${p} (UTC)`,
            }))}
          />
        )
      }
      legend={reported && <Legend items={[SPENT, SAVED]} />}
      table={
        reported ? (
          <DataTable
            caption={`Cost per ${unit}, spent upstream and saved by replay`}
            rows={rows}
            rowKey={(r) => r.start}
            columns={[
              {
                key: "start",
                header: COST_PERIOD_LABELS[period],
                cell: (r) => periodTitle(r, period),
              },
              {
                key: "up",
                header: "Spent upstream",
                numeric: true,
                cell: (r) => (r.reported ? formatCost(r.upstream) : "—"),
              },
              {
                key: "saved",
                header: "Saved",
                numeric: true,
                cell: (r) => (r.reported ? formatCost(r.saved) : "—"),
              },
              {
                key: "without",
                header: "Without replay",
                numeric: true,
                cell: (r) =>
                  r.reported ? formatCost(r.upstream + r.saved) : "—",
              },
            ]}
          />
        ) : undefined
      }
    >
      {!reported ? (
        <PanelEmpty title="No cost reported">
          Cost is read from each response&apos;s <code>usage.cost</code>, which
          some providers (such as OpenRouter) report. Responses without it count
          as traffic and tokens only.
        </PanelEmpty>
      ) : (
        <>
          <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_17rem]">
            <ColumnChart
              rows={rows}
              series={[SPENT]}
              value={value}
              aboveLabel="Spent"
              below={{ label: "Saved", series: [SAVED], value }}
              tick={(r) => periodTick(r.start, period)}
              title={(r) => periodTitle(r, period)}
              format={formatCost}
              footer={(r) =>
                r.reported
                  ? `${formatCost(r.upstream + r.saved)} without replay`
                  : "No cost reported"
              }
              label={`Cost per ${unit}: spent upstream above the baseline, saved by replay below`}
              unit={unit}
              integer={false}
              height={240}
            />
            <dl className="grid content-start grid-cols-2 gap-x-4 gap-y-3">
              <Figure label="Spent upstream" value={formatCost(c.spent)} />
              <Figure label="Saved" value={formatCost(c.saved)} />
              <Figure
                label="Without replay"
                value={formatCost(c.withoutReplay)}
                note="spent + saved"
              />
              <Figure
                label="Savings rate"
                value={formatPercent(c.savingsRate)}
                note="of what upstream would charge"
              />
              <Figure
                label="Per upstream call"
                value={formatCost(c.perCall)}
                note={`mean of ${(insights.totals.upstream_cost_calls ?? 0).toLocaleString()} priced`}
              />
              <Figure
                label="Saved per hit"
                value={formatCost(c.perHit)}
                note={`mean of ${(insights.totals.saved_cost_calls ?? 0).toLocaleString()} priced`}
              />
              <Figure
                label="Spend per day"
                value={formatCost(c.perDay)}
                note={`over ${c.days < 2 ? "the last 24 hours" : `${Math.ceil(c.days)} days`}`}
              />
              <Figure
                label="30-day projection"
                value={formatCost(c.perMonth)}
                note="at this daily rate"
              />
            </dl>
          </div>
          {(providers.length > 0 || routes.length > 0) && (
            <div className="mt-5 grid gap-5 border-t pt-4 sm:grid-cols-2">
              {providers.length > 0 && (
                <Breakdown title="By provider" rows={providers} />
              )}
              {routes.length > 0 && (
                <Breakdown title="By route" rows={routes} mono />
              )}
            </div>
          )}
        </>
      )}
    </Panel>
  );
}
