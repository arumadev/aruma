import { test as base, expect } from '@playwright/test';

export const arumaURL = process.env.ARUMA_URL;

// Every Google host the SDKs could reach: APIs, static files, the auth handler domains,
// reCAPTCHA and analytics.
const google = /(^|\.)(googleapis\.com|gstatic\.com|google\.com|googleusercontent\.com|firebaseio\.com|firebaseapp\.com|web\.app|recaptcha\.net|google-analytics\.com|googletagmanager\.com|doubleclick\.net|withgoogle\.com)$/;
const local = new Set(['127.0.0.1', 'localhost']);

export const test = base.extend({
  // The network guard: requests to this machine (the app and Aruma) go through; anything else is
  // aborted and recorded, and the test fails if there was any. log.aruma holds the requests that
  // reached Aruma.
  network: [async ({ context }, use) => {
    const log = { aruma: [], blocked: [] };
    // Only what leaves this machine is intercepted: a route on Aruma's own requests would buffer
    // the streaming Listen response in the browser, and the SDK would never see it.
    await context.route((url) => !local.has(url.hostname), (route) => {
      log.blocked.push(route.request().url());
      return route.abort('blockedbyclient');
    });
    context.on('request', (request) => {
      const url = new URL(request.url());
      if (url.origin === arumaURL) log.aruma.push({ method: request.method(), path: url.pathname, query: url.search });
    });
    await use(log);
    expect(log.blocked.filter((u) => google.test(new URL(u).hostname)), 'requests to Google').toEqual([]);
    expect(log.blocked, 'requests leaving the machine').toEqual([]);
  }, { auto: true }],

  // The app, loaded with the full SDK's transport of the test (stream or long-polling).
  app: async ({ page }, use) => {
    await use(async (transport = 'stream') => {
      await page.goto(`/?transport=${transport}`);
      await expect(page.locator('#status')).toHaveText('ready');
      expect(await page.evaluate(() => window.e2e.transport)).toBe(transport);
      return page;
    });
  },
});

export { expect };

// call runs fn in the page with window.e2e (the SDKs, the databases, the auth) and arg. A thrown
// Firebase error comes back as { code, message }, anything else as { value }.
export async function call(page, fn, arg) {
  return page.evaluate(async ([src, arg]) => {
    try {
      // eslint-disable-next-line no-new-func
      return { value: await new Function('e2e', 'arg', `return (${src})(e2e, arg)`)(window.e2e, arg) };
    } catch (e) {
      return { code: e.code ?? String(e), message: e.message };
    }
  }, [fn.toString(), arg]);
}

// value unwraps a call that must succeed.
export async function value(page, fn, arg) {
  const r = await call(page, fn, arg);
  expect(r.code, `unexpected error ${r.code}: ${r.message}`).toBeUndefined();
  return r.value;
}

// A path no other test (or run) uses.
export const unique = (name) => `${name}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

export const signIn = (page) => value(page, async ({ auth, authSdk }) => (await authSdk.signInAnonymously(auth)).user.uid);
export const signOut = (page) => value(page, async ({ auth, authSdk }) => authSdk.signOut(auth));

// listen starts onSnapshot on a document ('e2e/x/y') or a collection with an optional query
// ({ where: [field, op, value] }) of the full SDK and records every event under id: a snapshot as
// { data } (a document) or { docs: [[id, data]...], changes: [[type, id]...] }, an error as { error }.
// The listener is kept in e2e.unsubs[id].
export function listen(page, id, path, query) {
  return value(page, ({ fs, db, events, unsubs = (window.e2e.unsubs ??= {}) }, [id, path, query]) => {
    events[id] = [];
    const ref = path.split('/').length % 2 === 0 ? fs.doc(db, path) : fs.collection(db, path);
    const target = query?.where ? fs.query(ref, fs.where(...query.where)) : ref;
    // includeMetadataChanges: a result the cache already holds is first raised from the cache, and
    // the server's confirmation of it changes the metadata only.
    unsubs[id] = fs.onSnapshot(
      target,
      { includeMetadataChanges: true },
      (s) => events[id].push('docs' in s
        ? { docs: s.docs.map((d) => [d.id, d.data()]), changes: s.docChanges().map((c) => [c.type, c.doc.id]), cache: s.metadata.fromCache }
        : { data: s.exists() ? s.data() : null, cache: s.metadata.fromCache }),
      (e) => events[id].push({ error: e.code }),
    );
  }, [id, path, query]);
}

export const unlisten = (page, id) => value(page, ({ unsubs }, id) => unsubs[id](), id);
export const eventsOf = (page, id) => page.evaluate((id) => window.e2e.events[id], id);

// lastServer polls the events of a listener until the newest one that came from the server
// satisfies pred.
export async function expectLast(page, id, pred, what) {
  try {
    await expect.poll(async () => {
      const events = await eventsOf(page, id);
      const last = events.filter((e) => !e.cache || e.error).at(-1);
      return last !== undefined && pred(last);
    }, { message: what, timeout: 20_000 }).toBe(true);
  } catch (e) {
    e.message += `\nevents of ${id}: ${JSON.stringify(await eventsOf(page, id), (k, v) => (typeof v === 'string' && v.length > 40 ? `${v.slice(0, 40)}…(${v.length})` : v)).slice(0, 800)}`;
    throw e;
  }
}
