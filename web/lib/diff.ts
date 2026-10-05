// Explains POST /api/compare differences between a request that did not
// replay and its closest recording: categorises and ranks each difference,
// spots volatile values (timestamps, ids, random values) that change on every
// run, and suggests match exclusions. Pure; node:test loads it directly.
import type { Diff } from "./api";

export type DiffCategory =
  | "route"
  | "volatile"
  | "model"
  | "stream"
  | "parameter"
  | "instructions"
  | "tools"
  | "conversation"
  | "appended"
  | "truncated"
  | "excluded";

export type VolatileKind = "timestamp" | "date" | "time" | "id" | "random";

export type Volatile = {
  kind: VolatileKind;
  /** Why it looks volatile, e.g. "ISO timestamp" or "field named run_id". */
  reason: string;
  /** The volatile token in the request and recording, when found in text. */
  request?: string;
  recorded?: string;
  /** The whole value is volatile, not just a fragment of a longer text. */
  whole: boolean;
};

/** A changed string, trimmed to its differing middle with some context. */
export type InlineChange = {
  before: string;
  request: string;
  recorded: string;
  after: string;
  /** Context was cut at the start / end. */
  clippedStart: boolean;
  clippedEnd: boolean;
};

export type Suggestion = {
  pointer: string;
  /** Plain-language caveats, most important first. */
  caveats: string[];
};

export type ExplainedDiff = {
  diff: Diff;
  category: DiffCategory;
  /** Higher sorts first. */
  score: number;
  label: string;
  volatile: Volatile | null;
  inline: InlineChange | null;
  suggestion: Suggestion | null;
  /** Covered by an exclusion the collection already has. */
  excluded: boolean;
};

export type Verdict = {
  tone: "culprit" | "continued" | "different" | "identical" | "excluded";
  headline: string;
  body: string;
};

export type DiffReport = {
  verdict: Verdict;
  /** Prioritised; appended conversation items folded into `appended`. */
  items: ExplainedDiff[];
  appended: { count: number; collection: string; from: number } | null;
  truncated: { count: number; collection: string } | null;
};

// ---------------------------------------------------------------------------
// JSON Pointer helpers

export function pointerSegments(pointer: string): string[] {
  if (!pointer || pointer === "/") return [];
  return pointer
    .slice(1)
    .split("/")
    .map((s) => s.replace(/~1/g, "/").replace(/~0/g, "~"));
}

/** "/input/3/content/0/text" → "input › 4th item › content › text". */
export function pointerLabel(pointer: string): string {
  if (pointer === "/$route") return "Route";
  const segs = pointerSegments(pointer);
  if (!segs.length) return "Whole request";
  const out: string[] = [];
  segs.forEach((s, i) => {
    if (/^\d+$/.test(s)) {
      const parent = segs[i - 1];
      if (parent && CONVERSATION_ARRAYS.has(parent) && i === 1)
        out.push(`item ${Number(s) + 1}`);
      else if (Number(s) === 0 && i === segs.length - 1) out.push("[0]");
      else out.push(`[${s}]`);
    } else out.push(s);
  });
  return out.join(" › ").replace(/ › \[/g, "[");
}

/** True when `pointer` is `prefix` or lies below it. */
export function pointerWithin(pointer: string, prefix: string): boolean {
  return pointer === prefix || pointer.startsWith(prefix + "/");
}

const CONVERSATION_ARRAYS = new Set(["input", "messages", "contents"]);
const INSTRUCTION_FIELDS = new Set(["instructions", "system"]);
const TOOL_FIELDS = new Set(["tools", "functions", "tool_choice"]);

// ---------------------------------------------------------------------------
// Volatile value detection

const VOLATILE_PATTERNS: { kind: VolatileKind; reason: string; re: RegExp }[] =
  [
    {
      kind: "timestamp",
      reason: "ISO 8601 timestamp",
      re: /\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?/,
    },
    {
      kind: "timestamp",
      reason: "Unix timestamp",
      re: /(?<![\d.])1[5-9]\d{8}(?:\d{3}|\d{6})?(?:\.\d+)?(?![\d])/,
    },
    {
      kind: "id",
      reason: "UUID",
      re: /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i,
    },
    {
      kind: "id",
      reason: "generated identifier",
      re: /\b(?:resp|msg|call|fc|rs|run|req|chatcmpl|toolu|sess|evt|trace|span|ws|thread|asst|file)_[A-Za-z0-9-]{6,}/,
    },
    {
      kind: "date",
      reason: "date",
      re: /\b(?:\d{4}-\d{2}-\d{2}|\d{1,2}\/\d{1,2}\/\d{2,4}|(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\.? \d{1,2},? \d{4}|\d{1,2} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]* \d{4})\b/,
    },
    {
      kind: "time",
      reason: "time of day",
      re: /\b\d{1,2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:\s?[AaPp][Mm])?\b/,
    },
    { kind: "id", reason: "random hex value", re: /\b[0-9a-f]{16,}\b/i },
  ];

const VOLATILE_KEYS: { kind: VolatileKind; re: RegExp }[] = [
  {
    kind: "timestamp",
    re: /^(?:ts|time|timestamp|now|date|datetime|created|updated|created_at|updated_at|sent_at|expires|expires_at|current_time|current_date)$|_(?:at|time|timestamp|ts)$/i,
  },
  {
    kind: "id",
    re: /^(?:id|uuid|guid|nonce|user|session|idempotency_key|prompt_cache_key|safety_identifier)$|_(?:id|uuid|nonce|key)$/i,
  },
  { kind: "random", re: /^(?:seed|random|salt|rand|random_seed)$/i },
];

const BOUNDARY = /[\s"'`,;()[\]{}<>|]/;

/** Common prefix/suffix of two strings, aligned to whole code points. */
function commonEnds(a: string, b: string): [number, number] {
  let p = 0;
  const max = Math.min(a.length, b.length);
  while (p < max && a[p] === b[p]) p++;
  if (p > 0 && p < max && /[\uDC00-\uDFFF]/.test(a[p])) p--;
  let s = 0;
  while (s < max - p && a[a.length - 1 - s] === b[b.length - 1 - s]) s++;
  if (s > 0 && /[\uD800-\uDBFF]/.test(a[a.length - s - 1] || "")) s--;
  return [p, s];
}

/** Expand [start, end) of s outward to token boundaries. */
function tokenAround(
  s: string,
  start: number,
  end: number,
): { text: string; from: number; to: number } {
  let a = start,
    b = end;
  while (a > 0 && !BOUNDARY.test(s[a - 1])) a--;
  while (b < s.length && !BOUNDARY.test(s[b])) b++;
  // Join a date to a following time of day ("2026-10-04 10:00") and the
  // reverse, so either half changing reads as one timestamp.
  const next = /^ \d{1,2}:\d{2}(?::\d{2})?(?:\s?[AaPp][Mm])?/.exec(s.slice(b));
  if (next) b += next[0].length;
  const prev = /\d{4}-\d{2}-\d{2} $/.exec(s.slice(0, a));
  if (prev) a -= prev[0].length;
  return { text: s.slice(a, b), from: start - a, to: end - a };
}

/** A volatile pattern in `token` that covers the changed span [from, to). */
function matchVolatile(
  token: string,
  from = 0,
  to = token.length,
): { kind: VolatileKind; reason: string; match: string } | null {
  for (const p of VOLATILE_PATTERNS) {
    const re = new RegExp(p.re.source, p.re.flags.replace("g", "") + "g");
    for (let m = re.exec(token); m; m = re.exec(token)) {
      if (m.index <= from && m.index + m[0].length >= to)
        return { kind: p.kind, reason: p.reason, match: m[0] };
      if (!m[0].length) re.lastIndex++;
    }
  }
  return null;
}

/**
 * Looks for a value that changes run to run. Strings are compared by their
 * differing fragment, so a timestamp inside a long prompt is still found.
 */
export function detectVolatile(d: Diff): Volatile | null {
  if (!d.request_exists || !d.recorded_exists) return null;
  const segs = pointerSegments(d.path);
  const key = segs[segs.length - 1] || "";
  const scalar = (v: unknown) =>
    v === null || ["string", "number", "boolean"].includes(typeof v);
  if (typeof d.request === "string" && typeof d.recorded === "string") {
    const a = d.request,
      b = d.recorded;
    const [p, s] = commonEnds(a, b);
    const ta = tokenAround(a, p, a.length - s);
    const tb = tokenAround(b, p, b.length - s);
    const va = matchVolatile(ta.text, ta.from, ta.to),
      vb = matchVolatile(tb.text, tb.from, tb.to);
    if (va && vb && va.kind === vb.kind) {
      const whole =
        va.match.trim() === a.trim() && vb.match.trim() === b.trim();
      return {
        kind: va.kind,
        reason: va.reason,
        request: va.match,
        recorded: vb.match,
        whole,
      };
    }
  }
  if (scalar(d.request) && scalar(d.recorded)) {
    for (const k of VOLATILE_KEYS)
      if (k.re.test(key))
        return {
          kind: k.kind,
          reason: `field named “${key}”`,
          whole: true,
        };
    const whole = (v: unknown) =>
      typeof v === "number" || typeof v === "string"
        ? matchVolatile(String(v))
        : null;
    const va = whole(d.request_display.replace(/^"|"$/g, "")),
      vb = whole(d.recorded_display.replace(/^"|"$/g, ""));
    if (va && vb && va.kind === vb.kind)
      return {
        kind: va.kind,
        reason: va.reason,
        request: va.match,
        recorded: vb.match,
        whole: true,
      };
  }
  return null;
}

// ---------------------------------------------------------------------------
// Inline string change

const CONTEXT = 48;
// Highlights widen to whole tokens ("2026-10-04T09:41:07Z", not "4T09:41:07")
// unless that would grow them by more than this many characters per side.
const TOKEN_GROW = 32;

export function inlineChange(d: Diff): InlineChange | null {
  if (typeof d.request !== "string" || typeof d.recorded !== "string")
    return null;
  const a = d.request,
    b = d.recorded;
  let [p, s] = commonEnds(a, b);
  const shared = s;
  // The shared prefix and suffix are identical in both strings, so moving
  // the boundaries into them keeps the two sides aligned.
  let grow = 0;
  while (p > 0 && grow < TOKEN_GROW && !BOUNDARY.test(a[p - 1])) (p--, grow++);
  if (grow === TOKEN_GROW) p += grow;
  grow = 0;
  while (s > 0 && grow < TOKEN_GROW && !BOUNDARY.test(a[a.length - s]))
    (s--, grow++);
  if (grow === TOKEN_GROW) s += grow;
  // Give back sentence punctuation the expansion swallowed ("…07Z." → "…07Z").
  while (
    s < shared &&
    /[.,:;!?]/.test(a[a.length - s - 1]) &&
    (s === 0 || BOUNDARY.test(a[a.length - s]))
  )
    s++;
  const start = Math.max(0, p - CONTEXT);
  const endA = a.length - s;
  const after = a.slice(endA, Math.min(a.length, endA + CONTEXT));
  return {
    before: a.slice(start, p),
    request: a.slice(p, endA),
    recorded: b.slice(p, b.length - s),
    after,
    clippedStart: start > 0,
    clippedEnd: endA + CONTEXT < a.length,
  };
}

// ---------------------------------------------------------------------------
// Categorise and rank

const SCORE: Record<DiffCategory, number> = {
  route: 100,
  volatile: 90,
  model: 80,
  stream: 75,
  parameter: 60,
  instructions: 55,
  tools: 50,
  conversation: 40,
  truncated: 30,
  appended: 10,
  excluded: 0,
};

export const CATEGORY_LABEL: Record<DiffCategory, string> = {
  route: "Different endpoint",
  volatile: "Likely culprit",
  model: "Model",
  stream: "Streaming",
  parameter: "Parameter",
  instructions: "Instructions",
  tools: "Tools",
  conversation: "Conversation",
  appended: "Only in this request",
  truncated: "Only in the recording",
  excluded: "Already excluded",
};

export const VOLATILE_LABEL: Record<VolatileKind, string> = {
  timestamp: "timestamp",
  date: "date",
  time: "time of day",
  id: "generated id",
  random: "random value",
};

function categorise(d: Diff, segs: string[]): DiffCategory {
  if (d.path === "/$route") return "route";
  const top = segs[0] || "";
  if (top === "model" && segs.length === 1) return "model";
  if (top === "stream" || top === "stream_options") return "stream";
  if (CONVERSATION_ARRAYS.has(top) && segs.length === 2) {
    if (d.request_exists && !d.recorded_exists) return "appended";
    if (!d.request_exists && d.recorded_exists) return "truncated";
  }
  if (CONVERSATION_ARRAYS.has(top)) return "conversation";
  if (INSTRUCTION_FIELDS.has(top)) return "instructions";
  if (TOOL_FIELDS.has(top)) return "tools";
  return "parameter";
}

function suggest(d: Diff, segs: string[], v: Volatile): Suggestion {
  const caveats: string[] = [];
  if (!v.whole)
    caveats.push(
      `The ${VOLATILE_LABEL[v.kind]} is embedded in a longer text. Excluding this pointer makes matching ignore the entire text, so a genuinely different message here would still replay. Making the value deterministic in your app (a frozen clock or fixed ids) is safer.`,
    );
  if (segs.some((s) => /^\d+$/.test(s)))
    caveats.push(
      "The pointer names fixed array positions. It only covers the value while it stays at exactly that position in the request.",
    );
  if (d.path === "/" || !segs.length)
    caveats.push("The document root cannot be excluded.");
  return { pointer: d.path, caveats };
}

export function explainDiff(d: Diff, exclusions: string[] = []): ExplainedDiff {
  const segs = pointerSegments(d.path);
  const excluded = exclusions.some((x) => pointerWithin(d.path, x));
  let category = categorise(d, segs);
  const volatile =
    category === "route" || category === "model" ? null : detectVolatile(d);
  if (volatile) category = "volatile";
  if (excluded) category = "excluded";
  return {
    diff: d,
    category,
    score: SCORE[category],
    label: pointerLabel(d.path),
    volatile,
    inline: inlineChange(d),
    suggestion:
      volatile && !excluded && segs.length && d.path !== "/stream"
        ? suggest(d, segs, volatile)
        : null,
    excluded,
  };
}

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n.toLocaleString()} ${n === 1 ? one : many}`;

/** Ranks every difference and states the most likely reason in one line. */
export function explainDiffs(
  diffs: Diff[],
  exclusions: string[] = [],
): DiffReport {
  const all = diffs.map((d, i) => ({ e: explainDiff(d, exclusions), i }));
  const appendedRows = all.filter((x) => x.e.category === "appended");
  const truncatedRows = all.filter((x) => x.e.category === "truncated");
  const arrayName = (x: { e: ExplainedDiff }) =>
    pointerSegments(x.e.diff.path)[0];
  const appended = appendedRows.length
    ? {
        count: appendedRows.length,
        collection: arrayName(appendedRows[0]),
        from: Math.min(
          ...appendedRows.map((x) => Number(pointerSegments(x.e.diff.path)[1])),
        ),
      }
    : null;
  const truncated = truncatedRows.length
    ? { count: truncatedRows.length, collection: arrayName(truncatedRows[0]) }
    : null;
  const items = all
    .filter((x) => x.e.category !== "appended" && x.e.category !== "truncated")
    .sort((a, b) => b.e.score - a.e.score || a.i - b.i)
    .map((x) => x.e);
  const live = items.filter((e) => !e.excluded);
  const volatile = live.filter((e) => e.category === "volatile");
  const route = live.find((e) => e.category === "route");
  const model = live.find((e) => e.category === "model");

  let verdict: Verdict;
  const noun = (c: string) => (c === "messages" ? "message" : "input item");
  if (!diffs.length)
    verdict = {
      tone: "identical",
      headline: "The request bodies are identical",
      body: "The match key also covers the route and the upstream's identity (its configured URL and non-secret settings). The upstream configuration has most likely changed since this was recorded.",
    };
  else if (route)
    verdict = {
      tone: "different",
      headline: "Sent to a different endpoint",
      body: `This request went to ${route.diff.request_display}, the recording to ${route.diff.recorded_display}. Recordings only replay on the route they were recorded on.`,
    };
  else if (volatile.length)
    verdict = {
      tone: "culprit",
      headline: `Likely cause: ${volatile.length === 1 ? `a ${VOLATILE_LABEL[volatile[0].volatile!.kind]} that changes on every run` : `${volatile.length} values that change on every run`}`,
      body: `${volatile.length === 1 ? `“${volatile[0].label}” differs` : "These fields differ"} only by a ${[...new Set(volatile.map((e) => VOLATILE_LABEL[e.volatile!.kind]))].join(" or ")}${live.length > volatile.length ? `; ${plural(live.length - volatile.length, "other difference")} also ${live.length - volatile.length === 1 ? "needs" : "need"} checking` : ", and nothing else changed"}.`,
    };
  else if (!live.length && appended && !truncated)
    verdict = {
      tone: "continued",
      headline: "The conversation continued past the recording",
      body: `Everything the recording contains matches, but this request adds ${plural(appended.count, noun(appended.collection))} after it. This turn was never recorded; run it once in Record or Auto mode to make it replayable.`,
    };
  else if (!live.length && items.length)
    verdict = {
      tone: "excluded",
      headline: "Only excluded fields differ",
      body: "Every difference is under one of this collection's exclusions, so matching ignores it. The closest recording's match key differs for another reason, such as the upstream configuration.",
    };
  else if (model)
    verdict = {
      tone: "different",
      headline: "A different model was requested",
      body: `This request asked for ${model.diff.request_display}; the recording was made with ${model.diff.recorded_display}.`,
    };
  else {
    const n = live.length + (appended ? 1 : 0) + (truncated ? 1 : 0);
    const first = live[0];
    verdict = {
      tone: "different",
      headline: `${plural(n, "difference")} from the closest recording`,
      body: first
        ? `The most significant is in “${first.label}”. Any change to the request, however small, produces a different match key.`
        : "The conversation itself differs in length from the recording.",
    };
  }
  return { verdict, items, appended, truncated };
}
