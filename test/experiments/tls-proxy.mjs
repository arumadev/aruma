// A minimal TLS-terminating reverse proxy, standing in for the Caddy/nginx that fronts Aruma
// in production. firebase-tools only sends its credential to https origins (see
// docs/research/experiments.md, T23), so the CLI experiment needs https in front of the plain
// http test server.
//
//   node tls-proxy.mjs <cert.pem> <key.pem> <target-origin>
//
// It listens on a free loopback port and prints "https://127.0.0.1:<port>" on stdout.
import https from 'node:https';
import http from 'node:http';
import fs from 'node:fs';

const [certFile, keyFile, target] = process.argv.slice(2);
if (!target) {
  console.error('usage: node tls-proxy.mjs <cert.pem> <key.pem> <target-origin>');
  process.exit(2);
}
const upstream = new URL(target);

const server = https.createServer({ cert: fs.readFileSync(certFile), key: fs.readFileSync(keyFile) }, (req, res) => {
  const headers = { ...req.headers, 'x-forwarded-proto': 'https' };
  const out = http.request({ host: upstream.hostname, port: upstream.port, method: req.method, path: req.url, headers }, (up) => {
    res.writeHead(up.statusCode, up.headers);
    up.pipe(res);
  });
  out.on('error', (err) => {
    res.writeHead(502, { 'content-type': 'text/plain' });
    res.end(`upstream error: ${err.message}\n`);
  });
  req.pipe(out);
});
server.listen(0, '127.0.0.1', () => {
  console.log(`https://127.0.0.1:${server.address().port}`);
});
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => server.close(() => process.exit(0)));
