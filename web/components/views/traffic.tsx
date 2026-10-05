"use client";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity, Database, Pause, Play, Search, X } from "lucide-react";
import { Empty } from "@/components/common";
import { Button } from "@/components/ui/button";
import { ActivityStrip } from "@/components/traffic/activity-strip";
import {
  Chip,
  FacetSelect,
  SearchBox,
} from "@/components/traffic/filter-controls";
import { toneBg } from "@/components/traffic/tone";
import { TrafficTable } from "@/components/traffic/traffic-table";
import {
  FRESH_MS,
  useLiveHistory,
} from "@/components/traffic/use-live-history";
import type { History } from "@/lib/api";
import {
  emptyTrafficFilter,
  facet,
  filterTraffic,
  liveSplit,
  outcomeCounts,
  outcomeTone,
  rowModel,
  shortRoute,
  timingScale,
  TRAFFIC_ROWS,
  type TrafficFilter,
} from "@/lib/traffic";
import { cn } from "@/lib/utils";
import type { ViewProps } from "./types";

/** Outcomes a link's `outcome` selects; "error" also covers incomplete. */
const linkedOutcomes = (outcome?: string) =>
  !outcome ? [] : outcome === "error" ? ["error", "incomplete"] : [outcome];

export function TrafficView(props: ViewProps) {
  const {
    collectionId,
    refreshKey,
    filter: link,
    inspectRecording,
    openHistory,
    navigate,
  } = props;
  const live = useLiveHistory(collectionId, refreshKey);
  const [filter, setFilter] = useState<TrafficFilter>(() => ({
    ...emptyTrafficFilter,
    outcomes: linkedOutcomes(link.outcome),
    model: link.model ?? "",
    thread: link.thread ?? "",
  }));
  // A new link (e.g. from Overview) replaces the linked filters.
  useEffect(() => {
    setFilter((f) => ({
      ...f,
      outcomes: linkedOutcomes(link.outcome),
      model: link.model ?? "",
      thread: link.thread ?? "",
    }));
  }, [link.outcome, link.model, link.thread]);
  const patch = (p: Partial<TrafficFilter>) =>
    setFilter((f) => ({ ...f, ...p }));

  // Pausing freezes the list at the newest row shown; polling continues.
  const [pausedAt, setPausedAt] = useState<number | null>(null);
  const [flash, setFlash] = useState<ReadonlySet<number>>(new Set());
  const flashTimer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(flashTimer.current), []);
  useEffect(() => setPausedAt(null), [collectionId]);
  const { shown, waiting } = useMemo(
    () => liveSplit(live.rows, pausedAt),
    [live.rows, pausedAt],
  );
  const resume = () => {
    if (pausedAt === null) return;
    const ids = live.rows.filter((h) => h.id > pausedAt).map((h) => h.id);
    setPausedAt(null);
    setFlash(new Set(ids));
    window.clearTimeout(flashTimer.current);
    flashTimer.current = window.setTimeout(() => setFlash(new Set()), FRESH_MS);
  };
  const pause = () => setPausedAt(live.rows[0]?.id ?? 0);
  const fresh = useMemo(() => {
    if (pausedAt !== null) return flash;
    if (!flash.size) return live.fresh;
    return new Set([...live.fresh, ...flash]);
  }, [pausedAt, flash, live.fresh]);

  const visible = useMemo(() => filterTraffic(shown, filter), [shown, filter]);
  const counts = useMemo(() => outcomeCounts(shown, filter), [shown, filter]);
  const models = useMemo(() => facet(shown, rowModel), [shown]);
  const routes = useMemo(() => facet(shown, (h) => h.route), [shown]);
  const sources = useMemo(() => facet(shown, (h) => h.source), [shown]);
  const scale = useMemo(() => timingScale(visible), [visible]);
  const filtered =
    filter.outcomes.length > 0 ||
    !!(filter.model || filter.route || filter.source || filter.thread) ||
    !!filter.query.trim();

  const onOpen = useCallback(
    (h: History) =>
      h.recording_id ? inspectRecording(h.recording_id) : openHistory(h),
    [inspectRecording, openHistory],
  );
  const onThread = useCallback(
    (thread: string) => navigate({ view: "conversations", thread }),
    [navigate],
  );

  // Screen readers hear arrivals once per poll, not every row.
  const arrived = pausedAt === null ? live.fresh.size : 0;

  if (!collectionId)
    return (
      <Empty
        icon={Database}
        title="Choose a collection"
        body="Select a collection to watch its inference traffic."
      />
    );

  const toggleOutcome = (o: string) =>
    patch({
      outcomes: filter.outcomes.includes(o)
        ? filter.outcomes.filter((x) => x !== o)
        : [...filter.outcomes, o],
    });

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Traffic</h2>
        <LiveToggle
          paused={pausedAt !== null}
          waiting={waiting}
          onPause={pause}
          onResume={resume}
        />
        <p
          className="text-xs text-muted-foreground tabular-nums"
          aria-live="polite"
        >
          {live.loaded &&
            (filtered
              ? `${visible.length.toLocaleString()} of ${shown.length.toLocaleString()} recent requests`
              : `${shown.length.toLocaleString()} most recent request${shown.length === 1 ? "" : "s"}${shown.length >= TRAFFIC_ROWS ? " (newest " + TRAFFIC_ROWS + " kept)" : ""}`)}
          {arrived > 0 && (
            <span className="sr-only">
              {`, ${arrived} new request${arrived === 1 ? "" : "s"}`}
            </span>
          )}
        </p>
        {live.error && (
          <p role="status" className="text-xs text-destructive">
            Live updates failed: {live.error}
          </p>
        )}
      </div>

      {shown.length > 0 && <ActivityStrip rows={visible} now={live.now} />}

      <div className="space-y-2">
        <div
          role="group"
          aria-label="Filter by outcome"
          className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-0.5 [scrollbar-width:none] max-sm:pr-8 max-sm:[mask-image:linear-gradient(to_right,#000_85%,transparent)]"
        >
          <Chip
            pressed={filter.outcomes.length === 0}
            onClick={() => patch({ outcomes: [] })}
          >
            All
          </Chip>
          {counts.map(({ outcome, count }) => (
            <Chip
              key={outcome}
              pressed={filter.outcomes.includes(outcome)}
              onClick={() => toggleOutcome(outcome)}
              className={cn(
                count === 0 &&
                  !filter.outcomes.includes(outcome) &&
                  "text-muted-foreground opacity-70",
              )}
            >
              <span
                aria-hidden
                className={cn(
                  "size-2 rounded-full",
                  toneBg[outcomeTone(outcome)],
                )}
              />
              {outcome}
              <span className="tabular-nums opacity-70">
                {count.toLocaleString()}
              </span>
            </Chip>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <SearchBox
            label="Search requests"
            placeholder="Search message, model, key, detail…"
            value={filter.query}
            onChange={(query) => patch({ query })}
          />
          <FacetSelect
            label="Model"
            value={filter.model}
            options={models}
            onChange={(model) => patch({ model })}
          />
          <FacetSelect
            label="Route"
            value={filter.route}
            options={routes}
            format={shortRoute}
            onChange={(route) => patch({ route })}
          />
          <FacetSelect
            label="Source"
            value={filter.source}
            options={sources}
            onChange={(source) => patch({ source })}
          />
          {filter.thread && (
            <Chip
              pressed
              label={`Remove conversation filter ${filter.thread}`}
              onClick={() => patch({ thread: "" })}
            >
              Conversation {filter.thread.slice(0, 8)}
              <X aria-hidden className="size-3" />
            </Chip>
          )}
          {filtered && (
            <Button
              variant="ghost"
              size="sm"
              className="h-8 text-xs"
              onClick={() => setFilter({ ...emptyTrafficFilter })}
            >
              Clear filters
            </Button>
          )}
        </div>
      </div>

      {!live.loaded ? (
        <LoadingRows />
      ) : shown.length === 0 ? (
        <div className="rounded-xl border bg-card">
          <Empty
            icon={Activity}
            title={live.error ? "Traffic unavailable" : "No traffic yet"}
            body={
              live.error ||
              "Requests sent through the proxy for this collection appear here as they happen."
            }
          />
        </div>
      ) : visible.length === 0 ? (
        <div className="rounded-xl border bg-card">
          <Empty
            icon={Search}
            title="No matching requests"
            body="Search matches the user message, model, key, route and detail. Try clearing a filter."
          />
        </div>
      ) : (
        <TrafficTable
          rows={visible}
          fresh={fresh}
          now={live.now}
          scale={scale}
          onOpen={onOpen}
          onDetails={openHistory}
          onThread={onThread}
        />
      )}
    </div>
  );
}

function LiveToggle({
  paused,
  waiting,
  onPause,
  onResume,
}: {
  paused: boolean;
  waiting: number;
  onPause: () => void;
  onResume: () => void;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <button
        type="button"
        aria-pressed={!paused}
        onClick={paused ? onResume : onPause}
        className={cn(
          "inline-flex h-7 items-center gap-2 rounded-full border px-2.5 text-xs font-medium outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring",
          paused
            ? "bg-card text-muted-foreground hover:bg-muted"
            : "border-hit/40 bg-hit/10 text-foreground hover:bg-hit/15",
        )}
        title={paused ? "Resume live updates" : "Pause live updates"}
      >
        {paused ? (
          <Pause aria-hidden className="size-3" />
        ) : (
          <span aria-hidden className="relative flex size-2">
            <span className="absolute inline-flex size-full rounded-full bg-hit opacity-60 motion-safe:animate-ping" />
            <span className="relative inline-flex size-2 rounded-full bg-hit" />
          </span>
        )}
        {paused ? "Paused" : "Live"}
      </button>
      {paused && waiting > 0 && (
        <button
          type="button"
          onClick={onResume}
          className="inline-flex h-7 items-center gap-1.5 rounded-full bg-primary px-2.5 text-xs font-medium text-primary-foreground outline-none hover:bg-primary/90 focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Play aria-hidden className="size-3" />
          {waiting.toLocaleString()} new
        </button>
      )}
    </div>
  );
}

function LoadingRows() {
  return (
    <div
      className="space-y-px overflow-hidden rounded-xl border bg-card"
      aria-busy="true"
      aria-label="Loading traffic"
    >
      {Array.from({ length: 8 }, (_, i) => (
        <div
          key={i}
          className="flex items-center gap-3 border-b px-3 py-3 last:border-0"
        >
          <span className="h-4 w-14 rounded-full bg-muted motion-safe:animate-pulse" />
          <span
            className="h-3 rounded bg-muted motion-safe:animate-pulse"
            style={{ width: `${40 + ((i * 37) % 45)}%` }}
          />
        </div>
      ))}
    </div>
  );
}
