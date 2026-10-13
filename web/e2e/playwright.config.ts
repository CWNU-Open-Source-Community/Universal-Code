import { defineConfig } from "@playwright/test";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const artifacts = resolve(here, "../../output/playwright/workbench-journeys");

export default defineConfig({
  testDir: here,
  testMatch: "**/*.spec.ts",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  globalSetup: "./global-setup.ts",
  outputDir: resolve(artifacts, "results"),
  reporter: [["list"], ["html", { outputFolder: resolve(artifacts, "report"), open: "never" }],
    ["json", { outputFile: resolve(artifacts, "results.json") }]],
  use: {
    browserName: "chromium",
    channel: process.env.WORKBENCH_JOURNEY_BROWSER_CHANNEL,
    headless: true,
    locale: "zh-CN",
    viewport: { width: 1280, height: 900 },
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
});
