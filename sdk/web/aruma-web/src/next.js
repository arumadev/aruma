// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Next.js: the Storage rewrite for both of its bundlers, one line in next.config.mjs:
//
//   import { withArumaStorage } from '@aruma/web/next';
//   export default withArumaStorage({ /* the app's config */ }, { url: 'https://aruma.example.com' });
//
// - webpack (`next build --webpack`): the unplugin webpack plugin on every compiler, with all its
//   checks; the server compilers never fail for a bundle without Storage.
// - Turbopack (`next build`, `next dev`): a turbopack.rules loader on @firebase/storage's dist files
//   (webpack-loader.cjs). A file whose literals are not found fails the build. Turbopack gives loaders
//   no end-of-build hook, so a build in which no file reached the loader cannot fail here.
// - The server: Next.js leaves node_modules external to the Pages Router's server bundle
//   (bundlePagesRouterDependencies is off by default) and loads serverExternalPackages with Node's
//   require, so neither loader nor plugin would see the server's SDK. The Storage SDK and the
//   firebase package that re-exports it go into transpilePackages, which Next.js bundles in both
//   routers, and they are refused in serverExternalPackages.
import { createRequire } from 'node:module';
import { ArumaConfigError } from './index.js';
import { storageBase } from './storage.js';
import { arumaStorage } from './unplugin.js';

const require = createRequire(import.meta.url);

/** The packages the server must bundle for the rewrite to reach its Storage SDK. */
export const STORAGE_PACKAGES = Object.freeze(['firebase', '@firebase/storage', '@firebase/storage-compat']);

/**
 * @param {object} [nextConfig]
 * @param {{ url?: string | URL, allowInsecure?: boolean, required?: boolean }} [options]
 */
export function withArumaStorage(nextConfig = {}, { url, allowInsecure = false, required = true } = {}) {
  if (url === undefined || url === null || String(url).trim() === '') return nextConfig;
  const base = storageBase(url, { allowInsecure });
  const external = (nextConfig.serverExternalPackages ?? []).filter((p) => STORAGE_PACKAGES.includes(p));
  if (external.length) {
    throw new ArumaConfigError(
      `@aruma/web/next: ${external.join(', ')} in serverExternalPackages would load the Storage SDK with Google's host on the server; remove ${external.length > 1 ? 'them' : 'it'}`,
    );
  }
  const rule = {
    condition: { path: /[\\/]@firebase[\\/]storage[\\/]dist[\\/]/ },
    loaders: [{ loader: require.resolve('./webpack-loader.cjs'), options: { url: base.origin, allowInsecure: Boolean(allowInsecure) } }],
  };
  const rules = { ...nextConfig.turbopack?.rules };
  for (const glob of ['*.js', '*.mjs', '*.cjs']) {
    const prev = rules[glob];
    rules[glob] = prev === undefined ? [rule] : [...(Array.isArray(prev) ? prev : [prev]), rule];
  }
  return {
    ...nextConfig,
    transpilePackages: [...new Set([...(nextConfig.transpilePackages ?? []), ...STORAGE_PACKAGES])],
    webpack(config, ctx) {
      config.plugins.push(arumaStorage.webpack({ url: base.origin, allowInsecure, required }));
      return typeof nextConfig.webpack === 'function' ? nextConfig.webpack(config, ctx) : config;
    },
    turbopack: { ...nextConfig.turbopack, rules },
  };
}

export default withArumaStorage;
