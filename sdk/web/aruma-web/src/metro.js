// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The build-time Storage rewrite for Metro (Expo, and React Native with the firebase JS SDK),
// ADR-022 §16.1. One line in metro.config.js:
//
//   const { getDefaultConfig } = require('expo/metro-config');
//   const { withArumaStorage } = require('@aruma/web/metro');
//   module.exports = withArumaStorage(getDefaultConfig(__dirname), { url: 'https://aruma.example.com' });
//
// Metro transforms each file in a worker with the Babel transformer named by
// transformer.babelTransformerPath. withArumaStorage puts this module there and remembers the one it
// replaces (Expo's, or React Native's); transform() rewrites the @firebase/storage dist files with
// storage.js's rule (whichever one Metro resolves for the platform: index.esm.js on the web,
// index.esm.js or index.node.cjs.js on a device) and hands every file on to that transformer. A
// @firebase/storage file whose literals are not found fails the bundle.
//
// The settings reach the workers through the environment (ARUMA_WEB_METRO), which Metro's workers
// inherit from the process that loaded metro.config.js; they are part of the cache key, so a new
// URL transforms the files again.
//
// A config wrapper applied after this one (react-native-svg-transformer, …) may set its own
// babelTransformerPath and drop the rewrite. Metro gives a transformer no end of build, but its
// serializer asks serializer.processModuleFilter about every module it writes: there a
// @firebase/storage module whose code still names Google, or not Aruma, fails the bundle.
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { isStorageModule, literalProblem, rewriteStorageLiterals, storageBase, storageModuleRewritten } from './storage.js';

const require = createRequire(import.meta.url);
const ENV = 'ARUMA_WEB_METRO';
const CACHE_VERSION = 'aruma-web-storage-1';

function settings() {
  const raw = process.env[ENV];
  if (!raw) {
    throw new Error("@aruma/web/metro: this transformer is set by withArumaStorage(config, { url }) in metro.config.js; it was loaded without it");
  }
  return JSON.parse(raw);
}

let upstream;
function upstreamTransformer() {
  upstream ??= require(settings().upstream);
  return upstream;
}

/** Metro's Babel transformer entry: rewrite a @firebase/storage file, then the upstream transformer. */
export function transform(args) {
  let { src } = args;
  if (isStorageModule(args.filename)) {
    const s = settings();
    const base = storageBase(s.url, { allowInsecure: s.allowInsecure });
    const r = rewriteStorageLiterals(src, base);
    if (!r.ok) throw new Error(literalProblem(args.filename, r, base));
    src = r.code;
  }
  return upstreamTransformer().transform({ ...args, src });
}

/** Part of Metro's transform cache key: the upstream's key, this rule's version and the URL. */
export function getCacheKey(...args) {
  const u = upstreamTransformer();
  const s = settings();
  return [typeof u.getCacheKey === 'function' ? u.getCacheKey(...args) : '', CACHE_VERSION, s.url, s.allowInsecure].join('\0');
}

/**
 * Returns the Metro config with this transformer in front of the current one.
 * Not configured (`url` missing or empty): the config unchanged. Refused: throws.
 *
 * @param {object} config Metro's config (getDefaultConfig's)
 * @param {{ url?: string | URL, allowInsecure?: boolean }} [options]
 */
export function withArumaStorage(config, { url, allowInsecure = false } = {}) {
  if (url === undefined || url === null || String(url).trim() === '') return config;
  const base = storageBase(url, { allowInsecure });
  const self = fileURLToPath(import.meta.url);
  const current = config.transformer?.babelTransformerPath;
  const upstreamPath = current && current !== self ? current : require.resolve('@react-native/metro-babel-transformer');
  process.env[ENV] = JSON.stringify({ url: base.origin, allowInsecure: Boolean(allowInsecure), upstream: upstreamPath });
  const filter = config.serializer?.processModuleFilter;
  return {
    ...config,
    transformer: { ...config.transformer, babelTransformerPath: self },
    serializer: {
      ...config.serializer,
      processModuleFilter(module) {
        if (isStorageModule(module.path)) {
          const code = (module.output ?? []).map((o) => o?.data?.code ?? '').join('\n');
          if (!storageModuleRewritten(code, base)) throw new Error(notRewritten(module.path));
        }
        return filter ? filter(module) : true;
      },
    },
  };
}

const notRewritten = (path) =>
  `@aruma/web/metro: ${path} was bundled without the Storage rewrite, so it keeps firebasestorage.googleapis.com. A config wrapper applied after withArumaStorage replaced transformer.babelTransformerPath (react-native-svg-transformer and the like): apply withArumaStorage last, around the others`;
