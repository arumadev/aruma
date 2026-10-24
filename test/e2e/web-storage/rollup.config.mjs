import { nodeResolve } from '@rollup/plugin-node-resolve';
import { arumaStorage } from '@aruma/web/unplugin';

// The one line for Rollup: arumaStorage.rollup({ url }).
export default {
  // Rollup does not follow new Worker(new URL(…)) either: the worker is an input of its own.
  input: { main: 'src/main.js', worker: 'src/worker.js' },
  output: { dir: 'dist/rollup', format: 'es', entryFileNames: '[name].js' },
  plugins: [arumaStorage.rollup({ url: process.env.ARUMA_URL }), nodeResolve({ browser: true })],
  onwarn(w, warn) {
    if (w.code !== 'THIS_IS_UNDEFINED' && w.code !== 'CIRCULAR_DEPENDENCY') warn(w);
  },
};
