// Conversations view and the history dialog's miss explainer, against a fully
// mocked /api (see conversations-fixtures.js). Read-only: the proxy only
// serves the console. CONVERSATIONS_URL overrides the entry route.
const { test, expect } = require("@playwright/test");
const { mockAPI, THREAD, MISS } = require("./conversations-fixtures");

const ENTRY = process.env.CONVERSATIONS_URL || "/#/conversations";

test.beforeEach(async ({ page }) => {
  await mockAPI(page);
});

test("thread list filters and opens a turn-by-turn timeline", async ({
  page,
}) => {
  await page.goto(ENTRY);
  await expect(
    page.getByRole("heading", { level: 2, name: "Conversations" }),
  ).toBeVisible();
  const list = page.getByRole("list", { name: "Conversations" });
  await expect(list.getByRole("button")).toHaveCount(6);

  await page.getByRole("combobox", { name: "Filter by outcome" }).click();
  await page.getByRole("option", { name: "With misses" }).click();
  await expect(list.getByRole("button")).toHaveCount(3);
  await page
    .getByRole("searchbox", { name: "Search conversations" })
    .fill("flaky");
  await expect(list.getByRole("button")).toHaveCount(1);
  await expect(
    page.getByRole("status").filter({ hasText: "1 of 6" }),
  ).toBeVisible();

  await list.getByRole("button", { name: /cache tests are flaky/ }).click();
  await expect(
    page.getByRole("heading", {
      name: "The cache tests are flaky on CI. Can you find out why?",
    }),
  ).toBeVisible();
  const timeline = page.getByRole("group", {
    name: /Conversation timeline, 25 requests/,
  });
  const turns = timeline.getByRole("button");
  await expect(turns).toHaveCount(25);
  // The inspector starts on the first miss, and the keyboard walks the turns.
  await expect(turns.nth(12)).toHaveAttribute(
    "aria-label",
    /Turn 13, missed, 34 items \(\+3\)/,
  );
  await turns.nth(12).focus();
  await page.keyboard.press("ArrowRight");
  await expect(turns.nth(13)).toBeFocused();
  await expect(page.getByText(/^Turn 14\s*of 25$/)).toBeVisible();
  await expect(page.getByText("Replay broke at turn 13.")).toBeVisible();

  const table = page.getByRole("table");
  await expect(table.getByRole("row")).toHaveCount(26);
  await expect(table.getByRole("row").nth(14)).toContainText("retry");

  await page.getByRole("button", { name: "All conversations" }).click();
  await expect(list).toBeVisible();
});

test("a deep link opens the thread directly", async ({ page }) => {
  await page.goto(`${ENTRY}?thread=${THREAD}`);
  await expect(
    page.getByRole("heading", { name: "Turn by turn" }),
  ).toBeVisible();
});

test("a missed turn explains why it did not replay", async ({ page }) => {
  await page.goto(`${ENTRY}?thread=${THREAD}`);
  await page
    .getByRole("button", { name: "Why didn't turn 13 replay?" })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("No recording matched")).toBeVisible();
  await expect(dialog.getByText(/HTTP 404 recording_not_found/)).toBeVisible();
  await expect(
    dialog.getByRole("heading", { name: "Why didn't this replay?" }),
  ).toBeVisible();
  await expect(dialog.getByText("Recording #312")).toBeVisible();
  await expect(
    dialog.getByText("Same conversation", { exact: true }),
  ).toBeVisible();

  // The best candidate is compared automatically; volatile values lead.
  await expect(
    dialog.getByText("Likely cause: 2 values that change on every run"),
  ).toBeVisible();
  const diffs = dialog
    .getByRole("list", { name: /Differences/ })
    .getByRole("listitem");
  await expect(diffs.first()).toContainText("Likely culprit: timestamp");
  await expect(diffs.first()).toContainText("/input/0/content/0/text");
  await expect(diffs.first().locator("mark").first()).toHaveText(
    "2026-10-04T09:41:07Z",
  );
  await expect(diffs.nth(1)).toContainText("Likely culprit: generated id");
  await expect(
    diffs.nth(1).getByRole("button", { name: "Copy pointer /metadata/run_id" }),
  ).toBeVisible();
  await expect(dialog.getByText("About match exclusions.")).toBeVisible();

  // Another candidate: an earlier turn, so this request continues past it.
  await dialog.locator("label").filter({ hasText: "#111" }).click();
  await expect(
    dialog.getByText("3 more input items in this request"),
  ).toBeVisible();

  // The request itself is still readable below.
  await expect(
    dialog.getByRole("region", { name: "Request browser" }),
  ).toBeVisible();
  await expect(
    dialog.getByText(`Match key: ${"9c1e" + MISS.history_id}`, {
      exact: false,
    }),
  ).toBeVisible();
});
