"use client";
import { ArrowUpRight } from "lucide-react";
import type { InsightBucket, Insights } from "@/lib/api";
import { ColumnChart } from "@/components/charts/column-chart";
import { DataTable } from "@/components/charts/data-table";
import { Legend } from "@/components/charts/legend";
import {
  bucketTick,
  bucketTitle,
  formatCount,
  formatExact,
  formatPercent,
  hitRate,
  OUTCOMES,
} from "@/lib/insights";
import { Panel, PanelEmpty } from "./panel";

/** Requests over time, stacked by outcome; the legend doubles as the totals. */
export function TrafficPanel({
  insights,
  series,
  onOpenTraffic,
  className,
}: {
  insights: Insights;
  series: InsightBucket[];
  onOpenTraffic: () => void;
  className?: string;
}) {
  const bucket = insights.bucket;
  const empty = series.every((b) => b.requests === 0);
  return (
    <Panel
      className={className}
      title="Traffic"
      description={`Requests per ${bucket}, by outcome${bucket === "day" ? " (UTC days)" : ""}`}
      actions={
        <button
          type="button"
          onClick={onOpenTraffic}
          className="inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs font-medium text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
        >
          Open traffic <ArrowUpRight className="size-3.5" aria-hidden="true" />
        </button>
      }
      legend={
        <Legend
          items={OUTCOMES.map((o) => ({
            key: o.key,
            label: o.label,
            color: o.color,
            hint: o.hint,
          }))}
        />
      }
      table={
        <DataTable
          caption="Requests per bucket by outcome"
          rows={series}
          rowKey={(b) => b.start}
          columns={[
            {
              key: "start",
              header: bucket === "day" ? "Day (UTC)" : "Hour",
              cell: (b) => bucketTitle(b.start, bucket),
            },
            {
              key: "requests",
              header: "Requests",
              numeric: true,
              cell: (b) => formatExact(b.requests),
            },
            ...OUTCOMES.map((o) => ({
              key: o.key,
              header: o.label,
              numeric: true,
              cell: (b: InsightBucket) => formatExact(b[o.key]),
            })),
            {
              key: "rate",
              header: "Hit rate",
              numeric: true,
              cell: (b) => formatPercent(hitRate(b)),
            },
          ]}
        />
      }
    >
      {empty ? (
        <PanelEmpty title="No requests in this range">
          Send traffic through the proxy, or widen the range above.
        </PanelEmpty>
      ) : (
        <ColumnChart
          rows={series}
          series={OUTCOMES}
          value={(b, k) => b[k as (typeof OUTCOMES)[number]["key"]]}
          tick={(b) => bucketTick(b.start, bucket)}
          title={(b) => bucketTitle(b.start, bucket)}
          format={formatCount}
          formatValue={formatExact}
          footer={(b) => {
            const r = hitRate(b);
            return r == null ? null : <>Hit rate {formatPercent(r)}</>;
          }}
          label={`Requests per ${bucket} by outcome`}
          unit={bucket}
          height={290}
        />
      )}
    </Panel>
  );
}
