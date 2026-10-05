import test from "node:test";
import assert from "node:assert/strict";
import {
  activity,
  bucketLabel,
  emptyTrafficFilter,
  facet,
  filterTraffic,
  formatMs,
  historyURL,
  liveSplit,
  mergeHistory,
  newestId,
  outcomeCounts,
  outcomeTone,
  relativeTime,
  shortModel,
  timingScale,
} from "./traffic.ts";

const T0 = Date.parse("2026-10-04T12:00:00Z");
const ago = (ms) => new Date(T0 - ms).toISOString();
const row = (id, over = {}) => ({
  id,
  collection_id: 1,
  route: "/v1/responses",
  key: `key${id}`,
  request: {
    model: "gpt-a",
    items: 2,
    preview: `question ${id}`,
    opening: "hello",
    tool_calls: 0,
    images: 0,
    bytes: 100,
    thread: "t1",
  },
  outcome: "hit",
  detail: "",
  recording_id: 1,
  created_at: ago((100 - id) * 60_000),
  source: "replay",
  duration_ms: 100,
  first_event_ms: 10,
  lookup_outcome: "hit",
  ...over,
});
const ids = (rows) => rows.map((h) => h.id);

test("mergeHistory prepends new rows, dedupes and caps", () => {
  const current = [row(3), row(2), row(1)];
  const merged = mergeHistory(current, [row(5), row(4), row(3)], 4);
  assert.deepEqual(ids(merged), [5, 4, 3, 2]);
  assert.equal(merged[2], current[0], "known rows keep their identity");
  assert.deepEqual(mergeHistory(current, []), current);
  assert.deepEqual(ids(mergeHistory([], [row(1), row(9), row(4)])), [9, 4, 1]);
  assert.equal(newestId(merged), 5);
  assert.equal(newestId([]), 0);
});

test("historyURL asks for new rows only after the newest id", () => {
  assert.equal(historyURL(3, 0), "/api/history?collection_id=3&limit=500");
  assert.equal(
    historyURL(3, 41, 100),
    "/api/history?collection_id=3&limit=100&after_id=41",
  );
});

test("liveSplit freezes the list while paused", () => {
  const rows = [row(5), row(4), row(3)];
  assert.deepEqual(liveSplit(rows, null), { shown: rows, waiting: 0 });
  const paused = liveSplit(rows, 3);
  assert.deepEqual(ids(paused.shown), [3]);
  assert.equal(paused.waiting, 2);
});

test("filters combine; outcome counts ignore only the outcome selection", () => {
  const rows = [
    row(1, { outcome: "miss", recording_id: 0, source: "proxy" }),
    row(2, { request: { ...row(2).request, model: "gpt-b", thread: "t2" } }),
    row(3, { outcome: "error", detail: "upstream timeout", request: null }),
    row(4, { route: "/v1/chat/completions" }),
  ];
  const all = emptyTrafficFilter;
  const hits = { ...all, outcomes: ["hit"] };
  assert.deepEqual(ids(filterTraffic(rows, hits)), [2, 4]);
  assert.deepEqual(ids(filterTraffic(rows, { ...hits, model: "gpt-b" })), [2]);
  assert.deepEqual(ids(filterTraffic(rows, { ...all, query: "TIMEOUT" })), [3]);
  assert.deepEqual(ids(filterTraffic(rows, { ...all, query: "key4" })), [4]);
  assert.deepEqual(ids(filterTraffic(rows, { ...all, thread: "t2" })), [2]);
  assert.deepEqual(ids(filterTraffic(rows, { ...all, source: "proxy" })), [1]);
  const counts = Object.fromEntries(
    outcomeCounts(rows, { ...hits, route: "/v1/responses" }).map((c) => [
      c.outcome,
      c.count,
    ]),
  );
  assert.deepEqual(counts, {
    hit: 1,
    miss: 1,
    recorded: 0,
    interrupted: 0,
    error: 1,
    incomplete: 0,
  });
  assert.deepEqual(outcomeCounts([row(1, { outcome: "odd" })], all).at(-1), {
    outcome: "odd",
    count: 1,
  });
  assert.deepEqual(
    facet(rows, (h) => h.request?.model || ""),
    [
      { value: "gpt-a", count: 2 },
      { value: "gpt-b", count: 1 },
    ],
  );
});

test("activity buckets per minute and widens for long windows", () => {
  const rows = [
    row(1, { created_at: ago(30_000) }),
    row(2, { created_at: ago(45_000), outcome: "miss" }),
    row(3, { created_at: ago(5 * 60_000 - 1), outcome: "incomplete" }),
  ];
  const a = activity(rows, T0);
  assert.equal(a.minutes, 1);
  assert.equal(a.buckets.length, 30);
  assert.equal(a.end, T0);
  const last = a.buckets.at(-1);
  assert.equal(last.total, 2);
  assert.deepEqual(last.counts, {
    hit: 1,
    miss: 1,
    recorded: 0,
    interrupted: 0,
    error: 0,
  });
  assert.equal(a.buckets.at(-5).counts.error, 1);
  assert.equal(a.peak, 2);
  assert.equal(
    a.buckets.reduce((n, b) => n + b.total, 0),
    3,
  );

  const old = activity([row(1, { created_at: ago(3 * 86_400_000) })], T0);
  assert.equal(old.minutes, 120);
  assert.ok(old.buckets.length <= 60);
  assert.equal(old.buckets[0].total, 1);
  assert.equal(activity([], T0).peak, 0);
});

test("formatters", () => {
  assert.equal(outcomeTone("incomplete"), "error");
  assert.equal(outcomeTone("recorded"), "recorded");
  assert.equal(formatMs(null), "—");
  assert.equal(formatMs(840.4), "840 ms");
  assert.equal(formatMs(1234), "1.23 s");
  assert.equal(formatMs(12_345), "12.3 s");
  assert.equal(formatMs(64_000), "1m 4s");
  assert.equal(relativeTime(ago(2000), T0), "now");
  assert.equal(relativeTime(ago(42_000), T0), "42s ago");
  assert.equal(relativeTime(ago(5 * 60_000), T0), "5m ago");
  assert.equal(relativeTime(ago(3 * 86_400_000), T0), "3d ago");
  assert.equal(relativeTime("nope", T0), "—");
  assert.equal(bucketLabel(1), "per minute");
  assert.equal(bucketLabel(5), "per 5 min");
  assert.equal(bucketLabel(120), "per 2 h");
  assert.equal(
    timingScale([
      row(1),
      row(2, { duration_ms: 900 }),
      row(3, { duration_ms: null, first_event_ms: null }),
    ]),
    900,
  );
  assert.equal(
    timingScale([row(3, { duration_ms: null, first_event_ms: null })]),
    0,
  );
  const many = Array.from({ length: 40 }, (_, i) =>
    row(i + 1, { duration_ms: (i + 1) * 10 }),
  );
  many.push(row(99, { duration_ms: 1_000_000 }));
  assert.equal(timingScale(many), 390, "an outlier does not set the scale");
  assert.equal(shortModel("anthropic/claude-sonnet-4.5"), "claude-sonnet-4.5");
  assert.equal(shortModel("gpt-5"), "gpt-5");
});
