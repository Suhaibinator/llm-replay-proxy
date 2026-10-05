// Mocked /api/** fixtures for the conversations view and the history dialog's
// miss explainer. Shapes follow web/lib/api.ts exactly (ThreadSummary,
// ThreadDetail, ThreadTurn, ResponseSummary, History, NearestCandidate, Diff).

const ROUTE = "/v1/responses";
const MODEL = "gpt-5.1";
const THREAD = "th_7f3a9c2e41d05b8e";
const START = Date.parse("2026-10-04T09:30:00Z");
const now = new Date(START + 60 * 60 * 1000).toISOString();

const tokens = (input, cached_input, output, reasoning) => ({
  input,
  cached_input,
  output,
  reasoning,
});

const messages = [
  "The cache tests are flaky on CI. Can you find out why?",
  "Run just tests/cache.test.ts with --runInBand.",
  "Is the TTL check using the real clock?",
  "Show me where now() comes from in cache.ts.",
  "Inject a clock instead of calling Date.now() directly.",
  "Update the tests to use a fake clock.",
  "Run the cache suite again.",
  "Why does eviction still fail on the second run?",
  "Look at the LRU ordering in evict().",
  "Write a regression test for eviction order.",
  "Run it 50 times to check it's stable.",
  "Now run the whole suite.",
  "Commit this as 'Inject clock into cache'.",
];

// 25 requests of one agent conversation (Responses, store:false): every
// request re-sends the thread, so items grow turn by turn.
const plan = [
  ["hit", 2],
  ["hit", 5],
  ["hit", 8],
  ["hit", 10],
  ["hit", 13],
  ["hit", 16],
  ["hit", 18],
  ["hit", 21],
  ["hit", 24],
  ["hit", 26],
  ["hit", 29],
  ["hit", 31],
  ["miss", 34],
  ["recorded", 34],
  ["recorded", 37],
  ["recorded", 40],
  ["recorded", 42],
  ["hit", 45],
  ["hit", 48],
  ["interrupted", 50],
  ["recorded", 50],
  ["recorded", 53],
  ["error", 56],
  ["recorded", 56],
  ["recorded", 59],
];

function usageFor(i, items) {
  const input = 1800 + items * 410;
  return {
    input,
    cached_input: Math.round(input * 0.82),
    output: 180 + ((i * 137) % 720),
    reasoning: i % 3 === 0 ? 64 + ((i * 53) % 400) : 0,
    total: null,
  };
}

const turns = plan.map(([outcome, items], i) => {
  const at = new Date(START + i * 95_000).toISOString();
  const preview = messages[Math.min(messages.length - 1, Math.floor(i / 2))];
  const replay = outcome === "hit";
  const upstream = outcome === "recorded";
  const usage = usageFor(i, items);
  const response =
    replay || upstream
      ? {
          model: `${MODEL}-2026-08-01`,
          outcome: "completed",
          usage: {
            input: usage.input,
            cached_input: usage.cached_input,
            output: usage.output,
            reasoning: usage.reasoning,
            total: usage.input + usage.output,
          },
          cost: null,
          output_chars: usage.output * 3,
          reasoning_chars: usage.reasoning * 3,
          tool_calls: i % 2,
        }
      : null;
  const detail =
    outcome === "miss"
      ? "replay mode"
      : outcome === "interrupted"
        ? "context canceled: stream incomplete at disconnect: an SSE frame was partially received"
        : outcome === "error"
          ? "429 Too Many Requests"
          : "";
  return {
    history_id: 1001 + i,
    created_at: at,
    outcome,
    lookup_outcome: replay ? "hit" : outcome === "miss" ? "miss" : "miss",
    detail,
    recording_id: replay ? 100 + i : upstream ? 200 + i : 0,
    items,
    preview,
    duration_ms: replay
      ? 6 + ((i * 7) % 30)
      : upstream
        ? 1600 + ((i * 1733) % 7200)
        : outcome === "interrupted"
          ? 3240
          : outcome === "error"
            ? 182
            : null,
    first_event_ms: replay
      ? 2 + (i % 4)
      : upstream
        ? 420 + ((i * 211) % 900)
        : outcome === "interrupted"
          ? 910
          : null,
    response,
  };
});

const counts = (list) => ({
  requests: list.length,
  hits: list.filter((t) => t.outcome === "hit").length,
  misses: list.filter((t) => t.outcome === "miss").length,
  recorded: list.filter((t) => t.outcome === "recorded").length,
  interrupted: list.filter((t) => t.outcome === "interrupted").length,
  errors: list.filter(
    (t) => t.outcome === "error" || t.outcome === "incomplete",
  ).length,
});

const upstreamTokens = (list) =>
  list
    .filter((t) => t.outcome === "recorded")
    .reduce(
      (s, t) => ({
        input: s.input + t.response.usage.input,
        cached_input: s.cached_input + t.response.usage.cached_input,
        output: s.output + t.response.usage.output,
        reasoning: s.reasoning + t.response.usage.reasoning,
      }),
      tokens(0, 0, 0, 0),
    );

const mainSummary = {
  ...counts(turns),
  thread: THREAD,
  route: ROUTE,
  model: MODEL,
  opening: messages[0],
  latest: turns[turns.length - 1].preview,
  max_items: 59,
  first_at: turns[0].created_at,
  last_at: turns[turns.length - 1].created_at,
  last_outcome: "recorded",
  upstream_tokens: upstreamTokens(turns),
};

const other = (n, over) => ({
  requests: 0,
  hits: 0,
  misses: 0,
  recorded: 0,
  interrupted: 0,
  errors: 0,
  thread: `th_${String(n).padStart(4, "0")}aa${n}bb${n}cc`,
  route: ROUTE,
  model: MODEL,
  opening: "",
  latest: "",
  max_items: 2,
  first_at: new Date(START - n * 3_600_000).toISOString(),
  last_at: new Date(START - n * 3_600_000 + 600_000).toISOString(),
  last_outcome: "hit",
  upstream_tokens: tokens(0, 0, 0, 0),
  ...over,
});

const threads = [
  mainSummary,
  other(1, {
    opening: "Summarise the open incidents for the on-call handover",
    latest: "Add the Grafana links too",
    requests: 6,
    hits: 6,
    max_items: 11,
  }),
  other(2, {
    opening: "Translate the release notes into German and French",
    latest: "Use the formal register for German",
    model: "claude-sonnet-4.5",
    route: "/v1/messages",
    requests: 9,
    hits: 4,
    recorded: 4,
    misses: 1,
    max_items: 14,
    upstream_tokens: tokens(48200, 31000, 5120, 0),
  }),
  other(3, {
    opening: "Plan a migration from Postgres 14 to 17 with zero downtime",
    latest: "What about logical replication slots?",
    requests: 14,
    recorded: 12,
    errors: 1,
    interrupted: 1,
    max_items: 27,
    upstream_tokens: tokens(412000, 290000, 18400, 9100),
  }),
  other(4, {
    opening: "Classify these support tickets by product area",
    latest: "Classify these support tickets by product area",
    model: "gpt-5-mini",
    route: "/v1/chat/completions",
    requests: 3,
    hits: 3,
    max_items: 2,
  }),
  other(5, {
    opening: "Draft a SQL query for weekly active users by plan",
    latest: "Exclude internal accounts",
    requests: 5,
    misses: 5,
    max_items: 7,
  }),
];

function requestBody(t, opts = {}) {
  const input = [
    {
      role: "developer",
      content: [
        {
          type: "input_text",
          text: `You are a careful coding agent working in the payments-service repository. Current time: ${opts.time || "2026-10-04T09:41:07Z"}. Prefer small, reviewed changes.`,
        },
      ],
    },
  ];
  let n = 0;
  while (input.length < t.items) {
    const k = input.length % 3;
    if (k === 1)
      input.push({
        role: "user",
        content: [
          {
            type: "input_text",
            text: messages[Math.min(n++, messages.length - 1)],
          },
        ],
      });
    else if (k === 2)
      input.push({
        type: "function_call",
        call_id: `call_${input.length}`,
        name: "shell",
        arguments: JSON.stringify({
          cmd: "npx jest tests/cache.test.ts --runInBand",
        }),
      });
    else
      input.push({
        type: "function_call_output",
        call_id: `call_${input.length - 1}`,
        output: `FAIL tests/cache.test.ts\n  ✕ evicts the oldest entry (${input.length % 7} ms)`,
      });
  }
  return JSON.stringify(
    {
      model: MODEL,
      store: false,
      stream: true,
      input,
      tools: [
        {
          type: "function",
          name: "shell",
          description: "Run a shell command in the repository",
          parameters: {
            type: "object",
            properties: { cmd: { type: "string" } },
          },
        },
      ],
      metadata: { run_id: opts.run || "run_8f2k3j9d0s" },
    },
    null,
    2,
  );
}

function historyItem(t) {
  return {
    id: t.history_id,
    collection_id: 1,
    route: ROUTE,
    key: `9c1e${String(t.history_id)}f0d2b7a4e8c6135d0f9a2b4c6d8e0f1a3b5c7d9e1f2a4b6c8d0e2f4a6b8c0d`,
    request: {
      model: MODEL,
      items: t.items,
      preview: t.preview,
      opening: messages[0],
      tool_calls: Math.floor(t.items / 3),
      images: 0,
      bytes: 4096 + t.items * 380,
      thread: THREAD,
    },
    outcome: t.outcome,
    detail: t.detail,
    recording_id: t.recording_id,
    created_at: t.created_at,
    request_text: requestBody(t),
    source:
      t.outcome === "hit"
        ? "replay"
        : t.outcome === "miss"
          ? "proxy"
          : "upstream",
    duration_ms: t.duration_ms,
    first_event_ms: t.first_event_ms,
    lookup_outcome: t.lookup_outcome,
  };
}

const MISS = turns[12];

const nearest = {
  candidates: [
    {
      recording_id: 312,
      similarity: 0.97,
      reason: "same_thread",
      preview: MISS.preview,
      model: MODEL,
      items: 34,
    },
    {
      recording_id: 111,
      similarity: 0.93,
      reason: "same_thread",
      preview: turns[11].preview,
      model: MODEL,
      items: 31,
    },
    {
      recording_id: 57,
      similarity: 0.41,
      reason: "shared_content",
      preview: "Why is the session cache returning stale entries?",
      model: MODEL,
      items: 12,
    },
  ],
};

const diff = (path, request, recorded, exists = [true, true]) => ({
  path,
  request,
  recorded,
  request_exists: exists[0],
  recorded_exists: exists[1],
  request_display: exists[0] ? JSON.stringify(request) : "(missing)",
  recorded_display: exists[1] ? JSON.stringify(recorded) : "(missing)",
});

const prompt = (time) =>
  `You are a careful coding agent working in the payments-service repository. Current time: ${time}. Prefer small, reviewed changes.`;

const compare = {
  312: [
    diff(
      "/input/0/content/0/text",
      prompt("2026-10-04T09:41:07Z"),
      prompt("2026-10-03T16:02:55Z"),
    ),
    diff(
      "/input/32/output",
      "FAIL tests/cache.test.ts\n  ✕ evicts the oldest entry (3 ms)\n\nTests: 1 failed, 23 passed",
      "FAIL tests/cache.test.ts\n  ✕ evicts the oldest entry (5 ms)\n\nTests: 1 failed, 23 passed",
    ),
    diff("/metadata/run_id", "run_8f2k3j9d0s", "run_1a2b3c4d5e"),
  ],
  111: [
    diff(
      "/input/0/content/0/text",
      prompt("2026-10-04T09:41:07Z"),
      prompt("2026-10-03T16:01:12Z"),
    ),
    diff("/metadata/run_id", "run_8f2k3j9d0s", "run_1a2b3c4d5e"),
    diff("/input/31", { role: "user" }, null, [true, false]),
    diff("/input/32", { type: "function_call" }, null, [true, false]),
    diff("/input/33", { type: "function_call_output" }, null, [true, false]),
  ],
  57: [
    diff("/temperature", 0.2, 1),
    diff(
      "/input/1/content/0/text",
      messages[0],
      "Why is the session cache returning stale entries?",
    ),
  ],
};

const emptyTotals = tokens(0, 0, 0, 0);
const zeroCounts = {
  requests: 0,
  hits: 0,
  misses: 0,
  recorded: 0,
  interrupted: 0,
  errors: 0,
};
const pct = { p50: null, p95: null, p99: null, samples: 0 };
const latency = { duration_ms: pct, first_event_ms: pct, histogram: [] };

/** Answers every /api/** call the console might make. */
async function mockAPI(page) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    const json = (body, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    if (p === "/api/settings")
      return json({
        mode: "replay",
        active_collection_id: 1,
        first_event_delay_ms: 0,
        delay_multiplier: 1,
        history_limit: 10000,
      });
    if (p === "/api/collections")
      return json([
        {
          id: 1,
          name: "Default",
          exclusions: [],
          created_at: "2026-10-01T00:00:00Z",
        },
      ]);
    if (p === "/api/threads") return json(threads);
    if (p.startsWith("/api/threads/")) {
      const id = decodeURIComponent(p.slice("/api/threads/".length));
      if (id === THREAD) return json({ ...mainSummary, turns });
      const t = threads.find((x) => x.thread === id);
      if (!t)
        return json(
          { error: { code: "not_found", message: "not found" } },
          404,
        );
      return json({ ...t, turns: [] });
    }
    let m = /^\/api\/history\/(\d+)\/nearest$/.exec(p);
    if (m)
      return json(
        Number(m[1]) === MISS.history_id ? nearest : { candidates: [] },
      );
    m = /^\/api\/history\/(\d+)$/.exec(p);
    if (m) {
      const t = turns.find((x) => x.history_id === Number(m[1]));
      if (t) return json(historyItem(t));
      return json({ error: { code: "not_found", message: "not found" } }, 404);
    }
    if (p === "/api/compare") {
      const body = JSON.parse(route.request().postData() || "{}");
      return json({ differences: compare[body.recording_id] || [] });
    }
    if (p === "/api/history") return json(turns.map(historyItem).reverse());
    if (p === "/api/recordings") return json([]);
    if (p === "/api/analytics")
      return json({
        total: 0,
        lifetime_total: 0,
        hits: 0,
        misses: 0,
        errors: 0,
        recorded: 0,
        hit_rate: null,
        sources: {},
        series: [],
      });
    if (p === "/api/insights")
      return json({
        from: "2026-10-03T10:30:00Z",
        to: now,
        bucket: "hour",
        totals: {
          ...zeroCounts,
          hit_rate: null,
          lookups: 0,
          lookup_hits: 0,
          upstream_tokens: emptyTotals,
          replayed_tokens: emptyTotals,
          upstream_cost: null,
          saved_cost: null,
          threads: threads.length,
        },
        series: [],
        models: [],
        routes: [],
        latency: { upstream: latency, replay: latency },
        top_recordings: [],
        top_threads: threads.slice(0, 3),
      });
    return json({});
  });
}

module.exports = { mockAPI, THREAD, MISS, turns, threads, now };
