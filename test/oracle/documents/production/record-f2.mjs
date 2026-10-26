// F2 production recordings for the Document Database's writes and transactions
// (plan section 6, F2: Commit with transforms and preconditions, BatchWrite,
// BeginTransaction, Rollback, reads inside transactions, lock ranges):
//
//   f2-transforms.json    field transforms: REQUEST_TIME (its result against
//                         commitTime), increment (int and double mixes, int64
//                         overflow, missing and non-numeric fields, NaN, -0.0),
//                         maximum and minimum (mixed types, NaN, -0.0 against
//                         0.0, missing fields), appendMissingElements and
//                         removeAllFromArray (duplicates, NaN, int against
//                         double, map equality, non-array fields), transforms
//                         on a missing document, several transforms on one
//                         path, overlap with the update mask, the legacy
//                         `transform` write, transformResults, and the
//                         document size limit reached by a transform
//   f2-commit.json        Commit: the no-op write (does updateTime stay, does
//                         commitTime advance), an update mask on a missing
//                         document, exists and updateTime preconditions,
//                         verify, two writes to one document, 501 writes
//   f2-batch-write.json   BatchWrite: per-write status, mixed success and
//                         failure, one document twice, labels
//   f2-transactions.json  BeginTransaction (readWrite, readOnly, readOnly at a
//                         readTime, retryTransaction), Rollback (twice, unknown,
//                         forged), Commit with an unknown, forged, rolled-back or
//                         read-only transaction, reads in a transaction (Get,
//                         BatchGet, RunQuery, RunAggregationQuery, ListDocuments)
//                         and newTransaction, and the transaction lifetime
//                         (idle and active for 300 s)
//   f2-locks.json         which writes wait on a read-write transaction's
//                         query: the scanned range, an insert into it (phantom),
//                         outside it, a sibling collection of the same id, a
//                         collection-group query
//   f2-non-utf8.json      a document id (and a field name, and a string) that is
//                         not valid UTF-8, over REST (path, query parameter,
//                         raw JSON body) and gRPC (hand-encoded protobuf)
//   f2-cleanup.json       the deletion of everything written, verified
//
//   node record-f2.mjs
//
// Each topic is recorded over REST (firestore.googleapis.com/v1) and, where
// FIRESTORE_PROTOS is set, over gRPC for the cases that differ by transport.
//
// Files that hold replayable cases (`replay: true`: a fixed REST request whose
// answer depends only on the file's `setup` section, because it overwrites its
// document whole or is refused) have a `setup` section, as in record-f1.mjs.
//
// Data is written ONLY under top-level collections whose id starts with
// aruma-ref-f2 (assertScoped refuses anything outside aruma-ref), every
// transaction opened read-write is committed or rolled back, everything written
// is deleted at the end, and the deletion is verified by a kindless query over
// every collection id from "aruma-ref-f2" up to "aruma-ref-f3" and by showMissing
// lists of each collection used. The same kindless query runs first: documents
// left by an interrupted run are listed as strays and deleted. A run whose
// cleanup is not verified writes no corpus file. Nothing creates or changes an
// index, field override, rule, TTL policy, user or project setting.
import {
  PROJECT, DB, ROOT, rest, versions, normalise, compact, writeCorpus, assertScoped,
  grpcAvailable, grpcUnary, grpcStream, grpcRawUnary, grpcVersions, toGrpc,
} from './lib.mjs';
import { writeFileSync } from 'node:fs';

const GENERATOR = 'test/oracle/documents/production/record-f2.mjs';
const MIN_ID = '__id-9223372036854775808__';
const T = 'aruma-ref-f2-tf'; // transforms
const C = 'aruma-ref-f2-commit'; // commit
const B = 'aruma-ref-f2-bw'; // batchWrite
const X = 'aruma-ref-f2-txn'; // transactions
const E = 'aruma-ref-f2-expiry'; // transaction lifetime
const K = 'aruma-ref-f2-lock'; // lock ranges
const U = 'aruma-ref-f2-utf'; // non-UTF-8 ids
const TOPS = [T, C, B, X, E, K, U];
const STALE = '2001-01-01T00:00:00.000001Z'; // a fixed precondition time, so its version stays in the corpus
const EXPIRY_WAIT_MS = 300_000;

const log = (s) => process.stderr.write(s);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const pause = () => sleep(120); // keeps the request rate modest
const rel = (name) => (typeof name === 'string' ? name.replace(`${ROOT}/`, '') : name);
const relProject = (s) => s.split(`projects/${PROJECT}/`).join('projects/{project}/');
const g = grpcAvailable();

// ---- values ----

const I = (x) => ({ integerValue: String(x) });
const D = (x) => ({ doubleValue: x }); // x: a number (-0 too) or 'NaN', 'Infinity', '-Infinity'
const S = (x) => ({ stringValue: x });
const A = (...vals) => ({ arrayValue: { values: vals } });
const M = (f) => ({ mapValue: { fields: f } });
const NULL = { nullValue: null };
const INT64_MAX = '9223372036854775807';
const INT64_MIN = '-9223372036854775808';

// FIX is the document every transform case overwrites its own document with;
// FIXNZ adds nz = -0.0 for the cases that need it (a request holding -0.0 is
// not replayable: canonical JSON cannot keep it).
const FIX = {
  i: I(5), i0: I(0), d: D(2.5), dw: D(5), s: S('str'), n: NULL, b: { booleanValue: true },
  imax: I(INT64_MAX), imin: I(INT64_MIN), nan: D('NaN'), z: D(0), inf: D('Infinity'),
  a: A(I(1), S('x'), M({ m: I(1) })),
  a2: A(I(1), I(1), I(2)),
  an: A(D('NaN'), I(1)),
  az: A(D(0)),
  am: A(M({ a: I(1), b: I(2) })),
  m: M({ k: I(1) }),
};

const FIXNZ = { ...FIX, nz: D(-0) };

// NEG0 stands for -0.0 while a request is turned into JSON text, which
// JSON.stringify would write as 0.
const NEG0 = '\u0000aruma-negative-zero\u0000';
const toJson = (x) => JSON.stringify(x, (k, y) => (Object.is(y, -0) ? NEG0 : y)).split(JSON.stringify(NEG0)).join('-0.0');

// jsonSafe marks the numbers canonical JSON cannot keep: -0 (a JSON number -0
// over REST, a double over gRPC), and NaN and the infinities (gRPC doubles; REST
// sends them as the strings "NaN" and "Infinity").
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

// ---- times ----

function rfcToGrpc(rfc) {
  const m = /^(.*?)(?:\.(\d+))?Z$/.exec(rfc);
  return { seconds: String(Math.floor(Date.parse(`${m[1]}Z`) / 1000)), nanos: m[2] ? Number(m[2].padEnd(9, '0')) : 0 };
}
// nanosOf is a time (RFC 3339 or a Timestamp) in nanoseconds since the epoch.
function nanosOf(t) {
  if (t && typeof t === 'object') return BigInt(t.seconds) * 1_000_000_000n + BigInt(t.nanos || 0);
  const m = /^(.*?)(?:\.(\d+))?Z$/.exec(t);
  return BigInt(Date.parse(`${m[1]}Z`) / 1000) * 1_000_000_000n + BigInt((m[2] || '').padEnd(9, '0').slice(0, 9));
}
const micros = (rfc) => String(nanosOf(rfc) / 1000n);
const fractionDigits = (t) => (typeof t === 'string' ? (/\.(\d+)Z$/.exec(t)?.[1] ?? '').length : null);
const minuteAgo = (min, sec = 0) => new Date(Math.floor((Date.now() - min * 60_000) / 60_000) * 60_000 + sec * 1000).toISOString();

// pinVersions checks the two versions of a "stored version (S) does not match
// the required base version (R)" message (as record-f1.mjs).
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
  if (Array.isArray(b)) for (const x of b) if (x && x.error) x.error.message = fix(x.error.message);
}

// ---- writes, tracked for the cleanup ----

const written = new Set();
const rawPaths = new Set(); // REST paths (percent-encoded) of documents whose name is not UTF-8
const openTxns = new Set(); // read-write transactions still to roll back

function scopeWrites(writes) {
  for (const w of writes) {
    const name = w.update?.name ?? w.delete ?? w.verify ?? w.transform?.document;
    // A write naming no document (the write.no_operation cases) can touch none.
    if (name !== undefined || w.update || w.transform) assertScoped(name ?? '');
  }
}
function track(writes, http) {
  if (http !== 200) return;
  for (const w of writes) {
    const n = w.update?.name ?? w.transform?.document;
    if (n) written.add(n);
  }
}
async function commitWrites(writes, extra = {}) {
  scopeWrites(writes);
  const r = await rest('POST', `${DB}/documents:commit`, { writes, ...extra });
  track(writes, r.http);
  return r;
}

// setup writes fixtures by one commit and returns {commit, raw}.
async function setup(docs) {
  const writes = Object.entries(docs).map(([r, f]) => ({ update: { name: `${ROOT}/${r}`, fields: f } }));
  const res = await commitWrites(writes);
  if (res.http !== 200) throw new Error(`setup commit failed: HTTP ${res.http} ${JSON.stringify(res.body).slice(0, 300)}`);
  return { commit: JSON.parse(relProject(toJson({ writes }))), raw: res.body };
}

// ---- recording helpers ----

function replaceAll(s, replace) {
  for (const [from, to] of replace) {
    if (!from) continue;
    s = s.split(encodeURIComponent(from)).join(to).split(from).join(to);
  }
  return s;
}

// restCase sends one REST call and returns the case. A request body is sent as
// JSON text in which -0 stays -0.0 (bodyText keeps that text); `raw` sends bytes
// as given, recorded by `rawShown`.
async function restCase(id, description, method, path, body, { replace = new Map(), replay = false, raw, rawShown } = {}) {
  let sent;
  let bodyText;
  if (raw !== undefined) sent = raw;
  else if (body !== undefined) {
    sent = toJson(body);
    if (sent.includes('-0.0')) bodyText = relProject(replaceAll(compactText(sent), replace));
  }
  const r = await rest(method, path, sent);
  await pause();
  const arr = Array.isArray(r.body) ? r.body : [r.body];
  const err = arr.find((x) => x && x.error)?.error;
  const c = {
    id, description, transport: 'rest',
    request: { method, path: relProject(replaceAll(path, replace)) },
    response: { http: r.http, bodyIsArray: Array.isArray(r.body), body: normalise(jsonSafe(r.body), replace) },
  };
  if (body !== undefined) c.request.body = JSON.parse(relProject(JSON.stringify(compact(normalise(jsonSafe(body), replace)))));
  if (bodyText) c.request.bodyText = bodyText;
  if (raw !== undefined) c.request.bodyBytes = relProject(rawShown);
  if (err) c.error = { http: r.http, status: err.status, message: normalise(err.message, replace) };
  if (r.body && r.body.raw !== undefined) c.responseNotJson = true;
  if (replay) c.replay = true;
  return { c, raw: r };
}
const compactText = (s) => (s.length > 2000 ? `${s.slice(0, 300)}…(${Buffer.byteLength(s)} bytes)` : s);

// grpcCase sends one gRPC call (unary or server streaming) and returns the case.
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
  return { c, raw: r };
}

// maskTimestamps replaces every timestampValue (the REQUEST_TIME results; the
// fixtures hold no timestamp) by its type and precision.
function maskTimestamps(x) {
  if (Array.isArray(x)) return x.map(maskTimestamps);
  if (!x || typeof x !== 'object' || Buffer.isBuffer(x)) return x;
  const o = {};
  for (const [k, y] of Object.entries(x)) {
    if (k === 'timestampValue' && typeof y === 'string' && !y.startsWith('<')) o[k] = `<timestampValue: RFC 3339, ${fractionDigits(y)} fraction digits>`;
    else if (k === 'timestampValue' && y && typeof y === 'object' && 'seconds' in y) o[k] = `<timestampValue: Timestamp, nanos a multiple of 1000000: ${Number(y.nanos || 0) % 1_000_000 === 0}>`;
    else o[k] = maskTimestamps(y);
  }
  return o;
}
function timestampsIn(x, out = []) {
  if (Array.isArray(x)) x.forEach((y) => timestampsIn(y, out));
  else if (x && typeof x === 'object' && !Buffer.isBuffer(x)) {
    for (const [k, y] of Object.entries(x)) {
      if (k === 'timestampValue') out.push(y);
      else timestampsIn(y, out);
    }
  }
  return out;
}
// timeRelation compares a REQUEST_TIME result with the commit time.
function timeRelation(result, commitTime) {
  const r = nanosOf(result);
  const ct = nanosOf(commitTime);
  return {
    equalsCommitTime: r === ct,
    equalsCommitTimeTruncatedToMillis: r === (ct / 1_000_000n) * 1_000_000n,
    equalsCommitTimeTruncatedToMicros: r === (ct / 1000n) * 1000n,
    resultFractionDigits: fractionDigits(result),
    commitTimeFractionDigits: fractionDigits(commitTime),
    order: r < ct ? 'result before commitTime' : r > ct ? 'result after commitTime' : 'equal',
    commitTimeMinusResultMicros: Number((ct - r) / 1000n), // differs on every run
  };
}

// topField is the first segment of a field path, unquoted.
function topField(p) {
  if (p.startsWith('`')) return p.slice(1, p.indexOf('`', 1)).replace(/\\(.)/g, '$1');
  return p.split('.')[0];
}

// stored reads a document and keeps the named top-level fields.
async function stored(name, keep) {
  const r = await rest('GET', name);
  await pause();
  if (r.http !== 200) return { http: r.http, error: r.body.error ? { status: r.body.error.status, message: r.body.error.message } : r.body };
  const f = r.body.fields || {};
  const fieldsOut = {};
  for (const k of keep) fieldsOut[k] = k in f ? f[k] : '<absent>';
  return { http: 200, fields: jsonSafe(fieldsOut), fieldNames: Object.keys(f).sort(), raw: r.body };
}

// ---- transforms ----

const inc = (p, val) => ({ fieldPath: p, increment: val });
const max = (p, val) => ({ fieldPath: p, maximum: val });
const min = (p, val) => ({ fieldPath: p, minimum: val });
const app = (p, ...vals) => ({ fieldPath: p, appendMissingElements: { values: vals } });
const rem = (p, ...vals) => ({ fieldPath: p, removeAllFromArray: { values: vals } });
const st = (p) => ({ fieldPath: p, setToServerValue: 'REQUEST_TIME' });

async function transforms() {
  const out = [];
  const relations = [];
  const docName = (id) => `${ROOT}/${T}/${id.replace(/\./g, '-')}`;
  const cm = async (id, d, writes, { replay = false, keep = [], name, extra } = {}) => {
    scopeWrites(writes);
    const { c, raw } = await restCase(id, d, 'POST', `${ROOT}:commit`, { writes, ...(extra || {}) }, { replay });
    track(writes, raw.http);
    const wr = raw.body.writeResults || [];
    if (raw.http === 200) {
      c.writeResultKeys = wr.map((r) => Object.keys(r).sort());
      const ts = timestampsIn(wr);
      if (ts.length) {
        c.requestTimeRelations = ts.map((t) => timeRelation(t, raw.body.commitTime));
        c.replay = undefined;
      }
      if (name) {
        const s = await stored(name, keep);
        if (s.http === 200) {
          const sts = timestampsIn(s.fields);
          if (sts.length && ts.length) c.requestTimeRelations.push({ storedEqualsTransformResult: sts.map((x) => ts.some((y) => nanosOf(x) === nanosOf(y))) });
          c.stored = { fields: maskTimestamps(s.fields), fieldNames: s.fieldNames, updateTimeEqualsCommitTime: s.raw.updateTime === raw.body.commitTime };
        } else c.stored = s;
      }
    }
    c.response.body = maskTimestamps(c.response.body);
    if (JSON.stringify(c).includes('<number -0>')) delete c.replay;
    if (!c.replay) delete c.replay;
    out.push(c);
    return raw;
  };
  // tf overwrites the case's own document with FIX and applies the transforms.
  const tf = (id, d, ts, o = {}) => {
    const name = docName(id);
    const w = { update: { name, fields: o.fields ?? FIX }, ...(o.mask ? { updateMask: { fieldPaths: o.mask } } : {}), updateTransforms: ts, ...(o.current ? { currentDocument: o.current } : {}) };
    const keep = [...new Set([...ts.map((t) => (typeof t.fieldPath === 'string' && t.fieldPath ? topField(t.fieldPath) : null)).filter(Boolean), ...(o.keep || [])])];
    return cm(id, d, [w], { replay: o.replay ?? true, keep, name });
  };

  // REQUEST_TIME
  await tf('st.basic', 'ts = REQUEST_TIME', [st('ts')]);
  await tf('st.two_fields', 'ts1 and ts2 = REQUEST_TIME in one write', [st('ts1'), st('ts2')]);
  await tf('st.nested', 'm.ts = REQUEST_TIME (m is a map)', [st('m.ts')]);
  await tf('st.overwrite_string', 's (a string) = REQUEST_TIME', [st('s')]);
  {
    const n1 = docName('st.two_writes_one_commit_1');
    const n2 = docName('st.two_writes_one_commit_2');
    await cm('st.two_writes_one_commit', 'two documents, each with ts = REQUEST_TIME, in one commit', [
      { update: { name: n1, fields: { a: I(1) } }, updateTransforms: [st('ts')] },
      { update: { name: n2, fields: { a: I(2) } }, updateTransforms: [st('ts')] },
    ]);
    const wr = out.at(-1);
    const r = await rest('GET', n1);
    const r2 = await rest('GET', n2);
    if (r.http === 200 && r2.http === 200) wr.stored = { bothEqual: r.body.fields.ts.timestampValue === r2.body.fields.ts.timestampValue };
  }

  // increment
  await tf('inc.int_int', 'i (5) += 3', [inc('i', I(3))]);
  await tf('inc.int_double', 'i (5) += 0.5', [inc('i', D(0.5))]);
  await tf('inc.int_whole_double', 'i (5) += 1.0 (a double operand with an integral value)', [inc('i', D(1))]);
  await tf('inc.double_int', 'd (2.5) += 1', [inc('d', I(1))]);
  await tf('inc.double_double', 'd (2.5) += 0.25', [inc('d', D(0.25))]);
  await tf('inc.whole_double_int', 'dw (5.0) += 1', [inc('dw', I(1))]);
  await tf('inc.overflow_max', 'imax (int64 max) += 1', [inc('imax', I(1))]);
  await tf('inc.overflow_min', 'imin (int64 min) += -1', [inc('imin', I(-1))]);
  await tf('inc.max_plus_max', 'imax += int64 max', [inc('imax', I(INT64_MAX))]);
  await tf('inc.min_plus_min', 'imin += int64 min', [inc('imin', I(INT64_MIN))]);
  await tf('inc.max_plus_double', 'imax += 1.0', [inc('imax', D(1))]);
  await tf('inc.max_minus_one', 'imax += -1 (no overflow)', [inc('imax', I(-1))]);
  await tf('inc.missing_int', 'q (absent) += 2', [inc('q', I(2))]);
  await tf('inc.missing_double', 'q (absent) += 2.5', [inc('q', D(2.5))]);
  await tf('inc.on_string', 's (a string) += 1', [inc('s', I(1))]);
  await tf('inc.on_null', 'n (null) += 1.5', [inc('n', D(1.5))]);
  await tf('inc.on_array', 'a (an array) += 1', [inc('a', I(1))]);
  await tf('inc.on_map', 'm (a map) += 1', [inc('m', I(1))]);
  await tf('inc.nan_operand', 'i (5) += NaN', [inc('i', D('NaN'))]);
  await tf('inc.on_nan', 'nan += 1', [inc('nan', I(1))]);
  await tf('inc.infinity_operand', 'd (2.5) += Infinity', [inc('d', D('Infinity'))]);
  await tf('inc.inf_plus_neg_inf', 'inf (Infinity) += -Infinity', [inc('inf', D('-Infinity'))]);
  await tf('inc.zero_plus_negzero', 'z (0.0) += -0.0', [inc('z', D(-0))]);
  await tf('inc.negzero_plus_negzero', 'nz (-0.0) += -0.0', [inc('nz', D(-0))], { fields: FIXNZ });
  await tf('inc.negzero_plus_int_zero', 'nz (-0.0) += 0 (integer)', [inc('nz', I(0))], { fields: FIXNZ });
  await tf('inc.int_zero_plus_negzero', 'i0 (0) += -0.0', [inc('i0', D(-0))]);
  await tf('inc.missing_negzero', 'q (absent) += -0.0', [inc('q', D(-0))]);
  await tf('inc.nested', 'm.k (1) += 1', [inc('m.k', I(1))]);
  await tf('inc.nested_new_map', 'mm.x (mm absent) += 1', [inc('mm.x', I(1))]);
  await tf('inc.through_string', 's.x += 1 (s is a string)', [inc('s.x', I(1))]);
  await tf('inc.operand_string', 'i += "1" (a string operand)', [inc('i', S('1'))]);
  await tf('inc.operand_null', 'i += null', [inc('i', NULL)]);
  await tf('inc.operand_array', 'i += [1]', [inc('i', A(I(1)))]);
  await tf('inc.operand_no_type', 'i += {} (a Value with no type)', [{ fieldPath: 'i', increment: {} }]);

  // maximum and minimum
  await tf('max.int_vs_bigger_double', 'max(i 5, 7.5)', [max('i', D(7.5))]);
  await tf('max.int_vs_smaller_double', 'max(i 5, 2.5)', [max('i', D(2.5))]);
  await tf('max.int_vs_equal_double', 'max(i 5, 5.0)', [max('i', D(5))]);
  await tf('min.int_vs_equal_double', 'min(i 5, 5.0)', [min('i', D(5))]);
  await tf('max.double_vs_equal_int', 'max(dw 5.0, 5)', [max('dw', I(5))]);
  await tf('min.double_vs_equal_int', 'min(dw 5.0, 5)', [min('dw', I(5))]);
  await tf('min.int_vs_smaller_double', 'min(i 5, 2.5)', [min('i', D(2.5))]);
  await tf('max.int_vs_bigger_int', 'max(i 5, 9)', [max('i', I(9))]);
  await tf('max.nan_operand', 'max(i 5, NaN)', [max('i', D('NaN'))]);
  await tf('min.nan_operand', 'min(i 5, NaN)', [min('i', D('NaN'))]);
  await tf('max.on_nan', 'max(nan, 1)', [max('nan', I(1))]);
  await tf('min.on_nan', 'min(nan, 1)', [min('nan', I(1))]);
  await tf('max.zero_vs_negzero', 'max(z 0.0, -0.0)', [max('z', D(-0))]);
  await tf('min.zero_vs_negzero', 'min(z 0.0, -0.0)', [min('z', D(-0))]);
  await tf('max.negzero_vs_zero', 'max(nz -0.0, 0.0)', [max('nz', D(0))], { fields: FIXNZ });
  await tf('min.negzero_vs_zero', 'min(nz -0.0, 0.0)', [min('nz', D(0))], { fields: FIXNZ });
  await tf('max.int_zero_vs_negzero', 'max(i0 0, -0.0)', [max('i0', D(-0))]);
  await tf('min.int_zero_vs_negzero', 'min(i0 0, -0.0)', [min('i0', D(-0))]);
  await tf('max.missing', 'max(q absent, 3)', [max('q', I(3))]);
  await tf('min.missing', 'min(q absent, -1.5)', [min('q', D(-1.5))]);
  await tf('max.on_string', 'max(s "str", 3)', [max('s', I(3))]);
  await tf('min.on_null', 'min(n null, 3)', [min('n', I(3))]);
  await tf('max.int64_max_vs_double', 'max(imax, 9223372036854775807.0 as a double)', [max('imax', D(9223372036854775807))]);
  await tf('min.int64_max_vs_double', 'min(imax, 9223372036854775807.0 as a double)', [min('imax', D(9223372036854775807))]);
  await tf('max.operand_string', 'max(i, "9")', [max('i', S('9'))]);

  // appendMissingElements and removeAllFromArray
  await tf('arr.append_basic', 'append [2, "x"] to a [1, "x", {m: 1}]', [app('a', I(2), S('x'))]);
  await tf('arr.append_duplicate_operand', 'append [3, 3] to a', [app('a', I(3), I(3))]);
  await tf('arr.append_to_existing_duplicates', 'append [1] to a2 [1, 1, 2]', [app('a2', I(1))]);
  await tf('arr.append_double_equal_to_int', 'append [1.0] to a (holds integer 1)', [app('a', D(1))]);
  await tf('arr.append_nan', 'append [NaN] to an [NaN, 1]', [app('an', D('NaN'))]);
  await tf('arr.append_map_equal', 'append [{m: 1}] to a (holds {m: 1})', [app('a', M({ m: I(1) }))]);
  await tf('arr.append_map_double_equal', 'append [{m: 1.0}] to a (holds {m: 1})', [app('a', M({ m: D(1) }))]);
  await tf('arr.append_map_key_order', 'append [{b: 2, a: 1}] to am [{a: 1, b: 2}]', [app('am', M({ b: I(2), a: I(1) }))]);
  await tf('arr.append_negzero', 'append [-0.0] to az [0.0]', [app('az', D(-0))]);
  await tf('arr.append_on_string', 'append [1] to s (a string)', [app('s', I(1))]);
  await tf('arr.append_on_map', 'append [1] to m (a map)', [app('m', I(1))]);
  await tf('arr.append_missing', 'append [1, 1] to q (absent)', [app('q', I(1), I(1))]);
  await tf('arr.append_empty', 'appendMissingElements {} (no values) on a', [{ fieldPath: 'a', appendMissingElements: {} }]);
  await tf('arr.append_nested_array', 'append [[1]] to a (an array in an array)', [app('a', A(I(1)))]);
  await tf('arr.remove_basic', 'remove [1, "y"] from a', [rem('a', I(1), S('y'))]);
  await tf('arr.remove_all_occurrences', 'remove [1] from a2 [1, 1, 2]', [rem('a2', I(1))]);
  await tf('arr.remove_double_equal_to_int', 'remove [1.0] from a (holds integer 1)', [rem('a', D(1))]);
  await tf('arr.remove_nan', 'remove [NaN] from an [NaN, 1]', [rem('an', D('NaN'))]);
  await tf('arr.remove_map', 'remove [{m: 1}] from a', [rem('a', M({ m: I(1) }))]);
  await tf('arr.remove_map_key_order', 'remove [{b: 2, a: 1}] from am [{a: 1, b: 2}]', [rem('am', M({ b: I(2), a: I(1) }))]);
  await tf('arr.remove_negzero', 'remove [-0.0] from az [0.0]', [rem('az', D(-0))]);
  await tf('arr.remove_on_string', 'remove [1] from s (a string)', [rem('s', I(1))]);
  await tf('arr.remove_missing', 'remove [1] from q (absent)', [rem('q', I(1))]);
  await tf('arr.remove_empty', 'removeAllFromArray {} (no values) on a', [{ fieldPath: 'a', removeAllFromArray: {} }]);

  // several transforms, paths, kinds
  await tf('multi.same_field_twice', 'i += 1 and i += 2 in one write', [inc('i', I(1)), inc('i', I(2))]);
  await tf('multi.same_field_two_kinds', 'i += 1 and max(i, 9) in one write', [inc('i', I(1)), max('i', I(9))]);
  await tf('multi.parent_then_child', 'm = REQUEST_TIME and m.k += 1', [st('m'), inc('m.k', I(1))]);
  await tf('multi.child_then_parent', 'm.k += 1 and m = REQUEST_TIME', [inc('m.k', I(1)), st('m')]);
  await tf('multi.siblings', 'm.k += 1 and m.j += 1', [inc('m.k', I(1)), inc('m.j', I(1))]);
  await tf('multi.results_shape', 'ts = REQUEST_TIME, i += 1, max(d, 9), min(dw, 1), append [7] to a, remove [2] from a2: transformResults', [st('ts'), inc('i', I(1)), max('d', D(9)), min('dw', I(1)), app('a', I(7)), rem('a2', I(2))]);
  await tf('path.empty', 'fieldPath ""', [inc('', I(1))]);
  await tf('path.name', 'fieldPath __name__', [inc('__name__', I(1))]);
  await tf('path.reserved', 'fieldPath __x__', [inc('__x__', I(1))]);
  await tf('path.double_dot', 'fieldPath a..b', [inc('a..b', I(1))]);
  await tf('path.backticks', 'fieldPath `we ird` (quoted)', [inc('`we ird`', I(1))]);
  await tf('path.unquoted_space', 'fieldPath we ird (a space, unquoted)', [inc('we ird', I(1))]);
  await tf('kind.none', 'a field transform with no transform', [{ fieldPath: 'i' }]);
  await tf('kind.two', 'a field transform with increment and maximum', [{ fieldPath: 'i', increment: I(1), maximum: I(2) }]);
  await tf('kind.server_value_unspecified', 'setToServerValue SERVER_VALUE_UNSPECIFIED', [{ fieldPath: 'ts', setToServerValue: 'SERVER_VALUE_UNSPECIFIED' }]);

  // overlap with the update's fields and mask
  await tf('overlap.field_in_update_no_mask', 'update holds i: 5 (no mask) and i += 1', [inc('i', I(1))]);
  await tf('overlap.mask_names_field', 'updateMask [i] with i: 9, and i += 1', [inc('i', I(1))], { fields: { i: I(9) }, mask: ['i'] });
  await tf('overlap.mask_parent', 'updateMask [m] with m: {k: 1}, and m.k += 1', [inc('m.k', I(1))], { fields: { m: M({ k: I(1) }) }, mask: ['m'] });
  await tf('overlap.mask_child', 'updateMask [m.k] with m: {k: 1}, and m = REQUEST_TIME', [st('m')], { fields: { m: M({ k: I(1) }) }, mask: ['m.k'] });
  await tf('overlap.mask_other', 'updateMask [s] with s: "t", and i += 1 (on a fresh FIX document)', [inc('i', I(1))], { fields: { s: S('t') }, mask: ['s'], replay: false, keep: ['s'] });
  await tf('overlap.mask_absent_field', 'updateMask [zz] (deletes zz) and zz += 1', [inc('zz', I(1))], { fields: {}, mask: ['zz'], replay: false });

  // a document that does not exist
  const nx = (id) => docName(id);
  await cm('missing.mask_empty', 'update with empty fields, updateMask [] and i += 1, ts = REQUEST_TIME on a missing document', [{ update: { name: nx('missing.mask_empty'), fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [inc('i', I(1)), st('ts')] }], { name: nx('missing.mask_empty'), keep: ['i', 'ts'] });
  await cm('missing.exists_true', 'the same with currentDocument.exists=true', [{ update: { name: nx('missing.exists_true'), fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [inc('i', I(1))], currentDocument: { exists: true } }], { replay: true, name: nx('missing.exists_true'), keep: ['i'] });
  await cm('missing.exists_false', 'the same with currentDocument.exists=false', [{ update: { name: nx('missing.exists_false'), fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [inc('i', I(1))], currentDocument: { exists: false } }], { name: nx('missing.exists_false'), keep: ['i'] });
  await cm('missing.max_and_remove', 'max(q, 3) and remove [1] from r on a missing document (updateMask [])', [{ update: { name: nx('missing.max_and_remove'), fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [max('q', I(3)), rem('r', I(1))] }], { name: nx('missing.max_and_remove'), keep: ['q', 'r'] });
  await cm('legacy.transform_missing', 'a legacy transform write (i += 1) on a missing document', [{ transform: { document: nx('legacy.transform_missing'), fieldTransforms: [inc('i', I(1))] } }], { name: nx('legacy.transform_missing'), keep: ['i'] });
  await cm('legacy.transform_missing_exists_true', 'a legacy transform write on a missing document with currentDocument.exists=true', [{ transform: { document: nx('legacy.transform_missing_exists_true'), fieldTransforms: [inc('i', I(1))] }, currentDocument: { exists: true } }], { replay: true, name: nx('legacy.transform_missing_exists_true'), keep: ['i'] });

  // the legacy transform write
  const lg = nx('legacy.base');
  await cm('legacy.update_then_transform', 'update FIX then a legacy transform (i += 1, ts = REQUEST_TIME) of the same document in one commit', [{ update: { name: lg, fields: FIX } }, { transform: { document: lg, fieldTransforms: [inc('i', I(1)), st('ts')] } }], { name: lg, keep: ['i', 'ts'] });
  await cm('legacy.transform_existing', 'a legacy transform (i += 1) of an existing document', [{ transform: { document: lg, fieldTransforms: [inc('i', I(1))] } }], { name: lg, keep: ['i'] });
  await cm('legacy.transform_then_update', 'a legacy transform then an update of the same document', [{ transform: { document: lg, fieldTransforms: [inc('i', I(1))] } }, { update: { name: lg, fields: FIX } }], { name: lg, keep: ['i'] });
  await cm('legacy.with_update_transforms', 'a transform write that also has updateTransforms', [{ transform: { document: lg, fieldTransforms: [inc('i', I(1))] }, updateTransforms: [inc('d', I(1))] }], { replay: true });
  await cm('legacy.empty_field_transforms', 'a legacy transform with fieldTransforms []', [{ transform: { document: lg, fieldTransforms: [] } }], { name: lg, keep: ['i'] });
  await cm('legacy.same_field_twice', 'a legacy transform with i += 1 twice', [{ transform: { document: lg, fieldTransforms: [inc('i', I(1)), inc('i', I(1))] } }], { replay: true });
  await cm('delete.with_update_transforms', 'a delete write with updateTransforms', [{ delete: nx('legacy.base-x'), updateTransforms: [inc('i', I(1))] }], { replay: true });
  await cm('verify.with_update_transforms', 'a verify write with updateTransforms', [{ verify: lg, updateTransforms: [inc('i', I(1))] }], { replay: true });

  // the document size limit reached by a transform
  // Size: the name (each segment plus 1, plus 16), 32, and per field its name
  // plus 1 and its value (a string: its length plus 1; a timestamp: 8); the
  // formula f0-errors.json confirmed. Here 38 + 32 + (3 + L1 + 1) + (3 + L2 + 1).
  const LIMIT = 1_048_576;
  const sized = (id, extra) => {
    const name = nx(id); // ids of 5 characters: size<n>
    const total = LIMIT - 11 + extra; // ts (3 + 8) or ar ["xxxxxxx"] (3 + 8) adds 11
    const l1 = 1_000_000;
    const l2 = total - 78 - l1;
    return { name, fields: { s1: S('a'.repeat(l1)), s2: S('b'.repeat(l2)) } };
  };
  for (const [id, extra, ts] of [['size1', 0, [st('ts')]], ['size2', 1, [st('ts')]], ['size3', 0, [app('ar', S('xxxxxxx'))]], ['size4', 1, [app('ar', S('xxxxxxx'))]]]) {
    const { name, fields: f } = sized(id, extra);
    const d = `a document of ${LIMIT - 11 + extra} bytes plus ${ts[0].setToServerValue ? 'ts = REQUEST_TIME' : 'ar = append ["xxxxxxx"]'} (11 bytes): ${extra ? 'one byte over' : 'exactly'} the limit`;
    await cm(`size.${ts[0].setToServerValue ? 'request_time' : 'append'}_${extra ? 'over_by_one' : 'at_limit'}`, d, [{ update: { name, fields: f }, updateTransforms: ts }], { name, keep: ['ts', 'ar'] });
  }

  // BSON shapes, vectors and array elements (values-bson.json has the shapes)
  // Standard refuses a stored BSON int32 or Decimal128 (values-bson.json), so a
  // transform on such a field cannot be reached: bson.on_*_field show the refusal.
  const VEC = M({ __type__: S('__vector__'), value: A(D(1), D(2)) });
  const tv = (id, d, ts) => tf(id, d, ts, { fields: { ...FIX, v: VEC } });
  await tf('bson.on_int32_field', 'bi ({__int__: 7}) += 1, bi written by the same update', [inc('bi', I(1))], { fields: { ...FIX, bi: M({ __int__: I(7) }) } });
  await tf('bson.on_decimal_field', 'max(bd {__decimal128__: "1.5"}, 1), bd written by the same update', [max('bd', I(1))], { fields: { ...FIX, bd: M({ __decimal128__: S('1.5') }) } });
  await tf('bson.inc_int32_operand', 'i (5) += {__int__: 1}', [inc('i', M({ __int__: I(1) }))]);
  await tf('bson.inc_decimal_operand', 'i (5) += {__decimal128__: "1.5"}', [inc('i', M({ __decimal128__: S('1.5') }))]);
  await tf('bson.max_decimal_operand', 'max(i 5, {__decimal128__: "9.5"})', [max('i', M({ __decimal128__: S('9.5') }))]);
  await tf('bson.min_decimal_nan_operand', 'min(i 5, {__decimal128__: "NaN"})', [min('i', M({ __decimal128__: S('NaN') }))]);
  await tf('bson.append_int32', 'append [{__int__: 1}] to a (holds integer 1)', [app('a', M({ __int__: I(1) }))]);
  await tf('bson.append_decimal', 'append [{__decimal128__: "1"}] to a', [app('a', M({ __decimal128__: S('1') }))]);
  await tv('path.vector_value', 'v.value += 1 (v is a vector)', [inc('v.value', I(1))]);
  await tv('path.vector_type', 'v.__type__ = REQUEST_TIME', [st('v.__type__')]);
  await tf('path.bson_inner', 'bi.__int__ += 1', [inc('bi.__int__', I(1))]);
  await tv('path.inc_on_vector', 'v (a vector) += 1', [inc('v', I(1))]);
  await tv('path.append_on_vector', 'append [3.0] to v (a vector)', [app('v', D(3))]);
  await tv('path.max_on_vector', 'max(v (a vector), 1)', [max('v', I(1))]);
  await tf('path.array_element', 'a.0 += 1 (a is an array)', [inc('a.0', I(1))]);
  await tf('path.array_element_append', 'append [1] to a.0', [app('a.0', I(1))]);
  let deep = I(1);
  for (let i = 0; i < 20; i++) deep = M({ x: deep });
  await tf('arr.append_reserved_key_map', 'append [{__x__: 1}] to a', [app('a', M({ __x__: I(1) }))]);
  await tf('arr.append_too_deep', 'append a map nested 20 deep (inside the array: depth 21 from the document) to a', [app('a', deep)]);
  await tf('arr.append_vector_shape', 'append [a vector] to a', [app('a', M({ __type__: S('__vector__'), value: A(D(1)) }))]);

  // REQUEST_TIME and two writes; writes with nothing to do
  {
    const nm = docName('st.then_deleted');
    await cm('st.then_deleted', 'update with ts = REQUEST_TIME, then a delete of the same document, in one commit', [{ update: { name: nm, fields: { a: I(1) } }, updateTransforms: [st('ts')] }, { delete: nm }], { name: nm, keep: ['ts'] });
    const nm2 = docName('st.same_doc_two_writes');
    await cm('st.same_doc_two_writes', 'update {a: 1} with ts = REQUEST_TIME, then updateMask [] with ts2 = REQUEST_TIME, same document, one commit', [{ update: { name: nm2, fields: { a: I(1) } }, updateTransforms: [st('ts')] }, { update: { name: nm2, fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [st('ts2')] }], { name: nm2, keep: ['ts', 'ts2'] });
    const s2 = await rest('GET', nm2);
    if (s2.http === 200) out.at(-1).stored.tsEqualsTs2 = s2.body.fields.ts?.timestampValue === s2.body.fields.ts2?.timestampValue;
    // two commits sent at the same moment, each setting ts on the same document
    const nm3 = docName('st.two_commits_at_once');
    await commitWrites([{ update: { name: nm3, fields: { a: I(1) } } }]);
    const body = { writes: [{ update: { name: nm3, fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [st('ts')] }] };
    const [r1, r2] = await Promise.all([rest('POST', `${ROOT}:commit`, body), rest('POST', `${ROOT}:commit`, body)]);
    const tsOf = (r) => r.body.writeResults?.[0]?.transformResults?.[0]?.timestampValue;
    out.push({
      id: 'st.two_commits_at_once', description: 'two commits sent at the same moment, each with updateMask [] and ts = REQUEST_TIME on one document', transport: 'rest',
      request: { method: 'POST', path: relProject(`${ROOT}:commit`), body: JSON.parse(relProject(JSON.stringify(body))), sentTwice: 'concurrently' },
      responses: [r1, r2].map((r) => ({ http: r.http, body: maskTimestamps(normalise(r.body)) })),
      relations: r1.http === 200 && r2.http === 200 ? {
        sameRequestTime: tsOf(r1) === tsOf(r2),
        sameCommitTime: r1.body.commitTime === r2.body.commitTime,
        sameUpdateTime: r1.body.writeResults[0].updateTime === r2.body.writeResults[0].updateTime,
        requestTimeMillisApartAbs: Math.abs(Number((nanosOf(tsOf(r1)) - nanosOf(tsOf(r2))) / 1_000_000n)),
      } : null,
    });
    await pause();
  }
  await cm('write.no_operation', 'a write with only currentDocument (no update, delete, verify or transform)', [{ currentDocument: { exists: true } }], { replay: true });
  await cm('write.empty', 'a write {}', [{}], { replay: true });
  await cm('delete.with_update_mask', 'a delete write with updateMask [a]', [{ delete: docName('delete-mask'), updateMask: { fieldPaths: ['a'] } }], { replay: true });
  await cm('verify.with_update_mask', 'a verify write with updateMask [a]', [{ verify: lg, updateMask: { fieldPaths: ['a'] } }], { replay: true });
  await cm('legacy.transform_with_update_mask', 'a legacy transform write with updateMask [a]', [{ transform: { document: lg, fieldTransforms: [inc('i', I(1))] }, updateMask: { fieldPaths: ['a'] } }], { replay: true });

  if (g) {
    const gt = async (id, d, writes, keep = []) => {
      const name = writes[0].update?.name ?? writes[0].transform?.document;
      const { c, raw } = await grpcCase(id, d, 'commit', { database: DB, writes: toGrpc(writes) });
      if (raw.ok) {
        track(writes, 200);
        const wr = raw.response.writeResults || [];
        const ts = timestampsIn(wr);
        if (ts.length) c.requestTimeRelations = ts.map((t) => {
          const ct = raw.response.commitTime;
          return { equalsCommitTime: nanosOf(t) === nanosOf(ct), resultNanosMultipleOf1000000: Number(t.nanos || 0) % 1_000_000 === 0, commitNanosMultipleOf1000: Number(ct.nanos || 0) % 1000 === 0 };
        });
        if (keep.length) {
          const s = await stored(name, keep);
          c.stored = s.http === 200 ? { fields: maskTimestamps(s.fields), fieldNames: s.fieldNames } : s;
        }
      }
      c.response = maskTimestamps(c.response);
      out.push(c);
    };
    const w = (id, ts, o = {}) => [{ update: { name: docName(id), fields: o.fields ?? FIX }, ...(o.mask ? { updateMask: { fieldPaths: o.mask } } : {}), updateTransforms: ts }];
    await gt('grpc.st.basic', 'ts = REQUEST_TIME', w('grpc.st.basic', [st('ts')]), ['ts']);
    await gt('grpc.inc.int_int', 'i (5) += 3', w('grpc.inc.int_int', [inc('i', I(3))]), ['i']);
    await gt('grpc.inc.overflow_max', 'imax += 1', w('grpc.inc.overflow_max', [inc('imax', I(1))]), ['imax']);
    await gt('grpc.inc.nan_operand', 'i += NaN', w('grpc.inc.nan_operand', [inc('i', D('NaN'))]), ['i']);
    await gt('grpc.inc.negzero_plus_negzero', 'nz (-0.0) += -0.0', w('grpc.inc.negzero_plus_negzero', [inc('nz', D(-0))], { fields: FIXNZ }), ['nz']);
    await gt('grpc.max.zero_vs_negzero', 'max(z 0.0, -0.0)', w('grpc.max.zero_vs_negzero', [max('z', D(-0))]), ['z']);
    await gt('grpc.min.zero_vs_negzero', 'min(z 0.0, -0.0)', w('grpc.min.zero_vs_negzero', [min('z', D(-0))]), ['z']);
    await gt('grpc.arr.append_nan', 'append [NaN] to an', w('grpc.arr.append_nan', [app('an', D('NaN'))]), ['an']);
    await gt('grpc.multi.results_shape', 'the six transforms of multi.results_shape', w('grpc.multi.results_shape', [st('ts'), inc('i', I(1)), max('d', D(9)), min('dw', I(1)), app('a', I(7)), rem('a2', I(2))]), ['ts', 'i', 'd', 'dw', 'a', 'a2']);
    await gt('grpc.multi.same_field_twice', 'i += 1 twice', w('grpc.multi.same_field_twice', [inc('i', I(1)), inc('i', I(2))]));
    await gt('grpc.overlap.mask_names_field', 'updateMask [i] and i += 1', w('grpc.overlap.mask_names_field', [inc('i', I(1))], { fields: { i: I(9) }, mask: ['i'] }), ['i']);
    await gt('grpc.legacy.transform_missing', 'a legacy transform write on a missing document', [{ transform: { document: docName('grpc.legacy.transform_missing'), fieldTransforms: [inc('i', I(1))] } }], ['i']);
    await gt('grpc.inc.operand_string', 'i += "1"', w('grpc.inc.operand_string', [inc('i', S('1'))]));
  }
  return {
    cases: out,
    notes: [
      `Every tf case overwrites its own document ${T}/<case id with dots as dashes> with the fixture FIX (shown in each request) and applies the transforms in the same write; stored holds the transformed top-level fields as a GET returned them afterwards, and fieldNames all fields of the document.`,
      'timestampValue values (the REQUEST_TIME results, in transformResults and in stored fields) are replaced by their type and precision; requestTimeRelations compares each with the response commitTime.',
      '"<number -0>" stands for a JSON number -0 (REST) or a double -0.0 (gRPC), "<number NaN>" and "<number Infinity>" for gRPC doubles; REST sends NaN and the infinities as the strings "NaN", "Infinity" and "-Infinity". Where a request holds -0.0, bodyText is the exact text sent.',
      'size.*: the document size counts the name (each segment plus 1, plus 16), 32, and each field name plus 1 and its value (f0-errors.json confirmed the formula to the byte); 1,048,576 is the limit.',
    ],
  };
}

// ---- Commit ----

async function commits(setupRaw) {
  const out = [];
  const relations = [];
  const n = (r) => `${ROOT}/${C}/${r}`;
  const replace = new Map();
  const cm = async (id, d, writes, { replay = false, extra, keepStored, rest: restOpts } = {}) => {
    scopeWrites(writes);
    const { c, raw } = await restCase(id, d, 'POST', `${ROOT}:commit`, { writes, ...(extra || {}) }, { replay, replace, ...(restOpts || {}) });
    track(writes, raw.http);
    if (raw.http === 200) c.writeResultKeys = (raw.body.writeResults || []).map((r) => Object.keys(r).sort());
    out.push(c);
    if (keepStored) {
      const s = await stored(keepStored, ['a', 'b', 'c']);
      c.stored = s.http === 200 ? { fields: s.fields, fieldNames: s.fieldNames } : s;
    }
    return { c, raw };
  };
  const up = (r, f, o = {}) => ({ update: { name: n(r), fields: f }, ...o });

  // the no-op write
  const n1 = n('n1');
  const t1 = setupRaw.writeResults[0].updateTime;
  const c1 = setupRaw.commitTime;
  replace.set(t1, '<updateTime of the setup write of n1>');
  let prevCommit = c1;
  const relate = (id, raw, extra = {}) => {
    const r = { case: id };
    if (raw.http === 200 && !raw.body.commitTime) r.noCommitTime = true;
    else if (raw.http === 200) {
      r.commitTimeAfterPreviousCommit = nanosOf(raw.body.commitTime) > nanosOf(prevCommit);
      prevCommit = raw.body.commitTime;
      const wr = raw.body.writeResults || [];
      r.commitTimeAfterSetupCommit = nanosOf(raw.body.commitTime) > nanosOf(c1);
      r.writeResultUpdateTimes = wr.map((x) => (!x.updateTime ? 'absent' : x.updateTime === t1 ? 'the setup updateTime of n1' : x.updateTime === raw.body.commitTime ? 'this commitTime' : 'other'));
    }
    relations.push({ ...r, ...extra });
  };
  let r = await cm('noop.same_fields', 'commit n1 with the fields it already has ({a: 1})', [up('n1', { a: I(1) })]);
  relate('noop.same_fields', r.raw);
  let g1 = await rest('GET', n1);
  relations.at(-1).storedUpdateTimeIsSetup = g1.body.updateTime === t1;
  r = await cm('noop.max_unchanged', 'commit n1 with updateMask [] and max(a, 0): the value stays 1', [{ update: { name: n1, fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [max('a', I(0))] }]);
  relate('noop.max_unchanged', r.raw);
  g1 = await rest('GET', n1);
  relations.at(-1).storedUpdateTimeIsSetup = g1.body.updateTime === t1;
  r = await cm('noop.mask_absent_field', 'commit n1 with updateMask [zz] and no zz (deletes a field it does not have)', [{ update: { name: n1, fields: {} }, updateMask: { fieldPaths: ['zz'] } }]);
  relate('noop.mask_absent_field', r.raw);
  g1 = await rest('GET', n1);
  relations.at(-1).storedUpdateTimeIsSetup = g1.body.updateTime === t1;
  r = await cm('noop.same_fields_again', 'commit n1 with the fields it has, a second time in a row', [up('n1', { a: I(1) })]);
  relate('noop.same_fields_again', r.raw);
  g1 = await rest('GET', n1);
  relations.at(-1).storedUpdateTimeIsSetup = g1.body.updateTime === t1;
  r = await cm('noop.verify', 'commit a verify of n1 with exists=true', [{ verify: n1, currentDocument: { exists: true } }]);
  relate('noop.verify', r.raw);
  r = await cm('noop.verify_again', 'commit a verify of n1 with exists=true again', [{ verify: n1, currentDocument: { exists: true } }]);
  relate('noop.verify_again', r.raw);
  r = await cm('noop.delete_missing', 'commit a delete of a missing document', [{ delete: n('never') }], { replay: true });
  relate('noop.delete_missing', r.raw);
  r = await cm('noop.same_fields_with_change_elsewhere', 'n1 unchanged and n2 created in one commit', [up('n1', { a: I(1) }), up('n2', { a: I(2) })]);
  relate('noop.same_fields_with_change_elsewhere', r.raw);
  {
    const e1 = await restCase('noop.empty_1', 'commit with writes [] (the first of two in a row)', 'POST', `${ROOT}:commit`, { writes: [] }, { replay: true });
    const e2 = await restCase('noop.empty_2', 'commit with writes [] (the second)', 'POST', `${ROOT}:commit`, { writes: [] }, { replay: true });
    out.push(e1.c, e2.c);
    const ct1 = e1.raw.body.commitTime;
    const ct2 = e2.raw.body.commitTime;
    relations.push({ case: 'noop.empty_1, noop.empty_2', hasCommitTime: [Boolean(ct1), Boolean(ct2)], ...(ct1 && ct2 ? { secondCommitTimeAfterFirst: nanosOf(ct2) > nanosOf(ct1), firstAfterSetupCommit: nanosOf(ct1) > nanosOf(c1) } : {}) });
  }
  // a database that does not exist
  {
    const { c } = await restCase('missing_database.empty', 'commit with writes [] to databases/aruma-ref-nonexistent', 'POST', `projects/${PROJECT}/databases/aruma-ref-nonexistent/documents:commit`, { writes: [] });
    out.push(c);
    if (g) {
      const gm = await grpcCase('grpc.missing_database.empty', 'Commit with no writes to databases/aruma-ref-nonexistent', 'commit', { database: `projects/${PROJECT}/databases/aruma-ref-nonexistent` });
      out.push(gm.c);
    }
  }
  // the no-op write inside a read-write transaction
  {
    const b = await rest('POST', `${ROOT}:beginTransaction`, { options: { readWrite: {} } });
    const txn = b.body.transaction;
    openTxns.add(txn);
    replace.set(txn, '<a read-write transaction>');
    await rest('GET', `${n1}?transaction=${encodeURIComponent(txn)}`);
    r = await cm('noop.same_fields_in_transaction', 'in a read-write transaction that read n1: commit n1 with the fields it has', [up('n1', { a: I(1) })], { extra: { transaction: txn } });
    openTxns.delete(txn);
    relate('noop.same_fields_in_transaction', r.raw);
    g1 = await rest('GET', n1);
    relations.at(-1).storedUpdateTimeIsSetup = g1.body.updateTime === t1;
  }

  // an update mask on a missing document
  await cm('mask.missing_document', 'update of a missing document with {a: 1, b: 2} and updateMask [a]', [up('um1', { a: I(1), b: I(2) }, { updateMask: { fieldPaths: ['a'] } })], { replay: true, keepStored: n('um1') });
  await cm('mask.missing_document_nested', 'update of a missing document with {a: {x: 1}} and updateMask [a.x, b]', [up('um2', { a: M({ x: I(1) }) }, { updateMask: { fieldPaths: ['a.x', 'b'] } })], { replay: true, keepStored: n('um2') });
  await cm('mask.missing_document_empty', 'update of a missing document with {a: 1} and updateMask []', [up('um3', { a: I(1) }, { updateMask: { fieldPaths: [] } })], { keepStored: n('um3') });

  // preconditions
  await cm('pre.exists_true_on_missing', 'update of a missing document with currentDocument.exists=true', [up('missing1', { a: I(1) }, { currentDocument: { exists: true } })], { replay: true });
  await cm('pre.exists_false_on_existing', 'update of n1 with currentDocument.exists=false', [up('n1', { a: I(1) }, { currentDocument: { exists: false } })], { replay: true });
  let p = await cm('pre.update_time_stale', `update of n1 with currentDocument.updateTime=${STALE}`, [up('n1', { a: I(1) }, { currentDocument: { updateTime: STALE } })]);
  await pinVersions(p.c, n1, STALE);
  p = await cm('pre.update_time_on_missing', `update of a missing document with currentDocument.updateTime=${STALE}`, [up('missing2', { a: I(1) }, { currentDocument: { updateTime: STALE } })], { replay: true });
  await pinVersions(p.c, n('missing2'), STALE);
  const cur = (await rest('GET', n1)).body.updateTime;
  replace.set(cur, '<the current updateTime of n1>');
  await cm('pre.update_time_current', 'update of n1 with currentDocument.updateTime=<its current updateTime>', [up('n1', { a: I(1) }, { currentDocument: { updateTime: cur } })]);
  await cm('pre.both', 'update of n1 with currentDocument {exists: true, updateTime}', [up('n1', { a: I(1) }, { currentDocument: { exists: true, updateTime: STALE } })], { replay: true });
  await cm('pre.empty', 'update of n1 with currentDocument {}', [up('n1', { a: I(1) }, { currentDocument: {} })], { replay: true });
  await cm('pre.delete_exists_true_on_missing', 'delete of a missing document with currentDocument.exists=true', [{ delete: n('missing3'), currentDocument: { exists: true } }], { replay: true });
  await cm('pre.verify_exists_false_on_existing', 'verify n1 with currentDocument.exists=false', [{ verify: n1, currentDocument: { exists: false } }], { replay: true });
  p = await cm('pre.verify_update_time_stale', `verify n1 with currentDocument.updateTime=${STALE}`, [{ verify: n1, currentDocument: { updateTime: STALE } }]);
  await pinVersions(p.c, n1, STALE);
  await cm('pre.verify_missing_no_precondition', 'verify of a missing document with no currentDocument', [{ verify: n('missing4') }], { replay: true });
  await cm('pre.second_write_fails', 'update pw1 (no precondition) and an update of a missing document with exists=true: is pw1 written?', [up('pw1', { a: I(1) }), up('missing5', { a: I(1) }, { currentDocument: { exists: true } })], { replay: true, keepStored: n('pw1') });
  await cm('pre.two_failures', 'two failing preconditions: which is named?', [up('missing6', { a: I(1) }, { currentDocument: { exists: true } }), up('n1', { a: I(1) }, { currentDocument: { exists: false } })], { replay: true });
  await cm('pre.two_failures_reversed', 'the same two in the other order', [up('n1', { a: I(1) }, { currentDocument: { exists: false } }), up('missing6', { a: I(1) }, { currentDocument: { exists: true } })], { replay: true });

  // two writes to one document
  await cm('same.update_update', 'update s1 {a: 1} then update s1 {b: 2}', [up('s1', { a: I(1) }), up('s1', { b: I(2) })], { replay: true, keepStored: n('s1') });
  await cm('same.update_then_masked_update', 'update s2 {a: 1} then update s2 {b: 2} with updateMask [b]', [up('s2', { a: I(1) }), up('s2', { b: I(2) }, { updateMask: { fieldPaths: ['b'] } })], { replay: true, keepStored: n('s2') });
  await cm('same.update_delete', 'update s3 then delete s3', [up('s3', { a: I(1) }), { delete: n('s3') }], { replay: true, keepStored: n('s3') });
  await cm('same.delete_update', 'delete s4 then update s4', [{ delete: n('s4') }, up('s4', { a: I(1) })], { replay: true, keepStored: n('s4') });
  await cm('same.create_create', 'update s5 with exists=false twice (a missing document)', [up('s5', { a: I(1) }, { currentDocument: { exists: false } }), up('s5', { b: I(2) }, { currentDocument: { exists: false } })], { keepStored: n('s5') });
  await cm('same.update_then_verify_exists', 'update s6 (missing) then verify s6 with exists=true', [up('s6', { a: I(1) }), { verify: n('s6'), currentDocument: { exists: true } }], { keepStored: n('s6') });
  await cm('same.update_then_increment', 'update s7 {a: 1} then update s7 with updateMask [] and a += 1', [up('s7', { a: I(1) }), { update: { name: n('s7'), fields: {} }, updateMask: { fieldPaths: [] }, updateTransforms: [inc('a', I(1))] }], { replay: true, keepStored: n('s7') });

  // many writes in one commit
  {
    const writes = Array.from({ length: 501 }, (_, i) => up(`w${String(i).padStart(3, '0')}`, { i: I(i) }));
    const real = await commitWrites(writes);
    await pause();
    const c = {
      id: 'limit.501_writes', description: 'one commit of 501 updates', transport: 'rest',
      request: { method: 'POST', path: relProject(`${ROOT}:commit`), body: { writes: `<501 updates of ${C}/w000 .. w500, fields {i}>` } },
      response: { http: real.http, bodyIsArray: false, body: real.http === 200 ? { commitTime: '<commitTime: RFC 3339>', writeResults: `<${(real.body.writeResults || []).length} write results>` } : normalise(real.body) },
    };
    if (real.http !== 200) c.error = { http: real.http, status: real.body.error?.status, message: real.body.error?.message };
    out.push(c);
  }

  if (g) {
    const gc = async (id, d, writes, o = {}) => {
      const { c, raw } = await grpcCase(id, d, 'commit', { database: DB, writes: toGrpc(writes), ...(o.extra || {}) }, { replace });
      if (raw.ok) track(writes, 200);
      out.push(c);
      return c;
    };
    await gc('grpc.empty', 'Commit with no writes', []);
    await gc('grpc.noop.same_fields', 'Commit n1 with the fields it has', [up('n1', { a: I(1) })]);
    await gc('grpc.pre.exists_true_on_missing', 'update of a missing document with exists=true', [up('missing1', { a: I(1) }, { currentDocument: { exists: true } })]);
    await gc('grpc.pre.exists_false_on_existing', 'update of n1 with exists=false', [up('n1', { a: I(1) }, { currentDocument: { exists: false } })]);
    const pc = await gc('grpc.pre.update_time_stale', `update of n1 with updateTime=${STALE}`, [{ update: { name: n1, fields: toGrpc({ a: I(1) }) }, currentDocument: { updateTime: rfcToGrpc(STALE) } }]);
    await pinVersions(pc, n1, STALE);
    await gc('grpc.same.update_update', 'update s1 twice', [up('s1', { a: I(1) }), up('s1', { b: I(2) })]);
  }
  return {
    cases: out,
    relations,
    notes: [
      `${C}/n1 is written {a: 1} by the setup commit; relations compare each later commit with it (commitTimeAfterSetupCommit, the write results' updateTime, and storedUpdateTimeIsSetup: whether a GET afterwards still shows the setup updateTime).`,
      'stored is a GET of the named document after the case (fields a, b, c).',
      'limit.501_writes keeps only the shape of its request and response.',
    ],
  };
}

// ---- BatchWrite ----

async function batchWrites() {
  const out = [];
  const n = (r) => `${ROOT}/${B}/${r}`;
  const bw = async (id, d, writes, { labels, replay = false, keep } = {}) => {
    scopeWrites(writes);
    const { c, raw } = await restCase(id, d, 'POST', `${DB}/documents:batchWrite`, { writes, ...(labels ? { labels } : {}) }, { replay });
    if (raw.http === 200) {
      const st0 = raw.body.status || [];
      writes.forEach((w, i) => {
        if (!st0[i] || !st0[i].code) {
          const nm = w.update?.name ?? w.transform?.document;
          if (nm) written.add(nm);
        }
      });
      c.statusCodes = st0.map((s) => s.code ?? 0);
    }
    if (keep) {
      c.stored = {};
      for (const k of keep) {
        const s = await stored(n(k), ['a', 'b', 'i']);
        c.stored[k] = s.http === 200 ? { fields: s.fields, fieldNames: s.fieldNames } : s;
      }
    }
    out.push(c);
    return raw;
  };
  const up = (r, f, o = {}) => ({ update: { name: n(r), fields: f }, ...o });
  await bw('two_updates', 'two updates', [up('b1', { a: I(1) }), up('b2', { a: I(2) })], { replay: true });
  await bw('mixed', 'update m1; update of a missing document with exists=true; update m2; delete of a missing document with exists=true', [
    up('m1', { a: I(1) }), up('missing1', { a: I(1) }, { currentDocument: { exists: true } }), up('m2', { a: I(2) }),
    { delete: n('missing2'), currentDocument: { exists: true } },
  ], { replay: true, keep: ['m1', 'm2', 'missing1'] });
  await bw('verify', 'verify m1 with exists=true, and verify a missing document with exists=true', [{ verify: n('m1'), currentDocument: { exists: true } }, { verify: n('missing3'), currentDocument: { exists: true } }], { replay: true });
  await bw('stale_update_time', `update m1 with updateTime=${STALE}, and update m3`, [up('m1', { a: I(9) }, { currentDocument: { updateTime: STALE } }), up('m3', { a: I(3) })], { replay: true, keep: ['m1', 'm3'] });
  await bw('same_document_twice', 'update d1 {a: 1} then update d1 {b: 2}', [up('d1', { a: I(1) }), up('d1', { b: I(2) })], { replay: true, keep: ['d1'] });
  await bw('same_document_one_fails', 'update d2 {a: 1}, then update d2 with exists=false', [up('d2', { a: I(1) }), up('d2', { b: I(2) }, { currentDocument: { exists: false } })], { keep: ['d2'] });
  await bw('labels', 'two updates with labels {purpose: "aruma-ref"}', [up('l1', { a: I(1) }), up('l2', { a: I(2) })], { labels: { purpose: 'aruma-ref' }, replay: true });
  await bw('labels_bad_key', 'labels {"Bad Key!": "x"}', [up('l3', { a: I(1) })], { labels: { 'Bad Key!': 'x' }, replay: true });
  await bw('labels_long_value', 'labels with a 300-character value', [up('l4', { a: I(1) })], { labels: { k: 'v'.repeat(300) } });
  await bw('empty', 'writes []', [], { replay: true });
  await bw('transform', 'update t1 {i: 5} with i += 1 and max(j, 2)', [{ update: { name: n('t1'), fields: { i: I(5) } }, updateTransforms: [inc('i', I(1)), max('j', I(2))] }], { replay: true });
  await bw('legacy_transform', 'a legacy transform write (i += 1) of t1', [{ transform: { document: n('t1'), fieldTransforms: [inc('i', I(1))] } }]);
  await bw('invalid_value', 'an update with a field named __x__ (refused) and a valid update', [up('v1', { __x__: I(1) }), up('v2', { a: I(1) })], { replay: true, keep: ['v2'] });
  await bw('invalid_transform', 'an update with i += "1" and a valid update', [{ update: { name: n('v3'), fields: { i: I(1) } }, updateTransforms: [inc('i', S('1'))] }, up('v4', { a: I(1) })], { replay: true });
  await bw('collection_name', 'an update whose name is a collection path, and a valid update', [{ update: { name: `${ROOT}/${B}`, fields: { a: I(1) } } }, up('v5', { a: I(1) })], { replay: true });
  {
    const body = { writes: [up('v6', { a: I(1) })], transaction: Buffer.alloc(16, 7).toString('base64') };
    const { c } = await restCase('with_transaction', 'a batchWrite body with a transaction field (not in BatchWriteRequest)', 'POST', `${DB}/documents:batchWrite`, body, { replace: new Map([[body.transaction, body.transaction]]), replay: true });
    out.push(c);
  }
  {
    const { c } = await restCase('missing_database', 'batchWrite one update to databases/aruma-ref-nonexistent', 'POST', `projects/${PROJECT}/databases/aruma-ref-nonexistent/documents:batchWrite`, { writes: [{ update: { name: `projects/${PROJECT}/databases/aruma-ref-nonexistent/documents/${B}/x`, fields: { a: I(1) } } }] });
    out.push(c);
  }
  for (const count of [500, 501]) {
    const writes = Array.from({ length: count }, (_, i) => up(`n${count}-${String(i).padStart(3, '0')}`, { i: I(i) }));
    scopeWrites(writes);
    const r = await rest('POST', `${DB}/documents:batchWrite`, { writes });
    await pause();
    if (r.http === 200) writes.forEach((w, i) => { if (!(r.body.status?.[i]?.code)) written.add(w.update.name); });
    const c = {
      id: `limit.${count}_writes`, description: `one batchWrite of ${count} updates`, transport: 'rest',
      request: { method: 'POST', path: relProject(`${DB}/documents:batchWrite`), body: { writes: `<${count} updates of ${B}/n${count}-000 .., fields {i}>` } },
      response: { http: r.http, body: r.http === 200 ? { writeResults: `<${(r.body.writeResults || []).length} write results>`, statusCodes: [...new Set((r.body.status || []).map((x) => x.code ?? 0))] } : normalise(r.body) },
    };
    if (r.http !== 200) c.error = { http: r.http, status: r.body.error?.status, message: r.body.error?.message };
    out.push(c);
  }
  if (g) {
    const gb = async (id, d, writes, labels) => {
      const { c, raw } = await grpcCase(id, d, 'batchWrite', { database: DB, writes: toGrpc(writes), ...(labels ? { labels } : {}) });
      if (raw.ok) (raw.response.status || []).forEach((s, i) => { if (!s.code) { const nm = writes[i].update?.name; if (nm) written.add(nm); } });
      out.push(c);
    };
    await gb('grpc.mixed', 'update m1; update of a missing document with exists=true; update m2', [up('m1', { a: I(1) }), up('missing1', { a: I(1) }, { currentDocument: { exists: true } }), up('m2', { a: I(2) })]);
    await gb('grpc.same_document_twice', 'update d1 twice', [up('d1', { a: I(1) }), up('d1', { b: I(2) })]);
    await gb('grpc.labels', 'labels {purpose: "aruma-ref"}', [up('l1', { a: I(1) })], { purpose: 'aruma-ref' });
    await gb('grpc.empty', 'no writes', []);
    const gm = await grpcCase('grpc.missing_database', 'BatchWrite one update to databases/aruma-ref-nonexistent', 'batchWrite', { database: `projects/${PROJECT}/databases/aruma-ref-nonexistent`, writes: toGrpc([{ update: { name: `projects/${PROJECT}/databases/aruma-ref-nonexistent/documents/${B}/x`, fields: { a: I(1) } } }]) });
    out.push(gm.c);
  }
  return {
    cases: out,
    notes: ['statusCodes lists each write\'s status code (0 when the status is empty, i.e. OK). stored holds GETs of the named documents after the case.'],
  };
}

// ---- transactions ----

// forge flips the last byte of a transaction id (base64).
const forge = (txn) => {
  const b = Buffer.from(txn, 'base64');
  b[b.length - 1] ^= 0x01;
  return b.toString('base64');
};
const FIXED_IDS = {
  random16: Buffer.from('00112233445566778899aabbccddeeff', 'hex').toString('base64'),
  one_byte: Buffer.from([7]).toString('base64'),
  long_1000: Buffer.alloc(1000, 0x5a).toString('base64'),
};

async function transactions(setupRaw) {
  const out = [];
  const n = (r) => `${ROOT}/${X}/${r}`;
  const at = setupRaw.commitTime;
  const replace = new Map([[at, '<readTime: the setup commitTime>'], ...Object.values(FIXED_IDS).map((x) => [x, x])]);
  let count = 0;
  const label = (txn, what) => {
    if (!replace.has(txn)) replace.set(txn, `<${what} #${++count}>`);
    return txn;
  };
  const rc = async (id, d, method, path, body, o = {}) => {
    const { c, raw } = await restCase(id, d, method, path, body, { replace, ...o });
    out.push(c);
    return raw;
  };
  const begin = async (id, options, what) => {
    const raw = await rc(id, options === undefined ? 'beginTransaction {} (no options)' : `beginTransaction {options: ${JSON.stringify(options)}}`, 'POST', `${ROOT}:beginTransaction`, options === undefined ? {} : { options });
    const t = raw.body.transaction;
    if (t) {
      label(t, what);
      if (!options || options.readWrite) openTxns.add(t);
      out.at(-1).response.body = normalise(raw.body, replace);
      out.at(-1).idBytes = Buffer.from(t, 'base64').length;
    }
    return t;
  };
  const rollback = (id, d, txn) => rc(id, d, 'POST', `${ROOT}:rollback`, { transaction: txn }).then((r) => { if (r.http === 200) openTxns.delete(txn); return r; });
  const commit = (id, d, txn, writes) => {
    scopeWrites(writes);
    return rc(id, d, 'POST', `${ROOT}:commit`, { writes, ...(txn !== undefined ? { transaction: txn } : {}) }).then((r) => {
      track(writes, r.http);
      if (r.http === 200 || r.http === 409) openTxns.delete(txn);
      return r;
    });
  };
  const get = (id, d, doc, txn) => rc(id, d, 'GET', `${n(doc)}?transaction=${encodeURIComponent(txn)}`);
  const up = (r, f) => ({ update: { name: n(r), fields: f } });

  // BeginTransaction
  const rw = await begin('begin.read_write', { readWrite: {} }, 'read-write transaction');
  const ro = await begin('begin.read_only', { readOnly: {} }, 'read-only transaction');
  const dflt = await begin('begin.no_options', undefined, 'transaction begun with no options');
  const roAt = await begin('begin.read_only_read_time', { readOnly: { readTime: at } }, 'read-only transaction at the setup commitTime');
  const t2h = minuteAgo(120);
  replace.set(t2h, '<readTime: two hours ago, a whole minute>');
  await begin('begin.read_only_read_time_2h', { readOnly: { readTime: t2h } }, 'read-only transaction two hours ago');
  const t8d = minuteAgo(8 * 24 * 60);
  replace.set(t8d, '<readTime: eight days ago, a whole minute>');
  await begin('begin.read_only_read_time_8d', { readOnly: { readTime: t8d } }, 'read-only transaction eight days ago');
  const tFut = new Date(Date.now() + 3600_000).toISOString();
  replace.set(tFut, '<readTime: an hour in the future>');
  await begin('begin.read_only_read_time_future', { readOnly: { readTime: tFut } }, 'read-only transaction in the future');
  const t2hs = minuteAgo(120, 30);
  replace.set(t2hs, '<readTime: two hours ago, 30 s past the minute>');
  await begin('begin.read_only_read_time_2h_seconds', { readOnly: { readTime: t2hs } }, 'read-only transaction two hours ago plus 30 s');
  await begin('begin.read_write_read_time', { readWrite: { readTime: at } }, 'read-write transaction with readTime');
  // retryTransaction
  await get('retry.read_before', 'GET a in the read-write transaction (to hold a lock before the retry)', 'a', rw);
  const rw2 = await begin('begin.retry_of_active', { readWrite: { retryTransaction: rw } }, 'read-write transaction retrying the first');
  if (rw2) {
    out.at(-1).sameIdAsRetried = rw2 === rw;
    await get('retry.old_id_read', 'GET a with the retried (old) transaction id', 'a', rw);
    await get('retry.new_id_read', 'GET a with the new transaction id', 'a', rw2);
    await commit('retry.old_id_commit', 'commit with the retried (old) transaction id', rw, [up('r1', { a: I(1) })]);
    await commit('retry.new_id_commit', 'commit with the new id', rw2, [up('r2', { a: I(1) })]);
  }
  await begin('begin.retry_unknown', { readWrite: { retryTransaction: FIXED_IDS.random16 } }, 'read-write transaction retrying an unknown id');
  await begin('begin.retry_of_read_only', { readWrite: { retryTransaction: ro } }, 'read-write transaction retrying a read-only one');
  await begin('begin.retry_not_base64', { readWrite: { retryTransaction: '%%%' } }, 'read-write transaction retrying "%%%"');
  await begin('begin.read_only_with_retry', { readOnly: { retryTransaction: rw } }, 'readOnly with retryTransaction');

  // reads in a transaction
  const q = { structuredQuery: { from: [{ collectionId: X }], orderBy: [{ field: { fieldPath: '__name__' } }] } };
  const rwq = await begin('read.begin_read_write', { readWrite: {} }, 'read-write transaction for reads');
  await rc('read.run_query_read_write', 'runQuery in a read-write transaction', 'POST', `${ROOT}:runQuery`, { ...q, transaction: rwq });
  await rc('read.run_query_read_only', 'runQuery in a read-only transaction', 'POST', `${ROOT}:runQuery`, { ...q, transaction: ro });
  await rc('read.run_query_read_only_read_time', 'runQuery in the read-only transaction at the setup commitTime', 'POST', `${ROOT}:runQuery`, { ...q, transaction: roAt });
  await rc('read.list_read_write', 'ListDocuments in a read-write transaction', 'GET', `${ROOT}/${X}?transaction=${encodeURIComponent(rwq)}`);
  await rc('read.list_read_only_read_time', 'ListDocuments in the read-only transaction at the setup commitTime', 'GET', `${ROOT}/${X}?transaction=${encodeURIComponent(roAt)}`);
  await rc('read.aggregation_read_write', 'runAggregationQuery count in a read-write transaction', 'POST', `${ROOT}:runAggregationQuery`, { structuredAggregationQuery: { structuredQuery: { from: [{ collectionId: X }] }, aggregations: [{ alias: 'n', count: {} }] }, transaction: rwq });
  await rc('read.batch_get_read_write', 'batchGet a, missing in a read-write transaction', 'POST', `${ROOT}:batchGet`, { documents: [n('a'), n('nope')], transaction: rwq });
  await get('read.get_read_write', 'GET a in a read-write transaction', 'a', rwq);
  await commit('read.commit_read_write_empty', 'commit with no writes in the read-write transaction that read', rwq, []);
  await get('read.get_after_commit', 'GET a with that transaction after its commit', 'a', rwq);
  await get('read.get_no_options', 'GET a in the transaction begun with no options', 'a', dflt);
  await commit('read.commit_no_options_write', 'commit a write in the transaction begun with no options (read-write?)', dflt, [up('dflt', { a: I(1) })]);
  const nt = async (id, d, path, body, what) => {
    const raw = await rc(id, d, 'POST', path, body);
    const arr = Array.isArray(raw.body) ? raw.body : [raw.body];
    const t = arr.find((x) => x && x.transaction)?.transaction;
    if (t) {
      label(t, what);
      out.at(-1).response.body = normalise(raw.body, replace);
      if (body.newTransaction.readWrite) openTxns.add(t);
    }
    out.at(-1).transactionIn = arr.map((x, i) => ({ index: i, keys: Object.keys(x || {}).sort() }));
    return t;
  };
  const ntq = await nt('read.run_query_new_transaction_read_write', 'runQuery with newTransaction {readWrite: {}}', `${ROOT}:runQuery`, { ...q, newTransaction: { readWrite: {} } }, 'read-write transaction from runQuery');
  await nt('read.run_query_new_transaction_read_only', 'runQuery with newTransaction {readOnly: {}}', `${ROOT}:runQuery`, { ...q, newTransaction: { readOnly: {} } }, 'read-only transaction from runQuery');
  await nt('read.run_query_new_transaction_no_results', 'runQuery matching nothing with newTransaction {readOnly: {}}', `${ROOT}:runQuery`, { structuredQuery: { from: [{ collectionId: `${X}-none` }] }, newTransaction: { readOnly: {} } }, 'read-only transaction from an empty runQuery');
  const nta = await nt('read.aggregation_new_transaction_read_write', 'runAggregationQuery with newTransaction {readWrite: {}}', `${ROOT}:runAggregationQuery`, { structuredAggregationQuery: { structuredQuery: { from: [{ collectionId: X }] }, aggregations: [{ alias: 'n', count: {} }] }, newTransaction: { readWrite: {} } }, 'read-write transaction from runAggregationQuery');
  const ntb = await nt('read.batch_get_new_transaction_read_write', 'batchGet b with newTransaction {readWrite: {}}', `${ROOT}:batchGet`, { documents: [n('b')], newTransaction: { readWrite: {} } }, 'read-write transaction from batchGet');
  if (nta) await rollback('read.rollback_aggregation_transaction', 'rollback of the transaction runAggregationQuery began', nta);
  if (ntq) await commit('read.commit_run_query_transaction', 'commit a write with the transaction runQuery began', ntq, [up('q1', { a: I(1) })]);
  if (ntb) await commit('read.commit_batch_get_transaction', 'commit a write of b with the transaction batchGet began', ntb, [up('b', { v: I(20) })]);

  // writes in a read-only transaction
  await commit('ro.commit_write', 'commit a write in a read-only transaction', ro, [up('ro1', { a: I(1) })]);
  await commit('ro.commit_empty_after_refused_write', 'commit with no writes in that read-only transaction, after its refused write', ro, []);
  const ro2 = await begin('ro.begin_fresh', { readOnly: {} }, 'fresh read-only transaction');
  await get('ro.fresh_read', 'GET a in it', 'a', ro2);
  await commit('ro.commit_empty', 'commit with no writes in a fresh read-only transaction', ro2, []);
  await get('ro.read_after_commit', 'GET a with it after that commit', 'a', ro2);
  await commit('ro.commit_write_read_time', 'commit a write in the read-only transaction at the setup commitTime', roAt, [up('ro2', { a: I(1) })]);
  await rollback('ro.rollback_after_commit', 'rollback of the read-only transaction after its commit', ro);

  // Rollback
  const rb = await begin('rollback.begin', { readWrite: {} }, 'read-write transaction to roll back');
  await get('rollback.read', 'GET a in it', 'a', rb);
  await rollback('rollback.first', 'rollback', rb);
  await rollback('rollback.second', 'rollback of the same transaction again', rb);
  await get('rollback.read_after', 'GET a with the rolled-back transaction', 'a', rb);
  await commit('rollback.commit_after', 'commit with the rolled-back transaction', rb, [up('rb1', { a: I(1) })]);
  await rollback('rollback.unknown_random16', 'rollback of 16 bytes that were never a transaction', FIXED_IDS.random16);
  await rollback('rollback.unknown_one_byte', 'rollback of a one-byte id', FIXED_IDS.one_byte);
  await rollback('rollback.unknown_1000_bytes', 'rollback of a 1000-byte id', FIXED_IDS.long_1000);
  await rc('rollback.empty_id', 'rollback with transaction ""', 'POST', `${ROOT}:rollback`, { transaction: '' });
  await rc('rollback.no_field', 'rollback with {}', 'POST', `${ROOT}:rollback`, {});
  await rc('rollback.not_base64', 'rollback with transaction "%%%"', 'POST', `${ROOT}:rollback`, { transaction: '%%%' });
  const live = await begin('forged.begin', { readWrite: {} }, 'read-write transaction to forge from');
  const forged = forge(live);
  replace.set(forged, '<the read-write transaction to forge from, last byte flipped>');
  await rollback('forged.rollback', 'rollback of the forged id', forged);
  await commit('forged.commit', 'commit with the forged id', forged, [up('f1', { a: I(1) })]);
  await get('forged.get', 'GET a with the forged id', 'a', forged);
  await commit('forged.commit_live', 'commit with the real id after the forged ones', live, [up('f2', { a: I(1) })]);
  await commit('unknown.commit_random16', 'commit with 16 bytes that were never a transaction', FIXED_IDS.random16, [up('u1', { a: I(1) })]);
  await commit('unknown.commit_one_byte', 'commit with a one-byte id', FIXED_IDS.one_byte, [up('u2', { a: I(1) })]);
  await commit('unknown.commit_empty_string', 'commit with transaction "" (no transaction)', '', [up('u3', { a: I(1) })]);

  // REQUEST_TIME inside a transaction
  {
    const tt = await begin('time.begin', { readWrite: {} }, 'read-write transaction with a REQUEST_TIME write');
    await get('time.read', 'GET a in it', 'a', tt);
    const raw = await commit('time.commit', 'commit ts = REQUEST_TIME in the transaction', tt, [{ update: { name: n('ts1'), fields: {} }, updateTransforms: [st('ts')] }]);
    const c = out.at(-1);
    const ts = timestampsIn(raw.body.writeResults || []);
    if (ts.length) c.requestTimeRelations = ts.map((t) => timeRelation(t, raw.body.commitTime));
    c.response.body = maskTimestamps(c.response.body);
  }

  if (g) {
    const gc = async (id, d, rpc, req, o = {}) => {
      const { c, raw } = await grpcCase(id, d, rpc, req, { replace, ...o });
      out.push(c);
      return raw;
    };
    const gb = async (id, options, what) => {
      const raw = await gc(id, `BeginTransaction ${JSON.stringify(options)}`, 'beginTransaction', { database: DB, ...(options ? { options } : {}) });
      if (raw.ok) {
        const t = raw.response.transaction.toString('base64');
        label(t, what);
        if (!options || options.readWrite) openTxns.add(t);
        out.at(-1).response.message = normalise(raw.response, replace);
        out.at(-1).idBytes = raw.response.transaction.length;
        return raw.response.transaction;
      }
      return null;
    };
    const grw = await gb('grpc.begin.read_write', { readWrite: {} }, 'gRPC read-write transaction');
    const gro = await gb('grpc.begin.read_only', { readOnly: {} }, 'gRPC read-only transaction');
    await gb('grpc.begin.read_only_read_time', { readOnly: { readTime: rfcToGrpc(at) } }, 'gRPC read-only transaction at the setup commitTime');
    await gb('grpc.begin.read_only_read_time_future', { readOnly: { readTime: rfcToGrpc(new Date(Date.now() + 3600_000).toISOString()) } }, 'gRPC read-only transaction in the future');
    await gb('grpc.begin.retry_unknown', { readWrite: { retryTransaction: Buffer.from(FIXED_IDS.random16, 'base64') } }, 'gRPC retry of an unknown id');
    await gb('grpc.begin.retry_empty', { readWrite: { retryTransaction: Buffer.alloc(0) } }, 'gRPC retry of an empty id');
    await gc('grpc.commit.read_only_write', 'Commit a write in a read-only transaction', 'commit', { database: DB, writes: toGrpc([up('gro1', { a: I(1) })]), transaction: gro });
    await gc('grpc.rollback.first', 'Rollback the read-write transaction', 'rollback', { database: DB, transaction: grw });
    openTxns.delete(grw.toString('base64'));
    await gc('grpc.rollback.second', 'Rollback it again', 'rollback', { database: DB, transaction: grw });
    await gc('grpc.rollback.unknown', 'Rollback 16 bytes that were never a transaction', 'rollback', { database: DB, transaction: Buffer.from(FIXED_IDS.random16, 'base64') });
    await gc('grpc.rollback.empty', 'Rollback an empty id', 'rollback', { database: DB, transaction: Buffer.alloc(0) });
    const g2 = await gb('grpc.forged.begin', { readWrite: {} }, 'gRPC read-write transaction to forge from');
    const gf = Buffer.from(forge(g2.toString('base64')), 'base64');
    replace.set(gf.toString('base64'), '<the gRPC read-write transaction to forge from, last byte flipped>');
    await gc('grpc.forged.commit', 'Commit with the forged id', 'commit', { database: DB, writes: toGrpc([up('gf1', { a: I(1) })]), transaction: gf });
    await gc('grpc.forged.rollback_real', 'Rollback the real id', 'rollback', { database: DB, transaction: g2 });
    openTxns.delete(g2.toString('base64'));
    const sq = await gc('grpc.read.run_query_new_transaction', 'RunQuery with newTransaction {readWrite: {}}', 'runQuery', { parent: ROOT, structuredQuery: q.structuredQuery, newTransaction: { readWrite: {} } }, { stream: true });
    const t = (sq.responses || []).find((x) => x.transaction && x.transaction.length)?.transaction;
    if (t) {
      label(t.toString('base64'), 'gRPC read-write transaction from RunQuery');
      out.at(-1).response.responses = normalise(sq.responses, replace);
      out.at(-1).transactionIn = sq.responses.map((x, i) => ({ index: i, hasTransaction: Boolean(x.transaction && x.transaction.length), hasDocument: Boolean(x.document) }));
      await gc('grpc.read.rollback_run_query_transaction', 'Rollback the transaction RunQuery began', 'rollback', { database: DB, transaction: t });
    }
  }
  return {
    cases: out,
    notes: [
      'Transaction ids are replaced by numbered placeholders naming how each was made (<read-write transaction #1> and so on); the fixed made-up ids (16 bytes 00112233…ff, one byte 0x07, 1000 bytes 0x5a) stay as sent. idBytes is the length of a returned id.',
      `${X}/a, b and c are written by the setup commit.`,
    ],
  };
}

// expiry probes how long a transaction lives: idle and kept busy for 300 s.
async function expiry() {
  const n = (r) => `${ROOT}/${E}/${r}`;
  await setup({ [`${E}/e1`]: { v: I(1) }, [`${E}/e2`]: { v: I(2) }, [`${E}/e3`]: { v: I(3) }, [`${E}/e5`]: { v: I(5) }, [`${E}/e6`]: { v: I(6) } });
  const out = [];
  const replace = new Map();
  const probe = async (key, kind, body) => {
    const started = Date.now();
    const events = [];
    const b = await rest('POST', `${ROOT}:beginTransaction`, { options: kind === 'ro' ? { readOnly: {} } : { readWrite: {} } });
    const txn = b.body.transaction;
    replace.set(txn, `<the ${key} transaction>`);
    if (kind !== 'ro') openTxns.add(txn);
    const ev = async (what, method, path, reqBody) => {
      const t0 = Date.now();
      const r = await rest(method, path, reqBody);
      const err = (Array.isArray(r.body) ? r.body : [r.body]).find((x) => x && x.error)?.error;
      events.push({ what, atSeconds: Math.round((t0 - started) / 1000), latencyMs: Date.now() - t0, http: r.http, ...(err ? { status: err.status, message: normalise(err.message, replace) } : {}) });
      return r;
    };
    await body(txn, ev);
    if (kind !== 'ro' && openTxns.has(txn)) {
      await ev('rollback (cleanup)', 'POST', `${ROOT}:rollback`, { transaction: txn });
      openTxns.delete(txn);
    }
    out.push({ id: key, events });
  };
  const get = (doc, txn) => `${n(doc)}?transaction=${encodeURIComponent(txn)}`;
  const commitBody = (doc, txn) => ({ writes: [{ update: { name: n(doc), fields: { v: I(9) } } }], transaction: txn });
  await Promise.all([
    probe('idle_read_write', 'rw', async (txn, ev) => {
      await ev('GET e1 in the transaction', 'GET', get('e1', txn));
      await sleep(EXPIRY_WAIT_MS);
      const r = await ev(`commit a write of e1 after ${EXPIRY_WAIT_MS / 1000} s idle`, 'POST', `${ROOT}:commit`, commitBody('e1', txn));
      if (r.http === 200 || r.http === 409) openTxns.delete(txn);
      await ev('GET e1 with the transaction afterwards', 'GET', get('e1', txn));
    }),
    probe('busy_read_write', 'rw', async (txn, ev) => {
      for (let s = 0; s < EXPIRY_WAIT_MS; s += 30_000) {
        const t0 = Date.now();
        const r = await ev('GET e2 in the transaction', 'GET', get('e2', txn));
        if (r.http !== 200) break;
        await sleep(Math.max(0, 30_000 - (Date.now() - t0)));
      }
      const r = await ev(`commit a write of e2 after ${EXPIRY_WAIT_MS / 1000} s of reads every 30 s`, 'POST', `${ROOT}:commit`, commitBody('e2', txn));
      if (r.http === 200 || r.http === 409) openTxns.delete(txn);
    }),
    probe('idle_read_only', 'ro', async (txn, ev) => {
      await ev('GET e3 in the transaction', 'GET', get('e3', txn));
      await sleep(EXPIRY_WAIT_MS);
      await ev(`GET e3 in the transaction after ${EXPIRY_WAIT_MS / 1000} s idle`, 'GET', get('e3', txn));
    }),
    probe('idle_70s_then_plain_write', 'rw', async (txn, ev) => {
      await ev('GET e5 in the transaction', 'GET', get('e5', txn));
      await sleep(70_000);
      await ev('after 70 s idle: a commit without transaction writes e5 (the transaction holds a read lock on it)', 'POST', `${ROOT}:commit`, { writes: [{ update: { name: n('e5'), fields: { v: I(50) } } }] });
      const r = await ev('then the transaction commits a write of e5', 'POST', `${ROOT}:commit`, commitBody('e5', txn));
      if (r.http === 200 || r.http === 409) openTxns.delete(txn);
    }),
    probe('idle_271s_read_write', 'rw', async (txn, ev) => {
      await ev('GET e6 in the transaction', 'GET', get('e6', txn));
      await sleep(271_000);
      await ev('GET e6 in the transaction after 271 s idle', 'GET', get('e6', txn));
      const r = await ev('then commit a write of e6', 'POST', `${ROOT}:commit`, commitBody('e6', txn));
      if (r.http === 200 || r.http === 409) openTxns.delete(txn);
    }),
    probe('idle_blind_read_write', 'rw', async (txn, ev) => {
      await sleep(EXPIRY_WAIT_MS);
      const r = await ev(`commit a blind write of e4 after ${EXPIRY_WAIT_MS / 1000} s idle (nothing read)`, 'POST', `${ROOT}:commit`, commitBody('e4', txn));
      if (r.http === 200) written.add(n('e4'));
      if (r.http === 200 || r.http === 409) openTxns.delete(txn);
    }),
  ]);
  written.add(n('e4'));
  return out;
}

// ---- lock ranges ----

async function locks() {
  const ITEMS = `${K}-items`; // unique, so the collection-group query sees only these documents
  const OTHERS = `${K}-others`;
  const P1 = `${K}/p1/${ITEMS}`;
  const P2 = `${K}/p2/${ITEMS}`;
  const docs = {};
  for (let i = 1; i <= 6; i++) docs[`${P1}/i${i}`] = { n: I(i) };
  for (let i = 1; i <= 3; i++) docs[`${P2}/j${i}`] = { n: I(i + 3) };
  docs[`${K}/p1/${OTHERS}/o1`] = { n: I(5) };
  const s = await setup(docs);
  const queries = {
    range: { parent: `${ROOT}/${K}/p1`, structuredQuery: { from: [{ collectionId: ITEMS }], where: { fieldFilter: { field: { fieldPath: 'n' }, op: 'GREATER_THAN', value: I(3) } } } },
    whole: { parent: `${ROOT}/${K}/p1`, structuredQuery: { from: [{ collectionId: ITEMS }] } },
    group: { parent: ROOT, structuredQuery: { from: [{ collectionId: ITEMS, allDescendants: true }] } },
    limit: { parent: `${ROOT}/${K}/p1`, structuredQuery: { from: [{ collectionId: ITEMS }], orderBy: [{ field: { fieldPath: 'n' } }], limit: 2 } },
  };
  const writesFor = (doc, f) => [{ update: { name: `${ROOT}/${doc}`, fields: f } }];
  let serial = 0;
  const scenarios = [
    ['range.update_scanned', 'range', 'update p1/items/i5 (n 5, matched)', () => writesFor(`${P1}/i5`, { n: I(5), w: I(++serial) })],
    ['range.insert_in_range', 'range', 'create p1/items/new-in-<k> with n 5 (a phantom)', (k) => writesFor(`${P1}/new-in-${k}`, { n: I(5) })],
    ['range.insert_below_range', 'range', 'create p1/items/new-out-<k> with n 1 (outside n > 3)', (k) => writesFor(`${P1}/new-out-${k}`, { n: I(1) })],
    ['range.update_unscanned', 'range', 'update p1/items/i1 (n 1, not matched)', () => writesFor(`${P1}/i1`, { n: I(1), w: I(++serial) })],
    ['range.move_into_range', 'range', 'update p1/items/i2 (run 1) or i3 (run 2) from n 2 or 3 to n 9', (k) => writesFor(`${P1}/i${k === '1' ? 2 : 3}`, { n: I(9) })],
    ['range.insert_no_field', 'range', 'create p1/items/new-nofield-<k> with no n', (k) => writesFor(`${P1}/new-nofield-${k}`, { x: I(1) })],
    ['range.sibling_parent', 'range', 'create p2/items/new-sib-<k> with n 5 (same collection id, other parent)', (k) => writesFor(`${P2}/new-sib-${k}`, { n: I(5) })],
    ['range.other_collection', 'range', 'create p1/others/new-oth-<k> with n 5', (k) => writesFor(`${K}/p1/${OTHERS}/new-oth-${k}`, { n: I(5) })],
    ['whole.insert', 'whole', 'create p1/items/new-w-<k> (query: the whole collection)', (k) => writesFor(`${P1}/new-w-${k}`, { n: I(1) })],
    ['whole.sibling_parent', 'whole', 'create p2/items/new-wsib-<k>', (k) => writesFor(`${P2}/new-wsib-${k}`, { n: I(1) })],
    ['limit.insert_after_last', 'limit', 'create p1/items/new-l-<k> with n 6.5 (beyond the 2 returned, orderBy n limit 2)', (k) => writesFor(`${P1}/new-l-${k}`, { n: D(6.5) })],
    ['limit.insert_before_first', 'limit', 'create p1/items/new-l0-<k> with n 0 (before the first returned)', (k) => writesFor(`${P1}/new-l0-${k}`, { n: I(0) })],
    ['group.insert_other_parent', 'group', 'create p2/items/new-g-<k> (query: collection group items)', (k) => writesFor(`${P2}/new-g-${k}`, { n: I(1) })],
    ['group.other_collection', 'group', 'create p1/others/new-go-<k>', (k) => writesFor(`${K}/p1/${OTHERS}/new-go-${k}`, { n: I(1) })],
  ];
  const BLOCKED_MS = 6000;
  const out = [];
  for (const run of [1, 2]) {
    for (const [id, qk, desc, mk] of scenarios) {
      const k = `${run}`;
      const writes = mk(k);
      scopeWrites(writes);
      const t0 = Date.now();
      const b = await rest('POST', `${ROOT}:beginTransaction`, { options: { readWrite: {} } });
      const txn = b.body.transaction;
      openTxns.add(txn);
      const qr = await rest('POST', `${queries[qk].parent}:runQuery`, { structuredQuery: queries[qk].structuredQuery, transaction: txn });
      const qErr = (Array.isArray(qr.body) ? qr.body : [qr.body]).find((x) => x && x.error)?.error;
      const matched = (Array.isArray(qr.body) ? qr.body : []).filter((x) => x.document).map((x) => rel(x.document.name));
      const w0 = Date.now();
      let wDone = null;
      const wp = rest('POST', `${DB}/documents:commit`, { writes }).then((r) => { wDone = Date.now(); return r; });
      const first = await Promise.race([wp.then(() => 'done'), sleep(BLOCKED_MS).then(() => 'pending')]);
      const rb0 = Date.now();
      const rbr = await rest('POST', `${ROOT}:rollback`, { transaction: txn });
      openTxns.delete(txn);
      const rb1 = Date.now();
      const wr = await wp;
      track(writes, wr.http);
      const err = wr.body && wr.body.error;
      out.push({
        id: `${id}.run${run}`, scenario: id, run, description: `T1 (read-write) runs ${qk} query; then a commit without transaction: ${desc}`,
        query: { ...JSON.parse(relProject(JSON.stringify(queries[qk]))), matched, ...(qErr ? { error: { status: qErr.status, message: qErr.message } } : {}) },
        write: JSON.parse(relProject(JSON.stringify(writes))).map((w) => rel(w.update.name.replace('projects/{project}/databases/(default)/documents/', ''))),
        outcome: first === 'done' ? 'completed before T1 ended' : 'blocked until T1 rolled back',
        writeLatencyMs: wDone - w0,
        writeEndedAfterRollbackMs: first === 'done' ? null : wDone - rb1,
        rollbackSentAfterMs: rb0 - w0,
        rollbackHttp: rbr.http,
        writeResult: { http: wr.http, ...(err ? { status: err.status, message: err.message } : {}) },
        totalMs: Date.now() - t0,
      });
      await pause();
    }
  }
  // B7: a wounded transaction, call by call
  const timed = async (events, what, method, path, body) => {
    const t0 = Date.now();
    const r = await rest(method, path, body);
    const err = (Array.isArray(r.body) ? r.body : [r.body]).find((x) => x && x.error)?.error;
    events.push({ what, latencyMs: Date.now() - t0, http: r.http, ...(err ? { status: err.status, message: err.message } : {}) });
    return r;
  };
  const extra = [];
  {
    const W = `${ROOT}/${K}/wound`;
    await commitWrites([{ update: { name: W, fields: { v: I(0) } } }]);
    const events = [];
    const t1 = (await rest('POST', `${ROOT}:beginTransaction`, { options: { readWrite: {} } })).body.transaction;
    const t2 = (await rest('POST', `${ROOT}:beginTransaction`, { options: { readWrite: {} } })).body.transaction;
    openTxns.add(t1);
    openTxns.add(t2);
    await timed(events, 'T1 GET wound', 'GET', `${W}?transaction=${encodeURIComponent(t1)}`);
    await timed(events, 'T2 GET wound', 'GET', `${W}?transaction=${encodeURIComponent(t2)}`);
    const c1 = await timed(events, 'T1 commits a write of wound (T1 is older: wounds T2)', 'POST', `${ROOT}:commit`, { writes: [{ update: { name: W, fields: { v: I(1) } } }], transaction: t1 });
    if (c1.http === 200 || c1.http === 409) openTxns.delete(t1);
    await timed(events, 'T2 GET wound (first call after the wound)', 'GET', `${W}?transaction=${encodeURIComponent(t2)}`);
    await timed(events, 'T2 GET wound again (second call)', 'GET', `${W}?transaction=${encodeURIComponent(t2)}`);
    const c2 = await timed(events, 'T2 commits a write of wound', 'POST', `${ROOT}:commit`, { writes: [{ update: { name: W, fields: { v: I(2) } } }], transaction: t2 });
    if (c2.http === 200 || c2.http === 409) openTxns.delete(t2);
    const rb = await timed(events, 'T2 rollback', 'POST', `${ROOT}:rollback`, { transaction: t2 });
    if (rb.http === 200) openTxns.delete(t2);
    const fin = await rest('GET', W);
    extra.push({ id: 'wounded_transaction', description: 'T1 and T2 read-write; both read wound; T1 commits a write of it; then every call of T2', events, finalValue: fin.body.fields?.v });
  }
  // B3: an OPTIMISTIC transaction requested by the owner
  {
    const D = `${ROOT}/${K}/optimistic`;
    await commitWrites([{ update: { name: D, fields: { v: I(0) } } }]);
    const forms = [
      ['options.readWrite.concurrencyMode', { options: { readWrite: { concurrencyMode: 'OPTIMISTIC' } } }],
      ['top-level concurrencyMode', { options: { readWrite: {} }, concurrencyMode: 'OPTIMISTIC' }],
    ];
    for (const [form, body] of forms) {
      const events = [];
      const b = await timed(events, `beginTransaction with ${form} OPTIMISTIC`, 'POST', `${ROOT}:beginTransaction`, body);
      const txn = b.body.transaction;
      if (txn) {
        openTxns.add(txn);
        await timed(events, 'GET optimistic in the transaction', 'GET', `${D}?transaction=${encodeURIComponent(txn)}`);
        await timed(events, 'a commit without transaction writes optimistic', 'POST', `${ROOT}:commit`, { writes: [{ update: { name: D, fields: { v: I(1) } } }] });
        const c = await timed(events, 'the transaction commits a write of optimistic', 'POST', `${ROOT}:commit`, { writes: [{ update: { name: D, fields: { v: I(2) } } }], transaction: txn });
        if (c.http === 200 || c.http === 409) openTxns.delete(txn);
      }
      extra.push({ id: `optimistic.${form}`, description: `beginTransaction ${JSON.stringify(body)} as the owner`, request: body, events });
    }
  }
  out.push(...extra);

  const summary = {};
  for (const [id] of scenarios) {
    const runs = out.filter((x) => x.scenario === id);
    summary[id] = { outcomes: runs.map((x) => x.outcome), stable: new Set(runs.map((x) => x.outcome)).size === 1, writeHttp: runs.map((x) => x.writeResult.http) };
  }
  return {
    setup: { commits: [s.commit] },
    cases: out,
    summary,
    notes: [
      `"items" in the descriptions is the subcollection ${ITEMS} and "others" is ${OTHERS} (unique ids, so the collection-group query reads only this run's documents); p1 and p2 are documents of ${K} that are never written themselves.`,
      `Each scenario: T1 begins read-write and runs the query in its transaction; then a commit without transaction sends the write. If the write has not returned after ${BLOCKED_MS} ms, T1 is rolled back. outcome says whether the write returned before T1 ended; writeEndedAfterRollbackMs is how long after the rollback returned it completed. Latencies include about 0.3-1 s of network.`,
      'Two runs of every scenario, run 2 after run 1, so documents inserted by run 1 are present in run 2.',
    ],
  };
}

// ---- non-UTF-8 ids ----

// protobuf encoding, enough for the few requests sent by hand.
const varint = (n) => {
  const out = [];
  let x = BigInt(n);
  do {
    let b = Number(x & 0x7fn);
    x >>= 7n;
    if (x > 0n) b |= 0x80;
    out.push(b);
  } while (x > 0n);
  return Buffer.from(out);
};
const ld = (field, bytes) => Buffer.concat([varint((field << 3) | 2), varint(bytes.length), bytes]);
const vi = (field, n) => Buffer.concat([varint(field << 3), varint(n)]);
const bstr = (...parts) => Buffer.concat(parts.map((p) => (typeof p === 'string' ? Buffer.from(p, 'utf8') : Buffer.from(p))));
const FF = [0xff];

async function nonUtf8() {
  const out = [];
  const s = await setup({ [`${U}/ok`]: { a: I(1) } });
  const base = `${ROOT}/${U}`;
  rawPaths.add(`${base}/%FF`); // deleted at the cleanup whatever production answers
  const rc = async (id, d, method, path, body, o = {}) => {
    const { c, raw } = await restCase(id, d, method, path, body, o);
    if (raw.http === 200 && raw.body && raw.body.name) {
      written.add(raw.body.name);
      c.wroteDocument = true;
    }
    if (raw.http === 200 && method !== 'GET' && /%[89A-F][0-9A-F]/i.test(path)) rawPaths.add(path.replace(/\?documentId=/, '/').split('?')[0]);
    const e = (Array.isArray(raw.body) ? raw.body : [raw.body]).find((x) => x && x.error)?.error;
    if (e) c.errorDetailTypes = (e.details || []).map((x) => x['@type']);
    out.push(c);
    return raw;
  };
  await rc('rest.get_valid_utf8_control', 'GET …/caf%C3%A9 (valid UTF-8, missing)', 'GET', `${base}/caf%C3%A9`, undefined, { replay: true });
  await rc('rest.get_ff', 'GET …/%FF', 'GET', `${base}/%FF`, undefined, { replay: true });
  await rc('rest.get_ff_inside', 'GET …/a%FFb', 'GET', `${base}/a%FFb`, undefined, { replay: true });
  await rc('rest.get_overlong_slash', 'GET …/%C0%AF (an overlong encoding of /)', 'GET', `${base}/%C0%AF`, undefined, { replay: true });
  await rc('rest.get_surrogate', 'GET …/%ED%A0%80 (an encoded UTF-16 surrogate)', 'GET', `${base}/%ED%A0%80`, undefined, { replay: true });
  await rc('rest.get_truncated', 'GET …/%E2%82 (a truncated sequence)', 'GET', `${base}/%E2%82`, undefined, { replay: true });
  await rc('rest.get_collection_ff', 'GET …/%FF/x (the collection id is not UTF-8)', 'GET', `${ROOT}/${U}-%FF/x`, undefined, { replay: true });
  await rc('rest.list_ff', 'ListDocuments of collection aruma-ref-f2-utf-%FF', 'GET', `${ROOT}/${U}-%FF`, undefined, { replay: true });
  // not replay: test/differential's fake server answers every PATCH as a setup write
  await rc('rest.patch_ff', 'PATCH …/%FF {a: 1}', 'PATCH', `${base}/%FF`, { fields: { a: I(1) } });
  await rc('rest.delete_ff', 'DELETE …/%FF', 'DELETE', `${base}/%FF`, undefined, { replay: true });
  // Which document did a request that production accepted write? A space and
  // U+FFFD are the candidates; each is read, then deleted before the next case.
  const SPACE = `${base}/ `;
  const FFFD = `${base}/\uFFFD`;
  written.add(SPACE);
  written.add(FFFD);
  const which = async (after) => {
    await rc(`${after}.then_get_space`, `after ${after}: GET …/%20 (a document whose id is one space)`, 'GET', `${base}/%20`);
    await rc(`${after}.then_get_replacement_character`, `after ${after}: GET …/%EF%BF%BD (a document whose id is U+FFFD)`, 'GET', `${base}/%EF%BF%BD`);
    await commitWrites([{ delete: SPACE }, { delete: FFFD }]);
  };
  await rc('rest.create_document_id_ff', 'POST …?documentId=%FF', 'POST', `${base}?documentId=%FF`, { fields: { a: I(1) } });
  await which('rest.create_document_id_ff');
  await rc('rest.get_mask_ff', 'GET ok?mask.fieldPaths=%FF (a field path that is not UTF-8)', 'GET', `${base}/ok?mask.fieldPaths=%FF`, undefined, { replay: true });
  await rc('rest.list_collection_ids_ff_parent', 'listCollectionIds of …/%FF', 'POST', `${base}/%FF:listCollectionIds`, {}, { replay: true });
  // raw bytes in a JSON body
  const name = `${base}/`;
  const rawCommit = (shown, parts) => ({ raw: bstr(...parts), rawShown: shown });
  const commitPath = `${ROOT}:commit`;
  await rc('rest.commit_name_raw_ff', 'commit an update whose name ends in the raw byte 0xFF inside the JSON string', 'POST', commitPath, undefined,
    rawCommit(`{"writes":[{"update":{"name":"${name}<0xFF>","fields":{"a":{"integerValue":"1"}}}}]}`, ['{"writes":[{"update":{"name":"', name, FF, '","fields":{"a":{"integerValue":"1"}}}}]}']));
  await which('rest.commit_name_raw_ff');
  await rc('rest.commit_name_escaped_lone_surrogate', 'commit an update whose name ends in the JSON escape \\udc00 (a lone surrogate)', 'POST', commitPath, undefined,
    rawCommit(`{"writes":[{"update":{"name":"${name}\\udc00","fields":{"a":{"integerValue":"1"}}}}]}`, ['{"writes":[{"update":{"name":"', name, '\\udc00","fields":{"a":{"integerValue":"1"}}}}]}']));
  await which('rest.commit_name_escaped_lone_surrogate');
  await rc('rest.commit_field_name_raw_ff', 'commit an update of ok-field whose field name is the raw byte 0xFF', 'POST', commitPath, undefined,
    rawCommit(`{"writes":[{"update":{"name":"${name}ok-field","fields":{"<0xFF>":{"integerValue":"1"}}}}]}`, ['{"writes":[{"update":{"name":"', name, 'ok-field","fields":{"', FF, '":{"integerValue":"1"}}}}]}']));
  await rc('rest.commit_string_value_raw_ff', 'commit an update of ok-string whose string value is the raw byte 0xFF', 'POST', commitPath, undefined,
    rawCommit(`{"writes":[{"update":{"name":"${name}ok-string","fields":{"s":{"stringValue":"<0xFF>"}}}}]}`, ['{"writes":[{"update":{"name":"', name, 'ok-string","fields":{"s":{"stringValue":"', FF, '"}}}}]}']));
  await rc('rest.commit_field_name_raw_ff.then_get', 'GET ok-field: which field name was stored', 'GET', `${base}/ok-field`);
  await rc('rest.commit_string_value_raw_ff.then_get', 'GET ok-string: which string was stored', 'GET', `${base}/ok-string`);
  await rc('rest.get_query_ff_in_transaction_parameter', 'GET ok?transaction=%FF (a transaction id that is one byte 0xFF, not base64)', 'GET', `${base}/ok?transaction=%FF`);
  await rc('rest.batch_get_raw_ff', 'batchGet a name ending in the raw byte 0xFF', 'POST', `${ROOT}:batchGet`, undefined,
    rawCommit(`{"documents":["${name}<0xFF>"]}`, ['{"documents":["', name, FF, '"]}']));
  for (const k of ['ok-field', 'ok-string']) written.add(`${base}/${k}`);

  if (g) {
    const gr = async (id, d, rpc, bytes, shown) => {
      const r = await grpcRawUnary(rpc, bytes);
      await pause();
      const c = {
        id, description: d, transport: 'grpc',
        request: { rpc: `google.firestore.v1.Firestore/${rpc}`, messageHandEncoded: relProject(shown), bytes: bytes.length },
        response: r.ok ? { ok: true, message: normalise(jsonSafe(r.response)) } : { ok: false },
      };
      if (!r.ok) c.error = { code: r.error.code, status: r.error.status, message: r.error.message };
      if (r.ok && r.response && r.response.writeResults) c.wroteDocument = true;
      out.push(c);
    };
    const nm = (...tail) => bstr(name, ...tail);
    await gr('grpc.get_ascii_control', 'GetDocument of …/missing, hand-encoded (control)', 'GetDocument', ld(1, nm('missing')), `{name: "${name}missing"}`);
    await gr('grpc.get_ff', 'GetDocument name ending in the byte 0xFF', 'GetDocument', ld(1, nm(FF)), `{name: "${name}<0xFF>"}`);
    await gr('grpc.get_surrogate', 'GetDocument name ending in ED A0 80 (an encoded surrogate)', 'GetDocument', ld(1, nm([0xed, 0xa0, 0x80])), `{name: "${name}<0xED 0xA0 0x80>"}`);
    const doc = (nameBytes, fieldsBytes = Buffer.alloc(0)) => Buffer.concat([ld(1, nameBytes), fieldsBytes]);
    const intField = (key) => ld(2, Buffer.concat([ld(1, key), ld(2, vi(2, 1))])); // map entry {key: Value{integer_value: 1}}
    const commitReq = (document) => Buffer.concat([ld(1, Buffer.from(DB)), ld(2, ld(1, document))]);
    await gr('grpc.commit_ascii_control', 'Commit an update of ok-grpc {a: 1}, hand-encoded (control)', 'Commit', commitReq(doc(nm('ok-grpc'), intField(Buffer.from('a')))), `{database: "${DB}", writes: [{update: {name: "${name}ok-grpc", fields: {a: 1}}}]}`);
    written.add(`${name}ok-grpc`);
    await gr('grpc.commit_name_ff', 'Commit an update whose name ends in 0xFF', 'Commit', commitReq(doc(nm(FF), intField(Buffer.from('a')))), `{database: "${DB}", writes: [{update: {name: "${name}<0xFF>", fields: {a: 1}}}]}`);
    await gr('grpc.commit_field_name_ff', 'Commit an update of ok-grpc-field whose field name is 0xFF', 'Commit', commitReq(doc(nm('ok-grpc-field'), intField(Buffer.from(FF)))), `{database: "${DB}", writes: [{update: {name: "${name}ok-grpc-field", fields: {<0xFF>: 1}}}]}`);
    written.add(`${name}ok-grpc-field`);
    const strField = Buffer.concat([ld(2, Buffer.concat([ld(1, Buffer.from('s')), ld(2, ld(17, Buffer.from(FF)))]))]);
    await gr('grpc.commit_string_value_ff', 'Commit an update of ok-grpc-string whose string value is 0xFF', 'Commit', commitReq(doc(nm('ok-grpc-string'), strField)), `{database: "${DB}", writes: [{update: {name: "${name}ok-grpc-string", fields: {s: "<0xFF>"}}}]}`);
    written.add(`${name}ok-grpc-string`);
    await gr('grpc.create_document_id_ff', 'CreateDocument with documentId 0xFF', 'CreateDocument', Buffer.concat([ld(1, Buffer.from(ROOT)), ld(2, Buffer.from(U)), ld(3, Buffer.from(FF)), ld(4, Buffer.alloc(0))]), `{parent: "${ROOT}", collectionId: "${U}", documentId: "<0xFF>", document: {}}`);
    await gr('grpc.list_collection_ff', 'ListDocuments with collectionId 0xFF', 'ListDocuments', Buffer.concat([ld(1, Buffer.from(ROOT)), ld(2, Buffer.from(FF))]), `{parent: "${ROOT}", collectionId: "<0xFF>"}`);
  }
  return {
    setup: { commits: [s.commit] },
    cases: out,
    notes: [
      'bodyBytes shows a REST body sent as raw bytes: <0xFF> stands for the single byte 0xFF inside the JSON text (not valid UTF-8); the JSON escape \\udc00 is sent as those six characters.',
      'The gRPC cases are hand-encoded protobuf (proto-loader always writes valid UTF-8), sent with the usual metadata; messageHandEncoded shows the message, with <0xFF> for the raw byte.',
      'wroteDocument marks a request production accepted and wrote; the cleanup deletes it.',
    ],
  };
}

// ---- cleanup ----

const sweepF2 = () => rest('POST', `${ROOT}:runQuery`, {
  structuredQuery: {
    from: [{ allDescendants: true }],
    where: {
      compositeFilter: {
        op: 'AND',
        filters: [
          { fieldFilter: { field: { fieldPath: '__name__' }, op: 'GREATER_THAN_OR_EQUAL', value: { referenceValue: `${ROOT}/aruma-ref-f2/${MIN_ID}` } } },
          { fieldFilter: { field: { fieldPath: '__name__' }, op: 'LESS_THAN', value: { referenceValue: `${ROOT}/aruma-ref-f3/${MIN_ID}` } } },
        ],
      },
    },
    select: { fields: [{ fieldPath: '__name__' }] },
    orderBy: [{ field: { fieldPath: '__name__' }, direction: 'ASCENDING' }],
    limit: 1000,
  },
});
const found = (r) => (Array.isArray(r.body) ? r.body : []).filter((x) => x.document).map((x) => x.document.name);

async function deleteAll(names, res) {
  for (let i = 0; i < names.length; i += 400) {
    const r = await commitWrites(names.slice(i, i + 400).map((x) => ({ delete: x })));
    res.push(r.http);
    if (r.http !== 200) throw new Error(`cleanup commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
}

async function strayCheck() {
  const s = await sweepF2();
  if (s.http !== 200) throw new Error(`stray sweep failed: HTTP ${s.http}`);
  const strays = found(s);
  const res = [];
  if (strays.length) await deleteAll(strays, res);
  return { strays: strays.map(rel), commitHttp: res };
}

async function cleanup(pending) {
  await Promise.allSettled(pending);
  for (const t of [...openTxns]) {
    await rest('POST', `${ROOT}:rollback`, { transaction: t });
    openTxns.delete(t);
  }
  const res = [];
  const names = [...written];
  await deleteAll(names, res);
  const rawDeletes = [];
  for (const p of rawPaths) {
    const r = await rest('DELETE', p);
    rawDeletes.push({ path: relProject(p), http: r.http });
  }
  const strays = found(await sweepF2());
  if (strays.length) await deleteAll(strays, res);
  const sweep = await sweepF2();
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
    deleted: names.map(rel).sort(),
    rawPathDeletes: rawDeletes,
    strays: strays.map(rel),
    commitHttp: res,
    sweep: { http: sweep.http, query: `kindless allDescendants, __name__ in [aruma-ref-f2/${MIN_ID}, aruma-ref-f3/${MIN_ID})`, remaining },
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
  const pending = [];
  try {
    log('strays ');
    atStart = await strayCheck();
    let expiryP;
    if (want('expiry')) {
      expiryP = expiry();
      pending.push(expiryP);
    }
    if (want('transforms')) {
      log('transforms ');
      const sb = await setup({ [`${T}/base`]: FIX });
      results['f2-transforms.json'] = { setup: { commits: [sb.commit] }, ...(await transforms()) };
    }
    if (want('commit')) {
      log('commit ');
      const sc = await setup({ [`${C}/n1`]: { a: I(1) } });
      results['f2-commit.json'] = { setup: { commits: [sc.commit] }, ...(await commits(sc.raw)) };
    }
    if (want('batch-write')) {
      log('batch-write ');
      const sbw = await setup({ [`${B}/base`]: { a: I(1) } });
      results['f2-batch-write.json'] = { setup: { commits: [sbw.commit] }, ...(await batchWrites()) };
    }
    if (want('transactions')) {
      log('transactions ');
      const sx = await setup({ [`${X}/a`]: { v: I(1) }, [`${X}/b`]: { v: I(2) }, [`${X}/c`]: { v: I(3) } });
      results['f2-transactions.json'] = { setup: { commits: [sx.commit] }, ...(await transactions(sx.raw)) };
    }
    if (want('locks')) {
      log('locks ');
      results['f2-locks.json'] = await locks();
    }
    if (want('non-utf8')) {
      log('non-utf8 ');
      results['f2-non-utf8.json'] = await nonUtf8();
    }
    if (expiryP) {
      log('expiry(waiting) ');
      const ex = await expiryP;
      const tx = results['f2-transactions.json'] ?? (results['f2-transactions.json'] = { cases: [] });
      tx.lifetime = ex;
      (tx.notes ??= []).push(`lifetime: six transactions begun at the same moment: idle_70s_then_plain_write read e5, after 70 s idle a commit without transaction wrote e5, then the transaction committed a write of e5; idle_271s_read_write read e6, read it again after 271 s idle, then committed; idle_read_write read e1 and committed after ${EXPIRY_WAIT_MS / 1000} s idle; busy_read_write read e2 every 30 s and committed at ${EXPIRY_WAIT_MS / 1000} s; idle_read_only read e3 again after ${EXPIRY_WAIT_MS / 1000} s; idle_blind_read_write read nothing and committed a write after ${EXPIRY_WAIT_MS / 1000} s. atSeconds is from the begin.`);
    }
  } finally {
    log('\ncleanup ');
    cl = await cleanup(pending);
    log(cl.ok ? 'verified\n' : 'NOT VERIFIED\n');
  }
  if (!cl.ok) {
    console.error('record-f2: the cleanup is not verified; no corpus file written. Remaining:', JSON.stringify({ sweep: cl.sweep.remaining, lists: cl.listsShowMissing, open: cl.transactionsLeftOpen }));
    process.exit(2);
  }
  if (only.length) {
    console.error('record-f2: --only run; cleanup verified, no corpus file written (a partial run is a probe).');
    // F2_PROBE_OUT names a file (outside the repository) for a probe's results.
    if (process.env.F2_PROBE_OUT) writeFileSync(process.env.F2_PROBE_OUT, JSON.stringify({ results, cleanup: cl }, null, 2));
    return;
  }
  results['f2-cleanup.json'] = { cleanup: { ...cl, straysAtStart: atStart } };
  const finished = new Date().toISOString();
  for (const [file, body] of Object.entries(results)) {
    const header = {
      oracle: 'production',
      target,
      generator: GENERATOR,
      recordedAt: `${started} .. ${finished}`,
      versions: ver,
      notes: [
        'Auth: the firebase CLI login (a project owner), so beginTransaction gives a PESSIMISTIC read-write transaction and security rules do not apply; end-user behaviour is not recorded here.',
        'Times, durations and transaction ids are replaced by placeholders (normalise in lib.mjs, and the replace maps of record-f2.mjs); everything else is as returned.',
        ...(body.notes || []),
      ],
    };
    const { notes, ...payload } = body;
    writeCorpus(file, header, payload);
  }
}

main().catch((e) => {
  console.error('record-f2 failed:', e.stack);
  process.exit(1);
});
