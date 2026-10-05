// Deterministic /api/insights fixtures for the Overview browser tests. The
// shapes follow web/lib/api.ts (Insights, ModelInsight, InsightBucket, ...)
// field for field; numbers are synthetic but realistic for agent traffic
// through OpenRouter (several models, one heavy reasoning model, costs).

const HOUR = 3_600_000;
const DAY = 86_400_000;
const BOUNDS = [50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, null];

// Per-model character: share of traffic, tokens per request, reasoning share
// of output, cached share of input, $ per 1M tokens, upstream p50 (ms).
const MODELS = [
  {
    model: "openai/gpt-5.1",
    weight: 0.34,
    input: 5200,
    output: 1900,
    reasoning: 0.62,
    cached: 0.45,
    price: 6.5,
    p50: 7400,
    route: "/v1/responses",
  },
  {
    model: "openai/o4-mini",
    weight: 0.22,
    input: 3100,
    output: 2400,
    reasoning: 0.78,
    cached: 0.3,
    price: 2.2,
    p50: 9800,
    route: "/v1/responses",
  },
  {
    model: "anthropic/claude-sonnet-4.5",
    weight: 0.2,
    input: 7800,
    output: 900,
    reasoning: 0,
    cached: 0.62,
    price: 7.8,
    p50: 4300,
    route: "/v1/messages",
  },
  {
    model: "google/gemini-2.5-flash",
    weight: 0.14,
    input: 2600,
    output: 600,
    reasoning: 0.2,
    cached: 0.1,
    price: 0.4,
    p50: 1500,
    route: "/v1/chat/completions",
  },
  {
    model: "openai/gpt-4.1-mini",
    weight: 0.1,
    input: 1800,
    output: 420,
    reasoning: 0,
    cached: 0.2,
    price: 0.9,
    p50: 1100,
    route: "/v1/chat/completions",
  },
];

function rng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const tokens = () => ({ input: 0, cached_input: 0, output: 0, reasoning: 0 });
/** Adds the server's per-slice hit rate (Auto mode: every call looked up). */
const withRate = (c) => {
  const lookups = c.hits + c.misses + c.recorded;
  return { ...c, hit_rate: lookups ? c.hits / lookups : null };
};
const counts = () => ({
  requests: 0,
  hits: 0,
  misses: 0,
  recorded: 0,
  interrupted: 0,
  errors: 0,
});
const addInto = (a, b) => {
  for (const k of Object.keys(b)) a[k] += b[k];
  return a;
};
const iso = (t) => new Date(t).toISOString().replace(".000Z", "Z");

// Histogram over BOUNDS from samples, plus percentiles.
function latencyStats(samples, firstSamples) {
  const hist = (xs) =>
    BOUNDS.map((le, i) => ({
      le,
      count: xs.filter((x) =>
        le == null
          ? x > BOUNDS[i - 1]
          : x <= le && (i === 0 || x > BOUNDS[i - 1]),
      ).length,
    }));
  const pct = (xs) => {
    if (!xs.length) return { p50: null, p95: null, p99: null, samples: 0 };
    const s = [...xs].sort((a, b) => a - b);
    const q = (p) =>
      Math.round(s[Math.min(s.length - 1, Math.floor(p * s.length))]);
    return { p50: q(0.5), p95: q(0.95), p99: q(0.99), samples: s.length };
  };
  return {
    duration_ms: pct(samples),
    first_event_ms: pct(firstSamples),
    histogram: hist(samples),
    first_event_histogram: hist(firstSamples),
  };
}

/**
 * Build an Insights response for [from, to), optionally one model. `empty`
 * returns a zero range; `noCost` leaves costs unreported.
 */
function buildInsights({
  from,
  to,
  model = "",
  empty = false,
  noCost = false,
}) {
  const fromT = Date.parse(from),
    toT = Date.parse(to);
  const bucket = toT - fromT > 48 * HOUR ? "day" : "hour";
  const step = bucket === "hour" ? HOUR : DAY;
  const r = rng(1234 + Math.floor(fromT / DAY));
  const models = MODELS.filter((m) => !model || m.model === model);
  const per = new Map(
    models.map((m) => [
      m.model,
      {
        ...counts(),
        upstream_tokens: tokens(),
        replayed_tokens: tokens(),
        upstream_cost: 0,
        saved_cost: 0,
        up: [],
        upF: [],
        re: [],
        reF: [],
      },
    ]),
  );
  const routes = new Map();
  const series = [];
  let i = 0;
  for (let t = Math.floor(fromT / step) * step; t < toT; t += step, i++) {
    const b = {
      start: iso(t),
      ...counts(),
      upstream_tokens: tokens(),
      replayed_tokens: tokens(),
    };
    series.push(b);
    if (empty) continue;
    // Weekly rhythm plus a ramp: the team adopted replay over the month.
    const day = new Date(t).getUTCDay();
    const weekday = day === 0 || day === 6 ? 0.35 : 1;
    const hourShape =
      bucket === "hour"
        ? 0.4 +
          0.6 *
            Math.sin(
              (Math.PI * ((new Date(t).getUTCHours() + 18) % 24)) / 24,
            ) **
              2
        : 1;
    const base =
      (bucket === "hour" ? 9 : 120) * weekday * hourShape * (0.75 + r() * 0.5);
    const progress = Math.min(1, (t - fromT) / Math.max(step, toT - fromT));
    const hitP = 0.5 + 0.38 * progress;
    for (const m of models) {
      const n = Math.round(base * m.weight * (0.7 + r() * 0.6));
      const agg = per.get(m.model);
      for (let k = 0; k < n; k++) {
        const x = r();
        const outcome =
          x < hitP
            ? "hits"
            : x < hitP + 0.04
              ? "misses"
              : x < 0.965
                ? "recorded"
                : x < 0.985
                  ? "interrupted"
                  : "errors";
        b.requests++;
        b[outcome]++;
        agg.requests++;
        agg[outcome]++;
        const route = routes.get(m.route) ?? { route: m.route, ...counts() };
        route.requests++;
        route[outcome]++;
        routes.set(m.route, route);
        const scale = 0.5 + r();
        const input = Math.round(m.input * scale * (1 + i * 0.01));
        const output = Math.round(m.output * (0.5 + r()));
        const t2 = {
          input,
          cached_input: Math.round(input * m.cached * (0.6 + r() * 0.8)),
          output,
          reasoning: Math.round(output * m.reasoning * (0.8 + r() * 0.3)),
        };
        t2.cached_input = Math.min(t2.cached_input, input);
        t2.reasoning = Math.min(t2.reasoning, output);
        const cost = ((input + output) / 1e6) * m.price;
        if (outcome === "recorded") {
          addInto(b.upstream_tokens, t2);
          addInto(agg.upstream_tokens, t2);
          agg.upstream_cost += cost;
          const d = m.p50 * Math.exp((r() - 0.5) * 1.6) * (r() < 0.05 ? 3 : 1);
          agg.up.push(d);
          agg.upF.push(
            Math.min(d, d * (0.08 + r() * 0.25) + m.reasoning * 1800),
          );
        } else if (outcome === "hits") {
          addInto(b.replayed_tokens, t2);
          addInto(agg.replayed_tokens, t2);
          agg.saved_cost += cost;
          const d = 40 + r() * 160 + (r() < 0.04 ? 600 : 0);
          agg.re.push(d);
          agg.reF.push(2 + r() * 12);
        }
      }
    }
  }
  const modelList = [...per.entries()]
    .filter(([, a]) => a.requests > 0 || model)
    .map(([name, a]) => ({
      model: name,
      requests: a.requests,
      hits: a.hits,
      misses: a.misses,
      recorded: a.recorded,
      interrupted: a.interrupted,
      errors: a.errors,
      upstream_tokens: a.upstream_tokens,
      replayed_tokens: a.replayed_tokens,
      upstream_cost: noCost ? null : Number(a.upstream_cost.toFixed(6)),
      saved_cost: noCost ? null : Number(a.saved_cost.toFixed(6)),
      upstream: latencyStats(a.up, a.upF),
      replay: latencyStats(a.re, a.reF),
    }))
    .sort((a, b) => b.requests - a.requests);
  const all = [...per.values()];
  const totals = all.reduce(
    (acc, a) =>
      addInto(acc, {
        requests: a.requests,
        hits: a.hits,
        misses: a.misses,
        recorded: a.recorded,
        interrupted: a.interrupted,
        errors: a.errors,
      }),
    counts(),
  );
  // The fixtures model Auto mode, where every call looks up a recording.
  const lookups = totals.hits + totals.misses + totals.recorded;
  const sumTokens = (k) => all.reduce((acc, a) => addInto(acc, a[k]), tokens());
  const sumCost = (k) =>
    noCost || !totals.requests
      ? null
      : Number(all.reduce((s, a) => s + a[k], 0).toFixed(6));
  const previews = [
    "Summarize the attached quarterly report and list three risks",
    "You are the Post Assistant. Draft a reply to the customer about their delayed parcel",
    "Plan a 3-day itinerary for Lisbon with a budget under €600",
    "Refactor this TypeScript function to remove the nested ternaries",
    "Extract every invoice line item from the scanned PDF as JSON",
    "What does this stack trace mean? panic: runtime error: index out of range",
    "Write SQL to find customers whose last order was over 90 days ago",
    "Classify these 20 support tickets by urgency",
  ];
  const top_recordings = empty
    ? []
    : previews.map((p, k) => {
        const m = models[k % models.length];
        const hits = Math.round((totals.hits / 40) * (1 / (k + 1)) + 3);
        const t2 = {
          input: m.input * hits,
          cached_input: Math.round(m.input * hits * m.cached),
          output: m.output * hits,
          reasoning: Math.round(m.output * hits * m.reasoning),
        };
        return {
          recording_id: 4100 + k * 7,
          preview: p,
          model: m.model,
          hits,
          replayed_tokens: t2,
          saved_cost: noCost
            ? null
            : Number((((t2.input + t2.output) / 1e6) * m.price).toFixed(5)),
        };
      });
  const top_threads = empty
    ? []
    : previews.slice(0, 7).map((p, k) => {
        const m = models[(k + 1) % models.length];
        const requests = Math.round(60 / (k + 1) + 4);
        const hits = Math.round(requests * 0.7);
        return {
          thread: `thr_${(0x9a3f00 + k * 4111).toString(16)}${"c0ffee42"}`,
          route: m.route,
          model: m.model,
          opening: p,
          latest: "Thanks, now make it shorter",
          max_items: 6 + k * 5,
          first_at: iso(fromT + k * step),
          last_at: iso(toT - (k + 1) * 47 * 60_000),
          last_outcome: "hit",
          requests,
          hits,
          misses: 1,
          recorded: requests - hits - 2,
          interrupted: 1,
          errors: 0,
          upstream_tokens: {
            input: m.input * 3,
            cached_input: m.input,
            output: m.output * 3,
            reasoning: Math.round(m.output * 3 * m.reasoning),
          },
        };
      });
  const merge = (k) =>
    latencyStats(
      all.flatMap((a) => a[k]),
      all.flatMap((a) => a[k + "F"]),
    );
  return {
    from,
    to,
    bucket,
    totals: {
      ...totals,
      hit_rate: lookups ? totals.hits / lookups : null,
      lookups,
      lookup_hits: totals.hits,
      upstream_tokens: sumTokens("upstream_tokens"),
      replayed_tokens: sumTokens("replayed_tokens"),
      upstream_cost: sumCost("upstream_cost"),
      saved_cost: sumCost("saved_cost"),
      threads: empty ? 0 : Math.round(totals.requests / 9),
    },
    series: series.map(withRate),
    models: modelList.map(withRate),
    routes: [...routes.values()].map(withRate),
    latency: { upstream: merge("up"), replay: merge("re") },
    top_recordings,
    top_threads,
  };
}

const collections = [
  {
    id: 1,
    name: "Default",
    exclusions: [],
    created_at: "2026-08-01T00:00:00Z",
  },
];
const settings = {
  mode: "auto",
  active_collection_id: 1,
  first_event_delay_ms: 0,
  delay_multiplier: 1,
  history_limit: 10000,
};
const emptyAnalytics = {
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

/**
 * Route every /api/** request: insights are computed from the query; other
 * endpoints get minimal valid bodies so the shell can render. `options.empty`
 * and `options.noCost` shape the insights; `options.onInsights` sees each URL.
 */
async function mockAPI(page, options = {}) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const json = (body) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (url.pathname === "/api/insights") {
      options.onInsights?.(url);
      return json(
        buildInsights({
          from: url.searchParams.get("from"),
          to: url.searchParams.get("to"),
          model: url.searchParams.get("model") || "",
          empty: !!options.empty,
          noCost: !!options.noCost,
        }),
      );
    }
    if (url.pathname === "/api/collections") return json(collections);
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/analytics") return json(emptyAnalytics);
    if (url.pathname.startsWith("/api/threads")) return json([]);
    return json([]);
  });
}

module.exports = { buildInsights, mockAPI, MODELS };
