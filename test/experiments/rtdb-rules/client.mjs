// The client of rtdb-rules.sh: the official web SDK (firebase 13.0.0) signs in with Aruma Auth
// and works on Realtime Database data that rules protect per user; then the admin disables the
// user, and the client must lose access as a Firebase client does.
//
//   node client.mjs <config.json>
//
// config.json: {base, apiKey, projectId, namespace}. Auth goes to Aruma's Identity Toolkit
// through connectAuthEmulator, which only changes the host; the tokens are Aruma's own, signed,
// and the database verifies them. It prints one JSON line per event; "step":"ready" tells the
// script to disable the user. It exits 0 when every check held, 1 otherwise.
import fs from 'node:fs';
import { initializeApp } from 'firebase/app';
import { getAuth, connectAuthEmulator, createUserWithEmailAndPassword, onAuthStateChanged } from 'firebase/auth';
import { getDatabase, ref, set, get, onValue, update } from 'firebase/database';

const cfg = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const say = (o) => console.log(JSON.stringify({ t: Date.now(), ...o }));
const app = initializeApp({
  apiKey: cfg.apiKey,
  projectId: cfg.projectId,
  databaseURL: `${cfg.base}?ns=${cfg.namespace}`,
});
const auth = getAuth(app);
connectAuthEmulator(auth, cfg.base, { disableWarnings: true });
const db = getDatabase(app);

let failures = 0;
const check = (what, ok, detail) => {
  say({ check: what, ok, ...(detail === undefined ? {} : { detail }) });
  if (!ok) failures++;
};
const denied = async (what, p) => {
  try {
    await p;
    check(what, false, 'allowed');
  } catch (e) {
    check(what, /permission_denied|Permission denied/i.test(String(e.code ?? '') + String(e.message)), e.message);
  }
};
const within = (ms, p) => Promise.race([p, new Promise((r) => setTimeout(() => r('timeout'), ms))]);

let signedOut;
const signedOutP = new Promise((r) => (signedOut = r));
let signedIn = false;
onAuthStateChanged(auth, (u) => {
  say({ auth: u ? u.uid : null });
  if (u) signedIn = true;
  else if (signedIn) signedOut();
});

const cred = await createUserWithEmailAndPassword(auth, `rules-${Date.now()}@example.com`, 'secret-password');
const uid = cred.user.uid;
say({ step: 'signed up', uid });

// Per-user data: the user's own profile, nobody else's.
await set(ref(db, `users/${uid}`), { name: 'Sara', age: 30 });
check('own write', true);
check('own read', (await get(ref(db, `users/${uid}/name`))).val() === 'Sara');
await denied('another user\'s write', set(ref(db, 'users/someone-else/name'), 'x'));
await denied('another user\'s read', get(ref(db, 'users/someone-else')));
await denied('.validate refuses a bad age', set(ref(db, `users/${uid}/age`), 'old'));
await denied('an update reaching another user', update(ref(db, 'users'), { [`${uid}/a`]: 1, 'someone-else/a': 1 }));

// A listen on the user's own data, which the disabling must cancel.
let cancelled;
const cancelledP = new Promise((r) => (cancelled = r));
const values = [];
onValue(
  ref(db, `users/${uid}`),
  (s) => values.push(s.val()),
  (e) => cancelled(e),
);
await new Promise((r) => setTimeout(r, 300));
check('listen delivers', values.length > 0 && values.at(-1).name === 'Sara');

say({ step: 'ready', uid }); // the script now disables the user
const start = Date.now();
const c = await within(5000, cancelledP);
check('the listen is cancelled with permission_denied', c !== 'timeout' && /permission_denied/i.test(c.code ?? c.message), c === 'timeout' ? c : c.message);
say({ cancelledAfterMs: Date.now() - start });
// The SDK refreshes its token after ac; the refresh fails for a disabled user and Auth signs
// the user out.
const out = await within(10000, signedOutP);
check('the user is signed out', out !== 'timeout' && auth.currentUser === null);
await denied('a write after the disabling', within(5000, set(ref(db, `users/${uid}/name`), 'back')).then((r) => {
  if (r === 'timeout') throw new Error('no answer within 5 s');
}));

say({ result: failures === 0 ? 'ok' : `${failures} check(s) failed` });
process.exit(failures === 0 ? 0 : 1);
