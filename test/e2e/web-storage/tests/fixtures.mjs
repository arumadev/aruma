import { test as base, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const dir = process.env.ARUMA_E2E_DIR ?? new URL('../.generated/', import.meta.url).pathname;
export const e2e = JSON.parse(readFileSync(join(dir, 'e2e.json'), 'utf8'));
export const arumaURL = e2e.aruma;

// Every Google host an SDK could reach.
const google = /(^|\.)(googleapis\.com|gstatic\.com|google\.com|googleusercontent\.com|firebaseio\.com|firebasedatabase\.app|firebaseapp\.com|web\.app|recaptcha\.net|google-analytics\.com|googletagmanager\.com|doubleclick\.net|withgoogle\.com)$/;
const local = new Set(['127.0.0.1', 'localhost']);

// The Firebase JS SDK files Flutter web's firebase_core_web imports from www.gstatic.com: with
// mirror set, they are answered from the firebase npm package of this suite instead (the same CDN
// build), so the page runs with nothing leaving the machine. Every such answer is recorded.
const gstaticSDK = /^https:\/\/www\.gstatic\.com\/firebasejs\/([0-9.]+)\/(firebase-[a-z-]+\.js)$/;
const firebasePkg = new URL('../node_modules/firebase/', import.meta.url).pathname;

// guard puts the network guard on a browser context: HTTP to this machine goes through (Aruma's
// requests are recorded with the kind of Authorization they carry, never its value), anything else
// is aborted and recorded. The browser resolves no host name but localhost
// (--host-resolver-rules, playwright.config.mjs), so nothing can get past route() either.
export async function guard(context, log, { mirror } = {}) {
  await context.route('**/*', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    log.urls.push(url.href);
    if (local.has(url.hostname)) {
      if (url.origin === arumaURL) {
        const auth = (await req.headerValue('authorization')) ?? '';
        log.aruma.push({ method: req.method(), path: url.pathname, search: url.search, auth: auth.split(' ')[0], type: req.resourceType() });
      }
      return route.continue();
    }
    const m = gstaticSDK.exec(url.href);
    if (mirror && m && m[1] === mirror) {
      log.mirrored.push(url.href);
      return route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/javascript', 'access-control-allow-origin': '*' },
        body: readFileSync(join(firebasePkg, m[2])),
      });
    }
    log.blocked.push(url.href);
    return route.abort('blockedbyclient');
  });
}

export const test = base.extend({
  // The network log of a test; the test fails on a request that left the machine.
  network: [async ({}, use) => {
    const log = { aruma: [], blocked: [], urls: [], mirrored: [] };
    await use(log);
    expect(log.blocked.filter((u) => google.test(new URL(u).hostname)), 'requests to Google').toEqual([]);
    expect(log.blocked, 'requests leaving the machine').toEqual([]);
    expect(log.urls.filter((u) => new URL(u).hostname === 'firebasestorage.googleapis.com'), 'URLs on the Storage host').toEqual([]);
  }, { auto: true }],
});

export { expect };

// loadApp opens a guarded page on path and waits for its window.__done.
export async function loadApp(browser, network, path, { mirror, timeout = 120_000 } = {}) {
  const context = await browser.newContext();
  await guard(context, network, { mirror });
  const page = await context.newPage();
  page.errors = [];
  page.on('pageerror', (e) => page.errors.push(String(e)));
  page.on('console', (m) => m.type() === 'error' && page.errors.push(m.text()));
  await page.goto(path);
  const done = await page.waitForFunction(() => window.__done, null, { timeout }).then(() => page.evaluate(() => window.__done));
  return { context, page, done };
}

/** The eleven operations, all ok and none with a link off Aruma. */
export function expectEleven(done) {
  expect(done.error, 'the app failed before its operations').toBeUndefined();
  const summary = done.results.map((r) => `${r.ok ? '✓' : r.warn ? '⚠' : '✗'} ${r.op}: ${r.detail}`);
  expect(summary.filter((s) => !s.startsWith('✓')), summary.join('\n')).toEqual([]);
  expect(done.results.map((r) => r.op)).toEqual([
    'init', 'upload.small', 'upload.resumable3MB', 'getMetadata', 'getDownloadURL', 'download.getBytes',
    'download.getBlob', 'list', 'downloadURL.fetch', 'downloadURL.img', 'delete',
  ]);
  return summary;
}

/** What Aruma saw: Storage on its root, never under the Google-host prefix, with the user's token. */
export function expectStorageOnRoot(network, { signedIn = true } = {}) {
  const storage = network.aruma.filter((r) => r.path.startsWith('/v0/'));
  expect(network.aruma.filter((r) => r.path.startsWith('/firebasestorage.googleapis.com/')), 'prefixed Storage requests').toEqual([]);
  const kinds = new Set(storage.map((r) => `${r.method} ${r.search.includes('alt=media') ? 'media' : r.search.includes('upload_id') ? 'chunk' : 'api'}`));
  for (const k of ['POST api', 'GET api', 'GET media', 'DELETE api']) expect(kinds, [...kinds].join(', ')).toContain(k);
  // The SDK's own requests (XHR in the browser) carry the ID token; the link in <img> and fetch
  // carries its download token in the query instead. Chunks go to the absolute X-Goog-Upload-URL.
  // The worker's requests (open/worker-…) are signed out by design (src/worker.js).
  const worker = (r) => (r.path + r.search).includes('open%2Fworker-');
  const sdk = storage.filter((r) => r.type === 'xhr' && r.method !== 'OPTIONS' && !r.search.includes('upload_id') && !worker(r));
  expect(sdk.length).toBeGreaterThan(5);
  if (signedIn) expect(sdk.filter((r) => r.auth !== 'Firebase').map((r) => `${r.method} ${r.path}`), 'SDK requests without the ID token').toEqual([]);
  const img = storage.filter((r) => r.type === 'image');
  expect(img.length, 'the <img> request').toBeGreaterThan(0);
  return storage;
}
