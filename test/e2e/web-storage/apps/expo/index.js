// An Expo app on the firebase JS SDK (production mode), bundled by Metro. On the web it runs the
// eleven operations, as every app of the suite; on Android only the bundle is checked (measure.sh).
import { registerRootComponent } from 'expo';
import { useEffect } from 'react';
import { Platform, Text } from 'react-native';
import { install } from '@aruma/web';
import { initializeApp } from 'firebase/app';
import { getAuth, signInAnonymously } from 'firebase/auth';
import * as st from 'firebase/storage';
import { run } from './ops';

function App() {
  useEffect(() => {
    if (Platform.OS !== 'web' || window.__done) return;
    window.__done = (async () => {
      const e2e = await (await fetch('/e2e.json')).json();
      install({ url: e2e.aruma });
      const app = initializeApp(e2e.config);
      const { user } = await signInAnonymously(getAuth(app));
      const results = await run({ storage: st.getStorage(app), st, aruma: e2e.aruma, bucket: e2e.bucket, dir: `e2e/${user.uid}/expo` });
      return { bundler: 'expo', results };
    })().catch((e) => ({ bundler: 'expo', error: String(e?.stack ?? e) }));
  }, []);
  return <Text>expo</Text>;
}

registerRootComponent(App);
