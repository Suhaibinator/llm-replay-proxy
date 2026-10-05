// Mocked suite for the live Traffic and Recordings views. Every /api/** call
// is answered from traffic-fixtures.js, so it never touches the proxy's data.
const { test, expect } = require("@playwright/test");
const { mockAPI, moreHistory } = require("./traffic-fixtures");

// The console routes views by hash: #/traffic?outcome=miss, #/recordings.
const START = process.env.REPLAY_CONSOLE_PATH || "/";
const open = (page, route) => page.goto(`${START}#/${route}`);

const trafficRows = (page) =>
  page.getByRole("table", { name: /^Requests/ }).locator("tbody tr");
const recordingRows = (page) =>
  page.getByRole("table", { name: /^Recordings/ }).locator("tbody tr");

test("traffic filters by outcome chip, search and model", async ({ page }) => {
  const state = await mockAPI(page);
  await open(page, "traffic");
  await expect(trafficRows(page)).toHaveCount(state.history.length);

  const misses = state.history.filter((h) => h.outcome === "miss");
  const chip = page.getByRole("button", { name: /^miss\s*\d+$/ });
  await expect(chip).toContainText(String(misses.length));
  await chip.click();
  await expect(chip).toHaveAttribute("aria-pressed", "true");
  await expect(trafficRows(page)).toHaveCount(misses.length);

  const term = "regex";
  const matching = misses.filter((h) =>
    (h.request?.preview || "").toLowerCase().includes(term),
  );
  await page.getByRole("searchbox", { name: "Search requests" }).fill(term);
  await expect(trafficRows(page)).toHaveCount(matching.length);

  // Clearing everything brings every row back; then filter by model.
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(trafficRows(page)).toHaveCount(state.history.length);
  const model = "x-ai/grok-4-fast";
  await page.getByRole("combobox", { name: "Model" }).click();
  await page.getByRole("option", { name: new RegExp(`^${model}`) }).click();
  await expect(trafficRows(page)).toHaveCount(
    state.history.filter((h) => h.request?.model === model).length,
  );
});

test("a traffic link pre-filters the view", async ({ page }) => {
  const state = await mockAPI(page);
  await open(page, "traffic?outcome=error");
  await expect(page.getByRole("heading", { name: "Traffic" })).toBeVisible();
  const failed = state.history.filter((h) =>
    ["error", "incomplete"].includes(h.outcome),
  );
  await expect(trafficRows(page)).toHaveCount(failed.length);
  await expect(
    page.getByRole("button", { name: /^incomplete\s*\d+$/ }),
  ).toHaveAttribute("aria-pressed", "true");
});

test("traffic polls only for new rows, highlights them and can pause", async ({
  page,
}) => {
  const state = await mockAPI(page);
  await open(page, "traffic");
  const rows = trafficRows(page);
  const before = state.history.length;
  await expect(rows).toHaveCount(before);

  const added = moreHistory(state.history.at(-1).id, 3);
  state.history.push(...added);
  await expect(rows).toHaveCount(before + 3, { timeout: 15_000 });
  expect(state.historyRequests.some((q) => /after_id=\d+/.test(q))).toBe(true);
  // Newest first, and the arrivals are marked.
  await expect(rows.first()).toHaveAttribute("data-fresh", "true");
  const newest = added.at(-1).request?.preview;
  if (newest) await expect(rows.first()).toContainText(newest);

  await page.getByRole("button", { name: "Live" }).click();
  await expect(page.getByRole("button", { name: "Paused" })).toBeVisible();
  state.history.push(...moreHistory(state.history.at(-1).id, 2, Date.now(), 5));
  const resume = page.getByRole("button", { name: "2 new" });
  await expect(resume).toBeVisible({ timeout: 15_000 });
  await expect(rows).toHaveCount(before + 3);
  await resume.click();
  await expect(rows).toHaveCount(before + 5);
  await expect(page.getByRole("button", { name: "Live" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
});

test("traffic rows open the recording or the request details", async ({
  page,
}) => {
  const state = await mockAPI(page);
  await open(page, "traffic");
  const rows = trafficRows(page);
  await expect(rows).toHaveCount(state.history.length);

  // Keyboard: arrow keys move between rows' primary buttons.
  const first = rows.first().locator("button[data-row-primary]");
  await first.focus();
  await page.keyboard.press("ArrowDown");
  await expect(rows.nth(1).locator("button[data-row-primary]")).toBeFocused();

  const hitIndex = [...state.history]
    .reverse()
    .findIndex((h) => h.recording_id);
  await rows.nth(hitIndex).locator("button[data-row-primary]").click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("Escape");
  const dialog = page.getByRole("dialog");
  if (await dialog.count())
    await dialog.getByRole("button", { name: /close/i }).first().click();
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await rows.first().getByRole("button", { name: "Request details" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
});

test("recordings sort by column and filter never replayed", async ({
  page,
}) => {
  const state = await mockAPI(page);
  await open(page, "recordings");
  const rows = recordingRows(page);
  await expect(rows).toHaveCount(100); // first page
  await expect(page.getByText("1–100 of 320")).toBeVisible();

  const costed = state.recordings.filter((r) => r.response?.cost != null);
  const byCost = [...costed].sort((a, b) => b.response.cost - a.response.cost);
  const costHeader = page.getByRole("columnheader", { name: "Cost" });
  await costHeader.getByRole("button").click();
  await expect(costHeader).toHaveAttribute("aria-sort", "descending");
  await expect(rows.first()).toContainText(byCost[0].request.preview);
  await costHeader.getByRole("button").click();
  await expect(costHeader).toHaveAttribute("aria-sort", "ascending");
  await expect(rows.first()).toContainText(byCost.at(-1).request.preview);
  await page.getByRole("button", { name: "Next page" }).click();
  await expect(page.getByText("101–200 of 320")).toBeVisible();
  const never = state.recordings.filter((r) => r.hits === 0).length;
  await page.getByRole("button", { name: /^Never replayed/ }).click();
  await expect(
    page.getByText(`${never} of ${state.recordings.length} recordings`),
  ).toBeVisible();
  await expect(rows).toHaveCount(Math.min(100, never));

  await rows.first().locator("button[data-row-primary]").click();
  await expect(page.getByRole("dialog")).toBeVisible();
});

test("views fit a 390px screen without horizontal scrolling", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockAPI(page);
  await open(page, "traffic");
  await expect(trafficRows(page).first()).toBeVisible();
  const width = () => page.evaluate(() => document.documentElement.scrollWidth);
  expect(await width()).toBeLessThanOrEqual(390);
  await open(page, "recordings");
  await expect(recordingRows(page).first()).toBeVisible();
  expect(await width()).toBeLessThanOrEqual(390);
});
