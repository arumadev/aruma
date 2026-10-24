// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import { Storage } from '@google-cloud/storage';
import { emulatorGuard, findKey, resolveURL } from '../lib/config.js';
import { getStorage, getDownloadURL } from '../index.js';

// A throwaway key, made for the run: never a real one.
const pem = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey.export({ format: 'pem', type: 'pkcs8' });
const key = {
  type: 'service_account', project_id: 'demo-app', client_email: 'backend@demo-app.sa.example.com',
  private_key: pem, private_key_id: 'abc', token_uri: 'https://aruma.example.com/oauth2/v1/token',
};
const app = { options: { projectId: 'demo-app', storageBucket: 'demo-app.aruma.example.com' } };

test('either emulator variable stops the library, with no opt-in', () => {
  assert.doesNotThrow(() => emulatorGuard({}));
  for (const name of ['STORAGE_EMULATOR_HOST', 'FIREBASE_STORAGE_EMULATOR_HOST']) {
    assert.throws(() => emulatorGuard({ [name]: '127.0.0.1:9199' }), { code: 'storage/invalid-argument', message: new RegExp(name) });
    process.env[name] = '127.0.0.1:9199';
    try {
      assert.throws(() => getStorage(app, { credential: key }), { code: 'storage/invalid-argument' }, name);
    } finally {
      delete process.env[name];
    }
  }
  assert.ok(getStorage(app, { credential: key }) instanceof Storage);
});

test('the Aruma URL comes from the option, ARUMA_URL, then token_uri, as an origin', () => {
  const k = { tokenUri: 'https://Aruma.Example.com:443/oauth2/v1/token' };
  assert.equal(resolveURL({}, k, {}), 'https://aruma.example.com');
  assert.equal(resolveURL({}, k, { ARUMA_URL: 'https://env.example.com/' }), 'https://env.example.com');
  assert.equal(resolveURL({ url: 'https://opt.example.com:8443' }, k, { ARUMA_URL: 'https://env.example.com' }), 'https://opt.example.com:8443');
  assert.equal(resolveURL({ url: 'http://127.0.0.1:9350' }, {}, {}), 'http://127.0.0.1:9350');
  assert.equal(resolveURL({ url: 'http://localhost:80' }, {}, {}), 'http://localhost');
  for (const bad of ['http://aruma.example.com', 'https://storage.googleapis.com', 'https://firebasestorage.googleapis.com',
    'https://accounts.google.com', 'https://aruma.example.com/base', 'https://aruma.example.com/?x=1',
    'https://aruma.example.com/?', 'https://u:p@aruma.example.com', 'not a url']) {
    assert.throws(() => resolveURL({ url: bad }, {}, {}), { code: 'storage/invalid-argument' }, bad);
  }
  // A Google key file has a Google token_uri: refused, never followed.
  assert.throws(() => resolveURL({}, { tokenUri: 'https://oauth2.googleapis.com/token' }, {}), { code: 'storage/invalid-argument' });
  assert.throws(() => resolveURL({}, {}, {}), /ARUMA_URL/);
});

test('the key comes from the option, the cert() credential, then GOOGLE_APPLICATION_CREDENTIALS', () => {
  assert.equal(findKey(null, { credential: key }, {}).clientEmail, key.client_email);
  const fromCert = { options: { credential: { serviceAccountPathOrObject: key } } };
  assert.equal(findKey(fromCert, {}, {}).tokenUri, key.token_uri);
  const fields = { options: { credential: { clientEmail: 'a@b.c', privateKey: pem, projectId: 'p' } } };
  assert.equal(findKey(fields, {}, {}).projectId, 'p');
  assert.throws(() => findKey({ options: {} }, {}, {}), /No service account key/);
  assert.throws(() => findKey(null, {}, { GOOGLE_APPLICATION_CREDENTIALS: '/nonexistent/key.json' }), /Cannot read/);
});

test('getStorage is the official client on Aruma, with firebase-admin\'s bucket()', () => {
  const storage = getStorage(app, { credential: key });
  assert.equal(storage.url, 'https://aruma.example.com');
  assert.equal(storage.apiEndpoint, 'https://aruma.example.com');
  assert.equal(storage.universeDomain, 'aruma.example.com');
  assert.equal(storage.customEndpoint, true);
  assert.equal(storage.projectId, 'demo-app');
  assert.equal(storage.app, app);
  assert.equal(storage.bucket().name, 'demo-app.aruma.example.com');
  assert.equal(storage.bucket('other').name, 'other');
  const bare = getStorage({ options: {} }, { credential: key });
  assert.throws(() => bare.bucket(), { code: 'storage/invalid-argument' });
  // Shared per app without options, as firebase-admin's getStorage.
  const shared = { options: { credential: { serviceAccountPathOrObject: key } } };
  assert.equal(getStorage(shared), getStorage(shared));
});

test('getDownloadURL refuses a file of another client', async () => {
  const plain = new Storage({ apiEndpoint: 'https://aruma.example.com' }).bucket('b').file('f');
  await assert.rejects(getDownloadURL(plain), { code: 'storage/invalid-argument' });
});
