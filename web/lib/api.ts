// Shared API client, response types and formatting helpers for the console.
import { notifyAuthRequired } from "@/components/auth-required";

export type Collection = {
  id: number;
  name: string;
  exclusions: string[];
  created_at: string;
};
export type Settings = {
  mode: "record" | "replay" | "auto";
  active_collection_id: number;
  first_event_delay_ms: number;
  delay_multiplier: number;
  history_limit: number;
};
export type NumberSetting =
  "first_event_delay_ms" | "delay_multiplier" | "history_limit";
export type Event = { data: string; offset_ms: number };
export type Revision = {
  id: number;
  recording_id: number;
  status: number;
  headers: Record<string, string>;
  body: string;
  events: Event[];
  source: string;
  created_at: string;
};
export type RequestSummary = {
  model: string;
  items: number;
  preview: string;
  /** Start of the first user-written text. */
  opening: string;
  tool_calls: number;
  images: number;
  bytes: number;
  thread: string;
};
export type Recording = {
  id: number;
  collection_id: number;
  key: string;
  route: string;
  upstream_identity: string;
  streaming: boolean;
  active_revision_id: number;
  created_at: string;
};
export type RecordingSummary = Recording & {
  request: RequestSummary | null;
  updated_at: string;
  source: string;
  revisions: number;
  hits: number;
  last_hit_at: string;
};
export type Entry = {
  recording: Recording;
  revision: Revision;
  revisions?: Revision[];
  text?: string;
  text_unavailable_reason?: string;
  request_text?: string;
  matching_input_text?: string;
};
export type History = {
  id: number;
  collection_id: number;
  route: string;
  key: string;
  request: RequestSummary | null;
  outcome: string;
  detail: string;
  recording_id: number;
  created_at: string;
  request_text?: string;
  source: string;
  duration_ms: number | null;
  first_event_ms: number | null;
  lookup_outcome: string;
};
export type Percentiles = {
  p50: number | null;
  p95: number | null;
  p99: number | null;
  samples: number;
};
export type Analytics = {
  total: number;
  lifetime_total: number;
  hits: number;
  misses: number;
  errors: number;
  recorded: number;
  hit_rate: number | null;
  sources: Record<
    string,
    { total: number; duration_ms: Percentiles; first_event_ms: Percentiles }
  >;
  series: {
    start: string;
    total: number;
    hits: number;
    misses: number;
    errors: number;
  }[];
};
export const emptyAnalytics: Analytics = {
  total: 0,
  lifetime_total: 0,
  hits: 0,
  misses: 0,
  errors: 0,
  recorded: 0,
  hit_rate: null,
  sources: {},
  series: [],
};
export type Diff = {
  path: string;
  request: unknown;
  recorded: unknown;
  request_exists: boolean;
  recorded_exists: boolean;
  request_display: string;
  recorded_display: string;
};
export const defaults: Settings = {
  mode: "replay",
  active_collection_id: 0,
  first_event_delay_ms: 0,
  delay_multiplier: 1,
  history_limit: 10000,
};
export class APIError extends Error {
  code: string;
  constructor(message: string, code = "request_failed") {
    super(message);
    this.code = code;
  }
}
export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      ...(init?.body instanceof Uint8Array
        ? {}
        : { "Content-Type": "application/json" }),
      ...init?.headers,
    },
  });
  if (res.status === 401) notifyAuthRequired();
  if (!res.ok) {
    let msg = `Request failed (${res.status})`,
      code = "request_failed";
    try {
      const x = await res.json();
      msg = x.error?.message || msg;
      code = x.error?.code || code;
    } catch {}
    throw new APIError(msg, code);
  }
  return res.status === 204 ? (undefined as T) : res.json();
}
export const dateFormat = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
export const utcDayFormat = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "numeric",
  timeZone: "UTC",
});
export const formatDate = (format: Intl.DateTimeFormat, s: string) => {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : format.format(d);
};
export const date = (s: string) => formatDate(dateFormat, s);
export const bucketLabel = (s: string, range: string) =>
  range === "24h" ? date(s) : `${formatDate(utcDayFormat, s)} (UTC)`;
export const pretty = (v: unknown) => JSON.stringify(v, null, 2);
export const editableRevision = (revision: Revision) => ({
  status: revision.status,
  headers: revision.headers,
  body: revision.body,
  events: revision.events,
});
export const download = (href: string) => {
  const link = document.createElement("a");
  link.href = href;
  link.download = "";
  link.click();
};
export const count = (n: number, one: string) =>
  `${n.toLocaleString()} ${one}${n === 1 ? "" : "s"}`;
// The history list shows the newest rows; the API's default page is 200.
export const HISTORY_ROWS = 200;
export const shortKey = (s: string) =>
  s ? `${s.slice(0, 8)}…${s.slice(-5)}` : "—";

// ---------------------------------------------------------------------------
// Dashboard insight endpoints. The contract is docs/dashboard-api.md; the Go
// handlers and these types must agree field for field.

/** Token counts as the provider reported them; null when not reported. */
export type TokenUsage = {
  input: number | null;
  cached_input: number | null;
  output: number | null;
  reasoning: number | null;
  total: number | null;
};

/** What a stored response contained, derived from its body or SSE frames. */
export type ResponseSummary = {
  model: string;
  /** completed, incomplete, failed, … or a finish/stop reason. */
  outcome: string;
  usage: TokenUsage;
  /** Provider-reported cost in USD (e.g. OpenRouter usage.cost), else null. */
  cost: number | null;
  output_chars: number;
  reasoning_chars: number;
  tool_calls: number;
};

/** Sums over many responses; each field sums only reported values. */
export type TokenTotals = {
  input: number;
  cached_input: number;
  output: number;
  reasoning: number;
};

export type OutcomeCounts = {
  requests: number;
  hits: number;
  misses: number;
  recorded: number;
  interrupted: number;
  /** error and incomplete outcomes. */
  errors: number;
};

export type LatencyStats = {
  duration_ms: Percentiles;
  first_event_ms: Percentiles;
  /** Duration histogram: count of samples with duration <= le (ms); last le is null for "more". */
  histogram: { le: number | null; count: number }[];
};

export type InsightBucket = OutcomeCounts & {
  start: string;
  /** Tokens of responses fetched from upstream in this bucket. */
  upstream_tokens: TokenTotals;
  /** Tokens of recorded responses replayed in this bucket (work the cache saved). */
  replayed_tokens: TokenTotals;
};

export type ModelInsight = OutcomeCounts & {
  model: string;
  upstream_tokens: TokenTotals;
  replayed_tokens: TokenTotals;
  upstream_cost: number | null;
  saved_cost: number | null;
  upstream: LatencyStats;
  replay: LatencyStats;
};

export type Insights = {
  from: string;
  to: string;
  bucket: "hour" | "day";
  totals: OutcomeCounts & {
    hit_rate: number | null;
    upstream_tokens: TokenTotals;
    replayed_tokens: TokenTotals;
    upstream_cost: number | null;
    saved_cost: number | null;
    /** Distinct conversation threads seen in the range. */
    threads: number;
  };
  series: InsightBucket[];
  models: ModelInsight[];
  routes: (OutcomeCounts & { route: string })[];
  latency: { upstream: LatencyStats; replay: LatencyStats };
  top_recordings: {
    recording_id: number;
    preview: string;
    model: string;
    hits: number;
    replayed_tokens: TokenTotals;
    saved_cost: number | null;
  }[];
  top_threads: ThreadSummary[];
};

export type ThreadSummary = OutcomeCounts & {
  thread: string;
  route: string;
  model: string;
  /** Start of the first user message. */
  opening: string;
  /** Start of the latest user message. */
  latest: string;
  /** Largest item count seen: how long the conversation grew. */
  max_items: number;
  first_at: string;
  last_at: string;
  last_outcome: string;
  upstream_tokens: TokenTotals;
};

export type ThreadTurn = {
  history_id: number;
  created_at: string;
  outcome: string;
  lookup_outcome: string;
  detail: string;
  recording_id: number;
  items: number;
  preview: string;
  duration_ms: number | null;
  first_event_ms: number | null;
  /** Summary of the response served for this turn (recorded or replayed). */
  response: ResponseSummary | null;
};

export type ThreadDetail = ThreadSummary & { turns: ThreadTurn[] };

export type NearestCandidate = {
  recording_id: number;
  /** Share of the missed request's bytes found in the recording's request (0–1). */
  similarity: number;
  reason: "same_thread" | "shared_content";
  preview: string;
  model: string;
  items: number;
};

/** Recording list rows also carry the active response's summary. */
export type RecordingRow = RecordingSummary & {
  response: ResponseSummary | null;
};
