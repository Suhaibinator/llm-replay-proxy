import test from "node:test";
import assert from "node:assert/strict";
import {
  detectVolatile,
  explainDiff,
  explainDiffs,
  inlineChange,
  pointerLabel,
  pointerSegments,
} from "./diff.ts";

const diff = (path, request, recorded, exists = [true, true]) => ({
  path,
  request,
  recorded,
  request_exists: exists[0],
  recorded_exists: exists[1],
  request_display: exists[0] ? JSON.stringify(request) : "(missing)",
  recorded_display: exists[1] ? JSON.stringify(recorded) : "(missing)",
});

test("a timestamp embedded in a prompt is found by its changed fragment", () => {
  const d = diff(
    "/instructions",
    "You are helpful. Current time: 2026-10-04T10:12:03Z. Be brief.",
    "You are helpful. Current time: 2026-10-03T09:58:41Z. Be brief.",
  );
  const v = detectVolatile(d);
  assert.equal(v.kind, "timestamp");
  assert.equal(v.request, "2026-10-04T10:12:03Z");
  assert.equal(v.recorded, "2026-10-03T09:58:41Z");
  assert.equal(v.whole, false);
  const e = explainDiff(d);
  assert.equal(e.category, "volatile");
  assert.equal(e.suggestion.pointer, "/instructions");
  assert.match(e.suggestion.caveats[0], /entire text/);
});

test("a change wider than the volatile token is not blamed on it", () => {
  const d = diff(
    "/input/0/content",
    "At 10:00 the user asked for the weather",
    "At 11:00 the user asked for a joke",
  );
  assert.equal(detectVolatile(d), null);
  assert.equal(explainDiff(d).category, "conversation");
});

test("volatile field names and whole values suggest an exclusion", () => {
  const run = explainDiff(diff("/metadata/run_id", "a1", "b2"));
  assert.equal(run.category, "volatile");
  assert.equal(run.volatile.kind, "id");
  assert.equal(run.volatile.whole, true);
  assert.deepEqual(run.suggestion, {
    pointer: "/metadata/run_id",
    caveats: [],
  });

  const epoch = detectVolatile(
    diff("/metadata/started", 1791108723, 1791022301),
  );
  assert.equal(epoch.kind, "timestamp");
  assert.equal(epoch.whole, true);

  const uuid = detectVolatile(
    diff(
      "/input/2/call_id",
      "9b2c1e7a-0f3d-4c55-9d1e-2a6b7c8d9e0f",
      "1f4e5d6c-7b8a-4c9d-8e0f-1a2b3c4d5e6f",
    ),
  );
  assert.equal(uuid.kind, "id");
  const fixed = explainDiff(
    diff(
      "/input/2/call_id",
      "9b2c1e7a-0f3d-4c55-9d1e-2a6b7c8d9e0f",
      "1f4e5d6c-7b8a-4c9d-8e0f-1a2b3c4d5e6f",
    ),
  );
  assert.match(fixed.suggestion.caveats[0], /fixed array positions/);
});

test("ordinary parameter changes are not volatile", () => {
  assert.equal(detectVolatile(diff("/temperature", 0.2, 0.7)), null);
  assert.equal(
    explainDiff(diff("/temperature", 0.2, 0.7)).category,
    "parameter",
  );
  assert.equal(
    explainDiff(diff("/model", "gpt-5", "gpt-5-mini")).category,
    "model",
  );
  assert.equal(explainDiff(diff("/tools/0/name", "a", "b")).category, "tools");
});

test("inline change trims to the differing middle with context", () => {
  const prefix = "x".repeat(100);
  const c = inlineChange(diff("/a", `${prefix}cat sat`, `${prefix}dog sat`));
  assert.equal(c.request, "cat");
  assert.equal(c.recorded, "dog");
  assert.equal(c.before.length, 48);
  assert.equal(c.after, " sat");
  assert.equal(c.clippedStart, true);
  assert.equal(c.clippedEnd, false);
  assert.equal(inlineChange(diff("/a", 1, 2)), null);
  const ts = inlineChange(
    diff(
      "/a",
      "time: 2026-10-04T09:41:07Z. ok",
      "time: 2026-10-03T16:02:55Z. ok",
    ),
  );
  assert.equal(ts.request, "2026-10-04T09:41:07Z");
  assert.equal(ts.recorded, "2026-10-03T16:02:55Z");
  assert.equal(ts.after, ". ok");
  assert.equal(ts.before, "time: ");
});

test("pointers decode and read as breadcrumbs", () => {
  assert.deepEqual(pointerSegments("/a~1b/c~0d/0"), ["a/b", "c~d", "0"]);
  assert.equal(
    pointerLabel("/input/3/content/0/text"),
    "input › item 4 › content[0] › text",
  );
  assert.equal(pointerLabel("/$route"), "Route");
  assert.equal(pointerLabel("/"), "Whole request");
});

test("ranking puts volatile culprits first and folds appended items", () => {
  const report = explainDiffs([
    diff("/input/24", { role: "user" }, null, [true, false]),
    diff("/input/25", { role: "user" }, null, [true, false]),
    diff("/max_output_tokens", 800, 1000),
    diff(
      "/metadata/request_ts",
      "2026-10-04T10:00:00Z",
      "2026-10-03T10:00:00Z",
    ),
  ]);
  assert.deepEqual(
    report.items.map((e) => e.diff.path),
    ["/metadata/request_ts", "/max_output_tokens"],
  );
  assert.deepEqual(report.appended, {
    count: 2,
    collection: "input",
    from: 24,
  });
  assert.equal(report.verdict.tone, "culprit");
  assert.match(report.verdict.headline, /timestamp/);
});

test("only appended items means the conversation continued past the recording", () => {
  const report = explainDiffs([
    diff("/input/10", {}, null, [true, false]),
    diff("/input/11", {}, null, [true, false]),
  ]);
  assert.equal(report.verdict.tone, "continued");
  assert.match(report.verdict.body, /2 input items/);
  assert.equal(report.items.length, 0);
});

test("identical bodies, route changes and existing exclusions", () => {
  assert.equal(explainDiffs([]).verdict.tone, "identical");
  assert.equal(
    explainDiffs([diff("/$route", "/v1/responses", "/v1/chat/completions")])
      .verdict.tone,
    "different",
  );
  const excluded = explainDiffs(
    [diff("/metadata/run_id", "a", "b")],
    ["/metadata"],
  );
  assert.equal(excluded.items[0].excluded, true);
  assert.equal(excluded.items[0].suggestion, null);
  assert.equal(excluded.verdict.tone, "excluded");
});
