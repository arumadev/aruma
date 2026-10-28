// firebase-admin's Firestore (@google-cloud/firestore) Listen against a live Aruma with the
// Document Database (F4 of v0.4): onSnapshot on a document, a query and a limit query, with the
// adds, modifies, removes and deletes made by a second client, unsubscribe and resubscribe, and
// two listeners on one client. Listen is gRPC even with preferRest, so documents-run.mjs runs
// this file on the grpc transport alone. Every snapshot is checked exactly, and every step
// waits at most STEP ms.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { initializeApp, cert, deleteApp } from 'firebase-admin/app';
import { getFirestore, FieldValue } from 'firebase-admin/firestore';

const require = createRequire(import.meta.url);
const netguard = require.resolve('../../e2e/netguard.cjs');
assert.ok(require.cache[netguard], 'run this file through documents-run.mjs (npm run test:documents): the netguard is not loaded');
const { attempts } = require(netguard);

const dir = process.env.ARUMA_SMOKE_DIR;
const addr = process.env.ARUMA_SMOKE_DOCUMENTS_ADDR;
assert.ok(dir && addr, 'ARUMA_SMOKE_DIR and ARUMA_SMOKE_DOCUMENTS_ADDR are not set');
const project = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8')).projectId;

const STEP = 5000;
let app, db, other, otherApp;
let n = 0;
const coll = (name) => `listen-node-${name}-${Date.now()}-${++n}`;

before(async () => {
  const mk = async (name) => {
    const a = initializeApp({ credential: cert(`${dir}/service-account.json`), projectId: project }, name);
    const f = getFirestore(a);
    f.settings({ host: addr, ssl: false });
    await f.initializeIfNeeded('listen');
    return [a, f];
  };
  [app, db] = await mk('listen-a');
  [otherApp, other] = await mk('listen-b');
});

after(async () => {
  await db?.terminate();
  await other?.terminate();
  if (app) await deleteApp(app);
  if (otherApp) await deleteApp(otherApp);
  const off = attempts.filter((a) => !a.startsWith('ALLOW '));
  assert.deepEqual(off, [], `network access beyond loopback attempted: ${off.join(', ')}`);
});

// watch subscribes to a document or query and queues its snapshots; next(what) waits for one.
function watch(target) {
  const queue = [];
  let wake;
  let error;
  const unsubscribe = target.onSnapshot((s) => { queue.push(s); wake?.(); }, (e) => { error = e; wake?.(); });
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
}

const docs = (s) => s.docs.map((d) => `${d.id}:${d.get('n')}`);
const changes = (s) => s.docChanges().map((c) => [c.type, c.doc.id, c.doc.get('n'), c.oldIndex, c.newIndex]);

test('document: set, update, delete by a second client', async () => {
  const name = coll('doc');
  const w = watch(db.collection(name).doc('d'));
  try {
    let s = await w.next('initial');
    assert.equal(s.exists, false);
    assert.equal(s.readTime.constructor.name, 'Timestamp');

    await other.collection(name).doc('d').set({ n: 1, s: 'a' });
    s = await w.next('after set');
    assert.equal(s.exists, true);
    assert.deepEqual(s.data(), { n: 1, s: 'a' });
    assert.ok(s.createTime && s.updateTime);

    await other.collection(name).doc('d').update({ n: FieldValue.increment(4) });
    s = await w.next('after update');
    assert.deepEqual(s.data(), { n: 5, s: 'a' });

    await other.collection(name).doc('d').delete();
    s = await w.next('after delete');
    assert.equal(s.exists, false);
  } finally {
    w.unsubscribe();
  }
});

test('query: added, modified, removed and deleted documents', async () => {
  const name = coll('query');
  const c = other.collection(name);
  await c.doc('a').set({ n: 1 });
  await c.doc('b').set({ n: 2 });
  const w = watch(db.collection(name).where('n', '>', 0).orderBy('n'));
  try {
    let s = await w.next('initial');
    assert.deepEqual(docs(s), ['a:1', 'b:2']);
    assert.deepEqual(changes(s), [['added', 'a', 1, -1, 0], ['added', 'b', 2, -1, 1]]);
    assert.equal(s.size, 2);

    await c.doc('c').set({ n: 3 });
    s = await w.next('add');
    assert.deepEqual(docs(s), ['a:1', 'b:2', 'c:3']);
    assert.deepEqual(changes(s), [['added', 'c', 3, -1, 2]]);

    await c.doc('a').set({ n: 10 });
    s = await w.next('modify');
    assert.deepEqual(docs(s), ['b:2', 'c:3', 'a:10']);
    assert.deepEqual(changes(s), [['modified', 'a', 10, 0, 2]]);

    await c.doc('b').set({ n: 0 }); // leaves the query
    s = await w.next('leave');
    assert.deepEqual(docs(s), ['c:3', 'a:10']);
    assert.deepEqual(changes(s), [['removed', 'b', 2, 0, -1]]);

    await c.doc('c').delete();
    s = await w.next('delete');
    assert.deepEqual(docs(s), ['a:10']);
    assert.deepEqual(changes(s), [['removed', 'c', 3, 0, -1]]);
  } finally {
    w.unsubscribe();
  }
});

test('limit query: a document pushed out of the limit and pulled back in', async () => {
  const name = coll('limit');
  const c = other.collection(name);
  for (const i of [1, 2, 3]) await c.doc(`d${i}`).set({ n: i });
  const w = watch(db.collection(name).orderBy('n').limit(2));
  try {
    let s = await w.next('initial');
    assert.deepEqual(docs(s), ['d1:1', 'd2:2']);

    await c.doc('d0').set({ n: 0 });
    s = await w.next('push out');
    assert.deepEqual(docs(s), ['d0:0', 'd1:1']);
    assert.deepEqual(changes(s), [['removed', 'd2', 2, 1, -1], ['added', 'd0', 0, -1, 0]]);

    await c.doc('d0').delete();
    s = await w.next('pull in');
    assert.deepEqual(docs(s), ['d1:1', 'd2:2']);
    assert.deepEqual(changes(s), [['removed', 'd0', 0, 0, -1], ['added', 'd2', 2, -1, 1]]);
  } finally {
    w.unsubscribe();
  }
});

test('unsubscribe stops snapshots, and a new listener starts again from the current state', async () => {
  const name = coll('resub');
  const ref = other.collection(name).doc('d');
  await ref.set({ n: 1 });
  const first = watch(db.collection(name).doc('d'));
  let s = await first.next('first initial');
  assert.deepEqual(s.data(), { n: 1 });
  first.unsubscribe();
  await ref.set({ n: 2 });
  await first.quiet('after unsubscribe');

  const second = watch(db.collection(name).doc('d'));
  try {
    s = await second.next('resubscribed initial');
    assert.deepEqual(s.data(), { n: 2 });
    await ref.set({ n: 3 });
    s = await second.next('resubscribed update');
    assert.deepEqual(s.data(), { n: 3 });
  } finally {
    second.unsubscribe();
  }
});

test('two listeners on one client stream independently', async () => {
  const a = coll('two-a');
  const b = coll('two-b');
  const wa = watch(db.collection(a).orderBy('n'));
  const wb = watch(db.collection(b).orderBy('n'));
  try {
    assert.deepEqual(docs(await wa.next('a initial')), []);
    assert.deepEqual(docs(await wb.next('b initial')), []);

    await other.collection(a).doc('x').set({ n: 1 });
    assert.deepEqual(docs(await wa.next('a add')), ['x:1']);
    await wb.quiet('b after a write');

    await other.collection(b).doc('y').set({ n: 2 });
    assert.deepEqual(docs(await wb.next('b add')), ['y:2']);
    await wa.quiet('a after b write');

    wa.unsubscribe();
    await other.collection(b).doc('z').set({ n: 3 });
    assert.deepEqual(docs(await wb.next('b after a unsubscribed')), ['y:2', 'z:3']);
  } finally {
    wa.unsubscribe();
    wb.unsubscribe();
  }
});
