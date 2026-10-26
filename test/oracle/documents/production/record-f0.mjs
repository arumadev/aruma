// F0 production recordings for the Document Database (plan section 6, F0):
//
//   f0-missing-database.json   GET, list, batchGet, runQuery, runAggregationQuery,
//                              commit, beginTransaction, listCollectionIds and
//                              Listen against a database id that does not exist,
//                              and the Admin API databases.get that firebase
//                              deploy sends
//   f0-find-nearest.json       findNearest on Standard with no vector index
//   f0-explain.json            runQuery / runAggregationQuery with explainOptions
//   f0-errors.json             limit and transaction errors
//   f0-descendants.json        kindless allDescendants queries (firebase-tools
//                              firestore:delete -r, Node recursiveDelete) and
//                              PartitionQuery
//   f0-list-show-missing.json  ListDocuments with showMissing on a parent that
//                              only has a subcollection
//   f0-cleanup.json            the deletion of everything written, verified
//
// Each case is recorded over REST (firestore.googleapis.com/v1) and, where
// FIRESTORE_PROTOS is set, over gRPC (google.firestore.v1.Firestore).
//
//   node record-f0.mjs [--only topic,topic]
//
// Data is written ONLY under top-level collections whose id starts with
// aruma-ref (assertScoped refuses anything else), everything written is deleted
// by one commit at the end, and the deletion is verified by a kindless query
// over every collection id from "aruma-ref" up to "aruma-reg". Nothing creates
// or changes an index, field override, rule, TTL policy or project setting.
import { createHash } from 'node:crypto';
import {
  PROJECT, DB, ROOT, rest, v, fields, versions, normalise, compact, writeCorpus, assertScoped,
  grpcAvailable, grpcUnary, grpcStream, grpcListen, grpcVersions, toGrpc,
} from './lib.mjs';

const args = process.argv.slice(2);
const onlyArg = args.includes('--only') ? args[args.indexOf('--only') + 1].split(',') : null;
const want = (t) => !onlyArg || onlyArg.includes(t);

const GENERATOR = 'test/oracle/documents/production/record-f0.mjs';
const MIN_ID = '__id-9223372036854775808__'; // firebase-tools delete.ts:14, nodejs-firestore REFERENCE_NAME_MIN_ID
const C = 'aruma-ref-f0'; // scratch collection for writes and errors
const TREE = 'aruma-ref-f0-tree';
const TREE2 = 'aruma-ref-f0-tree2';
const VEC = 'aruma-ref-f0-vec';
const VEC_EMPTY = 'aruma-ref-f0-vecempty'; // never written
const EXPLAIN = 'aruma-ref-f0-explain';
const TOPS = [C, TREE, TREE2, VEC, EXPLAIN];

const sha = (s) => createHash('sha256').update(s).digest('hex');
const F = (f, op, x) => ({ fieldFilter: { field: { fieldPath: f }, op, value: v(x) } });
const AND = (...filters) => ({ compositeFilter: { op: 'AND', filters } });
const OR = (...filters) => ({ compositeFilter: { op: 'OR', filters } });
const NAME_ASC = [{ field: { fieldPath: '__name__' }, direction: 'ASCENDING' }];
const range = (n, f = (i) => i + 1) => Array.from({ length: n }, (_, i) => f(i));

// ---- writes, tracked for the cleanup ----

const written = new Set();

async function commitWrites(writes, database = DB) {
  for (const w of writes) assertScoped(w.update?.name ?? w.delete);
  return rest('POST', `${database}/documents:commit`, { writes });
}

async function put(rel, data) {
  const name = `${ROOT}/${rel}`;
  const r = await commitWrites([{ update: { name, fields: fields(data) } }]);
  if (r.http !== 200) throw new Error(`setup write of ${rel.slice(0, 80)} failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  written.add(name);
}

// ---- recording helpers ----

// restCase sends one REST call and returns the case: the request (long strings
// compacted), the HTTP status, the normalised body and, for an error, its
// status and exact message.
async function restCase(id, description, method, path, body, { replace, database } = {}) {
  const r = await rest(method, path, body);
  const arr = Array.isArray(r.body) ? r.body : [r.body];
  const err = arr.find((x) => x && x.error)?.error;
  const relPath = path.replace(`projects/${PROJECT}/`, 'projects/{project}/');
  const c = {
    id, description, transport: 'rest',
    request: { method, path: relPath.length > 300 ? compact(relPath) : relPath },
    response: { http: r.http, bodyIsArray: Array.isArray(r.body), body: normalise(r.body, replace) },
  };
  if (body !== undefined) c.request.body = compact(body);
  if (database) c.database = database;
  if (err) c.error = { http: r.http, status: err.status, message: normalise(err.message, replace) };
  return { c, raw: r };
}

// grpcCase sends one gRPC call (unary or server streaming) and returns the case.
async function grpcCase(id, description, rpc, request, { stream = false, database = DB, replace } = {}) {
  const r = stream ? await grpcStream(rpc, request, database) : await grpcUnary(rpc, request, database);
  const c = {
    id, description, transport: 'grpc',
    request: { rpc: `google.firestore.v1.Firestore/${rpc[0].toUpperCase()}${rpc.slice(1)}`, message: compact(normalise(request, replace)) },
    response: r.ok
      ? { ok: true, [stream ? 'responses' : 'message']: normalise(stream ? r.responses : r.response, replace) }
      : { ok: false, ...(stream && r.responses.length ? { responses: normalise(r.responses, replace) } : {}) },
  };
  if (database !== DB) c.request.resourcePrefix = database.replace(`projects/${PROJECT}/`, 'projects/{project}/');
  if (!r.ok) c.error = { code: r.error.code, status: r.error.status, message: normalise(r.error.message, replace) };
  return { c, raw: r };
}

// grpcQuery converts a REST structuredQuery to proto-loader's form: the int32
// and double wrappers are messages there.
function grpcQuery(sq) {
  const q = toGrpc(sq);
  if (typeof q.limit === 'number') q.limit = { value: q.limit };
  if (q.findNearest) {
    if (typeof q.findNearest.limit === 'number') q.findNearest.limit = { value: q.findNearest.limit };
    if (typeof q.findNearest.distanceThreshold === 'number') q.findNearest.distanceThreshold = { value: q.findNearest.distanceThreshold };
  }
  return q;
}

const log = (s) => process.stderr.write(s);

// ---- (a) a database that does not exist ----

async function missingDatabase() {
  const X = `projects/${PROJECT}/databases/aruma-ref-nonexistent`;
  const XR = `${X}/documents`;
  const out = [];
  const q = { from: [{ collectionId: C }] };
  const write = { update: { name: `${XR}/${C}/x`, fields: fields({ a: 1 }) } };
  assertScoped(write.update.name);
  out.push((await restCase('rest.get', 'GET a document', 'GET', `${XR}/${C}/x`)).c);
  out.push((await restCase('rest.list', 'ListDocuments of a collection', 'GET', `${XR}/${C}`)).c);
  out.push((await restCase('rest.batchGet', 'BatchGetDocuments', 'POST', `${XR}:batchGet`, { documents: [`${XR}/${C}/x`] })).c);
  out.push((await restCase('rest.runQuery', 'RunQuery', 'POST', `${XR}:runQuery`, { structuredQuery: q })).c);
  out.push((await restCase('rest.runAggregationQuery', 'RunAggregationQuery count()', 'POST', `${XR}:runAggregationQuery`, { structuredAggregationQuery: { structuredQuery: q, aggregations: [{ alias: 'n', count: {} }] } })).c);
  out.push((await restCase('rest.commit', 'Commit one update', 'POST', `${XR}:commit`, { writes: [write] })).c);
  out.push((await restCase('rest.beginTransaction', 'BeginTransaction', 'POST', `${XR}:beginTransaction`, {})).c);
  out.push((await restCase('rest.listCollectionIds', 'ListCollectionIds of the root', 'POST', `${XR}:listCollectionIds`, {})).c);
  out.push((await restCase('admin.getDatabase', 'Admin API databases.get (what firebase deploy sends first, firebase-tools prepare.ts:122)', 'GET', X)).c);
  if (grpcAvailable()) {
    const g = (id, d, rpc, req, o = {}) => grpcCase(id, d, rpc, req, { database: X, ...o });
    out.push((await g('grpc.getDocument', 'GetDocument', 'getDocument', { name: `${XR}/${C}/x` })).c);
    out.push((await g('grpc.listDocuments', 'ListDocuments', 'listDocuments', { parent: XR, collectionId: C })).c);
    out.push((await g('grpc.batchGetDocuments', 'BatchGetDocuments', 'batchGetDocuments', { database: X, documents: [`${XR}/${C}/x`] }, { stream: true })).c);
    out.push((await g('grpc.runQuery', 'RunQuery', 'runQuery', { parent: XR, structuredQuery: grpcQuery(q) }, { stream: true })).c);
    out.push((await g('grpc.commit', 'Commit one update', 'commit', { database: X, writes: [toGrpc(write)] })).c);
    out.push((await g('grpc.beginTransaction', 'BeginTransaction', 'beginTransaction', { database: X })).c);
    const listenReq = { database: X, addTarget: { targetId: 1, query: { parent: XR, structuredQuery: grpcQuery(q) } } };
    const l = await grpcListen(listenReq, X);
    const lc = {
      id: 'grpc.listen', description: 'Listen with one query target (what web and mobile clients open)', transport: 'grpc',
      request: { rpc: 'google.firestore.v1.Firestore/Listen', message: listenReq, resourcePrefix: X.replace(`projects/${PROJECT}/`, 'projects/{project}/') },
      response: { ok: l.ok, responses: normalise(l.responses), ...(l.note ? { note: l.note } : {}) },
    };
    if (!l.ok) lc.error = l.error;
    out.push(lc);
  }
  return {
    cases: out,
    notes: [
      'The database id aruma-ref-nonexistent was never created; every call names it in the path (REST) or in the request and the google-cloud-resource-prefix and x-goog-request-params metadata (gRPC).',
      'Over gRPC the status message is empty. A separate read-only check (2026-10-26) sent GetDocument with both metadata entries, with only one of them, and with neither: always code 5, details "", and no trailer but x-debug-tracking-id (no grpc-status-details-bin).',
      'The REST message ends with a space after "database." and has no full stop after the project id.',
    ],
  };
}

// ---- (b) findNearest on Standard with no vector index ----

async function findNearest() {
  await put(`${VEC}/v1`, { emb: { $vector: [1, 2, 3] }, k: 'a' });
  await put(`${VEC}/v2`, { emb: { $vector: [2, 3, 4] }, k: 'b' });
  const fn = (coll, extra = {}, where) => ({
    from: [{ collectionId: coll }],
    ...(where ? { where } : {}),
    findNearest: { vectorField: { fieldPath: 'emb' }, queryVector: v({ $vector: [1, 2, 3] }), distanceMeasure: 'EUCLIDEAN', limit: 2, ...extra },
  });
  const out = [];
  const qs = [
    ['rest.populated', 'findNearest EUCLIDEAN limit 2 on a collection with two vector documents', fn(VEC)],
    ['rest.empty', 'the same on a never-written collection', fn(VEC_EMPTY)],
    ['rest.cosine_threshold_field', 'COSINE with distanceResultField and distanceThreshold', fn(VEC, { distanceMeasure: 'COSINE', distanceResultField: 'dist', distanceThreshold: 0.5 })],
    ['rest.prefilter', "k == 'a' then findNearest (would need a composite vector index)", fn(VEC, {}, F('k', 'EQUAL', 'a'))],
  ];
  for (const [id, d, sq] of qs) out.push((await restCase(id, d, 'POST', `${ROOT}:runQuery`, { structuredQuery: sq })).c);
  if (grpcAvailable()) {
    out.push((await grpcCase('grpc.populated', 'findNearest EUCLIDEAN limit 2', 'runQuery', { parent: ROOT, structuredQuery: grpcQuery(fn(VEC)) }, { stream: true })).c);
  }
  return { cases: out, data: { [`${VEC}/v1`]: { emb: 'vector [1,2,3]', k: 'a' }, [`${VEC}/v2`]: { emb: 'vector [2,3,4]', k: 'b' } } };
}

// ---- (c) explain_options ----

async function explain() {
  for (const n of [1, 2, 3]) await put(`${EXPLAIN}/e${n}`, { n });
  const sq = { from: [{ collectionId: EXPLAIN }], where: F('n', 'GREATER_THAN', 1) };
  const agg = { structuredQuery: sq, aggregations: [{ alias: 'c', count: {} }] };
  const out = [];
  out.push((await restCase('rest.query.analyze_false', 'runQuery n > 1, explainOptions {analyze: false}', 'POST', `${ROOT}:runQuery`, { structuredQuery: sq, explainOptions: { analyze: false } })).c);
  out.push((await restCase('rest.query.analyze_true', 'runQuery n > 1, explainOptions {analyze: true}', 'POST', `${ROOT}:runQuery`, { structuredQuery: sq, explainOptions: { analyze: true } })).c);
  out.push((await restCase('rest.query.options_empty', 'runQuery n > 1, explainOptions {}', 'POST', `${ROOT}:runQuery`, { structuredQuery: sq, explainOptions: {} })).c);
  out.push((await restCase('rest.query.no_options', 'runQuery n > 1 without explainOptions, for comparison', 'POST', `${ROOT}:runQuery`, { structuredQuery: sq })).c);
  out.push((await restCase('rest.aggregation.analyze_false', 'count() where n > 1, explainOptions {analyze: false}', 'POST', `${ROOT}:runAggregationQuery`, { structuredAggregationQuery: agg, explainOptions: { analyze: false } })).c);
  out.push((await restCase('rest.aggregation.analyze_true', 'count() where n > 1, explainOptions {analyze: true}', 'POST', `${ROOT}:runAggregationQuery`, { structuredAggregationQuery: agg, explainOptions: { analyze: true } })).c);
  out.push((await restCase('rest.query.analyze_true_missing_index', "explain analyze of a query that needs a composite index (n > 1 orderBy m)", 'POST', `${ROOT}:runQuery`, { structuredQuery: { ...sq, orderBy: [{ field: { fieldPath: 'm' }, direction: 'ASCENDING' }] }, explainOptions: { analyze: true } })).c);
  if (grpcAvailable()) {
    out.push((await grpcCase('grpc.query.analyze_false', 'runQuery n > 1, analyze false', 'runQuery', { parent: ROOT, structuredQuery: grpcQuery(sq), explainOptions: { analyze: false } }, { stream: true })).c);
    out.push((await grpcCase('grpc.query.analyze_true', 'runQuery n > 1, analyze true', 'runQuery', { parent: ROOT, structuredQuery: grpcQuery(sq), explainOptions: { analyze: true } }, { stream: true })).c);
  }
  return { cases: out, data: { [`${EXPLAIN}/e1..e3`]: '{n: 1..3}' } };
}

// ---- (d) error shapes ----

// docSize is Google's storage size of a document with one string field
// (firebase.google.com/docs/firestore/storage-size): the name is the sum of
// each segment's UTF-8 length + 1, plus 16; a field name and a string value are
// their UTF-8 length + 1; a document adds 32.
function docSize(segments, field, value) {
  const name = segments.reduce((n, s) => n + Buffer.byteLength(s) + 1, 0) + 16;
  return name + Buffer.byteLength(field) + 1 + Buffer.byteLength(value) + 1 + 32;
}

async function errors() {
  const out = [];
  const g = grpcAvailable();
  const commitOne = (rel, data) => ({ writes: [{ update: { name: `${ROOT}/${rel}`, fields: fields(data) } }] });
  const track = (rel, r) => {
    if (r.http === 200) written.add(`${ROOT}/${rel}`);
  };

  // One value: production caps a single value at 1,048,487 bytes (found by the
  // first run of this recorder), below the document limit.
  const VALUE_CAP = 1048487;
  for (const [id, n] of [['value_size.at_cap', VALUE_CAP], ['value_size.over_cap', VALUE_CAP + 1]]) {
    const rel = `${C}/bigvalue`;
    const s = 'x'.repeat(n);
    const size = docSize([C, 'bigvalue'], 's', s);
    const { c, raw } = await restCase(`rest.${id}`, `commit one string field of ${n} bytes (computed document size ${size})`, 'POST', `${ROOT}:commit`, commitOne(rel, { s }));
    c.valueBytes = n;
    c.computedSize = size;
    track(rel, raw);
    out.push(c);
    if (g && n > VALUE_CAP) {
      const r = await grpcCase(`grpc.${id}`, `commit one string field of ${n} bytes`, 'commit', { database: DB, writes: [toGrpc(commitOne(rel, { s }).writes[0])] });
      r.c.valueBytes = n;
      if (r.raw.ok) written.add(`${ROOT}/${rel}`);
      out.push(r.c);
    }
    log('.');
  }

  // 1 MiB document: two string fields, each under the value cap, whose
  // computed size is exactly the limit and one byte over.
  const LIMIT = 1048576;
  for (const [id, over] of [['doc_size.at_limit', 0], ['doc_size.over_by_one', 1]]) {
    const rel = `${C}/big`;
    const s1 = 'x'.repeat(600000);
    const fixed = docSize([C, 'big'], 's1', s1) + Buffer.byteLength('s2') + 1 + 1;
    const s2 = 'y'.repeat(LIMIT - fixed + over);
    const size = fixed + s2.length;
    const { c, raw } = await restCase(`rest.${id}`, `commit a document of two string fields whose computed size is ${size} bytes (limit ${LIMIT})`, 'POST', `${ROOT}:commit`, commitOne(rel, { s1, s2 }));
    c.computedSize = size;
    track(rel, raw);
    out.push(c);
    if (g && over) {
      const r = await grpcCase(`grpc.${id}`, `commit, computed size ${size}`, 'commit', { database: DB, writes: [toGrpc(commitOne(rel, { s1, s2 }).writes[0])] });
      r.c.computedSize = size;
      if (r.raw.ok) written.add(`${ROOT}/${rel}`);
      out.push(r.c);
    }
    log('.');
  }

  // Document id 1500 / 1501 bytes.
  for (const n of [1500, 1501]) {
    const rel = `${C}/${'i'.repeat(n)}`;
    const { c, raw } = await restCase(`rest.id_${n}_bytes`, `commit a document whose id is ${n} bytes`, 'POST', `${ROOT}:commit`, commitOne(rel, { a: 1 }));
    track(rel, raw);
    out.push(c);
    if (g && n === 1501) {
      const r = await grpcCase(`grpc.id_${n}_bytes`, `commit, id ${n} bytes`, 'commit', { database: DB, writes: [toGrpc(commitOne(rel, { a: 1 }).writes[0])] });
      if (r.raw.ok) written.add(`${ROOT}/${rel}`);
      out.push(r.c);
    }
  }

  // A name over 6 KiB, every id under 1500 bytes.
  {
    const segs = [C];
    for (const ch of 'abcde') segs.push(ch.repeat(1500), 'c');
    segs.pop();
    const rel = segs.join('/');
    const full = `${ROOT}/${rel}`;
    const desc = `${segs.length / 2} levels, ids of 1500 bytes: ${Buffer.byteLength(rel)} bytes below documents/, ${Buffer.byteLength(full)} bytes as a full resource name`;
    const { c, raw } = await restCase('rest.name_over_6kib.commit', `commit; ${desc}`, 'POST', `${ROOT}:commit`, commitOne(rel, { a: 1 }));
    c.nameBytes = { relative: Buffer.byteLength(rel), resourceName: Buffer.byteLength(full) };
    track(rel, raw);
    out.push(c);
    const gget = await restCase('rest.name_over_6kib.get', `GET; ${desc}`, 'GET', full);
    gget.c.nameBytes = c.nameBytes;
    out.push(gget.c);
    if (g) {
      const r = await grpcCase('grpc.name_over_6kib.commit', `commit; ${desc}`, 'commit', { database: DB, writes: [toGrpc(commitOne(rel, { a: 1 }).writes[0])] });
      if (r.raw.ok) written.add(full);
      out.push(r.c);
    }
  }

  // The 6 KiB boundary: names of exactly L bytes below documents/ (ids of at
  // most 1500 bytes). 6127 + 17 is 6144 by the storage-size formula.
  for (const [L, data, tag] of [[6127, { a: 1 }, ''], [6127, {}, '_no_fields'], [6128, {}, '_no_fields'], [6128, { a: 1 }, ''], [6144, { a: 1 }, ''], [6145, { a: 1 }, '']]) {
    const segs = [C];
    for (const ch of 'abcd') segs.push(ch.repeat(1400), 'c');
    const head = segs.join('/');
    segs.push((tag ? 'f' : 'e').repeat(L - Buffer.byteLength(head) - 1));
    const rel = segs.join('/');
    const full = `${ROOT}/${rel}`;
    const what = Object.keys(data).length ? 'with one integer field' : 'with no fields';
    const { c, raw } = await restCase(`rest.name_bytes_${L}${tag}`, `commit a document ${what} whose name below documents/ is ${Buffer.byteLength(rel)} bytes (${Buffer.byteLength(full)} as a full resource name)`, 'POST', `${ROOT}:commit`, commitOne(rel, data));
    c.nameBytes = { relative: Buffer.byteLength(rel), resourceName: Buffer.byteLength(full), storageSizeFormula: segs.reduce((n, s) => n + Buffer.byteLength(s) + 1, 0) + 16 };
    track(rel, raw);
    out.push(c);
  }

  // Subcollection depth: N nested subcollections under the top-level one.
  for (const n of [99, 100, 101]) {
    const segs = [C, 'd'];
    for (let i = 1; i <= n; i++) segs.push(`c${i}`, 'd');
    const rel = segs.join('/');
    const { c, raw } = await restCase(`rest.depth_${n}`, `commit a document under ${n} nested subcollections (${segs.length} path segments, ${segs.length / 2} collections)`, 'POST', `${ROOT}:commit`, commitOne(rel, { a: 1 }));
    c.segments = segs.length;
    track(rel, raw);
    out.push(c);
    if (g && n === 101) {
      const r = await grpcCase(`grpc.depth_${n}`, `commit under ${n} nested subcollections`, 'commit', { database: DB, writes: [toGrpc(commitOne(rel, { a: 1 }).writes[0])] });
      if (r.raw.ok) written.add(`${ROOT}/${rel}`);
      out.push(r.c);
    }
  }
  log('.');

  // Disjunctions, in and not-in.
  const query = (where) => ({ structuredQuery: { from: [{ collectionId: C }], where } });
  const qcases = [
    ['or_30_same_field', 'OR of 30 a == i', OR(...range(30).map((i) => F('a', 'EQUAL', i)))],
    ['or_31_same_field', 'OR of 31 a == i', OR(...range(31).map((i) => F('a', 'EQUAL', i)))],
    ['or_31_distinct_fields', 'OR of 31 f_i == 1', OR(...range(31).map((i) => F(`f${i}`, 'EQUAL', 1)))],
    ['in_30', 'a in [1..30]', F('a', 'IN', range(30))],
    ['in_31', 'a in [1..31]', F('a', 'IN', range(31))],
    ['not_in_10', 'a not-in [1..10]', F('a', 'NOT_IN', range(10))],
    ['not_in_11', 'a not-in [1..11]', F('a', 'NOT_IN', range(11))],
    ['and_in_6x6', 'a in [1..6] && b in [1..6] (36 DNF disjuncts)', AND(F('a', 'IN', range(6)), F('b', 'IN', range(6)))],
  ];
  for (const [id, d, where] of qcases) {
    out.push((await restCase(`rest.${id}`, d, 'POST', `${ROOT}:runQuery`, query(where))).c);
    if (g && /31|11|6x6/.test(id)) out.push((await grpcCase(`grpc.${id}`, d, 'runQuery', { parent: ROOT, structuredQuery: grpcQuery(query(where).structuredQuery) }, { stream: true })).c);
  }
  log('.');

  // A transaction id that never existed.
  const bogus = Buffer.from('aruma-ref-no-such-transaction').toString('base64');
  const rel = `${C}/t1`;
  out.push((await restCase('rest.txn_unknown.commit', 'commit with a transaction id that never existed', 'POST', `${ROOT}:commit`, { transaction: bogus, ...commitOne(rel, { a: 1 }) }, { replace: new Map([[bogus, '<the unknown transaction id>']]) })).c);
  out.push((await restCase('rest.txn_unknown.get', 'GET in a transaction that never existed', 'GET', `${ROOT}/${rel}?transaction=${encodeURIComponent(bogus)}`, undefined, { replace: new Map([[bogus, '<the unknown transaction id>'], [encodeURIComponent(bogus), '<the unknown transaction id>']]) })).c);
  out.push((await restCase('rest.txn_unknown.runQuery', 'runQuery in a transaction that never existed', 'POST', `${ROOT}:runQuery`, { transaction: bogus, structuredQuery: { from: [{ collectionId: C }] } }, { replace: new Map([[bogus, '<the unknown transaction id>']]) })).c);
  out.push((await restCase('rest.txn_unknown.rollback', 'rollback of a transaction that never existed', 'POST', `${ROOT}:rollback`, { transaction: bogus }, { replace: new Map([[bogus, '<the unknown transaction id>']]) })).c);
  if (g) {
    const b = Buffer.from(bogus, 'base64');
    out.push((await grpcCase('grpc.txn_unknown.commit', 'commit with a transaction id that never existed', 'commit', { database: DB, transaction: b, writes: [toGrpc(commitOne(rel, { a: 1 }).writes[0])] }, { replace: new Map([[bogus, '<the unknown transaction id>']]) })).c);
  }
  log('.');

  // A transaction committed twice.
  const reuse = async (tag, begin, commit) => {
    const relT = `${C}/t-${tag}`;
    const b = await begin();
    const txn = b.txn;
    const replace = new Map([[txn, '<the transaction id from beginTransaction>']]);
    const first = await commit(txn, relT, replace, 'first');
    if (first.ok) written.add(`${ROOT}/${relT}`);
    const second = await commit(txn, relT, replace, 'second');
    return [b.c, first.c, second.c];
  };
  out.push(...(await reuse('rest', async () => {
    const { c, raw } = await restCase('rest.txn_reused.begin', 'beginTransaction (read-write)', 'POST', `${ROOT}:beginTransaction`, {});
    return { c, txn: raw.body.transaction };
  }, async (txn, relT, replace, which) => {
    const { c, raw } = await restCase(`rest.txn_reused.commit_${which}`, `the ${which} commit with that transaction`, 'POST', `${ROOT}:commit`, { transaction: txn, ...commitOne(relT, { a: which }) }, { replace });
    return { c, ok: raw.http === 200 };
  })));
  if (g) {
    out.push(...(await reuse('grpc', async () => {
      const { c, raw } = await grpcCase('grpc.txn_reused.begin', 'BeginTransaction (read-write)', 'beginTransaction', { database: DB });
      const txn = raw.response.transaction.toString('base64');
      c.response.message = normalise(raw.response, new Map([[txn, '<the transaction id from beginTransaction>']]));
      return { c, txn };
    }, async (txn, relT, replace, which) => {
      const { c, raw } = await grpcCase(`grpc.txn_reused.commit_${which}`, `the ${which} commit with that transaction`, 'commit', { database: DB, transaction: Buffer.from(txn, 'base64'), writes: [toGrpc(commitOne(relT, { a: which }).writes[0])] }, { replace });
      return { c, ok: raw.ok };
    })));
  }
  log('.');
  return {
    cases: out,
    notes: [
      'computedSize uses the storage-size rules of firebase.google.com/docs/firestore/storage-size (name: each segment + 1, plus 16; field name + 1; string + 1; document + 32).',
      'Request strings longer than 300 characters are written as their length and SHA-256 (compact in lib.mjs); messages are exact.',
      'Transaction ids are random per run; they are replaced by a placeholder wherever they appear in a request, a response or a message.',
    ],
  };
}

// ---- (e) kindless descendants and partitions; (f) showMissing ----

const TREE_DOCS = {
  [`${TREE}/a`]: { v: 1 },
  [`${TREE}/a/sub/x`]: { v: 2 },
  [`${TREE}/a/sub/x/deep/y`]: { v: 3 },
  [`${TREE}/a/other/z`]: { v: 4 },
  [`${TREE}/b`]: { v: 5 },
  [`${TREE}/m/kids/k1`]: { v: 6 }, // m itself is never written: a missing document
  [`${TREE2}/q`]: { v: 7 }, // a sibling collection the range must exclude
};

async function writeTree() {
  for (const [rel, data] of Object.entries(TREE_DOCS)) await put(rel, data);
}

const docNames = (body) => (Array.isArray(body) ? body : []).filter((x) => x.document).map((x) => x.document.name.replace(`${ROOT}/`, ''));

async function descendants() {
  const out = [];
  const nul = String.fromCharCode(0);
  const rangeWhere = (coll) => AND(
    F('__name__', 'GREATER_THAN_OR_EQUAL', { $ref: `${ROOT}/${coll}/${MIN_ID}` }),
    F('__name__', 'LESS_THAN', { $ref: `${ROOT}/${coll}${nul}/${MIN_ID}` }),
  );
  const select = { fields: [{ fieldPath: '__name__' }] };
  const shapes = [
    ['rest.cli_delete_collection_recursive', 'firebase-tools `firestore:delete -r aruma-ref-f0-tree` (delete.ts:170-235): parent = root, from [{allDescendants: true}], __name__ range, select __name__, orderBy __name__, limit',
      ROOT, { where: rangeWhere(TREE), limit: 50, from: [{ allDescendants: true }], select, orderBy: [{ field: { fieldPath: '__name__' } }] }],
    ['rest.cli_delete_collection_shallow', 'the same with allDescendants: false (firestore:delete of a collection without -r)',
      ROOT, { where: rangeWhere(TREE), limit: 50, from: [{ allDescendants: false }], select, orderBy: [{ field: { fieldPath: '__name__' } }] }],
    ['rest.cli_delete_document_recursive', 'firebase-tools `firestore:delete -r aruma-ref-f0-tree/a` (delete.ts:249-275): parent = the document, from [{allDescendants: true}], select __name__, orderBy __name__, limit',
      `${ROOT}/${TREE}/a`, { limit: 50, from: [{ allDescendants: true }], select, orderBy: [{ field: { fieldPath: '__name__' } }] }],
    ['rest.node_recursive_delete_collection', 'nodejs-firestore recursiveDelete(collection) (recursive-delete.js:190-215): kindless all-descendants, __name__ >= coll/MIN and < coll\\0/MIN, select __name__, limit',
      ROOT, { from: [{ allDescendants: true }], select, where: rangeWhere(TREE), orderBy: NAME_ASC, limit: 5000 }],
    ['rest.node_recursive_delete_document', 'nodejs-firestore recursiveDelete(document): kindless all-descendants under the document, select __name__, limit',
      `${ROOT}/${TREE}/a`, { from: [{ allDescendants: true }], select, orderBy: NAME_ASC, limit: 5000 }],
    ['rest.kindless_children_of_document', 'kindless, allDescendants false, parent = document a: the documents of its direct subcollections',
      `${ROOT}/${TREE}/a`, { from: [{ allDescendants: false }] }],
    ['rest.kindless_with_empty_collection_id', 'from [{collectionId: "", allDescendants: true}] under document a',
      `${ROOT}/${TREE}/a`, { from: [{ collectionId: '', allDescendants: true }], orderBy: NAME_ASC }],
    ['rest.kindless_with_field_filter', 'kindless all-descendants under document a filtered on v > 2',
      `${ROOT}/${TREE}/a`, { from: [{ allDescendants: true }], where: F('v', 'GREATER_THAN', 2) }],
    ['rest.kindless_order_by_field', 'kindless all-descendants under document a ordered by v',
      `${ROOT}/${TREE}/a`, { from: [{ allDescendants: true }], orderBy: [{ field: { fieldPath: 'v' }, direction: 'ASCENDING' }] }],
  ];
  for (const [id, d, parent, sq] of shapes) {
    const { c, raw } = await restCase(id, d, 'POST', `${parent}:runQuery`, { structuredQuery: sq });
    c.documents = docNames(raw.body);
    out.push(c);
  }
  if (grpcAvailable()) {
    const [, d, parent, sq] = shapes[0];
    const { c, raw } = await grpcCase('grpc.cli_delete_collection_recursive', d, 'runQuery', { parent, structuredQuery: grpcQuery(sq) }, { stream: true });
    c.documents = (raw.responses || []).filter((x) => x.document).map((x) => x.document.name.replace(`${ROOT}/`, ''));
    out.push(c);
  }

  // PartitionQuery on a small collection.
  const pq = (sq, extra) => ({ structuredQuery: sq, ...extra });
  const cg = { from: [{ collectionId: TREE, allDescendants: true }], orderBy: NAME_ASC };
  const parts = [
    ['rest.partition.cg_count_3', 'partitionQuery: collection group aruma-ref-f0-tree ordered by __name__, partitionCount 3', pq(cg, { partitionCount: '3' })],
    ['rest.partition.cg_count_2_page_1', 'the same with partitionCount 2 and pageSize 1', pq(cg, { partitionCount: '2', pageSize: 1 })],
    ['rest.partition.kindless_count_3', 'kindless all-descendants (no collectionId), partitionCount 3', pq({ from: [{ allDescendants: true }], orderBy: NAME_ASC }, { partitionCount: '3' })],
    ['rest.partition.not_collection_group', 'allDescendants false: not a collection-group query', pq({ from: [{ collectionId: TREE }], orderBy: NAME_ASC }, { partitionCount: '3' })],
    ['rest.partition.no_order', 'collection group with no orderBy', pq({ from: [{ collectionId: TREE, allDescendants: true }] }, { partitionCount: '3' })],
    ['rest.partition.with_filter', 'collection group with a filter v > 1', pq({ ...cg, where: F('v', 'GREATER_THAN', 1) }, { partitionCount: '3' })],
  ];
  for (const [id, d, body] of parts) {
    const parent = id.includes('kindless') ? `${ROOT}/${TREE}/a` : ROOT;
    out.push((await restCase(id, d, 'POST', `${parent}:partitionQuery`, body)).c);
  }
  if (grpcAvailable()) {
    out.push((await grpcCase('grpc.partition.cg_count_3', 'PartitionQuery, partitionCount 3', 'partitionQuery', { parent: ROOT, structuredQuery: grpcQuery(cg), partitionCount: '3' })).c);
  }
  return {
    cases: out,
    data: Object.fromEntries(Object.entries(TREE_DOCS).map(([k, d]) => [k, d])),
    notes: [
      `MIN_ID is ${MIN_ID} (firebase-tools src/firestore/delete.ts:14; nodejs-firestore REFERENCE_NAME_MIN_ID). "\\u0000" in a reference is the NUL character both tools append to the collection id for the upper bound.`,
      `${TREE}/m is never written: it exists only as the parent of m/kids/k1.`,
      'documents lists the names returned, relative to the database root, in the order returned.',
    ],
  };
}

async function listShowMissing() {
  const out = [];
  out.push((await restCase('rest.show_missing', `ListDocuments ${TREE}?showMissing=true`, 'GET', `${ROOT}/${TREE}?showMissing=true`)).c);
  out.push((await restCase('rest.show_missing_mask_name', `ListDocuments ${TREE}?showMissing=true&mask.fieldPaths=__name__`, 'GET', `${ROOT}/${TREE}?showMissing=true&mask.fieldPaths=__name__`)).c);
  out.push((await restCase('rest.show_missing_order_by_name', `ListDocuments ${TREE}?showMissing=true&orderBy=__name__`, 'GET', `${ROOT}/${TREE}?showMissing=true&orderBy=__name__`)).c);
  const p1 = await restCase('rest.show_missing_page_size_2', `ListDocuments ${TREE}?showMissing=true&pageSize=2 (first page)`, 'GET', `${ROOT}/${TREE}?showMissing=true&pageSize=2`);
  const token = p1.raw.body.nextPageToken;
  if (token) p1.c.response.body.nextPageToken = '<nextPageToken>';
  out.push(p1.c);
  if (token) {
    const p2 = await restCase('rest.show_missing_page_size_2_next', 'the second page (pageToken from the first)', 'GET', `${ROOT}/${TREE}?showMissing=true&pageSize=2&pageToken=${encodeURIComponent(token)}`, undefined, { replace: new Map([[encodeURIComponent(token), '<nextPageToken>'], [token, '<nextPageToken>']]) });
    p2.c.request.path = p2.c.request.path.split(encodeURIComponent(token)).join('<nextPageToken>');
    out.push(p2.c);
  }
  out.push((await restCase('rest.without_show_missing', `ListDocuments ${TREE} (showMissing false)`, 'GET', `${ROOT}/${TREE}`)).c);
  out.push((await restCase('rest.show_missing_with_order_by_field', `ListDocuments ${TREE}?showMissing=true&orderBy=v`, 'GET', `${ROOT}/${TREE}?showMissing=true&orderBy=v`)).c);
  out.push((await restCase('rest.get_missing_document', `GET ${TREE}/m (the missing parent)`, 'GET', `${ROOT}/${TREE}/m`)).c);
  out.push((await restCase('rest.list_collection_ids_of_missing', `listCollectionIds of ${TREE}/m`, 'POST', `${ROOT}/${TREE}/m:listCollectionIds`, {})).c);
  out.push((await restCase('rest.batch_get_missing', `batchGet ${TREE}/m`, 'POST', `${ROOT}:batchGet`, { documents: [`${ROOT}/${TREE}/m`] })).c);
  out.push((await restCase('rest.query_does_not_return_missing', `runQuery from ${TREE} (a missing document is not a query result)`, 'POST', `${ROOT}:runQuery`, { structuredQuery: { from: [{ collectionId: TREE }] } })).c);
  if (grpcAvailable()) {
    out.push((await grpcCase('grpc.admin_list_documents', `ListDocuments as Admin Node listDocuments() sends it: showMissing true, mask {fieldPaths: []} (collection-reference.js:144-145)`, 'listDocuments', { parent: ROOT, collectionId: TREE, showMissing: true, mask: { fieldPaths: [] } })).c);
    out.push((await grpcCase('grpc.empty_collection_id', `ListDocuments parent ${TREE}/a, collectionId "" (every collection), showMissing true`, 'listDocuments', { parent: `${ROOT}/${TREE}/a`, collectionId: '', showMissing: true })).c);
    out.push((await grpcCase('grpc.empty_collection_id_missing_parent', `ListDocuments parent ${TREE}/m, collectionId "", showMissing true`, 'listDocuments', { parent: `${ROOT}/${TREE}/m`, collectionId: '', showMissing: true })).c);
    out.push((await grpcCase('grpc.empty_collection_id_no_show_missing', `ListDocuments parent ${TREE}/a, collectionId "" (every collection), showMissing false`, 'listDocuments', { parent: `${ROOT}/${TREE}/a`, collectionId: '' })).c);
    out.push((await grpcCase('grpc.show_missing_page_size_2', `ListDocuments ${TREE}, showMissing true, pageSize 2`, 'listDocuments', { parent: ROOT, collectionId: TREE, showMissing: true, pageSize: 2 }, { replace: new Map() })).c);
  }
  return { cases: out, data: Object.fromEntries(Object.entries(TREE_DOCS)) };
}

// ---- cleanup ----

async function cleanup() {
  const names = [...written];
  // Children before parents is not required by Firestore; one commit deletes all.
  const res = [];
  for (let i = 0; i < names.length; i += 400) {
    const r = await commitWrites(names.slice(i, i + 400).map((n) => ({ delete: n })));
    res.push(r.http);
    if (r.http !== 200) throw new Error(`cleanup commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
  // Verify: every document in any collection whose id runs from "aruma-ref" to
  // "aruma-reg" (exclusive), at any depth, through one kindless query.
  const sweep = await rest('POST', `${ROOT}:runQuery`, {
    structuredQuery: {
      from: [{ allDescendants: true }],
      where: AND(
        F('__name__', 'GREATER_THAN_OR_EQUAL', { $ref: `${ROOT}/aruma-ref/${MIN_ID}` }),
        F('__name__', 'LESS_THAN', { $ref: `${ROOT}/aruma-reg/${MIN_ID}` }),
      ),
      select: { fields: [{ fieldPath: '__name__' }] },
      orderBy: NAME_ASC,
      limit: 100,
    },
  });
  const remaining = docNames(sweep.body);
  const lists = {};
  for (const t of TOPS) {
    const l = await rest('GET', `${ROOT}/${t}?showMissing=true&pageSize=100`);
    lists[t] = (l.body.documents || []).map((d) => d.name.replace(`${ROOT}/`, ''));
  }
  const ok = sweep.http === 200 && remaining.length === 0 && Object.values(lists).every((l) => l.length === 0);
  return {
    ok,
    deletedCount: names.length,
    deleted: names.map((n) => compact(n.replace(`${ROOT}/`, ''), 200)),
    commitHttp: res,
    sweep: { http: sweep.http, query: `kindless allDescendants, __name__ in [aruma-ref/${MIN_ID}, aruma-reg/${MIN_ID})`, remaining },
    listsShowMissing: lists,
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
  try {
    if (want('missing-database')) { log('missing-database '); results['f0-missing-database.json'] = await missingDatabase(); }
    if (want('find-nearest')) { log('\nfind-nearest '); results['f0-find-nearest.json'] = await findNearest(); }
    if (want('explain')) { log('\nexplain '); results['f0-explain.json'] = await explain(); }
    if (want('errors')) { log('\nerrors '); results['f0-errors.json'] = await errors(); }
    if (want('descendants') || want('list-show-missing')) await writeTree();
    if (want('descendants')) { log('\ndescendants '); results['f0-descendants.json'] = await descendants(); }
    if (want('list-show-missing')) { log('\nlist-show-missing '); results['f0-list-show-missing.json'] = await listShowMissing(); }
  } finally {
    log('\ncleanup ');
    const cl = await cleanup();
    results['f0-cleanup.json'] = { cleanup: cl };
    log(cl.ok ? 'verified\n' : 'NOT VERIFIED\n');
    if (!cl.ok) process.exitCode = 2;
  }
  const finished = new Date().toISOString();
  for (const [file, body] of Object.entries(results)) {
    const header = {
      oracle: 'production',
      target,
      generator: `${GENERATOR}${onlyArg ? ` --only ${onlyArg.join(',')}` : ''}`,
      recordedAt: `${started} .. ${finished}`,
      versions: ver,
      notes: [
        'Auth: the firebase CLI login (a project owner), so security rules do not apply; end-user behaviour is not recorded here.',
        'Times, durations and transaction ids are replaced by placeholders (normalise in lib.mjs); everything else is as returned.',
        ...(body.notes || []),
      ],
    };
    const { notes, ...payload } = body;
    writeCorpus(file, header, payload);
  }
}

main().catch((e) => {
  console.error('record-f0 failed:', e.message);
  process.exit(1);
});
