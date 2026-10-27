import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

// The app a developer ships: Aruma's one line in the bundler's config, Auth and Firestore on Aruma,
// the app's code unchanged. The page is served from another origin than Aruma (playwright.config.mjs).
export default defineConfig({
  clearScreen: false,
  preview: { strictPort: true },
  plugins: [aruma({ url: process.env.ARUMA_URL, firestore: true })],
});
