// The Storage compat run's adapter (run.sh starts one per project; see README.md). It stands
// where the tests and the capture script expect the Storage and Auth emulators, forwards every
// request to Aruma unchanged in method, path and body, and swaps in what a real deployment would
// carry instead of the emulators' fixed stand-ins:
//
// - key=fake-api-key (the Auth calls of a page pointed at the "emulator") becomes the project's
//   web API key.
// - Authorization: Bearer owner, the emulators' admin (and the capture script's fixed
//   ya29.fake-access-token), becomes a service account's access token
//   for the project (run.sh hands over its key file; the token comes from the key's token_uri by
//   the JWT bearer grant, as google-auth-library gets it, and is renewed after 45 minutes).
// - A request on the GCS paths (/storage/v1, /upload/storage/v1, /download/storage/v1) without
//   Authorization gets that token too: in emulator mode the Admin SDKs send none, and the
//   emulator treats every GCS call as the admin's. A session URL (upload_id) is left alone.
// - A JSON API path without its /storage/v1 (/b/<bucket>/...) gets it back. @google-cloud/storage
//   6.x takes STORAGE_EMULATOR_HOST itself as the base URL in emulator mode (the emulator serves
//   both forms); against Google it always sends /storage/v1/b/....
// - POST /internal/reset, the emulator's reset that the conformance tests call before each
//   test, deletes every object of the project's bucket through the GCS API.
//
// It is test code: Aruma itself serves none of this. One line per request goes to stderr, with
// what was swapped.
//
//   node adapter.mjs --aruma http://127.0.0.1:9410 --listen 9411 --bucket <b> \
//     --api-key <key> --service-account <key.json>
//
// It prints ADAPTER_READY once it listens.
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import { parseArgs } from 'node:util';

const { values: opt } = parseArgs({
  options: {
    aruma: { type: 'string' },
    listen: { type: 'string' },
    bucket: { type: 'string' },
    'api-key': { type: 'string' },
    'service-account': { type: 'string' },
  },
});
const aruma = new URL(opt.aruma);
const sa = JSON.parse(fs.readFileSync(opt['service-account'], 'utf8'));
const GCS = ['/storage/v1/', '/upload/storage/v1/', '/download/storage/v1/'];
// The fixed OAuth token test/oracle/storage/capture/capture.mjs gives @google-cloud/storage in its
// admin_gcs_endpoint scenario, where an app would hold a real one.
const FAKE_OAUTH = 'ya29.fake-access-token';

let token = null;
let tokenAt = 0;
async function accessToken() {
  if (token && Date.now() - tokenAt < 45 * 60e3) return token;
  const now = Math.floor(Date.now() / 1000);
  const b64 = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
  const unsigned = `${b64({ alg: 'RS256', typ: 'JWT', kid: sa.private_key_id })}.${b64({
    iss: sa.client_email,
    scope: 'https://www.googleapis.com/auth/cloud-platform',
    aud: sa.token_uri,
    iat: now,
    exp: now + 3600,
  })}`;
  const sig = crypto.sign('RSA-SHA256', Buffer.from(unsigned), sa.private_key).toString('base64url');
  // Straight to Aruma: token_uri names the public URL, which may be a recording proxy.
  const res = await fetch(new URL(new URL(sa.token_uri).pathname, aruma), {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ grant_type: 'urn:ietf:params:oauth:grant-type:jwt-bearer', assertion: `${unsigned}.${sig}` }),
  });
  if (!res.ok) throw new Error(`token exchange: ${res.status} ${await res.text()}`);
  token = (await res.json()).access_token;
  tokenAt = Date.now();
  return token;
}

async function gcs(method, path) {
  const res = await fetch(new URL(path, aruma), { method, headers: { authorization: `Bearer ${await accessToken()}` } });
  if (!res.ok && res.status !== 404) throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  return res.status === 204 || method === 'DELETE' ? null : res.json();
}

// reset deletes every object of the bucket, page by page, as the emulator's reset empties it.
async function reset() {
  const b = encodeURIComponent(opt.bucket);
  let deleted = 0;
  for (;;) {
    const page = await gcs('GET', `/storage/v1/b/${b}/o?maxResults=1000`);
    const items = page?.items ?? [];
    if (items.length === 0) return deleted;
    for (const o of items) {
      await gcs('DELETE', `/storage/v1/b/${b}/o/${encodeURIComponent(o.name)}`);
      deleted++;
    }
  }
}

const server = http.createServer(async (req, res) => {
  const swapped = [];
  try {
    const url = new URL(req.url, 'http://adapter');
    if (req.method === 'POST' && url.pathname === '/internal/reset') {
      const n = await reset();
      process.stderr.write(`POST /internal/reset 200 deleted=${n}\n`);
      res.writeHead(200, { 'content-type': 'text/plain' }).end('OK');
      return;
    }
    if (url.pathname.startsWith('/b/')) {
      url.pathname = `/storage/v1${url.pathname}`;
      swapped.push('path');
    }
    if (url.searchParams.get('key') === 'fake-api-key') {
      url.searchParams.set('key', opt['api-key']);
      swapped.push('key');
    }
    const headers = { ...req.headers, host: aruma.host };
    const auth = headers.authorization;
    if (auth === 'Bearer owner' || auth === `Bearer ${FAKE_OAUTH}`) {
      headers.authorization = `Bearer ${await accessToken()}`;
      swapped.push('owner');
    } else if (!auth && GCS.some((p) => url.pathname.startsWith(p)) && !url.searchParams.has('upload_id')) {
      headers.authorization = `Bearer ${await accessToken()}`;
      swapped.push('admin');
    }
    const out = http.request(
      { host: aruma.hostname, port: aruma.port, method: req.method, path: url.pathname + url.search, headers },
      (ares) => {
        process.stderr.write(`${req.method} ${req.url} ${ares.statusCode}${swapped.length ? ' swapped=' + swapped.join(',') : ''}\n`);
        res.writeHead(ares.statusCode, ares.rawHeaders);
        ares.pipe(res);
      },
    );
    out.on('error', (e) => {
      process.stderr.write(`${req.method} ${req.url} 502 ${e.message}\n`);
      if (!res.headersSent) res.writeHead(502);
      res.end();
    });
    req.pipe(out);
  } catch (e) {
    process.stderr.write(`${req.method} ${req.url} adapter error: ${e.stack}\n`);
    if (!res.headersSent) res.writeHead(500, { 'content-type': 'text/plain' });
    res.end(`adapter: ${e.message}`);
  }
});
server.listen(Number(opt.listen), '127.0.0.1', () => console.log('ADAPTER_READY'));
for (const s of ['SIGINT', 'SIGTERM']) process.on(s, () => server.close(() => process.exit(0)));
