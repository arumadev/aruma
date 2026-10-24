// @aruma/admin-storage against a live Aruma with Cloud Storage (ADR-022 §14), the one line a
// server changes: getStorage and getDownloadURL from @aruma/admin-storage instead of
// firebase-admin/storage, next to firebase-admin initialised with the key file Aruma issued.
// Uploads of each kind, downloads with the CRC check, getDownloadURL, copies, and the Bearer on
// every request the client makes, the resumable session's start and chunks included, measured
// again with key files that carry universe_domain (docs/research/storage-production-path.md).
//
// Run it through storage-run.mjs (npm run test:storage): it starts the server, in the loopback
// and the TLS form, and preloads the network guard, which refuses every connection beyond
// loopback.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import https from 'node:https';
import { readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { initializeApp, cert, deleteApp } from 'firebase-admin/app';
import { Storage } from '@google-cloud/storage';
import { OAuth2Client } from 'google-auth-library';
import { getStorage, getDownloadURL } from '@aruma/admin-storage';
import { arumaCredential } from '../../experiments/admin-node/aruma-credential.mjs';

const require = createRequire(import.meta.url);
const netguard = require.resolve('../../experiments/netguard.cjs');
assert.ok(require.cache[netguard], 'run this file through storage-run.mjs (npm run test:storage): the netguard is not loaded');
const { attempts } = require(netguard);

const dir = process.env.ARUMA_SMOKE_DIR;
assert.ok(dir, 'ARUMA_SMOKE_DIR is not set');
const mode = process.env.ARUMA_SMOKE_MODE ?? 'loopback';
const expected = JSON.parse(readFileSync(`${dir}/expected.json`, 'utf8'));
const keyPath = `${dir}/service-account.json`;
const origin = expected.serverUrl;
if (mode === 'tls') {
  assert.equal(origin, 'https://storage.aruma.test');
} else {
  assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
}

// exchanges records every request the Google libraries send, with its Authorization: gaxios
// sends through node-fetch, which calls http(s).request for each one (the token exchange and
// the anonymous download below use the built-in fetch, and are not recorded).
const exchanges = [];
for (const [scheme, mod] of [['http', http], ['https', https]]) {
  const original = mod.request;
  mod.request = function request(...args) {
    const url = args.find((a) => typeof a === 'string' || a instanceof URL);
    const opts = args.find((a) => a && typeof a === 'object' && !(a instanceof URL)) ?? {};
    const u = url ? new URL(String(url)) : new URL(`${scheme}://${opts.hostname ?? opts.host}${opts.port ? `:${opts.port}` : ''}${opts.path ?? '/'}`);
    const auth = Object.entries(opts.headers ?? {}).find(([k]) => k.toLowerCase() === 'authorization')?.[1];
    exchanges.push({ method: opts.method ?? 'GET', url: u, auth: auth ? String(auth) : '' });
    return original.apply(this, args);
  };
}
const since = (from) => exchanges.slice(from);
const show = (xs) => xs.map((x) => `${x.method} ${x.url.pathname}${x.url.search} ${x.auth.split(' ')[0] || 'none'}`).join('\n');
const isStart = (x) => x.method === 'POST' && x.url.searchParams.get('uploadType') === 'resumable' && !x.url.searchParams.has('upload_id');
const isChunk = (x) => x.method === 'PUT' && x.url.searchParams.has('upload_id');

// assertBearer checks that requests went out, all to the Aruma origin with a Bearer, and that
// they include a resumable start and its chunks when resumable is set.
function assertBearer(xs, { resumable = true } = {}) {
  assert.ok(xs.length > 0, 'no request recorded');
  if (resumable) {
    assert.ok(xs.some(isStart), `no resumable start among\n${show(xs)}`);
    assert.ok(xs.some(isChunk), `no resumable chunk among\n${show(xs)}`);
  }
  assert.deepEqual(xs.filter((x) => x.url.origin !== origin), [], `requests beyond ${origin}`);
  assert.deepEqual(xs.filter((x) => !/^Bearer \S+$/.test(x.auth)).map((x) => `${x.method} ${x.url.pathname}${x.url.search}`), [],
    `requests without Bearer among\n${show(xs)}`);
}

const chunkSize = 256 * 1024;
const pattern = (n) => Buffer.from(Array.from({ length: n }, (_, i) => i % 251));
const small = Buffer.from('hello aruma\n');
const big = pattern(2 * chunkSize + 1000);

let app, storage, bucket;

before(() => {
  // The developer's existing setup: firebase-admin with the key file Aruma issued.
  app = initializeApp({ credential: cert(keyPath), projectId: expected.projectId, storageBucket: expected.storageBucket }, 'storage');
  storage = getStorage(app); // the one changed line
  bucket = storage.bucket();
});

after(async () => {
  await deleteApp(app);
  const off = attempts.filter((a) => !a.startsWith('ALLOW '));
  assert.deepEqual(off, [], `network access beyond loopback attempted: ${off.join(', ')}`);
  assert.deepEqual(exchanges.filter((x) => x.url.origin !== origin).map((x) => x.url.href), [], 'requests beyond Aruma');
});

test('getStorage reads the server from the key file and defaults to the app\'s bucket', () => {
  assert.ok(storage instanceof Storage);
  assert.equal(storage.url, origin);
  assert.equal(storage.apiEndpoint, origin);
  assert.equal(bucket.name, expected.storageBucket);
  assert.equal(getStorage(app), storage);
});

test('both emulator variables stop getStorage', () => {
  for (const name of ['STORAGE_EMULATOR_HOST', 'FIREBASE_STORAGE_EMULATOR_HOST']) {
    process.env[name] = '127.0.0.1:9199';
    try {
      assert.throws(() => getStorage(app, { credential: keyPath }), { code: 'storage/invalid-argument', message: new RegExp(name) });
    } finally {
      delete process.env[name];
    }
  }
});

test('uploads of each kind carry Bearer, the resumable start and chunks included', async () => {
  const from = exchanges.length;
  // save() is resumable by default: the start, then one PUT of bytes 0-*/*.
  await bucket.file('smoke/small.txt').save(small, { contentType: 'text/plain', metadata: { metadata: { k: 'v' } } });
  // With chunkSize, a PUT per chunk, answered 308 until the last.
  await bucket.file('smoke/big.bin').save(big, { chunkSize });
  // resumable:false is one multipart request.
  await bucket.file('smoke/multipart.txt').save(small, { resumable: false, contentType: 'text/csv' });
  // A stream, as the documentation's createWriteStream examples write.
  await pipeline(Readable.from([big.subarray(0, 300_000), big.subarray(300_000)]),
    bucket.file('smoke/stream.bin').createWriteStream({ contentType: 'application/octet-stream' }));
  // bucket.upload() from a local file.
  writeFileSync(`${dir}/local.bin`, big);
  await bucket.upload(`${dir}/local.bin`, { destination: 'smoke/uploaded.bin' });
  const xs = since(from);
  assertBearer(xs);
  assert.ok(xs.some((x) => x.method === 'POST' && x.url.searchParams.get('uploadType') === 'multipart'), show(xs));
  assert.ok(xs.filter(isChunk).length >= 5, `chunks:\n${show(xs.filter(isChunk))}`);

  const [md] = await bucket.file('smoke/small.txt').getMetadata();
  assert.equal(md.size, String(small.length));
  assert.equal(md.contentType, 'text/plain');
  assert.equal(md.metadata.k, 'v');
  assert.equal((await bucket.file('smoke/multipart.txt').getMetadata())[0].contentType, 'text/csv');
  for (const name of ['smoke/big.bin', 'smoke/stream.bin', 'smoke/uploaded.bin']) {
    assert.equal((await bucket.file(name).getMetadata())[0].size, String(big.length), name);
  }
});

test('downloads pass the CRC check', async () => {
  const from = exchanges.length;
  // download() checks the crc32c of what it read against x-goog-hash by default.
  for (const [name, want] of [['smoke/small.txt', small], ['smoke/big.bin', big], ['smoke/stream.bin', big], ['smoke/uploaded.bin', big]]) {
    const [data] = await bucket.file(name).download();
    assert.ok(data.equals(want), `${name}: ${data.length} bytes that differ`);
  }
  const [md5Checked] = await bucket.file('smoke/big.bin').download({ validation: 'md5' });
  assert.ok(md5Checked.equals(big));
  const [part] = await bucket.file('smoke/big.bin').download({ start: 10, end: 29 });
  assert.ok(part.equals(big.subarray(10, 30)));
  assertBearer(since(from), { resumable: false });
});

test('getDownloadURL returns the unified link, which serves the bytes', async () => {
  await bucket.file('smoke/صور/a b+c.bin').save(big);
  for (const [name, want] of [['smoke/small.txt', small], ['smoke/صور/a b+c.bin', big]]) {
    const from = exchanges.length;
    const url = await getDownloadURL(bucket.file(name));
    const re = new RegExp(`^${origin.replace(/\./g, '\\.')}/v0/b/${bucket.name.replace(/\./g, '\\.')}/o/` +
      `${encodeURIComponent(name).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\?alt=media&token=[0-9a-f-]{36}$`);
    assert.match(url, re);
    assert.ok(!url.includes(':443'), url);
    assertBearer(since(from), { resumable: false });
    // Anyone with the link reads the file, with no credential.
    const res = await fetch(url);
    assert.equal(res.status, 200);
    assert.ok(Buffer.from(await res.arrayBuffer()).equals(want), `${name} through its download URL`);
    assert.equal(await getDownloadURL(bucket.file(name)), url, 'the same token the second time');
  }
});

test('copy, list, exists and delete', async () => {
  const from = exchanges.length;
  await bucket.file('smoke/big.bin').copy(bucket.file('copies/big.bin'));
  const [copied] = await bucket.file('copies/big.bin').download();
  assert.ok(copied.equals(big));
  const [files] = await bucket.getFiles({ prefix: 'smoke/' });
  assert.deepEqual(files.map((f) => f.name).sort(),
    ['smoke/big.bin', 'smoke/multipart.txt', 'smoke/small.txt', 'smoke/stream.bin', 'smoke/uploaded.bin', 'smoke/صور/a b+c.bin']);
  assert.deepEqual(await bucket.file('copies/big.bin').exists(), [true]);
  await bucket.file('copies/big.bin').delete();
  assert.deepEqual(await bucket.file('copies/big.bin').exists(), [false]);
  await assert.rejects(bucket.file('copies/big.bin').download(), { code: 404 });
  assertBearer(since(from), { resumable: false });
});

// P measured universeDomain on a host equal to the universe with a key file without
// universe_domain, as Aruma issues it. The key files below carry the field: Google's default,
// and the Aruma host. The library takes its token from token_uri all the same.
for (const universe of ['googleapis.com', new URL(origin).hostname]) {
  test(`a key file with universe_domain ${universe}: Bearer on save()'s start and chunks`, async () => {
    const key = { ...JSON.parse(readFileSync(keyPath, 'utf8')), universe_domain: universe };
    const path = `${dir}/service-account-${universe}.json`;
    writeFileSync(path, JSON.stringify(key));
    const other = getStorage(app, { credential: path }).bucket();
    const from = exchanges.length;
    await other.file(`universe/${universe}/small.txt`).save(small);
    await other.file(`universe/${universe}/big.bin`).save(big, { chunkSize });
    const [data] = await other.file(`universe/${universe}/big.bin`).download();
    assert.ok(data.equals(big));
    assertBearer(since(from));
  });
}

// The control: the same client without universeDomain, as P found it. The resumable start
// goes without Authorization, and Aruma refuses it; the other requests carry Bearer.
test('without universeDomain the resumable start has no Bearer, and Aruma refuses it', async () => {
  const cred = arumaCredential(keyPath);
  const authClient = new OAuth2Client();
  authClient.refreshHandler = async () => {
    const t = await cred.getAccessToken();
    return { access_token: t.access_token, expiry_date: Date.now() + t.expires_in * 1000 };
  };
  const plain = new Storage({ projectId: expected.projectId, apiEndpoint: origin, authClient, useAuthWithCustomEndpoint: true });
  const b = plain.bucket(bucket.name);
  const from = exchanges.length;
  await b.file('control/multipart.txt').save(small, { resumable: false });
  await assert.rejects(b.file('control/resumable.txt').save(small), (err) => {
    assert.equal(err.code, 401, err.message);
    return true;
  });
  const xs = since(from);
  assert.deepEqual(xs.filter(isStart).map((x) => x.auth), [''], show(xs));
  assert.ok(xs.filter((x) => !isStart(x)).every((x) => x.auth.startsWith('Bearer ')), show(xs));
  assert.deepEqual(await bucket.file('control/resumable.txt').exists(), [false]);
});
