// Shared plumbing for the Document Database production recorders: the access
// token, REST and gRPC calls to Cloud Firestore, and writing normalised corpus
// files together with the manifest.
//
// Auth: the access token of the firebase CLI login on this machine, obtained
// through firebase-tools' own auth module (lib/auth.js), exactly as
// test/oracle/rules/tools/rulesapi.mjs does. The token never leaves this
// module: it is not printed, logged or written, and travels only in the
// Authorization header (REST) or the authorization metadata entry (gRPC).
//
// Environment:
//   FIREBASE_TOOLS     firebase-tools package directory
//                      (default /opt/homebrew/lib/node_modules/firebase-tools)
//   DOCUMENTS_PROJECT  the project (default chatapp-8ae21)
//   FIRESTORE_PROTOS   a build/protos directory of @google-cloud/firestore;
//                      without it the gRPC half of a recording is skipped
import { createRequire } from 'node:module';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
export const ORACLE_DIR = resolve(HERE, '..');
export const CORPUS_DIR = join(ORACLE_DIR, 'corpus');
export const REPO_ROOT = resolve(ORACLE_DIR, '../../..');
export const FT = process.env.FIREBASE_TOOLS || '/opt/homebrew/lib/node_modules/firebase-tools';
export const PROJECT = process.env.DOCUMENTS_PROJECT || 'chatapp-8ae21';
export const ORIGIN = 'https://firestore.googleapis.com';
export const GRPC_HOST = 'firestore.googleapis.com:443';
export const DB = `projects/${PROJECT}/databases/(default)`;
export const ROOT = `${DB}/documents`;

const require = createRequire(import.meta.url);

// Every collection the recorders write to must start with this prefix.
export const PREFIX = 'aruma-ref';

let cached = null;

async function accessToken() {
  if (cached && cached.expires > Date.now() + 60_000) return cached.token;
  const auth = require(join(FT, 'lib/auth.js'));
  const scopes = require(join(FT, 'lib/scopes.js'));
  const account = auth.getGlobalDefaultAccount();
  if (!account) throw new Error('firebase CLI is not logged in');
  const want = [scopes.EMAIL, scopes.OPENID, scopes.CLOUD_PROJECTS_READONLY, scopes.FIREBASE_PLATFORM, scopes.CLOUD_PLATFORM];
  let t;
  for (let attempt = 0; ; attempt++) {
    try {
      t = await auth.getAccessToken(account.tokens.refresh_token, want);
      break;
    } catch (e) {
      // firebase-tools reports any failed refresh, a network one included, as
      // invalid credentials: try a few times before giving up.
      if (attempt >= 5) throw new Error('could not refresh the firebase CLI token');
      await new Promise((r) => setTimeout(r, 10_000 * (attempt + 1)));
    }
  }
  cached = { token: t.access_token, expires: t.expires_at || Date.now() + 30 * 60_000 };
  return cached.token;
}

// versions are the tool versions a recording names in its header.
export function versions(extra = {}) {
  const v = { node: process.version };
  try {
    v['firebase-tools (auth only)'] = JSON.parse(readFileSync(join(FT, 'package.json'), 'utf8')).version;
  } catch {}
  return { ...v, ...extra };
}

// rest calls the Firestore REST API (v1) and returns {http, body, text}; body is
// the parsed JSON, or {raw} when it is not JSON, and text the body as received.
// A request body is an object (sent as JSON), a string or a Buffer (sent as
// is). A path is relative to /v1/.
export async function rest(method, path, body, { origin = ORIGIN, auth = true } = {}) {
  const text = body === undefined ? undefined : typeof body === 'string' || Buffer.isBuffer(body) ? body : JSON.stringify(body);
  for (let attempt = 0; ; attempt++) {
    let res, out;
    try {
      const headers = { 'Content-Type': 'application/json' };
      if (auth) headers.Authorization = `Bearer ${origin === ORIGIN ? await accessToken() : 'owner'}`;
      res = await fetch(`${origin}/v1/${path}`, { method, headers, body: text, signal: AbortSignal.timeout(120_000) });
      out = await res.text();
    } catch (e) {
      if (attempt < 6) {
        await new Promise((r) => setTimeout(r, 3000 * (attempt + 1)));
        continue;
      }
      throw new Error(`${method} ${path}: ${e.message}`);
    }
    if ((res.status === 429 || res.status >= 500) && attempt < 4) {
      await new Promise((r) => setTimeout(r, 2000 * (attempt + 1)));
      continue;
    }
    let parsed;
    try {
      parsed = JSON.parse(out);
    } catch {
      parsed = { raw: out };
    }
    return { http: res.status, body: parsed, text: out };
  }
}

// ---- gRPC (google.firestore.v1.Firestore) ----

let grpcState;

// grpcAvailable loads @grpc/grpc-js and @grpc/proto-loader from firebase-tools'
// own node_modules and the Firestore protos from FIRESTORE_PROTOS.
export function grpcAvailable() {
  if (grpcState !== undefined) return grpcState !== null;
  grpcState = null;
  const protos = process.env.FIRESTORE_PROTOS;
  if (!protos || !existsSync(join(protos, 'google/firestore/v1/firestore.proto'))) return false;
  const grpc = require(join(FT, 'node_modules/@grpc/grpc-js'));
  const loader = require(join(FT, 'node_modules/@grpc/proto-loader'));
  const includeDirs = [protos, join(FT, 'node_modules/google-gax/build/protos')];
  const def = loader.loadSync('google/firestore/v1/firestore.proto', {
    includeDirs, keepCase: false, longs: String, enums: String, defaults: false, oneofs: false,
  });
  const pkg = grpc.loadPackageDefinition(def);
  const client = new pkg.google.firestore.v1.Firestore(GRPC_HOST, grpc.credentials.createSsl(), {
    'grpc.max_send_message_length': 32 << 20,
    'grpc.max_receive_message_length': 32 << 20,
  });
  const pv = (name) => {
    try {
      return JSON.parse(readFileSync(join(FT, 'node_modules', name, 'package.json'), 'utf8')).version;
    } catch {
      return 'unknown';
    }
  };
  let fsVersion = 'unknown';
  try {
    fsVersion = JSON.parse(readFileSync(join(protos, '../../package.json'), 'utf8')).version;
  } catch {}
  grpcState = { grpc, client, versions: { '@grpc/grpc-js': pv('@grpc/grpc-js'), '@grpc/proto-loader': pv('@grpc/proto-loader'), 'firestore protos': `@google-cloud/firestore ${fsVersion} build/protos` } };
  return true;
}

export function grpcVersions() {
  return grpcAvailable() ? grpcState.versions : {};
}

async function metadata(database) {
  const md = new grpcState.grpc.Metadata();
  md.set('authorization', `Bearer ${await accessToken()}`);
  md.set('google-cloud-resource-prefix', database);
  md.set('x-goog-request-params', `database=${encodeURIComponent(database)}`);
  return md;
}

function grpcError(err) {
  const names = grpcState.grpc.status;
  const name = Object.keys(names).find((k) => names[k] === err.code) || String(err.code);
  return { code: err.code, status: name, message: err.details };
}

// grpcUnary calls a unary method; returns {ok, response} or {ok:false, error}.
export async function grpcUnary(method, request, database = DB) {
  const md = await metadata(database);
  return new Promise((done) => {
    grpcState.client[method](request, md, { deadline: Date.now() + 120_000 }, (err, res) => {
      done(err ? { ok: false, error: grpcError(err) } : { ok: true, response: res });
    });
  });
}

// grpcRawUnary calls a unary method with a request already serialised to
// protobuf bytes (for a request proto-loader cannot encode, such as a string
// field that is not UTF-8); the response is decoded as usual.
export async function grpcRawUnary(method, bytes, database = DB) {
  const md = await metadata(database);
  const def = grpcState.client.constructor.service[method];
  return new Promise((done) => {
    grpcState.client.makeUnaryRequest(def.path, (b) => b, def.responseDeserialize, bytes, md, { deadline: Date.now() + 120_000 }, (err, res) => {
      done(err ? { ok: false, error: grpcError(err) } : { ok: true, response: res });
    });
  });
}

// grpcStream calls a server-streaming method and collects every response.
export async function grpcStream(method, request, database = DB) {
  const md = await metadata(database);
  return new Promise((done) => {
    const out = [];
    const call = grpcState.client[method](request, md, { deadline: Date.now() + 120_000 });
    call.on('data', (d) => out.push(d));
    call.on('error', (err) => done({ ok: false, responses: out, error: grpcError(err) }));
    call.on('end', () => done({ ok: true, responses: out }));
  });
}

// grpcListen opens a Listen stream, sends one request and returns what came
// back within waitMs: the responses, and the error if the server closed it.
export async function grpcListen(request, database = DB, waitMs = 8000) {
  const md = await metadata(database);
  return new Promise((done) => {
    const out = [];
    let finished = false;
    const call = grpcState.client.listen(md);
    const finish = (r) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      done(r);
    };
    const timer = setTimeout(() => {
      call.cancel();
      finish({ ok: true, responses: out, note: `still open after ${waitMs} ms; cancelled by the recorder` });
    }, waitMs);
    call.on('data', (d) => out.push(d));
    call.on('error', (err) => finish({ ok: false, responses: out, error: grpcError(err) }));
    call.on('end', () => finish({ ok: true, responses: out }));
    call.write(request);
  });
}

// grpcDuplex opens a bidirectional stream (listen or write) and returns the
// raw call; the caller writes requests and reads data, status and error events.
export async function grpcDuplex(method, database = DB) {
  const md = await metadata(database);
  return grpcState.client[method](md);
}

export function grpcCodeName(code) {
  const names = grpcState.grpc.status;
  return Object.keys(names).find((k) => names[k] === code) || String(code);
}

// ---- values ----

export const v = (x) => {
  if (x === null) return { nullValue: null };
  if (typeof x === 'boolean') return { booleanValue: x };
  if (typeof x === 'number') return Number.isInteger(x) ? { integerValue: String(x) } : { doubleValue: x };
  if (typeof x === 'string') return { stringValue: x };
  if (Array.isArray(x)) return { arrayValue: { values: x.map(v) } };
  if (x && x.$ref) return { referenceValue: x.$ref };
  if (x && x.$vector) return { mapValue: { fields: { __type__: { stringValue: '__vector__' }, value: { arrayValue: { values: x.$vector.map((d) => ({ doubleValue: d })) } } } } };
  return { mapValue: { fields: Object.fromEntries(Object.entries(x).map(([k, y]) => [k, v(y)])) } };
};
export const fields = (data) => v(data).mapValue.fields;

// toGrpc converts a REST JSON value tree to what proto-loader expects: the
// null value is the enum name, and int64 wrappers stay strings.
export function toGrpc(x) {
  if (Array.isArray(x)) return x.map(toGrpc);
  if (x && typeof x === 'object') {
    const o = {};
    for (const [k, y] of Object.entries(x)) o[k] = k === 'nullValue' ? 'NULL_VALUE' : toGrpc(y);
    return o;
  }
  return x;
}

// ---- normalising ----

// Volatile keys whose values change on every run; their values are replaced.
const VOLATILE = new Set(['readTime', 'updateTime', 'createTime', 'commitTime', 'executionDuration', 'expireTime', 'nextPageToken']);

// placeholder is what a volatile value is replaced with: its type, not its value.
function placeholder(key, val) {
  if (val && typeof val === 'object' && 'seconds' in val) return `<${key}: Timestamp>`;
  if (typeof val === 'string' && /^\d{4}-\d\d-\d\dT/.test(val)) return `<${key}: RFC 3339>`;
  if (typeof val === 'string' && /^\d+(\.\d+)?s$/.test(val)) return `<${key}: Duration>`;
  return `<${key}>`;
}

// normalise replaces volatile values (times, transaction ids, Buffers) so a
// re-recording differs only where behaviour differs. replace maps exact
// strings (such as a transaction id) to their placeholder anywhere they occur.
export function normalise(x, replace = new Map(), key = '') {
  if (Buffer.isBuffer(x)) {
    const b64 = x.toString('base64');
    return replace.get(b64) ?? `<bytes ${x.length}>`;
  }
  if (Array.isArray(x)) return x.map((y) => normalise(y, replace, key));
  if (x && typeof x === 'object') {
    const o = {};
    for (const [k, y] of Object.entries(x)) {
      if (VOLATILE.has(k) && y !== undefined && y !== null) o[k] = placeholder(k, y);
      else if (k === 'transaction' && typeof y === 'string') o[k] = replace.get(y) ?? '<transaction>';
      else o[k] = normalise(y, replace, k);
    }
    return o;
  }
  if (typeof x === 'string') {
    let s = x;
    for (const [from, to] of replace) if (from && s.includes(from)) s = s.split(from).join(to);
    return s;
  }
  return x;
}

// compact shortens the long strings of a request body (a 1 MiB field, a 6 KiB
// name) to their length and SHA-256, so a corpus file stays readable.
export function compact(x, limit = 300) {
  if (typeof x === 'string' && x.length > limit) {
    return `<string ${Buffer.byteLength(x)} bytes, sha256 ${createHash('sha256').update(x).digest('hex')}, starts ${JSON.stringify(x.slice(0, 40))}>`;
  }
  if (Array.isArray(x)) return x.map((y) => compact(y, limit));
  if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, compact(y, limit)]));
  return x;
}

// canonical writes JSON with sorted object keys, two-space indent and a final
// newline: the byte form the manifest hashes.
export function canonical(x) {
  const sort = (y) => {
    if (Array.isArray(y)) return y.map(sort);
    if (y && typeof y === 'object') return Object.fromEntries(Object.keys(y).sort().map((k) => [k, sort(y[k])]));
    return y;
  };
  return JSON.stringify(sort(x), null, 2) + '\n';
}

// ---- corpus and manifest ----

export const SCHEMA = 'aruma.documents-oracle/v1';

// GENERATOR_HASH is the header.versions key that holds the SHA-256 of the
// recorder script that wrote a file. oracle_test.go compares it with the script
// in the tree, so a recorder edited after its recording shows up as a failure
// until it is run again.
export const GENERATOR_HASH = 'generator sha256';

// generatorHash is the SHA-256 of a generator's script (its first word, a path
// from the repository root).
export function generatorHash(generator) {
  const script = generator.split(' ')[0];
  return createHash('sha256').update(readFileSync(join(REPO_ROOT, script))).digest('hex');
}

// writeCorpus writes corpus/<name> as {schema, header, ...body} and rebuilds
// the manifest. header must hold oracle, generator, recordedAt and versions;
// the generator's hash is added to versions.
export function writeCorpus(name, header, body) {
  for (const k of ['oracle', 'generator', 'recordedAt', 'versions']) {
    if (!header[k]) throw new Error(`${name}: header.${k} is missing`);
  }
  header = { ...header, versions: { ...header.versions, [GENERATOR_HASH]: generatorHash(header.generator) } };
  const file = join(CORPUS_DIR, name);
  const text = canonical({ schema: SCHEMA, header, ...body });
  // ADR-028 §7.5: every output is checked for an access token before it is saved.
  if (/ya29\.|Bearer [A-Za-z0-9._-]{20,}/.test(text)) throw new Error(`${name}: refusing to write what looks like an access token`);
  writeFileSync(file, text);
  process.stderr.write(`wrote ${relative(REPO_ROOT, file)}\n`);
  rebuildManifest();
}

// rebuildManifest hashes every corpus file and copies its header's oracle,
// generator, date and versions into manifest.json.
export function rebuildManifest() {
  const files = {};
  for (const n of readdirSync(CORPUS_DIR).sort()) {
    if (!n.endsWith('.json')) continue;
    const bytes = readFileSync(join(CORPUS_DIR, n));
    const { header } = JSON.parse(bytes.toString('utf8'));
    files[`corpus/${n}`] = {
      sha256: createHash('sha256').update(bytes).digest('hex'),
      oracle: header.oracle,
      generator: header.generator,
      recordedAt: header.recordedAt,
      versions: header.versions,
    };
  }
  const manifest = {
    schema: 'aruma.documents-oracle.manifest/v1',
    note: 'Generated by test/oracle/documents/production/lib.mjs (rebuildManifest). oracle_test.go checks every hash and that every corpus file is listed.',
    files,
  };
  writeFileSync(join(ORACLE_DIR, 'manifest.json'), canonical(manifest));
}

// assertScoped refuses any document or collection path outside the aruma-ref
// top-level collections, before a write leaves this machine.
export function assertScoped(name) {
  const m = /^projects\/[^/]+\/databases\/[^/]+\/documents\/(.*)$/.exec(name);
  const rel = m ? m[1] : name;
  if (!rel.startsWith(PREFIX)) throw new Error(`refusing to touch ${name.slice(0, 200)}: outside ${PREFIX}*`);
}

// assertMessageScoped walks a decoded stream message (a ListenResponse, a
// WriteResponse, or a request) and refuses any string that names a document or
// collection outside the aruma-ref top-level collections. It covers
// document_change, document_delete and document_remove, and every other string
// that holds a resource path, wherever it sits in the message.
export function assertMessageScoped(msg) {
  const walk = (x) => {
    if (typeof x === 'string') {
      if (/^projects\/[^/]+\/databases\/[^/]+\/documents\/./.test(x)) assertScoped(x);
    } else if (Array.isArray(x)) x.forEach(walk);
    else if (x && typeof x === 'object' && !Buffer.isBuffer(x)) Object.values(x).forEach(walk);
  };
  walk(msg);
}
