// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Host edge cases of the redirect, the guard and the build-time rewrite: ports, case, trailing dots,
// IPv6, punycode and full-width look-alikes, strings that only contain a host, minified code.
import { test, describe } from 'node:test';
import assert from 'node:assert/strict';
import { matchAuth, parseBase } from '../src/index.js';
import { matchStorage, storageBase, rewriteStorageLiterals, rewriteStorageURL } from '../src/storage.js';
import { firestoreBase, rewriteFirestoreLiterals, namesGoogleFirestore } from '../src/firestore.js';

const IDT = 'identitytoolkit.googleapis.com';

describe('matchAuth', () => {
  test('the spellings of the same host and port are one endpoint', () => {
    for (const u of [
      `https://${IDT}/v1/x`,
      `HTTPS://${IDT.toUpperCase()}/v1/x`,
      `https://${IDT}./v1/x`,
      `https://${IDT}:443/v1/x`,
      `https://${IDT}.:443/v1/x`,
      'https://identitytoolkit%E3%80%82googleapis.com/v1/x', // IDNA full stop
      'https://identitytoolkit。googleapis。com/v1/x',
      'https://ｉｄｅｎｔｉｔｙtoolkit.googleapis.com/v1/x', // full-width letters
    ]) assert.equal(matchAuth(u)?.host, IDT, u);
  });

  test('look-alikes and other shapes are not endpoints', () => {
    for (const u of [
      `https://${IDT}:8443/v1/x`,
      `http://${IDT}/v1/x`,
      `https://${IDT}.evil.example/v1/x`,
      `https://evil.example\\@${IDT}/v1/x`,
      `https://${IDT}@evil.example/v1/x`,
      `https://user@${IDT}/v1/x`,
      `//${IDT}/v1/x`,
      `/${IDT}/v1/x`,
      'https://[::ffff:1.2.3.4]/v1/x',
      '',
    ]) assert.equal(matchAuth(u), null, u);
  });

  test('dot segments are resolved before the path is kept', () => {
    assert.equal(matchAuth(`https://${IDT}/v1/../v2/x`).path, '/v2/x');
    assert.equal(matchAuth(`https://${IDT}/v1/%2e%2e/v2/x`).path, '/v2/x');
  });
});

describe('storageBase and firestoreBase', () => {
  test('hosts the SDK literal can carry', () => {
    const cases = {
      'https://aruma.example': 'aruma.example',
      'https://ARUMA.Example./': 'aruma.example.',
      'https://aruma.example:8443': 'aruma.example:8443',
      'https://aruma.example:443': 'aruma.example',
      'http://[::1]:8080': '[::1]:8080',
      'https://[2001:db8::1]': '[2001:db8::1]',
      'https://bücher.example': 'xn--bcher-kva.example', // punycode, as the SDK will dial it
      'http://127.1': '127.0.0.1',
      'http://localhost:9420': 'localhost:9420',
    };
    for (const [url, host] of Object.entries(cases)) {
      assert.equal(storageBase(url).host, host, url);
      assert.equal(firestoreBase(url).host, host, url);
    }
  });

  test('refused: Google hosts under any spelling, a path, characters that would leave the string literal', () => {
    for (const url of [
      'https://firebasestorage.googleapis.com',
      'https://FIREBASESTORAGE.GOOGLEAPIS.COM.',
      'https://x.googleapis.com',
      'https://googleapis.com',
      'https://aruma.example/path',
      'https://a_b.example',
      'https://aruma.example:99999',
      'https://u:p@aruma.example',
      'https://aruma.example?x=1',
      'https://aruma.example#f',
      'http://foo.localhost',
      'http://aruma.example',
    ]) {
      assert.throws(() => storageBase(url), (e) => e.name === 'ArumaConfigError', url);
      assert.throws(() => firestoreBase(url), (e) => e.name === 'ArumaConfigError', url);
    }
    assert.throws(() => parseBase('https://securetoken.googleapis.com.'), /Google Auth host/);
  });
});

describe('matchStorage and rewriteStorageURL', () => {
  test('the SDK host in its spellings', () => {
    for (const u of [
      'https://firebasestorage.googleapis.com/v0/b/b/o/x',
      'https://firebasestorage.googleapis.com./v0/b/b/o/x',
      'https://firebasestorage.googleapis.com:443/v0/b/b/o/x',
      'HTTPS://FireBaseStorage.GoogleAPIs.com/v0/b/b/o/x',
    ]) assert.deepEqual(matchStorage(u), { path: '/v0/b/b/o/x', search: '' }, u);
    for (const u of ['http://firebasestorage.googleapis.com/v0', 'https://firebasestorage.googleapis.com:8080/v0', 'https://firebasestorage.googleapis.com.evil.example/v0']) {
      assert.equal(matchStorage(u), null, u);
    }
  });

  test('the name and the token survive the rewrite byte for byte', () => {
    const u = 'https://firebasestorage.googleapis.com/v0/b/b/o/a%2Fb%20%2B%C3%A9.txt?alt=media&token=T-1_2';
    assert.equal(rewriteStorageURL(u, 'https://aruma.example'), 'https://aruma.example/v0/b/b/o/a%2Fb%20%2B%C3%A9.txt?alt=media&token=T-1_2');
  });
});

describe('rewriteStorageLiterals and rewriteFirestoreLiterals on minified code', () => {
  const base = storageBase('http://localhost:8080');

  test('a URL that merely contains the host is not the host literal', () => {
    const code = 'const a="https://firebasestorage.googleapis.com/v0",b="x";class S{constructor(){this._protocol="https";this.h="firebasestorage.googleapis.com"}}';
    const r = rewriteStorageLiterals(code, base);
    assert.ok(r.ok);
    assert.ok(r.code.includes('"https://firebasestorage.googleapis.com/v0"'), 'the URL string is untouched');
    assert.ok(r.code.includes('this.h="localhost:8080"'));
    assert.ok(r.code.includes('this._protocol="http"'));
  });

  test('a template literal or a second copy fails the check instead of shipping Google', () => {
    assert.equal(rewriteStorageLiterals('const a=`firebasestorage.googleapis.com`;this._protocol=`https`', base).ok, false);
    const one = 'const a="firebasestorage.googleapis.com";this._protocol="https";';
    assert.equal(rewriteStorageLiterals(one + one, base).ok, false);
  });

  test('replacement patterns in the host are not interpreted', () => {
    const r = rewriteStorageLiterals('var h="firebasestorage.googleapis.com";', { host: 'a$&b', protocol: 'https' });
    assert.equal(r.code, 'var h="a$&b";');
  });

  test('firestore: the gRPC descriptor host stays, the default host and ssl change', () => {
    const b = firestoreBase('http://127.0.0.1:9420');
    const code = 'var D={"(google.api.default_host)":"firestore.googleapis.com"};var rn="firestore.googleapis.com",sn=!0;';
    const r = rewriteFirestoreLiterals(code, b);
    assert.ok(r.ok, JSON.stringify(r));
    assert.ok(r.code.includes('"(google.api.default_host)":"firestore.googleapis.com"'));
    assert.ok(r.code.includes('rn="127.0.0.1:9420",sn=false'));
    assert.equal(namesGoogleFirestore(r.code), false);
  });
});
