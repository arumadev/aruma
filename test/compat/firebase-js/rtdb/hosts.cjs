// Production-mode name resolution for the RTDB suites: inside this Node process only, every
// *.aruma.test name resolves to 127.0.0.1, as a hosts entry would, and its port 443 is the local
// TLS front (tls-proxy.mjs). Nothing on the machine changes: /etc/hosts stays as it is. Preload
// it into a process and its Node children with
//
//   NODE_OPTIONS="--require /abs/path/hosts.cjs"
//   ARUMA_TEST_TLS_PORT=9471   (where port 443 of those names goes)
//
// It patches dns.lookup (and dns.promises.lookup), and net.Socket.prototype.connect, where every
// TCP connection ends up (http, https, tls and the built-in fetch alike). Only the copy of the
// options handed to the socket changes: TLS still sends the name as SNI and checks the
// certificate against it. Load it after test/e2e/netguard.cjs, which then sees a
// loopback address and lets the connection through.
'use strict';
const dns = require('node:dns');
const net = require('node:net');
const { syncBuiltinESMExports } = require('node:module');

const suffix = '.aruma.test';
const tlsPort = Number(process.env.ARUMA_TEST_TLS_PORT || 443);

function ours(host) {
  return typeof host === 'string' && host.toLowerCase().replace(/\.$/, '').endsWith(suffix);
}

const loopback = { address: '127.0.0.1', family: 4 };

const realLookup = dns.lookup;
dns.lookup = function lookup(hostname, options, callback) {
  if (!ours(hostname)) return realLookup.apply(this, arguments);
  if (typeof options === 'function') [callback, options] = [options, {}];
  const all = typeof options === 'object' && options !== null && options.all;
  process.nextTick(() => (all ? callback(null, [loopback]) : callback(null, loopback.address, loopback.family)));
  return {};
};

const realPromisesLookup = dns.promises.lookup;
dns.promises.lookup = async function lookup(hostname, options) {
  if (!ours(hostname)) return realPromisesLookup.apply(this, arguments);
  return typeof options === 'object' && options !== null && options.all ? [loopback] : loopback;
};

const realConnect = net.Socket.prototype.connect;
net.Socket.prototype.connect = function connect(...args) {
  // net.connect() passes its normalized arguments, [options, callback], as a single array.
  const a = Array.isArray(args[0]) ? args[0] : args;
  if (a[0] === null || typeof a[0] !== 'object' || !ours(a[0].host)) return realConnect.apply(this, args);
  const port = Number(a[0].port || 443) === 443 ? tlsPort : a[0].port;
  // Spread out again, so connect() normalizes the copy itself.
  return realConnect.call(this, { ...a[0], host: loopback.address, port }, ...a.slice(1));
};
syncBuiltinESMExports();
