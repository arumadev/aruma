// S0-B web driver: loads index.html in Chromium once per mode and prints one results line per mode
// ({ scenario, results, blocked }). Run by ../web.sh, which starts the stub.
//
//   STUB_HTTP=… STUB_HTTPS=… STUB_CA=… STUB_LOG=… node run.mjs [mode…]
//
// Modes:
//   emulator            connectStorageEmulator(storage, '127.0.0.1', <stub port>)
//   prod-asis           production mode + @aruma/web install() as it is (fetch, Auth hosts)
//   prod-xhr            production mode + the storage spike (XHR open + fetch), Aruma on http
//   prod-xhr-https      the same with Aruma on https (the stub's throwaway CA, trusted by SPKI pin)
//   prod-build          production mode, no runtime patch: firebase-storage.js served with its
//                       host constant replaced by <aruma>/firebasestorage.googleapis.com, as a
//                       bundler plugin would (Aruma on https)
//
// The SDK is the CDN build (what Flutter web loads from www.gstatic.com), served by the page
// server under /firebasejs/13.1.0/ with its gstatic import URLs pointed there.
//
// The guard: Chromium resolves no name but localhost (--host-resolver-rules), so a request to
// Google fails before it leaves the machine; every failed request is recorded. No request is
// intercepted, so CORS preflights really go to the stub.
import { createHash, X509Certificate } from 'node:crypto';
import { appendFileSync, readFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const here = dirname(fileURLToPath(import.meta.url));
const arumaWeb = join(here, '../../../../sdk/web/aruma-web/src');
const firebaseDir = join(here, 'node_modules/firebase');
const { STUB_HTTP, STUB_HTTPS, STUB_LOG } = process.env;
const pagePort = Number(process.env.S0PATH_PAGE_PORT ?? 8823);
const modes = process.argv.slice(2).length ? process.argv.slice(2) : ['emulator', 'prod-asis', 'prod-xhr', 'prod-xhr-https', 'prod-build'];
const httpsHost = new URL(STUB_HTTPS).host;
const CDN = 'https://www.gstatic.com/firebasejs/13.1.0/';

const page = createServer((req, res) => {
  const u = new URL(req.url, 'http://localhost');
  const send = (type, body) => res.writeHead(200, { 'content-type': type }).end(body);
  try {
    if (u.pathname === '/') return send('text/html', readFileSync(join(here, 'index.html')));
    if (u.pathname.startsWith('/aruma-web/') && !u.pathname.includes('..')) {
      return send('text/javascript', readFileSync(join(arumaWeb, u.pathname.slice(11))));
    }
    const m = /^\/firebasejs\/13\.1\.0\/(firebase-[a-z]+\.js)$/.exec(u.pathname);
    if (m) {
      let body = readFileSync(join(firebaseDir, m[1]), 'utf8').replaceAll(CDN, '/firebasejs/13.1.0/');
      if (u.searchParams.get('build') === '1' && m[1] === 'firebase-storage.js') {
        // What a bundler plugin would do: the host constant, once, else fail.
        const n = body.split('"firebasestorage.googleapis.com"').length - 1;
        if (n !== 1) throw new Error(`expected the host constant once in firebase-storage.js, found ${n}`);
        body = body.replace('"firebasestorage.googleapis.com"', `"${httpsHost}/firebasestorage.googleapis.com"`);
      }
      return send('text/javascript', body);
    }
    res.writeHead(404).end();
  } catch (e) {
    res.writeHead(500).end(String(e));
  }
});
await new Promise((r) => page.listen(pagePort, '127.0.0.1', r));

// Trust the stub's certificate in Chromium by its public key only (no system trust store change).
const certPem = readFileSync(join(dirname(process.env.STUB_CA), 'server.pem'), 'utf8');
const spki = createHash('sha256')
  .update(new X509Certificate(certPem).publicKey.export({ type: 'spki', format: 'der' }))
  .digest('base64');
const browser = await chromium.launch({
  args: [
    '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1',
    `--ignore-certificate-errors-spki-list=${spki}`,
  ],
});

const isLocal = (u) => ['localhost', '127.0.0.1', '[::1]'].includes(u.hostname);
for (const mode of modes) {
  appendFileSync(STUB_LOG, JSON.stringify({ marker: `web-${mode}` }) + '\n');
  const blocked = new Set();
  const context = await browser.newContext();
  const p = await context.newPage();
  p.on('requestfailed', (r) => {
    const u = new URL(r.url());
    if (!isLocal(u)) blocked.add(`${r.method()} ${u.protocol}//${u.host}${u.pathname} (${r.failure()?.errorText})`);
  });
  const aruma = mode === 'prod-xhr-https' ? STUB_HTTPS : STUB_HTTP;
  const emuPort = new URL(STUB_HTTP).port;
  const build = mode === 'prod-build' ? 1 : 0;
  await p.goto(`http://localhost:${pagePort}/?mode=${mode}&aruma=${encodeURIComponent(aruma)}&emuPort=${emuPort}&build=${build}`);
  const results = await p.evaluate(() => window.__done);
  console.log(JSON.stringify({ scenario: `web-${mode}`, results, blocked: [...blocked] }));
  await context.close();
}
await browser.close();
page.close();
