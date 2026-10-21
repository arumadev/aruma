// Captures how the official Realtime Database emulator answers the REST API, as a golden file
// that the acceptance tests replay against Aruma.
//
//   java -jar ~/.cache/firebase/emulators/firebase-database-emulator-v4.11.2.jar --host 127.0.0.1 --port 9441
//   node capture_rest.mjs rest_cases.mjs rest.golden.json 9441
//
// A cases module exports {groups}. A group has rules (an object, or null for the emulator's
// default open rules), the data stored at the root before its cases (or null), and cases that
// run in order on one fresh namespace, so a case sees what the cases before it wrote. Rules go
// in with PUT /.settings/rules.json and the owner bearer, data with the owner bearer too, so
// rules do not apply to the seeding.
//
// A case is one HTTP request: method, path (with its .json suffix), query (raw, without ns),
// headers, auth, authVia, authVariableOverride and body (raw text, or json: a value to encode).
// The runner always puts ns=<namespace> first in the query string, then the case's query, then
// the credential when it travels in the URL, then auth_variable_override. Credentials:
//   auth "none"            no credential
//   auth "admin"           the emulator's owner credential, "owner"
//   auth {uid, claims}     an unsigned (alg:none) ID token with those claims, which the
//                          emulator accepts
//   auth {token}           that literal string, as sent by a client with a broken credential
// and authVia says how it travels: "bearer" (Authorization: Bearer <c>, the default for
// admin), "auth" (?auth=<c>, the default otherwise), "access_token" (?access_token=<c>) or
// "header" (Authorization: <c>, with no scheme).
//
// A header value may hold {{etag}}: the ETag of the most recent response in the group that had
// one. The golden file keeps the placeholder, so the replay substitutes its own ETag. The
// response keeps the status, the headers etag, content-type, allow and
// access-control-expose-headers when present, and the body exactly as received. Push ids and
// timestamps differ from run to run; the replay normalises them.
//
// The provenance takes the JAR's SHA-256 from the file under ~/.cache/firebase/emulators and the
// Java version from `java -version`; CAPTURE_JAR_SHA256 and CAPTURE_JAVA override them.
//
// Node 22 or later, no packages.
import {spawnSync} from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';

const [, , inFile, outFile, port = '9441'] = process.argv;
const casesURL = new URL(inFile, `file://${process.cwd()}/`);
const spec = (await import(casesURL.href)).default;
const b64 = o => Buffer.from(JSON.stringify(o)).toString('base64url');
const kept = ['etag', 'content-type', 'allow', 'access-control-expose-headers'];

// claimsOf is the ID token payload for an auth spec {uid, claims}: the standard claims the
// emulator reads, and the custom ones next to them, as in a real ID token.
function claimsOf(auth) {
  return {sub: auth.uid, user_id: auth.uid, uid: auth.uid, iat: 1, exp: 99999999999, ...(auth.claims || {})};
}

function credential(auth) {
  if (auth === 'admin') return 'owner';
  if (auth.token !== undefined) return auth.token;
  return b64({alg: 'none', typ: 'JWT'}) + '.' + b64(claimsOf(auth)) + '.';
}

// send writes one HTTP/1.1 request on its own connection and reads the response until the
// server closes it. A raw socket rather than node:http or fetch: the request carries only the
// headers the case names (plus Host, Connection and Content-Length), any method or body goes
// through, and the body the emulator writes after a HEAD response is kept rather than
// tripping the client's parser.
function send(method, p, headers, body) {
  return new Promise((resolve, reject) => {
    const lines = [`${method} ${p} HTTP/1.1`, `Host: 127.0.0.1:${port}`, 'Connection: close'];
    for (const [k, v] of Object.entries(headers)) lines.push(`${k}: ${v}`);
    const payload = body === null || body === undefined ? null : Buffer.from(body, 'utf8');
    if (payload) lines.push(`Content-Length: ${payload.length}`);
    const chunks = [];
    const sock = net.connect(Number(port), '127.0.0.1', () => {
      sock.write(lines.join('\r\n') + '\r\n\r\n');
      if (payload) sock.write(payload);
    });
    sock.setTimeout(10000, () => sock.destroy(new Error(`timeout on ${method} ${p}`)));
    sock.on('data', c => chunks.push(c));
    sock.on('error', reject);
    sock.on('close', () => {
      try {
        resolve(parse(Buffer.concat(chunks)));
      } catch (e) {
        reject(new Error(`${method} ${p}: ${e.message}`));
      }
    });
  });
}

// parse splits a raw response into its status, the headers worth keeping and the body.
function parse(buf) {
  const sep = buf.indexOf('\r\n\r\n');
  if (sep < 0) throw new Error('no response head');
  const [statusLine, ...headerLines] = buf.subarray(0, sep).toString('latin1').split('\r\n');
  const status = Number(statusLine.split(' ')[1]);
  const all = {};
  for (const l of headerLines) {
    const i = l.indexOf(':');
    all[l.slice(0, i).trim().toLowerCase()] = l.slice(i + 1).trim();
  }
  let rest = buf.subarray(sep + 4);
  if (/chunked/i.test(all['transfer-encoding'] || '')) rest = dechunk(rest);
  else if (all['content-length'] !== undefined) rest = rest.subarray(0, Number(all['content-length']));
  const out = {};
  for (const k of kept) if (all[k] !== undefined) out[k] = all[k];
  return {status, headers: out, body: rest.toString('utf8')};
}

function dechunk(b) {
  const parts = [];
  for (;;) {
    const e = b.indexOf('\r\n');
    const n = parseInt(b.subarray(0, e).toString('latin1'), 16);
    if (!n) break;
    parts.push(b.subarray(e + 2, e + 2 + n));
    b = b.subarray(e + 2 + n + 2);
  }
  return Buffer.concat(parts);
}

// normalise fills in a case's defaults, giving the request as the golden file records it.
function normalise(c) {
  const auth = c.auth ?? 'none';
  const r = {method: c.method ?? 'GET', path: c.path, query: c.query ?? '', headers: c.headers ?? {}, auth};
  if (auth !== 'none') r.authVia = c.authVia ?? (auth === 'admin' ? 'bearer' : 'auth');
  if ('authVariableOverride' in c) r.authVariableOverride = c.authVariableOverride;
  r.body = 'json' in c ? JSON.stringify(c.json) : (c.body ?? null);
  return r;
}

async function run(ns, r, etag) {
  const q = [`ns=${ns}`];
  if (r.query) q.push(r.query);
  const headers = {};
  for (const [k, v] of Object.entries(r.headers)) headers[k] = v.replaceAll('{{etag}}', etag ?? '');
  if (r.auth !== 'none') {
    const cred = credential(r.auth);
    switch (r.authVia) {
      case 'bearer': headers.Authorization = 'Bearer ' + cred; break;
      case 'header': headers.Authorization = cred; break;
      case 'auth': q.push('auth=' + encodeURIComponent(cred)); break;
      case 'access_token': q.push('access_token=' + encodeURIComponent(cred)); break;
      default: throw new Error('unknown authVia ' + r.authVia);
    }
  }
  if ('authVariableOverride' in r) {
    q.push('auth_variable_override=' + encodeURIComponent(JSON.stringify(r.authVariableOverride)));
  }
  return send(r.method, `${r.path}?${q.join('&')}`, headers, r.body);
}

const jar = 'firebase-database-emulator-v4.11.2.jar';

function jarSha256() {
  if (process.env.CAPTURE_JAR_SHA256) return process.env.CAPTURE_JAR_SHA256;
  const f = path.join(os.homedir(), '.cache/firebase/emulators', jar);
  return fs.existsSync(f) ? crypto.createHash('sha256').update(fs.readFileSync(f)).digest('hex') : 'unknown';
}

// javaVersion turns `java -version` into "Temurin 19.0.2" (vendor and version).
function javaVersion() {
  if (process.env.CAPTURE_JAVA) return process.env.CAPTURE_JAVA;
  // java prints its version on stderr.
  const r = spawnSync('java', ['-version'], {encoding: 'utf8'});
  const text = r.stderr || '';
  const version = text.match(/version "([^"]+)"/);
  const vendor = text.match(/Runtime Environment (\w+)/);
  if (!version) return 'unknown';
  return vendor ? `${vendor[1]} ${version[1]}` : version[1];
}

const owner = {Authorization: 'Bearer owner'};
let seq = 0;
async function prepare(g) {
  const ns = `rest${process.pid}x${seq++}`;
  if (g.rules != null) {
    const r = await send('PUT', `/.settings/rules.json?ns=${ns}`, owner, JSON.stringify(g.rules));
    if (r.status !== 200) throw new Error(`${g.name}: rules rejected: ${r.body}`);
  }
  if (g.data != null) {
    const d = await send('PUT', `/.json?ns=${ns}`, owner, JSON.stringify(g.data));
    if (d.status !== 200) throw new Error(`${g.name}: data rejected: ${d.body}`);
  }
  return ns;
}

const out = {
  provenance: {
    source: 'Firebase Realtime Database emulator, run locally; no Firebase project was contacted',
    jar,
    jarSha256: jarSha256(),
    java: javaVersion(),
    captured: new Date().toISOString().slice(0, 10),
    script: 'capture_rest.mjs',
    cases: path.basename(casesURL.pathname),
    notes: spec.notes || '',
  },
  groups: [],
};

let n = 0;
const statuses = {};
for (const g of spec.groups) {
  const ns = await prepare(g);
  const og = {name: g.name, rules: g.rules ?? null, data: g.data ?? null, cases: []};
  let etag;
  for (const c of g.cases) {
    const request = normalise(c);
    const response = await run(ns, request, etag);
    if (response.headers.etag !== undefined) etag = response.headers.etag;
    og.cases.push({name: c.name, request, response});
    statuses[response.status] = (statuses[response.status] || 0) + 1;
    n++;
  }
  out.groups.push(og);
}
fs.writeFileSync(outFile, JSON.stringify(out, null, 1) + '\n');
console.log(`captured ${n} cases in ${out.groups.length} groups`);
console.log('statuses: ' + Object.entries(statuses).map(([s, k]) => `${s}x${k}`).join(' '));
