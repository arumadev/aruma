// The https front for Aruma's main host in the firebase-cli-rtdb.sh experiment: like
// ../tls-proxy.mjs it terminates TLS (firebase-tools sends its credential only to https origins)
// and forwards to the plain http server, and it logs one line per request so the run shows what
// the CLI asked the management APIs for.
//
//   node api-front.mjs <cert.pem> <key.pem> <aruma-origin> <port> <log>
//
// It listens on 127.0.0.1:<port> and prints "https://127.0.0.1:<port>" on stdout.
//
// firebase-tools finds a database through two management calls: the project's
// resources.realtimeDatabaseInstance (GET /v1beta1/projects/{p}) and the instance's databaseUrl
// (GET /v1beta/projects/{p}/locations/-/instances/{id}); the log shows both.
import https from 'node:https';
import http from 'node:http';
import fs from 'node:fs';

const [certFile, keyFile, target, port, logFile] = process.argv.slice(2);
if (!logFile) {
  console.error('usage: node api-front.mjs <cert.pem> <key.pem> <aruma-origin> <port> <log>');
  process.exit(2);
}
const upstream = new URL(target);
const log = (line) => fs.appendFileSync(logFile, line + '\n');

// fetchUpstream sends the request on to Aruma and buffers the answer.
function fetchUpstream(req, body) {
  return new Promise((resolve, reject) => {
    const headers = { ...req.headers, 'x-forwarded-proto': 'https' };
    const out = http.request({ host: upstream.hostname, port: upstream.port, method: req.method, path: req.url, headers }, (up) => {
      const chunks = [];
      up.on('data', (c) => chunks.push(c));
      up.on('end', () => resolve({ status: up.statusCode, headers: up.headers, body: Buffer.concat(chunks) }));
    });
    out.on('error', reject);
    out.end(body);
  });
}

function send(res, status, headers, body) {
  const h = { ...headers };
  delete h['content-length'];
  delete h['transfer-encoding'];
  res.writeHead(status, { ...h, 'content-length': Buffer.byteLength(body) });
  res.end(body);
}

const server = https.createServer({ cert: fs.readFileSync(certFile), key: fs.readFileSync(keyFile) }, (req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', async () => {
    try {
      const up = await fetchUpstream(req, Buffer.concat(chunks));
      log(`${req.method} ${req.url} -> ${up.status}`);
      send(res, up.status, up.headers, up.body);
    } catch (err) {
      log(`${req.method} ${req.url} -> 502 ${err.message}`);
      send(res, 502, { 'content-type': 'text/plain' }, `upstream error: ${err.message}\n`);
    }
  });
});
server.listen(Number(port), '127.0.0.1', () => {
  console.log(`https://127.0.0.1:${server.address().port}`);
});
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => server.close(() => process.exit(0)));
