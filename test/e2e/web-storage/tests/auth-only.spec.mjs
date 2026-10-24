import { test, expect, loadApp } from './fixtures.mjs';

// SR3-06: Auth on Aruma, Storage left on Google (src/auth-only.js, bundled with no plugin by
// build.mjs, which also checks that the Vite plugin without storage: true refuses this app). Every
// Storage operation fails, and nothing reaches firebasestorage.googleapis.com: the fixture's network
// guard fails the test on any request off the machine or any URL on that host.
test('auth only: Storage on Google is refused, the ID token stays on the machine', async ({ browser, network }) => {
  const { context, page, done } = await loadApp(browser, network, '/auth-only/');
  expect(done.error, 'the app failed before its operations').toBeUndefined();
  console.log(`auth-only: host=${done.host} in ${done.ms} ms\n  upload=${done.upload}\n  getMetadata=${done.metadata}`);
  expect(done.host).toBe('firebasestorage.googleapis.com');
  // At once and not retried, with the SDK's default budget: storage/unauthorized, and the reason.
  for (const r of [done.upload, done.metadata]) expect(r).toMatch(/^storage\/unauthorized \(.*Firebase Auth is redirected to Aruma but Storage is not/);
  expect(done.ms).toBeLessThan(5000);
  // Auth went to Aruma, Storage nowhere.
  expect(network.aruma.some((r) => r.path.startsWith('/identitytoolkit.googleapis.com/'))).toBe(true);
  expect(network.aruma.filter((r) => r.path.startsWith('/v0/')), 'Storage requests').toEqual([]);
  // Said once, loudly, in the console.
  expect(page.errors.filter((e) => /Storage request to firebasestorage\.googleapis\.com was blocked/.test(e))).toHaveLength(1);
  await context.close();
});
