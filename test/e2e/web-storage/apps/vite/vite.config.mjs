import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

// measure.sh's Vite app, for any Vite major: the same one line as the suite's own vite.config.mjs.
// The dev server asks the page server for /e2e.json.
const name = process.env.ARUMA_E2E_NAME;
export default defineConfig({
  clearScreen: false,
  logLevel: 'warn',
  base: `/${name}/`,
  plugins: [aruma({ url: process.env.ARUMA_URL, storage: true })],
  server: {
    host: '127.0.0.1',
    strictPort: true,
    proxy: { '/e2e.json': `http://127.0.0.1:${process.env.ARUMA_E2E_PAGE_PORT ?? 9421}` },
  },
  build: { outDir: 'dist', emptyOutDir: true },
});
