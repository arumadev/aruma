import { test as base, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';

// What the server wrote (server/main.go): Aruma's URL, the bucket, and each object's download
// URL, as getDownloadURL() gives it (alt=media&token=).
export const manifest = JSON.parse(readFileSync(`${process.env.ARUMA_E2E_DIR}/manifest.json`, 'utf8'));
export const aruma = manifest.aruma;
export const objects = manifest.objects;
export const marker = manifest.marker;

const port = Number(process.env.ARUMA_E2E_PAGE_PORT ?? 9261);
// The objects of the bucket without CORS (data, sdk).
export const noCORS = manifest.noCORS;

// Two app origins, both other than Aruma's and cross-site to each other (the same pages server).
export const appOrigin = `http://127.0.0.1:${port}`;
export const otherOrigin = `http://localhost:${port}`;
// A page on the dashboard's own origin, which Aruma's downloads share: a stand-in for a
// dashboard page with an HTML injection, fulfilled by the test on that origin.
export const dashboardPage = `${aruma}/__matrix/dashboard`;

const local = new Set(['127.0.0.1', 'localhost', '[::1]']);

export const test = base.extend({
  // The network guard and the console: every request is recorded, and the test fails on one
  // that left the machine (on Chromium none can: --host-resolver-rules). page.messages is the
  // page's console, where a script of the matrix's objects would say marker.
  page: async ({ page, context }, use) => {
    const left = [];
    context.on('request', (r) => {
      const u = new URL(r.url());
      if ((u.protocol === 'http:' || u.protocol === 'https:') && !local.has(u.hostname)) left.push(u.href);
    });
    page.messages = [];
    page.on('console', (m) => page.messages.push(m.text()));
    // The dashboard's session cookie: no download may read it.
    await context.addCookies([{ name: 'dashboard_session', value: 'session-secret', url: aruma }]);
    await context.route(dashboardPage, (route) => route.fulfill({
      contentType: 'text/html', body: '<!doctype html><title>dashboard</title><p>dashboard</p>',
    }));
    await use(page);
    expect(left, 'requests that left the machine').toEqual([]);
  },
});

export { expect };

// browse opens url in the page as a top-level navigation and waits a moment for any script.
export async function browse(page, url) {
  const res = await page.goto(url, { waitUntil: 'load' });
  await page.waitForTimeout(300);
  return res;
}

// browseToDownload opens url and expects the browser to save it rather than show it.
export async function browseToDownload(page, url) {
  const download = page.waitForEvent('download', { timeout: 10_000 });
  const nav = page.goto(url).then(() => 'shown', (e) => `not shown: ${e.message.split('\n')[0]}`);
  const [d, shown] = await Promise.all([download.catch(() => null), nav]);
  if (d) await d.cancel().catch(() => {});
  return { downloaded: Boolean(d), shown, filename: d?.suggestedFilename() };
}

// probe reads what a document lets a script reach: its origin, the cookies and storage of its
// origin. Run in the browsed document (Playwright's evaluation is not the page's own script).
export function probe(page) {
  return page.evaluate(() => {
    const read = (f) => { try { return String(f()); } catch (e) { return `throws ${e.name}`; } };
    return {
      origin: read(() => window.origin),
      cookie: read(() => document.cookie),
      storage: read(() => window.localStorage.getItem('matrix')),
      title: document.title,
    };
  });
}

// ran reports whether a script of the matrix's objects ran in the page.
export const ran = (page) => page.messages.includes(marker);

// mediaCheck loads a <video> or <audio> into the page, plays it muted and seeks into it: what it
// reached, or the error.
export function mediaCheck(page, { url, tag }) {
  return page.evaluate(async ({ url, tag }) => {
    const timeout = (ms, what) => new Promise((_, ko) => setTimeout(() => ko(new Error(`timeout: ${what}`)), ms));
    const once = (el, ok) => Promise.race([
      new Promise((resolve, reject) => {
        el.addEventListener(ok, resolve, { once: true });
        el.addEventListener('error', () => reject(new Error(`error ${el.error?.code ?? ''} ${el.error?.message ?? ''}`)), { once: true });
      }),
      timeout(15_000, ok),
    ]);
    try {
      const el = document.createElement(tag);
      el.muted = true;
      el.preload = 'auto';
      const loaded = once(el, 'loadedmetadata');
      el.src = url;
      document.body.append(el);
      await loaded;
      // WebKit's headless play() may never settle; playing is not what is checked, seeking is.
      const played = await Promise.race([
        el.play().then(() => true, (e) => `play: ${e.name}`),
        new Promise((resolve) => setTimeout(() => resolve('play: pending'), 3_000)),
      ]);
      el.pause();
      const seeked = once(el, 'seeked');
      el.currentTime = 2.5;
      await seeked;
      return { duration: Math.round(el.duration), currentTime: Math.round(el.currentTime * 10) / 10, played };
    } catch (e) {
      return { error: e.message };
    }
  }, { url, tag });
}

// mediaDocument checks the media document a browser shows for a clip or a song browsed to. Its
// sandbox runs no script, so no event listener or timer either: the test reads and sets the
// element from outside and polls.
export async function mediaDocument(page) {
  const state = () => page.evaluate(() => {
    const el = document.querySelector('video, audio');
    return el ? { readyState: el.readyState, duration: Math.round(el.duration || 0), currentTime: el.currentTime,
      seeking: el.seeking, error: el.error?.code ?? 0 } : { missing: document.contentType };
  });
  await expect.poll(async () => (await state()).readyState ?? -1, { timeout: 15_000 }).toBeGreaterThanOrEqual(1);
  await page.evaluate(() => {
    const el = document.querySelector('video, audio');
    el.muted = true;
    el.currentTime = 2.5;
  });
  await expect.poll(async () => {
    const s = await state();
    return !s.seeking && s.readyState >= 2 && s.currentTime >= 2.5;
  }, { timeout: 15_000 }).toBe(true);
  return state();
}

// playable is what the engine's <video> can play of the matrix's two clips. Playwright's
// WebKit answers "probably" for VP9 in WebM, yet decodes no frame of it, served by Aruma or by a
// plain file server alike (readyState stays at HAVE_METADATA): there the MP4 alone counts.
export function playable(page, browserName) {
  return page.evaluate((webkit) => {
    const v = document.createElement('video');
    return {
      mp4: v.canPlayType('video/mp4; codecs="avc1.42E01E"') !== '',
      webm: !webkit && v.canPlayType('video/webm; codecs="vp9"') !== '',
    };
  }, browserName === 'webkit');
}

// sdkRead reads public/sdk.txt of bucket with the official web SDK (firebase 13.1.0, the CDN
// build pages.mjs serves) in the page: getBytes and getBlob, as text, or the error's code. The
// production path to Aruma is S6's, so connectStorageEmulator points the SDK at Aruma; it changes
// the host alone, and the requests are production's (alt=media, X-Firebase-Storage-Version, so a
// preflight).
export function sdkRead(page, bucket) {
  return page.evaluate(async ({ bucket, aruma }) => {
    const { initializeApp } = await import('/firebasejs/13.1.0/firebase-app.js');
    const st = await import('/firebasejs/13.1.0/firebase-storage.js');
    const app = initializeApp({ apiKey: 'matrix', projectId: bucket.split('.')[0], storageBucket: bucket, appId: '1:1:web:1' }, bucket);
    const storage = st.getStorage(app);
    const u = new URL(aruma);
    st.connectStorageEmulator(storage, u.hostname, Number(u.port));
    storage.maxOperationRetryTime = 2_000; // a refused CORS is a network error, which it retries
    const ref = st.ref(storage, 'public/sdk.txt');
    const text = (p) => p.then((v) => new TextDecoder().decode(v), (e) => `error ${e.code}`);
    return {
      getBytes: await text(st.getBytes(ref)),
      getBlob: await text(st.getBlob(ref).then((b) => b.arrayBuffer())),
    };
  }, { bucket, aruma });
}

// childFrames returns what each frame below the main one shows: its URL and content type, and
// its text, or why it could not be read.
export async function childFrames(page) {
  const out = [];
  for (const f of page.frames()) {
    if (f === page.mainFrame()) continue;
    // A frame the browser refused may never answer (Firefox): 3 s, then it showed nothing.
    const doc = await Promise.race([
      f.evaluate(() => ({ type: document.contentType, text: document.body?.innerText ?? '' })),
      new Promise((_, ko) => setTimeout(() => ko(new Error('no answer in 3 s')), 3_000)),
    ]).catch((e) => ({ type: '', text: '', error: e.message.split('\n')[0] }));
    out.push({ url: f.url(), ...doc });
  }
  return out;
}

// frame adds <tag src=url> to the page and waits for it to load or for 5 s.
export function frame(page, url, tag = 'iframe') {
  return page.evaluate(({ url, tag }) => new Promise((resolve) => {
    const el = document.createElement(tag);
    el.addEventListener('load', () => resolve('load'), { once: true });
    el.addEventListener('error', () => resolve('error'), { once: true });
    setTimeout(() => resolve('timeout'), 5_000);
    el.src = url;
    if (tag === 'embed') el.type = 'application/pdf';
    document.body.append(el);
  }), { url, tag });
}
