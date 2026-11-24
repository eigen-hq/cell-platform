import { defineConfig, devices } from "@playwright/test";

// Runs against a cell's console (CONSOLE_URL); started by TestConsoleLogin
// in test/e2e, which creates the accounts and passes them in.
export default defineConfig({
  testDir: "tests",
  timeout: 60_000,
  retries: 0,
  reporter: "line",
  use: {
    baseURL: process.env.CONSOLE_URL,
    // The local cell's certificates come from its own CA.
    ignoreHTTPSErrors: true,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
