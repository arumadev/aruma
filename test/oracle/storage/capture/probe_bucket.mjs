// probe_bucket.mjs - asks production, without any credentials, whether a project has a
// default Cloud Storage bucket, and records the answers (../README.md, "production").
//
// Usage: node probe_bucket.mjs <project-id> <out.json>
//
// Both default bucket names are tried (<project>.appspot.com, the name before 30 Oct 2024,
// and <project>.firebasestorage.app, the name after), plus a random name as the control
// for "does not exist", on the client API and on the GCS JSON API. An existing bucket answers
// 401/403 to an anonymous caller; a missing one 404. The project id is written as <project>.

import fs from 'node:fs';

const [project, out] = process.argv.slice(2);
const control = `zz-no-such-bucket-${Math.random().toString(36).slice(2, 10)}.appspot.com`;
const buckets = [`${project}.appspot.com`, `${project}.firebasestorage.app`, control];
const KEEP = ['content-type', 'www-authenticate', 'access-control-allow-origin', 'access-control-expose-headers', 'x-content-type-options', 'cache-control', 'server'];

const probes = [];
for (const b of buckets) {
  for (const url of [
    `https://firebasestorage.googleapis.com/v0/b/${b}/o?maxResults=1`,
    `https://firebasestorage.googleapis.com/v0/b/${b}/o/x`,
    `https://storage.googleapis.com/storage/v1/b/${b}`,
    `https://storage.googleapis.com/storage/v1/b/${b}/o?maxResults=1`,
  ]) {
    const res = await fetch(url);
    const headers = {};
    for (const k of KEEP) if (res.headers.get(k)) headers[k] = res.headers.get(k);
    probes.push({ request: { method: 'GET', url }, response: { status: res.status, headers, body: await res.text() } });
  }
}
// Positive control: a public bucket that exists answers the same anonymous calls with
// 200 (or 401/403 where listing is not public), so a 404 above means "no such bucket".
for (const url of [
  'https://storage.googleapis.com/storage/v1/b/gcp-public-data-landsat',
  'https://storage.googleapis.com/storage/v1/b/gcp-public-data-landsat/o?maxResults=1',
]) {
  const res = await fetch(url);
  const body = await res.text();
  probes.push({ control: 'existing public bucket', request: { method: 'GET', url }, response: { status: res.status, body: body.length > 400 ? `${body.slice(0, 400)}...` : body } });
}
const doc = {
  scenario: 'bucket_probe',
  description: 'Anonymous GETs against both default bucket names of the project and a random control name, on the Firebase Storage client API and the GCS JSON API.',
  source: 'production',
  captured: new Date().toISOString().slice(0, 10),
  script: 'capture/probe_bucket.mjs',
  control,
  probes,
};
fs.writeFileSync(out, JSON.stringify(doc, null, 2).split(project).join('<project>') + '\n');
console.log(probes.map((p) => `${p.response.status} ${p.request.url.split(project).join('<project>')}`).join('\n'));
