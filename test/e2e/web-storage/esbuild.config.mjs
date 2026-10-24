import { fileURLToPath } from 'node:url';
import { arumaStorage } from '@aruma/web/unplugin';

// The one line for esbuild: arumaStorage.esbuild({ url }).
export default {
  absWorkingDir: fileURLToPath(new URL('.', import.meta.url)),
  entryPoints: ['./src/main.js'],
  bundle: true,
  format: 'esm',
  platform: 'browser',
  minify: true,
  outfile: 'dist/esbuild/main.js',
  logLevel: 'warning',
  plugins: [arumaStorage.esbuild({ url: process.env.ARUMA_URL })],
};
