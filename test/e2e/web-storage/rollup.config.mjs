import { nodeResolve } from '@rollup/plugin-node-resolve';
import { arumaStorage } from '@aruma/web/unplugin';

// The one line for Rollup: arumaStorage.rollup({ url }).
export default {
  input: 'src/main.js',
  output: { dir: 'dist/rollup', format: 'es', entryFileNames: 'main.js', inlineDynamicImports: true },
  plugins: [arumaStorage.rollup({ url: process.env.ARUMA_URL }), nodeResolve({ browser: true })],
  onwarn(w, warn) {
    if (w.code !== 'THIS_IS_UNDEFINED' && w.code !== 'CIRCULAR_DEPENDENCY') warn(w);
  },
};
