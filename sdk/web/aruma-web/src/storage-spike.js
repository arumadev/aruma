// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// SPIKE (S0-B, docs/research/storage-production-path.md). Not the S6 implementation: no tests,
// no build mode, no Vite integration. It exists to measure the smallest change that sends the
// official firebase/storage, in production mode, to an Aruma server.
//
//   https://firebasestorage.googleapis.com/<path> → <url>/firebasestorage.googleapis.com/<path>
//
// firebase/storage in the browser sends every request with XMLHttpRequest
// (packages/storage/src/platform/browser/connection.ts: `new XMLHttpRequest()`, then
// `xhr.open(method, url, true)`), which the fetch wrapper of index.js never sees. Patching
// XMLHttpRequest.prototype.open reaches every XHR, whenever it was created. Its Node build uses
// fetch, and so does code that fetches a download URL, so fetch is wrapped too.
import { parseBase } from './index.js';

export const STORAGE_HOST = 'firebasestorage.googleapis.com';
const MARK = Symbol.for('aruma.web.storage-spike');

/** `https://firebasestorage.googleapis.com/<path>?<query>` → `{ path, search }`, else null. */
export function matchStorage(url) {
  let u;
  try {
    u = new URL(String(url));
  } catch {
    return null;
  }
  const host = u.hostname.endsWith('.') ? u.hostname.slice(0, -1) : u.hostname;
  if (u.protocol !== 'https:' || u.port !== '' || u.username || u.password || host !== STORAGE_HOST) return null;
  return { path: u.pathname, search: u.search };
}

/**
 * Installs the redirect for Storage requests (XMLHttpRequest and fetch). Returns the state, or
 * throws an ArumaConfigError for a refused URL.
 *
 * @param {{ url: string | URL, allowInsecure?: boolean, log?: boolean, globalObject?: object }} options
 */
export function installStorage({ url, allowInsecure = false, log = false, globalObject = globalThis }) {
  const base = parseBase(url, { allowInsecure });
  const rewrite = (href) => {
    const m = matchStorage(href);
    if (!m) return null;
    const dest = `${base.href}/${STORAGE_HOST}${m.path}${m.search}`;
    if (log) console.info(`[aruma] storage ${m.path} -> ${base.href}/${STORAGE_HOST}${m.path}`);
    return dest;
  };
  const X = globalObject.XMLHttpRequest;
  if (X && !X.prototype.open[MARK]) {
    const open = X.prototype.open;
    const wrapped = function open_(method, href, ...rest) {
      return open.call(this, method, rewrite(href) ?? href, ...rest);
    };
    wrapped[MARK] = true;
    X.prototype.open = wrapped;
  }
  const f = globalObject.fetch;
  if (typeof f === 'function' && !f[MARK]) {
    const wrapped = function fetch(input, init) {
      const href = typeof input === 'string' ? input : input instanceof URL ? input.href : input?.url;
      const dest = href ? rewrite(href) : null;
      if (!dest) return f.call(globalObject, input, init);
      if (typeof input === 'string' || input instanceof URL) return f.call(globalObject, dest, init);
      return f.call(globalObject, new Request(dest, new Request(input, init)));
    };
    wrapped[MARK] = true;
    globalObject.fetch = wrapped;
  }
  return { url: base.href };
}
