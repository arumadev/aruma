import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

// The one line: Auth redirected at run time, Storage rewritten at build time.
export default defineConfig({
  clearScreen: false,
  logLevel: 'warn',
  base: '/vite/',
  plugins: [aruma({ url: process.env.ARUMA_URL, storage: true })],
  build: {
    outDir: 'dist/vite',
    emptyOutDir: true,
  },
});
