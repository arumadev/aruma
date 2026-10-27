// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// The Node half of the Firestore guard (SR3-10). The browser SDK reaches Google's Firestore host
// with fetch or XMLHttpRequest, which index.js wraps. The Node SDK (@firebase/firestore, node build)
// speaks gRPC over HTTP/2 instead (@grpc/grpc-js), and puts the ID token in the call's metadata, so
// fetch is never involved. Every way grpc-js 1.14 can reach the host is refused here, before any
// byte is written:
//
//   dns.lookup, dns.promises.lookup   the host name resolved (the first step of a direct connection)
//   net.Socket.prototype.connect      a TCP connection to the host name (net.connect, tls.connect)
//   tls.connect                       a TLS handshake naming the host (host, servername), over a socket too
//   http2.connect                     the HTTP/2 session whose authority is the host
//   http.request, https.request       a request to the host, or an HTTP CONNECT whose target is the host
//                                     (the path grpc-js takes through HTTP_PROXY / HTTPS_PROXY)
//
// Only the Firestore host (and its subdomains) is matched; nothing else is touched. The built-in
// modules are patched in place and syncBuiltinESMExports() makes `import { connect } from 'node:tls'`
// see the patched functions. A browser has no process.getBuiltinModule, so there this does nothing.
// A connection that is already open when install() runs is not closed.

const PATCHED = Symbol.for('aruma.web.nodeguard');

/**
 * @param {{ host: string, active: () => boolean, message: string, onBlocked?: () => void }} guard
 *   host: the guarded host; active: whether Auth is on Aruma right now; message: the error text.
 * @returns {boolean} whether the guard is (now) in place
 */
export function guardNode({ host, active, message, onBlocked }) {
  const proc = globalThis.process;
  if (typeof proc?.getBuiltinModule !== 'function' || !proc.versions?.node) return false;
  const get = (name) => proc.getBuiltinModule(name);
  const dns = get('node:dns');
  const net = get('node:net');
  const tls = get('node:tls');
  const http = get('node:http');
  const https = get('node:https');
  const http2 = get('node:http2');
  const mod = get('node:module');
  if (!dns || !net || !tls || !http || !https || !http2) return false;
  if (Object.prototype.hasOwnProperty.call(net, PATCHED)) return true;
  Object.defineProperty(net, PATCHED, { value: true });

  const isGuarded = (name) => {
    if (typeof name !== 'string') return false;
    const h = name.toLowerCase().replace(/\.$/, '');
    return (h === host || h.endsWith(`.${host}`)) && active();
  };
  // "host", "host:443", "https://host:443/", "[::1]:443" → the host name.
  const hostOf = (value) => {
    if (value instanceof URL) return value.hostname;
    if (typeof value !== 'string') return undefined;
    try {
      if (value.includes('://')) return new URL(value).hostname;
    } catch {
      return undefined;
    }
    const m = /^\[([^\]]*)\](?::\d+)?$/.exec(value) ?? /^([^:/]*)(?::\d+)?$/.exec(value);
    return m ? m[1] : undefined;
  };
  const refuse = () => {
    onBlocked?.();
    const e = new Error(`[aruma] ${message}`);
    e.code = 'ARUMA_FIRESTORE_BLOCKED';
    return e;
  };

  // dns.lookup(hostname, [options], callback) and dns.promises.lookup(hostname, [options]).
  const { lookup } = dns;
  dns.lookup = function lookup_(hostname, ...rest) {
    if (!isGuarded(hostname)) return lookup.call(this, hostname, ...rest);
    const cb = rest.find((a) => typeof a === 'function');
    const err = refuse();
    if (!cb) throw err;
    process.nextTick(cb, err);
    return {};
  };
  if (dns.promises?.lookup) {
    const plookup = dns.promises.lookup;
    dns.promises.lookup = function lookup_(hostname, ...rest) {
      return isGuarded(hostname) ? Promise.reject(refuse()) : plookup.call(this, hostname, ...rest);
    };
  }

  // Socket.prototype.connect(options | port, host | path, cb): net.connect and tls.connect end here.
  const connect = net.Socket.prototype.connect;
  net.Socket.prototype.connect = function connect_(...args) {
    // net.connect hands over the already normalized arguments as one array: [options, callback].
    const a = Array.isArray(args[0]) ? args[0][0] : args[0];
    const target = a !== null && typeof a === 'object' && !Array.isArray(a) ? (a.host ?? a.hostname) : typeof a === 'number' || /^\d+$/.test(String(a)) ? args[1] : undefined;
    if (!isGuarded(typeof target === 'string' ? target : undefined)) return connect.apply(this, args);
    this.destroy(refuse());
    return this;
  };

  // tls.connect(options) | (port, host, options) | (path, options); also over an existing socket.
  const tlsConnect = tls.connect;
  tls.connect = function connect_(...args) {
    const o = args.find((a) => a !== null && typeof a === 'object' && !(a instanceof net.Socket) && !Array.isArray(a));
    const positional = typeof args[0] === 'number' && typeof args[1] === 'string' ? args[1] : undefined;
    if ([o?.servername, o?.host, o?.hostname, positional].some(isGuarded)) throw refuse();
    return tlsConnect.apply(this, args);
  };

  // http2.connect(authority, [options], [listener])
  const h2connect = http2.connect;
  http2.connect = function connect_(authority, ...rest) {
    if (isGuarded(hostOf(authority))) throw refuse();
    return h2connect.call(this, authority, ...rest);
  };

  // http(s).request(url?, options?, cb?) and .get: a request to the host, or a CONNECT tunnel to it.
  const wrapRequest = (m, name) => {
    const original = m[name];
    m[name] = function request_(...args) {
      const [first, second] = args;
      const o = first !== null && typeof first === 'object' && !(first instanceof URL) ? first : second !== null && typeof second === 'object' ? second : undefined;
      const url = typeof first === 'string' || first instanceof URL ? first : undefined;
      const hit =
        isGuarded(hostOf(url)) ||
        isGuarded(o?.host) ||
        isGuarded(o?.hostname) ||
        isGuarded(o?.servername) ||
        (String(o?.method ?? '').toUpperCase() === 'CONNECT' && isGuarded(hostOf(o?.path)));
      if (hit) throw refuse();
      return original.apply(this, args);
    };
  };
  for (const m of [http, https]) for (const name of ['request', 'get']) wrapRequest(m, name);

  mod?.syncBuiltinESMExports?.();
  return true;
}
