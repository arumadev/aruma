// Name resolution for the suites that run as production code does, with Firebase's own host
// names (js/, see ../README.md): inside this Node process and its Node children only, each name
// in ARUMA_TEST_HOSTS resolves to 127.0.0.1, and its port 443 is the local TLS front
// (front.mjs), which hands the request to Aruma as a runtime redirect would. Nothing on the
// machine changes: /etc/hosts stays as it is, and no name is ever looked up in the real DNS.
//
//   NODE_OPTIONS="--require /abs/test/experiments/netguard.cjs --require /abs/hosts.cjs"
//   ARUMA_TEST_HOSTS=firebasestorage.googleapis.com,identitytoolkit.googleapis.com
//   ARUMA_TEST_TLS_PORT=9412
//
// It patches dns.lookup (and dns.promises.lookup), and net.Socket.prototype.connect, where every
// TCP connection ends up (http, https, tls and the built-in fetch alike). Only the copy of the
// options handed to the socket changes: TLS still sends the name as SNI and checks the
// certificate against it (NODE_EXTRA_CA_CERTS names the run's CA). It loads after netguard.cjs,
// which then sees a loopback address; a name that is not listed here meets the guard as is.
// The same idea as ../firebase-js/rtdb/hosts.cjs, for a list of names instead of a suffix.
'use strict';
const dns = require('node:dns');
const net = require('node:net');
const { syncBuiltinESMExports } = require('node:module');

const names = new Set(
  (process.env.ARUMA_TEST_HOSTS || '')
    .split(',')
    .map((n) => n.trim().toLowerCase())
    .filter(Boolean),
);
const tlsPort = Number(process.env.ARUMA_TEST_TLS_PORT || 443);

function ours(host) {
  return typeof host === 'string' && names.has(host.toLowerCase().replace(/\.$/, ''));
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
  return realConnect.call(this, { ...a[0], host: loopback.address, port }, ...a.slice(1));
};
syncBuiltinESMExports();
