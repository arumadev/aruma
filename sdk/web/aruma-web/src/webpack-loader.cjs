// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The Storage rewrite as a webpack-style loader, for the bundlers that take loaders but no plugin:
// Turbopack (turbopack.rules, which next.js sets up). The rule is storage.js's; a @firebase/storage
// file whose literals are not found fails the build. Loader options: { url, allowInsecure }.
//
// CommonJS, because Turbopack loads loaders with require(); storage.js is required as ES module
// (Node 20.19 and later).
'use strict';

module.exports = function arumaStorageLoader(source) {
  const { isStorageModule, literalProblem, rewriteStorageLiterals, storageBase } = require('./storage.js');
  const { url, allowInsecure = false } = this.getOptions();
  const id = this.resourcePath;
  if (!isStorageModule(id)) return source;
  const base = storageBase(url, { allowInsecure });
  const r = rewriteStorageLiterals(source, base);
  if (!r.ok) throw new Error(literalProblem(id, r, base));
  return r.code;
};
