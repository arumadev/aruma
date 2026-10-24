// Serves the static export in out/ the way the Go server does (internal/admin/web): directory
// paths answer with their index.html, everything else is a file, and every page carries the
// same Content-Security-Policy, with the hash of each inline script and the style hashes from
// csp.json. With ARUMA_API_URL set, /console/v1/ is proxied to a running `aruma serve`, for the
// live end-to-end tests.
import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { readFile, stat } from 'node:fs/promises';
import { createServer, request as httpRequest } from 'node:http';
import { extname, join, normalize, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('../out/', import.meta.url));
const PORT = Number(process.env.PORT ?? 4173);
const API = process.env.ARUMA_API_URL ? new URL(process.env.ARUMA_API_URL) : null;

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.txt': 'text/plain; charset=utf-8',
  '.json': 'application/json',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
};

const styleHashes = JSON.parse(await readFile(join(ROOT, 'csp.json'), 'utf8')).styleSrcElem;

/**
 * The page's policy, built like the Go server's (internal/admin/web): no inline script or style
 * without a hash, and scripts, stylesheets and fonts from <host>/_next/static/ alone, never
 * 'self', where Storage serves user files.
 */
function policy(html, host) {
  const scripts = new Set();
  for (const [, attrs = '', body] of html.matchAll(/<script(\s[^>]*)?>([\s\S]*?)<\/script>/g)) {
    if (attrs.includes('src=') || !body.trim()) continue;
    scripts.add(`'sha256-${createHash('sha256').update(body).digest('base64')}'`);
  }
  const statics = /^[A-Za-z0-9.-]+(:\d{1,5})?$/.test(host ?? '') ? `${host}/_next/static/` : "'none'";
  return [
    "default-src 'none'",
    `script-src ${statics} ${[...scripts].join(' ')}`,
    `style-src ${statics} ${styleHashes.join(' ')}`,
    `font-src ${statics}`,
    "img-src 'self' data:",
    "connect-src 'self'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'self'",
    "frame-ancestors 'none'",
  ].join('; ');
}

async function sendHtml(req, res, file, status) {
  const html = await readFile(file, 'utf8');
  res.writeHead(status, { 'Content-Type': TYPES['.html'], 'Cache-Control': 'no-store', 'Content-Security-Policy': policy(html, req.headers.host) });
  res.end(html);
}

async function resolveFile(pathname) {
  const decoded = decodeURIComponent(pathname);
  const target = normalize(join(ROOT, decoded));
  if (target !== ROOT.slice(0, -1) && !target.startsWith(ROOT)) return null;
  const candidate = decoded.endsWith('/') ? join(target, 'index.html') : target;
  try {
    const info = await stat(candidate);
    if (info.isFile()) return candidate;
    if (info.isDirectory()) return { redirect: `${pathname}/` };
  } catch {
    // Not found.
  }
  return null;
}

function proxy(req, res) {
  const upstream = httpRequest(
    { host: API.hostname, port: API.port, method: req.method, path: req.url, headers: { ...req.headers, host: API.host } },
    (up) => {
      res.writeHead(up.statusCode ?? 502, up.headers);
      up.pipe(res);
    },
  );
  upstream.on('error', () => {
    res.writeHead(502).end();
  });
  req.pipe(upstream);
}

createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', 'http://localhost');
  // /v0 too: the dashboard's uploads send their chunks to the session URLs the console issues.
  if (url.pathname.startsWith('/console/v1/') || (API && url.pathname.startsWith('/v0/'))) {
    if (API) return proxy(req, res);
    return res.writeHead(404, { 'Content-Type': 'application/json' }).end('{"error":{"code":404,"status":"NOT_FOUND","message":"No API."}}');
  }
  const file = await resolveFile(url.pathname);
  if (file?.redirect) return res.writeHead(308, { Location: file.redirect + url.search }).end();
  if (!file) return sendHtml(req, res, join(ROOT, '404.html'), 404);
  if (file.endsWith('.html')) return sendHtml(req, res, file, 200);
  res.writeHead(200, { 'Content-Type': TYPES[extname(file)] ?? 'application/octet-stream', 'Cache-Control': 'no-store' });
  createReadStream(file).pipe(res);
}).listen(PORT, '127.0.0.1', () => {
  console.log(`Serving ${ROOT.split(sep).slice(-2).join('/')} on http://127.0.0.1:${PORT}${API ? ` (API: ${API.origin})` : ''}`);
});
