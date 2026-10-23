// capture.mjs - records Cloud Storage for Firebase exchanges for test/oracle/storage.
//
// Every request goes through a recording reverse proxy in this process, so each file holds
// what the official client actually put on the wire and what the server answered, plus what
// the SDK made of it (values, error codes and messages).
//
// Usage:
//   node capture.mjs <config.json> <outDir> [scenario,...]
//
// config.json:
//   target       "emulator" | "aruma" (production is not wired: see ../README.md)
//   upstream     origin of the Storage server, e.g. "http://127.0.0.1:8860"
//   proxyPort    port of the recording proxy (8865); the SDKs are pointed at it
//   bucket       e.g. "demo-aruma-ref.appspot.com"
//   projectId    e.g. "demo-aruma-ref"
//   authEmulator host:port of an Auth emulator that signs the test users in
//   run          run id; every object goes under aruma-ref/<run>/
//   version      free text recorded as the server's version (emulator: firebase-tools 15.34.0)
//   goAdmin      optional path of the go-admin binary (go build -o <path> ./go-admin)
//
// The rules the server runs are those of emulator/storage.rules. Tokens, uids and emails are
// replaced by placeholders before anything is written; download tokens become <download-token-N>.

import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import crypto from 'node:crypto';
import { initializeApp, deleteApp } from 'firebase/app';
import { getAuth, connectAuthEmulator, createUserWithEmailAndPassword, signOut } from 'firebase/auth';
import {
  getStorage, connectStorageEmulator, ref, uploadBytes, uploadBytesResumable, uploadString,
  getMetadata, updateMetadata, getBytes, getDownloadURL, deleteObject, list, listAll,
} from 'firebase/storage';

const cfg = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
if (process.env.GO_ADMIN) cfg.goAdmin = process.env.GO_ADMIN;
const OUT = process.argv[3];
const ONLY = (process.argv[4] || '').split(',').filter(Boolean);
const RUN = cfg.run;
const ROOT = `aruma-ref/${RUN}`;
const PROXY = `127.0.0.1:${cfg.proxyPort}`;
const today = () => new Date().toISOString().slice(0, 10);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------------------------------------------------------- redaction
// Download tokens and upload session ids are learnt from the responses (downloadTokens,
// metadata.firebaseStorageDownloadTokens, X-GUploader-UploadID, upload_id=) and replaced by
// numbered placeholders, so the same token keeps the same name across a file. Other UUIDs
// (multipart boundaries, idempotency tokens) are not secret and stay.
const SECRETS = new Map(); // literal -> placeholder
let tokenCount = 0;
let uploadCount = 0;
function secret(value, placeholder) {
  if (value && !SECRETS.has(value)) SECRETS.set(value, placeholder);
}
function downloadToken(t) {
  if (t && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(t) && !/^0{8}-/.test(t)) {
    secret(t, `<download-token-${++tokenCount}>`);
  }
}
function uploadId(t) {
  if (t && t.length >= 8) secret(t, `<upload-id-${++uploadCount}>`);
}
function learn(headers, bodyText) {
  uploadId(headers['x-gupload-uploadid']);
  for (const h of ['x-goog-upload-url', 'location']) {
    const m = /[?&]upload_id=([^&]+)/.exec(headers[h] || '');
    if (m) uploadId(decodeURIComponent(m[1]));
  }
  if (!bodyText || !/^\s*\{/.test(bodyText)) return;
  let j;
  try { j = JSON.parse(bodyText); } catch { return; }
  const visit = (o) => {
    if (!o || typeof o !== 'object') return;
    for (const [k, v] of Object.entries(o)) {
      if ((k === 'downloadTokens' || k === 'firebaseStorageDownloadTokens') && typeof v === 'string') v.split(',').forEach(downloadToken);
      else visit(v);
    }
  };
  visit(j);
}
const LOCAL_PATH =/(?:\/Volumes|\/Users|\/private|\/var\/folders|\/opt|\/home|\/tmp)\/[^\s"'<>():\\]+/g;
function redact(s) {
  if (typeof s !== 'string') return s;
  // Longest first, so an ID token is replaced before a uid inside it could be.
  const keys = [...SECRETS.keys()].sort((a, b) => b.length - a.length);
  for (const k of keys) s = s.split(k).join(SECRETS.get(k));
  // Any other JWT (a forged token) is not recorded either.
  s = s.replace(/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*/g, '<jwt>');
  // Absolute paths of the capture machine (an emulator stack trace in an error page).
  s = s.replace(LOCAL_PATH, '<path>');
  return s;
}

// ---------------------------------------------------------------- recording proxy
const TEXTUAL = /^(application\/(json|x-www-form-urlencoded|javascript)|text\/|multipart\/)/i;
const utf8 = new TextDecoder('utf-8', { fatal: true });
function describeBody(buf, contentType) {
  if (!buf || buf.length === 0) return undefined;
  const sha256 = crypto.createHash('sha256').update(buf).digest('hex');
  let text = null;
  try { text = utf8.decode(buf); } catch { /* binary */ }
  const printable = text !== null && !/[\u0000-\u0008\u000e-\u001f]/.test(text);
  if (printable && (TEXTUAL.test(contentType || '') || buf.length <= 16384)) {
    if (buf.length <= 16384) return { text };
    return { length: buf.length, sha256, head: buf.subarray(0, 600).toString('utf8'), tail: buf.subarray(-300).toString('utf8') };
  }
  if (buf.length <= 512) return { base64: buf.toString('base64'), length: buf.length };
  return { length: buf.length, sha256, headBase64: buf.subarray(0, 64).toString('base64') };
}
// Headers that only describe this hop or the capture machine.
const DROP_REQ = new Set(['connection', 'keep-alive']);
const DROP_RES = new Set(['connection', 'keep-alive', 'date']);
function pickHeaders(h, drop) {
  const out = {};
  for (const [k, v] of Object.entries(h)) if (!drop.has(k.toLowerCase())) out[k.toLowerCase()] = v;
  return out;
}

let current = null; // the Recorder of the running scenario
const upstream = new URL(cfg.upstream);
const proxy = http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    const body = Buffer.concat(chunks);
    const t = Date.now();
    const out = http.request(
      { host: upstream.hostname, port: upstream.port, method: req.method, path: req.url, headers: req.headers },
      (ures) => {
        const rchunks = [];
        ures.on('data', (c) => rchunks.push(c));
        ures.on('end', () => {
          const rbody = Buffer.concat(rchunks);
          learn(ures.headers, rbody.length < 1 << 20 ? rbody.toString('utf8') : '');
          current?.exchange({
            request: {
              method: req.method,
              url: req.url,
              headers: pickHeaders(req.headers, DROP_REQ),
              body: describeBody(body, req.headers['content-type']),
            },
            response: {
              status: ures.statusCode,
              headers: pickHeaders(ures.headers, DROP_RES),
              body: describeBody(rbody, ures.headers['content-type']),
            },
            elapsedMs: Date.now() - t,
          });
          res.writeHead(ures.statusCode, ures.headers);
          res.end(rbody);
        });
      },
    );
    out.on('error', (e) => {
      current?.note(`proxy error: ${e.message}`);
      res.writeHead(502);
      res.end();
    });
    out.end(body);
  });
});

class Recorder {
  constructor(scenario, description) {
    this.doc = {
      scenario,
      description,
      source: cfg.target,
      version: cfg.version,
      captured: today(),
      script: 'capture/capture.mjs',
      clients: { web: 'firebase 13.1.0 (Node 24, fetch)', admin: 'firebase-admin 14.5.0 + @google-cloud/storage 8.4.0' },
      run: RUN,
      bucket: cfg.bucket,
      steps: [],
      notes: [],
    };
    this.t0 = Date.now();
    this.step = null;
  }
  begin(name, detail) {
    this.step = { step: name, ...(detail ? { detail } : {}), exchanges: [] };
    this.doc.steps.push(this.step);
  }
  exchange(x) {
    x.ms = Date.now() - this.t0;
    if (!this.step) this.begin('(unnamed)');
    this.step.exchanges.push(x);
  }
  result(v) {
    this.step.result = v;
  }
  note(s) {
    this.doc.notes.push(s);
  }
  finish() {
    fs.mkdirSync(OUT, { recursive: true });
    const file = path.join(OUT, `${this.doc.scenario}.json`);
    fs.writeFileSync(file, redact(JSON.stringify(this.doc, null, 2)) + '\n');
    const n = this.doc.steps.reduce((a, s) => a + s.exchanges.length, 0);
    console.log(`wrote ${file} (${this.doc.steps.length} steps, ${n} exchanges)`);
  }
}

// ---------------------------------------------------------------- helpers
// sdkCall runs one SDK operation as a named step and records its value or error.
async function sdkCall(rec, name, fn, shape = (v) => v) {
  rec.begin(name);
  try {
    const v = await fn();
    rec.result({ ok: shape(v) });
    return v;
  } catch (e) {
    rec.result({
      error: {
        name: e.name,
        code: e.code,
        status: e.status ?? e.status_,
        message: e.message,
        serverResponse: e.customData?.serverResponse ?? e.serverResponse,
      },
    });
    await sleep(50);
    return undefined;
  }
}

// raw sends one HTTP request through the proxy as a named step.
async function raw(rec, name, method, url, { headers = {}, body } = {}) {
  rec.begin(name);
  const res = await fetch(`http://${PROXY}${url}`, { method, headers, body });
  const text = await res.text();
  rec.result({ status: res.status });
  return { res, text, json: () => JSON.parse(text) };
}

const enc = (p) => encodeURIComponent(p);
const B = () => `/v0/b/${cfg.bucket}/o`;
const OBJ = (p) => `${B()}/${enc(p)}`;
const metaShape = (m) => m && { ...m, ref: undefined };
const listShape = (r) => ({
  items: r.items.map((i) => i.fullPath),
  prefixes: r.prefixes.map((p) => p.fullPath),
  nextPageToken: r.nextPageToken,
});
const bytes = (n, fill = 'a') => new TextEncoder().encode(fill.repeat(n));

// ---------------------------------------------------------------- users and apps
async function signUp(label) {
  const app = initializeApp({ apiKey: 'fake-api-key', projectId: cfg.projectId, storageBucket: cfg.bucket, appId: '1:1:web:1' }, `${label}-${Date.now()}`);
  const auth = getAuth(app);
  connectAuthEmulator(auth, `http://${cfg.authEmulator}`, { disableWarnings: true });
  const email = `${label}-${RUN}-${Date.now()}@example.com`;
  const cred = await createUserWithEmailAndPassword(auth, email, 'secret-password-1');
  const idToken = await cred.user.getIdToken();
  secret(idToken, `<id-token-${label}>`);
  secret(cred.user.uid, `<uid-${label}>`);
  secret(email, `<email-${label}>`);
  const storage = getStorage(app);
  connectStorageEmulator(storage, '127.0.0.1', cfg.proxyPort);
  return { app, auth, storage, uid: cred.user.uid, idToken, label };
}
function anonymousStorage() {
  const app = initializeApp({ apiKey: 'fake-api-key', projectId: cfg.projectId, storageBucket: cfg.bucket, appId: '1:1:web:1' }, `anon-${Date.now()}`);
  const storage = getStorage(app);
  connectStorageEmulator(storage, '127.0.0.1', cfg.proxyPort);
  return { app, storage };
}

// ---------------------------------------------------------------- scenarios
const scenarios = {};

scenarios.sdk_upload = async (A) => {
  const rec = new Recorder('sdk_upload', 'Uploads through the web SDK (firebase 13.1.0 in Node): uploadBytes (multipart), uploadString in each format, uploadBytesResumable small (multipart) and 700 KiB (resumable, several chunks), with metadata.');
  current = rec;
  const s = A.storage;
  await sdkCall(rec, 'uploadBytes small, no metadata', () => uploadBytes(ref(s, `${ROOT}/open/up/plain.txt`), bytes(11, 'h')), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadBytes with full metadata', () => uploadBytes(ref(s, `${ROOT}/open/up/meta.txt`), new TextEncoder().encode('hello meta'), {
    contentType: 'text/plain;charset=utf-8', cacheControl: 'public, max-age=60', contentDisposition: 'inline; filename="m.txt"',
    contentEncoding: 'identity', contentLanguage: 'en', customMetadata: { a: '1', b: 'two' },
  }), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadString raw', () => uploadString(ref(s, `${ROOT}/open/up/raw.txt`), 'raw text ✓'), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadString base64', () => uploadString(ref(s, `${ROOT}/open/up/b64.bin`), 'AAECAwQ=', 'base64'), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadString data_url', () => uploadString(ref(s, `${ROOT}/open/up/data.png`), 'data:image/png;base64,iVBORw0KGgo=', 'data_url'), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadBytesResumable 1 KiB', () => uploadBytesResumable(ref(s, `${ROOT}/open/up/small-resumable.txt`), bytes(1024, 'r')), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadBytesResumable 700 KiB', () => uploadBytesResumable(ref(s, `${ROOT}/open/up/large.txt`), bytes(700 * 1024, 'L'), { contentType: 'text/plain', customMetadata: { big: 'yes' } }), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadBytes path with spaces and unicode', () => uploadBytes(ref(s, `${ROOT}/open/up/dir with space/ملف #1?.txt`), bytes(3, 'u')), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadBytes overwrite plain.txt', () => uploadBytes(ref(s, `${ROOT}/open/up/plain.txt`), bytes(5, 'o')), (r) => metaShape(r.metadata));
  await sdkCall(rec, 'uploadBytes empty file', () => uploadBytes(ref(s, `${ROOT}/open/up/empty`), new Uint8Array(0)), (r) => metaShape(r.metadata));
  rec.finish();
};

scenarios.sdk_read = async (A) => {
  const rec = new Recorder('sdk_read', 'getMetadata, getBytes, getBytes with maxDownloadSizeBytes, getDownloadURL and an unauthenticated fetch of that URL, then a wrong and no token.');
  current = rec;
  const s = A.storage;
  const r = ref(s, `${ROOT}/open/read/r.txt`);
  await sdkCall(rec, 'setup upload', () => uploadBytes(r, new TextEncoder().encode('read me please'), { contentType: 'text/plain', customMetadata: { k: 'v' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'getMetadata', () => getMetadata(r), metaShape);
  await sdkCall(rec, 'getBytes', () => getBytes(r), (b) => new TextDecoder().decode(b));
  await sdkCall(rec, 'getBytes maxDownloadSizeBytes=4', () => getBytes(r, 4), (b) => new TextDecoder().decode(b));
  const url = await sdkCall(rec, 'getDownloadURL', () => getDownloadURL(r));
  if (url) {
    const u = new URL(url);
    await raw(rec, 'GET download URL without auth', 'GET', u.pathname + u.search);
    await raw(rec, 'GET download URL with Range bytes=0-3', 'GET', u.pathname + u.search, { headers: { Range: 'bytes=0-3' } });
    u.searchParams.set('token', '00000000-0000-4000-8000-000000000000');
    await raw(rec, 'GET alt=media with a wrong token, no auth', 'GET', u.pathname + u.search);
    u.searchParams.delete('token');
    await raw(rec, 'GET alt=media without token and auth', 'GET', u.pathname + u.search);
    await raw(rec, 'GET alt=media with auth', 'GET', u.pathname + u.search, { headers: { Authorization: `Firebase ${A.idToken}` } });
    await raw(rec, 'GET metadata without auth', 'GET', OBJ(`${ROOT}/open/read/r.txt`));
    await raw(rec, 'GET metadata with Bearer instead of Firebase', 'GET', OBJ(`${ROOT}/open/read/r.txt`), { headers: { Authorization: `Bearer ${A.idToken}` } });
  }
  await sdkCall(rec, 'getMetadata of a missing object', () => getMetadata(ref(s, `${ROOT}/open/read/missing.txt`)), metaShape);
  await sdkCall(rec, 'getBytes of a missing object', () => getBytes(ref(s, `${ROOT}/open/read/missing.txt`)));
  await sdkCall(rec, 'getDownloadURL of a missing object', () => getDownloadURL(ref(s, `${ROOT}/open/read/missing.txt`)));
  rec.finish();
};

scenarios.sdk_metadata = async (A) => {
  const rec = new Recorder('sdk_metadata', 'updateMetadata semantics: set fields, add and remove custom keys with null, clear contentType, and what PATCH the SDK sends.');
  current = rec;
  const s = A.storage;
  const r = ref(s, `${ROOT}/open/meta/m.txt`);
  await sdkCall(rec, 'setup upload', () => uploadBytes(r, new TextEncoder().encode('m'), { contentType: 'text/plain', cacheControl: 'no-cache', customMetadata: { keep: '1', drop: '2' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'update cacheControl and add custom key', () => updateMetadata(r, { cacheControl: 'public, max-age=300', customMetadata: { added: '3' } }), metaShape);
  await sdkCall(rec, 'remove custom key with null', () => updateMetadata(r, { customMetadata: { drop: null } }), metaShape);
  await sdkCall(rec, 'clear cacheControl with null', () => updateMetadata(r, { cacheControl: null }), metaShape);
  await sdkCall(rec, 'change contentType and contentDisposition', () => updateMetadata(r, { contentType: 'application/json', contentDisposition: 'attachment' }), metaShape);
  await sdkCall(rec, 'empty update', () => updateMetadata(r, {}), metaShape);
  await sdkCall(rec, 'update of a missing object', () => updateMetadata(ref(s, `${ROOT}/open/meta/missing`), { cacheControl: 'x' }), metaShape);
  await raw(rec, 'raw PATCH: metadata null removes all custom metadata', 'PATCH', OBJ(`${ROOT}/open/meta/m.txt`), {
    headers: { Authorization: `Firebase ${A.idToken}`, 'Content-Type': 'application/json; charset=utf-8' }, body: JSON.stringify({ metadata: null }),
  });
  await raw(rec, 'raw PATCH: unknown and read-only fields', 'PATCH', OBJ(`${ROOT}/open/meta/m.txt`), {
    headers: { Authorization: `Firebase ${A.idToken}`, 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ size: 99, md5Hash: 'AAAA', generation: '1', name: 'other', bogus: 1 }),
  });
  await raw(rec, 'raw PATCH: invalid JSON', 'PATCH', OBJ(`${ROOT}/open/meta/m.txt`), {
    headers: { Authorization: `Firebase ${A.idToken}`, 'Content-Type': 'application/json; charset=utf-8' }, body: '{not json',
  });
  rec.finish();
};

scenarios.sdk_list = async (A) => {
  const rec = new Recorder('sdk_list', 'list with maxResults and pageToken, listAll, prefixes, nested folders, an empty prefix, and the raw query parameters.');
  current = rec;
  const s = A.storage;
  const names = ['a.txt', 'b.txt', 'c.txt', 'd/1.txt', 'd/2.txt', 'e/f/3.txt', 'g h.txt'];
  rec.begin('setup uploads');
  for (const n of names) await uploadBytes(ref(s, `${ROOT}/open/list/${n}`), bytes(1, 'x'));
  await sdkCall(rec, 'list maxResults=2', () => list(ref(s, `${ROOT}/open/list`), { maxResults: 2 }), listShape);
  let page = await sdkCall(rec, 'list maxResults=2 page 1 (again, to follow its token)', () => list(ref(s, `${ROOT}/open/list`), { maxResults: 2 }), listShape);
  let i = 2;
  while (page && page.nextPageToken && i < 10) {
    const tok = page.nextPageToken;
    page = await sdkCall(rec, `list maxResults=2 page ${i++}`, () => list(ref(s, `${ROOT}/open/list`), { maxResults: 2, pageToken: tok }), listShape);
  }
  await sdkCall(rec, 'listAll', () => listAll(ref(s, `${ROOT}/open/list`)), listShape);
  await sdkCall(rec, 'listAll of a sub folder d', () => listAll(ref(s, `${ROOT}/open/list/d`)), listShape);
  await sdkCall(rec, 'listAll of an empty prefix', () => listAll(ref(s, `${ROOT}/open/nothing-here`)), listShape);
  await sdkCall(rec, 'list maxResults=0 (SDK validation)', () => list(ref(s, `${ROOT}/open/list`), { maxResults: 0 }), listShape);
  await sdkCall(rec, 'list maxResults=1001 (SDK validation)', () => list(ref(s, `${ROOT}/open/list`), { maxResults: 1001 }), listShape);
  const H = { Authorization: `Firebase ${A.idToken}` };
  await raw(rec, 'raw list without delimiter (flat)', 'GET', `${B()}?prefix=${enc(`${ROOT}/open/list/`)}`, { headers: H });
  await raw(rec, 'raw list prefix without trailing slash', 'GET', `${B()}?prefix=${enc(`${ROOT}/open/list`)}&delimiter=%2F`, { headers: H });
  await raw(rec, 'raw list maxResults=1000', 'GET', `${B()}?prefix=${enc(`${ROOT}/open/list/`)}&delimiter=%2F&maxResults=1000`, { headers: H });
  await raw(rec, 'raw list maxResults=5000', 'GET', `${B()}?prefix=${enc(`${ROOT}/open/list/`)}&delimiter=%2F&maxResults=5000`, { headers: H });
  await raw(rec, 'raw list invalid pageToken', 'GET', `${B()}?prefix=${enc(`${ROOT}/open/list/`)}&delimiter=%2F&pageToken=garbage`, { headers: H });
  await raw(rec, 'raw list startOffset (GCS-only parameter)', 'GET', `${B()}?prefix=${enc(`${ROOT}/open/list/`)}&delimiter=%2F&startOffset=${enc(`${ROOT}/open/list/c`)}`, { headers: H });
  rec.finish();
};

scenarios.sdk_delete = async (A) => {
  const rec = new Recorder('sdk_delete', 'deleteObject, then reading and deleting it again.');
  current = rec;
  const s = A.storage;
  const r = ref(s, `${ROOT}/open/del/x.txt`);
  await sdkCall(rec, 'setup upload', () => uploadBytes(r, bytes(2)), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'deleteObject', () => deleteObject(r));
  await sdkCall(rec, 'getMetadata after delete', () => getMetadata(r), metaShape);
  await sdkCall(rec, 'deleteObject again', () => deleteObject(r));
  rec.finish();
};

scenarios.rules = async (A, Bu, anon) => {
  const rec = new Recorder('rules', 'Rules denials and allows (emulator/storage.rules): unauthenticated, size, contentType, custom metadata, another user\'s folder, list vs get, update and delete guarded by stored metadata, public read, and download tokens on a path the reader may not read.');
  current = rec;
  const sA = A.storage;
  const sB = Bu.storage;
  const sN = anon.storage;
  await sdkCall(rec, 'unauthenticated upload to open/', () => uploadBytes(ref(sN, `${ROOT}/open/r/anon.txt`), bytes(3)), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'unauthenticated resumable upload to open/ (700 KiB)', () => uploadBytesResumable(ref(sN, `${ROOT}/open/r/anon-big.txt`), bytes(700 * 1024)), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'limited: allowed (text, small, tag=ok)', () => uploadBytes(ref(sA, `${ROOT}/limited/ok.txt`), bytes(10), { contentType: 'text/plain', customMetadata: { tag: 'ok' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'limited: too big (2000 bytes)', () => uploadBytes(ref(sA, `${ROOT}/limited/big.txt`), bytes(2000), { contentType: 'text/plain', customMetadata: { tag: 'ok' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'limited: wrong contentType', () => uploadBytes(ref(sA, `${ROOT}/limited/img.png`), bytes(10), { contentType: 'image/png', customMetadata: { tag: 'ok' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'limited: missing tag', () => uploadBytes(ref(sA, `${ROOT}/limited/notag.txt`), bytes(10), { contentType: 'text/plain' }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'limited: resumable too big (300 KiB, denied at start or finalize?)', () => uploadBytesResumable(ref(sA, `${ROOT}/limited/big-resumable.txt`), bytes(300 * 1024), { contentType: 'text/plain', customMetadata: { tag: 'ok' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'limited: updateMetadata drops tag (update rule)', () => updateMetadata(ref(sA, `${ROOT}/limited/ok.txt`), { customMetadata: { tag: null } }), metaShape);
  await sdkCall(rec, 'limited: updateMetadata keeps tag', () => updateMetadata(ref(sA, `${ROOT}/limited/ok.txt`), { customMetadata: { other: 'x' } }), metaShape);
  await sdkCall(rec, 'users: A writes own folder', () => uploadBytes(ref(sA, `${ROOT}/users/${A.uid}/a.txt`), bytes(4)), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'users: B reads A\'s file', () => getMetadata(ref(sB, `${ROOT}/users/${A.uid}/a.txt`)), metaShape);
  await sdkCall(rec, 'users: B writes A\'s folder', () => uploadBytes(ref(sB, `${ROOT}/users/${A.uid}/b.txt`), bytes(4)), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'users: B reads a missing file in A\'s folder', () => getMetadata(ref(sB, `${ROOT}/users/${A.uid}/missing.txt`)), metaShape);
  await sdkCall(rec, 'users: A reads a missing file in own folder', () => getMetadata(ref(sA, `${ROOT}/users/${A.uid}/missing.txt`)), metaShape);
  await sdkCall(rec, 'nolist: write', () => uploadBytes(ref(sA, `${ROOT}/nolist/n.txt`), bytes(4)), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'nolist: get', () => getMetadata(ref(sA, `${ROOT}/nolist/n.txt`)), metaShape);
  await sdkCall(rec, 'nolist: list', () => list(ref(sA, `${ROOT}/nolist`)), listShape);
  await sdkCall(rec, 'list of the run root (no rule)', () => list(ref(sA, `${ROOT}`)), listShape);
  await sdkCall(rec, 'guarded: create locked, not deletable', () => uploadBytes(ref(sA, `${ROOT}/guarded/g.txt`), bytes(4), { customMetadata: { locked: 'yes' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'guarded: update a locked object', () => updateMetadata(ref(sA, `${ROOT}/guarded/g.txt`), { cacheControl: 'x' }), metaShape);
  await sdkCall(rec, 'guarded: overwrite a locked object (update)', () => uploadBytes(ref(sA, `${ROOT}/guarded/g.txt`), bytes(5)), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'guarded: delete without deletable', () => deleteObject(ref(sA, `${ROOT}/guarded/g.txt`)));
  await sdkCall(rec, 'guarded: create deletable', () => uploadBytes(ref(sA, `${ROOT}/guarded/d.txt`), bytes(4), { customMetadata: { deletable: 'yes' } }), (x) => metaShape(x.metadata));
  await sdkCall(rec, 'guarded: delete deletable', () => deleteObject(ref(sA, `${ROOT}/guarded/d.txt`)));
  await sdkCall(rec, 'guarded: delete missing object', () => deleteObject(ref(sA, `${ROOT}/guarded/none.txt`)));
  // Download tokens against rules: A owns a file B may not read.
  const url = await sdkCall(rec, 'users: A gets a download URL of own file', () => getDownloadURL(ref(sA, `${ROOT}/users/${A.uid}/a.txt`)));
  if (url) {
    const u = new URL(url);
    await raw(rec, 'download URL with no auth: token bypasses rules?', 'GET', u.pathname + u.search);
    await raw(rec, 'download URL with B\'s auth', 'GET', u.pathname + u.search, { headers: { Authorization: `Firebase ${Bu.idToken}` } });
    await raw(rec, 'token on the metadata URL (no alt=media), no auth', 'GET', `${u.pathname}?token=${u.searchParams.get('token')}`);
  }
  await sdkCall(rec, 'public: anonymous read of a missing object', () => getMetadata(ref(sN, `${ROOT}/public/none.txt`)), metaShape);
  await sdkCall(rec, 'public: A writes (denied)', () => uploadBytes(ref(sA, `${ROOT}/public/p.txt`), bytes(4)), (x) => metaShape(x.metadata));
  await raw(rec, 'raw: forged alg=none token', 'GET', OBJ(`${ROOT}/open/up/plain.txt`), {
    headers: { Authorization: `Firebase ${Buffer.from('{"alg":"none","typ":"JWT"}').toString('base64url')}.${Buffer.from(JSON.stringify({ sub: 'forged', user_id: 'forged', aud: cfg.projectId, iss: `https://securetoken.google.com/${cfg.projectId}`, iat: Math.floor(Date.now() / 1000), exp: Math.floor(Date.now() / 1000) + 3600 })).toString('base64url')}.` },
  });
  await raw(rec, 'raw: garbage token', 'GET', OBJ(`${ROOT}/open/up/plain.txt`), { headers: { Authorization: 'Firebase not-a-token' } });
  rec.finish();
};

scenarios.tokens = async (A) => {
  const rec = new Recorder('tokens', 'Download tokens: created on upload, the create_token and delete_token calls the console makes, several tokens, revoking by metadata update, and what getDownloadURL returns after.');
  current = rec;
  const s = A.storage;
  const p = `${ROOT}/open/tok/t.txt`;
  const H = { Authorization: `Firebase ${A.idToken}` };
  await sdkCall(rec, 'setup upload', () => uploadBytes(ref(s, p), new TextEncoder().encode('token test')), (x) => metaShape(x.metadata));
  await raw(rec, 'GET metadata (downloadTokens field)', 'GET', OBJ(p), { headers: H });
  await raw(rec, 'POST create_token=true as the user', 'POST', `${OBJ(p)}?create_token=true`, { headers: H });
  // The console creates and revokes tokens with the owner's OAuth token; the emulator's
  // stand-in for an admin is "Authorization: Bearer owner".
  const ADMIN = cfg.target === 'emulator' ? { Authorization: 'Bearer owner' } : {};
  const created = await raw(rec, 'POST create_token=true as admin', 'POST', `${OBJ(p)}?create_token=true`, { headers: ADMIN });
  let tokens = [];
  try { tokens = (created.json().downloadTokens || '').split(','); } catch { /* recorded */ }
  await sdkCall(rec, 'getDownloadURL with two tokens', () => getDownloadURL(ref(s, p)));
  if (tokens[0]) {
    await raw(rec, 'POST delete_token=<first> as the user', 'POST', `${OBJ(p)}?delete_token=${tokens[0]}`, { headers: H });
    await raw(rec, 'POST delete_token=<first> as admin', 'POST', `${OBJ(p)}?delete_token=${tokens[0]}`, { headers: ADMIN });
    await raw(rec, 'download with the deleted token', 'GET', `${OBJ(p)}?alt=media&token=${tokens[0]}`);
    if (tokens[1]) await raw(rec, 'download with the remaining token', 'GET', `${OBJ(p)}?alt=media&token=${tokens[1]}`);
    await sdkCall(rec, 'getDownloadURL after the revocation', () => getDownloadURL(ref(s, p)));
    await raw(rec, 'POST delete_token of an unknown token as admin', 'POST', `${OBJ(p)}?delete_token=nope`, { headers: ADMIN });
  }
  await raw(rec, 'POST create_token and delete_token together as admin', 'POST', `${OBJ(p)}?create_token=true&delete_token=x`, { headers: ADMIN });
  await raw(rec, 'POST create_token without auth', 'POST', `${OBJ(p)}?create_token=true`);
  await raw(rec, 'POST create_token=false', 'POST', `${OBJ(p)}?create_token=false`, { headers: H });
  await raw(rec, 'PATCH metadata.firebaseStorageDownloadTokens (client)', 'PATCH', OBJ(p), {
    headers: { ...H, 'Content-Type': 'application/json; charset=utf-8' }, body: JSON.stringify({ metadata: { firebaseStorageDownloadTokens: 'mine-1' } }),
  });
  await raw(rec, 'PATCH downloadTokens field (client)', 'PATCH', OBJ(p), {
    headers: { ...H, 'Content-Type': 'application/json; charset=utf-8' }, body: JSON.stringify({ downloadTokens: 'mine-2' }),
  });
  await raw(rec, 'GET metadata after the PATCHes', 'GET', OBJ(p), { headers: H });
  await raw(rec, 'download with token mine-1', 'GET', `${OBJ(p)}?alt=media&token=mine-1`);
  await raw(rec, 'download with token mine-2', 'GET', `${OBJ(p)}?alt=media&token=mine-2`);
  rec.finish();
};

scenarios.raw_resumable = async (A) => {
  const rec = new Recorder('raw_resumable', 'The client resumable protocol by hand: start, query, upload with offsets (right, overlapping, gap), finalize, query and upload after final, cancel, and a size mismatch on finalize.');
  current = rec;
  const H = { Authorization: `Firebase ${A.idToken}` };
  const name = `${ROOT}/open/raw/res.txt`;
  const start = await raw(rec, 'start', 'POST', `${B()}?name=${enc(name)}`, {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'start', 'X-Goog-Upload-Header-Content-Length': '20', 'X-Goog-Upload-Header-Content-Type': 'text/plain', 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ name, fullPath: name, contentType: 'text/plain', metadata: { r: '1' } }),
  });
  const url = start.res.headers.get('x-goog-upload-url');
  if (!url) { rec.note('no X-Goog-Upload-URL'); rec.finish(); return; }
  const u = new URL(url);
  const up = u.pathname + u.search;
  rec.note(`upload URL shape: ${redact(url).replace(/upload_id=[^&]+/, 'upload_id=<id>')}`);
  await raw(rec, 'query before any bytes', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'query' } });
  await raw(rec, 'upload 0..10', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload', 'X-Goog-Upload-Offset': '0' }, body: '0123456789' });
  await raw(rec, 'query after 10', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'query' } });
  await raw(rec, 'upload with a gap (offset 15)', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload', 'X-Goog-Upload-Offset': '15' }, body: 'xxxxx' });
  await raw(rec, 'upload overlapping (offset 5)', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload', 'X-Goog-Upload-Offset': '5' }, body: '56789' });
  await raw(rec, 'query after the bad offsets', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'query' } });
  await raw(rec, 'upload, finalize (rest)', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload, finalize', 'X-Goog-Upload-Offset': '10' }, body: 'abcdefghij' });
  await raw(rec, 'query after final', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'query' } });
  await raw(rec, 'upload after final', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload', 'X-Goog-Upload-Offset': '20' }, body: 'z' });
  await raw(rec, 'finalize again (empty)', 'POST', up, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload, finalize', 'X-Goog-Upload-Offset': '20' }, body: '' });
  // A second session: cancel.
  const s2 = await raw(rec, 'start (to cancel)', 'POST', `${B()}?name=${enc(`${ROOT}/open/raw/cancel.txt`)}`, {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'start', 'X-Goog-Upload-Header-Content-Length': '4', 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ name: `${ROOT}/open/raw/cancel.txt` }),
  });
  const u2 = new URL(s2.res.headers.get('x-goog-upload-url'));
  const up2 = u2.pathname + u2.search;
  await raw(rec, 'cancel', 'POST', up2, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'cancel' } });
  await raw(rec, 'query after cancel', 'POST', up2, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'query' } });
  await raw(rec, 'upload after cancel', 'POST', up2, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload, finalize', 'X-Goog-Upload-Offset': '0' }, body: 'abcd' });
  await raw(rec, 'GET the cancelled object', 'GET', OBJ(`${ROOT}/open/raw/cancel.txt`), { headers: H });
  // A third session: finalize with fewer bytes than announced.
  const s3 = await raw(rec, 'start (announce 10 bytes)', 'POST', `${B()}?name=${enc(`${ROOT}/open/raw/short.txt`)}`, {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'start', 'X-Goog-Upload-Header-Content-Length': '10', 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ name: `${ROOT}/open/raw/short.txt` }),
  });
  const u3 = new URL(s3.res.headers.get('x-goog-upload-url'));
  await raw(rec, 'upload, finalize with 4 bytes', 'POST', u3.pathname + u3.search, { headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'upload, finalize', 'X-Goog-Upload-Offset': '0' }, body: 'abcd' });
  await raw(rec, 'start with an unknown command', 'POST', `${B()}?name=${enc(`${ROOT}/open/raw/x.txt`)}`, {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'bogus' },
  });
  await raw(rec, 'query with an unknown upload_id', 'POST', `${B()}?name=${enc(name)}&upload_id=nope&upload_protocol=resumable`, {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'resumable', 'X-Goog-Upload-Command': 'query' },
  });
  rec.finish();
};

scenarios.raw_upload = async (A) => {
  const rec = new Recorder('raw_upload', 'Non-SDK upload shapes on the client API: a raw body with ?name= and no protocol header (REST style), multipart by hand, a name in the metadata only, a missing name, a bad multipart boundary, and an unknown bucket.');
  current = rec;
  const H = { Authorization: `Firebase ${A.idToken}` };
  await raw(rec, 'raw body, ?name=, Content-Type text/plain', 'POST', `${B()}?name=${enc(`${ROOT}/open/rawup/a.txt`)}`, { headers: { ...H, 'Content-Type': 'text/plain' }, body: 'raw body' });
  await raw(rec, 'raw body, ?uploadType=media&name=', 'POST', `${B()}?uploadType=media&name=${enc(`${ROOT}/open/rawup/m.txt`)}`, { headers: { ...H, 'Content-Type': 'text/plain' }, body: 'media body' });
  await raw(rec, 'raw body to the object URL (POST /o/<name>)', 'POST', OBJ(`${ROOT}/open/rawup/p.txt`), { headers: { ...H, 'Content-Type': 'text/plain' }, body: 'post to object' });
  const boundary = '--------aruma1234';
  const mp = (meta, data) => `--${boundary}\r\nContent-Type: application/json; charset=utf-8\r\n\r\n${JSON.stringify(meta)}\r\n--${boundary}\r\nContent-Type: text/plain\r\n\r\n${data}\r\n--${boundary}--`;
  await raw(rec, 'multipart by hand, name in metadata only', 'POST', B(), {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'multipart', 'Content-Type': `multipart/related; boundary=${boundary}` },
    body: mp({ name: `${ROOT}/open/rawup/mp.txt`, contentType: 'text/plain', metadata: { x: '1' } }, 'multipart data'),
  });
  await raw(rec, 'multipart, name in query and a different one in metadata', 'POST', `${B()}?name=${enc(`${ROOT}/open/rawup/q.txt`)}`, {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'multipart', 'Content-Type': `multipart/related; boundary=${boundary}` },
    body: mp({ name: `${ROOT}/open/rawup/meta-name.txt` }, 'which name wins'),
  });
  await raw(rec, 'multipart, wrong boundary', 'POST', `${B()}?name=${enc(`${ROOT}/open/rawup/bad.txt`)}`, {
    headers: { ...H, 'X-Goog-Upload-Protocol': 'multipart', 'Content-Type': 'multipart/related; boundary=other' },
    body: mp({ name: 'x' }, 'y'),
  });
  await raw(rec, 'no name anywhere', 'POST', B(), { headers: { ...H, 'Content-Type': 'text/plain' }, body: 'nameless' });
  await raw(rec, 'unknown bucket metadata GET', 'GET', `/v0/b/no-such-bucket-aruma.appspot.com/o/${enc('x')}`, { headers: H });
  await raw(rec, 'unknown bucket upload', 'POST', `/v0/b/no-such-bucket-aruma.appspot.com/o?name=x`, { headers: { ...H, 'Content-Type': 'text/plain' }, body: 'x' });
  await raw(rec, 'GET /v0/b/<bucket>/o/ with an empty name', 'GET', `${B()}/`, { headers: H });
  await raw(rec, 'OPTIONS preflight from a web origin', 'OPTIONS', OBJ(`${ROOT}/open/rawup/a.txt`), {
    headers: { Origin: 'https://app.example.com', 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'authorization,x-goog-upload-protocol,x-goog-upload-command,x-goog-upload-offset,content-type,x-firebase-storage-version,x-firebase-gmpid' },
  });
  await raw(rec, 'GET with an Origin header (CORS response headers)', 'GET', OBJ(`${ROOT}/open/rawup/a.txt`), { headers: { ...H, Origin: 'https://app.example.com' } });
  rec.finish();
};

// The Admin SDK path: firebase-admin 14.5.0 -> @google-cloud/storage 8.4.0 (GCS JSON API),
// pointed at the proxy with FIREBASE_STORAGE_EMULATOR_HOST.
scenarios.admin_node = async () => {
  const rec = new Recorder('admin_node', 'firebase-admin 14.5.0 (Node) through @google-cloud/storage 8.4.0 against the GCS JSON API of the server: save (small and 6 MiB resumable), exists, getMetadata, setMetadata, download (whole and range), getFiles with prefix/delimiter/pagination, copy, move, delete, getDownloadURL, and errors.');
  current = rec;
  process.env.FIREBASE_STORAGE_EMULATOR_HOST = PROXY;
  const { initializeApp: adminInit, deleteApp: adminDelete } = await import('firebase-admin/app');
  const { getStorage: adminStorage, getDownloadURL: adminDownloadURL } = await import('firebase-admin/storage');
  const app = adminInit({ projectId: cfg.projectId, storageBucket: cfg.bucket }, `admin-${Date.now()}`);
  const bucket = adminStorage(app).bucket();
  const P = `${ROOT}/admin`;
  const step = async (name, fn, shape = (v) => v) => {
    rec.begin(name);
    try {
      const v = await fn();
      rec.result({ ok: shape(v) });
      return v;
    } catch (e) {
      rec.result({ error: { name: e.name, code: e.code, message: e.message, errors: e.errors } });
      return undefined;
    }
  };
  const m0 = (r) => r[0];
  await step('file.save small (resumable: false)', () => bucket.file(`${P}/small.txt`).save('admin small', { resumable: false, contentType: 'text/plain', metadata: { metadata: { who: 'admin' } } }));
  await step('file.save small (default options)', () => bucket.file(`${P}/default.txt`).save('admin default'));
  await step('file.save 6 MiB (resumable)', () => bucket.file(`${P}/big.bin`).save(Buffer.alloc(6 * 1024 * 1024, 66), { contentType: 'application/octet-stream' }));
  await step('file.exists (present)', () => bucket.file(`${P}/small.txt`).exists(), m0);
  await step('file.exists (missing)', () => bucket.file(`${P}/missing.txt`).exists(), m0);
  await step('file.getMetadata', () => bucket.file(`${P}/small.txt`).getMetadata(), m0);
  await step('file.setMetadata (cacheControl, custom key, remove who)', () => bucket.file(`${P}/small.txt`).setMetadata({ cacheControl: 'no-store', metadata: { added: '1', who: null } }), m0);
  await step('file.download', () => bucket.file(`${P}/small.txt`).download(), (r) => r[0].toString());
  await step('file.download range 0-4', () => bucket.file(`${P}/small.txt`).download({ start: 0, end: 4 }), (r) => r[0].toString());
  await step('bucket.getFiles prefix, delimiter', () => bucket.getFiles({ prefix: `${P}/`, delimiter: '/', autoPaginate: false }), (r) => ({ files: r[0].map((f) => f.name), next: r[1], apiResponse: r[2] }));
  await step('bucket.getFiles maxResults=1', () => bucket.getFiles({ prefix: `${P}/`, maxResults: 1, autoPaginate: false }), (r) => ({ files: r[0].map((f) => f.name), next: r[1] }));
  await step('file.copy', () => bucket.file(`${P}/small.txt`).copy(`${P}/copy.txt`), (r) => r[0].name);
  await step('file.move', () => bucket.file(`${P}/copy.txt`).move(`${P}/moved.txt`), (r) => r[0].name);
  await step('getDownloadURL (admin)', () => adminDownloadURL(bucket.file(`${P}/small.txt`)));
  await step('getDownloadURL of a file without tokens (admin upload)', () => adminDownloadURL(bucket.file(`${P}/default.txt`)));
  await step('file.getMetadata missing', () => bucket.file(`${P}/missing.txt`).getMetadata(), m0);
  await step('file.delete missing', () => bucket.file(`${P}/missing.txt`).delete());
  await step('bucket.exists', () => bucket.exists(), m0);
  await step('bucket.getMetadata', () => bucket.getMetadata(), m0);
  await step('file.save with ifGenerationMatch=0 on an existing file', () => bucket.file(`${P}/small.txt`).save('again', { resumable: false, preconditionOpts: { ifGenerationMatch: 0 } }));
  // Admin bypasses rules: write where no client may.
  await step('file.save under public/ (rules forbid clients)', () => bucket.file(`${ROOT}/public/seed.txt`).save('seeded by admin', { resumable: false, contentType: 'text/plain' }));
  rec.begin('cleanup: bucket.deleteFiles prefix (recorded)');
  await bucket.deleteFiles({ prefix: `${P}/` }).catch((e) => rec.note(`deleteFiles: ${e.message}`));
  await adminDelete(app);
  rec.finish();
};

// @google-cloud/storage 8.4.0 used directly, the way an app reaches a non-Google endpoint:
// apiEndpoint plus useAuthWithCustomEndpoint and a fixed OAuth access token, so the URLs and
// the Authorization header are those a server sees outside emulator mode.
scenarios.admin_gcs_endpoint = async () => {
  const rec = new Recorder('admin_gcs_endpoint', '@google-cloud/storage 8.4.0 with apiEndpoint=http://<proxy> and useAuthWithCustomEndpoint=true (Authorization: Bearer <access-token>), no emulator variable: the URL forms and headers a server sees in that mode.');
  current = rec;
  delete process.env.FIREBASE_STORAGE_EMULATOR_HOST;
  delete process.env.STORAGE_EMULATOR_HOST;
  const { Storage } = await import('@google-cloud/storage');
  const { OAuth2Client } = await import('google-auth-library');
  const authClient = new OAuth2Client();
  authClient.setCredentials({ access_token: 'ya29.fake-access-token', expiry_date: Date.now() + 3600e3 });
  secret('ya29.fake-access-token', '<access-token>');
  const gcs = new Storage({ apiEndpoint: `http://${PROXY}`, useAuthWithCustomEndpoint: true, authClient, projectId: cfg.projectId });
  const bucket = gcs.bucket(cfg.bucket);
  const P = `${ROOT}/gcs`;
  const step = async (name, fn, shape = (v) => v) => {
    rec.begin(name);
    try {
      const v = await fn();
      rec.result({ ok: shape(v) });
      return v;
    } catch (e) {
      rec.result({ error: { name: e.name, code: e.code, message: e.message } });
      return undefined;
    }
  };
  const m0 = (r) => r[0];
  await step('file.save small (resumable: false)', () => bucket.file(`${P}/a.txt`).save('gcs a', { resumable: false, contentType: 'text/plain' }));
  await step('file.save small (default: resumable)', () => bucket.file(`${P}/b.txt`).save('gcs b'));
  await step('file.save 9 MiB chunkSize 4 MiB (resumable, chunked)', () => bucket.file(`${P}/big.bin`).save(Buffer.alloc(9 * 1024 * 1024, 67), { chunkSize: 4 * 1024 * 1024 }));
  await step('file.getMetadata', () => bucket.file(`${P}/a.txt`).getMetadata(), m0);
  await step('file.download', () => bucket.file(`${P}/a.txt`).download(), (r) => r[0].toString());
  await step('bucket.getFiles', () => bucket.getFiles({ prefix: `${P}/`, autoPaginate: false }), (r) => r[0].map((f) => f.name));
  await step('file.setMetadata', () => bucket.file(`${P}/a.txt`).setMetadata({ contentDisposition: 'attachment' }), m0);
  await step('file.delete', () => bucket.file(`${P}/a.txt`).delete());
  rec.begin('cleanup');
  await bucket.deleteFiles({ prefix: `${P}/` }).catch((e) => rec.note(`deleteFiles: ${e.message}`));
  rec.finish();
};

// The Admin SDK for Go (go-admin/, built beforehand; cfg.goAdmin is the binary) with
// FIREBASE_STORAGE_EMULATOR_HOST pointing at the proxy. Each line it prints is one step.
scenarios.admin_go = async () => {
  const rec = new Recorder('admin_go', 'firebase-admin-go 4.22.0 with cloud.google.com/go/storage 1.62.1 (go-admin/main.go) and FIREBASE_STORAGE_EMULATOR_HOST: Writer with ChunkSize 0, 16 MiB and 4 MiB (resumable over 3 chunks), Attrs, Update, readers, Objects with a delimiter, CopierFrom, a DoesNotExist precondition, deletes, bucket Attrs.');
  rec.doc.clients = { admin: 'firebase.google.com/go/v4 4.22.0 + cloud.google.com/go/storage 1.62.1 (Go 1.27.2)' };
  current = rec;
  if (!cfg.goAdmin) { rec.note('cfg.goAdmin not set: skipped'); rec.finish(); return; }
  const { spawn } = await import('node:child_process');
  const env = { ...process.env, FIREBASE_STORAGE_EMULATOR_HOST: PROXY };
  delete env.STORAGE_EMULATOR_HOST;
  delete env.GOOGLE_APPLICATION_CREDENTIALS;
  const child = spawn(cfg.goAdmin, [cfg.bucket, `${ROOT}/go`], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  let buf = '';
  rec.begin('(start)');
  child.stdout.on('data', (d) => {
    buf += d;
    let i;
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i);
      buf = buf.slice(i + 1);
      const j = JSON.parse(line);
      // The exchanges recorded so far belong to the step that just reported.
      rec.step.step = j.step;
      rec.step.result = j.error !== undefined ? { error: j.error } : { ok: j.ok };
      rec.begin('(next)');
    }
  });
  child.stderr.on('data', (d) => rec.note(`stderr: ${String(d).trim()}`));
  await new Promise((r) => child.on('exit', r));
  if (rec.step && rec.step.exchanges.length === 0) rec.doc.steps.pop();
  rec.finish();
};

scenarios.cleanup = async (A) => {
  const rec = new Recorder('cleanup', 'Deletes everything under aruma-ref/<run>/ with the admin path and checks the prefix is empty.');
  current = rec;
  process.env.FIREBASE_STORAGE_EMULATOR_HOST = PROXY;
  const { initializeApp: adminInit, deleteApp: adminDelete } = await import('firebase-admin/app');
  const { getStorage: adminStorage } = await import('firebase-admin/storage');
  const app = adminInit({ projectId: cfg.projectId, storageBucket: cfg.bucket }, `cleanup-${Date.now()}`);
  const bucket = adminStorage(app).bucket();
  rec.begin('deleteFiles');
  await bucket.deleteFiles({ prefix: `${ROOT}/` });
  rec.begin('list after');
  const [files] = await bucket.getFiles({ prefix: `${ROOT}/` });
  rec.result({ remaining: files.length });
  await adminDelete(app);
  rec.finish();
  if (files.length) throw new Error(`cleanup left ${files.length} objects`);
};

// ---------------------------------------------------------------- main
await new Promise((r) => proxy.listen(cfg.proxyPort, '127.0.0.1', r));
const A = await signUp('a');
const Bu = await signUp('b');
const anon = anonymousStorage();
const order = ['sdk_upload', 'sdk_read', 'sdk_metadata', 'sdk_list', 'sdk_delete', 'rules', 'tokens', 'raw_resumable', 'raw_upload', 'admin_node', 'admin_gcs_endpoint', 'admin_go', 'cleanup'];
try {
  for (const name of order) {
    if (ONLY.length && !ONLY.includes(name) && name !== 'cleanup') continue;
    console.log(`-- ${name}`);
    await scenarios[name](A, Bu, anon);
  }
} finally {
  current = null;
  for (const x of [A, Bu, anon]) { if (x.auth) await signOut(x.auth).catch(() => {}); await deleteApp(x.app).catch(() => {}); }
  proxy.close();
}
process.exit(0);
