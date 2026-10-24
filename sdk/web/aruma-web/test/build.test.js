// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, mkdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { esbuildOptimizerPlugin, storageFactory, storageRewriter } from '../src/build.js';
import aruma, { storagePlugin } from '../src/vite.js';
import { rewriteStorageLiterals, storageBase } from '../src/storage.js';
import { ESM_SNIPPET } from './storage-fixtures.js';

const URL_ = 'http://127.0.0.1:9421';
const ID = '/app/node_modules/@firebase/storage/dist/index.esm.js';
const rewritten = rewriteStorageLiterals(ESM_SNIPPET, storageBase(URL_)).code;

describe('the rewriter', () => {
  test('transforms @firebase/storage only, and throws on a release it does not know', () => {
    const rw = storageRewriter({ url: URL_ });
    assert.equal(rw.transform(ESM_SNIPPET, '/app/src/main.js'), null);
    assert.equal(rw.transform(ESM_SNIPPET, ID).code, rewritten);
    assert.throws(() => rw.transform('const DEFAULT_HOST = "x";', ID), /not supported by the build-time rewrite/);
    assert.throws(() => storageRewriter({ url: 'http://aruma.example.com' }), /not loopback/);
  });

  test('verify: the module set as the bundler ends it', () => {
    const rw = storageRewriter({ url: URL_ });
    assert.deepEqual(rw.verify([{ id: ID, code: rewritten }, { id: '/app/src/main.js', code: '' }]), []);
    // A module restored from a cache, or replaced by another plugin, still names Google.
    assert.match(rw.verify([{ id: ID, code: ESM_SNIPPET }]).join(), /still names firebasestorage/);
    // Left external (Vite's SSR default): the SDK loaded at run time is not rewritten.
    assert.match(rw.verify([{ id: '@firebase/storage', external: true }, { id: ID, code: rewritten }]).join(), /external to this bundle/);
    assert.match(rw.verify([{ id: 'firebase/storage', external: true }], { browser: false }).join(), /external/);
    assert.deepEqual(rw.verify([{ id: 'react', external: true }, { id: ID, code: rewritten }]), []);
    // No Storage at all: an error in a browser build, not in a server build, in watch mode, or
    // with required: false.
    assert.match(rw.verify([]).join(), /no @firebase\/storage module/);
    assert.deepEqual(rw.verify([], { browser: false }), []);
    assert.deepEqual(rw.verify([], { watch: true }), []);
    assert.deepEqual(storageRewriter({ url: URL_, required: false }).verify([]), []);
  });

  test('gstatic: in an import and in the output', () => {
    const rw = storageRewriter({ url: URL_ });
    assert.throws(() => rw.resolveGstatic('https://www.gstatic.com/firebasejs/13.1.0/firebase-storage.js'), /www\.gstatic\.com/);
    assert.equal(rw.resolveGstatic('firebase/storage'), null);
    assert.deepEqual(rw.scanOutput([{ name: 'a.js', code: 'x' }]), []);
    assert.match(rw.scanOutput([{ name: 'index.html', code: '<script src="https://www.gstatic.com/firebasejs/12.0.0/firebase-app.js">' }]).join(), /index\.html loads/);
  });

  test('the unplugin factory: nothing when not configured', () => {
    assert.deepEqual(storageFactory({}), { name: 'aruma-web:storage' });
    const p = storageFactory({ url: URL_ }, { framework: 'esbuild' });
    assert.equal(p.enforce, 'pre');
    // esbuild: a browser build with no Storage module fails, a Node one does not.
    p.buildStart();
    assert.throws(() => p.buildEnd(), /no @firebase\/storage module/);
    p.esbuild.config({ platform: 'node' });
    assert.doesNotThrow(() => p.buildEnd());
    p.esbuild.config({ platform: 'browser' });
    p.buildStart();
    p.transform.handler(ESM_SNIPPET, ID);
    assert.doesNotThrow(() => p.buildEnd());
  });
});

describe('vite plugin, storage', () => {
  test('without storage: the Auth plugin alone, as before', () => {
    assert.equal(Array.isArray(aruma({ url: URL_ })), false);
    const plugins = aruma({ url: URL_, storage: true });
    assert.deepEqual(plugins.map((p) => p.name), ['aruma-web', 'aruma-web:storage']);
  });

  test('config: ssr.noExternal, and the optimizer of the Vite in use', () => {
    const p = storagePlugin({ url: URL_ });
    const v8 = p.config.call({ meta: { rolldownVersion: '1.0.0' } });
    assert.deepEqual(v8.ssr, { noExternal: ['@firebase/storage', 'firebase'] });
    const [rolldown] = v8.optimizeDeps.rolldownOptions.plugins;
    assert.equal(rolldown.name, 'aruma-web:storage:http://127.0.0.1:9421');
    assert.equal(rolldown.transform(ESM_SNIPPET, ID).code, rewritten);
    const v7 = p.config.call({ meta: {} });
    assert.equal(v7.optimizeDeps.esbuildOptions.plugins[0].name, 'aruma-web:storage:http://127.0.0.1:9421');
  });

  test('index.html that loads the SDK from gstatic fails', () => {
    const p = storagePlugin({ url: URL_ });
    assert.equal(p.transformIndexHtml.handler('<html></html>', {}), '<html></html>');
    assert.throws(() => p.transformIndexHtml.handler('<script type="module">import "https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js"</script>', { filename: 'index.html' }), /www\.gstatic\.com/);
  });

  test('generateBundle checks the module set in a build, not in serve', () => {
    const p = storagePlugin({ url: URL_ });
    const ctx = (ids) => ({
      getModuleIds: () => ids.map((m) => m.id),
      getModuleInfo: (id) => ids.find((m) => m.id === id),
      error(msg) {
        throw new Error(msg);
      },
      environment: { config: { consumer: 'client' } },
      meta: {},
    });
    p.configResolved({ command: 'serve', build: {} });
    assert.doesNotThrow(() => p.generateBundle.call(ctx([]), {}, {}));
    p.configResolved({ command: 'build', build: {} });
    assert.throws(() => p.generateBundle.call(ctx([]), {}, {}), /no @firebase\/storage module/);
    assert.doesNotThrow(() => p.generateBundle.call(ctx([{ id: ID, code: rewritten }]), {}, { 'a.js': { type: 'chunk', fileName: 'a.js', code: '' } }));
    const ssr = { ...ctx([{ id: '@firebase/storage', isExternal: true }]), environment: { config: { consumer: 'server' } } };
    assert.throws(() => p.generateBundle.call(ssr, {}, {}), /external to this bundle/);
    const ssrNone = { ...ctx([]), environment: { config: { consumer: 'server' } } };
    assert.doesNotThrow(() => p.generateBundle.call(ssrNone, {}, {}));
  });

  test("Vite 5 to 7: the optimizer's esbuild plugin rewrites the file it loads", async () => {
    const dir = mkdtempSync(join(tmpdir(), 'aruma-web-'));
    try {
      const file = join(dir, 'node_modules', '@firebase', 'storage', 'dist', 'index.esm.js');
      mkdirSync(join(file, '..'), { recursive: true });
      writeFileSync(file, ESM_SNIPPET);
      const hooks = {};
      esbuildOptimizerPlugin(storageRewriter({ url: URL_ }), 'x').setup({
        onResolve: (o, f) => (hooks.resolve = { o, f }),
        onLoad: (o, f) => (hooks.load = { o, f }),
      });
      assert.equal(hooks.load.o.filter.test(file), true);
      assert.deepEqual(await hooks.load.f({ path: file }), { contents: rewritten, loader: 'js' });
      assert.throws(() => hooks.resolve.f({ path: 'https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js' }), /gstatic/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
