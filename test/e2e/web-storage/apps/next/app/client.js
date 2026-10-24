'use client';

// In the browser (the client bundle): the eleven operations, as in every other app of the suite.
import { useEffect } from 'react';
import { install } from '@aruma/web';
import { initializeApp } from 'firebase/app';
import { getAuth, signInAnonymously } from 'firebase/auth';
import * as st from 'firebase/storage';
import { run } from './ops.js';

export default function Client({ name }) {
  useEffect(() => {
    if (window.__done) return;
    window.__done = (async () => {
      const e2e = await (await fetch('/e2e.json')).json();
      install({ url: e2e.aruma });
      const app = initializeApp(e2e.config);
      const { user } = await signInAnonymously(getAuth(app));
      const results = await run({ storage: st.getStorage(app), st, aruma: e2e.aruma, bucket: e2e.bucket, dir: `e2e/${user.uid}/${name}` });
      return { bundler: name, results };
    })().catch((e) => ({ bundler: name, error: String(e?.stack ?? e) }));
  }, [name]);
  return <p id="status">client</p>;
}
