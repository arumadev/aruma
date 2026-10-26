// firebase-admin's Firestore against a live Aruma with the Document Database (F1 of v0.4, plan
// §6), pointed at it the way ADR-027 documents for Node: settings host and ssl:false, with the
// service-account key file Aruma issued. Run through documents-run.mjs (npm run test:documents),
// once per transport:
//
//   grpc  gRPC on h2c. firebase-admin then sends Bearer owner and not the key (ADR-029 §6), so
//         the server runs with documents.dev_owner_token;
//   rest  preferRest: true, REST on HTTP/1.1 with the key's self-signed JWT; the server runs
//         without the owner token, so the JWT is what admits it.
//
// The SDK reads with the F1 RPCs (get and getAll: BatchGetDocuments; listDocuments;
// listCollections: ListCollectionIds), and every write of the SDK is a Commit, which comes in F2:
// those tests are listed in test/compat/documents/expected-failures/admin-node.txt, and a
// listed test that passes fails the run. GetDocument, CreateDocument, UpdateDocument and
// DeleteDocument run through the SDK's own client (Firestore.request), on the same transport
// and with the same credentials, and every value production recorded is written with them and
// must read back exactly.
import { test as nodeTest, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { initializeApp, cert, deleteApp } from 'firebase-admin/app';
import { getFirestore, FieldValue, GeoPoint, Timestamp, DocumentReference } from 'firebase-admin/firestore';
import { checked, expectedFailures } from '../documents-server.mjs';
import { loadValues, protoValue, protoFields, canon, canonFields, parseTimestamp } from '../documents-values.mjs';

const require = createRequire(import.meta.url);
const netguard = require.resolve('../../experiments/netguard.cjs');
assert.ok(require.cache[netguard], 'run this file through documents-run.mjs (npm run test:documents): the netguard is not loaded');
const { attempts } = require(netguard);

const dir = process.env.ARUMA_SMOKE_DIR;
const addr = process.env.ARUMA_SMOKE_DOCUMENTS_ADDR;
const transport = process.env.ARUMA_SMOKE_DOCUMENTS_TRANSPORT ?? 'grpc';
assert.ok(dir && addr, 'ARUMA_SMOKE_DIR and ARUMA_SMOKE_DOCUMENTS_ADDR are not set');
assert.ok(['grpc', 'rest'].includes(transport), `ARUMA_SMOKE_DOCUMENTS_TRANSPORT ${transport}`);
const expected = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8'));
const project = expected.projectId;
const database = `projects/${project}/databases/(default)`;
const root = `${database}/documents`;

const listName = 'test/compat/documents/expected-failures/admin-node.txt';
const list = expectedFailures(readFileSync(new URL(`../../compat/documents/expected-failures/admin-node.txt`, import.meta.url), 'utf8'));
const { test: listed, stale } = checked(nodeTest, list, listName);
// Every name carries the transport: "grpc/…" or "rest/…".
const test = (name, fn) => listed(`${transport}/${name}`, fn);

let app, db;
let tag = 0;
// call runs one F1 RPC through the SDK's own client.
const call = (method, request) => db.request(method, request, `smoke-${++tag}`);

// rejects checks a promise fails with the gRPC code (a number) and, when given, the message:
// gax puts "<code> <STATUS>: " before it on gRPC, and nothing on REST.
async function rejects(promise, code, message) {
  let err;
  try {
    await promise;
  } catch (e) {
    err = e;
  }
  assert.ok(err, `no error, want code ${code}`);
  assert.equal(err.code, code, `got ${err.code}: ${err.message}`);
  if (message !== undefined) assert.equal(messageOf(err), message);
}

// messageOf is the server's message: gax puts "<code> <STATUS>: " before it on gRPC.
const messageOf = (e) => String(e?.message ?? e).replace(/^\d+ [A-Z_]+: /, '');

// batchGet runs BatchGetDocuments, a server stream, through the SDK's own client.
function batchGet(documents) {
  return db._clientPool.run(`smoke-${++tag}`, false, (client) => new Promise((resolve, reject) => {
    const out = [];
    const stream = client.batchGetDocuments({ database, documents }, db.createCallOptions('batchGetDocuments'));
    stream.on('data', (d) => out.push(d)).on('error', reject).on('end', () => resolve(out));
  }));
}

// create runs CreateDocument with fields in proto3 JSON. On REST it is a request of its own with
// the SDK's credentials: google-gax's REST client cannot build CreateDocument under the root
// (its rule is {parent=projects/*/databases/*/documents/**}/{collection_id}, and it does not let
// ** match nothing), so firebase-admin itself never sends one there.
async function create(collection, id, fields, parent = root) {
  if (transport === 'grpc') {
    return call('createDocument', { parent, collectionId: collection, documentId: id, document: { fields: protoFields(fields) } });
  }
  const url = `http://${addr}/v1/${parent}/${collection}?documentId=${encodeURIComponent(id)}`;
  const auth = await db._clientPool.run(`smoke-${++tag}`, false, async (client) => (await client.auth.getClient()).getRequestHeaders(url));
  const res = await fetch(url, {
    method: 'POST',
    headers: { ...(auth instanceof Headers ? Object.fromEntries(auth) : auth), 'content-type': 'application/json' },
    body: JSON.stringify({ fields }),
  });
  const body = await res.json();
  if (!res.ok) {
    throw Object.assign(new Error(body.error?.message ?? JSON.stringify(body)), { code: grpcCodes.indexOf(body.error?.status) });
  }
  for (const k of ['createTime', 'updateTime']) if (typeof body[k] === 'string') body[k] = parseTimestamp(body[k]);
  return body;
}

const grpcCodes = ['OK', 'CANCELLED', 'UNKNOWN', 'INVALID_ARGUMENT', 'DEADLINE_EXCEEDED', 'NOT_FOUND', 'ALREADY_EXISTS',
  'PERMISSION_DENIED', 'RESOURCE_EXHAUSTED', 'FAILED_PRECONDITION', 'ABORTED', 'OUT_OF_RANGE', 'UNIMPLEMENTED', 'INTERNAL',
  'UNAVAILABLE', 'DATA_LOSS', 'UNAUTHENTICATED'];

const int = (n) => ({ integerValue: String(n) });
const str = (s) => ({ stringValue: s });
const map = (fields) => ({ mapValue: { fields } });

before(async () => {
  app = initializeApp({ credential: cert(`${dir}/service-account.json`), projectId: project }, `documents-${transport}`);
  db = getFirestore(app);
  db.settings({ host: addr, ssl: false, preferRest: transport === 'rest', useBigInt: true });
  await db.initializeIfNeeded('smoke');
});

after(async () => {
  await db?.terminate();
  if (app) await deleteApp(app);
  const off = attempts.filter((a) => !a.startsWith('ALLOW '));
  assert.deepEqual(off, [], `network access beyond loopback attempted: ${off.join(', ')}`);
  assert.deepEqual(stale(`${transport}/`), [], `${listName} names tests that did not run`);
});

// --- Every recorded value, through CreateDocument, GetDocument and BatchGetDocuments.

const values = loadValues(project);

test('values: every recorded value round-trips exactly', async (t) => {
  const failures = [];
  for (const v of values) {
    try {
      const doc = await create('values', v.id, { v: v.sent });
      if (!v.stored) {
        failures.push(`${v.id}: written, but production refused it with ${v.error.status}: ${v.error.message}`);
        continue;
      }
      if (canon(doc.fields.v) !== canon(v.stored)) failures.push(`${v.id}: CreateDocument returned ${canon(doc.fields.v)}, want ${canon(v.stored)}`);
      const got = await call('getDocument', { name: `${root}/values/${v.id}` });
      if (canon(got.fields.v) !== canon(v.stored)) failures.push(`${v.id}: GetDocument returned ${canon(got.fields.v)}, want ${canon(v.stored)}`);
    } catch (e) {
      if (v.stored) {
        failures.push(`${v.id}: ${e.message}`);
      } else if (e.code !== 3 || messageOf(e) !== v.error.message) {
        failures.push(`${v.id}: refused with ${e.code} ${e.message}, want 3 (INVALID_ARGUMENT) ${v.error.message}`);
      }
    }
  }
  t.diagnostic(`${values.length} values, ${values.filter((v) => !v.stored).length} of them refused as in production`);
  assert.deepEqual(failures, []);
});

test('values: batch get of every value', async () => {
  const stored = values.filter((v) => v.stored);
  const want = new Map(stored.map((v) => [`${root}/values/${v.id}`, canon(v.stored)]));
  const responses = await batchGet([...want.keys()]);
  assert.equal(responses.length, stored.length);
  const failures = [];
  for (const r of responses) {
    if (!r.found) {
      failures.push(`missing: ${r.missing}`);
      continue;
    }
    const got = canon(r.found.fields?.v);
    if (got !== want.get(r.found.name)) failures.push(`${r.found.name}: ${got}, want ${want.get(r.found.name)}`);
    if (!r.readTime) failures.push(`${r.found.name}: no readTime`);
  }
  assert.deepEqual(failures, []);
  // And the SDK's getAll finds and decodes every one.
  const snaps = await db.getAll(...stored.map((v) => db.doc(`values/${v.id}`)));
  assert.deepEqual(snaps.filter((s) => !s.exists).map((s) => s.id), []);
  for (const s of snaps) s.data();
});

// typed: what the SDK (useBigInt) decodes values to.
const typed = {
  'n-i-max': 9223372036854775807n,
  'n-i-min': -9223372036854775808n,
  'n-i-2p53-plus-1': 9007199254740993n,
  'n-d-max': Number.MAX_VALUE,
  'n-d-min': Number.MIN_VALUE,
  'n-pos-inf': Infinity,
  'n-neg-inf': -Infinity,
  'n-nan': NaN,
  'n-d-neg-zero': -0,
  'n-d-zero': 0,
  'bool-true': true,
  null: null,
  's-emoji': '\u{1F600}',
  's-e-acute': '\u00e9',
  's-u-ffff': '\uffff',
  's-nul': '\u0000',
};

test('sdk: every type decodes', async () => {
  const ids = ['b-ff00', 'g-s90-w180', 'v-1-2-3', 'a-i-1-s-a', 'm-a-1-b-1', 't-micros', 't-max', 't-min', 'r-c-a', ...Object.keys(typed)];
  const snaps = await db.getAll(...ids.map((id) => db.doc(`values/${id}`)));
  const byId = Object.fromEntries(snaps.map((s) => [s.id, s.get('v')]));
  for (const [id, want] of Object.entries(typed)) assert.ok(Object.is(byId[id], want), `${id}: ${String(byId[id])}, want ${String(want)}`);
  assert.deepEqual(byId['b-ff00'], Buffer.from([0xff, 0x00]));
  assert.ok(byId['g-s90-w180'] instanceof GeoPoint && byId['g-s90-w180'].isEqual(new GeoPoint(-90, -180)));
  assert.equal(byId['v-1-2-3']?.constructor?.name, 'VectorValue', `v-1-2-3: ${byId['v-1-2-3']}`);
  assert.deepEqual(byId['v-1-2-3'].toArray(), [1, 2, 3]);
  assert.deepEqual(byId['a-i-1-s-a'], [1n, 'a']);
  assert.deepEqual(byId['m-a-1-b-1'], { a: 1n, b: 1n });
  for (const id of ['t-micros', 't-max', 't-min']) {
    const want = parseTimestamp(values.find((v) => v.id === id).stored.timestampValue);
    assert.ok(byId[id] instanceof Timestamp, id);
    assert.deepEqual([byId[id].seconds, byId[id].nanoseconds], [Number(want.seconds), want.nanos], id);
  }
  assert.ok(byId['r-c-a'] instanceof DocumentReference);
  assert.equal(byId['r-c-a'].path, 'c/a');
});

// --- The SDK's reads.

test('sdk: get', async () => {
  const snap = await db.doc('values/bool-true').get();
  assert.ok(snap.exists);
  assert.equal(snap.get('v'), true);
  assert.ok(snap.createTime && snap.updateTime && snap.readTime);
  const missing = await db.doc('values/nope').get();
  assert.equal(missing.exists, false);
  assert.ok(missing.readTime);
});

test('sdk: getAll with missing documents and a field mask', async () => {
  await create('sdk', 'g1', { a: int(1), b: map({ c: int(2), d: int(3) }) });
  const [g1, nope] = await db.getAll(db.doc('sdk/g1'), db.doc('sdk/nope'));
  assert.equal(g1.exists, true);
  assert.equal(nope.exists, false);
  const [masked] = await db.getAll(db.doc('sdk/g1'), { fieldMask: ['b.c'] });
  assert.deepEqual(masked.data(), { b: { c: 2n } });
});

test('sdk: listDocuments and listCollections', async () => {
  for (const id of ['a', 'b', 'c']) await create('sdk-list', id, { n: int(1) });
  await create('sub', 'x', {}, `${root}/sdk-list/missing`);
  const refs = await db.collection('sdk-list').listDocuments();
  assert.deepEqual(refs.map((r) => r.id).sort(), ['a', 'b', 'c', 'missing']);
  const cols = await db.listCollections();
  assert.ok(cols.some((c) => c.id === 'sdk-list'), cols.map((c) => c.id).join());
  const sub = await db.doc('sdk-list/missing').listCollections();
  assert.deepEqual(sub.map((c) => c.id), ['sub']);
});

// --- The F1 RPCs and their errors, as production answers them (f1-*.json).

test('rpc: create, and the errors of create', async () => {
  const doc = await create('rpc', 'c1', { a: int(1) });
  assert.equal(doc.name, `${root}/rpc/c1`);
  assert.equal(canonFields(doc.fields), canonFields({ a: int(1) }));
  await rejects(create('rpc', 'c1', {}), 6, `Document already exists: ${root}/rpc/c1`);
  await rejects(create('rpc', '__x__', {}), 3, 'Resource id "__x__" is invalid because it is reserved.');
  const auto = await create('rpc', '', {});
  assert.match(auto.name, new RegExp(`^${root.replace(/[()]/g, '\\$&')}/rpc/[A-Za-z0-9]{20}$`));
});

test('rpc: get, and a missing document', async () => {
  await create('rpc', 'g1', { a: int(1), s: str('x') });
  const doc = await call('getDocument', { name: `${root}/rpc/g1`, mask: { fieldPaths: ['s'] } });
  assert.equal(canonFields(doc.fields), canonFields({ s: str('x') }));
  await rejects(call('getDocument', { name: `${root}/rpc/nope` }), 5, `Document "${root}/rpc/nope" not found.`);
});

test('rpc: update with masks and preconditions', async () => {
  const name = `${root}/rpc/u1`;
  const first = await call('updateDocument', { document: { name, fields: protoFields({ a: int(1), b: int(2) }) } });
  const replaced = await call('updateDocument', { document: { name, fields: protoFields({ c: int(3) }) } });
  assert.equal(canonFields(replaced.fields), canonFields({ c: int(3) }));
  const merged = await call('updateDocument', { document: { name, fields: protoFields({ a: int(10), z: int(9) }) }, updateMask: { fieldPaths: ['a'] } });
  assert.equal(canonFields(merged.fields), canonFields({ a: int(10), c: int(3) }));
  await rejects(call('updateDocument', { document: { name, fields: {} }, currentDocument: { exists: false } }), 6, `Document already exists: ${name}`);
  await rejects(call('updateDocument', { document: { name: `${root}/rpc/u-missing`, fields: {} }, currentDocument: { exists: true } }), 5,
    `No document to update: ${root}/rpc/u-missing`);
  const micros = (t) => BigInt(t.seconds) * 1000000n + BigInt(Math.floor(t.nanos / 1000));
  await rejects(call('updateDocument', { document: { name, fields: {} }, updateMask: { fieldPaths: ['a'] }, currentDocument: { updateTime: first.updateTime } }), 9,
    `the stored version (${micros(merged.updateTime)}) does not match the required base version (${micros(first.updateTime)})`);
  await call('updateDocument', { document: { name, fields: protoFields({ a: int(11) }) }, updateMask: { fieldPaths: ['a'] }, currentDocument: { updateTime: merged.updateTime } });
});

test('rpc: delete with preconditions', async () => {
  await call('deleteDocument', { name: `${root}/rpc/d-missing` });
  await rejects(call('deleteDocument', { name: `${root}/rpc/d-missing`, currentDocument: { exists: true } }), 5, `No document to update: ${root}/rpc/d-missing`);
  const d1 = await create('rpc', 'd1', { n: int(1) });
  await rejects(call('deleteDocument', { name: `${root}/rpc/d1`, currentDocument: { exists: false } }), 6, `Document already exists: ${root}/rpc/d1`);
  await call('deleteDocument', { name: `${root}/rpc/d1`, currentDocument: { updateTime: d1.updateTime } });
  await rejects(call('getDocument', { name: `${root}/rpc/d1` }), 5);
});

test('rpc: batch get naming another project', async () => {
  await rejects(batchGet(['projects/aruma-ref-no-such-project/databases/(default)/documents/rpc/c1']), 3,
    `The request was for database '${database}' but was attempting to access database 'projects/aruma-ref-no-such-project/databases/(default)'`);
});

test('rpc: listCollectionIds in pages', async () => {
  for (const id of ['s3', 's1', 's2']) await create(id, 'x', {}, `${root}/rpc/p`);
  const ids = await call('listCollectionIds', { parent: `${root}/rpc/p`, pageSize: 2 });
  // gax pages through on its own: every id, in order.
  assert.deepEqual(ids, ['s1', 's2', 's3']);
  await rejects(call('listCollectionIds', { parent: `${root}/rpc/p`, pageToken: 'abc' }), 3, 'invalid page token');
});

// --- The SDK's writes: every one is a Commit (F2).

test('writes: create', async () => {
  await db.doc('writes/w1').create({ a: 1 });
  await rejects(db.doc('writes/w1').create({ a: 2 }), 6, `Document already exists: ${root}/writes/w1`);
});

test('writes: set and merge', async () => {
  await db.doc('writes/w2').set({ a: 1, b: { c: 2 } });
  await db.doc('writes/w2').set({ b: { d: 3 } }, { merge: true });
  assert.deepEqual((await db.doc('writes/w2').get()).data(), { a: 1n, b: { c: 2n, d: 3n } });
});

test('writes: update with preconditions', async () => {
  const wr = await db.doc('writes/w3').set({ a: 1 });
  await db.doc('writes/w3').update({ a: 2 }, { lastUpdateTime: wr.writeTime });
  await rejects(db.doc('writes/w3').update({ a: 3 }, { lastUpdateTime: wr.writeTime }), 9);
  await rejects(db.doc('writes/w-missing').update({ a: 1 }), 5, `No document to update: ${root}/writes/w-missing`);
});

test('writes: delete with preconditions', async () => {
  await db.doc('writes/w4').set({ a: 1 });
  await db.doc('writes/w4').delete({ exists: true });
  await rejects(db.doc('writes/w4').delete({ exists: true }), 5, `No document to update: ${root}/writes/w4`);
  await db.doc('writes/w4').delete();
});

test('writes: batch', async () => {
  const b = db.batch();
  b.set(db.doc('writes/b1'), { a: 1 });
  b.create(db.doc('writes/b2'), { a: 2 });
  b.delete(db.doc('writes/b3'));
  assert.equal((await b.commit()).length, 3);
});

test('writes: every type round-trips', async () => {
  const data = {
    null: null, t: true, f: false,
    max: 9223372036854775807n, min: -9223372036854775808n,
    nan: NaN, negzero: -0, zero: 0, inf: Infinity, dmax: Number.MAX_VALUE,
    unicode: 'héllo \u0000 \uffff \u{1F600} \ue000', bytes: Buffer.from([0, 1, 0xfe, 0xff]),
    ref: db.doc('values/nope'), geo: new GeoPoint(-90, 180),
    array: [1n, 'a', { x: [true, null], empty: [] }],
    map: { a: { b: { c: 1n } }, 'ü 😀': 'unicode key' },
    vector: FieldValue.vector([1, -0.5, 3e300]),
    ts: new Timestamp(1793018096, 123456000), tsMin: new Timestamp(-62135596800, 0), tsMax: new Timestamp(253402300799, 999999000),
  };
  await db.doc('writes/types').set(data);
  const got = (await db.doc('writes/types').get()).data();
  for (const k of ['null', 't', 'f', 'max', 'min', 'nan', 'negzero', 'zero', 'inf', 'dmax', 'unicode']) assert.ok(Object.is(got[k], data[k]), `${k}: ${String(got[k])}`);
  assert.deepEqual(got.bytes, data.bytes);
  assert.ok(got.ref.isEqual(data.ref));
  assert.ok(got.geo.isEqual(data.geo));
  assert.deepEqual(got.array, data.array);
  assert.deepEqual(got.map, data.map);
  assert.deepEqual(got.vector.toArray(), [1, -0.5, 3e300]);
  for (const k of ['ts', 'tsMin', 'tsMax']) assert.ok(got[k].isEqual(data[k]), k);
});
