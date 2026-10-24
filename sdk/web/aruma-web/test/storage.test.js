// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { ArumaConfigError } from '../src/index.js';
import {
  installStorage,
  isStorageModule,
  matchStorage,
  rewriteStorageLiterals,
  rewriteStorageURL,
  storageBase,
  storageModuleRewritten,
  storageStatus,
  toArumaUrl,
  uninstallStorage,
  useArumaStorage,
} from '../src/storage.js';
import { CDN_SNIPPET, ESM_SNIPPET } from './storage-fixtures.js';

describe('storageBase', () => {
  test('the root of an https host, or of a loopback http host', () => {
    assert.equal(storageBase('https://Aruma.Example.com/').host, 'aruma.example.com');
    assert.equal(storageBase('http://127.0.0.1:9421').host, '127.0.0.1:9421');
    assert.equal(storageBase('https://aruma.example.com:8443').origin, 'https://aruma.example.com:8443');
  });

  test('refuses a path, http beyond loopback and Google hosts', () => {
    assert.throws(() => storageBase('https://aruma.example.com/base'), /root of its host/);
    assert.throws(() => storageBase('http://aruma.example.com'), ArumaConfigError);
    assert.throws(() => storageBase('https://firebasestorage.googleapis.com'), /Google host/);
    assert.throws(() => storageBase(''), ArumaConfigError);
    assert.equal(storageBase('http://aruma.lan', { allowInsecure: true }).protocol, 'http');
  });
});

describe('the build-time rule', () => {
  test('rewrites the definition of DEFAULT_HOST and leaves the comment', () => {
    const r = rewriteStorageLiterals(ESM_SNIPPET, storageBase('https://aruma.example.com'));
    assert.deepEqual([r.hosts, r.schemes, r.ok], [1, 0, true]);
    assert.match(r.code, /const DEFAULT_HOST = 'aruma\.example\.com';/);
    assert.match(r.code, /http\[s\]:\/\/firebasestorage\.googleapis\.com\//);
    assert.match(r.code, /this\._protocol = 'https';/);
  });

  test('on http also the default protocol, not the emulator one', () => {
    for (const code of [ESM_SNIPPET, CDN_SNIPPET]) {
      const r = rewriteStorageLiterals(code, storageBase('http://127.0.0.1:9421'));
      assert.deepEqual([r.hosts, r.schemes, r.ok], [1, 1, true]);
      assert.match(r.code, /["']127\.0\.0\.1:9421["']/);
      assert.match(r.code, /this\._protocol ?= ?["']http["']/);
      assert.match(r.code, /useSsl \? 'https' : 'http'|o\?"https":"http"/);
    }
  });

  test('a module it does not know is not ok', () => {
    const base = storageBase('http://127.0.0.1:9421');
    assert.equal(rewriteStorageLiterals('const x = 1;', base).ok, false);
    assert.equal(rewriteStorageLiterals(ESM_SNIPPET + ESM_SNIPPET, base).ok, false);
    // The host found but not the protocol: Aruma on http would get https requests.
    assert.equal(rewriteStorageLiterals("const DEFAULT_HOST = 'firebasestorage.googleapis.com';", base).ok, false);
    assert.equal(rewriteStorageLiterals("const DEFAULT_HOST = 'firebasestorage.googleapis.com';", storageBase('https://a.example')).ok, true);
  });

  test('which modules are @firebase/storage', () => {
    for (const id of [
      '/app/node_modules/@firebase/storage/dist/index.esm.js',
      '/app/node_modules/@firebase/storage/dist/node-esm/index.node.esm.js',
      '/app/node_modules/@firebase/storage/dist/index.node.cjs.js?commonjs-entry',
      'C:\\app\\node_modules\\@firebase\\storage\\dist\\index.cjs.js',
      '/app/node_modules/.pnpm/@firebase+storage@0.14.6/node_modules/@firebase/storage/dist/index.esm.js',
    ]) {
      assert.equal(isStorageModule(id), true, id);
    }
    for (const id of [
      '/app/node_modules/@firebase/storage/dist/index.esm.js.map',
      '/app/node_modules/@firebase/storage/dist/storage.d.ts',
      '/app/node_modules/@firebase/storage-compat/dist/index.esm.js',
      '/app/node_modules/firebase/storage/dist/esm/index.esm.js',
      '/app/node_modules/.vite/deps/firebase_storage.js',
    ]) {
      assert.equal(isStorageModule(id), false, id);
    }
  });

  test('a module in the bundle names Aruma, not Google', () => {
    const base = storageBase('https://aruma.example.com');
    assert.equal(storageModuleRewritten(rewriteStorageLiterals(ESM_SNIPPET, base).code, base), true);
    assert.equal(storageModuleRewritten(ESM_SNIPPET, base), false);
    assert.equal(storageModuleRewritten('', base), false);
  });
});

describe('links', () => {
  const base = 'https://aruma.example.com';
  const link = '/v0/b/p.aruma.example.com/o/a%2Fb.png?alt=media&token=t';

  test("Google's link (with :443 as iOS writes it) becomes the unified one", () => {
    for (const google of [`https://firebasestorage.googleapis.com${link}`, `https://FirebaseStorage.googleapis.com:443${link}`]) {
      assert.equal(rewriteStorageURL(google, base), `${base}${link}`);
      assert.equal(toArumaUrl(google, storageBase(base)), `${base}${link}`);
    }
  });

  test('anything else is not a Storage link', () => {
    for (const u of [`http://firebasestorage.googleapis.com${link}`, `https://firebasestorage.googleapis.com:8443${link}`,
      `https://storage.googleapis.com${link}`, `${base}${link}`, 'not a url', `https://u:p@firebasestorage.googleapis.com${link}`]) {
      assert.equal(matchStorage(u), null, u);
      assert.equal(toArumaUrl(u, storageBase(base)), u);
    }
  });
});

// A stand-in for the browser's XMLHttpRequest: records what was opened and sent.
function fakeGlobal() {
  const opened = [];
  const fetched = [];
  class XHR extends EventTarget {
    open(method, url) {
      opened.push(`${method} ${url}`);
    }
    send() {
      opened.push('sent');
    }
  }
  return {
    opened,
    fetched,
    g: {
      XMLHttpRequest: XHR,
      fetch: async (input) => {
        fetched.push(typeof input === 'string' ? input : input.url ?? String(input));
        return { ok: true };
      },
    },
  };
}

describe('installStorage (the fallback)', () => {
  const google = 'https://firebasestorage.googleapis.com/v0/b/p.aruma.test/o?name=x';

  test('not configured: nothing installed', () => {
    const { g } = fakeGlobal();
    const open = g.XMLHttpRequest.prototype.open;
    assert.equal(installStorage({ globalObject: g }), false);
    assert.equal(g.XMLHttpRequest.prototype.open, open);
    assert.equal(storageStatus({ globalObject: g }), null);
  });

  test('XHR and fetch to Google go to Aruma; everything else passes', async () => {
    const { g, opened, fetched } = fakeGlobal();
    assert.equal(installStorage({ url: 'http://127.0.0.1:9421', globalObject: g }), true);
    assert.equal(installStorage({ url: 'http://127.0.0.1:9422', globalObject: g }), true); // settings only
    const x = new g.XMLHttpRequest();
    x.open('POST', google, true);
    x.send();
    new g.XMLHttpRequest().open('GET', 'https://example.com/a');
    await g.fetch(google);
    await g.fetch(new URL('https://example.com/b'));
    assert.deepEqual(opened, ['POST http://127.0.0.1:9422/v0/b/p.aruma.test/o?name=x', 'sent', 'GET https://example.com/a']);
    assert.deepEqual(fetched, ['http://127.0.0.1:9422/v0/b/p.aruma.test/o?name=x', 'https://example.com/b']);
    assert.deepEqual(storageStatus({ globalObject: g }), { url: 'http://127.0.0.1:9422' });
    uninstallStorage({ globalObject: g });
    new g.XMLHttpRequest().open('GET', google);
    assert.equal(opened.at(-1), `GET ${google}`);
  });

  test('refused: Storage requests fail as network errors and never reach Google', async () => {
    const { g, opened, fetched } = fakeGlobal();
    assert.throws(() => installStorage({ url: 'http://aruma.example.com', globalObject: g }), ArumaConfigError);
    assert.ok(storageStatus({ globalObject: g }).blocked);
    const x = new g.XMLHttpRequest();
    const failed = new Promise((resolve) => x.addEventListener('error', resolve));
    x.open('GET', google, true);
    x.send();
    await failed;
    assert.deepEqual(opened, ['GET data:,']);
    await assert.rejects(g.fetch(google), /blocked/);
    assert.deepEqual(fetched, []);
  });
});

describe('useArumaStorage', () => {
  // The two fields of FirebaseStorageImpl it sets (host is a setter that recomputes the bucket).
  const fakeStorage = () => ({
    _host: 'firebasestorage.googleapis.com',
    _protocol: 'https',
    _isUsingEmulator: false,
    get host() {
      return this._host;
    },
    set host(h) {
      this._host = h;
      this.bucketRecomputed = true;
    },
  });

  test('points one instance at Aruma', () => {
    const s = useArumaStorage(fakeStorage(), { url: 'http://127.0.0.1:9421' });
    assert.deepEqual([s.host, s._protocol, s.bucketRecomputed], ['127.0.0.1:9421', 'http', true]);
    const t = useArumaStorage(fakeStorage(), { url: 'https://aruma.example.com' });
    assert.deepEqual([t.host, t._protocol], ['aruma.example.com', 'https']);
  });

  test("uses installStorage's URL, and refuses what it cannot point", () => {
    const { g } = fakeGlobal();
    assert.throws(() => useArumaStorage(fakeStorage(), { globalObject: g }), /needs the Aruma URL/);
    installStorage({ url: 'https://aruma.example.com', globalObject: g });
    assert.equal(useArumaStorage(fakeStorage(), { globalObject: g }).host, 'aruma.example.com');
    assert.throws(() => useArumaStorage({}, { url: 'https://aruma.example.com' }), ArumaConfigError);
    assert.throws(() => useArumaStorage({ ...fakeStorage(), _isUsingEmulator: true }, { url: 'https://a.example' }), /emulator/);
  });
});
