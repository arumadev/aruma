// Runs recorded Rules Test API cases against the official emulators, as
// black boxes, and records what they decide.
//
//   node record-emulator.mjs --firestore 127.0.0.1:8840 --out <dir> <production json>...
//
// Firestore cases go to a running cloud-firestore-emulator over its REST API:
// the rules are loaded with PUT /emulator/v1/projects/demo-oracle:securityRules,
// the case's documents (resource, mocked get()/exists() targets) are written
// as the owner, and the request is made with an unsigned ID token carrying the
// case's auth. Storage cases go to the cloud-storage-rules-runtime jar
// (storagert.mjs) with the case's request and resource as its variables.
//
// What a REST request cannot carry is skipped with a reason: a chosen
// request.time (Firestore), an auth that is not {uid, token}, request.query.
// Output: <dir>/<topic>.json with { id, emulator: {outcome, ...} | skip }.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, join, resolve } from 'node:path';
import { generate } from './oracle.mjs';
import { StorageRuntime, toValue } from './storagert.mjs';

const args = process.argv.slice(2);
const opt = (name, def) => {
  const i = args.indexOf(name);
  if (i < 0) return def;
  const v = args[i + 1];
  args.splice(i, 2);
  return v;
};
const FS = opt('--firestore', '127.0.0.1:8840');
const OUT = resolve(opt('--out'));
const PROJECT = 'demo-oracle';
const DOCS = `/v1/projects/${PROJECT}/databases/(default)/documents`;
const FS_VERSION = 'cloud-firestore-emulator v1.22.0 (Java 21)';
const ST_VERSION = 'cloud-storage-rules-runtime v1.1.3 (Java 21), driven over its stdin protocol';

// ---- values ----

function rest(v) {
  if (v === null || v === undefined) return { nullValue: null };
  if (typeof v === 'boolean') return { booleanValue: v };
  if (typeof v === 'number') return Number.isInteger(v) ? { integerValue: String(v) } : { doubleValue: v };
  if (typeof v === 'string') return { stringValue: v };
  if (Array.isArray(v)) return { arrayValue: { values: v.map(rest) } };
  if (v && v.__raw !== undefined) {
    return /[.eE]/.test(v.__raw) ? { doubleValue: Number(v.__raw) } : { integerValue: BigInt(v.__raw).toString() };
  }
  const fields = {};
  for (const [k, x] of Object.entries(v)) fields[k] = rest(x);
  return { mapValue: { fields } };
}

// applyRaw replaces the placeholder numbers of a case by {__raw: text}.
function applyRaw(v, raw) {
  if (!raw || v === null || typeof v !== 'object') {
    if (raw && typeof v === 'number') {
      const r = raw.find((x) => x.from === v);
      if (r) return { __raw: r.to };
    }
    return v;
  }
  if (Array.isArray(v)) return v.map((x) => applyRaw(x, raw));
  const out = {};
  for (const [k, x] of Object.entries(v)) out[k] = applyRaw(x, raw);
  return out;
}

const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
function idToken(auth) {
  const now = Math.floor(Date.now() / 1000);
  const claims = {
    iss: `https://securetoken.google.com/${PROJECT}`,
    aud: PROJECT,
    iat: now,
    exp: now + 3600,
    auth_time: now,
    sub: auth.uid,
    user_id: auth.uid,
    ...(auth.token || {}),
  };
  return `${b64url({ alg: 'none', typ: 'JWT' })}.${b64url(claims)}.`;
}

// ---- Firestore emulator ----

async function call(method, path, body, token) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`http://${FS}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  let json;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = text;
  }
  return { status: res.status, body: json };
}

let loaded = null;
async function loadRules(source) {
  if (loaded === source) return { ok: true };
  const r = await call('PUT', `/emulator/v1/projects/${PROJECT}:securityRules`, {
    rules: { files: [{ name: 'firestore.rules', content: source }] },
  });
  if (r.status !== 200) {
    loaded = null;
    return { ok: false, message: r.body?.error?.message ?? JSON.stringify(r.body) };
  }
  const errs = (r.body?.issues || []).filter((i) => i.severity === 'ERROR');
  if (errs.length) {
    loaded = null;
    return { ok: false, message: errs.map((i) => `ERROR L${i.sourcePosition?.line}:${i.sourcePosition?.column} ${i.description}`).join('\n') };
  }
  loaded = source;
  return { ok: true };
}

const docName = (p) => p.replace(/^\/databases\/\(default\)\/documents\//, '');
const owner = (m, p, b) => call(m, p, b, 'owner');

async function putDoc(path, data) {
  const r = await owner('PATCH', `${DOCS}/${path}`, { fields: rest(data).mapValue?.fields ?? {} });
  if (r.status !== 200) throw new Error(`seed ${path}: ${r.status} ${JSON.stringify(r.body)}`);
}
const delDoc = (path) => owner('DELETE', `${DOCS}/${path}`);

function fsClassify(r) {
  if (r.status === 200) return { outcome: 'allow' };
  const msg = typeof r.body === 'object' ? r.body?.error?.message ?? '' : String(r.body);
  if (r.status === 403) {
    // The emulator lists one reason per evaluated statement: "false for
    // 'get' @ L5", "evaluation error at L5:22 for 'get' @ L5", "<error
    // message> for 'get' @ L5", or "No matching allow statements". A
    // condition that reads resource is evaluated twice and the first pass
    // always errors (firebase-tools #6252), so any "false" decides.
    const parts = msg.split(/,\s*(?=[^,]*for '|No matching)/).map((s) => s.trim()).filter(Boolean);
    if (parts.some((p) => p.startsWith('false for'))) return { outcome: 'deny', reason: 'false', message: msg.trim() };
    if (parts.every((p) => p.startsWith('No matching allow statements'))) {
      return { outcome: 'deny', reason: 'no-matching-statement', message: msg.trim() };
    }
    return { outcome: 'error', errorKind: 'evaluation', message: msg.trim() };
  }
  return { outcome: 'error', errorKind: 'request', message: `${r.status} ${msg}`.trim() };
}

function firestoreSkip(c, source) {
  const usesTime = /request\.time/.test(c.expression || source);
  if (usesTime) return 'request.time cannot be chosen on the emulator';
  const a = c.request.auth;
  if (a !== undefined && a !== null && (typeof a.uid !== 'string' || !a.token)) return 'the auth cannot be an ID token';
  if (c.request.query) return 'request.query is not sent by a REST list';
  if (c.rawNumbers) return null;
  return null;
}

async function runFirestore(c, source) {
  const skip = firestoreSkip(c, source);
  if (skip) return { skip };
  const l = await loadRules(source);
  if (!l.ok) return { outcome: 'error', errorKind: 'compile', message: l.message };
  const path = docName(c.request.path);
  const raw = c.rawNumbers;
  const existing = c.resource && c.resource.data !== undefined ? applyRaw(c.resource.data, raw) : null;
  const token = c.request.auth ? idToken(c.request.auth) : null;
  // The documents get() and exists() read.
  const seeded = [];
  for (const m of c.functionMocks || []) {
    const p = docName(m.args[0].exactValue);
    const fn = m.function.replace(/^firestore\./, '');
    if (fn === 'get' && m.result?.value && m.result.value.data) await putDoc(p, m.result.value.data);
    else if (fn === 'exists' && m.result?.value === true) await putDoc(p, {});
    else await delDoc(p);
    seeded.push(p);
  }
  if (existing) await putDoc(path, existing);
  else await delDoc(path);
  const data = c.request.resource && c.request.resource.data !== undefined ? applyRaw(c.request.resource.data, raw) : {};
  const fields = rest(data).mapValue?.fields ?? {};
  let r;
  switch (c.request.method) {
    case 'get':
      r = await call('GET', `${DOCS}/${path}`, undefined, token);
      break;
    case 'list':
      r = await call('GET', `${DOCS}/${path.split('/').slice(0, -1).join('/')}`, undefined, token);
      break;
    case 'create':
      r = await call('PATCH', `${DOCS}/${path}?currentDocument.exists=false`, { fields }, token);
      break;
    case 'update':
      r = await call('PATCH', `${DOCS}/${path}?currentDocument.exists=true`, { fields }, token);
      break;
    case 'delete':
      r = await call('DELETE', `${DOCS}/${path}`, undefined, token);
      break;
    default:
      return { skip: `method ${c.request.method} has no REST request` };
  }
  await delDoc(path);
  for (const p of seeded) await delDoc(p);
  // A get of a missing document answers 404 once the rules allowed it.
  if (r.status === 404 && c.request.method === 'get') return { outcome: 'allow', note: '404: the rules allowed the read of a missing document' };
  // Otherwise 400, 404 or 409 is not a rules decision (a failed
  // precondition, such as an update of a document that does not exist).
  if (r.status === 404 || r.status === 409 || r.status === 400) {
    return { outcome: 'error', errorKind: 'request', message: `${r.status} ${r.body?.error?.message ?? ''}`.trim() };
  }
  return fsClassify(r);
}

// ---- Storage rules runtime ----

const TIME_FIELDS = new Set(['timeCreated', 'updated']);
function storageValue(obj) {
  if (obj === null || obj === undefined) return toValue(null);
  const conv = {};
  for (const [k, v] of Object.entries(obj)) {
    conv[k] = TIME_FIELDS.has(k) && typeof v === 'string' && !Number.isNaN(Date.parse(v)) ? { __timestamp: new Date(v).toISOString() } : v;
  }
  return toValue(conv);
}

let rt = null;
const rtRulesets = new Map();
async function runStorage(c, source) {
  const a = c.request.auth;
  if (a !== undefined && a !== null && (typeof a.uid !== 'string' || !a.token)) return { skip: 'the auth cannot be an ID token' };
  if (!rt) {
    rt = new StorageRuntime();
    await rt.start();
  }
  let name = rtRulesets.get(source);
  if (name === undefined) {
    const { name: n, res } = await rt.load(source);
    if (res.errors && res.errors.length) {
      const msgs = res.errors.map((e) => {
        try {
          const j = JSON.parse(e);
          return `ERROR L${j.sourcePosition_?.line_}:${j.sourcePosition_?.column_} ${j.description_}`;
        } catch {
          return String(e);
        }
      });
      rtRulesets.set(source, { error: msgs.join('\n') });
      name = rtRulesets.get(source);
    } else {
      rtRulesets.set(source, n);
      name = n;
    }
  }
  if (typeof name === 'object') return { outcome: 'error', errorKind: 'compile', message: name.error };
  const time = c.request.time ? { __timestamp: new Date(c.request.time).toISOString() } : { __timestamp: new Date().toISOString() };
  const fields = {
    path: toValue({ __path: c.request.path }),
    time: toValue(time),
    resource: c.request.resource ? storageValue(c.request.resource) : toValue(null),
    auth: a ? toValue({ uid: a.uid, token: a.token }) : toValue(null),
  };
  // As firebase-tools builds it (lib/emulator/storage/rules/runtime.js):
  // path, time, resource and auth; no method.
  const variables = { resource: c.resource ? storageValue(c.resource) : toValue(null), request: { map_value: { fields } } };
  const mocks = new Map();
  for (const m of c.functionMocks || []) {
    const p = m.args[0].exactValue;
    const fn = m.function.replace(/^firestore\./, '');
    if (fn === 'get' && m.result?.value?.data) mocks.set(p, rest(m.result.value.data).mapValue.fields);
    else if (fn === 'exists' && m.result?.value === true) mocks.set(p, {});
  }
  // A list is checked as the emulator checks a list from the SDKs: with
  // the delimiter '/'.
  const delimiter = c.request.method === 'list' ? '/' : undefined;
  const { res, lookups } = await rt.verify(name, { path: c.request.path, method: c.request.method, delimiter, variables }, (p) => mocks.get(p) ?? null);
  const out = res.result?.permit ? { outcome: 'allow' } : { outcome: 'deny' };
  if (!res.result?.permit && res.errors && res.errors.length) {
    Object.assign(out, { outcome: 'error', errorKind: 'evaluation', message: res.errors.map((e) => String(e).replace(/^com\.google\.firebase\.rules\.runtime\.common\.EvaluationException: /, '')).join('\n') });
  } else if (res.status && res.status !== 'ok') {
    Object.assign(out, { outcome: 'error', errorKind: 'request', message: `${res.status} ${res.message ?? ''}`.trim() });
  }
  if (lookups.length) out.lookups = lookups;
  return out;
}

// ---- main ----

mkdirSync(OUT, { recursive: true });
for (const file of args) {
  const f = JSON.parse(readFileSync(file, 'utf8'));
  const results = [];
  for (const c of f.cases) {
    const source = c.rulesetRef ? f.rulesets[c.rulesetRef] : c.rulesetGenerated ? generate(c.rulesetGenerated) : c.ruleset;
    const full = { ...c, resource: c.resourceRef ? f.resources[c.resourceRef] : c.resource };
    let e;
    if (c.expected.errorKind === 'request') e = { skip: 'the Test API refused the request itself' };
    else if (c.service === 'firestore') {
      try {
        e = await runFirestore(full, source);
      } catch (err) {
        // Such as a path with an odd number of segments: the Test API takes
        // it, but it names no document the emulator can hold.
        e = { skip: `the emulator cannot run it: ${err.message.slice(0, 200)}` };
      }
    }
    else e = await runStorage(full, source);
    results.push({ id: c.id, production: c.expected, emulator: e });
  }
  const out = {
    schema: 'aruma.rules-oracle/v1',
    topic: f.topic,
    service: f.service,
    set: f.set,
    source: 'aruma',
    provenance: 'emulator',
    recordedAt: new Date().toISOString(),
    version: f.service === 'firestore' ? FS_VERSION : ST_VERSION,
    cases: results,
  };
  writeFileSync(join(OUT, basename(file)), JSON.stringify(out, null, 1) + '\n');
  const n = { same: 0, differ: 0, skip: 0 };
  for (const r of results) {
    if (r.emulator.skip) n.skip++;
    else if (r.emulator.outcome === r.production.outcome) n.same++;
    else n.differ++;
  }
  console.log(`${f.topic} [${f.set}]`, JSON.stringify(n));
}
if (rt) await rt.stop();
