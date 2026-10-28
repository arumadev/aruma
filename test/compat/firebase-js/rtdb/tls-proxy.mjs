// A TLS-terminating reverse proxy for the RTDB suites in production mode, standing in for the
// Caddy that fronts Aruma: it serves https and wss on a loopback port with the run's certificate
// and forwards to a plain http target. Unlike a plain TLS proxy it also forwards
// WebSocket upgrades (/.ws), which is how the SDKs talk to RTDB. The Host header passes through
// unchanged, as Caddy's reverse_proxy does, so the target sees <ns>.rtdb.aruma.test; it adds
// X-Forwarded-Proto and X-Forwarded-For.
//
//   node tls-proxy.mjs [--rewrite-hello] <cert.pem> <key.pem> <listen-port> <target-origin>
//
// --rewrite-hello is for the emulator only. Its hello names its own bind address as the host
// ("h": "127.0.0.1:9470"), and the SDK sends every later connection of that database there:
// plain http, past this proxy and its TLS. Production names a host that serves the database
// over TLS, and Aruma names the host the client connected to (ADR-018). The option replaces
// "h" in the first frame of each WebSocket with the request's Host, and logs it.
//
// It prints "READY https://127.0.0.1:<port>" once it listens.
import fs from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';

const args = process.argv.slice(2);
const rewriteHello = args[0] === '--rewrite-hello';
if (rewriteHello) args.shift();
const [certFile, keyFile, listenPort, target] = args;
if (!target) {
  console.error('usage: node tls-proxy.mjs [--rewrite-hello] <cert.pem> <key.pem> <listen-port> <target-origin>');
  process.exit(2);
}
const upstream = new URL(target);

function forwarded(req) {
  const prior = req.headers['x-forwarded-for'];
  const addr = req.socket.remoteAddress;
  return { ...req.headers, 'x-forwarded-proto': 'https', 'x-forwarded-for': prior ? `${prior}, ${addr}` : addr };
}

const server = https.createServer({ cert: fs.readFileSync(certFile), key: fs.readFileSync(keyFile) }, (req, res) => {
  const out = http.request(
    { host: upstream.hostname, port: upstream.port, method: req.method, path: req.url, headers: forwarded(req) },
    up => {
      res.writeHead(up.statusCode, up.rawHeaders);
      up.pipe(res);
    }
  );
  out.on('error', err => {
    if (!res.headersSent) res.writeHead(502, { 'content-type': 'text/plain' });
    res.end(`upstream error: ${err.message}\n`);
  });
  req.pipe(out);
});

// firstFrame reads one unmasked server frame from the start of buf: its length in buf, and the
// frame to send instead, which differs only for a text frame holding the RTDB hello. It returns
// null until buf holds the whole frame.
function firstFrame(buf, host) {
  if (buf.length < 2) return null;
  let len = buf[1] & 0x7f;
  let off = 2;
  if (len === 126) {
    if (buf.length < 4) return null;
    len = buf.readUInt16BE(2);
    off = 4;
  } else if (len === 127) {
    if (buf.length < 10) return null;
    len = Number(buf.readBigUInt64BE(2));
    off = 10;
  }
  if (buf[1] & 0x80) off += 4; // masked: not from a server, left alone below
  if (buf.length < off + len) return null;
  const frame = buf.subarray(0, off + len);
  if (buf[0] !== 0x81 || buf[1] & 0x80) return { length: frame.length, bytes: frame }; // only FIN text, no RSV bits
  let msg;
  try {
    msg = JSON.parse(frame.subarray(off).toString('utf8'));
  } catch {
    return { length: frame.length, bytes: frame };
  }
  if (msg?.t !== 'c' || msg.d?.t !== 'h' || typeof msg.d.d?.h !== 'string') return { length: frame.length, bytes: frame };
  console.error(`hello: "h" ${msg.d.d.h} -> ${host}`);
  msg.d.d.h = host;
  const payload = Buffer.from(JSON.stringify(msg), 'utf8');
  const head = payload.length < 126 ? Buffer.from([0x81, payload.length]) : Buffer.from([0x81, 126, payload.length >> 8, payload.length & 0xff]);
  return { length: frame.length, bytes: Buffer.concat([head, payload]) };
}

// An upgrade request goes to the target as written, headers and all; from the target's answer
// on, the two sockets are piped both ways (after the first frame, with --rewrite-hello).
server.on('upgrade', (req, client, head) => {
  const up = net.connect(Number(upstream.port), upstream.hostname, () => {
    const lines = [`${req.method} ${req.url} HTTP/1.1`];
    for (const [name, value] of Object.entries(forwarded(req))) {
      for (const v of Array.isArray(value) ? value : [value]) lines.push(`${name}: ${v}`);
    }
    up.write(lines.join('\r\n') + '\r\n\r\n');
    if (head.length) up.write(head);
    client.pipe(up);
    if (!rewriteHello) return up.pipe(client);
    let pending = Buffer.alloc(0);
    let state = 'head';
    up.on('data', chunk => {
      if (state === 'pipe') return client.write(chunk);
      pending = Buffer.concat([pending, chunk]);
      if (state === 'head') {
        const end = pending.indexOf('\r\n\r\n');
        if (end < 0) return;
        client.write(pending.subarray(0, end + 4));
        pending = pending.subarray(end + 4);
        state = 'frame';
      }
      const frame = firstFrame(pending, req.headers.host);
      if (!frame) return;
      client.write(frame.bytes);
      if (pending.length > frame.length) client.write(pending.subarray(frame.length));
      state = 'pipe';
    });
    up.on('end', () => client.end());
  });
  const close = () => {
    client.destroy();
    up.destroy();
  };
  up.on('error', close);
  client.on('error', close);
  up.on('close', close);
  client.on('close', close);
});

server.listen(Number(listenPort), '127.0.0.1', () => {
  console.log(`READY https://127.0.0.1:${server.address().port}`);
});
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => process.exit(0));
