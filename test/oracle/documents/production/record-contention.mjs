// Production recording of lock contention between pessimistic read-write
// transactions (REST v1, an owner principal, so beginTransaction gives
// PESSIMISTIC transactions on this STANDARD database):
//
//   f0-contention.json   six scenarios, each run three times: who commits, who
//                        is ABORTED and when, the exact messages, and whether a
//                        blocked request waits (its latency) or fails fast
//
// The question (spike-sequencer.md §2.5, §3.4): does Firestore resolve lock
// conflicts by wound-wait (an older transaction aborts a younger holder; a
// younger requester waits), by wait-die (a younger requester is aborted at
// once), or by timeout only?
//
//   node record-contention.mjs [--only s1,s2,...]
//
// Requests are sent with fetch directly, not with lib.mjs rest(): rest()
// retries 5xx and network errors and waits up to 120 s, which would hide a
// blocked or failed commit. Here every request is sent once, with a short
// client-side timeout, and its start and end are recorded in ms from the start
// of its run. A request the client gave up on is recorded as clientTimeout; the
// final state of each document shows whether such a commit still applied.
//
// Data is written ONLY under the top-level collection aruma-ref-txn
// (assertScoped refuses anything else), deleted at the end, and the deletion is
// verified. Nothing creates or changes an index, rule, TTL policy or setting,
// and no Auth user is created. The token is obtained exactly as in lib.mjs
// (firebase-tools lib/auth.js) and is never printed, logged or written.
import { createRequire } from 'node:module';
import { join } from 'node:path';
import { performance } from 'node:perf_hooks';
import { setTimeout as sleep } from 'node:timers/promises';
import { FT, PROJECT, DB, ROOT, ORIGIN, fields, versions, normalise, writeCorpus, assertScoped } from './lib.mjs';

const args = process.argv.slice(2);
const onlyArg = args.includes('--only') ? args[args.indexOf('--only') + 1].split(',') : null;
const want = (t) => !onlyArg || onlyArg.includes(t);

const GENERATOR = 'test/oracle/documents/production/record-contention.mjs';
const C = 'aruma-ref-txn';
const MIN_ID = '__id-9223372036854775808__';
const RUNS = 3;
const HOLD_MS = 5000; // how long a request may stay pending before the next step goes ahead
const WAITED_MS = 3000; // a latency at or above this counts as "waited" (blocked on a lock); below it, "fast"
const S6_WINDOW_MS = 25_000; // s6: how long T1 keeps its read lock while the write waits
const T_SHORT = 15_000; // client timeout of an ordinary request
const T_LONG = 40_000; // client timeout of a request expected to block
const IDLE_MS = [70_000, 70_000, 70_000];
const IDLE_PROBE_MS = [50_000, 65_000];

const log = (s) => process.stderr.write(s);
const require = createRequire(import.meta.url);

// ---- token (as lib.mjs; never printed, logged or written) ----

let cached = null;
async function token() {
  if (cached && cached.expires > Date.now() + 5 * 60_000) return cached.token;
  const auth = require(join(FT, 'lib/auth.js'));
  const scopes = require(join(FT, 'lib/scopes.js'));
  const account = auth.getGlobalDefaultAccount();
  if (!account) throw new Error('firebase CLI is not logged in');
  const t = await auth.getAccessToken(account.tokens.refresh_token, [scopes.EMAIL, scopes.OPENID, scopes.CLOUD_PROJECTS_READONLY, scopes.FIREBASE_PLATFORM, scopes.CLOUD_PLATFORM]);
  cached = { token: t.access_token, expires: t.expires_at || Date.now() + 30 * 60_000 };
  return cached.token;
}

// once sends one REST request, with no retry, and gives up after timeoutMs.
async function once(method, path, body, timeoutMs) {
  const tok = await token();
  try {
    const res = await fetch(`${ORIGIN}/v1/${path}`, {
      method,
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${tok}` },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs),
    });
    const text = await res.text();
    let parsed;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { raw: text };
    }
    return { http: res.status, body: parsed };
  } catch (e) {
    if (e.name === 'TimeoutError') return { clientTimeout: true };
    return { networkError: e.cause?.code || e.message };
  }
}

// ---- setup and bookkeeping ----

const written = new Set();

async function put(rel, data) {
  const name = `${ROOT}/${rel}`;
  assertScoped(name);
  const r = await once('POST', `${DB}/documents:commit`, { writes: [{ update: { name, fields: fields(data) } }] }, T_SHORT);
  if (r.http !== 200) throw new Error(`setup write of ${rel} failed: ${JSON.stringify(r).slice(0, 300)}`);
  written.add(name);
}

async function finalState(rel) {
  const r = await once('GET', `${ROOT}/${rel}`, undefined, T_SHORT);
  if (r.http !== 200) return { http: r.http, error: r.body?.error ? { status: r.body.error.status, message: r.body.error.message } : r };
  const f = r.body.fields || {};
  return { http: 200, writer: f.writer?.stringValue ?? null, step: f.step?.stringValue ?? null };
}

// A Run records one execution of a scenario: every request, in the order sent,
// with times in ms from the start of the run.
class Run {
  constructor(id, scenario, run, description) {
    Object.assign(this, { id, scenario, run, description });
    this.t0 = performance.now();
    this.events = [];
    this.marks = [];
    this.txn = {};
    this.replace = new Map();
  }

  now() {
    return Math.round(performance.now() - this.t0);
  }

  mark(text) {
    this.marks.push({ atMs: this.now(), text });
  }

  async req(actor, op, method, path, body, timeoutMs = T_SHORT) {
    const ev = { n: this.events.length + 1, actor, op, startMs: this.now() };
    this.events.push(ev);
    const r = await once(method, path, body, timeoutMs);
    ev.endMs = this.now();
    ev.latencyMs = ev.endMs - ev.startMs;
    ev.request = { method, path: path.replace(`projects/${PROJECT}/`, 'projects/{project}/') };
    if (body !== undefined) ev.request.body = body;
    if (r.clientTimeout) {
      ev.outcome = 'clientTimeout';
      ev.clientTimeoutMs = timeoutMs;
    } else if (r.networkError) {
      ev.outcome = 'networkError';
      ev.networkError = r.networkError;
    } else {
      ev.http = r.http;
      const err = r.body?.error;
      if (err) {
        ev.outcome = 'error';
        ev.error = { code: err.code, status: err.status, message: err.message };
      } else {
        ev.outcome = 'ok';
      }
      ev.response = r.body;
    }
    ev.timing = ev.latencyMs >= WAITED_MS ? 'waited' : 'fast';
    return { ev, r };
  }

  async begin(actor) {
    const { ev, r } = await this.req(actor, 'beginTransaction', 'POST', `${ROOT}:beginTransaction`, { options: { readWrite: {} } });
    if (ev.outcome !== 'ok') throw new Error(`${this.id}: begin ${actor} failed: ${JSON.stringify(ev.error || ev.outcome)}`);
    const t = r.body.transaction;
    this.txn[actor] = t;
    this.replace.set(t, `<${actor} transaction id>`);
    this.replace.set(encodeURIComponent(t), `<${actor} transaction id>`);
    return ev;
  }

  read(actor, rel, timeoutMs = T_SHORT) {
    const path = `${ROOT}/${rel}?transaction=${encodeURIComponent(this.txn[actor])}`;
    return this.req(actor, `read ${rel.split('-').pop()}`, 'GET', path, undefined, timeoutMs).then((x) => x.ev);
  }

  commit(actor, rel, timeoutMs = T_SHORT, { transactional = true } = {}) {
    const name = `${ROOT}/${rel}`;
    assertScoped(name);
    written.add(name);
    const body = { writes: [{ update: { name, fields: fields({ writer: actor, step: `${this.id}` }) } }] };
    if (transactional) body.transaction = this.txn[actor];
    return this.req(actor, `commit write ${rel.split('-').pop()}${transactional ? '' : ' (no transaction)'}`, 'POST', `${ROOT}:commit`, body, timeoutMs).then((x) => x.ev);
  }

  rollback(actor) {
    return this.req(actor, 'rollback', 'POST', `${ROOT}:rollback`, { transaction: this.txn[actor] }).then((x) => x.ev);
  }

  // finish rolls back every transaction that neither committed nor failed, so
  // no lock outlives the run, and records each document's final state.
  async finish(docs) {
    for (const actor of Object.keys(this.txn)) {
      const evs = this.events.filter((e) => e.actor === actor);
      const committed = evs.some((e) => e.op.startsWith('commit') && e.outcome === 'ok');
      const failed = evs.some((e) => e.outcome === 'error' && e.error.status === 'ABORTED');
      const rolled = evs.some((e) => e.op === 'rollback');
      if (!committed && !failed && !rolled) {
        this.mark(`recorder: rollback of ${actor}, still open at the end of the run`);
        await this.rollback(actor);
      }
    }
    this.final = {};
    for (const [k, rel] of Object.entries(docs)) this.final[k] = await finalState(rel);
  }

  toCase() {
    const events = [...this.events].sort((a, b) => a.n - b.n).map((e) => normalise(e, this.replace));
    for (const e of events) {
      if (e.response?.transaction) e.response.transaction = this.replace.get(e.response.transaction) ?? e.response.transaction;
      // Keep the commit and read answers short: a document's fields, a commit's write results.
      if (e.response?.fields) e.response = { name: e.response.name?.replace(`${ROOT}/`, ''), fields: e.response.fields, updateTime: e.response.updateTime, createTime: e.response.createTime };
    }
    const txns = {};
    for (const actor of Object.keys(this.txn)) {
      const evs = events.filter((e) => e.actor === actor);
      const c = evs.find((e) => e.op.startsWith('commit') && e.outcome === 'ok');
      const a = evs.find((e) => e.outcome === 'error' && e.error.status === 'ABORTED');
      const rb = evs.find((e) => e.op === 'rollback');
      txns[actor] = c
        ? { result: 'committed', atMs: c.endMs, commitLatencyMs: c.latencyMs }
        : a
          ? { result: 'aborted', atMs: a.endMs, onOp: a.op, opLatencyMs: a.latencyMs, message: a.error.message }
          : rb
            ? { result: 'rolled back', atMs: rb.endMs }
            : { result: 'unknown' };
    }
    // nonTxn: the commit without a transaction in s6b.
    const nt = events.find((e) => e.op.includes('(no transaction)'));
    if (nt) txns[nt.actor] = nt.outcome === 'ok' ? { result: 'committed', atMs: nt.endMs, commitLatencyMs: nt.latencyMs } : { result: nt.outcome, atMs: nt.endMs, message: nt.error?.message };
    return {
      id: this.id, scenario: this.scenario, run: this.run, description: this.description,
      events: events.map(({ n, actor, op, startMs, endMs, latencyMs, timing, outcome, http, error, request, response, clientTimeoutMs, networkError }) => ({
        n, actor, op, startMs, endMs, latencyMs, timing, outcome,
        ...(http !== undefined ? { http } : {}),
        ...(error ? { error } : {}),
        ...(clientTimeoutMs ? { clientTimeoutMs } : {}),
        ...(networkError ? { networkError } : {}),
        request, ...(response ? { response } : {}),
      })),
      marks: this.marks,
      transactions: txns,
      final: this.final,
      pattern: this.pattern(events),
      timingPattern: this.pattern(events, true),
    };
  }

  // pattern is the run's behaviour without its times: each request's actor,
  // operation and result, in the order sent; timingPattern adds fast/waited.
  // Equal patterns across runs mean the scenario is stable.
  pattern(events, timing = false) {
    return events
      .filter((e) => e.op !== 'beginTransaction')
      .map((e) => `${e.actor} ${e.op}: ${e.outcome === 'error' ? e.error.status : e.outcome}${timing ? ` (${e.timing})` : ''}`)
      .join('; ');
  }
}

// settle waits for p, or for ms, whichever comes first, and says which.
async function settle(p, ms) {
  let done = false;
  p.then(() => { done = true; }, () => { done = true; });
  const end = Date.now() + ms;
  while (!done && Date.now() < end) await sleep(50);
  return done;
}

// ---- scenarios ----

// s1: T1 begins and reads D; T2 begins (younger) and reads D; T2 commits a
// write to D; then T1 commits a write to D.
async function s1(n) {
  const id = `s1.r${n}`;
  const D = `${C}/s1-r${n}-D`;
  await put(D, { writer: 'setup' });
  const r = new Run(id, 's1', n, 'T1 begin, T1 read D, T2 begin, T2 read D, T2 commit write D (sent; up to 5 s), then T1 commit write D');
  await r.begin('T1');
  await r.read('T1', D);
  await r.begin('T2');
  await r.read('T2', D);
  const p2 = r.commit('T2', D, T_LONG);
  const settled = await settle(p2, HOLD_MS);
  r.mark(settled ? 'T2 commit returned before T1 acted' : `T2 commit still pending after ${HOLD_MS} ms; T1 commits now`);
  const p1 = r.commit('T1', D, T_LONG);
  await Promise.all([p1, p2]);
  await r.finish({ D });
  return r.toCase();
}

// s2: T1 (older) reads D; T2 (younger) reads D and commits a write first. Does
// T2 block until T1 ends, or is T1 wounded the next time it acts (a read)?
async function s2(n) {
  const id = `s2.r${n}`;
  const D = `${C}/s2-r${n}-D`;
  await put(D, { writer: 'setup' });
  const r = new Run(id, 's2', n, 'T1 begin, T1 read D, T2 begin, T2 read D, T2 commit write D (sent; up to 5 s), T1 reads D again (its next action), up to 3 s more, T1 rollback (ends T1), then T2 commit awaited');
  await r.begin('T1');
  await r.read('T1', D);
  await r.begin('T2');
  await r.read('T2', D);
  const p2 = r.commit('T2', D, T_LONG);
  const settled = await settle(p2, HOLD_MS);
  r.mark(settled ? 'T2 commit returned before T1 acted again' : `T2 commit still pending after ${HOLD_MS} ms; T1 reads D again`);
  const rd = await r.read('T1', D);
  const settled2 = await settle(p2, 3000);
  r.mark(settled2 ? 'T2 commit has returned' : 'T2 commit still pending; T1 rolls back');
  if (!(rd.outcome === 'error' && rd.error.status === 'ABORTED')) await r.rollback('T1');
  await p2;
  await r.finish({ D });
  return r.toCase();
}

// s3: reverse ages. T1 (older) is begun first (variant a: and reads another
// document E, so it is older by first lock too); T2 (younger) reads D; T1 then
// commits a blind write to D. Does the older T1 wound T2, or wait?
async function s3(n, variant) {
  const id = `s3${variant}.r${n}`;
  const D = `${C}/s3${variant}-r${n}-D`;
  const E = `${C}/s3${variant}-r${n}-E`;
  await put(D, { writer: 'setup' });
  if (variant === 'a') await put(E, { writer: 'setup' });
  const r = new Run(id, `s3${variant}`, n, variant === 'a'
    ? 'T1 begin, T1 read E, T2 begin, T2 read D, T1 commit write D (sent; up to 5 s), T2 reads D again (its next action), up to 3 s more, T2 commit write D if T2 is still alive'
    : 'T1 begin, T2 begin, T2 read D, T1 commit write D (sent; up to 5 s; T1 has read nothing), T2 reads D again, up to 3 s more, T2 commit write D if T2 is still alive');
  await r.begin('T1');
  if (variant === 'a') await r.read('T1', E);
  await r.begin('T2');
  await r.read('T2', D);
  const p1 = r.commit('T1', D, T_LONG);
  const settled = await settle(p1, HOLD_MS);
  r.mark(settled ? 'T1 commit returned before T2 acted again' : `T1 commit still pending after ${HOLD_MS} ms; T2 reads D again`);
  const rd = await r.read('T2', D);
  const settled2 = await settle(p1, 3000);
  r.mark(settled2 ? 'T1 commit has returned' : 'T1 commit still pending');
  if (!(rd.outcome === 'error' && rd.error.status === 'ABORTED')) await r.commit('T2', D, T_LONG);
  await p1;
  await r.finish(variant === 'a' ? { D, E } : { D });
  return r.toCase();
}

// s4: write-write with no reads. a: T1 begun first commits first; b: T1 begun
// first commits second; c: both commits sent at once.
async function s4(n, variant) {
  const id = `s4${variant}.r${n}`;
  const D = `${C}/s4${variant}-r${n}-D`;
  await put(D, { writer: 'setup' });
  const desc = { a: 'T1 begin, T2 begin, T1 commit write D, then T2 commit write D', b: 'T1 begin, T2 begin, T2 commit write D, then T1 commit write D', c: 'T1 begin, T2 begin, T1 and T2 commit write D sent at the same time' }[variant];
  const r = new Run(id, `s4${variant}`, n, desc);
  await r.begin('T1');
  await r.begin('T2');
  if (variant === 'a') {
    await r.commit('T1', D, T_LONG);
    await r.commit('T2', D, T_LONG);
  } else if (variant === 'b') {
    await r.commit('T2', D, T_LONG);
    await r.commit('T1', D, T_LONG);
  } else {
    await Promise.all([r.commit('T1', D, T_LONG), r.commit('T2', D, T_LONG)]);
  }
  await r.finish({ D });
  return r.toCase();
}

// s5: idle expiry. Begin, read, wait, commit; then a read with the same
// transaction. The runs go in parallel with the other scenarios (own documents).
async function s5(n, idleMs, probe = false) {
  const tag = probe ? `s5probe${idleMs / 1000}` : `s5.r${n}`;
  const D = `${C}/${probe ? `s5-probe${idleMs / 1000}` : `s5-r${n}`}-D`;
  await put(D, { writer: 'setup' });
  const r = new Run(tag, probe ? 's5probe' : 's5', probe ? 1 : n, `T1 begin, T1 read D, idle ${idleMs / 1000} s, T1 commit write D, then T1 read D again with the same transaction`);
  await r.begin('T1');
  await r.read('T1', D);
  r.mark(`idle ${idleMs} ms`);
  await sleep(idleMs);
  await r.commit('T1', D);
  await r.read('T1', D);
  await r.finish({ D });
  return r.toCase();
}

// s6: T1 holds a read lock on D; T2's write-commit to D waits. a: T2 is a
// transaction that read nothing; b: T2's write is a commit with no
// transaction. Wait up to 25 s (the server's own limit showed at about 20 s),
// then end T1 by rollback.
async function s6(n, variant) {
  const id = `s6${variant}.r${n}`;
  const D = `${C}/s6${variant}-r${n}-D`;
  await put(D, { writer: 'setup' });
  const r = new Run(id, `s6${variant}`, n, variant === 'a'
    ? 'T1 begin, T1 read D, T2 begin, T2 commit write D (blind; sent), wait up to 25 s, T1 rollback, then T2 commit awaited'
    : 'T1 begin, T1 read D, commit write D with no transaction (sent), wait up to 25 s, T1 rollback, then the commit awaited');
  await r.begin('T1');
  await r.read('T1', D);
  let p2;
  if (variant === 'a') {
    await r.begin('T2');
    p2 = r.commit('T2', D, T_LONG);
  } else {
    p2 = r.commit('W', D, T_LONG, { transactional: false });
  }
  const settled = await settle(p2, S6_WINDOW_MS);
  r.mark(settled ? `the write commit returned within ${S6_WINDOW_MS} ms, before T1 ended` : `the write commit still pending after ${S6_WINDOW_MS} ms; T1 rolls back`);
  await r.rollback('T1');
  await p2;
  await r.finish({ D });
  return r.toCase();
}

// ---- cleanup ----

async function cleanup() {
  // Every document the run named, plus whatever a listing still finds.
  const listed = await once('GET', `${ROOT}/${C}?showMissing=true&pageSize=300`, undefined, T_SHORT);
  for (const d of listed.body?.documents || []) written.add(d.name);
  const names = [...written];
  const commits = [];
  for (let i = 0; i < names.length; i += 400) {
    const writes = names.slice(i, i + 400).map((name) => {
      assertScoped(name);
      return { delete: name };
    });
    const r = await once('POST', `${DB}/documents:commit`, { writes }, T_LONG);
    commits.push(r.http ?? r);
    if (r.http !== 200) throw new Error(`cleanup commit failed: ${JSON.stringify(r).slice(0, 300)}`);
  }
  const sweep = await once('POST', `${ROOT}:runQuery`, {
    structuredQuery: {
      from: [{ allDescendants: true }],
      where: {
        compositeFilter: {
          op: 'AND',
          filters: [
            { fieldFilter: { field: { fieldPath: '__name__' }, op: 'GREATER_THAN_OR_EQUAL', value: { referenceValue: `${ROOT}/${C}/${MIN_ID}` } } },
            { fieldFilter: { field: { fieldPath: '__name__' }, op: 'LESS_THAN', value: { referenceValue: `${ROOT}/aruma-ref-txo/${MIN_ID}` } } },
          ],
        },
      },
      select: { fields: [{ fieldPath: '__name__' }] },
      limit: 100,
    },
  }, T_LONG);
  const remaining = (Array.isArray(sweep.body) ? sweep.body : []).filter((x) => x.document).map((x) => x.document.name.replace(`${ROOT}/`, ''));
  const list = await once('GET', `${ROOT}/${C}?showMissing=true&pageSize=300`, undefined, T_SHORT);
  const listedAfter = (list.body?.documents || []).map((d) => d.name.replace(`${ROOT}/`, ''));
  const ok = sweep.http === 200 && list.http === 200 && remaining.length === 0 && listedAfter.length === 0;
  return {
    ok,
    deletedCount: names.length,
    deleted: names.map((n) => n.replace(`${ROOT}/`, '')).sort(),
    commitHttp: commits,
    sweep: { http: sweep.http, query: `kindless allDescendants, __name__ in [${C}/${MIN_ID}, aruma-ref-txo/${MIN_ID})`, remaining },
    listShowMissing: { http: list.http, documents: listedAfter },
  };
}

// ---- summary ----

function summarise(cases) {
  const by = {};
  for (const c of cases) (by[c.scenario] ||= []).push(c);
  const out = {};
  for (const [s, cs] of Object.entries(by)) {
    const patterns = [...new Set(cs.map((c) => c.pattern))];
    const timingPatterns = [...new Set(cs.map((c) => c.timingPattern))];
    out[s] = {
      description: cs[0].description,
      runs: cs.length,
      stable: patterns.length === 1,
      patterns,
      timingStable: timingPatterns.length === 1,
      timingPatterns,
      outcomes: cs.map((c) => ({ id: c.id, transactions: c.transactions, final: c.final })),
    };
  }
  return out;
}

// ---- main ----

async function main() {
  const dbr = await once('GET', DB, undefined, T_SHORT);
  if (dbr.http !== 200) throw new Error(`GET ${DB}: ${JSON.stringify(dbr).slice(0, 200)}`);
  const target = `${PROJECT} (default): ${dbr.body.type}, ${dbr.body.databaseEdition}, ${dbr.body.concurrencyMode}, ${dbr.body.locationId}; REST v1`;
  const started = new Date().toISOString();
  const cases = [];
  let cl;
  let idle = Promise.resolve([]);
  try {
    // s5 idles 70 s; its runs go in the background on their own documents.
    idle = want('s5')
      ? Promise.all([...IDLE_MS.map((ms, i) => s5(i + 1, ms)), ...IDLE_PROBE_MS.map((ms) => s5(1, ms, true))])
      : Promise.resolve([]);
    const fg = [
      ['s1', (n) => s1(n)],
      ['s2', (n) => s2(n)],
      ['s3', (n) => s3(n, 'a')],
      ['s3', (n) => s3(n, 'b')],
      ['s4', (n) => s4(n, 'a')],
      ['s4', (n) => s4(n, 'b')],
      ['s4', (n) => s4(n, 'c')],
      ['s6', (n) => s6(n, 'a')],
      ['s6', (n) => s6(n, 'b')],
    ];
    for (const [s, f] of fg) {
      if (!want(s)) continue;
      for (let n = 1; n <= RUNS; n++) {
        const c = await f(n);
        cases.push(c);
        log(`${c.id}: ${c.timingPattern}\n`);
      }
    }
    for (const c of await idle) {
      cases.push(c);
      log(`${c.id}: ${c.timingPattern}\n`);
    }
  } finally {
    await idle.catch(() => {});
    log('cleanup ');
    cl = await cleanup();
    log(cl.ok ? 'verified\n' : 'NOT VERIFIED\n');
    if (!cl.ok) process.exitCode = 2;
  }
  const finished = new Date().toISOString();
  cases.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  const header = {
    oracle: 'production',
    target,
    generator: `${GENERATOR}${onlyArg ? ` --only ${onlyArg.join(',')}` : ''}`,
    recordedAt: `${started} .. ${finished}`,
    versions: versions({ api: 'firestore.googleapis.com REST v1 (fetch, one attempt per request, no retry)' }),
    notes: [
      'Auth: the firebase CLI login (a project owner), so beginTransaction gives a PESSIMISTIC read-write transaction and security rules do not apply.',
      'T1 is always begun before T2 (T1 is the older transaction). A read is GET with ?transaction=; a write is commit with one update of fields {writer, step}.',
      `Each event has startMs and endMs from the start of its run, latencyMs, and timing "waited" when latencyMs >= ${WAITED_MS}, otherwise "fast". Times differ on every recording; outcomes, statuses, messages and patterns are what must match.`,
      `A request still pending after ${HOLD_MS} ms (${S6_WINDOW_MS} ms in s6) is treated as blocked and the scenario goes on; client-side timeouts are ${T_SHORT} ms, or ${T_LONG} ms for a request expected to block. A client timeout is recorded as outcome clientTimeout.`,
      'transactions: per actor, committed / aborted (with the operation and message) / rolled back. final: each document\'s writer after the run (GET without transaction).',
      'pattern: a run\'s requests (without begins) and results; timingPattern adds fast/waited. summary[scenario].stable is true when the runs have the same pattern, timingStable when they have the same timingPattern (network jitter alone can push an ordinary read past the threshold).',
      'Transaction ids are replaced by <T1 transaction id> and so on; times by placeholders (normalise in lib.mjs); messages are as returned.',
      `s5 and s5probe ran in parallel with the other scenarios, on their own documents; the probes (${IDLE_PROBE_MS.map((x) => x / 1000).join(' s and ')} s idle) bound the idle limit.`,
    ],
  };
  writeCorpus('f0-contention.json', header, { cases, summary: summarise(cases), cleanup: cl });
}

main().catch(async (e) => {
  console.error('record-contention failed:', e.message);
  process.exit(1);
});
