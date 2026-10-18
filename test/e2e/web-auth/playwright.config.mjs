import { defineConfig, devices } from '@playwright/test';

// The tests run against a real Aruma server that run.sh starts (ARUMA_URL), with the web app
// served by Vite. Every test runs once per major version of the firebase package (X-6).

if (!process.env.ARUMA_URL) {
  throw new Error('ARUMA_URL is not set: run the tests through test/e2e/web-auth/run.sh');
}

const PORT = 4180;
// ARUMA_E2E_MODE: emulator (default), production or production-build (see vite.config.mjs). The
// build-time mode is served from a production build, as a developer ships it.
const mode = process.env.ARUMA_E2E_MODE ?? 'emulator';
const command = mode === 'production-build'
  ? `npx vite build --logLevel warn && npx vite preview --port ${PORT}`
  : `npx vite --port ${PORT}`;

export default defineConfig({
  testDir: './tests',
  // One browser at a time: every request comes from 127.0.0.1, which Aruma rate-limits.
  workers: 1,
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
  },
  projects: ['11', '12', '13'].map((major) => ({ name: `firebase-${major}`, use: { firebaseMajor: major, mode } })),
  webServer: {
    command,
    url: `http://127.0.0.1:${PORT}/`,
    reuseExistingServer: false,
    env: { VITE_ARUMA_URL: process.env.ARUMA_URL, ARUMA_E2E_MODE: mode },
  },
});
