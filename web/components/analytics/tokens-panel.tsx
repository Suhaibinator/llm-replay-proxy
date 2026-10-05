"use client";
import type { InsightBucket, Insights, TokenTotals } from "@/lib/api";
import { ColumnChart } from "@/components/charts/column-chart";
import { DataTable } from "@/components/charts/data-table";
import { Legend, Swatch } from "@/components/charts/legend";
import { SplitBar } from "@/components/charts/split-bar";
import {
  bucketTick,
  bucketTitle,
  cachedShare,
  formatCount,
  formatExact,
  formatPercent,
  partsTotal,
  reasoningShare,
  TOKEN_PARTS,
  tokenParts,
  tokenSeries,
  type TokenRow,
} from "@/lib/insights";
import { Panel, PanelEmpty } from "./panel";

function Composition({
  title,
  note,
  totals,
}: {
  title: string;
  note: string;
  totals: TokenTotals;
}) {
  const parts = tokenParts(totals);
  const total = partsTotal(parts);
  const reasoning = reasoningShare(totals),
    cached = cachedShare(totals);
  return (
    <div className="min-w-0">
      <div className="flex items-baseline justify-between gap-2">
        <p className="text-xs font-medium text-muted-foreground">{title}</p>
        <p className="text-lg font-semibold tracking-tight">
          {formatCount(total)}
        </p>
      </div>
      <p className="mb-2 text-[11px] text-muted-foreground">{note}</p>
      <SplitBar
        height={10}
        label={`${title}: ${TOKEN_PARTS.map((p) => `${p.label} ${formatExact(parts[p.key])}`).join(", ")}`}
        parts={TOKEN_PARTS.map((p) => ({
          key: p.key,
          label: p.label,
          color: p.color,
          value: parts[p.key],
        }))}
      />
      <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
        {TOKEN_PARTS.map((p) => (
          <div key={p.key} className="flex min-w-0 items-center gap-1.5">
            <Swatch color={p.color} />
            <dt className="truncate text-muted-foreground">{p.label}</dt>
            <dd className="ml-auto font-medium tabular-nums">
              {total ? formatPercent(parts[p.key] / total) : "—"}
            </dd>
          </div>
        ))}
      </dl>
      {total > 0 && (
        <p className="mt-2 text-[11px] leading-relaxed text-muted-foreground">
          {reasoning != null && reasoning > 0 && (
            <>
              Reasoning is{" "}
              <strong className="font-semibold text-foreground">
                {formatPercent(reasoning)}
              </strong>{" "}
              of output.{" "}
            </>
          )}
          {cached != null && cached > 0 && (
            <>
              Provider cache served{" "}
              <strong className="font-semibold text-foreground">
                {formatPercent(cached)}
              </strong>{" "}
              of input.
            </>
          )}
        </p>
      )}
    </div>
  );
}

/**
 * Token flow over time as a mirrored column chart: tokens fetched upstream
 * rise above the baseline, tokens replayed from recordings hang below it, on
 * one scale, each stacked by composition. The aside totals the composition.
 */
export function TokensPanel({
  insights,
  series,
  className,
}: {
  insights: Insights;
  series: InsightBucket[];
  className?: string;
}) {
  const bucket = insights.bucket;
  const rows = tokenSeries(series);
  const t = insights.totals;
  const empty = rows.every(
    (r) => r.upstreamTotal === 0 && r.replayedTotal === 0,
  );
  const partSeries = TOKEN_PARTS.map((p) => ({
    key: p.key,
    label: p.label,
    color: p.color,
  }));
  const pick = (side: "upstream" | "replayed") => (r: TokenRow, k: string) =>
    r[side][k as keyof TokenRow["upstream"]];
  return (
    <Panel
      className={className}
      title="Tokens"
      description="Fetched upstream (above) vs replayed from recordings (below): the work the cache saved"
      legend={
        <Legend
          items={TOKEN_PARTS.map((p) => ({
            key: p.key,
            label: p.label,
            color: p.color,
            hint: p.hint,
          }))}
        />
      }
      table={
        <DataTable
          caption="Tokens per bucket, upstream and replayed"
          rows={rows}
          rowKey={(r) => r.start}
          columns={[
            {
              key: "start",
              header: bucket === "day" ? "Day (UTC)" : "Hour",
              cell: (r) => bucketTitle(r.start, bucket),
            },
            {
              key: "ut",
              header: "Upstream",
              numeric: true,
              cell: (r) => formatExact(r.upstreamTotal),
            },
            ...TOKEN_PARTS.map((p) => ({
              key: `u-${p.key}`,
              header: `↑ ${p.label}`,
              numeric: true,
              cell: (r: TokenRow) => formatExact(r.upstream[p.key]),
            })),
            {
              key: "rt",
              header: "Replayed",
              numeric: true,
              cell: (r) => formatExact(r.replayedTotal),
            },
            ...TOKEN_PARTS.map((p) => ({
              key: `r-${p.key}`,
              header: `↓ ${p.label}`,
              numeric: true,
              cell: (r: TokenRow) => formatExact(r.replayed[p.key]),
            })),
          ]}
        />
      }
    >
      <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_17rem]">
        {empty ? (
          <PanelEmpty title="No token usage reported">
            Usage is read from each response&apos;s usage block (for streams,
            the final response.completed event). Requests without one count as
            traffic only.
          </PanelEmpty>
        ) : (
          <ColumnChart
            rows={rows}
            series={partSeries}
            value={pick("upstream")}
            aboveLabel="Upstream"
            below={{
              label: "Replayed",
              series: partSeries,
              value: pick("replayed"),
            }}
            tick={(r) => bucketTick(r.start, bucket)}
            title={(r) => bucketTitle(r.start, bucket)}
            format={formatCount}
            formatValue={formatExact}
            label={`Tokens per ${bucket}: upstream above the baseline, replayed below, by composition`}
            unit={bucket}
            height={260}
          />
        )}
        <div className="grid content-start gap-5 sm:grid-cols-2 lg:grid-cols-1">
          <Composition
            title="Fetched upstream"
            note="Paid for: recorded responses"
            totals={t.upstream_tokens}
          />
          <Composition
            title="Replayed"
            note="Served from recordings instead of upstream"
            totals={t.replayed_tokens}
          />
        </div>
      </div>
    </Panel>
  );
}
