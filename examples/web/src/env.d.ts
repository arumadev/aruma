/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "true" connects the app to the Firebase Emulator Suite (the ports of firebase.json). */
  readonly VITE_FIREBASE_EMULATORS?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

declare module 'firebase-config.json' {
  import type { FirebaseOptions } from 'firebase/app';
  const config: FirebaseOptions;
  export default config;
}
