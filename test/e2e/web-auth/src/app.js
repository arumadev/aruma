// The app a developer writes: the web config exactly as Aruma's Management API serves it, and
// the one line ADR-013 allows. Nothing else differs from a Firebase project.
import firebaseConfig from '../.generated/firebase-config.json';

export function start(appSdk, authSdk) {
  const app = appSdk.initializeApp(firebaseConfig);
  const auth = authSdk.getAuth(app);
  authSdk.connectAuthEmulator(auth, import.meta.env.VITE_ARUMA_URL, { disableWarnings: true });

  // What the tests drive and observe.
  const authStates = [];
  authSdk.onAuthStateChanged(auth, (user) => authStates.push(user ? user.uid : null));
  window.e2e = { auth, sdk: authSdk, version: appSdk.SDK_VERSION, authStates };
  document.getElementById('status').textContent = `ready ${appSdk.SDK_VERSION}`;
}
