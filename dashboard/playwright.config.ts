/**
 * Playwright config — used by the Task 11-02-05A route-test gate.
 *
 * The default baseURL points at the dashboard dev server (port 3001).
 * The staging-smoke runbook (Task 11-02-06) overrides via
 * DASHBOARD_BASE_URL when targeting a non-local server.
 *
 * Vitest config (vitest.config.ts) explicitly includes only
 * `src/**​/*.test.{ts,tsx}` so this `tests/` directory is invisible to
 * the unit test runner — playwright runs separately via
 * `bunx playwright test`.
 */
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: "list",
  use: {
    baseURL: process.env.DASHBOARD_BASE_URL ?? "http://localhost:3001",
    extraHTTPHeaders: {},
  },
});
