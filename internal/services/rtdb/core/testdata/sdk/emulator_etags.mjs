// emulator_etags.mjs - REST PUT sample values (taken from hash.json) into the
// RTDB emulator, GET them back with X-Firebase-ETag: true, and compare the
// ETag with the JS SDK hash recorded in hash.json.
//
// Usage: node emulator_etags.mjs [host:port]   (default 127.0.0.1:9400)
// Writes emulator_etags.json next to this script.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HOST = process.argv[2] || process.env.RTDB_EMULATOR_HOST || '127.0.0.1:9400';
const DIR = path.dirname(fileURLToPath(import.meta.url));
const hash = JSON.parse(fs.readFileSync(path.join(DIR, 'hash.json'), 'utf8'));
const NAMES = [
  '{s:1} (datastale value)',
  'new (datastale txmissing value)',
  '1.5',
  'true',
  'unicode mix',
  'int-like keys',
  'children with priorities (priority order != key order)',
  'object string priority with child priority',
  'leaf priority number',
  '2^53-1 (SDK ieee754 differs)',
  '2^-29 (SDK ieee754 differs)',
  '1e-300 (SDK ieee754 differs)',
];
const d = new Date();
const p2 = (n) => String(n).padStart(2, '0');
const out = {
  source: 'emulator',
  emulator: 'firebase-database-emulator-v4.11.2',
  captured: `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())}`,
  script: 'emulator_etags.mjs',
  sdkHashFrom: `hash.json (${hash.source.package} ${hash.source.version})`,
  notes: [
    'Each value is written with REST PUT /golden/etag/<i>.json?ns=demo-test (body = json, export format), then read with GET and header X-Firebase-ETag: true.',
    'sdkHash is the hash.json vector with the same name; match = (etag === sdkHash).',
    'For plain numbers, trueIeeeHash = base64(sha1("number:" + true IEEE-754 hex)); matchTrueIeee shows whether the emulator hashes the real double bits instead of the JS SDK doubleToIEEE754String output.',
  ],
  vectors: [],
};
for (let i = 0; i < NAMES.length; i++) {
  const v = hash.vectors.find((x) => x.name === NAMES[i]);
  if (!v) throw new Error('missing vector ' + NAMES[i]);
  const url = `http://${HOST}/golden/etag/${i}.json?ns=demo-test`;
  const body = v.jsonText ?? JSON.stringify(v.json);
  const put = await fetch(url, { method: 'PUT', body });
  const putBody = await put.text();
  const get = await fetch(url, { headers: { 'X-Firebase-ETag': 'true' } });
  const getBody = await get.text();
  const etag = get.headers.get('etag');
  out.vectors.push({
    name: v.name,
    json: v.json,
    putStatus: put.status,
    putBody,
    getBody,
    etag,
    sdkHash: v.hash,
    match: etag === v.hash,
  });
  // For plain numbers, also the hash with the true IEEE-754 bits in place of
  // the SDK's doubleToIEEE754String output.
  if (typeof v.json === 'number') {
    const b = Buffer.alloc(8);
    b.writeDoubleBE(v.json);
    const canon = 'number:' + b.toString('hex');
    const h = crypto.createHash('sha1').update(canon, 'utf8').digest('base64');
    Object.assign(out.vectors[out.vectors.length - 1], { sdkCanonical: v.canonical, trueIeeeCanonical: canon, trueIeeeHash: h, matchTrueIeee: etag === h });
  }
}
await fetch(`http://${HOST}/golden/etag.json?ns=demo-test`, { method: 'DELETE' });
fs.writeFileSync(path.join(DIR, 'emulator_etags.json'), JSON.stringify(out, null, 2) + '\n');
for (const v of out.vectors) console.log(v.match ? 'MATCH ' : 'DIFF  ', v.name, v.etag, v.sdkHash, v.getBody);
