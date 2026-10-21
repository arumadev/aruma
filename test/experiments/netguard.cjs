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
//
//   ARUMA_NETGUARD_LOOPBACK_NAMES=.127.0.0.1.nip.io,db.example.test   (optional)
//
// lets names that public DNS points at this machine through, such as nip.io's: a name equal to
// an entry, or under an entry that starts with a dot, is resolved by the real DNS, and the
// connection goes ahead only when every address it resolves to is loopback ("ALLOW name:port
// (127.0.0.1)"); any other answer is a BLOCK. TLS still sends the name as SNI.
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

const loopbackNames = (process.env.ARUMA_NETGUARD_LOOPBACK_NAMES || '')
  .split(',')
  .map((n) => n.trim().toLowerCase())
  .filter(Boolean);

function loopbackName(host) {
  const h = host.toLowerCase().replace(/\.$/, '');
  return loopbackNames.some((n) => (n.startsWith('.') ? h.endsWith(n) : h === n));
}

function loopbackAddress(address) {
  return net.isIPv4(address) ? address.startsWith('127.') : address === '::1' || address === '::ffff:127.0.0.1';
}

const realLookup = dns.lookup;

// checkedLookup resolves a loopback name with the real DNS and fails unless every address
// is loopback. It is handed to the socket as its lookup, so whatever the socket asks for
// (one address, or all of them for autoSelectFamily) comes from here.
function checkedLookup(target) {
  return function lookup(hostname, options, callback) {
    if (typeof options === 'function') [callback, options] = [options, {}];
    realLookup(hostname, { ...options, all: true }, (err, addresses) => {
      if (err) return callback(err);
      const bad = addresses.filter((a) => !loopbackAddress(a.address));
      if (bad.length > 0 || addresses.length === 0) {
        record('BLOCK', `${target} (${addresses.map((a) => a.address).join(' ') || 'no address'})`);
        const e = Object.assign(new Error(`connect ECONNREFUSED ${target}: resolves beyond loopback (blocked by the Aruma netguard)`), { code: 'ECONNREFUSED' });
        return callback(e);
      }
      record('ALLOW', `${target} (${addresses.map((a) => a.address).join(' ')})`);
      if (options && options.all) return callback(null, addresses);
      callback(null, addresses[0].address, addresses[0].family);
    });
  };
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
  if (loopbackNames.length > 0 && net.isIP(host) === 0 && loopbackName(host)) {
    // Spread out again, so connect() normalizes the copy itself.
    const rest = Array.isArray(args[0]) ? a.slice(1) : args.slice(1).filter((x) => typeof x === 'function');
    return realConnect.call(this, { ...opts, lookup: checkedLookup(target) }, ...rest);
  }
  record('BLOCK', target);
  const err = Object.assign(new Error(`connect ECONNREFUSED ${target} (blocked by the Aruma netguard)`), { code: 'ECONNREFUSED' });
  process.nextTick(() => this.destroy(err));
  return this;
};

dns.lookup = function lookup(hostname, ...rest) {
  if (!loopback.has(hostname)) record('DNS', hostname);
  return realLookup.call(this, hostname, ...rest);
};
syncBuiltinESMExports();
