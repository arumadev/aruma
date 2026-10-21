import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

// Production mode only: the Auth SDK keeps Google's hosts and @aruma/web's plugin redirects its
// fetch calls to Aruma; the database SDK connects to the databaseURL of the generated config. The
// app's code calls no emulator API.
export default defineConfig({
  clearScreen: false,
  server: { host: '127.0.0.1', strictPort: true },
  plugins: [aruma({ url: process.env.ARUMA_URL })],
});
