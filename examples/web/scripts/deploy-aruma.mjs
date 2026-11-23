#!/usr/bin/env node
// Deploys Team Board's backend settings to an Aruma project, with the same HTTP calls the
// firebase CLI makes (the CLI itself sends its token over https only, so this also works with a
// local server on http://localhost):
//
//   - the Realtime Database rules (database.rules.json): PUT /.settings/rules.json
//   - the Firestore and Storage rules (firestore.rules, storage.rules): a ruleset each, then the
//     cloud.firestore and firebase.storage/<bucket> releases (the firebaserules API)
//   - the Firestore field override of firestore.indexes.json (members.uid, collection group scope)
//   - with --anonymous, guest sign-in (the switch on the dashboard's Sign-in methods page)
//
//   ARUMA_URL=http://localhost:8080 ARUMA_TOKEN=aruma_pat_... node scripts/deploy-aruma.mjs [--anonymous]
//
// ARUMA_TOKEN is a CLI token (dashboard: Account, CLI tokens) of the project's owner. The project,
// bucket and database come from firebase-config.json (or the file FIREBASE_CONFIG names).
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const base = (process.env.ARUMA_URL ?? '').replace(/\/+$/, '');
const token = process.env.ARUMA_TOKEN ?? '';
if (!base || !token) {
  console.error('Set ARUMA_URL (your server, e.g. http://localhost:8080) and ARUMA_TOKEN (a CLI token).');
  process.exit(2);
}
const config = JSON.parse(readFileSync(resolve(root, process.env.FIREBASE_CONFIG ?? 'firebase-config.json'), 'utf8'));
const project = config.projectId;
const read = (name) => readFileSync(resolve(root, name), 'utf8');

async function call(method, url, body) {
  const res = await fetch(url, {
    method,
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${new URL(url).pathname}: ${res.status} ${text.trim()}`);
  return text ? JSON.parse(text) : {};
}

// A rules file becomes a ruleset, and the release then points at it (created the first time).
async function release(name, file) {
  const ruleset = await call('POST', `${base}/v1/projects/${project}/rulesets`, {
    source: { files: [{ name: file, content: read(file) }] },
  });
  const releaseName = `projects/${project}/releases/${name}`;
  try {
    await call('PATCH', `${base}/v1/${releaseName}`, { release: { name: releaseName, rulesetName: ruleset.name } });
  } catch (e) {
    if (!/: 404 /.test(String(e))) throw e;
    await call('POST', `${base}/v1/projects/${project}/releases`, { name: releaseName, rulesetName: ruleset.name });
  }
  console.log(`${file}: released as ${name}`);
}

const db = new URL(config.databaseURL);
db.pathname = '/.settings/rules.json';
await call('PUT', db.href, read('database.rules.json'));
console.log('database.rules.json: published');

await release('cloud.firestore', 'firestore.rules');
await release(`firebase.storage/${config.storageBucket}`, 'storage.rules');

for (const o of JSON.parse(read('firestore.indexes.json')).fieldOverrides ?? []) {
  const path = `projects/${project}/databases/(default)/collectionGroups/${o.collectionGroup}/fields/${o.fieldPath}`;
  // The file's short form, in the Admin API's shape (as firebase deploy --only firestore:indexes sends it).
  const indexes = o.indexes.map(({ queryScope, order, arrayConfig }) => ({
    queryScope,
    fields: [{ fieldPath: o.fieldPath, ...(order ? { order } : { arrayConfig }) }],
  }));
  await call('PATCH', `${base}/v1/${path}?updateMask=indexConfig`, { indexConfig: { indexes } });
  console.log(`firestore.indexes.json: ${o.collectionGroup}.${o.fieldPath} indexed`);
}

if (process.argv.includes('--anonymous')) {
  await call('PATCH', `${base}/console/v1/projects/${project}/auth/config`, { anonymousEnabled: true });
  console.log('guest (anonymous) sign-in: on');
}
