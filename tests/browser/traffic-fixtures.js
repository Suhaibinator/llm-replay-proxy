// Deterministic API fixtures for the mocked Traffic/Recordings browser suite.
// Shapes follow web/lib/api.ts (History, RequestSummary, RecordingRow,
// ResponseSummary) field for field.

const MODELS = [
  "openai/gpt-5.1",
  "anthropic/claude-sonnet-4.5",
  "google/gemini-2.5-pro",
  "openai/gpt-5-mini",
  "x-ai/grok-4-fast",
];
const ROUTES = ["/v1/responses", "/v1/chat/completions", "/v1/messages"];
const PREVIEWS = [
  "Summarize the attached incident report in three bullet points",
  "Why does my Go test hang when the context is cancelled?",
  "Translate this onboarding email into Brazilian Portuguese",
  "Draft a polite reply declining the vendor's renewal offer",
  "What changed between the v2 and v3 billing schemas?",
  "Write a SQL query for weekly active users by plan tier",
  "Refactor this React hook so it stops re-rendering on every keystroke",
  "Explain the difference between p95 and p99 latency to a PM",
  "Classify these support tickets by urgency and product area",
  "Generate five subject lines for the October product update",
  "Is this regex vulnerable to catastrophic backtracking?",
  "Extract invoice number, total and due date from the scanned PDF",
  "Plan a three-day itinerary in Lisbon for a family of four",
  "Review this Terraform diff for anything that would recreate the database",
  "Continue the story from where the detective opens the letter",
  "Which of these log lines explain the 502s after the deploy?",
  "Convert the meeting transcript into action items with owners",
  "Suggest names for a CLI that records and replays LLM traffic",
];
const OUTCOMES = [
  ["hit", 55],
  ["recorded", 19],
  ["miss", 12],
  ["error", 6],
  ["interrupted", 5],
  ["incomplete", 3],
];

function rng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}
const pick = (r, list) => list[Math.floor(r() * list.length)];
function weighted(r, table) {
  let x = r() * table.reduce((n, [, w]) => n + w, 0);
  for (const [v, w] of table) if ((x -= w) < 0) return v;
  return table[0][0];
}
const hex = (r, n) =>
  Array.from({ length: n }, () => Math.floor(r() * 16).toString(16)).join("");

function request(r, i) {
  const items = 1 + Math.floor(r() * 24);
  return {
    model: pick(r, MODELS),
    items,
    preview: `${pick(r, PREVIEWS)}${i % 7 === 0 ? " — and keep it short" : ""}`,
    opening: pick(r, PREVIEWS),
    tool_calls: r() < 0.25 ? 1 + Math.floor(r() * 4) : 0,
    images: r() < 0.08 ? 1 + Math.floor(r() * 2) : 0,
    bytes: 400 + Math.floor(r() * 120_000),
    thread: hex(r, 16),
  };
}

function historyRow(r, id, createdAt) {
  const outcome = weighted(r, OUTCOMES);
  const req = r() < 0.03 ? null : request(r, id);
  const upstream = ["recorded", "interrupted", "incomplete"].includes(outcome);
  const replay = outcome === "hit";
  const duration = replay
    ? 40 + Math.floor(r() * 900)
    : upstream
      ? 700 + Math.floor(r() * r() * 24_000)
      : outcome === "error" && r() < 0.5
        ? 200 + Math.floor(r() * 30_000)
        : null;
  const first =
    duration === null
      ? null
      : Math.floor(duration * (replay ? 0.05 + r() * 0.1 : 0.1 + r() * 0.3));
  const detail = {
    miss: "replay mode",
    error: pick(r, [
      "upstream returned 502 Bad Gateway",
      "dial tcp 127.0.0.1:9: connect: connection refused",
      "upstream timeout after 30s",
    ]),
    interrupted: "client disconnected before the response finished",
    incomplete: "stream ended without a completion event",
  }[outcome];
  return {
    id,
    collection_id: 1,
    route: pick(r, ROUTES),
    key: hex(r, 64),
    request: req,
    outcome,
    detail: detail || "",
    recording_id:
      replay || outcome === "recorded" ? 1 + Math.floor(r() * 320) : 0,
    created_at: new Date(createdAt).toISOString(),
    source: replay
      ? "replay"
      : upstream
        ? "upstream"
        : outcome === "miss"
          ? "proxy"
          : pick(r, ["proxy", "upstream"]),
    duration_ms: duration,
    first_event_ms: first,
    lookup_outcome: replay ? "hit" : "miss",
    provider: "openrouter",
  };
}

/** `count` history rows, oldest first, ending at `now`, denser recently. */
function makeHistory(count, now = Date.now(), seed = 7) {
  const r = rng(seed);
  const rows = [];
  let t = now - 4000;
  for (let i = 0; i < count; i++) {
    rows.push(t);
    // Bursty: mostly seconds apart, sometimes minutes of quiet.
    t -= r() < 0.06 ? 60_000 + r() * 600_000 : 2_000 + r() * 25_000;
  }
  return rows.reverse().map((at, i) => historyRow(r, i + 1, at));
}

/** More rows after `last`, created just now. */
function moreHistory(last, n, now = Date.now(), seed = 99) {
  const r = rng(seed + last);
  return Array.from({ length: n }, (_, i) =>
    historyRow(r, last + i + 1, now - (n - i) * 300),
  );
}

function makeRecordings(count, now = Date.now(), seed = 3) {
  const r = rng(seed);
  return Array.from({ length: count }, (_, i) => {
    const id = count - i;
    const req = request(r, id);
    const created = now - (i * 41 + r() * 30) * 60_000;
    const revisions = r() < 0.12 ? 2 + Math.floor(r() * 3) : 1;
    const source = revisions > 1 && r() < 0.7 ? "edit" : "recorded";
    const hits = r() < 0.3 ? 0 : Math.floor(r() * r() * 90) + 1;
    const input = 200 + Math.floor(r() * r() * 60_000);
    const output = 20 + Math.floor(r() * 3_000);
    const reasons = /gpt-5|gemini|grok/.test(req.model);
    const response =
      r() < 0.05
        ? null
        : {
            model: req.model,
            outcome: pick(r, [
              "completed",
              "completed",
              "completed",
              "stop",
              "end_turn",
              "tool_calls",
              "length",
              "incomplete",
            ]),
            usage: {
              input,
              cached_input: r() < 0.4 ? Math.floor(input * r()) : null,
              output,
              reasoning: reasons ? Math.floor(output * r()) : null,
              total: input + output,
            },
            cost: r() < 0.7 ? (input * 1.25 + output * 10) / 1_000_000 : null,
            output_chars: output * 4,
            reasoning_chars: 0,
            tool_calls: req.tool_calls ? 1 : 0,
          };
    const updated = created + (revisions > 1 ? r() * (now - created) : 0);
    return {
      id,
      collection_id: 1,
      key: hex(r, 64),
      route: pick(r, ROUTES),
      streaming: r() < 0.7,
      active_revision_id: id * 10 + revisions,
      created_at: new Date(created).toISOString(),
      request: req,
      updated_at: new Date(Math.min(updated, now)).toISOString(),
      source,
      revisions,
      hits,
      last_hit_at: hits
        ? new Date(now - r() * 5 * 86_400_000).toISOString()
        : "",
      response,
    };
  });
}

/**
 * Installs /api/** mocks on a Playwright page. Returns the mutable state so a
 * test can append history rows that the next poll picks up.
 */
async function mockAPI(page, { history = 420, recordings = 320 } = {}) {
  const now = Date.now();
  const state = {
    history: makeHistory(history, now),
    recordings: makeRecordings(recordings, now),
    historyRequests: [],
  };
  const json = (route, body, status = 200) =>
    route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  await page.route("**/api/**", (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === "/api/settings")
      return json(route, {
        mode: "auto",
        active_collection_id: 1,
        first_event_delay_ms: 0,
        delay_multiplier: 1,
        history_limit: 10000,
      });
    if (path === "/api/collections")
      return json(route, [
        {
          id: 1,
          name: "Demo",
          exclusions: [],
          created_at: "2026-10-01T00:00:00Z",
        },
      ]);
    if (path === "/api/history") {
      state.historyRequests.push(url.search);
      const after = Number(url.searchParams.get("after_id") || 0);
      const limit = Number(url.searchParams.get("limit") || 200);
      const rows = state.history
        .filter((h) => h.id > after)
        .sort((a, b) => b.id - a.id)
        .slice(0, limit);
      return json(route, rows);
    }
    const one = path.match(/^\/api\/history\/(\d+)$/);
    if (one) {
      const h = state.history.find((x) => x.id === Number(one[1]));
      return h
        ? json(route, { ...h, request_text: "{}" })
        : json(
            route,
            { error: { code: "not_found", message: "not found" } },
            404,
          );
    }
    if (path === "/api/recordings") return json(route, state.recordings);
    const rec = path.match(/^\/api\/recordings\/(\d+)$/);
    if (rec) {
      const r = state.recordings.find((x) => x.id === Number(rec[1]));
      if (!r)
        return json(
          route,
          { error: { code: "not_found", message: "not found" } },
          404,
        );
      const {
        request,
        updated_at,
        source,
        revisions,
        hits,
        last_hit_at,
        response,
        ...recording
      } = r;
      const revision = {
        id: r.active_revision_id,
        recording_id: r.id,
        status: 200,
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          id: "resp_fixture",
          object: "response",
          status: "completed",
          model: response?.model || "",
          output: [],
        }),
        events: [],
        source,
        created_at: updated_at,
      };
      return json(route, {
        recording,
        revision,
        revisions: [revision],
        text: "",
        request_text: JSON.stringify({
          model: request?.model,
          input: request?.preview,
        }),
        matching_input_text: "{}",
      });
    }
    return json(
      route,
      { error: { code: "not_found", message: "not mocked" } },
      404,
    );
  });
  return state;
}

module.exports = { makeHistory, moreHistory, makeRecordings, mockAPI };
