// S0-B: two questions about firebase/storage 13.1.0 answered with its Node build (fetch; the URLs
// are built and parsed by the same code as in the browser), against the recording stub:
//
// 1. The runtime fallback for apps without a bundler plugin: `getStorage(app).host = '<aruma>'`
//    (FirebaseStorageImpl's setter, service.ts; not in the public typings). Do the operations work,
//    and what download URL comes back?
// 2. Which download URL forms does ref(storage, url) (refFromURL) accept, with and without the
//    setter: the Google form (as the web and Android return it, and as iOS does, with :443), the
//    prefixed Aruma form and the root Aruma form?
//
//   STUB_HTTPS=https://localhost:8821 STUB_LOG=… node node-host.mjs   (run by ../web.sh node-host)
import { appendFileSync } from 'node:fs';
import { initializeApp } from 'firebase/app';
import { deleteObject, getBytes, getDownloadURL, getStorage, ref, uploadBytes } from 'firebase/storage';

const { STUB_HTTPS, STUB_LOG } = process.env;
const aruma = new URL(STUB_HTTPS).host;
const config = { apiKey: 'AIzaS0pathNotARealKey', projectId: 's0path', appId: '1:1:web:s0path', storageBucket: 's0path.appspot.com' };
const results = [];
async function step(op, fn) {
  try {
    results.push({ op, ok: true, detail: String((await fn()) ?? '') });
  } catch (e) {
    results.push({ op, ok: false, detail: `${e.code ?? ''} ${e.message ?? e}`.trim().slice(0, 240) });
  }
}
const redact = (u) => u.replace(/token=[^&]*/, 'token=<redacted>');

appendFileSync(STUB_LOG, JSON.stringify({ marker: 'web-node-host' }) + '\n');
const plain = getStorage(initializeApp(config, 'plain'));
const set = getStorage(initializeApp(config, 'setter'));
set.host = aruma;
set.maxOperationRetryTime = 4000;
set.maxUploadRetryTime = 4000;
await step('setter.init', () => `host=${set.host} protocol=${set._protocol}`);
const small = new TextEncoder().encode('hello aruma\n');
let url;
await step('setter.upload', () => uploadBytes(ref(set, 's0/small.txt'), small, { contentType: 'text/plain' }).then(() => ''));
await step('setter.getDownloadURL', async () => redact((url = await getDownloadURL(ref(set, 's0/small.txt')))));
await step('setter.getBytes', async () => `${(await getBytes(ref(set, 's0/small.txt'))).byteLength} bytes`);
await step('setter.downloadURL.fetch', async () => `${(await fetch(url)).status}`);

const path = 'v0/b/s0path.appspot.com/o/s0%2Fsmall.txt?alt=media&token=t';
const forms = {
  google: `https://firebasestorage.googleapis.com/${path}`,
  // What FirebaseStorage for iOS returns (measured by ios.sh): the port spelled out.
  googleIos: `https://firebasestorage.googleapis.com:443/${path}`,
  prefixed: `https://${aruma}/firebasestorage.googleapis.com/${path}`,
  root: `https://${aruma}/${path}`,
};
for (const [name, storage] of [['plain', plain], ['setter', set]]) {
  for (const [form, u] of Object.entries(forms)) {
    await step(`refFromURL.${name}.${form}`, () => {
      const r = ref(storage, u);
      return `bucket=${r.bucket} path=${r.fullPath}`;
    });
  }
}
await step('setter.delete', () => deleteObject(ref(set, 's0/small.txt')).then(() => ''));
console.log(JSON.stringify({ scenario: 'web-node-host', results }));
process.exit(0);
