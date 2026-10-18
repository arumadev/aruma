// The app a developer writes: the web config exactly as Aruma's Management API serves it, and
// in emulator mode the one line ADR-013 allows. In production mode not even that: vite.config
// carries @aruma/web's plugin, and nothing else differs from a Firebase project.
import firebaseConfig from '../.generated/firebase-config.json';

export function start(appSdk, authSdk) {
  const app = appSdk.initializeApp(firebaseConfig);
  const auth = authSdk.getAuth(app);
  if (import.meta.env.VITE_ARUMA_E2E_MODE === 'emulator') {
    authSdk.connectAuthEmulator(auth, import.meta.env.VITE_ARUMA_URL, { disableWarnings: true });
  }

  // What the tests drive and observe.
  const authStates = [];
  authSdk.onAuthStateChanged(auth, (user) => authStates.push(user ? user.uid : null));
  window.e2e = { auth, sdk: authSdk, version: appSdk.SDK_VERSION, authStates };
  document.getElementById('status').textContent = `ready ${appSdk.SDK_VERSION}`;
}
