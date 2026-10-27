// F3 production recordings for the Document Database's queries, aggregations,
// indexes and Admin API (plan section 6, F3; ADR-025 §1, §2, §3, §4.3, §4.5, §5,
// §6):
//
//   f3-run-query.json     RunQuery semantics on one fixture of every value type:
//                         result order for mixed types, a missing ordered field,
//                         the implicit order of an inequality, cursors (partial
//                         values, __name__, more values than the order), offset
//                         and skippedResults, limit 0, projections, every
//                         operator (IS_NAN, IS_NULL and their NOT_ forms, null
//                         and NaN in in, not-in and !=), array-contains(-any),
//                         OR and nested AND/OR, the shape of an empty result,
//                         collection groups, kindless all-descendants queries
//                         with __name__ ranges, transaction / newTransaction /
//                         readTime, and the gRPC stream's elements
//   f3-query-limits.json  the refusals: disjunction, in, not-in and inequality
//                         counts, filter counts, two not-in, not-in with !=,
//                         two array-contains, cursors, limit and offset, field
//                         paths, __name__ filters of the wrong type, structure
//                         errors, and the missing-index error over gRPC
//   f3-aggregations.json  count (upTo), sum (int64 overflow, mixed types, NaN,
//                         the infinities, an empty set), avg, several
//                         aggregations, aliases, more than five, and the
//                         missing-index error over gRPC
//   f3-partition-query.json  PartitionQuery on a collection group of a few
//                         thousand documents, paging, and each refused shape
//   f3-admin.json         read-only Admin API shapes: the database, composite
//                         indexes, field overrides (with firebase-tools' filter),
//                         __default__/fields/*, a single field, and missing
//                         indexes, fields and operations
//   f3-index-entries.json the per-document index entry limits, reached by writes
//                         alone: 40,000 entries (fields, array elements, map
//                         subfields) and the 8 MiB total of entry sizes
//   f3-cleanup.json       the deletion of everything written, verified
//
//   node record-f3.mjs [--only=run-query,limits,aggregations,partition,admin,index-entries]
//
// REST throughout (firestore.googleapis.com/v1), and gRPC where FIRESTORE_PROTOS
// is set for the cases whose transport matters (stream element shapes, the
// missing-index code).
//
// Files with replayable cases (`replay: true`: a fixed REST request whose answer
// depends only on the file's `setup` section) have a `setup` section, as in
// record-f1.mjs. A case whose message holds a console link or a gcloud command is
// never replayable: the link encodes the project id.
//
// Data is written ONLY under top-level collections whose id starts with
// aruma-ref-f3 (assertScoped refuses anything outside aruma-ref). A kindless
// query over every collection id from "aruma-ref-f3" up to "aruma-ref-f4" runs
// first (strays are listed and deleted) and last (it must find nothing), with
// showMissing lists of each collection used. A run whose cleanup is not verified
// writes no corpus file. Nothing creates or changes an index, field override,
// rule, TTL policy, user or project setting: the Admin API calls are GETs.
import {
  PROJECT, DB, ROOT, PREFIX, rest, versions, normalise, compact, writeCorpus, assertScoped,
  grpcAvailable, grpcUnary, grpcStream, grpcVersions, toGrpc,
} from './lib.mjs';
import { writeFileSync } from 'node:fs';

const GENERATOR = 'test/oracle/documents/production/record-f3.mjs';
const MIN_ID = '__id-9223372036854775808__';
const Q = 'aruma-ref-f3-q'; // the query fixture
const QR = 'aruma-ref-f3-r'; // a second parent of the collection group
const QCG = 'aruma-ref-f3-qcg'; // the collection group id under both
const LIM = 'aruma-ref-f3-lim'; // limits
const AG = 'aruma-ref-f3-agg'; // aggregations: aruma-ref-f3-agg-<set>
const AGE = 'aruma-ref-f3-agg-empty'; // never written
const PB = 'aruma-ref-f3-part'; // PartitionQuery bulk collection group
const PX = 'aruma-ref-f3-pqx'; // PartitionQuery refusals
const IX = 'aruma-ref-f3-ix'; // index entry limits
const AGG_SETS = ['int', 'ovf', 'ovf-back', 'negovf', 'mix', 'nan', 'inf', 'dbl', 'big'];
const TOPS = [Q, QR, LIM, ...AGG_SETS.map((s) => `${AG}-${s}`), AGE, PB, PX, IX];
const P_DOCS = Number(process.env.F3_PARTITION_DOCS || 3000);

const log = (s) => process.stderr.write(s);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const pause = () => sleep(120); // keeps the request rate modest
const rel = (name) => (typeof name === 'string' ? name.replace(`${ROOT}/`, '') : name);
const relProject = (s) => s.split(`projects/${PROJECT}/`).join('projects/{project}/');
const g = grpcAvailable();

// ---- values ----

const I = (x) => ({ integerValue: String(x) });
const D = (x) => ({ doubleValue: x }); // a number, or 'NaN', 'Infinity', '-Infinity'
const S = (x) => ({ stringValue: x });
const A = (...vals) => ({ arrayValue: { values: vals } });
const M = (f) => ({ mapValue: { fields: f } });
const R = (path) => ({ referenceValue: `${ROOT}/${path}` });
const NULL = { nullValue: null };
const NAN = D('NaN');
const INT64_MAX = '9223372036854775807';
const INT64_MIN = '-9223372036854775808';

function jsonSafe(x) {
  if (typeof x === 'number') {
    if (Object.is(x, -0)) return '<number -0>';
    if (Number.isNaN(x)) return '<number NaN>';
    if (!Number.isFinite(x)) return `<number ${x}>`;
    return x;
  }
  if (Buffer.isBuffer(x)) return x;
  if (Array.isArray(x)) return x.map(jsonSafe);
  if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, jsonSafe(y)]));
  return x;
}

// gq turns a REST request (structured query JSON) into what proto-loader
// encodes: the null enum, doubles as numbers, limit as an Int32Value.
function gq(x, key = '') {
  if (Array.isArray(x)) return x.map((y) => gq(y));
  if (x && typeof x === 'object') {
    const o = {};
    for (const [k, y] of Object.entries(x)) {
      if (k === 'nullValue') o[k] = 'NULL_VALUE';
      else if (k === 'doubleValue') o[k] = typeof y === 'string' ? Number(y) : y;
      else if (k === 'limit' && typeof y === 'number' && key === 'structuredQuery') o[k] = { value: y };
      else if (k === 'upTo' && typeof y !== 'object') o[k] = { value: String(y) };
      else o[k] = gq(y, k);
    }
    return o;
  }
  return x;
}

// ---- filters ----

const fp = (p) => ({ fieldPath: p });
const ff = (p, op, value) => ({ fieldFilter: { field: fp(p), op, value } });
const uf = (p, op) => ({ unaryFilter: { field: fp(p), op } });
const and = (...filters) => ({ compositeFilter: { op: 'AND', filters } });
const or = (...filters) => ({ compositeFilter: { op: 'OR', filters } });
const asc = (p) => ({ field: fp(p), direction: 'ASCENDING' });
const desc = (p) => ({ field: fp(p), direction: 'DESCENDING' });
const from = (c, allDescendants) => [allDescendants === undefined ? { collectionId: c } : { collectionId: c, allDescendants }];

// ---- writes, tracked for the cleanup ----

const written = new Set();
const openTxns = new Set();

async function commitWrites(writes) {
  for (const w of writes) assertScoped(w.update?.name ?? w.delete ?? '');
  const r = await rest('POST', `${DB}/documents:commit`, { writes });
  if (r.http === 200) for (const w of writes) if (w.update) written.add(w.update.name);
  return r;
}

// setup writes fixtures by commits of at most 400 writes and returns the
// setup section (the first commit's writes are all of them for small fixtures).
async function setup(docs) {
  const writes = Object.entries(docs).map(([r, f]) => ({ update: { name: `${ROOT}/${r}`, fields: f } }));
  const commits = [];
  let last;
  for (let i = 0; i < writes.length; i += 400) {
    const part = writes.slice(i, i + 400);
    let res = await commitWrites(part);
    for (let t = 0; t < 3 && (res.http === 409 || res.http >= 500); t++) {
      await sleep(3000);
      res = await commitWrites(part);
    }
    if (res.http !== 200) throw new Error(`setup commit failed: HTTP ${res.http} ${JSON.stringify(res.body).slice(0, 300)}`);
    commits.push({ writes: JSON.parse(relProject(JSON.stringify(part))) });
    last = res.body;
    await pause();
  }
  return { commits, commitTime: last.commitTime };
}

// ---- recording helpers ----

const linked = (m) => typeof m === 'string' && (m.includes('console.firebase.google.com') || m.includes('gcloud '));
const replayKeys = new Map();

function names(body) {
  if (!Array.isArray(body)) return undefined;
  const n = body.filter((x) => x && x.document).map((x) => rel(x.document.name));
  return n;
}

// restCase sends one REST call and returns the case.
async function restCase(id, description, method, path, body, { replay = false, replace = new Map() } = {}) {
  const r = await rest(method, path, body);
  await pause();
  // Only documents of the aruma-ref collections may enter the corpus: a query
  // that reaches the project's own data is a recorder bug.
  for (const n of names(r.body) ?? []) if (!n.startsWith(PREFIX)) throw new Error(`${id}: the answer holds a document outside ${PREFIX}*; not recorded`);
  const arr = Array.isArray(r.body) ? r.body : [r.body];
  const err = arr.find((x) => x && x.error)?.error;
  const c = {
    id, description, transport: 'rest',
    request: { method, path: relProject(path) },
    response: { http: r.http, bodyIsArray: Array.isArray(r.body), body: normalise(jsonSafe(r.body), replace) },
  };
  if (body !== undefined) c.request.body = JSON.parse(relProject(JSON.stringify(compact(normalise(jsonSafe(body), replace), 2000))));
  if (err) c.error = { http: r.http, status: err.status, message: normalise(err.message, replace) };
  if (r.body && r.body.raw !== undefined) c.responseNotJson = true;
  const ids = names(r.body);
  if (ids) c.documents = ids;
  if (Array.isArray(r.body)) c.elementKeys = r.body.map((x) => Object.keys(x || {}).sort().join(','));
  if (replay && !linked(err?.message)) {
    const key = `${method} ${c.request.path} ${JSON.stringify(c.request.body)}`;
    if (replayKeys.has(key)) log(`\n  (${id}: same request as ${replayKeys.get(key)}; not replayable)\n`);
    else {
      replayKeys.set(key, id);
      c.replay = true;
    }
  }
  return { c, raw: r };
}

async function grpcCase(id, description, rpc, request, { stream = false, replace = new Map() } = {}) {
  const r = stream ? await grpcStream(rpc, request) : await grpcUnary(rpc, request);
  await pause();
  const c = {
    id, description, transport: 'grpc',
    request: { rpc: `google.firestore.v1.Firestore/${rpc[0].toUpperCase()}${rpc.slice(1)}`, message: JSON.parse(relProject(JSON.stringify(compact(normalise(jsonSafe(request), replace))))) },
    response: r.ok
      ? { ok: true, [stream ? 'responses' : 'message']: normalise(jsonSafe(stream ? r.responses : r.response), replace) }
      : { ok: false, ...(stream && r.responses.length ? { responses: normalise(jsonSafe(r.responses), replace) } : {}) },
  };
  if (!r.ok) c.error = { code: r.error.code, status: r.error.status, message: normalise(r.error.message, replace) };
  if (stream) c.elementKeys = (r.responses || []).map((x) => Object.keys(x).filter((k) => x[k] !== undefined && x[k] !== null).sort().join(','));
  return { c, raw: r };
}

// ---- RunQuery semantics ----

const QDOCS = {
  [`${Q}/n1`]: { v: NULL, k: I(1), arr: A(I(1), I(2)), g: S('a') },
  [`${Q}/b0`]: { v: { booleanValue: false }, k: I(2), arr: A(I(2), I(3)), g: S('a') },
  [`${Q}/b1`]: { v: { booleanValue: true }, k: I(3), g: S('b') },
  [`${Q}/nan`]: { v: NAN, k: I(4), arr: A(NAN), g: S('b') },
  [`${Q}/ninf`]: { v: D('-Infinity'), k: I(5), g: S('c') },
  [`${Q}/i1`]: { v: I(1), k: I(6), arr: A(I(1)), g: S('c') },
  [`${Q}/d15`]: { v: D(1.5), k: I(7), g: S('a') },
  [`${Q}/i2`]: { v: I(2), k: I(8), arr: A(NULL), g: S('b') },
  [`${Q}/d2`]: { v: D(2), k: I(9), g: S('c') },
  [`${Q}/ts`]: { v: { timestampValue: '2020-01-01T00:00:00Z' }, k: I(10), g: S('a') },
  [`${Q}/sa`]: { v: S('a'), k: I(11), arr: A(S('a')), g: S('b') },
  [`${Q}/sb`]: { v: S('b'), k: I(12), arr: A(S('a'), S('b')), g: S('c') },
  [`${Q}/by`]: { v: { bytesValue: 'AAE=' }, k: I(13), g: S('a') },
  [`${Q}/geo`]: { v: { geoPointValue: { latitude: 1.5, longitude: 2.5 } }, k: I(14), g: S('b') },
  [`${Q}/arr`]: { v: A(I(1), I(2)), k: I(15), g: S('c') },
  [`${Q}/map`]: { v: M({ a: I(1) }), k: I(16), g: S('a') },
  [`${Q}/none`]: { k: I(17), g: S('b') },
  [`${Q}/nok`]: { v: S('c'), g: S('z') },
  [`${Q}/i1/${QCG}/c1`]: { w: I(1) },
  [`${Q}/i1/${QCG}/c1/deep/y`]: { w: I(3) },
  [`${QR}/x/${QCG}/c2`]: { w: I(2) },
  [`${Q}/ghost/${QCG}/c3`]: { w: I(4) },
};

async function runQuery(setupInfo) {
  const out = [];
  const qp = ROOT;
  const rq = async (id, d, sq, o = {}) => {
    const { c, raw } = await restCase(id, d, 'POST', `${o.parent ?? qp}:runQuery`, { structuredQuery: sq, ...(o.extra || {}) }, { replay: o.replay ?? true, replace: o.replace });
    out.push(c);
    return { c, raw };
  };
  const base = (extra = {}) => ({ from: from(Q), ...extra });

  // order
  await rq('order.v_asc', 'orderBy v: every type, ties of 2 and 2.0, the document without v left out', base({ orderBy: [asc('v')] }));
  await rq('order.v_desc', 'orderBy v descending', base({ orderBy: [desc('v')] }));
  await rq('order.k_asc', 'orderBy k (nok has no k)', base({ orderBy: [asc('k')] }));
  await rq('order.none', 'no filter, no order: by __name__', base());
  await rq('order.name_asc', 'orderBy __name__', base({ orderBy: [asc('__name__')] }));
  await rq('order.v_asc_limit_3', 'orderBy v, limit 3', base({ orderBy: [asc('v')], limit: 3 }));
  await rq('order.v_twice', 'orderBy v, then v again', base({ orderBy: [asc('v'), asc('v')] }));
  await rq('order.v_asc_v_desc', 'orderBy v asc, then v desc', base({ orderBy: [asc('v'), desc('v')] }));
  await rq('order.map_subfield', 'orderBy v.a', base({ orderBy: [asc('v.a')] }));

  // inequality and its implicit order
  await rq('filter.v_gt_1', 'v > 1: numbers only, implicit orderBy v', base({ where: ff('v', 'GREATER_THAN', I(1)) }));
  await rq('filter.v_gte_1_lt_2', '1 <= v < 2', base({ where: and(ff('v', 'GREATER_THAN_OR_EQUAL', I(1)), ff('v', 'LESS_THAN', I(2))) }));
  await rq('filter.v_gt_1_desc', 'v > 1, orderBy v desc', base({ where: ff('v', 'GREATER_THAN', I(1)), orderBy: [desc('v')] }));
  await rq('filter.v_gt_neg_inf', 'v > -Infinity (NaN is below -Infinity)', base({ where: ff('v', 'GREATER_THAN', D('-Infinity')) }));
  await rq('filter.v_lt_nan', 'v < NaN', base({ where: ff('v', 'LESS_THAN', NAN) }));
  await rq('filter.v_lte_nan', 'v <= NaN', base({ where: ff('v', 'LESS_THAN_OR_EQUAL', NAN) }));
  await rq('filter.v_gt_null', 'v > null', base({ where: ff('v', 'GREATER_THAN', NULL) }));
  await rq('filter.v_gte_null', 'v >= null', base({ where: ff('v', 'GREATER_THAN_OR_EQUAL', NULL) }));
  await rq('filter.v_gt_a', 'v > "a": strings only', base({ where: ff('v', 'GREATER_THAN', S('a')) }));
  await rq('filter.v_gt_false', 'v > false', base({ where: ff('v', 'GREATER_THAN', { booleanValue: false }) }));
  await rq('filter.v_gt_array_empty', 'v > []', base({ where: ff('v', 'GREATER_THAN', A()) }));
  await rq('filter.k_gt_3_limit_2_offset_1', 'k > 3, offset 1, limit 2', base({ where: ff('k', 'GREATER_THAN', I(3)), offset: 1, limit: 2 }));

  // equality operators with null and NaN
  await rq('filter.v_eq_null', 'v == null', base({ where: ff('v', 'EQUAL', NULL) }));
  await rq('filter.v_eq_nan', 'v == NaN (as a field filter)', base({ where: ff('v', 'EQUAL', NAN) }));
  await rq('filter.v_eq_2', 'v == 2 (matches 2 and 2.0)', base({ where: ff('v', 'EQUAL', I(2)) }));
  await rq('filter.v_eq_2_0', 'v == 2.0', base({ where: ff('v', 'EQUAL', D(2)) }));
  await rq('filter.v_neq_null', 'v != null', base({ where: ff('v', 'NOT_EQUAL', NULL) }));
  await rq('filter.v_neq_nan', 'v != NaN', base({ where: ff('v', 'NOT_EQUAL', NAN) }));
  await rq('filter.v_neq_1', 'v != 1', base({ where: ff('v', 'NOT_EQUAL', I(1)) }));
  await rq('filter.v_in_null', 'v in [null]', base({ where: ff('v', 'IN', A(NULL)) }));
  await rq('filter.v_in_nan', 'v in [NaN]', base({ where: ff('v', 'IN', A(NAN)) }));
  await rq('filter.v_in_null_nan_1', 'v in [null, NaN, 1]', base({ where: ff('v', 'IN', A(NULL, NAN, I(1))) }));
  await rq('filter.v_in_dup', 'v in [1, 1, 1.0]', base({ where: ff('v', 'IN', A(I(1), I(1), D(1))) }));
  await rq('filter.v_in_array', 'v in [[1, 2]] (an array value)', base({ where: ff('v', 'IN', A(A(I(1), I(2)))) }));
  await rq('filter.v_not_in_1_2', 'v not-in [1, 2]', base({ where: ff('v', 'NOT_IN', A(I(1), I(2))) }));
  await rq('filter.v_not_in_null', 'v not-in [null]', base({ where: ff('v', 'NOT_IN', A(NULL)) }));
  await rq('filter.v_not_in_nan', 'v not-in [NaN]', base({ where: ff('v', 'NOT_IN', A(NAN)) }));
  await rq('filter.v_not_in_null_nan', 'v not-in [null, NaN]', base({ where: ff('v', 'NOT_IN', A(NULL, NAN)) }));
  await rq('filter.v_is_nan', 'v IS_NAN', base({ where: uf('v', 'IS_NAN') }));
  await rq('filter.v_is_null', 'v IS_NULL', base({ where: uf('v', 'IS_NULL') }));
  await rq('filter.v_is_not_nan', 'v IS_NOT_NAN', base({ where: uf('v', 'IS_NOT_NAN') }));
  await rq('filter.v_is_not_null', 'v IS_NOT_NULL', base({ where: uf('v', 'IS_NOT_NULL') }));
  await rq('filter.k_is_nan', 'k IS_NAN (k is an integer everywhere)', base({ where: uf('k', 'IS_NAN') }));
  await rq('filter.missing_is_null', 'zz IS_NULL (no document has zz)', base({ where: uf('zz', 'IS_NULL') }));
  await rq('filter.missing_neq', 'zz != 1 (no document has zz)', base({ where: ff('zz', 'NOT_EQUAL', I(1)) }));

  // arrays
  await rq('array.contains_1', 'arr array-contains 1', base({ where: ff('arr', 'ARRAY_CONTAINS', I(1)) }));
  await rq('array.contains_1_0', 'arr array-contains 1.0', base({ where: ff('arr', 'ARRAY_CONTAINS', D(1)) }));
  await rq('array.contains_null', 'arr array-contains null', base({ where: ff('arr', 'ARRAY_CONTAINS', NULL) }));
  await rq('array.contains_nan', 'arr array-contains NaN', base({ where: ff('arr', 'ARRAY_CONTAINS', NAN) }));
  await rq('array.contains_any_1_a', 'arr array-contains-any [1, "a"]', base({ where: ff('arr', 'ARRAY_CONTAINS_ANY', A(I(1), S('a'))) }));
  await rq('array.contains_any_null', 'arr array-contains-any [null]', base({ where: ff('arr', 'ARRAY_CONTAINS_ANY', A(NULL)) }));
  await rq('array.contains_any_nan', 'arr array-contains-any [NaN]', base({ where: ff('arr', 'ARRAY_CONTAINS_ANY', A(NAN)) }));
  await rq('array.contains_on_scalar', 'v array-contains 1 (v is an array in one document)', base({ where: ff('v', 'ARRAY_CONTAINS', I(1)) }));
  await rq('array.eq_whole', 'v == [1, 2]', base({ where: ff('v', 'EQUAL', A(I(1), I(2))) }));

  // OR and nesting
  await rq('or.v_1_or_k_3', 'v == 1 OR k == 3', base({ where: or(ff('v', 'EQUAL', I(1)), ff('k', 'EQUAL', I(3))) }));
  await rq('or.overlap', 'g == "a" OR k == 1 (n1 matches both)', base({ where: or(ff('g', 'EQUAL', S('a')), ff('k', 'EQUAL', I(1))) }));
  await rq('or.single', 'OR of one filter k == 2', base({ where: or(ff('k', 'EQUAL', I(2))) }));
  await rq('or.and_single', 'AND of one filter k == 2', base({ where: and(ff('k', 'EQUAL', I(2))) }));
  await rq('or.and_of_ors', '(g == "a" OR g == "b") AND (k == 1 OR k == 3 OR k == 7)', base({ where: and(or(ff('g', 'EQUAL', S('a')), ff('g', 'EQUAL', S('b'))), or(ff('k', 'EQUAL', I(1)), ff('k', 'EQUAL', I(3)), ff('k', 'EQUAL', I(7)))) }));
  await rq('or.or_of_ands', '(g == "a" AND k == 1) OR (g == "c" AND k == 6)', base({ where: or(and(ff('g', 'EQUAL', S('a')), ff('k', 'EQUAL', I(1))), and(ff('g', 'EQUAL', S('c')), ff('k', 'EQUAL', I(6)))) }));
  await rq('or.in_or_eq', 'k in [1, 2] OR g == "c"', base({ where: or(ff('k', 'IN', A(I(1), I(2))), ff('g', 'EQUAL', S('c'))) }));
  await rq('or.range_same_field', 'k < 2 OR k > 15', base({ where: or(ff('k', 'LESS_THAN', I(2)), ff('k', 'GREATER_THAN', I(15))) }));
  await rq('or.nested_three', 'k == 1 OR (g == "b" AND (k == 3 OR k == 4))', base({ where: or(ff('k', 'EQUAL', I(1)), and(ff('g', 'EQUAL', S('b')), or(ff('k', 'EQUAL', I(3)), ff('k', 'EQUAL', I(4))))) }));
  await rq('or.is_null_or_is_nan', 'v IS_NULL OR v IS_NAN', base({ where: or(uf('v', 'IS_NULL'), uf('v', 'IS_NAN')) }));
  await rq('or.limit', 'g == "a" OR g == "b", limit 3', base({ where: or(ff('g', 'EQUAL', S('a')), ff('g', 'EQUAL', S('b'))), limit: 3 }));

  // cursors (orderBy k)
  const ok = { orderBy: [asc('k')] };
  const cur = (values, before) => ({ values, before });
  await rq('cursor.start_at_5', 'orderBy k, startAt [5]', base({ ...ok, startAt: cur([I(5)], true) }));
  await rq('cursor.start_after_5', 'orderBy k, startAfter [5] (startAt before:false)', base({ ...ok, startAt: cur([I(5)], false) }));
  await rq('cursor.end_at_8', 'orderBy k, endAt [8] (endAt before:false)', base({ ...ok, endAt: cur([I(8)], false) }));
  await rq('cursor.end_before_8', 'orderBy k, endBefore [8]', base({ ...ok, endAt: cur([I(8)], true) }));
  await rq('cursor.start_end', 'orderBy k, startAt [5], endAt [8]', base({ ...ok, startAt: cur([I(5)], true), endAt: cur([I(8)], false) }));
  await rq('cursor.start_after_end', 'orderBy k, startAt [8], endAt [5]: empty', base({ ...ok, startAt: cur([I(8)], true), endAt: cur([I(5)], false) }));
  await rq('cursor.no_before', 'orderBy k, startAt [5] with no before field', base({ ...ok, startAt: { values: [I(5)] } }));
  await rq('cursor.double_value', 'orderBy k, startAt [5.5]', base({ ...ok, startAt: cur([D(5.5)], true) }));
  await rq('cursor.string_value', 'orderBy k, startAt ["x"]: strings sort after numbers', base({ ...ok, startAt: cur([S('x')], true) }));
  await rq('cursor.null_value', 'orderBy k, startAt [null]', base({ ...ok, startAt: cur([NULL], true) }));
  await rq('cursor.with_name', 'orderBy k, startAt [6, ref i1] before:false (after i1)', base({ ...ok, startAt: cur([I(6), R(`${Q}/i1`)], false) }));
  await rq('cursor.with_name_string', 'orderBy k, startAt [6, "i1"] (a string for __name__)', base({ ...ok, startAt: cur([I(6), S('i1')], false) }));
  await rq('cursor.too_many_values', 'orderBy k, startAt [5, ref, 3]: more values than the order', base({ ...ok, startAt: cur([I(5), R(`${Q}/i1`), I(3)], true) }));
  await rq('cursor.too_many_values_end', 'orderBy k, endAt [5, ref, 3]', base({ ...ok, endAt: cur([I(5), R(`${Q}/i1`), I(3)], false) }));
  await rq('cursor.two_values_no_order', 'no orderBy, startAt [ref i1, 1]', base({ startAt: cur([R(`${Q}/i1`), I(1)], true) }));
  await rq('cursor.empty_values', 'orderBy k, startAt with values []', base({ ...ok, startAt: cur([], true) }));
  await rq('cursor.name_only', 'no orderBy, startAt [ref i1]', base({ startAt: cur([R(`${Q}/i1`)], true) }));
  await rq('cursor.name_order', 'orderBy __name__, startAfter [ref i1], endBefore [ref sa]', base({ orderBy: [asc('__name__')], startAt: cur([R(`${Q}/i1`)], false), endAt: cur([R(`${Q}/sa`)], true) }));
  await rq('cursor.name_string', 'orderBy __name__, startAt ["i1"]', base({ orderBy: [asc('__name__')], startAt: cur([S('i1')], true) }));
  await rq('cursor.name_other_collection', `orderBy __name__, startAt [ref ${LIM}/a]`, base({ orderBy: [asc('__name__')], startAt: cur([R(`${LIM}/a`)], true) }));
  await rq('cursor.name_child_path', `orderBy __name__, startAt [ref ${Q}/i1/${QCG}/c1]`, base({ orderBy: [asc('__name__')], startAt: cur([R(`${Q}/i1/${QCG}/c1`)], true) }));
  await rq('cursor.name_missing_doc', 'orderBy __name__, startAt [ref c] (no such document)', base({ orderBy: [asc('__name__')], startAt: cur([R(`${Q}/c`)], true) }));
  await rq('cursor.name_relative', 'orderBy __name__, startAt [referenceValue "aruma-ref-f3-q/i1"] (not a full name)', base({ orderBy: [asc('__name__')], startAt: cur([{ referenceValue: `${Q}/i1` }], true) }));
  await rq('cursor.inequality_implicit', 'k > 3 (implicit orderBy k), startAt [6]', base({ where: ff('k', 'GREATER_THAN', I(3)), startAt: cur([I(6)], true) }));
  await rq('cursor.inequality_implicit_two', 'k > 3, startAt [6, ref i1] before:false', base({ where: ff('k', 'GREATER_THAN', I(3)), startAt: cur([I(6), R(`${Q}/i1`)], false) }));
  await rq('cursor.desc', 'orderBy k desc, startAt [8]', base({ orderBy: [desc('k')], startAt: cur([I(8)], true) }));
  await rq('cursor.desc_end', 'orderBy k desc, endBefore [3]', base({ orderBy: [desc('k')], endAt: cur([I(3)], true) }));
  await rq('cursor.v_mixed', 'orderBy v, startAt ["a"]', base({ orderBy: [asc('v')], startAt: cur([S('a')], true) }));
  await rq('cursor.v_nan', 'orderBy v, startAfter [NaN]', base({ orderBy: [asc('v')], startAt: cur([NAN], false) }));
  await rq('cursor.limit', 'orderBy k, startAfter [5], limit 2', base({ ...ok, startAt: cur([I(5)], false), limit: 2 }));

  // offset and limit
  await rq('offset.3_limit_2', 'orderBy k, offset 3, limit 2', base({ ...ok, offset: 3, limit: 2 }));
  await rq('offset.past_end', 'offset 100 (more than the results)', base({ offset: 100 }));
  await rq('offset.equal_count', 'orderBy k, offset 17 (k matches 17 documents)', base({ ...ok, offset: 17 }));
  await rq('offset.one_left', 'orderBy k, offset 16', base({ ...ok, offset: 16 }));
  await rq('offset.zero', 'offset 0, limit 1', base({ offset: 0, limit: 1 }));
  await rq('offset.with_cursor', 'orderBy k, startAt [5], offset 2, limit 2', base({ ...ok, startAt: cur([I(5)], true), offset: 2, limit: 2 }));
  await rq('offset.with_filter_past_end', 'g == "a", offset 10', base({ where: ff('g', 'EQUAL', S('a')), offset: 10 }));
  await rq('limit.0', 'limit 0', base({ limit: 0 }));
  await rq('limit.0_offset', 'limit 0, offset 2', base({ limit: 0, offset: 2 }));
  await rq('limit.1', 'limit 1', base({ limit: 1 }));
  await rq('limit.exact', 'k == 1, limit 1 (exactly one match)', base({ where: ff('k', 'EQUAL', I(1)), limit: 1 }));
  await rq('limit.wrapper_object', 'limit {value: 2} (the Int32Value as an object)', base({ limit: { value: 2 } }));

  // projection
  await rq('select.k', 'select [k]', base({ select: { fields: [fp('k')] }, orderBy: [asc('k')], limit: 3 }));
  await rq('select.empty_fields', 'select {fields: []}', base({ select: { fields: [] }, limit: 3 }));
  await rq('select.empty_object', 'select {}', base({ select: {}, limit: 3 }));
  await rq('select.name', 'select [__name__]', base({ select: { fields: [fp('__name__')] }, limit: 3 }));
  await rq('select.name_and_k', 'select [__name__, k]', base({ select: { fields: [fp('__name__'), fp('k')] }, limit: 3 }));
  await rq('select.missing_field', 'select [zz] (no document has zz)', base({ select: { fields: [fp('zz')] }, limit: 3 }));
  await rq('select.map_subfield', 'select [v.a] on map', base({ where: ff('k', 'EQUAL', I(16)), select: { fields: [fp('v.a')] } }));
  await rq('select.duplicate', 'select [k, k]', base({ select: { fields: [fp('k'), fp('k')] }, limit: 2 }));
  await rq('select.parent_and_child', 'select [v, v.a]', base({ select: { fields: [fp('v'), fp('v.a')] }, limit: 2 }));
  await rq('select.invalid_path', 'select ["a..b"]', base({ select: { fields: [fp('a..b')] }, limit: 2 }));
  await rq('select.empty_path', 'select [""]', base({ select: { fields: [fp('')] }, limit: 2 }));
  await rq('select.order_field_not_selected', 'orderBy k, select [g]', base({ orderBy: [asc('k')], select: { fields: [fp('g')] }, limit: 3 }));

  // empty results
  await rq('empty.no_match', 'k == 999: no document', base({ where: ff('k', 'EQUAL', I(999)) }));
  await rq('empty.never_written', 'a collection never written', { from: from(`${Q}-never`) });
  await rq('empty.offset_and_limit', 'k == 999, offset 3, limit 2', base({ where: ff('k', 'EQUAL', I(999)), offset: 3, limit: 2 }));

  // collection group and descendants
  await rq('cg.root', `collection group ${QCG} at the root`, { from: from(QCG, true) });
  await rq('cg.root_name_asc', `collection group ${QCG}, orderBy __name__`, { from: from(QCG, true), orderBy: [asc('__name__')] });
  await rq('cg.under_document', `collection group ${QCG} under ${Q}/i1`, { from: from(QCG, true) }, { parent: `${ROOT}/${Q}/i1` });
  await rq('cg.under_missing_document', `collection group ${QCG} under ${Q}/ghost (a missing document)`, { from: from(QCG, true) }, { parent: `${ROOT}/${Q}/ghost` });
  await rq('cg.under_collection', `collection group ${QCG} with parent ${Q} (a collection)`, { from: from(QCG, true) }, { parent: `${ROOT}/${Q}` });
  await rq('cg.subcollection_plain', `collection ${QCG} under ${Q}/i1 (allDescendants false)`, { from: from(QCG, false) }, { parent: `${ROOT}/${Q}/i1` });
  await rq('cg.root_limit_cursor', `collection group ${QCG}, startAfter [ref ${Q}/i1/${QCG}/c1]`, { from: from(QCG, true), orderBy: [asc('__name__')], startAt: cur([R(`${Q}/i1/${QCG}/c1`)], false) });
  await rq('cg.root_name_range', `collection group ${QCG}, __name__ >= ref ${QR}`, { from: from(QCG, true), where: ff('__name__', 'GREATER_THAN_OR_EQUAL', R(`${QR}/x/${QCG}/c2`)) });
  await rq('cg.count_like_select', `collection group ${QCG}, select [__name__]`, { from: from(QCG, true), select: { fields: [fp('__name__')] } });
  await rq('cg.two_from', `from [${Q}, ${QR}]`, { from: [{ collectionId: Q }, { collectionId: QR }] });
  const lo = R(`${Q}/${MIN_ID}`);
  const hi = R(`${Q}/zzzz`);
  await rq('kindless.root_name_range', `kindless all-descendants at the root, __name__ in [${Q}/${MIN_ID}, ${Q}/zzzz), select __name__, orderBy __name__`, { from: [{ allDescendants: true }], where: and(ff('__name__', 'GREATER_THAN_OR_EQUAL', lo), ff('__name__', 'LESS_THAN', hi)), select: { fields: [fp('__name__')] }, orderBy: [asc('__name__')] });
  await rq('kindless.root_name_range_limit', 'the same, limit 3', { from: [{ allDescendants: true }], where: and(ff('__name__', 'GREATER_THAN_OR_EQUAL', lo), ff('__name__', 'LESS_THAN', hi)), select: { fields: [fp('__name__')] }, orderBy: [asc('__name__')], limit: 3 });
  await rq('kindless.root_name_range_no_order', 'the same with no orderBy and no select', { from: [{ allDescendants: true }], where: and(ff('__name__', 'GREATER_THAN_OR_EQUAL', lo), ff('__name__', 'LESS_THAN', hi)) });
  await rq('kindless.root_name_range_desc', 'the same, orderBy __name__ desc', { from: [{ allDescendants: true }], where: and(ff('__name__', 'GREATER_THAN_OR_EQUAL', lo), ff('__name__', 'LESS_THAN', hi)), orderBy: [desc('__name__')] });
  await rq('kindless.root_name_eq', `kindless at the root, __name__ == ref ${Q}/i1`, { from: [{ allDescendants: true }], where: ff('__name__', 'EQUAL', R(`${Q}/i1`)) });
  await rq('kindless.root_name_in', `kindless at the root, __name__ in [ref ${Q}/i1, ref ${QR}/x/${QCG}/c2]`, { from: [{ allDescendants: true }], where: ff('__name__', 'IN', A(R(`${Q}/i1`), R(`${QR}/x/${QCG}/c2`))) });
  await rq('kindless.under_document', `kindless all-descendants under ${Q}/i1`, { from: [{ allDescendants: true }] }, { parent: `${ROOT}/${Q}/i1` });
  await rq('kindless.under_document_name_range', `kindless under ${Q}/i1, __name__ > ref ${Q}/i1/${QCG}/c1`, { from: [{ allDescendants: true }], where: ff('__name__', 'GREATER_THAN', R(`${Q}/i1/${QCG}/c1`)) }, { parent: `${ROOT}/${Q}/i1` });
  await rq('kindless.under_document_cursor', `kindless under ${Q}/i1, orderBy __name__, startAfter [ref …/c1]`, { from: [{ allDescendants: true }], orderBy: [asc('__name__')], startAt: cur([R(`${Q}/i1/${QCG}/c1`)], false) }, { parent: `${ROOT}/${Q}/i1` });
  await rq('kindless.direct_children', `kindless direct children (allDescendants false) under ${Q}/ghost`, { from: [{ allDescendants: false }] }, { parent: `${ROOT}/${Q}/ghost` });

  // transactions and readTime
  await rq('txn.new_read_only', 'newTransaction readOnly', base({ where: ff('k', 'LESS_THAN', I(3)) }), { extra: { newTransaction: { readOnly: {} } } });
  await rq('txn.new_read_only_empty', 'newTransaction readOnly, no result', base({ where: ff('k', 'EQUAL', I(999)) }), { extra: { newTransaction: { readOnly: {} } } });
  {
    const { c, raw } = await rq('txn.new_read_write', 'newTransaction readWrite (rolled back after)', base({ where: ff('k', 'LESS_THAN', I(3)) }), { extra: { newTransaction: { readWrite: {} } }, replay: false });
    const t = Array.isArray(raw.body) ? raw.body.find((x) => x.transaction)?.transaction : undefined;
    if (t) {
      openTxns.add(t);
      const rb = await rest('POST', `${ROOT}:rollback`, { transaction: t });
      if (rb.http === 200) openTxns.delete(t);
      c.rolledBack = rb.http;
    }
  }
  {
    const b = await rest('POST', `${ROOT}:beginTransaction`, { options: { readOnly: {} } });
    const t = b.body.transaction;
    const replace = new Map([[t, '<read-only transaction from beginTransaction>']]);
    await rq('txn.existing_read_only', 'transaction: a read-only transaction from beginTransaction', base({ where: ff('k', 'LESS_THAN', I(3)) }), { extra: { transaction: t }, replay: false, replace });
    await rq('txn.transaction_and_read_time', 'transaction and readTime together', base({ limit: 1 }), { extra: { transaction: t, readTime: setupInfo.commitTime }, replay: false, replace });
    await rq('txn.transaction_and_new', 'transaction and newTransaction together', base({ limit: 1 }), { extra: { transaction: t, newTransaction: { readOnly: {} } }, replay: false, replace });
  }
  await rq('txn.read_time_setup', 'readTime = the setup commit time', base({ where: ff('k', 'LESS_THAN', I(3)) }), { extra: { readTime: setupInfo.commitTime }, replay: false });
  await rq('txn.read_time_before_setup', 'readTime = 1 µs before the setup commit time (the collection is empty then)', base({ where: ff('k', 'LESS_THAN', I(3)) }), { extra: { readTime: microsBefore(setupInfo.commitTime) }, replay: false });
  await rq('txn.read_time_two_hours_ago', 'readTime two hours ago, whole minute', base({ limit: 1 }), { extra: { readTime: new Date(Math.floor((Date.now() - 120 * 60_000) / 60_000) * 60_000).toISOString() }, replay: false });
  await rq('txn.read_time_future', 'readTime an hour ahead', base({ limit: 1 }), { extra: { readTime: new Date(Math.floor((Date.now() + 60 * 60_000) / 1000) * 1000).toISOString() }, replay: false });
  await rq('txn.read_time_nanos', 'readTime with nanoseconds', base({ limit: 1 }), { extra: { readTime: setupInfo.commitTime.replace(/(\.\d+)?Z$/, '.123456789Z') }, replay: false });

  // gRPC: stream element shapes
  if (g) {
    const gc = async (id, d, sq, extra = {}, parent = ROOT, replace) => {
      const { c } = await grpcCase(id, d, 'runQuery', { parent, structuredQuery: gq({ structuredQuery: sq }).structuredQuery, ...extra }, { stream: true, replace });
      out.push(c);
      return c;
    };
    await gc('grpc.order_k_limit_3', 'orderBy k, limit 3', base({ orderBy: [asc('k')], limit: 3 }));
    await gc('grpc.no_limit', 'k < 4 (three results, no limit)', base({ where: ff('k', 'LESS_THAN', I(4)) }));
    await gc('grpc.empty', 'k == 999: no result', base({ where: ff('k', 'EQUAL', I(999)) }));
    await gc('grpc.offset_past_end', 'offset 100', base({ offset: 100 }));
    await gc('grpc.offset_3_limit_2', 'orderBy k, offset 3, limit 2', base({ orderBy: [asc('k')], offset: 3, limit: 2 }));
    await gc('grpc.limit_0', 'limit 0', base({ limit: 0 }));
    await gc('grpc.limit_exact', 'k == 1, limit 1', base({ where: ff('k', 'EQUAL', I(1)), limit: 1 }));
    await gc('grpc.new_read_only', 'newTransaction readOnly', base({ where: ff('k', 'LESS_THAN', I(3)) }), { newTransaction: { readOnly: {} } });
    await gc('grpc.new_read_only_empty', 'newTransaction readOnly, no result', base({ where: ff('k', 'EQUAL', I(999)) }), { newTransaction: { readOnly: {} } });
    await gc('grpc.v_eq_nan', 'v == NaN', base({ where: ff('v', 'EQUAL', NAN) }));
    await gc('grpc.cursor_too_many_values', 'orderBy k, startAt [5, ref, 3]', base({ orderBy: [asc('k')], startAt: cur([I(5), R(`${Q}/i1`), I(3)], true) }));
    await gc('grpc.missing_index', 'g == "a", orderBy k: needs a composite index', base({ where: ff('g', 'EQUAL', S('a')), orderBy: [asc('k')] }));
    await gc('grpc.missing_index_two_ranges', 'k > 1 and v > 1: needs a composite index (template 2)', base({ where: and(ff('k', 'GREATER_THAN', I(1)), ff('v', 'GREATER_THAN', I(1))) }));
    await gc('grpc.cg_exemption', `collection group ${QCG}, w == 1: needs a single-field collection-group index`, { from: from(QCG, true), where: ff('w', 'EQUAL', I(1)) });
  }
  {
    // the same missing-index queries over REST, for the gRPC comparison (never replayed)
    await rq('index.missing_composite', 'g == "a", orderBy k (REST twin of grpc.missing_index)', base({ where: ff('g', 'EQUAL', S('a')), orderBy: [asc('k')] }), { replay: false });
    await rq('index.missing_two_ranges', 'k > 1 and v > 1 (REST twin)', base({ where: and(ff('k', 'GREATER_THAN', I(1)), ff('v', 'GREATER_THAN', I(1))) }), { replay: false });
    await rq('index.cg_exemption', `collection group ${QCG}, w == 1 (REST twin)`, { from: from(QCG, true), where: ff('w', 'EQUAL', I(1)) }, { replay: false });
  }
  return {
    cases: out,
    notes: [
      `The fixture (setup) is one collection ${Q} with a field v of every type (null, false, true, NaN, -Infinity, 1, 1.5, 2, 2.0, a timestamp, "a", "b", bytes, a geopoint, an array, a map), k = 1..17 (nok has none), g in a/b/c, arr arrays; none has no v. Subcollections ${QCG} under ${Q}/i1, ${QR}/x and the missing document ${Q}/ghost, and ${Q}/i1/${QCG}/c1/deep/y.`,
      'documents lists the names returned, in order (rel. to the documents root); elementKeys the keys of each stream element as received.',
      'Cursors: startAt with before:true is startAt, before:false startAfter; endAt with before:false is endAt, before:true endBefore.',
    ],
  };
}

function microsBefore(rfc) {
  const m = /^(.*?)(?:\.(\d+))?Z$/.exec(rfc);
  const us = BigInt(Date.parse(`${m[1]}Z`)) * 1000n + BigInt((m[2] || '').padEnd(6, '0').slice(0, 6)) - 1n;
  const sec = us / 1_000_000n;
  const frac = String(us % 1_000_000n).padStart(6, '0');
  return `${new Date(Number(sec) * 1000).toISOString().slice(0, 19)}.${frac}Z`;
}

// ---- limits and refusals ----

async function limits() {
  const out = [];
  const rq = async (id, d, sq, o = {}) => {
    const { c } = await restCase(id, d, 'POST', `${o.parent ?? ROOT}:runQuery`, sq === undefined ? o.body : { structuredQuery: sq, ...(o.extra || {}) }, { replay: o.replay ?? true });
    out.push(c);
    return c;
  };
  const base = (extra = {}) => ({ from: from(LIM), ...extra });
  const vals = (n, f = (i) => I(i)) => A(...Array.from({ length: n }, (_, i) => f(i)));
  const eqs = (n) => Array.from({ length: n }, (_, i) => ff(`f${i}`, 'EQUAL', I(i)));
  const ranges = (n) => Array.from({ length: n }, (_, i) => ff(`f${i}`, 'GREATER_THAN', I(i)));

  // disjunction and list sizes (f0-errors.json has the first recording of in 30/31, not-in 10/11, 31 ORs, 6x6)
  await rq('in.30', 'a in [30 values]', base({ where: ff('a', 'IN', vals(30)) }));
  await rq('in.31', 'a in [31 values]', base({ where: ff('a', 'IN', vals(31)) }));
  await rq('not_in.10', 'a not-in [10 values]', base({ where: ff('a', 'NOT_IN', vals(10)) }));
  await rq('not_in.11', 'a not-in [11 values]', base({ where: ff('a', 'NOT_IN', vals(11)) }));
  await rq('array_contains_any.30', 'arr array-contains-any [30 values]', base({ where: ff('arr', 'ARRAY_CONTAINS_ANY', vals(30)) }));
  await rq('array_contains_any.31', 'arr array-contains-any [31 values]', base({ where: ff('arr', 'ARRAY_CONTAINS_ANY', vals(31)) }));
  await rq('dnf.or_31', '31 ORs of a == i', base({ where: or(...Array.from({ length: 31 }, (_, i) => ff('a', 'EQUAL', I(i)))) }));
  await rq('dnf.in_5_x_in_6', 'a in [5] AND b in [6]: 30 disjunctions', base({ where: and(ff('a', 'IN', vals(5)), ff('b', 'IN', vals(6))) }));
  await rq('dnf.in_5_x_in_7', 'a in [5] AND b in [7]: 35 disjunctions', base({ where: and(ff('a', 'IN', vals(5)), ff('b', 'IN', vals(7))) }));
  await rq('dnf.or_of_in', 'a in [20] OR b in [11]: 31 disjunctions', base({ where: or(ff('a', 'IN', vals(20)), ff('b', 'IN', vals(11))) }));
  await rq('dnf.aca_x_in', 'arr array-contains-any [6] AND a in [6]: 36 disjunctions', base({ where: and(ff('arr', 'ARRAY_CONTAINS_ANY', vals(6)), ff('a', 'IN', vals(6))) }));
  await rq('dnf.not_in_10_with_in', 'a not-in [10] AND b in [30]', base({ where: and(ff('a', 'NOT_IN', vals(10)), ff('b', 'IN', vals(30))) }));
  await rq('in.empty', 'a in []', base({ where: ff('a', 'IN', A()) }));
  await rq('not_in.empty', 'a not-in []', base({ where: ff('a', 'NOT_IN', A()) }));
  await rq('array_contains_any.empty', 'arr array-contains-any []', base({ where: ff('arr', 'ARRAY_CONTAINS_ANY', A()) }));
  await rq('in.not_array', 'a in "x" (not an array)', base({ where: ff('a', 'IN', S('x')) }));
  await rq('not_in.not_array', 'a not-in 1', base({ where: ff('a', 'NOT_IN', I(1)) }));
  await rq('array_contains_any.not_array', 'arr array-contains-any 1', base({ where: ff('arr', 'ARRAY_CONTAINS_ANY', I(1)) }));
  await rq('in.nested_array', 'a in [[1], [2]]', base({ where: ff('a', 'IN', A(A(I(1)), A(I(2)))) }));
  await rq('array_contains.array_operand', 'arr array-contains [1]', base({ where: ff('arr', 'ARRAY_CONTAINS', A(I(1))) }));

  // operator combinations
  await rq('combo.two_not_in', 'a not-in [1] AND b not-in [2]', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('b', 'NOT_IN', A(I(2)))) }));
  await rq('combo.two_not_in_same_field', 'a not-in [1] AND a not-in [2]', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('a', 'NOT_IN', A(I(2)))) }));
  await rq('combo.not_in_and_neq', 'a not-in [1] AND b != 2', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('b', 'NOT_EQUAL', I(2))) }));
  await rq('combo.not_in_and_neq_same_field', 'a not-in [1] AND a != 2', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('a', 'NOT_EQUAL', I(2))) }));
  await rq('combo.two_neq', 'a != 1 AND b != 2', base({ where: and(ff('a', 'NOT_EQUAL', I(1)), ff('b', 'NOT_EQUAL', I(2))) }));
  await rq('combo.two_neq_same_field', 'a != 1 AND a != 2', base({ where: and(ff('a', 'NOT_EQUAL', I(1)), ff('a', 'NOT_EQUAL', I(2))) }));
  await rq('combo.not_in_or_neq', 'a not-in [1] OR b != 2', base({ where: or(ff('a', 'NOT_IN', A(I(1))), ff('b', 'NOT_EQUAL', I(2))) }));
  await rq('combo.not_in_and_in', 'a not-in [1] AND b in [2]', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('b', 'IN', A(I(2)))) }));
  await rq('combo.not_in_and_in_same_field', 'a not-in [1] AND a in [2]', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('a', 'IN', A(I(2)))) }));
  await rq('combo.not_in_and_aca', 'a not-in [1] AND arr array-contains-any [2]', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('arr', 'ARRAY_CONTAINS_ANY', A(I(2)))) }));
  await rq('combo.two_array_contains', 'arr array-contains 1 AND arr2 array-contains 2', base({ where: and(ff('arr', 'ARRAY_CONTAINS', I(1)), ff('arr2', 'ARRAY_CONTAINS', I(2))) }));
  await rq('combo.two_array_contains_same_field', 'arr array-contains 1 AND arr array-contains 2', base({ where: and(ff('arr', 'ARRAY_CONTAINS', I(1)), ff('arr', 'ARRAY_CONTAINS', I(2))) }));
  await rq('combo.array_contains_and_aca', 'arr array-contains 1 AND arr2 array-contains-any [2]', base({ where: and(ff('arr', 'ARRAY_CONTAINS', I(1)), ff('arr2', 'ARRAY_CONTAINS_ANY', A(I(2)))) }));
  await rq('combo.two_aca', 'arr array-contains-any [1] AND arr2 array-contains-any [2]', base({ where: and(ff('arr', 'ARRAY_CONTAINS_ANY', A(I(1))), ff('arr2', 'ARRAY_CONTAINS_ANY', A(I(2)))) }));
  await rq('combo.aca_and_in', 'arr array-contains-any [1] AND a in [2]', base({ where: and(ff('arr', 'ARRAY_CONTAINS_ANY', A(I(1))), ff('a', 'IN', A(I(2)))) }));
  await rq('combo.aca_or_aca', 'arr array-contains-any [1] OR arr2 array-contains-any [2]', base({ where: or(ff('arr', 'ARRAY_CONTAINS_ANY', A(I(1))), ff('arr2', 'ARRAY_CONTAINS_ANY', A(I(2)))) }));
  await rq('combo.two_in', 'a in [1] AND b in [2]', base({ where: and(ff('a', 'IN', A(I(1))), ff('b', 'IN', A(I(2)))) }));
  await rq('combo.eq_and_neq_same_field', 'a == 1 AND a != 2', base({ where: and(ff('a', 'EQUAL', I(1)), ff('a', 'NOT_EQUAL', I(2))) }));
  await rq('combo.is_nan_and_not_nan', 'a IS_NAN AND b IS_NOT_NAN', base({ where: and(uf('a', 'IS_NAN'), uf('b', 'IS_NOT_NAN')) }));
  await rq('combo.is_not_null_two', 'a IS_NOT_NULL AND b IS_NOT_NULL', base({ where: and(uf('a', 'IS_NOT_NULL'), uf('b', 'IS_NOT_NULL')) }));

  // inequality fields and filter counts
  await rq('ineq.10_fields', 'f0 > 0 … f9 > 9: ten inequality fields', base({ where: and(...ranges(10)) }));
  await rq('ineq.11_fields', 'eleven inequality fields', base({ where: and(...ranges(11)) }));
  await rq('ineq.11_fields_neq', 'eleven != filters on distinct fields', base({ where: and(...Array.from({ length: 11 }, (_, i) => ff(`f${i}`, 'NOT_EQUAL', I(i)))) }));
  await rq('ineq.mismatch_order', 'a > 1, orderBy b (first order not the inequality field)', base({ where: ff('a', 'GREATER_THAN', I(1)), orderBy: [asc('b')] }));
  await rq('filters.eq_99', '99 equality filters on distinct fields', base({ where: and(...eqs(99)) }));
  await rq('filters.eq_100', '100 equality filters on distinct fields', base({ where: and(...eqs(100)) }));
  await rq('filters.eq_101', '101 equality filters on distinct fields', base({ where: and(...eqs(101)) }));
  await rq('filters.eq_100_subcollection', `100 equality filters, on ${LIM}/a/sub (parent depth 1)`, { from: from('sub'), where: and(...eqs(100)) }, { parent: `${ROOT}/${LIM}/a` });
  await rq('filters.eq_99_subcollection', `99 equality filters, on ${LIM}/a/sub`, { from: from('sub'), where: and(...eqs(99)) }, { parent: `${ROOT}/${LIM}/a` });
  await rq('filters.eq_60_orders_41', '60 equality filters and orderBy on 41 fields', base({ where: and(...eqs(60)), orderBy: Array.from({ length: 41 }, (_, i) => asc(`o${i}`)) }));
  await rq('filters.orders_101', 'orderBy on 101 fields', base({ orderBy: Array.from({ length: 101 }, (_, i) => asc(`o${i}`)) }));
  await rq('filters.in_30_and_eq_69', 'a in [30 values] AND 69 equality filters: 70 filters, 30 disjunctions', base({ where: and(ff('a', 'IN', vals(30)), ...eqs(69)) }));

  // order structure
  await rq('order.name_then_field', 'orderBy __name__, then a', base({ orderBy: [asc('__name__'), asc('a')] }));
  await rq('order.name_twice', 'orderBy __name__ twice', base({ orderBy: [asc('__name__'), asc('__name__')] }));
  await rq('order.direction_unspecified', 'orderBy a with direction DIRECTION_UNSPECIFIED', base({ orderBy: [{ field: fp('a'), direction: 'DIRECTION_UNSPECIFIED' }] }));
  await rq('order.no_direction', 'orderBy a with no direction', base({ orderBy: [{ field: fp('a') }] }));
  await rq('order.no_field', 'orderBy with no field', base({ orderBy: [{ direction: 'ASCENDING' }] }));
  await rq('order.empty_path', 'orderBy ""', base({ orderBy: [asc('')] }));

  // cursors
  await rq('cursor.too_many', 'orderBy a, startAt [1, 2]: two values but the order is a, __name__', base({ orderBy: [asc('a')], startAt: { values: [I(1), I(2)], before: true } }));
  await rq('cursor.too_many_three', 'orderBy a, startAt [1, ref, 3]', base({ orderBy: [asc('a')], startAt: { values: [I(1), R(`${LIM}/a`), I(3)], before: true } }));
  await rq('cursor.end_too_many', 'orderBy a, endAt [1, ref, 3]', base({ orderBy: [asc('a')], endAt: { values: [I(1), R(`${LIM}/a`), I(3)], before: false } }));
  await rq('cursor.no_order_two_values', 'no orderBy, startAt [ref, 1]', base({ startAt: { values: [R(`${LIM}/a`), I(1)], before: true } }));
  await rq('cursor.name_string', 'no orderBy, startAt ["a"] (a string for __name__)', base({ startAt: { values: [S('a')], before: true } }));
  await rq('cursor.name_integer', 'orderBy a, startAt [1, 2] where 2 stands for __name__', base({ orderBy: [asc('a')], startAt: { values: [I(1), I(2)], before: true } }), { replay: false });
  await rq('cursor.name_other_collection', `no orderBy, startAt [ref ${Q}/i1]`, base({ startAt: { values: [R(`${Q}/i1`)], before: true } }));
  await rq('cursor.name_other_project', 'no orderBy, startAt [ref projects/other/…]', base({ startAt: { values: [{ referenceValue: 'projects/aruma-other/databases/(default)/documents/x/y' }], before: true } }));
  await rq('cursor.name_collection_path', `no orderBy, startAt [ref ${LIM}] (a collection path)`, base({ startAt: { values: [{ referenceValue: `${ROOT}/${LIM}` }], before: true } }));
  await rq('cursor.name_invalid_reference', 'no orderBy, startAt [referenceValue "not a name"]', base({ startAt: { values: [{ referenceValue: 'not a name' }], before: true } }));
  await rq('cursor.cg_name_other_group', `collection group ${LIM}, startAt [ref ${Q}/i1]`, { from: from(LIM, true), startAt: { values: [R(`${Q}/i1`)], before: true } });

  // limit and offset
  await rq('limit.negative', 'limit -1', base({ limit: -1 }));
  await rq('limit.int32_max', 'limit 2147483647', base({ limit: 2147483647 }));
  await rq('limit.over_int32', 'limit 2147483648', base({ limit: 2147483648 }));
  await rq('limit.string', 'limit "2"', base({ limit: '2' }));
  await rq('offset.negative', 'offset -1', base({ offset: -1 }));
  await rq('offset.int32_max', 'offset 2147483647', base({ offset: 2147483647 }));
  await rq('offset.over_int32', 'offset 2147483648', base({ offset: 2147483648 }));
  await rq('offset.huge_limit', 'offset 1000000, limit 1', base({ offset: 1000000, limit: 1 }));

  // field paths
  for (const [id, p] of [['empty', ''], ['double_dot', 'a..b'], ['leading_dot', '.a'], ['trailing_dot', 'a.'], ['unclosed_backtick', '`a'], ['backticked', '`a b`'], ['space', 'a b'], ['reserved', '__x__'], ['reserved_nested', 'a.__x__'], ['name_nested', '__name__.x'], ['name_under_map', 'a.__name__'], ['dash', 'a-b'], ['digit_start', '1a'], ['star', '*'], ['long_1500', 'a'.repeat(1500)], ['long_1501', 'a'.repeat(1501)]]) {
    await rq(`path.${id}`, `where ${JSON.stringify(p.length > 40 ? `<${p.length} × a>` : p)} == 1`, base({ where: ff(p, 'EQUAL', I(1)) }));
  }
  await rq('path.order_invalid', 'orderBy "a..b"', base({ orderBy: [asc('a..b')] }));
  await rq('path.unary_invalid', '"a..b" IS_NULL', base({ where: uf('a..b', 'IS_NULL') }));

  // __name__ filters with the wrong type
  await rq('name.eq_string', '__name__ == "a"', base({ where: ff('__name__', 'EQUAL', S('a')) }));
  await rq('name.eq_integer', '__name__ == 1', base({ where: ff('__name__', 'EQUAL', I(1)) }));
  await rq('name.eq_null', '__name__ == null', base({ where: ff('__name__', 'EQUAL', NULL) }));
  await rq('name.gt_string', '__name__ > "a"', base({ where: ff('__name__', 'GREATER_THAN', S('a')) }));
  await rq('name.in_strings', '__name__ in ["a"]', base({ where: ff('__name__', 'IN', A(S('a'))) }));
  await rq('name.not_in_strings', '__name__ not-in ["a"]', base({ where: ff('__name__', 'NOT_IN', A(S('a'))) }));
  await rq('name.neq_string', '__name__ != "a"', base({ where: ff('__name__', 'NOT_EQUAL', S('a')) }));
  await rq('name.eq_other_collection', `__name__ == ref ${Q}/i1`, base({ where: ff('__name__', 'EQUAL', R(`${Q}/i1`)) }));
  await rq('name.gt_other_collection', `__name__ > ref ${Q}/i1`, base({ where: ff('__name__', 'GREATER_THAN', R(`${Q}/i1`)) }));
  await rq('name.eq_child_doc', `__name__ == ref ${LIM}/a/sub/b (a document of a subcollection)`, base({ where: ff('__name__', 'EQUAL', R(`${LIM}/a/sub/b`)) }));
  await rq('name.eq_collection_path', `__name__ == ref ${LIM} (a collection path)`, base({ where: ff('__name__', 'EQUAL', { referenceValue: `${ROOT}/${LIM}` }) }));
  await rq('name.eq_other_project', '__name__ == ref projects/aruma-other/…', base({ where: ff('__name__', 'EQUAL', { referenceValue: `projects/aruma-other/databases/(default)/documents/${LIM}/a` }) }));
  await rq('name.eq_relative', `__name__ == referenceValue "${LIM}/a"`, base({ where: ff('__name__', 'EQUAL', { referenceValue: `${LIM}/a` }) }));
  await rq('name.array_contains', '__name__ array-contains ref', base({ where: ff('__name__', 'ARRAY_CONTAINS', R(`${LIM}/a`)) }));
  await rq('name.is_null', '__name__ IS_NULL', base({ where: uf('__name__', 'IS_NULL') }));
  await rq('name.is_nan', '__name__ IS_NAN', base({ where: uf('__name__', 'IS_NAN') }));
  await rq('name.eq_ok', `__name__ == ref ${LIM}/a (control)`, base({ where: ff('__name__', 'EQUAL', R(`${LIM}/a`)) }));
  await rq('name.in_mixed', `__name__ in [ref ${LIM}/a, "b"]`, base({ where: ff('__name__', 'IN', A(R(`${LIM}/a`), S('b'))) }));

  // filter structure
  await rq('where.empty', 'where {}', base({ where: {} }));
  await rq('where.composite_empty', 'AND of no filter', base({ where: { compositeFilter: { op: 'AND', filters: [] } } }));
  await rq('where.or_empty', 'OR of no filter', base({ where: { compositeFilter: { op: 'OR', filters: [] } } }));
  await rq('where.composite_op_unspecified', 'composite op OPERATOR_UNSPECIFIED', base({ where: { compositeFilter: { op: 'OPERATOR_UNSPECIFIED', filters: [ff('a', 'EQUAL', I(1))] } } }));
  await rq('where.composite_no_op', 'composite with no op', base({ where: { compositeFilter: { filters: [ff('a', 'EQUAL', I(1))] } } }));
  await rq('where.field_op_unspecified', 'field filter op OPERATOR_UNSPECIFIED', base({ where: ff('a', 'OPERATOR_UNSPECIFIED', I(1)) }));
  await rq('where.field_no_value', 'field filter with no value', base({ where: { fieldFilter: { field: fp('a'), op: 'EQUAL' } } }));
  await rq('where.field_no_field', 'field filter with no field', base({ where: { fieldFilter: { op: 'EQUAL', value: I(1) } } }));
  await rq('where.unary_op_unspecified', 'unary op OPERATOR_UNSPECIFIED', base({ where: uf('a', 'OPERATOR_UNSPECIFIED') }));
  await rq('where.unary_no_field', 'unary filter with no field', base({ where: { unaryFilter: { op: 'IS_NULL' } } }));
  await rq('where.and_nested_empty', 'AND(a == 1, AND())', base({ where: and(ff('a', 'EQUAL', I(1)), { compositeFilter: { op: 'AND', filters: [] } }) }));
  await rq('where.unary_ops_on_eq', 'field filter with op IS_NAN', base({ where: ff('a', 'IS_NAN', I(1)) }));
  await rq('where.value_unset', 'field filter value {}', base({ where: ff('a', 'EQUAL', {}) }));

  // from
  await rq('from.empty', `from [] under ${LIM}/a (every direct child collection)`, { from: [] }, { parent: `${ROOT}/${LIM}/a` });
  await rq('from.none', 'no from', { where: ff('a', 'EQUAL', I(1)) });
  await rq('from.two', `from [${LIM}, ${LIM}-2]`, { from: [{ collectionId: LIM }, { collectionId: `${LIM}-2` }] });
  await rq('from.slash', 'from collectionId "a/b"', { from: from(`${LIM}/b`) });
  await rq('from.reserved', 'from collectionId "__x__"', { from: from('__x__') });
  await rq('from.empty_id', `from collectionId "" under ${LIM}/a`, { from: from('') }, { parent: `${ROOT}/${LIM}/a` });
  await rq('from.parent_collection', `parent ${LIM} (a collection path)`, { from: from('sub') }, { parent: `${ROOT}/${LIM}` });
  await rq('request.no_query', 'runQuery with an empty body', undefined, { body: {} });

  if (g) {
    const gc = async (id, d, sq) => {
      const { c } = await grpcCase(id, d, 'runQuery', { parent: ROOT, structuredQuery: gq({ structuredQuery: sq }).structuredQuery }, { stream: true });
      out.push(c);
    };
    await gc('grpc.in_31', 'a in [31 values]', base({ where: ff('a', 'IN', vals(31)) }));
    await gc('grpc.ineq_11_fields', 'eleven inequality fields', base({ where: and(...ranges(11)) }));
    await gc('grpc.filters_eq_101', '101 equality filters', base({ where: and(...eqs(101)) }));
    await gc('grpc.cursor_too_many', 'orderBy a, startAt [1, 2]', base({ orderBy: [asc('a')], startAt: { values: [I(1), I(2)], before: true } }));
    await gc('grpc.limit_negative', 'limit -1', base({ limit: -1 }));
    await gc('grpc.name_eq_string', '__name__ == "a"', base({ where: ff('__name__', 'EQUAL', S('a')) }));
    await gc('grpc.two_not_in', 'a not-in [1] AND b not-in [2]', base({ where: and(ff('a', 'NOT_IN', A(I(1))), ff('b', 'NOT_IN', A(I(2)))) }));
    await gc('grpc.path_double_dot', '"a..b" == 1', base({ where: ff('a..b', 'EQUAL', I(1)) }));
  }
  return {
    cases: out,
    notes: [`The setup is ${LIM}/a {a: 1, arr: [1]} and ${LIM}/a/sub/b {a: 1}; the refusals do not depend on it.`],
  };
}

// ---- aggregations ----

const AGG_DOCS = {
  int: [{ x: I(1) }, { x: I(2) }, { x: I(3) }],
  ovf: [{ x: I(INT64_MAX) }, { x: I(1) }],
  'ovf-back': [{ x: I(INT64_MAX) }, { x: I(1) }, { x: I(-2) }],
  negovf: [{ x: I(INT64_MIN) }, { x: I(-1) }],
  mix: [{ x: I(1) }, { x: D(2.5) }, { x: S('s') }, { x: NULL }, { x: { booleanValue: true } }, { x: A(I(1)) }, { x: M({ a: I(1) }) }, { y: I(1) }, { x: { timestampValue: '2020-01-01T00:00:00Z' } }],
  nan: [{ x: I(1) }, { x: NAN }],
  inf: [{ x: D('Infinity') }, { x: D('-Infinity') }, { x: I(1) }],
  dbl: [{ x: D(0.1) }, { x: D(0.2) }, { x: D(0.3) }],
  big: [{ x: I('9007199254740993') }, { x: I(1) }],
};

async function aggregations() {
  const out = [];
  const ra = async (id, d, aggs, sq, o = {}) => {
    const body = o.body ?? { structuredAggregationQuery: { structuredQuery: sq, ...(aggs === undefined ? {} : { aggregations: aggs }) }, ...(o.extra || {}) };
    const { c, raw } = await restCase(id, d, 'POST', `${o.parent ?? ROOT}:runAggregationQuery`, body, { replay: o.replay ?? true, replace: o.replace });
    out.push(c);
    return { c, raw };
  };
  const col = (s) => ({ from: from(`${AG}-${s}`) });
  const cnt = (alias = 'n', upTo) => ({ alias, count: upTo === undefined ? {} : { upTo } });
  const sum = (f = 'x', alias = 's') => ({ alias, sum: { field: fp(f) } });
  const avg = (f = 'x', alias = 'a') => ({ alias, avg: { field: fp(f) } });
  const all3 = [cnt(), sum(), avg()];
  for (const s of AGG_SETS) await ra(`set.${s}`, `count, sum(x), avg(x) over ${AG}-${s}`, all3, col(s));
  await ra('set.empty', `count, sum(x), avg(x) over ${AGE} (never written)`, all3, { from: from(AGE) });
  await ra('set.empty_filter', 'count, sum(k), avg(k) over the query fixture with k == 999 (no match)', [cnt(), sum('k'), avg('k')], { from: from(Q), where: ff('k', 'EQUAL', I(999)) });
  await ra('sum.missing_field', 'sum(zz), avg(zz): no document has zz', [sum('zz'), avg('zz')], col('int'));
  await ra('sum.map_subfield', 'sum(x.a) over the mixed set (one map {a: 1})', [sum('x.a'), avg('x.a')], col('mix'));
  await ra('count.up_to_2', 'count upTo 2 over 3 documents', [cnt('n', 2)], col('int'));
  await ra('count.up_to_3', 'count upTo 3 over 3 documents', [cnt('n', 3)], col('int'));
  await ra('count.up_to_string', 'count upTo "2" (the Int64Value as a string)', [cnt('n', '2')], col('int'));
  await ra('count.up_to_0', 'count upTo 0', [cnt('n', 0)], col('int'));
  await ra('count.up_to_negative', 'count upTo -1', [cnt('n', -1)], col('int'));
  await ra('count.up_to_huge', 'count upTo 9223372036854775807', [cnt('n', INT64_MAX)], col('int'));
  await ra('count.up_to_object', 'count upTo {value: 2}', [cnt('n', { value: 2 })], col('int'));
  await ra('count.with_limit', 'count over a query with limit 2', [cnt()], { ...col('int'), limit: 2 });
  await ra('count.with_offset', 'count over a query with offset 1', [cnt()], { ...col('int'), offset: 1 });
  await ra('sum.with_limit', 'sum(x) over a query with limit 2, orderBy x', [sum()], { ...col('int'), orderBy: [asc('x')], limit: 2 });
  await ra('sum.with_filter_same_field', 'sum(x), x > 1', [sum(), avg()], { ...col('int'), where: ff('x', 'GREATER_THAN', I(1)) });
  await ra('count.with_cursor', 'count, orderBy x, startAfter [1]', [cnt()], { ...col('int'), orderBy: [asc('x')], startAt: { values: [I(1)], before: false } });
  await ra('count.with_select', 'count over a query with select [x]', [cnt()], { ...col('int'), select: { fields: [fp('x')] } });
  await ra('count.collection_group', `count over the collection group ${QCG}`, [cnt()], { from: from(QCG, true) }, { replay: false });
  await ra('count.kindless', `count over a kindless query under ${Q}/i1`, [cnt()], { from: [{ allDescendants: true }] }, { parent: `${ROOT}/${Q}/i1`, replay: false });
  await ra('multi.five', 'five aggregations', [cnt('a1'), cnt('a2', 1), sum('x', 'a3'), avg('x', 'a4'), sum('x', 'a5')], col('int'));
  await ra('multi.six', 'six aggregations', [cnt('a1'), cnt('a2', 1), sum('x', 'a3'), avg('x', 'a4'), sum('x', 'a5'), avg('x', 'a6')], col('int'));
  await ra('multi.duplicate_alias', 'count and sum both aliased "n"', [cnt('n'), sum('x', 'n')], col('int'));
  await ra('multi.same_aggregation_twice', 'count twice under two aliases', [cnt('n1'), cnt('n2')], col('int'));
  await ra('multi.none', 'aggregations []', [], col('int'));
  await ra('multi.absent', 'no aggregations field', undefined, col('int'));
  await ra('alias.absent', 'count with no alias', [{ count: {} }], col('int'));
  await ra('alias.absent_two', 'count and sum with no alias', [{ count: {} }, { sum: { field: fp('x') } }], col('int'));
  await ra('alias.empty', 'alias ""', [cnt('')], col('int'));
  await ra('alias.dot', 'alias "a.b"', [cnt('a.b')], col('int'));
  await ra('alias.space', 'alias "a b"', [cnt('a b')], col('int'));
  await ra('alias.reserved', 'alias "__x__"', [cnt('__x__')], col('int'));
  await ra('alias.backticked', 'alias "`a`"', [cnt('`a`')], col('int'));
  await ra('alias.long_1500', 'alias of 1500 bytes', [cnt('a'.repeat(1500))], col('int'));
  await ra('alias.long_1501', 'alias of 1501 bytes', [cnt('a'.repeat(1501))], col('int'));
  await ra('alias.field_1', 'alias "field_1"', [cnt('field_1')], col('int'));
  await ra('alias.absent_with_field_1', 'count aliased "field_1" and sum with no alias', [cnt('field_1'), { sum: { field: fp('x') } }], col('int'));
  await ra('operator.none', 'an aggregation with only an alias', [{ alias: 'n' }], col('int'));
  await ra('operator.two', 'an aggregation with count and sum', [{ alias: 'n', count: {}, sum: { field: fp('x') } }], col('int'));
  await ra('sum.no_field', 'sum {} (no field)', [{ alias: 's', sum: {} }], col('int'));
  await ra('sum.name', 'sum(__name__)', [sum('__name__')], col('int'));
  await ra('sum.invalid_path', 'sum("a..b")', [sum('a..b')], col('int'));
  await ra('sum.empty_path', 'sum("")', [sum('')], col('int'));
  await ra('avg.name', 'avg(__name__)', [avg('__name__')], col('int'));
  await ra('request.no_query', 'structuredAggregationQuery with aggregations but no structuredQuery', undefined, undefined, { body: { structuredAggregationQuery: { aggregations: [cnt()] } }, replay: false });
  await ra('request.empty', 'an empty body', undefined, undefined, { body: {} });
  await ra('index.sum_with_eq_other', 'sum(x) where y == 1: needs a composite index (R13)', [sum()], { ...col('mix'), where: ff('y', 'EQUAL', I(1)) });
  await ra('txn.new_read_only', 'count with newTransaction readOnly', [cnt()], col('int'), { extra: { newTransaction: { readOnly: {} } } });
  {
    const rt = new Date(Math.floor((Date.now() - 120 * 60_000) / 60_000) * 60_000).toISOString();
    await ra('txn.read_time_two_hours_ago', 'count with readTime two hours ago (before the setup)', [cnt()], col('int'), { extra: { readTime: rt }, replay: false });
  }
  if (g) {
    const gc = async (id, d, aggs, sq, extra = {}) => {
      const { c } = await grpcCase(id, d, 'runAggregationQuery', { parent: ROOT, structuredAggregationQuery: { structuredQuery: gq({ structuredQuery: sq }).structuredQuery, aggregations: gq(aggs) }, ...extra }, { stream: true });
      out.push(c);
    };
    await gc('grpc.set.int', 'count, sum(x), avg(x)', all3, col('int'));
    await gc('grpc.set.ovf', 'count, sum(x), avg(x) over the overflowing set', all3, col('ovf'));
    await gc('grpc.set.nan', 'count, sum(x), avg(x) over the NaN set', all3, col('nan'));
    await gc('grpc.set.empty', 'count, sum(x), avg(x) over a never-written collection', all3, { from: from(AGE) });
    await gc('grpc.multi.six', 'six aggregations', [cnt('a1'), cnt('a2', 1), sum('x', 'a3'), avg('x', 'a4'), sum('x', 'a5'), avg('x', 'a6')], col('int'));
    await gc('grpc.multi.duplicate_alias', 'duplicate alias', [cnt('n'), sum('x', 'n')], col('int'));
    await gc('grpc.index.sum_with_eq_other', 'sum(x) where y == 1: needs a composite index', [sum()], { ...col('mix'), where: ff('y', 'EQUAL', I(1)) });
    await gc('grpc.txn.new_read_only', 'count with newTransaction readOnly', [cnt()], col('int'), { newTransaction: { readOnly: {} } });
  }
  return {
    cases: out,
    notes: [
      `The sets are top-level collections ${AG}-<set> (setup): int {1, 2, 3}; ovf {int64 max, 1}; ovf-back {int64 max, 1, -2}; negovf {int64 min, -1}; mix {1, 2.5, "s", null, true, [1], {a: 1}, a timestamp, and a document without x}; nan {1, NaN}; inf {Infinity, -Infinity, 1}; dbl {0.1, 0.2, 0.3}; big {9007199254740993, 1}. ${AGE} is never written.`,
      'request.no_query (no structuredQuery) counts every document of the database, the project\'s own included: only that number is recorded, and it is not replayable.',
      'The collection-group and kindless counts read the run-query fixture (f3-run-query.json), which is not part of this file\'s setup: they are not replayable.',
    ],
  };
}

// ---- PartitionQuery ----

async function partition() {
  const out = [];
  const rp = async (id, d, body, o = {}) => {
    const { c, raw } = await restCase(id, d, 'POST', `${o.parent ?? ROOT}:partitionQuery`, body, { replay: o.replay ?? false });
    out.push(c);
    return { c, raw };
  };
  const cgq = (cg, extra = {}) => ({ from: from(cg, true), orderBy: [asc('__name__')], ...extra });
  // bulk: P_DOCS documents in the top-level collection PB
  const id = (i) => `p${String(i).padStart(5, '0')}`;
  const bulk = {};
  for (let i = 0; i < P_DOCS; i++) bulk[`${PB}/${id(i)}`] = { i: I(i) };
  const sb = await setup(bulk);
  const bulkInfo = { documents: P_DOCS, collection: PB, ids: `${id(0)} .. ${id(P_DOCS - 1)}`, fields: '{i: <its number>}', commits: sb.commits.length };
  await sleep(2000);
  for (const n of [2, 3, 10, 1]) await rp(`valid.count_${n}`, `partitionCount ${n} over ${P_DOCS} documents`, { structuredQuery: cgq(PB), partitionCount: String(n) });
  await rp('valid.count_100', 'partitionCount 100', { structuredQuery: cgq(PB), partitionCount: '100' });
  await rp('valid.count_number', 'partitionCount 3 as a JSON number', { structuredQuery: cgq(PB), partitionCount: 3 });
  await rp('valid.no_order', 'partitionCount 3 with no orderBy', { structuredQuery: { from: from(PB, true) }, partitionCount: '3' });
  {
    const p1 = await rp('valid.page_size_1', 'partitionCount 4, pageSize 1', { structuredQuery: cgq(PB), partitionCount: '4', pageSize: 1 });
    const tok = p1.raw.body.nextPageToken;
    if (tok) {
      const p2 = await rp('valid.page_2', 'the next page (pageToken from valid.page_size_1)', { structuredQuery: cgq(PB), partitionCount: '4', pageSize: 1, pageToken: tok }, {});
      p2.c.request.body.pageToken = '<nextPageToken of valid.page_size_1>';
      const tok2 = p2.raw.body.nextPageToken;
      if (tok2) {
        const p3 = await rp('valid.page_3', 'the page after', { structuredQuery: cgq(PB), partitionCount: '4', pageSize: 1, pageToken: tok2 });
        p3.c.request.body.pageToken = '<nextPageToken of valid.page_2>';
      }
      const pw = await rp('valid.page_token_other_count', 'pageToken of valid.page_size_1 with partitionCount 5', { structuredQuery: cgq(PB), partitionCount: '5', pageSize: 1, pageToken: tok });
      pw.c.request.body.pageToken = '<nextPageToken of valid.page_size_1>';
    }
  }
  await rp('valid.read_time', 'partitionCount 3 with readTime = the last bulk commit', { structuredQuery: cgq(PB), partitionCount: '3', readTime: sb.commitTime });
  await rp('valid.read_time_before', 'partitionCount 3 with readTime 1 µs before the last bulk commit', { structuredQuery: cgq(PB), partitionCount: '3', readTime: microsBefore(sb.commitTime) });
  // refusals on PX (one document, the setup)
  const bad = async (id2, d, body, parent) => rp(`invalid.${id2}`, d, body, { replay: true, parent });
  await bad('count_0', 'partitionCount 0', { structuredQuery: cgq(PX), partitionCount: '0' });
  await bad('count_negative', 'partitionCount -1', { structuredQuery: cgq(PX), partitionCount: '-1' });
  await bad('count_missing', 'no partitionCount', { structuredQuery: cgq(PX) });
  await bad('count_1', 'partitionCount 1', { structuredQuery: cgq(PX), partitionCount: '1' });
  await bad('count_3_one_doc', 'partitionCount 3 over one document', { structuredQuery: cgq(PX), partitionCount: '3' });
  await bad('no_query', 'no structuredQuery', { partitionCount: '3' });
  await bad('order_desc', 'orderBy __name__ desc', { structuredQuery: { from: from(PX, true), orderBy: [desc('__name__')] }, partitionCount: '3' });
  await bad('order_field', 'orderBy a', { structuredQuery: { from: from(PX, true), orderBy: [asc('a')] }, partitionCount: '3' });
  await bad('order_name_then_field', 'orderBy __name__, a', { structuredQuery: { from: from(PX, true), orderBy: [asc('__name__'), asc('a')] }, partitionCount: '3' });
  await bad('limit', 'limit 10', { structuredQuery: cgq(PX, { limit: 10 }), partitionCount: '3' });
  await bad('offset', 'offset 1', { structuredQuery: cgq(PX, { offset: 1 }), partitionCount: '3' });
  await bad('start_at', 'startAt [ref]', { structuredQuery: cgq(PX, { startAt: { values: [R(`${PX}/a`)], before: true } }), partitionCount: '3' });
  await bad('end_at', 'endAt [ref]', { structuredQuery: cgq(PX, { endAt: { values: [R(`${PX}/a`)], before: false } }), partitionCount: '3' });
  await bad('select', 'select [__name__]', { structuredQuery: cgq(PX, { select: { fields: [fp('__name__')] } }), partitionCount: '3' });
  await bad('filter_name', '__name__ >= ref', { structuredQuery: cgq(PX, { where: ff('__name__', 'GREATER_THAN_OR_EQUAL', R(`${PX}/a`)) }), partitionCount: '3' });
  await bad('filter_eq', 'a == 1 (no order)', { structuredQuery: { from: from(PX, true), where: ff('a', 'EQUAL', I(1)) }, partitionCount: '3' });
  await bad('page_size_negative', 'pageSize -1', { structuredQuery: cgq(PX), partitionCount: '3', pageSize: -1 });
  await bad('page_token_garbage', 'pageToken "garbage"', { structuredQuery: cgq(PX), partitionCount: '3', pageToken: 'garbage' });
  await bad('two_from', 'from two collection groups', { structuredQuery: { from: [{ collectionId: PX, allDescendants: true }, { collectionId: PB, allDescendants: true }], orderBy: [asc('__name__')] }, partitionCount: '3' });
  await bad('under_document', `collection group ${PX} under ${PX}/a`, { structuredQuery: cgq(PX), partitionCount: '3' }, `${ROOT}/${PX}/a`);
  await bad('parent_collection', `parent ${PX} (a collection path)`, { structuredQuery: cgq(PX), partitionCount: '3' }, `${ROOT}/${PX}`);
  await bad('find_nearest', 'with findNearest', { structuredQuery: cgq(PX, { findNearest: { vectorField: fp('e'), queryVector: { mapValue: { fields: { __type__: S('__vector__'), value: A(D(1)) } } }, distanceMeasure: 'EUCLIDEAN', limit: 1 } }), partitionCount: '3' });
  if (g) {
    const gc = async (id2, d, req) => {
      const { c } = await grpcCase(id2, d, 'partitionQuery', { parent: ROOT, ...req });
      out.push(c);
    };
    await gc('grpc.valid.count_3', 'partitionCount 3 over the bulk collection group', { structuredQuery: gq({ structuredQuery: cgq(PB) }).structuredQuery, partitionCount: '3' });
    await gc('grpc.invalid.count_0', 'partitionCount 0', { structuredQuery: gq({ structuredQuery: cgq(PX) }).structuredQuery, partitionCount: '0' });
    await gc('grpc.invalid.order_desc', 'orderBy __name__ desc', { structuredQuery: gq({ structuredQuery: { from: from(PX, true), orderBy: [desc('__name__')] } }).structuredQuery, partitionCount: '3' });
  }
  return {
    bulk: bulkInfo,
    cases: out,
    notes: [
      `The valid.* cases read ${P_DOCS} documents written to ${PB} before them (bulk); they are not replayable (the partition points are production's sampling). The invalid.* cases read ${PX}, whose one document is the setup.`,
      'f0-descendants.json has the earlier partition cases (a kindless parent, a collection that is not a group, no order, a filter).',
    ],
  };
}

// ---- Admin API (read-only) ----

async function admin() {
  const out = [];
  // Values that change on their own (version retention window, etag) become placeholders.
  const vol = (x) => {
    if (Array.isArray(x)) return x.map(vol);
    if (x && typeof x === 'object') {
      const o = {};
      for (const [k, y] of Object.entries(x)) o[k] = k === 'earliestVersionTime' && typeof y === 'string' ? '<earliestVersionTime: RFC 3339>' : k === 'etag' && typeof y === 'string' ? '<etag>' : vol(y);
      return o;
    }
    return x;
  };
  const ga = async (id, d, path) => {
    const { c } = await restCase(id, d, 'GET', path);
    c.response.body = vol(c.response.body);
    out.push(c);
    return c;
  };
  const cg = (c) => `${DB}/collectionGroups/${c}`;
  await ga('database.get', 'GET the (default) database', DB);
  await ga('database.list', 'GET the project\'s databases', `projects/${PROJECT}/databases`);
  await ga('indexes.list_all', 'list composite indexes of every collection group (firebase-tools listIndexes)', `${cg('-')}/indexes`);
  await ga('indexes.list_cg', `list composite indexes of ${Q}`, `${cg(Q)}/indexes`);
  await ga('indexes.list_page_size_1', 'list composite indexes, pageSize 1', `${cg('-')}/indexes?pageSize=1`);
  await ga('indexes.list_filter', 'list composite indexes with filter "queryScope=COLLECTION"', `${cg('-')}/indexes?filter=queryScope%3DCOLLECTION`);
  await ga('indexes.get_missing', 'GET a composite index that does not exist (a well-formed id)', `${cg(Q)}/indexes/CICAgICAgICA`);
  await ga('indexes.get_missing_docs_id', 'GET a composite index with the id format of the docs (CICAgOjXh4EK)', `${cg(Q)}/indexes/CICAgOjXh4EK`);
  await ga('indexes.get_missing_word', 'GET a composite index named "nonexistent"', `${cg(Q)}/indexes/nonexistent`);
  await ga('indexes.get_dash_group', 'GET collectionGroups/-/indexes/CICAgICAgICA', `${cg('-')}/indexes/CICAgICAgICA`);
  await ga('fields.list_firebase_tools', 'list field overrides with firebase-tools\' filter "indexConfig.usesAncestorConfig=false OR ttlConfig:*"', `${cg('-')}/fields?filter=indexConfig.usesAncestorConfig=false%20OR%20ttlConfig:*`);
  await ga('fields.list_ancestor_false', 'list fields with filter "indexConfig.usesAncestorConfig:false" (gcloud)', `${cg('-')}/fields?filter=indexConfig.usesAncestorConfig:false`);
  await ga('fields.list_no_filter', 'list fields of collectionGroups/- with no filter', `${cg('-')}/fields`);
  await ga('fields.list_cg_no_filter', `list fields of ${Q} with no filter`, `${cg(Q)}/fields`);
  await ga('fields.list_cg_filter', `list fields of ${Q} with firebase-tools' filter`, `${cg(Q)}/fields?filter=indexConfig.usesAncestorConfig=false%20OR%20ttlConfig:*`);
  await ga('fields.list_bad_filter', 'list fields with filter "nonsense"', `${cg('-')}/fields?filter=nonsense`);
  await ga('fields.list_page_size_1', 'list fields with the filter, pageSize 1', `${cg('-')}/fields?filter=indexConfig.usesAncestorConfig=false%20OR%20ttlConfig:*&pageSize=1`);
  await ga('fields.get_default', 'GET collectionGroups/__default__/fields/*', `${cg('__default__')}/fields/*`);
  await ga('fields.get_plain', `GET ${Q}/fields/v (no override)`, `${cg(Q)}/fields/v`);
  await ga('fields.get_nested', `GET ${Q}/fields/v.a`, `${cg(Q)}/fields/v.a`);
  await ga('fields.get_backticked', `GET ${Q}/fields/\`a b\``, `${cg(Q)}/fields/${encodeURIComponent('`a b`')}`);
  await ga('fields.get_name', `GET ${Q}/fields/__name__`, `${cg(Q)}/fields/__name__`);
  await ga('fields.get_star', `GET ${Q}/fields/* (the collection-level default)`, `${cg(Q)}/fields/*`);
  await ga('fields.get_never_written_group', 'GET fields/v of a collection group never written', `${cg('aruma-ref-f3-never')}/fields/v`);
  await ga('fields.get_invalid_path', `GET ${Q}/fields/a..b`, `${cg(Q)}/fields/a..b`);
  await ga('fields.get_reserved', `GET ${Q}/fields/__x__`, `${cg(Q)}/fields/__x__`);
  await ga('fields.get_dash_group', 'GET collectionGroups/-/fields/v', `${cg('-')}/fields/v`);
  await ga('fields.get_default_other', 'GET collectionGroups/__default__/fields/v', `${cg('__default__')}/fields/v`);
  await ga('operations.list', 'list the database\'s long-running operations', `${DB}/operations`);
  await ga('operations.list_page_size_1', 'list operations, pageSize 1', `${DB}/operations?pageSize=1`);
  await ga('operations.get_missing', 'GET an operation that does not exist (a well-formed name)', `${DB}/operations/ASA0NjE1MDcyMDYxChp0bHVhZmVkBxJsYXJ0bmVjc3Utc2Jvai1uaW1kYRQKLRI`);
  await ga('operations.get_missing_word', 'GET operations/nonexistent', `${DB}/operations/nonexistent`);
  await ga('backup_schedules.list', 'list backup schedules', `${DB}/backupSchedules`);
  await ga('database.get_missing_collection_group', 'GET collectionGroups/x (not a resource)', `${cg(Q)}`);
  return {
    cases: out,
    notes: [
      'Every call is a GET; nothing was created, changed or deleted. earliestVersionTime and etag become placeholders (they move on their own); every other value is as returned.',
      'The project has no composite index and no field override at the time of recording (indexes-105.json saw the same).',
    ],
  };
}

// ---- index entry limits ----

async function indexEntries() {
  const out = [];
  const docName = (id) => `${ROOT}/${IX}/${id}`;
  // put writes one document by commit and returns the outcome; the request is
  // described, not stored (it is up to a megabyte).
  const put = async (caseId, d, name, fieldsObj, shape) => {
    assertScoped(name);
    const r = await commitWrites([{ update: { name, fields: fieldsObj } }]);
    await pause();
    const err = r.body.error;
    const c = {
      id: caseId, description: d, transport: 'rest',
      request: { method: 'POST', path: relProject(`${DB}/documents:commit`), shape: relProject(shape), bytes: Buffer.byteLength(JSON.stringify({ writes: [{ update: { name, fields: fieldsObj } }] })) },
      response: { http: r.http, body: normalise(r.body) },
    };
    if (err) c.error = { http: r.http, status: err.status, message: err.message };
    out.push(c);
    return r.http === 200;
  };
  const scalars = (n) => Object.fromEntries(Array.from({ length: n }, (_, i) => [`f${String(i).padStart(5, '0')}`, I(i)]));
  const arr = (n) => ({ a: A(...Array.from({ length: n }, (_, i) => I(i))) });
  const map = (n) => ({ m: M(scalars(n)) });
  // nameOf is a document name of exactly `bytes` bytes below documents/:
  // IX/<id1>/c/<id2>/c/<id3>/c/<id4>/c/<id5>, the ids sharing the length.
  const nameOf = (bytes, tag) => {
    const sum = bytes - (IX.length + 5 + 8);
    const ids = [];
    for (let i = 0; i < 5; i++) {
      const len = Math.floor(sum / 5) + (i < sum % 5 ? 1 : 0);
      ids.push(i === 0 ? `${tag}-`.concat('n'.repeat(len - tag.length - 1)) : 'n'.repeat(len));
    }
    const rel = `${IX}/${ids.join('/c/')}`;
    if (Buffer.byteLength(rel) !== bytes) throw new Error(`nameOf ${bytes}: got ${Buffer.byteLength(rel)}`);
    return `${ROOT}/${rel}`;
  };
  const longShape = (bytes, what) => `${IX}/<${bytes - IX.length - 1} bytes in 5 ids under collections "c">: ${what}`;
  // search finds the boundary between an accepted lo and a refused hi (both
  // checked first), by bisection; at(n) writes one document for n.
  const search = async (kind, lo, hi, write) => {
    const tries = [];
    const at = async (n) => {
      const ok = await write(n);
      tries.push({ n, accepted: ok });
      return ok;
    };
    if (!(await at(lo))) return { kind, tries, note: 'lower bound refused' };
    if (await at(hi)) return { kind, tries, note: 'upper bound accepted' };
    while (hi - lo > 1) {
      const mid = Math.floor((lo + hi) / 2);
      if (await at(mid)) lo = mid;
      else hi = mid;
    }
    return { kind, maxAccepted: lo, minRefused: hi, tries };
  };
  const count = (kind, make, what) => (n) => put(`${kind}.n_${n}`, `one document with ${what(n)}`, docName(`${kind}-${n}`), make(n), `${kind} n=${n}`);
  const results = [];
  results.push(await search('fields', 19990, 20010, count('fields', scalars, (n) => `${n} integer fields`)));
  results.push(await search('array', 19000, 39990, count('array', arr, (n) => `one array of ${n} distinct integers`)));
  results.push(await search('map', 19990, 20010, count('map', map, (n) => `one map of ${n} integer subfields`)));
  // the size of one entry: the document name carries it (f0-errors.json: a
  // 6127-byte name with no field is refused "Index entry is too large.")
  const nameSearch = (kind, fieldsObj, what) => (L) => put(`${kind}.name_${L}`, `one document named with ${L} bytes below documents/, ${what}`, nameOf(L, kind), fieldsObj, longShape(L, what));
  results.push(await search('entry_no_fields', 4000, 6127, nameSearch('entry_no_fields', {}, 'no field')));
  results.push(await search('entry_int', 1000, 6127, nameSearch('entry_int', { f: I(1) }, 'one integer field f')));
  results.push(await search('entry_string_1500', 2000, 6127, nameSearch('entry_string_1500', { f: S('s'.repeat(1500)) }, 'one string field f of 1500 bytes')));
  results.push(await search('entry_string_3000', 2000, 6127, nameSearch('entry_string_3000', { f: S('s'.repeat(3000)) }, 'one string field f of 3000 bytes')));
  results.push(await search('entry_long_field_name', 1000, 6127, nameSearch('entry_long_field_name', { ['x'.repeat(1500)]: I(1) }, 'one integer field whose name is 1500 bytes')));
  // the total of entry sizes: many fields each carrying a long name
  const okInt = results.find((r) => r.kind === 'entry_int').maxAccepted;
  if (okInt) {
    const L = okInt - 200;
    const nameN = (n) => nameOf(L, `total${n}`);
    const tr = await search('total', 2000, 20000, (n) => put(`total.n_${n}`, `a new document named with ${L} bytes below documents/, ${n} integer fields`, nameN(n), scalars(n), longShape(L, `${n} integer fields`)));
    results.push({ ...tr, nameBytes: L });
    // the largest accepted document, deleted by one commit
    const name = nameN(tr.maxAccepted ?? 2000);
    const cur = await rest('GET', `${name}?mask.fieldPaths=f00000`);
    if (cur.http === 200) {
      const r = await rest('POST', `${DB}/documents:commit`, { writes: [{ delete: name }] });
      await pause();
      const c = { id: 'total.delete', description: `delete, by one commit, the document of the last accepted total.n_* write`, transport: 'rest', request: { method: 'POST', path: relProject(`${DB}/documents:commit`), shape: relProject(longShape(L, 'delete')) }, response: { http: r.http, body: normalise(r.body) } };
      if (r.body.error) c.error = { http: r.http, status: r.body.error.status, message: r.body.error.message };
      if (r.http !== 200) c.thenShrunkCommits = await shrinkDelete(name);
      out.push(c);
    }
  }
  return {
    cases: out,
    summary: results,
    notes: [
      'The requests are described (shape, byte length), not stored. The documents under aruma-ref-f3-ix were deleted at the cleanup.',
      'Single-field index entries per value: two (ascending, descending) for a scalar field; for an array one per distinct element (contains) and two for the whole array; for a map two per subfield and two for the whole map (IX §1.1).',
    ],
  };
}

// ---- cleanup ----

const sweepPage = (after) => rest('POST', `${ROOT}:runQuery`, {
  structuredQuery: {
    from: [{ allDescendants: true }],
    where: {
      compositeFilter: {
        op: 'AND',
        filters: [
          { fieldFilter: { field: { fieldPath: '__name__' }, op: after ? 'GREATER_THAN' : 'GREATER_THAN_OR_EQUAL', value: { referenceValue: after ?? `${ROOT}/aruma-ref-f3/${MIN_ID}` } } },
          { fieldFilter: { field: { fieldPath: '__name__' }, op: 'LESS_THAN', value: { referenceValue: `${ROOT}/aruma-ref-f4/${MIN_ID}` } } },
        ],
      },
    },
    select: { fields: [{ fieldPath: '__name__' }] },
    orderBy: [{ field: { fieldPath: '__name__' }, direction: 'ASCENDING' }],
    limit: 1000,
  },
});
async function sweep() {
  const all = [];
  let after;
  for (;;) {
    const r = await sweepPage(after);
    if (r.http !== 200) return { http: r.http, names: all };
    const n = (Array.isArray(r.body) ? r.body : []).filter((x) => x.document).map((x) => x.document.name);
    all.push(...n);
    if (n.length < 1000) return { http: 200, names: all };
    after = n[n.length - 1];
  }
}

async function deleteAll(names, res) {
  for (let i = 0; i < names.length; i += 400) {
    const writes = names.slice(i, i + 400).map((x) => ({ delete: x }));
    for (const w of writes) assertScoped(w.delete);
    const r = await rest('POST', `${DB}/documents:commit`, { writes });
    res.push(r.http);
    if (r.http === 400 && /Transaction too big/.test(r.body.error?.message) && writes.length > 1) {
      // documents with tens of thousands of index entries: one delete per commit
      for (const w of writes) {
        const r1 = await rest('POST', `${DB}/documents:commit`, { writes: [w] });
        res.push(r1.http);
        if (r1.http === 400 && /Transaction too big/.test(r1.body.error?.message)) {
          res.push(`shrunk: ${await shrinkDelete(w.delete)}`);
          continue;
        }
        if (r1.http !== 200) throw new Error(`cleanup commit failed: HTTP ${r1.http} ${JSON.stringify(r1.body).slice(0, 300)}`);
      }
      continue;
    }
    if (r.http !== 200) throw new Error(`cleanup commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
}

// shrinkDelete deletes a document too big to delete in one commit (each of its
// thousands of fields carries a long name into its index entries): its fields
// are removed 1000 at a time by update masks, then it is deleted. Returns the
// number of commits.
async function shrinkDelete(name) {
  assertScoped(name);
  let n = 0;
  for (;;) {
    const cur = await rest('GET', name);
    if (cur.http === 404) return n;
    const keys = Object.keys(cur.body.fields || {});
    const r = keys.length
      ? await rest('POST', `${DB}/documents:commit`, { writes: [{ update: { name, fields: {} }, updateMask: { fieldPaths: keys.slice(0, 1000) } }] })
      : await rest('POST', `${DB}/documents:commit`, { writes: [{ delete: name }] });
    n++;
    if (r.http !== 200) throw new Error(`shrinkDelete: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
    if (!keys.length) return n;
    await pause();
  }
}

async function strayCheck() {
  const s = await sweep();
  if (s.http !== 200) throw new Error(`stray sweep failed: HTTP ${s.http}`);
  const res = [];
  if (s.names.length) await deleteAll(s.names, res);
  return { strays: s.names.map(rel), commitHttp: res };
}

async function cleanup() {
  for (const t of [...openTxns]) {
    await rest('POST', `${ROOT}:rollback`, { transaction: t });
    openTxns.delete(t);
  }
  const res = [];
  const names = [...written];
  await deleteAll(names, res);
  const after = await sweep();
  const strays = after.names;
  if (strays.length) await deleteAll(strays, res);
  const fin = await sweep();
  const lists = {};
  for (const t of TOPS) {
    const l = await rest('GET', `${ROOT}/${t}?showMissing=true&pageSize=100`);
    lists[t] = (l.body.documents || []).map((d) => rel(d.name));
  }
  const ok = fin.http === 200 && fin.names.length === 0 && Object.values(lists).every((l) => l.length === 0) && openTxns.size === 0;
  const short = (n) => (n.length > 200 ? `<${Buffer.byteLength(n)} bytes, starts ${JSON.stringify(n.slice(0, 60))}>` : n);
  return {
    ok,
    deletedCount: names.length,
    deleted: names.map(rel).map(short).sort(),
    strays: strays.map(rel).map(short),
    commitHttp: res,
    sweep: { http: fin.http, query: `kindless allDescendants, __name__ in [aruma-ref-f3/${MIN_ID}, aruma-ref-f4/${MIN_ID}), paged by 1000`, remaining: fin.names.map(rel) },
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
  const only = (process.argv.find((a) => a.startsWith('--only=')) || '').slice(7).split(',').filter(Boolean);
  const want = (k) => !only.length || only.includes(k);
  let cl;
  let atStart;
  try {
    log('strays ');
    atStart = await strayCheck();
    let qsetup;
    if (want('run-query') || want('aggregations')) qsetup = await setup(QDOCS);
    if (want('run-query')) {
      log('run-query ');
      results['f3-run-query.json'] = { setup: { commits: qsetup.commits }, ...(await runQuery(qsetup)) };
    }
    if (want('limits')) {
      log('limits ');
      const s = await setup({ [`${LIM}/a`]: { a: I(1), arr: A(I(1)) }, [`${LIM}/a/sub/b`]: { a: I(1) } });
      results['f3-query-limits.json'] = { setup: { commits: s.commits }, ...(await limits()) };
    }
    if (want('aggregations')) {
      log('aggregations ');
      const docs = {};
      for (const [s, list] of Object.entries(AGG_DOCS)) list.forEach((f, i) => { docs[`${AG}-${s}/d${i}`] = f; });
      const s = await setup(docs);
      results['f3-aggregations.json'] = { setup: { commits: s.commits }, ...(await aggregations()) };
    }
    if (want('partition')) {
      log('partition ');
      const s = await setup({ [`${PX}/a`]: { a: I(1) } });
      results['f3-partition-query.json'] = { setup: { commits: s.commits }, ...(await partition()) };
    }
    if (want('admin')) {
      log('admin ');
      results['f3-admin.json'] = await admin();
    }
    if (want('index-entries')) {
      log('index-entries ');
      results['f3-index-entries.json'] = await indexEntries();
    }
  } finally {
    log('\ncleanup ');
    cl = await cleanup();
    log(cl.ok ? 'verified\n' : 'NOT VERIFIED\n');
  }
  if (!cl.ok) {
    console.error('record-f3: the cleanup is not verified; no corpus file written. Remaining:', JSON.stringify({ sweep: cl.sweep.remaining, lists: cl.listsShowMissing, open: cl.transactionsLeftOpen }));
    process.exit(2);
  }
  if (only.length) {
    console.error('record-f3: --only run; cleanup verified, no corpus file written (a partial run is a probe).');
    if (process.env.F3_PROBE_OUT) writeFileSync(process.env.F3_PROBE_OUT, JSON.stringify({ results, cleanup: cl }, null, 2));
    return;
  }
  results['f3-cleanup.json'] = { cleanup: { ...cl, straysAtStart: atStart } };
  const finished = new Date().toISOString();
  for (const [file, body] of Object.entries(results)) {
    const header = {
      oracle: 'production',
      target,
      generator: GENERATOR,
      recordedAt: `${started} .. ${finished}`,
      versions: ver,
      notes: [
        'Auth: the firebase CLI login (a project owner); security rules do not apply.',
        'Times, durations, page tokens and transaction ids are replaced by placeholders (normalise in lib.mjs); everything else is as returned.',
        ...(body.notes || []),
      ],
    };
    const { notes, ...payload } = body;
    writeCorpus(file, header, payload);
  }
}

main().catch((e) => {
  console.error('record-f3 failed:', e.stack);
  process.exit(1);
});
