// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The build-time Storage and Firestore rewrite, bundler by bundler (ADR-022 §16.1, SR3-10): the hooks unplugin.js turns
// into a webpack, Rspack, esbuild, Rollup and Rolldown plugin, and that vite.js uses directly (so
// the Vite plugin, which Auth-only apps load too, does not need unplugin).
//
// It rewrites DEFAULT_HOST in @firebase/storage (storage.js) or @firebase/firestore (firestore.js) to
// Aruma's host, and fails the build, never silently ships Google's host. Read "storage" below as
// either product; a Firestore dist file with no host literal is fine (the shared chunk has it):
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
  firestoreBase,
  firestoreLiteralProblem,
  firestoreModuleCarries,
  firestoreModuleRewritten,
  isFirestoreModule,
  namesGoogleFirestore,
  rewriteFirestoreLiterals,
} from './firestore.js';
import {
  GSTATIC_FIREBASE,
  isStorageModule,
  literalProblem,
  rewriteStorageLiterals,
  storageBase,
  storageModuleRewritten,
} from './storage.js';

// A bare request for an SDK, as a bundler names an external module.
const STORAGE_REQUEST = /^(?:@firebase\/storage(?:-compat)?|firebase\/(?:compat\/)?storage)(?:$|\/)/;
const FIRESTORE_REQUEST = /^(?:@firebase\/firestore(?:-compat)?|firebase\/(?:compat\/)?firestore)(?:$|\/)/;

const gstatic = (where) =>
  `@aruma/web: ${where} loads the Firebase SDK from a CDN (www.gstatic.com, or the Storage or Firestore SDK from jsdelivr, unpkg and the like), which never passes through the build and keeps Google's Storage and Firestore hosts. Import it from the 'firebase' npm package instead`;

// What differs between the products the build rewrites: Storage's DEFAULT_HOST and Firestore's. A
// Firestore dist file that holds no host literal is fine (most import the shared chunk): the build
// needs one module that carries the rewritten definition.
const STORAGE = {
  name: 'aruma-web:storage',
  label: '@firebase/storage',
  host: 'firebasestorage.googleapis.com',
  base: storageBase,
  isModule: isStorageModule,
  filter: /[\\/]@firebase[\\/]storage[\\/]dist[\\/]/,
  request: STORAGE_REQUEST,
  rewrite: rewriteStorageLiterals,
  problem: literalProblem,
  rewritten: storageModuleRewritten,
  carries: () => true,
  // The Storage SDK is told by its version header, so an app's own mention of the host is not flagged.
  leaks: (code) => code.includes('X-Firebase-Storage-Version') && /["'`]firebasestorage\.googleapis\.com["'`]/.test(code),
  notRewritten: (id) =>
    `@aruma/web: ${id} still names firebasestorage.googleapis.com in the bundle. Another plugin or a cached build replaced the rewritten module; clear the bundler's cache`,
  externalized: (id) =>
    `@aruma/web: ${id} is external to this bundle, so the Storage SDK loaded at run time keeps Google's host. Bundle it (Vite: ssr.noExternal, which the Vite plugin sets; webpack: remove it from externals)`,
  noneSeen:
    "@aruma/web: no @firebase/storage module was in this build, so nothing was rewritten. Import Storage from the 'firebase/storage' npm package, or pass required: false if this bundle does not use Storage",
  unrewrittenCopy: (file) =>
    `@aruma/web: ${file} holds a copy of the Storage SDK that still names firebasestorage.googleapis.com: a bundle the plugin did not reach (a Web Worker, a child compiler). Add the plugin there too (Vite: worker.plugins, which the Vite plugin sets)`,
};

const FIRESTORE = {
  name: 'aruma-web:firestore',
  label: '@firebase/firestore',
  host: 'firestore.googleapis.com',
  base: firestoreBase,
  isModule: isFirestoreModule,
  filter: /[\\/]@firebase[\\/]firestore[\\/]dist[\\/]/,
  request: FIRESTORE_REQUEST,
  rewrite: rewriteFirestoreLiterals,
  problem: firestoreLiteralProblem,
  rewritten: firestoreModuleRewritten,
  carries: firestoreModuleCarries,
  leaks: namesGoogleFirestore,
  notRewritten: (id) =>
    `@aruma/web: ${id} still names firestore.googleapis.com in the bundle. Another plugin or a cached build replaced the rewritten module; clear the bundler's cache`,
  externalized: (id) =>
    `@aruma/web: ${id} is external to this bundle, so the Firestore SDK loaded at run time keeps Google's host and sends the Aruma ID token there. Bundle it (Vite: ssr.noExternal, which the Vite plugin sets; webpack: remove it from externals)`,
  noneSeen:
    "@aruma/web: no @firebase/firestore module carrying the Firestore host was in this build, so nothing was rewritten. Import Firestore from the 'firebase/firestore' npm package, or pass required: false if this bundle does not use Firestore",
  unrewrittenCopy: (file) =>
    `@aruma/web: ${file} holds a copy of the Firestore SDK that still names firestore.googleapis.com and would send the Aruma ID token there: a bundle the plugin did not reach (a Web Worker, a child compiler). Add the plugin there too (Vite: worker.plugins, which the Vite plugin sets)`,
};

export { FIRESTORE, STORAGE };

/**
 * The options every adapter takes.
 * @typedef {{ url?: string | URL, allowInsecure?: boolean, required?: boolean, log?: boolean }} StorageOptions
 */

/**
 * The shared state and checks of one plugin instance. Exported for the Vite plugin and the tests.
 * @param {StorageOptions} options
 */
export function storageRewriter(options) {
  return productRewriter(STORAGE, options);
}

/** The same for Cloud Firestore (SR3-10). */
export function firestoreRewriter(options) {
  return productRewriter(FIRESTORE, options);
}

function productRewriter(P, options) {
  const { url, allowInsecure = false, required = true, log = false } = options;
  const base = P.base(url, { allowInsecure });
  const rewritten = new Set();
  return {
    base,
    required,
    rewritten,
    product: P,
    isModule: P.isModule,
    /** The transform of one module, or null. Throws on a module it cannot rewrite. */
    transform(code, id) {
      if (!P.isModule(id)) return null;
      const r = P.rewrite(code, base);
      if (!r.ok) throw new Error(P.problem(id, r, base));
      if (r.hosts === 0 && P !== STORAGE) return null;
      rewritten.add(id);
      if (log) console.info(`[aruma] ${P.name.slice(10)}: ${id} -> ${base.host}`);
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
          if (P.request.test(String(m.id)) || P.isModule(m.id)) problems.push(P.externalized(m.id));
          continue;
        }
        if (!P.isModule(m.id)) continue;
        const code = m.code ?? '';
        if (!P.rewritten(code, base)) problems.push(P.notRewritten(m.id));
        else if (P.carries(code, base)) storage++;
      }
      if (storage === 0 && required && browser && !watch) problems.push(P.noneSeen);
      return problems;
    },
    /**
     * The output check on the files ([{ name, code }]) the build emits, wherever they came from
     * (worker bundles included): one that loads the SDK from a CDN, and one that carries the Storage
     * SDK (its X-Firebase-Storage-Version header) with Google's host literal still in it.
     */
    scanOutput(files) {
      const problems = [];
      for (const f of files) {
        if (GSTATIC_FIREBASE.test(f.code)) problems.push(gstatic(f.name));
        if (P.leaks(f.code)) problems.push(P.unrewrittenCopy(f.name));
      }
      return problems;
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
  // Rolldown does not list external modules among the module ids; the chunks' imports name them
  // (an import that is not a file of this bundle is external).
  for (const f of Object.values(bundle)) {
    if (f.type !== 'chunk') continue;
    for (const id of [...(f.imports ?? []), ...(f.dynamicImports ?? [])]) {
      if (!(id in bundle)) modules.push({ id, external: true });
    }
  }
  // Code and pages only: a source map holds the original sources, Google's literal included.
  const files = Object.values(bundle)
    .filter((f) => /\.(?:m?js|cjs|html)$/.test(f.fileName))
    .map((f) => ({ name: f.fileName, code: f.type === 'chunk' ? f.code : String(f.source ?? '') }));
  const problems = [...rw.verify(modules, { browser, watch: Boolean(ctx.meta?.watchMode) }), ...rw.scanOutput(files)];
  if (problems.length) ctx.error(problems.join('\n'));
}

// webpack and Rspack: the module set at finishModules (also the modules a persistent cache
// restored, which the transform never saw), and the emitted assets.
function tapWebpack(rw, compiler) {
  const NAME = rw.product.name;
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
        if (!rw.isModule(id)) continue;
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
  return productFactory(STORAGE, options, meta);
}

/** The unplugin factory for Firestore. */
export function firestoreFactory(options = {}, meta = {}) {
  return productFactory(FIRESTORE, options, meta);
}

function productFactory(P, options, meta) {
  const { url } = options;
  const NAME = P.name;
  // Not configured: the plugin does nothing (as the Auth plugins).
  if (url === undefined || url === null || String(url).trim() === '') return { name: NAME };
  const rw = productRewriter(P, options);
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
      filter: { id: P.filter },
      handler(code, id) {
        const r = rw.transform(code, id);
        if (r) esbuild.built++;
        return r;
      },
    },
    buildEnd() {
      if (meta.framework !== 'esbuild') return;
      if (esbuild.built === 0 && rw.required && esbuild.browser) throw new Error(P.noneSeen);
    },
    esbuild: {
      onLoadFilter: P.filter,
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
      build.onLoad({ filter: rw.product.filter }, async (args) => {
        const r = rw.transform(await readFile(args.path, 'utf8'), args.path);
        return r ? { contents: r.code, loader: 'js' } : null;
      });
    },
  };
}

export { rollupVerify };
