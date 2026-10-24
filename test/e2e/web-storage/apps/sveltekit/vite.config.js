import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

// The one line, as in any Vite app. Without it, Vite would leave @firebase/storage external to the
// SSR bundle and the server would load Node's unrewritten build (ADR-022 §16.1).
export default defineConfig({
  logLevel: 'warn',
  plugins: [aruma({ url: process.env.ARUMA_URL, storage: true }), sveltekit({ adapter: adapter() })],
});
