const { defineConfig } = require("@playwright/test");
module.exports = defineConfig({
  testDir: ".",
  testMatch: "overview.spec.js",
  timeout: 30_000,
  workers: 1,
  use: {
    baseURL: process.env.REPLAY_BROWSER_BASE_URL || "http://127.0.0.1:18080",
    viewport: { width: 1440, height: 900 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  outputDir: "test-results/overview",
  reporter: [["list"]],
});
