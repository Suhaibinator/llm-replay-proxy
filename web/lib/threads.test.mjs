import test from "node:test";
import assert from "node:assert/strict";
import {
  compactNumber,
  describeCounts,
  explainOutcome,
  filterThreads,
  formatMs,
  needsMissExplanation,
  outcomeKind,
  relativeTime,
  shapeTimeline,
  sortThreads,
  stripSegments,
  threadFilterFromOutcome,
} from "./threads.ts";

const counts = (hits, misses, recorded, interrupted, errors) => ({
  requests: hits + misses + recorded + interrupted + errors,
  hits,
  misses,
  recorded,
  interrupted,
  errors,
});
const tokens = { input: 0, cached_input: 0, output: 0, reasoning: 0 };
const thread = (over) => ({
  ...counts(1, 0, 0, 0, 0),
  thread: "t",
  route: "/v1/responses",
  model: "gpt-5",
  opening: "Plan a trip",
  latest: "And hotels?",
  max_items: 4,
  first_at: "2026-10-04T10:00:00Z",
  last_at: "2026-10-04T10:05:00Z",
  last_outcome: "hit",
  upstream_tokens: tokens,
  ...over,
});
const turn = (outcome, items, over = {}) => ({
  history_id: items,
  created_at: "2026-10-04T10:00:00Z",
  outcome,
  lookup_outcome: outcome === "hit" ? "hit" : "miss",
  detail: "",
  recording_id: outcome === "hit" || outcome === "recorded" ? 7 : 0,
  items,
  preview: "",
  duration_ms: null,
  first_event_ms: null,
  response: null,
  ...over,
});

test("outcomes fold into five kinds", () => {
  assert.equal(outcomeKind("hit"), "hit");
  assert.equal(outcomeKind("incomplete"), "error");
  assert.equal(outcomeKind("something-new"), "error");
  assert.deepEqual(
    stripSegments(counts(3, 1, 0, 0, 0)).map((s) => [s.kind, s.share]),
    [
      ["hit", 0.75],
      ["miss", 0.25],
    ],
  );
  assert.equal(
    describeCounts(counts(18, 1, 4, 0, 0)),
    "18 replayed, 4 recorded, 1 missed",
  );
});

test("plain-language explanations", () => {
  const base = {
    detail: "",
    lookup_outcome: "miss",
    source: "proxy",
    recording_id: 0,
  };
  assert.match(
    explainOutcome({ ...base, outcome: "miss", detail: "replay mode" }).body,
    /404/,
  );
  assert.match(
    explainOutcome({ ...base, outcome: "interrupted", source: "upstream" })
      .body,
    /hung up before/,
  );
  assert.match(
    explainOutcome({
      ...base,
      outcome: "interrupted",
      source: "replay",
      recording_id: 3,
    }).body,
    /recording #3/,
  );
  assert.equal(
    explainOutcome({
      ...base,
      outcome: "error",
      source: "upstream",
      detail: "429 Too Many Requests",
    }).title,
    "Upstream returned HTTP 429",
  );
  assert.match(
    explainOutcome({
      ...base,
      outcome: "error",
      detail: "request references recorded provider state resp_1",
    }).title,
    /provider state/,
  );
  assert.match(
    explainOutcome({
      ...base,
      outcome: "recorded",
      recording_id: 9,
      lookup_outcome: "bypass",
    }).body,
    /Record mode.*recording #9/,
  );
  assert.equal(
    needsMissExplanation({ outcome: "miss", recording_id: 0 }),
    true,
  );
  assert.equal(
    needsMissExplanation({ outcome: "interrupted", recording_id: 4 }),
    false,
  );
});

test("thread filters and sorting", () => {
  const a = thread({
    thread: "a",
    ...counts(6, 0, 0, 0, 0),
    last_at: "2026-10-04T10:00:00Z",
  });
  const b = thread({
    thread: "b",
    ...counts(2, 2, 1, 0, 0),
    model: "claude",
    opening: "Debug the build",
    last_at: "2026-10-04T11:00:00Z",
    max_items: 40,
  });
  assert.deepEqual(
    filterThreads([a, b], { outcome: "misses" }).map((t) => t.thread),
    ["b"],
  );
  assert.deepEqual(
    filterThreads([a, b], { outcome: "replayed" }).map((t) => t.thread),
    ["a"],
  );
  assert.deepEqual(
    filterThreads([a, b], { model: "claude" }).map((t) => t.thread),
    ["b"],
  );
  assert.deepEqual(
    filterThreads([a, b], { query: "debug BUILD" }).map((t) => t.thread),
    ["b"],
  );
  assert.deepEqual(
    sortThreads([a, b], "recent").map((t) => t.thread),
    ["b", "a"],
  );
  assert.deepEqual(
    sortThreads([a, b], "requests").map((t) => t.thread),
    ["a", "b"],
  );
  assert.deepEqual(
    sortThreads([b, a], "longest").map((t) => t.thread),
    ["b", "a"],
  );
  assert.equal(threadFilterFromOutcome("miss"), "misses");
  assert.equal(threadFilterFromOutcome(undefined), "all");
});

test("timeline shape: growth, retries, first miss and medians", () => {
  const tl = shapeTimeline([
    turn("recorded", 2, {
      duration_ms: 1200,
      response: {
        usage: {
          input: 50,
          cached_input: null,
          output: 20,
          reasoning: 5,
          total: 70,
        },
      },
    }),
    turn("hit", 4, { duration_ms: 10 }),
    turn("miss", 6),
    turn("error", 8),
    turn("recorded", 8, { duration_ms: 3000 }),
  ]);
  assert.deepEqual(
    tl.turns.map((t) => t.added),
    [2, 2, 2, 2, 0],
  );
  assert.deepEqual(
    tl.turns.map((t) => t.retry),
    [false, false, false, false, true],
  );
  assert.equal(tl.firstMiss, 3);
  assert.equal(tl.maxItems, 8);
  assert.equal(tl.maxOutput, 20);
  assert.equal(tl.turns[0].reasoning, 5);
  assert.equal(tl.medianUpstream, 2100);
  assert.equal(tl.medianReplay, 10);
  assert.deepEqual(tl.counts, {
    hit: 1,
    miss: 1,
    recorded: 2,
    interrupted: 0,
    error: 1,
  });
  assert.equal(shapeTimeline([]).maxItems, 1);
});

test("formatting", () => {
  assert.equal(formatMs(null), "—");
  assert.equal(formatMs(820), "820 ms");
  assert.equal(formatMs(1234), "1.23 s");
  assert.equal(formatMs(65_000), "1 m 05 s");
  assert.equal(compactNumber(950), "950");
  assert.equal(compactNumber(12_345), "12k");
  assert.equal(compactNumber(1_500_000), "1.5M");
  const now = Date.parse("2026-10-04T12:00:00Z");
  assert.equal(relativeTime("2026-10-04T11:59:50Z", now), "just now");
  assert.equal(relativeTime("2026-10-04T11:00:00Z", now), "1 h ago");
});
