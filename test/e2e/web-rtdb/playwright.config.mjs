import { defineConfig, devices } from '@playwright/test';

// The tests run against a real Aruma server that run.sh starts (ARUMA_URL), with the web app
// served by Vite. Every test runs once per major version of the firebase package.

if (!process.env.ARUMA_URL) {
  throw new Error('ARUMA_URL is not set: run the tests through test/e2e/web-rtdb/run.sh');
}

const PORT = Number(process.env.ARUMA_E2E_PAGE_PORT ?? 4181);

export default defineConfig({
  testDir: './tests',
  // One test at a time: the offline tests take the whole server down.
  workers: 1,
  timeout: 90_000,
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    // Half of the network guard (tests/fixtures.mjs): the browser resolves no host name but
    // localhost, so not even a WebSocket can reach Google.
    launchOptions: { args: ['--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'] },
  },
  projects: ['11', '12', '13'].map((major) => ({ name: `firebase-${major}`, use: { firebaseMajor: major } })),
  webServer: {
    command: `npx vite --port ${PORT}`,
    url: `http://127.0.0.1:${PORT}/`,
    reuseExistingServer: false,
    env: { ARUMA_URL: process.env.ARUMA_URL },
  },
});
