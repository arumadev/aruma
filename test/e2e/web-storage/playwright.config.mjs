import { defineConfig, devices } from '@playwright/test';

// The tests run against the Aruma server run.sh starts (server/main.go, e2e.json), with the built
// apps served by serve.mjs (ARUMA_E2E_PAGE_PORT, default 9421) and, for SSR, by their own servers.
const PORT = Number(process.env.ARUMA_E2E_PAGE_PORT ?? 9421);
const roots = process.env.ARUMA_E2E_ROOTS ?? '';

export default defineConfig({
  testDir: './tests',
  workers: 1,
  timeout: 180_000,
  forbidOnly: Boolean(process.env.CI),
  // On GitHub Actions Playwright would otherwise git fetch the pull request's base commit from
  // github.com for the report's diff: the suite reaches nothing off this machine.
  captureGitInfo: { commit: false, diff: false },
  reporter: 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    // Half of the network guard (tests/fixtures.mjs): the browser resolves no host name but
    // localhost.
    launchOptions: { args: ['--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'] },
  },
  webServer: {
    command: `node serve.mjs ${PORT} ${roots}`,
    url: `http://127.0.0.1:${PORT}/e2e.json`,
    reuseExistingServer: false,
  },
});
