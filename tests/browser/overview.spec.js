const { test, expect } = require("@playwright/test");
const { mockAPI } = require("./overview-fixtures");

// Mocked-API suite for the Overview analytics view (every /api/** call is
// answered by overview-fixtures.js, so any running proxy works):
//
//   REPLAY_BROWSER_BASE_URL=http://127.0.0.1:18080 \
//     npx --prefix tests/browser playwright test \
//     --config=tests/browser/overview.config.js
//
// The console opens on Overview at "/". OVERVIEW_PATH lets the spec target
// another page that mounts the view.
const path = process.env.OVERVIEW_PATH || "/";

async function open(page, options) {
  const insights = [];
  await mockAPI(page, { ...options, onInsights: (url) => insights.push(url) });
  await page.goto(path);
  await expect(
    page.getByRole("heading", { name: "Overview", level: 2 }).first(),
  ).toBeVisible();
  return insights;
}

test("renders the headline, charts and breakdowns from /api/insights", async ({
  page,
}) => {
  const insights = await open(page);
  await expect(page.getByText("Cache hit rate", { exact: true })).toBeVisible();
  await expect(page.getByText(/lookups replayed/)).toBeVisible();
  await expect(page.getByText("Saved by replay")).toBeVisible();
  for (const name of [
    /Requests per day by outcome/,
    /Tokens per day/,
    /Histogram of total duration/,
  ])
    await expect(page.getByRole("group", { name })).toBeVisible();
  const models = page.getByRole("table").filter({ hasText: "Token mix" });
  await expect(models.getByRole("row")).toHaveCount(6);
  await expect(
    models.getByRole("button", { name: "openai/gpt-5.1", exact: true }),
  ).toBeVisible();
  await expect(
    page
      .getByRole("button", { name: /Summarize the attached quarterly report/ })
      .first(),
  ).toBeVisible();
  const first = insights[0];
  expect(first.searchParams.get("collection_id")).toBe("1");
  expect(first.searchParams.has("model")).toBe(false);
});

test("range and model filters refetch and scope the page", async ({ page }) => {
  const insights = await open(page);
  await page.getByRole("button", { name: "24h" }).click();
  await expect(
    page.getByRole("group", { name: /Requests per hour by outcome/ }),
  ).toBeVisible();
  const last = () => insights[insights.length - 1];
  const span =
    Date.parse(last().searchParams.get("to")) -
    Date.parse(last().searchParams.get("from"));
  expect(span).toBeLessThanOrEqual(24 * 3_600_000);

  // Clicking a model focuses the whole page on it.
  await page
    .getByRole("table")
    .filter({ hasText: "Token mix" })
    .getByRole("button", { name: "openai/o4-mini", exact: true })
    .click();
  await expect
    .poll(() => last().searchParams.get("model"))
    .toBe("openai/o4-mini");
  await expect(page.getByText("Model:")).toBeVisible();
  const models = page.getByRole("table").filter({ hasText: "Token mix" });
  await expect(models.getByRole("row")).toHaveCount(2);
  // The model menu still offers every model while filtered.
  await page.getByRole("combobox", { name: "Model filter" }).click();
  await expect(page.getByRole("option")).toHaveCount(6);
  await page.getByRole("option", { name: "All models" }).click();
  await expect.poll(() => last().searchParams.has("model")).toBe(false);
  await expect(models.getByRole("row")).toHaveCount(6);
});

test("charts are keyboard readable and have a table view", async ({ page }) => {
  await open(page);
  const chart = page.getByRole("group", {
    name: /Requests per day by outcome/,
  });
  await chart.focus();
  await page.keyboard.press("Home");
  const live = page.locator(
    `[id="${await chart.getAttribute("aria-describedby")}"]`,
  );
  await expect(live).toContainText(/Hits \d+/);
  const traffic = page.locator("section").filter({
    has: page.getByRole("heading", { name: "Traffic", exact: true }),
  });
  await traffic.getByRole("button", { name: "Table" }).click();
  await expect(traffic.getByRole("table")).toBeVisible();
  await expect(
    traffic.getByRole("columnheader", { name: "Hit rate" }),
  ).toBeVisible();
});

test("fits a phone screen without horizontal scroll", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await open(page);
  await expect(page.getByText("Cache hit rate", { exact: true })).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
});

test("an empty range explains what to do next", async ({ page }) => {
  await open(page, { empty: true });
  await expect(page.getByText(/No traffic in the last 7 days/)).toBeVisible();
  await expect(page.getByText(/OPENAI_BASE_URL=/)).toBeVisible();
  await page.getByRole("button", { name: "Show last 90 days" }).click();
  await expect(page.getByText(/No traffic in the last 90 days/)).toBeVisible();
});
