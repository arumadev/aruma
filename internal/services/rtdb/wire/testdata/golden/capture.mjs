// capture.mjs - records golden RTDB wire-protocol (v5) frames from the official
// Firebase Realtime Database emulator.
//
// Usage:
//   java -jar firebase-database-emulator-v4.11.2.jar --host 127.0.0.1 --port 9400
//   node capture.mjs [host:port] [outDir]
// host:port defaults to $RTDB_EMULATOR_HOST, then 127.0.0.1:9400.
// outDir defaults to the directory of this script.
//
// Requires Node >= 22 (global WebSocket and fetch). Every text frame in both
// directions is recorded in arrival order, as the raw string. Frames longer
// than 1000 chars are stored as {len, sha256} instead of the text.

import net from 'node:net';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HOST = process.argv[2] || process.env.RTDB_EMULATOR_HOST || '127.0.0.1:9400';
const OUT = process.argv[3] || path.dirname(fileURLToPath(import.meta.url));
const EMULATOR = 'firebase-database-emulator-v4.11.2';
const TIMEOUT = 1500; // wait for a reply matching r
const SETTLE = 250; // extra wait after each step so pushes to other sockets arrive
const BIG = 1000; // frames longer than this are stored as len + sha256
const CHUNK = 16384; // SDK max frame size

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const sha256 = (s) => crypto.createHash('sha256').update(s, 'utf8').digest('hex');
const today = () => {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
};

class Recorder {
  constructor(scenario, description) {
    this.doc = {
      scenario,
      description,
      source: 'emulator',
      emulator: EMULATOR,
      captured: today(),
      script: 'capture.mjs',
      connections: {},
      frames: [],
      notes: [],
    };
    this.conns = [];
  }
  entry(conn, dir, text) {
    if (text.length > BIG) return { conn, dir, len: text.length, sha256: sha256(text) };
    return { conn, dir, text };
  }
  note(s) {
    this.doc.notes.push(s);
  }
  async finish() {
    for (const c of this.conns) await c.close();
    const byClient = this.conns.filter((c) => c.clientClosed).map((c) => c.name);
    const byServer = this.conns.filter((c) => !c.clientClosed && c.isClosed).map((c) => c.name);
    if (byClient.length)
      this.note(
        `close initiated by the client (close frame with code 1000) on: ${byClient.join(', ')}; the code recorded is what the client saw in the emulator's closing reply (1005 = the reply carried no status code).`,
      );
    if (byServer.length) this.note(`close initiated by the server on: ${byServer.join(', ')}.`);
    const file = path.join(OUT, `${this.doc.scenario}.json`);
    fs.writeFileSync(file, JSON.stringify(this.doc, null, 2) + '\n');
    console.log(`wrote ${file} (${this.doc.frames.length} frames)`);
  }
}

// Base connection: recording, multi-frame reassembly, request/response matching.
class Conn {
  constructor(rec, name, urlPath) {
    this.rec = rec;
    this.name = name;
    this.urlPath = urlPath;
    this.rid = 0;
    this.waiters = new Map();
    this.pending = 0;
    this.buf = '';
    this.msgs = [];
    this.isClosed = false;
    rec.doc.connections[name] = urlPath;
    rec.conns.push(this);
    this.hello = new Promise((r) => (this._helloResolve = r));
    this.closedP = new Promise((r) => (this._closedResolve = r));
  }
  _recv(text) {
    this.rec.doc.frames.push(this.rec.entry(this.name, 'recv', text));
    this._helloResolve(text);
    if (this.pending > 0) {
      this.buf += text;
      if (--this.pending === 0) this._msg(this.buf);
    } else if (text.length <= 6 && /^\d+$/.test(text)) {
      this.pending = Number(text);
      this.buf = '';
    } else this._msg(text);
  }
  _msg(s) {
    let o;
    try {
      o = JSON.parse(s);
    } catch {
      return;
    }
    this.msgs.push(o);
    if (o && o.t === 'd' && o.d && o.d.r != null && this.waiters.has(o.d.r)) {
      this.waiters.get(o.d.r)(o);
      this.waiters.delete(o.d.r);
    }
  }
  _closed(code, reason) {
    if (this.isClosed) return;
    this.isClosed = true;
    this.rec.doc.frames.push({ conn: this.name, dir: 'close', code, reason });
    for (const [, w] of this.waiters) w(null);
    this.waiters.clear();
    this._closedResolve({ code, reason });
  }
  send(text) {
    this.rec.doc.frames.push(this.rec.entry(this.name, 'send', text));
    this._send(text);
  }
  // Send a raw request frame text whose r is known; wait for reply or timeout.
  async sendAndWait(text, r, { timeout = TIMEOUT, settle = SETTLE, chunked = false } = {}) {
    const p = new Promise((res) => {
      this.waiters.set(r, res);
      setTimeout(() => {
        if (this.waiters.has(r)) {
          this.waiters.delete(r);
          res(null);
        }
      }, timeout);
    });
    if (chunked) this.sendChunked(text);
    else this.send(text);
    const resp = await p;
    if (!resp)
      this.rec.note(
        `${this.name} r=${r}: no reply within ${timeout} ms` + (this.isClosed ? ' (connection closed)' : ''),
      );
    await sleep(settle);
    return resp;
  }
  async req(a, b, opts = {}) {
    const r = ++this.rid;
    const d = opts.noBody ? { r, a } : { r, a, b };
    return this.sendAndWait(JSON.stringify({ t: 'd', d }), r, opts);
  }
  sendChunked(text) {
    const parts = [];
    for (let i = 0; i < text.length; i += CHUNK) parts.push(text.slice(i, i + CHUNK));
    this.send(String(parts.length));
    for (const p of parts) this.send(p);
  }
}

// Connection over Node's global WebSocket.
class WsConn extends Conn {
  constructor(rec, name, ns = 'demo-test', query = '') {
    const urlPath = `/.ws?v=5&ns=${ns}${query}`;
    super(rec, name, urlPath);
    this.ws = new WebSocket(`ws://${HOST}${urlPath}`);
    this.ws.onmessage = (e) => this._recv(typeof e.data === 'string' ? e.data : '<binary>');
    this.ws.onclose = (e) => this._closed(e.code, e.reason);
    this.ws.onerror = () => {};
  }
  _send(t) {
    this.ws.send(t);
  }
  async close() {
    if (this.isClosed) return;
    this.clientClosed = true;
    this.ws.close(1000);
    await Promise.race([this.closedP, sleep(2000)]);
  }
}

// Minimal hand-rolled WebSocket client over net, so arbitrary Host/Origin
// headers can be sent and the HTTP upgrade response recorded.
class RawConn extends Conn {
  constructor(rec, name, urlPath, headers) {
    super(rec, name, urlPath);
    const [h, port] = HOST.split(':');
    this.sock = net.createConnection(Number(port), h);
    this.raw = Buffer.alloc(0);
    this.upgraded = false;
    this.frag = [];
    this.request = `GET ${urlPath} HTTP/1.1\r\n` + headers.map((x) => x + '\r\n').join('') + '\r\n';
    this.httpDone = new Promise((r) => (this._httpResolve = r));
    this.sock.on('connect', () => this.sock.write(this.request));
    this.sock.on('data', (d) => this._data(d));
    this.sock.on('close', () => {
      this._httpResolve(null);
      this._helloResolve(null);
      this._closed(this.closeCode ?? 1006, this.closeReason ?? '');
    });
    this.sock.on('error', () => {});
  }
  _data(d) {
    this.raw = Buffer.concat([this.raw, d]);
    if (!this.upgraded) {
      const i = this.raw.indexOf('\r\n\r\n');
      if (i < 0) return;
      const head = this.raw.subarray(0, i).toString('latin1');
      this.raw = this.raw.subarray(i + 4);
      this.rec.doc.frames.push({ conn: this.name, dir: 'http', text: head });
      this.status = Number(head.split(' ')[1]);
      this.upgraded = this.status === 101;
      this._httpResolve(head);
      if (!this.upgraded) {
        this._helloResolve(null);
        this.sock.end();
        return;
      }
    }
    for (;;) {
      const b = this.raw;
      if (b.length < 2) return;
      const fin = (b[0] & 0x80) !== 0;
      const op = b[0] & 0x0f;
      let len = b[1] & 0x7f;
      let off = 2;
      if (len === 126) {
        if (b.length < 4) return;
        len = b.readUInt16BE(2);
        off = 4;
      } else if (len === 127) {
        if (b.length < 10) return;
        len = Number(b.readBigUInt64BE(2));
        off = 10;
      }
      if (b[1] & 0x80) off += 4; // server frames should not be masked
      if (b.length < off + len) return;
      const payload = b.subarray(off, off + len);
      this.raw = b.subarray(off + len);
      if (op === 8) {
        this.closeCode = payload.length >= 2 ? payload.readUInt16BE(0) : 1005;
        this.closeReason = payload.subarray(2).toString('utf8');
        if (!this.sentClose) this._frame(8, payload.subarray(0, 2));
        this.sock.end();
      } else if (op === 9) this._frame(10, payload);
      else if (op === 1 || op === 0) {
        this.frag.push(payload);
        if (fin) {
          const t = Buffer.concat(this.frag).toString('utf8');
          this.frag = [];
          this._recv(t);
        }
      }
    }
  }
  _frame(op, payload) {
    const mask = crypto.randomBytes(4);
    const len = payload.length;
    let head;
    if (len < 126) head = Buffer.from([0x80 | op, 0x80 | len]);
    else if (len < 65536) {
      head = Buffer.alloc(4);
      head[0] = 0x80 | op;
      head[1] = 0x80 | 126;
      head.writeUInt16BE(len, 2);
    } else {
      head = Buffer.alloc(10);
      head[0] = 0x80 | op;
      head[1] = 0x80 | 127;
      head.writeBigUInt64BE(BigInt(len), 2);
    }
    const body = Buffer.from(payload);
    for (let i = 0; i < body.length; i++) body[i] ^= mask[i & 3];
    this.sock.write(Buffer.concat([head, mask, body]));
  }
  _send(t) {
    this._frame(1, Buffer.from(t, 'utf8'));
  }
  async close() {
    if (this.isClosed) return;
    this.clientClosed = true;
    if (this.upgraded) {
      this.sentClose = true;
      const p = Buffer.alloc(2);
      p.writeUInt16BE(1000);
      this._frame(8, p);
    } else this.sock.end();
    await Promise.race([this.closedP, sleep(2000)]);
    if (!this.isClosed) this.sock.destroy();
  }
}

async function open(rec, name, ns, query) {
  const c = new WsConn(rec, name, ns, query);
  await Promise.race([c.hello, sleep(3000)]);
  return c;
}

async function rest(method, p, { ns = 'demo-test', body, headers = {} } = {}) {
  const sep = p.includes('?') ? '&' : '?';
  const url = `http://${HOST}${p}${ns == null ? '' : `${sep}ns=${ns}`}`;
  const r = await fetch(url, { method, body: body === undefined ? undefined : JSON.stringify(body), headers });
  const text = await r.text();
  return { method, url: url.replace(`http://${HOST}`, ''), status: r.status, etag: r.headers.get('etag'), body: text };
}

const reset = (p) => rest('PUT', `${p}.json`, { body: null });

// ---------------------------------------------------------------- scenarios

async function handshake() {
  const rec = new Recorder('handshake', 'Hello frame on connect, stats request, client control ping and its reply, then close.');
  const A = await open(rec, 'A');
  await A.req('s', { c: { 'sdk.js.10-0-0': 1 } });
  A.send('{"t":"c","d":{"t":"p","d":{}}}');
  await sleep(TIMEOUT);
  rec.note('hello: d.ts = server ms epoch, d.v = "5", d.h = the emulator host:port (see origin.json: it does not follow the Host header), d.s = random 32-char session id.');
  rec.note('The control ping is answered with a control pong (t:"o"); stats get a normal ok reply.');
  await rec.finish();
}

async function putGet() {
  await reset('/golden/pg');
  const rec = new Recorder('put_get', 'Put objects, priorities and arrays, then read them back with g (get), including a missing path and a child path.');
  const A = await open(rec, 'A');
  await A.req('p', { p: '/golden/pg', d: { a: 1, b: 'hi', n: { c: true } } });
  await A.req('g', { p: '/golden/pg', q: {} });
  await A.req('g', { p: '/golden/pgmissing', q: {} });
  await A.req('g', { p: '/golden/pg/n', q: {} });
  await A.req('p', { p: '/golden/pg/pr', d: { '.value': 'v', '.priority': 7 } });
  await A.req('g', { p: '/golden/pg/pr', q: {} });
  await A.req('p', { p: '/golden/pg/arr', d: [1, 2] });
  await A.req('g', { p: '/golden/pg/arr', q: {} });
  rec.note("Observed with firebase-database-emulator-v4.11.2: puts are acked with d:\"\"; g returns the value (null for a missing path); a leaf priority comes back in export form with the number as 7.0; an array is stored and returned as an object with keys \"0\",\"1\".");
  await rec.finish();
}

async function updateMultipath() {
  await reset('/golden/up');
  const rec = new Recorder('update_multipath', 'B listens; A sets an object then sends a multi-path merge (m) with a deep path, a delete and a new deep path; A reads back.');
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  await B.req('q', { p: '/golden/up', h: '' });
  await A.req('p', { p: '/golden/up', d: { a: 1, b: { c: 2 }, d: 3 } });
  await A.req('m', { p: '/golden/up', d: { 'b/c': 20, d: null, 'e/f': 5 } });
  await A.req('g', { p: '/golden/up', q: {} });
  rec.note('Frames of A and B are interleaved in the order this single-threaded client observed them.');
  rec.note("Observed with firebase-database-emulator-v4.11.2: the m push to the listener carries the merge map exactly as sent (slash paths and nulls kept), not the resulting tree; pushes use paths without a leading slash.");
  await rec.finish();
}

async function datastale() {
  await reset('/golden/tx');
  await reset('/golden/txmissing');
  const rec = new Recorder('datastale', 'Conditional put (transaction) with a wrong hash, with the REST ETag as hash, and with an empty hash on a missing path.');
  rec.doc.rest = [];
  const A = await open(rec, 'A');
  await A.req('p', { p: '/golden/tx', d: { s: 1 } });
  const g = await rest('GET', '/golden/tx.json', { headers: { 'X-Firebase-ETag': 'true' } });
  rec.doc.rest.push(g);
  rec.doc.etag = g.etag;
  rec.doc.etagBody = g.body;
  await A.req('p', { p: '/golden/tx', d: { s: 2 }, h: 'wronghash' });
  await A.req('p', { p: '/golden/tx', d: { s: 2 }, h: g.etag });
  await A.req('g', { p: '/golden/tx', q: {} });
  await A.req('p', { p: '/golden/txmissing', d: 'new', h: '' });
  const g2 = await rest('GET', '/golden/txmissing.json', { headers: { 'X-Firebase-ETag': 'true' } });
  rec.doc.rest.push(g2);
  rec.note('etag = REST ETag of /golden/tx after the first put (value {"s":1}), fetched with X-Firebase-ETag: true; it is used as h for the successful conditional put.');
  rec.note("Observed with firebase-database-emulator-v4.11.2: wrong h gives s:\"datastale\" with d:\"Transaction ETag does not match\"; matching h gives ok with d = the written data; h:\"\" on a missing path succeeds (ok, d = written data), so \"\" is the hash of null.");
  await rec.finish();
}

async function serverValues() {
  await reset('/golden/sv');
  const rec = new Recorder('server_values', 'Server values: timestamp, increment on number/missing/string nodes, and invalid server values.');
  const A = await open(rec, 'A');
  await A.req('p', { p: '/golden/sv', d: { ts: { '.sv': 'timestamp' }, n: { '.sv': { increment: 5 } } } });
  await A.req('p', { p: '/golden/sv/n', d: { '.sv': { increment: 2.5 } } });
  await A.req('p', { p: '/golden/sv/m', d: { '.sv': { increment: 1 } } });
  await A.req('p', { p: '/golden/sv/s', d: 'str' });
  await A.req('p', { p: '/golden/sv/s', d: { '.sv': { increment: 3 } } });
  await A.req('g', { p: '/golden/sv', q: {} });
  rec.note("Observed with firebase-database-emulator-v4.11.2: increment adds to a number (5 + 2.5 = 7.5), starts from 0 on a missing node, and replaces a string with the increment value (3); timestamp is server ms epoch. Unknown .sv names and a non-number increment produce a control frame t:\"e\" \"ClientId[n]:ErrorId[m]: Error on incoming message\" and no reply; the socket stays open.");
  await A.req('p', { p: '/golden/sv/u', d: { '.sv': 'unknown' } });
  await A.req('p', { p: '/golden/sv/x', d: { '.sv': { increment: 'x' } } });
  await A.req('g', { p: '/golden/sv', q: {} });
  await rec.finish();
}

function nestPath(n) {
  // path with exactly n segments: golden/depth/s3/.../sn
  const segs = ['golden', 'depth'];
  for (let i = 3; i <= n; i++) segs.push('s' + i);
  return '/' + segs.join('/');
}
function nestData(levels) {
  // levels = number of nested objects; innermost value is the number 1
  let d = 1;
  for (let i = 0; i < levels; i++) d = { k: d };
  return d;
}

async function limits() {
  await reset('/golden/limits');
  await reset('/golden/depth');
  await reset('/golden/len');
  await reset('/golden/deep');
  await reset('/golden/big');
  await reset('/golden/huge');
  await reset('/gk');
  await reset('/gdeep');
  await reset('/g');
  await reset('/gg');
  const rec = new Recorder('limits', 'Invalid keys and paths, path depth/length limits, key length, data nesting, a chunked 60 KB put with a listener, and an unsplit 120 KB frame.');
  let n = 0;
  let A = await open(rec, 'A');
  const ensure = async () => {
    if (A.isClosed) {
      n++;
      A = await open(rec, 'A' + (n + 1));
    }
    return A;
  };
  // (a) forbidden char in data key
  await (await ensure()).req('p', { p: '/golden/limits/a', d: { 'a.b': 1 } });
  // (b) forbidden char in path
  await (await ensure()).req('p', { p: '/golden/bad/x.y', d: 1 });
  // (c) path depth
  for (const segs of [33, 32, 31]) await (await ensure()).req('p', { p: nestPath(segs), d: segs });
  // (d) path length in bytes (counted on the path without leading slash, segments joined by "/")
  const lenPrefix = 'golden/len/';
  const p812 = lenPrefix + 'a'.repeat(400) + '/' + 'b'.repeat(400);
  const p768 = lenPrefix + 'c'.repeat(768 - lenPrefix.length);
  const p769 = lenPrefix + 'd'.repeat(769 - lenPrefix.length);
  for (const p of [p812, p769, p768]) await (await ensure()).req('p', { p: '/' + p, d: Buffer.byteLength(p) });
  rec.note(
    `path byte lengths are counted on the path without the leading slash, segments joined by "/", UTF-8 bytes (all ASCII here): ` +
      `${Buffer.byteLength(p812)} (two 400-char segments under golden/len), ${Buffer.byteLength(p769)}, ${Buffer.byteLength(p768)}; the put value d is that byte count.`,
  );
  // (d2) discriminate: separators not counted? (segment bytes summed)
  const s768 = '/golden/len/' + 'x'.repeat(759); // segments 6+3+759 = 768, joined 770
  const s769 = '/golden/len/' + 'y'.repeat(760); // segments 769, joined 771
  await (await ensure()).req('p', { p: s768, d: 'sum768' });
  await (await ensure()).req('p', { p: s769, d: 'sum769' });
  rec.note('(d2) /golden/len/<759 x> has segment bytes 6+3+759 = 768 (770 joined with "/"); /golden/len/<760 y> has 769 (771 joined).');
  // (e) key length inside data
  await (await ensure()).req('p', { p: '/golden/limits/k766', d: { ['k'.repeat(766)]: 1 } });
  await (await ensure()).req('p', { p: '/golden/limits/k767', d: { ['k'.repeat(767)]: 1 } });
  await (await ensure()).req('p', { p: '/golden/limits/k768', d: { ['k'.repeat(768)]: 1 } });
  await (await ensure()).req('p', { p: '/golden/limits/k769', d: { ['k'.repeat(769)]: 1 } });
  // data key whose full path (gk/<key>) is exactly 768 / 769 bytes
  await (await ensure()).req('p', { p: '/gk', d: { ['g'.repeat(765)]: 1 } });
  await (await ensure()).req('p', { p: '/gk', d: { ['h'.repeat(766)]: 1 } });
  // data-relative key path joined with "/" of 767 vs 768 bytes; path + data segment-byte sum 768 vs 769
  await (await ensure()).req('p', { p: '/g', d: { a: { ['c'.repeat(765)]: 1 } } });
  await (await ensure()).req('p', { p: '/g', d: { ab: { ['c'.repeat(765)]: 1 } } });
  await (await ensure()).req('p', { p: '/gg', d: { ['f'.repeat(766)]: 1 } });
  await (await ensure()).req('p', { p: '/gg', d: { ['e'.repeat(767)]: 1 } });
  rec.note(
    'data-path cases: /g {a:{<765>}} (keys inside data joined "a/<765>" = 767 bytes), /g {ab:{<765>}} (768 bytes); ' +
      '/gg {<766>} (segment-byte sum path+key = 2+766 = 768), /gg {<767>} (769).',
  );
  rec.note('key-length cases:keys of 766, 767, 768 and 769 bytes in data under /golden/limits/k<len>, then keys of 765 and 766 bytes under /gk, i.e. full path gk/<key> of exactly 768 and 769 bytes.');
  // (f) data nesting under /golden/deep (path depth 2)
  for (const lv of [29, 30, 31, 32, 33]) await (await ensure()).req('p', { p: `/golden/deep/l${lv}`, d: nestData(lv) });
  for (const lv of [30, 31, 32, 33]) await (await ensure()).req('p', { p: '/gdeep', d: nestData(lv) });
  rec.note(
    'nesting cases: put at /golden/deep/l<N> (3 path segments) of {"k":{"k":...1}} with N nested objects, N in 29..33; then put at /gdeep (1 path segment) with N in 30..33. ' +
      'Each data leaf sits at depth (path segments + N).',
  );
  // (g) 60 KB chunked put, B listening
  const B = await open(rec, 'B');
  await B.req('q', { p: '/golden/big', h: '' });
  const a = await ensure();
  const r = ++a.rid;
  const big = JSON.stringify({ t: 'd', d: { r, a: 'p', b: { p: '/golden/big', d: 'x'.repeat(60000) } } });
  await a.sendAndWait(big, r, { chunked: true, settle: 600 });
  rec.note(`(g) chunked put: message of ${big.length} chars sent as count frame + ${Math.ceil(big.length / CHUNK)} chunks of <= ${CHUNK} chars.`);
  await B.close();
  // (h) 120 KB single frame
  const h = await ensure();
  const r2 = ++h.rid;
  const huge = JSON.stringify({ t: 'd', d: { r: r2, a: 'p', b: { p: '/golden/huge', d: 'y'.repeat(120000) } } });
  await h.sendAndWait(huge, r2, { settle: 600 });
  rec.note(`(h) single unsplit text frame of ${huge.length} chars.`);
  rec.note('Frames longer than 1000 chars are stored as {len, sha256} (sha256 of the UTF-8 frame text).');
  rec.note("Observed with firebase-database-emulator-v4.11.2: forbidden char in a data key -> t:\"e\" control frame, no reply. Forbidden char in path -> invalid_parameters \"Invalid token in path\". Depth: path segments + data nesting must be <= 32 (31 and 32 segment paths pass, 33 fails) else invalid_parameters \"Path specified exceeds the maximum depth that can be written (32).\"; a value nested >= 33 objects deep inside d fails earlier with the t:\"e\" frame. Length: the sum of UTF-8 bytes of all segments (path + keys inside d), separators not counted, must be <= 768 else invalid_parameters \"Path specified exceeds the maximum length that can be written (768 bytes).\"; independently, keys inside d joined with \"/\" must be < 768 bytes, else the t:\"e\" frame. A count-prefixed chunked 60 KB message is accepted and pushed to the listener in 16384-char chunks with a count frame; a single unsplit ~120 KB text frame makes the server close the socket with 1002.");
  await rec.finish();
}

async function isolation() {
  await reset('/golden/iso');
  await rest('PUT', '/golden/iso.json', { ns: 'other-ns', body: null });
  const rec = new Recorder('isolation', 'Two namespaces write the same path; each reads back its own value.');
  const A = await open(rec, 'A', 'demo-test');
  const B = await open(rec, 'B', 'other-ns');
  await A.req('p', { p: '/golden/iso', d: 'A' });
  await B.req('p', { p: '/golden/iso', d: 'B' });
  await A.req('g', { p: '/golden/iso', q: {} });
  await B.req('g', { p: '/golden/iso', q: {} });
  rec.note("Observed with firebase-database-emulator-v4.11.2: each namespace keeps its own data at the same path.");
  await rec.finish();
}

async function ondisconnect() {
  await reset('/golden/od');
  const rec = new Recorder('ondisconnect', 'A registers onDisconnect put/merge/cancel/increment, closes; listener B sees the result.');
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  await B.req('q', { p: '/golden/od', h: '' });
  await A.req('o', { p: '/golden/od/put', d: 'gone' });
  await A.req('om', { p: '/golden/od', d: { m1: 1, m2: { '.sv': 'timestamp' } } });
  await A.req('o', { p: '/golden/od/cancelme', d: 'x' });
  await A.req('oc', { p: '/golden/od/cancelme' });
  await A.req('o', { p: '/golden/od/inc', d: { '.sv': { increment: 1 } } });
  await A.close();
  await sleep(1500);
  await B.req('g', { p: '/golden/od', q: {} });
  rec.note("Observed with firebase-database-emulator-v4.11.2: o/om/oc are acked immediately with d:\"\"; after A closes, B gets a single m push at golden/od with put, m1, m2 (timestamp resolved at disconnect time) and inc (increment from 0 = 1).");
  rec.note('The onDisconnect writes reached B after A closed, as one coalesced m push; the cancelled one (oc) is absent.');
  await rec.finish();
}

async function listenBasic() {
  await reset('/golden/ls');
  const rec = new Recorder('listen_basic', 'B listens on an empty path; A writes twice; B unlistens; A writes again (no push expected).');
  const A = await open(rec, 'A');
  const B = await open(rec, 'B');
  await B.req('q', { p: '/golden/ls', h: '' });
  await A.req('p', { p: '/golden/ls', d: { x: 1 } });
  await A.req('p', { p: '/golden/ls/y', d: 2 });
  await B.req('n', { p: '/golden/ls' });
  await A.req('p', { p: '/golden/ls', d: { x: 3 } });
  rec.note("Observed with firebase-database-emulator-v4.11.2: a listen on an empty path is acked with d:{} and no data push; pushes are a:\"d\" with the written path; after n (unlisten) no more pushes arrive. The relative order of the writer's ack and the listener's push varies between runs (both orders were seen).");
  await sleep(500);
  await rec.finish();
}

async function unknownAction() {
  const rec = new Recorder('unknown_action', 'Unknown action, a non-JSON text frame, and requests without b.');
  let A = await open(rec, 'A');
  let k = 1;
  const ensure = async () => {
    if (A.isClosed) A = await open(rec, 'A' + ++k);
    return A;
  };
  await (await ensure()).req('zzz', {});
  (await ensure()).send('hello');
  await sleep(TIMEOUT);
  await (await ensure()).req('p', null, { noBody: true });
  await (await ensure()).req('s', null, { noBody: true });
  rec.note("Observed with firebase-database-emulator-v4.11.2: an unknown action, a non-JSON text frame and requests without b all produce the generic t:\"e\" control frame and no reply; the socket stays open and later requests work.");
  // check the connection still works
  await (await ensure()).req('g', { p: '/golden/unknown', q: {} });
  await rec.finish();
}

async function origin() {
  await reset('/golden/origin');
  await rest('PUT', '/golden/origin.json', { ns: 'other-ns', body: null });
  const rec = new Recorder('origin', 'Raw HTTP upgrades: hostile Origin header, namespace from Host without ?ns, and ?ns vs Host mismatch; markers show which namespace received the data.');
  rec.doc.rest = [];
  rec.doc.requests = {};
  const [, port] = HOST.split(':');
  const key = 'dGhlIHNhbXBsZSBub25jZQ==';
  const std = ['Upgrade: websocket', 'Connection: Upgrade', `Sec-WebSocket-Key: ${key}`, 'Sec-WebSocket-Version: 13'];
  const cases = [
    { name: 'A', path: '/.ws?v=5&ns=demo-test', headers: [`Host: ${HOST}`, ...std, 'Origin: https://evil.example'], marker: 'evilOrigin' },
    { name: 'B', path: '/.ws?v=5', headers: [`Host: other-ns.example.com`, ...std], marker: 'hostOnly' },
    { name: 'C', path: '/.ws?v=5&ns=other-ns', headers: [`Host: demo-test.example.com`, ...std], marker: 'nsVsHost' },
    { name: 'D', path: '/.ws?v=5', headers: [`Host: other-ns.example.com:${port}`, ...std], marker: 'hostOnlyWithPort' },
    { name: 'E', path: '/.ws?v=5&ns=demo-test', headers: ['Host: localhost:1234', ...std], marker: 'otherHostHeader' },
  ];
  for (const c of cases) {
    const conn = new RawConn(rec, c.name, c.path, c.headers);
    rec.doc.requests[c.name] = conn.request;
    await Promise.race([conn.httpDone, sleep(3000)]);
    await Promise.race([conn.hello, sleep(2000)]);
    if (conn.upgraded && !conn.isClosed) {
      await conn.req('p', { p: `/golden/origin/${c.marker}`, d: c.marker });
      await conn.req('g', { p: '/golden/origin', q: {} });
    }
    await conn.close();
  }
  for (const ns of ['demo-test', 'other-ns', null]) rec.doc.rest.push(await rest('GET', '/golden/origin.json', { ns }));
  rec.note('A: hostile Origin; B: no ns, Host other-ns.example.com; C: ns=other-ns, Host demo-test.example.com; D: no ns, Host other-ns.example.com:<port>; E: ns=demo-test, Host localhost:1234 (to see whether hello h follows Host).');
  rec.note('requests holds the exact HTTP upgrade request text sent per connection (fixed Sec-WebSocket-Key).');
  rec.note('rest holds REST GETs of /golden/origin on ns=demo-test, ns=other-ns and with no ns (Host 127.0.0.1) after all markers were written.');
  rec.note("Observed with firebase-database-emulator-v4.11.2: Origin is not checked (101 for https://evil.example). Without ?ns the namespace is the first label of the Host header (other-ns.example.com -> other-ns, with or without port); when ?ns is present it wins over Host. The hello h field stays the emulator host:port whatever the Host header says. X-Firebase-Project-Id names the chosen namespace.");
  await rec.finish();
}

const all = {
  handshake,
  put_get: putGet,
  update_multipath: updateMultipath,
  datastale,
  server_values: serverValues,
  limits,
  isolation,
  ondisconnect,
  listen_basic: listenBasic,
  unknown_action: unknownAction,
  origin,
};
const only = process.env.SCENARIOS ? process.env.SCENARIOS.split(',') : Object.keys(all);
for (const name of only) await all[name]();
