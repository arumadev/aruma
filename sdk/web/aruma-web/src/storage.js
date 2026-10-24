// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Firebase Storage on the web (ADR-022 §16.1). Aruma serves the Firebase Storage REST protocol from
// the root of its host, so the one download link every platform produces is
//
//   https://<aruma host>/v0/b/<bucket>/o/<path>?alt=media&token=<token>
//
// and the SDK builds it from its own host string (makeUrl(urlPart, storage.host, storage._protocol)).
// Two ways to give it Aruma's host:
//
// - The build-time rewrite (the default, unplugin.js, metro.js, flutter-web.js): the one definition
//   DEFAULT_HOST = 'firebasestorage.googleapis.com' in each @firebase/storage dist file becomes
//   Aruma's host, and `this._protocol = 'https'` becomes 'http' when Aruma is on http. The SDK then
//   sends every request to Aruma and returns links on Aruma, which work in <img>, <video>, CSS and
//   anywhere they are stored or shared. rewriteStorageLiterals below is that rule.
// - The fallback, for a build that cannot be rewritten: useArumaStorage(storage) sets the same two
//   fields on one instance at run time, and installStorage() redirects the requests that still go
//   to Google's host (XMLHttpRequest in the browser build, fetch in the Node build and for links).
//   A link made before useArumaStorage still names Google; toArumaUrl rewrites it.
import { ArumaConfigError, parseBase } from './index.js';

/** The host @firebase/storage compiles in. */
// Not written as the bare quoted literal: the build's output check takes a bundle holding the SDK
// and that literal for an unrewritten SDK, and this module may share the SDK's chunk.
export const STORAGE_HOST = new URL('https://firebasestorage.googleapis.com/').hostname;

const LOG_PREFIX = '[aruma]';
const STATE = Symbol.for('aruma.web.storage');
const BLOCKED = Symbol('aruma.web.storage.blocked');

/**
 * Validates the Aruma URL for Storage: parseBase's rules, and no path, because Storage is served
 * from the root of the host (the unified link has /v0 right after the host, which the web, iOS and
 * Dart parsers all require).
 *
 * @returns {{ origin: string, prefix: string, href: string, protocol: string, host: string }}
 * @throws {ArumaConfigError}
 */
export function storageBase(url, { allowInsecure = false } = {}) {
  const base = parseBase(url, { allowInsecure });
  if (base.prefix !== '') {
    throw new ArumaConfigError('the Aruma URL for Storage must be the root of its host (no path): Storage links are https://<host>/v0/b/…');
  }
  if (stripDot(new URL(base.origin).hostname).endsWith('googleapis.com')) {
    throw new ArumaConfigError('the Aruma URL cannot be a Google host');
  }
  // The host goes into a JS string literal of the SDK: only a host name, an IPv4 address or a
  // bracketed IPv6 address, and a port (the WHATWG parser lets through characters such as quotes).
  if (!HOST_SHAPE.test(base.host)) {
    throw new ArumaConfigError('the Aruma URL for Storage must have a plain host: letters, digits, dots and hyphens, or an IP address, and an optional port');
  }
  return base;
}

const HOST_SHAPE = /^(?:[a-z0-9.-]+|\[[0-9a-f:.]+\])(?::[0-9]{1,5})?$/;

const stripDot = (hostname) => (hostname.endsWith('.') ? hostname.slice(0, -1) : hostname);

// ---------------------------------------------------------------------------------------------
// The build-time rule.

// Every dist file of @firebase/storage: the ESM, CJS, browser-CJS and both Node builds, also when a
// package manager nests it (pnpm's node_modules/.pnpm/@firebase+storage@…/node_modules/@firebase/…).
const STORAGE_DIST = /[\\/]@firebase[\\/]storage[\\/]dist[\\/](?:.*[\\/])?[^\\/]+\.(?:m?js|cjs)$/;

/** True for a module id (a path, maybe with a ?query) of one of @firebase/storage's dist files. */
export function isStorageModule(id) {
  return STORAGE_DIST.test(String(id).split('?')[0]);
}

/**
 * The Firebase JS SDK from a CDN, which never passes through a bundler, so it is never rewritten:
 * anything from www.gstatic.com/firebasejs/ (also protocol-relative), and the Storage SDK from any
 * absolute URL (firebase-storage.js, @firebase/storage or firebase@…/storage on jsdelivr, unpkg,
 * esm.sh …). A relative or root-relative import is local and not matched.
 */
export const GSTATIC_FIREBASE =
  /(?:https?:)?\/\/(?:www\.gstatic\.com\/firebasejs\/|[^\s"'`()<>]*?(?:\/firebase-storage(?:-compat)?(?:\.min)?\.js|\/@firebase\/storage|\/firebase@[^\/\s"'`]*\/(?:compat\/)?storage))/;

const HOST_LITERAL = /(["'])firebasestorage\.googleapis\.com\1/g;
// `this._protocol = 'https';` in the constructor of FirebaseStorageImpl (`this._protocol="https"`
// minified). connectStorageEmulator's `_protocol = useSsl ? 'https' : 'http'` does not match.
const PROTOCOL_LITERAL = /(_protocol\s*=\s*)(["'])https\2/g;

/**
 * The build-time rewrite of one @firebase/storage module: the quoted host literal (the definition
 * of DEFAULT_HOST; the host also appears unquoted in a comment, which stays) becomes Aruma's host,
 * and, when Aruma is on http, the default protocol becomes 'http'.
 *
 * It returns the counts; the caller fails unless each is exactly one (`ok`). A module where the
 * literal is spelled differently, or appears twice, is a release this rule does not know.
 *
 * @param {string} code
 * @param {{ host: string, protocol: string }} base from storageBase
 * @returns {{ code: string, hosts: number, schemes: number, ok: boolean }}
 */
export function rewriteStorageLiterals(code, base) {
  let hosts = 0;
  let schemes = 0;
  let out = code.replace(HOST_LITERAL, () => {
    hosts++;
    return JSON.stringify(base.host);
  });
  if (base.protocol !== 'https') {
    out = out.replace(PROTOCOL_LITERAL, (_, key) => {
      schemes++;
      return `${key}${JSON.stringify(base.protocol)}`;
    });
  }
  const ok = hosts === 1 && (base.protocol === 'https' || schemes === 1);
  return { code: out, hosts, schemes, ok };
}

/** Why a module was not rewritten, for the build error. */
export function literalProblem(id, r, base) {
  const want = base.protocol === 'https' ? 'the host literal once' : 'the host literal and the protocol literal once each';
  return `@aruma/web: expected ${want} in ${id}, found ${r.hosts} host and ${r.schemes} protocol literals; this @firebase/storage release is not supported by the build-time rewrite. Use the fallback (useArumaStorage) and report the version`;
}

/**
 * Whether the code of a @firebase/storage module, as it ends up in the bundle, names Aruma and not
 * Google: the check that runs on the final module set, whatever the bundler's cache did.
 */
export function storageModuleRewritten(code, base) {
  HOST_LITERAL.lastIndex = 0;
  if (HOST_LITERAL.test(code)) return false;
  return code.includes(`"${base.host}"`) || code.includes(`'${base.host}'`);
}

// ---------------------------------------------------------------------------------------------
// The fallback.

/** `https://firebasestorage.googleapis.com[:443]/<path>?<query>` → `{ path, search }`, else null. */
export function matchStorage(url) {
  let u;
  try {
    u = new URL(String(url));
  } catch {
    return null;
  }
  // The WHATWG parser lowercases the host and drops :443 (iOS writes it into its links).
  if (u.protocol !== 'https:' || u.port !== '' || u.username || u.password || stripDot(u.hostname) !== STORAGE_HOST) return null;
  return { path: u.pathname, search: u.search };
}

/**
 * The pure rule of the fallback: a Google Storage URL → the same path and query on Aruma's root,
 * or null when the URL is not a Google Storage URL. `base` is a URL string or storageBase's result.
 */
export function rewriteStorageURL(url, base) {
  const b = typeof base === 'string' ? storageBase(base, { allowInsecure: true }) : base;
  const m = matchStorage(url);
  return m && `${b.origin}${m.path}${m.search}`;
}

function stateOf(global) {
  return Object.prototype.hasOwnProperty.call(global, STATE) ? global[STATE] : null;
}

/**
 * A link on Google's host (from before useArumaStorage, from another platform, or stored) → the
 * unified link on Aruma. Anything else comes back unchanged. Without `base` it uses the URL given to
 * installStorage.
 */
export function toArumaUrl(url, base, { globalObject = globalThis } = {}) {
  const b = base ?? stateOf(globalObject)?.mode?.base;
  if (!b) throw new ArumaConfigError('toArumaUrl needs the Aruma URL: pass it, or call installStorage first');
  return rewriteStorageURL(url, b) ?? String(url);
}

function blockedError(reason) {
  return new TypeError(`${LOG_PREFIX} Firebase Storage request blocked: ${reason}`);
}

function wrapOnce(global) {
  let state = stateOf(global);
  if (state) return state;
  state = { mode: null, log: false };
  Object.defineProperty(global, STATE, { value: state, configurable: true });

  // The decision for one URL: the URL to use (null: leave it), or a thrown refusal.
  const decide = (href) => {
    if (state.mode === null) return null;
    const m = matchStorage(href);
    if (!m) return null;
    if (state.mode.error) throw blockedError(state.mode.error);
    const dest = `${state.mode.base.origin}${m.path}${m.search}`;
    // The path only: the query holds the download token.
    if (state.log) console.info(`${LOG_PREFIX} storage ${m.path} -> ${state.mode.base.origin}${m.path}`);
    return dest;
  };

  // The browser build sends every request with `new XMLHttpRequest()` then `xhr.open(method, url,
  // true)`, so the prototype reaches every request whenever the SDK was loaded.
  const X = global.XMLHttpRequest;
  if (X?.prototype?.open && X.prototype.send) {
    const { open, send } = X.prototype;
    X.prototype.open = function open_(method, href, ...rest) {
      let dest;
      try {
        dest = decide(href);
      } catch {
        // Refused: never open Google's URL. The SDK sets headers after open, so the request is
        // opened on an inert URL, and send() fails it as a network error, which the SDK reports
        // (a throw here would escape the SDK's retry timer and leave the operation hanging).
        this[BLOCKED] = true;
        return open.call(this, method, 'data:,', ...rest);
      }
      this[BLOCKED] = false;
      return open.call(this, method, dest ?? href, ...rest);
    };
    X.prototype.send = function send_(...args) {
      if (!this[BLOCKED]) return send.apply(this, args);
      setTimeout(() => this.dispatchEvent(new (global.ProgressEvent ?? Event)('error')), 0);
      return undefined;
    };
  }
  // The Node build sends with fetch, and so does an app that fetches a link.
  const f = global.fetch;
  if (typeof f === 'function') {
    global.fetch = function fetch(input, init) {
      const href = typeof input === 'string' ? input : input instanceof URL ? input.href : input?.url;
      let dest;
      try {
        dest = href ? decide(href) : null;
      } catch (e) {
        return Promise.reject(e);
      }
      if (!dest) return f.call(global, input, init);
      if (typeof input === 'string' || input instanceof URL) return f.call(global, dest, init);
      return f.call(global, new Request(dest, new Request(input, init)));
    };
  }
  return state;
}

/**
 * The fallback's request redirect: Storage requests to Google's host go to Aruma's root instead.
 * Call it first, before the app's first Storage request.
 *
 * - Not configured (`url` missing or empty): does nothing and returns false.
 * - Valid: wraps XMLHttpRequest.prototype.open and fetch once (a second call only updates the
 *   settings) and returns true.
 * - Refused: Storage requests to Google's host fail instead of being sent, and an
 *   ArumaConfigError is thrown.
 *
 * It does not change the links the SDK returns: useArumaStorage does.
 *
 * @param {{ url?: string | URL, allowInsecure?: boolean, log?: boolean, globalObject?: object }} [options]
 */
export function installStorage({ url, allowInsecure = false, log = false, globalObject = globalThis } = {}) {
  if (url === undefined || url === null || (typeof url === 'string' && url.trim() === '')) return false;
  let mode;
  let failure = null;
  try {
    mode = { base: storageBase(url, { allowInsecure }) };
  } catch (e) {
    if (!(e instanceof ArumaConfigError)) throw e;
    mode = { error: e.message };
    failure = e;
  }
  const state = wrapOnce(globalObject);
  state.mode = mode;
  state.log = Boolean(log);
  if (failure) {
    console.error(`${LOG_PREFIX} refused: ${failure.message}. Firebase Storage requests will fail instead of reaching Google`);
    throw failure;
  }
  return true;
}

/** The installed Storage redirect: `{ url }`, `{ blocked }`, or null. */
export function storageStatus({ globalObject = globalThis } = {}) {
  const state = stateOf(globalObject);
  if (!state || state.mode === null) return null;
  return state.mode.error ? { blocked: state.mode.error } : { url: state.mode.base.origin };
}

/** Turns the Storage redirect off; the wrappers stay and pass everything through. */
export function uninstallStorage({ globalObject = globalThis } = {}) {
  const state = stateOf(globalObject);
  if (state) {
    state.mode = null;
    state.log = false;
  }
}

/**
 * The fallback for links: points one FirebaseStorage instance (getStorage(app) or
 * getStorage(app, bucket)) at Aruma, so its requests and the links getDownloadURL returns are on
 * Aruma. It sets the instance's `host` (an internal setter of FirebaseStorageImpl, not in the public
 * types, which also recomputes the default bucket) and, when Aruma is on http, `_protocol`.
 *
 * Call it before the instance's first operation; each getStorage instance needs its own call.
 * Without `url` it uses the URL given to installStorage. Returns the instance.
 *
 * @template T
 * @param {T} storage
 * @param {{ url?: string | URL, allowInsecure?: boolean, globalObject?: object }} [options]
 * @returns {T}
 */
export function useArumaStorage(storage, { url, allowInsecure = false, globalObject = globalThis } = {}) {
  if (!storage || typeof storage !== 'object' || !('host' in storage) || !('_protocol' in storage)) {
    throw new ArumaConfigError('useArumaStorage needs the instance getStorage() returns');
  }
  let base;
  if (url !== undefined && url !== null && String(url).trim() !== '') {
    base = storageBase(url, { allowInsecure });
  } else {
    const mode = stateOf(globalObject)?.mode;
    if (mode?.error) throw new ArumaConfigError(mode.error);
    base = mode?.base;
    if (!base) throw new ArumaConfigError('useArumaStorage needs the Aruma URL: pass it, or call installStorage first');
  }
  if (storage._isUsingEmulator) {
    throw new ArumaConfigError('useArumaStorage: this instance is connected to the Storage emulator');
  }
  storage.host = base.host;
  storage._protocol = base.protocol;
  return storage;
}
