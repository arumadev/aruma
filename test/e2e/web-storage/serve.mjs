// The page server: the built apps under /<name>/ (dist/<name>/, or a directory given as
// name=dir), and /e2e.json, the server's URL and web config that server/main.go wrote.
//
//   node serve.mjs <port> [name=dir …]
import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { extname, join, normalize, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('.', import.meta.url));
const port = Number(process.argv[2] ?? 9421);
const roots = Object.fromEntries(
  process.argv.slice(3).map((a) => {
    const [name, dir] = a.split('=');
    return [name, resolve(dir)];
  }),
);
const generated = process.env.ARUMA_E2E_DIR ?? join(here, '.generated');
const types = {
  '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.json': 'application/json',
  '.css': 'text/css', '.wasm': 'application/wasm', '.png': 'image/png', '.ico': 'image/x-icon', '.svg': 'image/svg+xml',
  '.ttf': 'font/ttf', '.otf': 'font/otf', '.map': 'application/json',
};

async function file(path) {
  const s = await stat(path).catch(() => null);
  if (s?.isDirectory()) return file(join(path, 'index.html'));
  return s?.isFile() ? path : null;
}

createServer(async (req, res) => {
  const { pathname } = new URL(req.url, 'http://localhost');
  if (pathname === '/e2e.json') {
    return res.writeHead(200, { 'content-type': 'application/json' }).end(await readFile(join(generated, 'e2e.json')));
  }
  const [, name, ...rest] = decodeURIComponent(pathname).split('/');
  const root = roots[name] ?? join(here, 'dist', name);
  const target = normalize(join(root, ...rest));
  const found = target.startsWith(root) ? await file(target) : null;
  if (!found) return res.writeHead(404).end('not found');
  res.writeHead(200, { 'content-type': types[extname(found)] ?? 'application/octet-stream', 'cache-control': 'no-store' });
  res.end(await readFile(found));
}).listen(port, '127.0.0.1');
