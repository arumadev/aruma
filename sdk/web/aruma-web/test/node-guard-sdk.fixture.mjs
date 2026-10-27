// Run by node-guard.test.js in a child process (it patches Node's built-in networking).
// The Node Firestore SDK talks gRPC over HTTP/2 (@grpc/grpc-js). With Auth on Aruma, install() must
// stop it from connecting to Google's Firestore host: record what reaches the lowest layer.
import dns from 'node:dns';
import net from 'node:net';
import tls from 'node:tls';
import http from 'node:http';
import https from 'node:https';
import http2 from 'node:http2';

const host = process.argv[2];
const seen = [];
const mentions = (v) => JSON.stringify(v, (_k, x) => (typeof x === 'object' && x !== null && x.constructor === net.Socket ? '[socket]' : x))?.includes(host);
const record = (what, args) => {
  if (mentions(args)) seen.push(what);
};
// Installed before install(): the guard sits on top, so these see only what got through it.
// Nothing here ever reaches the network: a lookup of the host is recorded and fails.
const { lookup } = dns;
dns.lookup = (...a) => {
  if (!mentions(a)) return lookup(...a);
  record('dns.lookup', a);
  process.nextTick(a.find((x) => typeof x === 'function'), new Error('lookup attempted'));
  return {};
};
const plookup = dns.promises.lookup;
dns.promises.lookup = (...a) => (mentions(a) ? (record('dns.promises.lookup', a), Promise.reject(new Error('lookup attempted'))) : plookup(...a));
const connect = net.Socket.prototype.connect;
net.Socket.prototype.connect = function (...a) {
  record('net.connect', a);
  if (mentions(a)) return this.destroy(new Error('connection attempted'));
  return connect.apply(this, a);
};
const tlsConnect = tls.connect;
tls.connect = (...a) => (record('tls.connect', a), tlsConnect(...a));
const h2 = http2.connect;
http2.connect = (...a) => (record('http2.connect', a), h2(...a));
for (const m of [http, https]) {
  for (const name of ['request', 'get']) {
    const f = m[name];
    m[name] = (...a) => (record(`${m === http ? 'http' : 'https'}.${name}`, a), f(...a));
  }
}

const { install } = await import('../src/index.js');
// `control` runs without install(), to show the recorder does see the SDK's connection attempt.
if (process.argv[3] !== 'control') install({ url: 'https://aruma.example.com' });

const { initializeApp } = await import('firebase/app');
const { getFirestore, doc, getDoc } = await import('firebase/firestore');
const db = getFirestore(initializeApp({ projectId: 'p', apiKey: 'k', appId: '1:1:web:1' }));

// The SDK reports the failure once it gives up on the backend (its client-is-offline timeout).
let error = null;
try {
  await getDoc(doc(db, 'c/d'));
} catch (e) {
  error = { code: e.code, message: e.message };
}
console.log(JSON.stringify({ seen: [...new Set(seen)], error }));
process.exit(0);
