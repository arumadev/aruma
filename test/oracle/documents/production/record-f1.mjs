// F1 production recordings for the Document Database's unary document RPCs
// (plan section 6, F1: Get, BatchGet, List with showMissing, Create, Update,
// Delete and ListCollectionIds, over gRPC and REST):
//
//   f1-get.json                  GetDocument: mask, missing, read_time (the
//                                setup commit, before it, older than one hour,
//                                in the future, nanoseconds), transactions
//   f1-batch-get.json            BatchGetDocuments: found and missing, response
//                                order, mask, duplicates, transaction,
//                                newTransaction, read_time, bad names
//   f1-list-documents.json       ListDocuments: paging and the page token,
//                                orderBy forms accepted and refused, showMissing,
//                                mask, the empty collection id, read_time
//   f1-list-collection-ids.json  ListCollectionIds: paging and the page token
//   f1-create.json               CreateDocument: the automatic id (length and
//                                alphabet over many ids), an existing id, bad ids
//   f1-update.json               UpdateDocument (PATCH): with and without
//                                updateMask, currentDocument exists and
//                                updateTime, upsert
//   f1-delete.json               DeleteDocument: missing documents and failed
//                                preconditions
//   f1-write-results.json        update_time / create_time / commit_time /
//                                read_time precision on both transports, their
//                                relations, and when transformResults appears
//   f1-resource-names.json       names of another project or database inside a
//                                request, a resource prefix that disagrees with
//                                the body, and an invalid API key
//   f1-wire-errors.json          the REST front end's refusals: unknown fields,
//                                wrong JSON kinds, syntax errors, non-object roots,
//                                bad enum names, unknown query parameters, malformed
//                                and out-of-range timestamps, a value with no type,
//                                malformed database and project ids in the path
//                                and in gRPC's database field, and oneof conflicts
//                                (whether the "Root element" second line recurs)
//   f1-cleanup.json              the deletion of everything written, verified
//
// Each topic is recorded over REST (firestore.googleapis.com/v1) and, where
// FIRESTORE_PROTOS is set, over gRPC (google.firestore.v1.Firestore).
//
//   node record-f1.mjs
//
// Files hold a `setup` section (the commits written before the cases, names
// relative to {project}) and mark with `replay: true` the cases whose request
// is fixed and whose answer depends only on that setup:
// test/differential/documents replays exactly those against Aruma.
//
// Data is written ONLY under top-level collections whose id starts with
// aruma-ref-f1 (assertScoped refuses anything outside aruma-ref), every
// transaction opened read-write is rolled back, everything written is deleted
// at the end, and the deletion is verified by a kindless query over every
// collection id from "aruma-ref-f1" up to "aruma-ref-f2" and by showMissing
// lists of each collection used. A run whose cleanup is not verified writes no
// corpus file. Nothing creates or changes an index, field override, rule, TTL
// policy, user or project setting.
import {
  PROJECT, DB, ROOT, rest, v, fields, versions, normalise, compact, writeCorpus, assertScoped,
  grpcAvailable, grpcUnary, grpcStream, grpcVersions, toGrpc,
} from './lib.mjs';

const GENERATOR = 'test/oracle/documents/production/record-f1.mjs';
const MIN_ID = '__id-9223372036854775808__';
const G = 'aruma-ref-f1-get'; // Get and BatchGet fixtures
const L = 'aruma-ref-f1-list'; // ListDocuments fixtures
const LC = 'aruma-ref-f1-lcids'; // ListCollectionIds fixtures
const W = 'aruma-ref-f1-write'; // Create, Update, Delete
const A = 'aruma-ref-f1-auto'; // automatic ids
const NEVER = 'aruma-ref-f1-never'; // never written
const TOPS = [G, L, LC, W, A];
const OTHER_PROJECT = 'aruma-ref-no-such-project';
const OTHER_DB = `projects/${PROJECT}/databases/aruma-ref-nonexistent`;
const AUTO_IDS = 25;

const log = (s) => process.stderr.write(s);
const rel = (name) => (typeof name === 'string' ? name.replace(`${ROOT}/`, '') : name);
const relProject = (s) => s.split(`projects/${PROJECT}/`).join('projects/{project}/');
const g = grpcAvailable();

// ---- writes, tracked for the cleanup ----

const written = new Set();
const openTxns = new Set(); // read-write transactions still to roll back

async function commitWrites(writes) {
  for (const w of writes) assertScoped(w.update?.name ?? w.delete);
  return rest('POST', `${DB}/documents:commit`, { writes });
}

// setup writes fixtures by one commit per topic and returns {commit, raw}: the
// commit body with names relative to {project}, for replay, and the response.
async function setup(docs) {
  const writes = Object.entries(docs).map(([r, data]) => ({ update: { name: `${ROOT}/${r}`, fields: fields(data) } }));
  const res = await commitWrites(writes);
  if (res.http !== 200) throw new Error(`setup commit failed: HTTP ${res.http} ${JSON.stringify(res.body).slice(0, 300)}`);
  for (const w of writes) written.add(w.update.name);
  return { commit: JSON.parse(relProject(JSON.stringify({ writes }))), raw: res.body };
}

// ---- timestamps: precision across every response of the run ----

const TIME_KEYS = new Set(['readTime', 'updateTime', 'createTime', 'commitTime']);
const precision = { rest: {}, grpc: {} };

function collectTimes(x, transport) {
  if (Array.isArray(x)) return x.forEach((y) => collectTimes(y, transport));
  if (!x || typeof x !== 'object' || Buffer.isBuffer(x)) return;
  for (const [k, y] of Object.entries(x)) {
    if (!TIME_KEYS.has(k) || y == null) {
      collectTimes(y, transport);
      continue;
    }
    const p = (precision[transport][k] ??= { samples: 0 });
    p.samples++;
    if (typeof y === 'string') {
      const d = (/\.(\d+)Z$/.exec(y)?.[1] ?? '').length;
      p.fractionDigits ??= {};
      p.fractionDigits[d] = (p.fractionDigits[d] || 0) + 1;
      if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$/.test(y)) (p.otherForms ??= []).push(y.replace(/\d/g, '9'));
    } else if (typeof y === 'object') {
      const n = Number(y.nanos || 0);
      p.nanosMultipleOf1000 = (p.nanosMultipleOf1000 || 0) + (n % 1000 === 0 ? 1 : 0);
      p.nanosZero = (p.nanosZero || 0) + (n === 0 ? 1 : 0);
    }
  }
}

// rfcToGrpc turns an RFC 3339 time into a google.protobuf.Timestamp.
function rfcToGrpc(rfc) {
  const m = /^(.*?)(?:\.(\d+))?Z$/.exec(rfc);
  return { seconds: String(Math.floor(Date.parse(`${m[1]}Z`) / 1000)), nanos: m[2] ? Number(m[2].padEnd(9, '0')) : 0 };
}
// micros is an RFC 3339 time in microseconds since the epoch, as a string: the
// form of the "stored version" and "required base version" in production's
// FAILED_PRECONDITION message.
function micros(rfc) {
  const m = /^(.*?)(?:\.(\d+))?Z$/.exec(rfc);
  return String(BigInt(Date.parse(`${m[1]}Z`) / 1000) * 1000000n + BigInt((m[2] || '').padEnd(6, '0').slice(0, 6)));
}
const STALE = '2001-01-01T00:00:00.000001Z'; // a fixed precondition time, so its version stays in the corpus

// pinVersions checks the two versions of a "stored version (S) does not match
// the required base version (R)" message: S must be the document's updateTime
// in microseconds (0 for a missing document) and R the precondition's. A
// version that depends on the run is replaced by a placeholder; 0 and the
// version of STALE stay.
async function pinVersions(c, docName, sentRfc) {
  const re = /the stored version \((\d+)\) does not match the required base version \((\d+)\)/;
  const m = c.error && re.exec(c.error.message);
  if (!m) return;
  const cur = await rest('GET', docName);
  const stored = cur.http === 200 ? micros(cur.body.updateTime) : '0';
  c.versions = { storedIsDocumentUpdateTimeMicros: m[1] === stored, requiredIsPreconditionMicros: m[2] === micros(sentRfc) };
  const s = m[1] === '0' ? '0' : '<the document\'s updateTime in microseconds>';
  const r = sentRfc === STALE ? m[2] : '<the precondition updateTime in microseconds>';
  const fix = (t) => t.replace(m[0], `the stored version (${s}) does not match the required base version (${r})`);
  c.error.message = fix(c.error.message);
  const b = c.response.body;
  if (b && b.error) b.error.message = fix(b.error.message);
}
const minuteAgo = (min, sec = 0) => new Date(Math.floor((Date.now() - min * 60_000) / 60_000) * 60_000 + sec * 1000).toISOString();

// ---- recording helpers ----

// replaceAll applies a replace map (raw string to placeholder) to a string,
// also to the URL-encoded form of each raw string.
function replaceAll(s, replace) {
  for (const [from, to] of replace) {
    if (!from) continue;
    s = s.split(encodeURIComponent(from)).join(to).split(from).join(to);
  }
  return s;
}

// restCase sends one REST call and returns the case: the request (path with
// {project}, run-dependent values replaced), the HTTP status, the normalised
// body and, for an error, its status and exact message.
async function restCase(id, description, method, path, body, { replace = new Map(), replay = false, auth = true } = {}) {
  const r = await rest(method, path, body, { auth });
  collectTimes(r.body, 'rest');
  const arr = Array.isArray(r.body) ? r.body : [r.body];
  const err = arr.find((x) => x && x.error)?.error;
  const c = {
    id, description, transport: 'rest',
    request: { method, path: relProject(replaceAll(path, replace)) },
    response: { http: r.http, bodyIsArray: Array.isArray(r.body), body: normalise(r.body, replace) },
  };
  if (body !== undefined) c.request.body = JSON.parse(relProject(JSON.stringify(compact(normalise(body, replace)))));
  if (!auth) c.request.auth = 'none';
  if (err) c.error = { http: r.http, status: err.status, message: normalise(err.message, replace) };
  if (replay) c.replay = true;
  return { c, raw: r };
}

// grpcCase sends one gRPC call (unary or server streaming) and returns the case.
async function grpcCase(id, description, rpc, request, { stream = false, database = DB, replace = new Map() } = {}) {
  const r = stream ? await grpcStream(rpc, request, database) : await grpcUnary(rpc, request, database);
  collectTimes(stream ? r.responses : r.response, 'grpc');
  const c = {
    id, description, transport: 'grpc',
    request: { rpc: `google.firestore.v1.Firestore/${rpc[0].toUpperCase()}${rpc.slice(1)}`, message: compact(normalise(request, replace)) },
    response: r.ok
      ? { ok: true, [stream ? 'responses' : 'message']: normalise(stream ? r.responses : r.response, replace) }
      : { ok: false, ...(stream && r.responses.length ? { responses: normalise(r.responses, replace) } : {}) },
  };
  if (database !== DB) c.request.resourcePrefix = relProject(database);
  if (!r.ok) c.error = { code: r.error.code, status: r.error.status, message: normalise(r.error.message, replace) };
  return { c, raw: r };
}

// order lists a batchGet answer in response order: found names, or missing.
const order = (arr) => (Array.isArray(arr) ? arr : []).map((x) => (x.found ? `found ${rel(x.found.name)}` : x.missing ? `missing ${rel(x.missing)}` : x.transaction !== undefined ? 'transaction only' : Object.keys(x).join(',')));

// tokenShape describes a page token without keeping it: its length and
// alphabet, the length of what it decodes to, and the printable runs of the
// decoded bytes that name a resource (project id replaced).
function tokenShape(tok) {
  if (typeof tok !== 'string') return null;
  const alphabet = /^[A-Za-z0-9_-]+$/.test(tok) ? 'base64url, no padding' : /^[A-Za-z0-9+/]+={0,2}$/.test(tok) ? 'base64' : 'other';
  const bytes = Buffer.from(tok, alphabet === 'base64' ? 'base64' : 'base64url');
  const runs = (bytes.toString('latin1').match(/[\x20-\x7e]{3,}/g) || []).filter((s) => /aruma|projects/.test(s));
  return { length: tok.length, alphabet, decodedBytes: bytes.length, namedRuns: runs.map((s) => s.split(PROJECT).join('{project}')) };
}

// beginRest opens a REST transaction and returns its id and the case.
async function beginRest(id, options) {
  const { c, raw } = await restCase(id, `beginTransaction ${JSON.stringify(options)}`, 'POST', `${ROOT}:beginTransaction`, options ? { options } : {});
  const txn = raw.body.transaction;
  if (!txn) throw new Error(`${id}: no transaction: HTTP ${raw.http} ${JSON.stringify(raw.body).slice(0, 200)}`);
  if (!options || options.readWrite) openTxns.add(txn);
  return { c, txn };
}

async function rollback(txn) {
  if (!openTxns.has(txn)) return;
  await rest('POST', `${ROOT}:rollback`, { transaction: txn });
  openTxns.delete(txn);
}

// ---- fixtures ----

const GET_DOCS = {
  [`${G}/g1`]: { a: 1, b: { c: 2, d: 'x' }, s: 'one' },
  [`${G}/g2`]: { a: 2 },
  [`${G}/g3`]: { a: 3 },
  [`${G}/gm/sub/k`]: { v: 1 }, // gm is a missing document
};
const LIST_DOCS = {
  [`${L}/d1`]: { n: 3, s: 'c' },
  [`${L}/d2`]: { n: 1, s: 'a' },
  [`${L}/d3`]: { n: 2 },
  [`${L}/d4`]: { n: 1, s: 'b' },
  [`${L}/d5`]: { s: 'd' }, // no n
  [`${L}/d1/s1/x`]: { k: 1 },
  [`${L}/d1/s2/y`]: { k: 2 },
  [`${L}/m/sub/z`]: { k: 3 }, // m is a missing document
};
const LC_DOCS = Object.fromEntries([
  [`${LC}/p`, { a: 1 }],
  ...['c1', 'c2', 'c3', 'c4', 'c5'].map((c) => [`${LC}/p/${c}/x`, { a: 1 }]),
  [`${LC}/q/k1/x`, { a: 1 }], // q is a missing document
]);

// ---- Get ----

async function get(setupRaw) {
  const out = [];
  const g1 = `${ROOT}/${G}/g1`;
  const at = setupRaw.commitTime;
  const before = new Date(Date.parse(at) - 1000).toISOString();
  const nanos = at.replace(/\.(\d+)Z$/, (_, d) => `.${d.padEnd(6, '0').slice(0, 6)}789Z`);
  const times = new Map([[at, '<readTime: the setup commitTime>'], [before, '<readTime: one second before the setup>'], [nanos, '<readTime: the setup commitTime with 789 ns appended>']]);
  const q = (path, extra) => `${path}?${extra}`;
  const rc = async (id, d, path, o = {}) => out.push((await restCase(id, d, 'GET', path, undefined, o)).c);

  await rc('rest.found', 'GET a document', g1, { replay: true });
  await rc('rest.mask_one', 'mask.fieldPaths=a', q(g1, 'mask.fieldPaths=a'), { replay: true });
  await rc('rest.mask_nested', 'mask.fieldPaths=b.c', q(g1, 'mask.fieldPaths=b.c'), { replay: true });
  await rc('rest.mask_two', 'mask.fieldPaths=a and s', q(g1, 'mask.fieldPaths=a&mask.fieldPaths=s'), { replay: true });
  await rc('rest.mask_absent_field', 'mask naming a field the document does not have', q(g1, 'mask.fieldPaths=zz'), { replay: true });
  await rc('rest.mask_parent_and_child', 'mask.fieldPaths=b and b.c', q(g1, 'mask.fieldPaths=b&mask.fieldPaths=b.c'), { replay: true });
  await rc('rest.mask_empty_path', 'mask.fieldPaths= (empty)', q(g1, 'mask.fieldPaths='), { replay: true });
  await rc('rest.missing', 'GET a document that does not exist', `${ROOT}/${G}/nope`, { replay: true });
  await rc('rest.missing_with_subcollection', 'GET gm, a missing document with a subcollection', `${ROOT}/${G}/gm`, { replay: true });
  await rc('rest.missing_mask', 'GET a missing document with a mask', q(`${ROOT}/${G}/nope`, 'mask.fieldPaths=a'), { replay: true });

  // read_time
  await rc('rest.read_time.setup_commit', 'readTime = the setup commitTime', q(g1, `readTime=${encodeURIComponent(at)}`), { replace: times });
  await rc('rest.read_time.before_setup', 'readTime = one second before the setup commit (the document did not exist yet)', q(g1, `readTime=${encodeURIComponent(before)}`), { replace: times });
  await rc('rest.read_time.nanoseconds', 'readTime with nanoseconds that are not a whole microsecond', q(g1, `readTime=${encodeURIComponent(nanos)}`), { replace: times });
  const rt = [
    ['rest.read_time.59min_seconds', 'readTime 59 minutes ago, 17 s past the minute', minuteAgo(59, 17)],
    ['rest.read_time.61min_whole_minute', 'readTime 61 minutes ago, a whole minute', minuteAgo(61)],
    ['rest.read_time.2h_whole_minute', 'readTime two hours ago, a whole minute', minuteAgo(120)],
    ['rest.read_time.2h_seconds', 'readTime two hours ago, 30 s past the minute', minuteAgo(120, 30)],
    ['rest.read_time.8d_whole_minute', 'readTime eight days ago, a whole minute (beyond PITR\'s seven days)', minuteAgo(8 * 24 * 60)],
    ['rest.read_time.future', 'readTime one hour in the future', new Date(Date.now() + 3600_000).toISOString()],
    ['rest.read_time.epoch', 'readTime 1970-01-01T00:00:00Z', '1970-01-01T00:00:00Z'],
  ];
  for (const [id, d, t] of rt) {
    const label = `<readTime: ${id.split('.').pop()}>`;
    await rc(id, d, q(g1, `readTime=${encodeURIComponent(t)}`), { replace: new Map([[t, label]]) });
  }

  // transactions
  const ro = await beginRest('rest.txn.begin_read_only', { readOnly: {} });
  out.push(ro.c);
  const rw = await beginRest('rest.txn.begin_read_write', { readWrite: {} });
  out.push(rw.c);
  const roAt = await beginRest('rest.txn.begin_read_only_read_time', { readOnly: { readTime: at } });
  out.push(roAt.c);
  const tr = new Map([[ro.txn, '<the read-only transaction>'], [rw.txn, '<the read-write transaction>'], [roAt.txn, '<the read-only transaction at the setup commitTime>'], ...times]);
  await rc('rest.txn.read_only', 'GET in a read-only transaction', q(g1, `transaction=${encodeURIComponent(ro.txn)}`), { replace: tr });
  await rc('rest.txn.read_only_missing', 'GET a missing document in a read-only transaction', q(`${ROOT}/${G}/nope`, `transaction=${encodeURIComponent(ro.txn)}`), { replace: tr });
  await rc('rest.txn.read_write', 'GET in a read-write transaction', q(g1, `transaction=${encodeURIComponent(rw.txn)}`), { replace: tr });
  await rc('rest.txn.read_write_missing', 'GET a missing document in a read-write transaction', q(`${ROOT}/${G}/nope`, `transaction=${encodeURIComponent(rw.txn)}`), { replace: tr });
  await rc('rest.txn.read_only_at_read_time', 'GET in a read-only transaction opened at the setup commitTime', q(g1, `transaction=${encodeURIComponent(roAt.txn)}`), { replace: tr });
  await rc('rest.txn.and_read_time', 'GET with both transaction and readTime', q(g1, `transaction=${encodeURIComponent(ro.txn)}&readTime=${encodeURIComponent(at)}`), { replace: tr });
  await rc('rest.txn.not_base64', 'GET with transaction=%%% (not base64)', q(g1, 'transaction=%25%25%25'));
  await rollback(rw.txn);
  out.push((await restCase('rest.txn.read_only_rollback', 'rollback of a read-only transaction', 'POST', `${ROOT}:rollback`, { transaction: ro.txn }, { replace: tr })).c);

  if (g) {
    const gc = async (id, d, req, o = {}) => out.push((await grpcCase(id, d, 'getDocument', req, o)).c);
    await gc('grpc.found', 'GetDocument', { name: g1 });
    await gc('grpc.mask_one', 'GetDocument mask [a]', { name: g1, mask: { fieldPaths: ['a'] } });
    await gc('grpc.mask_empty_list', 'GetDocument mask {fieldPaths: []}', { name: g1, mask: { fieldPaths: [] } });
    await gc('grpc.missing', 'GetDocument of a document that does not exist', { name: `${ROOT}/${G}/nope` });
    await gc('grpc.read_time.setup_commit', 'GetDocument readTime = the setup commitTime', { name: g1, readTime: rfcToGrpc(at) });
    await gc('grpc.read_time.2h_whole_minute', 'GetDocument readTime two hours ago, a whole minute', { name: g1, readTime: rfcToGrpc(minuteAgo(120)) });
    await gc('grpc.read_time.2h_seconds', 'GetDocument readTime two hours ago, 30 s past the minute', { name: g1, readTime: rfcToGrpc(minuteAgo(120, 30)) });
    await gc('grpc.read_time.future', 'GetDocument readTime one hour in the future', { name: g1, readTime: rfcToGrpc(new Date(Date.now() + 3600_000).toISOString()) });
    const b = await grpcUnary('beginTransaction', { database: DB, options: { readOnly: {} } });
    const t = b.response.transaction;
    const gtr = new Map([[t.toString('base64'), '<the read-only transaction>']]);
    await gc('grpc.txn.read_only', 'GetDocument in a read-only transaction', { name: g1, transaction: t }, { replace: gtr });
    await gc('grpc.txn.read_only_missing', 'GetDocument of a missing document in a read-only transaction', { name: `${ROOT}/${G}/nope`, transaction: t }, { replace: gtr });
  }
  return {
    cases: out,
    notes: [
      'readTime values are relative to the run: the setup commitTime, or minutes before the request; the request path names which.',
      `${G}/gm is never written: it exists only as the parent of gm/sub/k.`,
    ],
  };
}

// ---- BatchGet ----

async function batchGet(setupRaw) {
  const out = [];
  const n = (r) => `${ROOT}/${G}/${r}`;
  const at = setupRaw.commitTime;
  const times = new Map([[at, '<readTime: the setup commitTime>']]);
  const bg = async (id, d, body, o = {}) => {
    const { c, raw } = await restCase(id, d, 'POST', `${ROOT}:batchGet`, body, o);
    c.order = order(raw.body);
    out.push(c);
    return raw;
  };
  const mixed = [n('g3'), n('nope1'), n('g1'), n('nope2'), n('g2')];
  for (const i of [1, 2, 3]) await bg(`rest.order_run${i}`, `g3, nope1, g1, nope2, g2 (run ${i} of 3, to see whether the order is stable)`, { documents: mixed }, { replay: true });
  await bg('rest.found_only', 'g1, g2', { documents: [n('g1'), n('g2')] }, { replay: true });
  await bg('rest.missing_only', 'nope', { documents: [n('nope')] }, { replay: true });
  await bg('rest.mask', 'g1, g2 and nope with mask [a]', { documents: [n('g1'), n('g2'), n('nope')], mask: { fieldPaths: ['a'] } }, { replay: true });
  await bg('rest.mask_empty_list', 'g1 with mask {fieldPaths: []}', { documents: [n('g1')], mask: { fieldPaths: [] } }, { replay: true });
  await bg('rest.duplicates', 'g1, g1, nope, nope', { documents: [n('g1'), n('g1'), n('nope'), n('nope')] }, { replay: true });
  await bg('rest.empty_list', 'documents: []', { documents: [] }, { replay: true });
  await bg('rest.no_documents_field', '{} (no documents field)', {}, { replay: true });
  await bg('rest.missing_with_subcollection', 'gm, a missing document with a subcollection', { documents: [n('gm')] }, { replay: true });
  await bg('rest.collection_name', 'a collection path instead of a document', { documents: [`${ROOT}/${G}`] }, { replay: true });
  await bg('rest.relative_name', 'a name without projects/…/documents/', { documents: [`${G}/g1`] }, { replay: true });
  await bg('rest.read_time', 'g1, nope at readTime = the setup commitTime', { documents: [n('g1'), n('nope')], readTime: at }, { replace: times });
  await bg('rest.read_time_2h', 'g1 at readTime two hours ago, a whole minute', { documents: [n('g1')], readTime: minuteAgo(120) });

  const ro = await beginRest('rest.txn.begin_read_only', { readOnly: {} });
  out.push(ro.c);
  const tr = new Map([[ro.txn, '<the read-only transaction>'], ...times]);
  await bg('rest.txn.read_only', 'g1, nope in a read-only transaction', { documents: [n('g1'), n('nope')], transaction: ro.txn }, { replace: tr });
  const nro = await bg('rest.new_transaction.read_only', 'g1, nope, g2 with newTransaction {readOnly: {}}', { documents: [n('g1'), n('nope'), n('g2')], newTransaction: { readOnly: {} } });
  const nrw = await bg('rest.new_transaction.read_write', 'g1, nope, g2 with newTransaction {readWrite: {}}', { documents: [n('g1'), n('nope'), n('g2')], newTransaction: { readWrite: {} } });
  const rwTxn = (Array.isArray(nrw.body) ? nrw.body : []).find((x) => x.transaction)?.transaction;
  if (rwTxn) openTxns.add(rwTxn);
  const newTxnFlags = (raw) => (Array.isArray(raw.body) ? raw.body : []).map((x, i) => ({ index: i, hasTransaction: 'transaction' in x, hasResult: Boolean(x.found || x.missing) }));
  out.at(-2).transactionIn = newTxnFlags(nro);
  out.at(-1).transactionIn = newTxnFlags(nrw);
  await bg('rest.new_transaction.empty_list', 'documents [] with newTransaction {readOnly: {}}', { documents: [], newTransaction: { readOnly: {} } });
  await bg('rest.txn.and_new_transaction', 'both transaction and newTransaction', { documents: [n('g1')], transaction: ro.txn, newTransaction: { readOnly: {} } }, { replace: tr });
  await bg('rest.txn.and_read_time', 'both transaction and readTime', { documents: [n('g1')], transaction: ro.txn, readTime: at }, { replace: tr });
  if (rwTxn) await rollback(rwTxn);

  if (g) {
    const gb = async (id, d, req, o = {}) => {
      const { c, raw } = await grpcCase(id, d, 'batchGetDocuments', { database: DB, ...req }, { stream: true, ...o });
      c.order = order(raw.responses);
      out.push(c);
      return raw;
    };
    await gb('grpc.order', 'g3, nope1, g1, nope2, g2', { documents: mixed });
    await gb('grpc.duplicates', 'g1, g1, nope, nope', { documents: [n('g1'), n('g1'), n('nope'), n('nope')] });
    await gb('grpc.mask', 'g1, nope with mask [a]', { documents: [n('g1'), n('nope')], mask: { fieldPaths: ['a'] } });
    await gb('grpc.empty_list', 'documents: []', { documents: [] });
    const r = await gb('grpc.new_transaction.read_only', 'g1, nope, g2 with newTransaction readOnly', { documents: [n('g1'), n('nope'), n('g2')], newTransaction: { readOnly: {} } });
    const t = (r.responses || []).find((x) => x.transaction && x.transaction.length)?.transaction;
    out.at(-1).transactionIn = (r.responses || []).map((x, i) => ({ index: i, hasTransaction: Boolean(x.transaction && x.transaction.length), hasResult: Boolean(x.found || x.missing) }));
    if (t) {
      await gb('grpc.txn.read_only', 'g1, nope in that read-only transaction', { documents: [n('g1'), n('nope')], transaction: t }, { replace: new Map([[t.toString('base64'), '<the read-only transaction>']]) });
    }
  }
  return {
    cases: out,
    notes: [
      'order lists each response element in the order production sent it: "found <name>", "missing <name>", or "transaction only" for an element that carries only the new transaction.',
      'transactionIn lists, for a newTransaction request, which response elements carry the transaction id and which a result.',
    ],
  };
}

// ---- ListDocuments ----

async function listDocuments(setupRaw) {
  const out = [];
  const base = `${ROOT}/${L}`;
  const at = setupRaw.commitTime;
  const times = new Map([[at, '<readTime: the setup commitTime>']]);
  const names = (raw) => (raw.body.documents || []).map((d) => rel(d.name));
  const lc = async (id, d, query, o = {}) => {
    const { c, raw } = await restCase(id, d, 'GET', query ? `${base}?${query}` : base, undefined, o);
    c.documents = names(raw);
    if (raw.body.nextPageToken) c.nextPageTokenShape = tokenShape(raw.body.nextPageToken);
    out.push(c);
    return raw;
  };

  await lc('rest.default', 'ListDocuments with no parameters', '', { replay: true });
  await lc('rest.page_size_0', 'pageSize=0', 'pageSize=0', { replay: true });
  await lc('rest.page_size_negative', 'pageSize=-1', 'pageSize=-1', { replay: true });
  await lc('rest.page_size_huge', 'pageSize=100000', 'pageSize=100000', { replay: true });
  await lc('rest.page_size_not_number', 'pageSize=two', 'pageSize=two', { replay: true });

  // Paging: follow the token to the end with pageSize 2.
  const chain = async (prefix, d, extra) => {
    let token = '';
    const toks = new Map();
    for (let page = 1; page <= 6; page++) {
      const query = [extra, 'pageSize=2', token && `pageToken=${encodeURIComponent(token)}`].filter(Boolean).join('&');
      const raw = await lc(`${prefix}.page${page}`, `${d}, page ${page}${page > 1 ? ' (pageToken from the page before)' : ''}`, query, { replace: toks });
      token = raw.body.nextPageToken;
      if (!token) break;
      toks.set(token, `<nextPageToken of page ${page}>`);
    }
    return toks;
  };
  const toks = await chain('rest.paging', 'pageSize=2', '');
  const firstToken = [...toks.keys()][0];
  await chain('rest.paging_order_by_n_desc', 'orderBy=n desc, pageSize=2', `orderBy=${encodeURIComponent('n desc')}`);
  await chain('rest.paging_show_missing', 'showMissing=true, pageSize=2', 'showMissing=true');
  if (firstToken) {
    const tk = new Map([[firstToken, '<nextPageToken of page 1 of rest.paging>']]);
    await lc('rest.token.other_page_size', 'the page-1 token of rest.paging with pageSize=3', `pageSize=3&pageToken=${encodeURIComponent(firstToken)}`, { replace: tk });
    await lc('rest.token.no_page_size', 'the page-1 token of rest.paging with no pageSize', `pageToken=${encodeURIComponent(firstToken)}`, { replace: tk });
    await lc('rest.token.other_order_by', 'the page-1 token of rest.paging with orderBy=n', `pageSize=2&orderBy=n&pageToken=${encodeURIComponent(firstToken)}`, { replace: tk });
    await lc('rest.token.with_show_missing', 'the page-1 token of rest.paging with showMissing=true', `pageSize=2&showMissing=true&pageToken=${encodeURIComponent(firstToken)}`, { replace: tk });
    const { c } = await restCase('rest.token.other_collection', `the page-1 token of rest.paging on ${NEVER}`, 'GET', `${ROOT}/${NEVER}?pageSize=2&pageToken=${encodeURIComponent(firstToken)}`, undefined, { replace: tk });
    out.push(c);
  }
  await lc('rest.token.garbage', 'pageToken=abc', 'pageSize=2&pageToken=abc', { replay: true });
  await lc('rest.token.empty', 'pageToken= (empty)', 'pageSize=2&pageToken=', { replay: true });

  // orderBy forms.
  const forms = [
    '__name__', '__name__ asc', '__name__ desc', '__name__ DESC', '__name__ ASC', 'n', 'n asc', 'n desc', 'n desc, __name__ desc',
    'n,__name__', 'n desc,__name__', 'n, __name__ desc', 's', 's desc', 'n,s', 'b.c', '`n`', '__name__,n', '__name__ desc,n',
    '', ' ', 'n asc asc', 'n descending', 'n,', ',n', 'foo bar', 'n desc desc', 'a-b', '`a-b`', 'n,n',
  ];
  for (const [i, f] of forms.entries()) {
    await lc(`rest.order_by.${String(i + 1).padStart(2, '0')}`, `orderBy=${JSON.stringify(f)}`, `orderBy=${encodeURIComponent(f)}`, { replay: true });
  }

  await lc('rest.show_missing', 'showMissing=true', 'showMissing=true', { replay: true });
  await lc('rest.show_missing_order_by_name_desc', 'showMissing=true&orderBy=__name__ desc', `showMissing=true&orderBy=${encodeURIComponent('__name__ desc')}`, { replay: true });
  await lc('rest.mask', 'mask.fieldPaths=n', 'mask.fieldPaths=n', { replay: true });
  await lc('rest.mask_absent', 'mask.fieldPaths=zz', 'mask.fieldPaths=zz', { replay: true });
  await lc('rest.mask_show_missing', 'mask.fieldPaths=n&showMissing=true', 'mask.fieldPaths=n&showMissing=true', { replay: true });
  await lc('rest.read_time', 'readTime = the setup commitTime', `readTime=${encodeURIComponent(at)}`, { replace: times });
  const twoHours = minuteAgo(120);
  await lc('rest.read_time_2h', 'readTime two hours ago, a whole minute', `readTime=${encodeURIComponent(twoHours)}`, { replace: new Map([[twoHours, '<readTime: 2h_whole_minute>']]) });
  const ro = await beginRest('rest.txn.begin_read_only', { readOnly: {} });
  out.push(ro.c);
  await lc('rest.txn.read_only', 'in a read-only transaction', `transaction=${encodeURIComponent(ro.txn)}`, { replace: new Map([[ro.txn, '<the read-only transaction>']]) });

  const other = async (id, d, path, o = {}) => {
    const { c, raw } = await restCase(id, d, 'GET', path, undefined, o);
    c.documents = names(raw);
    out.push(c);
  };
  await other('rest.never_written', `ListDocuments of ${NEVER}`, `${ROOT}/${NEVER}`, { replay: true });
  await other('rest.subcollection', `ListDocuments of ${L}/d1/s1`, `${ROOT}/${L}/d1/s1`, { replay: true });
  await other('rest.subcollection_of_missing', `ListDocuments of ${L}/m/sub`, `${ROOT}/${L}/m/sub`, { replay: true });
  await other('rest.trailing_slash_document', `GET ${L}/d1/ (a document path with a trailing slash: an empty collection id?)`, `${ROOT}/${L}/d1/`, { replay: true });
  await other('rest.trailing_slash_collection', `GET ${L}/ (a collection path with a trailing slash)`, `${ROOT}/${L}/`, { replay: true });

  if (g) {
    const gl = async (id, d, req, o = {}) => {
      const { c, raw } = await grpcCase(id, d, 'listDocuments', { parent: ROOT, collectionId: L, ...req }, o);
      c.documents = raw.ok ? (raw.response.documents || []).map((x) => rel(x.name)) : [];
      if (raw.ok && raw.response.nextPageToken) c.nextPageTokenShape = tokenShape(raw.response.nextPageToken);
      out.push(c);
      return raw;
    };
    await gl('grpc.default', 'ListDocuments', {});
    let token = '';
    const gt = new Map();
    for (let page = 1; page <= 6; page++) {
      const r = await gl(`grpc.paging.page${page}`, `pageSize 2, page ${page}`, { pageSize: 2, ...(token ? { pageToken: token } : {}) }, { replace: gt });
      token = r.ok && r.response.nextPageToken;
      if (!token) break;
      gt.set(token, `<nextPageToken of page ${page}>`);
    }
    await gl('grpc.order_by_n_desc', 'orderBy "n desc"', { orderBy: 'n desc' });
    await gl('grpc.order_by_bad', 'orderBy "foo bar"', { orderBy: 'foo bar' });
    await gl('grpc.mask', 'mask [n]', { mask: { fieldPaths: ['n'] } });
    await gl('grpc.token_garbage', 'pageToken "abc"', { pageSize: 2, pageToken: 'abc' });
    await gl('grpc.page_size_negative', 'pageSize -1', { pageSize: -1 });
    await gl('grpc.empty_collection_id', `parent ${L}/d1, collectionId "" (every subcollection of d1)`, { parent: `${ROOT}/${L}/d1`, collectionId: '' });
    await gl('grpc.empty_collection_id_show_missing', `parent ${L}/d1, collectionId "", showMissing true`, { parent: `${ROOT}/${L}/d1`, collectionId: '', showMissing: true });
    await gl('grpc.empty_collection_id_order_by_name_desc', `parent ${L}/d1, collectionId "", orderBy "__name__ desc"`, { parent: `${ROOT}/${L}/d1`, collectionId: '', orderBy: '__name__ desc' });
    await gl('grpc.empty_collection_id_paging', `parent ${L}/d1, collectionId "", pageSize 1`, { parent: `${ROOT}/${L}/d1`, collectionId: '', pageSize: 1 });
    await gl('grpc.empty_collection_id_missing_parent', `parent ${L}/m, collectionId ""`, { parent: `${ROOT}/${L}/m`, collectionId: '' });
    await gl('grpc.read_time_2h', 'readTime two hours ago, a whole minute', { readTime: rfcToGrpc(minuteAgo(120)) });
  }
  return {
    cases: out,
    notes: [
      'documents lists the names returned, relative to the database root, in the order returned.',
      'nextPageTokenShape describes a page token without keeping it: its length, alphabet, decoded length, and the printable runs of its decoded bytes that name a resource. The token itself is replaced by a placeholder naming the page it came from.',
      `${L}/m is never written: it exists only as the parent of m/sub/z. ${L}/d5 has no field n.`,
    ],
  };
}

// ---- ListCollectionIds ----

async function listCollectionIds(setupRaw) {
  const out = [];
  const at = setupRaw.commitTime;
  const p = `${ROOT}/${LC}/p`;
  const lc = async (id, d, parent, body, o = {}) => {
    const { c, raw } = await restCase(id, d, 'POST', `${parent}:listCollectionIds`, body, o);
    c.collectionIds = raw.body.collectionIds || [];
    if (raw.body.nextPageToken) c.nextPageTokenShape = tokenShape(raw.body.nextPageToken);
    out.push(c);
    return raw;
  };
  await lc('rest.default', `listCollectionIds of ${LC}/p (five subcollections)`, p, {}, { replay: true });
  await lc('rest.page_size_0', 'pageSize 0', p, { pageSize: 0 }, { replay: true });
  await lc('rest.page_size_negative', 'pageSize -1', p, { pageSize: -1 }, { replay: true });
  let token = '';
  const toks = new Map();
  for (let page = 1; page <= 5; page++) {
    const raw = await lc(`rest.paging.page${page}`, `pageSize 2, page ${page}`, p, { pageSize: 2, ...(token ? { pageToken: token } : {}) }, { replace: toks });
    token = raw.body.nextPageToken;
    if (!token) break;
    toks.set(token, `<nextPageToken of page ${page}>`);
  }
  const first = [...toks.keys()][0];
  if (first) {
    const tk = new Map([[first, '<nextPageToken of page 1 of rest.paging>']]);
    await lc('rest.token.other_parent', `the page-1 token of ${LC}/p used on ${LC}/q`, `${ROOT}/${LC}/q`, { pageSize: 2, pageToken: first }, { replace: tk });
    await lc('rest.token.other_page_size', 'the page-1 token with pageSize 3', p, { pageSize: 3, pageToken: first }, { replace: tk });
  }
  await lc('rest.token.garbage', 'pageToken "abc"', p, { pageSize: 2, pageToken: 'abc' }, { replay: true });
  await lc('rest.missing_parent', `${LC}/q, a missing document with one subcollection`, `${ROOT}/${LC}/q`, {}, { replay: true });
  await lc('rest.never_written_parent', `${LC}/never`, `${ROOT}/${LC}/never`, {}, { replay: true });
  await lc('rest.read_time', 'readTime = the setup commitTime', p, { readTime: at }, { replace: new Map([[at, '<readTime: the setup commitTime>']]) });
  await lc('rest.read_time_2h', 'readTime two hours ago, a whole minute', p, { readTime: minuteAgo(120) });
  await lc('rest.collection_parent', `a collection (${LC}) as the parent`, `${ROOT}/${LC}`, {}, { replay: true });
  if (g) {
    const gl = async (id, d, req, o = {}) => {
      const { c, raw } = await grpcCase(id, d, 'listCollectionIds', { parent: p, ...req }, o);
      c.collectionIds = raw.ok ? raw.response.collectionIds || [] : [];
      if (raw.ok && raw.response.nextPageToken) c.nextPageTokenShape = tokenShape(raw.response.nextPageToken);
      out.push(c);
      return raw;
    };
    await gl('grpc.default', 'ListCollectionIds', {});
    let t = '';
    const gt = new Map();
    for (let page = 1; page <= 5; page++) {
      const r = await gl(`grpc.paging.page${page}`, `pageSize 2, page ${page}`, { pageSize: 2, ...(t ? { pageToken: t } : {}) }, { replace: gt });
      t = r.ok && r.response.nextPageToken;
      if (!t) break;
      gt.set(t, `<nextPageToken of page ${page}>`);
    }
    await gl('grpc.token_garbage', 'pageToken "abc"', { pageSize: 2, pageToken: 'abc' });
  }
  return {
    cases: out,
    notes: [
      'The root of the database is never listed: its collection ids would name the project\'s own collections.',
      `${LC}/q is never written: it exists only as the parent of q/k1/x.`,
    ],
  };
}

// ---- CreateDocument ----

async function create() {
  const out = [];
  const ids = [];
  const replace = new Map();
  const track = (raw) => {
    if (raw.http === 200 && raw.body.name) written.add(raw.body.name);
  };
  for (let i = 0; i < AUTO_IDS; i++) {
    const r = await rest('POST', `${ROOT}/${A}`, { fields: fields({ i }) });
    if (r.http !== 200) throw new Error(`auto-id create failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 200)}`);
    collectTimes(r.body, 'rest');
    track(r);
    const id = r.body.name.split('/').pop();
    ids.push(id);
    replace.set(id, `<auto id ${i + 1}>`);
    if (i === 0) {
      out.push({
        id: 'rest.auto_id', description: `POST ${A} with no documentId (the first of ${AUTO_IDS})`, transport: 'rest',
        request: { method: 'POST', path: relProject(`${ROOT}/${A}`), body: { fields: fields({ i }) } },
        response: { http: r.http, bodyIsArray: false, body: normalise(r.body, replace) },
        relations: { createTimeEqualsUpdateTime: r.body.createTime === r.body.updateTime },
      });
    }
  }
  const chars = [...new Set(ids.join(''))].sort().join('');
  const autoIds = {
    count: ids.length,
    lengths: [...new Set(ids.map((x) => x.length))],
    charactersSeen: chars,
    classes: { upper: /[A-Z]/.test(chars), lower: /[a-z]/.test(chars), digit: /[0-9]/.test(chars), other: chars.replace(/[A-Za-z0-9]/g, '') },
    distinct: new Set(ids).size,
  };

  const w = `${ROOT}/${W}`;
  const cc = async (id, d, path, body, o = {}) => {
    const { c, raw } = await restCase(id, d, 'POST', path, body, { replace, ...o });
    track(raw);
    if (raw.http === 200 && raw.body.name && !raw.body.name.endsWith('/c1') && !raw.body.name.includes('?')) {
      const auto = raw.body.name.split('/').pop();
      if (/^[A-Za-z0-9]{20}$/.test(auto) && !replace.has(auto)) {
        replace.set(auto, `<auto id of ${id}>`);
        c.response.body = normalise(raw.body, replace);
      }
    }
    out.push(c);
    return raw;
  };
  await cc('rest.with_id', 'POST ?documentId=c1', `${w}?documentId=c1`, { fields: fields({ a: 1 }) });
  await cc('rest.existing_id', 'POST ?documentId=c1 again: the document exists', `${w}?documentId=c1`, { fields: fields({ a: 2 }) });
  await cc('rest.with_mask', 'POST ?documentId=c2&mask.fieldPaths=a', `${w}?documentId=c2&mask.fieldPaths=a`, { fields: fields({ a: 1, b: 2 }) });
  await cc('rest.empty_id', 'POST ?documentId= (empty)', `${w}?documentId=`, { fields: fields({ a: 1 }) });
  await cc('rest.no_fields', 'POST ?documentId=c3 with {} (no fields)', `${w}?documentId=c3`, {});
  await cc('rest.reserved_id', 'POST ?documentId=__x__', `${w}?documentId=__x__`, { fields: fields({ a: 1 }) });
  await cc('rest.dot_id', 'POST ?documentId=.', `${w}?documentId=.`, { fields: fields({ a: 1 }) });
  await cc('rest.slash_id', 'POST ?documentId=a%2Fb', `${w}?documentId=a%2Fb`, { fields: fields({ a: 1 }) });
  await cc('rest.name_in_body', 'POST ?documentId=c4 with a body naming another document', `${w}?documentId=c4`, { name: `${w}/other`, fields: fields({ a: 1 }) });
  await cc('rest.create_time_in_body', 'POST ?documentId=c5 with createTime and updateTime in the body', `${w}?documentId=c5`, { fields: fields({ a: 1 }), createTime: '2001-01-01T00:00:00Z', updateTime: '2001-01-01T00:00:00Z' });
  await cc('rest.under_missing_parent', `POST ${W}/mp/sub?documentId=x (mp does not exist)`, `${w}/mp/sub?documentId=x`, { fields: fields({ a: 1 }) });
  if (g) {
    const gc = async (id, d, req) => {
      const { c, raw } = await grpcCase(id, d, 'createDocument', { parent: ROOT, collectionId: W, document: { fields: toGrpc(fields({ a: 1 })) }, ...req }, { replace });
      if (raw.ok) {
        written.add(raw.response.name);
        const auto = raw.response.name.split('/').pop();
        if (!replace.has(auto) && auto !== 'g1') {
          replace.set(auto, `<auto id of ${id}>`);
          c.response.message = normalise(raw.response, replace);
        }
      }
      out.push(c);
    };
    await gc('grpc.auto_id', 'CreateDocument documentId ""', { documentId: '' });
    await gc('grpc.with_id', 'CreateDocument documentId g1', { documentId: 'g1' });
    await gc('grpc.existing_id', 'CreateDocument documentId c1 (exists)', { documentId: 'c1' });
  }
  return {
    cases: out,
    autoIds,
    notes: [
      `autoIds summarises ${AUTO_IDS} automatic ids from POST ${A} (not kept: each is replaced by <auto id N>).`,
    ],
  };
}

// ---- UpdateDocument ----

async function update() {
  const out = [];
  const w = (r) => `${ROOT}/${W}/${r}`;
  const replace = new Map();
  const pc = async (id, d, path, query, body, o = {}) => {
    const { c, raw } = await restCase(id, d, 'PATCH', query ? `${path}?${query}` : path, body, { replace, ...o });
    if (raw.http === 200 && raw.body.name) written.add(raw.body.name);
    if (raw.http === 200) c.relations = { createTimeEqualsUpdateTime: raw.body.createTime === raw.body.updateTime };
    out.push(c);
    return raw;
  };
  const f = (data) => ({ fields: fields(data) });
  const first = await pc('rest.upsert_create', 'PATCH u1 (missing) with no precondition: creates it', w('u1'), '', f({ a: 1, b: 2 }));
  const t0 = first.body.updateTime;
  replace.set(t0, '<updateTime of rest.upsert_create>');
  const r2 = await pc('rest.no_mask', 'PATCH u1 {c: 3} with no updateMask: replaces every field', w('u1'), '', f({ c: 3 }));
  out.at(-1).relations.createTimeUnchanged = r2.body.createTime === first.body.createTime;
  out.at(-1).relations.updateTimeChanged = r2.body.updateTime !== first.body.updateTime;
  replace.set(r2.body.updateTime, '<updateTime of rest.no_mask>');
  await pc('rest.mask_one', 'PATCH u1 ?updateMask.fieldPaths=a with {a: 10, z: 9}: z is outside the mask', w('u1'), 'updateMask.fieldPaths=a', f({ a: 10, z: 9 }));
  await pc('rest.mask_absent_in_body', 'PATCH u1 ?updateMask.fieldPaths=c with {a: 11}: c is in the mask but not the body', w('u1'), 'updateMask.fieldPaths=c', f({ a: 11 }));
  await pc('rest.mask_nested', 'PATCH u1 ?updateMask.fieldPaths=x.y with {x: {y: 1, q: 2}}', w('u1'), 'updateMask.fieldPaths=x.y', f({ x: { y: 1, q: 2 } }));
  await pc('rest.mask_parent_and_child', 'PATCH u1 ?updateMask.fieldPaths=x&updateMask.fieldPaths=x.y', w('u1'), 'updateMask.fieldPaths=x&updateMask.fieldPaths=x.y', f({ x: { y: 5 } }));
  await pc('rest.response_mask', 'PATCH u1 ?updateMask.fieldPaths=a&mask.fieldPaths=a with {a: 12}', w('u1'), 'updateMask.fieldPaths=a&mask.fieldPaths=a', f({ a: 12 }));
  const cur = await rest('GET', w('u1'));
  const tCur = cur.body.updateTime;
  replace.set(tCur, '<the current updateTime of u1>');
  await pc('rest.exists_true', 'PATCH u1 ?currentDocument.exists=true (it exists)', w('u1'), 'currentDocument.exists=true', f({ a: 13 }));
  await pc('rest.exists_false_on_existing', 'PATCH u1 ?currentDocument.exists=false (it exists)', w('u1'), 'currentDocument.exists=false', f({ a: 14 }));
  await pc('rest.exists_true_on_missing', 'PATCH u-missing ?currentDocument.exists=true', w('u-missing'), 'currentDocument.exists=true', f({ a: 1 }));
  await pc('rest.exists_false_on_missing', 'PATCH u-new ?currentDocument.exists=false: creates it', w('u-new'), 'currentDocument.exists=false', f({ a: 1 }));
  await pc('rest.update_time_stale', 'PATCH u1 ?currentDocument.updateTime=<updateTime of the first write>', w('u1'), `currentDocument.updateTime=${encodeURIComponent(t0)}`, f({ a: 15 }));
  await pinVersions(out.at(-1), w('u1'), t0);
  const now = await rest('GET', w('u1'));
  replace.set(now.body.updateTime, '<the current updateTime of u1>');
  await pc('rest.update_time_current', 'PATCH u1 ?currentDocument.updateTime=<its current updateTime>', w('u1'), `currentDocument.updateTime=${encodeURIComponent(now.body.updateTime)}`, f({ a: 16 }));
  const shifted = now.body.updateTime.replace(/\.(\d+)Z$/, (_, d) => `.${d.padEnd(6, '0').slice(0, 6)}001Z`);
  replace.set(shifted, '<the updateTime of u1 before rest.update_time_current, plus 1 ns>');
  await pc('rest.update_time_plus_1ns', 'PATCH u1 ?currentDocument.updateTime=<an earlier updateTime plus 1 ns>', w('u1'), `currentDocument.updateTime=${encodeURIComponent(shifted)}`, f({ a: 17 }));
  await pc('rest.update_time_on_missing', 'PATCH u-missing2 ?currentDocument.updateTime=<updateTime of the first write>', w('u-missing2'), `currentDocument.updateTime=${encodeURIComponent(t0)}`, f({ a: 1 }));
  await pinVersions(out.at(-1), w('u-missing2'), t0);
  await pc('rest.update_time_fixed_on_missing', `PATCH u-missing2 ?currentDocument.updateTime=${STALE}`, w('u-missing2'), `currentDocument.updateTime=${encodeURIComponent(STALE)}`, f({ a: 1 }));
  await pinVersions(out.at(-1), w('u-missing2'), STALE);
  await pc('rest.exists_and_update_time', 'PATCH u1 with both currentDocument.exists and updateTime', w('u1'), `currentDocument.exists=true&currentDocument.updateTime=${encodeURIComponent(t0)}`, f({ a: 18 }));
  await pc('rest.name_in_body_differs', 'PATCH u1 with a body naming u2', w('u1'), '', { name: w('u2'), ...f({ a: 19 }) });
  await pc('rest.mask_reserved', 'PATCH u1 ?updateMask.fieldPaths=__name__', w('u1'), 'updateMask.fieldPaths=__name__', f({ a: 20 }));
  if (g) {
    const ensure = await rest('GET', w('u1'));
    replace.set(ensure.body.updateTime, '<the current updateTime of u1>');
    const gu = async (id, d, req) => {
      const { c, raw } = await grpcCase(id, d, 'updateDocument', req, { replace });
      if (raw.ok) written.add(raw.response.name);
      out.push(c);
      return c;
    };
    const doc = (r, data) => ({ name: w(r), fields: toGrpc(fields(data)) });
    await gu('grpc.no_mask', 'UpdateDocument u1 with no updateMask', { document: doc('u1', { g: 1 }) });
    await gu('grpc.mask_empty_list', 'UpdateDocument u1 with updateMask {fieldPaths: []}', { document: doc('u1', { h: 1 }), updateMask: { fieldPaths: [] } });
    await gu('grpc.mask_one', 'UpdateDocument u1 with updateMask [a]', { document: doc('u1', { a: 30 }), updateMask: { fieldPaths: ['a'] } });
    await gu('grpc.exists_false_on_existing', 'UpdateDocument u1 currentDocument.exists=false', { document: doc('u1', { a: 31 }), currentDocument: { exists: false } });
    await gu('grpc.exists_true_on_missing', 'UpdateDocument u-missing currentDocument.exists=true', { document: doc('u-missing', { a: 1 }), currentDocument: { exists: true } });
    await pinVersions(await gu('grpc.update_time_stale', 'UpdateDocument u1 currentDocument.updateTime=<first write>', { document: doc('u1', { a: 32 }), currentDocument: { updateTime: rfcToGrpc(t0) } }), w('u1'), t0);
  }
  return {
    cases: out,
    notes: ['updateTime values in requests and messages are replaced by placeholders naming the write they came from.'],
  };
}

// ---- DeleteDocument ----

async function remove() {
  const out = [];
  const w = (r) => `${ROOT}/${W}/${r}`;
  await setup({ [`${W}/d1`]: { a: 1 }, [`${W}/d2`]: { a: 1 }, [`${W}/d3`]: { a: 1 } });
  const t1 = (await rest('GET', w('d1'))).body.updateTime;
  const old = STALE;
  const replace = new Map([[t1, '<the current updateTime of d1>']]);
  const dc = async (id, d, path, query, o = {}) => {
    const { c } = await restCase(id, d, 'DELETE', query ? `${path}?${query}` : path, undefined, { replace, ...o });
    out.push(c);
    const sent = /currentDocument\.updateTime=([^&]+)/.exec(query || '');
    if (sent) await pinVersions(c, path, decodeURIComponent(sent[1]));
  };
  await dc('rest.missing', 'DELETE d-missing (no precondition)', w('d-missing'), '');
  await dc('rest.missing_exists_true', 'DELETE d-missing ?currentDocument.exists=true', w('d-missing'), 'currentDocument.exists=true');
  await dc('rest.missing_exists_false', 'DELETE d-missing ?currentDocument.exists=false', w('d-missing'), 'currentDocument.exists=false');
  await dc('rest.missing_update_time', `DELETE d-missing ?currentDocument.updateTime=${STALE}`, w('d-missing'), `currentDocument.updateTime=${encodeURIComponent(old)}`);
  await dc('rest.exists_false_on_existing', 'DELETE d1 ?currentDocument.exists=false', w('d1'), 'currentDocument.exists=false');
  await dc('rest.update_time_stale', `DELETE d1 ?currentDocument.updateTime=${STALE}`, w('d1'), `currentDocument.updateTime=${encodeURIComponent(old)}`);
  await dc('rest.update_time_current', 'DELETE d1 ?currentDocument.updateTime=<its current updateTime>', w('d1'), `currentDocument.updateTime=${encodeURIComponent(t1)}`);
  await dc('rest.exists_true', 'DELETE d2 ?currentDocument.exists=true', w('d2'), 'currentDocument.exists=true');
  await dc('rest.collection_path', `DELETE ${W} (a collection)`, `${ROOT}/${W}`, '');
  await dc('rest.missing_with_subcollection', `DELETE ${G}/gm (a missing document with a subcollection)`, `${ROOT}/${G}/gm`, '');
  if (g) {
    const gd = async (id, d, req) => {
      const { c } = await grpcCase(id, d, 'deleteDocument', req, { replace });
      out.push(c);
      if (req.currentDocument?.updateTime) await pinVersions(c, req.name, old);
    };
    await gd('grpc.missing', 'DeleteDocument d-missing', { name: w('d-missing') });
    await gd('grpc.missing_exists_true', 'DeleteDocument d-missing currentDocument.exists=true', { name: w('d-missing'), currentDocument: { exists: true } });
    await gd('grpc.update_time_stale', `DeleteDocument d3 currentDocument.updateTime=${STALE}`, { name: w('d3'), currentDocument: { updateTime: rfcToGrpc(old) } });
    await gd('grpc.exists_false_on_existing', 'DeleteDocument d3 currentDocument.exists=false', { name: w('d3'), currentDocument: { exists: false } });
  }
  return { cases: out, notes: [`${W}/d1, d2 and d3 are written by one commit before the cases.`] };
}

// ---- write results, times and transformResults ----

async function writeResults() {
  const out = [];
  const w = (r) => `${ROOT}/${W}/${r}`;
  const relations = [];
  const cm = async (id, d, writes) => {
    for (const x of writes) assertScoped(x.update?.name ?? x.delete ?? x.verify ?? x.transform?.document);
    const { c, raw } = await restCase(id, d, 'POST', `${ROOT}:commit`, { writes });
    if (raw.http === 200) for (const x of writes) if (x.update) written.add(x.update.name);
    const wr = raw.body.writeResults || [];
    c.writeResultKeys = wr.map((r) => Object.keys(r).sort());
    if (raw.http === 200) relations.push({ case: id, commitTimeEqualsEveryUpdateTime: wr.every((r) => !r.updateTime || r.updateTime === raw.body.commitTime), writeResultsHaveUpdateTime: wr.map((r) => 'updateTime' in r) });
    out.push(c);
    return raw;
  };
  const up = (r, data) => ({ update: { name: w(r), fields: fields(data) } });
  const first = await cm('rest.update', 'commit one update', [up('t1', { a: 1 })]);
  const t1Time = first.body.writeResults?.[0]?.updateTime;
  await cm('rest.update_with_transform', 'commit an update with updateTransforms [ts = REQUEST_TIME, n += 1]', [{ ...up('t2', { a: 1 }), updateTransforms: [{ fieldPath: 'ts', setToServerValue: 'REQUEST_TIME' }, { fieldPath: 'n', increment: { integerValue: '1' } }] }]);
  await cm('rest.update_with_empty_transforms', 'commit an update with updateTransforms []', [{ ...up('t3', { a: 1 }), updateTransforms: [] }]);
  await cm('rest.two_updates', 'commit two updates', [up('t4', { a: 1 }), up('t5', { a: 1 })]);
  const same = await cm('rest.update_unchanged', 'commit t1 again with the same fields', [up('t1', { a: 1 })]);
  if (same.http === 200) relations.at(-1).updateTimeIsTheEarlierWrite = same.body.writeResults?.[0]?.updateTime === t1Time;
  await cm('rest.delete_existing', 'commit a delete of t5', [{ delete: w('t5') }]);
  await cm('rest.delete_missing', 'commit a delete of a missing document', [{ delete: w('t-missing') }]);
  const ver = await cm('rest.verify_existing', 'commit a verify of t1 with currentDocument.exists=true', [{ verify: w('t1'), currentDocument: { exists: true } }]);
  if (ver.http === 200) relations.at(-1).updateTimeIsTheDocumentUpdateTime = ver.body.writeResults?.[0]?.updateTime === t1Time;
  await cm('rest.verify_missing', 'commit a verify of a missing document with currentDocument.exists=true', [{ verify: w('t-missing'), currentDocument: { exists: true } }]);
  await cm('rest.verify_no_precondition', 'commit a verify of t1 with no currentDocument', [{ verify: w('t1') }]);
  await cm('rest.empty', 'commit with writes []', []);
  if (g) {
    const gc = async (id, d, writes) => {
      const { c, raw } = await grpcCase(id, d, 'commit', { database: DB, writes });
      if (raw.ok) {
        for (const x of writes) if (x.update) written.add(x.update.name);
        c.writeResultKeys = (raw.response.writeResults || []).map((r) => Object.keys(r).sort());
      }
      out.push(c);
    };
    await gc('grpc.update', 'Commit one update', [toGrpc(up('t6', { a: 1 }))]);
    await gc('grpc.update_with_transform', 'Commit an update with a REQUEST_TIME transform', [{ ...toGrpc(up('t7', { a: 1 })), updateTransforms: [{ fieldPath: 'ts', setToServerValue: 'REQUEST_TIME' }] }]);
    await gc('grpc.delete_missing', 'Commit a delete of a missing document', [{ delete: w('t-missing') }]);
  }
  return {
    cases: out,
    relations,
    notes: [
      'writeResultKeys lists the keys of each write result as sent: over REST an empty repeated field (transformResults) is omitted from the JSON; over gRPC an empty repeated field is not on the wire at all, so the gRPC keys show only what proto-loader decodes.',
      'precision counts every time value in every response of the run (every topic), by key: over REST the number of fraction digits of the RFC 3339 string, over gRPC how many Timestamp nanos were a multiple of 1000.',
    ],
  };
}

// ---- resource names of another project or database ----

async function resourceNames() {
  const out = [];
  const otherProjectDoc = `projects/${OTHER_PROJECT}/databases/(default)/documents/${W}/x`;
  const otherDbDoc = `${OTHER_DB}/documents/${W}/x`;
  // Should production ever write these into this database, the cleanup deletes them.
  written.add(`${ROOT}/${W}/x`);
  const cm = async (id, d, writes) => {
    for (const x of writes) assertScoped(x.update?.name ?? x.delete);
    out.push((await restCase(id, d, 'POST', `${ROOT}:commit`, { writes })).c);
  };
  await cm('rest.commit.write_other_project', 'commit to this database, one write naming a document of another project', [{ update: { name: otherProjectDoc, fields: fields({ a: 1 }) } }]);
  await cm('rest.commit.write_other_database', 'commit to this database, one write naming a document of another database of this project', [{ update: { name: otherDbDoc, fields: fields({ a: 1 }) } }]);
  await cm('rest.commit.mixed', 'commit to this database: one write here, one in another project', [{ update: { name: `${ROOT}/${W}/x`, fields: fields({ a: 1 }) } }, { update: { name: otherProjectDoc, fields: fields({ a: 1 }) } }]);
  out.push((await restCase('rest.batch_get.other_project', 'batchGet on this database naming a document of another project', 'POST', `${ROOT}:batchGet`, { documents: [`${ROOT}/${G}/g1`, otherProjectDoc] })).c);
  out.push((await restCase('rest.batch_get.other_database', 'batchGet on this database naming a document of another database', 'POST', `${ROOT}:batchGet`, { documents: [otherDbDoc] })).c);
  out.push((await restCase('rest.get.invalid_api_key', 'GET with ?key=<an invalid API key> and no Authorization header', 'GET', `${ROOT}/${G}/g1?key=AIzaSyA-aruma-ref-invalid-key-000000000`, undefined, { auth: false })).c);
  out.push((await restCase('rest.get.invalid_api_key_with_auth', 'GET with ?key=<an invalid API key> and the owner token', 'GET', `${ROOT}/${G}/g1?key=AIzaSyA-aruma-ref-invalid-key-000000000`)).c);
  out.push((await restCase('rest.get.malformed_api_key', 'GET with ?key=x and no Authorization header', 'GET', `${ROOT}/${G}/g1?key=x`, undefined, { auth: false })).c);
  if (g) {
    const doc = (name) => ({ update: { name, fields: toGrpc(fields({ a: 1 })) } });
    out.push((await grpcCase('grpc.commit.write_other_project', 'Commit to this database, one write naming another project', 'commit', { database: DB, writes: [doc(otherProjectDoc)] })).c);
    out.push((await grpcCase('grpc.commit.prefix_other_project', 'Commit whose body names this database, with google-cloud-resource-prefix of another project', 'commit', { database: DB, writes: [doc(`${ROOT}/${W}/x`)] }, { database: `projects/${OTHER_PROJECT}/databases/(default)` })).c);
    out.push((await grpcCase('grpc.get.prefix_other_project', 'GetDocument of this database with google-cloud-resource-prefix of another project', 'getDocument', { name: `${ROOT}/${G}/g1` }, { database: `projects/${OTHER_PROJECT}/databases/(default)` })).c);
  }
  return {
    cases: out,
    notes: [
      `${OTHER_PROJECT} is a project id that does not exist; ${relProject(OTHER_DB)} is a database id that was never created.`,
      'The API key is not a real key: the string starts like one and is otherwise made up.',
    ],
  };
}

// ---- the REST front end's refusals ----

async function wireErrors() {
  const out = [];
  const g1 = `${ROOT}/${G}/g1`;
  const w = (r) => `${ROOT}/${W}/${r}`;
  const rc = async (id, d, method, path, body, o = {}) => {
    const { c, raw } = await restCase(id, d, method, path, body, o);
    if (raw.http === 200 && raw.body && raw.body.name) written.add(raw.body.name);
    // A transaction a refusal case opened after all is rolled back at the cleanup.
    if (raw.http === 200 && raw.body && typeof raw.body.transaction === 'string') openTxns.add(raw.body.transaction);
    const arr = Array.isArray(raw.body) ? raw.body : [raw.body];
    const e = arr.find((x) => x && x.error)?.error;
    if (e) c.errorDetailTypes = (e.details || []).map((x) => x['@type']);
    if (raw.body && raw.body.raw !== undefined) c.responseNotJson = true;
    out.push(c);
  };
  const bg = `${ROOT}:batchGet`;
  const q = `${ROOT}:runQuery`;
  const from = { from: [{ collectionId: G }] };

  // (1) JSON the front end refuses.
  await rc('json.unknown_field', 'batchGet with an unknown top-level field', 'POST', bg, { documents: [g1], bogus: 1 });
  await rc('json.unknown_nested_field', 'runQuery with an unknown field inside structuredQuery', 'POST', q, { structuredQuery: { ...from, bogus: 1 } });
  await rc('json.unknown_field_in_value', 'PATCH with an unknown key in a Value', 'PATCH', w('wire-v'), { fields: { a: { integerValue: '1', bogusValue: 2 } } });
  await rc('json.wrong_kind_string_for_array', 'batchGet with documents: "x"', 'POST', bg, { documents: 'x' });
  await rc('json.wrong_kind_number_in_array', 'batchGet with documents: [1]', 'POST', bg, { documents: [1] });
  await rc('json.wrong_kind_fields_string', 'PATCH with fields: "x"', 'PATCH', w('wire-v'), { fields: 'x' });
  await rc('json.wrong_kind_integer_value_word', 'PATCH with integerValue: "one"', 'PATCH', w('wire-v'), { fields: { a: { integerValue: 'one' } } });
  await rc('json.wrong_kind_boolean_string', 'PATCH with booleanValue: "true"', 'PATCH', w('wire-v'), { fields: { a: { booleanValue: 'true' } } });
  await rc('json.syntax_error', 'batchGet with a truncated JSON body', 'POST', bg, '{"documents": [');
  await rc('json.trailing_garbage', 'batchGet with JSON followed by garbage', 'POST', bg, `{"documents": []} x`);
  await rc('json.root_array', 'batchGet with the JSON body []', 'POST', bg, '[]');
  await rc('json.root_string', 'batchGet with the JSON body "x"', 'POST', bg, '"x"');
  await rc('json.root_number', 'batchGet with the JSON body 1', 'POST', bg, '1');
  await rc('json.root_null', 'batchGet with the JSON body null', 'POST', bg, 'null');
  await rc('json.empty_body', 'batchGet with an empty body', 'POST', bg, '');
  await rc('json.bad_enum', 'runQuery orderBy direction "SIDEWAYS"', 'POST', q, { structuredQuery: { ...from, orderBy: [{ field: { fieldPath: 'a' }, direction: 'SIDEWAYS' }] } });
  await rc('json.bad_enum_number', 'runQuery orderBy direction 99', 'POST', q, { structuredQuery: { ...from, orderBy: [{ field: { fieldPath: 'a' }, direction: 99 }] } });
  await rc('json.bad_enum_operator', 'runQuery where a NEAR 1', 'POST', q, { structuredQuery: { ...from, where: { fieldFilter: { field: { fieldPath: 'a' }, op: 'NEAR', value: { integerValue: '1' } } } } });
  await rc('query.unknown_parameter', 'GET with ?bogus=1', 'GET', `${g1}?bogus=1`);
  await rc('query.unknown_parameter_list', 'ListDocuments with ?bogus=1', 'GET', `${ROOT}/${G}?bogus=1`);
  await rc('query.repeated_scalar', 'ListDocuments with pageSize=1&pageSize=2', 'GET', `${ROOT}/${G}?pageSize=1&pageSize=2`);
  await rc('timestamp.not_a_time', 'GET ?readTime=notatime', 'GET', `${g1}?readTime=notatime`);
  await rc('timestamp.no_zone', 'GET ?readTime=2026-10-26T18:00:00 (no Z)', 'GET', `${g1}?readTime=2026-10-26T18%3A00%3A00`);
  const offset = new Date(Date.now() - 60_000 + 3600_000).toISOString().replace('Z', '+01:00');
  await rc('timestamp.offset', 'GET ?readTime=<a minute ago, written with a +01:00 offset>', 'GET', `${g1}?readTime=${encodeURIComponent(offset)}`, undefined, { replace: new Map([[offset, '<readTime: a minute ago with +01:00>']]) });
  await rc('timestamp.month_13', 'batchGet readTime "2026-13-01T00:00:00Z"', 'POST', bg, { documents: [g1], readTime: '2026-13-01T00:00:00Z' });
  await rc('timestamp.value_malformed', 'PATCH with timestampValue "yesterday"', 'PATCH', w('wire-v'), { fields: { t: { timestampValue: 'yesterday' } } });

  // (2) Values with no type, timestamps out of range.
  await rc('value.no_type', 'PATCH with a field whose Value is {}', 'PATCH', w('wire-notype'), { fields: { a: {} } });
  await rc('value.no_type_nested', 'PATCH with a map field holding a Value {}', 'PATCH', w('wire-notype2'), { fields: { m: { mapValue: { fields: { a: {} } } } } });
  await rc('value.no_type_in_filter', 'runQuery where a == {} (a Value with no type)', 'POST', q, { structuredQuery: { ...from, where: { fieldFilter: { field: { fieldPath: 'a' }, op: 'EQUAL', value: {} } } } });
  await rc('value.two_types', 'PATCH with a Value holding integerValue and stringValue', 'PATCH', w('wire-v'), { fields: { a: { integerValue: '1', stringValue: 'x' } } });
  await rc('value.timestamp_year_0', 'PATCH timestampValue "0000-12-31T23:59:59Z"', 'PATCH', w('wire-v'), { fields: { t: { timestampValue: '0000-12-31T23:59:59Z' } } });
  await rc('value.timestamp_year_10000', 'PATCH timestampValue "10000-01-01T00:00:00Z"', 'PATCH', w('wire-v'), { fields: { t: { timestampValue: '10000-01-01T00:00:00Z' } } });
  await rc('value.timestamp_min', 'PATCH timestampValue "0001-01-01T00:00:00Z" (the minimum)', 'PATCH', w('wire-tsmin'), { fields: { t: { timestampValue: '0001-01-01T00:00:00Z' } } });
  await rc('value.timestamp_max', 'PATCH timestampValue "9999-12-31T23:59:59.999999Z" (the maximum)', 'PATCH', w('wire-tsmax'), { fields: { t: { timestampValue: '9999-12-31T23:59:59.999999Z' } } });
  await rc('readtime.year_10000', 'batchGet readTime "10000-01-01T00:00:00Z"', 'POST', bg, { documents: [g1], readTime: '10000-01-01T00:00:00Z' });

  // (3, 4) Database and project ids in the path, and an Admin API name outside projects/.
  const dbs = ['abc', 'abcd', 'a'.repeat(63), 'a'.repeat(64), 'Upper-case', '1abc', 'abc-', 'ab_c', '-abc', 'aruma-ref-valid-but-missing', '00000000-0000-0000-0000-000000000000', '(other)', '(default'];
  for (const d of dbs) {
    const id = d.length > 20 ? `${d.slice(0, 6)}…(${d.length})` : d;
    await rc(`path.database_id.${id}`, `GET a document in database ${JSON.stringify(id)}`, 'GET', `projects/${PROJECT}/databases/${encodeURIComponent(d)}/documents/${G}/g1`);
  }
  await rc('path.database_empty', 'GET with an empty database id', 'GET', `projects/${PROJECT}/databases//documents/${G}/g1`);
  await rc('path.no_database_segment', 'batchGet at projects/<p>/documents:batchGet (no databases segment)', 'POST', `projects/${PROJECT}/documents:batchGet`, { documents: [g1] });
  for (const p of ['Bad_Project!', 'ab', 'UPPER-CASE', 'aruma-ref-no-such-project', '-starts-with-dash', '123456789012']) {
    await rc(`path.project_id.${p}`, `GET a document in project ${JSON.stringify(p)}`, 'GET', `projects/${encodeURIComponent(p)}/databases/(default)/documents/${G}/g1`);
  }
  await rc('admin.name_without_projects', 'Admin API GET v1/databases/(default) (no projects/ prefix)', 'GET', 'databases/(default)');
  await rc('admin.name_other_prefix', 'Admin API GET v1/organizations/x/databases/(default)', 'GET', 'organizations/x/databases/(default)');
  await rc('admin.database_get_bad_id', 'Admin API GET projects/<p>/databases/Upper-case', 'GET', `projects/${PROJECT}/databases/Upper-case`);
  await rc('admin.database_get_missing', 'Admin API GET projects/<p>/databases/aruma-ref-valid-but-missing', 'GET', `projects/${PROJECT}/databases/aruma-ref-valid-but-missing`);

  // (5) Does the "Root element" second line recur? oneof conflicts on other methods.
  const at = new Date(Date.now() - 5000).toISOString();
  const tm = new Map([[at, '<readTime: five seconds ago>']]);
  const ro = await rest('POST', `${ROOT}:beginTransaction`, { options: { readOnly: {} } });
  const txn = ro.body.transaction;
  tm.set(txn, '<a read-only transaction>');
  await rc('oneof.batch_get_new_transaction_and_read_time', 'batchGet with newTransaction and readTime', 'POST', bg, { documents: [g1], newTransaction: { readOnly: {} }, readTime: at }, { replace: tm });
  await rc('oneof.batch_get_transaction_and_read_time', 'batchGet with transaction and readTime (the recorded case, again)', 'POST', bg, { documents: [g1], transaction: txn, readTime: at }, { replace: tm });
  await rc('oneof.run_query_transaction_and_read_time', 'runQuery with transaction and readTime', 'POST', q, { structuredQuery: from, transaction: txn, readTime: at }, { replace: tm });
  await rc('oneof.run_query_new_transaction_and_read_time', 'runQuery with newTransaction and readTime', 'POST', q, { structuredQuery: from, newTransaction: { readOnly: {} }, readTime: at }, { replace: tm });
  await rc('oneof.begin_read_only_and_read_write', 'beginTransaction with readOnly and readWrite', 'POST', `${ROOT}:beginTransaction`, { options: { readOnly: {}, readWrite: {} } });
  await rc('oneof.list_collection_ids_bad_field', 'listCollectionIds with an unknown field', 'POST', `${ROOT}/${LC}/p:listCollectionIds`, { bogus: 1 });
  await rc('oneof.value_two_types_in_filter', 'runQuery where a == {integerValue, stringValue}', 'POST', q, { structuredQuery: { ...from, where: { fieldFilter: { field: { fieldPath: 'a' }, op: 'EQUAL', value: { integerValue: '1', stringValue: 'x' } } } } });

  if (g) {
    const gb = async (id, d, database) => out.push((await grpcCase(id, d, 'beginTransaction', { database, options: { readOnly: {} } })).c);
    await gb('grpc.database_field.bad', 'BeginTransaction database "bad"', 'bad');
    await gb('grpc.database_field.project_only', 'BeginTransaction database "projects/<p>"', `projects/${PROJECT}`);
    await gb('grpc.database_field.empty_id', 'BeginTransaction database "projects/<p>/databases/"', `projects/${PROJECT}/databases/`);
    await gb('grpc.database_field.with_documents', 'BeginTransaction database "projects/<p>/databases/(default)/documents"', `${DB}/documents`);
    await gb('grpc.database_field.empty', 'BeginTransaction database ""', '');
    await gb('grpc.database_field.bad_id', 'BeginTransaction database "projects/<p>/databases/Upper-case"', `projects/${PROJECT}/databases/Upper-case`);
  }
  return {
    cases: out,
    notes: [
      'errorDetailTypes lists the @type of each entry of error.details (google.rpc.BadRequest and the like); an empty list means the error carried no details.',
      'responseNotJson marks an answer whose body was not JSON (kept as {raw}).',
      'The gRPC BeginTransaction cases send google-cloud-resource-prefix and x-goog-request-params naming (default), as lib.mjs always does; only the database field is malformed.',
    ],
  };
}

// ---- cleanup ----

async function cleanup() {
  for (const t of [...openTxns]) await rollback(t);
  const names = [...written];
  const res = [];
  for (let i = 0; i < names.length; i += 400) {
    const r = await commitWrites(names.slice(i, i + 400).map((n) => ({ delete: n })));
    res.push(r.http);
    if (r.http !== 200) throw new Error(`cleanup commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
  // sweepF1 lists every document, at any depth, under a collection id from
  // "aruma-ref-f1" up to "aruma-ref-f2" (exclusive), by one kindless query.
  const sweepF1 = () => rest('POST', `${ROOT}:runQuery`, {
    structuredQuery: {
      from: [{ allDescendants: true }],
      where: {
        compositeFilter: {
          op: 'AND',
          filters: [
            { fieldFilter: { field: { fieldPath: '__name__' }, op: 'GREATER_THAN_OR_EQUAL', value: { referenceValue: `${ROOT}/aruma-ref-f1/${MIN_ID}` } } },
            { fieldFilter: { field: { fieldPath: '__name__' }, op: 'LESS_THAN', value: { referenceValue: `${ROOT}/aruma-ref-f2/${MIN_ID}` } } },
          ],
        },
      },
      select: { fields: [{ fieldPath: '__name__' }] },
      orderBy: [{ field: { fieldPath: '__name__' }, direction: 'ASCENDING' }],
      limit: 400,
    },
  });
  const found = (r) => (Array.isArray(r.body) ? r.body : []).filter((x) => x.document).map((x) => x.document.name);
  // Strays: documents the run wrote without tracking them (a refusal production
  // accepted after all, or an earlier run that stopped before its cleanup).
  // They are deleted too, and listed so that a stray never goes unnoticed.
  const strays = found(await sweepF1());
  if (strays.length) {
    const r = await commitWrites(strays.map((n) => ({ delete: n })));
    res.push(r.http);
    if (r.http !== 200) throw new Error(`stray cleanup commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
  const sweep = await sweepF1();
  const remaining = found(sweep).map(rel);
  const lists = {};
  for (const t of TOPS) {
    const l = await rest('GET', `${ROOT}/${t}?showMissing=true&pageSize=100`);
    lists[t] = (l.body.documents || []).map((d) => rel(d.name));
  }
  const ok = sweep.http === 200 && remaining.length === 0 && Object.values(lists).every((l) => l.length === 0) && openTxns.size === 0;
  return {
    ok,
    deletedCount: names.length,
    deleted: names.map((n) => rel(n)).sort().map((n) => (/^aruma-ref-f1-auto\/[A-Za-z0-9]{20}$/.test(n) ? `${A}/<auto id>` : n)),
    strays: strays.map(rel),
    commitHttp: res,
    sweep: { http: sweep.http, query: `kindless allDescendants, __name__ in [aruma-ref-f1/${MIN_ID}, aruma-ref-f2/${MIN_ID})`, remaining },
    listsShowMissing: lists,
    transactionsLeftOpen: openTxns.size,
  };
}

// ---- main ----

async function main() {
  const dbr = await rest('GET', DB);
  if (dbr.http !== 200) throw new Error(`GET ${DB}: HTTP ${dbr.http}`);
  const target = `${PROJECT} (default): ${dbr.body.type}, ${dbr.body.databaseEdition}, ${dbr.body.concurrencyMode}, ${dbr.body.locationId}`;
  const ver = versions({ api: 'firestore.googleapis.com REST v1 and gRPC google.firestore.v1.Firestore', ...grpcVersions() });
  const results = {};
  const started = new Date().toISOString();
  let cl;
  try {
    log('setup ');
    const sg = await setup(GET_DOCS);
    const sl = await setup(LIST_DOCS);
    const sc = await setup(LC_DOCS);
    log('get ');
    results['f1-get.json'] = { setup: { commits: [sg.commit] }, ...(await get(sg.raw)) };
    log('batch-get ');
    results['f1-batch-get.json'] = { setup: { commits: [sg.commit] }, ...(await batchGet(sg.raw)) };
    log('list-documents ');
    results['f1-list-documents.json'] = { setup: { commits: [sl.commit] }, ...(await listDocuments(sl.raw)) };
    log('list-collection-ids ');
    results['f1-list-collection-ids.json'] = { setup: { commits: [sc.commit] }, ...(await listCollectionIds(sc.raw)) };
    log('create ');
    results['f1-create.json'] = await create();
    log('update ');
    results['f1-update.json'] = await update();
    log('delete ');
    results['f1-delete.json'] = await remove();
    log('write-results ');
    results['f1-write-results.json'] = await writeResults();
    log('resource-names ');
    results['f1-resource-names.json'] = { setup: { commits: [sg.commit] }, ...(await resourceNames()) };
    log('wire-errors ');
    results['f1-wire-errors.json'] = { setup: { commits: [sg.commit] }, ...(await wireErrors()) };
    results['f1-write-results.json'].precision = precision;
  } finally {
    log('\ncleanup ');
    cl = await cleanup();
    log(cl.ok ? 'verified\n' : 'NOT VERIFIED\n');
  }
  if (!cl.ok) {
    console.error('record-f1: the cleanup is not verified; no corpus file written. Remaining:', JSON.stringify({ sweep: cl.sweep.remaining, lists: cl.listsShowMissing, open: cl.transactionsLeftOpen }));
    process.exit(2);
  }
  results['f1-cleanup.json'] = { cleanup: cl };
  const finished = new Date().toISOString();
  for (const [file, body] of Object.entries(results)) {
    const header = {
      oracle: 'production',
      target,
      generator: GENERATOR,
      recordedAt: `${started} .. ${finished}`,
      versions: ver,
      notes: [
        'Auth: the firebase CLI login (a project owner), so security rules do not apply; end-user behaviour is not recorded here.',
        'Times, durations, page tokens and transaction ids are replaced by placeholders (normalise in lib.mjs, and the replace maps of record-f1.mjs); everything else is as returned.',
        ...(body.notes || []),
      ],
    };
    const { notes, ...payload } = body;
    writeCorpus(file, header, payload);
  }
}

main().catch((e) => {
  console.error('record-f1 failed:', e.message);
  process.exit(1);
});
