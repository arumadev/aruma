// SR3-06: an app that set up only Auth with Aruma and uses Storage without the rewrite, bundled with
// no plugin, so @firebase/storage keeps Google's host and would send the Aruma ID token there with
// every request. install()'s Storage guard must fail those requests in the browser instead, at once
// and with the SDK's default retry budget (2 minutes, 10 for uploads) left as it is.
import { install } from '@aruma/web';
import { initializeApp } from 'firebase/app';
import { getAuth, signInAnonymously } from 'firebase/auth';
import { getMetadata, getStorage, ref, uploadBytes } from 'firebase/storage';

const outcome = async (fn) => {
  try {
    await fn();
    return 'ok';
  } catch (e) {
    return `${e.code ?? e}${e.serverResponse ? ` (${e.serverResponse})` : ''}`;
  }
};

window.__done = (async () => {
  const e2e = await (await fetch('/e2e.json')).json();
  install({ url: e2e.aruma });
  const app = initializeApp(e2e.config);
  const { user } = await signInAnonymously(getAuth(app));
  const storage = getStorage(app);
  const object = ref(storage, `e2e/${user.uid}/auth-only.txt`);
  const started = performance.now();
  const upload = await outcome(() => uploadBytes(object, new TextEncoder().encode('never sent\n')));
  const metadata = await outcome(() => getMetadata(object));
  return { bundler: 'auth-only', uid: user.uid, host: storage.host, upload, metadata, ms: Math.round(performance.now() - started) };
})().catch((e) => ({ bundler: 'auth-only', error: String(e?.stack ?? e) }));
