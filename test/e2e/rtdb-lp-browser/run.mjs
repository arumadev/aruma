// The long-poll trap against Aruma: the official web SDK in
// Chromium, with Aruma serving /.lp. ../rtdb-lp-browser.sh builds and configures Aruma and
// runs this with ARUMA_BIN (the binary, its environment already set), BASE, NS, PROJECT_ID
// and PAGE_PORT; this starts and stops the server itself, prints one JSON report and exits
// non-zero when a check fails.
//
//   S1: a page connected over a WebSocket; the server stops for DOWN_MS (10 s) and comes back.
//       The SDK's WebSocket attempts fail meanwhile, which sets the localStorage flag, so it
//       comes back over long polling. It must reconnect, upgrade to a WebSocket (s=<session>),
//       clear the flag, and receive a write made after the restart.
//   S2: localStorage throws, so the SDK starts with long polling; it must connect, receive a
//       write and upgrade.
//   S3: localStorage throws and WebSockets are refused (a network that blocks them): long
//       polling alone must carry the data both ways.
import http from 'node:http';
import { spawn } from 'node:child_process';
import { build } from 'esbuild';
import { chromium } from 'playwright-core';

const { ARUMA_BIN, BASE, NS, PROJECT_ID, PAGE_PORT = '0', DOWN_MS = '10000' } = process.env;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const T0 = Date.now();
const ts = () => Date.now() - T0;

// --- the server
let server = null;
async function startServer() {
  server = spawn(ARUMA_BIN, ['serve'], { stdio: ['ignore', 'ignore', 'inherit'] });
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(`${BASE}/healthz`)).ok) return;
    } catch {}
    await sleep(100);
  }
  throw new Error('Aruma did not start');
}
async function stopServer() {
  const s = server;
  server = null;
  const exited = new Promise((r) => s.once('exit', r));
  s.kill('SIGTERM');
  await exited;
}

// --- the page
const bundle = (await build({ entryPoints: [new URL('./app.js', import.meta.url).pathname], bundle: true, write: false, format: 'esm', logLevel: 'error' })).outputFiles[0].text;
const pages = http.createServer((req, res) => {
  if (req.url.startsWith('/app.js')) {
    res.writeHead(200, { 'Content-Type': 'text/javascript' });
    res.end(bundle);
    return;
  }
  const config = { databaseURL: `${BASE}?ns=${NS}`, projectId: PROJECT_ID };
  res.writeHead(200, { 'Content-Type': 'text/html' });
  res.end(`<!doctype html><meta charset="utf-8"><title>lp</title><script>window.__CONFIG=${JSON.stringify(config)}</script><script type="module" src="/app.js"></script>`);
});
await new Promise((r) => pages.listen(Number(PAGE_PORT), '127.0.0.1', r));
const pageURL = `http://127.0.0.1:${pages.address().port}/`;

// What a page asked of the server: long-poll requests (with the bodies of their answers) and
// WebSockets, each with the time since the start.
function watch(page) {
  const net = { lp: [], ws: [] };
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (u.pathname !== '/.lp') return;
    const kind = u.searchParams.has('start') ? 'start' : u.searchParams.has('dframe') ? 'dframe' : u.searchParams.has('disconn') ? 'disconn' : u.searchParams.has('seg0') ? 'send' : 'poll';
    net.lp.push({ t: ts(), kind });
  });
  page.on('response', async (r) => {
    if (new URL(r.url()).pathname !== '/.lp') return;
    try {
      const body = await r.text();
      if (body.includes('"p":"x"')) net.lp.push({ t: ts(), kind: 'data', body: body.slice(0, 200) });
    } catch {}
  });
  page.on('websocket', (ws) => net.ws.push({ t: ts(), upgrade: new URL(ws.url()).searchParams.has('s'), url: ws.url().replace(/[?&]ls=[^&]*/, '') }));
  return net;
}
const connected = async (page, ms) => {
  const start = Date.now();
  while (Date.now() - start < ms) {
    if (await page.evaluate(() => window.__connected === true)) return Date.now() - start;
    await sleep(100);
  }
  return null;
};
const seen = async (page, value, ms = 10000) => {
  const start = Date.now();
  while (Date.now() - start < ms) {
    if (await page.evaluate((v) => window.__ev.some((e) => e.k === 'x' && e.v === v), value)) return Date.now() - start;
    await sleep(100);
  }
  return null;
};
const until = async (fn, ms) => {
  const start = Date.now();
  while (Date.now() - start < ms) {
    if (await fn()) return Date.now() - start;
    await sleep(200);
  }
  return null;
};

const report = { pageURL, checks: {}, scenarios: {} };
const check = (name, ok, detail) => {
  report.checks[name] = ok ? 'ok' : `FAILED${detail === undefined ? '' : ': ' + JSON.stringify(detail)}`;
};
setTimeout(() => {
  report.error = 'timed out';
  console.log(JSON.stringify(report, null, 2));
  if (server) server.kill('SIGKILL');
  process.exit(2);
}, 300000).unref();

let browser;
try {
  await startServer();
  browser = await chromium.launch({ channel: 'chromium' });
  // The writer: another client, on WebSockets only.
  const writer = await (await browser.newContext()).newPage();
  await writer.goto(pageURL + '?fws=1');
  check('writer connects', (await connected(writer, 15000)) !== null);
  const write = (v) => writer.evaluate((x) => window.__write(x), v);

  // S1: restart.
  {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();
    const net = watch(page);
    await page.goto(pageURL);
    const r = { firstConnectMs: await connected(page, 15000), flagAtStart: await page.evaluate(() => window.__flag()) };
    r.firstTransport = net.lp.some((x) => x.kind === 'start') ? 'long poll' : net.ws.length ? 'websocket' : 'none';
    await write('before-' + T0);
    r.seenBefore = await seen(page, 'before-' + T0);
    await stopServer();
    await sleep(Number(DOWN_MS));
    r.flagWhileDown = await page.evaluate(() => window.__flag());
    const up = ts();
    await startServer();
    r.reconnectMs = await connected(page, 120000);
    r.lpAfterRestart = net.lp.filter((x) => x.t > up && x.kind === 'start').length;
    r.upgradeAfterRestartMs = await until(async () => net.ws.some((w) => w.t > up && w.upgrade), 30000);
    r.flagClearedMs = await until(async () => (await page.evaluate(() => window.__flag())) === null, 30000);
    await connected(writer, 30000);
    await write('after-' + T0);
    r.seenAfter = await seen(page, 'after-' + T0);
    r.net = { lp: net.lp.filter((x) => x.kind !== 'data').slice(0, 30), ws: net.ws };
    r.sdkLog = (await page.evaluate(() => window.__log)).filter((l) => /long-poll|Longpoll|Websocket connecting|upgrade|Upgrade|healthy|Realtime connection established/i.test(l)).slice(0, 40);
    report.scenarios.S1_restart = r;
    check('S1 first connection over a WebSocket, flag clear', r.firstConnectMs !== null && r.firstTransport === 'websocket' && r.flagAtStart === null, r);
    check('S1 the flag is set while the server is down', r.flagWhileDown === 'true', r.flagWhileDown);
    check('S1 reconnects after the restart, over long polling', r.reconnectMs !== null && r.lpAfterRestart > 0, { reconnectMs: r.reconnectMs, lp: r.lpAfterRestart });
    check('S1 upgrades back to a WebSocket', r.upgradeAfterRestartMs !== null);
    check('S1 the flag is cleared', r.flagClearedMs !== null);
    check('S1 receives writes before and after', r.seenBefore !== null && r.seenAfter !== null);
    await ctx.close();
  }

  // S2 and S3: localStorage blocked; in S3 WebSockets are refused too.
  for (const [name, blockWS] of [['S2_storage_blocked', false], ['S3_storage_and_websockets_blocked', true]]) {
    const ctx = await browser.newContext();
    await ctx.addInitScript(() => {
      Storage.prototype.setItem = () => {
        throw new Error('blocked');
      };
      Storage.prototype.getItem = () => {
        throw new Error('blocked');
      };
    });
    if (blockWS) await ctx.routeWebSocket(/\/\.ws/, (ws) => ws.close());
    const page = await ctx.newPage();
    const net = watch(page);
    await page.goto(pageURL);
    const r = { connectMs: await connected(page, 30000) };
    r.firstRequest = net.lp[0] ? `lp ${net.lp[0].kind}` : net.ws[0] ? 'websocket' : 'none';
    const v = `${name}-${T0}`;
    await write(v);
    r.seenMs = await seen(page, v);
    // The page may see the value before the response event hands this script the body.
    r.carriedByLongPoll = (await until(async () => net.lp.some((x) => x.kind === 'data' && x.body.includes(v)), 3000)) !== null;
    r.upgraded = net.ws.some((w) => w.upgrade);
    if (blockWS) {
      // The page's own write, over long polling, reaches the writer.
      const own = `${v}-own`;
      r.ownWrite = await page.evaluate((x) => window.__write(x), own);
      r.ownWriteSeenByWriterMs = await seen(writer, own);
      r.lpSends = net.lp.filter((x) => x.kind === 'send').length;
    }
    r.net = { lp: net.lp.filter((x) => x.kind !== 'data').slice(0, 12), ws: net.ws.slice(0, 5) };
    report.scenarios[name] = r;
    check(`${name} connects over long polling`, r.connectMs !== null && r.firstRequest === 'lp start', r);
    check(`${name} receives a write`, r.seenMs !== null);
    if (blockWS) {
      check(`${name} long polling carries the data both ways`, r.carriedByLongPoll && r.ownWriteSeenByWriterMs !== null && r.lpSends > 0, r);
    } else {
      check(`${name} upgrades to a WebSocket`, r.upgraded);
    }
    await ctx.close();
  }
} catch (e) {
  report.error = String((e && e.stack) || e);
} finally {
  if (browser) await browser.close();
  if (server) await stopServer();
  pages.close();
}
console.log(JSON.stringify(report, null, 2));
const failed = report.error || Object.values(report.checks).some((c) => c !== 'ok');
process.exit(failed ? 1 : 0);
