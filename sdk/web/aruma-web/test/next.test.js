// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { withArumaStorage } from '../src/next.js';
import { rewriteStorageLiterals, storageBase } from '../src/storage.js';
import { ESM_SNIPPET } from './storage-fixtures.js';

const URL_ = 'http://127.0.0.1:9421';
const ID = '/app/node_modules/@firebase/storage/dist/index.esm.js';
const rewritten = rewriteStorageLiterals(ESM_SNIPPET, storageBase(URL_)).code;

describe('Next.js and Turbopack', () => {
  test('withArumaStorage: the webpack plugin and a turbopack.rules loader on @firebase/storage', () => {
    const user = { reactStrictMode: true, turbopack: { rules: { '*.svg': { loaders: ['svgr'] } } }, webpack: (c) => ({ ...c, touched: true }) };
    assert.equal(withArumaStorage(user, {}), user);
    const c = withArumaStorage(user, { url: URL_ });
    assert.equal(c.reactStrictMode, true);
    assert.deepEqual(c.turbopack.rules['*.svg'], { loaders: ['svgr'] });
    const [rule] = c.turbopack.rules['*.js'];
    assert.equal(rule.condition.path.test(ID), true);
    assert.equal(rule.condition.path.test('/app/node_modules/@firebase/auth/dist/index.esm.js'), false);
    assert.deepEqual(rule.loaders[0].options, { url: URL_, allowInsecure: false, product: 'storage' });
    const out = c.webpack({ plugins: [] }, {});
    assert.equal(out.touched, true);
    assert.equal(out.plugins.length, 1);
    assert.throws(() => withArumaStorage({}, { url: 'https://aruma.example.com/base' }), /root of its host/);
    // The server bundles the SDK in both routers, and may not load it with require.
    assert.deepEqual(withArumaStorage({ transpilePackages: ['ui', 'firebase'] }, { url: URL_ }).transpilePackages, ['ui', 'firebase', '@firebase/storage', '@firebase/storage-compat']);
    assert.throws(() => withArumaStorage({ serverExternalPackages: ['sharp', 'firebase'] }, { url: URL_ }), /firebase in serverExternalPackages/);
    assert.equal(withArumaStorage({ serverExternalPackages: ['sharp'] }, { url: URL_ }).serverExternalPackages[0], 'sharp');
  });

  test('the loader rewrites @firebase/storage and fails on a release it does not know', () => {
    const loader = createRequire(import.meta.url)('../src/webpack-loader.cjs');
    const ctx = (resourcePath) => ({ resourcePath, getOptions: () => ({ url: URL_ }) });
    assert.equal(loader.call(ctx(ID), ESM_SNIPPET), rewritten);
    assert.equal(loader.call(ctx('/app/src/a.js'), 'x'), 'x');
    assert.throws(() => loader.call(ctx(ID), 'const x = 1;'), /not supported by the build-time rewrite/);
  });
});
