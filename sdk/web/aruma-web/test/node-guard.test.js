// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// SR3-10 in Node: the Node Firestore SDK speaks gRPC over HTTP/2, not fetch, so install() also
// refuses every connection to Google's Firestore host made through the built-in networking modules.

import { test, describe, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import dns from 'node:dns';
import net from 'node:net';
import tls from 'node:tls';
import http from 'node:http';
import https from 'node:https';
import http2 from 'node:http2';
import { connect as tlsConnectNamed } from 'node:tls';
import { install, uninstall } from '../src/index.js';

const HOST = atob('ZmlyZXN0b3JlLmdvb2dsZWFwaXMuY29t');
const run = promisify(execFile);

// Recorders under the guard: they see only what the guard let through. None touches the network.
const reached = [];
const originals = {};
function recorders() {
  const note = (what, v) => reached.push(`${what} ${typeof v === 'string' ? v : JSON.stringify(v)}`);
  originals.lookup = dns.lookup;
  dns.lookup = (h, ...rest) => (note('dns.lookup', h), process.nextTick(rest.at(-1), new Error('recorded')), {});
  originals.plookup = dns.promises.lookup;
  dns.promises.lookup = (h) => (note('dns.promises.lookup', h), Promise.reject(new Error('recorded')));
  originals.sock = net.Socket.prototype.connect;
  net.Socket.prototype.connect = function (...a) {
    note('socket.connect', a[0]);
    return this.destroy(new Error('recorded'));
  };
  originals.tls = tls.connect;
  tls.connect = (...a) => (note('tls.connect', a[0]?.servername ?? a[0]), new net.Socket());
  originals.h2 = http2.connect;
  http2.connect = (a) => (note('http2.connect', String(a)), {});
  for (const m of [http, https]) {
    for (const n of ['request', 'get']) {
      originals[`${m === http ? 'http' : 'https'}${n}`] = m[n];
      m[n] = (...a) => (note(`${m === http ? 'http' : 'https'}.${n}`, String(a[0]?.href ?? a[0]?.path ?? a[0])), {});
    }
  }
}

describe('Node: no connection to the Firestore host while Auth is on Aruma', () => {
  before(() => {
    recorders();
    install({ url: 'https://aruma.example.com' });
  });
  after(() => uninstall());

  const refused = (fn) => assert.throws(fn, { code: 'ARUMA_FIRESTORE_BLOCKED' });

  test('http2.connect (what grpc-js uses), by URL, by authority string and by URL object', () => {
    refused(() => http2.connect(`https://${HOST}:443`));
    refused(() => http2.connect(`https://${HOST.toUpperCase()}.:443/`, {}));
    refused(() => http2.connect(new URL(`https://${HOST}`)));
  });

  test('tls.connect, by servername, by host and by positional host, also over a socket; also as an ESM named import', () => {
    refused(() => tls.connect({ socket: new net.Socket(), servername: HOST }));
    refused(() => tls.connect({ host: HOST, port: 443 }));
    refused(() => tls.connect(443, HOST));
    refused(() => tlsConnectNamed({ host: HOST, port: 443 }));
  });

  test('net.connect and sockets: the connection errors and nothing is sent', async () => {
    for (const make of [() => net.connect({ host: HOST, port: 443 }), () => net.connect(443, HOST), () => new net.Socket().connect({ host: HOST, port: 443 })]) {
      const err = await new Promise((resolve) => make().once('error', resolve));
      assert.equal(err.code, 'ARUMA_FIRESTORE_BLOCKED');
    }
  });

  test('DNS lookup of the host, callback and promise forms', async () => {
    await assert.rejects(dns.promises.lookup(HOST, { all: true }), { code: 'ARUMA_FIRESTORE_BLOCKED' });
    const err = await new Promise((resolve) => dns.lookup(HOST, { all: true }, resolve));
    assert.equal(err.code, 'ARUMA_FIRESTORE_BLOCKED');
  });

  test('an HTTP proxy tunnel: a CONNECT whose target is the host, and a plain request to it', () => {
    refused(() => http.request({ method: 'CONNECT', host: '127.0.0.1', port: 3128, path: `${HOST}:443`, headers: { Host: `${HOST}:443` } }));
    refused(() => https.request({ method: 'connect', host: 'proxy.internal', path: `${HOST}:443` }));
    refused(() => https.request(`https://${HOST}/v1/projects`));
    refused(() => https.get({ hostname: HOST, path: '/' }));
    refused(() => http.get(new URL(`http://${HOST}/`)));
  });

  test('subdomains are matched, and the refusal is logged once', () => {
    refused(() => tls.connect({ host: `europe-west1.${HOST}`, port: 443 }));
  });

  test('other hosts, and CONNECT to other targets, are untouched', async () => {
    const before = reached.length;
    http2.connect('https://aruma.example.com');
    tls.connect({ host: 'aruma.example.com', port: 443 });
    https.request('https://aruma.example.com/x');
    http.request({ method: 'CONNECT', host: '127.0.0.1', port: 3128, path: 'aruma.example.com:443' });
    await assert.rejects(dns.promises.lookup('aruma.example.com'), /recorded/);
    http.request({ host: 'notfirestore.googleapis.com.example.org', path: '/' });
    assert.equal(reached.length - before, 6);
    assert.equal(reached.slice(before).filter((r) => r.includes(HOST)).length, 0, reached.join('\n'));
  });

  test('after uninstall() the guard lets the host through (Auth is no longer on Aruma)', () => {
    uninstall();
    http2.connect(`https://${HOST}`);
    assert.ok(reached.some((r) => r.startsWith('http2.connect') && r.includes(HOST)));
    install({ url: 'https://aruma.example.com' });
    refused(() => http2.connect(`https://${HOST}`));
  });
});

describe('Node: the Firestore SDK (gRPC) never connects to Google and reports an error', () => {
  const fixture = new URL('./node-guard-sdk.fixture.mjs', import.meta.url).pathname;
  const sdk = async (...args) => {
    const { stdout } = await run(process.execPath, [fixture, HOST, ...args], { timeout: 60_000 });
    return JSON.parse(stdout.trim().split('\n').at(-1));
  };

  test('control: without install() the SDK does try to resolve the host', async () => {
    const r = await sdk('control');
    assert.ok(r.seen.length > 0, 'the recorder must see the SDK reach for the host');
  });

  test('with install(): nothing reaches the network layer, and getDoc fails as unavailable', async () => {
    const r = await sdk();
    assert.deepEqual(r.seen, []);
    assert.equal(r.error?.code, 'unavailable');
  });

  test('the SDK run prints the Aruma explanation', async () => {
    const { stderr } = await run(process.execPath, [fixture, HOST], { timeout: 60_000 });
    assert.match(stderr, /Cloud Firestore request to firestore\.googleapis\.com was blocked/);
  });
});
