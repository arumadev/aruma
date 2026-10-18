import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

// ARUMA_E2E_MODE (run.sh):
//   emulator (default)  the app calls connectAuthEmulator (ADR-013's one line).
//   production          no connectAuthEmulator: the SDK keeps Google's hosts and @aruma/web's plugin
//                       redirects its fetch calls to Aruma. The app's code is unchanged.
//   production-build    the same, with the host literals in @firebase/auth rewritten at build time.
const mode = process.env.ARUMA_E2E_MODE ?? 'emulator';
const plugins = {
  emulator: () => [],
  production: () => [aruma({ url: process.env.ARUMA_URL })],
  'production-build': () => [aruma({ url: process.env.ARUMA_URL, mode: 'build' })],
}[mode]?.();
if (!plugins) throw new Error(`unknown ARUMA_E2E_MODE ${mode}`);

export default defineConfig({
  clearScreen: false,
  server: { host: '127.0.0.1', strictPort: true },
  preview: { host: '127.0.0.1', strictPort: true },
  define: { 'import.meta.env.VITE_ARUMA_E2E_MODE': JSON.stringify(mode) },
  plugins,
});
