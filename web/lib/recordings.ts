// Pure logic for the Recordings view: filtering, sorting, paging and number
// formatting. Free of runtime imports so node:test can load it directly.
import type { RecordingRow } from "./api";

export type RecordingSortKey =
  | "request"
  | "model"
  | "input"
  | "output"
  | "reasoning"
  | "cost"
  | "replays"
  | "last_replay"
  | "revisions"
  | "size"
  | "updated";
export type SortDir = "asc" | "desc";
export type RecordingSort = { key: RecordingSortKey; dir: SortDir };
export const defaultRecordingSort: RecordingSort = {
  key: "updated",
  dir: "desc",
};

/** Numbers and dates read best biggest/newest first; text A→Z. */
export const firstDir = (key: RecordingSortKey): SortDir =>
  key === "request" || key === "model" ? "asc" : "desc";

/** Clicking a header sorts by it, or flips the direction if already sorted. */
export function nextSort(
  sort: RecordingSort,
  key: RecordingSortKey,
): RecordingSort {
  if (sort.key !== key) return { key, dir: firstDir(key) };
  return { key, dir: sort.dir === "asc" ? "desc" : "asc" };
}

export const recordingModel = (r: RecordingRow) =>
  r.response?.model || r.request?.model || "";

/** Ever edited: more than one revision, or the active one is an edit. */
export const isEdited = (r: RecordingRow) =>
  r.revisions > 1 || r.source === "edit";

const time = (s: string) => {
  const t = Date.parse(s);
  return Number.isFinite(t) ? t : null;
};

function sortValue(
  r: RecordingRow,
  key: RecordingSortKey,
): string | number | null {
  switch (key) {
    case "request":
      return r.request?.preview?.toLowerCase() || null;
    case "model":
      return recordingModel(r).toLowerCase() || null;
    case "input":
      return r.response?.usage.input ?? null;
    case "output":
      return r.response?.usage.output ?? null;
    case "reasoning":
      return r.response?.usage.reasoning ?? null;
    case "cost":
      return r.response?.cost ?? null;
    case "replays":
      return r.hits;
    case "last_replay":
      return r.hits ? time(r.last_hit_at) : null;
    case "revisions":
      return r.revisions;
    case "size":
      return r.request?.bytes ?? null;
    case "updated":
      return time(r.updated_at || r.created_at);
  }
}

/**
 * Sorts a copy. Missing values (unreported tokens, never replayed) always sort
 * last whatever the direction; ties fall back to newest id first.
 */
export function sortRecordings(rows: RecordingRow[], sort: RecordingSort) {
  const sign = sort.dir === "asc" ? 1 : -1;
  return rows
    .map((r) => ({ r, v: sortValue(r, sort.key) }))
    .sort((a, b) => {
      if (a.v === null || b.v === null) {
        if (a.v === b.v) return b.r.id - a.r.id;
        return a.v === null ? 1 : -1;
      }
      const c =
        typeof a.v === "string"
          ? a.v.localeCompare(String(b.v))
          : a.v - (b.v as number);
      return c ? c * sign : b.r.id - a.r.id;
    })
    .map((x) => x.r);
}

export type RecordingFilter = {
  model: string;
  route: string;
  neverReplayed: boolean;
  edited: boolean;
  query: string;
};
export const emptyRecordingFilter: RecordingFilter = {
  model: "",
  route: "",
  neverReplayed: false,
  edited: false,
  query: "",
};

export function filterRecordings(rows: RecordingRow[], f: RecordingFilter) {
  const q = f.query.trim().toLowerCase();
  return rows.filter(
    (r) =>
      (!f.model || recordingModel(r) === f.model) &&
      (!f.route || r.route === f.route) &&
      (!f.neverReplayed || r.hits === 0) &&
      (!f.edited || isEdited(r)) &&
      (!q ||
        [
          r.request?.preview,
          r.request?.opening,
          r.request?.model,
          r.response?.model,
          r.route,
          r.key,
          String(r.id),
        ].some((v) => v && v.toLowerCase().includes(q))),
  );
}

export function recordingFacet(
  rows: RecordingRow[],
  pick: (r: RecordingRow) => string,
) {
  const counts = new Map<string, number>();
  for (const r of rows) {
    const v = pick(r);
    if (v) counts.set(v, (counts.get(v) ?? 0) + 1);
  }
  return [...counts]
    .map(([value, count]) => ({ value, count }))
    .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
}

/** Clamps a page index and returns that page's rows. */
export function page<T>(rows: T[], index: number, size: number) {
  const pages = Math.max(1, Math.ceil(rows.length / size));
  const current = Math.min(Math.max(0, index), pages - 1);
  return {
    rows: rows.slice(current * size, current * size + size),
    page: current,
    pages,
    from: rows.length ? current * size + 1 : 0,
    to: Math.min(rows.length, current * size + size),
  };
}

/** 950, 1.2k, 34k, 1.2M — compact token counts; "—" when not reported. */
export function formatTokens(n: number | null | undefined) {
  if (n === null || n === undefined || !Number.isFinite(n)) return "—";
  if (n < 1000) return String(n);
  if (n < 10_000) return `${(n / 1000).toFixed(1)}k`;
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

/** US dollars with enough precision for per-request costs. */
export function formatCost(usd: number | null | undefined) {
  if (usd === null || usd === undefined || !Number.isFinite(usd)) return "—";
  if (usd === 0) return "$0";
  if (usd < 0.0001) return "<$0.0001";
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  if (usd < 1) return `$${usd.toFixed(3)}`;
  return `$${usd.toFixed(2)}`;
}

/** Totals over the given rows, for the summary line. */
export function recordingTotals(rows: RecordingRow[]) {
  let cost = 0,
    costs = 0,
    replays = 0,
    output = 0;
  for (const r of rows) {
    replays += r.hits;
    output += r.response?.usage.output ?? 0;
    if (r.response?.cost !== null && r.response?.cost !== undefined) {
      cost += r.response.cost;
      costs++;
    }
  }
  return { cost: costs ? cost : null, replays, output };
}
