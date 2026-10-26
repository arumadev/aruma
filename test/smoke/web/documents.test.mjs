// The Firebase JS SDK's Firestore lite (REST) against a live Aruma with the Document Database
// (F1 of v0.4, plan §6), for the last three majors, pointed at it in the production form of
// ADR-027: initializeFirestore(app, {host, ssl: false}) with the web config Aruma generated, not
// connectFirestoreEmulator. Run through documents-run.mjs (npm run test:documents), which starts
// the server with documents.dev_open_rules (genconfig -documents-open-rules): until the rules of
// F5, only that admits a client without credentials.
//
// The values production recorded are written with CreateDocument over REST and read back with
// getDoc (BatchGetDocuments). Every lite write, and runTransaction's commit, is a Commit (F2) and
// getDocs a RunQuery (F3): those tests are listed in test/compat/documents/expected-failures/web-lite.txt, and a listed
// test that passes fails the run.
import { test as nodeTest, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { checked, expectedFailures } from '../documents-server.mjs';
import { loadValues, parseTimestamp } from '../documents-values.mjs';

const require = createRequire(import.meta.url);
const netguard = require.resolve('../../experiments/netguard.cjs');
assert.ok(require.cache[netguard], 'run this file through documents-run.mjs (npm run test:documents): the netguard is not loaded');
const { attempts } = require(netguard);

const dir = process.env.ARUMA_SMOKE_DIR;
const addr = process.env.ARUMA_SMOKE_DOCUMENTS_ADDR;
assert.ok(dir && addr, 'ARUMA_SMOKE_DIR and ARUMA_SMOKE_DOCUMENTS_ADDR are not set');
const config = JSON.parse(readFileSync(`${dir}/firebase-config.json`, 'utf8'));
const project = config.projectId;
const root = `projects/${project}/databases/(default)/documents`;

const listName = 'test/compat/documents/expected-failures/web-lite.txt';
const list = expectedFailures(readFileSync(new URL('../../compat/documents/expected-failures/web-lite.txt', import.meta.url), 'utf8'));
const { test, stale } = checked(nodeTest, list, listName);

const values = loadValues(project);
const stored = values.filter((v) => v.stored);

// Seeded once, over REST, with no credentials (open rules).
let seeded;
function seed() {
  seeded ??= (async () => {
    for (const v of stored) {
      const res = await fetch(`http://${addr}/v1/${root}/lite-values?documentId=${encodeURIComponent(v.id)}`, {
        method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ fields: { v: v.sent } }),
      });
      if (!res.ok) throw new Error(`createDocument ${v.id}: ${res.status} ${await res.text()}`);
    }
  })();
  return seeded;
}

after(() => {
  const off = attempts.filter((a) => !a.startsWith('ALLOW '));
  assert.deepEqual(off, [], `network access beyond loopback attempted: ${off.join(', ')}`);
  assert.deepEqual(stale(''), [], `${listName} names tests that did not run`);
});

for (const major of ['11', '12', '13']) {
  const sdk = async () => {
    const { initializeApp, deleteApp } = await import(`firebase-${major}/app`);
    const lite = await import(`firebase-${major}/firestore/lite`);
    const app = initializeApp(config, `documents-${major}-${Math.random()}`);
    const db = lite.initializeFirestore(app, { host: addr, ssl: false });
    return { ...lite, db, close: async () => { await lite.terminate(db); await deleteApp(app); } };
  };
  const t = (name, fn) => test(`firebase@${major}/${name}`, async () => {
    const s = await sdk();
    try {
      await fn(s);
    } finally {
      await s.close();
    }
  });

  t('getDoc reads every recorded value', async ({ db, doc, getDoc }) => {
    await seed();
    const failures = [];
    for (const v of stored) {
      try {
        const snap = await getDoc(doc(db, 'lite-values', v.id));
        if (!snap.exists()) failures.push(`${v.id}: missing`);
        else snap.data();
      } catch (e) {
        failures.push(`${v.id}: ${e.code ?? ''} ${e.message}`);
      }
    }
    assert.deepEqual(failures, []);
  });

  t('getDoc decodes every type', async ({ db, doc, getDoc, Bytes, GeoPoint, Timestamp, DocumentReference }) => {
    await seed();
    const get = async (id) => (await getDoc(doc(db, 'lite-values', id))).get('v');
    // Integers are JS numbers in the web SDKs: the int64 extremes round as Number() does.
    const scalars = {
      'n-i-max': Number('9223372036854775807'), 'n-i-min': Number('-9223372036854775808'), 'n-i-2p53-plus-1': Number('9007199254740993'),
      'n-d-max': Number.MAX_VALUE, 'n-d-min': Number.MIN_VALUE, 'n-pos-inf': Infinity, 'n-neg-inf': -Infinity,
      'n-nan': NaN, 'n-d-neg-zero': -0, 'n-d-zero': 0, 'bool-true': true, null: null,
      's-emoji': '\u{1F600}', 's-e-acute': 'é', 's-u-ffff': '￿', 's-nul': '\u0000',
    };
    for (const [id, want] of Object.entries(scalars)) {
      const got = await get(id);
      assert.ok(Object.is(got, want), `${id}: ${String(got)}, want ${String(want)}`);
    }
    const bytes = await get('b-ff00');
    assert.ok(bytes instanceof Bytes);
    assert.deepEqual([...bytes.toUint8Array()], [0xff, 0x00]);
    assert.ok((await get('g-s90-w180')).isEqual(new GeoPoint(-90, -180)));
    const vec = await get('v-1-2-3');
    assert.equal(vec?.constructor?.name, 'VectorValue');
    assert.deepEqual(vec.toArray(), [1, 2, 3]);
    assert.deepEqual(await get('a-i-1-s-a'), [1, 'a']);
    assert.deepEqual(await get('m-a-1-b-1'), { a: 1, b: 1 });
    for (const id of ['t-micros', 't-max', 't-min']) {
      const want = parseTimestamp(values.find((v) => v.id === id).stored.timestampValue);
      const got = await get(id);
      assert.ok(got instanceof Timestamp, id);
      assert.deepEqual([got.seconds, got.nanoseconds], [Number(want.seconds), want.nanos], id);
    }
    const ref = await get('r-c-a');
    assert.ok(ref instanceof DocumentReference);
    assert.equal(ref.path, 'c/a');
    assert.equal((await getDoc(doc(db, 'lite-values', 'nope'))).exists(), false);
  });

  t('writes: setDoc, updateDoc and deleteDoc', async ({ db, doc, getDoc, setDoc, updateDoc, deleteDoc }) => {
    const ref = doc(db, 'lite-writes', `w-${major}`);
    await setDoc(ref, { a: 1, b: { c: 2 } });
    await setDoc(ref, { b: { d: 3 } }, { merge: true });
    await updateDoc(ref, { a: 2 });
    assert.deepEqual((await getDoc(ref)).data(), { a: 2, b: { c: 2, d: 3 } });
    await deleteDoc(ref);
    assert.equal((await getDoc(ref)).exists(), false);
  });

  t('writes: addDoc and writeBatch', async ({ db, doc, collection, addDoc, writeBatch }) => {
    const added = await addDoc(collection(db, 'lite-writes'), { a: 1 });
    assert.equal(added.id.length, 20);
    const b = writeBatch(db);
    b.set(doc(db, 'lite-writes', `b1-${major}`), { a: 1 });
    b.delete(doc(db, 'lite-writes', `b2-${major}`));
    await b.commit();
  });

  t('writes: transforms', async ({ db, doc, getDoc, setDoc, updateDoc, serverTimestamp, increment, arrayUnion, arrayRemove }) => {
    const ref = doc(db, 'lite-writes', `t-${major}`);
    await setDoc(ref, { n: 1, tags: ['a', 'b'] });
    await updateDoc(ref, { at: serverTimestamp(), n: increment(2), tags: arrayUnion('b', 'c') });
    await updateDoc(ref, { tags: arrayRemove('a') });
    const data = (await getDoc(ref)).data();
    assert.equal(data.n, 3);
    assert.deepEqual(data.tags, ['b', 'c']);
    // REQUEST_TIME has whole milliseconds.
    assert.equal(data.at.nanoseconds % 1e6, 0);
  });

  t('writes: every type round-trips', async ({ db, doc, getDoc, setDoc, Bytes, GeoPoint, Timestamp, vector }) => {
    const ref = doc(db, 'lite-writes', `types-${major}`);
    const data = {
      null: null, t: true, f: false, int: 9007199254740991, nan: NaN, negzero: -0, inf: -Infinity, dmax: Number.MAX_VALUE,
      unicode: 'héllo \u0000 ￿ \u{1F600} ', bytes: Bytes.fromUint8Array(new Uint8Array([0, 1, 0xfe, 0xff])),
      ref: doc(db, 'lite-values', 'nope'), geo: new GeoPoint(90, -180),
      array: [1, 'a', { x: [true, null], empty: [] }], map: { a: { b: { c: 1 } }, list: [{ y: [1, 2] }] },
      vector: vector([1, -0.5, 3e300]), ts: new Timestamp(1793018096, 123456000),
    };
    await setDoc(ref, data);
    const got = (await getDoc(ref)).data();
    for (const k of ['null', 't', 'f', 'int', 'nan', 'negzero', 'inf', 'dmax', 'unicode']) assert.ok(Object.is(got[k], data[k]), k);
    assert.ok(got.bytes.isEqual(data.bytes));
    assert.equal(got.ref.path, data.ref.path);
    assert.ok(got.geo.isEqual(data.geo));
    assert.deepEqual(got.array, data.array);
    assert.deepEqual(got.map, data.map);
    assert.deepEqual(got.vector.toArray(), [1, -0.5, 3e300]);
    assert.ok(got.ts.isEqual(data.ts));
  });

  // Transactions. Lite's are the client's own (ADR-025 §7.2): it reads with BatchGetDocuments
  // outside any transaction, then commits its writes with preconditions on what it read
  // (update_time, or exists:false) and a verify for each document read and not written, and
  // retries a commit whose precondition failed. It begins no transaction on the server, so the
  // end user's concurrency mode never comes up; it has no read-only option either.
  t('runTransaction: read-modify-write', async ({ db, doc, getDoc, setDoc, runTransaction }) => {
    const ref = doc(db, 'lite-txn', `rmw-${major}`);
    await setDoc(ref, { n: 1 });
    const got = await runTransaction(db, async (tx) => {
      const n = (await tx.get(ref)).get('n');
      tx.update(ref, { n: n + 1 });
      return n;
    });
    assert.equal(got, 1);
    assert.equal((await getDoc(ref)).get('n'), 2);
  });

  t('runTransaction: concurrent increments keep the total', async ({ db, doc, getDoc, setDoc, runTransaction }) => {
    const ref = doc(db, 'lite-txn', `counter-${major}`);
    await setDoc(ref, { n: 0 });
    // Each round of conflicting commits lets one through: n workers need at most n attempts.
    const workers = 5;
    await Promise.all(Array.from({ length: workers }, () => runTransaction(db, async (tx) => {
      const n = (await tx.get(ref)).get('n');
      tx.set(ref, { n: n + 1 });
    }, { maxAttempts: workers + 5 })));
    assert.equal((await getDoc(ref)).get('n'), workers);
  });

  t('runTransaction: one that only reads', async ({ db, doc, getDoc, setDoc, runTransaction }) => {
    const [a, missing] = [doc(db, 'lite-txn', `ro-${major}`), doc(db, 'lite-txn', `ro-missing-${major}`)];
    await setDoc(a, { a: 1 });
    // Its commit holds a verify of each document read: that a still has its update_time, and that
    // the missing one is still missing.
    const got = await runTransaction(db, async (tx) => [(await tx.get(a)).get('a'), (await tx.get(missing)).exists()]);
    assert.deepEqual(got, [1, false]);
    assert.equal((await getDoc(missing)).exists(), false);
  });

  t('runTransaction: one that throws writes nothing', async ({ db, doc, getDoc, setDoc, runTransaction }) => {
    const ref = doc(db, 'lite-txn', `throws-${major}`);
    await setDoc(ref, { a: 1 });
    await assert.rejects(runTransaction(db, async (tx) => {
      await tx.get(ref);
      tx.set(ref, { a: 2 });
      throw new Error('boom');
    }), /boom/);
    assert.equal((await getDoc(ref)).get('a'), 1);
  });

  t('runTransaction: several reads, a create and a delete', async ({ db, doc, getDoc, setDoc, runTransaction }) => {
    const [a, b, missing] = [doc(db, 'lite-txn', `all-a-${major}`), doc(db, 'lite-txn', `all-b-${major}`), doc(db, 'lite-txn', `all-missing-${major}`)];
    await setDoc(a, { v: 'a' });
    await setDoc(b, { v: 'b' });
    await runTransaction(db, async (tx) => {
      const snaps = [await tx.get(b), await tx.get(missing), await tx.get(a)];
      assert.deepEqual(snaps.map((s) => (s.exists() ? s.get('v') : null)), ['b', null, 'a']);
      tx.set(missing, { v: 'created' });
      tx.delete(b);
    });
    assert.equal((await getDoc(missing)).get('v'), 'created');
    assert.equal((await getDoc(b)).exists(), false);
  });

  t('getDocs of a collection', async ({ db, collection, getDocs }) => {
    await seed();
    const snap = await getDocs(collection(db, 'lite-values'));
    assert.equal(snap.size, stored.length);
  });
}
