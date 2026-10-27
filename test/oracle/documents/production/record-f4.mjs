// F4 production recordings for the Document Database's Listen and Write streams
// (ADR-026 §1 and §6):
//
//   f4-listen.json        raw Listen frames, in order, with times relative to
//                         the stream's opening: a document target and its
//                         update and delete, a missing document, a query target
//                         (enter, change, leave, delete), a limit target at the
//                         window edge, remove_target, two targets on one stream,
//                         resumes by resume_token (insert, leave, delete while
//                         disconnected, with and without expected_count), resumes
//                         by read_time, garbled and foreign tokens, and every
//                         target the server refuses
//   f4-write-stream.json  raw Write frames: the handshake, batches with
//                         set / update / delete / transforms, pipelined batches,
//                         an empty batch, precondition failures, stream_token
//                         reuse and the resumption of a stream
//   f4-cleanup.json       the deletion of everything written, verified
//
//   node record-f4.mjs [--only=listen,write]
//
// gRPC only (Listen and Write have no REST form). Environment:
//   FIRESTORE_PROTOS   a build/protos directory of @google-cloud/firestore
//   NODE_OPTIONS       --dns-result-order=ipv4first
//
// Data is written ONLY under top-level collections whose id starts with
// aruma-ref-f4c (assertScoped refuses anything outside aruma-ref, and every
// stream message, received or sent, passes assertMessageScoped before anything
// is written to disk). Every Listen target is scoped to one of those
// collections, or to a document of one: no query at the database root, and the
// only allDescendants targets have a document of aruma-ref-f4c-rj as parent.
// A kindless sweep over every collection id from "aruma-ref-f4c" up to
// "aruma-ref-f4d" runs first and last. Nothing creates or changes an index,
// rule, TTL policy or project setting.
import { randomBytes } from 'node:crypto';
import {
  PROJECT, DB, ROOT, PREFIX, rest, v, fields, versions, writeCorpus, assertScoped, assertMessageScoped,
  grpcAvailable, grpcDuplex, grpcCodeName, grpcVersions,
} from './lib.mjs';

const GENERATOR = 'test/oracle/documents/production/record-f4.mjs';
const MIN_ID = '__id-9223372036854775808__';
const DOC = 'aruma-ref-f4c-doc'; // document targets
const Q = 'aruma-ref-f4c-q'; // query target
const LIM = 'aruma-ref-f4c-lim'; // limit target
const MULTI = 'aruma-ref-f4c-multi'; // two targets on one stream
const RES = 'aruma-ref-f4c-res'; // resume by token
const RT = 'aruma-ref-f4c-rt'; // resume by read_time
const RJ = 'aruma-ref-f4c-rj'; // refused targets
const W = 'aruma-ref-f4c-w'; // the Write stream
const TOPS = [DOC, Q, LIM, MULTI, RES, RT, RJ, W];

const log = (s) => process.stderr.write(s);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const relProject = (s) => s.split(`projects/${PROJECT}/`).join('projects/{project}/');
const rel = (name) => name.replace(`${ROOT}/`, '');

// ---- writes, tracked for the cleanup ----

const written = new Set();
let writeCount = 0;

async function commitWrites(writes) {
  for (const w of writes) assertScoped(w.update?.name ?? w.delete ?? w.transform?.document ?? '');
  for (const w of writes) {
    const n = w.update?.name ?? w.delete ?? w.transform?.document;
    if (n) written.add(n);
  }
  const r = await rest('POST', `${DB}/documents:commit`, { writes });
  if (r.http === 429) throw new Error('HTTP 429 from Firestore: stopping, as agreed');
  if (r.http !== 200) throw new Error(`commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  writeCount += writes.length;
  return r.body.commitTime;
}

const upd = (path, data, mask) => ({
  update: { name: `${ROOT}/${path}`, fields: fields(data) },
  ...(mask ? { updateMask: { fieldPaths: mask } } : {}),
});
const del = (path) => ({ delete: `${ROOT}/${path}` });

function parseTime(s) {
  const m = /^(.*?)(?:\.(\d+))?Z$/.exec(s);
  return { seconds: String(Math.floor(Date.parse(`${m[1]}Z`) / 1000)), nanos: Number((m[2] || '').padEnd(9, '0')) };
}

// ---- sessions: one stream, every frame with its time ----

const isTs = (x) => x && typeof x === 'object' && !Array.isArray(x) && !Buffer.isBuffer(x) && 'seconds' in x && Object.keys(x).every((k) => k === 'seconds' || k === 'nanos');
const tsKey = (x) => `${x.seconds}.${String(x.nanos || 0).padStart(9, '0')}`;

class Session {
  constructor(method, call, sink) {
    this.method = method;
    this.call = call;
    this.sink = sink; // the case's event list
    this.t0 = Date.now();
    this.closed = false;
    this.violation = null;
    this.recvCount = 0;
    this.lastRecv = Date.now();
    this.stream = sink.sessions++;
    call.on('data', (d) => {
      this.recvCount++;
      this.lastRecv = Date.now();
      try {
        assertMessageScoped(d);
      } catch (e) {
        this.violation = e;
      }
      this.push({ dir: 'recv', msg: d });
    });
    call.on('status', (st) => {
      this.closed = true;
      this.push({ dir: 'end', status: { code: st.code, status: grpcCodeName(st.code), message: st.details } });
    });
    call.on('error', () => {});
  }
  push(e) {
    this.sink.events.push({ t: Date.now() - this.t0, stream: this.stream, ...e });
  }
  check() {
    if (this.violation) throw this.violation;
  }
  send(msg) {
    assertMessageScoped(msg);
    this.push({ dir: 'send', msg });
    this.call.write(msg);
  }
  // mark records something done outside the stream (a commit), with its commit time.
  mark(action, commitTime) {
    const e = { t: Date.now() - this.t0, stream: this.stream, dir: 'action', action, ...(commitTime ? { commitTime: parseTime(commitTime) } : {}) };
    this.sink.events.push(e);
    return e;
  }
  // settle waits for at least one new frame (up to max ms), then until the
  // stream has been quiet for quiet ms. With min, waits at least that long.
  async settle({ max = 8000, quiet = 1800, min = 0 } = {}) {
    const start = Date.now();
    const base = this.recvCount;
    while (Date.now() - start < max && !this.closed) {
      await sleep(40);
      const el = Date.now() - start;
      if (el < min) continue;
      if (this.recvCount > base && Date.now() - this.lastRecv >= quiet) break;
    }
    if (this.recvCount === base && !this.closed) await sleep(min ? 0 : 600);
    this.check();
  }
  async idle(ms) {
    await sleep(ms);
    this.check();
  }
  async close() {
    if (!this.closed) {
      this.call.end();
      for (let i = 0; i < 60 && !this.closed; i++) await sleep(50);
      if (!this.closed) {
        this.push({ dir: 'note', note: 'still open 3 s after the client half-closed; cancelled by the recorder' });
        this.call.cancel();
        for (let i = 0; i < 40 && !this.closed; i++) await sleep(50);
      }
    }
    this.check();
  }
  // lastToken is the resume_token of the last target change that carries one.
  lastToken(type) {
    for (let i = this.sink.events.length - 1; i >= 0; i--) {
      const e = this.sink.events[i];
      if (e.stream !== this.stream || e.dir !== 'recv') continue;
      const tc = e.msg.targetChange;
      if (tc && tc.resumeToken && tc.resumeToken.length && (!type || tc.targetChangeType === type)) return tc.resumeToken;
    }
    return undefined;
  }
  lastReadTime() {
    for (let i = this.sink.events.length - 1; i >= 0; i--) {
      const e = this.sink.events[i];
      if (e.stream === this.stream && e.dir === 'recv' && e.msg.targetChange?.readTime) return e.msg.targetChange.readTime;
    }
    return undefined;
  }
}

async function open(method, sink) {
  const call = await grpcDuplex(method, DB);
  return new Session(method, call, sink);
}

// ---- turning the recorded events into the corpus form ----

// render replaces timestamps by labels in time order (equal values share one),
// byte strings by labels (equal contents share one) and stream ids likewise; the
// tables of what the labels stand for go beside the frames.
function render(events) {
  const times = new Map();
  const bytes = new Map();
  const ids = new Map();
  const collect = (x) => {
    if (Buffer.isBuffer(x)) bytes.set(x.toString('hex'), x);
    else if (isTs(x)) times.set(tsKey(x), x);
    else if (Array.isArray(x)) x.forEach(collect);
    else if (x && typeof x === 'object') {
      for (const [k, y] of Object.entries(x)) {
        if (k === 'streamId' && typeof y === 'string') ids.set(y, y);
        else collect(y);
      }
    }
  };
  events.forEach((e) => collect(e.msg ?? e.commitTime));
  const tkeys = [...times.keys()].sort((a, b) => {
    const [as, an] = a.split('.');
    const [bs, bn] = b.split('.');
    return BigInt(as) === BigInt(bs) ? an.localeCompare(bn) : BigInt(as) < BigInt(bs) ? -1 : 1;
  });
  const tlabel = new Map(tkeys.map((k, i) => [k, `<ts t${i + 1}>`]));
  const blabel = new Map([...bytes.keys()].map((h, i) => [h, `<bytes b${i + 1} len=${bytes.get(h).length}>`]));
  const ilabel = new Map([...ids.keys()].map((s, i) => [s, `<stream_id s${i + 1} len=${s.length}>`]));
  const walk = (x, key) => {
    if (Buffer.isBuffer(x)) return blabel.get(x.toString('hex'));
    if (isTs(x)) return tlabel.get(tsKey(x));
    if (Array.isArray(x)) return x.map((y) => walk(y));
    if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, walk(y, k)]));
    if (typeof x === 'string') {
      if (key === 'streamId') return ilabel.get(x);
      return relProject(x);
    }
    return x;
  };
  const kindOf = (e) => {
    if (e.dir === 'send') {
      const m = e.msg;
      if (e.method === 'listen') return m.addTarget ? 'add_target' : m.removeTarget !== undefined ? 'remove_target' : 'listen_request';
      return m.streamId || (m.streamToken && m.streamToken.length) ? 'write_request' : m.writes && m.writes.length ? 'write_request_no_stream' : 'handshake_or_empty';
    }
    if (e.dir !== 'recv') return undefined;
    const m = e.msg;
    if (m.targetChange) return `target_change:${m.targetChange.targetChangeType || 'NO_CHANGE'}${m.targetChange.targetIds?.length ? '' : ':global'}`;
    for (const k of ['documentChange', 'documentDelete', 'documentRemove', 'filter', 'streamId']) if (m[k] !== undefined) return k === 'streamId' ? 'write_response' : k;
    return 'write_response';
  };
  const frames = events.map((e, i) => {
    const f = { seq: i, stream: e.stream, tMs: e.t, dir: e.dir };
    const kind = kindOf(e);
    if (kind) f.kind = kind;
    if (e.action) f.action = e.action;
    if (e.note) f.note = e.note;
    if (e.status) f.status = walk(e.status);
    if (e.commitTime) f.commitTime = walk(e.commitTime);
    if (e.msg) f.msg = walk(e.msg);
    return f;
  });
  const t1 = tkeys.length ? BigInt(tkeys[0].split('.')[0]) * 1_000_000_000n + BigInt(tkeys[0].split('.')[1]) : 0n;
  const timestamps = Object.fromEntries(tkeys.map((k, i) => {
    const [s, n] = k.split('.');
    const ns = BigInt(s) * 1_000_000_000n + BigInt(n);
    return [`t${i + 1}`, { offsetMicrosFromT1: Number((ns - t1) / 1000n) }];
  }));
  const tokens = Object.fromEntries([...bytes.entries()].map(([h, b], i) => [`b${i + 1}`, { length: b.length, hex: h }]));
  return { frames, tables: { timestamps, bytes: tokens, streamIds: Object.fromEntries([...ids.keys()].map((s, i) => [`s${i + 1}`, { length: s.length }])) } };
}

// ---- cases ----

const cases = [];
const extra = {}; // values carried from one case to a later one

async function runCase(id, description, setup, fn) {
  log(`${id} `);
  const sink = { events: [], sessions: 0 };
  let error;
  try {
    await fn(sink);
  } catch (e) {
    if (/outside aruma-ref|HTTP 429/.test(e.message)) throw e;
    error = e;
  }
  for (const e of sink.events) if (e.msg) assertMessageScoped(e.msg);
  const method = id.startsWith('write') ? 'write' : 'listen';
  sink.events.forEach((e) => (e.method = method));
  const r = render(sink.events);
  const c = {
    id, description, transport: 'grpc',
    rpc: `google.firestore.v1.Firestore/${method === 'listen' ? 'Listen' : 'Write'}`,
    streams: sink.sessions,
    setup: setup ? JSON.parse(relProject(JSON.stringify(setup))) : [],
    frames: r.frames,
    labels: r.tables,
  };
  if (error) c.recorderError = error.message;
  cases.push(c);
  return c;
}

// toGrpc converts a REST JSON value tree to what proto-loader expects (the null
// enum name); byte strings stay Buffers.
function toGrpc(x) {
  if (Buffer.isBuffer(x)) return x;
  if (Array.isArray(x)) return x.map(toGrpc);
  if (x && typeof x === 'object') return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, k === 'nullValue' ? 'NULL_VALUE' : toGrpc(y)]));
  return x;
}

// ---- Listen helpers ----

const nm = (p) => `${ROOT}/${p}`;
const docTarget = (targetId, paths, extra = {}) => ({ targetId, documents: { documents: paths.map(nm) }, ...extra });
const gq = (sq) => {
  const conv = (x, key) => {
    if (Array.isArray(x)) return x.map((y) => conv(y));
    if (x && typeof x === 'object') {
      return Object.fromEntries(Object.entries(x).map(([k, y]) => [k, k === 'nullValue' ? 'NULL_VALUE' : k === 'limit' && typeof y === 'number' ? { value: y } : conv(y, k)]));
    }
    return x;
  };
  return conv(sq);
};
const queryTarget = (targetId, sq, parent = ROOT, extra = {}) => ({ targetId, query: { parent, structuredQuery: gq(sq) }, ...extra });
const F = (f, op, x) => ({ fieldFilter: { field: { fieldPath: f }, op, value: v(x) } });
const ORD = (f, d = 'ASCENDING') => ({ field: { fieldPath: f }, direction: d });
const add = (s, target) => s.send({ database: DB, addTarget: toGrpc(target) });
const rm = (s, id) => s.send({ database: DB, removeTarget: id });
const SEC = (ms) => {
  const t = Date.now() + ms;
  return { seconds: String(Math.floor(t / 1000)), nanos: (t % 1000) * 1_000_000 };
};

// act commits writes while a stream is open and marks the commit on it.
async function act(s, label, writes) {
  const e = s.mark(label);
  const ct = await commitWrites(writes);
  e.commitTime = parseTime(ct);
  return ct;
}

async function listenCases() {
  // 1. a document target, then an update and a delete of the document
  {
    const setup = [upd(`${DOC}/d1`, { n: 1, s: 'a' })];
    await runCase('listen.doc-target', 'Listen on one existing document, then an update and a delete of it', setup, async (sink) => {
      await commitWrites(setup);
      const s = await open('listen', sink);
      add(s, docTarget(1, [`${DOC}/d1`]));
      await s.settle();
      extra.docToken = s.lastToken('CURRENT');
      await act(s, 'commit: update aruma-ref-f4c-doc/d1 {n: 2} (mask n)', [upd(`${DOC}/d1`, { n: 2 }, ['n'])]);
      await s.settle();
      await act(s, 'commit: delete aruma-ref-f4c-doc/d1', [del(`${DOC}/d1`)]);
      await s.settle();
      await s.idle(1500);
      await s.close();
    });
  }
  // 2. a missing document
  await runCase('listen.doc-missing', 'Listen on a document that does not exist, then create it and delete it', [], async (sink) => {
    const s = await open('listen', sink);
    add(s, docTarget(1, [`${DOC}/missing`]));
    await s.settle();
    await act(s, 'commit: create aruma-ref-f4c-doc/missing {n: 1}', [upd(`${DOC}/missing`, { n: 1 })]);
    await s.settle();
    await act(s, 'commit: delete aruma-ref-f4c-doc/missing', [del(`${DOC}/missing`)]);
    await s.settle();
    await s.close();
  });
  // 3. a query target: enter, change, leave by field change, delete
  {
    const setup = [upd(`${Q}/a`, { rank: 1 }), upd(`${Q}/b`, { rank: 2 }), upd(`${Q}/c`, { rank: 3 })];
    await runCase('listen.query-target', 'Listen on rank >= 2 order by rank: a document enters, one changes, one leaves by a field change, one is deleted', setup, async (sink) => {
      await commitWrites(setup);
      const s = await open('listen', sink);
      add(s, queryTarget(1, { from: [{ collectionId: Q }], where: F('rank', 'GREATER_THAN_OR_EQUAL', 2), orderBy: [ORD('rank')] }));
      await s.settle();
      await act(s, 'commit: create q/d {rank: 5} (enters)', [upd(`${Q}/d`, { rank: 5 })]);
      await s.settle();
      await act(s, 'commit: update q/b {rank: 9} (stays, changes)', [upd(`${Q}/b`, { rank: 9 }, ['rank'])]);
      await s.settle();
      await act(s, 'commit: update q/c {rank: 0} (leaves by field change)', [upd(`${Q}/c`, { rank: 0 }, ['rank'])]);
      await s.settle();
      await act(s, 'commit: delete q/d', [del(`${Q}/d`)]);
      await s.settle();
      await act(s, 'commit: update q/a {rank: 1, note: x} (outside the query, changes)', [upd(`${Q}/a`, { note: 'x' }, ['note'])]);
      await s.idle(2500);
      await act(s, 'commit: create q/e and update q/b in one commit (one batch)', [upd(`${Q}/e`, { rank: 4 }), upd(`${Q}/b`, { rank: 10 }, ['rank'])]);
      await s.settle();
      await s.close();
    });
  }
  // 4. a limit target: members leave at the window edge
  {
    const setup = [upd(`${LIM}/r1`, { rank: 1 }), upd(`${LIM}/r2`, { rank: 2 }), upd(`${LIM}/r3`, { rank: 3 }), upd(`${LIM}/r4`, { rank: 4 })];
    await runCase('listen.limit-target', 'Listen on order by rank limit 2 (r1, r2): delete r1, then insert at the front, then change a doc outside the window, then a member leaves by field change', setup, async (sink) => {
      await commitWrites(setup);
      const s = await open('listen', sink);
      add(s, queryTarget(1, { from: [{ collectionId: LIM }], orderBy: [ORD('rank')], limit: 2 }));
      await s.settle();
      await act(s, 'commit: delete lim/r1 (window edge member; next is r3)', [del(`${LIM}/r1`)]);
      await s.settle();
      await act(s, 'commit: create lim/r0 {rank: 0} (enters at the front, pushes r3 out)', [upd(`${LIM}/r0`, { rank: 0 })]);
      await s.settle();
      await act(s, 'commit: update lim/r4 {note: x} (outside the window)', [upd(`${LIM}/r4`, { note: 'x' }, ['note'])]);
      await s.idle(2500);
      await act(s, 'commit: update lim/r2 {rank: 50} (member moves behind the window)', [upd(`${LIM}/r2`, { rank: 50 }, ['rank'])]);
      await s.settle();
      await s.close();
    });
  }
  // 5. remove_target, remove then add the same id, and odd ids
  {
    const setup = [upd(`${DOC}/d5`, { n: 1 })];
    await runCase('listen.remove-target', 'remove_target, then add_target with the same id; remove of an id never added; add with target_id 0; a duplicate id', setup, async (sink) => {
      await commitWrites(setup);
      const s = await open('listen', sink);
      add(s, docTarget(1, [`${DOC}/d5`]));
      await s.settle();
      rm(s, 1);
      await s.settle();
      await act(s, 'commit: update doc/d5 {n: 2} (no target is listening)', [upd(`${DOC}/d5`, { n: 2 }, ['n'])]);
      await s.idle(2000);
      add(s, docTarget(1, [`${DOC}/d5`]));
      await s.settle();
      rm(s, 99);
      await s.settle();
      add(s, docTarget(1, [`${DOC}/d5`]));
      await s.settle();
      await s.close();
      const s2 = await open('listen', sink);
      s2.mark('second stream: target_id 0 (server assigns), then a manual id after it');
      add(s2, docTarget(0, [`${DOC}/d5`]));
      await s2.settle();
      add(s2, docTarget(7, [`${DOC}/d5`]));
      await s2.settle();
      await s2.close();
      const s3 = await open('listen', sink);
      s3.mark('third stream: a manual id, then target_id 0 after it');
      add(s3, docTarget(7, [`${DOC}/d5`]));
      await s3.settle();
      add(s3, docTarget(0, [`${DOC}/d5`]));
      await s3.settle();
      await s3.close();
      return;
      await s.close();
    });
  }
  // 6. two targets on one stream
  {
    const setup = [upd(`${MULTI}/m1`, { rank: 1 }), upd(`${MULTI}/m2`, { rank: 2 })];
    await runCase('listen.two-targets', 'Two targets on one stream (a document target 1 and a query target 2 over the same collection): are the global snapshots joined', setup, async (sink) => {
      await commitWrites(setup);
      const s = await open('listen', sink);
      add(s, docTarget(1, [`${MULTI}/m1`]));
      add(s, queryTarget(2, { from: [{ collectionId: MULTI }], orderBy: [ORD('rank')] }));
      await s.settle();
      await act(s, 'commit: update multi/m1 {rank: 5} (in both targets)', [upd(`${MULTI}/m1`, { rank: 5 }, ['rank'])]);
      await s.settle();
      await act(s, 'commit: update multi/m2 {rank: 6} (in target 2 only)', [upd(`${MULTI}/m2`, { rank: 6 }, ['rank'])]);
      await s.settle();
      await act(s, 'commit: delete multi/m1 (both)', [del(`${MULTI}/m1`)]);
      await s.settle();
      await s.close();
    });
  }
}

// 7 and 8: resumes
async function resumeCases() {
  const setup = [upd(`${RES}/x1`, { rank: 1 }), upd(`${RES}/x2`, { rank: 2 }), upd(`${RES}/x3`, { rank: 3 })];
  const sq = { from: [{ collectionId: RES }], where: F('rank', 'GREATER_THAN_OR_EQUAL', 1), orderBy: [ORD('rank')] };
  const target = (token, count, id = 1) => queryTarget(id, sq, ROOT, { resumeToken: token, ...(count === undefined ? {} : { expectedCount: { value: count } }) });
  let T0, T1, T2, T3;
  await runCase('listen.resume-token', 'Resume by resume_token after documents were inserted, left the query by a field change and were deleted while disconnected; with and without expected_count', setup, async (sink) => {
    await commitWrites(setup);
    const step = async (title, token, count) => {
      const s = await open('listen', sink);
      s.mark(`stream ${s.stream}: ${title}`);
      add(s, target(token, count));
      await s.settle();
      const t = s.lastToken('CURRENT') ?? s.lastToken();
      await s.close();
      return t;
    };
    T0 = await step('fresh target (x1, x2, x3); the token of its CURRENT is T0', undefined, undefined);
    let s = await open('listen', sink);
    s.mark('disconnected: the following commit happens while no stream is open');
    await s.close();
    await commitWrites([upd(`${RES}/x4`, { rank: 4 })]);
    s.mark('commit while disconnected: create res/x4 {rank: 4}');
    T1 = await step('resume T0 expected_count 3 (client holds x1,x2,x3); x4 was inserted', T0, 3);
    await step('resume T0 without expected_count; x4 was inserted', T0, undefined);
    s = await open('listen', sink);
    await commitWrites([upd(`${RES}/x2`, { rank: 0 }, ['rank'])]);
    s.mark('commit while disconnected: update res/x2 {rank: 0} (leaves the query)');
    await s.close();
    T2 = await step('resume T1 expected_count 4 (client holds x1,x2,x3,x4); x2 left by field change', T1, 4);
    await step('resume T1 without expected_count; x2 left by field change', T1, undefined);
    s = await open('listen', sink);
    await commitWrites([del(`${RES}/x3`)]);
    s.mark('commit while disconnected: delete res/x3');
    await s.close();
    T3 = await step('resume T2 expected_count 3 (client holds x1,x3,x4); x3 was deleted', T2, 3);
    await step('resume T2 without expected_count; x3 was deleted', T2, undefined);
    await step('resume T0 expected_count 3 after all three changes (insert, leave, delete); the result is x1, x4', T0, 3);
    await step('resume T0 without expected_count after all three changes', T0, undefined);
    await step('resume T3 expected_count 2 with nothing changed since (client holds x1,x4)', T3, 2);
    await step('resume T3 expected_count 7 (a count that is wrong) with nothing changed', T3, 7);
    await step('resume T3 without expected_count with nothing changed', T3, undefined);
    extra.queryToken = T0;
  });

  // read_time resumes
  const rsetup = [upd(`${RT}/y1`, { rank: 1 })];
  await runCase('listen.resume-read-time', 'Resume by read_time: after creation of the first document, before it, 70 minutes, 25 hours and 8 days ago, and in the future; query and document targets', rsetup, async (sink) => {
    const c1 = await commitWrites(rsetup);
    await sleep(2500);
    const c2 = await commitWrites([upd(`${RT}/y2`, { rank: 2 })]);
    await sleep(1500);
    const rq = { from: [{ collectionId: RT }], orderBy: [ORD('rank')] };
    const tc1 = parseTime(c1);
    const plus = (t, ms) => {
      const total = BigInt(t.seconds) * 1000n + BigInt(Math.floor(t.nanos / 1e6)) + BigInt(ms);
      return { seconds: String(total / 1000n), nanos: Number(total % 1000n) * 1_000_000 };
    };
    const probe = async (title, target) => {
      const s = await open('listen', sink);
      s.mark(`stream ${s.stream}: ${title}`);
      add(s, target);
      await s.settle({ max: 7000 });
      await s.close();
    };
    const q = (readTime, count) => queryTarget(1, rq, ROOT, { readTime, ...(count === undefined ? {} : { expectedCount: { value: count } }) });
    s0(sink, 'y1 was created at the first commit time, y2 about 2.5 s later', c1);
    await probe('query, read_time = y1 commit time + 1 ms (y2 is newer), expected_count 1', q(plus(tc1, 1), 1));
    await probe('query, read_time = y1 commit time + 1 ms, no expected_count', q(plus(tc1, 1)));
    await probe('query, read_time = y1 commit time - 1 s (before any document), expected_count 0', q(plus(tc1, -1000), 0));
    await probe('query, read_time = 70 minutes ago, expected_count 0', q(SEC(-70 * 60_000), 0));
    await probe('query, read_time = 70 minutes ago, no expected_count', q(SEC(-70 * 60_000)));
    await probe('query, read_time = 25 hours ago, expected_count 0', q(SEC(-25 * 3600_000), 0));
    await probe('query, read_time = 8 days ago, expected_count 0', q(SEC(-8 * 86400_000), 0));
    await probe('query, read_time = now + 1 hour', q(SEC(3600_000)));
    await probe('document target y1, read_time = 70 minutes ago', docTarget(1, [`${RT}/y1`], { readTime: SEC(-70 * 60_000) }));
    await probe('document target y2, read_time = y1 commit time + 1 ms', docTarget(1, [`${RT}/y2`], { readTime: plus(tc1, 1) }));
  });
  // garbled and foreign tokens
  await runCase('listen.resume-bad-token', 'Resume by a garbled token, an empty token, a token of another target, a token from a document target, and a token with a changed byte', [], async (sink) => {
    const probe = async (title, target) => {
      const s = await open('listen', sink);
      s.mark(`stream ${s.stream}: ${title}`);
      add(s, target);
      await s.settle({ max: 7000 });
      await s.close();
    };
    const rq = { from: [{ collectionId: RT }], orderBy: [ORD('rank')] };
    await probe('garbled token: 20 fixed bytes', queryTarget(1, rq, ROOT, { resumeToken: Buffer.from('0102030405060708090a0b0c0d0e0f1011121314', 'hex') }));
    await probe('garbled token: the ASCII text "not-a-token"', queryTarget(1, rq, ROOT, { resumeToken: Buffer.from('not-a-token') }));
    await probe('empty token (zero bytes)', queryTarget(1, rq, ROOT, { resumeToken: Buffer.alloc(0) }));
    if (extra.queryToken) {
      await probe('the token T0 of the res query (another collection) on the rt query', queryTarget(1, rq, ROOT, { resumeToken: extra.queryToken }));
      const flipped = Buffer.from(extra.queryToken);
      flipped[flipped.length - 1] ^= 0xff;
      await probe('T0 with its last byte flipped, on the original res query', queryTarget(1, { from: [{ collectionId: RES }], where: F('rank', 'GREATER_THAN_OR_EQUAL', 1), orderBy: [ORD('rank')] }, ROOT, { resumeToken: flipped }));
      await probe('T0 on a document target', docTarget(1, [`${RT}/y1`], { resumeToken: extra.queryToken }));
    }
    if (extra.docToken) await probe('the token of a document target on a query target', queryTarget(1, rq, ROOT, { resumeToken: extra.docToken }));
  });
}

function s0(sink, note, ct) {
  sink.events.push({ t: 0, stream: -1, dir: 'action', action: note, commitTime: parseTime(ct) });
}

// 9: refused targets
async function refusedCases() {
  const setup = [upd(`${RJ}/p`, { a: 1 })];
  await runCase('listen.refused-targets', 'Targets the server may refuse: each on a fresh stream, followed by a valid sentinel target 99 to see whether the stream stays open', setup, async (sink) => {
    await commitWrites(setup);
    const P = `${ROOT}/${RJ}/p`;
    const probe = async (title, target, { sentinel = true, raw } = {}) => {
      const s = await open('listen', sink);
      s.mark(`stream ${s.stream}: ${title}`);
      if (raw) s.send(raw);
      else add(s, target);
      await s.settle({ max: 7000 });
      if (sentinel && !s.closed) {
        s.mark('sentinel: a valid document target 99 on the same stream');
        add(s, docTarget(99, [`${RJ}/p`]));
        await s.settle({ max: 7000 });
      }
      await s.close();
    };
    const base = { from: [{ collectionId: RJ }] };
    await probe('allDescendants without collection id, parent = a document', queryTarget(5, { from: [{ allDescendants: true }] }, P));
    await probe('allDescendants with a collection id (collection group "sub"), parent = a document', queryTarget(5, { from: [{ collectionId: 'sub', allDescendants: true }] }, P));
    await probe('collection group with a collection id, parent = a document, plain subcollection (not allDescendants)', queryTarget(5, { from: [{ collectionId: 'sub' }] }, P));
    await probe('findNearest on the collection', queryTarget(5, { ...base, findNearest: { vectorField: { fieldPath: 'emb' }, queryVector: v({ $vector: [1, 2, 3] }), distanceMeasure: 'EUCLIDEAN', limit: 2 } }));
    await probe('select projection (field a)', queryTarget(5, { ...base, select: { fields: [{ fieldPath: 'a' }] } }));
    await probe('select projection of __name__ only', queryTarget(5, { ...base, select: { fields: [{ fieldPath: '__name__' }] } }));
    await probe('offset 1', queryTarget(5, { ...base, orderBy: [ORD('a')], offset: 1 }));
    await probe('limit 1 with offset 1', queryTarget(5, { ...base, orderBy: [ORD('a')], limit: 1, offset: 1 }));
    await probe('missing composite index: a == 1 order by b', queryTarget(5, { ...base, where: F('a', 'EQUAL', 1), orderBy: [ORD('b')] }));
    await probe('accepted check: OR of two equalities', queryTarget(5, { ...base, where: { compositeFilter: { op: 'OR', filters: [F('a', 'EQUAL', 1), F('a', 'EQUAL', 2)] } } }));
    await probe('accepted check: a != 7', queryTarget(5, { ...base, where: F('a', 'NOT_EQUAL', 7) }));
    await probe('accepted check: a not-in [7, 8]', queryTarget(5, { ...base, where: F('a', 'NOT_IN', [7, 8]) }));
    await probe('accepted check: a in [1, 2]', queryTarget(5, { ...base, where: F('a', 'IN', [1, 2]) }));
    await probe('accepted check: a array-contains-any [1, 2]', queryTarget(5, { ...base, where: F('a', 'ARRAY_CONTAINS_ANY', [1, 2]) }));
    await probe('accepted check: order by __name__ descending', queryTarget(5, { ...base, orderBy: [ORD('__name__', 'DESCENDING')] }));
    await probe('wrong project in the query parent', queryTarget(5, base, `projects/aruma-ref-f4c-noproject/databases/(default)/documents`));
    await probe('wrong project in a documents target', {
      targetId: 5, documents: { documents: [`projects/aruma-ref-f4c-noproject/databases/(default)/documents/${RJ}/p`] },
    });
    await probe('wrong database in the query parent', queryTarget(5, base, `projects/${PROJECT}/databases/aruma-ref-f4c-nodb/documents`));
    await probe('wrong database in a documents target', {
      targetId: 5, documents: { documents: [`projects/${PROJECT}/databases/aruma-ref-f4c-nodb/documents/${RJ}/p`] },
    });
    await probe('documents target with a collection path (odd number of segments)', { targetId: 5, documents: { documents: [`${ROOT}/${RJ}`] } });
    await probe('documents target with no documents', { targetId: 5, documents: { documents: [] } });
    await probe('documents target with two documents', docTarget(5, [`${RJ}/p`, `${RJ}/p2`]));
    await probe('add_target with a target id only (no query, no documents)', { targetId: 5 });
    await probe('negative target id', docTarget(-1, [`${RJ}/p`]));
    await probe('once = true on a document target', docTarget(5, [`${RJ}/p`], { once: true }));
    await probe('once = true on a query target', queryTarget(5, base, ROOT, { once: true }));
    await probe('a ListenRequest with no target change', null, { raw: { database: DB } });
    await probe('a ListenRequest for a database that is not the stream\'s (database field only differs)', null, {
      raw: { database: `projects/${PROJECT}/databases/aruma-ref-f4c-nodb`, addTarget: toGrpc(docTarget(5, [`${RJ}/p`])) },
    });
    await probe('a query with a bad field path (empty string)', queryTarget(5, { ...base, where: F('', 'EQUAL', 1) }));
    await probe('a query with 31 filters ANDed', queryTarget(5, { ...base, where: { compositeFilter: { op: 'AND', filters: Array.from({ length: 31 }, (_, i) => F(`f${i}`, 'EQUAL', 1)) } } }));
    // a document target at the project's root path form
    await probe('documents target on a nonexistent subdocument path (rj/p/sub/s)', docTarget(5, [`${RJ}/p/sub/s`]));
  });
}

// ---- the Write stream ----

const wreq = (m) => ({ database: DB, ...m });
const SERVER_TS = { setToServerValue: 'REQUEST_TIME' };
const tf = (fieldPath, op) => ({ fieldPath, ...op });

async function writeCases() {
  // a Write batch helper
  const names = (writes) => {
    for (const w of writes) {
      const n = w.update?.name ?? w.delete ?? w.transform?.document;
      assertScoped(n);
      written.add(n);
    }
  };
  const batch = (s, writes, { token, streamId, label } = {}) => {
    names(writes);
    if (label) s.mark(label);
    s.send(wreq({ ...(streamId ? { streamId } : {}), writes: writes.map(toGrpc), ...(token ? { streamToken: token } : {}) }));
    writeCount += writes.length;
  };
  const last = (s) => {
    for (let i = s.sink.events.length - 1; i >= 0; i--) {
      const e = s.sink.events[i];
      if (e.stream === s.stream && e.dir === 'recv' && e.msg.streamToken && e.msg.streamToken.length) return { token: e.msg.streamToken, streamId: e.msg.streamId };
    }
    return {};
  };
  const handshake = async (s) => {
    s.send(wreq({}));
    await s.settle({ max: 6000 });
    return last(s);
  };

  await runCase('write.handshake-and-batches', 'Handshake, then batches: set, update with mask, delete, update_transforms of every kind, the legacy transform, an empty batch, and the stream_token of each response', [], async (sink) => {
    const s = await open('write', sink);
    let st = await handshake(s);
    const w = (p) => `${W}/${p}`;
    batch(s, [
      upd(w('a'), { n: 1, s: 'x', keep: true }),
      upd(w('b'), { n: 2 }, ['n']),
      del(w('gone')),
    ], { token: st.token, label: 'batch 1: set a, update b with mask (creates), delete of a missing document' });
    await s.settle({ max: 6000 });
    st = last(s);
    batch(s, [
      {
        ...upd(w('a'), { n: 5 }, ['n']),
        updateTransforms: [
          tf('ts', SERVER_TS),
          tf('inc', { increment: { integerValue: '2' } }),
          tf('union', { appendMissingElements: { values: [{ integerValue: '1' }, { integerValue: '2' }] } }),
          tf('remove', { removeAllFromArray: { values: [{ integerValue: '9' }] } }),
          tf('mx', { maximum: { integerValue: '7' } }),
          tf('mn', { minimum: { integerValue: '3' } }),
        ],
      },
      { transform: { document: nm(w('b')), fieldTransforms: [tf('legacy', SERVER_TS), tf('legacyInc', { increment: { integerValue: '1' } })] } },
    ], { token: st.token, label: 'batch 2: update with six update_transforms; the legacy transform write on b' });
    await s.settle({ max: 6000 });
    st = last(s);
    batch(s, [{ ...upd(w('t'), {}, []), updateTransforms: [tf('ts', SERVER_TS)] }], { token: st.token, label: 'batch 3: a transform-only update (empty fields, empty mask) on a new document' });
    await s.settle({ max: 6000 });
    st = last(s);
    batch(s, [del(w('a')), del(w('b')), del(w('t'))], { token: st.token, label: 'batch 4: delete a, b, t' });
    await s.settle({ max: 6000 });
    st = last(s);
        batch(s, [], { token: st.token, label: 'batch 5: empty writes (after four good batches)' });
    await s.settle({ max: 4000 });
    st = last(s);
    await s.close();
  });

  await runCase('write.empty-first', 'An empty batch (writes: []) with the handshake token straight after the handshake, then a good batch on the same stream', [], async (sink) => {
    const s = await open('write', sink);
    const st = await handshake(s);
    batch(s, [], { token: st.token, label: 'empty writes with the handshake token' });
    await s.settle({ max: 4000 });
    await s.idle(1500);
    if (!s.closed) {
      batch(s, [upd(`${W}/e1`, { n: 1 })], { token: last(s).token || st.token, label: 'a good batch after the empty one' });
      await s.settle({ max: 6000 });
    }
    await s.close();
  });

  await runCase('write.pipelined', 'Three batches sent back to back before any response, all with the handshake token; then three more each with the token of the latest response so far', [], async (sink) => {
    const s = await open('write', sink);
    const st = await handshake(s);
    batch(s, [upd(`${W}/p1`, { n: 1 })], { token: st.token, label: 'pipelined 1' });
    batch(s, [upd(`${W}/p2`, { n: 2 })], { token: st.token, label: 'pipelined 2' });
    batch(s, [upd(`${W}/p3`, { n: 3 })], { token: st.token, label: 'pipelined 3' });
    await s.settle({ max: 8000 });
    const st2 = last(s);
    batch(s, [upd(`${W}/p1`, { n: 11 })], { token: st2.token, label: 'sequential 4 (token of the latest response)' });
    await s.settle({ max: 6000 });
    batch(s, [upd(`${W}/p2`, { n: 12 })], { token: last(s).token, label: 'sequential 5' });
    await s.settle({ max: 6000 });
    await s.close();
  });

  await runCase('write.no-handshake', 'The first request carries writes (no handshake), and a handshake request sent a second time on a started stream', [], async (sink) => {
    let s = await open('write', sink);
    batch(s, [upd(`${W}/n1`, { n: 1 })], { label: 'first request with writes and no stream_id / stream_token' });
    await s.settle({ max: 6000 });
    await s.close();
    s = await open('write', sink);
    const st = await handshake(s);
    s.mark('second handshake request ({database} only) on the started stream');
    s.send(wreq({}));
    await s.settle({ max: 6000 });
    batch(s, [upd(`${W}/n2`, { n: 2 })], { token: st.token, label: 'a batch after the second handshake, with the first token' });
    await s.settle({ max: 6000 });
    await s.close();
  });

  const failing = async (id, description, setupWrites, failWrite, extraBatchAfter = true) => {
    await runCase(id, description, setupWrites, async (sink) => {
      if (setupWrites.length) await commitWrites(setupWrites);
      const s = await open('write', sink);
      const st = await handshake(s);
      batch(s, [upd(`${W}/ok1`, { n: 1 })], { token: st.token, label: 'good batch before the failure' });
      batch(s, [failWrite], { token: st.token, label: 'the failing batch' });
      if (extraBatchAfter) batch(s, [upd(`${W}/ok2`, { n: 2 })], { token: st.token, label: 'good batch pipelined after the failure' });
      await s.settle({ max: 8000 });
      await s.idle(1500);
      await s.close();
    });
  };
  await failing('write.precondition-exists', 'A batch with current_document exists:true on a missing document, between two good batches', [], { ...upd(`${W}/missing`, { n: 1 }), currentDocument: { exists: true } });
  await failing('write.precondition-not-exists', 'A batch with current_document exists:false on an existing document', [upd(`${W}/there`, { n: 1 })], { ...upd(`${W}/there`, { n: 2 }), currentDocument: { exists: false } });
  await failing('write.precondition-update-time', 'A batch with a current_document update_time that does not match', [upd(`${W}/there2`, { n: 1 })], { ...upd(`${W}/there2`, { n: 2 }), currentDocument: { updateTime: { seconds: '1000', nanos: 0 } } });
  await failing('write.invalid-argument', 'A batch with an invalid write (a reserved field name __bad__)', [], { update: { name: nm(`${W}/bad`), fields: { __bad__: { integerValue: '1' } } } });
  await failing('write.transform-on-missing', 'A batch whose update_transforms increment a field with a string value', [upd(`${W}/str`, { f: 'text' })], { ...upd(`${W}/str`, { g: 1 }, ['g']), updateTransforms: [tf('f', { increment: { integerValue: '1' } })] });

  await runCase('write.token-reuse', 'stream_token: stale token reused, empty token, garbled token, a token with the stream_id of another stream', [], async (sink) => {
    const probe = async (title, mk) => {
      const s = await open('write', sink);
      s.mark(`stream ${s.stream}: ${title}`);
      const st = await handshake(s);
      batch(s, [upd(`${W}/tk1`, { n: 1 })], { token: st.token, label: 'batch 1 with the handshake token' });
      await s.settle({ max: 6000 });
      const st1 = last(s);
      batch(s, [upd(`${W}/tk2`, { n: 2 })], mk(st, st1));
      await s.settle({ max: 6000 });
      if (!s.closed) {
        batch(s, [upd(`${W}/tk3`, { n: 3 })], { token: last(s).token, label: 'batch 3 with the latest token' });
        await s.settle({ max: 6000 });
      }
      await s.close();
    };
    await probe('batch 2 reuses the handshake token (stale)', (st) => ({ token: st.token, label: 'batch 2 with the stale handshake token' }));
    await probe('batch 2 with an empty token', () => ({ label: 'batch 2 with no stream_token' }));
    await probe('batch 2 with a garbled token', () => ({ token: Buffer.from('0102030405060708', 'hex'), label: 'batch 2 with a garbled 8-byte token' }));
    await probe('batch 2 with the stream_id set and the latest token', (st, st1) => ({ token: st1.token, streamId: st.streamId, label: 'batch 2 with stream_id and the latest token' }));
    await probe('batch 2 with a stream_id that is not the stream\'s', (st, st1) => ({ token: st1.token, streamId: 'aruma-ref-f4c-not-a-stream', label: 'batch 2 with a foreign stream_id' }));
  });

  await runCase('write.resume-stream', 'A new stream resumed with stream_id and stream_token of an earlier one (the latest token, then an older one, then a garbled stream_id)', [], async (sink) => {
    const s = await open('write', sink);
    const st0 = await handshake(s);
    batch(s, [upd(`${W}/rs1`, { n: 1 })], { token: st0.token, label: 'batch 1' });
    await s.settle({ max: 6000 });
    const st1 = last(s);
    batch(s, [upd(`${W}/rs2`, { n: 2 })], { token: st1.token, label: 'batch 2' });
    await s.settle({ max: 6000 });
    const st2 = last(s);
    await s.close();
    for (const [title, id, token] of [
      ['resume with stream_id and the latest token (after batch 2)', st0.streamId, st2.token],
      ['resume with stream_id and the token of batch 1 (one batch older)', st0.streamId, st1.token],
      ['resume with a garbled stream_id', 'aruma-ref-f4c-not-a-stream', st2.token],
    ]) {
      const r = await open('write', sink);
      r.mark(`stream ${r.stream}: ${title}`);
      r.send(wreq({ streamId: id, streamToken: token }));
      await r.settle({ max: 6000 });
      if (!r.closed) {
        batch(r, [upd(`${W}/rs3`, { n: 3 })], { token: last(r).token || token, label: 'a batch after the resume' });
        await r.settle({ max: 6000 });
      }
      await r.close();
    }
  });
}

// ---- cleanup ----

const sweepPage = (after) => rest('POST', `${ROOT}:runQuery`, {
  structuredQuery: {
    from: [{ allDescendants: true }],
    where: {
      compositeFilter: {
        op: 'AND',
        filters: [
          { fieldFilter: { field: { fieldPath: '__name__' }, op: after ? 'GREATER_THAN' : 'GREATER_THAN_OR_EQUAL', value: { referenceValue: after ?? `${ROOT}/aruma-ref-f4c/${MIN_ID}` } } },
          { fieldFilter: { field: { fieldPath: '__name__' }, op: 'LESS_THAN', value: { referenceValue: `${ROOT}/aruma-ref-f4d/${MIN_ID}` } } },
        ],
      },
    },
    select: { fields: [{ fieldPath: '__name__' }] },
    orderBy: [{ field: { fieldPath: '__name__' }, direction: 'ASCENDING' }],
    limit: 1000,
  },
});

async function sweep() {
  const all = [];
  let after;
  for (;;) {
    const r = await sweepPage(after);
    if (r.http !== 200) return { http: r.http, names: all };
    const n = (Array.isArray(r.body) ? r.body : []).filter((x) => x.document).map((x) => x.document.name);
    all.push(...n);
    if (n.length < 1000) return { http: 200, names: all };
    after = n[n.length - 1];
  }
}

async function deleteAll(names) {
  const res = [];
  for (let i = 0; i < names.length; i += 400) {
    const writes = names.slice(i, i + 400).map((x) => ({ delete: x }));
    for (const w of writes) assertScoped(w.delete);
    const r = await rest('POST', `${DB}/documents:commit`, { writes });
    res.push(r.http);
    if (r.http !== 200) throw new Error(`cleanup commit failed: HTTP ${r.http} ${JSON.stringify(r.body).slice(0, 300)}`);
  }
  return res;
}

async function cleanup() {
  const res = await deleteAll([...written]);
  const after = await sweep();
  if (after.names.length) res.push(...(await deleteAll(after.names)));
  const fin = await sweep();
  const lists = {};
  for (const t of TOPS) {
    const l = await rest('GET', `${ROOT}/${t}?showMissing=true&pageSize=100`);
    lists[t] = (l.body.documents || []).map((d) => rel(d.name));
  }
  const ok = fin.http === 200 && fin.names.length === 0 && Object.values(lists).every((l) => l.length === 0);
  return {
    ok,
    deletedCount: written.size,
    deleted: [...written].map(rel).sort(),
    strays: after.names.map(rel),
    commitHttp: res,
    sweep: { http: fin.http, query: `kindless allDescendants, __name__ in [aruma-ref-f4c/${MIN_ID}, aruma-ref-f4d/${MIN_ID}), paged by 1000`, remaining: fin.names.map(rel) },
    listsShowMissing: lists,
  };
}

// ---- main ----

async function main() {
  if (!grpcAvailable()) throw new Error('FIRESTORE_PROTOS is not set: Listen and Write need gRPC');
  const dbr = await rest('GET', DB);
  if (dbr.http !== 200) throw new Error(`GET ${DB}: HTTP ${dbr.http}`);
  const target = `${PROJECT} (default): ${dbr.body.type}, ${dbr.body.databaseEdition}, ${dbr.body.concurrencyMode}, ${dbr.body.locationId}`;
  const ver = versions({ api: 'gRPC google.firestore.v1.Firestore Listen and Write (bidirectional streams); REST commit for the writes made while a Listen stream is open', ...grpcVersions() });
  const started = new Date().toISOString();
  const only = (process.argv.find((a) => a.startsWith('--only=')) || '').slice(7).split(',').filter(Boolean);
  const want = (k) => !only.length || only.includes(k);
  let cl;
  let atStart;
  let fatal;
  try {
    log('strays ');
    const s0r = await sweep();
    if (s0r.http !== 200) throw new Error(`stray sweep failed: HTTP ${s0r.http}`);
    atStart = { strays: s0r.names.map(rel), commitHttp: s0r.names.length ? await deleteAll(s0r.names) : [] };
    if (want('listen')) {
      await listenCases();
      await resumeCases();
      await refusedCases();
    }
    if (want('write')) await writeCases();
  } catch (e) {
    fatal = e;
  } finally {
    log('\ncleanup ');
    cl = await cleanup();
    log(cl.ok ? 'verified\n' : 'NOT VERIFIED\n');
  }
  if (fatal) {
    console.error('record-f4 failed (cleanup done, no corpus written):', fatal.message);
    process.exit(1);
  }
  if (!cl.ok) {
    console.error('record-f4: the cleanup is not verified; no corpus file written. Remaining:', JSON.stringify(cl.sweep.remaining));
    process.exit(2);
  }
  if (only.length) {
    console.error('record-f4: --only run; cleanup verified, no corpus file written (a partial run is a probe).');
    if (process.env.F4_PROBE_OUT) (await import('node:fs')).writeFileSync(process.env.F4_PROBE_OUT, JSON.stringify(cases));
    return;
  }
  const finished = new Date().toISOString();
  const header = (notes) => ({
    oracle: 'production',
    target,
    generator: GENERATOR,
    recordedAt: `${started} .. ${finished}`,
    versions: ver,
    notes: [
      'Auth: the firebase CLI login (a project owner); security rules do not apply.',
      'Every frame of a case is in order, with tMs the milliseconds since its stream opened (stream = index of the stream within the case). dir is send (client to server), recv (server to client), action (something the recorder did outside the stream, with its commit time) or end (the stream\'s final gRPC status).',
      'Timestamps are replaced by labels <ts tN> numbered in time order within the case (equal values share a label); labels.timestamps gives each label\'s offset in microseconds from t1. Byte strings (resume tokens, stream tokens) are replaced by <bytes bN len=L> (equal contents share a label); labels.bytes holds their hex. Stream ids are <stream_id sN len=L>.',
      'Enums are names, int64 values strings, as @grpc/proto-loader decodes them with defaults off: a field at its default value (target_change_type NO_CHANGE, nanos 0) is absent from a frame, so a missing targetChangeType means NO_CHANGE and a missing targetIds means empty.',
      ...notes,
    ],
  });
  const split = (prefix) => cases.filter((c) => c.id.startsWith(prefix));
  writeCorpus('f4-listen.json', header(['Writes made while a stream is open are REST commits; the action frame carries the commit time.', `Total document writes of the run: ${writeCount} (cleanup deletes excluded).`]), { cases: split('listen.') });
  writeCorpus('f4-write-stream.json', header([]), { cases: split('write.') });
  writeCorpus('f4-cleanup.json', header([]), { cleanup: { ...cl, straysAtStart: atStart } });
}

main().catch((e) => {
  console.error('record-f4 failed:', e.stack);
  process.exit(1);
});
