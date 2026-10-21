import { test as base, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';

const dir = process.env.ARUMA_E2E_DIR ?? new URL('../.generated/', import.meta.url).pathname;
export const arumaURL = process.env.ARUMA_URL;
export const switchURL = process.env.ARUMA_SWITCH_URL;
export const config = JSON.parse(readFileSync(`${dir}/firebase-config.json`, 'utf8'));
export const expected = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8'));
const cliToken = process.env.ARUMA_CLI_TOKEN;

// The database is the one the generated config names: Aruma's development form (ADR-018),
// http://127.0.0.1:<port>/?ns=<project>-default-rtdb, on the same server as Auth.
export const ns = new URL(config.databaseURL).searchParams.get('ns');

// Every Google host the SDKs could reach: APIs, static files, the auth handler domains, reCAPTCHA,
// analytics, and the Realtime Database's own (*.firebaseio.com, *.firebasedatabase.app).
const google = /(^|\.)(googleapis\.com|gstatic\.com|google\.com|googleusercontent\.com|firebaseio\.com|firebasedatabase\.app|firebaseapp\.com|web\.app|recaptcha\.net|google-analytics\.com|googletagmanager\.com|doubleclick\.net|withgoogle\.com)$/;
export const firebaseDatabaseHost = /(^|\.)(firebaseio\.com|firebasedatabase\.app)$/;
const local = new Set(['127.0.0.1', 'localhost']);
const pageOrigin = `http://127.0.0.1:${process.env.ARUMA_E2E_PAGE_PORT ?? 4181}`;

// guard puts the network guard on a browser context. HTTP requests to this machine (the Vite app,
// Aruma, long polling) go through; anything else is aborted and recorded. WebSockets do not pass
// through route(): every page reports its own, and the browser resolves no host name but
// localhost (--host-resolver-rules, playwright.config.mjs), so one to a Google host cannot open.
async function guard(context, log) {
  await context.route('**/*', (route) => {
    const url = new URL(route.request().url());
    log.urls.push(url.href);
    if (local.has(url.hostname)) {
      if (url.origin === arumaURL) log.aruma.push(url.pathname + url.search);
      return route.continue();
    }
    log.blocked.push(url.href);
    return route.abort('blockedbyclient');
  });
  const watch = (page) => page.on('websocket', (ws) => {
    const url = new URL(ws.url());
    log.urls.push(url.href);
    log.sockets.push(url.href);
    if (!local.has(url.hostname)) log.blocked.push(url.href);
  });
  context.pages().forEach(watch);
  context.on('page', watch);
}

export const test = base.extend({
  firebaseMajor: ['13', { option: true }],

  // The network log every client of a test shares; the test fails on a request that left the
  // machine, and on any URL of a Firebase database host, blocked or not.
  network: [async ({}, use) => {
    const log = { aruma: [], sockets: [], blocked: [], urls: [] };
    await use(log);
    expect(log.blocked.filter((u) => google.test(new URL(u).hostname)), 'requests to Google').toEqual([]);
    expect(log.urls.filter((u) => firebaseDatabaseHost.test(new URL(u).hostname)), 'requests to Firebase database hosts').toEqual([]);
    expect(log.blocked, 'requests leaving the machine').toEqual([]);
    // Every WebSocket but the Vite dev server's own went to the databaseURL's host, for its
    // namespace.
    const db = new URL(config.databaseURL);
    for (const s of log.sockets.filter((u) => new URL(u).origin.replace(/^ws/, 'http') !== pageOrigin)) {
      const u = new URL(s);
      expect(`${u.host} ${u.pathname} ${u.searchParams.get('ns')}`).toBe(`${db.host} /.ws ${ns}`);
    }
  }, { auto: true }],

  // client opens one more client: a browser context of its own (its own Auth persistence and
  // database connection) with the app loaded, firebase major version of the project. setup runs
  // on the context before the page loads (e.g. refusing WebSockets).
  client: async ({ browser, firebaseMajor, network }, use) => {
    const contexts = [];
    await use(async ({ setup } = {}) => {
      const context = await browser.newContext();
      contexts.push(context);
      await guard(context, network);
      if (setup) await setup(context);
      const page = await context.newPage();
      page.on('pageerror', (e) => console.log(`page error: ${e}`));
      // The SDKs' warnings (console.warn), for the tests to check.
      page.warnings = [];
      page.on('console', (m) => m.type() === 'warning' && page.warnings.push(m.text()));
      await page.goto(`/?v=${firebaseMajor}`);
      await expect(page.locator('#status')).toHaveText(new RegExp(`^ready ${firebaseMajor}\\.`));
      return page;
    });
    for (const c of contexts) await c.close().catch(() => {});
  },
});

export { expect };

// call runs fn(e2e, arg) in the page, e2e being the app's window.e2e (SDKs, auth, db, listen).
// A thrown error comes back as { code }, the Firebase code when there is one; a value as { value }.
export async function call(page, fn, arg) {
  return page.evaluate(async ([src, arg]) => {
    try {
      // eslint-disable-next-line no-new-func
      return { value: await new Function('e2e', 'arg', `return (${src})(e2e, arg)`)(window.e2e, arg) };
    } catch (e) {
      return { code: e.code ?? String(e) };
    }
  }, [fn.toString(), arg]);
}

// value unwraps a call that must succeed.
export async function value(page, fn, arg) {
  const r = await call(page, fn, arg);
  expect(r.code, `unexpected error ${r.code}`).toBeUndefined();
  return r.value;
}

// events returns what listen recorded under name.
export const events = (page, name) => page.evaluate((n) => window.e2e.events[n] ?? [], name);

// signIn signs the page's user in: anonymously, or with a new email and password. It returns the uid.
export function signIn(page, email) {
  if (!email) return value(page, (e) => e.authSdk.signInAnonymously(e.auth).then((c) => c.user.uid));
  return value(page, (e, a) => e.authSdk.createUserWithEmailAndPassword(e.auth, a, 'secret1').then((c) => c.user.uid), email);
}

// connected waits until the page's database connection is up (.info/connected), or down.
export async function connected(page, state = true, timeout = 30_000) {
  await value(page, (e) => {
    if (!e.events.connected) e.listen('connected', '.info/connected');
  });
  await expect.poll(async () => (await events(page, 'connected')).at(-1)?.val, { timeout }).toBe(state);
}

// admin is the second party outside the browser: the database's REST API with an admin
// credential (the CLI token, which bypasses the rules as a service account does). It returns
// { status, body, etag }.
export async function admin(method, path, body, headers = {}) {
  const res = await fetch(`${arumaURL}/${path.replace(/^\//, '')}.json?ns=${ns}`, {
    method,
    headers: { authorization: `Bearer ${cliToken}`, 'content-type': 'application/json', ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  return { status: res.status, body: text ? JSON.parse(text) : null, etag: res.headers.get('etag') };
}

// adminIncrement adds one to a number with a conditional REST write (ETag and if-match), as
// firebase-admin's transaction() does over REST, retrying when another writer came first. It
// returns how many attempts it took.
export async function adminIncrement(path) {
  let cur = await admin('GET', path, undefined, { 'x-firebase-etag': 'true' });
  for (let attempt = 1; attempt <= 100; attempt++) {
    const next = await admin('PUT', path, (cur.body ?? 0) + 1, { 'if-match': cur.etag, 'x-firebase-etag': 'true' });
    if (next.status === 200) return attempt;
    expect(next.status, JSON.stringify(next.body)).toBe(412);
    cur = next; // 412 carries the current value and its ETag
  }
  throw new Error(`increment of ${path}: no luck in 100 attempts`);
}

// disableUser disables a user as an admin does (firebase-admin's updateUser(uid, { disabled:
// true })): Identity Toolkit's accounts:update with an admin credential.
export async function disableUser(uid) {
  const res = await fetch(`${arumaURL}/identitytoolkit.googleapis.com/v1/projects/${expected.projectId}/accounts:update`, {
    method: 'POST',
    headers: { authorization: `Bearer ${cliToken}`, 'content-type': 'application/json' },
    body: JSON.stringify({ localId: uid, disableUser: true }),
  });
  expect(res.status, await res.text()).toBe(200);
}

// server takes the whole server down (every connection closed, new ones refused) or up again on
// the same address, with genconfig's switch: the database and its state stay.
export async function server(state) {
  const res = await fetch(`${switchURL}/${state}`, { method: 'POST' });
  expect(res.status, await res.text()).toBe(200);
}
