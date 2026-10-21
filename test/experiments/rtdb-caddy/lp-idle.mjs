// Long polling through the proxy, in the official web SDK in Chromium: a page whose
// localStorage throws and whose WebSockets are refused (a network that blocks them) can only
// long-poll. It must connect, stay connected through an idle period longer than the 60 s the
// operator guide asks every hop to allow, and carry data both ways afterwards. A second page,
// on WebSockets only, is the other side.
//
//   node lp-idle.mjs <databaseURL> <idle seconds>
//
// It reuses ../rtdb-lp-browser (its app.js and node_modules: run `npm ci` there) and
// Playwright's Chromium. Chromium resolves only *.<the database domain's parent>, 127.0.0.1
// and localhost, and accepts the proxy's certificates only when their chain has a key in
// RTDB_CA_SPKI (comma-separated base64 SHA-256 hashes of the CA keys: Caddy's local root and
// intermediate), so nothing is trusted beyond this run's CA and the browser profile is a
// throwaway one. RTDB_PAGE_PORT is where the page is served (default: any free port). The
// database's rules must let anyone read and write /x. It prints one JSON report and exits 1
// when a check fails.
import http from 'node:http';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const lpDir = fileURLToPath(new URL('../rtdb-lp-browser/', import.meta.url));
const require = createRequire(lpDir + 'package.json');
const { build } = require('esbuild');
const { chromium } = require('playwright-core');

const [databaseURL, idleArg] = process.argv.slice(2);
const idleMs = Number(idleArg) * 1000;
const spki = process.env.RTDB_CA_SPKI || '';
if (!databaseURL || !(idleMs > 0) || !spki) {
  console.error('usage: RTDB_CA_SPKI=<hash>[,<hash>] node lp-idle.mjs <databaseURL> <idle seconds>');
  process.exit(2);
}
const u = new URL(databaseURL);
const parent = u.hostname.split('.').slice(2).join('.'); // 127.0.0.1.nip.io for <ns>.rtdb.127.0.0.1.nip.io
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const T0 = Date.now();
const ts = () => Date.now() - T0;

const bundle = (await build({ entryPoints: [lpDir + 'app.js'], bundle: true, write: false, format: 'esm', logLevel: 'error' })).outputFiles[0].text;
const pages = http.createServer((req, res) => {
  if (req.url.startsWith('/app.js')) {
    res.writeHead(200, { 'Content-Type': 'text/javascript' });
    res.end(bundle);
    return;
  }
  const config = { databaseURL, projectId: u.hostname.split('.')[0].replace(/-default-rtdb$/, '') };
  res.writeHead(200, { 'Content-Type': 'text/html' });
  res.end(`<!doctype html><meta charset="utf-8"><title>lp</title><script>window.__CONFIG=${JSON.stringify(config)}</script><script type="module" src="/app.js"></script>`);
});
await new Promise((r) => pages.listen(Number(process.env.RTDB_PAGE_PORT || 0), '127.0.0.1', r));
const pageURL = `http://127.0.0.1:${pages.address().port}/`;

const report = { databaseURL, idleSeconds: idleMs / 1000, checks: {} };
const check = (name, ok, detail) => {
  report.checks[name] = ok ? 'ok' : `FAILED${detail === undefined ? '' : ': ' + JSON.stringify(detail)}`;
};
const until = async (fn, ms) => {
  const start = Date.now();
  while (Date.now() - start < ms) {
    if (await fn()) return Date.now() - start;
    await sleep(200);
  }
  return null;
};
const connected = (page, ms) => until(() => page.evaluate(() => window.__connected === true), ms);
const seen = (page, v, ms = 15000) => until(() => page.evaluate((x) => window.__ev.some((e) => e.k === 'x' && e.v === x), v), ms);

// Every request of a page to the database host: long polls by kind, and WebSockets.
function watch(page) {
  const net = { lp: [], ws: [], failed: [] };
  page.on('request', (r) => {
    const q = new URL(r.url());
    if (q.pathname !== '/.lp') return;
    const kind = q.searchParams.has('start') ? 'start' : q.searchParams.has('seg0') ? 'send' : q.searchParams.has('disconn') ? 'disconn' : 'poll';
    net.lp.push({ t: ts(), kind });
  });
  page.on('requestfailed', (r) => net.failed.push({ t: ts(), url: r.url().slice(0, 120), error: r.failure()?.errorText }));
  page.on('websocket', (ws) => net.ws.push({ t: ts(), url: ws.url().replace(/[?&]ls=[^&]*/, '') }));
  return net;
}

setTimeout(() => {
  report.error = 'timed out';
  console.log(JSON.stringify(report, null, 2));
  process.exit(2);
}, idleMs + 180000).unref();

let browser;
try {
  browser = await chromium.launch({
    channel: 'chromium',
    args: [
      `--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE *.${parent}, EXCLUDE 127.0.0.1, EXCLUDE localhost`,
      `--ignore-certificate-errors-spki-list=${spki}`,
    ],
  });
  const writer = await (await browser.newContext()).newPage();
  await writer.goto(pageURL + '?fws=1');
  check('the WebSocket page connects through the proxy', (await connected(writer, 30000)) !== null);
  const write = (page, v) => page.evaluate((x) => window.__write(x), v);

  const ctx = await browser.newContext();
  await ctx.addInitScript(() => {
    Storage.prototype.setItem = () => {
      throw new Error('blocked');
    };
    Storage.prototype.getItem = () => {
      throw new Error('blocked');
    };
  });
  await ctx.routeWebSocket(/\/\.ws/, (ws) => ws.close());
  const page = await ctx.newPage();
  const net = watch(page);
  await page.goto(pageURL);
  report.connectMs = await connected(page, 30000);
  check('the long-poll page connects through the proxy', report.connectMs !== null && net.lp[0]?.kind === 'start', { connectMs: report.connectMs, first: net.lp[0] });
  report.before = await write(writer, `before-${T0}`);
  check('it receives a write before the idle period', (await seen(page, `before-${T0}`)) !== null, report.before);

  const idleStart = ts();
  const pollsBefore = net.lp.filter((x) => x.kind === 'poll').length;
  await sleep(idleMs);
  // .info/connected starts false; what counts is a drop after the first connection.
  const events = await page.evaluate(() => window.__ev.filter((e) => e.k === 'connected'));
  const drops = events.slice(events.findIndex((e) => e.v === true)).filter((e) => e.v === false);
  report.idle = {
    from: idleStart,
    to: ts(),
    polls: net.lp.filter((x) => x.kind === 'poll').length - pollsBefore,
    starts: net.lp.filter((x) => x.kind === 'start').length,
    connectedEvents: events,
    failedRequests: net.failed,
  };
  check(`the long-poll session survives ${idleMs / 1000} s without data`,
    report.idle.starts === 1 && report.idle.polls >= 2 && drops.length === 0 && (await page.evaluate(() => window.__connected)) === true,
    report.idle);

  report.after = await write(writer, `after-${T0}`);
  check('a write after the idle period arrives over long polling', (await seen(page, `after-${T0}`)) !== null, report.after);
  report.own = await write(page, `own-${T0}`);
  check('the long-poll page writes, and the WebSocket page sees it', report.own === 'ok' && (await seen(writer, `own-${T0}`)) !== null, report.own);
  report.longPolls = { start: net.lp.filter((x) => x.kind === 'start').length, poll: net.lp.filter((x) => x.kind === 'poll').length, send: net.lp.filter((x) => x.kind === 'send').length };
  check('its writes went out as long-poll sends', report.longPolls.send > 0, report.longPolls);
  await ctx.close();
} catch (e) {
  report.error = String((e && e.stack) || e);
} finally {
  if (browser) await browser.close();
  pages.close();
}
console.log(JSON.stringify(report, null, 2));
process.exit(report.error || Object.values(report.checks).some((c) => c !== 'ok') ? 1 : 0);
