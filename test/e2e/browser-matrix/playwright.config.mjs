import { defineConfig, devices } from '@playwright/test';

// The browser matrix runs against a real Aruma server that run.sh starts (manifest.json in
// ARUMA_E2E_DIR), with the app's pages served by pages.mjs. One project per engine; run.sh runs
// them one after the other.

if (!process.env.ARUMA_E2E_DIR) {
  throw new Error('ARUMA_E2E_DIR is not set: run the matrix through test/e2e/browser-matrix/run.sh');
}

const PORT = Number(process.env.ARUMA_E2E_PAGE_PORT ?? 9261);

export default defineConfig({
  testDir: './tests',
  workers: 1,
  timeout: 60_000,
  forbidOnly: Boolean(process.env.CI),
  // On GitHub Actions Playwright would otherwise git fetch the pull request's base commit from
  // github.com for the report's diff: the suite reaches nothing off this machine.
  captureGitInfo: { commit: false, diff: false },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: { trace: 'retain-on-failure', acceptDownloads: true },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        // Half of the network guard (tests/fixtures.mjs): Chromium resolves no host name but
        // localhost.
        launchOptions: { args: ['--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'] },
      },
    },
    { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'] } },
  ],
  webServer: {
    command: 'node pages.mjs',
    url: `http://127.0.0.1:${PORT}/blank`,
    reuseExistingServer: false,
    env: { ARUMA_E2E_PAGE_PORT: String(PORT) },
  },
});
