// Pure helpers for the conversations view and the history detail: outcome
// classification and plain-language explanations, thread filtering, and the
// per-turn shape of a conversation timeline. No React and no runtime imports,
// so node:test can load this file directly.
import type {
  OutcomeCounts,
  ThreadSummary,
  ThreadTurn,
  TokenTotals,
} from "./api";

/** The five outcome colors the console uses everywhere. */
export type OutcomeKind = "hit" | "miss" | "recorded" | "interrupted" | "error";
export const OUTCOME_KINDS: OutcomeKind[] = [
  "hit",
  "recorded",
  "miss",
  "interrupted",
  "error",
];

export function outcomeKind(outcome: string): OutcomeKind {
  switch (outcome) {
    case "hit":
    case "miss":
    case "recorded":
    case "interrupted":
      return outcome;
    default:
      // error, incomplete, and anything a newer server adds.
      return "error";
  }
}

export const OUTCOME_LABEL: Record<OutcomeKind, string> = {
  hit: "Replayed",
  recorded: "Recorded",
  miss: "Missed",
  interrupted: "Interrupted",
  error: "Failed",
};

/** Count field of OutcomeCounts for each kind. */
const COUNT_FIELD: Record<OutcomeKind, keyof OutcomeCounts> = {
  hit: "hits",
  recorded: "recorded",
  miss: "misses",
  interrupted: "interrupted",
  error: "errors",
};

export function outcomeCount(c: OutcomeCounts, kind: OutcomeKind): number {
  return c[COUNT_FIELD[kind]] || 0;
}

export type StripSegment = { kind: OutcomeKind; count: number; share: number };

/** Proportional outcome segments for a thread's request count, in a fixed order. */
export function stripSegments(c: OutcomeCounts): StripSegment[] {
  const total = OUTCOME_KINDS.reduce((n, k) => n + outcomeCount(c, k), 0);
  if (!total) return [];
  return OUTCOME_KINDS.map((kind) => ({
    kind,
    count: outcomeCount(c, kind),
    share: outcomeCount(c, kind) / total,
  })).filter((s) => s.count > 0);
}

/** "18 replayed, 1 missed, 4 recorded" — the strip as words. */
export function describeCounts(c: OutcomeCounts): string {
  const parts = stripSegments(c).map(
    (s) => `${s.count.toLocaleString()} ${OUTCOME_LABEL[s.kind].toLowerCase()}`,
  );
  return parts.length ? parts.join(", ") : "No requests";
}

// ---------------------------------------------------------------------------
// Outcome explanations

export type OutcomeExplanation = {
  kind: OutcomeKind;
  title: string;
  body: string;
};

const HTTP_STATUS = /^(\d{3})(?: ([^:]+))?(?::|$)/;

/**
 * Plain-language account of what happened to a request. Uses only the row's
 * own fields; `detail` is the server's raw text and is shown alongside.
 */
export function explainOutcome(row: {
  outcome: string;
  detail: string;
  lookup_outcome: string;
  source: string;
  recording_id: number;
}): OutcomeExplanation {
  const kind = outcomeKind(row.outcome);
  const detail = row.detail.trim();
  const rec = row.recording_id ? `recording #${row.recording_id}` : "";
  switch (row.outcome) {
    case "hit":
      return {
        kind,
        title: "Replayed from a recording",
        body: `This request matched ${rec || "a recording"} exactly, so the proxy served the saved response and made no upstream call.`,
      };
    case "miss":
      return {
        kind,
        title: "No recording matched",
        body: "Replay mode looked up this request's match key and found no recording with exactly the same request, so the caller received HTTP 404 recording_not_found. Nothing was sent upstream.",
      };
    case "recorded": {
      const how =
        row.lookup_outcome === "miss"
          ? "Auto mode found no matching recording, so it fetched the response from upstream"
          : "Record mode skips the lookup and fetched the response from upstream";
      const tail = /caller disconnected after the complete stream/i.test(detail)
        ? " The caller hung up after the final event; the complete stream was still saved."
        : "";
      return {
        kind,
        title: "Fetched upstream and recorded",
        body: `${how}, then saved it as ${rec || "a new recording"} for future replays.${tail}`,
      };
    }
    case "interrupted":
      return {
        kind,
        title: "Caller disconnected",
        body:
          row.source === "replay"
            ? `The caller hung up while ${rec || "a recording"} was being replayed, before the stream finished. The recording itself is unchanged.`
            : "The caller hung up before the upstream response finished streaming, so the response was incomplete and was not recorded. Any earlier recording of this request is kept.",
      };
    case "incomplete":
      return {
        kind,
        title: "Response incomplete",
        body: "The upstream response arrived but did not pass validation (for example, a stream that ended without its completion event), so it was not saved. Any earlier recording of this request is kept.",
      };
  }
  if (/references recorded provider state/i.test(detail))
    return {
      kind,
      title: "Needs provider state the live API doesn't have",
      body: "The request points at provider-side state (such as previous_response_id) that came from a recorded response, so the live provider would not recognise it. Warm this exact request while recording, or send the whole conversation in the request (store: false).",
    };
  if (row.source === "replay")
    return {
      kind,
      title: "Replay failed",
      body: `The proxy could not finish replaying ${rec || "the recording"}.`,
    };
  const status = HTTP_STATUS.exec(detail);
  if (status)
    return {
      kind,
      title: `Upstream returned HTTP ${status[1]}`,
      body: `The provider answered ${status[1]}${status[2] ? ` ${status[2].trim()}` : ""}, which the proxy passed through to the caller. Error responses are never recorded.`,
    };
  return {
    kind,
    title: "Request failed",
    body: "The request did not complete and nothing was recorded. The server's detail is below.",
  };
}

/** Whether "why didn't this replay?" applies: nothing was served from a recording. */
export function needsMissExplanation(row: {
  recording_id: number;
  outcome: string;
}): boolean {
  return !row.recording_id && row.outcome !== "hit";
}

// ---------------------------------------------------------------------------
// Thread list

export type ThreadOutcomeFilter =
  "all" | "misses" | "errors" | "interrupted" | "replayed" | "recorded";

export const THREAD_OUTCOME_FILTERS: {
  value: ThreadOutcomeFilter;
  label: string;
}[] = [
  { value: "all", label: "Any outcome" },
  { value: "misses", label: "With misses" },
  { value: "errors", label: "With failures" },
  { value: "interrupted", label: "With interruptions" },
  { value: "recorded", label: "With recordings" },
  { value: "replayed", label: "Fully replayed" },
];

/** Maps a ViewLink outcome ("miss", "error", …) to a thread filter. */
export function threadFilterFromOutcome(o?: string): ThreadOutcomeFilter {
  switch (o) {
    case "miss":
    case "misses":
      return "misses";
    case "error":
    case "errors":
    case "incomplete":
      return "errors";
    case "interrupted":
      return "interrupted";
    case "recorded":
      return "recorded";
    case "hit":
    case "hits":
    case "replayed":
      return "replayed";
    default:
      return "all";
  }
}

export type ThreadSort =
  "recent" | "requests" | "misses" | "longest" | "tokens";
export const THREAD_SORTS: { value: ThreadSort; label: string }[] = [
  { value: "recent", label: "Recent activity" },
  { value: "requests", label: "Most requests" },
  { value: "misses", label: "Most misses" },
  { value: "longest", label: "Longest" },
  { value: "tokens", label: "Most upstream tokens" },
];

export const tokenTotal = (t: TokenTotals | null | undefined) =>
  t ? (t.input || 0) + (t.output || 0) : 0;

export function filterThreads(
  threads: ThreadSummary[],
  f: { query?: string; outcome?: ThreadOutcomeFilter; model?: string },
): ThreadSummary[] {
  const terms = (f.query || "").toLowerCase().split(/\s+/).filter(Boolean);
  return threads.filter((t) => {
    if (f.model && t.model !== f.model) return false;
    switch (f.outcome || "all") {
      case "misses":
        if (!t.misses) return false;
        break;
      case "errors":
        if (!t.errors) return false;
        break;
      case "interrupted":
        if (!t.interrupted) return false;
        break;
      case "recorded":
        if (!t.recorded) return false;
        break;
      case "replayed":
        if (!t.requests || t.hits !== t.requests) return false;
        break;
    }
    if (!terms.length) return true;
    const hay =
      `${t.opening}\n${t.latest}\n${t.model}\n${t.thread}\n${t.route}`.toLowerCase();
    return terms.every((term) => hay.includes(term));
  });
}

export function sortThreads(
  threads: ThreadSummary[],
  sort: ThreadSort,
): ThreadSummary[] {
  const by = (f: (t: ThreadSummary) => number) =>
    [...threads].sort(
      (a, b) => f(b) - f(a) || b.last_at.localeCompare(a.last_at),
    );
  switch (sort) {
    case "requests":
      return by((t) => t.requests);
    case "misses":
      return by((t) => t.misses);
    case "longest":
      return by((t) => t.max_items);
    case "tokens":
      return by((t) => tokenTotal(t.upstream_tokens));
    default:
      return [...threads].sort((a, b) => b.last_at.localeCompare(a.last_at));
  }
}

export function distinctModels(threads: ThreadSummary[]): string[] {
  return [...new Set(threads.map((t) => t.model).filter(Boolean))].sort();
}

// ---------------------------------------------------------------------------
// Timeline

export type TimelineTurn = {
  /** 1-based position in the thread. */
  n: number;
  turn: ThreadTurn;
  kind: OutcomeKind;
  items: number;
  /** Items added since the previous request (all items for the first). */
  added: number;
  /** Same item count as the previous request after it failed: a retry. */
  retry: boolean;
  duration: number | null;
  firstEvent: number | null;
  input: number | null;
  cachedInput: number | null;
  output: number | null;
  reasoning: number | null;
};

export type Timeline = {
  turns: TimelineTurn[];
  maxItems: number;
  maxDuration: number;
  maxOutput: number;
  counts: Record<OutcomeKind, number>;
  /** 1-based position of the first miss, if any. */
  firstMiss: number | null;
  /** Median request duration in ms per source, if measured. */
  medianReplay: number | null;
  medianUpstream: number | null;
};

function median(values: number[]): number | null {
  if (!values.length) return null;
  const s = [...values].sort((a, b) => a - b);
  const mid = s.length >> 1;
  return s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2;
}

export function shapeTimeline(turns: ThreadTurn[]): Timeline {
  const counts: Record<OutcomeKind, number> = {
    hit: 0,
    miss: 0,
    recorded: 0,
    interrupted: 0,
    error: 0,
  };
  const replay: number[] = [];
  const upstream: number[] = [];
  let prev: TimelineTurn | null = null;
  let firstMiss: number | null = null;
  const shaped = turns.map((turn, i) => {
    const kind = outcomeKind(turn.outcome);
    counts[kind]++;
    if (kind === "miss" && firstMiss === null) firstMiss = i + 1;
    const usage = turn.response?.usage;
    const items = Math.max(0, turn.items || 0);
    const t: TimelineTurn = {
      n: i + 1,
      turn,
      kind,
      items,
      added: prev ? Math.max(0, items - prev.items) : items,
      retry:
        !!prev &&
        prev.items === items &&
        prev.kind !== "hit" &&
        prev.kind !== "recorded",
      duration: turn.duration_ms,
      firstEvent: turn.first_event_ms,
      input: usage?.input ?? null,
      cachedInput: usage?.cached_input ?? null,
      output: usage?.output ?? null,
      reasoning: usage?.reasoning ?? null,
    };
    if (turn.duration_ms !== null) {
      if (kind === "hit") replay.push(turn.duration_ms);
      else if (kind === "recorded") upstream.push(turn.duration_ms);
    }
    prev = t;
    return t;
  });
  return {
    turns: shaped,
    maxItems: Math.max(1, ...shaped.map((t) => t.items)),
    maxDuration: Math.max(1, ...shaped.map((t) => t.duration ?? 0)),
    maxOutput: Math.max(1, ...shaped.map((t) => t.output ?? 0)),
    counts,
    firstMiss,
    medianReplay: median(replay),
    medianUpstream: median(upstream),
  };
}

/** One-sentence description of a turn for screen readers and tooltips. */
export function describeTurn(t: TimelineTurn): string {
  const parts = [
    `Turn ${t.n}`,
    OUTCOME_LABEL[t.kind].toLowerCase(),
    `${t.items} items${t.n > 1 ? ` (+${t.added})` : ""}`,
  ];
  if (t.retry) parts.push("retry");
  if (t.duration !== null) parts.push(`took ${formatMs(t.duration)}`);
  if (t.firstEvent !== null)
    parts.push(`first event ${formatMs(t.firstEvent)}`);
  if (t.output !== null) parts.push(`${compactNumber(t.output)} output tokens`);
  return parts.join(", ");
}

// ---------------------------------------------------------------------------
// Formatting

export function formatMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s < 10 ? s.toFixed(2) : s.toFixed(1)} s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s - m * 60);
  if (m < 60) return `${m} m ${String(rest).padStart(2, "0")} s`;
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} m`;
}

export function compactNumber(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return "—";
  const a = Math.abs(n);
  if (a < 1000) return String(Math.round(n));
  if (a < 10_000) return `${(n / 1000).toFixed(1)}k`;
  if (a < 1_000_000) return `${Math.round(n / 1000)}k`;
  if (a < 10_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  return `${Math.round(n / 1_000_000)}M`;
}

export function relativeTime(iso: string, now = Date.now()): string {
  const t = Date.parse(iso);
  if (!iso || Number.isNaN(t)) return "—";
  const s = Math.round((now - t) / 1000);
  if (s < 45) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h} h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d} d ago`;
  return new Date(t).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}

/** Wall-clock span of a thread, e.g. "14 min". */
export function spanLabel(first: string, last: string): string {
  const a = Date.parse(first),
    b = Date.parse(last);
  if (Number.isNaN(a) || Number.isNaN(b) || b < a) return "—";
  const s = (b - a) / 1000;
  if (s < 60) return `${Math.round(s)} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) return `${(s / 3600).toFixed(1)} h`;
  return `${Math.round(s / 86400)} d`;
}
