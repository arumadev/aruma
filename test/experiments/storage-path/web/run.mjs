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
// The guard: Chromium resolves no name but localhost, and every request that is not to this
// machine is aborted and recorded (the gstatic CDN URLs are answered from node_modules).
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

const page = createServer((req, res) => {
  const u = new URL(req.url, 'http://localhost');
  try {
    if (u.pathname === '/') {
      res.writeHead(200, { 'content-type': 'text/html' }).end(readFileSync(join(here, 'index.html')));
    } else if (u.pathname.startsWith('/aruma-web/') && !u.pathname.includes('..')) {
      res.writeHead(200, { 'content-type': 'text/javascript' }).end(readFileSync(join(arumaWeb, u.pathname.slice(11))));
    } else res.writeHead(404).end();
  } catch {
    res.writeHead(404).end();
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
  const httpsHost = new URL(STUB_HTTPS).host;
  await context.route('**/*', async (route) => {
    const u = new URL(route.request().url());
    if (u.hostname === 'www.gstatic.com' && u.pathname.startsWith('/firebasejs/13.1.0/')) {
      let body = readFileSync(join(firebaseDir, u.pathname.split('/').pop()), 'utf8');
      if (mode === 'prod-build' && u.pathname.endsWith('/firebase-storage.js')) {
        const n = body.split('"firebasestorage.googleapis.com"').length - 1;
        if (n !== 1) throw new Error(`expected the host constant once in firebase-storage.js, found ${n}`);
        body = body.replace('"firebasestorage.googleapis.com"', `"${httpsHost}/firebasestorage.googleapis.com"`);
      }
      return route.fulfill({ status: 200, contentType: 'text/javascript', body });
    }
    if (isLocal(u)) return route.continue();
    blocked.add(`${route.request().method()} ${u.protocol}//${u.host}${u.pathname}`);
    return route.abort('internetdisconnected');
  });
  const p = await context.newPage();
  const consoleLines = [];
  p.on('console', (m) => consoleLines.push(m.text()));
  const aruma = mode === 'prod-xhr-https' ? STUB_HTTPS : STUB_HTTP;
  const emuPort = new URL(STUB_HTTP).port;
  await p.goto(`http://localhost:${pagePort}/?mode=${mode}&aruma=${encodeURIComponent(aruma)}&emuPort=${emuPort}`);
  const results = await p.evaluate(() => window.__done);
  console.log(JSON.stringify({ scenario: `web-${mode}`, results, blocked: [...blocked], console: consoleLines.slice(0, 3) }));
  await context.close();
}
await browser.close();
page.close();
