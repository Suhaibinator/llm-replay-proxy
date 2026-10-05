"use client";
import { useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  ArrowUpRight,
  Inbox,
  Loader2,
  RefreshCw,
  X,
} from "lucide-react";
import { api, type Insights } from "@/lib/api";
import {
  fillSeries,
  headline,
  insightsURL,
  isRange,
  mergeModelOptions,
  RANGES,
  type RangeId,
} from "@/lib/insights";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { HeadlineStrip } from "@/components/analytics/headline";
import { TrafficPanel } from "@/components/analytics/traffic-panel";
import { TokensPanel } from "@/components/analytics/tokens-panel";
import { ModelsPanel } from "@/components/analytics/models-panel";
import { LatencyPanel } from "@/components/analytics/latency-panel";
import {
  OutcomesPanel,
  TopRecordingsPanel,
  TopThreadsPanel,
} from "@/components/analytics/breakdown-panels";
import { Segmented } from "@/components/analytics/panel";
import { OverviewSkeleton } from "@/components/analytics/skeleton";
import { cn } from "@/lib/utils";
import type { ViewProps } from "./types";

const RANGE_KEY = "replay-lab.overview.range";
const ALL = "__all__";

function storedRange(): RangeId {
  try {
    const v = window.localStorage.getItem(RANGE_KEY);
    if (isRange(v)) return v;
  } catch {}
  return "7d";
}

type Loaded = { key: string; data: Insights };

/**
 * The analytics overview: one filter row (range, model) scoping a headline
 * band, traffic and token flow over time, a per-model breakdown, latency, and
 * the routes, recordings and conversations that matter most.
 */
export function OverviewView(props: ViewProps) {
  const {
    collectionId,
    collections,
    refreshKey,
    filter,
    navigate,
    inspectRecording,
  } = props;
  const [range, setRangeState] = useState<RangeId>("7d");
  const [model, setModel] = useState(filter.model ?? "");
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [retry, setRetry] = useState(0);
  const [known, setKnown] = useState<{ collection: number; models: string[] }>({
    collection: 0,
    models: [],
  });
  const [ready, setReady] = useState(false);

  // The remembered range is read after mount so the static export hydrates
  // cleanly; nothing is fetched until it is known.
  useEffect(() => {
    setRangeState(storedRange());
    setReady(true);
  }, []);
  useEffect(() => setModel(filter.model ?? ""), [filter.model]);

  const setRange = (r: RangeId) => {
    setRangeState(r);
    try {
      window.localStorage.setItem(RANGE_KEY, r);
    } catch {}
  };

  const key = `${collectionId}|${range}|${model}`;
  useEffect(() => {
    if (!collectionId || !ready) return;
    const controller = new AbortController();
    let live = true;
    setLoading(true);
    api<Insights>(insightsURL(collectionId, range, model), {
      signal: controller.signal,
    })
      .then((data) => {
        if (!live) return;
        setLoaded({ key, data });
        setError("");
        setKnown((k) => ({
          collection: collectionId,
          models: mergeModelOptions(
            k.collection === collectionId ? k.models : [],
            data.models,
          ),
        }));
      })
      .catch((e: unknown) => {
        if (!live || controller.signal.aborted) return;
        setError(e instanceof Error ? e.message : "Could not load analytics");
      })
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
      controller.abort();
    };
    // `key` covers collection, range and model; refreshKey and retry refetch in place.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, refreshKey, retry, ready]);

  const data =
    loaded && loaded.key.split("|")[0] === String(collectionId)
      ? loaded.data
      : null;
  // A different slice is on its way: hold the old frame, dimmed (no flash on polls).
  const stale = !!data && loaded!.key !== key;
  const series = useMemo(() => (data ? fillSeries(data) : []), [data]);
  const h = useMemo(
    () => (data ? headline(data, series) : null),
    [data, series],
  );
  const modelOptions = useMemo(
    () =>
      mergeModelOptions(
        known.collection === collectionId ? known.models : [],
        [],
        model,
      ),
    [known, collectionId, model],
  );
  const rangeInfo = RANGES.find((r) => r.id === range)!;
  const collection = collections.find((c) => c.id === collectionId);
  const toggleModel = (m: string) => setModel((cur) => (cur === m ? "" : m));
  const openTraffic = (m = model) =>
    navigate(m ? { view: "traffic", model: m } : { view: "traffic" });

  const filters = (
    <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
      <div className="min-w-0">
        <h2 className="text-lg font-semibold tracking-tight">Overview</h2>
        <p className="text-sm text-muted-foreground">
          {collection ? (
            <span className="font-medium text-foreground">
              {collection.name}
            </span>
          ) : (
            "No collection"
          )}
          {" · "}
          {rangeInfo.long}
          {data && data.bucket === "day" && " · UTC days"}
          {(loading || stale) && (
            <Loader2
              className="ml-2 inline size-3.5 animate-spin align-[-2px] motion-reduce:animate-none"
              aria-label="Loading"
            />
          )}
        </p>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Segmented
          label="Time range"
          size="md"
          value={range}
          onChange={setRange}
          options={RANGES.map((r) => ({
            id: r.id,
            label: r.label,
            title: r.long,
          }))}
        />
        <Select
          value={model || ALL}
          onValueChange={(v) => setModel(v === ALL ? "" : v)}
        >
          <SelectTrigger
            aria-label="Model filter"
            className="w-auto min-w-40 max-w-[16rem] bg-card"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>All models</SelectItem>
            {modelOptions.map((m) => (
              <SelectItem key={m} value={m}>
                {m}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {model && (
          <span className="inline-flex h-9 max-w-full items-center gap-1 rounded-lg border bg-secondary pl-3 pr-1 text-xs text-secondary-foreground">
            <span className="truncate">
              Model: <span className="font-semibold">{model}</span>
            </span>
            <button
              type="button"
              onClick={() => openTraffic(model)}
              className="inline-flex h-7 items-center gap-0.5 rounded-md px-1.5 font-medium outline-none hover:bg-background/70 focus-visible:ring-2 focus-visible:ring-ring"
            >
              Traffic <ArrowUpRight className="size-3" aria-hidden="true" />
            </button>
            <button
              type="button"
              onClick={() => setModel("")}
              aria-label="Clear model filter"
              className="inline-flex size-7 items-center justify-center rounded-md outline-none hover:bg-background/70 focus-visible:ring-2 focus-visible:ring-ring"
            >
              <X className="size-3.5" />
            </button>
          </span>
        )}
      </div>
    </div>
  );

  let body: React.ReactNode;
  if (!collectionId) {
    body = (
      <EmptyState title="Choose a collection">
        Analytics are per collection. Create or select one to see its traffic.
      </EmptyState>
    );
  } else if (!data && error) {
    body = (
      <div
        role="alert"
        className="flex flex-col items-start gap-3 rounded-xl border bg-card p-5 text-sm shadow-sm sm:flex-row sm:items-center"
      >
        <AlertCircle
          className="size-5 shrink-0 text-[var(--color-error)]"
          aria-hidden="true"
        />
        <div className="flex-1">
          <p className="font-medium">Analytics could not be loaded</p>
          <p className="text-muted-foreground">{error}</p>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setRetry((n) => n + 1)}
        >
          <RefreshCw className="size-3.5" /> Retry
        </Button>
      </div>
    );
  } else if (!data || !h) {
    body = <OverviewSkeleton />;
  } else if (data.totals.requests === 0) {
    body = (
      <EmptyState
        title={
          model
            ? `No requests for ${model} in the ${rangeInfo.long.toLowerCase()}`
            : `No traffic in the ${rangeInfo.long.toLowerCase()}`
        }
      >
        <p>
          {model
            ? "This model has no requests in the selected range."
            : "Point your agent's OpenAI or Anthropic base URL at this proxy and its requests show up here as they are recorded and replayed."}
        </p>
        {!model && (
          <code className="mt-3 block rounded-md bg-muted px-3 py-2 text-left text-xs text-foreground">
            OPENAI_BASE_URL=
            {typeof window === "undefined"
              ? "http://<proxy>"
              : window.location.origin}
            /v1
          </code>
        )}
        <div className="mt-4 flex flex-wrap justify-center gap-2">
          {model && (
            <Button size="sm" variant="outline" onClick={() => setModel("")}>
              Clear model filter
            </Button>
          )}
          {range !== "90d" && (
            <Button size="sm" variant="outline" onClick={() => setRange("90d")}>
              Show last 90 days
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => openTraffic()}>
            Open traffic <ArrowUpRight className="size-3.5" />
          </Button>
        </div>
      </EmptyState>
    );
  } else {
    body = (
      <div
        aria-busy={stale}
        className={cn(
          "space-y-4 transition-opacity duration-200",
          stale && "pointer-events-none opacity-55",
        )}
      >
        {error && (
          <p
            role="status"
            className="flex items-center gap-2 text-xs text-muted-foreground"
          >
            <AlertCircle className="size-3.5" aria-hidden="true" /> Showing the
            last loaded data: {error}
          </p>
        )}
        <HeadlineStrip h={h} />
        <div className="grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
          <TrafficPanel
            insights={data}
            series={series}
            onOpenTraffic={() => openTraffic()}
          />
          <OutcomesPanel
            totals={data.totals}
            routes={data.routes}
            onOpenOutcome={(outcome) =>
              navigate({
                view: "traffic",
                outcome,
                ...(model ? { model } : {}),
              })
            }
          />
        </div>
        <TokensPanel insights={data} series={series} />
        <ModelsPanel
          models={data.models}
          selected={model}
          onSelect={toggleModel}
          onOpenTraffic={(m) => openTraffic(m)}
        />
        <LatencyPanel insights={data} />
        <div className="grid gap-4 lg:grid-cols-2">
          <TopRecordingsPanel
            recordings={data.top_recordings}
            onInspect={inspectRecording}
          />
          <TopThreadsPanel
            threads={data.top_threads}
            onOpen={(thread) => navigate({ view: "conversations", thread })}
          />
        </div>
      </div>
    );
  }

  return (
    <div className="min-w-0 space-y-4">
      {filters}
      {body}
    </div>
  );
}

function EmptyState({
  title,
  children,
}: {
  title: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center rounded-xl border bg-card px-5 py-14 text-center shadow-sm">
      <div className="mb-3 rounded-xl bg-muted p-3">
        <Inbox className="size-5 text-muted-foreground" aria-hidden="true" />
      </div>
      <p className="font-medium">{title}</p>
      <div className="mt-1 max-w-md text-sm text-muted-foreground">
        {children}
      </div>
    </div>
  );
}
