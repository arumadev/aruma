import { test, expect, loadApp } from './fixtures.mjs';

// SR3-06: Auth on Aruma, Storage left on Google (src/auth-only.js, bundled with no plugin by
// build.mjs, which also checks that the Vite plugin without storage: true refuses this app). Every
// Storage operation fails, and nothing reaches firebasestorage.googleapis.com: the fixture's network
// guard fails the test on any request off the machine or any URL on that host.
test('auth only: Storage on Google is refused, the ID token stays on the machine', async ({ browser, network }) => {
  const { context, page, done } = await loadApp(browser, network, '/auth-only/');
  expect(done.error, 'the app failed before its operations').toBeUndefined();
  console.log(`auth-only: host=${done.host} upload=${done.upload} getMetadata=${done.metadata}`);
  expect(done.host).toBe('firebasestorage.googleapis.com');
  expect(done.upload).not.toBe('ok');
  expect(done.metadata).not.toBe('ok');
  // Auth went to Aruma, Storage nowhere.
  expect(network.aruma.some((r) => r.path.startsWith('/identitytoolkit.googleapis.com/'))).toBe(true);
  expect(network.aruma.filter((r) => r.path.startsWith('/v0/')), 'Storage requests').toEqual([]);
  // Said once, loudly, in the console.
  expect(page.errors.filter((e) => /Storage request to firebasestorage\.googleapis\.com was blocked/.test(e))).toHaveLength(1);
  await context.close();
});
