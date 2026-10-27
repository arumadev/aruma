// The full Firebase JS SDK's Firestore (firebase 11, 12 and 13) against a live Aruma with the
// Document Database (F4 of v0.4). In Node the full package speaks gRPC, so this file covers what
// Firestore lite cannot: Listen (onSnapshot on a document and a query, getDoc and getDocFromServer,
// which are short-lived listens) and the Write stream (setDoc, updateDoc with serverTimestamp and
// increment, writeBatch). Pointed at Aruma as in documents.test.mjs: initializeFirestore(app,
// {host, ssl: false}) with the web config Aruma generated, under dev_open_rules. Run through
// documents-run.mjs (npm run test:documents). Every snapshot is checked exactly, and every step
// waits at most STEP ms.
import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const netguard = require.resolve('../../experiments/netguard.cjs');
assert.ok(require.cache[netguard], 'run this file through documents-run.mjs (npm run test:documents): the netguard is not loaded');
const { attempts } = require(netguard);

const dir = process.env.ARUMA_SMOKE_DIR;
const addr = process.env.ARUMA_SMOKE_DOCUMENTS_ADDR;
assert.ok(dir && addr, 'ARUMA_SMOKE_DIR and ARUMA_SMOKE_DOCUMENTS_ADDR are not set');
const config = JSON.parse(readFileSync(`${dir}/firebase-config.json`, 'utf8'));

const STEP = 5000;

after(() => {
  const off = attempts.filter((a) => !a.startsWith('ALLOW '));
  assert.deepEqual(off, [], `network access beyond loopback attempted: ${off.join(', ')}`);
});

// within fails with message if promise does not settle in STEP ms.
const within = (promise, message) => {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${message}: nothing within ${STEP} ms`)), STEP); }),
  ]).finally(() => clearTimeout(timer));
};

for (const major of ['11', '12', '13']) {
  const sdk = async (label) => {
    const { initializeApp, deleteApp } = await import(`firebase-${major}/app`);
    const fs = await import(`firebase-${major}/firestore`);
    const app = initializeApp(config, `listen-${major}-${label}-${Math.random()}`);
    const db = fs.initializeFirestore(app, { host: addr, ssl: false });
    return { ...fs, db, close: async () => { await fs.terminate(db); await deleteApp(app); } };
  };
  let n = 0;
  const t = (name, fn) => test(`firebase@${major}/${name}`, async () => {
    const a = await sdk('a');
    const b = await sdk('b'); // the second client
    const coll = `listen-web-${major}-${Date.now()}-${++n}`;
    try {
      await fn({ ...a, other: b, coll });
    } finally {
      await a.close();
      await b.close();
    }
  });

  // watch queues the snapshots of a document or query; next(what) waits for one.
  const watch = (s, ref, options = {}) => {
    const queue = [];
    let wake;
    let error;
    const unsubscribe = s.onSnapshot(ref, options, (snap) => { queue.push(snap); wake?.(); }, (e) => { error = e; wake?.(); });
    const next = async (what) => {
      const deadline = Date.now() + STEP;
      while (!queue.length && !error) {
        const left = deadline - Date.now();
        if (left <= 0) assert.fail(`${what}: no snapshot within ${STEP} ms`);
        await new Promise((resolve) => { wake = resolve; setTimeout(resolve, left); });
      }
      if (error) assert.fail(`${what}: listen failed: ${error.code} ${error.message}`);
      return queue.shift();
    };
    const quiet = async (what, ms = 400) => {
      await new Promise((r) => setTimeout(r, ms));
      assert.equal(queue.length, 0, `${what}: an unexpected snapshot arrived`);
    };
    return { next, quiet, unsubscribe };
  };
  const docs = (snap) => snap.docs.map((d) => `${d.id}:${d.get('n')}`);
  const changes = (snap) => snap.docChanges().map((c) => [c.type, c.doc.id, c.doc.get('n'), c.oldIndex, c.newIndex]);

  t('onSnapshot on a document: set, update, delete by a second client', async (s) => {
    const mine = s.doc(s.db, s.coll, 'd');
    const theirs = s.doc(s.other.db, s.coll, 'd');
    const w = watch(s, mine);
    try {
      let snap = await w.next('initial');
      assert.equal(snap.exists(), false);
      assert.equal(snap.metadata.fromCache, false, 'the first snapshot should come from the server');

      await within(s.setDoc(theirs, { n: 1, s: 'a' }), 'setDoc');
      snap = await w.next('after set');
      assert.deepEqual(snap.data(), { n: 1, s: 'a' });
      assert.equal(snap.metadata.hasPendingWrites, false);

      await within(s.updateDoc(theirs, { n: s.increment(4) }), 'updateDoc');
      snap = await w.next('after update');
      assert.deepEqual(snap.data(), { n: 5, s: 'a' });

      await within(s.deleteDoc(theirs), 'deleteDoc');
      snap = await w.next('after delete');
      assert.equal(snap.exists(), false);
    } finally {
      w.unsubscribe();
    }
  });

  t('onSnapshot on a query: added, modified, removed and deleted documents', async (s) => {
    const theirs = (id) => s.doc(s.other.db, s.coll, id);
    await within(s.setDoc(theirs('a'), { n: 1 }), 'seed a');
    await within(s.setDoc(theirs('b'), { n: 2 }), 'seed b');
    const w = watch(s, s.query(s.collection(s.db, s.coll), s.where('n', '>', 0), s.orderBy('n')));
    try {
      let snap = await w.next('initial');
      assert.deepEqual(docs(snap), ['a:1', 'b:2']);
      assert.deepEqual(changes(snap), [['added', 'a', 1, -1, 0], ['added', 'b', 2, -1, 1]]);

      await within(s.setDoc(theirs('c'), { n: 3 }), 'add c');
      snap = await w.next('add');
      assert.deepEqual(docs(snap), ['a:1', 'b:2', 'c:3']);
      assert.deepEqual(changes(snap), [['added', 'c', 3, -1, 2]]);

      await within(s.setDoc(theirs('a'), { n: 10 }), 'modify a');
      snap = await w.next('modify');
      assert.deepEqual(docs(snap), ['b:2', 'c:3', 'a:10']);
      assert.deepEqual(changes(snap), [['modified', 'a', 10, 0, 2]]);

      await within(s.setDoc(theirs('b'), { n: 0 }), 'move b out');
      snap = await w.next('leave');
      assert.deepEqual(docs(snap), ['c:3', 'a:10']);
      assert.deepEqual(changes(snap), [['removed', 'b', 2, 0, -1]]);

      await within(s.deleteDoc(theirs('c')), 'delete c');
      snap = await w.next('delete');
      assert.deepEqual(docs(snap), ['a:10']);
      assert.deepEqual(changes(snap), [['removed', 'c', 3, 0, -1]]);
    } finally {
      w.unsubscribe();
    }
  });

  t('Write stream: setDoc and updateDoc with serverTimestamp and increment', async (s) => {
    const ref = s.doc(s.db, s.coll, 'w');
    const before = Date.now();
    await within(s.setDoc(ref, { n: 1, at: s.serverTimestamp(), nested: { c: s.increment(2) } }), 'setDoc');
    let snap = await within(s.getDocFromServer(ref), 'getDocFromServer');
    assert.equal(snap.metadata.fromCache, false);
    assert.equal(snap.get('n'), 1);
    assert.equal(snap.get('nested.c'), 2);
    const at = snap.get('at');
    assert.equal(at.constructor.name, 'Timestamp');
    assert.ok(Math.abs(at.toMillis() - before) < 60_000, `serverTimestamp ${at.toDate().toISOString()} is not near now`);

    await within(s.updateDoc(ref, { n: s.increment(5), 'nested.c': s.increment(-1), at2: s.serverTimestamp() }), 'updateDoc');
    snap = await within(s.getDocFromServer(ref), 'getDocFromServer after update');
    assert.equal(snap.get('n'), 6);
    assert.equal(snap.get('nested.c'), 1);
    assert.deepEqual(snap.get('at'), at, 'the update must leave "at" alone');
    assert.ok(snap.get('at2').toMillis() >= at.toMillis(), 'at2 is not after at');

    // updateDoc on a missing document is NOT_FOUND from the server.
    await assert.rejects(within(s.updateDoc(s.doc(s.db, s.coll, 'missing'), { n: 1 }), 'updateDoc missing'), (e) => e.code === 'not-found');
  });

  t('getDoc is a short-lived listen: it sees the server state, and a missing document', async (s) => {
    const theirs = s.doc(s.other.db, s.coll, 'g');
    const mine = s.doc(s.db, s.coll, 'g');
    let snap = await within(s.getDoc(mine), 'getDoc missing');
    assert.equal(snap.exists(), false);
    await within(s.setDoc(theirs, { n: 7, tags: ['x', 'y'], m: { k: true } }), 'setDoc');
    snap = await within(s.getDoc(mine), 'getDoc');
    assert.deepEqual(snap.data(), { n: 7, tags: ['x', 'y'], m: { k: true } });
    assert.equal(snap.metadata.fromCache, false);
    const all = await within(s.getDocs(s.collection(s.db, s.coll)), 'getDocs');
    assert.deepEqual(docs(all), ['g:7']);
  });

  t('writeBatch: set, update and delete commit together, and a listener sees them as one snapshot', async (s) => {
    const theirs = (id) => s.doc(s.other.db, s.coll, id);
    await within(s.setDoc(theirs('u'), { n: 1 }), 'seed u');
    await within(s.setDoc(theirs('x'), { n: 2 }), 'seed x');
    const w = watch(s, s.query(s.collection(s.db, s.coll), s.orderBy('n')));
    try {
      assert.deepEqual(docs(await w.next('initial')), ['u:1', 'x:2']);

      const batch = s.writeBatch(s.other.db);
      batch.set(theirs('new'), { n: 3, at: s.serverTimestamp() });
      batch.update(theirs('u'), { n: s.increment(10) });
      batch.delete(theirs('x'));
      await within(batch.commit(), 'batch.commit');

      const snap = await w.next('batch');
      assert.deepEqual(docs(snap), ['new:3', 'u:11']);
      // The web SDK numbers a batch's changes as it applies them: removals, then the rest in key order.
      assert.deepEqual(changes(snap).sort(), [['added', 'new', 3, -1, 1], ['modified', 'u', 11, 0, 1], ['removed', 'x', 2, 1, -1]].sort());
      await w.quiet('after batch');
    } finally {
      w.unsubscribe();
    }
  });
}
