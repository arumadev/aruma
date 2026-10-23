// S0-B: one Admin SDK configuration against the recording stub, in a process of its own (the
// Storage clients read STORAGE_EMULATOR_HOST when they are built, and firebase-admin writes it).
//
//   node scenario.mjs <scenario>    (run.sh sets KEY_FILE, BUCKET, STUB_HTTP, STUB_HTTPS)
//
// Prints one JSON line: { scenario, results: [{ op, ok, detail }] }. The network guard
// (../../netguard.cjs, preloaded by run.sh) refuses every connection that leaves loopback.
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { arumaCredential } from '../../admin-node/aruma-credential.mjs';

const require = createRequire(import.meta.url);
const scenario = process.argv[2];
const { KEY_FILE, BUCKET, STUB_HTTP, STUB_HTTPS } = process.env;
const key = JSON.parse(readFileSync(KEY_FILE, 'utf8'));
const results = [];

async function step(op, fn) {
  try {
    const detail = await fn();
    results.push({ op, ok: true, detail: detail ?? '' });
  } catch (e) {
    results.push({ op, ok: false, detail: `${e.code ?? ''} ${String(e.message ?? e).split('\n')[0]}`.trim().slice(0, 240) });
  }
}

// arumaAuthClient: an OAuth2Client whose tokens come from Aruma's token_uri (arumaCredential).
function arumaAuthClient() {
  const { OAuth2Client } = require('google-auth-library');
  const cred = arumaCredential(key);
  const client = new OAuth2Client();
  client.refreshHandler = async () => {
    const t = await cred.getAccessToken();
    return { access_token: t.access_token, expiry_date: Date.now() + t.expires_in * 1000 };
  };
  return client;
}

// bucketFor builds the Storage bucket handle the scenario names, and whether firebase-admin's
// getDownloadURL applies.
async function bucketFor() {
  const { Storage } = require('@google-cloud/storage');
  const gcs = (opts) => new Storage({ projectId: key.project_id, ...opts }).bucket(BUCKET);
  switch (scenario) {
    case 'admin-cert-default':
    case 'admin-cert-emulator-http':
    case 'admin-cert-emulator-https':
    case 'admin-firebase-emulator-host':
    case 'admin-aruma-credential': {
      const { initializeApp, cert } = await import('firebase-admin/app');
      const { getStorage } = await import('firebase-admin/storage');
      const credential = scenario === 'admin-aruma-credential' ? arumaCredential(key) : cert(key);
      const app = initializeApp({ credential, projectId: key.project_id, storageBucket: BUCKET });
      return { bucket: getStorage(app).bucket(), admin: true };
    }
    case 'gcs-endpoint-noauth':
      return { bucket: gcs({ apiEndpoint: STUB_HTTPS, keyFilename: KEY_FILE }) };
    case 'gcs-endpoint-auth-keyfile':
      return { bucket: gcs({ apiEndpoint: STUB_HTTPS, keyFilename: KEY_FILE, useAuthWithCustomEndpoint: true }) };
    case 'gcs-endpoint-auth-aruma':
      return { bucket: gcs({ apiEndpoint: STUB_HTTPS, authClient: arumaAuthClient(), useAuthWithCustomEndpoint: true }) };
    case 'gcs-endpoint-prefix-aruma':
      return {
        bucket: gcs({ apiEndpoint: `${STUB_HTTPS}/storage.googleapis.com`, authClient: arumaAuthClient(), useAuthWithCustomEndpoint: true }),
      };
    default:
      throw new Error(`unknown scenario ${scenario}`);
  }
}

const small = Buffer.from('hello aruma\n');
const big = Buffer.alloc(3 * 1024 * 1024 + 17, 7);

let bucket;
let admin = false;
await step('init', async () => {
  ({ bucket, admin } = await bucketFor());
  return `endpoint ${bucket.storage.apiEndpoint}, customEndpoint=${bucket.storage.customEndpoint}`;
});
if (bucket) {
  const f1 = bucket.file('s0/small.txt');
  const f2 = bucket.file('s0/big.bin');
  await step('upload.small', () => f1.save(small, { resumable: false, contentType: 'text/plain' }));
  await step('upload.resumable3MB', () => f2.save(big, { resumable: true, contentType: 'application/octet-stream' }));
  await step('getMetadata', async () => `size=${(await f1.getMetadata())[0].size}`);
  await step('download', async () => {
    const [data] = await f2.download();
    if (!data.equals(big)) throw new Error(`downloaded ${data.length} bytes that differ`);
    return `${data.length} bytes`;
  });
  await step('list', async () => `${(await bucket.getFiles({ prefix: 's0/' }))[0].length} files`);
  if (admin) {
    await step('getDownloadURL', async () => {
      const { getDownloadURL } = await import('firebase-admin/storage');
      return (await getDownloadURL(f1)).replace(/token=[^&]*/, 'token=<redacted>');
    });
  }
  await step('delete', async () => {
    await f1.delete();
    await f2.delete();
  });
}
const { attempts } = require('../../netguard.cjs');
const blocked = [...new Set(attempts.filter((a) => a.startsWith('BLOCK')).map((a) => a.slice(6)))];
console.log(JSON.stringify({ scenario, results, blocked }));
process.exit(0);
