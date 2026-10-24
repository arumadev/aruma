// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Vite plugin: one line in vite.config, no change in the app.
//
//   import aruma from '@aruma/web/vite';
//   export default defineConfig({ plugins: [aruma({ url: 'https://aruma.example.com' })] });
//
// mode 'fetch' (default): injects the install as the first module of index.html, before the app's
//   own scripts, so the fetch redirect is active before the app sends its first Auth request.
// mode 'build': no runtime code. Rewrites the two host literals inside @firebase/auth at build
//   time ("identitytoolkit.googleapis.com" → "<aruma host>/identitytoolkit.googleapis.com", the
//   same for securetoken, and the scheme when Aruma is on http), in `vite build` and in the dev
//   server's dependency optimizer. The build fails if the literals are not found.
//
// storage: true adds the build-time Storage rewrite (ADR-022 §16.1, storage.js and unplugin.js):
//   DEFAULT_HOST in @firebase/storage becomes Aruma's host, in `vite build`, in SSR (it sets
//   ssr.noExternal, since Vite externalizes dependencies from the SSR transform by default) and in
//   the dev server's dependency optimizer (Rolldown's on Vite 8, esbuild's on Vite 5 to 7). The
//   build fails when a module is not rewritten, when a browser build has no @firebase/storage
//   module, and when anything loads the SDK from www.gstatic.com.
//
// Without storage: true the build fails on a @firebase/storage module (SR3-06): that SDK sends the
// app's ID token, here Aruma's, with every request (`Authorization: Firebase <token>`), and its
// host would stay Google's. At run time install() refuses such a request too (index.js).
import { parseBase } from './index.js';
import { esbuildOptimizerPlugin, rollupVerify, storageRewriter } from './build.js';
import { isStorageModule } from './storage.js';

const VIRTUAL = 'virtual:aruma-web/install';
const RESOLVED = `\0${VIRTUAL}`;
const AUTH_DIST = /[\\/]@firebase[\\/]auth[\\/]dist[\\/].*\.m?js$/;

/**
 * The build-time rewrite of one @firebase/auth module. Exported for tests.
 * @returns {{ code: string, hosts: number, scheme: number }}
 */
export function rewriteAuthLiterals(code, base) {
  let hosts = 0;
  let scheme = 0;
  let out = code.replace(/(["'])(identitytoolkit|securetoken)\.googleapis\.com\1/g, (_, q, name) => {
    hosts++;
    return `${q}${base.host}${base.prefix}/${name}.googleapis.com${q}`;
  });
  if (base.protocol !== 'https') {
    out = out.replace(/(apiScheme\s*:\s*)(["'])https\2/g, (_, key, q) => {
      scheme++;
      return `${key}${q}${base.protocol}${q}`;
    });
  }
  return { code: out, hosts, scheme };
}

function buildModeRewriter(base, name) {
  const seen = { modules: 0, hosts: 0, scheme: 0 };
  const transform = (code, id) => {
    if (!AUTH_DIST.test(id.split('?')[0])) return null;
    seen.modules++;
    const r = rewriteAuthLiterals(code, base);
    seen.hosts += r.hosts;
    seen.scheme += r.scheme;
    return r.hosts || r.scheme ? { code: r.code, map: null } : null;
  };
  // Fail closed: if this @firebase/auth no longer spells its hosts as literals, its requests would
  // go to Google. Then the build stops instead.
  const check = () => {
    if (seen.modules === 0) return null;
    if (seen.hosts < 2 || (base.protocol !== 'https' && seen.scheme === 0)) {
      return `@aruma/web: the Firebase Auth host literals were not found in @firebase/auth (${seen.hosts} host and ${seen.scheme} scheme replacements); this SDK version is not supported by mode 'build', use mode 'fetch'`;
    }
    return null;
  };
  const reset = () => Object.assign(seen, { modules: 0, hosts: 0, scheme: 0 });
  return {
    name,
    buildStart: reset,
    transform,
    buildEnd(error) {
      const problem = !error && check();
      if (problem) this.error(problem);
    },
  };
}

/**
 * The Storage half of the Vite plugin. Exported for tests.
 *
 * Vite builds a Web Worker (new Worker(new URL(…)), ?worker) with worker.plugins alone, so config
 * puts a second instance there (`worker: true`: no config of its own, and a worker bundle without
 * Storage is fine); without it a worker would ship Google's host.
 *
 * @param {{ url: string, allowInsecure?: boolean, log?: boolean, required?: boolean, worker?: boolean }} options
 */
export function storagePlugin(options) {
  const { worker = false, ...rest } = options;
  const rw = storageRewriter(worker ? { ...rest, required: false } : rest);
  // The optimizer's own plugin: it cannot see the whole app, so it never asks for a module. Its
  // name carries the URL, which Vite hashes into the dependency cache, so a new URL rebuilds it.
  const name = `aruma-web:storage:${rw.base.origin}`;
  const rolldownOptimizer = {
    name,
    resolveId: (id) => rw.resolveGstatic(id),
    transform: (code, id) => rw.transform(code, id),
  };
  let resolved;
  return {
    name: 'aruma-web:storage',
    enforce: 'pre',
    config() {
      if (worker) return null;
      const rolldown = Boolean(this?.meta?.rolldownVersion);
      return {
        ssr: { noExternal: ['@firebase/storage', '@firebase/storage-compat', 'firebase'] },
        worker: { plugins: () => [storagePlugin({ ...rest, worker: true })] },
        optimizeDeps: rolldown
          ? { rolldownOptions: { plugins: [rolldownOptimizer] } }
          : { esbuildOptions: { plugins: [esbuildOptimizerPlugin(rw, name)] } },
      };
    },
    configResolved(config) {
      resolved = config;
    },
    resolveId(id) {
      return rw.resolveGstatic(id);
    },
    transform(code, id) {
      return rw.transform(code, id);
    },
    // Last, so that index.html, which Vite emits in its own generateBundle, is in the bundle too.
    // (Not transformIndexHtml: SvelteKit and other frameworks without an index.html warn about it.)
    generateBundle: {
      order: 'post',
      handler(_, bundle) {
        // A worker bundle is only ever generated by a build.
        if (worker) return rollupVerify(rw, this, bundle, { browser: false });
        if (resolved?.command !== 'build') return;
        const env = this.environment?.config;
        const server = env ? env.consumer === 'server' : Boolean(resolved.build?.ssr);
        rollupVerify(rw, this, bundle, { browser: !server });
      },
    },
  };
}

/**
 * @param {{ url?: string, allowInsecure?: boolean, log?: boolean, mode?: 'fetch' | 'build',
 *   storage?: boolean | { required?: boolean } }} [options]
 */
export default function aruma(options = {}) {
  const { url, allowInsecure = false, log = false, mode = 'fetch', storage = false } = options;
  // Not configured: the plugin does nothing.
  if (url === undefined || url === null || String(url).trim() === '') return { name: 'aruma-web' };
  // Refused: stop the build or the dev server instead of shipping an app that talks to Google.
  const base = parseBase(url, { allowInsecure });
  const auth = authPlugin(base, { allowInsecure, log, mode, storage: Boolean(storage) });
  if (!storage) return auth;
  const required = typeof storage === 'object' && storage.required === false ? false : true;
  return [auth, storagePlugin({ url, allowInsecure, log, required })];
}

/** The error of a build that has Auth on Aruma and @firebase/storage left on Google. */
export const storageOff = (id) =>
  `@aruma/web: this build includes @firebase/storage (${id}) but Storage is not enabled, so its requests would go to firebasestorage.googleapis.com carrying the Aruma ID token. Add storage: true: aruma({ url, storage: true })`;

// Without the Storage half, a @firebase/storage module stops the build (SR3-06).
function refuseStorage(id) {
  if (isStorageModule(id)) throw new Error(storageOff(id.split('?')[0]));
}

function authPlugin(base, { allowInsecure, log, mode, storage }) {
  const guard = storage ? () => {} : refuseStorage;

  if (mode === 'build') {
    // The name carries the URL so that Vite's dependency cache is rebuilt when it changes.
    const rewriter = buildModeRewriter(base, `aruma-web:build:${base.href}`);
    const transform = (code, id) => {
      guard(id);
      return rewriter.transform(code, id);
    };
    return {
      name: 'aruma-web:build',
      enforce: 'pre',
      config() {
        // The dev server pre-bundles @firebase/auth; only Rolldown's optimizer (Vite 8+) runs this
        // rewrite there. On an older Vite the dev app would talk to Google, so refuse instead.
        if (!this?.meta?.rolldownVersion) {
          throw new Error("@aruma/web/vite: mode 'build' needs Vite 8 or later (Rolldown); use mode 'fetch'");
        }
        return { optimizeDeps: { rolldownOptions: { plugins: [{ ...rewriter, transform }] } } };
      },
      buildStart: rewriter.buildStart,
      transform,
      buildEnd: rewriter.buildEnd,
    };
  }
  if (mode !== 'fetch') throw new Error(`@aruma/web/vite: unknown mode ${JSON.stringify(mode)} (use 'fetch' or 'build')`);

  const settings = JSON.stringify({ url: base.href, allowInsecure, log: Boolean(log) });
  return {
    name: 'aruma-web',
    config() {
      return { optimizeDeps: { include: ['@aruma/web'] } };
    },
    resolveId(id) {
      return id === VIRTUAL ? RESOLVED : null;
    },
    // The dev server pre-bundles @firebase/storage without this hook; there install() refuses.
    transform(_, id) {
      guard(id);
      return null;
    },
    load(id) {
      if (id !== RESOLVED) return null;
      return `import { install } from '@aruma/web';\ninstall(${settings});\n`;
    },
    transformIndexHtml: {
      order: 'pre',
      handler() {
        return [{ tag: 'script', attrs: { type: 'module' }, children: `import '${VIRTUAL}';`, injectTo: 'head-prepend' }];
      },
    },
  };
}

export { aruma };
