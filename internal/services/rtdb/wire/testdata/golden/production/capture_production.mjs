// capture_production.mjs - records the same RTDB scenarios from production Firebase, the
// emulator 4.11.2 and Aruma, so that the three can be compared (README.md in this directory).
//
// Usage:
//   node capture_production.mjs <config.json> <outDir> [scenario,...]
//
// config.json:
//   target     "production" | "emulator" | "aruma"
//   host       host[:port] of the database (production: <ns>.<region>.firebasedatabase.app)
//   tls        true for wss/https
//   ns         namespace; sent as ?ns= on every WebSocket and long-poll URL, as the JS SDK does,
//              and on REST URLs when restNs is true (the emulator and Aruma's main host)
//   restNs     see ns
//   run        run id; every write goes under /aruma-ref/<run>/
//   tokenFile  JSON file {idToken, uid} of a signed-in user (production and Aruma); for the
//              emulator, leave it out: the script signs unsigned ID tokens, which it accepts
//   expired    optional JSON file {idToken} of an ID token that has expired
//   rulesAuth  optional Authorization header value the script uses to install the rules itself
//              (emulator, Aruma); production's rules are deployed beforehand
//   redact     {"text": "replacement"} applied to everything recorded (project id, uid, email)
//
// Requires Node >= 22 (global WebSocket and fetch). Tokens are replaced by <id-token> before
// anything is recorded; nothing secret is printed.
//
// The rules every target runs under (production: added beside the project's own rules):
//   "aruma-ref": {"$run": {"open": {".read": "auth != null && auth.uid === '<uid>'",
//                                   ".write": "auth != null && auth.uid === '<uid>'",
//                                   "v": {".validate": "newData.isNumber()"}},
//                          "public": {".read": true}}}
// so the signed-in user reads and writes /aruma-ref/<run>/open and nothing else, and anyone
// reads /aruma-ref/<run>/public (which stays empty).

import fs from 'node:fs';
import path from 'node:path';

const cfg = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const OUT = process.argv[3];
const ONLY = (process.argv[4] || process.env.SCENARIOS || '').split(',').filter(Boolean);
const PROD = cfg.target === 'production';
const TIMEOUT = PROD ? 6000 : 2000;
const SETTLE = PROD ? 500 : 250;
const ROOT = `/aruma-ref/${cfg.run}`;
const OPEN = `${ROOT}/open`;
const CLOSED = `${ROOT}/closed`;
const PUBLIC = `${ROOT}/public`;
const WSURL = `${cfg.tls ? 'wss' : 'ws'}://${cfg.host}/.ws?v=5&ns=${cfg.ns}`;
const HTTP = `${cfg.tls ? 'https' : 'http'}://${cfg.host}`;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const today = () => new Date().toISOString().slice(0, 10);
const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');

// unsignedToken is an alg:none ID token: the emulator's way to sign in, and a forged token
// to the others.
function unsignedToken(uid, extra = {}) {
  const now = Math.floor(Date.now() / 1000);
  return `${b64url({ alg: 'none', typ: 'JWT' })}.${b64url({
    iss: 'https://securetoken.google.com/example-project',
    aud: 'example-project',
    auth_time: now,
    user_id: uid,
    sub: uid,
    iat: now,
    exp: now + 3600,
    email: 'someone@example.com',
    email_verified: false,
    firebase: { identities: { email: ['someone@example.com'] }, sign_in_provider: 'password' },
    ...extra,
  })}.`;
}

let USER;
if (cfg.tokenFile) USER = JSON.parse(fs.readFileSync(cfg.tokenFile, 'utf8'));
else USER = { uid: 'ref-user', idToken: unsignedToken('ref-user') };
const EXPIRED = cfg.expired ? JSON.parse(fs.readFileSync(cfg.expired, 'utf8')).idToken : null;

const SECRETS = [
  [USER.idToken, '<id-token>'],
  ...(EXPIRED ? [[EXPIRED, '<expired-id-token>']] : []),
  ...Object.entries(cfg.redact || {}),
];
function redact(s) {
  if (typeof s !== 'string') return s;
  for (const [from, to] of SECRETS) if (from) s = s.split(from).join(to);
  // A forged token carries the real uid in its payload.
  if (PROD) s = s.replace(/eyJhbGciOiJub25l[A-Za-z0-9_.-]*/g, '<alg-none-token-for-the-same-uid>');
  return s;
}

export const RULES = (uid) => ({
  'aruma-ref': {
    $run: {
      open: {
        '.read': `auth != null && auth.uid === '${uid}'`,
        '.write': `auth != null && auth.uid === '${uid}'`,
        v: { '.validate': 'newData.isNumber()' },
      },
      public: { '.read': true },
    },
  },
});

class Recorder {
  constructor(scenario, description) {
    this.doc = {
      scenario,
      description,
      source: cfg.target,
      ...(cfg.target === 'emulator' ? { emulator: 'firebase-database-emulator-v4.11.2' } : {}),
      ...(PROD ? { project: '<redacted>', host: redact(cfg.host) } : {}),
      captured: today(),
      script: 'capture_production.mjs',
      run: cfg.run,
      connections: {},
      frames: [],
      notes: [],
    };
    this.conns = [];
    this.t0 = Date.now();
  }
  push(f) {
    f.ms = Date.now() - this.t0;
    this.doc.frames.push(f);
  }
  note(s) {
    this.doc.notes.push(redact(s));
  }
  async finish() {
    for (const c of this.conns) await c.close();
    fs.mkdirSync(OUT, { recursive: true });
    const file = path.join(OUT, `${this.doc.scenario}.json`);
    fs.writeFileSync(file, redact(JSON.stringify(this.doc, null, 2)) + '\n');
    console.log(`wrote ${file} (${this.doc.frames.length} frames)`);
  }
}

// One raw WebSocket connection, recorded frame by frame.
class WsConn {
  constructor(rec, name, url = WSURL) {
    this.rec = rec;
    this.name = name;
    this.rid = 0;
    this.waiters = new Map();
    this.isClosed = false;
    rec.doc.connections[name] = redact(url.replace(/^wss?:\/\/[^/]+/, ''));
    rec.conns.push(this);
    this.hello = new Promise((r) => (this._hello = r));
    this.closedP = new Promise((r) => (this._closedResolve = r));
    this.ws = new WebSocket(url);
    this.ws.onmessage = (e) => this._recv(e.data);
    this.ws.onclose = (e) => this._closed(e.code, e.reason);
    this.ws.onerror = () => {};
  }
  _recv(text) {
    this.rec.push({ conn: this.name, dir: 'recv', text: redact(String(text)) });
    let o;
    try {
      o = JSON.parse(text);
    } catch {
      return;
    }
    if (o && o.t === 'c' && o.d && o.d.t === 'h') this._hello(o.d.d);
    if (o && o.t === 'd' && o.d && o.d.r != null && this.waiters.has(o.d.r)) {
      this.waiters.get(o.d.r)(o);
      this.waiters.delete(o.d.r);
    }
  }
  _closed(code, reason) {
    if (this.isClosed) return;
    this.isClosed = true;
    this.rec.push({ conn: this.name, dir: 'close', code, reason });
    for (const [, w] of this.waiters) w(null);
    this.waiters.clear();
    this._closedResolve();
  }
  raw(text) {
    this.rec.push({ conn: this.name, dir: 'send', text: redact(text) });
    this.ws.send(text);
  }
  async req(a, b, { settle = SETTLE, text } = {}) {
    if (this.isClosed) {
      this.rec.note(`${this.name}: ${a} not sent, the connection is closed`);
      return null;
    }
    const r = ++this.rid;
    const msg = text ? text(r) : JSON.stringify({ t: 'd', d: { r, a, b } });
    const p = new Promise((res) => {
      this.waiters.set(r, res);
      setTimeout(() => {
        if (this.waiters.has(r)) {
          this.waiters.delete(r);
          res(null);
        }
      }, TIMEOUT);
    });
    this.raw(msg);
    const resp = await p;
    if (!resp && !this.isClosed) this.rec.note(`${this.name} r=${r} (${a}): no reply within ${TIMEOUT} ms`);
    await sleep(settle);
    return resp;
  }
  async close() {
    if (this.isClosed) return;
    this.ws.close(1000);
    await Promise.race([this.closedP, sleep(3000)]);
  }
}

// open connects, and connects again when the connection fails before the hello (an
// unreliable network path to production); failed attempts are dropped from the record.
async function open(rec, name, url) {
  for (let attempt = 1; ; attempt++) {
    const c = new WsConn(rec, name, url);
    const n = rec.doc.frames.length;
    const ok = await Promise.race([c.hello.then(() => true), c.closedP.then(() => false), sleep(8000).then(() => false)]);
    if (ok || attempt === 8) return c;
    rec.doc.frames.splice(n);
    rec.conns.splice(rec.conns.indexOf(c), 1);
    try {
      c.ws.close();
    } catch {}
    rec.note(`${name}: connection attempt ${attempt} failed before the hello; connected again`);
    await sleep(1000);
  }
}

async function signedIn(rec, name) {
  const c = await open(rec, name);
  await c.req('auth', { cred: USER.idToken });
  return c;
}

const lb = (p, q, t) => (q ? { p, q, t, h: '' } : { p, h: '' });

// rest makes one REST call and records it. Every call carries the user's ID token as ?auth=,
// the documented way to authenticate REST with a user, unless auth is false.
async function rest(rec, method, p, { body, raw, headers = {}, auth = true, record = true, bodyNote } = {}) {
  const params = [];
  if (cfg.restNs) params.push(`ns=${cfg.ns}`);
  if (auth) params.push(`auth=${USER.idToken}`);
  const url = p + (params.length ? (p.includes('?') ? '&' : '?') + params.join('&') : '');
  const payload = raw !== undefined ? raw : body === undefined ? undefined : JSON.stringify(body);
  let r, text;
  for (let attempt = 1; ; attempt++) {
    try {
      r = await fetch(HTTP + url, { method, body: payload, headers });
      text = await r.text();
      break;
    } catch (e) {
      // A request that never got a response (the network path to production drops some)
      // is sent again.
      if (attempt < 5) {
        await sleep(1000);
        continue;
      }
      if (record) rec.push({ conn: 'REST', dir: 'rest', method, path: redact(url), error: String(e) });
      return { status: 0, text: '', headers: {} };
    }
  }
  const h = {};
  for (const [k, v] of r.headers) if (!['date', 'connection', 'strict-transport-security', 'server', 'keep-alive', 'vary'].includes(k)) h[k] = v;
  if (record) {
    const f = { conn: 'REST', dir: 'rest', method, path: redact(url) };
    if (Object.keys(headers).length) f.reqHeaders = headers;
    if (bodyNote) f.body = bodyNote;
    else if (payload !== undefined) f.body = redact(payload);
    f.status = r.status;
    f.headers = h;
    f.response = redact(text.length > 2000 ? text.slice(0, 2000) + `...(${text.length} chars)` : text);
    rec.push(f);
  }
  return { status: r.status, text, headers: h };
}

// ---------------------------------------------------------------- scenarios

const scenarios = {};

scenarios.handshake = async () => {
  const rec = new Recorder(
    'handshake',
    'Hello frame, the stats request the SDK sends first, a control ping, a keepalive "0" as the JS SDK sends every 45 s, then a second connection to the host the hello names, and one connection left idle for 75 s without keepalives.',
  );
  const A = await open(rec, 'A');
  const K = await open(rec, 'K');
  const hello = await A.hello;
  await A.req('s', { c: { 'sdk.js.11-0-0': 1 } });
  A.raw(JSON.stringify({ t: 'c', d: { t: 'p', d: {} } }));
  await sleep(1500);
  A.raw('0');
  await sleep(2000);
  if (hello && hello.h) {
    const url = `${cfg.tls ? 'wss' : 'ws'}://${hello.h}/.ws?v=5&ns=${cfg.ns}&ls=${hello.s}`;
    const B = await open(rec, 'B', url);
    await B.req('s', { c: { 'sdk.js.11-0-0': 1 } });
    rec.note('B connects to the host the hello named (d.h), with ls=<A session id>, as the SDK does on its next connection (PersistentConnection lastSessionId).');
  }
  await A.close();
  const start = Date.now();
  await Promise.race([K.closedP, sleep(75000)]);
  rec.note(`K stayed idle (no keepalive) for ${Math.round((Date.now() - start) / 1000)} s; ${K.isClosed ? 'the server closed it' : 'the server did not close it nor send anything'}.`);
  await rec.finish();
};

scenarios.auth = async () => {
  const rec = new Recorder(
    'auth',
    'auth with a real ID token (reply shape and expires), unauth, auth with "garbage", auth with an unsigned (alg:none) token, gauth with an ID token, and a read after each refusal on the same connection.',
  );
  const A = await open(rec, 'A');
  await A.req('auth', { cred: USER.idToken });
  await A.req('g', { p: OPEN, q: {} });
  await A.req('unauth', {});
  await A.req('g', { p: OPEN, q: {} });
  await A.req('auth', { cred: USER.idToken });
  await A.req('auth', { cred: 'garbage' });
  await A.req('g', { p: OPEN, q: {} });
  await sleep(1500);
  const B = await open(rec, 'B');
  await B.req('auth', { cred: USER.idToken });
  await B.req('auth', { cred: unsignedToken(USER.uid) });
  await B.req('g', { p: OPEN, q: {} });
  await sleep(1500);
  const C = await open(rec, 'C');
  await C.req('gauth', { cred: USER.idToken });
  await C.req('g', { p: OPEN, q: {} });
  await sleep(1500);
  rec.note('A: auth(user), g open, unauth, g open, auth(user), auth("garbage"), g open. B: auth(user), auth(alg:none token for the same uid), g open. C: gauth(user ID token), g open.');
  await rec.finish();
};

scenarios.auth_expired = async () => {
  if (!EXPIRED) return console.log('auth_expired: no expired token configured, skipped');
  const rec = new Recorder('auth_expired', 'auth with a real ID token whose exp has passed, a read, then auth with a fresh one.');
  const A = await open(rec, 'A');
  await A.req('auth', { cred: EXPIRED });
  await A.req('g', { p: OPEN, q: {} });
  await sleep(1500);
  const B = await open(rec, 'B');
  await B.req('auth', { cred: USER.idToken });
  await B.req('auth', { cred: EXPIRED });
  await B.req('g', { p: OPEN, q: {} });
  await sleep(1500);
  await rest(rec, 'GET', `${OPEN}.json?auth=${EXPIRED}`, { auth: false });
  await rest(rec, 'GET', `${PUBLIC}.json?auth=${EXPIRED}`, { auth: false });
  await rest(rec, 'GET', `${PUBLIC}.json`, { auth: false, headers: { Authorization: `Bearer ${EXPIRED}` } });
  await rec.finish();
};

scenarios.denied = async () => {
  const rec = new Recorder(
    'denied',
    'permission_denied texts, signed in, on a path the rules do not grant: q (untagged, tagged), g, g with a query, p, m, m reaching one granted and one refused child, o, om, oc; then .validate refusals on open/v (p, m, o).',
  );
  const A = await signedIn(rec, 'A');
  await A.req('q', lb(`${CLOSED}/x`));
  await A.req('q', lb(`${CLOSED}/x`, { l: 1, vf: 'l' }, 1));
  await A.req('g', { p: `${CLOSED}/x`, q: {} });
  await A.req('g', { p: `${CLOSED}/x`, q: { l: 1, vf: 'l' } });
  await A.req('p', { p: `${CLOSED}/x`, d: 1 });
  await A.req('m', { p: `${CLOSED}/x`, d: { a: 1 } });
  await A.req('m', { p: ROOT, d: { 'open/m1': 1, 'closed/m2': 2 } });
  await A.req('o', { p: `${CLOSED}/x`, d: 1 });
  await A.req('om', { p: `${CLOSED}/x`, d: { a: 1 } });
  await A.req('oc', { p: `${CLOSED}/x` });
  await A.req('p', { p: `${OPEN}/v`, d: 'not a number' });
  await A.req('m', { p: OPEN, d: { v: 'not a number' } });
  await A.req('o', { p: `${OPEN}/v`, d: 'not a number' });
  await A.req('g', { p: `${OPEN}/m1`, q: {} });
  const U = await open(rec, 'U');
  await U.req('q', lb(`${OPEN}/x`));
  await U.req('p', { p: `${OPEN}/x`, d: 1 });
  rec.note('U is not signed in.');
  await rec.finish();
};

scenarios.cancel = async () => {
  const rec = new Recorder(
    'cancel',
    'Listens the rules grant only to the signed-in user (untagged, a limit query, an untagged child, an index-only query, startAt+limit, endBefore by key, startAfter+endAt with names), then unauth: the c pushes (and the query object each carries) and the unauth reply, in arrival order. Then auth again, listen again, and auth with "garbage" while listening.',
  );
  const A = await signedIn(rec, 'A');
  await A.req('p', { p: `${OPEN}/c`, d: { a: { n: 1 }, b: { n: 2 } } });
  await A.req('q', lb(`${OPEN}/c`));
  await A.req('q', lb(`${OPEN}/c`, { l: 1, vf: 'l' }, 1));
  await A.req('q', lb(`${OPEN}/c/a`));
  await A.req('q', lb(`${OPEN}/c`, { i: 'n' }, 3));
  await A.req('q', lb(`${OPEN}/c`, { sp: 1, sin: true, l: 5, vf: 'l', i: 'n' }, 4));
  await A.req('q', lb(`${OPEN}/c`, { ep: 'b', ein: false, i: '.key' }, 5));
  await A.req('q', lb(`${OPEN}/c`, { sp: 'a', sn: 'a', sin: false, ep: 2, ein: true, i: 'n' }, 6));
  await A.req('unauth', {}, { settle: 2000 });
  await A.req('auth', { cred: USER.idToken });
  await A.req('q', lb(`${OPEN}/c`));
  await A.req('q', lb(`${OPEN}/c/b`, { l: 1, vf: 'r' }, 2));
  await A.req('auth', { cred: 'garbage' }, { settle: 2000 });
  await rec.finish();
};

scenarios.no_index = async () => {
  const rec = new Recorder(
    'no_index',
    'Queries on data without .indexOn: orderByChild and orderByValue listens (the reply warning), get with a query, and REST orderBy on a child, $value, $key and $priority, plus REST query errors.',
  );
  const A = await signedIn(rec, 'A');
  await A.req('p', { p: `${OPEN}/q`, d: { a: { x: 2 }, b: { x: 1 }, c: { x: 3 } } });
  await A.req('q', lb(`${OPEN}/q`, { i: 'x', l: 2, vf: 'l' }, 1));
  await A.req('q', lb(`${OPEN}/q`, { i: '.value', l: 1, vf: 'r' }, 2));
  await A.req('q', lb(`${OPEN}/q`, { i: '.key', l: 1, vf: 'l' }, 3));
  await A.req('g', { p: `${OPEN}/q`, q: { i: 'x', l: 1, vf: 'l' } });
  await A.req('g', { p: `${OPEN}/q`, q: { i: 'x', sp: 2, sin: true } });
  const q = `${OPEN}/q.json`;
  await rest(rec, 'GET', `${q}?orderBy=%22x%22&limitToFirst=1`);
  await rest(rec, 'GET', `${q}?orderBy=%22%24value%22&limitToFirst=1`);
  await rest(rec, 'GET', `${q}?orderBy=%22%24key%22&limitToFirst=1`);
  await rest(rec, 'GET', `${q}?orderBy=%22%24priority%22&limitToFirst=1`);
  await rest(rec, 'GET', `${q}?orderBy=x`);
  await rest(rec, 'GET', `${q}?limitToFirst=1`);
  await rest(rec, 'GET', `${q}?orderBy=%22%24key%22&startAt=1`);
  await rest(rec, 'GET', `${q}?orderBy=%22x%22&limitToFirst=1&limitToLast=1`);
  await rest(rec, 'GET', `${q}?orderBy=%22x%22&equalTo=1&startAt=1`);
  await rest(rec, 'GET', `${q}?orderBy=%22x%22&shallow=true`);
  await rec.finish();
};

scenarios.oversize = async () => {
  const rec = new Recorder(
    'oversize',
    'REST writeSizeLimit: tiny with a ~300 KB body and with a small one, an unknown value; then the key-length (768 bytes) and depth (32) limits over WebSocket and REST, just over and at the limit.',
  );
  const A = await signedIn(rec, 'A');
  const big = {};
  for (let i = 0; i < 300; i++) big[`k${i}`] = 'x'.repeat(1000);
  await rest(rec, 'PUT', `${OPEN}/big.json?writeSizeLimit=tiny`, { body: big, bodyNote: '<object of 300 children, 1000-char strings, 305 KB>' });
  await rest(rec, 'PUT', `${OPEN}/small.json?writeSizeLimit=tiny`, { body: { a: 1 } });
  await rest(rec, 'PUT', `${OPEN}/small.json?writeSizeLimit=bogus`, { body: { a: 2 } });
  await rest(rec, 'DELETE', `${OPEN}/big.json?writeSizeLimit=tiny`);
  await rest(rec, 'DELETE', `${OPEN}/big.json`);
  const k768 = 'k'.repeat(768);
  const k769 = 'k'.repeat(769);
  await A.req('p', { p: `${OPEN}/keys/${k768}`, d: 1 });
  await A.req('p', { p: `${OPEN}/keys/${k769}`, d: 1 });
  await A.req('p', { p: `${OPEN}/keys`, d: { [k769]: 1 } });
  await rest(rec, 'PUT', `${OPEN}/keys/${k769}.json`, { body: 1 });
  await rest(rec, 'PUT', `${OPEN}/keys.json`, { body: { [k769]: 1 } });
  // ROOT is 3 levels deep (aruma-ref/run/open); 29 more make 32 and 30 make 33.
  const deep = (n) => Array.from({ length: n }, (_, i) => `d${i}`).join('/');
  const nest = (n) => (n === 0 ? 1 : { d: nest(n - 1) });
  await A.req('p', { p: `${OPEN}/${deep(29)}`, d: 1 });
  await A.req('p', { p: `${OPEN}/${deep(30)}`, d: 1 });
  await A.req('p', { p: `${OPEN}/nest`, d: nest(29) });
  await A.req('p', { p: `${OPEN}/nest`, d: nest(30) });
  await rest(rec, 'PUT', `${OPEN}/${deep(30)}.json`, { body: 1 });
  await rest(rec, 'PUT', `${OPEN}/nest.json`, { body: nest(30) });
  await A.req('p', { p: `${OPEN}/d0`, d: null });
  await A.req('p', { p: `${OPEN}/nest`, d: null });
  await A.req('p', { p: `${OPEN}/keys`, d: null });
  rec.note('The 16 MB WebSocket write limit was not exercised against production (too heavy for a reference run).');
  await rec.finish();
};

scenarios.rest_auth = async () => {
  const rec = new Recorder(
    'rest_auth',
    'REST credentials on a path anyone may read (public) and on one only the user may read (open): none, a valid ID token, "garbage", an unsigned token, as ?auth=, ?access_token= and Authorization: Bearer.',
  );
  const forged = unsignedToken(USER.uid);
  for (const p of [PUBLIC, OPEN]) {
    await rest(rec, 'GET', `${p}.json`, { auth: false });
    await rest(rec, 'GET', `${p}.json`);
    await rest(rec, 'GET', `${p}.json?auth=garbage`, { auth: false });
    await rest(rec, 'GET', `${p}.json?auth=${forged}`, { auth: false });
    await rest(rec, 'GET', `${p}.json?access_token=garbage`, { auth: false });
    await rest(rec, 'GET', `${p}.json`, { auth: false, headers: { Authorization: 'Bearer garbage' } });
    await rest(rec, 'GET', `${p}.json`, { auth: false, headers: { Authorization: `Bearer ${USER.idToken}` } });
  }
  await rest(rec, 'PUT', `${PUBLIC}.json?auth=garbage`, { auth: false, body: 1 });
  await rest(rec, 'PUT', `${OPEN}/ra.json?auth=garbage`, { auth: false, body: 1 });
  await rec.finish();
};

scenarios.rest = async () => {
  const rec = new Recorder(
    'rest',
    'REST: ETag on PUT and GET (present and absent node), if-match 412 and success, print=silent on each method, shallow with .priority, format=export, the arrays rule, server values, and errors.',
  );
  const E = { 'X-Firebase-ETag': 'true' };
  const e = `${OPEN}/e.json`;
  const r1 = await rest(rec, 'PUT', e, { body: { a: 1 }, headers: E });
  await rest(rec, 'GET', e, { headers: E });
  await rest(rec, 'GET', `${OPEN}/absent.json`, { headers: E });
  await rest(rec, 'PUT', e, { body: { a: 2 }, headers: { 'if-match': 'wrong-etag' } });
  await rest(rec, 'PUT', e, { body: { a: 3 }, headers: { 'if-match': r1.headers.etag || 'none' } });
  await rest(rec, 'DELETE', e, { headers: { 'if-match': 'wrong-etag' } });
  await rest(rec, 'PUT', `${OPEN}/absent.json`, { body: 1, headers: { 'if-match': 'null_etag' } });
  await rest(rec, 'GET', e, { headers: { 'if-none-match': r1.headers.etag || 'none' } });

  await rest(rec, 'PUT', `${OPEN}/silent.json?print=silent`, { body: { a: 1 } });
  await rest(rec, 'GET', `${OPEN}/silent.json?print=silent`);
  await rest(rec, 'PATCH', `${OPEN}/silent.json?print=silent`, { body: { b: 2 } });
  await rest(rec, 'POST', `${OPEN}/silent.json?print=silent`, { body: 3 });
  await rest(rec, 'DELETE', `${OPEN}/silent.json?print=silent`);
  await rest(rec, 'GET', `${OPEN}/silent.json?print=pretty`);

  await rest(rec, 'PUT', `${OPEN}/s.json`, { body: { a: 1, b: { c: 2 }, '.priority': 5 } });
  await rest(rec, 'GET', `${OPEN}/s.json?shallow=true`);
  await rest(rec, 'GET', `${OPEN}/s/a.json?shallow=true`);
  await rest(rec, 'GET', `${OPEN}/s.json?format=export`);
  await rest(rec, 'GET', `${OPEN}/s.json`);
  await rest(rec, 'PUT', `${OPEN}/s/b.json`, { body: { c: 2, '.priority': 'p' } });
  await rest(rec, 'GET', `${OPEN}/s.json?shallow=true`);
  await rest(rec, 'PUT', `${OPEN}/sp.json`, { body: { '.value': 7, '.priority': 1 } });
  await rest(rec, 'GET', `${OPEN}/sp.json?shallow=true`);
  await rest(rec, 'GET', `${OPEN}/sp.json?format=export`);

  const arrays = {
    a1: { 0: 'a', 1: 'b', 2: 'c' },
    a2: { 0: 'a', 2: 'c' },
    a3: { 0: 'a', 3: 'd' },
    a4: { 1: 'b', 2: 'c' },
    a5: { 5: 'x' },
    a6: { 0: 'a', 1: 'b', 4: 'e' },
    a7: { 0: 'a', 1: 'b', x: 'c' },
    a8: { '00': 'a', 1: 'b' },
    a9: ['a', null, 'c'],
    a10: { '-1': 'a', 0: 'b' },
  };
  await rest(rec, 'PUT', `${OPEN}/arr.json`, { body: arrays });
  await rest(rec, 'GET', `${OPEN}/arr.json`);
  await rest(rec, 'GET', `${OPEN}/arr.json?shallow=true`);
  await rest(rec, 'GET', `${OPEN}/arr/a2.json`);

  await rest(rec, 'PUT', `${OPEN}/sv.json`, { body: { t: { '.sv': 'timestamp' }, i: { '.sv': { increment: 2 } }, n: 1 } });
  await rest(rec, 'PATCH', `${OPEN}/sv.json`, { body: { n: { '.sv': { increment: 1.5 } }, m: { '.sv': { increment: 1 } } } });
  await rest(rec, 'POST', `${OPEN}/sv.json`, { body: { '.sv': 'timestamp' } });
  await rest(rec, 'PUT', `${OPEN}/sv/bad.json`, { body: { '.sv': 'bogus' } });
  await rest(rec, 'PUT', `${OPEN}/sv/bad.json`, { body: { '.sv': { increment: 'x' } } });
  await rest(rec, 'PUT', `${OPEN}/sv/bad.json`, { body: { '.sv': { other: 1 } } });
  await rest(rec, 'GET', `${OPEN}/sv/bad.json`);

  await rest(rec, 'PUT', `${OPEN}/bad.json`, { raw: '{"a":' });
  await rest(rec, 'PUT', `${OPEN}/bad.json`, { body: { 'a.b': 1 } });
  await rest(rec, 'GET', `${OPEN}/bad.json?shallow=true&orderBy=%22%24key%22`);
  await rest(rec, 'GET', `${OPEN}/e.json`, { auth: false });
  await rest(rec, 'GET', `${OPEN}/e.json?auth=garbage`, { auth: false });
  await rest(rec, 'GET', `${OPEN}/e`);
  await rec.finish();
};

scenarios.numbers = async () => {
  const rec = new Recorder(
    'numbers',
    'How numbers are stored: REST PUT of 1.0, a huge integer, -0, 1e300, 2^53+1, 0.1 and 1E2 as raw JSON, read back over REST and WebSocket; the same values sent as raw WebSocket text.',
  );
  const vals = { n1: '1.0', n2: '12345678901234567890', n3: '-0', n4: '1e300', n5: '9007199254740993', n6: '0.1', n7: '1E2', n8: '-0.0', n9: '100', n10: '1.5e-7' };
  for (const [k, v] of Object.entries(vals)) await rest(rec, 'PUT', `${OPEN}/num/${k}.json`, { raw: v });
  await rest(rec, 'GET', `${OPEN}/num.json`);
  await rest(rec, 'GET', `${OPEN}/num/n1.json`);
  await rest(rec, 'GET', `${OPEN}/num.json?orderBy=%22%24value%22&limitToFirst=3`);
  const A = await signedIn(rec, 'A');
  await A.req('g', { p: `${OPEN}/num`, q: {} });
  const body = `{${Object.entries(vals).map(([k, v]) => `"${k}":${v}`).join(',')}}`;
  await A.req('p', null, { text: (r) => `{"t":"d","d":{"r":${r},"a":"p","b":{"p":"${OPEN}/wnum","d":${body}}}}` });
  await A.req('g', { p: `${OPEN}/wnum`, q: {} });
  await rest(rec, 'GET', `${OPEN}/wnum.json`);
  await A.req('q', lb(`${OPEN}/wnum`));
  await A.req('p', null, { text: (r) => `{"t":"d","d":{"r":${r},"a":"p","b":{"p":"${OPEN}/wnum/n1","d":2.0}}}` });
  await A.req('p', { p: `${OPEN}/wnum/n11`, d: { '.sv': { increment: 1.0 } } });
  await rest(rec, 'GET', `${OPEN}/wnum.json`);
  await rec.finish();
};

// Long polling, as BrowserPollConnection does it: start, the dframe page, one poll that is
// answered when a write arrives through another connection, a send, and disconnect.
scenarios.lp = async () => {
  const rec = new Recorder('lp', 'Long-poll handshake (start with cb), the dframe page, a send of a get, a held poll answered with its reply, and disconnect.');
  const nsq = `&ns=${cfg.ns}`;
  const get = async (q, abortMs) => {
    const ac = new AbortController();
    const t = abortMs ? setTimeout(() => ac.abort(), abortMs) : null;
    const url = `/.lp?${q}${nsq}`;
    rec.push({ conn: 'LP', dir: 'http_req', text: redact(`GET ${url}`) });
    try {
      const r = await fetch(HTTP + url, { signal: ac.signal });
      const text = await r.text();
      const h = {};
      for (const [k, v] of r.headers) if (!['date', 'connection', 'strict-transport-security', 'server'].includes(k)) h[k] = v;
      rec.push({ conn: 'LP', dir: 'http_resp', status: r.status, headers: h, text: redact(text) });
      return text;
    } catch (e) {
      rec.push({ conn: 'LP', dir: 'http_abort', text: String(e.name) });
      return null;
    } finally {
      if (t) clearTimeout(t);
    }
  };
  const start = await get('start=t&ser=31415926&cb=1&v=5');
  const m = start && start.match(/pLPCommand\('start','([^']+)','([^']+)'\)/);
  if (!m) {
    rec.note('no start command in the reply');
    return rec.finish();
  }
  const [, id, pw] = m;
  await get(`dframe=t&id=${id}&pw=${pw}`);
  // A poll is held open until the server has something to send.
  let ser = 27182819;
  const poll = get(`id=${id}&pw=${pw}&ser=${ser++}`, 40000);
  await sleep(1000);
  const frame = JSON.stringify({ t: 'd', d: { r: 1, a: 'g', b: { p: OPEN, q: {} } } });
  rec.push({ conn: 'LP', dir: 'send', text: frame });
  const seg = Buffer.from(frame).toString('base64').replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '.');
  const sent = get(`id=${id}&pw=${pw}&ser=${ser++}&seg0=0&ts0=1&d0=${seg}`, 40000);
  await Promise.all([poll, sent]);
  await get(`id=${id}&pw=${pw}&ser=${ser++}`, 5000);
  await get(`disconn=t&id=${id}&pw=${pw}`, 10000);
  rec.note('Sends use the SDK encoding: base64 with -, _ and . for +, / and = (BrowserPollConnection addLongPollTag_, segments seg/ts/d).');
  await rec.finish();
};

scenarios.cleanup = async () => {
  const rec = new Recorder('cleanup', 'Removes everything under the run root that the user can write, then reads it back.');
  const A = await signedIn(rec, 'A');
  await A.req('p', { p: OPEN, d: null });
  await A.req('g', { p: OPEN, q: {} });
  await rec.finish();
};

// ---------------------------------------------------------------- main

if (cfg.rulesAuth) {
  const params = cfg.restNs ? `?ns=${cfg.ns}` : '';
  const r = await fetch(`${HTTP}/.settings/rules.json${params}`, {
    method: 'PUT',
    headers: { Authorization: cfg.rulesAuth },
    body: JSON.stringify({ rules: RULES(USER.uid) }),
  });
  if (!r.ok) throw new Error(`rules: HTTP ${r.status} ${await r.text()}`);
}
const order = ['handshake', 'auth', 'auth_expired', 'denied', 'cancel', 'no_index', 'oversize', 'rest_auth', 'rest', 'numbers', 'lp', 'cleanup'];
const run = ONLY.length ? ONLY : order.filter((s) => s !== 'handshake');
for (const s of run) {
  if (!scenarios[s]) throw new Error(`unknown scenario ${s}`);
  await scenarios[s]();
}
process.exit(0);
