// The page under test: the official web SDK, listening to /x and .info/connected. ?fws=1
// calls forceWebSockets(), for the writer page, which must not depend on long polling.
import { initializeApp } from 'firebase/app';
import { getDatabase, ref, onValue, set, enableLogging, forceWebSockets } from 'firebase/database';

const cfg = window.__CONFIG;
const q = new URLSearchParams(location.search);
const t0 = performance.now();
window.__ev = [];
window.__log = [];
const ev = (k, v) => window.__ev.push({ t: Math.round(performance.now() - t0), k, v });
window.__flag = () => {
  try {
    return localStorage.getItem('firebase:previous_websocket_failure');
  } catch (e) {
    return 'blocked';
  }
};
if (q.get('fws')) forceWebSockets();
enableLogging((m) => window.__log.push(`${Math.round(performance.now() - t0)} ${m}`.slice(0, 300)));
const app = initializeApp({ databaseURL: cfg.databaseURL, projectId: cfg.projectId });
const db = getDatabase(app);
window.__connected = false;
onValue(ref(db, '.info/connected'), (s) => {
  window.__connected = s.val();
  ev('connected', s.val());
});
onValue(ref(db, 'x'), (s) => ev('x', s.val()));
window.__write = (v) => set(ref(db, 'x'), v).then(() => 'ok', (e) => String(e));
ev('init', window.__flag());
