import { defineConfig, devices } from '@playwright/test';

// The end-to-end test runs against a built app served by `vite preview`, on a backend that
// e2e/run.sh starts: a fresh Aruma server (the default) or the Firebase Emulator Suite
// (--emulators). Run it through run.sh, which sets the environment below.
const port = Number(process.env.E2E_PAGE_PORT ?? 4191);
const dist = process.env.E2E_DIST;
if (!dist) throw new Error('E2E_DIST is not set: run the tests through e2e/run.sh');

export default defineConfig({
  testDir: '.',
  testMatch: process.env.E2E_SCREENSHOTS ? 'screenshots.spec.ts' : 'team-board.spec.ts',
  outputDir: './.generated/test-results',
  workers: 1,
  timeout: 240_000,
  expect: { timeout: 15_000 },
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? [['list'], ['html', { open: 'never', outputFolder: './.generated/report' }]] : 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL: `http://127.0.0.1:${port}`,
    trace: 'retain-on-failure',
    launchOptions: {
      // The browser Playwright installed, or the one PW_CHROMIUM names.
      executablePath: process.env.PW_CHROMIUM || undefined,
      // Half of the network guard (fixtures.ts): the browser resolves no host name but this
      // machine's, so not even a WebSocket can reach Google.
      args: ['--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'],
    },
  },
  webServer: {
    command: `npx vite preview --outDir "${dist}" --port ${port} --strictPort`,
    cwd: '..',
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: false,
    // vite.config.ts reads the config file at start; preview serves the build as it is.
    env: { FIREBASE_CONFIG: process.env.FIREBASE_CONFIG ?? '', PORT: String(port) },
  },
});
