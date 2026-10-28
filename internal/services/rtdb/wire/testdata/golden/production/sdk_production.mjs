// sdk_production.mjs - what the official JS SDK (firebase 13.0.0, in Node) reports for the
// same cases as capture_production.mjs: values, error codes and messages, and the SDK's own
// warnings, against production Firebase or Aruma.
//
//   node sdk_production.mjs <config.json> <out.json>
//
// config.json: {target, databaseURL, apiKey, projectId, run, email, password, authEmulator?}
// The firebase package is loaded from test/e2e/rtdb-rules/node_modules (npm ci there),
// or from $SDK_DIR/node_modules. The SDK's warnings are what it prints with console.warn.
// The password and tokens are never written out.
import fs from 'node:fs';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const sdkDir = process.env.SDK_DIR || path.join(here, '../../../../../../../test/e2e/rtdb-rules');
const require = createRequire(path.join(sdkDir, 'package.json'));
const { initializeApp } = require('firebase/app');
const { getAuth, connectAuthEmulator, signInWithEmailAndPassword, signOut } = require('firebase/auth');
const db_ = require('firebase/database');
const { getDatabase, ref, set, get, update, remove, push, onValue, query, orderByChild, orderByValue, limitToFirst, onDisconnect, runTransaction, serverTimestamp, increment, goOffline } = db_;

const cfg = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const OUT = process.argv[3];
const ROOT = `aruma-ref/${cfg.run}`;
const OPEN = `${ROOT}/open`;
const CLOSED = `${ROOT}/closed`;
const results = [];
const logs = [];
const redact = (s) => {
  s = String(s);
  for (const [a, b] of Object.entries(cfg.redact || {})) s = s.split(a).join(b);
  return s.split(cfg.run).join('<run>').replace(/eyJ[\w-]+\.[\w-]+\.[\w-]*/g, '<token>');
};

const origWarn = console.warn;
console.warn = (...a) => logs.push(redact(a.join(' ')));
const origErr = console.error;
console.error = (...a) => logs.push(redact(a.join(' ')));

const app = initializeApp({ apiKey: cfg.apiKey, projectId: cfg.projectId, databaseURL: cfg.databaseURL });
const auth = getAuth(app);
if (cfg.authEmulator) connectAuthEmulator(auth, cfg.authEmulator, { disableWarnings: true });
const cred = await signInWithEmailAndPassword(auth, cfg.email, cfg.password);
const db = getDatabase(app);

const within = (ms, p) => Promise.race([p, new Promise((_, rej) => setTimeout(() => rej(Object.assign(new Error('timeout'), { code: 'timeout' })), ms))]);
async function op(name, fn) {
  try {
    const v = await within(15000, fn());
    results.push({ op: name, ok: true, value: v === undefined ? null : v });
  } catch (e) {
    results.push({ op: name, ok: false, code: e.code ?? null, message: redact(e.message) });
  }
}
const listenOnce = (q) =>
  new Promise((res, rej) => {
    const off = onValue(
      q,
      (s) => {
        off();
        res(s.val());
      },
      (e) => rej(e),
    );
  });

await op('set open/sdk', () => set(ref(db, `${OPEN}/sdk`), { a: { x: 2 }, b: { x: 1 }, c: { x: 3 } }));
await op('get open/sdk', async () => (await get(ref(db, `${OPEN}/sdk`))).val());
await op('get orderByChild(x) limitToFirst(1) without index', async () => (await get(query(ref(db, `${OPEN}/sdk`), orderByChild('x'), limitToFirst(1)))).val());
await op('onValue orderByChild(x) without index', () => listenOnce(query(ref(db, `${OPEN}/sdk`), orderByChild('x'), limitToFirst(2))));
await op('onValue orderByValue without index', () => listenOnce(query(ref(db, `${OPEN}/sdk`), orderByValue(), limitToFirst(1))));
await op('set closed', () => set(ref(db, `${CLOSED}/x`), 1));
await op('get closed', async () => (await get(ref(db, `${CLOSED}/x`))).val());
await op('onValue closed', () => listenOnce(ref(db, `${CLOSED}/x`)));
await op('update closed', () => update(ref(db, `${CLOSED}/x`), { a: 1 }));
await op('update mixed', () => update(ref(db, ROOT), { 'open/m1': 1, 'closed/m2': 2 }));
await op('push closed', () => push(ref(db, `${CLOSED}/list`), 1));
await op('remove closed', () => remove(ref(db, `${CLOSED}/x`)));
await op('onDisconnect.set closed', () => onDisconnect(ref(db, `${CLOSED}/x`)).set(1));
await op('onDisconnect.update closed', () => onDisconnect(ref(db, `${CLOSED}/x`)).update({ a: 1 }));
await op('onDisconnect.cancel closed', () => onDisconnect(ref(db, `${CLOSED}/x`)).cancel());
await op('set open/v string (fails .validate)', () => set(ref(db, `${OPEN}/v`), 'not a number'));
await op('transaction closed', async () => {
  const r = await runTransaction(ref(db, `${CLOSED}/t`), (v) => (v || 0) + 1);
  return { committed: r.committed, value: r.snapshot.val() };
});
await op('transaction open', async () => {
  const r = await runTransaction(ref(db, `${OPEN}/t`), (v) => (v || 0) + 1);
  return { committed: r.committed, value: r.snapshot.val() };
});
await op('set server values', () => set(ref(db, `${OPEN}/sv`), { t: serverTimestamp(), i: increment(2) }));
await op('get server values (types)', async () => {
  const v = (await get(ref(db, `${OPEN}/sv`))).val();
  return { t: typeof v.t, i: v.i };
});
await op('set numbers', () => set(ref(db, `${OPEN}/num`), { a: 1.0, b: -0, c: 12345678901234567890, d: 0.1, e: 2 ** 53 + 2 }));
await op('get numbers', async () => {
  const v = (await get(ref(db, `${OPEN}/num`))).val();
  return { ...v, bIsNegativeZero: Object.is(v.b, -0) };
});
await op('set array', () => set(ref(db, `${OPEN}/arr`), ['a', null, 'c']));
await op('get array', async () => (await get(ref(db, `${OPEN}/arr`))).val());
await op('set {0,2} object', () => set(ref(db, `${OPEN}/arr2`), { 0: 'a', 2: 'c' }));
await op('get {0,2} object', async () => (await get(ref(db, `${OPEN}/arr2`))).val());

// A listen the rules grant only while signed in, then sign out: the SDK cancels it.
let cancelled = null;
const live = await new Promise((res) => {
  let first = true;
  const off = onValue(
    ref(db, `${OPEN}/sdk/a`),
    () => {
      if (first) {
        first = false;
        res(off);
      }
    },
    (e) => (cancelled = { code: e.code ?? null, message: redact(e.message) }),
  );
});
await signOut(auth);
await new Promise((r) => setTimeout(r, 3000));
results.push({ op: 'listen cancelled after signOut', ok: cancelled !== null, value: cancelled });
live();
await signInWithEmailAndPassword(auth, cfg.email, cfg.password);
await op('remove open', () => remove(ref(db, OPEN)));
goOffline(db);

console.warn = origWarn;
console.error = origErr;
const doc = {
  source: cfg.target,
  ...(cfg.target === 'production' ? { project: '<redacted>' } : {}),
  sdk: 'firebase 13.0.0 (Node, WebSocket)',
  captured: new Date().toISOString().slice(0, 10),
  script: 'sdk_production.mjs',
  uid: cred.user.uid === undefined ? null : '<uid>',
  results,
  logs: [...new Set(logs)],
};
fs.writeFileSync(OUT, redact(JSON.stringify(doc, null, 2)).split(cred.user.uid).join('<uid>') + '\n');
console.log(`wrote ${OUT}: ${results.length} results, ${doc.logs.length} log lines`);
process.exit(0);
