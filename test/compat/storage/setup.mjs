// Sets up one project of the Storage compat run on a fresh Aruma, through the console API with
// the owner's CLI token, as the dashboard would (run.sh calls it once per project):
//
// - the project, and a web app (which gives the project its web API key);
// - Auth with anonymous and email/password sign-in, the providers the tests use;
// - the Storage rules (--rules), published as the rules editor publishes them;
// - the bucket's CORS (--cors <file>, GCS's cors[] shape), when the suite reads bytes from a
//   browser on another origin;
// - a service account and its key file, which the adapter exchanges for access tokens.
//
//   node setup.mjs --base http://127.0.0.1:9410 --project p --rules f.rules [--cors c.json] --out <dir>
//
// ARUMA_CLI_TOKEN carries the token. It writes <dir>/<project>.json ({projectId, bucket,
// apiKey, serviceAccount}) and <dir>/<project>-key.json, and prints the first.
import fs from 'node:fs';
import path from 'node:path';
import { parseArgs } from 'node:util';

const { values: opt } = parseArgs({
  options: {
    base: { type: 'string' },
    project: { type: 'string' },
    rules: { type: 'string' },
    cors: { type: 'string' },
    out: { type: 'string' },
    'display-name': { type: 'string', default: 'Storage compat tests' },
  },
});
const token = process.env.ARUMA_CLI_TOKEN;
const p = opt.project;

async function api(method, route, body) {
  const res = await fetch(`${opt.base}/console/v1${route}`, {
    method,
    headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${route}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : {};
}

await api('POST', '/projects', { projectId: p, displayName: opt['display-name'] });
await api('POST', `/projects/${p}/apps`, { platform: 'WEB', displayName: 'Compat web' });
await api('PATCH', `/projects/${p}/auth/config`, { anonymousEnabled: true, passwordEnabled: true, enumerationProtection: false });
const keys = await api('GET', `/projects/${p}/apiKeys`);
const apiKey = keys.keys.find((k) => k.platform === 'WEB' && k.state === 'ACTIVE')?.keyString;
if (!apiKey) throw new Error(`project ${p} has no active web API key`);

const storage = await api('GET', `/projects/${p}/storage`);
const source = fs.readFileSync(opt.rules, 'utf8');
await api('PUT', `/projects/${p}/storage/rules`, { source });
const published = await api('GET', `/projects/${p}/storage/rules`);
if (published.source !== source) throw new Error(`the rules read back differ from ${opt.rules}`);
if (opt.cors) await api('PUT', `/projects/${p}/storage/cors`, JSON.parse(fs.readFileSync(opt.cors, 'utf8')));

const sa = await api('POST', `/projects/${p}/serviceAccounts`, { accountId: 'compat-admin' });
const keyRes = await fetch(`${opt.base}/console/v1/projects/${p}/serviceAccounts/${sa.id}/keys`, {
  method: 'POST',
  headers: { authorization: `Bearer ${token}` },
});
if (!keyRes.ok) throw new Error(`service account key: ${keyRes.status} ${await keyRes.text()}`);
const keyFile = path.join(opt.out, `${p}-key.json`);
fs.writeFileSync(keyFile, await keyRes.text(), { mode: 0o600 });

const info = { projectId: p, bucket: storage.bucket ?? storage.name, apiKey, serviceAccount: sa.email ?? sa.id, keyFile };
fs.writeFileSync(path.join(opt.out, `${p}.json`), JSON.stringify(info, null, 2) + '\n');
console.log(JSON.stringify({ ...info, apiKey: '<redacted>' }));
