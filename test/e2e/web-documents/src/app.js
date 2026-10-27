// The app a developer writes: the web config exactly as Aruma's Management API serves it, the full
// and the lite Firestore SDK on the same app, and nothing that names Aruma (vite.config.mjs does).
// ?transport=stream (default) or long-polling picks the full SDK's WebChannel transport.
import { initializeApp } from 'firebase/app';
import * as authSdk from 'firebase/auth';
import * as fs from 'firebase/firestore';
import * as lite from 'firebase/firestore/lite';
import firebaseConfig from '../.generated/firebase-config.json';

const transport = new URLSearchParams(location.search).get('transport') ?? 'stream';
const app = initializeApp(firebaseConfig);
// No popup/redirect resolver: getAuth() adds one, and in Safari it proactively loads Google's
// apis.google.com/js/api.js (the iframe for OAuth redirects), which this app does not use.
const auth = authSdk.initializeAuth(app, { persistence: authSdk.indexedDBLocalPersistence });
const db = transport === 'long-polling'
  ? fs.initializeFirestore(app, { experimentalForceLongPolling: true })
  : fs.getFirestore(app);
const liteDb = lite.getFirestore(app);

// What the tests drive: the SDKs as namespaces, and a place for snapshots to land.
window.e2e = { auth, authSdk, fs, lite, db, liteDb, transport, events: {} };
document.getElementById('status').textContent = 'ready';
