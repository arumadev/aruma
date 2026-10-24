// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The Aruma access token never leaves the Aruma origin. The resumable upload sends its chunks to
// whatever URL the start's Location names, and gaxios follows redirects with the headers it was
// given; the library refuses both. A fake Aruma answers with a hostile Location or redirect, and
// a second server on another port of the same host records what reaches it: nothing may.

import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import https from 'node:https';
import { execFileSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { getStorage } from '../index.js';

const pem = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey.export({ format: 'pem', type: 'pkcs8' });

// plan says how the fake Aruma answers the resumable start and its chunk.
const plan = { location: null, startRedirect: null, chunkRedirect: null };
const stolen = []; // what reached the other server
let aruma, other, tlsAruma, origin, otherOrigin, tlsOrigin, tlsDir;
let plaintextOnTLS = 0;

function reply(res, status, body, headers = {}) {
  res.writeHead(status, { 'content-type': 'application/json', ...headers });
  res.end(body === undefined ? '' : JSON.stringify(body));
}

// handler is the fake Aruma: the token endpoint, the resumable start, and its chunks.
function handler(self) {
  return async (req, res) => {
    for await (const _ of req); // drain
    const u = new URL(req.url, 'http://x');
    if (req.method === 'POST' && u.pathname === '/oauth2/v1/token') {
      return reply(res, 200, { access_token: 'aruma-token', expires_in: 3600 });
    }
    if (req.method === 'POST' && u.searchParams.get('uploadType') === 'resumable') {
      if (plan.startRedirect) return reply(res, 307, undefined, { location: plan.startRedirect });
      return reply(res, 200, undefined, { location: plan.location ?? `${self()}/upload/storage/v1/b/demo/o?uploadType=resumable&upload_id=s1` });
    }
    if (req.method === 'PUT' && u.searchParams.has('upload_id')) {
      if (plan.chunkRedirect) return reply(res, 302, undefined, { location: plan.chunkRedirect });
      return reply(res, 200, { bucket: 'demo', name: 'x', size: '5', generation: '1' });
    }
    reply(res, 404, { error: { code: 404, message: 'Not Found' } });
  };
}

const listen = (srv) => new Promise((resolve) => srv.listen(0, '127.0.0.1', () => resolve(srv.address().port)));

before(async () => {
  aruma = http.createServer(handler(() => origin));
  origin = `http://127.0.0.1:${await listen(aruma)}`;
  other = http.createServer(async (req, res) => {
    for await (const _ of req);
    stolen.push(`${req.method} ${req.url} auth=${req.headers.authorization ?? 'none'}`);
    reply(res, 200, { bucket: 'demo', name: 'x', size: '5', generation: '1' });
  });
  otherOrigin = `http://127.0.0.1:${await listen(other)}`;
  // An https Aruma, for the downgrade: a certificate made for the run with openssl.
  tlsDir = mkdtempSync(join(tmpdir(), 'aruma-admin-storage-tls-'));
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=127.0.0.1',
    '-addext', 'subjectAltName=IP:127.0.0.1', '-keyout', join(tlsDir, 'key.pem'), '-out', join(tlsDir, 'cert.pem')], { stdio: 'ignore' });
  const cert = readFileSync(join(tlsDir, 'cert.pem'));
  tlsAruma = https.createServer({ key: readFileSync(join(tlsDir, 'key.pem')), cert }, handler(() => tlsOrigin));
  tlsAruma.on('tlsClientError', () => { plaintextOnTLS++; });
  tlsOrigin = `https://127.0.0.1:${await listen(tlsAruma)}`;
  https.globalAgent.options.ca = cert; // node-fetch, under gaxios, connects through the global agent
});

after(async () => {
  for (const s of [aruma, other, tlsAruma]) await new Promise((resolve) => s.close(resolve));
  rmSync(tlsDir, { recursive: true, force: true });
});

function storageAt(url, fetchImpl) {
  const key = { client_email: 'backend@demo-app.sa.example.com', private_key: pem, token_uri: `${url}/oauth2/v1/token` };
  return getStorage({ options: { storageBucket: 'demo' } }, { credential: key, url, ...(fetchImpl ? { fetch: fetchImpl } : {}) });
}

// tlsFetch is the token exchange's fetch for the https Aruma, trusting the run's certificate.
async function tlsFetch(url, init) {
  const body = await new Promise((resolve, reject) => {
    const req = https.request(url, { method: init.method, headers: init.headers, ca: https.globalAgent.options.ca }, (res) => {
      let data = '';
      res.on('data', (c) => { data += c; }).on('end', () => resolve({ status: res.statusCode, data }));
    });
    req.on('error', reject);
    req.end(String(init.body));
  });
  return { ok: body.status === 200, status: body.status, json: async () => JSON.parse(body.data) };
}

function setPlan(what = {}) {
  plan.location = what.location ?? null;
  plan.startRedirect = what.startRedirect ?? null;
  plan.chunkRedirect = what.chunkRedirect ?? null;
}

async function refused(storage, what) {
  setPlan(what);
  const before = stolen.length;
  await assert.rejects(storage.bucket().file('x').save(Buffer.from('hello'), { validation: false, retryOptions: { autoRetry: false } }),
    what.error);
  assert.deepEqual(stolen.slice(before), [], 'a request reached the other origin');
}

test('a Location on another origin is refused before anything is sent there', async () => {
  await refused(storageAt(origin), {
    location: `${otherOrigin}/upload/storage/v1/b/demo/o?uploadType=resumable&upload_id=s1`, error: /Refusing to send the Aruma access token/,
  });
  // Another host altogether, never dialled.
  await refused(storageAt(origin), {
    location: 'https://storage.googleapis.com/upload/storage/v1/b/demo/o?uploadType=resumable&upload_id=s1', error: /Refusing/,
  });
});

test('a redirect, to another port of the same host, is not followed', async () => {
  // The start has a JSON body, which node-fetch would follow a 307 with, headers and all.
  await refused(storageAt(origin), { startRedirect: `${otherOrigin}/stolen`, error: /maximum redirect/i });
  await refused(storageAt(origin), { chunkRedirect: `${otherOrigin}/stolen`, error: /redirect/i });
});

test('an https Aruma refuses a Location that downgrades to http', async () => {
  setPlan();
  const storage = storageAt(tlsOrigin, tlsFetch);
  // First, the plan the server would follow works over https.
  await storage.bucket().file('x').save(Buffer.from('hello'), { validation: false });
  const plain = new URL(tlsOrigin);
  plain.protocol = 'http:';
  await refused(storage, { location: `${plain.origin}/upload/storage/v1/b/demo/o?uploadType=resumable&upload_id=s1`, error: /Refusing/ });
  await refused(storage, { location: `${otherOrigin}/upload/storage/v1/b/demo/o?uploadType=resumable&upload_id=s1`, error: /Refusing/ });
  assert.equal(plaintextOnTLS, 0, 'a plain http request reached the https server');
});

test('signed URLs are refused before anything asks for a signer', async () => {
  const file = storageAt(origin).bucket().file('x');
  const before = stolen.length;
  const expires = Date.now() + 3600_000;
  await assert.rejects(file.getSignedUrl({ action: 'read', expires }), { message: /Signed URLs are not supported/ });
  await assert.rejects(file.getSignedUrl({ action: 'write', expires, version: 'v4' }), { message: /Signed URLs are not supported/ });
  await assert.rejects(file.generateSignedPostPolicyV4({ expires }), { message: /Signed URLs are not supported/ });
  assert.deepEqual(stolen.slice(before), []);
});

test('requests on the Aruma origin still go through, with Bearer', async () => {
  setPlan();
  await storageAt(origin).bucket().file('x').save(Buffer.from('hello'), { validation: false });
});
