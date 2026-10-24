// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Sends the official Firebase web Auth SDK, in its normal production mode (no connectAuthEmulator),
// to an Aruma server. Aruma serves Google's exact paths under its own host (ADR-014), so the
// redirect only rewrites the host of the two Auth hosts:
//
//   https://identitytoolkit.googleapis.com/<path> → <url>/identitytoolkit.googleapis.com/<path>
//   https://securetoken.googleapis.com/<path>     → <url>/securetoken.googleapis.com/<path>
//
// Firebase Auth on the web sends every request with fetch, which it looks up on each call
// (FetchProvider: self.fetch, then globalThis.fetch). Wrapping globalThis.fetch therefore reaches
// it whether this runs before or after firebase/auth is imported. Every other request passes
// through untouched, but one: a Firebase Storage SDK request to Google's Storage host while Auth is
// redirected (SR3-06). @firebase/storage takes the ID token from the app's Auth whatever its host
// (FirebaseStorageImpl._getAuthToken) and sends it as `Authorization: Firebase <token>` with every
// request (addAuthHeader_), so with Auth on Aruma and Storage left on Google that request would carry
// an Aruma ID token to Google. It is refused instead (storageGuard below), unless the Storage
// fallback of storage.js is installed and takes it.

/** The hosts that are rewritten. Nothing else is. */
export const AUTH_HOSTS = Object.freeze(['identitytoolkit.googleapis.com', 'securetoken.googleapis.com']);

const STATE = Symbol.for('aruma.web.redirect');
// storage.js's state: when its fallback is installed, it handles Storage requests to Google's host.
const STORAGE_STATE = Symbol.for('aruma.web.storage');
const XHR_WRAPPED = Symbol.for('aruma.web.redirect.xhr');
const XHR_GUARD = Symbol('aruma.web.redirect.xhr.guard');
const LOG_PREFIX = '[aruma]';

/** An Aruma URL that was refused. The message never contains the URL itself. */
export class ArumaConfigError extends Error {
  constructor(message) {
    super(message);
    this.name = 'ArumaConfigError';
  }
}

/** `localhost`, `127.0.0.0/8` and `::1`, as a WHATWG URL spells them. */
export function isLoopback(hostname) {
  const h = String(hostname).toLowerCase().replace(/^\[(.*)\]$/, '$1');
  if (h === 'localhost' || h === '::1') return true;
  const m = /^127\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(h);
  return m !== null && m.slice(1).every((octet) => Number(octet) <= 255);
}

/**
 * Validates the Aruma server URL: an absolute https URL with a host and an optional path prefix,
 * no query, fragment or credentials. http is accepted only for a loopback host, or with
 * allowInsecure (development only).
 *
 * @returns {{ origin: string, prefix: string, href: string, protocol: string, host: string }}
 * @throws {ArumaConfigError}
 */
export function parseBase(url, { allowInsecure = false } = {}) {
  const text = url instanceof URL ? url.href : typeof url === 'string' ? url.trim() : '';
  if (text === '') throw new ArumaConfigError('the Aruma URL is empty');
  let u;
  try {
    u = new URL(text);
  } catch {
    throw new ArumaConfigError('the Aruma URL is not an absolute URL');
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') {
    throw new ArumaConfigError('the Aruma URL must use https');
  }
  if (!u.hostname || u.username || u.password || u.search || u.hash || /[?#]/.test(text)) {
    throw new ArumaConfigError('the Aruma URL must have a host and no credentials, query or fragment');
  }
  if (AUTH_HOSTS.includes(stripDot(u.hostname))) {
    throw new ArumaConfigError('the Aruma URL cannot be a Google Auth host');
  }
  if (u.protocol === 'http:' && !allowInsecure && !isLoopback(u.hostname)) {
    throw new ArumaConfigError(
      `the Aruma URL uses http to a host that is not loopback (${u.host}); use https, or allowInsecure for development`,
    );
  }
  const prefix = u.pathname.replace(/\/+$/, '');
  return { origin: u.origin, prefix, href: u.origin + prefix, protocol: u.protocol.slice(0, -1), host: u.host };
}

const stripDot = (hostname) => (hostname.endsWith('.') ? hostname.slice(0, -1) : hostname);

/**
 * The Auth endpoint a URL names, or null when it is not one: https on the default port to one of
 * AUTH_HOSTS, without credentials. Relative and unparsable URLs are never Auth endpoints.
 */
export function matchAuth(url) {
  let u;
  try {
    u = new URL(String(url));
  } catch {
    return null;
  }
  // The WHATWG parser lowercases the host, drops :443 and resolves dot segments (also %2e%2e).
  const host = stripDot(u.hostname);
  if (u.protocol !== 'https:' || u.port !== '' || u.username || u.password || !AUTH_HOSTS.includes(host)) return null;
  return { host, path: u.pathname, search: u.search };
}

/**
 * `https://<auth host><path>?<query>` → `<base>/<auth host><path>?<query>`, or null when the URL
 * is not an Auth endpoint. `base` is a URL string or the result of parseBase.
 */
export function rewriteURL(url, base) {
  const b = typeof base === 'string' ? parseBase(base, { allowInsecure: true }) : base;
  const m = matchAuth(url);
  return m && `${b.href}/${m.host}${m.path}${m.search}`;
}

function isRequest(input) {
  if (typeof Request !== 'undefined' && input instanceof Request) return true;
  return input !== null && typeof input === 'object' && typeof input.url === 'string' && typeof input.clone === 'function';
}

function inputURL(input) {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.href;
  if (isRequest(input)) return input.url;
  try {
    return String(input);
  } catch {
    return null;
  }
}

// A Request is rebuilt on the new URL with the same method, headers, body, signal and options.
// The body is read into memory: a stream body would need `duplex`, and Auth bodies are small.
async function forwardRequest(original, global, input, init, dest) {
  const req = init === undefined ? input : new Request(input, init);
  const body = req.body === null || req.method === 'GET' || req.method === 'HEAD' ? undefined : await req.arrayBuffer();
  const out = new Request(dest, {
    method: req.method,
    headers: req.headers,
    body,
    signal: req.signal,
    credentials: req.credentials,
    cache: req.cache,
    redirect: req.redirect,
    referrer: req.referrer,
    referrerPolicy: req.referrerPolicy,
    integrity: req.integrity,
    keepalive: req.keepalive,
    mode: req.mode === 'navigate' ? 'same-origin' : req.mode,
  });
  return original.call(global, out);
}

// ---------------------------------------------------------------------------------------------
// The Storage guard (SR3-06).

/** Google's Storage host. Not the bare quoted literal, which the build's output check looks for. */
const STORAGE_HOST = new URL('https://firebasestorage.googleapis.com/').hostname;

// The headers every @firebase/storage request carries (X-Firebase-Storage-Version) or the token
// rides in (Authorization). A plain fetch of a download link has neither and is left alone.
const SDK_HEADERS = new Set(['authorization', 'x-firebase-storage-version']);

const storageBlocked = `a Firebase Storage request to ${STORAGE_HOST} was blocked: Firebase Auth is redirected to Aruma but Storage is not, and the request would carry the Aruma ID token to Google. Enable Storage too (Vite: aruma({ url, storage: true }); other bundlers: arumaStorage from @aruma/web/unplugin; or the fallback, installStorage from @aruma/web/storage)`;

function onGoogleStorage(href) {
  try {
    return stripDot(new URL(String(href)).hostname) === STORAGE_HOST;
  } catch {
    return false;
  }
}

// Whether the guard is on in this global: Auth redirected (or refused), and storage.js not taking
// Storage requests to Google's host itself.
function guarding(global) {
  const auth = stateOf(global);
  if (!auth || auth.mode === null) return false;
  const storage = Object.prototype.hasOwnProperty.call(global, STORAGE_STATE) ? global[STORAGE_STATE] : null;
  return !storage || storage.mode === null;
}

function reportBlocked(global) {
  const state = stateOf(global);
  if (state && !state.storageWarned) {
    state.storageWarned = true;
    console.error(`${LOG_PREFIX} ${storageBlocked}`);
  }
  return new TypeError(`${LOG_PREFIX} ${storageBlocked}`);
}

function hasSDKHeader(headers) {
  if (headers === undefined || headers === null || typeof Headers === 'undefined') return false;
  let h;
  try {
    h = new Headers(headers);
  } catch {
    return false;
  }
  for (const name of SDK_HEADERS) if (h.has(name)) return true;
  return false;
}

/** A fetch the guard refuses: a Storage SDK request to Google's host. */
function refusedFetch(global, href, input, init) {
  if (href === null || !onGoogleStorage(href) || !guarding(global)) return false;
  return hasSDKHeader(init?.headers) || (isRequest(input) && hasSDKHeader(input.headers));
}

// The browser build of @firebase/storage sends with XMLHttpRequest: open(method, url), then
// setRequestHeader for each header, then send (platform/browser/connection.ts). An XHR opened on
// Google's Storage host that gets one of the SDK's headers is never sent: the token header is
// dropped, and send() fails it as a network error, which the SDK reports (a throw would escape its
// retry timer). Wrapped once per prototype; the guard reads the global's state on each call.
function guardXHR(global) {
  const proto = global.XMLHttpRequest?.prototype;
  if (!proto?.open || !proto.send || !proto.setRequestHeader || Object.prototype.hasOwnProperty.call(proto, XHR_WRAPPED)) return;
  const { open, setRequestHeader, send } = proto;
  Object.defineProperty(proto, XHR_WRAPPED, { value: true });
  proto.open = function open_(method, href, ...rest) {
    this[XHR_GUARD] = onGoogleStorage(href) && guarding(global) ? { refuse: false } : null;
    return open.call(this, method, href, ...rest);
  };
  proto.setRequestHeader = function setRequestHeader_(name, value) {
    const g = this[XHR_GUARD];
    if (g && SDK_HEADERS.has(String(name).toLowerCase())) {
      g.refuse = true;
      if (String(name).toLowerCase() === 'authorization') return undefined;
    }
    return setRequestHeader.call(this, name, value);
  };
  proto.send = function send_(...args) {
    if (!this[XHR_GUARD]?.refuse) return send.apply(this, args);
    reportBlocked(global);
    setTimeout(() => this.dispatchEvent(new (global.ProgressEvent ?? Event)('error')), 0);
    return undefined;
  };
}

function makeWrapper(global, state) {
  const wrapped = function fetch(input, init) {
    const href = inputURL(input);
    if (refusedFetch(global, href, input, init)) return Promise.reject(reportBlocked(global));
    const m = href === null ? null : matchAuth(href);
    if (m === null || state.mode === null) return state.original.call(global, input, init);
    if (state.mode.error) {
      // Configured but refused: fail the Auth request rather than send it to Google.
      return Promise.reject(new TypeError(`${LOG_PREFIX} Firebase Auth request blocked: ${state.mode.error}`));
    }
    const { base } = state.mode;
    const dest = `${base.href}/${m.host}${m.path}${m.search}`;
    if (state.log) {
      const method = (init?.method ?? (isRequest(input) ? input.method : 'GET')).toUpperCase();
      // Method, host and path only: the query holds the API key, and headers and bodies hold tokens.
      console.info(`${LOG_PREFIX} ${method} https://${m.host}${m.path} -> ${base.href}/${m.host}${m.path}`);
    }
    if (isRequest(input)) return forwardRequest(state.original, global, input, init, dest);
    return state.original.call(global, dest, init);
  };
  wrapped[STATE] = true;
  return wrapped;
}

function stateOf(global) {
  return Object.prototype.hasOwnProperty.call(global, STATE) ? global[STATE] : null;
}

function wrapOnce(global) {
  let state = stateOf(global);
  if (state) return state;
  if (typeof global.fetch !== 'function') {
    throw new ArumaConfigError('there is no global fetch to wrap');
  }
  state = { original: global.fetch, mode: null, log: false, wrapper: null };
  state.wrapper = makeWrapper(global, state);
  Object.defineProperty(global, STATE, { value: state, configurable: true });
  global.fetch = state.wrapper;
  guardXHR(global);
  return state;
}

/**
 * Rewrites Firebase Auth requests to the Aruma server at `url`. Call it before the app sends its
 * first Auth request; before or after importing firebase/auth both work in the browser.
 *
 * - Not configured (`url` missing or empty): does nothing and returns false.
 * - Valid: wraps globalThis.fetch once (idempotent: a second call only updates the settings) and
 *   returns true.
 * - Refused (not https to a non-loopback host, query, credentials…): Auth requests are made to fail
 *   instead of reaching Google, and an ArumaConfigError is thrown.
 *
 * Either way, while it is installed, a Firebase Storage request to Google's Storage host fails
 * instead of carrying the Aruma ID token there, unless Storage is rewritten or installStorage runs.
 *
 * @param {{ url?: string | URL, allowInsecure?: boolean, log?: boolean }} [options]
 */
export function install({ url, allowInsecure = false, log = false, globalObject = globalThis } = {}) {
  if (url === undefined || url === null || (typeof url === 'string' && url.trim() === '')) return false;
  let mode;
  let failure = null;
  try {
    mode = { base: parseBase(url, { allowInsecure }) };
  } catch (e) {
    if (!(e instanceof ArumaConfigError)) throw e;
    mode = { error: e.message };
    failure = e;
  }
  const state = wrapOnce(globalObject);
  state.mode = mode;
  state.log = Boolean(log);
  if (failure) {
    console.error(`${LOG_PREFIX} refused: ${failure.message}. Firebase Auth requests will fail instead of reaching Google`);
    throw failure;
  }
  if (state.log) console.info(`${LOG_PREFIX} installed: Firebase Auth requests go to ${mode.base.href}`);
  return true;
}

/**
 * The installed redirect: `{ url }` when active, `{ blocked: reason }` when configured but refused,
 * or null when not installed.
 */
export function status({ globalObject = globalThis } = {}) {
  const state = stateOf(globalObject);
  if (!state || state.mode === null) return null;
  return state.mode.error ? { blocked: state.mode.error } : { url: state.mode.base.href };
}

/**
 * Removes the redirect. The original fetch is restored when the wrapper is still the global
 * fetch; otherwise the wrapper stays in place and passes everything through.
 */
export function uninstall({ globalObject = globalThis } = {}) {
  const state = stateOf(globalObject);
  if (!state) return;
  state.mode = null;
  state.log = false;
  if (globalObject.fetch === state.wrapper) {
    globalObject.fetch = state.original;
    delete globalObject[STATE];
  }
}

/**
 * Installs from a configuration object such as `globalThis.__ARUMA__`. Unlike install, a refused URL
 * does not throw (the error is logged once and Auth requests fail), so a side-effect import can never
 * stop the app from loading. Returns what install returns, or false when refused.
 */
export function installFromConfig(config, { globalObject = globalThis } = {}) {
  if (config === undefined || config === null) return false;
  const options = typeof config === 'string' || config instanceof URL ? { url: config } : config;
  try {
    return install({
      url: options.url,
      allowInsecure: truthy(options.allowInsecure),
      log: truthy(options.log),
      globalObject,
    });
  } catch (e) {
    if (e instanceof ArumaConfigError) return false;
    throw e;
  }
}

const truthy = (v) => v === true || (typeof v === 'string' && ['1', 'true', 'yes'].includes(v.toLowerCase()));
