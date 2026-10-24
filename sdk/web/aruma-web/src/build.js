// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The build-time Storage rewrite, bundler by bundler (ADR-022 §16.1): the hooks unplugin.js turns
// into a webpack, Rspack, esbuild, Rollup and Rolldown plugin, and that vite.js uses directly (so
// the Vite plugin, which Auth-only apps load too, does not need unplugin).
//
// It rewrites DEFAULT_HOST in @firebase/storage to Aruma's host (storage.js), and fails the build,
// never silently ships Google's host:
//
// 1. A @firebase/storage module whose literals are not found exactly once (another release).
// 2. A @firebase/storage module that is in the final module set but still names Google, or that
//    the bundle leaves external (SSR externals, `externals`): checked on the module set as the
//    bundler ends it, so a module restored from a persistent cache is checked too.
// 3. A browser build with no @firebase/storage module at all (`required`, default true): the app
//    is not using the SDK this plugin was configured for, or the bundler took another path to it.
//    Watch mode and server builds (an SSR or Node build may well not use Storage) skip this one.
// 4. Any reference to the Firebase SDK on www.gstatic.com, in an import or in the output: that
//    copy never passes through the bundler.
import { readFile } from 'node:fs/promises';
import {
  GSTATIC_FIREBASE,
  isStorageModule,
  literalProblem,
  rewriteStorageLiterals,
  storageBase,
  storageModuleRewritten,
} from './storage.js';

const NAME = 'aruma-web:storage';

// A bare request for the Storage SDK, as a bundler names an external module.
const STORAGE_REQUEST = /^(?:@firebase\/storage(?:-compat)?|firebase\/(?:compat\/)?storage)(?:$|\/)/;

const isStorageExternal = (id) => STORAGE_REQUEST.test(String(id)) || isStorageModule(id);

const notRewritten = (id) =>
  `@aruma/web: ${id} still names firebasestorage.googleapis.com in the bundle. Another plugin or a cached build replaced the rewritten module; clear the bundler's cache`;
const externalized = (id) =>
  `@aruma/web: ${id} is external to this bundle, so the Storage SDK loaded at run time keeps Google's host. Bundle it (Vite: ssr.noExternal, which the Vite plugin sets; webpack: remove it from externals)`;
const noneSeen =
  "@aruma/web: no @firebase/storage module was in this build, so nothing was rewritten. Import Storage from the 'firebase/storage' npm package, or pass required: false if this bundle does not use Storage";
const gstatic = (where) =>
  `@aruma/web: ${where} loads the Firebase SDK from www.gstatic.com, which never passes through the build and keeps Google's Storage host. Import it from the 'firebase' npm package instead`;

/**
 * The options every adapter takes.
 * @typedef {{ url?: string | URL, allowInsecure?: boolean, required?: boolean, log?: boolean }} StorageOptions
 */

/**
 * The shared state and checks of one plugin instance. Exported for the Vite plugin and the tests.
 * @param {StorageOptions} options
 */
export function storageRewriter(options) {
  const { url, allowInsecure = false, required = true, log = false } = options;
  const base = storageBase(url, { allowInsecure });
  const rewritten = new Set();
  return {
    base,
    required,
    rewritten,
    /** The transform of one module, or null. Throws on a module it cannot rewrite. */
    transform(code, id) {
      if (!isStorageModule(id)) return null;
      const r = rewriteStorageLiterals(code, base);
      if (!r.ok) throw new Error(literalProblem(id, r, base));
      rewritten.add(id);
      if (log) console.info(`[aruma] storage: ${id} -> ${base.host}`);
      return { code: r.code, map: null };
    },
    /**
     * The checks on a finished module set: [{ id, code, external }]. `browser` is false for a
     * server build, `watch` true in watch mode. Returns the problems found (empty: fine).
     */
    verify(modules, { browser = true, watch = false } = {}) {
      const problems = [];
      let storage = 0;
      for (const m of modules) {
        if (m.external) {
          if (isStorageExternal(m.id)) problems.push(externalized(m.id));
          continue;
        }
        if (!isStorageModule(m.id)) continue;
        storage++;
        if (!storageModuleRewritten(m.code ?? '', base)) problems.push(notRewritten(m.id));
      }
      if (storage === 0 && required && browser && !watch) problems.push(noneSeen);
      return problems;
    },
    /** The output check: problems for the files ([{ name, code }]) that load the SDK from gstatic. */
    scanOutput(files) {
      return files.filter((f) => GSTATIC_FIREBASE.test(f.code)).map((f) => gstatic(f.name));
    },
    resolveGstatic(id) {
      if (GSTATIC_FIREBASE.test(id)) throw new Error(gstatic(`the import ${id}`));
      return null;
    },
  };
}

// Rollup, Rolldown and Vite: the module set and the chunks as generateBundle sees them.
function rollupVerify(rw, ctx, bundle, { browser }) {
  const modules = [...ctx.getModuleIds()].map((id) => {
    const info = ctx.getModuleInfo(id);
    return { id, code: info?.code, external: Boolean(info?.isExternal) };
  });
  const files = Object.values(bundle).map((f) => ({ name: f.fileName, code: f.type === 'chunk' ? f.code : String(f.source ?? '') }));
  const problems = [...rw.verify(modules, { browser, watch: Boolean(ctx.meta?.watchMode) }), ...rw.scanOutput(files)];
  if (problems.length) ctx.error(problems.join('\n'));
}

// webpack and Rspack: the module set at finishModules (also the modules a persistent cache
// restored, which the transform never saw), and the emitted assets.
function tapWebpack(rw, compiler) {
  // Asked per compilation: webpack sets the compiler's name and target defaults after the plugins
  // are applied. Next.js names its compilers client, server and edge-server.
  const server = () =>
    ['server', 'edge-server'].includes(compiler.name) ||
    [compiler.options.target].flat().some((t) => typeof t === 'string' && /^(?:node|async-node|electron-main)/.test(t));
  const fail = (compilation, problems) => {
    const E = compiler.webpack?.WebpackError ?? Error;
    for (const p of problems) compilation.errors.push(new E(compiler.name ? `${p} (webpack compiler ${compiler.name})` : p));
  };
  compiler.hooks.thisCompilation.tap(NAME, (compilation) => {
    compilation.hooks.finishModules.tap(NAME, (modules) => {
      const list = [];
      for (const m of modules) {
        if (m.constructor?.name === 'ExternalModule' || m.externalType !== undefined) {
          list.push({ id: String(m.userRequest ?? m.request ?? ''), external: true });
          continue;
        }
        const id = m.resource ?? '';
        if (!isStorageModule(id)) continue;
        let code = '';
        try {
          code = String(m.originalSource()?.source() ?? '');
        } catch {
          // No source: counted as not rewritten.
        }
        list.push({ id, code });
      }
      fail(compilation, rw.verify(list, { browser: !server(), watch: Boolean(compiler.watchMode) }));
    });
    const stage = compiler.webpack?.Compilation?.PROCESS_ASSETS_STAGE_REPORT ?? 5000;
    compilation.hooks.processAssets.tap({ name: NAME, stage }, (assets) => {
      const files = Object.entries(assets)
        .filter(([name]) => /\.(?:m?js|cjs|html)$/.test(name))
        .map(([name, src]) => ({ name, code: String(src.source()) }));
      fail(compilation, rw.scanOutput(files));
    });
  });
}

/** The unplugin factory. Exported for the Vite plugin. */
export function storageFactory(options = {}, meta = {}) {
  const { url } = options;
  // Not configured: the plugin does nothing (as the Auth plugins).
  if (url === undefined || url === null || String(url).trim() === '') return { name: NAME };
  const rw = storageRewriter(options);
  // esbuild has no module set to inspect after the build: the transforms of this build count.
  const esbuild = { browser: true, built: 0 };
  return {
    name: NAME,
    enforce: 'pre',
    buildStart() {
      esbuild.built = 0;
    },
    resolveId: { filter: { id: GSTATIC_FIREBASE }, handler: (id) => rw.resolveGstatic(id) },
    transform: {
      filter: { id: /[\\/]@firebase[\\/]storage[\\/]dist[\\/]/ },
      handler(code, id) {
        const r = rw.transform(code, id);
        if (r) esbuild.built++;
        return r;
      },
    },
    buildEnd() {
      if (meta.framework !== 'esbuild') return;
      if (esbuild.built === 0 && rw.required && esbuild.browser) throw new Error(noneSeen);
    },
    esbuild: {
      onLoadFilter: /[\\/]@firebase[\\/]storage[\\/]dist[\\/]/,
      config(initial) {
        esbuild.browser = initial.platform !== 'node';
      },
    },
    rollup: {
      generateBundle(_, bundle) {
        rollupVerify(rw, this, bundle, { browser: true });
      },
    },
    rolldown: {
      generateBundle(_, bundle) {
        rollupVerify(rw, this, bundle, { browser: true });
      },
    },
    webpack: (compiler) => tapWebpack(rw, compiler),
    rspack: (compiler) => tapWebpack(rw, compiler),
  };
}

/**
 * The esbuild plugin of Vite 5 to 7's dependency optimizer (optimizeDeps.esbuildOptions.plugins),
 * which does not run Rollup plugins. Written by hand so that the Vite plugin does not need unplugin.
 */
export function esbuildOptimizerPlugin(rw, name) {
  return {
    name,
    setup(build) {
      build.onResolve({ filter: GSTATIC_FIREBASE }, (args) => {
        rw.resolveGstatic(args.path);
        return null;
      });
      build.onLoad({ filter: /[\\/]@firebase[\\/]storage[\\/]dist[\\/]/ }, async (args) => {
        const r = rw.transform(await readFile(args.path, 'utf8'), args.path);
        return r ? { contents: r.code, loader: 'js' } : null;
      });
    },
  };
}

export { rollupVerify };
