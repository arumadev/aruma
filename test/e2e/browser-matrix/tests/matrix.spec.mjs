import {
  test, expect, manifest, objects, noCORS, appOrigin, otherOrigin, dashboardPage, sdkRead,
  browse, browseToDownload, probe, ran, mediaCheck, mediaDocument, playable, childFrames, frame,
} from './fixtures.mjs';

// F §2.4's cases in the browser, numbered as there (ADR-022 §10.6). Every download URL is the
// one getDownloadURL() gives (alt=media&token=), in a bucket with Firebase's documented cors.json;
// a second bucket has no CORS. The app's pages are on two origins other than Aruma's, appOrigin
// and otherOrigin, cross-site to each other. Each protection is checked with a control that shows
// the check would see it fail.

const sandboxCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox";

test.describe('browsing to an active document runs no script', () => {
  test('control: the same page without Aruma\'s headers runs its script', async ({ page }) => {
    await browse(page, `${appOrigin}/evil.html`);
    expect(ran(page)).toBe(true);
  });

  // 1. text/html: no script, an opaque origin, nothing of the dashboard's cookies or storage.
  // 5. The same with a contentDisposition: inline the client set.
  for (const [n, key] of [[1, 'evilHTML'], [5, 'inlineHTML']]) {
    test(`${n}. ${objects[key].name}`, async ({ page }) => {
      const res = await browse(page, objects[key].url);
      expect(res.status()).toBe(200);
      expect(res.headers()['content-security-policy']).toBe(sandboxCSP);
      if (key === 'inlineHTML') expect(res.headers()['content-disposition']).toBe('inline');
      expect(ran(page), 'the page\'s script ran').toBe(false);
      const p = await probe(page);
      expect(p.origin).toBe('null');
      expect(p.cookie).not.toContain('session-secret');
      expect(p.cookie).toMatch(/^(|throws SecurityError)$/);
      expect(p.storage).toBe('throws SecurityError');
      expect(p.title).toBe('matrix');
    });
  }

  // 2. An SVG with a script: drawn, and the script does not run; drawn in an <img> elsewhere.
  test('2. evil.svg', async ({ page }) => {
    const res = await browse(page, objects.evilSVG.url);
    expect(res.headers()['content-type']).toBe('image/svg+xml');
    expect(ran(page)).toBe(false);
    const doc = await page.evaluate(() => ({
      root: document.documentElement.localName,
      fill: document.getElementById('r')?.getAttribute('fill'),
      origin: window.origin,
    }));
    expect(doc).toEqual({ root: 'svg', fill: 'green', origin: 'null' });

    await page.goto(`${otherOrigin}/blank`);
    const width = await page.evaluate((url) => new Promise((resolve) => {
      const img = new Image();
      img.onload = () => resolve(img.naturalWidth);
      img.onerror = () => resolve('error');
      img.src = url;
    }), objects.evilSVG.url);
    expect(width).toBe(40);
  });

  // 3. No stored type and a name that says HTML: octet-stream, which no browser renders.
  test('3. x.html without a contentType', async ({ page }) => {
    const res = await page.request.get(objects.untypedHTML.url);
    expect(res.headers()['content-type']).toBe('application/octet-stream');
    const r = await browseToDownload(page, objects.untypedHTML.url);
    expect(r.downloaded, JSON.stringify(r)).toBe(true);
    expect(ran(page)).toBe(false);
  });

  // 4. text/plain holding HTML: shown as text.
  test('4. plain.txt holding HTML', async ({ page }) => {
    await browse(page, objects.plainHTML.url);
    expect(ran(page)).toBe(false);
    const text = await page.evaluate(() => document.body.innerText);
    expect(text).toContain('<script>');
    expect(await page.evaluate(() => document.querySelector('script'))).toBeNull();
  });
});

// 6. <img>, <video> and <audio> on another origin load (CORP cross-origin), also on a page with
// COEP require-corp; the control is an image without CORP, which COEP refuses.
test.describe('6. embedding from another origin', () => {
  for (const path of ['/blank', '/coep']) {
    test(`on ${path}`, async ({ page, browserName }) => {
      await page.goto(`${otherOrigin}${path}`);
      if (path === '/coep') {
        expect(await page.evaluate(() => self.crossOriginIsolated), 'the page is cross-origin isolated').toBe(true);
        const control = await page.evaluate((url) => new Promise((resolve) => {
          const img = new Image();
          img.onload = () => resolve('loaded');
          img.onerror = () => resolve('blocked');
          img.src = url;
        }), `${appOrigin}/nocorp.png`);
        expect(control, 'control: an image without CORP').toBe('blocked');
      }
      for (const key of ['png', 'evilSVG']) {
        const w = await page.evaluate((url) => new Promise((resolve) => {
          const img = new Image();
          img.onload = () => resolve(img.naturalWidth);
          img.onerror = () => resolve('error');
          img.src = url;
        }), objects[key].url);
        expect(w, key).toBeGreaterThan(0);
      }
      const can = await playable(page, browserName);
      for (const key of ['mp4', 'webm'].filter((k) => can[k])) {
        const v = await mediaCheck(page, { url: objects[key].url, tag: 'video' });
        expect(v, key).toMatchObject({ duration: 4, currentTime: 2.5 });
      }
      const a = await mediaCheck(page, { url: objects.mp3.url, tag: 'audio' });
      expect(a, 'mp3').toMatchObject({ duration: 4, currentTime: 2.5 });
    });
  }
});

// 7. fetch() and the SDK's getBytes and getBlob from another origin: refused by a bucket without
// CORS, as a new Firebase bucket; served by one with Firebase's documented cors.json
// ({origin: ["*"], method: ["GET"], maxAgeSeconds: 3600}), whose preflight (the SDK sends
// X-Firebase-Storage-Version) passes. The same for a gzip object, which the browser decodes (11,
// as a browser sees it).
test('7. fetch and the SDK from another origin, by the bucket\'s CORS', async ({ page }) => {
  const read = (url) => page.evaluate((url) => fetch(url).then(
    async (r) => ({ status: r.status, text: await r.text() }),
    (e) => ({ error: e.name })), url);
  await page.goto(`${appOrigin}/blank`);
  expect(await read(noCORS.data.url)).toEqual({ error: 'TypeError' });
  const refused = await sdkRead(page, manifest.noCORSBucket);
  expect(refused.getBytes).toMatch(/^error storage\//);
  expect(refused.getBlob).toMatch(/^error storage\//);

  expect(await read(objects.data.url)).toEqual({ status: 200, text: objects.data.body });
  expect(await read(objects.gzip.url)).toEqual({ status: 200, text: objects.gzip.body });
  expect(await sdkRead(page, manifest.bucket)).toEqual({ getBytes: objects.sdk.body, getBlob: objects.sdk.body });
  // A refusal reaches the page with its status: the SDK reads it.
  expect(await read(objects.data.url.replace(/token=[^&]+/, 'token=wrong'))).toMatchObject({ status: 403 });
});

// 8. On the dashboard's origin: a download is never registered as a service worker, nor loaded as
// a script. The control registers a service worker of the pages' origin, the same script served
// without Aruma's refusal: the engine registers one. (Playwright's Firefox does not route a
// service worker's script, so the control cannot be a fulfilled script on Aruma's origin.)
test('8. service worker and script from the dashboard\'s origin', async ({ page }) => {
  const register = (url) => page.evaluate(async (url) => {
    if (!navigator.serviceWorker) return 'unsupported';
    try {
      const reg = await navigator.serviceWorker.register(url);
      await reg.unregister();
      return 'registered';
    } catch (e) {
      return `refused ${e.name}`;
    }
  }, url);
  await page.goto(`${appOrigin}/blank`);
  expect(await register(`${appOrigin}/sw.js`), 'control: a service worker of the pages\' origin').toBe('registered');
  await page.goto(dashboardPage);
  expect(await register(objects.sw.url)).toMatch(/^refused /);

  const load = (url) => page.evaluate((url) => new Promise((resolve) => {
    const s = document.createElement('script');
    s.onload = () => resolve('loaded');
    s.onerror = () => resolve('refused');
    s.src = url;
    document.head.append(s);
  }), url);
  expect(await load(objects.sw.url)).toBe('refused');
  expect(ran(page)).toBe(false);
  // From an app's origin the same script loads: the refusal is the dashboard's origin alone.
  await page.goto(`${otherOrigin}/blank`);
  expect(await load(objects.sw.url)).toBe('loaded');
});

// 9. A download in an <iframe> on another page is not shown (X-Frame-Options: DENY).
test('9. iframe', async ({ page }) => {
  await page.goto(`${appOrigin}/blank`);
  await frame(page, `${otherOrigin}/frame.html`);
  expect((await childFrames(page)).some((f) => f.text.includes('MATRIX-FRAME-CONTENT')), 'control: a frame that may be framed').toBe(true);

  await page.goto(`${appOrigin}/blank`);
  await frame(page, objects.frameText.url);
  const frames = await childFrames(page);
  expect(frames.some((f) => f.text.includes('MATRIX-FRAME-CONTENT')), JSON.stringify(frames)).toBe(false);
});

// 10. Range, from a page the bucket's CORS allows: one range is 206, past the end 416, two ranges
// (not safelisted, so a preflight) the whole object. The documented cors.json exposes no header,
// so Content-Range stays hidden from the page (the acceptance tests read it).
test('10. Range', async ({ page }) => {
  await page.goto(`${appOrigin}/blank`);
  const get = (range) => page.evaluate(async ([url, range]) => {
    const r = await fetch(url, { headers: { range } });
    return { status: r.status, text: await r.text() };
  }, [objects.data.url, range]);
  const n = objects.data.size;
  expect(await get('bytes=0-0')).toEqual({ status: 206, text: '0' });
  expect(await get('bytes=5-')).toEqual({ status: 206, text: objects.data.body.slice(5) });
  expect(await get(`bytes=${n}-`)).toMatchObject({ status: 416 });
  expect(await get('bytes=0-1,4-5')).toEqual({ status: 200, text: objects.data.body });
});

// 13. A PDF: saved, never shown, browsed to or framed from another origin.
test('13. report.pdf', async ({ page }) => {
  const r = await browseToDownload(page, objects.pdf.url);
  expect(r.downloaded, JSON.stringify(r)).toBe(true);
  expect(r.filename).toBe('report.pdf');
  expect(ran(page)).toBe(false);
  for (const tag of ['iframe', 'embed']) {
    await page.goto(`${appOrigin}/blank`);
    page.on('download', (d) => d.cancel().catch(() => {}));
    await frame(page, objects.pdf.url, tag);
    const frames = await childFrames(page);
    expect(frames.filter((f) => f.type === 'application/pdf'), `${tag}: ${JSON.stringify(frames)}`).toEqual([]);
  }
});

// 14. The clip and the song browsed to: they play and seek, under sandbox. (Embedded: case 6.)
// WebKit must play the MP4 (Safari seeks with Range, F §2.2-5).
test('14. media browsed to', async ({ page, browserName }) => {
  await page.goto(`${otherOrigin}/blank`);
  const can = await playable(page, browserName);
  if (browserName === 'webkit') expect(can.mp4, 'WebKit plays H.264 in MP4').toBe(true);
  const keys = ['mp4', 'webm'].filter((k) => can[k]);
  expect(keys.length, 'the engine plays one of the clips').toBeGreaterThan(0);
  for (const key of [...keys, 'mp3']) {
    const res = await browse(page, objects[key].url);
    expect(res.headers()['content-security-policy']).toContain('sandbox');
    const m = await mediaDocument(page);
    expect(m, key).toMatchObject({ duration: 4, error: 0 });
  }
  expect(ran(page)).toBe(false);
});

