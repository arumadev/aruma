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
import {
  firestoreBase,
  firestoreLiteralProblem,
  firestoreModuleRewritten,
  isFirestoreModule,
  rewriteFirestoreLiterals,
} from './firestore.js';
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

/** Metro's Babel transformer entry: rewrite a @firebase/storage or @firebase/firestore file, then the upstream transformer. */
export function transform(args) {
  let { src } = args;
  const s = settings();
  if (isStorageModule(args.filename) && s.url) {
    const base = storageBase(s.url, { allowInsecure: s.allowInsecure });
    const r = rewriteStorageLiterals(src, base);
    if (!r.ok) throw new Error(literalProblem(args.filename, r, base));
    src = r.code;
  } else if (isFirestoreModule(args.filename) && s.firestore) {
    const base = firestoreBase(s.firestore.url, { allowInsecure: s.firestore.allowInsecure });
    const r = rewriteFirestoreLiterals(src, base);
    if (!r.ok) throw new Error(firestoreLiteralProblem(args.filename, r, base));
    src = r.code;
  }
  return upstreamTransformer().transform({ ...args, src });
}

/** Part of Metro's transform cache key: the upstream's key, this rule's version and the URLs. */
export function getCacheKey(...args) {
  const u = upstreamTransformer();
  const s = settings();
  const parts = [typeof u.getCacheKey === 'function' ? u.getCacheKey(...args) : '', CACHE_VERSION, s.url, s.allowInsecure];
  if (s.firestore) parts.push('firestore', s.firestore.url, s.firestore.allowInsecure);
  return parts.join('\0');
}

// The settings of the transformer, and of the serializer's check, merged over the calls of
// withArumaStorage and withArumaFirestore on the same config (the second finds the first's).
function wrap(config, product, { url, allowInsecure }) {
  const self = fileURLToPath(import.meta.url);
  const current = config.transformer?.babelTransformerPath;
  const prev = current === self && process.env[ENV] ? JSON.parse(process.env[ENV]) : null;
  const upstreamPath = prev?.upstream ?? (current && current !== self ? current : require.resolve('@react-native/metro-babel-transformer'));
  const own = { url: new URL(url).origin, allowInsecure: Boolean(allowInsecure) };
  const next = product === 'storage' ? { ...prev, ...own, upstream: upstreamPath } : { ...prev, firestore: own, upstream: upstreamPath };
  process.env[ENV] = JSON.stringify(next);
  const filter = config.serializer?.processModuleFilter;
  const check = (module) => {
    const code = () => (module.output ?? []).map((o) => o?.data?.code ?? '').join('\n');
    if (product === 'storage' && isStorageModule(module.path)) {
      if (!storageModuleRewritten(code(), storageBase(url, { allowInsecure }))) throw new Error(notRewritten(module.path));
    } else if (product === 'firestore' && isFirestoreModule(module.path)) {
      if (!firestoreModuleRewritten(code())) throw new Error(notRewritten(module.path, 'Firestore', 'firestore.googleapis.com'));
    }
  };
  return {
    ...config,
    transformer: { ...config.transformer, babelTransformerPath: self },
    serializer: {
      ...config.serializer,
      processModuleFilter(module) {
        check(module);
        return filter ? filter(module) : true;
      },
    },
  };
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
  return wrap(config, 'storage', { url: base.origin, allowInsecure });
}

/** The same for Cloud Firestore (SR3-10), alone or on the config withArumaStorage returned. */
export function withArumaFirestore(config, { url, allowInsecure = false } = {}) {
  if (url === undefined || url === null || String(url).trim() === '') return config;
  const base = firestoreBase(url, { allowInsecure });
  return wrap(config, 'firestore', { url: base.origin, allowInsecure });
}

const notRewritten = (path, product = 'Storage', host = 'firebasestorage.googleapis.com') =>
  `@aruma/web/metro: ${path} was bundled without the ${product} rewrite, so it keeps ${host}. A config wrapper applied after withAruma${product} replaced transformer.babelTransformerPath (react-native-svg-transformer and the like): apply withAruma${product} last, around the others`;
