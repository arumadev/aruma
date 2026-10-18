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
import { parseBase } from './index.js';

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
 * @param {{ url?: string, allowInsecure?: boolean, log?: boolean, mode?: 'fetch' | 'build' }} [options]
 */
export default function aruma(options = {}) {
  const { url, allowInsecure = false, log = false, mode = 'fetch' } = options;
  // Not configured: the plugin does nothing.
  if (url === undefined || url === null || String(url).trim() === '') return { name: 'aruma-web' };
  // Refused: stop the build or the dev server instead of shipping an app that talks to Google.
  const base = parseBase(url, { allowInsecure });

  if (mode === 'build') {
    // The name carries the URL so that Vite's dependency cache is rebuilt when it changes.
    const rewriter = buildModeRewriter(base, `aruma-web:build:${base.href}`);
    return {
      name: 'aruma-web:build',
      enforce: 'pre',
      config() {
        // The dev server pre-bundles @firebase/auth; only Rolldown's optimizer (Vite 8+) runs this
        // rewrite there. On an older Vite the dev app would talk to Google, so refuse instead.
        if (!this?.meta?.rolldownVersion) {
          throw new Error("@aruma/web/vite: mode 'build' needs Vite 8 or later (Rolldown); use mode 'fetch'");
        }
        return { optimizeDeps: { rolldownOptions: { plugins: [rewriter] } } };
      },
      buildStart: rewriter.buildStart,
      transform: rewriter.transform,
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
