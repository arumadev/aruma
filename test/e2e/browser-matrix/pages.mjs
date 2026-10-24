// The app's side of the browser matrix: plain pages on an origin that is not Aruma's, as an app
// that puts getDownloadURL() into its own markup. It is reached under two origins,
// http://127.0.0.1:<port> and http://localhost:<port>, cross-site to each other and to Aruma's
// port only cross-origin; both are potentially trustworthy, so the browsers send Fetch Metadata
// and enforce COEP. Nothing here comes from Aruma.
//
//   node pages.mjs   (ARUMA_E2E_PAGE_PORT, default 9261)
//
//   /blank      an empty page
//   /coep       the same with Cross-Origin-Embedder-Policy: require-corp (and COOP same-origin)
//   /evil.html  the matrix's evil page, with no protection: the control that its script runs
//   /frame.html a page that may be framed: the control that a frame's content is seen
//   /nocorp.png an image without CORP: the control that COEP is enforced
//   /sw.js      a service worker: the control that the engine registers one
//   /firebasejs/13.1.0/firebase-{app,storage}.js  the official web SDK
import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';

const port = Number(process.env.ARUMA_E2E_PAGE_PORT ?? 9261);
const marker = 'MATRIX-SCRIPT-RAN';
const page = (body = '') => `<!doctype html><html><head><meta charset="utf-8"><title>page</title></head><body>${body}</body></html>`;
// A 1×1 transparent PNG.
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64');

const routes = {
  '/blank': [{ 'content-type': 'text/html; charset=utf-8' }, page()],
  '/coep': [{
    'content-type': 'text/html; charset=utf-8',
    'cross-origin-embedder-policy': 'require-corp',
    'cross-origin-opener-policy': 'same-origin',
  }, page()],
  '/evil.html': [{ 'content-type': 'text/html; charset=utf-8' },
    page(`<p>evil page</p><script>console.log("${marker}"); document.title = "executed";</script>`)],
  '/frame.html': [{ 'content-type': 'text/html; charset=utf-8' }, page('MATRIX-FRAME-CONTENT')],
  '/nocorp.png': [{ 'content-type': 'image/png' }, png],
  '/sw.js': [{ 'content-type': 'text/javascript' }, 'self.addEventListener("fetch", () => {});'],
};

// The official web SDK, the CDN build (what an app loads from www.gstatic.com), from the
// firebase package, its gstatic imports pointed here.
const cdn = 'https://www.gstatic.com/firebasejs/13.1.0/';
const firebaseDir = new URL('./node_modules/firebase/', import.meta.url);
for (const file of ['firebase-app.js', 'firebase-storage.js']) {
  const body = readFileSync(new URL(file, firebaseDir), 'utf8').replaceAll(cdn, '/firebasejs/13.1.0/');
  routes[`/firebasejs/13.1.0/${file}`] = [{ 'content-type': 'text/javascript' }, body];
}

const serve = (req, res) => {
  const route = routes[new URL(req.url, 'http://x').pathname];
  if (!route) {
    res.writeHead(404).end();
    return;
  }
  res.writeHead(200, { 'cache-control': 'no-store', ...route[0] }).end(route[1]);
};

// On both loopback addresses, since a browser may resolve localhost to either.
createServer(serve).listen(port, '127.0.0.1', () => console.log(`pages on http://127.0.0.1:${port}`));
createServer(serve).listen(port, '::1').on('error', (e) => console.log(`pages on [::1]: ${e.code}`));
