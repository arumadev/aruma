// SR3-06: an app that set up only Auth with Aruma and uses Storage without the rewrite, bundled with
// no plugin, so @firebase/storage keeps Google's host and would send the Aruma ID token there with
// every request. install()'s Storage guard must fail those requests in the browser instead.
import { install } from '@aruma/web';
import { initializeApp } from 'firebase/app';
import { getAuth, signInAnonymously } from 'firebase/auth';
import { getMetadata, getStorage, ref, uploadBytes } from 'firebase/storage';

const outcome = async (fn) => {
  try {
    await fn();
    return 'ok';
  } catch (e) {
    return e.code ?? String(e);
  }
};

window.__done = (async () => {
  const e2e = await (await fetch('/e2e.json')).json();
  install({ url: e2e.aruma });
  const app = initializeApp(e2e.config);
  const { user } = await signInAnonymously(getAuth(app));
  const storage = getStorage(app);
  // A refused request is retried until these run out (defaults: 2 and 10 minutes).
  storage.maxOperationRetryTime = 2000;
  storage.maxUploadRetryTime = 2000;
  const object = ref(storage, `e2e/${user.uid}/auth-only.txt`);
  return {
    bundler: 'auth-only',
    uid: user.uid,
    host: storage.host,
    upload: await outcome(() => uploadBytes(object, new TextEncoder().encode('never sent\n'))),
    metadata: await outcome(() => getMetadata(object)),
  };
})().catch((e) => ({ bundler: 'auth-only', error: String(e?.stack ?? e) }));
