"use client";
import { memo, type KeyboardEvent, type MouseEvent } from "react";
import { ArrowDown, ArrowUp, ChevronRight } from "lucide-react";
import { count, shortKey, type RecordingRow } from "@/lib/api";
import { formatBytes } from "@/lib/request-viewer";
import {
  formatCost,
  formatTokens,
  isEdited,
  recordingModel,
  type RecordingSort,
  type RecordingSortKey,
} from "@/lib/recordings";
import { relativeTime, shortModel, shortRoute } from "@/lib/traffic";
import { cn } from "@/lib/utils";

const exact = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});
const absolute = (iso: string) => {
  const d = new Date(iso);
  return !iso || Number.isNaN(d.getTime()) ? "" : exact.format(d);
};

/** Response outcomes that mean the answer finished normally. */
const normalEnd =
  /^(completed|stop|end_turn|tool_calls|tool_use|stop_sequence)$/;

type Column = {
  key: RecordingSortKey;
  label: string;
  /** Container width from which the column shows. */
  show: string;
  numeric?: boolean;
  width: string;
  title?: string;
};
const columns: Column[] = [
  { key: "model", label: "Model", show: "@4xl:table-cell", width: "w-36" },
  {
    key: "input",
    label: "In",
    show: "@3xl:table-cell",
    width: "w-24",
    numeric: true,
    title: "Input tokens (cached below)",
  },
  {
    key: "output",
    label: "Out",
    show: "@3xl:table-cell",
    width: "w-16",
    numeric: true,
    title: "Output tokens",
  },
  {
    key: "reasoning",
    label: "Reason",
    show: "@5xl:table-cell",
    width: "w-16",
    numeric: true,
    title: "Reasoning tokens",
  },
  {
    key: "cost",
    label: "Cost",
    show: "@4xl:table-cell",
    width: "w-20",
    numeric: true,
    title: "Provider-reported cost of the recorded response",
  },
  {
    key: "replays",
    label: "Replays",
    show: "@xl:table-cell",
    width: "w-20",
    numeric: true,
  },
  {
    key: "revisions",
    label: "Revs",
    show: "@5xl:table-cell",
    width: "w-14",
    numeric: true,
    title: "Revisions",
  },
  {
    key: "size",
    label: "Size",
    show: "@5xl:table-cell",
    width: "w-20",
    numeric: true,
    title: "Request size",
  },
  {
    key: "updated",
    label: "Updated",
    show: "@2xl:table-cell",
    width: "w-24",
    numeric: true,
  },
];

function rowKeys(event: KeyboardEvent<HTMLTableSectionElement>) {
  if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
  const buttons = [
    ...event.currentTarget.querySelectorAll<HTMLButtonElement>(
      "button[data-row-primary]",
    ),
  ];
  const at = buttons.indexOf(event.target as HTMLButtonElement);
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

export function RecordingsTable({
  rows,
  sort,
  onSort,
  onInspect,
  now,
}: {
  rows: RecordingRow[];
  sort: RecordingSort;
  onSort: (key: RecordingSortKey) => void;
  onInspect: (id: number) => void;
  now: number;
}) {
  return (
    <div className="@container overflow-hidden rounded-xl border bg-card">
      <table className="w-full table-fixed text-left text-sm">
        <caption className="sr-only">
          Recordings. Column headers sort; arrow keys move between rows.
        </caption>
        <thead className="border-b bg-muted/40 text-xs text-muted-foreground">
          <tr>
            <SortHeader
              column={{ key: "request", label: "Request", show: "", width: "" }}
              sort={sort}
              onSort={onSort}
            />
            {columns.map((c) => (
              <SortHeader key={c.key} column={c} sort={sort} onSort={onSort} />
            ))}
            <th scope="col" className="w-8">
              <span className="sr-only">Inspect</span>
            </th>
          </tr>
        </thead>
        <tbody onKeyDown={rowKeys}>
          {rows.map((r) => (
            <Row key={r.id} r={r} onInspect={onInspect} now={now} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SortHeader({
  column: c,
  sort,
  onSort,
}: {
  column: Column;
  sort: RecordingSort;
  onSort: (key: RecordingSortKey) => void;
}) {
  const active = sort.key === c.key;
  const Icon = sort.dir === "asc" ? ArrowUp : ArrowDown;
  return (
    <th
      scope="col"
      aria-sort={
        active ? (sort.dir === "asc" ? "ascending" : "descending") : undefined
      }
      className={cn(
        "px-2 py-1.5 font-medium first:pl-3",
        c.show && `hidden ${c.show}`,
        c.width,
        c.numeric && "text-right",
      )}
    >
      <button
        type="button"
        onClick={() => onSort(c.key)}
        title={c.title}
        className={cn(
          "inline-flex items-center gap-1 rounded px-1 py-0.5 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring",
          c.numeric && "-mr-1 flex-row-reverse",
          !c.numeric && "-ml-1",
          active && "text-foreground",
        )}
      >
        {c.label}
        <Icon
          aria-hidden
          className={cn("size-3", active ? "opacity-100" : "opacity-0")}
        />
      </button>
    </th>
  );
}

const Row = memo(function Row({
  r,
  onInspect,
  now,
}: {
  r: RecordingRow;
  onInspect: (id: number) => void;
  now: number;
}) {
  const q = r.request,
    s = r.response,
    u = s?.usage;
  const model = recordingModel(r);
  const edited = isEdited(r);
  const updated = r.updated_at || r.created_at;
  const click = (event: MouseEvent<HTMLTableRowElement>) => {
    if ((event.target as HTMLElement).closest("button,a")) return;
    if (window.getSelection()?.toString()) return;
    onInspect(r.id);
  };
  const num =
    "whitespace-nowrap px-2 py-2 text-right align-top text-xs tabular-nums";
  return (
    <tr
      onClick={click}
      className="group cursor-pointer border-b last:border-0 hover:bg-muted/50 focus-within:bg-muted/50"
    >
      <td className="min-w-0 py-2 pl-3 pr-2 align-top">
        <button
          type="button"
          data-row-primary
          onClick={() => onInspect(r.id)}
          className="block w-full min-w-0 rounded-sm text-left outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <span
            className={cn(
              "block truncate font-medium",
              !q?.preview && "font-normal italic text-muted-foreground",
            )}
            title={q?.preview || undefined}
          >
            {q?.preview || "No user message"}
          </span>
        </button>
        <p className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs leading-4 text-muted-foreground @3xl:flex-nowrap @3xl:overflow-hidden [&>*]:shrink-0">
          {model && (
            <span className="max-w-full truncate font-medium text-foreground @4xl:hidden">
              {shortModel(model)}
            </span>
          )}
          <span>{shortRoute(r.route)}</span>
          <span>{r.streaming ? "stream" : "json"}</span>
          {q && <span>{count(q.items, "item")}</span>}
          {!!q?.tool_calls && <span>{count(q.tool_calls, "tool call")}</span>}
          {!!q?.images && <span>{count(q.images, "image")}</span>}
          {u && (
            <span className="tabular-nums @3xl:hidden">
              {formatTokens(u.input)} in · {formatTokens(u.output)} out
            </span>
          )}
          {s?.cost != null && (
            <span className="tabular-nums @4xl:hidden">
              {formatCost(s.cost)}
            </span>
          )}
          <span className="@xl:hidden">
            {r.hits ? count(r.hits, "replay") : "never replayed"}
          </span>
          {edited && (
            <span className="font-medium text-recorded @5xl:hidden">
              edited
            </span>
          )}
          <span className="font-mono text-[11px]" title={r.key}>
            {shortKey(r.key)}
          </span>
          <span className="@2xl:hidden" title={absolute(updated)}>
            {relativeTime(updated, now)}
          </span>
        </p>
      </td>
      <td className="hidden px-2 py-2 align-top text-xs @4xl:table-cell">
        <span className="block truncate font-medium" title={model}>
          {model ? shortModel(model) : "—"}
        </span>
        {s?.outcome && (
          <span
            className={cn(
              "block truncate",
              normalEnd.test(s.outcome) ? "text-muted-foreground" : "text-miss",
            )}
          >
            {s.outcome}
          </span>
        )}
      </td>
      <td className={cn(num, "hidden @3xl:table-cell")}>
        <span className="block">{formatTokens(u?.input)}</span>
        {!!u?.cached_input && (
          <span
            className="block whitespace-nowrap text-[11px] text-muted-foreground"
            title={`${u.cached_input.toLocaleString()} cached input tokens`}
          >
            {formatTokens(u.cached_input)} cached
          </span>
        )}
      </td>
      <td className={cn(num, "hidden @3xl:table-cell")}>
        {formatTokens(u?.output)}
      </td>
      <td className={cn(num, "hidden @5xl:table-cell")}>
        <span className={cn(u?.reasoning == null && "text-muted-foreground")}>
          {formatTokens(u?.reasoning)}
        </span>
      </td>
      <td className={cn(num, "hidden @4xl:table-cell")}>
        <span className={cn(s?.cost == null && "text-muted-foreground")}>
          {formatCost(s?.cost)}
        </span>
      </td>
      <td className={cn(num, "hidden @xl:table-cell")}>
        {r.hits ? (
          <>
            <span className="block font-medium">{r.hits.toLocaleString()}</span>
            <span
              className="block text-muted-foreground"
              title={`Last replayed ${absolute(r.last_hit_at)}`}
            >
              {relativeTime(r.last_hit_at, now)}
            </span>
          </>
        ) : (
          <span className="text-muted-foreground">never</span>
        )}
      </td>
      <td className={cn(num, "hidden @5xl:table-cell")}>
        <span className="block">{r.revisions}</span>
        {edited && (
          <span className="block font-medium text-recorded">edited</span>
        )}
      </td>
      <td className={cn(num, "hidden text-muted-foreground @5xl:table-cell")}>
        {q ? formatBytes(q.bytes) : "—"}
      </td>
      <td className={cn(num, "hidden @2xl:table-cell")}>
        <span className="block" title={absolute(updated)}>
          {relativeTime(updated, now)}
        </span>
      </td>
      <td className="py-2 pr-2 align-top">
        <ChevronRight
          aria-hidden
          className="mt-0.5 size-4 text-muted-foreground opacity-50 group-hover:opacity-100"
        />
      </td>
    </tr>
  );
});
