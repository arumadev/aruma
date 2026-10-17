import '../netguard.mjs';
import { attempts } from '../netguard.mjs';
import { test, after } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { initializeApp, cert, deleteApp } from 'firebase-admin/app';

const dir = process.env.ARUMA_SMOKE_DIR ?? new URL('../.generated/', import.meta.url).pathname;
const keyPath = `${dir}/service-account.json`;
const expected = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8'));
const admin = JSON.parse(readFileSync(`${dir}/firebase-config.json`, 'utf8'));

test('firebase-admin accepts the Aruma key file with cert()', async () => {
  const credential = cert(keyPath); // parses and validates project_id, client_email, private_key
  const app = initializeApp({
    credential,
    projectId: expected.projectId,
    databaseURL: admin.databaseURL,
    storageBucket: admin.storageBucket,
  }, 'aruma');
  assert.equal(app.options.projectId, expected.projectId);
  assert.equal(app.options.storageBucket, expected.storageBucket);
  await deleteApp(app);
});

test('the key file parses as a service account credential', () => {
  const key = JSON.parse(readFileSync(keyPath, 'utf8'));
  assert.equal(key.type, 'service_account');
  assert.equal(key.client_email, expected.serviceAccountEmail);
  assert.equal(key.token_uri, expected.tokenUri);
  cert(key); // the object form is accepted as well
});

after(() => assert.deepEqual(attempts, [], `network access attempted: ${attempts.join(', ')}`));
