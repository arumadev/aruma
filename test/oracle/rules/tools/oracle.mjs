// Shared pieces of the oracle recorders: how a topic's cases become rulesets
// and Rules Test API test cases, and how a test result is classified.
//
// A topic module exports { topic, service, resource?, fns?, cases }. The
// topic modules live with the held-out set (see ../README.md), because they
// define the held-out cases too; the recorded JSON files are self-contained.
// A case is
//   { id, desc, expr?, rules?, gen?, alone?, allow?, method?, path?, auth?,
//     data?, query?, resource?, time?, mocks?, raw?, emu?, pin? }
// - expr: a condition. Cases with expr are packed: one ruleset per pack, one
//   match block per case (match /<id>/{doc} or {file}), so a pack is one API
//   call. A case whose condition does not compile is moved out of its pack
//   and recorded alone (alone: true does that from the start).
// - rules: a whole ruleset, recorded alone; path is then required.
// - gen: a generated ruleset (see generate), recorded alone.
// - allow: the allow statement's methods (default: the request method).
// - method: request method, get list create update delete (default get).
// - auth: request.auth ({uid, token}); undefined means ALICE, null signed out.
// - data: request.resource (Firestore: its data map; Storage: the object).
// - resource: the existing resource (Firestore: its data map; Storage: the
//   object); undefined means the topic's default, null none.
// - query: request.query (list requests).
// - time: request.time (RFC 3339); undefined means TIME.
// - mocks: [{fn: 'get'|'exists', path: 'users/alice', data?, value?}] for
//   get()/exists() (Firestore) or firestore.get()/firestore.exists() (Storage).
// - raw: replaces numbers in the request JSON text, [[placeholder, text]],
//   for values JSON.stringify cannot write (2.0).
// - emu: false or a reason string when the case cannot run on the emulator.
// - pin: keeps the case in the development set; heldout: puts it in the
//   held-out set (see heldOut).
// - seg: the first path segment of a packed case (default c_<id>).
// A topic may set wildcard, the name of its packs' second segment variable.
// The resource names 'DOC' and 'OBJECT' stand for the constants below, and
// 'ABSENT' leaves a field out of the request.
import { createHash } from 'node:crypto';

export const ALICE = {
  uid: 'alice',
  token: {
    sub: 'alice',
    email: 'alice@example.com',
    email_verified: true,
    admin: true,
    role: 'editor',
    groups: ['g1', 'g2'],
    firebase: { sign_in_provider: 'password' },
  },
};

export const DOC = {
  n: 1,
  f: 1.5,
  s: 'abc',
  b: true,
  nul: null,
  arr: [1, 2, 3],
  tags: ['a', 'b'],
  m: { x: 1, y: 'z' },
  owner: 'alice',
};

export const OBJECT = {
  name: 'dir/file.txt',
  bucket: 'demo-oracle.appspot.com',
  size: 1024,
  contentType: 'text/plain',
  metadata: { owner: 'alice', k: 'v' },
  timeCreated: '2026-01-02T03:04:05.000Z',
  updated: '2026-01-03T03:04:05.000Z',
  generation: 1700000000000000,
  metageneration: 1,
  md5Hash: 'XrY7u+Ae7tCTyyK7j1rNww==',
  crc32c: 'yZRlqg==',
  etag: 'CJqUmJ6Ry4ADEAE=',
  contentDisposition: 'inline',
  contentEncoding: 'identity',
};

export const BUCKET = 'demo-oracle.appspot.com';

export function namedResource(r) {
  if (r === 'DOC') return DOC;
  if (r === 'OBJECT') return OBJECT;
  return r;
}

// heldOut says whether a case of one of our topics belongs to the held-out
// set: a quarter of the unpinned cases (about a fifth of all), chosen by a
// fixed seed over topic/id. Pinned
// cases (the ones the oracle findings note cites) stay in the
// development set.
export const HELDOUT_SEED = 'aruma-s0a-rules-heldout-v1';
export function heldOut(topic, c) {
  if (c.heldout) return true;
  if (c.pin) return false;
  const h = createHash('sha256').update(`${HELDOUT_SEED}:${topic}/${c.id}`).digest();
  return h.readUInt32BE(0) / 0x100000000 < 0.25;
}

// generate builds the rulesets of the size cases, so the recorded files keep
// a short description instead of hundreds of kilobytes:
// - {kind: 'comment-padding', total}: one block comment, then a ruleset that
//   allows get on /lim/{doc}; total bytes in all.
// - {kind: 'functions', count}: count functions h<i>(a) and the same allow
//   calling h0(0).
export function generate(gen) {
  const base = (fns, cond) => `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
${fns}
    match /lim/{doc} {
      allow get: if ${cond};
    }
  }
}
`;
  if (gen.kind === 'comment-padding') {
    const b = base('', 'true');
    const filler = gen.total - b.length - 1;
    return `/*${'x'.repeat(filler - 4)}*/\n${b}`;
  }
  if (gen.kind === 'functions') {
    const fns = [];
    for (let i = 0; i < gen.count; i++) {
      fns.push(`    function h${i}(a) { return a == ${i} || a == 'v${i}' || a in [${i}, ${i + 1}, ${i + 2}]; }`);
    }
    return base(fns.join('\n'), 'h0(0)');
  }
  throw new Error(`unknown generator ${gen.kind}`);
}
export const DB_PREFIX = '/databases/(default)/documents/';

export function segment(c) {
  return c.seg || 'c_' + c.id.replace(/[^A-Za-z0-9_]/g, '_');
}

export function defaultPath(service, c) {
  if (c.path) return c.path;
  return service === 'firestore' ? `${segment(c)}/doc` : `${segment(c)}/file.txt`;
}

export function wirePath(service, p) {
  if (service === 'firestore') return p.startsWith('/databases/') ? p : DB_PREFIX + p;
  return p.startsWith('/b/') ? p : `/b/${BUCKET}/o/${p}`;
}

const HEADER = {
  firestore: (body) => `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
${body}
  }
}
`,
  storage: (body) => `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
${body}
  }
}
`,
};

function matchBlock(service, c, wildcard) {
  const allow = c.allow || c.method || 'get';
  const v = `{${wildcard || (service === 'firestore' ? 'doc' : 'file')}}`;
  return `    match /${segment(c)}/${v} {\n      allow ${allow}: if ${c.expr};\n    }`;
}

// packRuleset returns the ruleset for a pack and the line of each case's
// allow statement.
export function packRuleset(service, fns, cases, wildcard) {
  const parts = [];
  const lines = {};
  let line = 4; // the header takes three lines
  if (fns) {
    const f = fns.replace(/\n+$/, '');
    parts.push(f);
    line += f.split('\n').length;
  }
  for (const c of cases) {
    parts.push(matchBlock(service, c, wildcard));
    lines[c.id] = [line, line + 2];
    line += 3;
  }
  return { source: HEADER[service](parts.join('\n')), lines };
}

export function aloneRuleset(service, fns, c, wildcard) {
  if (c.rules) return c.rules;
  if (c.gen) return generate(c.gen);
  return packRuleset(service, c.fns ?? fns, [c], wildcard).source;
}

export function fileName(service) {
  return service === 'firestore' ? 'firestore.rules' : 'storage.rules';
}

function mock(service, m) {
  const fn = service === 'firestore' ? m.fn : `firestore.${m.fn}`;
  const out = { function: fn, args: [{ exactValue: wirePath('firestore', m.path) }] };
  if (m.fn === 'get') out.result = m.data === null ? { undefined: {} } : m.data === 'VALUE_NULL' ? { value: null } : { value: { data: m.data } };
  else out.result = { value: m.value === true };
  return out;
}

// testCase is the Rules Test API test case for c. The API takes request and
// resource as literal maps: a field it is not given is undefined in the
// rules ("Property auth is undefined on object."), so a signed-out request
// sends auth: null and a missing document resource: null. ABSENT leaves the
// field out, to record what the API does then.
export const ABSENT = 'ABSENT';
export const TIME = '2026-01-15T10:20:30.123Z';

export function testCase(service, topicResource, c) {
  const auth = c.auth === undefined ? ALICE : c.auth;
  const request = { method: c.method || 'get', path: wirePath(service, defaultPath(service, c)) };
  if (auth !== ABSENT) request.auth = auth;
  const time = c.time === undefined ? TIME : c.time;
  if (time !== ABSENT) request.time = time;
  if (c.query !== undefined) request.query = c.query;
  if (c.data !== undefined) request.resource = c.data === null ? null : service === 'firestore' ? { data: c.data } : c.data;
  const tc = { expectation: 'ALLOW', request };
  const res = namedResource(c.resource === undefined ? topicResource : c.resource);
  if (res !== ABSENT && res !== undefined) tc.resource = res === null ? null : service === 'firestore' ? { data: res } : res;
  if (c.mocks && c.mocks.length) tc.functionMocks = c.mocks.map((m) => mock(service, m));
  return tc;
}

// classify turns one testResults entry into an outcome:
// allow, deny (a condition was false, or no statement matched) or error
// (the decision was a deny caused by an evaluation error).
export function classify(r) {
  if (!r) return { outcome: 'missing' };
  if (r.state === 'SUCCESS') return { outcome: 'allow' };
  const msgs = r.debugMessages || [];
  const errs = msgs.filter((m) => m.startsWith('Error:'));
  if (r.errorPosition || errs.length) {
    return { outcome: 'error', errorKind: 'evaluation', message: (errs.length ? errs : msgs).join('\n') };
  }
  const out = { outcome: 'deny' };
  if (!r.visitedExpressions || r.visitedExpressions.length === 0) out.reason = 'no-matching-statement';
  else out.reason = 'false';
  if (msgs.length) out.message = msgs.join('\n');
  return out;
}

export function issuesText(issues) {
  return (issues || [])
    .map((i) => `${i.severity} L${i.sourcePosition?.line}:${i.sourcePosition?.column} ${i.description}`)
    .join('\n');
}
