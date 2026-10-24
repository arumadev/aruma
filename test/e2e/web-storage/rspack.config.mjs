import { fileURLToPath } from 'node:url';
import { arumaStorage } from '@aruma/web/unplugin';

// The one line for Rspack: arumaStorage.rspack({ url }).
export default {
  mode: 'production',
  context: fileURLToPath(new URL('.', import.meta.url)),
  entry: './src/main.js',
  output: { path: fileURLToPath(new URL('dist/rspack', import.meta.url)), filename: 'main.js', clean: true },
  devtool: false,
  performance: { hints: false },
  plugins: [arumaStorage.rspack({ url: process.env.ARUMA_URL })],
};
