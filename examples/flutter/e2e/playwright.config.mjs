import { defineConfig, devices } from '@playwright/test';

// run.sh starts Aruma (ARUMA_URL) and serves the built app (ARUMA_E2E_PAGE) on localhost.
if (!process.env.ARUMA_URL || !process.env.ARUMA_E2E_PAGE) {
  throw new Error('ARUMA_URL and ARUMA_E2E_PAGE are not set: run the tests through examples/flutter/e2e/run.sh');
}

export default defineConfig({
  testDir: './tests',
  // Two users in two browser contexts make one scenario; the server rate-limits one address.
  workers: 1,
  timeout: 240_000,
  expect: { timeout: 20_000 },
  forbidOnly: Boolean(process.env.CI),
  captureGitInfo: { commit: false, diff: false },
  reporter: 'list',
  use: {
    baseURL: process.env.ARUMA_E2E_PAGE,
    trace: 'retain-on-failure',
    // A phone, as the screenshots in the README.
    viewport: { width: 412, height: 915 },
    deviceScaleFactor: 2,
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 412, height: 915 },
        deviceScaleFactor: 2,
        launchOptions: {
          executablePath: process.env.PW_CHROMIUM || undefined,
          // Half of the network guard (tests/fixtures.mjs): no host name resolves but localhost.
          args: ['--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'],
        },
      },
    },
  ],
});
