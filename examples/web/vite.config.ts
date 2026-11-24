import { existsSync } from 'node:fs';
import path from 'node:path';
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';
import aruma from '@aruma/web/vite';

// Every backend setting comes from two places:
//
//   firebase-config.json   the web config your backend hands out (Aruma's dashboard or
//                          `arumactl apps sdkconfig`, or the Firebase console). FIREBASE_CONFIG
//                          names another file.
//   ARUMA_URL              the build setting of @aruma/web: your Aruma server's public URL. Leave it
//                          empty for Firebase or the emulators, and the plugin does nothing.
//
// VITE_FIREBASE_EMULATORS=true connects the app to the Firebase Emulator Suite instead (the ports of
// firebase.json). Both can live in .env.local; the environment wins over the file.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const arumaURL = (env.ARUMA_URL ?? '').trim();
  const emulators = env.VITE_FIREBASE_EMULATORS === 'true';
  const configFile = path.resolve(env.FIREBASE_CONFIG || 'firebase-config.json');

  if (arumaURL && emulators) {
    throw new Error('Set either ARUMA_URL (Aruma) or VITE_FIREBASE_EMULATORS=true (the emulators), not both.');
  }
  if (!existsSync(configFile)) {
    throw new Error(
      `${configFile} is missing. Save your web app's config there (see firebase-config.example.json and README.md).`,
    );
  }

  return {
    plugins: [react(), aruma({ url: arumaURL, storage: true, firestore: true })],
    resolve: { alias: { 'firebase-config.json': configFile } },
    server: { host: '127.0.0.1', port: Number(env.PORT || 5173), strictPort: true },
    preview: { host: '127.0.0.1', port: Number(env.PORT || 4173), strictPort: true },
    // The Firebase SDK alone is about 900 kB minified (300 kB gzipped).
    build: { target: 'es2022', sourcemap: true, chunkSizeWarningLimit: 1200 },
  };
});
