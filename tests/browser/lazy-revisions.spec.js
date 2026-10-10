const { test, expect } = require("@playwright/test");

function revision(recording, id, text) {
  return {
    id,
    recording_id: recording,
    status: 200,
    source: "recorded",
    created_at: "2026-01-01T00:00:00Z",
    headers: { "Content-Type": "application/json" },
    events: [],
    body: `{ "id":"example", "choices":[{"message":{"role":"assistant","content":${JSON.stringify(text)}},"finish_reason":"stop"}], "exact":900719925474099312345 }`,
  };
}
function entry(id) {
  const active = revision(id, id * 10 + 2, `Active response ${id}`);
  const historical = revision(id, id * 10 + 1, `Historical response ${id}`);
  const summary = ({ id, recording_id, status, source, created_at }) => ({
    id,
    recording_id,
    status,
    source,
    created_at,
  });
  return {
    recording: {
      id,
      collection_id: 1,
      key: `key${id}`,
      route: "/v1/chat/completions",
      streaming: false,
      active_revision_id: active.id,
      created_at: active.created_at,
    },
    revision: active,
    revision_summaries: [summary(active), summary(historical)],
    text: `Active response ${id}`,
    request_text: '{"model":"fixture","messages":[]}',
    matching_input_text: "{}",
  };
}

async function fixtures(page, detail) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (/\/revisions\/\d+$/.test(path)) {
      await detail(route);
      return;
    }
    const match = path.match(/^\/api\/recordings\/(\d+)$/);
    if (match) {
      expect(url.searchParams.get("revision_details")).toBe("lazy");
      await route.fulfill({ json: entry(Number(match[1])) });
      return;
    }
    const json =
      path === "/api/settings"
        ? {
            mode: "replay",
            active_collection_id: 1,
            first_event_delay_ms: 0,
            delay_multiplier: 0,
            history_limit: 10000,
          }
        : path === "/api/collections"
          ? [
              {
                id: 1,
                name: "Lazy fixtures",
                exclusions: [],
                created_at: "2026-01-01T00:00:00Z",
              },
            ]
          : path === "/api/recordings"
            ? [1, 2].map((id) => ({
                ...entry(id).recording,
                request: {
                  model: "fixture",
                  items: 0,
                  preview: "",
                  opening: "",
                  tool_calls: 0,
                  images: 0,
                  bytes: 2,
                  thread: "",
                },
                revisions: 2,
                hits: 0,
                last_hit_at: "",
                updated_at: "2026-01-01T00:00:00Z",
                source: "recorded",
              }))
            : [];
    await route.fulfill({ json });
  });
}

test("revision details load only on expansion, retry failures, and reuse the active response", async ({
  page,
}) => {
  let calls = 0;
  await fixtures(page, async (route) => {
    calls++;
    if (calls === 1) {
      await route.fulfill({
        status: 500,
        json: { error: { message: "Could not load revision" } },
      });
      return;
    }
    await route.fulfill({ json: revision(1, 11, "Historical response 1") });
  });
  await page.goto("/#/recordings?recording=1");
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Active response 1");
  await dialog.getByRole("tab", { name: "Revisions" }).click();
  expect(calls).toBe(0);
  const active = dialog
    .getByRole("listitem")
    .filter({ hasText: "Revision 12" });
  await active.locator("summary").click();
  await expect(active.locator("pre")).toContainText("Active response 1");
  expect(calls).toBe(0);
  const historical = dialog
    .getByRole("listitem")
    .filter({ hasText: "Revision 11" });
  await historical.locator("summary").click();
  await expect(historical.getByRole("alert")).toContainText(
    "Could not load revision",
  );
  await historical.getByRole("button", { name: "Retry" }).click();
  await expect(historical.locator("pre")).toHaveText(
    JSON.stringify(revision(1, 11, "Historical response 1"), null, 2),
  );
  expect(calls).toBe(2);
  await historical.locator("summary").click();
  await expect(historical.locator("pre")).toHaveCount(0);
  await historical.locator("summary").click();
  await expect(historical.locator("pre")).toBeVisible();
  expect(calls).toBe(2);
});

test("a delayed detail response cannot populate another recording", async ({
  page,
}) => {
  let release;
  let requestedResolve;
  const requested = new Promise((resolve) => {
    requestedResolve = resolve;
  });
  const pending = new Promise((resolve) => {
    release = resolve;
  });
  let deliveredResolve;
  const delivered = new Promise((resolve) => {
    deliveredResolve = resolve;
  });
  await fixtures(page, async (route) => {
    requestedResolve();
    await pending;
    await route.fulfill({ json: revision(1, 11, "STALE HISTORICAL RESPONSE") });
    deliveredResolve();
  });
  await page.goto("/#/recordings?recording=1");
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("tab", { name: "Revisions" }).click();
  await dialog
    .getByRole("listitem")
    .filter({ hasText: "Revision 11" })
    .locator("summary")
    .click();
  await requested;
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await page.evaluate(() => {
    window.location.hash = "/recordings?recording=2";
  });
  await expect(dialog).toContainText("Active response 2");
  release();
  await delivered;
  await dialog.getByRole("tab", { name: "Revisions" }).click();
  await dialog
    .getByRole("listitem")
    .filter({ hasText: "Revision 22" })
    .locator("summary")
    .click();
  await expect(dialog.locator("pre")).toContainText("Active response 2");
  await expect(dialog).not.toContainText("STALE HISTORICAL RESPONSE");
});
