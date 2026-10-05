import test from "node:test";
import assert from "node:assert/strict";
import {
  defaultRecordingSort,
  emptyRecordingFilter,
  filterRecordings,
  formatCost,
  formatTokens,
  isEdited,
  nextSort,
  page,
  recordingFacet,
  recordingModel,
  recordingTotals,
  sortRecordings,
} from "./recordings.ts";

const rec = (id, over = {}, response = {}) => ({
  id,
  collection_id: 1,
  key: `k${id}`,
  route: "/v1/responses",
  upstream_identity: "x",
  streaming: true,
  active_revision_id: id,
  created_at: `2026-10-0${id}T00:00:00Z`,
  request: {
    model: "req-model",
    items: 1,
    preview: `preview ${id}`,
    opening: "",
    tool_calls: 0,
    images: 0,
    bytes: id * 100,
    thread: "t",
  },
  updated_at: `2026-10-0${id}T00:00:00Z`,
  source: "recorded",
  revisions: 1,
  hits: 0,
  last_hit_at: "",
  response:
    response === null
      ? null
      : {
          model: "resp-model",
          outcome: "completed",
          usage: {
            input: 10,
            cached_input: null,
            output: 5,
            reasoning: null,
            total: 15,
          },
          cost: null,
          output_chars: 10,
          reasoning_chars: 0,
          tool_calls: 0,
          ...response,
        },
  ...over,
});

const ids = (rows) => rows.map((r) => r.id);

test("sorting puts missing values last in both directions", () => {
  const rows = [
    rec(1, {}, { cost: 0.5 }),
    rec(2, {}, null),
    rec(3, {}, { cost: 0.01 }),
    rec(4, {}, { cost: 2 }),
  ];
  const by = (key, dir) => ids(sortRecordings(rows, { key, dir }));
  assert.deepEqual(by("cost", "desc"), [4, 1, 3, 2]);
  assert.deepEqual(by("cost", "asc"), [3, 1, 4, 2]);
  assert.deepEqual(by("updated", "desc"), [4, 3, 2, 1]);
  assert.deepEqual(by("size", "asc"), [1, 2, 3, 4]);
  assert.notEqual(sortRecordings(rows, defaultRecordingSort), rows);

  const replayed = [
    rec(1, { hits: 3, last_hit_at: "2026-10-03T00:00:00Z" }),
    rec(2, { hits: 0 }),
    rec(3, { hits: 1, last_hit_at: "2026-10-04T00:00:00Z" }),
  ];
  const sorted = (key, dir) => ids(sortRecordings(replayed, { key, dir }));
  assert.deepEqual(sorted("replays", "desc"), [1, 3, 2]);
  assert.deepEqual(sorted("last_replay", "asc"), [1, 3, 2]);
  assert.deepEqual(sorted("last_replay", "desc"), [3, 1, 2]);

  const text = [
    rec(1, { request: { ...rec(1).request, preview: "beta" } }),
    rec(2, { request: null }),
    rec(3, { request: { ...rec(3).request, preview: "Alpha" } }),
  ];
  assert.deepEqual(
    ids(sortRecordings(text, { key: "request", dir: "asc" })),
    [3, 1, 2],
  );
});

test("nextSort flips the same key and starts new keys in natural order", () => {
  assert.deepEqual(nextSort(defaultRecordingSort, "updated"), {
    key: "updated",
    dir: "asc",
  });
  assert.deepEqual(nextSort(defaultRecordingSort, "model"), {
    key: "model",
    dir: "asc",
  });
  assert.deepEqual(nextSort(defaultRecordingSort, "cost"), {
    key: "cost",
    dir: "desc",
  });
});

test("filters: model, route, never replayed, edited, search", () => {
  const rows = [
    rec(1, { hits: 2 }),
    rec(2, { revisions: 3 }, { model: "other" }),
    rec(3, { route: "/v1/chat/completions", source: "edit" }, null),
    rec(4, { key: "deadbeef" }),
  ];
  const f = emptyRecordingFilter;
  const only = (patch) => ids(filterRecordings(rows, { ...f, ...patch }));
  assert.equal(recordingModel(rows[2]), "req-model");
  assert.equal(recordingModel(rows[1]), "other");
  assert.ok(isEdited(rows[1]) && isEdited(rows[2]) && !isEdited(rows[0]));
  assert.deepEqual(only({ model: "other" }), [2]);
  assert.deepEqual(only({ route: "/v1/chat/completions" }), [3]);
  assert.deepEqual(only({ neverReplayed: true }), [2, 3, 4]);
  assert.deepEqual(only({ edited: true }), [2, 3]);
  assert.deepEqual(only({ edited: true, neverReplayed: true }), [2, 3]);
  assert.deepEqual(only({ query: "DEADBEEF" }), [4]);
  assert.deepEqual(only({ query: "preview 3" }), [3]);
  assert.deepEqual(recordingFacet(rows, recordingModel), [
    { value: "resp-model", count: 2 },
    { value: "other", count: 1 },
    { value: "req-model", count: 1 },
  ]);
});

test("paging clamps the index", () => {
  const rows = Array.from({ length: 250 }, (_, i) => i);
  assert.equal(page(rows, 0, 100).rows.length, 100);
  const last = page(rows, 9, 100);
  assert.equal(last.page, 2);
  assert.equal(last.pages, 3);
  assert.deepEqual([last.from, last.to], [201, 250]);
  assert.deepEqual(page([], 3, 100), {
    rows: [],
    page: 0,
    pages: 1,
    from: 0,
    to: 0,
  });
});

test("number formatting and totals", () => {
  assert.equal(formatTokens(null), "—");
  assert.equal(formatTokens(950), "950");
  assert.equal(formatTokens(1234), "1.2k");
  assert.equal(formatTokens(34_567), "35k");
  assert.equal(formatTokens(1_250_000), "1.3M");
  assert.equal(formatCost(null), "—");
  assert.equal(formatCost(0), "$0");
  assert.equal(formatCost(0.00001), "<$0.0001");
  assert.equal(formatCost(0.00123), "$0.0012");
  assert.equal(formatCost(0.123), "$0.123");
  assert.equal(formatCost(12.345), "$12.35");
  assert.deepEqual(
    recordingTotals([rec(1, { hits: 2 }, { cost: 0.5 }), rec(2, {}, null)]),
    { cost: 0.5, replays: 2, output: 5 },
  );
  assert.equal(recordingTotals([rec(1)]).cost, null);
});
