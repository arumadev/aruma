import { test, expect } from '@playwright/test';
import { authHost, e2e, guard } from './fixtures.mjs';

// The control of measure.sh's flutter target: the same Flutter web app built after
// `aruma-web flutter-web --products storage` (Auth left out). firebase_core_web then loads Google's
// firebase-auth.js from www.gstatic.com (answered here from the firebase npm package), and the first
// Auth request, the sign-up with the app's throwaway email and password, is built for Google's
// host. The guard aborts it in the browser (and the browser resolves no host name but localhost), so
// nothing reaches Google: this test shows where it would have gone.
//
// ARUMA_E2E_CONTROL=<name>[@url]: the app (served as /<name>/); ARUMA_E2E_MIRROR as in bundlers.spec.
// Without ARUMA_E2E_CONTROL (run.sh, which runs every spec) there is no such app: skipped.
const control = process.env.ARUMA_E2E_CONTROL;
const [name, url] = (control ?? 'flutter-control').split('@');
const mirror = process.env.ARUMA_E2E_MIRROR;
test.skip(!control, 'measure.sh flutter sets ARUMA_E2E_CONTROL');

test(`${name}: without the auth product, sign-up is sent to Google's Auth host (aborted here)`, async ({ browser }) => {
  const log = { aruma: [], blocked: [], urls: [], mirrored: [] };
  const context = await browser.newContext();
  await guard(context, log, { mirror });
  const page = await context.newPage();
  await page.goto(url ?? `/${name}/`);
  await page.waitForFunction(() => window.__done, null, { timeout: 120_000 });
  const done = await page.evaluate(() => window.__done);
  await context.close();
  console.log(`${name}: ${JSON.stringify(done.auth)}`);
  console.log(`  aborted by the guard: ${log.blocked.map((u) => u.replace(/key=[^&]*/, 'key=…')).join(', ')}`);
  console.log(`  from www.gstatic.com (answered from npm): ${log.mirrored.map((u) => u.split('/').pop()).join(', ')}`);
  // The app stopped at its first Auth step, as a network error.
  expect(done.auth).toEqual([expect.objectContaining({ op: 'auth.signUp', ok: false })]);
  expect(done.auth[0].detail).toMatch(/^network-request-failed/);
  expect(done.results).toEqual([]);
  // The one request that left the page was the sign-up, to Google's host, with the app's API key.
  expect(log.blocked.map((u) => new URL(u).hostname)).toEqual(['identitytoolkit.googleapis.com']);
  const signUp = new URL(log.blocked[0]);
  expect(`${signUp.origin}${signUp.pathname}`).toBe('https://identitytoolkit.googleapis.com/v1/accounts:signUp');
  expect(signUp.searchParams.get('key')).toBe(e2e.config.apiKey);
  expect(log.urls.filter((u) => authHost.test(new URL(u).hostname)).length).toBe(1);
  // Aruma saw no Auth request; Google's firebase-auth.js was the one loaded.
  expect(log.aruma.filter((r) => /^\/(identitytoolkit|securetoken)\.googleapis\.com\//.test(r.path))).toEqual([]);
  expect(log.mirrored.filter((u) => u.endsWith('/firebase-auth.js')).length).toBe(1);
});
