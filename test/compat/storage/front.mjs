// The local TLS front of the Storage compat run (run.sh starts it; see README.md). One https
// listener on loopback, with a certificate for every name below signed by the run's CA, that
// answers by the name the client asked for (Host):
//
// - Firebase's API hosts (--api, e.g. firebasestorage.googleapis.com): the request goes to the
//   upstream (Aruma, through the adapter) as a runtime redirect sends it, the host becoming the
//   first path segment: https://firebasestorage.googleapis.com/v0/b/... is
//   <upstream>/firebasestorage.googleapis.com/v0/b/... (ADR-022 §1, the prefixes in
//   internal/server). Bodies stream both ways; X-Forwarded-Proto and -For are added.
// - example.com: an empty page, the one the conformance tests open before adding the SDK.
// - www.gstatic.com: /firebasejs/<version>/<file> from --cdn <dir>/<version>/<file>, the
//   compat builds that the npm release of that version ships.
//
// Anything else is 404. Only the run's own clients reach it: Node through hosts.cjs, the
// browsers through their resolver rules.
//
//   node front.mjs --cert c.pem --key k.pem --port 9412 --upstream http://127.0.0.1:9411 \
//     --api firebasestorage.googleapis.com,identitytoolkit.googleapis.com --cdn <dir>
//
// It prints READY once it listens, and logs one line per request to stderr.
import fs from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import path from 'node:path';
import { parseArgs } from 'node:util';

const { values: opt } = parseArgs({
  options: {
    cert: { type: 'string' },
    key: { type: 'string' },
    port: { type: 'string' },
    upstream: { type: 'string' },
    api: { type: 'string', default: '' },
    cdn: { type: 'string', default: '' },
  },
});
const upstream = new URL(opt.upstream);
const api = new Set(opt.api.split(',').filter(Boolean));

function log(req, status, note = '') {
  process.stderr.write(`${req.method} https://${req.headers.host}${req.url} ${status}${note ? ' ' + note : ''}\n`);
}

function forward(req, res, host) {
  const headers = { ...req.headers, host: upstream.host, 'x-forwarded-proto': 'https', 'x-forwarded-for': req.socket.remoteAddress };
  const out = http.request(
    { host: upstream.hostname, port: upstream.port, method: req.method, path: `/${host}${req.url}`, headers },
    (ures) => {
      log(req, ures.statusCode);
      res.writeHead(ures.statusCode, ures.rawHeaders);
      ures.pipe(res);
    },
  );
  out.on('error', (e) => {
    log(req, 502, e.message);
    if (!res.headersSent) res.writeHead(502);
    res.end();
  });
  req.pipe(out);
}

function cdn(req, res) {
  const m = /^\/firebasejs\/([0-9.]+)\/(firebase-[a-z-]+\.js)$/.exec(req.url);
  const file = m && opt.cdn ? path.join(opt.cdn, m[1], m[2]) : '';
  if (!file || !fs.existsSync(file)) {
    log(req, 404);
    res.writeHead(404).end();
    return;
  }
  log(req, 200);
  res.writeHead(200, { 'content-type': 'text/javascript; charset=utf-8', 'access-control-allow-origin': '*' });
  fs.createReadStream(file).pipe(res);
}

const server = https.createServer({ cert: fs.readFileSync(opt.cert), key: fs.readFileSync(opt.key) }, (req, res) => {
  const host = (req.headers.host || '').toLowerCase().replace(/:\d+$/, '');
  if (api.has(host)) return forward(req, res, host);
  if (host === 'example.com') {
    log(req, 200);
    res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' }).end('<!doctype html><title>Example Domain</title><p>Aruma compat page</p>\n');
    return;
  }
  if (host === 'www.gstatic.com') return cdn(req, res);
  log(req, 404);
  res.writeHead(404).end();
});
server.listen(Number(opt.port), '127.0.0.1', () => console.log('READY'));
for (const s of ['SIGINT', 'SIGTERM']) process.on(s, () => server.close(() => process.exit(0)));
