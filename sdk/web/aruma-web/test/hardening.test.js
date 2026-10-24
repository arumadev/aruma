// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { ArumaConfigError } from '../src/index.js';
import { GSTATIC_FIREBASE, STORAGE_HOST, rewriteStorageLiterals, storageBase } from '../src/storage.js';
import { storageRewriter } from '../src/build.js';
import { ESM_SNIPPET } from './storage-fixtures.js';

describe('the host that goes into the SDK', () => {
  test('only a host name, an IP address and a port', () => {
    for (const [url, host] of [
      ['https://aruma.example.com', 'aruma.example.com'],
      ['https://ARUMA.example.com:8443', 'aruma.example.com:8443'],
      ['https://xn--bcher-kva.example', 'xn--bcher-kva.example'],
      ['http://127.0.0.1:9420', '127.0.0.1:9420'],
      ['http://[::1]:9420', '[::1]:9420'],
    ]) {
      assert.equal(storageBase(url).host, host, url);
    }
    for (const url of ['https://a"b.example.com', "https://a'b.example.com", 'https://a,b.example.com', 'https://a;b.example.com', 'https://a$b.example.com', 'https://a`b.example.com']) {
      assert.throws(() => storageBase(url), ArumaConfigError, url);
    }
  });

  test('the replacement is a JSON string literal', () => {
    const r = rewriteStorageLiterals(ESM_SNIPPET, storageBase('http://[::1]:9420'));
    assert.match(r.code, /const DEFAULT_HOST = "\[::1\]:9420";/);
    assert.match(r.code, /this\._protocol = "http";/);
  });
});

describe('CDN copies of the SDK', () => {
  test('gstatic, also protocol-relative, and the Storage SDK from any CDN', () => {
    for (const u of [
      'https://www.gstatic.com/firebasejs/13.1.0/firebase-app.js',
      '//www.gstatic.com/firebasejs/13.1.0/firebase-storage.js',
      'http://www.gstatic.com/firebasejs/9.0.0/firebase-storage-compat.js',
      'https://cdn.jsdelivr.net/npm/firebase@13.1.0/firebase-storage.js',
      'https://unpkg.com/@firebase/storage@0.14.6/dist/index.esm.js',
      'https://esm.sh/firebase@13/storage',
      'https://cdn.jsdelivr.net/npm/firebase@13/compat/storage/+esm',
      'https://cdn.example.com/firebase-storage.min.js',
    ]) {
      assert.equal(GSTATIC_FIREBASE.test(u), true, u);
    }
    for (const u of ['./firebase-storage.js', '/aruma/firebase-storage-13.1.0.js', 'firebase/storage', '@firebase/storage',
      'https://firebase.google.com/docs/storage/web/start', 'https://cdn.example.com/my/storage.js']) {
      assert.equal(GSTATIC_FIREBASE.test(u), false, u);
    }
  });

  test('the output check: a CDN load, and a copy of the SDK that still names Google', () => {
    const rw = storageRewriter({ url: 'https://aruma.example.com' });
    assert.match(rw.scanOutput([{ name: 'a.js', code: 'import("//cdn.jsdelivr.net/npm/firebase@13/firebase-storage.js")' }]).join(), /from a CDN/);
    const sdk = `${ESM_SNIPPET}h['X-Firebase-Storage-Version']='webjs/13';`;
    assert.match(rw.scanOutput([{ name: 'assets/worker-1.js', code: sdk }]).join(), /worker-1\.js holds a copy of the Storage SDK/);
    assert.deepEqual(rw.scanOutput([{ name: 'a.js', code: rewriteStorageLiterals(sdk, storageBase('https://aruma.example.com')).code }]), []);
    // @aruma/web/storage in the same chunk as the SDK is not taken for an unrewritten copy.
    assert.equal(STORAGE_HOST, 'firebasestorage.googleapis.com');
  });
});
