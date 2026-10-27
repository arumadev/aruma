// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The Storage and Firestore rewrite as a webpack-style loader, for the bundlers that take loaders but
// no plugin: Turbopack (turbopack.rules, which next.js sets up). The rule is storage.js's or
// firestore.js's; a @firebase/storage file whose literals are not found fails the build. Loader
// options: { url, allowInsecure, product: 'storage' (default) | 'firestore' }.
//
// CommonJS, because Turbopack loads loaders with require(); storage.js and firestore.js are required
// as ES modules (Node 20.19 and later).
'use strict';

function rule(product) {
  if (product === 'firestore') {
    const m = require('./firestore.js');
    return { isModule: m.isFirestoreModule, rewrite: m.rewriteFirestoreLiterals, problem: m.firestoreLiteralProblem, baseOf: m.firestoreBase };
  }
  const m = require('./storage.js');
  return { isModule: m.isStorageModule, rewrite: m.rewriteStorageLiterals, problem: m.literalProblem, baseOf: m.storageBase };
}

module.exports = function arumaStorageLoader(source) {
  const { url, allowInsecure = false, product = 'storage' } = this.getOptions();
  const id = this.resourcePath;
  const P = rule(product);
  if (!P.isModule(id)) return source;
  const base = P.baseOf(url, { allowInsecure });
  // A Firestore dist file with no host literal passes unchanged: most hold only the shared chunk.
  const r = P.rewrite(source, base);
  if (!r.ok) throw new Error(P.problem(id, r, base));
  return r.code;
};
