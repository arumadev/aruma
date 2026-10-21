// capture_r4.mjs - records golden RTDB frames for phase R4 (security rules and user
// sign-in) from the official Firebase Realtime Database emulator.
//
// Usage:
//   java -jar firebase-database-emulator-v4.11.2.jar --host 127.0.0.1 --port 9400
//   node capture_r4.mjs [host:port] [outDir]
//   SCENARIOS=rules_denied,rules_change node capture_r4.mjs 127.0.0.1:9400
// host:port defaults to $RTDB_EMULATOR_HOST, then 127.0.0.1:9400.
// outDir defaults to the directory of this script.
//
// Requires Node >= 22 (global WebSocket and fetch). Same file format as capture_r3.mjs:
// every WebSocket text frame in both directions in arrival order, as the raw string. Users
// sign in with unsigned (alg:none) ID tokens, which the emulator accepts; the token's payload
// names the user (sub). A REST call the scenario makes between frames, such as a rules change,
// is recorded in place as {conn:"REST", dir:"rest", method, path, body, status, response},
// so that a replay can make it at the same point. Every scenario runs in its own namespace
// (r4-<name>), whose rules and data it resets first with the emulator owner token, recorded
// under "setup".

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HOST = process.argv[2] || process.env.RTDB_EMULATOR_HOST || '127.0.0.1:9400';
const OUT = process.argv[3] || path.dirname(fileURLToPath(import.meta.url));
const EMULATOR = 'firebase-database-emulator-v4.11.2';
const TIMEOUT = 1500; // wait for a reply matching r
const SETTLE = 500; // extra wait after each step so pushes to other sockets arrive

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
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
      script: 'capture_r4.mjs',
      namespace: ns,
      setup: [],
      connections: {},
      frames: [],
      notes: [],
    };
    this.ns = ns;
    this.conns = [];
  }
  note(s) {
    this.doc.notes.push(s);
  }
  async finish() {
    for (const c of this.conns) await c.close();
    const byClient = this.conns.filter((c) => c.clientClosed).map((c) => c.name);
    if (byClient.length)
      this.note(
        `WebSocket close initiated by the client (close frame with code 1000) on: ${byClient.join(', ')}; the code recorded is what the client saw in the emulator's closing reply (1005 = the reply carried no status code).`,
      );
    const file = path.join(OUT, `${this.doc.scenario}.json`);
    fs.writeFileSync(file, JSON.stringify(this.doc, null, 2) + '\n');
    console.log(`wrote ${file} (${this.doc.frames.length} frames)`);
  }
}

// One WebSocket connection: recording and request/response matching. Messages are never
// split into frames here (all are small).
class WsConn {
  constructor(rec, name) {
    this.rec = rec;
    this.name = name;
    this.rid = 0;
    this.waiters = new Map();
    this.isClosed = false;
    this.urlPath = `/.ws?v=5&ns=${rec.ns}`;
    rec.doc.connections[name] = this.urlPath;
    rec.conns.push(this);
    this.hello = new Promise((r) => (this._hello = r));
    this.closedP = new Promise((r) => (this._closedResolve = r));
    this.ws = new WebSocket(`ws://${HOST}${this.urlPath}`);
    this.ws.onmessage = (e) => this._recv(e.data);
    this.ws.onclose = (e) => this._closed(e.code, e.reason);
    this.ws.onerror = () => {};
  }
  _recv(text) {
    this.rec.doc.frames.push({ conn: this.name, dir: 'recv', text });
    this._hello();
    let o;
    try {
      o = JSON.parse(text);
    } catch {
      return;
    }
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
    this._closedResolve();
  }
  async req(a, b, { settle = SETTLE } = {}) {
    const r = ++this.rid;
    const text = JSON.stringify({ t: 'd', d: { r, a, b } });
    const p = new Promise((res) => {
      this.waiters.set(r, res);
      setTimeout(() => {
        if (this.waiters.has(r)) {
          this.waiters.delete(r);
          res(null);
        }
      }, TIMEOUT);
    });
    this.rec.doc.frames.push({ conn: this.name, dir: 'send', text });
    this.ws.send(text);
    const resp = await p;
    if (!resp) this.rec.note(`${this.name} r=${r}: no reply within ${TIMEOUT} ms`);
    await sleep(settle);
    return resp;
  }
  async close() {
    if (this.isClosed) return;
    this.clientClosed = true;
    this.ws.close(1000);
    await Promise.race([this.closedP, sleep(2000)]);
  }
}

async function open(rec, name) {
  const c = new WsConn(rec, name);
  await Promise.race([c.hello, sleep(3000)]);
  return c;
}

const OWNER = { Authorization: 'Bearer owner' };

async function rest(method, p, ns, body) {
  const sep = p.includes('?') ? '&' : '?';
  const url = `${p}${sep}ns=${ns}`;
  const r = await fetch(`http://${HOST}${url}`, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
    headers: OWNER,
  });
  return { url, status: r.status, text: await r.text() };
}

async function prep(rec, rules, data) {
  const r1 = await rest('PUT', '/.settings/rules.json', rec.ns, rules);
  rec.doc.setup.push({ method: 'PUT', path: r1.url, body: rules, status: r1.status, response: r1.text });
  const r2 = await rest('PUT', '/.json', rec.ns, data);
  rec.doc.setup.push({ method: 'PUT', path: r2.url, body: data, status: r2.status });
}

// midway makes a REST call between frames and records it where it happened.
async function midway(rec, method, p, body) {
  const r = await rest(method, p, rec.ns, body);
  const f = { conn: 'REST', dir: 'rest', method, path: r.url, status: r.status, response: r.text };
  if (body !== undefined) f.body = body;
  rec.doc.frames.push(f);
  await sleep(SETTLE);
  return r;
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

const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
// token is an unsigned ID token for uid, with extra claims.
function token(uid, extra = {}) {
  const now = Math.floor(Date.now() / 1000);
  return `${b64url({ alg: 'none', typ: 'JWT' })}.${b64url({ sub: uid, user_id: uid, iat: now, exp: now + 3600, ...extra })}.`;
}

const USERS = {
  users: {
    $uid: {
      '.read': 'auth != null && auth.uid == $uid',
      '.write': 'auth != null && auth.uid == $uid',
      age: { '.validate': 'newData.isNumber()' },
    },
  },
  open: { '.read': true, '.write': true },
};

// ---------------------------------------------------------------- scenarios

async function authUser() {
  const rec = new Recorder(
    'auth_user',
    "auth with unsigned ID tokens of each sign-in provider: the reply's auth (uid, token, provider, user_id) and expires. Then tokens the emulator refuses, and a read after each.",
    'r4-auth-user',
  );
  await prep(rec, { rules: { '.read': 'auth != null', '.write': false } }, { x: 1 });
  const A = await open(rec, 'A');
  for (const p of ['password', 'google.com', 'github.com', 'custom', 'phone', 'anonymous']) {
    await A.req('auth', { cred: token('u1', { firebase: { sign_in_provider: p } }) }, { settle: 50 });
  }
  const now = Math.floor(Date.now() / 1000);
  await A.req('auth', { cred: `${b64url({ alg: 'none', typ: 'JWT' })}.${b64url({ sub: 'u1', iat: now, exp: now + 3600 })}.` }, { settle: 50 });
  await A.req('auth', { cred: `${b64url({ alg: 'none', typ: 'JWT' })}.${b64url({ iat: now, exp: now + 3600 })}.` }, { settle: 50 });
  await A.req('g', { p: '/x', q: {} }, { settle: 50 });
  await A.req('auth', { cred: 'garbage' }, { settle: 50 });
  await A.req('g', { p: '/x', q: {} }, { settle: 50 });
  await A.req('unauth', {}, { settle: 50 });
  await A.req('g', { p: '/x', q: {} }, { settle: 50 });
  rec.note('Providers, in order: password, google.com, github.com, custom, phone, anonymous (firebase.sign_in_provider); then a token without user_id or firebase claims; one without sub; g /x; "garbage"; g /x; unauth; g /x.');
  rec.note('The emulator keeps the previous sign-in when a token is refused (g /x after "garbage" is ok); Aruma signs the session out, as the SDKs close the connection on any refusal anyway (PersistentConnection.onAuthRevoked_).');
  await rec.finish();
}

async function rulesDenied() {
  const rec = new Recorder(
    'rules_denied',
    'permission_denied on every operation: unauthenticated q (untagged and tagged), g, p, m, o, om, and oc (allowed). Then signed in as u1: m and om reaching u2, p, m and o failing .validate, a p that passes, and its own listen and get.',
    'r4-denied',
  );
  await prep(rec, { rules: USERS }, { users: { u1: { name: 'one' }, u2: { name: 'two' } }, open: { x: 1 } });
  const A = await open(rec, 'A');
  await A.req('q', lb('/users/u1'));
  await A.req('q', lb('/users/u1', { l: 1, vf: 'l' }, 1));
  await A.req('g', { p: '/users/u1', q: {} });
  await A.req('p', { p: '/users/u1/name', d: 'x' });
  await A.req('m', { p: '/users/u1', d: { name: 'x' } });
  await A.req('o', { p: '/users/u1/name', d: 'x' });
  await A.req('om', { p: '/users/u1', d: { name: 'x' } });
  await A.req('oc', { p: '/users/u1' });
  await A.req('auth', { cred: token('u1') });
  await A.req('m', { p: '/users', d: { 'u1/name': 'a', 'u2/name': 'b' } });
  await A.req('om', { p: '/users', d: { 'u1/name': 'a', 'u2/name': 'b' } });
  await A.req('p', { p: '/users/u1/age', d: 'old' });
  await A.req('m', { p: '/users/u1', d: { age: 'old' } });
  await A.req('o', { p: '/users/u1/age', d: 'old' });
  await A.req('p', { p: '/users/u1/age', d: 5 });
  await A.req('q', lb('/users/u1'));
  await A.req('g', { p: '/users/u2', q: {} });
  rec.note('Rules: users/$uid .read and .write auth.uid == $uid, users/$uid/age .validate newData.isNumber(); open is open.');
  await rec.finish();
}

async function authChange() {
  const rec = new Recorder(
    'auth_change',
    'A signs in as u1 and listens /users/u1 and /open; it then signs in as u2 without unauth: the c push for /users/u1 comes before the reply. W, signed in as u1, writes to both.',
    'r4-auth-change',
  );
  await prep(rec, { rules: USERS }, { users: { u1: { name: 'one' } }, open: { x: 1 } });
  const A = await open(rec, 'A');
  const W = await open(rec, 'W');
  await W.req('auth', { cred: token('u1') });
  await A.req('auth', { cred: token('u1') });
  await A.req('q', lb('/users/u1'));
  await A.req('q', lb('/open'));
  await A.req('auth', { cred: token('u2') });
  await W.req('p', { p: '/users/u1/name', d: 'uno' });
  await W.req('p', { p: '/open/x', d: 2 });
  await A.req('g', { p: '/users/u1', q: {} });
  await rec.finish();
}

async function rulesChange() {
  const rec = new Recorder(
    'rules_change',
    'B, signed in as u1, listens /users/u1 (untagged and tagged) and /open; the rules then change (PUT /.settings/rules.json) to deny reading users: both listens on /users/u1 are cancelled with c, /open stays. Then data change through REST.',
    'r4-rules-change',
  );
  await prep(rec, { rules: USERS }, { users: { u1: { name: 'one' } }, open: { x: 1 } });
  const B = await open(rec, 'B');
  await B.req('auth', { cred: token('u1') });
  await B.req('q', lb('/users/u1'));
  await B.req('q', lb('/users/u1', { l: 1, vf: 'l' }, 3));
  await B.req('q', lb('/open'));
  await midway(rec, 'PUT', '/.settings/rules.json', {
    rules: { users: { $uid: { '.read': false, '.write': 'auth != null' } }, open: { '.read': true, '.write': true } },
  });
  await midway(rec, 'PUT', '/open/x.json', 2);
  await midway(rec, 'PUT', '/users/u1/name.json', 'after');
  await B.req('q', lb('/users/u1'));
  await rec.finish();
}

async function onDisconnectRules() {
  const rec = new Recorder(
    'ondisconnect_rules',
    'onDisconnect writes are checked again when they run. A (u1) registers o /users/u1/a and o /open/a, the rules then deny writing users, A closes: only /open/a is written. C (u1) registers o /users/u1/c, signs out, closes: nothing. E (u1) registers om / {users/u1/e, open/e} and o /users/u1/ts (a timestamp), the rules then make users/$uid/e fail .validate, E closes: the other two are written. R reads after each.',
    'r4-od-rules',
  );
  const allow = {
    rules: {
      '.read': true,
      users: { $uid: { '.write': 'auth != null && auth.uid == $uid', ts: { '.validate': 'newData.isNumber()' } } },
      open: { '.write': true },
    },
  };
  await prep(rec, allow, { users: { u1: { name: 'one' } }, open: { x: 1 } });
  const R = await open(rec, 'R');

  const A = await open(rec, 'A');
  await A.req('auth', { cred: token('u1') });
  await A.req('o', { p: '/users/u1/a', d: 'A-set' });
  await A.req('o', { p: '/open/a', d: 'A-open' });
  await midway(rec, 'PUT', '/.settings/rules.json', {
    rules: { '.read': true, users: { $uid: { '.write': false } }, open: { '.write': true } },
  });
  await A.close();
  await sleep(SETTLE);
  await R.req('g', { p: '/', q: {} });

  await midway(rec, 'PUT', '/.settings/rules.json', allow);
  const C = await open(rec, 'C');
  await C.req('auth', { cred: token('u1') });
  await C.req('o', { p: '/users/u1/c', d: 'C-set' });
  await C.req('unauth', {});
  await C.close();
  await sleep(SETTLE);
  await R.req('g', { p: '/', q: {} });

  const E = await open(rec, 'E');
  await E.req('auth', { cred: token('u1') });
  await E.req('om', { p: '/', d: { 'users/u1/e': 'E-merge', 'open/e': 'E-open' } });
  await E.req('o', { p: '/users/u1/ts', d: { '.sv': 'timestamp' } });
  await midway(rec, 'PUT', '/.settings/rules.json', {
    rules: {
      '.read': true,
      users: { $uid: { '.write': 'auth != null', e: { '.validate': false }, ts: { '.validate': 'newData.isNumber()' } } },
      open: { '.write': true },
    },
  });
  await E.close();
  await sleep(SETTLE);
  await R.req('g', { p: '/', q: {} });
  rec.note('R is never signed in: the rules let everyone read. The timestamp in the last read differs on every run.');
  await rec.finish();
}

const all = {
  auth_user: authUser,
  rules_denied: rulesDenied,
  auth_change: authChange,
  rules_change: rulesChange,
  ondisconnect_rules: onDisconnectRules,
};
const only = process.env.SCENARIOS ? process.env.SCENARIOS.split(',') : Object.keys(all);
for (const name of only) await all[name]();
process.exit(0);
