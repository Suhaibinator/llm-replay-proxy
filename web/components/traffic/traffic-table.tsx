"use client";
import { memo, type KeyboardEvent, type MouseEvent } from "react";
import { ChevronRight, FileText, MessagesSquare } from "lucide-react";
import { Outcome } from "@/components/common";
import { count, shortKey, type History } from "@/lib/api";
import { formatMs, relativeTime, shortModel, shortRoute } from "@/lib/traffic";
import { cn } from "@/lib/utils";

const full = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});
const clock = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});
const dayClock = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
/** Time of day for today's rows, date and minute for older ones. */
function compact(iso: string, now: number) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toDateString() === new Date(now).toDateString()
    ? clock.format(d)
    : dayClock.format(d);
}
const absolute = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : full.format(d);
};

type Actions = {
  onOpen: (row: History) => void;
  onDetails: (row: History) => void;
  onThread: (thread: string) => void;
};

/** Moves focus between rows' primary buttons with the arrow keys. */
function rowKeys(event: KeyboardEvent<HTMLTableSectionElement>) {
  if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
  const buttons = [
    ...event.currentTarget.querySelectorAll<HTMLButtonElement>(
      "button[data-row-primary]",
    ),
  ];
  const row = (event.target as HTMLElement).closest("tr");
  const at = buttons.findIndex((b) => row?.contains(b));
  if (at < 0) return;
  const next =
    event.key === "Home"
      ? 0
      : event.key === "End"
        ? buttons.length - 1
        : at + (event.key === "ArrowDown" ? 1 : -1);
  if (next < 0 || next >= buttons.length) return;
  event.preventDefault();
  buttons[next].focus();
}

export function TrafficTable({
  rows,
  fresh,
  now,
  scale,
  ...actions
}: Actions & {
  rows: History[];
  fresh: ReadonlySet<number>;
  now: number;
  scale: number;
}) {
  return (
    <div className="@container overflow-hidden rounded-xl border bg-card">
      <table className="w-full table-fixed text-left text-sm">
        <caption className="sr-only">
          Requests, newest first. Arrow keys move between rows; Enter opens one.
        </caption>
        <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
          <tr>
            <th
              scope="col"
              className="hidden w-28 py-2 pl-3 pr-2 font-medium @2xl:table-cell"
            >
              Outcome
            </th>
            <th scope="col" className="px-3 py-2 font-medium @2xl:pl-2">
              Request
            </th>
            <th
              scope="col"
              className="hidden w-40 px-2 py-2 font-medium @4xl:table-cell"
            >
              Model
            </th>
            <th
              scope="col"
              className="hidden w-36 px-2 py-2 font-medium @3xl:table-cell"
            >
              <span title="Bar length is the total duration; the dark part is the time to the first event. Scaled to the visible rows.">
                Duration
                <span className="font-normal"> · first event</span>
              </span>
            </th>
            <th
              scope="col"
              className="hidden w-28 px-2 py-2 font-medium @xl:table-cell"
            >
              Time
            </th>
            <th scope="col" className="w-[4.75rem] py-2 pr-2 @md:w-24">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody onKeyDown={rowKeys}>
          {rows.map((h) => (
            <Row
              key={h.id}
              h={h}
              fresh={fresh.has(h.id)}
              now={now}
              scale={scale}
              {...actions}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

const failed = (outcome: string) =>
  outcome === "error" || outcome === "incomplete";

const Row = memo(function Row({
  h,
  fresh,
  now,
  scale,
  onOpen,
  onDetails,
  onThread,
}: Actions & { h: History; fresh: boolean; now: number; scale: number }) {
  const r = h.request;
  const model = r?.model || "";
  const meta = [
    shortRoute(h.route),
    h.source,
    r && count(r.items, "item"),
    r?.tool_calls ? count(r.tool_calls, "tool call") : "",
    r?.images ? count(r.images, "image") : "",
  ].filter(Boolean);
  const click = (event: MouseEvent<HTMLTableRowElement>) => {
    if ((event.target as HTMLElement).closest("button,a")) return;
    if (window.getSelection()?.toString()) return;
    onOpen(h);
  };
  return (
    <tr
      onClick={click}
      data-fresh={fresh || undefined}
      className={cn(
        "group cursor-pointer border-b transition-[background-color,box-shadow] duration-[1600ms] last:border-0 hover:bg-muted/50 focus-within:bg-muted/50",
        fresh &&
          "bg-primary/[0.07] shadow-[inset_3px_0_0_var(--primary)] duration-0",
      )}
    >
      <td className="hidden py-2 pl-3 pr-2 align-top @2xl:table-cell">
        <Outcome value={h.outcome} />
      </td>
      <td className="min-w-0 px-3 py-2 align-top @2xl:pl-2">
        <button
          type="button"
          data-row-primary
          onClick={() => onOpen(h)}
          className="block w-full min-w-0 rounded-sm text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <span
            className={cn(
              "block truncate font-medium leading-5",
              !r?.preview && "font-normal italic text-muted-foreground",
            )}
            title={r?.preview || undefined}
          >
            {r?.preview || (r ? "No user message" : "No request body")}
          </span>
          <span className="sr-only">
            {`, ${h.outcome}`}
            {h.recording_id ? ", inspect recording" : ", open details"}
          </span>
        </button>
        <p className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs leading-4 text-muted-foreground @2xl:flex-nowrap @2xl:overflow-hidden [&>*]:shrink-0">
          <span className="@2xl:hidden">
            <Outcome value={h.outcome} />
          </span>
          {model && (
            <span
              className="max-w-full truncate font-medium text-foreground @4xl:hidden"
              title={model}
            >
              {shortModel(model)}
            </span>
          )}
          {meta.map((m, i) => (
            <span key={i} className="whitespace-nowrap">
              {m}
            </span>
          ))}
          <span
            className="hidden font-mono text-[11px] @2xl:inline"
            title={h.key}
          >
            {shortKey(h.key)}
          </span>
          {h.duration_ms !== null && (
            <span className="whitespace-nowrap tabular-nums @3xl:hidden">
              {formatMs(h.duration_ms)}
            </span>
          )}
          <time
            dateTime={h.created_at}
            title={absolute(h.created_at)}
            className="whitespace-nowrap @xl:hidden"
          >
            {relativeTime(h.created_at, now)}
          </time>
          {h.detail && (
            <span
              className={cn(
                "min-w-0 basis-full truncate @2xl:basis-auto @2xl:!shrink",
                failed(h.outcome) ? "text-destructive" : "italic",
              )}
              title={h.detail}
            >
              {h.detail}
            </span>
          )}
        </p>
      </td>
      <td className="hidden px-2 py-2 align-top @4xl:table-cell">
        <span
          className="block truncate text-xs font-medium leading-5"
          title={model}
        >
          {model ? (
            shortModel(model)
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </span>
      </td>
      <td className="hidden px-2 py-2 align-top @3xl:table-cell">
        <Timing
          duration={h.duration_ms}
          first={h.first_event_ms}
          scale={scale}
        />
      </td>
      <td className="hidden px-2 py-2 align-top text-xs leading-5 @xl:table-cell">
        <time
          dateTime={h.created_at}
          title={absolute(h.created_at)}
          className="block whitespace-nowrap tabular-nums"
        >
          {relativeTime(h.created_at, now)}
        </time>
        <span className="block whitespace-nowrap leading-4 tabular-nums text-muted-foreground">
          {compact(h.created_at, now)}
        </span>
      </td>
      <td className="py-1 pr-2 align-top">
        <div className="flex items-center justify-end">
          {r?.thread && (
            <IconButton
              label="Open conversation"
              onClick={() => onThread(r.thread)}
            >
              <MessagesSquare className="size-4" />
            </IconButton>
          )}
          <IconButton label="Request details" onClick={() => onDetails(h)}>
            <FileText className="size-4" />
          </IconButton>
          <ChevronRight
            aria-hidden
            className="ml-0.5 hidden size-4 shrink-0 text-muted-foreground opacity-50 group-hover:opacity-100 @md:block"
          />
        </div>
      </td>
    </tr>
  );
});

function IconButton({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="inline-flex size-8 shrink-0 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
    >
      {children}
    </button>
  );
}

/**
 * One bar per row: its length is the total duration and the darker leading
 * part the time to first event, scaled to the visible rows. A row slower than
 * the scale draws a full bar with a clipped end.
 */
function Timing({
  duration,
  first,
  scale,
}: {
  duration: number | null;
  first: number | null;
  scale: number;
}) {
  if (duration === null && first === null)
    return <span className="text-xs leading-5 text-muted-foreground">—</span>;
  const total = duration ?? first ?? 0;
  const pct = (v: number) =>
    scale <= 0 ? 0 : Math.min(100, Math.max(2, (v / scale) * 100));
  const clipped = scale > 0 && total > scale;
  return (
    <div className="text-xs tabular-nums">
      <div className="flex items-baseline justify-between gap-2 leading-5">
        <span className="font-medium">{formatMs(duration)}</span>
        {first !== null && (
          <span className="text-muted-foreground" title="Time to first event">
            {formatMs(first)}
          </span>
        )}
      </div>
      <div
        aria-hidden
        className="relative mt-0.5 h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <span
          className={cn(
            "absolute inset-y-0 left-0 rounded-full bg-muted-foreground/35",
            clipped && "rounded-r-none",
          )}
          style={{ width: `${pct(total)}%` }}
        />
        {first !== null && (
          <span
            className="absolute inset-y-0 left-0 rounded-full bg-muted-foreground"
            style={{ width: `${pct(first)}%` }}
          />
        )}
        {clipped && (
          <span className="absolute inset-y-0 right-0 w-1 bg-[repeating-linear-gradient(90deg,var(--card)_0_1px,transparent_1px_2px)]" />
        )}
      </div>
    </div>
  );
}
