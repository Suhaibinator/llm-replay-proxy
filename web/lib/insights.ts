// Pure data shaping and formatting for the Overview analytics view. No React,
// no DOM: everything here is unit tested in insights.test.mjs.
import type {
  InsightBucket,
  Insights,
  LatencyStats,
  ModelInsight,
  OutcomeCounts,
  Percentiles,
  ThreadSummary,
  TokenTotals,
} from "./api.ts";

// ---------------------------------------------------------------------------
// Ranges

export const RANGES = [
  { id: "24h", label: "24h", long: "Last 24 hours" },
  { id: "7d", label: "7d", long: "Last 7 days" },
  { id: "30d", label: "30d", long: "Last 30 days" },
  { id: "90d", label: "90d", long: "Last 90 days" },
] as const;
export type RangeId = (typeof RANGES)[number]["id"];
export const isRange = (v: unknown): v is RangeId =>
  RANGES.some((r) => r.id === v);

const HOUR = 3_600_000,
  DAY = 86_400_000;

/**
 * The [from, to) window for a range preset, aligned to the server's UTC
 * buckets so the first bar is a whole bucket: 24 hourly buckets for 24h,
 * otherwise N whole UTC days ending today.
 */
export function rangeWindow(range: RangeId, now = new Date()) {
  const to = new Date(now.getTime());
  if (range === "24h")
    return {
      from: new Date(Math.floor(to.getTime() / HOUR) * HOUR - 23 * HOUR),
      to,
    };
  const days = range === "7d" ? 7 : range === "30d" ? 30 : 90;
  const from = new Date(
    Date.UTC(to.getUTCFullYear(), to.getUTCMonth(), to.getUTCDate() - days + 1),
  );
  return { from, to };
}

export function insightsURL(
  collectionId: number,
  range: RangeId,
  model = "",
  now = new Date(),
  provider = "",
) {
  const { from, to } = rangeWindow(range, now);
  const q = new URLSearchParams({
    collection_id: String(collectionId),
    from: from.toISOString(),
    to: to.toISOString(),
  });
  if (model) q.set("model", model);
  if (provider) q.set("provider", provider);
  return `/api/insights?${q}`;
}

// ---------------------------------------------------------------------------
// Number formatting

const trim = (s: string) => s.replace(/\.0$/, "");

/** 950, 1.2k, 12k, 3.4M, 1.1B. */
export function formatCount(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n)) return "—";
  const sign = n < 0 ? "-" : "";
  let a = Math.abs(n);
  if (a < 1000) return sign + String(Math.round(a));
  const units = ["k", "M", "B", "T"];
  let i = -1;
  while (a >= 1000 && i < units.length - 1) {
    a /= 1000;
    i++;
  }
  let text = a < 10 ? a.toFixed(1) : a.toFixed(0);
  // 999.96k rounds to "1000k": carry into the next unit.
  if (Number(text) >= 1000 && i < units.length - 1) {
    i++;
    text = (Number(text) / 1000).toFixed(1);
  }
  return sign + trim(text) + units[i];
}

/** Full-precision integer for tables and tooltips: 1,234,567. */
export const formatExact = (n: number | null | undefined) =>
  n == null || !Number.isFinite(n)
    ? "—"
    : Math.round(n).toLocaleString("en-US");

/** USD: $0.0123 below ten cents (3 significant digits), $0.42, $12.34, $1.2k. */
export function formatCost(usd: number | null | undefined): string {
  if (usd == null || !Number.isFinite(usd)) return "—";
  const a = Math.abs(usd),
    sign = usd < 0 ? "-" : "";
  if (a === 0) return "$0";
  if (a < 0.0001) return `${sign}<$0.0001`;
  if (a < 0.1) return `${sign}$${String(Number(a.toPrecision(3)))}`;
  if (a < 1000) return `${sign}$${a.toFixed(2)}`;
  return `${sign}$${formatCount(a)}`;
}

/** 840 ms, 1.25 s, 12.4 s, 2.5 min. */
export function formatMs(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms)) return "—";
  if (ms < 1) return ms === 0 ? "0 ms" : "<1 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 10_000) return `${Number((ms / 1000).toFixed(2))} s`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(1))} s`;
  return `${trim((ms / 60_000).toFixed(1))} min`;
}

/** A 0–1 ratio as 87%, with <1% and >99% at the ends. */
export function formatPercent(ratio: number | null | undefined): string {
  if (ratio == null || !Number.isFinite(ratio)) return "—";
  const v = ratio * 100;
  if (v > 0 && v < 1) return "<1%";
  if (v > 99 && v < 100) return ">99%";
  return `${Math.round(v)}%`;
}

/** 38× or 1.4×: how many times faster replay is. */
export function formatRatio(r: number | null | undefined): string {
  if (r == null || !Number.isFinite(r)) return "—";
  return r >= 10 ? `${formatCount(r)}×` : `${trim(r.toFixed(1))}×`;
}

// ---------------------------------------------------------------------------
// Axes

/** Clean 0-based ticks covering max: 0/250/500/750/1,000. */
export function niceScale(max: number, target = 4, integer = true) {
  if (!(max > 0) || !Number.isFinite(max)) {
    const ticks = integer ? [0, 1, 2, 3, 4] : [0, 0.25, 0.5, 0.75, 1];
    return { max: ticks[ticks.length - 1], ticks };
  }
  const raw = max / target;
  const mag = 10 ** Math.floor(Math.log10(raw));
  const norm = raw / mag;
  const steps = integer && mag < 10 ? [1, 2, 5, 10] : [1, 2, 2.5, 5, 10];
  let step = (steps.find((s) => norm <= s) ?? 10) * mag;
  if (integer) step = Math.max(1, Math.ceil(step));
  const top = Math.ceil(max / step - 1e-9) * step;
  const ticks: number[] = [];
  for (let v = 0; v <= top + step / 2; v += step)
    ticks.push(Number(v.toPrecision(12)));
  return { max: ticks[ticks.length - 1], ticks };
}

/** Which of n category labels to print so they do not collide. */
export function labelStride(n: number, plotWidth: number, labelWidth = 56) {
  if (n <= 0 || plotWidth <= 0) return 1;
  const fit = Math.max(1, Math.floor(plotWidth / labelWidth));
  return Math.max(1, Math.ceil(n / fit));
}

// ---------------------------------------------------------------------------
// Buckets

const dayFormat = new Intl.DateTimeFormat("en-US", {
  month: "short",
  day: "numeric",
  timeZone: "UTC",
});
const hourFormat = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
});
const hourTitleFormat = new Intl.DateTimeFormat(undefined, {
  weekday: "short",
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
const dayTitleFormat = new Intl.DateTimeFormat("en-US", {
  weekday: "short",
  month: "short",
  day: "numeric",
  timeZone: "UTC",
});

/** Short axis label: "14:00" for hours (local), "Oct 4" for UTC days. */
export function bucketTick(start: string, bucket: "hour" | "day") {
  const d = new Date(start);
  if (Number.isNaN(d.getTime())) return start;
  return bucket === "hour" ? hourFormat.format(d) : dayFormat.format(d);
}

/** Tooltip/table label for a bucket. */
export function bucketTitle(start: string, bucket: "hour" | "day") {
  const d = new Date(start);
  if (Number.isNaN(d.getTime())) return start;
  return bucket === "hour"
    ? hourTitleFormat.format(d)
    : `${dayTitleFormat.format(d)} (UTC)`;
}

const zeroTokens = (): TokenTotals => ({
  input: 0,
  cached_input: 0,
  output: 0,
  reasoning: 0,
});
const emptyBucket = (start: string): InsightBucket => ({
  start,
  hit_rate: null,
  requests: 0,
  hits: 0,
  misses: 0,
  recorded: 0,
  interrupted: 0,
  errors: 0,
  upstream_tokens: zeroTokens(),
  replayed_tokens: zeroTokens(),
});

/**
 * The series with every bucket in [from, to) present, oldest first. The
 * server already zero-fills; this keeps the x axis honest if it ever does not.
 */
export function fillSeries(
  ins: Pick<Insights, "from" | "to" | "bucket" | "series">,
) {
  const step = ins.bucket === "hour" ? HOUR : DAY;
  const from = Date.parse(ins.from),
    to = Date.parse(ins.to);
  const byTime = new Map<number, InsightBucket>();
  for (const b of ins.series) {
    const t = Date.parse(b.start);
    if (!Number.isNaN(t)) byTime.set(t, b);
  }
  if (
    Number.isNaN(from) ||
    Number.isNaN(to) ||
    to <= from ||
    (to - from) / step > 2000
  )
    return [...byTime.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1]);
  const out: InsightBucket[] = [];
  for (let t = Math.floor(from / step) * step; t < to; t += step) {
    out.push(
      byTime.get(t) ??
        emptyBucket(new Date(t).toISOString().replace(".000Z", "Z")),
    );
    byTime.delete(t);
  }
  // Keep anything the server sent off-grid rather than dropping it.
  if (byTime.size)
    return [...out, ...byTime.values()].sort(
      (a, b) => Date.parse(a.start) - Date.parse(b.start),
    );
  return out;
}

// ---------------------------------------------------------------------------
// Outcomes

export type OutcomeKey =
  "hits" | "recorded" | "misses" | "interrupted" | "errors";

/**
 * Stack order (bottom to top). This order keeps the outcome colors that are
 * hard to tell apart (recorded / interrupted) from touching in a stack.
 */
export const OUTCOMES: {
  key: OutcomeKey;
  label: string;
  color: string;
  hint: string;
}[] = [
  {
    key: "hits",
    label: "Hits",
    color: "var(--color-hit)",
    hint: "Served from a recording",
  },
  {
    key: "recorded",
    label: "Recorded",
    color: "var(--color-recorded)",
    hint: "Fetched upstream and saved",
  },
  {
    key: "misses",
    label: "Misses",
    color: "var(--color-miss)",
    hint: "No recording; not fetched",
  },
  {
    key: "interrupted",
    label: "Interrupted",
    color: "var(--color-interrupted)",
    hint: "Caller hung up mid-stream",
  },
  {
    key: "errors",
    label: "Errors",
    color: "var(--color-error)",
    hint: "Errors and incomplete responses",
  },
];

/**
 * Cache hit rate for a slice, always the server's: it counts recording
 * lookups, which outcome counts alone cannot reproduce (an interrupted replay
 * still hit; Record mode never looks up).
 */
export function hitRate(c: { hit_rate: number | null }) {
  return c.hit_rate;
}

// ---------------------------------------------------------------------------
// Tokens

export type TokenPartKey = "input" | "cached" | "output" | "reasoning";

/**
 * Token composition as disjoint parts. Providers report cached input as part
 * of input, and reasoning as part of output, so they are split out here:
 * input = uncached prompt, cached = cached prompt, output = visible output,
 * reasoning = reasoning output.
 */
export const TOKEN_PARTS: {
  key: TokenPartKey;
  label: string;
  color: string;
  hint: string;
}[] = [
  {
    key: "input",
    label: "Input",
    color: "var(--color-chart-2)",
    hint: "Prompt tokens not served from the provider's cache",
  },
  {
    key: "cached",
    label: "Cached input",
    color: "color-mix(in oklab, var(--color-chart-2) 45%, var(--color-card))",
    hint: "Prompt tokens the provider served from its prompt cache",
  },
  {
    key: "output",
    label: "Output",
    color: "var(--color-chart-3)",
    hint: "Visible output: text and tool calls",
  },
  {
    key: "reasoning",
    label: "Reasoning",
    color: "var(--color-chart-4)",
    hint: "Hidden reasoning tokens of reasoning models",
  },
];

export type TokenParts = Record<TokenPartKey, number>;

export function tokenParts(t: TokenTotals | null | undefined): TokenParts {
  if (!t) return { input: 0, cached: 0, output: 0, reasoning: 0 };
  const input = Math.max(0, t.input || 0),
    output = Math.max(0, t.output || 0);
  const cached = Math.min(Math.max(0, t.cached_input || 0), input || Infinity);
  const reasoning = Math.min(Math.max(0, t.reasoning || 0), output || Infinity);
  return {
    input: Math.max(0, input - cached),
    cached,
    output: Math.max(0, output - reasoning),
    reasoning,
  };
}

export const partsTotal = (p: TokenParts) =>
  p.input + p.cached + p.output + p.reasoning;
export const tokenTotal = (t: TokenTotals | null | undefined) =>
  partsTotal(tokenParts(t));

export function addTokens(
  ...ts: (TokenTotals | null | undefined)[]
): TokenTotals {
  const out = zeroTokens();
  for (const t of ts) {
    if (!t) continue;
    out.input += t.input || 0;
    out.cached_input += t.cached_input || 0;
    out.output += t.output || 0;
    out.reasoning += t.reasoning || 0;
  }
  return out;
}

/** Share of output tokens spent on reasoning, null without output. */
export function reasoningShare(t: TokenTotals | null | undefined) {
  const p = tokenParts(t);
  const out = p.output + p.reasoning;
  return out > 0 ? p.reasoning / out : null;
}

/** Share of input tokens the provider served from its prompt cache. */
export function cachedShare(t: TokenTotals | null | undefined) {
  const p = tokenParts(t);
  const input = p.input + p.cached;
  return input > 0 ? p.cached / input : null;
}

export type TokenRow = {
  start: string;
  upstream: TokenParts;
  replayed: TokenParts;
  upstreamTotal: number;
  replayedTotal: number;
};

export function tokenSeries(series: InsightBucket[]): TokenRow[] {
  return series.map((b) => {
    const upstream = tokenParts(b.upstream_tokens),
      replayed = tokenParts(b.replayed_tokens);
    return {
      start: b.start,
      upstream,
      replayed,
      upstreamTotal: partsTotal(upstream),
      replayedTotal: partsTotal(replayed),
    };
  });
}

// ---------------------------------------------------------------------------
// Latency

const has = (p: Percentiles | null | undefined) => !!p && p.samples > 0;
export const p50 = (p: Percentiles | null | undefined) =>
  has(p) ? p!.p50 : null;
export const p95 = (p: Percentiles | null | undefined) =>
  has(p) ? p!.p95 : null;

/** Upstream p50 / replay p50: how many times faster a replay answers. */
export function speedup(
  upstream: Percentiles | null | undefined,
  replay: Percentiles | null | undefined,
) {
  const u = p50(upstream),
    r = p50(replay);
  if (u == null || r == null || u <= 0) return null;
  return u / Math.max(r, 1);
}

export type HistogramRow = {
  le: number | null;
  /** Full bucket range: "100ms–250ms". */
  label: string;
  /** Compact axis label, the upper bound: "≤250ms", ">60s". */
  short: string;
  upstream: number;
  replay: number;
  /** Share of each source's samples in this bucket (0–1). */
  upstreamShare: number;
  replayShare: number;
};

const msLabel = (ms: number) =>
  ms < 1000 ? `${ms}ms` : `${trim(String(ms / 1000))}s`;

type Histogram = { le: number | null; count: number }[];
/**
 * LatencyStats plus the first-event histogram the backend is adding
 * (same bounds as `histogram`); optional until api.ts carries it.
 */
export type LatencyWithFirst = LatencyStats & {
  first_event_histogram?: Histogram;
};
export type LatencyMetric = "duration" | "first_event";

export const histogramOf = (
  s: LatencyWithFirst | null | undefined,
  metric: LatencyMetric,
): Histogram =>
  (metric === "duration" ? s?.histogram : s?.first_event_histogram) ?? [];

/**
 * Merge the two sources' histograms onto shared bucket bounds. Counts are per
 * bucket (previous le < value <= le); the last bucket (le null) is the rest.
 */
export function histogramRows(
  upstream: LatencyWithFirst | null | undefined,
  replay: LatencyWithFirst | null | undefined,
  metric: LatencyMetric = "duration",
): HistogramRow[] {
  const uh = histogramOf(upstream, metric),
    rh = histogramOf(replay, metric);
  const key = (le: number | null) => (le == null ? Infinity : le);
  const bounds = new Map<number, number | null>();
  for (const h of [...uh, ...rh]) bounds.set(key(h.le), h.le);
  const sorted = [...bounds.entries()].sort((a, b) => a[0] - b[0]);
  const count = (hs: Histogram, k: number) =>
    hs.filter((h) => key(h.le) === k).reduce((a, h) => a + h.count, 0);
  const uTotal = uh.reduce((a, h) => a + h.count, 0);
  const rTotal = rh.reduce((a, h) => a + h.count, 0);
  let prev: number | null = null;
  return sorted.map(([k, le]) => {
    const label =
      le == null
        ? prev == null
          ? "all"
          : `>${msLabel(prev)}`
        : prev == null
          ? `≤${msLabel(le)}`
          : `${msLabel(prev)}–${msLabel(le)}`;
    const short = le == null ? label : `≤${msLabel(le)}`;
    if (le != null) prev = le;
    const u = count(uh, k),
      r = count(rh, k);
    return {
      le,
      label,
      short,
      upstream: u,
      replay: r,
      upstreamShare: uTotal ? u / uTotal : 0,
      replayShare: rTotal ? r / rTotal : 0,
    };
  });
}

// ---------------------------------------------------------------------------
// Models

export type ModelRow = {
  model: string;
  requests: number;
  /** Share of all requests in the range (0–1). */
  share: number;
  hits: number;
  errors: number;
  hitRate: number | null;
  upstreamTokens: number;
  replayedTokens: number;
  tokens: number;
  /** Composition of all tokens the model handled, upstream and replayed. */
  mix: TokenParts;
  reasoningShare: number | null;
  cachedShare: number | null;
  upstreamCost: number | null;
  savedCost: number | null;
  upstreamP50: number | null;
  upstreamP95: number | null;
  replayP50: number | null;
  replayP95: number | null;
  firstEventP50: number | null;
};

export function modelRows(models: ModelInsight[]): ModelRow[] {
  const total = models.reduce((a, m) => a + m.requests, 0);
  return models.map((m) => {
    const all = addTokens(m.upstream_tokens, m.replayed_tokens);
    const upstreamTokens = tokenTotal(m.upstream_tokens),
      replayedTokens = tokenTotal(m.replayed_tokens);
    return {
      model: m.model || "unknown",
      requests: m.requests,
      share: total ? m.requests / total : 0,
      hits: m.hits,
      errors: m.errors,
      hitRate: hitRate(m),
      upstreamTokens,
      replayedTokens,
      tokens: upstreamTokens + replayedTokens,
      mix: tokenParts(all),
      reasoningShare: reasoningShare(all),
      cachedShare: cachedShare(all),
      upstreamCost: m.upstream_cost,
      savedCost: m.saved_cost,
      upstreamP50: p50(m.upstream?.duration_ms),
      upstreamP95: p95(m.upstream?.duration_ms),
      replayP50: p50(m.replay?.duration_ms),
      replayP95: p95(m.replay?.duration_ms),
      firstEventP50: p50(m.upstream?.first_event_ms),
    };
  });
}

export type ModelSortKey =
  | "model"
  | "requests"
  | "hitRate"
  | "tokens"
  | "reasoningShare"
  | "upstreamCost"
  | "upstreamP50"
  | "replayP50";
export type SortDir = "asc" | "desc";

/** Sorted copy; missing values always sort last, ties by name. */
export function sortModels(rows: ModelRow[], key: ModelSortKey, dir: SortDir) {
  const sign = dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    if (key === "model") return sign * a.model.localeCompare(b.model);
    const x = a[key],
      y = b[key];
    if (x == null && y == null) return a.model.localeCompare(b.model);
    if (x == null) return 1;
    if (y == null) return -1;
    return x === y ? a.model.localeCompare(b.model) : sign * (x - y);
  });
}

/**
 * Models to offer in the filter: everything seen so far (so the list does not
 * shrink to one entry while filtered), busiest first, plus the active filter.
 */
export function mergeModelOptions(
  known: string[],
  models: ModelInsight[],
  active = "",
) {
  const out = [...known];
  const seen = new Set(out);
  for (const m of [...models].sort((a, b) => b.requests - a.requests)) {
    const name = m.model || "unknown";
    if (!seen.has(name)) {
      seen.add(name);
      out.push(name);
    }
  }
  if (active && !seen.has(active)) out.push(active);
  return out;
}

/** Provider names seen so far, busiest first; same merge as mergeModelOptions. */
export function mergeProviderOptions(
  known: string[],
  providers: Insights["providers"],
  active = "",
) {
  const out = [...known];
  const seen = new Set(out);
  for (const p of [...providers].sort((a, b) => b.requests - a.requests)) {
    if (!seen.has(p.provider)) {
      seen.add(p.provider);
      out.push(p.provider);
    }
  }
  if (active && !seen.has(active)) out.push(active);
  return out;
}

// ---------------------------------------------------------------------------
// Routes and threads

export type RouteRow = OutcomeCounts & {
  route: string;
  hitRate: number | null;
  share: number;
};

export function routeRows(routes: Insights["routes"]): RouteRow[] {
  const total = routes.reduce((a, r) => a + r.requests, 0);
  return [...routes]
    .sort((a, b) => b.requests - a.requests || a.route.localeCompare(b.route))
    .map((r) => ({
      ...r,
      hitRate: hitRate(r),
      share: total ? r.requests / total : 0,
    }));
}

/** The best one-line title for a thread. */
export function threadTitle(
  t: Pick<ThreadSummary, "opening" | "latest" | "thread">,
) {
  const text = (t.opening || t.latest || "").replace(/\s+/g, " ").trim();
  return text || `Thread ${t.thread.slice(0, 8)}`;
}

// ---------------------------------------------------------------------------
// Headline

export type Headline = {
  requests: number;
  hitRate: number | null;
  hits: number;
  lookups: number;
  replayedTokens: number;
  upstreamTokens: number;
  savedCost: number | null;
  upstreamCost: number | null;
  threads: number;
  upstreamP50: number | null;
  upstreamP95: number | null;
  firstEventP50: number | null;
  firstEventP95: number | null;
  replayP50: number | null;
  speedup: number | null;
  /** Per-bucket sparkline values. */
  trend: {
    requests: number[];
    hitRate: (number | null)[];
    replayedTokens: number[];
    upstreamTokens: number[];
  };
};

export function headline(ins: Insights, series = ins.series): Headline {
  const t = ins.totals;
  const up = ins.latency.upstream,
    re = ins.latency.replay;
  return {
    requests: t.requests,
    hitRate: t.hit_rate,
    hits: t.lookup_hits,
    lookups: t.lookups,
    replayedTokens: tokenTotal(t.replayed_tokens),
    upstreamTokens: tokenTotal(t.upstream_tokens),
    savedCost: t.saved_cost,
    upstreamCost: t.upstream_cost,
    threads: t.threads,
    upstreamP50: p50(up?.duration_ms),
    upstreamP95: p95(up?.duration_ms),
    firstEventP50: p50(up?.first_event_ms),
    firstEventP95: p95(up?.first_event_ms),
    replayP50: p50(re?.duration_ms),
    speedup: speedup(up?.duration_ms, re?.duration_ms),
    trend: {
      requests: series.map((b) => b.requests),
      hitRate: series.map((b) => hitRate(b)),
      replayedTokens: series.map((b) => tokenTotal(b.replayed_tokens)),
      upstreamTokens: series.map((b) => tokenTotal(b.upstream_tokens)),
    },
  };
}

/** "just now", "5m ago", "3h ago", "2d ago", else a short date. */
export function formatAgo(iso: string, now = new Date()) {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const s = Math.max(0, (now.getTime() - t) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86_400) return `${Math.round(s / 3600)}h ago`;
  if (s < 30 * 86_400) return `${Math.round(s / 86_400)}d ago`;
  return dayFormat.format(new Date(t));
}
