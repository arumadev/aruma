import { test, expect, e2e, loadApp, expectEleven, expectStorageOnRoot } from './fixtures.mjs';

// The SvelteKit app (apps/sveltekit), built with the same one line, served by Node under the
// network guard (run.sh): the server's Storage SDK is the SSR bundle's, rewritten, and the browser's
// is the client bundle's.
const ssr = process.env.ARUMA_E2E_SSR_URL;

test.describe('SvelteKit', () => {
  test.skip(!ssr, 'ARUMA_E2E_SSR_URL is not set (run.sh starts the server)');

  test('the server renders the unified link, from Aruma', async ({ request }) => {
    const res = await request.get(`${ssr}/`);
    expect(res.status()).toBe(200);
    const html = await res.text();
    expect(html).toContain(`<p id="ssr-host">${e2e.aruma}</p>`);
    const src = /<img id="ssr" src="([^"]+)"/.exec(html)?.[1]?.replaceAll('&amp;', '&');
    expect(src).toMatch(new RegExp(`^${e2e.aruma}/v0/b/${encodeURIComponent(e2e.bucket).replaceAll('.', '\\.')}/o/public%2Fssr\\.png\\?alt=media&token=[0-9a-f-]{36}$`));
  });

  test('the browser: the server-rendered <img> and the eleven operations', async ({ browser, network }) => {
    const { context, page, done } = await loadApp(browser, network, `${ssr}/`);
    const summary = expectEleven(done);
    console.log(`sveltekit:\n  ${summary.join('\n  ')}`);
    expectStorageOnRoot(network);
    expect(await page.locator('#ssr').evaluate((img) => img.complete && img.naturalWidth)).toBe(4);
    expect(network.aruma.some((r) => r.type === 'image' && r.path.endsWith('/o/public%2Fssr.png'))).toBe(true);
    await context.close();
  });
});
