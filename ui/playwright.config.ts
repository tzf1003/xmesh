import { defineConfig } from "@playwright/test";

// One worker: the specs each launch their own Electron app, but tray and
// single-instance state is per user session, so they must not overlap.
export default defineConfig({
  testDir: "test/e2e",
  timeout: 60_000,
  expect: { timeout: 10_000 },
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
});
