// Production recordings of the maps the SDKs send for Enterprise BSON types
// (JS SDK 4.18+, Android 27, iOS 13) on a Standard database, and of masks that
// reach into a vector (internal/services/documents/value):
//
//   values-bson.json          one document per value in aruma-ref-bson (field
//                             v): the eight BSON shapes with edge contents,
//                             malformed shapes (extra keys, wrong value types,
//                             out-of-range contents), BSON values inside arrays
//                             and maps, and their neighbours of every type;
//                             what each write returned and what was stored;
//                             orderBy v both ways; the == and startAt/endAt
//                             class of every stored value; range, !=, in, unary
//                             and array-contains filters with BSON and
//                             neighbouring operands; the storage size of each
//                             shape (from the "exceeds the maximum allowed
//                             size" message); and pairs that show how much of
//                             the 1500-byte index budget each shape spends
//   values-mask-vector.json   read masks and update masks naming a vector's or
//                             a BSON map's inner keys (vec.value, vec.__type__,
//                             b.__int__)
//   values-bson-cleanup.json  the deletion of everything written, verified
//
// REST only (firestore.googleapis.com/v1).
//
//   node record-bson.mjs
//
// Data is written ONLY under the top-level collections aruma-ref-bson,
// aruma-ref-bson-size and aruma-ref-bson-mask (assertScoped refuses anything
// else), everything written is deleted at the end, and the deletion is verified
// by a kindless query over every collection id from "aruma-ref-bson" up to
// "aruma-ref-bsoo" and by showMissing lists of each collection used. Nothing
// creates or changes an index, field override, rule, TTL policy or setting.
import {
  PROJECT, DB, ROOT, rest, versions, compact, writeCorpus, assertScoped,
} from './lib.mjs';

const GENERATOR = 'test/oracle/documents/production/record-bson.mjs';
const MIN_ID = '__id-9223372036854775808__';
const ORD = 'aruma-ref-bson';
const SIZE = 'aruma-ref-bson-size';
const MASK = 'aruma-ref-bson-mask';
const TOPS = [ORD, SIZE, MASK];
const LIMIT = 1048576;
const log = (s) => process.stderr.write(s);

// ---- values in proto3 JSON (as record-values.mjs) ----

const NEG0 = '@@negative-zero@@';
const send = (x) => JSON.stringify(x).replaceAll(`"${NEG0}"`, '-0.0');
const forCorpus = (x) => JSON.parse(JSON.stringify(x ?? null).replaceAll(`"${NEG0}"`, '"-0"'));
function signed(x) {
  if (Object.is(x, -0)) return '-0';
  if (Array.isArray(x)) return x.map(signed);
  if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, signed(y)]));
  return x;
}
// resend turns a corpus value back into what is sent: "-0" is a negative zero
// only where a double stands (a decimal128 string "-0" stays a string).
function resend(x, key = '') {
  if (x === '-0' && ['doubleValue', 'latitude', 'longitude'].includes(key)) return NEG0;
  if (Array.isArray(x)) return x.map((y) => resend(y, key));
  if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, resend(y, k)]));
  return x;
}

const NUL = { nullValue: null };
const BOOL = (b) => ({ booleanValue: b });
const I = (s) => ({ integerValue: String(s) });
const D = (x) => ({ doubleValue: Object.is(x, -0) ? NEG0 : x });
const S = (s) => ({ stringValue: s });
const BY = (bytes) => ({ bytesValue: Buffer.from(bytes).toString('base64') });
const T = (s) => ({ timestampValue: s });
const R = (rel) => ({ referenceValue: rel.startsWith('projects/') ? rel : `${ROOT}/${rel}` });
const G = (lat, lng) => ({ geoPointValue: { latitude: lat, longitude: lng } });
const A = (...xs) => ({ arrayValue: xs.length ? { values: xs } : {} });
const M = (o) => ({ mapValue: Object.keys(o).length ? { fields: o } : {} });
const VEC = (...ds) => M({ __type__: S('__vector__'), value: A(...ds.map(D)) });

// The BSON shapes, as the JS SDK writes them (BsonBinaryData sends its subtype
// as the first byte of __binary__).
const MINK = M({ __min__: NUL });
const MAXK = M({ __max__: NUL });
const I32 = (n) => M({ __int__: I(n) });
const DEC = (s) => M({ __decimal128__: S(s) });
const BTS = (s, i) => M({ __request_timestamp__: M({ seconds: I(s), increment: I(i) }) });
const BIN = (bytes) => M({ __binary__: BY(bytes) });
const OID = (s) => M({ __oid__: S(s) });
const RE = (p, o) => M({ __regex__: M({ pattern: S(p), options: S(o) }) });

const VALUES = [
  // neighbours
  ['null', NUL],
  ['bool-false', BOOL(false)],
  ['bool-true', BOOL(true)],
  ['n-nan', D('NaN')],
  ['n-neg-inf', D('-Infinity')],
  ['n-d-neg-max', D(-1.7976931348623157e308)],
  ['n-d-neg-1e308', D(-1e308)],
  ['n-i-min', I('-9223372036854775808')],
  ['n-d-neg-1', D(-1)],
  ['n-d-neg-0.1', D(-0.1)],
  ['n-d-neg-zero', D(-0)],
  ['n-i-zero', I(0)],
  ['n-d-min', D(5e-324)],
  ['n-d-0.1', D(0.1)],
  ['n-d-0.5', D(0.5)],
  ['n-i-1', I(1)],
  ['n-d-1', D(1)],
  ['n-d-1.5', D(1.5)],
  ['n-i-2', I(2)],
  ['n-d-2.5', D(2.5)],
  ['n-i-2p31', I('2147483648')],
  ['n-d-2p53', D(9007199254740992)],
  ['n-i-2p53-plus-1', I('9007199254740993')],
  ['n-i-max', I('9223372036854775807')],
  ['n-d-2p63', D(9223372036854775808)],
  ['n-d-1e308', D(1e308)],
  ['n-d-max', D(1.7976931348623157e308)],
  ['n-pos-inf', D('Infinity')],
  ['t-epoch', T('1970-01-01T00:00:00Z')],
  ['t-max', T('9999-12-31T23:59:59.999999Z')],
  ['s-empty', S('')],
  ['s-a', S('a')],
  ['b-empty', BY([])],
  ['b-00', BY([0])],
  ['b-01', BY([1])],
  ['b-0102', BY([1, 2])],
  ['b-ff', BY([255])],
  ['b-ff-x20', BY(Array(20).fill(255))],
  ['r-c-a', R('c/a')],
  ['r-other-project', R('projects/zzzz-aruma-ref-other/databases/(default)/documents/c/a')],
  ['g-s90-w180', G(-90, -180)],
  ['g-0-0', G(0, 0)],
  ['g-n90-e180', G(90, 180)],
  ['a-empty', A()],
  ['a-i-1', A(I(1))],
  ['a-d-1', A(D(1))],
  ['vec-1', VEC(1)],
  ['m-empty', M({})],
  ['m-a-1', M({ a: I(1) })],
  ['m-key-u-ffff', M({ '￿': I(1) })],
  ['m-key-under', M({ _: I(1) })],
  // MinKey and MaxKey
  ['k-min', MINK],
  ['k-max', MAXK],
  // int32
  ['i32-min', I32(-2147483648)],
  ['i32-neg-1', I32(-1)],
  ['i32-0', I32(0)],
  ['i32-1', I32(1)],
  ['i32-2', I32(2)],
  ['i32-max', I32(2147483647)],
  // decimal128
  ['dec-nan', DEC('NaN')],
  ['dec-neg-inf', DEC('-Infinity')],
  ['dec-inf', DEC('Infinity')],
  ['dec-neg-1e309', DEC('-1E+309')],
  ['dec-neg-1e308', DEC('-1E+308')],
  ['dec-neg-1', DEC('-1')],
  ['dec-neg-0.1', DEC('-0.1')],
  ['dec-neg-0', DEC('-0')],
  ['dec-0', DEC('0')],
  ['dec-1e-400', DEC('1E-400')],
  ['dec-0.1', DEC('0.1')],
  ['dec-0.1-lo', DEC('0.1000000000000000055511151231257827')],
  ['dec-0.1-hi', DEC('0.1000000000000000055511151231257828')],
  ['dec-1', DEC('1')],
  ['dec-1.0', DEC('1.0')],
  ['dec-1.5', DEC('1.5')],
  ['dec-1.50', DEC('1.50')],
  ['dec-2.5', DEC('2.5')],
  ['dec-2p53-plus-1', DEC('9007199254740993')],
  ['dec-int-max', DEC('9223372036854775807')],
  ['dec-2p63', DEC('9223372036854775808')],
  ['dec-34-digits', DEC('1234567890123456789012345678901234')],
  ['dec-1e308', DEC('1E+308')],
  ['dec-1e309', DEC('1E+309')],
  ['dec-max', DEC('9.999999999999999999999999999999999E+6144')],
  ['dec-tiny', DEC('1E-6176')],
  // non-canonical decimal strings
  ['dec-plus', DEC('+1.5')],
  ['dec-exp', DEC('15e-1')],
  ['dec-dot', DEC('.5')],
  ['dec-lead-zero', DEC('01.50')],
  ['dec-inf-word', DEC('inf')],
  ['dec-snan', DEC('sNaN')],
  ['dec-35-digits', DEC('12345678901234567890123456789012345')],
  ['dec-over', DEC('1E+6145')],
  ['dec-under', DEC('1E-6177')],
  ['dec-abc', DEC('abc')],
  ['dec-empty', DEC('')],
  ['dec-space', DEC(' 1')],
  ['dec-neg-nan', DEC('-NaN')],
  ['dec-plus-nan', DEC('+NaN')],
  ['dec-plus-inf-word', DEC('+inf')],
  ['dec-neg-inf-word', DEC('-inf')],
  ['dec-inf-upper', DEC('INFINITY')],
  ['dec-1e6144', DEC('1E+6144')],
  ['dec-zero-e-7000', DEC('0E-7000')],
  ['dec-35-digits-zero', DEC('12345678901234567890123456789012340')],
  ['dec-1e2', DEC('1e2')],
  ['dec-100', DEC('100')],
  ['dec-neg-0.0', DEC('-0.0')],
  ['dec-1e-6', DEC('0.000001')],
  ['dec-1e-7', DEC('0.0000001')],
  ['dec-zero-e10', DEC('0E+10')],
  // BSON timestamps
  ['bts-0-0', BTS(0, 0)],
  ['bts-1-2', BTS(1, 2)],
  ['bts-1-3', BTS(1, 3)],
  ['bts-2-0', BTS(2, 0)],
  ['bts-max', BTS(4294967295, 4294967295)],
  // binary: subtype byte first
  ['bin-0', BIN([0])],
  ['bin-0-01', BIN([0, 1])],
  ['bin-0-01-02', BIN([0, 1, 2])],
  ['bin-0-ff', BIN([0, 255])],
  ['bin-1-02', BIN([1, 2])],
  ['bin-2', BIN([2])],
  ['bin-80-01', BIN([128, 1])],
  ['bin-ff-x20', BIN(Array(20).fill(255))],
  // object ids
  ['oid-0', OID('000000000000000000000000')],
  ['oid-507f', OID('507f1f77bcf86cd799439011')],
  ['oid-507f-upper', OID('507F1F77BCF86CD799439011')],
  ['oid-f', OID('ffffffffffffffffffffffff')],
  // regular expressions
  ['re-empty-empty', RE('', '')],
  ['re-a-empty', RE('a', '')],
  ['re-a-i', RE('a', 'i')],
  ['re-a-m', RE('a', 'm')],
  ['re-a-mi', RE('a', 'mi')],
  ['re-b-empty', RE('b', '')],
  ['re-a-z', RE('a', 'z')],
  ['re-a-zi', RE('a', 'zi')],
  ['re-a-ii', RE('a', 'ii')],
  ['re-a-imsux', RE('a', 'imsux')],
  ['re-paren', RE('(', '')],
  // inside arrays and maps
  ['a-i32-1', A(I32(1))],
  ['a-k-min', A(MINK)],
  ['a-dec-1', A(DEC('1'))],
  ['m-a-i32-1', M({ a: I32(1) })],
  ['m-a-k-max', M({ a: MAXK })],
  ['m-a-dec-1.5', M({ a: DEC('1.5') })],
  // malformed shapes
  ['bad-int-string', M({ __int__: S('x') })],
  ['bad-int-double', M({ __int__: D(1.5) })],
  ['bad-int-null', M({ __int__: NUL })],
  ['bad-int-2p31', I32('2147483648')],
  ['bad-int-neg-2p31-minus-1', I32('-2147483649')],
  ['bad-int-extra', M({ __int__: I(1), b: I(2) })],
  ['bad-min-1', M({ __min__: I(1) })],
  ['bad-min-extra', M({ __min__: NUL, a: I(1) })],
  ['bad-max-string', M({ __max__: S('x') })],
  ['bad-min-max', M({ __min__: NUL, __max__: NUL })],
  ['bad-dec-double', M({ __decimal128__: D(1.5) })],
  ['bad-dec-extra', M({ __decimal128__: S('1'), x: I(1) })],
  ['bad-bts-missing', M({ __request_timestamp__: M({ seconds: I(1) }) })],
  ['bad-bts-extra', M({ __request_timestamp__: M({ seconds: I(1), increment: I(2), x: I(1) }) })],
  ['bad-bts-string', M({ __request_timestamp__: M({ seconds: S('1'), increment: I(2) }) })],
  ['bad-bts-neg', BTS(-1, 0)],
  ['bad-bts-2p32', BTS('4294967296', 0)],
  ['bad-bts-not-map', M({ __request_timestamp__: I(1) })],
  ['bad-bin-empty', BIN([])],
  ['bad-bin-string', M({ __binary__: S('AQI=') })],
  ['bad-oid-short', OID('507f')],
  ['bad-oid-nonhex', OID('zzzzzzzzzzzzzzzzzzzzzzzz')],
  ['bad-oid-25', OID('507f1f77bcf86cd7994390111')],
  ['bad-oid-int', M({ __oid__: I(1) })],
  ['bad-re-no-options', M({ __regex__: M({ pattern: S('a') }) })],
  ['bad-re-extra', M({ __regex__: M({ pattern: S('a'), options: S(''), x: I(1) }) })],
  ['bad-re-pattern-int', M({ __regex__: M({ pattern: I(1), options: S('') }) })],
  ['bad-re-not-map', M({ __regex__: S('a') })],
  ['bad-other-key', M({ __bson__: I(1) })],
  ['bad-bts-empty', M({ __request_timestamp__: M({}) })],
  ['bad-bts-increment-2p32', BTS(0, '4294967296')],
  ['bad-bts-double', M({ __request_timestamp__: M({ seconds: D(1.5), increment: I(0) }) })],
  ['bin-4-01', BIN([4, 1])],
  ['nested-bad-int', M({ a: M({ __int__: S('x') }) })],
  ['array-bad-int', A(M({ __int__: S('x') }))],
  ['nested-int-extra', M({ a: M({ __int__: I(1), b: I(1) }) })],
  ['depth-19-maps-int32', nestMaps(19, I32(1))],
  ['depth-20-maps-int32', nestMaps(20, I32(1))],
  ['depth-20-maps-regex', nestMaps(20, RE('a', ''))],
  ['depth-21-maps-int32', nestMaps(21, I32(1))],
];

function nestMaps(depth, leaf) {
  let x = leaf;
  for (let i = 0; i < depth; i++) x = M({ a: x });
  return x;
}

// ---- recording helpers ----

const written = new Set();

async function call(method, path, body) {
  const r = await rest(method, path, body === undefined ? undefined : send(body));
  return { http: r.http, body: signed(r.body) };
}

function errorOf(body) {
  const arr = Array.isArray(body) ? body : [body];
  const e = arr.find((x) => x && x.error)?.error;
  return e ? { status: e.status, message: e.message } : undefined;
}

async function commitOne(rel, fields, { mask } = {}) {
  const name = `${ROOT}/${rel}`;
  assertScoped(name);
  const update = { name };
  if (Object.keys(fields).length) update.fields = fields;
  const w = { update };
  if (mask) w.updateMask = { fieldPaths: mask };
  const r = await call('POST', `${ROOT}:commit`, { writes: [w] });
  if (r.http === 200) written.add(name);
  const out = { http: r.http };
  const err = errorOf(r.body);
  if (err) out.error = err;
  return out;
}

async function getDoc(rel, maskPaths) {
  const q = maskPaths ? `?${maskPaths.map((p) => `mask.fieldPaths=${encodeURIComponent(p)}`).join('&')}` : '';
  const r = await call('GET', `${ROOT}/${rel}${q}`);
  const out = { http: r.http };
  const err = errorOf(r.body);
  if (err) out.error = err;
  else out.fields = r.body.fields ?? {};
  return out;
}

const docIds = (body) => (Array.isArray(body) ? body : []).filter((x) => x.document).map((x) => x.document.name.slice(`${ROOT}/${ORD}/`.length));

async function runQuery(sq) {
  const r = await call('POST', `${ROOT}:runQuery`, { structuredQuery: { from: [{ collectionId: ORD }], ...sq } });
  const err = errorOf(r.body);
  return err ? { http: r.http, error: err } : { http: r.http, ids: docIds(r.body) };
}

async function pool(items, n, fn) {
  const out = new Array(items.length);
  let next = 0;
  await Promise.all(Array.from({ length: n }, async () => {
    while (next < items.length) {
      const i = next++;
      out[i] = await fn(items[i], i);
    }
  }));
  return out;
}

const V = { field: { fieldPath: 'v' } };
const ASC = [{ ...V, direction: 'ASCENDING' }];
const DESC = [{ ...V, direction: 'DESCENDING' }];
const FF = (op, value) => ({ fieldFilter: { ...V, op, value } });
const sortKeys = (o) => Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]]));

// ---- values-bson ----

async function bson() {
  const values = [];
  const stored = new Map();
  await pool(VALUES, 6, async ([id, val]) => {
    const w = await commitOne(`${ORD}/${id}`, { v: val });
    values.push({ id, sent: forCorpus(val), write: w });
    log('.');
  });
  values.sort((a, b) => VALUES.findIndex((x) => x[0] === a.id) - VALUES.findIndex((x) => x[0] === b.id));
  const names = values.filter((x) => x.write.http === 200).map((x) => `${ROOT}/${ORD}/${x.id}`);
  for (let i = 0; i < names.length; i += 50) {
    const r = await call('POST', `${DB}/documents:batchGet`, { documents: names.slice(i, i + 50) });
    for (const x of r.body) {
      if (!x.found) continue;
      stored.set(x.found.name.slice(`${ROOT}/${ORD}/`.length), x.found.fields.v);
    }
  }
  for (const x of values) if (stored.has(x.id)) x.stored = stored.get(x.id);
  log(' read ');
  const orderAsc = await runQuery({ orderBy: ASC });
  const orderDesc = await runQuery({ orderBy: DESC });
  const ids = [...stored.keys()];
  const equal = {};
  const classes = {};
  await pool(ids, 6, async (id) => {
    const val = resend(stored.get(id));
    equal[id] = await runQuery({ where: FF('EQUAL', val) });
    const r = await runQuery({ orderBy: ASC, startAt: { values: [val], before: true }, endAt: { values: [val], before: false } });
    classes[id] = r.error ? r : r.ids;
    log('.');
  });
  log(' filters ');
  const filters = [];
  const probe = async (label, where) => filters.push({ label, request: forCorpus(where), ...(await runQuery({ where })) });
  const operands = [
    ['MinKey', MINK], ['MaxKey', MAXK], ['int32 1', I32(1)], ['decimal "1.5"', DEC('1.5')], ['decimal "NaN"', DEC('NaN')],
    ['decimal "Infinity"', DEC('Infinity')], ['decimal "-Infinity"', DEC('-Infinity')], ['decimal "0.1"', DEC('0.1')],
    ['bson timestamp (1, 2)', BTS(1, 2)], ['binary 01 02', BIN([1, 2])], ['binary 00', BIN([0])],
    ['oid 507f…', OID('507f1f77bcf86cd799439011')], ['regex /a/i', RE('a', 'i')],
    ['null', NUL], ['false', BOOL(false)], ['NaN', D('NaN')], ['-Infinity', D('-Infinity')], ['Infinity', D('Infinity')],
    ['double 0.1', D(0.1)], ['t-max', T('9999-12-31T23:59:59.999999Z')], ['epoch', T('1970-01-01T00:00:00Z')], ['""', S('')],
    ['bytes ""', BY([])], ['bytes ff×20', BY(Array(20).fill(255))], ['bytes ff×40', BY(Array(40).fill(255))],
    ['reference c/a', R('c/a')], ['reference zzzz', R('projects/zzzz-aruma-ref-other/databases/(default)/documents/c/a')],
    ['geopoint(-90,-180)', G(-90, -180)], ['geopoint(90,180)', G(90, 180)], ['[]', A()], ['vector [1]', VEC(1)], ['{}', M({})],
    ['{"\\uffff": 1}', M({ '￿': I(1) })],
  ];
  const ops = ['GREATER_THAN', 'GREATER_THAN_OR_EQUAL', 'LESS_THAN', 'LESS_THAN_OR_EQUAL'];
  await pool(operands.flatMap(([label, val]) => ops.map((op) => [label, val, op])), 6, async ([label, val, op]) => {
    await probe(`v ${op} ${label}`, FF(op, val));
  });
  log('.');
  for (const [label, val] of [['MinKey', MINK], ['MaxKey', MAXK], ['int32 1', I32(1)], ['decimal "NaN"', DEC('NaN')], ['decimal "1.5"', DEC('1.5')], ['null', NUL], ['NaN', D('NaN')]]) {
    await probe(`v NOT_EQUAL ${label}`, FF('NOT_EQUAL', val));
  }
  await probe('v IN [MinKey, MaxKey]', FF('IN', A(MINK, MAXK)));
  await probe('v IN [decimal "NaN"]', FF('IN', A(DEC('NaN'))));
  await probe('v NOT_IN [MinKey, int32 1]', FF('NOT_IN', A(MINK, I32(1))));
  for (const op of ['IS_NAN', 'IS_NOT_NAN', 'IS_NULL', 'IS_NOT_NULL']) {
    await probe(`v ${op}`, { unaryFilter: { op, field: { fieldPath: 'v' } } });
  }
  for (const [label, val] of [['int32 1', I32(1)], ['int 1', I(1)], ['MinKey', MINK], ['decimal "1"', DEC('1')]]) {
    await probe(`v ARRAY_CONTAINS ${label}`, FF('ARRAY_CONTAINS', val));
  }
  await probe('v ARRAY_CONTAINS_ANY [int32 1, MinKey]', FF('ARRAY_CONTAINS_ANY', A(I32(1), MINK)));
  await probe('v EQUAL {__int__: "x"}', FF('EQUAL', M({ __int__: S('x') })));
  await probe('v EQUAL {__int__: 1, b: 2}', FF('EQUAL', M({ __int__: I(1), b: I(2) })));
  await probe('v EQUAL decimal "+1.5"', FF('EQUAL', DEC('+1.5')));
  await probe('v GREATER_THAN decimal "abc"', FF('GREATER_THAN', DEC('abc')));
  await probe('v EQUAL {__bson__: 1}', FF('EQUAL', M({ __bson__: I(1) })));
  filters.sort((a, b) => (a.label < b.label ? -1 : a.label > b.label ? 1 : 0));

  // Documents whose own fields carry a BSON key.
  const documents = [];
  for (const [id, fields] of [['top-int', { __int__: I(1) }], ['top-min', { __min__: NUL }]]) {
    documents.push({ id, fields: forCorpus(fields), write: await commitOne(`${ORD}/${id}`, fields) });
  }

  log(' size ');
  const size = [];
  const shapes = [
    ['min', MINK], ['max', MAXK], ['int32', I32(7)], ['decimal-1.5', DEC('1.5')], ['decimal-34-digits', DEC('1234567890123456789012345678901234')],
    ['decimal-nan', DEC('NaN')], ['bson-timestamp', BTS(1, 2)], ['binary-3', BIN([1, 2, 3])], ['binary-1', BIN([5])],
    ['oid', OID('507f1f77bcf86cd799439011')], ['regex-ab-i', RE('ab', 'i')], ['regex-empty', RE('', '')],
    ['map-a-int32', M({ a: I32(1) })], ['array-int32-min', A(I32(1), MINK)],
  ];
  // The storage size as a plain map would count it.
  const plain = (val) => {
    const [k, x] = Object.entries(val)[0];
    switch (k) {
      case 'nullValue': case 'booleanValue': return 1;
      case 'integerValue': case 'doubleValue': case 'timestampValue': return 8;
      case 'stringValue': return Buffer.byteLength(x) + 1;
      case 'bytesValue': return Buffer.from(x, 'base64').length;
      case 'arrayValue': return (x.values || []).reduce((n, y) => n + plain(y), 0) || 1;
      case 'mapValue': return Object.entries(x.fields || {}).reduce((n, [name, y]) => n + Buffer.byteLength(name) + 1 + plain(y), 0) || 1;
      default: throw new Error(`plain ${k}`);
    }
  };
  const nameSize = (segs) => segs.reduce((n, s) => n + Buffer.byteLength(s) + 1, 0) + 16;
  await pool(shapes, 3, async ([id, val]) => {
    const rel = `${SIZE}/${id}`;
    const s1 = 'x'.repeat(600000);
    const without = nameSize(rel.split('/')) + (2 + 1) + (s1.length + 1) + (2 + 1) + 1 + (1 + 1) + 32;
    const ours = plain(val);
    const fill = LIMIT + 100 - without - ours;
    const w = await commitOne(rel, { s1: S(s1), s2: S('y'.repeat(fill)), t: val });
    const m = /its size \(([\d,]+) bytes\)/.exec(w.error?.message || '');
    const prod = m ? Number(m[1].replaceAll(',', '')) : null;
    size.push({
      id: `size.${id}`,
      value: forCorpus(val),
      fillerBytes: [s1.length, fill],
      plainMapValueSize: ours,
      write: w,
      productionDocumentSize: prod,
      productionValueSize: prod === null ? null : prod - without - fill,
    });
    log('.');
  });
  size.sort((a, b) => (a.id < b.id ? -1 : 1));

  // Index budget: [x×k, shape, false] against [x×k, shape, true]. With the
  // shape spending s bytes of the 1500, the booleans count while k+1+s < 1500,
  // so the pair sorts apart at k = 1498-s and ties at k = 1499-s.
  log(' budget ');
  const truncation = [];
  const budgetShapes = [['min', MINK], ['max', MAXK], ['int32', I32(7)], ['decimal-1.5', DEC('1.5')], ['bson-timestamp', BTS(1, 2)],
    ['binary-3', BIN([1, 2, 3])], ['oid', OID('507f1f77bcf86cd799439011')], ['regex-ab-i', RE('ab', 'i')]];
  const pairs = [];
  for (const [id, val] of budgetShapes) {
    const s = size.find((x) => x.id === `size.${id}`)?.productionValueSize;
    if (s == null) continue;
    for (const k of [1498 - s, 1499 - s]) {
      pairs.push([`tr-${id}-${k}`, k, val]);
    }
  }
  await pool(pairs, 6, async ([pid, k, val]) => {
    const docs = {};
    for (const b of [false, true]) {
      const did = `${pid}-${b ? 'b' : 'a'}`;
      docs[did] = await commitOne(`${ORD}/${did}`, { v: A(S('x'.repeat(k)), val, BOOL(b)) });
    }
    const a = `${ROOT}/${ORD}/${pid}-a`;
    const r = await call('POST', `${DB}/documents:batchGet`, { documents: [a] });
    const sv = resend(r.body.find((x) => x.found)?.found.fields.v);
    const cls = await runQuery({ orderBy: ASC, startAt: { values: [sv], before: true }, endAt: { values: [sv], before: false } });
    truncation.push({ id: pid, prefixBytes: k, shape: forCorpus(val), writes: docs, classOfA: cls.error ? cls : cls.ids });
    log('.');
  });
  truncation.sort((a, b) => (a.id < b.id ? -1 : 1));

  return {
    values,
    orderAsc,
    orderDesc,
    equal: sortKeys(equal),
    cursorClasses: sortKeys(classes),
    filters,
    documents,
    size,
    truncation,
    notes: [
      `Each value is the field v of ${ORD}/{id}; ids are document ids there. stored is the value production returned on batchGet. Doubles are proto3 JSON numbers or "NaN", "Infinity", "-Infinity", "-0".`,
      'orderAsc / orderDesc: runQuery orderBy v over the values (run before the tr-* documents of truncation were written), ties by __name__ in the same direction. equal[id]: where v EQUAL (stored value of id). cursorClasses[id]: orderBy v ASC startAt(stored, before) endAt(stored, after).',
      'filters: other operators, with the request and the ids returned.',
      `documents: documents of ${ORD} whose own top-level field is named with a BSON key.`,
      `size: a document of ${SIZE} holding s1 (600000 bytes), s2 (filler) and t (the shape), sized so that the storage-size formula, counting the shape as a plain map (plainMapValueSize), puts it 100 bytes over 1 MiB. productionValueSize is the shape's size by production's refusal message less the rest of the document by the formula.`,
      'truncation: [x×k, shape, false] (…-a) and [x×k, shape, true] (…-b) in the order collection, at k = 1498 - productionValueSize and 1499 - productionValueSize; classOfA lists the documents that tie with …-a.',
      'BsonBinaryData travels with its subtype as the first byte of __binary__ (JS SDK); bin-80-01 is subtype 128 with data 01.',
    ],
  };
}

// ---- values-mask-vector ----

async function maskVector() {
  const doc = { vec: VEC(1, 2), b: I32(1), d: DEC('1.5'), p: M({ a: I(1) }) };
  const rel = `${MASK}/m`;
  const w = await commitOne(rel, doc);
  if (w.http !== 200) throw new Error(`mask setup failed: ${JSON.stringify(w)}`);
  const reads = [];
  for (const p of [['vec.value'], ['vec.__type__'], ['vec'], ['vec.value', 'vec.__type__'], ['b.__int__'], ['d.__decimal128__'], ['p.a'],
    ['p.__x__'], ['__int__'], ['__type__'], ['p.__name__'], ['b'], ['b.x'], ['__min__.a']]) {
    reads.push({ maskFieldPaths: p, ...(await getDoc(rel, p)) });
    log('.');
  }
  const updates = [];
  const upd = [
    ['vec.value replaced by [3.0]', { vec: M({ value: A(D(3)) }) }, ['vec.value']],
    ['vec.value replaced by [int 1]', { vec: M({ value: A(I(1)) }) }, ['vec.value']],
    ['vec.value replaced by []', { vec: M({ value: A() }) }, ['vec.value']],
    ['vec.value deleted', {}, ['vec.value']],
    ['vec.__type__ deleted', {}, ['vec.__type__']],
    ['vec.__type__ replaced by "x"', { vec: M({ __type__: S('x') }) }, ['vec.__type__']],
    ['vec.extra added', { vec: M({ extra: I(1) }) }, ['vec.extra']],
    ['b.__int__ replaced by 2', { b: I32(2) }, ['b.__int__']],
    ['b.__int__ replaced by "x"', { b: M({ __int__: S('x') }) }, ['b.__int__']],
    ['b.x added beside __int__', { b: M({ x: I(1) }) }, ['b.x']],
    ['d.__decimal128__ deleted', {}, ['d.__decimal128__']],
    ['p.__int__ added to a plain map', { p: M({ __int__: I(1) }) }, ['p.__int__']],
  ];
  for (const [desc, fields, m] of upd) {
    await commitOne(rel, doc);
    const res = await commitOne(rel, fields, { mask: m });
    const after = await getDoc(rel);
    updates.push({ description: desc, fields: forCorpus(fields), mask: m, write: res, after: after.fields });
    log('.');
  }
  return {
    document: forCorpus(doc),
    reads,
    updates,
    notes: [`The document ${MASK}/m holds the fields of document; reads are GETs with mask.fieldPaths; updates rewrite it, commit one update with updateMask.fieldPaths, and read it back (after).`],
  };
}

// ---- cleanup ----

async function cleanup() {
  const names = [...written];
  const res = [];
  for (let i = 0; i < names.length; i += 400) {
    const r = await rest('POST', `${ROOT}:commit`, { writes: names.slice(i, i + 400).map((n) => { assertScoped(n); return { delete: n }; }) });
    res.push(r.http);
    if (r.http !== 200) throw new Error(`cleanup commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
  const sweep = await rest('POST', `${ROOT}:runQuery`, {
    structuredQuery: {
      from: [{ allDescendants: true }],
      where: { compositeFilter: { op: 'AND', filters: [
        { fieldFilter: { field: { fieldPath: '__name__' }, op: 'GREATER_THAN_OR_EQUAL', value: { referenceValue: `${ROOT}/${ORD}/${MIN_ID}` } } },
        { fieldFilter: { field: { fieldPath: '__name__' }, op: 'LESS_THAN', value: { referenceValue: `${ROOT}/aruma-ref-bsoo/${MIN_ID}` } } },
      ] } },
      select: { fields: [{ fieldPath: '__name__' }] },
      orderBy: [{ field: { fieldPath: '__name__' }, direction: 'ASCENDING' }],
      limit: 100,
    },
  });
  const remaining = (Array.isArray(sweep.body) ? sweep.body : []).filter((x) => x.document).map((x) => x.document.name.replace(`${ROOT}/`, ''));
  const lists = {};
  for (const t of TOPS) {
    const l = await rest('GET', `${ROOT}/${t}?showMissing=true&pageSize=300`);
    lists[t] = (l.body.documents || []).map((d) => d.name.replace(`${ROOT}/`, ''));
  }
  const ok = sweep.http === 200 && remaining.length === 0 && Object.values(lists).every((l) => l.length === 0);
  return {
    ok,
    deletedCount: names.length,
    deleted: names.map((n) => compact(n.replace(`${ROOT}/`, ''), 200)).sort(),
    commitHttp: res,
    sweep: { http: sweep.http, query: `kindless allDescendants, __name__ in [${ORD}/${MIN_ID}, aruma-ref-bsoo/${MIN_ID})`, remaining },
    listsShowMissing: lists,
  };
}

// ---- main ----

async function main() {
  const dbr = await rest('GET', DB);
  if (dbr.http !== 200) throw new Error(`GET ${DB}: HTTP ${dbr.http}`);
  const target = `${PROJECT} (default): ${dbr.body.type}, ${dbr.body.databaseEdition}, ${dbr.body.concurrencyMode}, ${dbr.body.locationId}`;
  const ver = versions({ api: 'firestore.googleapis.com REST v1' });
  const results = {};
  const started = new Date().toISOString();
  try {
    log('bson ');
    results['values-bson.json'] = await bson();
    log('\nmask ');
    results['values-mask-vector.json'] = await maskVector();
  } finally {
    log('\ncleanup ');
    const cl = await cleanup();
    results['values-bson-cleanup.json'] = { cleanup: cl };
    log(cl.ok ? 'verified\n' : 'NOT VERIFIED\n');
    if (!cl.ok) process.exitCode = 2;
  }
  const finished = new Date().toISOString();
  for (const [file, body] of Object.entries(results)) {
    const header = {
      oracle: 'production',
      target,
      generator: GENERATOR,
      recordedAt: `${started} .. ${finished}`,
      versions: ver,
      notes: [
        'Auth: the firebase CLI login (a project owner), so security rules do not apply.',
        'Only values, document ids, HTTP statuses and error statuses and messages are recorded; they are exact.',
        ...(body.notes || []),
      ],
    };
    const { notes, ...payload } = body;
    writeCorpus(file, header, payload);
  }
}

main().catch((e) => {
  console.error('record-bson failed:', e.message);
  process.exit(1);
});
