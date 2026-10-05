"use client";
import { useState } from "react";
import { Timer } from "lucide-react";
import type { Insights } from "@/lib/api";
import { ColumnChart } from "@/components/charts/column-chart";
import { DataTable } from "@/components/charts/data-table";
import { Legend } from "@/components/charts/legend";
import { PercentilePlot } from "@/components/charts/percentile-plot";
import {
  formatExact,
  formatMs,
  formatPercent,
  formatRatio,
  histogramRows,
  speedup,
  type HistogramRow,
  type LatencyMetric,
  type LatencyWithFirst,
} from "@/lib/insights";
import { Panel, PanelEmpty, Segmented } from "./panel";

export const SOURCES = [
  { key: "upstream", label: "Upstream", color: "var(--color-recorded)" },
  { key: "replay", label: "Replay", color: "var(--color-hit)" },
] as const;

/**
 * How long requests take, upstream vs replay: a distribution (share of each
 * source's requests per duration bucket, so a few slow upstream calls compare
 * fairly with many fast replays) and the p50/p95/p99 spread on a log axis.
 */
export function LatencyPanel({
  insights,
  className,
}: {
  insights: Insights;
  className?: string;
}) {
  const up = insights.latency.upstream as LatencyWithFirst,
    re = insights.latency.replay as LatencyWithFirst;
  const hasFirstHistogram = !!(
    up.first_event_histogram?.length || re.first_event_histogram?.length
  );
  const [metric, setMetric] = useState<LatencyMetric>("duration");
  const [scale, setScale] = useState<"share" | "count">("share");
  const m: LatencyMetric =
    metric === "first_event" && !hasFirstHistogram ? "duration" : metric;
  const rows = histogramRows(up, re, m);
  const empty = rows.every((r) => r.upstream === 0 && r.replay === 0);
  const x = speedup(up.duration_ms, re.duration_ms);
  const xFirst = speedup(up.first_event_ms, re.first_event_ms);
  const metricLabel =
    m === "duration" ? "total duration" : "time to first event";
  const val = (r: HistogramRow, k: string) =>
    scale === "share"
      ? k === "upstream"
        ? r.upstreamShare
        : r.replayShare
      : k === "upstream"
        ? r.upstream
        : r.replay;
  const percentileRows = [
    {
      key: "ud",
      label: "Upstream · duration",
      color: SOURCES[0].color,
      ...pick(up.duration_ms),
    },
    {
      key: "rd",
      label: "Replay · duration",
      color: SOURCES[1].color,
      ...pick(re.duration_ms),
    },
    {
      key: "uf",
      label: "Upstream · first event",
      color: SOURCES[0].color,
      ...pick(up.first_event_ms),
    },
    {
      key: "rf",
      label: "Replay · first event",
      color: SOURCES[1].color,
      ...pick(re.first_event_ms),
    },
  ];
  return (
    <Panel
      className={className}
      title="Latency"
      description={`Distribution of ${metricLabel}, upstream vs replay`}
      actions={
        <>
          {hasFirstHistogram && (
            <Segmented
              label="Latency metric"
              value={metric}
              onChange={setMetric}
              options={[
                { id: "duration", label: "Duration" },
                { id: "first_event", label: "First event" },
              ]}
            />
          )}
          <Segmented
            label="Histogram scale"
            value={scale}
            onChange={setScale}
            options={[
              {
                id: "share",
                label: "%",
                title: "Share of each source's requests",
              },
              { id: "count", label: "Count", title: "Number of requests" },
            ]}
          />
        </>
      }
      legend={<Legend items={SOURCES.map((s) => ({ ...s }))} />}
      table={
        <div className="space-y-3">
          <DataTable
            caption={`Histogram of ${metricLabel}`}
            rows={rows}
            rowKey={(r) => r.label}
            columns={[
              { key: "b", header: "Bucket", cell: (r) => r.label },
              {
                key: "u",
                header: "Upstream",
                numeric: true,
                cell: (r) => formatExact(r.upstream),
              },
              {
                key: "us",
                header: "Upstream %",
                numeric: true,
                cell: (r) => formatPercent(r.upstreamShare),
              },
              {
                key: "r",
                header: "Replay",
                numeric: true,
                cell: (r) => formatExact(r.replay),
              },
              {
                key: "rs",
                header: "Replay %",
                numeric: true,
                cell: (r) => formatPercent(r.replayShare),
              },
            ]}
          />
          <DataTable
            caption="Latency percentiles"
            rows={percentileRows}
            rowKey={(r) => r.key}
            columns={[
              { key: "l", header: "Series", cell: (r) => r.label },
              {
                key: "50",
                header: "p50",
                numeric: true,
                cell: (r) => formatMs(r.p50),
              },
              {
                key: "95",
                header: "p95",
                numeric: true,
                cell: (r) => formatMs(r.p95),
              },
              {
                key: "99",
                header: "p99",
                numeric: true,
                cell: (r) => formatMs(r.p99),
              },
              {
                key: "n",
                header: "Samples",
                numeric: true,
                cell: (r) => formatExact(r.samples),
              },
            ]}
          />
        </div>
      }
    >
      <div className="grid gap-6 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
        {empty ? (
          <PanelEmpty
            icon={<Timer className="size-5" />}
            title="No timed requests"
          >
            Timing is recorded for every request the proxy serves; older history
            rows without timing are left out.
          </PanelEmpty>
        ) : (
          <ColumnChart
            rows={rows}
            series={SOURCES.map((s) => ({ ...s }))}
            value={val}
            mode="group"
            tick={(r) => r.short}
            title={(r) => `${r.label} · ${metricLabel}`}
            format={
              scale === "share"
                ? (n) => formatPercent(n)
                : (n) => formatExact(n)
            }
            formatValue={
              scale === "share"
                ? (n) => formatPercent(n)
                : (n) => formatExact(n)
            }
            footer={(r) => (
              <>
                {formatExact(r.upstream)} upstream · {formatExact(r.replay)}{" "}
                replay requests
              </>
            )}
            integer={scale === "count"}
            label={`Histogram of ${metricLabel} by bucket, upstream vs replay, as ${scale === "share" ? "share of requests" : "request counts"}`}
            unit="bucket"
            tickWidth={52}
            height={230}
          />
        )}
        <div className="min-w-0">
          <p className="mb-1 text-xs font-medium text-muted-foreground">
            Percentiles · log scale
          </p>
          <p className="mb-3 text-[11px] text-muted-foreground">
            <span className="inline-block size-2 rounded-full bg-muted-foreground align-middle" />{" "}
            p50{" "}
            <span className="ml-1.5 inline-block size-2 rounded-full border-2 border-muted-foreground align-middle" />{" "}
            p95{" "}
            <span className="ml-1.5 inline-block h-2 w-0.5 bg-muted-foreground align-middle" />{" "}
            p99
          </p>
          <PercentilePlot
            rows={percentileRows}
            label="Latency percentiles, upstream vs replay"
          />
          {(x != null || xFirst != null) && (
            <p className="mt-3 rounded-lg bg-muted px-3 py-2 text-xs text-muted-foreground">
              At the median, replay answers{" "}
              {x != null && (
                <strong className="font-semibold text-foreground">
                  {formatRatio(x)} faster
                </strong>
              )}
              {x != null && xFirst != null && " overall and "}
              {xFirst != null && (
                <strong className="font-semibold text-foreground">
                  {formatRatio(xFirst)} sooner
                </strong>
              )}
              {xFirst != null && " to the first event"} than upstream.
            </p>
          )}
        </div>
      </div>
    </Panel>
  );
}

function pick(
  p:
    | {
        p50: number | null;
        p95: number | null;
        p99: number | null;
        samples: number;
      }
    | undefined,
) {
  return p && p.samples > 0
    ? { p50: p.p50, p95: p.p95, p99: p.p99, samples: p.samples }
    : { p50: null, p95: null, p99: null, samples: p?.samples ?? 0 };
}
