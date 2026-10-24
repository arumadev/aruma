import { test, expect, e2e, loadApp, expectEleven, expectStorageOnRoot } from './fixtures.mjs';

// The app of src/main.js as each bundler built it (build.mjs), from the real npm packages: the
// domain-only switch in the browser, eleven operations with no ⚠, and the <img> on the unified link.
//
// ARUMA_E2E_APPS lists the apps: a name is served by serve.mjs under /<name>/; name@url is loaded
// from url (a dev server, measure.sh). ARUMA_E2E_SIGNED_OUT=1: the app signs no one in.
const apps = (process.env.ARUMA_E2E_APPS ?? 'vite,webpack,esbuild').split(',').filter(Boolean).map((a) => {
  const [name, url] = a.split('@');
  return { name, url: url ?? `/${name}/` };
});
const signedIn = process.env.ARUMA_E2E_SIGNED_OUT !== '1';
// ARUMA_E2E_MIRROR=<version>: answer the Firebase JS SDK of that version on www.gstatic.com from the
// firebase npm package (Flutter web's firebase_core_web imports it from there).
const mirror = process.env.ARUMA_E2E_MIRROR;

for (const { name, url } of apps) {
  test(`${name}: eleven operations on Aruma, links unified`, async ({ browser, network }) => {
    const { context, page, done } = await loadApp(browser, network, url, { mirror });
    const summary = expectEleven(done);
    console.log(`${name}:\n  ${summary.join('\n  ')}`);
    expectStorageOnRoot(network, { signedIn });
    // The app's Web Worker (src/main.js ends with one): its own bundle, rewritten too.
    if (done.worker) {
      expect(done.worker, JSON.stringify(done.worker)).toMatchObject({ ok: true, host: new URL(e2e.aruma).host });
      expect(done.worker.url.startsWith(`${e2e.aruma}/v0/b/${encodeURIComponent(e2e.bucket)}/o/open%2Fworker-`)).toBe(true);
      expect(network.aruma.some((r) => r.method === 'POST' && r.search.includes('open%2Fworker-'))).toBe(true);
      console.log(`  worker: ${done.worker.host}`);
    }
    // The <img> loaded the unified link itself, from Aruma's root.
    const src = await page.locator('#img').getAttribute('src');
    expect(src.startsWith(`${e2e.aruma}/v0/b/${encodeURIComponent(e2e.bucket)}/o/`)).toBe(true);
    // An app that renders on the server (Next.js): its server's link, in the HTML, is unified too.
    if (await page.locator('#ssr').count()) {
      expect(await page.locator('#ssr-host').textContent()).toBe(e2e.aruma);
      expect((await page.locator('#ssr').getAttribute('src')).startsWith(`${e2e.aruma}/v0/b/`)).toBe(true);
      expect(await page.locator('#ssr').evaluate((img) => img.complete && img.naturalWidth)).toBe(4);
    }
    // Flutter web: firebase_core_web took the app's SDK from gstatic (answered from npm here), but
    // Storage from the rewritten file the tool wrote, never firebase-storage.js from gstatic.
    if (network.mirrored.length) console.log(`  from www.gstatic.com (answered from npm): ${network.mirrored.map((u) => u.split('/').pop()).join(', ')}`);
    expect(network.mirrored.filter((u) => u.endsWith('/firebase-storage.js')), 'gstatic firebase-storage.js').toEqual([]);
    expect(page.errors.filter((e) => !/404|Failed to load resource/.test(e))).toEqual([]);
    await context.close();
  });
}
