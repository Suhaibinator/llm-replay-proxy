"use client";
import { ChevronRight } from "lucide-react";
import { date, type ThreadSummary } from "@/lib/api";
import {
  compactNumber,
  describeCounts,
  relativeTime,
  tokenTotal,
} from "@/lib/threads";
import { cn } from "@/lib/utils";
import { OutcomeDot, OutcomeStrip } from "./outcome";

const COLS =
  "lg:grid-cols-[minmax(0,1fr)_minmax(9rem,13rem)_5rem_5.5rem_6rem_6.5rem_1rem]";

export function ThreadList({
  threads,
  onOpen,
}: {
  threads: ThreadSummary[];
  onOpen: (thread: string) => void;
}) {
  return (
    <div className="overflow-hidden rounded-xl border bg-card shadow-sm">
      <div
        aria-hidden="true"
        className={cn(
          "hidden gap-4 border-b bg-muted/40 px-4 py-2 text-xs font-medium text-muted-foreground lg:grid",
          COLS,
        )}
      >
        <span>Conversation</span>
        <span>Outcomes</span>
        <span className="text-right">Requests</span>
        <span className="text-right">Grew to</span>
        <span className="text-right">Upstream</span>
        <span className="text-right">Last active</span>
        <span />
      </div>
      <ul aria-label="Conversations" className="divide-y">
        {threads.map((t) => (
          <li key={t.thread}>
            <ThreadRow t={t} onOpen={() => onOpen(t.thread)} />
          </li>
        ))}
      </ul>
    </div>
  );
}

function ThreadRow({ t, onOpen }: { t: ThreadSummary; onOpen: () => void }) {
  const tokens = tokenTotal(t.upstream_tokens);
  const continued = t.latest && t.latest !== t.opening;
  return (
    <button
      type="button"
      onClick={onOpen}
      data-thread={t.thread}
      className={cn(
        "group grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 px-4 py-3 text-left outline-none transition-colors hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
        COLS,
      )}
    >
      <span className="min-w-0 space-y-0.5">
        <span className="block truncate font-medium">
          {t.opening || (
            <span className="italic text-muted-foreground">No user text</span>
          )}
        </span>
        {continued ? (
          <span className="flex min-w-0 items-center gap-1.5 text-sm text-muted-foreground">
            <span className="shrink-0 text-xs">Latest</span>
            <span className="truncate">{t.latest}</span>
          </span>
        ) : null}
        <span className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
          {t.model && (
            <span className="font-medium text-foreground/80">{t.model}</span>
          )}
          <span className="font-mono">{t.route}</span>
        </span>
      </span>

      {/* Mobile: compact facts on the right. */}
      <span className="flex flex-col items-end gap-1 text-xs text-muted-foreground lg:hidden">
        <span>{relativeTime(t.last_at)}</span>
        <ChevronRight className="size-4" aria-hidden="true" />
      </span>

      <span className="col-span-2 min-w-0 space-y-1.5 lg:col-span-1">
        <OutcomeStrip counts={t} />
        <span className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-xs text-muted-foreground">
          {t.misses > 0 && (
            <span className="inline-flex items-center gap-1 font-medium text-foreground">
              <OutcomeDot kind="miss" />
              {t.misses} missed
            </span>
          )}
          {t.errors + t.interrupted > 0 && (
            <span className="inline-flex items-center gap-1 text-foreground">
              <OutcomeDot kind="error" />
              {t.errors + t.interrupted} failed
            </span>
          )}
          <span className="sr-only">{describeCounts(t)}</span>
          <span aria-hidden="true" className="lg:hidden">
            {t.requests} requests · grew to {t.max_items} items
            {tokens ? ` · ${compactNumber(tokens)} upstream tokens` : ""}
          </span>
          {t.misses + t.errors + t.interrupted === 0 && (
            <span aria-hidden="true" className="hidden lg:inline">
              {t.hits === t.requests ? "all replayed" : `${t.hits} replayed`}
            </span>
          )}
        </span>
      </span>
      <span className="hidden text-right tabular-nums lg:block">
        {t.requests.toLocaleString()}
      </span>
      <span className="hidden text-right tabular-nums lg:block">
        {t.max_items.toLocaleString()}
        <span className="text-xs text-muted-foreground"> items</span>
      </span>
      <span
        className="hidden text-right tabular-nums lg:block"
        title={`${t.upstream_tokens.input.toLocaleString()} input, ${t.upstream_tokens.output.toLocaleString()} output tokens`}
      >
        {tokens ? (
          compactNumber(tokens)
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </span>
      <span
        className="hidden text-right text-sm text-muted-foreground lg:block"
        title={date(t.last_at)}
      >
        {relativeTime(t.last_at)}
      </span>
      <ChevronRight
        className="hidden size-4 text-muted-foreground transition-transform group-hover:translate-x-0.5 lg:block"
        aria-hidden="true"
      />
    </button>
  );
}
