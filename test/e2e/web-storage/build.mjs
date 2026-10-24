// Builds the suite's app with each bundler's real npm package, from its config file (the one line
// of @aruma/web in each), one at a time, then the builds that must fail:
//
//   node build.mjs [vite|webpack|esbuild|rollup|rspack|fallback …]     (default: all)
//
// - dist/<bundler>/: the app, served under /<bundler>/ by serve.mjs.
// - dist/fallback/: src/fallback.js bundled by esbuild with no plugin: the run-time fallback.
// - For each bundler, src/no-storage.js (no @firebase/storage module) and src/gstatic.js (the SDK
//   from www.gstatic.com) must fail with @aruma/web's error, not with the bundler's own; and so must
//   src/ssr.js with the SDK left external (a Vite SSR build with ssr.external, webpack externals).
//
// It prints one line per build ("ok vite", "fails as it must: webpack no-storage: …") and exits 1
// on anything else. ARUMA_URL is the server the rewrite targets.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('.', import.meta.url));
if (!process.env.ARUMA_URL) throw new Error('ARUMA_URL is not set');
const fresh = async (file) => (await import(`./${file}?${Date.now()}${Math.random()}`)).default;

const html = (script) => `<!doctype html>\n<meta charset="utf-8" />\n<title>web storage e2e</title>\n<p id="status">loading</p>\n<script type="module" src="./${script}"></script>\n`;

const builders = {
  async vite(entry, outDir) {
    const { build } = await import('vite');
    const inline = { configFile: `${here}vite.config.mjs`, root: here, logLevel: 'error' };
    if (entry) Object.assign(inline, { build: { outDir, emptyOutDir: true, rolldownOptions: { input: entry } } });
    await build(inline);
  },
  async webpack(entry, outDir, base) {
    const { default: webpack } = await import('webpack');
    const config = base ?? (await fresh('webpack.config.mjs'));
    if (entry) Object.assign(config, { entry, output: { ...config.output, path: outDir } });
    await new Promise((resolve, reject) =>
      webpack(config, (err, stats) => {
        if (err) return reject(err);
        if (stats.hasErrors()) return reject(new Error(stats.toJson({ all: false, errors: true }).errors.map((e) => e.message).join('\n')));
        resolve();
      }),
    );
    if (!entry) writeFileSync(`${config.output.path}/index.html`, html('main.js'));
  },
  async esbuild(entry, outDir) {
    const esbuild = await import('esbuild');
    const config = await fresh('esbuild.config.mjs');
    if (entry) Object.assign(config, { entryPoints: [entry], outfile: `${outDir}/main.js` });
    await esbuild.build({ ...config, logLevel: 'silent' });
    if (!entry) writeFileSync(`${here}dist/esbuild/index.html`, html('main.js'));
  },
  // Only as a build that must fail: an SSR build that keeps the Storage SDK external (ssr.external
  // wins over the ssr.noExternal the plugin sets), so the server would load Google's host.
  async 'vite-ssr'(entry, outDir) {
    const { build } = await import('vite');
    await build({
      configFile: `${here}vite.config.mjs`, root: here, logLevel: 'silent',
      build: { ssr: entry, outDir, emptyOutDir: true },
      ssr: { external: ['firebase', '@firebase/storage'] },
    });
  },
  async rollup(entry, outDir) {
    const { rollup } = await import('rollup');
    const config = await fresh('rollup.config.mjs');
    if (entry) Object.assign(config, { input: entry, output: { ...config.output, dir: outDir } });
    const bundle = await rollup({ ...config, logLevel: 'silent' });
    try {
      await bundle.write(config.output);
    } finally {
      await bundle.close();
    }
    if (!entry) writeFileSync(`${here}dist/rollup/index.html`, html('main.js'));
  },
  async rspack(entry, outDir) {
    const { rspack } = await import('@rspack/core');
    const config = await fresh('rspack.config.mjs');
    if (entry) Object.assign(config, { entry, output: { ...config.output, path: outDir } });
    await new Promise((resolve, reject) =>
      rspack(config, (err, stats) => {
        if (err) return reject(err);
        if (stats.hasErrors()) return reject(new Error(stats.toJson({ all: false, errors: true }).errors.map((e) => e.message).join('\n')));
        resolve();
      }),
    );
    if (!entry) writeFileSync(`${config.output.path}/index.html`, html('main.js'));
  },
  // Only as a build that must fail: webpack with the Storage SDK in `externals`.
  async 'webpack-externals'(entry, outDir) {
    const config = await fresh('webpack.config.mjs');
    Object.assign(config, { externals: { 'firebase/storage': 'var {}' } });
    await builders.webpack.call(null, entry, outDir, config);
  },
  async fallback() {
    const esbuild = await import('esbuild');
    const config = await fresh('esbuild.config.mjs');
    await esbuild.build({ ...config, entryPoints: ['./src/fallback.js'], outfile: 'dist/fallback/main.js', plugins: [], logLevel: 'silent' });
    // Not rewritten: what reaches Aruma in the browser is the fallback's doing.
    if (!readFileSync(`${here}dist/fallback/main.js`, 'utf8').includes('"firebasestorage.googleapis.com"')) {
      throw new Error('the fallback bundle does not have Google host literal');
    }
    writeFileSync(`${here}dist/fallback/index.html`, html('main.js'));
  },
};

// The builds that must fail, and the start of @aruma/web's message each must fail with.
const failures = [
  ['no-storage', './src/no-storage.js', /no @firebase\/storage module was in this build/],
  ['gstatic', './src/gstatic.js', /loads the Firebase SDK from www\.gstatic\.com/],
  ['ssr-external', './src/ssr.js', /is external to this bundle/, 'vite-ssr'],
  ['externals', './src/ssr.js', /is external to this bundle/, 'webpack-externals'],
];

const names = process.argv.slice(2).length ? process.argv.slice(2) : ['vite', 'webpack', 'esbuild', 'rollup', 'rspack', 'fallback'];
let bad = 0;
for (const name of names) {
  mkdirSync(`${here}dist`, { recursive: true });
  try {
    await builders[name]();
    console.log(`ok ${name}`);
  } catch (e) {
    bad++;
    console.log(`FAILED ${name}: ${e.message}`);
  }
  for (const [what, entry, want, only] of name === 'fallback' ? [] : failures) {
    if (only && !only.startsWith(name)) continue;
    try {
      await builders[only ?? name](entry, `${here}dist/fail/${name}-${what}`);
      bad++;
      console.log(`FAILED ${name} ${what}: the build succeeded`);
    } catch (e) {
      const msg = String(e.message ?? e);
      if (want.test(msg)) {
        console.log(`fails as it must: ${name} ${what}: ${msg.match(/@aruma\/web: [^\n]*/)?.[0].slice(0, 140)}`);
      } else {
        bad++;
        console.log(`FAILED ${name} ${what}: failed for another reason: ${msg.slice(0, 600)}`);
      }
    }
  }
}
process.exit(bad ? 1 : 0);
