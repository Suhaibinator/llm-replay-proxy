"use client";
import { useState } from "react";
import { ArrowDown, ArrowUp, ArrowUpRight, Brain, Cpu } from "lucide-react";
import type { ModelInsight } from "@/lib/api";
import { Legend } from "@/components/charts/legend";
import { SplitBar } from "@/components/charts/split-bar";
import {
  formatCost,
  formatCount,
  formatExact,
  formatMs,
  formatPercent,
  modelRows,
  sortModels,
  TOKEN_PARTS,
  type ModelRow,
  type ModelSortKey,
  type SortDir,
} from "@/lib/insights";
import { cn } from "@/lib/utils";
import { Panel, PanelEmpty } from "./panel";

const COLUMNS: {
  key: ModelSortKey;
  label: string;
  numeric?: boolean;
  title?: string;
}[] = [
  { key: "model", label: "Model" },
  { key: "requests", label: "Requests", numeric: true },
  { key: "hitRate", label: "Hit rate", numeric: true },
  {
    key: "tokens",
    label: "Token mix",
    title: "All tokens handled (upstream + replayed), by composition",
  },
  {
    key: "reasoningShare",
    label: "Reasoning",
    numeric: true,
    title: "Share of output tokens spent on reasoning",
  },
  {
    key: "upstreamCost",
    label: "Spend · saved",
    numeric: true,
    title: "Upstream spend and cost saved by replay (USD)",
  },
  { key: "upstreamP50", label: "Upstream p50 · p95", numeric: true },
  { key: "replayP50", label: "Replay p50 · p95", numeric: true },
];

const mixParts = (r: ModelRow) =>
  TOKEN_PARTS.map((p) => ({
    key: p.key,
    label: p.label,
    color: p.color,
    value: r.mix[p.key],
  }));
const mixLabel = (r: ModelRow) =>
  `${r.model} tokens: ${TOKEN_PARTS.map((p) => `${p.label} ${formatExact(r.mix[p.key])}`).join(", ")}`;

function Latency({ p50, p95 }: { p50: number | null; p95: number | null }) {
  if (p50 == null) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="whitespace-nowrap">
      {formatMs(p50)}{" "}
      <span className="text-muted-foreground">· {formatMs(p95)}</span>
    </span>
  );
}

/** Marks models that spent output on reasoning; keeps names aligned either way. */
function ReasoningMark({ row }: { row: ModelRow }) {
  const on = row.reasoningShare != null && row.reasoningShare > 0;
  return (
    <span
      className="inline-flex size-3.5 shrink-0"
      title={on ? "Reasoning model" : undefined}
    >
      {on && (
        <Brain
          className="size-3.5 text-[var(--color-chart-4)]"
          aria-label="Reasoning model"
        />
      )}
    </span>
  );
}

function ReasoningCell({ share }: { share: number | null }) {
  if (share == null || share === 0)
    return (
      <span className="text-muted-foreground">
        {share == null ? "—" : "0%"}
      </span>
    );
  return (
    <span className="inline-flex items-center justify-end gap-1.5">
      <span
        aria-hidden="true"
        className="relative h-1.5 w-10 overflow-hidden rounded-full bg-muted"
      >
        <span
          className="absolute inset-y-0 left-0 rounded-full"
          style={{
            width: `${share * 100}%`,
            background: "var(--color-chart-4)",
          }}
        />
      </span>
      {formatPercent(share)}
    </span>
  );
}

/**
 * Per-model breakdown. Sortable; clicking a model filters the whole page to
 * it, and the arrow opens Traffic filtered to that model.
 */
export function ModelsPanel({
  models,
  selected,
  onSelect,
  onOpenTraffic,
  className,
}: {
  models: ModelInsight[];
  selected: string;
  onSelect: (model: string) => void;
  onOpenTraffic: (model: string) => void;
  className?: string;
}) {
  const [sort, setSort] = useState<{ key: ModelSortKey; dir: SortDir }>({
    key: "requests",
    dir: "desc",
  });
  const rows = sortModels(modelRows(models), sort.key, sort.dir);
  const maxRequests = Math.max(1, ...rows.map((r) => r.requests));
  const maxTokens = Math.max(1, ...rows.map((r) => r.tokens));
  const toggle = (key: ModelSortKey) =>
    setSort((s) =>
      s.key === key
        ? { key, dir: s.dir === "asc" ? "desc" : "asc" }
        : { key, dir: key === "model" ? "asc" : "desc" },
    );
  const anyCost = rows.some(
    (r) => r.upstreamCost != null || r.savedCost != null,
  );

  const selectButton = (r: ModelRow) => (
    <button
      type="button"
      onClick={() => onSelect(r.model)}
      aria-pressed={selected === r.model}
      title={
        selected === r.model
          ? "Clear model filter"
          : `Filter the overview to ${r.model}`
      }
      className="min-w-0 truncate rounded-sm text-left font-medium underline-offset-2 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
    >
      {r.model}
    </button>
  );
  const trafficButton = (r: ModelRow) => (
    <button
      type="button"
      onClick={() => onOpenTraffic(r.model)}
      aria-label={`Open traffic for ${r.model}`}
      title="Open traffic for this model"
      className="inline-flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
    >
      <ArrowUpRight className="size-3.5" />
    </button>
  );

  return (
    <Panel
      className={className}
      title="Models"
      description="Click a model to focus the whole overview on it"
      legend={
        rows.length ? (
          <Legend
            items={TOKEN_PARTS.map((p) => ({
              key: p.key,
              label: p.label,
              color: p.color,
              hint: p.hint,
            }))}
          />
        ) : undefined
      }
    >
      {!rows.length ? (
        <PanelEmpty
          icon={<Cpu className="size-5" />}
          title="No models in this range"
        >
          Models come from each response&apos;s model field, or the
          request&apos;s when the response has none.
        </PanelEmpty>
      ) : (
        <>
          {/* Wide screens: a sortable table. */}
          <div className="-mx-1 hidden overflow-x-auto md:block">
            <table className="w-full border-collapse text-left text-xs">
              <caption className="sr-only">
                Per-model requests, cache hit rate, tokens, cost and latency.
                Column headers sort.
              </caption>
              <thead>
                <tr className="border-b">
                  {COLUMNS.map((c) => (
                    <th
                      key={c.key}
                      scope="col"
                      aria-sort={
                        sort.key === c.key
                          ? sort.dir === "asc"
                            ? "ascending"
                            : "descending"
                          : "none"
                      }
                      className={cn(
                        "px-2 pb-2 font-medium text-muted-foreground",
                        c.numeric && "text-right",
                      )}
                    >
                      <button
                        type="button"
                        title={c.title}
                        onClick={() => toggle(c.key)}
                        className={cn(
                          "inline-flex items-center gap-1 whitespace-nowrap rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
                          sort.key === c.key && "text-foreground",
                        )}
                      >
                        {c.label}
                        {sort.key === c.key &&
                          (sort.dir === "asc" ? (
                            <ArrowUp className="size-3" aria-hidden="true" />
                          ) : (
                            <ArrowDown className="size-3" aria-hidden="true" />
                          ))}
                      </button>
                    </th>
                  ))}
                  <th scope="col" className="w-8 pb-2">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr
                    key={r.model}
                    className={cn(
                      "border-b last:border-0 transition-colors hover:bg-muted/50",
                      selected === r.model &&
                        "bg-secondary/60 hover:bg-secondary/60",
                    )}
                  >
                    <th
                      scope="row"
                      className="max-w-[16rem] px-2 py-2.5 font-normal"
                    >
                      <div className="flex items-center gap-1.5">
                        <ReasoningMark row={r} />
                        {selectButton(r)}
                      </div>
                    </th>
                    <td className="px-2 py-2.5 text-right tabular-nums">
                      <div className="flex items-center justify-end gap-2">
                        <span
                          aria-hidden="true"
                          className="hidden h-1.5 w-14 overflow-hidden rounded-full bg-muted lg:block"
                        >
                          <span
                            className="block h-full rounded-full bg-muted-foreground/60"
                            style={{
                              width: `${(r.requests / maxRequests) * 100}%`,
                            }}
                          />
                        </span>
                        <span className="min-w-10">
                          {formatExact(r.requests)}
                        </span>
                      </div>
                    </td>
                    <td className="px-2 py-2.5 text-right tabular-nums">
                      {formatPercent(r.hitRate)}
                    </td>
                    <td className="min-w-40 px-2 py-2.5">
                      <div className="flex items-center gap-2">
                        <SplitBar
                          className="flex-1"
                          parts={mixParts(r)}
                          fill={r.tokens / maxTokens}
                          label={mixLabel(r)}
                        />
                        <span className="w-10 text-right tabular-nums text-muted-foreground">
                          {formatCount(r.tokens)}
                        </span>
                      </div>
                    </td>
                    <td className="px-2 py-2.5 text-right tabular-nums">
                      <ReasoningCell share={r.reasoningShare} />
                    </td>
                    <td className="whitespace-nowrap px-2 py-2.5 text-right tabular-nums">
                      {anyCost ? (
                        <>
                          {formatCost(r.upstreamCost)}{" "}
                          <span className="text-muted-foreground">
                            · {formatCost(r.savedCost)}
                          </span>
                        </>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="px-2 py-2.5 text-right tabular-nums">
                      <Latency p50={r.upstreamP50} p95={r.upstreamP95} />
                    </td>
                    <td className="px-2 py-2.5 text-right tabular-nums">
                      <Latency p50={r.replayP50} p95={r.replayP95} />
                    </td>
                    <td className="py-1 pl-1">{trafficButton(r)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {/* Narrow screens: one card per model. */}
          <div className="md:hidden">
            <label className="mb-2 flex items-center justify-end gap-2 text-xs text-muted-foreground">
              Sort by
              <select
                value={`${sort.key}:${sort.dir}`}
                onChange={(e) => {
                  const [key, dir] = e.target.value.split(":") as [
                    ModelSortKey,
                    SortDir,
                  ];
                  setSort({ key, dir });
                }}
                className="h-7 rounded-md border bg-background px-2 text-xs text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <option value="requests:desc">Requests</option>
                <option value="tokens:desc">Tokens</option>
                <option value="hitRate:desc">Hit rate</option>
                <option value="reasoningShare:desc">Reasoning share</option>
                <option value="upstreamCost:desc">Spend</option>
                <option value="upstreamP50:desc">Upstream latency</option>
                <option value="model:asc">Name</option>
              </select>
            </label>
            <ul className="divide-y rounded-lg border">
              {rows.map((r) => (
                <li
                  key={r.model}
                  className={cn(
                    "space-y-2 p-3",
                    selected === r.model && "bg-secondary/60",
                  )}
                >
                  <div className="flex items-center gap-1.5 text-sm">
                    <ReasoningMark row={r} />
                    {selectButton(r)}
                    <span className="ml-auto" />
                    {trafficButton(r)}
                  </div>
                  <SplitBar
                    parts={mixParts(r)}
                    fill={r.tokens / maxTokens}
                    label={mixLabel(r)}
                  />
                  <dl className="grid grid-cols-3 gap-x-3 gap-y-1.5 text-xs">
                    {(
                      [
                        ["Requests", formatExact(r.requests)],
                        ["Hit rate", formatPercent(r.hitRate)],
                        ["Tokens", formatCount(r.tokens)],
                        [
                          "Reasoning",
                          r.reasoningShare == null
                            ? "—"
                            : formatPercent(r.reasoningShare),
                        ],
                        ["Spend", formatCost(r.upstreamCost)],
                        ["Saved", formatCost(r.savedCost)],
                        ["Upstream p50", formatMs(r.upstreamP50)],
                        ["Upstream p95", formatMs(r.upstreamP95)],
                        ["Replay p50", formatMs(r.replayP50)],
                      ] as const
                    ).map(([k, v]) => (
                      <div key={k} className="min-w-0">
                        <dt className="truncate text-muted-foreground">{k}</dt>
                        <dd className="font-medium tabular-nums">{v}</dd>
                      </div>
                    ))}
                  </dl>
                </li>
              ))}
            </ul>
          </div>
        </>
      )}
    </Panel>
  );
}
