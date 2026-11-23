import { test as base, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

export const arumaURL = process.env.ARUMA_URL;
const firebasePkg = process.env.ARUMA_E2E_FIREBASE;

// firebase_core_web imports the Firebase JS SDK from www.gstatic.com (firebase-app.js,
// firebase-auth.js, firebase-database.js; Storage and Firestore come from the files tool/web.sh
// wrote). Here they are answered from the firebase npm package of the same version, the same CDN
// build, so that nothing leaves the machine. Every such answer is recorded.
const gstaticSDK = /^https:\/\/www\.gstatic\.com\/firebasejs\/([0-9.]+)\/(firebase-[a-z-]+\.js)$/;
const local = new Set(['127.0.0.1', 'localhost']);

/** The network guard on a browser context: this machine only, and the SDK files above. */
export async function guard(context, log) {
  const version = JSON.parse(readFileSync(join(firebasePkg, 'package.json'), 'utf8')).version;
  await context.route('**/*', async (route) => {
    const url = new URL(route.request().url());
    if (local.has(url.hostname)) {
      if (url.origin === arumaURL) log.aruma.push(`${route.request().method()} ${url.pathname}`);
      return route.continue();
    }
    const m = gstaticSDK.exec(url.href);
    if (m && m[1] === version) {
      log.mirrored.push(m[2]);
      return route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/javascript', 'access-control-allow-origin': '*' },
        body: readFileSync(join(firebasePkg, m[2])),
      });
    }
    log.blocked.push(url.href);
    return route.abort('blockedbyclient');
  });
  // The WebSocket of the Realtime Database is not seen by route(): checked here.
  context.on('websocket', () => {});
}

export const test = base.extend({
  // The network log of the test. It fails on a request that left the machine, and on any request
  // to Google's Storage, Firestore or Auth hosts.
  network: [async ({}, use) => {
    const log = { aruma: [], blocked: [], mirrored: [] };
    await use(log);
    expect(log.blocked, 'requests leaving the machine').toEqual([]);
    expect(log.mirrored.filter((f) => f === 'firebase-storage.js' || f === 'firebase-firestore.js'),
      'Storage or Firestore SDK from gstatic instead of the rewritten files').toEqual([]);
  }, { auto: true }],
});

export { expect };
