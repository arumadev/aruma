// The page server for the e2e: a directory (the Flutter web build) on localhost, another origin
// than Aruma's 127.0.0.1:<port>, as an app and its backend usually are.
//
//   node serve.mjs <port> <dir>
import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { extname, join, normalize, resolve } from 'node:path';

const port = Number(process.argv[2]);
const root = resolve(process.argv[3]);
const types = {
  '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.json': 'application/json',
  '.css': 'text/css', '.wasm': 'application/wasm', '.png': 'image/png', '.ico': 'image/x-icon',
  '.svg': 'image/svg+xml', '.ttf': 'font/ttf', '.otf': 'font/otf', '.bin': 'application/octet-stream',
  '.frag': 'application/octet-stream', '.symbols': 'text/plain',
};

async function file(path) {
  const s = await stat(path).catch(() => null);
  if (s?.isDirectory()) return file(join(path, 'index.html'));
  return s?.isFile() ? path : null;
}

createServer(async (req, res) => {
  const { pathname } = new URL(req.url, 'http://localhost');
  const target = normalize(join(root, decodeURIComponent(pathname)));
  const found = target.startsWith(root) ? await file(target) : null;
  if (!found) return res.writeHead(404).end('not found');
  res.writeHead(200, { 'content-type': types[extname(found)] ?? 'application/octet-stream', 'cache-control': 'no-store' });
  res.end(await readFile(found));
}).listen(port, 'localhost');
