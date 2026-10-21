// The app a developer writes: the web config exactly as Aruma's Management API serves it, its
// databaseURL included, and nothing else. No connectDatabaseEmulator and no connectAuthEmulator:
// both SDKs stay in production mode. Auth reaches Aruma through @aruma/web's Vite plugin
// (vite.config.mjs); the database needs nothing, its URL is Aruma's.
import firebaseConfig from '../.generated/firebase-config.json';

export function start(appSdk, authSdk, dbSdk) {
  const app = appSdk.initializeApp(firebaseConfig);
  const auth = authSdk.getAuth(app);
  const db = dbSdk.getDatabase(app);

  // What the tests drive and observe. log keeps the database SDK's own log, for a failure's report.
  const log = [];
  dbSdk.enableLogging((m) => {
    log.push(m.slice(0, 300));
    if (log.length > 3000) log.shift();
  });
  const events = {};
  const off = {};

  // listen records the events of one query under events[name]: a value event as { keys, val }
  // (keys in the query's order), a child event as { key, val, prev }, a cancellation as
  // { cancel: code }. query is a list of [constraint, ...args], e.g. [['orderByChild', 'score']].
  function listen(name, path, { on = 'value', query = [] } = {}) {
    const ref = dbSdk.ref(db, path);
    const q = query.length ? dbSdk.query(ref, ...query.map(([fn, ...args]) => dbSdk[fn](...args))) : ref;
    const out = (events[name] = []);
    const subscribe = { value: dbSdk.onValue, child_added: dbSdk.onChildAdded, child_removed: dbSdk.onChildRemoved, child_changed: dbSdk.onChildChanged }[on];
    off[name] = subscribe(
      q,
      (snap, prev) => {
        if (on !== 'value') {
          out.push({ key: snap.key, val: snap.val(), prev: prev ?? null });
          return;
        }
        const keys = [];
        snap.forEach((c) => {
          keys.push(c.key);
        });
        out.push({ keys, val: snap.val() });
      },
      (err) => out.push({ cancel: err.code ?? String(err) }),
    );
  }
  const unlisten = (name) => off[name]?.();

  window.e2e = { app, auth, authSdk, db, dbSdk, version: appSdk.SDK_VERSION, log, events, listen, unlisten };
  document.getElementById('status').textContent = `ready ${appSdk.SDK_VERSION}`;
}
