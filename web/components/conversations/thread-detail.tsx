"use client";
import { useMemo, useState } from "react";
import {
  ArrowLeft,
  Database,
  FileSearch,
  LoaderCircle,
  MessageSquareText,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/transcript";
import { date, type ThreadDetail } from "@/lib/api";
import {
  OUTCOME_LABEL,
  compactNumber,
  formatMs,
  relativeTime,
  shapeTimeline,
  spanLabel,
  tokenTotal,
  type TimelineTurn,
} from "@/lib/threads";
import { cn } from "@/lib/utils";
import { AccentBar, OutcomePill, isTrouble } from "./outcome";
import { TimelineChart } from "./timeline";

export function ThreadDetailView({
  detail,
  pending,
  onBack,
  onOpenTurn,
  inspectRecording,
}: {
  detail: ThreadDetail;
  /** history_id currently being opened. */
  pending: number | null;
  onBack: () => void;
  onOpenTurn: (t: TimelineTurn) => void;
  inspectRecording: (recordingId: number) => void;
}) {
  const timeline = useMemo(() => shapeTimeline(detail.turns), [detail.turns]);
  const n = timeline.turns.length;
  // Start on the first miss (the likeliest reason to be here), else the latest.
  const initial = timeline.firstMiss ? timeline.firstMiss - 1 : n - 1;
  const [active, setActive] = useState(Math.max(0, initial));
  const [forThread, setForThread] = useState(detail.thread);
  if (forThread !== detail.thread) {
    setForThread(detail.thread);
    setActive(Math.max(0, initial));
  }
  const current = timeline.turns[Math.min(active, n - 1)];
  const served = detail.hits + detail.recorded;
  const tokens = detail.upstream_tokens;

  return (
    <div className="space-y-5">
      <div className="space-y-3">
        <Button variant="ghost" size="sm" className="-ml-2" onClick={onBack}>
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          All conversations
        </Button>
        <div className="space-y-2">
          <h3 className="line-clamp-2 max-w-[80ch] text-lg font-semibold leading-snug tracking-tight sm:text-xl">
            {detail.opening || (
              <span className="text-muted-foreground">
                Conversation without user text
              </span>
            )}
          </h3>
          <div className="flex flex-wrap items-center gap-1.5">
            {detail.model && (
              <Chip className="font-medium">{detail.model}</Chip>
            )}
            <Chip className="font-mono">{detail.route}</Chip>
            <Chip>
              {date(detail.first_at)} → {date(detail.last_at)} (
              {spanLabel(detail.first_at, detail.last_at)})
            </Chip>
            <Chip className="font-mono text-muted-foreground">
              thread {detail.thread.slice(0, 10)}
            </Chip>
          </div>
        </div>
      </div>

      <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border lg:grid-cols-4">
        <Stat
          label="Requests"
          value={detail.requests.toLocaleString()}
          note={`${served.toLocaleString()} served (${detail.hits} replayed, ${detail.recorded} recorded)`}
        />
        <Stat
          label="Grew to"
          value={`${detail.max_items.toLocaleString()} items`}
          note={
            n > 1
              ? `from ${timeline.turns[0].items} on the first request`
              : "a single request"
          }
        />
        <Stat
          label="Upstream tokens"
          value={compactNumber(tokenTotal(tokens))}
          note={`${compactNumber(tokens.input)} in · ${compactNumber(tokens.output)} out${tokens.reasoning ? ` · ${compactNumber(tokens.reasoning)} reasoning` : ""}`}
        />
        <Stat
          label="Median duration"
          value={formatMs(timeline.medianUpstream ?? timeline.medianReplay)}
          note={
            timeline.medianUpstream !== null && timeline.medianReplay !== null
              ? `upstream · replay ${formatMs(timeline.medianReplay)}`
              : timeline.medianUpstream !== null
                ? "upstream"
                : timeline.medianReplay !== null
                  ? "replay"
                  : "not measured"
          }
        />
      </dl>

      {timeline.firstMiss !== null && (
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-miss/40 bg-miss/10 px-3 py-2 text-sm">
          <span className="font-medium">
            Replay broke at turn {timeline.firstMiss}.
          </span>
          <span className="text-muted-foreground">
            Open it to see the closest recording and what differs.
          </span>
          <Button
            size="sm"
            variant="outline"
            className="ml-auto bg-background"
            onClick={() => onOpenTurn(timeline.turns[timeline.firstMiss! - 1])}
          >
            <FileSearch className="size-3.5" aria-hidden="true" />
            Why didn&apos;t turn {timeline.firstMiss} replay?
          </Button>
        </div>
      )}

      <section
        aria-labelledby="timeline-heading"
        className="space-y-3 rounded-xl border bg-card p-4 shadow-sm sm:p-5"
      >
        <h4 id="timeline-heading" className="text-sm font-semibold">
          Turn by turn
        </h4>
        {n > 0 ? (
          <>
            <TimelineChart
              timeline={timeline}
              active={active}
              onActive={setActive}
              onOpen={(i) => onOpenTurn(timeline.turns[i])}
            />
            {current && (
              <TurnInspector
                t={current}
                total={n}
                pending={pending === current.turn.history_id}
                onOpen={() => onOpenTurn(current)}
                inspectRecording={inspectRecording}
              />
            )}
          </>
        ) : (
          <p className="text-sm text-muted-foreground">No requests.</p>
        )}
      </section>

      <TurnTable
        turns={timeline.turns}
        active={active}
        pending={pending}
        onActive={setActive}
        onOpen={onOpenTurn}
      />
    </div>
  );
}

function Stat({
  label,
  value,
  note,
}: {
  label: string;
  value: string;
  note: string;
}) {
  return (
    <div className="min-w-0 bg-card px-4 py-3">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-lg font-semibold tabular-nums tracking-tight">
        {value}
      </dd>
      <dd className="text-xs text-muted-foreground">{note}</dd>
    </div>
  );
}

function TurnInspector({
  t,
  total,
  pending,
  onOpen,
  inspectRecording,
}: {
  t: TimelineTurn;
  total: number;
  pending: boolean;
  onOpen: () => void;
  inspectRecording: (id: number) => void;
}) {
  const facts: [string, string][] = [
    ["Items", `${t.items}${t.n > 1 ? ` (+${t.added})` : ""}`],
    ["Duration", formatMs(t.duration)],
    ["First event", formatMs(t.firstEvent)],
    [
      "Input tokens",
      t.input === null
        ? "—"
        : `${compactNumber(t.input)}${t.cachedInput ? ` (${compactNumber(t.cachedInput)} cached)` : ""}`,
    ],
    [
      "Output tokens",
      t.output === null
        ? "—"
        : `${compactNumber(t.output)}${t.reasoning ? ` (${compactNumber(t.reasoning)} reasoning)` : ""}`,
    ],
  ];
  return (
    <div className="relative grid gap-4 overflow-hidden rounded-lg border bg-background p-3 pl-4 sm:p-4 sm:pl-5 lg:grid-cols-[minmax(0,1fr)_auto]">
      <AccentBar kind={t.kind} />
      <div className="min-w-0 space-y-2">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-semibold">
            Turn {t.n}
            <span className="font-normal text-muted-foreground">
              {" "}
              of {total}
            </span>
          </span>
          <OutcomePill outcome={t.turn.outcome} />
          {t.retry && <Chip>retry</Chip>}
          <span className="text-xs text-muted-foreground">
            {date(t.turn.created_at)}
          </span>
        </div>
        <p className="line-clamp-3 max-w-[90ch] text-sm leading-relaxed">
          {t.turn.preview || (
            <span className="italic text-muted-foreground">
              No user text in this turn (tool results only)
            </span>
          )}
        </p>
        {t.turn.detail && isTrouble(t.kind) && (
          <p className="break-words font-mono text-xs text-muted-foreground">
            {t.turn.detail}
          </p>
        )}
        <dl className="flex flex-wrap gap-x-5 gap-y-1 text-xs">
          {facts.map(([k, v]) => (
            <div key={k} className="flex gap-1.5">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="font-medium tabular-nums">{v}</dd>
            </div>
          ))}
        </dl>
      </div>
      <div className="flex flex-wrap items-start gap-2 lg:flex-col lg:items-stretch">
        <Button size="sm" onClick={onOpen} disabled={pending}>
          {pending ? (
            <LoaderCircle
              className="size-3.5 animate-spin"
              aria-hidden="true"
            />
          ) : t.kind === "miss" ? (
            <FileSearch className="size-3.5" aria-hidden="true" />
          ) : (
            <MessageSquareText className="size-3.5" aria-hidden="true" />
          )}
          {t.kind === "miss" ? "Why didn't this replay?" : "Open request"}
        </Button>
        {t.turn.recording_id > 0 && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => inspectRecording(t.turn.recording_id)}
          >
            <Database className="size-3.5" aria-hidden="true" />
            Recording #{t.turn.recording_id}
          </Button>
        )}
      </div>
    </div>
  );
}

function TurnTable({
  turns,
  active,
  pending,
  onActive,
  onOpen,
}: {
  turns: TimelineTurn[];
  active: number;
  pending: number | null;
  onActive: (i: number) => void;
  onOpen: (t: TimelineTurn) => void;
}) {
  return (
    <section aria-labelledby="turns-heading" className="space-y-2">
      <h4 id="turns-heading" className="text-sm font-semibold">
        All turns
      </h4>
      <div className="overflow-x-auto rounded-xl border bg-card shadow-sm">
        <table className="w-full text-left text-sm">
          <caption className="sr-only">
            Every request in this conversation, oldest first
          </caption>
          <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
            <tr>
              <th scope="col" className="w-px py-2 pl-4 pr-2 font-medium">
                #
              </th>
              <th scope="col" className="w-px px-2 py-2 font-medium">
                Outcome
              </th>
              <th scope="col" className="w-full px-2 py-2 font-medium">
                User message
              </th>
              <th
                scope="col"
                className="w-px whitespace-nowrap px-3 py-2 text-right font-medium"
              >
                Items
              </th>
              <th
                scope="col"
                className="w-px whitespace-nowrap px-3 py-2 text-right font-medium"
              >
                Duration
              </th>
              <th
                scope="col"
                className="hidden w-px whitespace-nowrap px-3 py-2 text-right font-medium md:table-cell"
              >
                First event
              </th>
              <th
                scope="col"
                className="hidden w-px whitespace-nowrap px-3 py-2 text-right font-medium md:table-cell"
              >
                Tokens in / out
              </th>
              <th
                scope="col"
                className="hidden w-px whitespace-nowrap py-2 pl-3 pr-4 text-right font-medium lg:table-cell"
              >
                Time
              </th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {turns.map((t, i) => (
              <tr
                key={t.turn.history_id}
                onMouseEnter={() => onActive(i)}
                className={cn(
                  "cursor-pointer transition-colors hover:bg-muted/50",
                  i === active && "bg-muted/60",
                )}
                onClick={() => onOpen(t)}
              >
                <td className="py-2 pl-4 pr-2 tabular-nums text-muted-foreground">
                  {t.n}
                </td>
                <td className="px-2 py-2">
                  <OutcomePill outcome={t.turn.outcome} compact />
                </td>
                <td className="max-w-0 px-2 py-2">
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation();
                      onOpen(t);
                    }}
                    onFocus={() => onActive(i)}
                    aria-label={`Open turn ${t.n}: ${OUTCOME_LABEL[t.kind]}. ${t.turn.preview || "No user text"}`}
                    className="flex w-full min-w-0 items-center gap-2 rounded-sm text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {pending === t.turn.history_id && (
                      <LoaderCircle
                        className="size-3.5 shrink-0 animate-spin"
                        aria-hidden="true"
                      />
                    )}
                    <span
                      className={cn(
                        "truncate",
                        !t.turn.preview && "italic text-muted-foreground",
                      )}
                    >
                      {t.turn.preview || "tool results only"}
                    </span>
                  </button>
                </td>
                <td className="whitespace-nowrap px-2 py-2 text-right tabular-nums">
                  {t.items}
                  {t.n > 1 && (
                    <span className="ml-1 inline-block w-9 text-left text-xs text-muted-foreground">
                      {t.retry ? "retry" : `+${t.added}`}
                    </span>
                  )}
                </td>
                <td className="whitespace-nowrap px-2 py-2 text-right tabular-nums">
                  {formatMs(t.duration)}
                </td>
                <td className="hidden whitespace-nowrap px-2 py-2 text-right tabular-nums md:table-cell">
                  {formatMs(t.firstEvent)}
                </td>
                <td className="hidden whitespace-nowrap px-2 py-2 text-right tabular-nums md:table-cell">
                  {t.input === null && t.output === null
                    ? "—"
                    : `${compactNumber(t.input)} / ${compactNumber(t.output)}`}
                </td>
                <td
                  className="hidden whitespace-nowrap py-2 pl-2 pr-4 text-right text-xs text-muted-foreground lg:table-cell"
                  title={date(t.turn.created_at)}
                >
                  {relativeTime(t.turn.created_at)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
