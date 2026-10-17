import { defineConfig, devices } from '@playwright/test';
import type { Options } from './e2e/fixtures';

// End-to-end and visual tests run against the static export (npm run build) served by
// e2e/serve.mjs, with /console/v1 answered by the in-memory mock in e2e/mock-api.ts. Every
// test runs twice: Arabic right-to-left and English left-to-right.
//
// Set ARUMA_API_URL (and ARUMA_SETUP_CODE on a fresh server) to also run the live smoke test
// against a real `aruma serve`.

const PORT = 4173;
const live = Boolean(process.env.ARUMA_API_URL);

const browser = {
  ...devices['Desktop Chrome'],
  viewport: { width: 1280, height: 800 },
  timezoneId: 'UTC',
};

export default defineConfig<Options>({
  testDir: './e2e',
  snapshotPathTemplate: '{testDir}/__screenshots__/{projectName}/{testFilePath}/{arg}-{platform}{ext}',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : 3,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  expect: {
    toHaveScreenshot: { maxDiffPixels: 50, animations: 'disabled', caret: 'hide' },
  },
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    acceptDownloads: true,
  },
  projects: [
    { name: 'ar-rtl', testIgnore: /live\.spec/, use: { ...browser, locale: 'ar', appLocale: 'ar' } },
    { name: 'en-ltr', testIgnore: /live\.spec/, use: { ...browser, locale: 'en-US', appLocale: 'en' } },
    ...(live ? [{ name: 'live', testMatch: /live\.spec/, use: { ...browser, appLocale: 'ar' as const } }] : []),
  ],
  webServer: {
    command: 'node e2e/serve.mjs',
    url: `http://127.0.0.1:${PORT}/login/`,
    reuseExistingServer: !process.env.CI,
    env: { PORT: String(PORT), ...(process.env.ARUMA_API_URL && { ARUMA_API_URL: process.env.ARUMA_API_URL }) },
  },
});
