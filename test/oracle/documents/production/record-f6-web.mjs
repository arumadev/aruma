// F6 production recordings of the browser transports of Cloud Firestore: what
// chatapp-8ae21 does to the real Firebase JS SDK (the version test/smoke/web
// uses, firebase-13) driven in headless Chromium by Playwright, seen at the HTTP
// level through the Chrome DevTools Protocol (CDP):
//
//   f6-webchannel-listen.json     full SDK, default settings: Listen on a document, a change, a
//                                 query target added and removed, an idle period (keep-alive), terminate
//   f6-webchannel-longpoll.json   the same with experimentalForceLongPolling
//   f6-webchannel-write.json      the Write channel: handshake, writes, stream token, terminate
//   f6-webchannel-errors.json     rules denial on Listen and Write, an invalid API key, no auth where
//                                 rules need it, an unknown SID, a bad Authorization header
//   f6-cors.json                  OPTIONS preflights and real response headers, from a foreign origin
//   f6-lite.json                  firebase/firestore/lite: a get and a write (fetch)
//
// Every request to firestore.googleapis.com is kept: method, full URL, request
// headers, body, response status, response headers and the response body bytes,
// with the chunk boundaries and arrival times of streaming responses. Access
// tokens, the API key, SID and gsessionid are replaced by placeholders of the
// same length (so the frame length prefixes stay exact); timestamps by N-masks of
// the same length; the first RID of a session by <rid0> and later ones by <rid0+k>.
//
// What it touches in the project: the cloud.firestore rules release (the original
// ruleset is restored at the end), one Auth test user (deleted at the end), and
// documents under aruma-ref-f6-* (deleted at the end).
//
//   node record-f6-web.mjs [--only=listen,longpoll,write,errors,cors,lite]
//
// Environment: F6_WEBCONFIG_FILE (JSON of the web app config, with apiKey),
// F6_ORIGINAL_RULES_FILE (the rules source released before the run),
// F6_ORIGINAL_RULESET (its ruleset name), F6_SDK_DIR (default test/smoke/web/node_modules/firebase-13),
// PLAYWRIGHT_DIR (default dashboard/node_modules/playwright), NODE_OPTIONS=--dns-result-order=ipv4first.
import { createRequire } from 'node:module';
import { createServer } from 'node:http';
import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { PROJECT, DB, ROOT, REPO_ROOT, rest, versions, writeCorpus, accessToken, assertScoped } from './lib.mjs';

const GENERATOR = 'test/oracle/documents/production/record-f6-web.mjs';
const P = 'aruma-ref-f6';
const COL = { open: `${P}-open`, auth: `${P}-auth`, deny: `${P}-deny` };
const log = (s) => process.stderr.write(s);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const only = (process.argv.find((a) => a.startsWith('--only=')) || '').slice(7).split(',').filter(Boolean);
const wanted = (t) => !only.length || only.includes(t);
const CONFIG = JSON.parse(readFileSync(process.env.F6_WEBCONFIG_FILE, 'utf8'));
const API_KEY = CONFIG.apiKey;
const ORIGINAL_RULES = readFileSync(process.env.F6_ORIGINAL_RULES_FILE, 'utf8');
const ORIGINAL_RULESET = process.env.F6_ORIGINAL_RULESET;
const SDK_DIR = process.env.F6_SDK_DIR || join(REPO_ROOT, 'test/smoke/web/node_modules/firebase-13');
const PW_DIR = process.env.PLAYWRIGHT_DIR || join(REPO_ROOT, 'dashboard/node_modules/playwright');
const SDK_VERSION = JSON.parse(readFileSync(join(SDK_DIR, 'package.json'), 'utf8')).version;
const PORT = 18765;
const ORIGIN_PAGE = `http://localhost:${PORT}`;
const RULES_API = 'https://firebaserules.googleapis.com/v1';
const TOOLKIT = 'https://identitytoolkit.googleapis.com/v1';
const FS_HOST = 'firestore.googleapis.com';
const GSTATIC = `https://www.gstatic.com/firebasejs/${SDK_VERSION}/`;
const FAKE_KEY = 'AIzaSyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';
const NONASCII = 'héllo ✓ 日本語 😀';

// ---- redaction ----

const rx = {
  jwt: /eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]*/g,
  ya29: /ya29\.[A-Za-z0-9._-]+/g,
  ts: /\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z/g,
};
const mask = (s) => s.replace(/\d/g, 'N');
let secrets = []; // {value, label}: exact strings replaced everywhere, longest first
function addSecret(value, label) {
  if (value && value.length > 8) secrets.push({ value, label });
  secrets.sort((a, b) => b.value.length - a.value.length);
}
function scrub(s, sess) {
  if (typeof s !== 'string') return s;
  for (const { value, label } of secrets) s = s.split(value).join(label.length === 1 ? label.repeat(value.length) : label);
  if (sess) for (const [value, fill] of sess.ids) s = s.split(value).join(fill.repeat(value.length));
  s = s.replace(rx.jwt, '<id-token>').replace(rx.ya29, '<access-token>');
  s = s.replace(rx.ts, (m) => mask(m));
  return s;
}

// session ids found in a capture: SID and gsessionid values become fill characters of equal length
function learnIds(entries) {
  const ids = new Map();
  const put = (v, f) => v && v.length >= 4 && !ids.has(v) && ids.set(v, f);
  for (const e of entries) {
    try {
      const u = new URL(e.request.url);
      for (const [k, f] of [['SID', 'S'], ['gsessionid', 'G']]) put(u.searchParams.get(k), f);
    } catch {}
  }
  return { ids };
}

// ---- framing ----

// parseFrames reads "<len>\n<payload>" records; len counts UTF-16 code units or
// bytes. ok means the records tile the body exactly.
function parseUnits(str) {
  const frames = [];
  let i = 0;
  while (i < str.length) {
    const nl = str.indexOf('\n', i);
    if (nl < 0 || !/^\d+$/.test(str.slice(i, nl))) return { ok: false, frames };
    const len = Number(str.slice(i, nl));
    const payload = str.slice(nl + 1, nl + 1 + len);
    if (payload.length !== len) return { ok: false, frames };
    frames.push({ len, payload });
    i = nl + 1 + len;
  }
  return { ok: true, frames };
}
function parseBytes(buf) {
  const frames = [];
  let i = 0;
  while (i < buf.length) {
    const nl = buf.indexOf(0x0a, i);
    if (nl < 0 || !/^\d+$/.test(buf.subarray(i, nl).toString('latin1'))) return { ok: false, frames };
    const len = Number(buf.subarray(i, nl).toString('latin1'));
    if (nl + 1 + len > buf.length) return { ok: false, frames };
    frames.push({ len, payload: buf.subarray(nl + 1, nl + 1 + len).toString('utf8') });
    i = nl + 1 + len;
  }
  return { ok: true, frames };
}

// ---- CDP capture ----

const require = createRequire(import.meta.url);
const { chromium } = require(PW_DIR);

class Capture {
  constructor(cdp, label) {
    this.cdp = cdp;
    this.label = label;
    this.by = new Map();
    this.order = [];
    this.t0 = null;
  }
  async start() {
    const c = this.cdp;
    await c.send('Network.enable', { maxPostDataSize: 65536 });
    c.on('Network.requestWillBeSent', (e) => {
      let host;
      try {
        host = new URL(e.request.url).host;
      } catch {}
      if (host !== FS_HOST) return;
      const ent = { id: e.requestId, type: e.type, method: e.request.method, url: e.request.url, reqHeaders: e.request.headers, postData: e.request.postData ?? null, ts: e.timestamp, wall: e.wallTime, chunks: [], resHeaders: null, status: null, protocol: null, done: null, failed: null, rawReq: null, rawRes: null, initiator: e.initiator?.type };
      this.by.set(e.requestId, ent);
      this.order.push(ent);
      if (this.t0 === null) this.t0 = e.timestamp;
      c.send('Network.streamResourceContent', { requestId: e.requestId }).then((r) => {
        if (r.bufferedData) ent.chunks.push({ ts: e.timestamp, b64: r.bufferedData, buffered: true });
        ent.streamed = true;
      }).catch(() => {});
    });
    c.on('Network.requestWillBeSentExtraInfo', (e) => {
      const ent = this.by.get(e.requestId);
      if (ent) ent.rawReq = e.headers;
    });
    c.on('Network.responseReceived', (e) => {
      const ent = this.by.get(e.requestId);
      if (!ent) return;
      ent.status = e.response.status;
      ent.resHeaders = e.response.headers;
      ent.protocol = e.response.protocol;
      ent.tResp = e.timestamp;
    });
    c.on('Network.responseReceivedExtraInfo', (e) => {
      const ent = this.by.get(e.requestId);
      if (ent) {
        ent.rawRes = e.headers;
        ent.rawStatus = e.statusCode;
      }
    });
    c.on('Network.dataReceived', (e) => {
      const ent = this.by.get(e.requestId);
      if (ent) ent.chunks.push({ ts: e.timestamp, len: e.dataLength, enc: e.encodedDataLength, b64: e.data ?? null });
    });
    c.on('Network.loadingFinished', (e) => {
      const ent = this.by.get(e.requestId);
      if (ent) ent.done = e.timestamp;
    });
    c.on('Network.loadingFailed', (e) => {
      const ent = this.by.get(e.requestId);
      if (ent) {
        ent.done = e.timestamp;
        ent.failed = { errorText: e.errorText, canceled: e.canceled, blockedReason: e.blockedReason, corsErrorStatus: e.corsErrorStatus };
      }
    });
  }
  async finish() {
    for (const ent of this.order) {
      if (!ent.chunks.some((c) => c.b64) && ent.done && !ent.failed && ent.status !== null) {
        try {
          const r = await this.cdp.send('Network.getResponseBody', { requestId: ent.id });
          ent.chunks = [{ ts: ent.done, b64: r.base64Encoded ? r.body : Buffer.from(r.body).toString('base64'), whole: true }];
        } catch {}
      }
    }
    return this.order;
  }
}

const roundMs = (x) => Math.round(x * 10) * 100; // seconds -> ms rounded to 100

function renderEntries(ents, sess, t0) {
  let rid0 = null;
  const out = [];
  for (const e of ents) {
    let url = e.url;
    const u = new URL(url);
    // RID: the first numeric one of the session is <rid0>, later ones <rid0+k>
    const ridv = u.searchParams.get('RID');
    let ridNote;
    if (ridv && /^\d+$/.test(ridv)) {
      if (rid0 === null || u.searchParams.has('CVER')) rid0 = Number(ridv);
      ridNote = `<rid0${Number(ridv) - rid0 ? '+' + (Number(ridv) - rid0) : ''}>`;
    }
    const zx = u.searchParams.get('zx');
    let q = u.search;
    if (ridv && ridNote) q = q.replace(`RID=${ridv}`, `RID=${ridNote}`);
    if (zx) q = q.replace(`zx=${zx}`, 'zx=<zx>');
    url = scrub(`${u.origin}${u.pathname}${q}`, sess);
    // the body: concatenated bytes, with the chunk boundaries
    const real = e.chunks.filter((c) => c.b64);
    const bufs = real.map((c) => Buffer.from(c.b64, 'base64'));
    const whole = Buffer.concat(bufs);
    const dec = new TextDecoder('utf-8');
    const chunks = real.map((c, i) => ({ atMs: roundMs(c.ts - (e.tResp ?? e.ts)), bytes: bufs[i].length, text: scrub(dec.decode(bufs[i], { stream: true }), sess) }));
    const full = whole.toString('utf8');
    const resp = {
      status: e.status,
      statusFromNetwork: e.rawStatus,
      protocol: e.protocol,
      headers: hdrs(e.rawRes || e.resHeaders, sess),
      bodyBytes: whole.length,
      body: scrub(full, sess),
    };
    if (e.status === null) resp.noResponse = e.failed || 'none';
    if (e.failed) resp.failed = e.failed;
    if (real.length > 1 || e.streamed) resp.chunks = chunks;
    if (whole.length && /^\d+\n/.test(full)) {
      const u16 = parseUnits(full);
      const by = parseBytes(whole);
      resp.framing = { lengthCountsUtf16CodeUnits: u16.ok, lengthCountsUtf8Bytes: by.ok, nonAscii: whole.length !== full.length || /[^\x00-\x7f]/.test(full), frames: (u16.ok ? u16 : by).frames.map((f) => ({ len: f.len, payload: scrub(f.payload, sess) })) };
    }
    out.push({
      type: e.type,
      atMs: roundMs(e.ts - t0),
      durationMs: e.done ? roundMs(e.done - e.ts) : null,
      request: { method: e.method, url, headers: hdrs(e.rawReq || e.reqHeaders, sess), body: e.postData === null ? null : scrub(redactRid(e.postData, rid0), sess) },
      response: resp,
    });
  }
  return out;
}

function redactRid(body) {
  return body;
}

const keepLower = new Set();
function hdrs(h, sess) {
  if (!h) return null;
  const o = {};
  for (const [k, v] of Object.entries(h)) {
    const lk = k.toLowerCase();
    if (lk === 'authorization') o[k] = String(v).replace(/(Bearer|Firebase)\s+\S+/, '$1 <token>');
    else if (lk === 'x-goog-api-key') o[k] = '<api-key>';
    else if (lk === 'cookie') o[k] = '<cookie>';
    else if (lk === 'date' || lk === 'expires') o[k] = '<date>';
    else if (lk === 'set-cookie') o[k] = '<cookie>';
    else if (lk === 'x-debug-tracking-id') o[k] = '<tracking-id>';
    else if (lk === 'content-length' || lk === 'x-firebase-gmpid') o[k] = v;
    else o[k] = scrub(String(v), sess);
  }
  return Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]]));
}

// ---- page and browser ----

const pageHtml = `<!doctype html><meta charset="utf-8"><title>f6</title><script type="module">
import * as app from '${GSTATIC}firebase-app.js';
import * as auth from '${GSTATIC}firebase-auth.js';
import * as fs from '${GSTATIC}firebase-firestore.js';
import * as lite from '${GSTATIC}firebase-firestore-lite.js';
window.f6 = { app, auth, fs, lite, events: [], net: window.__net || [] };
window.f6ready = true;
</script>`;

function startServer() {
  const srv = createServer((req, res) => {
    res.setHeader('content-type', 'text/html; charset=utf-8');
    res.end(pageHtml);
  });
  return new Promise((ok, bad) => srv.once('error', bad).listen(PORT, '127.0.0.1', () => ok(srv)));
}

const initScript = `(() => {
  window.__net = [];
  const X = XMLHttpRequest.prototype, open = X.open, send = X.send, setH = X.setRequestHeader;
  X.open = function (m, u) { this.__r = { kind: 'xhr', method: m, url: String(u), headersSet: [] }; return open.apply(this, arguments); };
  X.setRequestHeader = function (k, v) { if (this.__r) this.__r.headersSet.push(k); return setH.apply(this, arguments); };
  X.send = function () { if (this.__r) { this.__r.withCredentials = this.withCredentials; window.__net.push(this.__r); } return send.apply(this, arguments); };
  const f = window.fetch;
  window.fetch = function (input, init) {
    const url = typeof input === 'string' ? input : input.url;
    window.__net.push({ kind: 'fetch', method: (init && init.method) || (input.method) || 'GET', url: String(url), credentials: (init && init.credentials) || (input.credentials) || 'default(same-origin)', mode: (init && init.mode) || input.mode || 'default(cors)', headersSet: init && init.headers ? Object.keys(init.headers instanceof Headers ? Object.fromEntries(init.headers) : init.headers) : [], keepalive: !!(init && init.keepalive) });
    return f.apply(this, arguments);
  };
  const ws = window.WebSocket; window.WebSocket = function (u) { window.__net.push({ kind: 'websocket', url: String(u) }); return new ws(...arguments); }; window.WebSocket.prototype = ws.prototype;
  const sb = navigator.sendBeacon && navigator.sendBeacon.bind(navigator); if (sb) navigator.sendBeacon = function (u) { window.__net.push({ kind: 'beacon', url: String(u) }); return sb.apply(null, arguments); };
})();`;

let browser;
async function newScenario(label) {
  const ctx = await browser.newContext({ ignoreHTTPSErrors: false });
  await ctx.addInitScript(initScript);
  // serve the SDK files the page imports from gstatic out of the installed package
  await ctx.route(`${GSTATIC}*`, (route) => {
    const name = new URL(route.request().url()).pathname.split('/').pop();
    let body;
    try {
      body = readFileSync(join(SDK_DIR, name));
    } catch {
      return route.abort();
    }
    route.fulfill({ status: 200, contentType: 'text/javascript', body, headers: { 'access-control-allow-origin': '*' } });
  });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => log(`  [${label}] pageerror: ${e.message.slice(0, 200)}\n`));
  const cdp = await ctx.newCDPSession(page);
  const cap = new Capture(cdp, label);
  await cap.start();
  await page.goto(ORIGIN_PAGE + '/');
  await page.waitForFunction(() => window.f6ready === true, null, { timeout: 30000 });
  return { ctx, page, cdp, cap, close: () => ctx.close() };
}

// a scenario: fn(page) drives the SDK; returns {entries, net, result}
async function runScenario(label, fn) {
  log(`scenario ${label}\n`);
  const s = await newScenario(label);
  let result, err;
  try {
    result = await fn(s.page);
  } catch (e) {
    err = e.message;
    log(`  [${label}] failed: ${e.message.slice(0, 300)}\n`);
  }
  await sleep(1500);
  const net = await s.page.evaluate(() => window.__net).catch(() => []);
  const ents = await s.cap.finish();
  const t0 = s.cap.t0 ?? 0;
  await s.close();
  const sess = learnIds(ents.map((e) => ({ request: { url: e.url } })));
  // SIDs also appear in response bodies: learn from bodies too ("c","<sid>")
  for (const e of ents) {
    const body = Buffer.concat(e.chunks.filter((c) => c.b64).map((c) => Buffer.from(c.b64, 'base64'))).toString('utf8');
    const m = /\["c","([^"]+)"/.exec(body);
    if (m && m[1].length >= 4) sess.ids.set(m[1], 'S');
  }
  return { label, entries: renderEntries(ents, sess, t0), net: net.map((n) => JSON.parse(scrub(JSON.stringify(n), sess))), result, error: err };
}

// ---- SDK helpers inside the page ----

const initApp = (cfg, settings, name) => async () => {};

async function pageInit(page, { cfg = CONFIG, settings = {}, name, signIn = null, lite = false }) {
  return page.evaluate(async ({ cfg, settings, name, signIn, lite }) => {
    const { app, auth, fs, lite: L } = window.f6;
    const a = app.initializeApp(cfg, name);
    window.f6.a = a;
    if (signIn) {
      const au = auth.getAuth(a);
      await auth.signInWithEmailAndPassword(au, signIn.email, signIn.pw);
    }
    if (lite) window.f6.db = L.getFirestore(a);
    else window.f6.db = fs.initializeFirestore(a, settings);
    window.f6.log = (kind, data) => window.f6.events.push({ kind, ...data });
    return 'ok';
  }, { cfg, settings, name, signIn, lite });
}

// ---- rules, user ----

async function rulesCall(method, path, body) {
  const r = await fetch(`${RULES_API}/${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${await accessToken()}`, 'x-goog-user-project': PROJECT },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return { http: r.status, body: await r.json().catch(() => ({})) };
}
async function deployRules(source) {
  const c = await rulesCall('POST', `projects/${PROJECT}/rulesets`, { source: { files: [{ name: 'firestore.rules', content: source }] } });
  if (c.http !== 200) throw new Error(`create ruleset: HTTP ${c.http}`);
  const u = await rulesCall('PATCH', `projects/${PROJECT}/releases/cloud.firestore`, { release: { name: `projects/${PROJECT}/releases/cloud.firestore`, rulesetName: c.body.name } });
  if (u.http !== 200) throw new Error(`update release: HTTP ${u.http}`);
  return c.body.name;
}
const extraRules = `
    match /${COL.open}/{d=**} { allow read, write: if true; }
    match /${COL.auth}/{d=**} { allow read, write: if request.auth != null; }
    match /${COL.deny}/{d=**} { allow read, write: if false; }
`;
const rulesSource = () => {
  const marker = 'match /databases/{database}/documents {\n';
  if (!ORIGINAL_RULES.includes(marker)) throw new Error('rules marker not found');
  return ORIGINAL_RULES.replace(marker, marker + extraRules);
};
async function toolkit(path, body, owner = false) {
  const url = owner ? `${TOOLKIT}/${path}` : `${TOOLKIT}/${path}?key=${API_KEY}`;
  const headers = { 'Content-Type': 'application/json' };
  if (owner) {
    headers.Authorization = `Bearer ${await accessToken()}`;
    headers['x-goog-user-project'] = PROJECT;
  }
  const r = await fetch(url, { method: 'POST', headers, body: JSON.stringify(body) });
  return { http: r.status, body: await r.json().catch(() => ({})) };
}

// owner-side writes (bypass rules) to change a document a page listens to
const written = new Set();
async function ownerSet(col, id, data) {
  const name = `${ROOT}/${col}/${id}`;
  assertScoped(name);
  written.add(name);
  const fields = {};
  for (const [k, v] of Object.entries(data)) fields[k] = typeof v === 'string' ? { stringValue: v } : { integerValue: String(v) };
  const r = await rest('PATCH', `${DB}/documents/${col}/${id}`, { fields });
  if (r.http !== 200) throw new Error(`owner set ${col}/${id}: HTTP ${r.http}`);
}

// ---- scenarios ----

const noteCommon = [
  `Driver: Playwright (headless Chromium) on the page ${ORIGIN_PAGE}/ (origin of every request), Firebase JS SDK ${SDK_VERSION}; HTTP seen through CDP (Network domain; streaming bodies via Network.streamResourceContent).`,
  'Placeholders: SID and gsessionid values become S.../G... of the same length; the first RID of a session is <rid0>, later ones <rid0+k>; zx is <zx>; tokens and the API key are placeholders; timestamps inside bodies keep their length with digits as N.',
  'atMs / durationMs are milliseconds since the first request of the scenario (rounded to 100 ms) and are indicative only. chunks[].atMs is since the response headers.',
  '"framing" parses a response body as "<len>\\n<payload>" records and says whether len counts UTF-16 code units or UTF-8 bytes (both are true when the body is ASCII; nonAscii says whether the body had non-ASCII characters).',
  '"net" lists what the page asked of XMLHttpRequest / fetch / WebSocket / sendBeacon (credentials mode, withCredentials, headers set by script).',
];

const textData = { s: NONASCII, n: 1 };

async function scListen(user, settings, label, idleMs) {
  return runScenario(label, async (page) => {
    await pageInit(page, { settings, name: label });
    await ownerSet(COL.open, 'a', { s: NONASCII, n: 1 });
    await ownerSet(COL.open, 'q1', { v: 1, s: 'one' });
    const evs = [];
    await page.evaluate(({ col }) => {
      const { fs, db } = window.f6;
      window.f6.un = {};
      window.f6.un.doc = fs.onSnapshot(fs.doc(db, col, 'a'), { includeMetadataChanges: false }, (s) => window.f6.log('doc', { exists: s.exists(), data: s.data() ?? null, fromCache: s.metadata.fromCache }), (e) => window.f6.log('doc-error', { code: e.code, message: e.message }));
    }, { col: COL.open });
    await sleep(4000);
    await ownerSet(COL.open, 'a', { s: NONASCII + ' changed', n: 2 });
    await sleep(3000);
    await page.evaluate(({ col }) => {
      const { fs, db } = window.f6;
      window.f6.un.q = fs.onSnapshot(fs.query(fs.collection(db, col), fs.where('v', '==', 1)), (s) => window.f6.log('query', { size: s.size, ids: s.docs.map((d) => d.id), fromCache: s.metadata.fromCache }), (e) => window.f6.log('query-error', { code: e.code, message: e.message }));
    }, { col: COL.open });
    await sleep(3000);
    await ownerSet(COL.open, 'q2', { v: 1, s: 'two' });
    await sleep(2500);
    await page.evaluate(() => window.f6.un.q());
    await sleep(3000);
    await ownerSet(COL.open, 'a', { s: 'after the query was removed', n: 3 });
    await sleep(2500);
    if (idleMs) await sleep(idleMs);
    const events = await page.evaluate(() => window.f6.events);
    await page.evaluate(() => window.f6.fs.terminate(window.f6.db));
    await sleep(2500);
    return { events };
  });
}

async function main() {
  const server = await startServer();
  browser = await chromium.launch({ headless: true });
  addSecret(API_KEY, '<api-key>');
  const pw = `Aa1!${randomBytes(12).toString('hex')}`;
  const email = `${P}-user@example.com`;
  let uid = null;
  let ruleset = null;
  const startedAt = new Date().toISOString();
  const files = {};
  let restored = false;
  try {
    ruleset = await deployRules(rulesSource());
    log(`deployed rules\n`);
    await sleep(8000); // rules propagate
    const su = await toolkit('accounts:signUp', { email, password: pw, returnSecureToken: true });
    if (su.http !== 200) throw new Error(`signUp: HTTP ${su.http}`);
    uid = su.body.localId;
    addSecret(pw, '<password>');
    addSecret(su.body.idToken, '<id-token>');

    if (wanted('listen')) {
      const s = await scListen(null, {}, 'listen-default', 75000);
      files['f6-webchannel-listen.json'] = {
        notes: [...noteCommon, 'Full SDK with default settings. Steps: onSnapshot on a document (non-ASCII string), the owner changes it over REST, a query target is added (where v == 1) and removed, the document changes again, ~40 s idle (keep-alive), terminate().'],
        cases: [{ id: 'listen.default', description: 'initializeFirestore(app, {}) ; Listen on a document, a query added and removed, idle, terminate', ...s }],
      };
    }
    if (wanted('longpoll')) {
      const s = await scListen(null, { experimentalForceLongPolling: true }, 'listen-longpoll', 40000);
      files['f6-webchannel-longpoll.json'] = {
        notes: [...noteCommon, 'Same steps as f6-webchannel-listen.json with experimentalForceLongPolling: true.'],
        cases: [{ id: 'listen.forceLongPolling', description: 'initializeFirestore(app, {experimentalForceLongPolling: true})', ...s }],
      };
    }
    if (wanted('write')) {
      const s = await runScenario('write', async (page) => {
        await pageInit(page, { name: 'write' });
        const r = await page.evaluate(async ({ col, text }) => {
          const { fs, db } = window.f6;
          const d = fs.doc(db, col, 'w1');
          const out = [];
          await fs.setDoc(d, { s: text, n: 1 });
          out.push('set');
          await new Promise((r) => setTimeout(r, 1500));
          await fs.updateDoc(d, { n: fs.increment(2), t: fs.serverTimestamp() });
          out.push('update');
          await new Promise((r) => setTimeout(r, 1500));
          const b = fs.writeBatch(db);
          b.set(fs.doc(db, col, 'w2'), { x: 1 });
          b.set(fs.doc(db, col, 'w3'), { y: text });
          await b.commit();
          out.push('batch');
          await new Promise((r) => setTimeout(r, 1500));
          await fs.deleteDoc(fs.doc(db, col, 'w2'));
          out.push('delete');
          await fs.waitForPendingWrites(db);
          await new Promise((r) => setTimeout(r, 40000));
          await fs.terminate(db);
          return out;
        }, { col: COL.open, text: NONASCII });
        for (const i of ['w1', 'w2', 'w3']) written.add(`${ROOT}/${COL.open}/${i}`);
        return r;
      });
      files['f6-webchannel-write.json'] = {
        notes: [...noteCommon, 'Full SDK default settings. Steps: setDoc (non-ASCII), updateDoc (increment + serverTimestamp), a two-write batch, deleteDoc, ~40 s idle, terminate().'],
        cases: [{ id: 'write.default', description: 'the Write channel: handshake, four commits, stream token, idle, terminate', ...s }],
      };
    }
    if (wanted('errors')) {
      const cases = [];
      cases.push({ id: 'listen.denied.doc', description: 'Listen on a document under rules `allow read: if false`', ...(await runScenario('err-listen-denied', async (page) => {
        await pageInit(page, { name: 'e1' });
        const r = await page.evaluate(async ({ col }) => {
          const { fs, db } = window.f6;
          return await new Promise((res) => {
            const un = fs.onSnapshot(fs.doc(db, col, 'x'), () => res({ ok: true }), (e) => res({ code: e.code, message: e.message }));
            setTimeout(() => res({ timeout: true }), 15000);
          });
        }, { col: COL.deny });
        await sleep(2000);
        await page.evaluate(() => window.f6.fs.terminate(window.f6.db));
        return r;
      })) });
      cases.push({ id: 'listen.denied.two-targets', description: 'a permitted Listen is open when a second, denied target is added: what arrives on the channel', ...(await runScenario('err-listen-denied2', async (page) => {
        await pageInit(page, { name: 'e1b' });
        await ownerSet(COL.open, 'e', { n: 1 });
        const r = await page.evaluate(async ({ open, deny }) => {
          const { fs, db } = window.f6;
          const out = [];
          fs.onSnapshot(fs.doc(db, open, 'e'), () => out.push('open-snap'), (e) => out.push('open-error ' + e.code));
          await new Promise((r) => setTimeout(r, 3000));
          fs.onSnapshot(fs.doc(db, deny, 'x'), () => out.push('deny-snap'), (e) => out.push('deny-error ' + e.code + ' ' + e.message));
          await new Promise((r) => setTimeout(r, 4000));
          out.push('state-after: ' + JSON.stringify(await new Promise((r) => { try { fs.getDocFromServer(fs.doc(db, open, 'e')).then((d) => r(d.exists()), (e) => r(e.code)); } catch (e) { r(String(e)); } })));
          return out;
        }, { open: COL.open, deny: COL.deny });
        await page.evaluate(() => window.f6.fs.terminate(window.f6.db));
        return r;
      })) });
      cases.push({ id: 'write.denied', description: 'setDoc under rules `allow write: if false`', ...(await runScenario('err-write-denied', async (page) => {
        await pageInit(page, { name: 'e2' });
        const r = await page.evaluate(async ({ col }) => {
          const { fs, db } = window.f6;
          try {
            await fs.setDoc(fs.doc(db, col, 'x'), { a: 1 });
            return 'ok';
          } catch (e) {
            return { code: e.code, message: e.message };
          }
        }, { col: COL.deny });
        await sleep(2000);
        // a good write after the denied one: does the stream survive?
        const r2 = await page.evaluate(async ({ col }) => {
          const { fs, db } = window.f6;
          try {
            await fs.setDoc(fs.doc(db, col, 'after-deny'), { a: 1 });
            return 'ok';
          } catch (e) {
            return { code: e.code, message: e.message };
          }
        }, { col: COL.open });
        written.add(`${ROOT}/${COL.open}/after-deny`);
        await page.evaluate(() => window.f6.fs.terminate(window.f6.db));
        return { denied: r, afterDenied: r2 };
      })) });
      cases.push({ id: 'listen.no-auth', description: 'Listen on a collection that needs request.auth, with no signed-in user', ...(await runScenario('err-noauth', async (page) => {
        await pageInit(page, { name: 'e3' });
        const r = await page.evaluate(async ({ col }) => {
          const { fs, db } = window.f6;
          return await new Promise((res) => {
            fs.onSnapshot(fs.doc(db, col, 'x'), () => res({ ok: true }), (e) => res({ code: e.code, message: e.message }));
            setTimeout(() => res({ timeout: true }), 15000);
          });
        }, { col: COL.auth });
        await page.evaluate(() => window.f6.fs.terminate(window.f6.db));
        return r;
      })) });
      cases.push({ id: 'listen.with-auth', description: 'the same Listen with a signed-in user (Authorization on the channel)', ...(await runScenario('err-auth', async (page) => {
        await pageInit(page, { name: 'e4', signIn: { email, pw } });
        const r = await page.evaluate(async ({ col }) => {
          const { fs, db } = window.f6;
          return await new Promise((res) => {
            fs.onSnapshot(fs.doc(db, col, 'x'), (s) => res({ ok: true, exists: s.exists() }), (e) => res({ code: e.code, message: e.message }));
            setTimeout(() => res({ timeout: true }), 15000);
          });
        }, { col: COL.auth });
        await sleep(1500);
        await page.evaluate(() => window.f6.fs.terminate(window.f6.db));
        return r;
      })) });
      cases.push({ id: 'listen.invalid-api-key', description: 'Listen with a syntactically valid but unknown API key', ...(await runScenario('err-badkey', async (page) => {
        await pageInit(page, { cfg: { ...CONFIG, apiKey: FAKE_KEY }, name: 'e5' });
        const r = await page.evaluate(async ({ col }) => {
          const { fs, db } = window.f6;
          const a = await new Promise((res) => {
            fs.onSnapshot(fs.doc(db, col, 'x'), () => res({ ok: true }), (e) => res({ code: e.code, message: e.message }));
            setTimeout(() => res({ timeout: true }), 15000);
          });
          return a;
        }, { col: COL.open });
        await page.evaluate(() => window.f6.fs.terminate(window.f6.db));
        return r;
      })) });
      cases.push({ id: 'getdoc.invalid-api-key', description: 'lite getDoc with an unknown API key (REST)', ...(await runScenario('err-badkey-lite', async (page) => {
        await pageInit(page, { cfg: { ...CONFIG, apiKey: FAKE_KEY }, name: 'e6', lite: true });
        return await page.evaluate(async ({ col }) => {
          const { lite, db } = window.f6;
          try {
            await lite.getDoc(lite.doc(db, col, 'x'));
            return 'ok';
          } catch (e) {
            return { code: e.code, message: e.message };
          }
        }, { col: COL.open });
      })) });
      cases.push({ id: 'raw.channel-errors', description: 'hand-made requests to the Listen and Write channels: unknown SID, malformed Authorization, no handshake params (Node fetch, Origin set)', requests: await rawChannelCases() });
      files['f6-webchannel-errors.json'] = { notes: [...noteCommon, 'Errors on the browser transports. The raw.channel-errors case sends requests from Node (not the SDK) with the Origin header of the page.'], cases };
    }
    if (wanted('cors')) {
      files['f6-cors.json'] = { notes: [...noteCommon, 'CORS: OPTIONS preflights sent from Node with Origin ' + ORIGIN_PAGE + ' (and others) and the headers the SDK sends, plus the browser-seen preflights of the other files (type Preflight in their entries).'], cases: [{ id: 'cors.preflights', description: 'OPTIONS to channel and REST data paths', requests: await corsCases() }] };
    }
    if (wanted('lite')) {
      const s = await runScenario('lite', async (page) => {
        await pageInit(page, { name: 'lite', lite: true });
        await ownerSet(COL.open, 'l1', { s: NONASCII, n: 1 });
        const r = await page.evaluate(async ({ col, text }) => {
          const { lite, db } = window.f6;
          const g = await lite.getDoc(lite.doc(db, col, 'l1'));
          await lite.setDoc(lite.doc(db, col, 'l2'), { s: text, n: 2 });
          const q = await lite.getDocs(lite.query(lite.collection(db, col), lite.where('n', '==', 2)));
          return { got: g.data(), queried: q.size };
        }, { col: COL.open, text: NONASCII });
        written.add(`${ROOT}/${COL.open}/l2`);
        return r;
      });
      files['f6-lite.json'] = { notes: [...noteCommon, 'firebase/firestore/lite on fetch: getDoc, setDoc, a query.'], cases: [{ id: 'lite.get-set-query', description: 'lite SDK: get, set (non-ASCII), runQuery', ...s }] };
    }
    const header = (notes) => ({
      oracle: 'production',
      target: `${PROJECT} (default): FIRESTORE_NATIVE, STANDARD, europe-west1; the browser transports (WebChannel and REST) of firestore.googleapis.com, from the foreign origin ${ORIGIN_PAGE}; rules deployed by the recorder for aruma-ref-f6-* (originals restored)`,
      generator: GENERATOR,
      recordedAt: `${startedAt} .. ${new Date().toISOString()}`,
      versions: versions({ 'firebase JS SDK': SDK_VERSION, chromium: browser.version(), playwright: JSON.parse(readFileSync(join(PW_DIR, 'package.json'), 'utf8')).version }),
      notes,
    });
    for (const [name, f] of Object.entries(files)) writeCorpus(name, header(f.notes), { cases: f.cases });
  } finally {
    try {
      const del = [...written].map((n) => ({ delete: n }));
      for (let i = 0; i < del.length; i += 100) await rest('POST', `${DB}/documents:commit`, { writes: del.slice(i, i + 100) });
      const left = [];
      for (const cid of Object.values(COL)) {
        const r = await rest('GET', `${DB}/documents/${cid}?showMissing=true&pageSize=100`);
        for (const d of r.body.documents || []) left.push(d.name);
      }
      log(`cleanup: deleted ${del.length}, left ${left.length}\n`);
    } catch (e) {
      log(`cleanup failed: ${e.message}\n`);
    }
    try {
      const r = await rulesCall('PATCH', `projects/${PROJECT}/releases/cloud.firestore`, { release: { name: `projects/${PROJECT}/releases/cloud.firestore`, rulesetName: ORIGINAL_RULESET } });
      restored = r.http === 200;
      log(`restore release: HTTP ${r.http}\n`);
      if (ruleset) await rulesCall('DELETE', ruleset);
    } catch (e) {
      log(`restore failed: ${e.message}\n`);
    }
    try {
      if (uid) await toolkit('accounts:delete', { localId: uid, targetProjectId: PROJECT }, true);
      log('deleted test user\n');
    } catch (e) {
      log(`user deletion failed: ${e.message}\n`);
    }
    await browser.close().catch(() => {});
    server.close();
  }
}

// ---- Node-side raw requests ----

async function raw(method, url, headers, body) {
  const t = Date.now();
  let res, text, err;
  try {
    res = await fetch(url, { method, headers, body, signal: AbortSignal.timeout(20000) });
    text = await res.text();
  } catch (e) {
    err = e.message;
  }
  const sess = { ids: new Map() };
  if (res) {
    const hs = res.headers.get('x-http-session-id');
    if (hs) sess.ids.set(hs, 'G');
    const m = /\["c","([^"]+)"/.exec(text || '');
    if (m) sess.ids.set(m[1], 'S');
  }
  return {
    request: { method, url: scrub(url, sess), headers: hdrs(headers, sess), body: body ?? null },
    response: err ? { error: err } : { status: res.status, headers: hdrs(Object.fromEntries(res.headers), sess), body: scrub(text, sess) },
  };
}

const chanBase = (kind) => `https://${FS_HOST}/google.firestore.v1.Firestore/${kind}/channel`;
const dbName = DB;
const initBody = (kind) => {
  // the first message the SDK sends is ofs=0 req0___data__={...}; for the Listen channel an addTarget
  const doc = `${ROOT}/${COL.open}/a`;
  const data = kind === 'Listen' ? { database: dbName, addTarget: { documents: { documents: [doc] }, targetId: 2 } } : { database: dbName };
  return `count=1&ofs=0&req0___data__=${encodeURIComponent(JSON.stringify(data))}`;
};

async function rawChannelCases() {
  const out = [];
  const H = { 'content-type': 'application/x-www-form-urlencoded', origin: ORIGIN_PAGE, referer: ORIGIN_PAGE + '/' };
  const q = (extra) => `?VER=8&database=${encodeURIComponent(dbName)}${extra}&RID=${10000 + Math.floor(Math.random() * 1000)}&CVER=22&X-HTTP-Session-Id=gsessionid&zx=${randomBytes(6).toString('hex')}&t=1`;
  for (const kind of ['Listen', 'Write']) {
    out.push({ label: `${kind}: handshake without Authorization (rules allow the open path)`, ...(await raw('POST', chanBase(kind) + q(''), H, initBody(kind))) });
    out.push({ label: `${kind}: handshake with a malformed Authorization`, ...(await raw('POST', chanBase(kind) + q(''), { ...H, authorization: 'Bearer not-a-token' }, initBody(kind))) });
    out.push({ label: `${kind}: handshake with a malformed Authorization and an invalid API key`, ...(await raw('POST', chanBase(kind) + q('&key=' + FAKE_KEY), { ...H, authorization: 'Bearer not-a-token' }, initBody(kind))) });
    out.push({ label: `${kind}: backchannel GET with an unknown SID`, ...(await raw('GET', chanBase(kind) + `?VER=8&database=${encodeURIComponent(dbName)}&gsessionid=bogusbogusbogus&SID=bogusbogusbogus&RID=rpc&AID=0&CI=0&TYPE=xmlhttp&zx=${randomBytes(6).toString('hex')}&t=1`, { origin: ORIGIN_PAGE, referer: ORIGIN_PAGE + '/' })) });
    out.push({ label: `${kind}: forward POST with an unknown SID`, ...(await raw('POST', chanBase(kind) + `?VER=8&database=${encodeURIComponent(dbName)}&gsessionid=bogusbogusbogus&SID=bogusbogusbogus&RID=20001&AID=0&zx=${randomBytes(6).toString('hex')}&t=1`, H, initBody(kind))) });
    out.push({ label: `${kind}: TYPE=terminate with an unknown SID`, ...(await raw('POST', chanBase(kind) + `?VER=8&database=${encodeURIComponent(dbName)}&gsessionid=bogusbogusbogus&SID=bogusbogusbogus&RID=20002&TYPE=terminate&zx=${randomBytes(6).toString('hex')}&t=1`, H, 'count=1&ofs=0&req0___data__=%5B%5D')) });
    out.push({ label: `${kind}: no VER and no database parameter`, ...(await raw('POST', chanBase(kind) + `?RID=30000&zx=${randomBytes(6).toString('hex')}`, H, initBody(kind))) });
  }
  return out;
}

async function corsCases() {
  const out = [];
  const docUrl = `https://${FS_HOST}/v1/${ROOT}/${COL.open}/a`;
  const listUrl = `https://${FS_HOST}/v1/${ROOT}/${COL.open}`;
  const commitUrl = `https://${FS_HOST}/v1/${DB}/documents:commit`;
  const chan = (k) => chanBase(k) + `?VER=8&database=${encodeURIComponent(DB)}&RID=30001&CVER=22&X-HTTP-Session-Id=gsessionid&zx=abc&t=1`;
  const pre = (url, method, reqHeaders, origin = ORIGIN_PAGE) => raw('OPTIONS', url, { origin, 'access-control-request-method': method, ...(reqHeaders ? { 'access-control-request-headers': reqHeaders } : {}) });
  const sdkRest = 'authorization,content-type,x-goog-api-client,x-goog-request-params,x-firebase-gmpid,x-goog-api-key,x-firebase-appcheck';
  out.push({ label: 'Listen channel preflight POST, headers the SDK sends', ...(await pre(chan('Listen'), 'POST', 'content-type,x-goog-api-client,x-goog-request-params,authorization,x-firebase-gmpid')) });
  out.push({ label: 'Listen channel preflight GET (backchannel)', ...(await pre(chan('Listen'), 'GET', null)) });
  out.push({ label: 'Write channel preflight POST', ...(await pre(chan('Write'), 'POST', 'content-type,x-goog-api-client,x-goog-request-params,authorization,x-firebase-gmpid')) });
  out.push({ label: 'REST get preflight (GET) with authorization and x-goog-api-client', ...(await pre(docUrl, 'GET', 'authorization,x-goog-api-client')) });
  out.push({ label: 'REST list preflight (GET), headers of the lite SDK', ...(await pre(listUrl, 'GET', sdkRest)) });
  out.push({ label: 'REST commit preflight (POST)', ...(await pre(commitUrl, 'POST', sdkRest)) });
  out.push({ label: 'REST PATCH preflight', ...(await pre(docUrl, 'PATCH', sdkRest)) });
  out.push({ label: 'REST DELETE preflight', ...(await pre(docUrl, 'DELETE', sdkRest)) });
  const liteHdrs = 'google-cloud-resource-prefix,x-firebase-gmpid,x-goog-api-client,x-goog-request-params';
  const liteUa = { 'user-agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/156.0.8078.4 Safari/537.36', accept: '*/*', 'sec-fetch-mode': 'cors', 'sec-fetch-site': 'cross-site', 'sec-fetch-dest': 'empty', referer: ORIGIN_PAGE + '/' };
  for (const op of ['batchGet', 'commit', 'runQuery']) {
    out.push({ label: `lite preflight as Chromium sends it: ${op}?key=`, ...(await raw('OPTIONS', `https://${FS_HOST}/v1/${DB}/documents:${op}?key=${API_KEY}`, { origin: ORIGIN_PAGE, 'access-control-request-method': 'POST', 'access-control-request-headers': liteHdrs, ...liteUa })) });
  }
  out.push({ label: 'lite preflight as Chromium sends it, plus authorization', ...(await raw('OPTIONS', `https://${FS_HOST}/v1/${DB}/documents:commit?key=${API_KEY}`, { origin: ORIGIN_PAGE, 'access-control-request-method': 'POST', 'access-control-request-headers': 'authorization,' + liteHdrs, ...liteUa })) });
  out.push({ label: 'REST preflight asking for an arbitrary header', ...(await pre(commitUrl, 'POST', 'x-made-up-header')) });
  out.push({ label: 'REST preflight with no Access-Control-Request-Headers', ...(await pre(commitUrl, 'POST', null)) });
  out.push({ label: 'REST preflight from https://example.com', ...(await pre(commitUrl, 'POST', sdkRest, 'https://example.com')) });
  out.push({ label: 'REST preflight from Origin: null', ...(await pre(commitUrl, 'POST', sdkRest, 'null')) });
  out.push({ label: 'REST OPTIONS without Origin', ...(await raw('OPTIONS', commitUrl, { 'access-control-request-method': 'POST' })) });
  out.push({ label: 'channel path OPTIONS from https://example.com', ...(await pre(chan('Listen'), 'POST', 'content-type', 'https://example.com')) });
  // real responses with Origin (no credentials): a successful GET of a document, a 404 and a 403
  out.push({ label: 'REST GET document (open path), Origin set, no auth', ...(await raw('GET', `${docUrl}?key=${API_KEY}`, { origin: ORIGIN_PAGE })) });
  out.push({ label: 'REST GET a missing document, Origin set', ...(await raw('GET', `https://${FS_HOST}/v1/${ROOT}/${COL.open}/nonexistent?key=${API_KEY}`, { origin: ORIGIN_PAGE })) });
  out.push({ label: 'REST GET denied path, Origin set', ...(await raw('GET', `https://${FS_HOST}/v1/${ROOT}/${COL.deny}/x?key=${API_KEY}`, { origin: ORIGIN_PAGE })) });
  out.push({ label: 'REST GET without key, Origin set', ...(await raw('GET', docUrl, { origin: ORIGIN_PAGE })) });
  out.push({ label: 'REST GET, Origin set to https://example.com', ...(await raw('GET', `${docUrl}?key=${API_KEY}`, { origin: 'https://example.com' })) });
  out.push({ label: 'REST GET without Origin', ...(await raw('GET', `${docUrl}?key=${API_KEY}`, {})) });
  return out;
}

main().catch((e) => {
  log(`\nFAILED: ${e.message}\n`);
  process.exit(1);
});
