// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The library against a fake Aruma on loopback that records the Authorization of every request:
// the token exchange, the resumable upload's start and its chunks, and the Firebase metadata
// getDownloadURL reads. The live server is the smoke test's (test/smoke/admin-node).

import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { generateKeyPairSync } from 'node:crypto';
import { getStorage, getDownloadURL } from '../index.js';

const pem = generateKeyPairSync('rsa', { modulusLength: 2048 }).privateKey.export({ format: 'pem', type: 'pkcs8' });
const seen = [];
let server, origin, exchanges = 0;

function reply(res, status, body, headers = {}) {
  res.writeHead(status, { 'content-type': 'application/json', ...headers });
  res.end(body === undefined ? '' : JSON.stringify(body));
}

before(async () => {
  const sessions = new Map();
  server = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    const body = Buffer.concat(chunks);
    const u = new URL(req.url, origin);
    seen.push({ method: req.method, path: u.pathname, query: u.searchParams, auth: req.headers.authorization ?? '',
      range: req.headers['content-range'] ?? '' });
    if (req.method === 'POST' && u.pathname === '/oauth2/v1/token') {
      exchanges++;
      return reply(res, 200, { access_token: 'aruma-token', expires_in: 3600, token_type: 'Bearer' });
    }
    if (req.headers.authorization !== 'Bearer aruma-token') {
      return reply(res, 401, { error: { code: 401, message: 'Anonymous caller.' } });
    }
    const name = u.searchParams.get('name');
    if (req.method === 'POST' && u.searchParams.get('uploadType') === 'resumable' && !u.searchParams.has('upload_id')) {
      const id = `s${sessions.size + 1}`;
      sessions.set(id, { name, bytes: 0 });
      return reply(res, 200, undefined, { location: `${origin}/upload/storage/v1/b/demo/o?uploadType=resumable&upload_id=${id}` });
    }
    const session = sessions.get(u.searchParams.get('upload_id'));
    if (req.method === 'PUT' && session) {
      const [, , end, total] = /^bytes (\d+|\*)-?(\d*)\/(\d+|\*)$/.exec(req.headers['content-range']) ?? [];
      session.bytes += body.length;
      if (total === '*' && end !== '') {
        return reply(res, 308, undefined, { range: `bytes=0-${session.bytes - 1}` });
      }
      return reply(res, 200, { kind: 'storage#object', bucket: 'demo', name: session.name, size: String(session.bytes), generation: '1' });
    }
    const md = /^\/v0\/b\/demo\/o\/(.+)$/.exec(u.pathname);
    if (req.method === 'GET' && md) {
      const object = decodeURIComponent(md[1]);
      if (object === 'bare.txt') return reply(res, 200, { name: object });
      if (object === 'missing') return reply(res, 404, { error: { code: 404, message: 'Not Found.' } });
      return reply(res, 200, { name: object, downloadTokens: 'tok-1,tok-2' });
    }
    reply(res, 404, { error: { code: 404, message: `${req.method} ${u.pathname}` } });
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
});

after(() => new Promise((resolve) => server.close(resolve)));

function keyFile(universe) {
  return {
    type: 'service_account', project_id: 'demo-app', private_key_id: 'k1', private_key: pem,
    client_email: 'backend@demo-app.sa.example.com', token_uri: `${origin}/oauth2/v1/token`,
    ...(universe ? { universe_domain: universe } : {}),
  };
}

// universe_domain in the key changes nothing: the library takes its token from token_uri and
// sets the universe to the Aruma host itself.
for (const universe of [undefined, 'googleapis.com', '127.0.0.1']) {
  test(`save() starts its resumable session and sends its chunks with Bearer (universe_domain ${universe ?? 'absent'})`, async () => {
    const storage = getStorage({ options: { storageBucket: 'demo' } }, { credential: keyFile(universe) });
    const from = seen.length;
    await storage.bucket().file('one.bin').save(Buffer.alloc(1000, 1), { validation: false });
    await storage.bucket().file('chunked.bin').save(Buffer.alloc(2 * 256 * 1024 + 7, 2), { chunkSize: 256 * 1024, validation: false });
    const mine = seen.slice(from).filter((x) => x.path !== '/oauth2/v1/token');
    const starts = mine.filter((x) => x.method === 'POST' && x.query.get('uploadType') === 'resumable');
    const chunks = mine.filter((x) => x.method === 'PUT' && x.query.has('upload_id'));
    assert.equal(starts.length, 2, JSON.stringify(mine));
    assert.deepEqual(chunks.map((x) => x.range), ['bytes 0-*/*', 'bytes 0-262143/*', 'bytes 262144-524287/*', 'bytes 524288-524294/524295']);
    assert.deepEqual(mine.filter((x) => x.auth !== 'Bearer aruma-token').map((x) => `${x.method} ${x.path}`), []);
  });
}

test('one token exchange per instance', () => {
  assert.equal(exchanges, 3);
});

test('getDownloadURL reads the metadata from Aruma and returns the unified URL', async () => {
  const storage = getStorage({ options: { storageBucket: 'demo' } }, { credential: keyFile() });
  const from = seen.length;
  assert.equal(await getDownloadURL(storage.bucket().file('photos/a b+c.png')),
    `${origin}/v0/b/demo/o/photos%2Fa%20b%2Bc.png?alt=media&token=tok-1`);
  assert.deepEqual(seen.slice(from).filter((x) => x.path.startsWith('/v0/')).map((x) => x.auth), ['Bearer aruma-token']);
  await assert.rejects(getDownloadURL(storage.bucket().file('bare.txt')), { code: 'storage/no-download-token' });
  await assert.rejects(getDownloadURL(storage.bucket().file('missing')), { code: 404 });
});
