// Network guard for the experiments: records every outbound TCP connection and DNS lookup a
// Node process makes, and refuses anything that is not loopback. Preload it into a process
// and all of its Node children with
//
//   NODE_OPTIONS="--require /abs/path/test/experiments/netguard.cjs"
//   ARUMA_NETGUARD_LOG=/abs/path/attempts.log   (optional; one line per attempt)
//
// Lines look like "ALLOW 127.0.0.1:49441 pid=123" or "BLOCK oauth2.googleapis.com:443 pid=123".
// It patches net.Socket.prototype.connect, where every TCP connection ends up (http, https,
// tls, http2 and the built-in fetch alike), and dns.lookup, so blocked hosts are recorded by
// name before any resolution happens.
'use strict';
const fs = require('node:fs');
const net = require('node:net');
const dns = require('node:dns');
const { syncBuiltinESMExports } = require('node:module');

const logFile = process.env.ARUMA_NETGUARD_LOG;
const loopback = new Set(['127.0.0.1', '::1', 'localhost', '::ffff:127.0.0.1']);

// In-process record of every attempt, e.g. ["BLOCK oauth2.googleapis.com:443"].
const attempts = [];
module.exports = { attempts };

function record(verdict, target) {
  attempts.push(`${verdict} ${target}`);
  const line = `${verdict} ${target} pid=${process.pid}\n`;
  if (logFile) fs.appendFileSync(logFile, line);
  if (verdict === 'BLOCK') process.stderr.write(`[netguard] blocked ${target}\n`);
}

const realConnect = net.Socket.prototype.connect;
net.Socket.prototype.connect = function connect(...args) {
  // net.connect() passes its normalized arguments as a single array.
  const a = Array.isArray(args[0]) ? args[0] : args;
  const opts = a[0] !== null && typeof a[0] === 'object' ? a[0] : { port: a[0], host: typeof a[1] === 'string' ? a[1] : undefined };
  if (opts.path) return realConnect.apply(this, args); // Unix sockets stay local.
  const host = String(opts.host ?? 'localhost').replace(/^\[|\]$/g, '');
  const target = `${host}:${opts.port}`;
  if (loopback.has(host)) {
    record('ALLOW', target);
    return realConnect.apply(this, args);
  }
  record('BLOCK', target);
  const err = Object.assign(new Error(`connect ECONNREFUSED ${target} (blocked by the Aruma netguard)`), { code: 'ECONNREFUSED' });
  process.nextTick(() => this.destroy(err));
  return this;
};

const realLookup = dns.lookup;
dns.lookup = function lookup(hostname, ...rest) {
  if (!loopback.has(hostname)) record('DNS', hostname);
  return realLookup.call(this, hostname, ...rest);
};
syncBuiltinESMExports();
