// F1 production recordings for the Document Database's value model (plan
// section 6, F1; internal/services/documents/value):
//
//   values-order.json     one document per edge value (every type: NaN, ±0,
//                         ±Inf, int64 extremes, doubles near 2^53 and 2^63,
//                         timestamps at microsecond precision, unicode, empty
//                         and 1500-byte strings and bytes, references,
//                         geopoints, arrays, vectors, maps), each stored in the
//                         field v of one collection; the value production
//                         stored; orderBy v ascending and descending; and the
//                         class of documents a startAt/endAt cursor on each
//                         value selects (the documents that sort equal to it)
//   values-equality.json  ==, in, !=, not-in, the unary IS_NAN / IS_NULL
//                         filters, array-contains(-any) and the four range
//                         operators on the same documents
//   values-size.json      the storage size production computes for a value of
//                         each type (read from the "exceeds the maximum
//                         allowed size" message of a document one hundred
//                         bytes over by the formula), and the per-value cap
//                         on bytes, maps and arrays
//   values-validate.json  what a write may not hold: nesting depth, reserved
//                         and oversized field names, nested arrays, malformed
//                         vectors, geopoints, timestamps and references
//   values-mask.json      field paths in a read mask and an update mask
//   values-cleanup.json   the deletion of everything written, verified
//
// REST only (firestore.googleapis.com/v1): a value travels as proto3 JSON, so
// -0.0 is sent as the literal -0.0 and read back with its sign.
//
//   node record-values.mjs [--only order,equality,size,validate,mask]
//
// Data is written ONLY under top-level collections whose id starts with
// aruma-ref (assertScoped refuses anything else), everything written is deleted
// at the end, and the deletion is verified by a kindless query over every
// collection id from "aruma-ref-values" up to "aruma-ref-valuet" (other
// recorders may be writing their own aruma-ref* collections meanwhile) and by
// showMissing lists of each collection used. Single-field ordering uses
// the automatic indexes; nothing creates or changes an index, field override,
// rule, TTL policy or project setting.
import {
  PROJECT, DB, ROOT, rest, versions, compact, writeCorpus, assertScoped,
} from './lib.mjs';

const args = process.argv.slice(2);
const onlyArg = args.includes('--only') ? args[args.indexOf('--only') + 1].split(',') : null;
const want = (t) => !onlyArg || onlyArg.includes(t);

const GENERATOR = 'test/oracle/documents/production/record-values.mjs';
const MIN_ID = '__id-9223372036854775808__';
const ORD = 'aruma-ref-values'; // one document per edge value, field v
const SIZE = 'aruma-ref-values-size';
const BAD = 'aruma-ref-values-bad';
const MASK = 'aruma-ref-values-mask';
const TOPS = [ORD, SIZE, BAD, MASK];
const LIMIT = 1048576;
const log = (s) => process.stderr.write(s);

// ---- values in proto3 JSON ----

// -0.0 cannot be written by JSON.stringify; it travels as this marker and the
// request text replaces it with the literal -0.0. In the corpus it is "-0".
const NEG0 = '@@negative-zero@@';
const send = (x) => JSON.stringify(x).replaceAll(`"${NEG0}"`, '-0.0');
const forCorpus = (x) => JSON.parse(JSON.stringify(x ?? null).replaceAll(`"${NEG0}"`, '"-0"'));
// signed marks every -0 a response holds as "-0", which JSON would lose.
function signed(x) {
  if (Object.is(x, -0)) return '-0';
  if (Array.isArray(x)) return x.map(signed);
  if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, signed(y)]));
  return x;
}
// resend turns a corpus value back into what is sent.
function resend(x) {
  if (x === '-0') return NEG0;
  if (Array.isArray(x)) return x.map(resend);
  if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, resend(y)]));
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

const x1500 = 'x'.repeat(1500);
const x1499 = 'x'.repeat(1499);
const x1498 = 'x'.repeat(1498);

// The edge values, one document each in ORD. The ids are chosen so that a
// tie broken by __name__ is visible: within a group they run against the
// expected order where that is cheap.
const VALUES = [
  ['null', NUL],
  ['bool-false', BOOL(false)],
  ['bool-true', BOOL(true)],
  // numbers
  ['n-nan', D('NaN')],
  ['n-neg-inf', D('-Infinity')],
  ['n-d-neg-max', D(-1.7976931348623157e308)],
  ['n-d-below-neg-2p63', D(-9223372036854777856)],
  ['n-d-neg-2p63', D(-9223372036854775808)],
  ['n-i-min', I('-9223372036854775808')],
  ['n-i-min-plus-1', I('-9223372036854775807')],
  ['n-d-neg-2p53', D(-9007199254740992)],
  ['n-i-neg-2p53-minus-1', I('-9007199254740993')],
  ['n-d-neg-1.5', D(-1.5)],
  ['n-i-neg-1', I(-1)],
  ['n-d-neg-1', D(-1)],
  ['n-d-neg-min', D(-5e-324)],
  ['n-d-neg-zero', D(-0)],
  ['n-i-zero', I(0)],
  ['n-d-zero', D(0)],
  ['n-d-min', D(5e-324)],
  ['n-d-0.5', D(0.5)],
  ['n-i-1', I(1)],
  ['n-d-1', D(1)],
  ['n-d-1.5', D(1.5)],
  ['n-i-2', I(2)],
  ['n-i-2p53', I('9007199254740992')],
  ['n-d-2p53', D(9007199254740992)],
  ['n-i-2p53-plus-1', I('9007199254740993')],
  ['n-d-2p53-plus-2', D(9007199254740994)],
  ['n-i-2p53-plus-2', I('9007199254740994')],
  ['n-d-below-2p63', D(9223372036854774784)],
  ['n-i-max-minus-1', I('9223372036854775806')],
  ['n-i-max', I('9223372036854775807')],
  ['n-d-2p63', D(9223372036854775808)],
  ['n-d-1e300', D(1e300)],
  ['n-d-max', D(1.7976931348623157e308)],
  ['n-pos-inf', D('Infinity')],
  // timestamps
  ['t-min', T('0001-01-01T00:00:00Z')],
  ['t-before-epoch', T('1969-12-31T23:59:59.999999Z')],
  ['t-epoch', T('1970-01-01T00:00:00Z')],
  ['t-epoch-999ns', T('1970-01-01T00:00:00.000000999Z')],
  ['t-epoch-1us', T('1970-01-01T00:00:00.000001Z')],
  ['t-nanos', T('2026-01-01T00:00:00.123456789Z')],
  ['t-micros', T('2026-01-01T00:00:00.123456Z')],
  ['t-micros-plus-1', T('2026-01-01T00:00:00.123457Z')],
  ['t-max', T('9999-12-31T23:59:59.999999Z')],
  // strings, UTF-8 byte order against UTF-16 order
  ['s-empty', S('')],
  ['s-nul', S('\u0000')],
  ['s-A', S('A')],
  ['s-a', S('a')],
  ['s-a-nul', S('a\u0000')],
  ['s-ab', S('ab')],
  ['s-b', S('b')],
  ['s-e-acute', S('é')],
  ['s-u-e000', S('')],
  ['s-u-ffff', S('￿')],
  ['s-emoji', S('\u{1F600}')],
  // strings around the 1500-byte index truncation
  ['s-long-z-1499-x-e-circ', S(`${x1499}ê`)],
  ['s-long-y-1499-x-e-acute', S(`${x1499}é`)],
  ['s-long-x-1500-x-b', S(`${x1500}b`)],
  ['s-long-w-1500-x-a', S(`${x1500}a`)],
  ['s-long-v-1500-x', S(x1500)],
  ['s-long-u-1499-x', S(x1499)],
  // where the truncation cuts: 1499 or 1500 bytes, on a byte or a character
  ['s-t-a-1498-x', S(x1498)],
  ['s-t-b-1498-x-y', S(`${x1498}y`)],
  ['s-t-c-1498-x-ya', S(`${x1498}ya`)],
  ['s-t-d-1498-x-yb', S(`${x1498}yb`)],
  ['s-t-e-1498-x-ba', S(`${x1498}ba`)],
  ['s-t-f-1498-x-e-acute', S(`${x1498}\u00e9`)],
  ['s-t-g-1498-x-e-acute-a', S(`${x1498}\u00e9a`)],
  ['s-t-h-1497-x-emoji', S(`${'x'.repeat(1497)}\u{1F600}`)],
  ['s-t-i-1497-x-y', S(`${'x'.repeat(1497)}y`)],
  // bytes
  ['b-empty', BY([])],
  ['b-00', BY([0])],
  ['b-0000', BY([0, 0])],
  ['b-01', BY([1])],
  ['b-ff', BY([255])],
  ['b-ff00', BY([255, 0])],
  ['b-long-x-1500-b', BY(Buffer.from(`${x1500}b`))],
  ['b-long-w-1500-a', BY(Buffer.from(`${x1500}a`))],
  ['b-long-v-1500', BY(Buffer.from(x1500))],
  ['b-t-a-1499-a', BY(Buffer.from(`${x1499}a`))],
  ['b-t-b-1499-ab', BY(Buffer.from(`${x1499}ab`))],
  ['b-t-c-1499-ac', BY(Buffer.from(`${x1499}ac`))],
  // references: segment-wise against whole-string order, numeric ids
  ['r-c-A', R('c/A')],
  ['r-c-a', R('c/a')],
  ['r-c-a-s-x', R('c/a/s/x')],
  ['r-c-b', R('c/b')],
  ['r-c-dash-b', R('c-b/x')],
  ['r-c-id-10', R('c/__id10__')],
  ['r-c-id-5', R('c/__id5__')],
  ['r-c-id-neg-1', R('c/__id-1__')],
  ['r-long-a', R(`c/${'i'.repeat(1490)}/s/a`)],
  ['r-long-b', R(`c/${'i'.repeat(1490)}/s/b`)],
  ['r-mid-a', R(`c/${'i'.repeat(1470)}/s/a`)],
  ['r-mid-b', R(`c/${'i'.repeat(1470)}/s/b`)],
  // which __id…__ forms are numeric ids
  ['r-c-id-05', R('c/__id05__')],
  ['r-c-id-neg-0', R('c/__id-0__')],
  ['r-c-id-plus-5', R('c/__id+5__')],
  ['r-c-id-empty', R('c/__id__')],
  ['r-c-id-min', R('c/__id-9223372036854775808__')],
  ['r-c-id-max', R('c/__id9223372036854775807__')],
  ['r-c-id-overflow', R('c/__id9223372036854775808__')],
  ['r-other-db', R(`projects/${PROJECT}/databases/aruma-ref-other/documents/c/a`)],
  ['r-other-project', R('projects/aruma-ref-other-project/databases/(default)/documents/c/a')],
  // geopoints
  ['g-s90-w180', G(-90, -180)],
  ['g-s90-e180', G(-90, 180)],
  ['g-s0.5-0', G(-0.5, 0)],
  ['g-0-0', G(0, 0)],
  ['g-0-1', G(0, 1)],
  ['g-1-w180', G(1, -180)],
  ['g-n90-e180', G(90, 180)],
  // arrays
  ['a-empty', A()],
  ['a-null', A(NUL)],
  ['a-false', A(BOOL(false))],
  ['a-nan', A(D('NaN'))],
  ['a-i-1', A(I(1))],
  ['a-d-1', A(D(1))],
  ['a-i-1-i-2', A(I(1), I(2))],
  ['a-i-1-s-a', A(I(1), S('a'))],
  ['a-i-2', A(I(2))],
  ['a-s-a', A(S('a'))],
  ['a-map-empty', A(M({}))],
  ['a-map-a-1', A(M({ a: I(1) }))],
  ['a-neg-zero', A(D(-0))],
  ['a-zero', A(D(0))],
  ['a-long-a', A(S('x'.repeat(1000)), S('a'))],
  ['a-long-b', A(S('x'.repeat(1000)), S('b'))],
  ['a-elem-a', A(S(`${x1500}a`))],
  ['a-elem-b', A(S(`${x1500}b`))],
  // vectors: by length, then element-wise?
  ['v-1', VEC(1)],
  ['v-2', VEC(2)],
  ['v-1-2', VEC(1, 2)],
  ['v-3-1', VEC(3, 1)],
  ['v-neg1-5', VEC(-1, 5)],
  ['v-0-0-0', VEC(0, 0, 0)],
  ['v-1-2-3', VEC(1, 2, 3)],
  ['v-nan', VEC('NaN')],
  ['v-neg-zero', VEC(-0)],
  ['v-zero', VEC(0)],
  // maps
  ['m-empty', M({})],
  ['m-a-null', M({ a: NUL })],
  ['m-a-i-1', M({ a: I(1) })],
  ['m-a-d-1', M({ a: D(1) })],
  ['m-a-1-b-1', M({ a: I(1), b: I(1) })],
  ['m-a-1-c-1', M({ a: I(1), c: I(1) })],
  ['m-a-2', M({ a: I(2) })],
  ['m-b-0', M({ b: I(0) })],
  ['m-a-map', M({ a: M({}) })],
  ['m-key-A', M({ A: I(1) })],
  ['m-key-e-acute', M({ 'é': I(1) })],
  ['m-key-u-e000', M({ '': I(1) })],
  ['m-key-emoji', M({ '\u{1F600}': I(1) })],
  ['m-key-u-ffff', M({ '￿': I(1) })],
  ['m-neg-zero', M({ a: D(-0) })],
  ['m-zero', M({ a: D(0) })],
  ['m-long-a', M({ a: S('x'.repeat(1000)), b: S('a') })],
  ['m-long-b', M({ a: S('x'.repeat(1000)), b: S('b') })],
  ['m-elem-a', M({ a: S(`${x1500}a`) })],
  ['m-elem-b', M({ a: S(`${x1500}b`) })],
  // the map shapes the SDKs use for Enterprise BSON types (JS 4.18): plain
  // maps on Standard, or refused?
  ['m-bson-int', M({ __int__: I(1) })],
  ['m-bson-min', M({ __min__: NUL })],
  ['m-bson-max', M({ __max__: NUL })],
  ['m-bson-oid', M({ __oid__: S('507f1f77bcf86cd799439011') })],
  ['m-bson-regex', M({ __regex__: M({ pattern: S('a'), options: S('i') }) })],
  ['m-bson-decimal', M({ __decimal128__: S('1.5') })],
  ['m-bson-timestamp', M({ __request_timestamp__: M({ seconds: I(1), increment: I(2) }) })],
  ['m-bson-binary', M({ __binary__: BY([1, 2]) })],
  ['m-type-string', M({ __type__: S('x') })],
  ['m-value-only', M({ value: A(D(1)) })],
  ...truncationPairs(),
];

// truncationPairs gives, for each kind of content, two values that differ
// only at one late position, on both sides of the point where production's
// index entry stops (found by bisecting): at k they sort apart, at k+1 they
// tie. The point follows the storage-size formula as a 1500-byte budget.
function truncationPairs() {
  const xs = (k) => 'x'.repeat(k);
  const zeros = (k) => Array(k).fill(0);
  const fams = [
    ['string', [1498, 1499], (k, c) => S(xs(k) + c)],
    ['bytes', [1499, 1500], (k, c) => BY(Buffer.from(xs(k) + c))],
    ['array-second', [497, 498], (k, c) => A(S(xs(1000)), S(xs(k) + c))],
    ['array-third', [496, 497], (k, c) => A(S(xs(1000)), S(xs(k)), S(c))],
    ['map-value', [493, 494], (k, c) => M({ a: S(xs(1000)), b: S(xs(k) + c) })],
    ['map-key', [494, 495], (k, c) => M({ a: S(xs(1000)), [`b${xs(k)}${c}`]: S('') })],
    ['ref-last-segment', [1477, 1478], (k, c) => R(`c/${'i'.repeat(k)}/s/${c}`)],
    ['ref-id', [1480, 1481], (k, c) => R(`c/${'i'.repeat(k)}${c}`)],
    ['int-after', [1491, 1492, 1498, 1499], (k, c) => A(S(xs(k)), I(c === 'a' ? 1 : 2))],
    ['bool-after', [1498, 1499], (k, c) => A(S(xs(k)), BOOL(c === 'b'))],
    ['null-or-false-after', [1498, 1499], (k, c) => A(S(xs(k)), c === 'a' ? NUL : BOOL(false))],
    ['empty-string-after', [1498, 1499], (k, c) => A(S(xs(k)), S(c === 'a' ? '' : 'a'))],
    ['utf8-char', [1498, 1499], (k, c) => S(xs(k) + (c === 'a' ? '\u00e9' : '\u00ea'))],
    ['vector-last', [186, 187], (k, c) => VEC(...zeros(k), c === 'a' ? 1 : 2)],
    ['vector-in-array', [1491, 1492], (k, c) => A(S(xs(k)), VEC(c === 'a' ? 1 : 2))],
    ['empty-array-then-bool', [1498], (k, c) => A(S(xs(k)), A(), BOOL(c === 'b'))],
    ['empty-map-then-bool', [1498], (k, c) => A(S(xs(k)), M({}), BOOL(c === 'b'))],
    ['empty-arrays', [1498], (k, c) => (c === 'a' ? A(S(xs(k)), A()) : A(S(xs(k)), A(), A()))],
    ['empty-array-or-null', [1498, 1499], (k, c) => A(S(xs(k)), c === 'a' ? A() : NUL)],
    ['empty-array-or-end', [1499], (k, c) => (c === 'a' ? A(S(xs(k)), A()) : A(S(xs(k))))],
    ['vector-length-in-array', [1498, 1499], (k, c) => A(S(xs(k)), c === 'a' ? VEC(1) : VEC(1, 2))],
  ];
  const out = [];
  for (const [fam, ks, f] of fams) {
    for (const k of ks) for (const c of ['a', 'b']) out.push([`tr-${fam}-${k}-${c}`, f(k, c)]);
  }
  return out;
}

// ---- recording helpers ----

const written = new Set();

// call sends a REST request whose body may hold NEG0, and returns {http, body}
// with every -0 of the response marked.
async function call(method, path, body) {
  const r = await rest(method, path, body === undefined ? undefined : send(body));
  return { http: r.http, body: signed(r.body) };
}

function errorOf(body) {
  const arr = Array.isArray(body) ? body : [body];
  const e = arr.find((x) => x && x.error)?.error;
  return e ? { status: e.status, message: e.message } : undefined;
}

// commitOne writes one document (an update with the given fields, and an
// optional update mask) and tracks it for the cleanup.
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

// runQuery runs a structured query on ORD and returns the ids it returned, in
// order, or the error.
async function runQuery(sq) {
  const r = await call('POST', `${ROOT}:runQuery`, { structuredQuery: { from: [{ collectionId: ORD }], ...sq } });
  const err = errorOf(r.body);
  return err ? { http: r.http, error: err } : { http: r.http, ids: docIds(r.body) };
}

// pool runs fn over items with a few requests in flight.
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

// ---- values-order ----

let stored = new Map(); // id -> stored value (corpus form)

async function order() {
  const values = [];
  await pool(VALUES, 6, async ([id, val]) => {
    const w = await commitOne(`${ORD}/${id}`, { v: val });
    values.push({ id, sent: forCorpus(val), write: w });
    log('.');
  });
  values.sort((a, b) => VALUES.findIndex((x) => x[0] === a.id) - VALUES.findIndex((x) => x[0] === b.id));
  // Read back what production stored.
  const names = values.filter((x) => x.write.http === 200).map((x) => `${ROOT}/${ORD}/${x.id}`);
  for (let i = 0; i < names.length; i += 50) {
    const r = await call('POST', `${DB}/documents:batchGet`, { documents: names.slice(i, i + 50) });
    for (const x of r.body) {
      if (!x.found) continue;
      const id = x.found.name.slice(`${ROOT}/${ORD}/`.length);
      stored.set(id, x.found.fields.v);
    }
  }
  for (const x of values) if (stored.has(x.id)) x.stored = stored.get(x.id);
  log(' read ');
  const asc = await runQuery({ orderBy: ASC });
  const desc = await runQuery({ orderBy: DESC });
  // The class of each value: startAt(v) .. endAt(v) on orderBy v.
  const ids = [...stored.keys()];
  const classes = {};
  await pool(ids, 6, async (id) => {
    const val = resend(stored.get(id));
    const r = await runQuery({ orderBy: ASC, startAt: { values: [val], before: true }, endAt: { values: [val], before: false } });
    classes[id] = r.error ? r : r.ids;
    log('.');
  });
  return {
    values,
    orderAsc: asc,
    orderDesc: desc,
    cursorClasses: Object.fromEntries(Object.keys(classes).sort().map((k) => [k, classes[k]])),
    notes: [
      `Each value is the field v of ${ORD}/{id}; ids are document ids in that collection. stored is the value production returned on batchGet.`,
      'Doubles are proto3 JSON numbers, or the strings "NaN", "Infinity", "-Infinity", and "-0" for negative zero (sent as the literal -0.0).',
      'orderAsc / orderDesc: runQuery orderBy v (ties fall to __name__ in the same direction). cursorClasses[id]: the ids runQuery orderBy v ASC startAt(stored v, before) endAt(stored v, after) returns, which are the documents that sort equal to it.',
    ],
  };
}

// ---- values-equality ----

async function equality() {
  if (!stored.size) throw new Error('equality needs the order topic');
  const ids = [...stored.keys()];
  const eq = {};
  const inq = {};
  await pool(ids, 6, async (id) => {
    const val = resend(stored.get(id));
    eq[id] = await runQuery({ where: FF('EQUAL', val) });
    inq[id] = await runQuery({ where: FF('IN', A(val)) });
    log('.');
  });
  const probe = async (label, where) => ({ label, request: forCorpus(where), ...(await runQuery({ where })) });
  const others = [];
  const reps = [
    ['null', NUL], ['false', BOOL(false)], ['NaN', D('NaN')], ['-Infinity', D('-Infinity')], ['int 0', I(0)], ['double -0.0', D(-0)],
    ['Infinity', D('Infinity')], ['epoch', T('1970-01-01T00:00:00Z')], ['""', S('')], ['bytes ""', BY([])],
    ['reference c/a', R('c/a')], ['geopoint(0,0)', G(0, 0)], ['[]', A()], ['vector [1]', VEC(1)], ['vector []', VEC()], ['{}', M({})],
  ];
  for (const [label, val] of reps) {
    for (const op of ['GREATER_THAN', 'GREATER_THAN_OR_EQUAL', 'LESS_THAN', 'LESS_THAN_OR_EQUAL']) {
      others.push(await probe(`v ${op} ${label}`, FF(op, val)));
    }
    log('.');
  }
  for (const [label, val] of [['null', NUL], ['NaN', D('NaN')], ['int 1', I(1)], ['double -0.0', D(-0)], ['"a"', S('a')], ['[int 1]', A(I(1))], ['{}', M({})]]) {
    others.push(await probe(`v NOT_EQUAL ${label}`, FF('NOT_EQUAL', val)));
  }
  for (const [label, val] of [['[int 1]', A(I(1))], ['[null]', A(NUL)], ['[NaN]', A(D('NaN'))], ['[int 1, "a"]', A(I(1), S('a'))]]) {
    others.push(await probe(`v NOT_IN ${label}`, FF('NOT_IN', val)));
  }
  for (const [label, val] of [['[null, int 1]', A(NUL, I(1))], ['[NaN]', A(D('NaN'))], ['[NaN, null]', A(D('NaN'), NUL)]]) {
    others.push(await probe(`v IN ${label}`, FF('IN', val)));
  }
  for (const op of ['IS_NAN', 'IS_NOT_NAN', 'IS_NULL', 'IS_NOT_NULL']) {
    others.push(await probe(`v ${op}`, { unaryFilter: { op, field: { fieldPath: 'v' } } }));
  }
  for (const [label, val] of [['int 1', I(1)], ['double 1.0', D(1)], ['null', NUL], ['NaN', D('NaN')], ['false', BOOL(false)], ['{}', M({})], ['{a: 1}', M({ a: I(1) })]]) {
    others.push(await probe(`v ARRAY_CONTAINS ${label}`, FF('ARRAY_CONTAINS', val)));
  }
  for (const [label, val] of [['[int 1, null]', A(I(1), NUL)], ['[NaN]', A(D('NaN'))], ['["a", false]', A(S('a'), BOOL(false))]]) {
    others.push(await probe(`v ARRAY_CONTAINS_ANY ${label}`, FF('ARRAY_CONTAINS_ANY', val)));
  }
  const sortIds = (o) => Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]]));
  return {
    equal: sortIds(eq),
    in: sortIds(inq),
    filters: others,
    notes: [
      `Documents and value encoding as in values-order.json (collection ${ORD}, field v).`,
      'equal[id]: runQuery where v EQUAL (the stored value of id); in[id]: where v IN [that value]. Each lists the ids returned (in __name__ order), or the error. These are raw requests: the SDKs rewrite == NaN and == null to IS_NAN / IS_NULL, the server sees EQUAL here.',
      'filters: other operators with the request and the ids returned.',
    ],
  };
}

// ---- values-size ----

// Google's storage size (firebase.google.com/docs/firestore/storage-size), as
// production applies it; a vector is counted here as the map it travels as.
function sizeOf(val) {
  const [k, x] = Object.entries(val)[0];
  switch (k) {
    case 'nullValue': case 'booleanValue': return 1;
    case 'integerValue': case 'doubleValue': case 'timestampValue': return 8;
    case 'stringValue': return Buffer.byteLength(x) + 1;
    case 'bytesValue': return Buffer.from(x, 'base64').length;
    case 'referenceValue': return nameSize(x.slice(x.indexOf('/documents/') + '/documents/'.length).split('/'));
    case 'geoPointValue': return 16;
    case 'arrayValue': return (x.values || []).reduce((n, y) => n + sizeOf(y), 0);
    case 'mapValue': return fieldsSize(x.fields || {});
    default: throw new Error(`sizeOf ${k}`);
  }
}
const fieldsSize = (f) => Object.entries(f).reduce((n, [name, y]) => n + Buffer.byteLength(name) + 1 + sizeOf(y), 0);
const nameSize = (segs) => segs.reduce((n, s) => n + Buffer.byteLength(s) + 1, 0) + 16;

async function size() {
  const cases = [];
  const probes = [
    ['null', NUL], ['true', BOOL(true)], ['int', I('-9223372036854775808')], ['double', D(1.5)], ['nan', D('NaN')],
    ['timestamp', T('2026-01-01T00:00:00.123456Z')], ['string', S('héllo \u{1F600}')], ['string-empty', S('')],
    ['bytes', BY([1, 2, 3])], ['bytes-empty', BY([])], ['reference', R('c/a/s/x')], ['reference-other-db', R(`projects/${PROJECT}/databases/aruma-ref-other/documents/c/a`)],
    ['geopoint', G(1, 2)], ['array', A(I(1), S('ab'), NUL)], ['array-empty', A()],
    ['map', M({ a: I(1), bc: M({ d: BOOL(true) }) })], ['map-empty', M({})],
    ['vector-3', VEC(1, 2, 3)], ['vector-1', VEC(1)], ['field-name-unicode', I(1), 'é\u{1F600}'],
  ];
  for (const [id, val, fname = 't'] of probes) {
    const rel = `${SIZE}/${id}`;
    const s1 = 'x'.repeat(600000);
    const without = nameSize(rel.split('/')) + (2 + 1) + (s1.length + 1) + (2 + 1) + 1 + (Buffer.byteLength(fname) + 1) + 32;
    const ours = sizeOf(val);
    const fill = LIMIT + 100 - without - ours;
    const s2 = 'y'.repeat(fill);
    const w = await commitOne(rel, { s1: S(s1), s2: S(s2), [fname]: val });
    const m = /its size \(([\d,]+) bytes\)/.exec(w.error?.message || '');
    const prod = m ? Number(m[1].replaceAll(',', '')) : null;
    cases.push({
      id: `size.${id}`,
      fieldName: fname,
      value: forCorpus(val),
      fillerBytes: [s1.length, s2.length],
      formulaValueSize: ours,
      formulaDocumentSize: without + fill + ours,
      write: { http: w.http, error: w.error && { status: w.error.status, message: w.error.message } },
      productionDocumentSize: prod,
      productionValueSize: prod === null ? null : prod - without - fill,
    });
    log('.');
  }
  // The per-value cap of 1,048,487 bytes on bytes, maps and arrays.
  const cap = [];
  // Short ids: one field of 1,048,488 bytes must still fit the 1 MiB limit,
  // whose name, field and document overhead the cap leaves 89 bytes for.
  const capProbe = async (id, desc, val, fname = 'p') => {
    const rel = `${SIZE}/k${cap.length}`;
    const w = await commitOne(rel, { [fname]: val });
    cap.push({ id: `cap.${id}`, document: rel, description: desc, formulaValueSize: sizeOf(val), formulaDocumentSize: nameSize(rel.split('/')) + Buffer.byteLength(fname) + 1 + sizeOf(val) + 32, value: compact(forCorpus(val)), write: w });
    log('.');
  };
  for (const n of [1048487, 1048488]) await capProbe(`bytes-${n}`, `bytes of ${n} bytes`, BY(Buffer.alloc(n, 120)));
  for (const n of [1048484, 1048485, 1048486]) await capProbe(`map-string-${n}`, `{a: string of ${n} bytes}`, M({ a: S('x'.repeat(n)) }));
  for (const n of [1048485, 1048486, 1048487]) await capProbe(`array-string-${n}`, `[string of ${n} bytes]`, A(S('x'.repeat(n))));
  await capProbe('map-string-1048488', '{a: string of 1048488 bytes}', M({ a: S('x'.repeat(1048488)) }));
  await capProbe('array-string-1048488', '[string of 1048488 bytes]', A(S('x'.repeat(1048488))));
  await capProbe('map-bytes-1048488', '{a: bytes of 1048488 bytes}', M({ a: BY(Buffer.alloc(1048488, 120)) }));
  await capProbe('array-two-strings', '[string of 524240 bytes, string of 524240 bytes]', A(S('x'.repeat(524240)), S('y'.repeat(524240))));
  await capProbe('map-nested', '{a: {b: string of 1048480 bytes}}', M({ a: M({ b: S('x'.repeat(1048480)) }) }));
  return {
    cases,
    cap,
    notes: [
      'size: each document holds s1 (600000 bytes), s2 (filler) and one field of the probed type, sized so that the storage-size formula puts it 100 bytes over the 1 MiB limit. productionDocumentSize is the size in production\'s refusal message; productionValueSize subtracts the rest of the document by the formula (name: each segment + 1, plus 16; field name + 1; string + 1; document + 32).',
      'formulaValueSize counts a vector as the map it travels as ({__type__: "__vector__", value: [doubles]}).',
      'cap: one field per document, around the per-value cap of 1,048,487 bytes. Long strings in requests are compacted (length and SHA-256).',
    ],
  };
}

// ---- values-validate ----

function nestMaps(depth, leaf = I(1)) {
  let x = leaf;
  for (let i = 0; i < depth; i++) x = M({ a: x });
  return x;
}
function nestMixed(depth, leaf = I(1)) {
  let x = leaf;
  for (let i = 0; i < depth; i++) x = i % 2 ? M({ a: x }) : A(x);
  return x;
}

async function validate() {
  const cases = [];
  let n = 0;
  const probe = async (id, desc, fields, opts) => {
    const rel = `${BAD}/c${String(n++).padStart(3, '0')}`;
    const w = await commitOne(rel, fields, opts);
    const c = { id, description: desc, fields: compact(forCorpus(fields)), ...(opts?.mask ? { mask: opts.mask } : {}), write: w };
    if (w.http === 200) c.stored = compact((await getDoc(rel)).fields);
    cases.push(c);
    log('.');
  };
  for (const d of [18, 19, 20, 21]) await probe(`depth.maps_${d}`, `field d holds ${d} nested maps around an integer`, { d: nestMaps(d) });
  for (const d of [19, 20, 21]) await probe(`depth.mixed_${d}`, `field d holds ${d} nested containers (array, map, array, ...) around an integer`, { d: nestMixed(d) });
  await probe('depth.array_outer_21', 'field q holds an array around 20 nested maps around an integer', { q: A(nestMaps(20)) });
  await probe('depth.array_inner_21', 'field q holds 20 nested maps around an array holding an integer', { q: nestMaps(20, A(I(1))) });
  await probe('depth.map_then_mixed_21', 'field q holds a map whose key k holds 20 nested containers (array, map, ...) around an integer', { q: M({ k: nestMixed(20) }) });
  await probe('depth.arrays_only_21', 'field q holds 21 arrays (each directly inside the last) around an integer', { q: (() => { let x = I(1); for (let i = 0; i < 21; i++) x = A(x); return x; })() });
  await probe('depth.arrays_only_20', 'field q holds 20 arrays (each directly inside the last) around an integer', { q: (() => { let x = I(1); for (let i = 0; i < 20; i++) x = A(x); return x; })() });
  await probe('depth.maps_20_vector', 'field q holds 19 nested maps around a vector', { q: nestMaps(19, VEC(1)) });
  await probe('depth.maps_21_vector', 'field q holds 20 nested maps around a vector', { q: nestMaps(20, VEC(1)) });
  await probe('depth.maps_20_empty', 'field d holds 20 nested maps, the innermost empty', { d: nestMaps(19, M({})) });
  await probe('depth.maps_21_empty', 'field d holds 21 nested maps, the innermost empty', { d: nestMaps(20, M({})) });
  await probe('depth.arrays_20_empty', 'field d: maps then an empty array, 20 containers', { d: nestMaps(19, A()) });
  await probe('depth.arrays_21_empty', 'field d: maps then an empty array, 21 containers', { d: nestMaps(20, A()) });
  for (const name of ['__a__', '__name__', '__type__', '__key__', '____', '___', '__a_', '_a__', 'a__b__', '__é__', '__ __']) {
    await probe(`name.top.${name}`, `top-level field named ${JSON.stringify(name)}`, { [name]: I(1) });
  }
  for (const name of ['__a__', '__type__', '___']) {
    await probe(`name.nested.${name}`, `map key ${JSON.stringify(name)} inside field m`, { m: M({ [name]: I(1) }) });
  }
  await probe('name.top.empty', 'top-level field named ""', { '': I(1) });
  await probe('name.nested.empty', 'map key "" inside field m', { m: M({ '': I(1) }) });
  await probe('name.top.1500', 'top-level field name of 1500 bytes', { ['f'.repeat(1500)]: I(1) });
  await probe('name.top.1501', 'top-level field name of 1501 bytes', { ['f'.repeat(1501)]: I(1) });
  await probe('name.nested.1501', 'map key of 1501 bytes inside field m', { m: M({ ['f'.repeat(1501)]: I(1) }) });
  await probe('name.path.1500', 'field a(749) holding key b(749): path a.b is 1499 bytes', { ['a'.repeat(749)]: M({ ['b'.repeat(749)]: I(1) }) });
  await probe('name.path.1501', 'field a(750) holding key b(750): path a.b is 1501 bytes', { ['a'.repeat(750)]: M({ ['b'.repeat(750)]: I(1) }) });
  await probe('name.path.quoted.1500', 'field "a-"(748) holding key b(749): the quoted path `a-…`.b is 1502 bytes, the plain one 1498', { [`${'a'.repeat(747)}-`]: M({ ['b'.repeat(749)]: I(1) }) });
  await probe('name.path.raw_1500', 'field a holding key b(1498): path a.b is 1500 bytes, its segments plus one each 1501', { a: M({ ['b'.repeat(1498)]: I(1) }) });
  await probe('name.path.raw_1501', 'field a holding key b(1499): path a.b is 1501 bytes', { a: M({ ['b'.repeat(1499)]: I(1) }) });
  await probe('name.path.quoted_1502', 'field "a-"(749) holding key b(750): the plain path is 1500 bytes, the quoted one 1502', { [`${'a'.repeat(748)}-`]: M({ ['b'.repeat(750)]: I(1) }) });
  await probe('name.top.dot', 'top-level field named "a.b"', { 'a.b': I(1) });
  await probe('name.top.backtick', 'top-level field named "`"', { '`': I(1) });
  await probe('array.nested', 'an array directly inside an array', { d: A(A(I(1))) });
  await probe('array.nested_empty', 'an empty array directly inside an array', { d: A(A()) });
  await probe('array.map_array', 'an array inside a map inside an array', { d: A(M({ a: A(I(1)) })) });
  await probe('vector.empty', 'a vector of no dimensions', { d: VEC() });
  await probe('vector.2048', 'a vector of 2048 dimensions', { d: VEC(...Array(2048).fill(0.5)) });
  await probe('vector.2049', 'a vector of 2049 dimensions', { d: VEC(...Array(2049).fill(0.5)) });
  await probe('vector.integer', 'a vector whose element is an integer', { d: M({ __type__: S('__vector__'), value: A(I(1)) }) });
  await probe('vector.string_element', 'a vector whose element is a string', { d: M({ __type__: S('__vector__'), value: A(D(1), S('a')) }) });
  await probe('vector.value_string', 'a vector map whose value is a string', { d: M({ __type__: S('__vector__'), value: S('x') }) });
  await probe('vector.no_value', 'a vector map with no value', { d: M({ __type__: S('__vector__') }) });
  await probe('vector.extra', 'a vector map with one more key', { d: M({ __type__: S('__vector__'), value: A(D(1)), x: I(1) }) });
  await probe('vector.type_int', 'a map whose __type__ is an integer', { d: M({ __type__: I(1), value: A(D(1)) }) });
  for (const [k, val] of [['double', D(1.5)], ['boolean', BOOL(true)], ['null', NUL], ['map', M({})], ['array', A()], ['bytes', BY([1])], ['timestamp', T('2026-01-01T00:00:00Z')], ['reference', R('c/a')], ['geopoint', G(1, 2)]]) {
    await probe(`vector.type_${k}`, `a map whose __type__ is a ${k}`, { d: M({ __type__: val, value: A(D(1)) }) });
  }
  await probe('vector.top_level', 'a vector map as the document itself', { __type__: S('__vector__'), value: A(D(1)) });
  await probe('vector.in_array', 'a vector inside an array', { d: A(VEC(1)) });
  await probe('type.other', 'a map whose __type__ is "x" (no other key)', { d: M({ __type__: S('x') }) });
  await probe('type.max', 'a map whose __type__ is "__max__" (the SDKs\' internal max value)', { d: M({ __type__: S('__max__') }) });
  await probe('type.min', 'a map whose __type__ is "__min__"', { d: M({ __type__: S('__min__') }) });
  await probe('type.bson_int32', 'an Enterprise BSON int32 map {__int__: 1}', { d: M({ __int__: I(1) }) });
  await probe('geo.lat_91', 'geopoint latitude 91', { d: G(91, 0) });
  await probe('geo.lat_neg_90_5', 'geopoint latitude -90.5', { d: G(-90.5, 0) });
  await probe('geo.lng_181', 'geopoint longitude 181', { d: G(0, 181) });
  await probe('geo.lng_neg_180_5', 'geopoint longitude -180.5', { d: G(0, -180.5) });
  await probe('geo.lat_nan', 'geopoint latitude NaN', { d: G('NaN', 0) });
  await probe('geo.lng_inf', 'geopoint longitude Infinity', { d: G(0, 'Infinity') });
  await probe('geo.neg_zero', 'geopoint (-0.0, -0.0)', { d: G(NEG0, NEG0) });
  await probe('time.year_0', 'timestamp 0000-12-31T23:59:59Z', { d: T('0000-12-31T23:59:59Z') });
  await probe('time.year_10000', 'timestamp 10000-01-01T00:00:00Z', { d: T('10000-01-01T00:00:00Z') });
  await probe('time.max_nanos', 'timestamp 9999-12-31T23:59:59.999999999Z', { d: T('9999-12-31T23:59:59.999999999Z') });
  await probe('time.offset', 'timestamp with a +01:00 offset', { d: T('2026-01-01T01:00:00.5+01:00') });
  await probe('int.overflow', 'integer 9223372036854775808', { d: I('9223372036854775808') });
  await probe('ref.collection', 'reference to a collection (odd number of segments)', { d: R('c') });
  await probe('ref.relative', 'reference without projects/…/documents/', { d: { referenceValue: 'c/a' } });
  await probe('ref.empty_segment', 'reference with an empty segment', { d: R('c//a') });
  await probe('ref.reserved_id', 'reference to c/__x__', { d: R('c/__x__') });
  await probe('ref.id_1501', 'reference to a document id of 1501 bytes', { d: R(`c/${'i'.repeat(1501)}`) });
  await probe('ref.bad_database', 'reference with database id "Bad DB"', { d: R(`projects/${PROJECT}/databases/Bad DB/documents/c/a`) });
  await probe('bytes.bad_base64', 'bytes value that is not base64', { d: { bytesValue: '***' } });
  await probe('string.max_nul', 'a string of NUL bytes', { d: S('\u0000\u0000') });
  return {
    cases,
    notes: [`Each case is one commit of one document in ${BAD} (ids c000, c001, … in case order). Long strings in requests are compacted (length and SHA-256).`, 'A depth of n counts the containers (maps and arrays) around the innermost value of field d; the document\'s own fields are depth 0.'],
  };
}

// ---- values-mask ----

async function mask() {
  const doc = {
    a: M({ b: I(1), c: I(2) }),
    'x.y': I(3),
    'é': I(4),
    '`': I(5),
    'a b': I(6),
    arr: A(M({ p: I(1) })),
    'b\\c': I(7),
    _u: I(8),
    '1n': I(9),
  };
  const rel = `${MASK}/m`;
  const w = await commitOne(rel, doc);
  if (w.http !== 200) throw new Error(`mask setup failed: ${JSON.stringify(w)}`);
  const reads = [];
  const paths = [
    ['a.b'], ['a'], ['a', 'a.b'], ['`x.y`'], ['x.y'], ['é'], ['`é`'], ['`\\``'], ['`'], ['a b'], ['`a b`'], ['`b\\\\c`'],
    ['b\\c'], ['arr.p'], ['arr'], ['a.b.c'], ['__name__'], ['a..b'], ['.a'], ['a.'], ['``'], ['`a'], ['a.`b`'], ['`a`.`b`'],
    ['_u'], ['1n'], ['`1n`'], ['missing'], [''], ['a.b', 'missing.z'], ['a.zz'], ['a.b', 'a.zz'],
    [Array(21).fill('a').join('.')], [Array(101).fill('a').join('.')], ['a'.repeat(1500)], ['a'.repeat(1501)],
    [`${'a'.repeat(750)}.${'b'.repeat(750)}`], [`a.${'b'.repeat(1497)}`], [`a.${'b'.repeat(1498)}`], ['a'.repeat(1499)],
    [Array(20).fill('a').join('.')], [`\`${'a'.repeat(1498)}\``], [`\`${'a'.repeat(1499)}\``],
  ];
  for (const p of paths) {
    reads.push({ maskFieldPaths: p, ...(await getDoc(rel, p)) });
    log('.');
  }
  // Update masks: each case rewrites the document, applies one masked
  // update, and reads the result back.
  const updates = [];
  const upd = [
    ['replace a.b', { a: M({ b: I(9) }) }, ['a.b']],
    ['a.c in the mask, absent from the update: deleted', { a: M({ b: I(9) }) }, ['a.c']],
    ['a in the mask: the whole map replaced', { a: M({ z: I(1) }) }, ['a']],
    ['quoted `x.y`', { 'x.y': I(30) }, ['`x.y`']],
    ['new nested path n.m.o', { n: M({ m: M({ o: I(1) }) }) }, ['n.m.o']],
    ['a.b.c through an integer', { a: M({ b: M({ c: I(1) }) }) }, ['a.b.c']],
    ['field in the update but not in the mask', { a: I(1), q: I(1) }, ['a']],
    ['overlapping a and a.b', { a: M({ b: I(1) }) }, ['a', 'a.b']],
    ['duplicate a.b', { a: M({ b: I(1) }) }, ['a.b', 'a.b']],
    ['unquoted é', { 'é': I(40) }, ['é']],
    ['__name__', {}, ['__name__']],
    ['a.b and a.c absent from the update: a left empty', {}, ['a.b', 'a.c']],
    ['missing.z absent from the update, missing absent from the document', {}, ['missing.z']],
    ['invalid a..b', {}, ['a..b']],
    ['empty path', {}, ['']],
    ['unterminated backtick', {}, ['`a']],
  ];
  for (const [d, fields, m] of upd) {
    await commitOne(rel, doc);
    const res = await commitOne(rel, fields, { mask: m });
    const after = await getDoc(rel);
    updates.push({ description: d, fields: forCorpus(fields), mask: m, write: res, after: after.fields });
    log('.');
  }
  return {
    document: forCorpus(doc),
    reads,
    updates,
    notes: [`The document ${MASK}/m holds the fields of document; reads are GETs with mask.fieldPaths (one query parameter per path); updates rewrite it, commit one update with updateMask.fieldPaths, and read it back (after).`],
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
  // Verify: every document at any depth under each collection this recorder
  // writes ("aruma-ref-values" up to "aruma-ref-valuet", which covers the
  // others), through one kindless query. Other recorders may be writing their
  // own aruma-ref* collections at the same time, so the range stops there.
  const sweep = await rest('POST', `${ROOT}:runQuery`, {
    structuredQuery: {
      from: [{ allDescendants: true }],
      where: { compositeFilter: { op: 'AND', filters: [
        { fieldFilter: { field: { fieldPath: '__name__' }, op: 'GREATER_THAN_OR_EQUAL', value: { referenceValue: `${ROOT}/${ORD}/${MIN_ID}` } } },
        { fieldFilter: { field: { fieldPath: '__name__' }, op: 'LESS_THAN', value: { referenceValue: `${ROOT}/aruma-ref-valuet/${MIN_ID}` } } },
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
    sweep: { http: sweep.http, query: `kindless allDescendants, __name__ in [${ORD}/${MIN_ID}, aruma-ref-valuet/${MIN_ID})`, remaining },
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
    if (want('order') || want('equality')) { log('order '); results['values-order.json'] = await order(); }
    if (want('equality')) { log('\nequality '); results['values-equality.json'] = await equality(); }
    if (want('size')) { log('\nsize '); results['values-size.json'] = await size(); }
    if (want('validate')) { log('\nvalidate '); results['values-validate.json'] = await validate(); }
    if (want('mask')) { log('\nmask '); results['values-mask.json'] = await mask(); }
  } finally {
    log('\ncleanup ');
    const cl = await cleanup();
    results['values-cleanup.json'] = { cleanup: cl };
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
  console.error('record-values failed:', e.message);
  process.exit(1);
});
