import test from "node:test";
import assert from "node:assert/strict";
import {
  addTokens,
  cachedShare,
  fillSeries,
  formatAgo,
  formatCost,
  formatCount,
  formatMs,
  formatPercent,
  formatRatio,
  headline,
  histogramRows,
  hitRate,
  insightsURL,
  labelStride,
  mergeModelOptions,
  modelRows,
  niceScale,
  rangeWindow,
  reasoningShare,
  routeRows,
  sortModels,
  speedup,
  threadTitle,
  tokenParts,
  tokenSeries,
  tokenTotal,
} from "./insights.ts";

const tokens = (input, cached_input, output, reasoning) => ({
  input,
  cached_input,
  output,
  reasoning,
});
const pct = (p50, p95, samples = 10) => ({ p50, p95, p99: p95, samples });
const none = { p50: null, p95: null, p99: null, samples: 0 };
const latency = (d, f, histogram = [], first_event_histogram) => ({
  duration_ms: d,
  first_event_ms: f,
  histogram,
  ...(first_event_histogram ? { first_event_histogram } : {}),
});
const counts = (o = {}) => ({
  requests: 0,
  hits: 0,
  misses: 0,
  recorded: 0,
  interrupted: 0,
  errors: 0,
  ...o,
});
const model = (name, o = {}) => ({
  ...counts(o),
  model: name,
  hit_rate: null,
  upstream_tokens: tokens(0, 0, 0, 0),
  replayed_tokens: tokens(0, 0, 0, 0),
  upstream_cost: null,
  saved_cost: null,
  upstream: latency(none, none),
  replay: latency(none, none),
  ...o,
});

test("counts compact to k/M/B and carry across units", () => {
  assert.equal(formatCount(0), "0");
  assert.equal(formatCount(999), "999");
  assert.equal(formatCount(1000), "1k");
  assert.equal(formatCount(1234), "1.2k");
  assert.equal(formatCount(12_345), "12k");
  assert.equal(formatCount(3_400_000), "3.4M");
  assert.equal(formatCount(999_960), "1M");
  assert.equal(formatCount(2_100_000_000), "2.1B");
  assert.equal(formatCount(-1500), "-1.5k");
  assert.equal(formatCount(null), "—");
  assert.equal(formatCount(Number.NaN), "—");
});

test("costs keep sub-cent precision", () => {
  assert.equal(formatCost(null), "—");
  assert.equal(formatCost(0), "$0");
  assert.equal(formatCost(0.0123456), "$0.0123");
  assert.equal(formatCost(0.00004), "<$0.0001");
  assert.equal(formatCost(0.05), "$0.05");
  assert.equal(formatCost(0.42), "$0.42");
  assert.equal(formatCost(12.345), "$12.35");
  assert.equal(formatCost(1234), "$1.2k");
});

test("durations switch from ms to s to min", () => {
  assert.equal(formatMs(null), "—");
  assert.equal(formatMs(0), "0 ms");
  assert.equal(formatMs(0.4), "<1 ms");
  assert.equal(formatMs(842.4), "842 ms");
  assert.equal(formatMs(1250), "1.25 s");
  assert.equal(formatMs(2000), "2 s");
  assert.equal(formatMs(12_400), "12.4 s");
  assert.equal(formatMs(150_000), "2.5 min");
});

test("percent and ratio formatting", () => {
  assert.equal(formatPercent(null), "—");
  assert.equal(formatPercent(0), "0%");
  assert.equal(formatPercent(0.004), "<1%");
  assert.equal(formatPercent(0.874), "87%");
  assert.equal(formatPercent(0.996), ">99%");
  assert.equal(formatPercent(1), "100%");
  assert.equal(formatRatio(38.2), "38×");
  assert.equal(formatRatio(1.44), "1.4×");
  assert.equal(formatRatio(null), "—");
});

test("nice scales are clean and cover the max", () => {
  assert.deepEqual(niceScale(0).ticks, [0, 1, 2, 3, 4]);
  assert.deepEqual(niceScale(3).ticks, [0, 1, 2, 3]);
  assert.deepEqual(niceScale(7).ticks, [0, 2, 4, 6, 8]);
  assert.deepEqual(niceScale(950).ticks, [0, 250, 500, 750, 1000]);
  assert.deepEqual(niceScale(1_234_567).ticks, [0, 500000, 1000000, 1500000]);
  const frac = niceScale(0.37, 4, false);
  assert.equal(frac.max, 0.4);
  assert.deepEqual(frac.ticks, [0, 0.1, 0.2, 0.3, 0.4]);
  for (const max of [1, 13, 99, 101, 4321, 77_777])
    assert.ok(niceScale(max).max >= max, `covers ${max}`);
});

test("label stride thins labels to fit", () => {
  assert.equal(labelStride(24, 1400), 1);
  assert.equal(labelStride(90, 300), 18);
  assert.equal(labelStride(0, 300), 1);
});

test("range windows align to UTC buckets", () => {
  const now = new Date("2026-10-04T15:42:10Z");
  const day = rangeWindow("24h", now);
  assert.equal(day.from.toISOString(), "2026-10-03T16:00:00.000Z");
  assert.equal(day.to.toISOString(), now.toISOString());
  assert.equal(
    rangeWindow("7d", now).from.toISOString(),
    "2026-09-28T00:00:00.000Z",
  );
  assert.equal(
    rangeWindow("90d", now).from.toISOString(),
    "2026-07-07T00:00:00.000Z",
  );
  const url = new URL(insightsURL(3, "30d", "openai/gpt-5", now), "http://x");
  assert.equal(url.pathname, "/api/insights");
  assert.equal(url.searchParams.get("collection_id"), "3");
  assert.equal(url.searchParams.get("model"), "openai/gpt-5");
  assert.equal(url.searchParams.get("from"), "2026-09-05T00:00:00.000Z");
  assert.equal(
    new URL(insightsURL(3, "7d", "", now), "http://x").searchParams.has(
      "model",
    ),
    false,
  );
});

test("fillSeries zero-fills missing buckets and keeps order", () => {
  const filled = fillSeries({
    from: "2026-10-01T00:00:00Z",
    to: "2026-10-04T12:00:00Z",
    bucket: "day",
    series: [
      { ...counts({ requests: 5 }), start: "2026-10-03T00:00:00Z" },
      { ...counts({ requests: 2 }), start: "2026-10-01T00:00:00Z" },
    ],
  });
  assert.deepEqual(
    filled.map((b) => [b.start.slice(0, 10), b.requests]),
    [
      ["2026-10-01", 2],
      ["2026-10-02", 0],
      ["2026-10-03", 5],
      ["2026-10-04", 0],
    ],
  );
  assert.deepEqual(filled[1].upstream_tokens, tokens(0, 0, 0, 0));
  // Unparseable range: just sort what came back.
  assert.equal(
    fillSeries({ from: "", to: "", bucket: "hour", series: filled }).length,
    4,
  );
});

test("token parts split cached input and reasoning out of their parents", () => {
  assert.deepEqual(tokenParts(tokens(1000, 600, 500, 300)), {
    input: 400,
    cached: 600,
    output: 200,
    reasoning: 300,
  });
  assert.equal(tokenTotal(tokens(1000, 600, 500, 300)), 1500);
  // Never negative, even with inconsistent provider numbers.
  assert.deepEqual(tokenParts(tokens(100, 400, 10, 50)), {
    input: 0,
    cached: 100,
    output: 0,
    reasoning: 10,
  });
  assert.deepEqual(tokenParts(null), {
    input: 0,
    cached: 0,
    output: 0,
    reasoning: 0,
  });
  assert.equal(reasoningShare(tokens(1000, 0, 500, 300)), 0.6);
  assert.equal(reasoningShare(tokens(1000, 0, 0, 0)), null);
  assert.equal(cachedShare(tokens(1000, 250, 0, 0)), 0.25);
  assert.deepEqual(
    addTokens(tokens(1, 2, 3, 4), null, tokens(10, 20, 30, 40)),
    tokens(11, 22, 33, 44),
  );
  const rows = tokenSeries([
    {
      ...counts(),
      start: "s",
      upstream_tokens: tokens(10, 0, 5, 0),
      replayed_tokens: tokens(100, 50, 20, 10),
    },
  ]);
  assert.equal(rows[0].upstreamTotal, 15);
  assert.equal(rows[0].replayedTotal, 120);
});

test("hit rate is always the server's lookup-based value", () => {
  // Outcome counts would say 1/4; the server's lookups say otherwise.
  assert.equal(
    hitRate({ ...counts({ hits: 1, recorded: 3 }), hit_rate: 0.5 }),
    0.5,
  );
  assert.equal(hitRate({ ...counts({ hits: 3 }), hit_rate: null }), null);
});

test("histograms merge on shared bounds with labels and shares", () => {
  const up = latency(none, none, [
    { le: 1000, count: 1 },
    { le: 5000, count: 3 },
    { le: null, count: 0 },
  ]);
  const re = latency(
    none,
    none,
    [
      { le: 50, count: 8 },
      { le: 1000, count: 2 },
    ],
    [{ le: 50, count: 10 }],
  );
  const rows = histogramRows(up, re);
  assert.deepEqual(
    rows.map((r) => r.label),
    ["≤50ms", "50ms–1s", "1s–5s", ">5s"],
  );
  assert.deepEqual(
    rows.map((r) => r.short),
    ["≤50ms", "≤1s", "≤5s", ">5s"],
  );
  assert.deepEqual(
    rows.map((r) => [r.upstream, r.replay]),
    [
      [0, 8],
      [1, 2],
      [3, 0],
      [0, 0],
    ],
  );
  assert.equal(rows[0].replayShare, 0.8);
  assert.equal(rows[2].upstreamShare, 0.75);
  const first = histogramRows(up, re, "first_event");
  assert.deepEqual(
    first.map((r) => [r.label, r.replay]),
    [["≤50ms", 10]],
  );
  assert.deepEqual(histogramRows(null, undefined), []);
});

test("speedup compares p50s and needs both", () => {
  assert.equal(speedup(pct(2000, 4000), pct(50, 80)), 40);
  assert.equal(speedup(pct(2000, 4000), none), null);
  assert.equal(speedup(none, pct(5, 8)), null);
  // Sub-millisecond replays are floored to 1 ms rather than dividing by ~0.
  assert.equal(speedup(pct(900, 900), pct(0, 0)), 900);
});

test("model rows derive mix, shares, latency and sort with nulls last", () => {
  const rows = modelRows([
    model("openai/o4-mini", {
      requests: 30,
      hits: 20,
      misses: 2,
      recorded: 8,
      hit_rate: 20 / 30,
      upstream_tokens: tokens(1000, 200, 800, 600),
      replayed_tokens: tokens(4000, 1000, 2000, 1500),
      upstream_cost: 0.5,
      saved_cost: 2,
      upstream: latency(pct(3000, 9000), pct(800, 2000)),
      replay: latency(pct(40, 90), pct(5, 9)),
    }),
    model("anthropic/claude", {
      requests: 10,
      hits: 9,
      misses: 1,
      hit_rate: 0.9,
      upstream_tokens: tokens(500, 0, 100, 0),
    }),
    model("", { requests: 0 }),
  ]);
  const [o4, claude, unknown] = rows;
  assert.equal(o4.share, 0.75);
  assert.equal(o4.hitRate, 20 / 30);
  assert.equal(o4.upstreamTokens, 1800);
  assert.equal(o4.replayedTokens, 6000);
  assert.equal(o4.tokens, 7800);
  assert.deepEqual(o4.mix, {
    input: 3800,
    cached: 1200,
    output: 700,
    reasoning: 2100,
  });
  assert.equal(o4.reasoningShare, 0.75);
  assert.equal(o4.upstreamP95, 9000);
  assert.equal(o4.replayP50, 40);
  assert.equal(o4.firstEventP50, 800);
  assert.equal(claude.reasoningShare, 0);
  assert.equal(claude.upstreamP50, null);
  assert.equal(unknown.model, "unknown");
  assert.deepEqual(
    sortModels(rows, "requests", "desc").map((r) => r.model),
    ["openai/o4-mini", "anthropic/claude", "unknown"],
  );
  assert.deepEqual(
    sortModels(rows, "upstreamP50", "asc").map((r) => r.model),
    ["openai/o4-mini", "anthropic/claude", "unknown"],
  );
  assert.deepEqual(
    sortModels(rows, "model", "asc").map((r) => r.model),
    ["anthropic/claude", "openai/o4-mini", "unknown"],
  );
  assert.deepEqual(
    sortModels(rows, "upstreamCost", "desc").map((r) => r.model),
    ["openai/o4-mini", "anthropic/claude", "unknown"],
  );
});

test("model options remember models seen while filtered", () => {
  const first = mergeModelOptions(
    [],
    [model("b", { requests: 1 }), model("a", { requests: 9 })],
  );
  assert.deepEqual(first, ["a", "b"]);
  assert.deepEqual(mergeModelOptions(first, [model("a")], "c"), [
    "a",
    "b",
    "c",
  ]);
});

test("routes sort busiest first with shares", () => {
  const rows = routeRows([
    { ...counts({ requests: 1, hits: 1 }), route: "/v1/messages", hit_rate: 1 },
    {
      ...counts({ requests: 3, misses: 3 }),
      route: "/v1/responses",
      hit_rate: 0,
    },
  ]);
  assert.deepEqual(
    rows.map((r) => [r.route, r.share, r.hitRate]),
    [
      ["/v1/responses", 0.75, 0],
      ["/v1/messages", 0.25, 1],
    ],
  );
});

test("thread titles fall back from opening to latest to id", () => {
  assert.equal(
    threadTitle({ opening: "  Plan a\n trip ", latest: "x", thread: "t" }),
    "Plan a trip",
  );
  assert.equal(threadTitle({ opening: "", latest: "Hi", thread: "t" }), "Hi");
  assert.equal(
    threadTitle({ opening: "", latest: "", thread: "abcdef123456" }),
    "Thread abcdef12",
  );
});

test("headline totals and per-bucket trends", () => {
  const series = [
    {
      ...counts({ requests: 4, hits: 3, misses: 1 }),
      start: "a",
      hit_rate: 0.75,
      upstream_tokens: tokens(10, 0, 10, 0),
      replayed_tokens: tokens(100, 0, 50, 0),
    },
    {
      ...counts(),
      start: "b",
      hit_rate: null,
      upstream_tokens: tokens(0, 0, 0, 0),
      replayed_tokens: tokens(0, 0, 0, 0),
    },
  ];
  const h = headline({
    from: "",
    to: "",
    bucket: "hour",
    totals: {
      ...counts({ requests: 4, hits: 3, misses: 1 }),
      hit_rate: 0.75,
      lookups: 4,
      lookup_hits: 3,
      upstream_tokens: tokens(10, 0, 10, 0),
      replayed_tokens: tokens(100, 0, 50, 0),
      upstream_cost: null,
      saved_cost: 0.25,
      threads: 2,
    },
    series,
    models: [],
    routes: [],
    latency: {
      upstream: latency(pct(2000, 5000), pct(400, 900)),
      replay: latency(pct(20, 50), pct(2, 4)),
    },
    top_recordings: [],
    top_threads: [],
  });
  assert.equal(h.hitRate, 0.75);
  assert.equal(h.lookups, 4);
  assert.equal(h.hits, 3);
  assert.equal(h.replayedTokens, 150);
  assert.equal(h.upstreamTokens, 20);
  assert.equal(h.savedCost, 0.25);
  assert.equal(h.firstEventP95, 900);
  assert.equal(h.speedup, 100);
  assert.deepEqual(h.trend.requests, [4, 0]);
  assert.deepEqual(h.trend.hitRate, [0.75, null]);
  assert.deepEqual(h.trend.replayedTokens, [150, 0]);
});

test("relative times", () => {
  const now = new Date("2026-10-04T12:00:00Z");
  assert.equal(formatAgo("2026-10-04T11:59:40Z", now), "just now");
  assert.equal(formatAgo("2026-10-04T11:55:00Z", now), "5m ago");
  assert.equal(formatAgo("2026-10-04T09:00:00Z", now), "3h ago");
  assert.equal(formatAgo("2026-10-02T12:00:00Z", now), "2d ago");
  assert.equal(formatAgo("2026-08-01T12:00:00Z", now), "Aug 1");
  assert.equal(formatAgo("nope", now), "—");
});
