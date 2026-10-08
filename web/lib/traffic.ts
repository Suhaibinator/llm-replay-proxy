// Pure logic for the live Traffic view: merging incremental history polls,
// filtering, facet counts, the activity strip and small formatters. Kept free
// of runtime imports so node:test can load it with --experimental-strip-types.
import type { History } from "./api";

/** Rows the Traffic view keeps client-side. */
export const TRAFFIC_ROWS = 500;

/** Outcomes in display order; `incomplete` shares the error color. */
export const OUTCOMES = [
  "hit",
  "miss",
  "recorded",
  "interrupted",
  "error",
  "incomplete",
] as const;

/** The color group an outcome is drawn with (one meaning everywhere). */
export type OutcomeTone = "hit" | "miss" | "recorded" | "interrupted" | "error";
export function outcomeTone(outcome: string): OutcomeTone {
  switch (outcome) {
    case "hit":
    case "miss":
    case "recorded":
    case "interrupted":
      return outcome;
    default:
      return "error";
  }
}
export const TONES: OutcomeTone[] = [
  "hit",
  "miss",
  "recorded",
  "interrupted",
  "error",
];

/**
 * Merges a poll's rows (any order) into the current newest-first list. History
 * rows never change, so an id seen before is kept as is.
 */
export function mergeHistory(
  current: History[],
  incoming: History[],
  max = TRAFFIC_ROWS,
): History[] {
  if (!incoming.length) return current.slice(0, max);
  const seen = new Set(current.map((h) => h.id));
  const fresh = incoming.filter((h) => !seen.has(h.id));
  if (!fresh.length) return current.slice(0, max);
  return [...fresh, ...current].sort((a, b) => b.id - a.id).slice(0, max);
}

/** Largest id in a list (0 when empty): the next poll's `after_id`. */
export const newestId = (rows: History[]) =>
  rows.reduce((max, h) => (h.id > max ? h.id : max), 0);

/** The history URL for a full load (`afterId` 0) or an incremental poll. */
export function historyURL(
  collectionId: number,
  afterId: number,
  limit = TRAFFIC_ROWS,
) {
  const q = new URLSearchParams({
    collection_id: String(collectionId),
    limit: String(limit),
  });
  if (afterId > 0) q.set("after_id", String(afterId));
  return `/api/history?${q}`;
}

/**
 * While live updates are paused the view keeps showing rows up to the id it
 * paused at; the rest are counted as waiting.
 */
export function liveSplit(rows: History[], pausedAt: number | null) {
  if (pausedAt === null) return { shown: rows, waiting: 0 };
  const shown = rows.filter((h) => h.id <= pausedAt);
  return { shown, waiting: rows.length - shown.length };
}

export type TrafficFilter = {
  /** Selected outcomes; empty means all. */
  outcomes: string[];
  model: string;
  provider: string;
  route: string;
  source: string;
  thread: string;
  query: string;
};
export const emptyTrafficFilter: TrafficFilter = {
  outcomes: [],
  model: "",
  provider: "",
  route: "",
  source: "",
  thread: "",
  query: "",
};

export const rowModel = (h: History) => h.request?.model || "";
/** The row's provider, named as the insights breakdown names it. */
export const rowProvider = (h: History) => h.provider || "unknown";

function matchesQuery(h: History, query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [
    h.request?.preview,
    h.request?.model,
    h.key,
    h.detail,
    h.route,
    h.provider,
    String(h.id),
  ].some((v) => v && v.toLowerCase().includes(q));
}

/** Everything except the outcome selection, so outcome chips can count. */
function matchesFacets(h: History, f: TrafficFilter) {
  return (
    (!f.model || rowModel(h) === f.model) &&
    (!f.provider || rowProvider(h) === f.provider) &&
    (!f.route || h.route === f.route) &&
    (!f.source || h.source === f.source) &&
    (!f.thread || h.request?.thread === f.thread) &&
    matchesQuery(h, f.query)
  );
}

export function filterTraffic(rows: History[], f: TrafficFilter) {
  return rows.filter(
    (h) =>
      (!f.outcomes.length || f.outcomes.includes(h.outcome)) &&
      matchesFacets(h, f),
  );
}

/**
 * Outcome counts under every filter except the outcome selection itself, in
 * display order. Known outcomes always appear; unknown ones are appended.
 */
export function outcomeCounts(rows: History[], f: TrafficFilter) {
  const counts = new Map<string, number>(OUTCOMES.map((o) => [o, 0]));
  for (const h of rows) {
    if (!matchesFacets(h, f)) continue;
    counts.set(h.outcome, (counts.get(h.outcome) ?? 0) + 1);
  }
  return [...counts].map(([outcome, n]) => ({ outcome, count: n }));
}

/** Distinct values of a field with counts, most frequent first. */
export function facet(rows: History[], pick: (h: History) => string) {
  const counts = new Map<string, number>();
  for (const h of rows) {
    const v = pick(h);
    if (v) counts.set(v, (counts.get(v) ?? 0) + 1);
  }
  return [...counts]
    .map(([value, n]) => ({ value, count: n }))
    .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
}

export type ActivityBucket = {
  start: number;
  total: number;
  counts: Record<OutcomeTone, number>;
};
export type Activity = {
  start: number;
  end: number;
  /** Bucket width in minutes. */
  minutes: number;
  buckets: ActivityBucket[];
  peak: number;
};

const STEPS = [1, 2, 5, 10, 15, 30, 60, 120, 360, 720, 1440];
const MINUTE = 60_000;

/**
 * Requests per minute over the window the rows cover, ending at `now`. The
 * bucket widens (2, 5, 10 … minutes) so the strip has at most `maxBuckets`
 * bars; it has at least `minMinutes` of history so a quiet minute still reads
 * as a timeline.
 */
export function activity(
  rows: History[],
  now: number,
  maxBuckets = 60,
  minMinutes = 30,
): Activity {
  const times = rows
    .map((h) => Date.parse(h.created_at))
    .filter((t) => Number.isFinite(t));
  const end = Math.ceil(Math.max(now, ...times) / MINUTE) * MINUTE;
  const oldest = times.length ? Math.min(...times) : end;
  const span = Math.max(minMinutes, Math.ceil((end - oldest) / MINUTE));
  const minutes =
    STEPS.find((s) => Math.ceil(span / s) <= maxBuckets) ??
    Math.ceil(span / maxBuckets / 1440) * 1440;
  const count = Math.ceil(span / minutes);
  const width = minutes * MINUTE;
  const start = end - count * width;
  const buckets: ActivityBucket[] = Array.from({ length: count }, (_, i) => ({
    start: start + i * width,
    total: 0,
    counts: { hit: 0, miss: 0, recorded: 0, interrupted: 0, error: 0 },
  }));
  rows.forEach((h) => {
    const t = Date.parse(h.created_at);
    if (!Number.isFinite(t)) return;
    const b = buckets[Math.min(count - 1, Math.floor((t - start) / width))];
    if (!b) return;
    b.total++;
    b.counts[outcomeTone(h.outcome)]++;
  });
  return {
    start,
    end,
    minutes,
    buckets,
    peak: buckets.reduce((m, b) => Math.max(m, b.total), 0),
  };
}

/**
 * The value timing bars are scaled to among the visible rows: the 95th
 * percentile of their durations (the maximum with fewer than 20 timed rows),
 * so one slow outlier does not flatten every other bar. Longer rows draw a
 * full bar marked as clipped.
 */
export function timingScale(rows: History[]) {
  const values: number[] = [];
  for (const h of rows) {
    const v = Math.max(h.duration_ms ?? 0, h.first_event_ms ?? 0);
    if (v > 0) values.push(v);
  }
  if (!values.length) return 0;
  values.sort((a, b) => a - b);
  if (values.length < 20) return values[values.length - 1];
  return values[Math.ceil(values.length * 0.95) - 1];
}

/** The model name without its provider prefix ("openai/gpt-5" → "gpt-5"). */
export const shortModel = (model: string) =>
  model.slice(model.lastIndexOf("/") + 1);

export function formatMs(ms: number | null | undefined) {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 10_000) return `${(ms / 1000).toFixed(2)} s`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  const m = Math.floor(ms / 60_000);
  return `${m}m ${Math.round((ms % 60_000) / 1000)}s`;
}

/** "now", "42s", "5m", "3h", "2d" — compact relative age. */
export function relativeTime(iso: string, now: number) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "—";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 5) return "now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

/** "per minute", "per 5 min", "per hour", "per 6 h", "per day". */
export function bucketLabel(minutes: number) {
  if (minutes === 1) return "per minute";
  if (minutes < 60) return `per ${minutes} min`;
  if (minutes === 60) return "per hour";
  if (minutes < 1440) return `per ${minutes / 60} h`;
  return minutes === 1440 ? "per day" : `per ${minutes / 1440} days`;
}

export const shortRoute = (route: string) => route.replace(/^\/v1\//, "");
