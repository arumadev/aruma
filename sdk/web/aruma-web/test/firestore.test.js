// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { firestoreFactory, firestoreRewriter, storageRewriter } from '../src/build.js';
import {
  arumaFirestoreSettings,
  firestoreBase,
  firestoreLiteralProblem,
  isFirestoreModule,
  namesGoogleFirestore,
  rewriteFirestoreLiterals,
} from '../src/firestore.js';
import { GSTATIC_FIREBASE } from '../src/storage.js';
import { ArumaConfigError } from '../src/index.js';
import aruma, { firestoreOff, firestorePlugin } from '../src/vite.js';
import { withArumaFirestore, withArumaStorage, FIRESTORE_PACKAGES } from '../src/next.js';
import { getCacheKey, transform, withArumaFirestore as metroFirestore, withArumaStorage as metroStorage } from '../src/metro.js';
import { flutterWeb } from '../src/flutter-web.js';
import { ESM_SNIPPET } from './storage-fixtures.js';

// The lines of @firebase/firestore 4.19 that the rewrite touches: the browser build as it is
// minified (common-DE1gv9M5.esm.js:10251), the Node build (common-CJxxXJP6.node.mjs:12070, :18293)
// and the lite browser and React Native builds (lite/common-Cl98i9kc.esm.js:5321, …rn.esm.js:5350).
const BROWSER = 'const rn = "firestore.googleapis.com", sn = true;\nclass S { constructor(e) { this.host = rn, this.ssl = sn; } }';
const LITE = ' */ const Me = 1048576, qe = "firestore.googleapis.com", Le = true;';
const RN = ' */ const Ue = 1048576, $e = "firestore.googleapis.com", ke = !0;';
const NODE = `
\t\t\t\t\t"(google.api.default_host)": "firestore.googleapis.com",
const DEFAULT_HOST = 'firestore.googleapis.com';
const DEFAULT_SSL = true;
`;
const ID = '/app/node_modules/@firebase/firestore/dist/common-DE1gv9M5.esm.js';

describe('firestoreBase', () => {
  test('the root of a plain host, https or loopback http', () => {
    assert.equal(firestoreBase('https://aruma.example.com/').host, 'aruma.example.com');
    assert.equal(firestoreBase('http://127.0.0.1:8093').host, '127.0.0.1:8093');
    assert.equal(firestoreBase('http://[::1]:8093').host, '[::1]:8093');
  });
  test('refuses a path, a Google host, a host that breaks out of a string literal, and plain http', () => {
    assert.throws(() => firestoreBase('https://aruma.example.com/base'), /root of its host/);
    assert.throws(() => firestoreBase('https://firestore.googleapis.com'), /Google host/);
    assert.throws(() => firestoreBase("https://a'b.example.com"), ArumaConfigError);
    assert.throws(() => firestoreBase('https://a"b.example.com'), ArumaConfigError);
    assert.throws(() => firestoreBase('https://a%22b.example.com'), ArumaConfigError);
    assert.throws(() => firestoreBase('http://aruma.example.com'), /not loopback/);
    assert.doesNotThrow(() => firestoreBase('http://aruma.example.com', { allowInsecure: true }));
  });
});

describe('rewriteFirestoreLiterals', () => {
  const https = firestoreBase('https://aruma.example.com');
  const http = firestoreBase('http://127.0.0.1:9420');

  test('the browser, lite and React Native builds: host, and ssl on http', () => {
    let r = rewriteFirestoreLiterals(BROWSER, https);
    assert.deepEqual([r.hosts, r.schemes, r.ok], [1, 0, true]);
    assert.match(r.code, /const rn = "aruma\.example\.com", sn = true;/);
    r = rewriteFirestoreLiterals(BROWSER, http);
    assert.deepEqual([r.hosts, r.schemes, r.ok], [1, 1, true]);
    assert.match(r.code, /const rn = "127\.0\.0\.1:9420", sn = false;/);
    assert.match(rewriteFirestoreLiterals(LITE, http).code, /qe = "127\.0\.0\.1:9420", Le = false;/);
    assert.match(rewriteFirestoreLiterals(RN, http).code, /\$e = "127\.0\.0\.1:9420", ke = false;/);
    assert.equal(rewriteFirestoreLiterals(LITE, https).schemes, 0);
  });

  test('the Node build: DEFAULT_HOST and DEFAULT_SSL, the gRPC descriptor metadata stays', () => {
    const r = rewriteFirestoreLiterals(NODE, http);
    assert.deepEqual([r.hosts, r.schemes, r.ok], [1, 1, true]);
    assert.match(r.code, /const DEFAULT_HOST = "127\.0\.0\.1:9420";/);
    assert.match(r.code, /const DEFAULT_SSL = false;/);
    assert.match(r.code, /"\(google\.api\.default_host\)": "firestore\.googleapis\.com"/);
    assert.equal(namesGoogleFirestore(r.code), false, 'the metadata is not a connection');
  });

  test('a file with no literal is left alone; two literals, or no ssl default on http, is a release it does not know', () => {
    const none = rewriteFirestoreLiterals('export { x } from "./common.js";', http);
    assert.deepEqual([none.hosts, none.ok, none.code], [0, true, 'export { x } from "./common.js";']);
    const two = rewriteFirestoreLiterals(`${BROWSER}\nconst z = "firestore.googleapis.com";`, https);
    assert.equal(two.ok, false);
    assert.match(firestoreLiteralProblem(ID, two, https), /expected the host literal at most once in .*, found 2 host/);
    const noSsl = rewriteFirestoreLiterals('const h = "firestore.googleapis.com";', http);
    assert.equal(noSsl.ok, false);
    assert.match(firestoreLiteralProblem(ID, noSsl, http), /ssl default once each/);
  });

  test('only the dist files of @firebase/firestore', () => {
    for (const id of [
      ID,
      '/a/node_modules/@firebase/firestore/dist/lite/common-Cl98i9kc.esm.js?v=1',
      'C:\\a\\node_modules\\@firebase\\firestore\\dist\\index.node.cjs.js',
      '/a/node_modules/.pnpm/@firebase+firestore@4.19.0/node_modules/@firebase/firestore/dist/index.esm.js',
    ]) assert.ok(isFirestoreModule(id), id);
    for (const id of ['/a/node_modules/@firebase/firestore-compat/dist/index.esm.js', '/a/node_modules/@firebase/firestore/dist/index.d.ts', '/a/src/firestore.js']) {
      assert.ok(!isFirestoreModule(id), id);
    }
  });
});

describe('the run-time setting', () => {
  test('host and ssl, production mode, other settings kept', () => {
    assert.deepEqual(arumaFirestoreSettings('https://aruma.example.com', { ignoreUndefinedProperties: true }), {
      ignoreUndefinedProperties: true,
      host: 'aruma.example.com',
      ssl: true,
    });
    assert.deepEqual(arumaFirestoreSettings('http://127.0.0.1:9420'), { host: '127.0.0.1:9420', ssl: false });
    assert.equal('emulatorOptions' in arumaFirestoreSettings('https://aruma.example.com'), false);
    assert.throws(() => arumaFirestoreSettings('https://aruma.example.com', { emulatorOptions: { mockUserToken: 'x' } }), ArumaConfigError);
    assert.throws(() => arumaFirestoreSettings('https://aruma.example.com/base'), ArumaConfigError);
    assert.throws(() => arumaFirestoreSettings(''), ArumaConfigError);
  });
});

describe('the build checks', () => {
  const url = 'https://aruma.example.com';

  test('transform: rewrites the module with the literal, passes the rest, fails an unknown release', () => {
    const rw = firestoreRewriter({ url });
    assert.match(rw.transform(BROWSER, ID).code, /"aruma\.example\.com"/);
    assert.equal(rw.transform('export * from "./x.js"', '/a/node_modules/@firebase/firestore/dist/index.esm.js'), null);
    assert.equal(rw.transform(BROWSER, '/a/src/main.js'), null);
    assert.throws(() => rw.transform(`${BROWSER}\n"firestore.googleapis.com"`, ID), /not supported by the build-time rewrite/);
  });

  test('verify: a module that still names Google, an external SDK, and no carrying module at all', () => {
    const rw = firestoreRewriter({ url });
    const done = rw.transform(BROWSER, ID).code;
    assert.deepEqual(rw.verify([{ id: ID, code: done }]), []);
    assert.deepEqual(rw.verify([{ id: ID, code: done }, { id: '/a/node_modules/@firebase/firestore/dist/index.esm.js', code: 'export * from "./c.js"' }]), []);
    assert.match(rw.verify([{ id: ID, code: BROWSER }]).join(), /still names firestore\.googleapis\.com/);
    assert.match(rw.verify([{ id: '@firebase/firestore', external: true }, { id: ID, code: done }]).join(), /external to this bundle.*Aruma ID token/);
    assert.match(rw.verify([{ id: 'firebase/firestore/lite', external: true }], { browser: false }).join(), /external/);
    assert.match(rw.verify([]).join(), /no @firebase\/firestore module carrying the Firestore host/);
    assert.deepEqual(rw.verify([], { browser: false }), []);
    assert.deepEqual(firestoreRewriter({ url, required: false }).verify([]), []);
  });

  test("scanOutput: a built file holding Google's Firestore host, and the SDK from a CDN", () => {
    const rw = firestoreRewriter({ url });
    assert.match(rw.scanOutput([{ name: 'worker.js', code: 'const e="firestore.googleapis.com",t=!0;' }]).join(), /worker\.js holds a copy of the Firestore SDK/);
    assert.deepEqual(rw.scanOutput([{ name: 'a.js', code: 'const e="aruma.example.com",t=!0;' }]), []);
    assert.match(rw.scanOutput([{ name: 'i.html', code: '<script src="https://www.gstatic.com/firebasejs/13.1.0/firebase-firestore.js">' }]).join(), /i\.html loads/);
    for (const cdn of [
      'https://cdn.jsdelivr.net/npm/@firebase/firestore@4.19.0/dist/index.esm.js',
      'https://esm.sh/firebase@13.1.0/firestore',
      'https://esm.sh/firebase@13.1.0/firestore/lite',
      'https://unpkg.com/firebase@13.1.0/firebase-firestore-lite.js',
      'https://example.com/firebase-firestore-compat.min.js',
    ]) assert.ok(GSTATIC_FIREBASE.test(cdn), cdn);
    assert.throws(() => rw.resolveGstatic('https://www.gstatic.com/firebasejs/13.1.0/firebase-firestore.js'), /www\.gstatic\.com/);
  });

  test('the Storage rewriter does not touch Firestore, and the other way round', () => {
    assert.equal(storageRewriter({ url }).transform(BROWSER, ID), null);
    assert.equal(firestoreRewriter({ url }).transform(ESM_SNIPPET, '/a/node_modules/@firebase/storage/dist/index.esm.js'), null);
  });

  test('the unplugin factory: nothing without a URL, a rollup and a webpack hook with one', () => {
    for (const u of [undefined, '', '  ']) assert.deepEqual(Object.keys(firestoreFactory({ url: u })), ['name']);
    const f = firestoreFactory({ url }, { framework: 'rollup' });
    assert.equal(f.name, 'aruma-web:firestore');
    assert.equal(f.transform.handler(BROWSER, ID).code.includes('aruma.example.com'), true);
    assert.equal(typeof f.rollup.generateBundle, 'function');
    assert.equal(typeof f.webpack, 'function');
    assert.throws(() => firestoreFactory({ url: 'https://aruma.example.com/base' }), ArumaConfigError);
  });
});

describe('the Vite plugin', () => {
  test('firestore: true adds the Firestore plugin; Auth alone refuses a Firestore module', () => {
    const [auth, fs] = aruma({ url: 'https://aruma.example.com', firestore: true });
    assert.equal(fs.name, 'aruma-web:firestore');
    assert.equal(auth.transform('x', ID), null, 'enabled: the Auth plugin leaves it to the Firestore plugin');
    const alone = aruma({ url: 'https://aruma.example.com' });
    assert.throws(() => alone.transform('x', `${ID}?v=1`), (e) => e.message === firestoreOff(ID));
    assert.match(firestoreOff(ID), /firestore\.googleapis\.com carrying the Aruma ID token.*firestore: true/);
    assert.equal(alone.transform('x', '/app/src/main.js'), null);
    const both = aruma({ url: 'https://aruma.example.com', storage: true, firestore: { required: false } });
    assert.deepEqual(both.map((p) => p.name), ['aruma-web', 'aruma-web:storage', 'aruma-web:firestore']);
    assert.equal(both[0].transform('x', '/a/node_modules/@firebase/storage/dist/index.esm.js'), null);
  });

  test('config: SSR bundles the SDK, workers get a plugin of their own', () => {
    const p = firestorePlugin({ url: 'https://aruma.example.com' });
    const c = p.config();
    assert.deepEqual(c.ssr, { noExternal: ['@firebase/firestore', '@firebase/firestore-compat', 'firebase'] });
    const [w] = c.worker.plugins();
    assert.match(w.name, /^aruma-web:firestore/);
    assert.equal(w.config(), null);
  });
});

describe('Next.js', () => {
  test('the loader rule, the transpiled packages, and no external SDK; both products together', () => {
    assert.equal(withArumaFirestore({ a: 1 }, {}).a, 1);
    const c = withArumaFirestore({}, { url: 'https://aruma.example.com' });
    for (const pkg of FIRESTORE_PACKAGES) assert.ok(c.transpilePackages.includes(pkg), pkg);
    const rule = c.turbopack.rules['*.js'][0];
    assert.ok(rule.condition.path.test(ID));
    assert.equal(rule.loaders[0].options.product, 'firestore');
    assert.throws(() => withArumaFirestore({ serverExternalPackages: ['@firebase/firestore'] }, { url: 'https://aruma.example.com' }), /serverExternalPackages/);
    const both = withArumaFirestore(withArumaStorage({}, { url: 'https://aruma.example.com' }), { url: 'https://aruma.example.com' });
    assert.deepEqual(both.turbopack.rules['*.mjs'].map((r) => r.loaders[0].options.product), ['storage', 'firestore']);
  });

  test('the loader rewrites @firebase/firestore, passes a file without the host, and fails on an unknown release', async () => {
    const { createRequire } = await import('node:module');
    const loader = createRequire(import.meta.url)('../src/webpack-loader.cjs');
    const run = (resourcePath, src, options) => loader.call({ resourcePath, getOptions: () => ({ product: 'firestore', url: 'http://127.0.0.1:9420', ...options }) }, src);
    assert.match(run(ID, BROWSER), /const rn = "127\.0\.0\.1:9420", sn = false;/);
    assert.equal(run('/a/node_modules/@firebase/firestore/dist/index.esm.js', 'export * from "./c.js"'), 'export * from "./c.js"');
    assert.equal(run('/a/src/App.js', BROWSER), BROWSER);
    assert.throws(() => run(ID, 'const h = "firestore.googleapis.com";'), /not supported by the build-time rewrite/);
  });
});

describe('Metro', () => {
  test('both products on one config: one transformer, both rules, the serializer refuses an unrewritten module', () => {
    const upstreamDir = mkdtempSync(join(tmpdir(), 'aruma-metro-fs-'));
    try {
      const upstream = join(upstreamDir, 'upstream.cjs');
      writeFileSync(upstream, 'exports.transform = (a) => ({ src: a.src }); exports.getCacheKey = () => "up";');
      assert.equal(metroFirestore({ a: 1 }, {}).a, 1);
      const c1 = metroStorage({ transformer: { babelTransformerPath: upstream } }, { url: 'http://127.0.0.1:9420' });
      const c2 = metroFirestore(c1, { url: 'http://127.0.0.1:9421' });
      assert.deepEqual(JSON.parse(process.env.ARUMA_WEB_METRO), {
        url: 'http://127.0.0.1:9420',
        allowInsecure: false,
        upstream,
        firestore: { url: 'http://127.0.0.1:9421', allowInsecure: false },
      });
      assert.match(transform({ filename: ID, src: BROWSER }).src, /const rn = "127\.0\.0\.1:9421", sn = false;/);
      assert.match(transform({ filename: '/app/node_modules/@firebase/storage/dist/index.esm.js', src: ESM_SNIPPET }).src, /"127\.0\.0\.1:9420"/);
      assert.equal(transform({ filename: '/app/App.js', src: 'x' }).src, 'x');
      assert.equal(getCacheKey(), ['up', 'aruma-web-storage-1', 'http://127.0.0.1:9420', 'false', 'firestore', 'http://127.0.0.1:9421', 'false'].join('\0'));
      const mod = (path, code) => ({ path, output: [{ data: { code } }] });
      assert.equal(c2.serializer.processModuleFilter(mod(ID, 'const rn = "127.0.0.1:9421";')), true);
      assert.throws(() => c2.serializer.processModuleFilter(mod(ID, BROWSER)), /bundled without the Firestore rewrite.*withArumaFirestore last/);
    } finally {
      delete process.env.ARUMA_WEB_METRO;
      rmSync(upstreamDir, { recursive: true, force: true });
    }
  });
});

describe('flutter-web', () => {
  const INDEX = '<html><body>\n  <script src="flutter_bootstrap.js" async></script>\n</body></html>\n';
  // The CDN build of firebase-firestore.js 13.1.0: `const Or="firestore.googleapis.com",Lr=!0;`.
  const CDN = 'import{x}from"https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js";return new Y(r,s);const Or="firestore.googleapis.com",Lr=!0;class FirestoreSettingsImpl{}';
  function project() {
    const dir = mkdtempSync(join(tmpdir(), 'aruma-flutter-fs-'));
    mkdirSync(join(dir, 'web'));
    writeFileSync(join(dir, 'web', 'index.html'), INDEX);
    const pkg = join(dir, 'node_modules', 'firebase');
    mkdirSync(pkg, { recursive: true });
    writeFileSync(join(pkg, 'package.json'), JSON.stringify({ name: 'firebase', version: '13.1.0' }));
    writeFileSync(join(pkg, 'firebase-firestore.js'), CDN);
    writeFileSync(join(pkg, 'firebase-storage.js'), 'import{x}from"https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js";const l="firebasestorage.googleapis.com",c="storageBucket";this._protocol="https"');
    return dir;
  }

  test('Firestore and Storage: both files rewritten, both scripts ignored, both set before the bootstrap', () => {
    const dir = project();
    try {
      const r = flutterWeb({ url: 'http://127.0.0.1:9420', project: dir, version: '13.1.0', products: ['storage', 'firestore'] });
      assert.equal(r.files.length, 2);
      const fs = readFileSync(r.files[1], 'utf8');
      assert.match(fs, /^\/\* firebase-firestore\.js 13\.1\.0 .*its Firestore host is 127\.0\.0\.1:9420 over http/);
      assert.match(fs, /const Or="127\.0\.0\.1:9420",Lr=false;/);
      assert.match(fs, /from"https:\/\/www\.gstatic\.com\/firebasejs\/13\.1\.0\/firebase-app\.js"/);
      const html = readFileSync(r.index, 'utf8');
      assert.match(html, /flutterfire_ignore_scripts \?\? \[\]\), 'storage', 'firestore'\]/);
      assert.match(html, /window\.firebase_firestore = await import\('\.\/aruma\/firebase-firestore-13\.1\.0\.js'\);/);
      assert.match(html, /window\.firebase_storage = await import\('\.\/aruma\/firebase-storage-13\.1\.0\.js'\);/);
      // Firestore alone, again: the block is replaced.
      flutterWeb({ url: 'https://aruma.example.com', project: dir, version: '13.1.0', products: ['firestore'] });
      const again = readFileSync(r.index, 'utf8');
      assert.equal(again.split('<!-- @aruma/web flutter-web').length - 1, 1);
      assert.doesNotMatch(again, /firebase_storage/);
      assert.match(readFileSync(r.files[1], 'utf8'), /const Or="aruma\.example\.com",Lr=!0;/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  test('refuses an unknown product and a firebase-firestore.js it does not know', () => {
    const dir = project();
    try {
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com', project: dir, version: '13.1.0', products: ['database'] }), /products are/);
      writeFileSync(join(dir, 'node_modules', 'firebase', 'firebase-firestore.js'), 'const x = 1;');
      assert.throws(() => flutterWeb({ url: 'https://aruma.example.com', project: dir, version: '13.1.0', products: ['firestore'] }), /not supported by the build-time rewrite/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
