// The app every browser bundle of the suite builds (Vite, webpack, esbuild): the official firebase
// package in production mode, the web config the console serves (e2e.json), Auth redirected at run
// time by @aruma/web's install(), and Storage rewritten at build time by the bundler's plugin. Its
// code calls no emulator API and nothing of @aruma/web/storage. It ends with src/worker.js.
import { install } from '@aruma/web';
import { initializeApp } from 'firebase/app';
import { getAuth, signInAnonymously } from 'firebase/auth';
import * as st from 'firebase/storage';
import { run } from './ops.js';

const bundler = location.pathname.split('/')[1] || 'unknown';

window.__done = (async () => {
  const e2e = await (await fetch('/e2e.json')).json();
  install({ url: e2e.aruma });
  const app = initializeApp(e2e.config);
  const { user } = await signInAnonymously(getAuth(app));
  const storage = st.getStorage(app);
  const results = await run({ storage, st, aruma: e2e.aruma, bucket: e2e.bucket, dir: `e2e/${user.uid}/${bundler}` });
  // The same SDK inside a Web Worker, which the bundler builds as a bundle of its own.
  const worker = await new Promise((resolve) => {
    const w = new Worker(new URL('./worker.js', import.meta.url), { type: 'module' });
    w.onmessage = (m) => {
      resolve(m.data);
      w.terminate();
    };
    w.onerror = (e) => resolve({ ok: false, error: e.message || 'the worker failed to load' });
    w.postMessage({ config: e2e.config, bundler });
  });
  return { bundler, uid: user.uid, results, worker };
})().catch((e) => ({ bundler, error: String(e?.stack ?? e) }));
