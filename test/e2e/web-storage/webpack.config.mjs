import { fileURLToPath } from 'node:url';
import { arumaStorage } from '@aruma/web/unplugin';

// The one line for webpack: arumaStorage.webpack({ url }).
export default {
  mode: 'production',
  context: fileURLToPath(new URL('.', import.meta.url)),
  entry: './src/main.js',
  output: { path: fileURLToPath(new URL('dist/webpack', import.meta.url)), filename: 'main.js', clean: true },
  devtool: false,
  performance: { hints: false },
  plugins: [arumaStorage.webpack({ url: process.env.ARUMA_URL })],
};
