// T22: what firebase-admin does with an Aruma service account key, and the supported
// workaround. Needs a live Aruma server and one of its key files:
//
//   ARUMA_KEY_FILE=/path/service-account.json [ARUMA_URL=http://127.0.0.1:port] npm test
//
// The network guard lets loopback through (the Aruma server) and refuses everything else,
// recording the host, so nothing reaches Google.
import guard from '../netguard.cjs';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { initializeApp, deleteApp, cert } from 'firebase-admin/app';
import { arumaCredential } from './aruma-credential.mjs';

const keyFile = process.env.ARUMA_KEY_FILE;
if (!keyFile) throw new Error('set ARUMA_KEY_FILE to an Aruma service account key file');
const key = JSON.parse(readFileSync(keyFile, 'utf8'));
const arumaURL = process.env.ARUMA_URL ?? new URL(key.token_uri).origin;
const tokenHost = new URL(key.token_uri).host;

test('cert(keyFile).getAccessToken() ignores token_uri and goes to Google', async () => {
  guard.attempts.length = 0;
  const credential = cert(keyFile);
  await assert.rejects(credential.getAccessToken());
  console.log('cert() connection attempts:', guard.attempts);
  assert.ok(guard.attempts.includes('BLOCK oauth2.googleapis.com:443'), `attempts: ${guard.attempts}`);
  assert.ok(!guard.attempts.some((a) => a.includes(tokenHost)), 'cert() unexpectedly contacted the key file token_uri');
});

test('arumaCredential exchanges the key at token_uri and the token works on the Management API', async () => {
  guard.attempts.length = 0;
  const app = initializeApp({ credential: arumaCredential(keyFile), projectId: key.project_id }, 't22-aruma');
  try {
    const token = await app.options.credential.getAccessToken();
    assert.match(token.access_token, /^aruma_sat_/);
    assert.ok(token.expires_in > 3000 && token.expires_in <= 3600, `expires_in ${token.expires_in}`);

    const res = await fetch(`${arumaURL}/v1beta1/projects`, { headers: { authorization: `Bearer ${token.access_token}` } });
    const body = await res.json();
    console.log('GET /v1beta1/projects ->', res.status, JSON.stringify(body.results?.map((p) => p.projectId)));
    assert.equal(res.status, 200);
    assert.deepEqual(body.results.map((p) => p.projectId), [key.project_id]);
  } finally {
    await deleteApp(app);
  }
  assert.deepEqual(guard.attempts.filter((a) => a.startsWith('BLOCK')), [], 'arumaCredential contacted a non-loopback host');
});

test('arumaCredential surfaces token endpoint errors (assertion signed with the wrong key)', async () => {
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const forged = { ...key, private_key: privateKey.export({ type: 'pkcs8', format: 'pem' }) };
  await assert.rejects(arumaCredential(forged).getAccessToken(), /HTTP 400 \(invalid_grant/);
});
