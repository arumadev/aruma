// firebase-admin's Realtime Database against a live Aruma in its production form: databaseURL
// https://smoke-app-default-rtdb.rtdb.aruma.test, served over TLS by genconfig's front (ADR-018).
// The Admin SDK talks to it over the WebSocket protocol, as @firebase/database-compat does; only
// the rules go over REST (/.settings/rules.json).
//
// Run it through rtdb-run.mjs (npm run test:rtdb), which starts the server and preloads
// test/experiments/netguard.cjs and test/compat/firebase-js/rtdb/hosts.cjs: every connection off
// this machine fails, and *.aruma.test resolves to the TLS front inside this process only. Or,
// with ARUMA_RTDB_DATABASE_URL, a server behind a front of its own, such as Caddy.
//
// onDisconnect is not covered: it is a client-session feature, and an Admin SDK process that
// never goes away has no use for it; the client SDK suites exercise it.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { initializeApp, deleteApp } from 'firebase-admin/app';
import { getDatabase } from 'firebase-admin/database';
// firebase-admin's cert() ignores token_uri and sends the assertion to Google; this credential
// makes the same exchange at the key file's token_uri, which is Aruma's.
import { arumaCredential } from '../../experiments/admin-node/aruma-credential.mjs';

const require = createRequire(import.meta.url);
const netguard = require.resolve('../../experiments/netguard.cjs');
assert.ok(require.cache[netguard], 'run this file through rtdb-run.mjs (npm run test:rtdb): the netguard is not loaded');
const { attempts } = require(netguard);

const dir = process.env.ARUMA_SMOKE_DIR;
assert.ok(dir, 'ARUMA_SMOKE_DIR is not set');
const expected = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8'));
const keyPath = `${dir}/service-account.json`;
const databaseURL = expected.databaseURL;
if (process.env.ARUMA_RTDB_DATABASE_URL) {
  // A server behind a front of its own (rtdb-run.mjs): any rtdb.domain, any port.
  assert.equal(databaseURL, process.env.ARUMA_RTDB_DATABASE_URL);
  assert.ok(databaseURL.startsWith(`https://${expected.projectId}-default-rtdb.`), databaseURL);
} else {
  assert.equal(databaseURL, `https://${expected.projectId}-default-rtdb.rtdb.aruma.test`);
}

const apps = [];
function app(name, extra = {}) {
  const a = initializeApp({ credential: arumaCredential(keyPath), projectId: expected.projectId, databaseURL, ...extra }, name);
  apps.push(a);
  return a;
}
const admin = getDatabase(app('rtdb-admin'));

const permissionDenied = /permission.denied/i;

// rules is what the data tests below run under: users/$uid for their owner only, an index on
// scores, and one public node.
const rules = {
  rules: {
    users: { $uid: { '.read': 'auth != null && auth.uid == $uid', '.write': 'auth != null && auth.uid == $uid' } },
    scores: { '.indexOn': ['score'] },
    public: { '.read': true },
  },
};

// A server kept running between runs (ARUMA_SMOKE_DIR) still holds the last run's data.
before(() => admin.ref().remove());

test('set and get', async () => {
  const ref = admin.ref('smoke/a');
  await ref.set({ n: 1, s: 'سلام', nested: { t: true } });
  assert.deepEqual((await ref.get()).val(), { n: 1, s: 'سلام', nested: { t: true } });
  assert.equal((await ref.child('nested/t').get()).val(), true);
  assert.equal((await admin.ref('smoke/missing').get()).exists(), false);
});

test('update writes several paths at once', async () => {
  await admin.ref('smoke').update({ 'a/n': 2, 'a/nested': null, 'b/y': 'new' });
  assert.deepEqual((await admin.ref('smoke').get()).val(), { a: { n: 2, s: 'سلام' }, b: { y: 'new' } });
});

test('push adds children under ordered keys', async () => {
  const list = admin.ref('smoke/list');
  const first = await list.push({ i: 1 });
  const second = list.push();
  await second.set({ i: 2 });
  assert.match(first.key, /^[-0-9A-Za-z_]{20}$/);
  assert.ok(first.key < second.key, `${first.key} < ${second.key}`);
  const snap = await list.get();
  const keys = [];
  snap.forEach((c) => {
    keys.push(c.key);
  });
  assert.deepEqual(keys, [first.key, second.key]);
  assert.deepEqual(snap.child(second.key).val(), { i: 2 });
});

test('remove deletes a node', async () => {
  await admin.ref('smoke/b').remove();
  assert.equal((await admin.ref('smoke/b').get()).exists(), false);
  assert.deepEqual((await admin.ref('smoke').get()).val().a, { n: 2, s: 'سلام' });
});

test('transaction', async () => {
  // Two connections race on one counter: the server must refuse every write based on a stale
  // value, so each client retries until all ten increments land.
  const other = getDatabase(app('rtdb-admin-2'));
  const increment = (db) => db.ref('smoke/counter').transaction((n) => (n ?? 0) + 1);
  const results = await Promise.all([admin, other].flatMap((db) => Array.from({ length: 5 }, () => increment(db))));
  assert.ok(results.every((r) => r.committed));
  assert.equal((await admin.ref('smoke/counter').get()).val(), 10);
  // Returning undefined aborts. A client without the value cached first runs the update on
  // null; its write of 0 then fails the server's hash check and the update runs again on 10.
  const aborted = await admin.ref('smoke/counter').transaction((n) => (n === 10 ? undefined : 0));
  assert.equal(aborted.committed, false);
  assert.equal(aborted.snapshot.val(), 10);
  assert.equal((await other.ref('smoke/counter').get()).val(), 10);
});

test('getRules, getRulesJSON and setRules round trip', async () => {
  await admin.setRules(rules);
  assert.deepEqual(await admin.getRulesJSON(), rules);
  // The source keeps its comments; the JSON form drops them.
  const source = '{\n  // set by the smoke test\n  "rules": ' + JSON.stringify(rules.rules, null, 2) + '\n}\n';
  await admin.setRules(source);
  assert.equal(await admin.getRules(), source);
  assert.deepEqual(await admin.getRulesJSON(), rules);
  await assert.rejects(admin.setRules('{"rules": {".read": "nonsense("}}'));
  assert.deepEqual(await admin.getRulesJSON(), rules, 'a rejected source leaves the rules as they were');
});

test('orderByChild with limitToFirst and startAt (with .indexOn)', async () => {
  await admin.ref('scores').set({ a: { score: 30 }, b: { score: 10 }, c: { score: 20 }, d: { score: 5 }, e: { name: 'no score' } });
  const keysOf = (snap) => {
    const keys = [];
    snap.forEach((c) => {
      keys.push(c.key);
    });
    return keys;
  };
  const scores = admin.ref('scores').orderByChild('score');
  // Children without the value sort first, then by the value.
  assert.deepEqual(keysOf(await scores.limitToFirst(3).get()), ['e', 'd', 'b']);
  const top = await scores.startAt(10).limitToFirst(2).get();
  assert.deepEqual(keysOf(top), ['b', 'c']);
  assert.deepEqual(top.val(), { b: { score: 10 }, c: { score: 20 } }, 'the result holds only what the query matched');
  assert.deepEqual(keysOf(await scores.limitToLast(2).get()), ['c', 'a']);
  assert.deepEqual(keysOf(await admin.ref('scores').orderByKey().startAt('c').get()), ['c', 'd', 'e']);
});

test('databaseAuthVariableOverride {uid: "u1"} is held to the rules', async () => {
  await admin.setRules(rules);
  await admin.ref('users/u2').set({ name: 'two' });
  const u1 = getDatabase(app('rtdb-u1', { databaseAuthVariableOverride: { uid: 'u1' } }));
  await u1.ref('users/u1').set({ name: 'one' });
  assert.deepEqual((await u1.ref('users/u1').get()).val(), { name: 'one' });
  await u1.ref('users/u1').update({ age: 3 });
  assert.deepEqual((await admin.ref('users/u1').get()).val(), { name: 'one', age: 3 });

  await assert.rejects(u1.ref('users/u2').set({ name: 'taken' }), permissionDenied);
  await assert.rejects(u1.ref('users/u2').get(), permissionDenied);
  await assert.rejects(u1.ref('users').get(), permissionDenied);
  await assert.rejects(u1.ref('smoke/a').get(), permissionDenied);
  assert.deepEqual((await admin.ref('users/u2').get()).val(), { name: 'two' }, 'the denied write left no trace');
  assert.deepEqual((await u1.ref('public').get()).val(), null);
});

test('databaseAuthVariableOverride null acts unauthenticated', async () => {
  await admin.ref('public').set({ motd: 'hello' });
  const anon = getDatabase(app('rtdb-null', { databaseAuthVariableOverride: null }));
  assert.deepEqual((await anon.ref('public').get()).val(), { motd: 'hello' });
  await assert.rejects(anon.ref('users/u1').get(), permissionDenied);
  await assert.rejects(anon.ref('users/u1').set({ name: 'anon' }), permissionDenied);
  await assert.rejects(anon.ref('public/motd').set('defaced'), permissionDenied);
  assert.equal((await admin.ref('public/motd').get()).val(), 'hello');
});

after(async () => {
  await Promise.all(apps.map((a) => deleteApp(a)));
  const off = attempts.filter((a) => !a.startsWith('ALLOW '));
  assert.deepEqual(off, [], `network access beyond loopback attempted: ${off.join(', ')}`);
});
