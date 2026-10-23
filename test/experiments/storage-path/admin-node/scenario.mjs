// S0-B: one Admin SDK configuration against the recording stub, in a process of its own (the
// Storage clients read STORAGE_EMULATOR_HOST when they are built, and firebase-admin writes it).
//
//   node scenario.mjs <scenario>    (run.sh sets KEY_FILE, BUCKET, STUB_HTTP, STUB_HTTPS)
//
// Prints one JSON line: { scenario, results: [{ op, ok, detail }], blocked, outbound }. The network
// guard (../../netguard.cjs, preloaded by run.sh) refuses every connection that leaves loopback;
// `outbound` lists, for each such request, what it would have carried (method, URL without query,
// Authorization scheme), recorded at http(s).request before the guard refuses the socket.
import { readFileSync } from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import { createRequire } from 'node:module';
import { arumaCredential } from '../../admin-node/aruma-credential.mjs';

const require = createRequire(import.meta.url);
const scenario = process.argv[2];
const { KEY_FILE, BUCKET, STUB_HTTP, STUB_HTTPS } = process.env;
const key = JSON.parse(readFileSync(KEY_FILE, 'utf8'));
const results = [];

// gaxios sends with node-fetch, which looks up http(s).request for every request.
const outbound = new Set();
for (const [scheme, mod] of [['http', http], ['https', https]]) {
  const original = mod.request;
  mod.request = function request(...args) {
    const url = args.find((a) => typeof a === 'string' || a instanceof URL);
    const opts = args.find((a) => a && typeof a === 'object' && !(a instanceof URL)) ?? {};
    const u = url ? new URL(String(url)) : new URL(`${scheme}://${opts.hostname ?? opts.host}${opts.path ?? '/'}`);
    if (!['localhost', '127.0.0.1', '[::1]'].includes(u.hostname)) {
      const headers = opts.headers ?? {};
      const auth = Object.entries(headers).find(([k]) => k.toLowerCase() === 'authorization')?.[1];
      outbound.add(`${opts.method ?? 'GET'} ${u.protocol}//${u.host}${u.pathname} Authorization=${auth ? String(auth).split(' ')[0] : 'none'}`);
    }
    return original.apply(this, args);
  };
}

async function step(op, fn) {
  try {
    const detail = await fn();
    results.push({ op, ok: true, detail: detail ?? '' });
  } catch (e) {
    results.push({ op, ok: false, detail: `${e.code ?? ''} ${String(e.message ?? e).split('\n')[0]}`.trim().slice(0, 240) });
  }
}

// arumaAuthClient: an OAuth2Client whose tokens come from Aruma's token_uri (arumaCredential).
function arumaAuthClient(options = {}) {
  const { OAuth2Client } = require('google-auth-library');
  const cred = arumaCredential(key);
  const client = new OAuth2Client(options);
  client.refreshHandler = async () => {
    const t = await cred.getAccessToken();
    return { access_token: t.access_token, expiry_date: Date.now() + t.expires_in * 1000 };
  };
  return client;
}

// bucketFor builds the Storage bucket handle the scenario names, and whether it came from
// firebase-admin.
async function bucketFor() {
  const { Storage } = require('@google-cloud/storage');
  const gcs = (opts) => new Storage({ projectId: key.project_id, ...opts }).bucket(BUCKET);
  const stubHost = new URL(STUB_HTTPS).hostname;
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
    case 'gcs-universe-aruma':
      // universeDomain = the Aruma host: resumable-upload.js keeps the auth client for a hostname
      // equal to (or under) the universe, whatever useAuthWithCustomEndpoint says.
      return {
        bucket: gcs({
          apiEndpoint: STUB_HTTPS, universeDomain: stubHost,
          authClient: arumaAuthClient({ universeDomain: stubHost }), useAuthWithCustomEndpoint: true,
        }),
      };
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
  if (scenario.startsWith('gcs-')) {
    // The workaround for save()'s unauthenticated session start with a custom endpoint:
    // createResumableUpload passes useAuthWithCustomEndpoint (file.js), then save() uses the URI.
    await step('upload.resumable3MB.createResumableUpload', async () => {
      const f3 = bucket.file('s0/big-uri.bin');
      const [uri] = await f3.createResumableUpload({ metadata: { contentType: 'application/octet-stream' } });
      await f3.save(big, { uri, resumable: true, contentType: 'application/octet-stream' });
      await f3.delete();
    });
  }
  await step('getMetadata', async () => `size=${(await f1.getMetadata())[0].size}`);
  await step('download', async () => {
    const [data] = await f2.download();
    if (!data.equals(big)) throw new Error(`downloaded ${data.length} bytes that differ`);
    return `${data.length} bytes`;
  });
  await step('list', async () => `${(await bucket.getFiles({ prefix: 's0/' }))[0].length} files`);
  // firebase-admin's getDownloadURL, also on a plain @google-cloud/storage File (what a wrapper
  // around @google-cloud/storage hands out): where does its metadata request go, and with what?
  await step(admin ? 'getDownloadURL' : 'firebaseAdmin.getDownloadURL', async () => {
    const { getDownloadURL } = await import('firebase-admin/storage');
    return (await getDownloadURL(f1)).replace(/token=[^&]*/, 'token=<redacted>');
  });
  if (!admin) {
    // What a wrapper's own getDownloadURL would do: the Firebase REST metadata from the Aruma
    // origin, with the same authorized client, and a URL on the Aruma origin (/v0 at the root).
    await step('wrapper.getDownloadURL', async () => {
      const origin = new URL(bucket.storage.apiEndpoint).origin;
      const uri = `${origin}/v0/b/${bucket.name}/o/${encodeURIComponent(f1.name)}`;
      const body = await new Promise((resolve, reject) =>
        bucket.storage.makeAuthenticatedRequest({ method: 'GET', uri }, (err, b) => (err ? reject(err) : resolve(b))));
      const [token] = String(body?.downloadTokens ?? '').split(',');
      if (!token) throw new Error('no download token');
      return `${uri}?alt=media&token=<redacted>`;
    });
  }
  await step('delete', async () => {
    await f1.delete();
    await f2.delete();
  });
}
const { attempts } = require('../../netguard.cjs');
const blocked = [...new Set(attempts.filter((a) => a.startsWith('BLOCK')).map((a) => a.slice(6)))];
console.log(JSON.stringify({ scenario, results, blocked, outbound: [...outbound] }));
process.exit(0);
