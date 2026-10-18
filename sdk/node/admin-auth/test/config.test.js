// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import { emulatorGuard, findKey, resolveURL } from '../lib/config.js';
import { getAuth } from '../index.js';

const pem = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey.export({ format: 'pem', type: 'pkcs8' });
const key = {
  type: 'service_account', project_id: 'demo-app', client_email: 'backend@demo-app.sa.example.com',
  private_key: pem, private_key_id: 'abc', token_uri: 'https://aruma.example.com/oauth2/v1/token',
};

test('the emulator variable stops the library unless allowed outside production', () => {
  assert.doesNotThrow(() => emulatorGuard({}, {}));
  assert.throws(() => emulatorGuard({}, { FIREBASE_AUTH_EMULATOR_HOST: 'localhost:9099' }), { code: 'auth/argument-error' });
  assert.doesNotThrow(() => emulatorGuard({ allowEmulatorHost: true }, { FIREBASE_AUTH_EMULATOR_HOST: 'localhost:9099' }));
  assert.throws(() => emulatorGuard({ allowEmulatorHost: true }, { FIREBASE_AUTH_EMULATOR_HOST: 'x', NODE_ENV: 'production' }),
    /FIREBASE_AUTH_EMULATOR_HOST/);
});

test('getAuth refuses to start with the emulator variable set', () => {
  const app = { options: { projectId: 'demo-app' } };
  process.env.FIREBASE_AUTH_EMULATOR_HOST = 'localhost:9099';
  try {
    assert.throws(() => getAuth(app, { credential: key }), { code: 'auth/argument-error' });
  } finally {
    delete process.env.FIREBASE_AUTH_EMULATOR_HOST;
  }
  assert.equal(getAuth(app, { credential: key }).projectId, 'demo-app');
});

test('the Aruma URL comes from the option, ARUMA_URL, then token_uri', () => {
  const k = { tokenUri: key.token_uri };
  assert.equal(resolveURL({}, k, {}), 'https://aruma.example.com');
  assert.equal(resolveURL({}, k, { ARUMA_URL: 'https://env.example.com/' }), 'https://env.example.com');
  assert.equal(resolveURL({ url: 'https://opt.example.com/base/' }, k, { ARUMA_URL: 'https://env.example.com' }), 'https://opt.example.com/base');
  assert.equal(resolveURL({ url: 'http://127.0.0.1:8080' }, {}, {}), 'http://127.0.0.1:8080');
  for (const bad of ['http://aruma.example.com', 'https://identitytoolkit.googleapis.com', 'https://accounts.google.com',
    'https://aruma.example.com/?x=1', 'not a url']) {
    assert.throws(() => resolveURL({ url: bad }, {}, {}), { code: 'auth/argument-error' }, bad);
  }
  // A Google key file has a Google token_uri: refused, never followed.
  assert.throws(() => resolveURL({}, { tokenUri: 'https://oauth2.googleapis.com/token' }, {}), { code: 'auth/argument-error' });
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
