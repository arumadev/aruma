// Captures the official Realtime Database emulator's verdicts for a matrix of rules and
// operations, as golden files for the Go tests in this package.
//
//   java -jar ~/.cache/firebase/emulators/firebase-database-emulator-v4.11.2.jar --host 127.0.0.1 --port 9450
//   node capture.mjs matrix.mjs matrix.golden.json 9450
//   node capture.mjs apps.mjs apps.golden.json 9450
//   node capture.mjs nulls.mjs nulls.golden.json 9450
//
// A matrix module exports {groups, loads}. A group is rules (an object, or rulesText: the raw
// document, comments and all), the data stored before each check, and checks. Every check runs
// twice, over REST and over the raw WebSocket protocol (version 5), each time on a fresh
// namespace seeded with the group's rules and data. Rules go in with PUT /.settings/rules.json
// and the owner bearer, data with the owner bearer too, so rules do not apply to the seeding.
// A user is an unsigned (alg:none) ID token, which the emulator accepts; the golden file keeps
// the token's claims so the Go side builds the same auth. A load is a rules document whose
// acceptance (200) or refusal (400 and its message) is recorded.
//
// Node 22 or later (global fetch and WebSocket), no packages.
import fs from 'node:fs';
import path from 'node:path';

const [, , inFile, outFile, port = '9450'] = process.argv;
const base = `127.0.0.1:${port}`;
const owner = {Authorization: 'Bearer owner'};
const matrixURL = new URL(inFile, `file://${process.cwd()}/`);
const matrix = (await import(matrixURL.href)).default;
const b64 = o => Buffer.from(JSON.stringify(o)).toString('base64url');

// claimsOf is the ID token payload for an auth spec {uid, claims}: the standard claims the
// emulator reads, and the custom ones next to them, as in a real ID token.
function claimsOf(auth) {
  return {sub: auth.uid, user_id: auth.uid, uid: auth.uid, iat: 1, exp: 99999999999, ...(auth.claims || {})};
}

function token(auth) {
  return b64({alg: 'none', typ: 'JWT'}) + '.' + b64(claimsOf(auth)) + '.';
}

async function http(method, p, body, headers = {}) {
  const r = await fetch(`http://${base}${p}`, {method, body, headers});
  return {status: r.status, text: await r.text()};
}

function rulesDoc(group) {
  return group.rulesText ?? JSON.stringify(group.rules);
}

let seq = 0;
async function prepare(group) {
  const ns = `cap${process.pid}x${seq++}`;
  const r = await http('PUT', `/.settings/rules.json?ns=${ns}`, rulesDoc(group), owner);
  if (r.status !== 200) throw new Error(`${group.name}: rules rejected: ${r.text}`);
  const d = await http('PUT', `/.json?ns=${ns}`, JSON.stringify(group.data ?? null), owner);
  if (d.status !== 200) throw new Error(`${group.name}: data rejected: ${d.text}`);
  return ns;
}

// restQuery renders a query spec as REST parameters.
function restQuery(q) {
  if (!q) return '';
  const p = [];
  const ord = {key: '$key', value: '$value', priority: '$priority'}[q.orderBy] ?? q.orderBy;
  if (ord) p.push('orderBy=' + encodeURIComponent(JSON.stringify(ord)));
  for (const k of ['startAt', 'endAt', 'equalTo']) if (k in q) p.push(k + '=' + encodeURIComponent(JSON.stringify(q[k])));
  for (const k of ['limitToFirst', 'limitToLast']) if (k in q) p.push(k + '=' + q[k]);
  return p.length ? '&' + p.join('&') : '';
}

// wireQuery renders a query spec as the protocol's query object.
function wireQuery(q) {
  if (!q) return undefined;
  const o = {};
  if (q.orderBy === 'key') o.i = '.key';
  else if (q.orderBy === 'value') o.i = '.value';
  else if (q.orderBy && q.orderBy !== 'priority') o.i = q.orderBy;
  if ('startAt' in q) o.sp = q.startAt;
  if ('endAt' in q) o.ep = q.endAt;
  if ('equalTo' in q) { o.sp = q.equalTo; o.ep = q.equalTo; }
  if ('limitToFirst' in q) { o.l = q.limitToFirst; o.vf = 'l'; }
  if ('limitToLast' in q) { o.l = q.limitToLast; o.vf = 'r'; }
  return o;
}

function verdictREST(r) {
  if (r.status === 200 || r.status === 204) return 'allow';
  if (r.status === 401) return 'deny';
  return `error ${r.status} ${r.text.replace(/\s+/g, ' ').trim()}`;
}

async function viaREST(ns, c) {
  const auth = c.auth ? '&auth=' + token(c.auth) : '';
  const url = `${c.path === '/' ? '/' : c.path}.json?ns=${ns}${auth}`;
  switch (c.op) {
    case 'read': return verdictREST(await http('GET', url + restQuery(c.query)));
    case 'set': return verdictREST(await http('PUT', url, JSON.stringify(c.value)));
    case 'update': return verdictREST(await http('PATCH', url, JSON.stringify(c.value)));
    case 'delete': return verdictREST(await http('DELETE', url));
    default: return null; // onDisconnect exists only on the protocol
  }
}

// session opens a protocol connection and returns a function that sends a request and
// resolves to the response body {s, d}.
async function session(ns) {
  const ws = new WebSocket(`ws://${base}/.ws?v=5&ns=${ns}`);
  const pending = new Map();
  let rid = 0;
  let ready;
  const hello = new Promise(r => { ready = r; });
  ws.onmessage = e => {
    const m = JSON.parse(e.data);
    if (m.t === 'c' && m.d.t === 'h') ready();
    if (m.t === 'd' && m.d.r && pending.has(m.d.r)) {
      pending.get(m.d.r)(m.d.b);
      pending.delete(m.d.r);
    }
  };
  await hello;
  const req = (a, b) => new Promise((resolve, reject) => {
    const r = ++rid;
    pending.set(r, resolve);
    ws.send(JSON.stringify({t: 'd', d: {r, a, b}}));
    setTimeout(() => reject(new Error(`timeout on ${a}`)), 5000);
  });
  return {req, close: () => ws.close()};
}

async function viaWS(ns, c) {
  const s = await session(ns);
  try {
    if (c.auth) {
      const a = await s.req('auth', {cred: token(c.auth)});
      if (a.s !== 'ok') return `error auth ${a.s}`;
    }
    let b;
    switch (c.op) {
      case 'read': {
        const q = wireQuery(c.query);
        b = await s.req('q', q ? {p: c.path, q, t: 1, h: ''} : {p: c.path, h: ''});
        break;
      }
      case 'set': b = await s.req('p', {p: c.path, d: c.value}); break;
      case 'update': b = await s.req('m', {p: c.path, d: c.value}); break;
      case 'delete': b = await s.req('p', {p: c.path, d: null}); break;
      case 'onDisconnectSet': b = await s.req('o', {p: c.path, d: c.value}); break;
      case 'onDisconnectUpdate': b = await s.req('om', {p: c.path, d: c.value}); break;
      case 'onDisconnectCancel': b = await s.req('oc', {p: c.path}); break;
      default: throw new Error('unknown op ' + c.op);
    }
    if (b.s === 'ok') return 'allow';
    if (b.s === 'permission_denied') return 'deny';
    return `error ${b.s} ${typeof b.d === 'string' ? b.d : JSON.stringify(b.d ?? '')}`;
  } finally {
    s.close();
  }
}

const out = {
  provenance: {
    source: 'Firebase Realtime Database emulator, run locally; no Firebase project was contacted',
    jar: 'firebase-database-emulator-v4.11.2.jar',
    java: process.env.CAPTURE_JAVA || 'unknown',
    captured: new Date().toISOString().slice(0, 10),
    script: 'capture.mjs',
    matrix: path.basename(matrixURL.pathname),
    transports: 'REST (GET/PUT/PATCH/DELETE .json) and protocol v5 over WebSocket (q, p, m, o, om, oc)',
    auth: 'none, or an unsigned alg:none ID token whose claims are listed under token',
    verdicts: 'allow, deny, or "error <status> <message>" for an answer that is not a rules decision',
  },
  loads: [],
  groups: [],
};

for (const l of matrix.loads || []) {
  const r = await http('PUT', `/.settings/rules.json?ns=cap${process.pid}load`, l.doc, owner);
  const o = {name: l.name, doc: l.doc, status: r.status};
  if (r.status !== 200) o.error = JSON.parse(r.text).error.trim();
  out.loads.push(o);
}

let n = 0;
for (const g of matrix.groups || []) {
  const og = {name: g.name};
  if (g.rulesText !== undefined) og.rulesText = g.rulesText; else og.rules = g.rules;
  og.data = g.data ?? null;
  og.checks = [];
  const probe = await http('PUT', `/.settings/rules.json?ns=cap${process.pid}probe`, rulesDoc(g), owner);
  if (probe.status !== 200) {
    og.loadError = JSON.parse(probe.text).error.trim();
    out.groups.push(og);
    continue;
  }
  for (const c of g.checks) {
    const oc = {op: c.op, path: c.path};
    if ('value' in c) oc.value = c.value;
    if (c.query) oc.query = c.query;
    oc.auth = c.auth ? {uid: c.auth.uid, token: claimsOf(c.auth)} : null;
    const rest = await viaREST(await prepare(g), c);
    if (rest !== null) oc.rest = rest;
    oc.ws = await viaWS(await prepare(g), c);
    og.checks.push(oc);
    n++;
  }
  out.groups.push(og);
}
fs.writeFileSync(outFile, JSON.stringify(out, null, 1) + '\n');
console.log(`captured ${n} checks in ${out.groups.length} groups, ${out.loads.length} loads`);
