// capture_r3.mjs - records golden RTDB frames for phase R3 (listens, server-side
// queries, listen revocation, long polling /.lp) from the official Firebase
// Realtime Database emulator.
//
// Usage:
//   java -jar firebase-database-emulator-v4.11.2.jar --host 127.0.0.1 --port 9400
//   node capture_r3.mjs [host:port] [outDir]
//   SCENARIOS=query_key,lp_upgrade node capture_r3.mjs 127.0.0.1:9400
// host:port defaults to $RTDB_EMULATOR_HOST, then 127.0.0.1:9400.
// outDir defaults to the directory of this script.
//
// Requires Node >= 22 (global WebSocket and fetch). Same file format as
// capture.mjs: every WebSocket text frame in both directions is recorded in
// arrival order as the raw string; frames longer than 1000 chars are stored as
// {len, sha256}. Long-poll HTTP exchanges are recorded as
//   {conn, dir:"http_req", text:"GET /.lp?..."}
//   {conn, dir:"http_resp", ser, status, headers:{...}, ms, text}
//   {conn, dir:"http_abort", ser, ms, text}   (client gave up / closed)
// where ser is the request's ser parameter (absent if it has none), ms the
// time from sending that request to reading the body. Messages the emulated
// client sends over LP are also recorded as {dir:"send"} (the JSON before
// base64/segmenting), WebSocket frames as in capture.mjs.
// Every scenario runs in its own namespace (r3-<name>); rules and data are
// reset at the start with the emulator owner token, recorded under "setup".
// lp_poll waits for the server to answer an idle poll on its own (~1-2 min).

import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HOST = process.argv[2] || process.env.RTDB_EMULATOR_HOST || '127.0.0.1:9400';
const OUT = process.argv[3] || path.dirname(fileURLToPath(import.meta.url));
const EMULATOR = 'firebase-database-emulator-v4.11.2';
const TIMEOUT = 1500; // wait for a reply matching r
const SETTLE = 500; // extra wait after each step so pushes to other sockets arrive
const BIG = 1000; // frames longer than this are stored as len + sha256

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const sha256 = (s) => crypto.createHash('sha256').update(s, 'utf8').digest('hex');
const today = () => {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
};

class Recorder {
  constructor(scenario, description, ns) {
    this.doc = {
      scenario,
      description,
      source: 'emulator',
      emulator: EMULATOR,
      captured: today(),
      script: 'capture_r3.mjs',
      namespace: ns,
      setup: [],
      connections: {},
      frames: [],
      notes: [],
    };
    this.ns = ns;
    this.conns = [];
  }
  entry(conn, dir, text) {
    if (text.length > BIG) return { conn, dir, len: text.length, sha256: sha256(text) };
    return { conn, dir, text };
  }
  note(s) {
    this.doc.notes.push(s);
  }
  async finish() {
    for (const c of this.conns) await c.close();
    const byClient = this.conns.filter((c) => c.ws && c.clientClosed).map((c) => c.name);
    const byServer = this.conns.filter((c) => c.ws && !c.clientClosed && c.isClosed).map((c) => c.name);
    if (byClient.length)
      this.note(
        `WebSocket close initiated by the client (close frame with code 1000) on: ${byClient.join(', ')}; the code recorded is what the client saw in the emulator's closing reply (1005 = the reply carried no status code).`,
      );
    if (byServer.length) this.note(`WebSocket close initiated by the server on: ${byServer.join(', ')}.`);
    const file = path.join(OUT, `${this.doc.scenario}.json`);
    fs.writeFileSync(file, JSON.stringify(this.doc, null, 2) + '\n');
    console.log(`wrote ${file} (${this.doc.frames.length} frames)`);
  }
}

// Base connection: recording, multi-frame reassembly, request/response matching.
class Conn {
  constructor(rec, name, urlPath) {
    this.rec = rec;
    this.name = name;
    this.urlPath = urlPath;
    this.rid = 0;
    this.waiters = new Map();
    this.pending = 0;
    this.buf = '';
    this.msgs = [];
    this.isClosed = false;
    this.onMsg = null;
    rec.doc.connections[name] = urlPath;
    rec.conns.push(this);
    this.hello = new Promise((r) => (this._helloResolve = r));
    this.closedP = new Promise((r) => (this._closedResolve = r));
  }
  _recv(text) {
    this.rec.doc.frames.push(this.rec.entry(this.name, 'recv', text));
    this._helloResolve(text);
    if (this.pending > 0) {
      this.buf += text;
      if (--this.pending === 0) this._msg(this.buf);
    } else if (text.length <= 6 && /^\d+$/.test(text)) {
      this.pending = Number(text);
      this.buf = '';
    } else this._msg(text);
  }
  _msg(s) {
    let o;
    try {
      o = JSON.parse(s);
    } catch {
      return;
    }
    this.msgs.push(o);
    if (this.onMsg) this.onMsg(o);
    if (o && o.t === 'd' && o.d && o.d.r != null && this.waiters.has(o.d.r)) {
      this.waiters.get(o.d.r)(o);
      this.waiters.delete(o.d.r);
    }
  }
  _closed(code, reason) {
    if (this.isClosed) return;
    this.isClosed = true;
    this.rec.doc.frames.push({ conn: this.name, dir: 'close', code, reason });
    for (const [, w] of this.waiters) w(null);
    this.waiters.clear();
    this._closedResolve({ code, reason });
  }
  send(text) {
    this.rec.doc.frames.push(this.rec.entry(this.name, 'send', text));
    this._send(text);
  }
  async sendAndWait(text, r, { timeout = TIMEOUT, settle = SETTLE } = {}) {
    const p = new Promise((res) => {
      this.waiters.set(r, res);
      setTimeout(() => {
        if (this.waiters.has(r)) {
          this.waiters.delete(r);
          res(null);
        }
      }, timeout);
    });
    this.send(text);
    const resp = await p;
    if (!resp)
      this.rec.note(
        `${this.name} r=${r}: no reply within ${timeout} ms` + (this.isClosed ? ' (connection closed)' : ''),
      );
    await sleep(settle);
    return resp;
  }
  async req(a, b, opts = {}) {
    const r = ++this.rid;
    return this.sendAndWait(JSON.stringify({ t: 'd', d: { r, a, b } }), r, opts);
  }
}

class WsConn extends Conn {
  constructor(rec, name, ns, query = '') {
    const urlPath = `/.ws?v=5${query}&ns=${ns}`;
    super(rec, name, urlPath);
    this.ws = new WebSocket(`ws://${HOST}${urlPath}`);
    this.ws.onmessage = (e) => this._recv(typeof e.data === 'string' ? e.data : '<binary>');
    this.ws.onclose = (e) => this._closed(e.code, e.reason);
    this.ws.onerror = () => {};
    this.opened = new Promise((r) => (this.ws.onopen = () => r(true)));
  }
  _send(t) {
    this.ws.send(t);
  }
  async close() {
    if (this.isClosed) return;
    this.clientClosed = true;
    this.ws.close(1000);
    await Promise.race([this.closedP, sleep(2000)]);
  }
}

async function open(rec, name, query = '') {
  const c = new WsConn(rec, name, rec.ns, query);
  await Promise.race([c.hello, sleep(3000)]);
  return c;
}

// ---------------------------------------------------------------- long polling
// Emulates the JS SDK's BrowserPollConnection + FirebaseIFrameScriptHolder
// (packages/database/src/realtime/BrowserPollConnection.ts) without the iframe:
// script URLs are fetched directly and the JSONP bodies are parsed.
const MAX_URL_DATA_SIZE = 1870;
const SEG_HEADER_SIZE = 30;
const MAX_PAYLOAD_SIZE = MAX_URL_DATA_SIZE - SEG_HEADER_SIZE;
const KEEPALIVE_REQUEST_INTERVAL = 25000;
// @firebase/util base64Encode: UTF-8 bytes, web-safe alphabet "-_" and "." padding
const b64ws = (s) => Buffer.from(s, 'utf8').toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '.');

async function httpGet(rec, name, urlPath, { signal, timeout } = {}) {
  rec.doc.frames.push({ conn: name, dir: 'http_req', text: `GET ${urlPath}` });
  const m = urlPath.match(/[?&]ser=(\d+)/);
  const ser = m ? Number(m[1]) : undefined;
  const t = Date.now();
  if (timeout) signal = AbortSignal.timeout(timeout);
  const abort = () => {
    rec.doc.frames.push({ conn: name, dir: 'http_abort', ser, ms: Date.now() - t, text: `GET ${urlPath}` });
    return null;
  };
  let r;
  try {
    r = await fetch(`http://${HOST}${urlPath}`, { signal });
  } catch (e) {
    return abort();
  }
  let text;
  try {
    text = await r.text();
  } catch (e) {
    return abort();
  }
  const headers = {};
  for (const [k, v] of r.headers) if (k !== 'x-firebase-uuid' && k !== 'date') headers[k] = v;
  const e = { conn: name, dir: 'http_resp', ser, status: r.status, headers, ms: Date.now() - t };
  if (text.length > BIG) Object.assign(e, { len: text.length, sha256: sha256(text) });
  else e.text = text;
  rec.doc.frames.push(e);
  return { status: r.status, text, headers };
}

class LpClient {
  constructor(rec, name, { cb = 1, startSer = 31415926, serial = 27182818, keepalive = true } = {}) {
    this.rec = rec;
    this.name = name;
    this.ns = rec.ns;
    this.cb = cb;
    this.startSer = startSer;
    this.currentSerial = serial;
    this.keepalive = keepalive;
    this.outstanding = new Set();
    this.pendingSegs = [];
    this.curSegmentNum = 0;
    this.sendNewPolls = true;
    this.alive = false;
    this.rid = 0;
    this.waiters = new Map();
    this.msgs = [];
    this.pendingResponses = [];
    this.currentResponseNum = 0;
    this.closeAfterResponse = -1;
    this.isClosed = false;
    this.aborts = new Set();
    this.timers = new Set();
    this.onMsg = null;
    this.closedByServer = new Promise((r) => (this._closedByServer = r));
    rec.conns.push(this);
    rec.doc.connections[name] = '/.lp (long polling, JS SDK BrowserPollConnection emulation)';
  }
  // repoInfoConnectionURL: params in insertion order, ns appended last, values not URL-encoded
  url(params) {
    const all = { ...params, ns: this.ns };
    return '/.lp?' + Object.entries(all).map(([k, v]) => `${k}=${v}`).join('&');
  }
  async get(urlPath) {
    const ac = new AbortController();
    this.aborts.add(ac);
    const r = await httpGet(this.rec, this.name, urlPath, { signal: ac.signal });
    this.aborts.delete(ac);
    if (r && r.status === 200) this.handleBody(r.text);
    return r;
  }
  handleBody(text) {
    for (const raw of text.split('\n')) {
      const line = raw.trim();
      let m;
      if ((m = line.match(/^pLPCommand\('start','([^']*)','([^']*)'\);$/))) {
        this.id = m[1];
        this.pw = m[2];
      } else if ((m = line.match(/^pLPCommand\('close'(?:,(\d+))?\);$/))) {
        this.sendNewPolls = false;
        this.serverClose = m[1] == null ? 'close' : `close after ${m[1]}`;
        if (m[1] != null) this.closeAfterResponse = Number(m[1]);
        this._closedByServer(this.serverClose);
      } else if ((m = line.match(/^pRTLPCB\((\d+),(.*)\);$/))) {
        this.handleResponse(Number(m[1]), JSON.parse(m[2]));
      }
    }
  }
  // PacketReceiver.handleResponse
  handleResponse(n, data) {
    this.pendingResponses[n] = data;
    while (this.pendingResponses[this.currentResponseNum]) {
      const list = this.pendingResponses[this.currentResponseNum];
      delete this.pendingResponses[this.currentResponseNum];
      for (const o of list) this._msg(o);
      if (this.currentResponseNum === this.closeAfterResponse) break;
      this.currentResponseNum++;
    }
  }
  _msg(o) {
    this.msgs.push(o);
    if (o && o.t === 'c' && o.d && o.d.t === 'h') this.helloMsg = o.d.d;
    if (this.onMsg) this.onMsg(o);
    if (o && o.t === 'd' && o.d && o.d.r != null && this.waiters.has(o.d.r)) {
      this.waiters.get(o.d.r)(o);
      this.waiters.delete(o.d.r);
    }
  }
  async start() {
    const params = { start: 't', ser: this.startSer, cb: this.cb, v: '5' };
    const r = await this.get(this.url(params));
    return r;
  }
  // FirebaseIFrameScriptHolder.startLongPoll
  begin() {
    this.alive = true;
    while (this.newRequest()) {}
  }
  newRequest() {
    if (this.alive && this.sendNewPolls && this.outstanding.size < (this.pendingSegs.length > 0 ? 2 : 1)) {
      this.currentSerial++;
      let theURL = this.url({ id: this.id, pw: this.pw, ser: this.currentSerial });
      let cur = '';
      let i = 0;
      while (this.pendingSegs.length > 0) {
        const next = this.pendingSegs[0];
        if (next.d.length + SEG_HEADER_SIZE + cur.length <= MAX_URL_DATA_SIZE) {
          const s = this.pendingSegs.shift();
          cur += `&seg${i}=${s.seg}&ts${i}=${s.ts}&d${i}=${s.d}`;
          i++;
        } else break;
      }
      this.addLongPollTag(theURL + cur, this.currentSerial);
      return true;
    }
    return false;
  }
  addLongPollTag(url, serial) {
    this.outstanding.add(serial);
    const doNewRequest = () => {
      this.outstanding.delete(serial);
      this.newRequest();
    };
    let ka = null;
    if (this.keepalive) {
      ka = setTimeout(() => {
        this.timers.delete(ka);
        doNewRequest();
      }, KEEPALIVE_REQUEST_INTERVAL);
      this.timers.add(ka);
    }
    // script tags are added with setTimeout(..., 1) in the SDK
    setTimeout(() => {
      if (!this.sendNewPolls) {
        this.outstanding.delete(serial);
        return;
      }
      this.get(url).then((r) => {
        if (ka) {
          clearTimeout(ka);
          this.timers.delete(ka);
        }
        if (r === null) return;
        doNewRequest();
      });
    }, 1);
  }
  send(obj) {
    const segs = [];
    const b = b64ws(JSON.stringify(obj));
    for (let c = 0; c < b.length; c += MAX_PAYLOAD_SIZE) segs.push(b.slice(c, c + MAX_PAYLOAD_SIZE));
    this.rec.doc.frames.push(this.rec.entry(this.name, 'send', JSON.stringify(obj)));
    for (const d of segs) {
      this.pendingSegs.push({ seg: this.curSegmentNum, ts: segs.length, d });
      this.curSegmentNum++;
    }
    if (this.alive) this.newRequest();
  }
  async req(a, b, { timeout = TIMEOUT, settle = SETTLE } = {}) {
    const r = ++this.rid;
    const p = new Promise((res) => {
      this.waiters.set(r, res);
      setTimeout(() => {
        if (this.waiters.has(r)) {
          this.waiters.delete(r);
          res(null);
        }
      }, timeout);
    });
    this.send({ t: 'd', d: { r, a, b } });
    const resp = await p;
    if (!resp) this.rec.note(`${this.name} r=${r}: no reply within ${timeout} ms`);
    await sleep(settle);
    return resp;
  }
  // stop polling (no disconn request), abort what is still outstanding
  async close() {
    if (this.isClosed) return;
    this.isClosed = true;
    this.alive = false;
    this.sendNewPolls = false;
    for (const t of this.timers) clearTimeout(t);
    for (const ac of this.aborts) ac.abort();
    await sleep(50);
  }
}

// ---------------------------------------------------------------- helpers

async function rest(method, p, { ns, body, headers = {} } = {}) {
  const sep = p.includes('?') ? '&' : '?';
  const url = `http://${HOST}${p}${sep}ns=${ns}`;
  const r = await fetch(url, { method, body: body === undefined ? undefined : JSON.stringify(body), headers });
  const text = await r.text();
  return { method, url: url.replace(`http://${HOST}`, ''), status: r.status, etag: r.headers.get('etag'), body: text };
}

const OPEN_RULES = { rules: { '.read': true, '.write': true } };
const OWNER = { Authorization: 'Bearer owner' };

async function prep(rec, { rules = OPEN_RULES, data = null } = {}) {
  let r1;
  // a namespace that was reset (see listen_errors) answers 503 for a while
  for (let i = 0; i < 60; i++) {
    r1 = await rest('PUT', '/.settings/rules.json', { ns: rec.ns, body: rules, headers: OWNER });
    if (r1.status !== 503) break;
    await sleep(1000);
  }
  rec.doc.setup.push({ method: 'PUT', path: r1.url, body: rules, status: r1.status, response: r1.body });
  const r2 = await rest('PUT', '/.json', { ns: rec.ns, body: data, headers: OWNER });
  rec.doc.setup.push({ method: 'PUT', path: r2.url, body: data, status: r2.status });
}

async function etagOf(rec, p) {
  const g = await rest('GET', `${p}.json`, { ns: rec.ns, headers: { 'X-Firebase-ETag': 'true' } });
  rec.doc.setup.push({ method: 'GET', path: g.url, headers: { 'X-Firebase-ETag': 'true' }, status: g.status, etag: g.etag, response: g.body });
  return g.etag;
}

// listen body in the SDK's key order: p, q, t, h (PersistentConnection.sendListen_)
function lb(p, q, t, h = '') {
  const b = { p };
  if (q) {
    b.q = q;
    b.t = t;
  }
  b.h = h;
  return b;
}
// unlisten body: p, q, t (PersistentConnection.sendUnlisten_)
function ub(p, q, t) {
  const b = { p };
  if (q) {
    b.q = q;
    b.t = t;
  }
  return b;
}

const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
function unsignedJwt(uid) {
  const now = Math.floor(Date.now() / 1000);
  return `${b64url({ alg: 'none', typ: 'JWT' })}.${b64url({ uid, sub: uid, user_id: uid, iat: now, exp: now + 3600 })}.`;
}

// ---------------------------------------------------------------- query / listen scenarios

async function listenDefault() {
  const rec = new Recorder(
    'listen_default',
    'Default (untagged) listens on /l (B), /l/b (C) and / (D) while A writes: child set, deep set, replace, multi-path merge, root replace, delete. Then listens with a matching hash (REST ETag), a wrong hash and an empty hash on existing data.',
    'r3-listen-default',
  );
  await prep(rec, { data: { l: { a: 1, b: { c: 2 } } } });
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  const C = await open(rec, 'C');
  const D = await open(rec, 'D');
  await B.req('q', lb('/l'));
  await C.req('q', lb('/l/b'));
  await D.req('q', lb('/'));
  await A.req('p', { p: '/l/a', d: 5 });
  await A.req('p', { p: '/l/b/c', d: 3 });
  await A.req('p', { p: '/l', d: { z: 1 } });
  await A.req('m', { p: '/l', d: { x: 1, 'y/z': 2, z: null } });
  await A.req('p', { p: '/', d: { l: { k: 1 }, other: 2 } });
  await A.req('p', { p: '/l', d: null });
  // hash listens
  await A.req('p', { p: '/h', d: { a: 1, b: { c: 2 } } });
  const tag = await etagOf(rec, '/h');
  rec.doc.etag = tag;
  const E = await open(rec, 'E');
  await E.req('q', lb('/h', null, null, tag));
  const F = await open(rec, 'F');
  await F.req('q', lb('/h', null, null, 'wronghash'));
  const G = await open(rec, 'G');
  await G.req('q', lb('/h', null, null, ''));
  await A.req('p', { p: '/h/a', d: 2 });
  rec.note('A writes in order: p /l/a 5; p /l/b/c 3; p /l {z:1}; m /l {x:1,"y/z":2,z:null}; p / {l:{k:1},other:2}; p /l null. B listens /l, C listens /l/b, D listens /.');
  rec.note('Hash part: A sets /h = {a:1,b:{c:2}}; etag = REST ETag of /h (GET with X-Firebase-ETag: true, see setup); E listens /h with h = etag, F with h = "wronghash", G with h = ""; then A sets /h/a 2.');
  await rec.finish();
}

async function queryLimitPriority() {
  const rec = new Recorder(
    'query_limit_priority',
    'Limit queries on the default (priority) order over leaf children a..e (all null priority, so key order): B limitToLast(2) tag 1, C limitToFirst(2) tag 1. A adds, deletes, changes values, sets priorities, merges; then B unlistens.',
    'r3-qlp',
  );
  await prep(rec, { data: { q: { a: 1, b: 2, c: 3, d: 4, e: 5 } } });
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  const C = await open(rec, 'C');
  const last2 = { l: 2, vf: 'r' };
  const first2 = { l: 2, vf: 'l' };
  await B.req('q', lb('/q', last2, 1));
  await C.req('q', lb('/q', first2, 1));
  await A.req('p', { p: '/q/f', d: 6 }); // 1 enters B (evicts d)
  await A.req('p', { p: '/q/e', d: null }); // 2 delete window child of B
  await A.req('p', { p: '/q/f', d: 60 }); // 3 change window child value
  await A.req('p', { p: '/q/a', d: { '.value': 1, '.priority': 1 } }); // 4 priority moves a to the end
  await A.req('p', { p: '/q/b0', d: 22 }); // 5 sorts before B's window, inside C's
  await A.req('m', { p: '/q', d: { c: null, g: 7 } }); // 6 merge
  await A.req('p', { p: '/q/a', d: 1 }); // 7 priority removed, a moves back to the front
  await A.req('p', { p: '/q/zz/deep', d: 1 }); // 8 deep write creating a new last child
  await B.req('n', ub('/q', last2, 1));
  await A.req('p', { p: '/q/zzz', d: 9 }); // 9 after B unlisten
  await A.req('g', { p: '/q', q: {} });
  rec.note('Steps (A): 1 p /q/f 6; 2 p /q/e null; 3 p /q/f 60; 4 p /q/a {.value:1,.priority:1}; 5 p /q/b0 22; 6 m /q {c:null,g:7}; 7 p /q/a 1; 8 p /q/zz/deep 1; B unlistens (n with q and t); 9 p /q/zzz 9; A reads /q.');
  rec.note('Windows by the client-side rules: B (last 2) starts {d,e}; C (first 2) starts {a,b}.');
  await rec.finish();
}

async function queryKey() {
  const rec = new Recorder(
    'query_key',
    'orderByKey queries on /k = {a..e}: B startAt(b).endAt(d), C startAfter(b).endBefore(d), D startAt(b).limitToFirst(2), E equalTo(c), F limitToLast(2). A writes entering/leaving the ranges.',
    'r3-qkey',
  );
  await prep(rec, { data: { k: { a: 1, b: 2, c: 3, d: 4, e: 5 } } });
  const A = await open(rec, 'A');
  const qs = {
    B: { sp: 'b', sin: true, ep: 'd', ein: true, i: '.key' },
    C: { sp: 'b', sin: false, ep: 'd', ein: false, i: '.key' },
    D: { sp: 'b', sin: true, l: 2, vf: 'l', i: '.key' },
    E: { sp: 'c', sin: true, ep: 'c', ein: true, i: '.key' },
    F: { l: 2, vf: 'r', i: '.key' },
  };
  const L = {};
  for (const [n, q] of Object.entries(qs)) {
    L[n] = await open(rec, n);
    await L[n].req('q', lb('/k', q, 1));
  }
  await A.req('p', { p: '/k/bb', d: 22 }); // 1
  await A.req('p', { p: '/k/c', d: 33 }); // 2
  await A.req('p', { p: '/k/b', d: null }); // 3
  await A.req('p', { p: '/k/f', d: 6 }); // 4
  await A.req('p', { p: '/k/a0', d: 0 }); // 5
  await A.req('m', { p: '/k', d: { d: 44, e: null, c: { x: 1 } } }); // 6
  await A.req('p', { p: '/k/c', d: null }); // 7
  await L.E.req('n', ub('/k', qs.E, 1));
  rec.note('Queries (all tag 1, one connection each): ' + Object.entries(qs).map(([n, q]) => `${n} ${JSON.stringify(q)}`).join('; ') + '.');
  rec.note('Steps (A): 1 p /k/bb 22; 2 p /k/c 33; 3 p /k/b null; 4 p /k/f 6; 5 p /k/a0 0; 6 m /k {d:44,e:null,c:{x:1}}; 7 p /k/c null; E unlistens.');
  await rec.finish();
}

async function queryChildIndexed() {
  const rec = new Recorder(
    'query_child_indexed',
    'orderByChild("s") with .indexOn on /q and orderByValue with .indexOn ".value" on /v. B limitToFirst(2); C startAt(1).endAt(2); D startAt(1,"b").endAt(3,"c"); E startAfter(1); F equalTo(2); G endBefore(2); V orderByValue limitToLast(2) on /v. A moves children in and out.',
    'r3-qci',
  );
  const rules = { rules: { '.read': true, '.write': true, q: { '.indexOn': ['s'] }, v: { '.indexOn': '.value' } } };
  await prep(rec, { rules, data: { q: { a: { s: 3 }, b: { s: 1 }, c: { s: 2 }, d: { s: 0 } }, v: { a: 3, b: 1, c: 2, d: 0 } } });
  const A = await open(rec, 'A');
  const qs = {
    B: { l: 2, vf: 'l', i: 's' },
    C: { sp: 1, sin: true, ep: 2, ein: true, i: 's' },
    D: { sp: 1, sn: 'b', sin: true, ep: 3, en: 'c', ein: true, i: 's' },
    E: { sp: 1, sn: '[MAX_NAME]', sin: false, i: 's' },
    F: { sp: 2, sin: true, ep: 2, ein: true, i: 's' },
    G: { ep: 2, en: '[MIN_NAME]', ein: false, i: 's' },
  };
  const L = {};
  for (const [n, q] of Object.entries(qs)) {
    L[n] = await open(rec, n);
    await L[n].req('q', lb('/q', q, 1));
  }
  const vq = { l: 2, vf: 'r', i: '.value' };
  const V = await open(rec, 'V');
  await V.req('q', lb('/v', vq, 1));
  await A.req('p', { p: '/q/e', d: { s: -1 } }); // 1 enters B's window, evicts b
  await A.req('p', { p: '/q/d/s', d: 10 }); // 2 window child of B moves out (deep write)
  await A.req('p', { p: '/q/a/s', d: -5 }); // 3 non-window child moves in (deep write)
  await A.req('p', { p: '/q/e', d: null }); // 4 delete window child
  await A.req('p', { p: '/q/f', d: { t: 1 } }); // 5 child without s (sorts first)
  await A.req('p', { p: '/q/f/t', d: 2 }); // 6 change inside window, sort value unchanged
  await A.req('p', { p: '/q/c/s', d: 2.5 }); // 7 change outside B's window
  await A.req('p', { p: '/q/b', d: { s: 2 } }); // 8 whole-child replace into F's equalTo
  await A.req('m', { p: '/q', d: { 'c/s': 1, 'g/s': -10 } }); // 9 merge with deep paths
  await A.req('p', { p: '/v/e', d: 10 }); // 10 enters V
  await A.req('p', { p: '/v/a', d: -1 }); // 11 window? a=3 was in V (last 2: a=3, c=2) -> moves out
  await A.req('p', { p: '/v/e', d: null }); // 12 delete V window child
  rec.note('Queries (tag 1, one connection each, all on /q): ' + Object.entries(qs).map(([n, q]) => `${n} ${JSON.stringify(q)}`).join('; ') + `; V on /v ${JSON.stringify(vq)}.`);
  rec.note('Steps (A): 1 p /q/e {s:-1}; 2 p /q/d/s 10; 3 p /q/a/s -5; 4 p /q/e null; 5 p /q/f {t:1}; 6 p /q/f/t 2; 7 p /q/c/s 2.5; 8 p /q/b {s:2}; 9 m /q {"c/s":1,"g/s":-10}; 10 p /v/e 10; 11 p /v/a -1; 12 p /v/e null.');
  rec.note('Initial data: /q = {a:{s:3},b:{s:1},c:{s:2},d:{s:0}}, /v = {a:3,b:1,c:2,d:0}.');
  await rec.finish();
}

async function queryChildUnindexed() {
  const rec = new Recorder(
    'query_child_unindexed',
    'orderByChild("s") limitToFirst(2) and orderByValue listens without .indexOn, writes afterwards, unlisten, and g with the same and with key/priority queries.',
    'r3-qcu',
  );
  await prep(rec, { data: { q: { a: { s: 3 }, b: { s: 1 }, c: { s: 2 }, d: { s: 0 } }, v: { a: 3, b: 1 } } });
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  const C = await open(rec, 'C');
  const cq = { l: 2, vf: 'l', i: 's' };
  const rq = { sp: 1, sin: true, i: 's' };
  const vq = { l: 1, vf: 'r', i: '.value' };
  await B.req('q', lb('/q', cq, 1));
  await C.req('q', lb('/q', rq, 1));
  await C.req('q', lb('/v', vq, 2));
  await A.req('p', { p: '/q/e', d: { s: -1 } });
  await A.req('p', { p: '/q/a/s', d: -5 });
  await A.req('p', { p: '/q/e', d: null });
  await A.req('m', { p: '/q', d: { 'c/s': 9, f: { s: 4 } } });
  await A.req('p', { p: '/v/c', d: 0 });
  await B.req('g', { p: '/q', q: cq });
  await B.req('g', { p: '/q', q: { i: 's' } });
  await B.req('g', { p: '/q', q: { l: 2, vf: 'l', i: '.key' } });
  await B.req('g', { p: '/q', q: { l: 2, vf: 'r' } });
  await B.req('g', { p: '/v', q: vq });
  await B.req('n', ub('/q', cq, 1));
  await A.req('p', { p: '/q/z', d: { s: 0 } });
  rec.note(`B listens /q ${JSON.stringify(cq)} t 1; C listens /q ${JSON.stringify(rq)} t 1 and /v ${JSON.stringify(vq)} t 2. No .indexOn anywhere.`);
  rec.note('Steps (A): p /q/e {s:-1}; p /q/a/s -5; p /q/e null; m /q {"c/s":9,f:{s:4}}; p /v/c 0. B gets: same child query, orderByChild only, key limitToFirst(2), priority limitToLast(2), orderByValue on /v. B unlistens; A p /q/z {s:0}.');
  await rec.finish();
}

async function queryTwoListens() {
  const rec = new Recorder(
    'query_two_listens',
    'One connection B listens on /q untagged (default) and with tagged queries (limitToLast(2) t1, limitToFirst(1) t2, orderByKey startAt(b) t3); writes; tagged unlisten; untagged unlisten. Connection C listens the same query twice with tags 4 and 5.',
    'r3-q2',
  );
  await prep(rec, { data: { q: { a: 1, b: 2, c: 3 } } });
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  const q1 = { l: 2, vf: 'r' };
  const q2 = { l: 1, vf: 'l' };
  const q3 = { sp: 'b', sin: true, i: '.key' };
  await B.req('q', lb('/q'));
  await B.req('q', lb('/q', q1, 1));
  await B.req('q', lb('/q', q2, 2));
  await B.req('q', lb('/q', q3, 3));
  await A.req('p', { p: '/q/d', d: 4 }); // 1
  await A.req('p', { p: '/q/a', d: 10 }); // 2
  await A.req('m', { p: '/q', d: { b: 20, e: 5 } }); // 3
  await B.req('n', ub('/q', q1, 1));
  await A.req('p', { p: '/q/f', d: 6 }); // 4
  await B.req('n', ub('/q'));
  await A.req('p', { p: '/q/a0', d: 0 }); // 5
  const C = await open(rec, 'C');
  await C.req('q', lb('/q', q1, 4));
  await C.req('q', lb('/q', q1, 5));
  await A.req('p', { p: '/q/zz', d: 7 }); // 6
  await C.req('n', ub('/q', q1, 4));
  await A.req('p', { p: '/q/zzz', d: 8 }); // 7
  rec.note(`B: q /q untagged; q /q ${JSON.stringify(q1)} t1; ${JSON.stringify(q2)} t2; ${JSON.stringify(q3)} t3.`);
  rec.note('Steps (A): 1 p /q/d 4; 2 p /q/a 10; 3 m /q {b:20,e:5}; B n t1; 4 p /q/f 6; B n untagged; 5 p /q/a0 0; C listens q1 with t4 then t5; 6 p /q/zz 7; C n t4; 7 p /q/zzz 8.');
  await rec.finish();
}

async function queryGet() {
  const rec = new Recorder(
    'query_get',
    'g (get) with query objects: default-order limits, key ranges, indexed child order (/q has .indexOn s), unindexed child and value order (/u), equalTo, orderBy without limit, a missing path, a leaf.',
    'r3-qget',
  );
  const rules = { rules: { '.read': true, '.write': true, q: { '.indexOn': ['s'] } } };
  const kids = { a: { s: 3 }, b: { s: 1 }, c: { s: 2 }, d: { s: 0 } };
  await prep(rec, { rules, data: { q: kids, u: kids, leaf: 'x' } });
  const A = await open(rec, 'A');
  const gets = [
    ['/q', { l: 2, vf: 'l' }],
    ['/q', { l: 2, vf: 'r' }],
    ['/q', { sp: 'b', sin: true, ep: 'c', ein: true, i: '.key' }],
    ['/q', { sp: 'b', sin: false, i: '.key' }],
    ['/q', { ep: 'c', ein: false, l: 1, vf: 'r', i: '.key' }],
    ['/q', { l: 2, vf: 'l', i: 's' }],
    ['/q', { sp: 1, sin: true, ep: 2, ein: true, i: 's' }],
    ['/q', { sp: 2, sin: true, ep: 2, ein: true, i: 's' }],
    ['/q', { sp: 1, sn: '[MAX_NAME]', sin: false, i: 's' }],
    ['/q', { i: 's' }],
    ['/u', { l: 2, vf: 'l', i: 's' }],
    ['/u', { i: 's' }],
    ['/u', { l: 1, vf: 'l', i: '.value' }],
    ['/u', { l: 2, vf: 'l', i: '.key' }],
    ['/missing', { l: 1, vf: 'l' }],
    ['/missing', { l: 1, vf: 'l', i: 's' }],
    ['/leaf', { l: 1, vf: 'l' }],
    ['/leaf', { sp: 'z', sin: true, i: '.key' }],
    ['/q', {}],
  ];
  for (const [p, q] of gets) await A.req('g', { p, q }, { settle: 100 });
  rec.note('Data: /q and /u = {a:{s:3},b:{s:1},c:{s:2},d:{s:0}} (.indexOn s only on /q), /leaf = "x".');
  await rec.finish();
}

async function revokeUnauth() {
  const rec = new Recorder(
    'revoke_unauth',
    'A authenticates with an unsigned JWT for u1, listens /users/u1 (untagged and tagged) and /open, then unauth: c pushes vs the unauth reply. Then listens again unauthenticated (denied), re-auths and listens again. W (another authed client) writes to /users/u1 throughout.',
    'r3-revoke',
  );
  const rules = {
    rules: {
      users: { $uid: { '.read': 'auth != null && auth.uid === $uid', '.write': 'auth != null' } },
      open: { '.read': true },
    },
  };
  await prep(rec, { rules, data: { users: { u1: { name: 'one' } }, open: { x: 1 } } });
  const A = await open(rec, 'A');
  const W = await open(rec, 'W');
  await W.req('auth', { cred: unsignedJwt('w9') });
  const tok = unsignedJwt('u1');
  rec.doc.jwt = tok;
  await A.req('auth', { cred: tok });
  const tq = { l: 1, vf: 'l' };
  await A.req('q', lb('/users/u1'));
  await A.req('q', lb('/users/u1', tq, 1));
  await A.req('q', lb('/open'));
  await W.req('p', { p: '/users/u1/name', d: 'two' });
  await A.req('unauth', {});
  await W.req('p', { p: '/users/u1/name', d: 'three' });
  await A.req('q', lb('/users/u1'));
  await A.req('g', { p: '/users/u1', q: {} });
  await A.req('auth', { cred: tok });
  await A.req('q', lb('/users/u1'));
  await W.req('p', { p: '/users/u1/name', d: 'four' });
  rec.note('jwt = header {"alg":"none","typ":"JWT"}, payload {uid,sub,user_id = "u1", iat, exp = iat+3600}, empty signature. W authenticates as w9 (may write any /users/$uid).');
  rec.note('Steps: A auth u1; A q /users/u1; A q /users/u1 {l:1,vf:"l"} t1; A q /open; W p /users/u1/name "two"; A unauth; W p "three"; A q /users/u1 (unauthenticated); A g /users/u1; A auth u1 again; A q /users/u1; W p "four".');
  await rec.finish();
}

async function listenErrors() {
  const rec = new Recorder(
    'listen_errors',
    'Listen/unlisten edge cases: forbidden chars in the path, invalid query objects, tag without query, query without tag, unlisten of listens that do not exist, duplicate identical listens (untagged and tagged) and what a write then pushes, unlisten once after a duplicate listen.',
    'r3-lerr',
  );
  await prep(rec, { data: { dup: { a: 1, b: 2 }, x: { y: 1 } } });
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  const steps = [
    ['q', lb('/bad.path')],
    ['q', lb('/bad#x')],
    ['q', lb('/x', { l: 'abc', vf: 'l' }, 1)],
    ['q', lb('/x', { l: 1, vf: 'z' }, 2)],
    ['q', lb('/x', { l: -1, vf: 'l' }, 3)],
    ['q', lb('/x', { i: '$bad' }, 4)],
    ['q', lb('/x', { sp: 1, sin: 'yes', i: '.key' }, 5)],
    ['q', { p: '/x', q: {}, t: 8, h: '' }],
    ['n', ub('/nolisten')],
    ['n', ub('/x', { l: 1, vf: 'l' }, 9)],
    ['n', ub('/bad.path')],
  ];
  for (const [a, b] of steps) {
    await B.req(a, b);
  }
  // duplicates on /dup
  await B.req('q', lb('/dup'));
  await B.req('q', lb('/dup'));
  await B.req('q', lb('/dup', { l: 1, vf: 'l' }, 11));
  await B.req('q', lb('/dup', { l: 1, vf: 'l' }, 11));
  await A.req('p', { p: '/dup/0', d: 0 });
  await B.req('n', ub('/dup'));
  await A.req('p', { p: '/dup/00', d: 0 });
  await B.req('n', ub('/dup', { l: 1, vf: 'l' }, 11));
  await A.req('p', { p: '/dup/0', d: 1 });
  await B.req('n', ub('/dup'));
  await B.req('n', ub('/dup', { l: 1, vf: 'l' }, 11));
  await A.req('p', { p: '/dup/0', d: 2 });
  // tag without query, then query without tag (the latter resets the namespace), each on a fresh socket
  const C = await open(rec, 'C');
  await C.req('q', { p: '/x', t: 7, h: '' });
  await A.req('p', { p: '/x/y', d: 2 });
  const D = await open(rec, 'D');
  await D.req('q', { p: '/x', q: { l: 1, vf: 'l' }, h: '' });
  await sleep(1000);
  for (let i = 1; i <= 8; i++) {
    const E = new WsConn(rec, 'E' + i, rec.ns);
    const hello = await Promise.race([E.hello.then((h) => h != null), sleep(3000).then(() => false)]);
    if (hello && !E.isClosed) {
      await E.req('g', { p: '/dup', q: {} });
      rec.doc.recoveredAfterAttempts = i;
      break;
    }
    await E.close();
    await sleep(2000);
  }
  rec.note('B sends, in order: q /bad.path; q /bad#x; q /x {l:"abc",vf:"l"} t1; {l:1,vf:"z"} t2; {l:-1,vf:"l"} t3; {i:"$bad"} t4; {sp:1,sin:"yes",i:".key"} t5; q /x with q:{} and t 8; n /nolisten; n /x tagged t9 never listened; n /bad.path.');
  rec.note('Duplicates: B q /dup twice (untagged), q /dup {l:1,vf:"l"} t11 twice; A p /dup/0 0 (sorts first, enters t11 window); B n /dup once; A p /dup/00 0; B n t11 once; A p /dup/0 1 (a change inside the t11 window); B n /dup again; B n t11 again; A p /dup/0 2.');
  rec.note('Then C (fresh socket): q /x with t:7 but no q; A p /x/y 2. D (fresh socket): q /x with q {l:1,vf:"l"} but no t. E1, E2, ... (fresh sockets, 2 s apart, until one stays open): g /dup.');
  await rec.finish();
}

// ---------------------------------------------------------------- long-poll scenarios

async function lpHandshake() {
  const rec = new Recorder(
    'lp_handshake',
    'Long-poll handshake as the JS SDK builds it (start, ser, cb, v, ns), the disconnect-frame page (dframe), a start without cb, and a start with an unknown protocol version.',
    'r3-lp-handshake',
  );
  await prep(rec);
  const A = new LpClient(rec, 'A', { cb: 1 });
  await A.start();
  await httpGet(rec, 'A', A.url({ dframe: 't', id: A.id, pw: A.pw }));
  const B = new LpClient(rec, 'B', { cb: 2 });
  await httpGet(rec, 'B', `/.lp?start=t&ser=${B.startSer}&v=5&ns=${rec.ns}`);
  await httpGet(rec, 'B', `/.lp?start=t&ser=${B.startSer}&cb=3&v=4&ns=${rec.ns}`, { timeout: 10000 });
  await httpGet(rec, 'B', `/.lp?start=t&ser=${B.startSer}&cb=4&v=5&ls=${A.helloMsg ? A.helloMsg.s : 'x'}&p=1:123:web:abc&ns=${rec.ns}`);
  rec.note('A: SDK start URL: start=t, ser=<random int>, cb=<callback id>, v=5, then ns (BrowserPollConnection.ts:206-235, RepoInfo.ts:118-149). Then the dframe URL the SDK puts in a hidden iframe after the handshake (dframe=t&id&pw&ns, BrowserPollConnection.ts:376-389).');
  rec.note('B: start without cb; start with v=4 and cb=3 (the client gives up after 10 s, recorded as http_abort; an earlier run waited 300 s without any answer); start with ls=<A hello s> and p=<app id> and cb=4.');
  await rec.finish();
}

async function lpPoll() {
  const rec = new Recorder(
    'lp_poll',
    'A pending idle poll held by the server until it answers on its own (no client keepalive); then a listen sent over LP, an outstanding poll answered with a data push when WebSocket client B writes, and how a newer request releases an older pending poll.',
    'r3-lp-poll',
  );
  await prep(rec, { data: { w: { v: 1 } } });
  const A = new LpClient(rec, 'A', { cb: 1 });
  await A.start();
  const poll = (c, ser) => c.get(c.url({ id: c.id, pw: c.pw, ser: ser ?? ++c.currentSerial }));
  // 1) SDK-style polling: listen over LP, B writes over WebSocket, then 27 s idle (keepalive re-poll at 25 s)
  A.begin();
  await sleep(300);
  const B = await open(rec, 'B');
  await A.req('q', lb('/w'));
  await sleep(300);
  await B.req('p', { p: '/w/v', d: 2 });
  await sleep(300);
  await B.req('m', { p: '/w', d: { v: 3, n: 1 } });
  await sleep(27000);
  // 2) manual polls: p1, 1 s later p2 (higher ser), then B writes
  A.alive = false;
  A.keepalive = false;
  const p1 = poll(A);
  await sleep(1000);
  const p2 = poll(A);
  await sleep(1000);
  await B.req('p', { p: '/w/v', d: 4 });
  await Promise.all([p1, p2]);
  // 3) out-of-order serials: the higher serial first, then a lower one, then B writes
  const s1 = ++A.currentSerial;
  const s2 = ++A.currentSerial;
  const p3 = poll(A, s2);
  await sleep(500);
  const p4 = poll(A, s1);
  await sleep(1000);
  await B.req('p', { p: '/w/v', d: 5 });
  await Promise.race([Promise.all([p3, p4]), sleep(5000)]);
  // 4) a single idle poll with nothing else: measure how long the server holds it
  await poll(A); // collects what is still queued from step 3 (the push of /w/v 5)
  await sleep(500);
  let t = Date.now();
  await poll(A);
  rec.doc.idleHoldMs = Date.now() - t;
  await sleep(500);
  await poll(A); // session state afterwards
  // 5) session A2: poll q1 at t0, poll q2 at t0+60 s (releases q1); when is q2 answered?
  const A2 = new LpClient(rec, 'A2', { cb: 2, keepalive: false });
  await A2.start();
  t = Date.now();
  const q1 = poll(A2);
  await sleep(60000);
  const t2 = Date.now();
  await Promise.all([q1, poll(A2)]);
  rec.doc.secondPollHoldMs = Date.now() - t2;
  rec.doc.sessionLifeMs = Date.now() - t;
  rec.note('Step 1: SDK-style polling (one outstanding poll; a second request while data is queued; a new poll after 25 s keepalive), A listens /w over LP, B writes /w/v 2 and merges /w over WebSocket, then 27 s idle.');
  rec.note('Step 2: automatic polling stopped; poll p1, 1 s later poll p2 (higher ser), then B writes /w/v 4.');
  rec.note('Step 3: poll with ser N+1 sent first, then ser N; then B writes /w/v 5.');
  rec.note(`Step 4: one poll that collects what step 3 left queued, then one idle poll and nothing else: answered by the server after ${rec.doc.idleHoldMs} ms (single run); then one more poll on the same session.`);
  rec.note(`Step 5: session A2: poll at t0, second poll at t0+60 s; the second poll was answered after ${rec.doc.secondPollHoldMs} ms (t0 + ${rec.doc.sessionLifeMs} ms).`);
  await rec.finish();
}

async function lpSend() {
  const rec = new Recorder(
    'lp_send',
    'Client-to-server messages over long polling with the SDK segment encoding (web-safe base64 of the JSON, 1840-char segments, segN/tsN/dN params): stats, listen, put, a put needing several segments, a control ping, and a request batching several small messages in one URL.',
    'r3-lp-send',
  );
  await prep(rec, { data: { s: { a: 1 } } });
  const A = new LpClient(rec, 'A', { cb: 1 });
  await A.start();
  A.begin();
  await sleep(300);
  await A.req('s', { c: { 'sdk.js.10-0-0': 1 } });
  await A.req('q', lb('/s'));
  await A.req('p', { p: '/s/b', d: 'x' });
  const big = 'é'.repeat(800) + 'y'.repeat(3000);
  await A.req('p', { p: '/s/big', d: big }, { timeout: 4000 });
  A.send({ t: 'c', d: { t: 'p', d: {} } });
  await sleep(800);
  // several messages queued before the next request goes out: they share one URL
  A.alive = false;
  await sleep(1500);
  const r1 = ++A.rid;
  const r2 = ++A.rid;
  A.send({ t: 'd', d: { r: r1, a: 'p', b: { p: '/s/c', d: 1 } } });
  A.send({ t: 'd', d: { r: r2, a: 'g', b: { p: '/s/c', q: {} } } });
  A.alive = true;
  A.newRequest();
  await sleep(1500);
  await A.req('g', { p: '/s', q: {} });
  rec.note(`The big put is ${JSON.stringify({ t: 'd', d: { r: 4, a: 'p', b: { p: '/s/big', d: big } } }).length} chars of JSON (800 x U+00E9 + 3000 x "y"), sent as ${Math.ceil(b64ws(JSON.stringify({ t: 'd', d: { r: 4, a: 'p', b: { p: '/s/big', d: big } } })).length / MAX_PAYLOAD_SIZE)} segments.`);
  rec.note('The batched request carries two complete messages (seg0/ts0/d0 and seg1/ts1/d1) in one URL, as the SDK does when several small segments are queued.');
  await rec.finish();
}

async function lpDisconnect() {
  const rec = new Recorder(
    'lp_disconnect',
    'LP client A registers onDisconnect writes, then sends the disconn request that the dframe page fires on unload; WebSocket listener B shows when the onDisconnect writes run. Then a poll on the dead session, and a second LP session C that is abandoned (no disconn) to see if/when its onDisconnect fires.',
    'r3-lp-disconnect',
  );
  await prep(rec, { data: { od: { here: true } } });
  const B = await open(rec, 'B');
  await B.req('q', lb('/od'));
  const A = new LpClient(rec, 'A', { cb: 1 });
  await A.start();
  A.begin();
  await sleep(300);
  await A.req('o', { p: '/od/a', d: 'gone' });
  await A.req('om', { p: '/od', d: { m: { '.sv': 'timestamp' } } });
  const page = await httpGet(rec, 'A', A.url({ dframe: 't', id: A.id, pw: A.pw }));
  const m = page && page.text.match(/EnvSendPing\("([^"]+)"\)/);
  const disconnUrl = m ? m[1] : `/.lp?disconn=t&id=${A.id}&pw=${A.pw}`;
  await httpGet(rec, 'A', disconnUrl);
  await sleep(2000);
  await sleep(1000);
  await A.close();
  await httpGet(rec, 'A', A.url({ id: A.id, pw: A.pw, ser: ++A.currentSerial }));
  // abandoned session
  const C = new LpClient(rec, 'C', { cb: 2, keepalive: false });
  await C.start();
  C.begin();
  await sleep(300);
  await C.req('o', { p: '/od/c', d: 'abandoned' });
  const t = Date.now();
  C.alive = false;
  rec.doc.abandonWaitMs = null;
  for (let i = 0; i < 200; i++) {
    if (B.msgs.some((o) => o.t === 'd' && o.d && o.d.a && JSON.stringify(o.d.b).includes('abandoned'))) {
      rec.doc.abandonWaitMs = Date.now() - t;
      break;
    }
    if (i % 40 === 39) B.send('0');
    await sleep(1000);
  }
  await C.close();
  rec.note('The disconn URL is taken verbatim from the dframe page (EnvSendPing argument).');
  rec.note(`Abandoned session C: its last request completed, then no further requests; onDisconnect write seen by B after ${rec.doc.abandonWaitMs} ms (null = not within 200 s). B sends "0" keepalives meanwhile.`);
  await rec.finish();
}

async function lpUpgrade() {
  const rec = new Recorder(
    'lp_upgrade',
    'LP -> WebSocket upgrade as Connection.ts does it: LP handshake, listen over LP, open the secondary WebSocket with s=<hello s>, ping until 2 pongs, send a on the WebSocket and n on LP, then use the WebSocket. B writes during and after the switch.',
    'r3-lp-upgrade',
  );
  await prep(rec, { data: { u: { v: 1 } } });
  const A = new LpClient(rec, 'A', { cb: 1 });
  await A.start();
  A.begin();
  await sleep(300);
  const B = await open(rec, 'B');
  await A.req('q', lb('/u'));
  const s = A.helloMsg.s;
  // Connection.startUpgrade_: secondary transport with transportSessionId = hello.s
  const W = new WsConn(rec, 'A-ws', rec.ns, `&s=${s}`);
  let required = 2; // WebSocketConnection.responsesRequiredToBeHealthy
  let upgraded;
  const done = new Promise((r) => (upgraded = r));
  let rxSwitched = false;
  let flushed = 0;
  const pendingData = [];
  const proceed = async () => {
    W.send(JSON.stringify({ t: 'c', d: { t: 'a', d: {} } }));
    A.send({ t: 'c', d: { t: 'n', d: {} } });
    // a write by B immediately after the switch request
    B.send(JSON.stringify({ t: 'd', d: { r: ++B.rid, a: 'p', b: { p: '/u/v', d: 3 } } }));
    upgraded();
  };
  const healthy = () => {
    if (required <= 0) proceed();
    else W.send(JSON.stringify({ t: 'c', d: { t: 'p', d: {} } }));
  };
  W.onMsg = (o) => {
    if (o.t === 'c' && o.d) {
      if (o.d.t === 'a') healthy();
      else if (o.d.t === 'o') {
        required--;
        healthy();
      }
    } else if (o.t === 'd' && !rxSwitched) pendingData.push(o);
  };
  A.onMsg = (o) => {
    if (o.t === 'c' && o.d && o.d.t === 'n') {
      rxSwitched = true;
      flushed = pendingData.length;
    }
  };
  await Promise.race([W.opened, sleep(3000)]);
  // B writes while the secondary is being validated
  await B.req('p', { p: '/u/v', d: 2 });
  await Promise.race([done, sleep(5000)]);
  await Promise.race([A.closedByServer, sleep(4000)]);
  await sleep(1000);
  await B.req('p', { p: '/u/v', d: 4 });
  const r = ++W.rid;
  await W.sendAndWait(JSON.stringify({ t: 'd', d: { r, a: 'g', b: { p: '/u', q: {} } } }), r);
  const r2 = ++W.rid;
  await W.sendAndWait(JSON.stringify({ t: 'd', d: { r: r2, a: 'p', b: { p: '/u/w', d: 1 } } }), r2);
  // what the dead LP session answers now
  await httpGet(rec, 'A', A.url({ id: A.id, pw: A.pw, ser: ++A.currentSerial }));
  // secondary WebSocket with an unknown transport session id
  const X = new WsConn(rec, 'X', rec.ns, '&s=unknownsession0000000000000000');
  await Promise.race([X.closedP, sleep(3000)]);
  rec.doc.result = { rxSwitchedByServerN: rxSwitched, bufferedDataOnWsBeforeSwitch: flushed, lpServerClose: A.serverClose || null };
  rec.note('Client logic follows Connection.ts: on a or o from the secondary: if fewer than 2 pongs, send ping p; after the 2nd pong send a on the secondary and n on the primary (LP) and switch tx (Connection.ts:240-304). B writes /u/v 2 while the secondary is validated, /u/v 3 right after the switch request, /u/v 4 after the LP close.');
  rec.note('result.rxSwitchedByServerN = whether the server sent a control n on LP (Connection.ts:353-360 switches rx only then); lpServerClose = the pLPCommand close seen on LP.');
  rec.note('X: a WebSocket with s=<unknown session id>.');
  await rec.finish();
}

const all = {
  listen_default: listenDefault,
  query_limit_priority: queryLimitPriority,
  query_key: queryKey,
  query_child_indexed: queryChildIndexed,
  query_child_unindexed: queryChildUnindexed,
  query_two_listens: queryTwoListens,
  query_get: queryGet,
  revoke_unauth: revokeUnauth,
  listen_errors: listenErrors,
  lp_handshake: lpHandshake,
  lp_poll: lpPoll,
  lp_send: lpSend,
  lp_disconnect: lpDisconnect,
  lp_upgrade: lpUpgrade,
};
const only = process.env.SCENARIOS ? process.env.SCENARIOS.split(',') : Object.keys(all);
for (const name of only) await all[name]();
process.exit(0);
