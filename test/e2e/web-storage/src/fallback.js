// The run-time fallback, for a build the Storage rewrite cannot reach (ADR-022 §16.1): bundled with
// no plugin at all, so @firebase/storage keeps Google's host, and pointed at Aruma by two lines of
// @aruma/web/storage before the first Storage operation.
import { install } from '@aruma/web';
import { installStorage, useArumaStorage } from '@aruma/web/storage';
import { initializeApp } from 'firebase/app';
import { getAuth, signInAnonymously } from 'firebase/auth';
import * as st from 'firebase/storage';
import { run } from './ops.js';

window.__done = (async () => {
  const e2e = await (await fetch('/e2e.json')).json();
  install({ url: e2e.aruma });
  installStorage({ url: e2e.aruma });
  const app = initializeApp(e2e.config);
  const { user } = await signInAnonymously(getAuth(app));
  const storage = useArumaStorage(st.getStorage(app));
  const results = await run({ storage, st, aruma: e2e.aruma, bucket: e2e.bucket, dir: `e2e/${user.uid}/fallback` });
  return { bundler: 'fallback', uid: user.uid, results };
})().catch((e) => ({ bundler: 'fallback', error: String(e?.stack ?? e) }));
