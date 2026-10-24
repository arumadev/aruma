// S4-B's and S4-C's scenarios with @google-cloud/storage 8.4.0 against a live Aruma, configured
// as @aruma/admin-storage will be (ADR-022 §14): apiEndpoint and universeDomain on Aruma's
// host, so the resumable start of save() and its chunks carry the Bearer too, and an auth client
// holding an access token Aruma issued. The acceptance test TestStorageGCSWithTheAdminSDKs runs
// it and checks the exchanges it recorded.
//
//   ARUMA_GCS_ORIGIN=http://127.0.0.1:<port> ACCESS_TOKEN=… PROJECT_ID=… BUCKET=… node scenario.mjs
//
// Prints one JSON line, { results: [{ op, ok, detail }] }, and exits 1 when a step failed.
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { Storage } = require('@google-cloud/storage');
const { OAuth2Client } = require('google-auth-library');

const { ARUMA_GCS_ORIGIN, ACCESS_TOKEN, PROJECT_ID, BUCKET } = process.env;
const host = new URL(ARUMA_GCS_ORIGIN).hostname;
const authClient = new OAuth2Client({ universeDomain: host });
authClient.setCredentials({ access_token: ACCESS_TOKEN, expiry_date: Date.now() + 3600 * 1000 });
const storage = new Storage({
  projectId: PROJECT_ID, apiEndpoint: ARUMA_GCS_ORIGIN, universeDomain: host, authClient, useAuthWithCustomEndpoint: true,
});
const bucket = storage.bucket(BUCKET);

const results = [];
async function step(op, fn) {
  try {
    results.push({ op, ok: true, detail: String((await fn()) ?? '') });
  } catch (e) {
    results.push({ op, ok: false, detail: `${e.code ?? ''} ${String(e.message ?? e).split('\n')[0]}`.trim().slice(0, 400) });
  }
}
// rejects runs fn and expects it to fail with an ApiError of the given code.
async function rejects(fn, code) {
  try {
    await fn();
  } catch (e) {
    if (e.code === code) return code;
    throw new Error(`failed with ${e.code} ${e.message}, want ${code}`);
  }
  throw new Error(`succeeded, want ${code}`);
}
async function absent(file) {
  const [exists] = await file.exists();
  if (exists) throw new Error(`${file.name} exists`);
}
function same(got, want, what) {
  if (!Buffer.from(got).equals(Buffer.from(want))) throw new Error(`${what}: ${got.length} bytes that differ from ${want.length}`);
}

const small = Buffer.from('hello aruma\n');
const chunkSize = 256 * 1024;
const odd = Buffer.alloc(2 * chunkSize + 1000);
for (let i = 0; i < odd.length; i++) odd[i] = i % 251;
const even = Buffer.alloc(2 * chunkSize, 'e');

// save() is resumable by default: one PUT with Content-Range: bytes 0-*/* and a chunked body,
// after which Node checks the crc32c of the reply (deleting the object when it differs).
await step('save', () => bucket.file('node/small.txt').save(small, { contentType: 'text/plain' }));
await step('save.empty', () => bucket.file('node/empty.txt').save(Buffer.alloc(0)));
await step('save.metadata', async () => {
  // contentType goes to X-Upload-Content-Type, the rest in the start's JSON.
  const f = bucket.file('node/meta.txt');
  await f.save(small, { metadata: { contentType: 'text/csv', cacheControl: 'no-cache', metadata: { k: 'v' } } });
  const [md] = await f.getMetadata();
  if (md.contentType !== 'text/csv' || md.cacheControl !== 'no-cache' || md.metadata?.k !== 'v') {
    throw new Error(`metadata ${JSON.stringify(md)}`);
  }
  return md.contentType;
});
// With chunkSize every chunk but the last is answered 308 with Range.
await step('save.chunked', () => bucket.file('node/odd.bin').save(odd, { chunkSize }));
await step('save.chunked.boundary',() => bucket.file('node/even.bin').save(even, { chunkSize }));
await step('save.multipart', async () => {
  const f = bucket.file('node/multipart.txt');
  await f.save(small, { resumable: false, metadata: { contentType: 'text/plain', metadata: { k: 'v' } } });
  const [md] = await f.getMetadata();
  return `${md.size} ${md.contentType}`;
});
await step('save.multipart.badCRC32C', async () => {
  const f = bucket.file('node/bad-multipart.txt');
  await rejects(() => f.save(small, { resumable: false, validation: false, metadata: { crc32c: 'AAAAAA==' } }), 400);
  await absent(f);
  return 400;
});
await step('save.badMD5', async () => {
  const f = bucket.file('node/bad-resumable.txt');
  await rejects(() => f.save(small, { validation: false, metadata: { md5Hash: Buffer.alloc(16).toString('base64') } }), 400);
  await absent(f);
  return 400;
});
await step('save.ifGenerationMatch0', async () => {
  const f = bucket.file('node/small.txt');
  await rejects(() => f.save(Buffer.from('again'), { resumable: false, preconditionOpts: { ifGenerationMatch: 0 } }), 412);
  await rejects(() => f.save(Buffer.from('again'), { preconditionOpts: { ifGenerationMatch: 0 } }), 412);
  const [data] = await f.download();
  same(data, small, 'after the 412s');
  return 412;
});

// download() checks the crc32c of what it read by default, against x-goog-hash, when
// x-goog-stored-content-encoding is identity.
await step('download', async () => {
  const [data] = await bucket.file('node/small.txt').download();
  same(data, small, 'small');
  const [big] = await bucket.file('node/odd.bin').download();
  same(big, odd, 'odd');
  const [empty] = await bucket.file('node/empty.txt').download();
  same(empty, Buffer.alloc(0), 'empty');
  return `${data.length} + ${big.length} bytes`;
});
await step('download.headers', () => new Promise((resolve, reject) => {
  bucket.file('node/odd.bin').createReadStream()
    .on('response', (res) => {
      const h = res.headers;
      const get = (k) => (typeof h.get === 'function' ? h.get(k) : h[k]);
      const hash = get('x-goog-hash');
      if (get('x-goog-stored-content-encoding') !== 'identity' || !/^crc32c=[^,]+,md5=[^,]+$/.test(hash ?? '')) {
        reject(new Error(`headers ${JSON.stringify(Object.fromEntries(Object.entries(h)))}`));
      }
    })
    .on('error', reject)
    .on('data', () => {})
    .on('end', () => resolve('validated'));
}));
await step('download.range', async () => {
  const [part] = await bucket.file('node/odd.bin').download({ start: 10, end: 29 });
  same(part, odd.subarray(10, 30), 'range');
  return part.length;
});

console.log(JSON.stringify({ results }));
process.exitCode = results.every((r) => r.ok) ? 0 : 1;
