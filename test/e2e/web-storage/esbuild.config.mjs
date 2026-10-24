import { fileURLToPath } from 'node:url';
import { arumaStorage } from '@aruma/web/unplugin';

// The one line for esbuild: arumaStorage.esbuild({ url }).
export default {
  absWorkingDir: fileURLToPath(new URL('.', import.meta.url)),
  // esbuild does not follow new Worker(new URL(…)): the worker is an entry point of its own.
  entryPoints: { main: './src/main.js', worker: './src/worker.js' },
  bundle: true,
  format: 'esm',
  platform: 'browser',
  minify: true,
  outdir: 'dist/esbuild',
  logLevel: 'warning',
  plugins: [arumaStorage.esbuild({ url: process.env.ARUMA_URL })],
};
