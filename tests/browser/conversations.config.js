const { defineConfig } = require("@playwright/test");
module.exports = defineConfig({
  testDir: ".",
  testMatch: "conversations.spec.js",
  timeout: 30_000,
  workers: 1,
  use: {
    baseURL: process.env.REPLAY_BROWSER_BASE_URL || "http://127.0.0.1:18080",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  outputDir: "test-results/conversations",
  reporter: [["list"]],
});
