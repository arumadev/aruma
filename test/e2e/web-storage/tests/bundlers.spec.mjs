import { test, expect, e2e, loadApp, expectEleven, expectStorageOnRoot } from './fixtures.mjs';

// The app of src/main.js as each bundler built it (build.mjs), from the real npm packages: the
// domain-only switch in the browser, eleven operations with no ⚠, and the <img> on the unified link.
const apps = (process.env.ARUMA_E2E_APPS ?? 'vite,webpack,esbuild').split(',').filter(Boolean);

for (const name of apps) {
  test(`${name}: eleven operations on Aruma, links unified`, async ({ browser, network }) => {
    const { context, page, done } = await loadApp(browser, network, `/${name}/`);
    const summary = expectEleven(done);
    console.log(`${name}:\n  ${summary.join('\n  ')}`);
    expectStorageOnRoot(network);
    // The <img> loaded the unified link itself, from Aruma's root.
    const src = await page.locator('#img').getAttribute('src');
    expect(src.startsWith(`${e2e.aruma}/v0/b/${encodeURIComponent(e2e.bucket)}/o/`)).toBe(true);
    expect(page.errors.filter((e) => !/404|Failed to load resource/.test(e))).toEqual([]);
    await context.close();
  });
}
