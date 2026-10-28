// Drives app.js in headless Chromium: popup and redirect sign-in with Google and Apple, link and
// re-authentication. The Go test TestAuthIdPBrowser starts Aruma and the fake providers and
// passes ARUMA_URL, API_KEY and PROJECT_ID; this prints one JSON report and exits non-zero on a
// failed step. APP_HOST picks the app's host: 127.0.0.1 (same site as Aruma) or localhost.
// Playwright turns third-party storage partitioning off; PARTITIONED=1 turns it back on, as in
// Chrome (a later --disable-features replaces Playwright's list).
import http from 'node:http';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
import { chromium } from 'playwright-core';

const { ARUMA_URL, API_KEY, PROJECT_ID, APP_HOST = '127.0.0.1', PARTITIONED, RUN = 'run' } = process.env;
const bundle = (await build({ entryPoints: [new URL('./app.js', import.meta.url).pathname], bundle: true, write: false, format: 'esm', logLevel: 'error' })).outputFiles[0].text;

const server = http.createServer((req, res) => {
  if (req.url.startsWith('/app.js')) {
    res.writeHead(200, { 'Content-Type': 'text/javascript' });
    res.end(bundle);
    return;
  }
  const config = { apiKey: API_KEY, projectId: PROJECT_ID, authDomain: new URL(ARUMA_URL).host, arumaURL: ARUMA_URL, run: RUN };
  res.writeHead(200, { 'Content-Type': 'text/html' });
  res.end(`<!doctype html><meta charset="utf-8"><title>app</title><script>window.__CONFIG=${JSON.stringify(config)}</script><script type="module" src="/app.js"></script>`);
});
await new Promise((r) => server.listen(0, APP_HOST, r));
const appURL = `http://${APP_HOST}:${server.address().port}/`;

// The full Chromium build in its new headless mode, which runs Chrome's own code. Playwright's
// default headless shell stalls a cross-site iframe created just before a popup opens: the
// SDK's relay iframe then never runs until the next popup.
const browser = await chromium.launch({ channel: 'chromium', ...(PARTITIONED ? { args: ['--disable-features=Translate'] } : {}) });
const context = await browser.newContext();
const external = new Set();
const cspViolations = [];
context.on('request', (req) => {
  const u = new URL(req.url());
  if (!['127.0.0.1', 'localhost'].includes(u.hostname) && u.protocol.startsWith('http')) external.add(u.origin + u.pathname.replace(/\/[^/]*$/, '/…'));
});
context.on('console', (msg) => { if (/Content Security Policy/i.test(msg.text())) cspViolations.push(msg.text()); });
const log = [];
context.on('page', (p) => {
  log.push(`page opened ${p.url()}`);
  p.on('framenavigated', (f) => log.push(`navigated ${f.url().slice(0, 120)}`));
  p.on('pageerror', (e) => log.push(`pageerror ${String(e).slice(0, 600)}`));
  p.on('console', (msg) => { log.push(`console ${msg.type()} ${msg.text().slice(0, 1200)}`); if (/Content Security Policy/i.test(msg.text())) cspViolations.push(msg.text()); });
  p.on('close', () => log.push('page closed'));
});

const page = await context.newPage();
page.on('console', (msg) => { if (/Content Security Policy/i.test(msg.text())) cspViolations.push(msg.text()); });
const report = { appURL, partitioned: !!PARTITIONED, steps: {} };
let failed = false;

// A step that hangs must not hang the run: report what happened and fail.
setTimeout(() => {
  report.log = log;
  report.externalRequests = [...external].sort();
  console.log(JSON.stringify(report, null, 2));
  process.exit(2);
}, 180000).unref();

async function step(name, fn) {
  console.error(`step ${name}`);
  try {
    report.steps[name] = await fn();
  } catch (e) {
    failed = true;
    report.steps[name] = { failed: String(e && e.message || e) };
  }
}
const result = async (name) => {
  await page.waitForFunction((n) => window.__results && window.__results[n], name, { timeout: 30000 });
  return page.evaluate((n) => window.__results[n], name);
};
const ready = () => page.waitForFunction(() => window.__ready, null, { timeout: 30000 });

await page.goto(appURL);
await ready();

await step('googlePopup', async () => {
  await page.evaluate(() => { window.__steps.googlePopup(); });
  const r = await result('googlePopup');
  assert.ok(r.ok, JSON.stringify(r));
  assert.deepEqual(r.ok.providers, ['google.com']);
  assert.equal(r.ok.email, `popup-${RUN}@gmail.com`);
  assert.equal(r.ok.oauthIdToken, true);
  return r;
});
await page.evaluate(() => window.__steps.signOut());
await step('applePopup', async () => {
  await page.evaluate(() => { window.__steps.applePopup(); });
  const r = await result('applePopup');
  assert.ok(r.ok, JSON.stringify(r));
  assert.deepEqual(r.ok.providers, ['apple.com']);
  assert.equal(r.ok.displayName, 'Layla Hassan');
  return r;
});
await page.evaluate(() => window.__steps.signOut());
await step('googleRedirect', async () => {
  await page.evaluate(() => { window.__steps.googleRedirect(); });
  await page.waitForURL(appURL, { timeout: 30000 });
  await ready();
  const r = await result('redirectResult');
  if (PARTITIONED && APP_HOST !== '127.0.0.1') {
    // The known limit: the relay iframe cannot read what the handler stored on another site.
    assert.equal(r.error, 'none', JSON.stringify(r));
    return { expected: 'no redirect result across sites with partitioned storage' };
  }
  assert.ok(r.ok, JSON.stringify(r));
  assert.equal(r.ok.email, `redirect-${RUN}@gmail.com`);
  assert.equal(r.ok.operationType, 'signIn');
  return r;
});
await page.evaluate(() => window.__steps.signOut());
await step('anonymousThenLink', async () => {
  await page.evaluate(() => { window.__steps.anonymousThenLink(); });
  const r = await result('anonymousThenLink');
  const anonymousUid = await page.evaluate(() => window.__results.anonymousUid);
  assert.ok(r.ok, JSON.stringify(r));
  assert.equal(r.ok.uid, anonymousUid);
  assert.equal(r.ok.operationType, 'link');
  assert.equal(r.ok.isAnonymous, false);
  return r;
});
await step('reauth', async () => {
  await page.evaluate(() => { window.__steps.reauth(); });
  const r = await result('reauth');
  assert.ok(r.ok, JSON.stringify(r));
  assert.equal(r.ok.operationType, 'reauthenticate');
  return r;
});

report.externalRequests = [...external].sort();
report.cspViolations = cspViolations;
if (failed) report.log = log;
if (cspViolations.length) failed = true;
console.log(JSON.stringify(report, null, 2));
await browser.close();
server.close();
process.exit(failed ? 1 : 0);
