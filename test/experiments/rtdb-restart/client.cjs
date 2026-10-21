// The client of rtdb-restart.sh: after a graceful server restart the JS SDK must reconnect,
// and a write queued while the server was down must land.
//
//   node client.cjs <base url> <namespace>    the development form, <base url>?ns=<namespace>
//   node client.cjs <databaseURL>             any other, such as https://<ns>.<rtdb.domain>
//
// It prints one JSON line per event; "step":"ready" tells the script to stop the server. It
// exits 0 when the queued write landed, 2 when the client never reconnected, 3 when the
// write was lost.
const { initializeApp } = require('@firebase/app');
const { getDatabase, ref, set, get, onValue, goOnline } = require('@firebase/database');

const [base, ns] = process.argv.slice(2);
const databaseURL = ns ? `${base}?ns=${ns}` : base;
const namespace = ns || new URL(base).hostname.split('.')[0];
const app = initializeApp({ databaseURL, projectId: namespace.replace(/-default-rtdb$/, '') });
const db = getDatabase(app);
const say = (o) => console.log(JSON.stringify({ t: Date.now(), ...o }));

let connected = false;
let waiters = [];
onValue(ref(db, '.info/connected'), (s) => {
  connected = s.val();
  say({ connected });
  waiters.forEach((w) => w(connected));
});
const until = (want, ms) =>
  new Promise((resolve) => {
    if (connected === want) return resolve(true);
    const timer = setTimeout(() => resolve(false), ms);
    waiters.push((c) => {
      if (c === want) {
        clearTimeout(timer);
        resolve(true);
      }
    });
  });

(async () => {
  if (!(await until(true, 10000))) return say({ result: 'never connected' }), process.exit(1);
  await set(ref(db, 'before'), 1);
  say({ step: 'ready' }); // the harness now stops the server
  if (!(await until(false, 20000))) return say({ result: 'never saw the disconnect' }), process.exit(1);
  // Queued while the server is down.
  const queued = set(ref(db, 'queued'), 'landed').then(() => say({ step: 'queued write acked' }));
  say({ step: 'queued' });
  if (!(await until(true, 60000))) return say({ result: 'did not reconnect within 60 s' }), process.exit(2);
  const ack = await Promise.race([queued.then(() => true), new Promise((r) => setTimeout(() => r(false), 15000))]);
  const snap = await get(ref(db, 'queued'));
  say({ result: ack && snap.val() === 'landed' ? 'ok' : 'write lost', value: snap.val() });
  process.exit(ack && snap.val() === 'landed' ? 0 : 3);
})();
