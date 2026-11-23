import { initializeApp } from 'firebase/app';
import {
  browserLocalPersistence,
  connectAuthEmulator,
  indexedDBLocalPersistence,
  initializeAuth,
} from 'firebase/auth';
import { connectFirestoreEmulator, getFirestore } from 'firebase/firestore';
import { connectDatabaseEmulator, getDatabase } from 'firebase/database';
import { connectStorageEmulator, getStorage } from 'firebase/storage';
import firebaseConfig from 'firebase-config.json';
import firebaseJson from '../firebase.json';

// The one config file your backend hands out. On Aruma, @aruma/web's Vite plugin points Auth,
// Firestore and Storage at your server when the app is built (ARUMA_URL); the Realtime Database
// needs nothing, its databaseURL is in the file. On Firebase the same code runs unchanged.
export const app = initializeApp(firebaseConfig);

// No popupRedirectResolver: the app signs in with email and password or as a guest, so nothing
// is loaded from apis.google.com (see docs/guides/auth-web.md in the Aruma repository).
export const auth = initializeAuth(app, {
  persistence: [indexedDBLocalPersistence, browserLocalPersistence],
});

// Firestore with the SDK's defaults: long polling is detected automatically.
export const db = getFirestore(app);
export const rtdb = getDatabase(app);
export const storage = getStorage(app);

// The one switch for the Firebase Emulator Suite. Never set it against Aruma or Firebase.
export const usingEmulators = import.meta.env.VITE_FIREBASE_EMULATORS === 'true';
if (usingEmulators) {
  const { auth: a, firestore: f, database: d, storage: s } = firebaseJson.emulators;
  connectAuthEmulator(auth, `http://${a.host}:${a.port}`, { disableWarnings: true });
  connectFirestoreEmulator(db, f.host, f.port);
  connectDatabaseEmulator(rtdb, d.host, d.port);
  connectStorageEmulator(storage, s.host, s.port);
}
