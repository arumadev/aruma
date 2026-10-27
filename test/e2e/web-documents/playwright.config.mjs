import { defineConfig, devices } from '@playwright/test';

// The tests run against a real Aruma server that run.sh starts (ARUMA_URL), with the built app
// served by vite preview on localhost: another origin than Aruma's 127.0.0.1:<port>, so every
// Firestore request is cross-origin and carries credentials. One project per engine.

if (!process.env.ARUMA_URL) {
  throw new Error('ARUMA_URL is not set: run the tests through test/e2e/web-documents/run.sh');
}

const PORT = Number(process.env.ARUMA_E2E_PAGE_PORT ?? 4190);

export default defineConfig({
  testDir: './tests',
  // Every request comes from 127.0.0.1, which Aruma rate-limits: one worker.
  workers: 1,
  timeout: 60_000,
  forbidOnly: Boolean(process.env.CI),
  // On GitHub Actions Playwright would otherwise git fetch the pull request's base commit from
  // github.com for the report's diff: the suite reaches nothing off this machine.
  captureGitInfo: { commit: false, diff: false },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: { baseURL: `http://localhost:${PORT}`, trace: 'retain-on-failure' },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        // Half of the network guard (tests/fixtures.mjs): Chromium resolves no host name but localhost.
        launchOptions: { args: ['--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'] },
      },
    },
    { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
    { name: 'webkit', use: { ...devices['Desktop Safari'] } },
  ],
  webServer: {
    command: `npx vite build --logLevel warn && npx vite preview --host localhost --port ${PORT}`,
    url: `http://localhost:${PORT}/`,
    reuseExistingServer: false,
    env: { ARUMA_URL: process.env.ARUMA_URL },
  },
});
